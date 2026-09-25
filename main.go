package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type config struct {
	host, port          string
	user, password      string
	sourceFolder        string
	spamFolder          string
	cleanFolder         string
	unsureFolder        string
	whitelist           addressList
	blacklist           addressList
	rules               ruleset
	requireDMARC        bool   // whitelist and "from" rule conditions only apply to verified senders
	authservID          string // only trust Authentication-Results from this server
	maxPerRun           int
	spamThreshold       int
	spamUnsureThreshold int // 0 disables the unsure zone for the spam score
	dryRun              bool
	fingerprint         string // hash of the config files, to notice edits
}

func loadConfig() (config, error) {
	dsn, err := url.Parse(os.Getenv("IMAP_DSN"))
	if err != nil || dsn.Scheme != "imaps" || dsn.Host == "" {
		return config{}, errors.New("IMAP_DSN must be imaps://user:password@host:993")
	}
	if dsn.User == nil {
		return config{}, errors.New("DSN missing user")
	}
	password, ok := dsn.User.Password()
	if !ok {
		return config{}, errors.New("DSN missing password")
	}

	var r envReader
	cfg := config{
		host:                dsn.Hostname(),
		port:                dsn.Port(),
		user:                dsn.User.Username(),
		password:            password,
		sourceFolder:        env("SOURCE_FOLDER", "INBOX"),
		spamFolder:          env("TARGET_FOLDER", "Junk"),
		cleanFolder:         os.Getenv("CLEAN_FOLDER"),
		unsureFolder:        os.Getenv("UNSURE_FOLDER"),
		requireDMARC:        r.bool("REQUIRE_DMARC_FOR_LISTS", true),
		authservID:          strings.TrimSpace(os.Getenv("AUTHSERV_ID")),
		maxPerRun:           r.int("MAX_PER_RUN", 100),
		spamThreshold:       r.int("SPAM_SCORE", 5),
		spamUnsureThreshold: r.int("SPAM_UNSURE_SCORE", 0),
		dryRun:              r.bool("DRY_RUN", true),
	}
	if r.err != nil {
		return config{}, r.err
	}
	if cfg.port == "" {
		cfg.port = "993"
	}

	if cfg.cleanFolder == "" {
		return config{}, errors.New("CLEAN_FOLDER must be set")
	}
	if cfg.maxPerRun < 0 {
		return config{}, errors.New("MAX_PER_RUN must be >= 0")
	}
	if cfg.spamThreshold < 1 {
		return config{}, errors.New("SPAM_SCORE must be >= 1")
	}
	if os.Getenv("SPAM_UNSURE_SCORE") != "" && (cfg.spamUnsureThreshold <= 0 || cfg.spamUnsureThreshold >= cfg.spamThreshold) {
		return config{}, errors.New("SPAM_UNSURE_SCORE must be between 1 and SPAM_SCORE-1")
	}

	if err := loadConfigFiles(&cfg); err != nil {
		return config{}, err
	}
	if err := validateFolders(cfg); err != nil {
		return config{}, err
	}
	return cfg, nil
}

// loadConfigFiles reads the lists and rules. They are re-read on every run,
// so edits apply without a restart.
func loadConfigFiles(cfg *config) error {
	whitelistPath := env("WHITELIST_FILE", "/config/whitelist.json")
	blacklistPath := env("BLACKLIST_FILE", "/config/blacklist.json")
	mappingPath := env("MAPPING_FILE", "/config/mapping.json")

	hash := sha256.New()
	read := func(path string) ([]byte, error) {
		content, err := readConfigFile(path)
		fmt.Fprintf(hash, "%s\x00%d\x00", path, len(content))
		hash.Write(content)
		return content, err
	}

	content, err := read(whitelistPath)
	if err == nil {
		cfg.whitelist, err = parseAddressList(whitelistPath, content)
	}
	if err != nil {
		return fmt.Errorf("whitelist: %w", err)
	}

	content, err = read(blacklistPath)
	if err == nil {
		cfg.blacklist, err = parseAddressList(blacklistPath, content)
	}
	if err != nil {
		return fmt.Errorf("blacklist: %w", err)
	}

	content, err = read(mappingPath)
	if err == nil {
		cfg.rules, err = parseRules(mappingPath, content)
	}
	if err != nil {
		return fmt.Errorf("mapping: %w", err)
	}

	cfg.fingerprint = hex.EncodeToString(hash.Sum(nil))
	return nil
}

// validateFolders rejects folder settings that would lose or loop mail.
func validateFolders(cfg config) error {
	usesUnsure := cfg.rules.unsureThreshold > 0 || cfg.spamUnsureThreshold > 0
	if usesUnsure && cfg.unsureFolder == "" {
		return errors.New("UNSURE_FOLDER must be set when unsure_threshold or SPAM_UNSURE_SCORE is used")
	}

	// Names are compared in their canonical form, so that e.g. "inbox" and
	// "INBOX" or "geschäftlich" and "gesch&AOQ-ftlich" count as the same folder.
	settings := []struct{ name, folder string }{
		{"SOURCE_FOLDER", cfg.sourceFolder},
		{"TARGET_FOLDER", cfg.spamFolder},
		{"CLEAN_FOLDER", cfg.cleanFolder},
	}
	if cfg.unsureFolder != "" {
		settings = append(settings, struct{ name, folder string }{"UNSURE_FOLDER", cfg.unsureFolder})
	}
	for i, a := range settings {
		for _, b := range settings[i+1:] {
			if canonicalFolder(a.folder) == canonicalFolder(b.folder) {
				return fmt.Errorf("%s and %s must differ (%q, %q)", a.name, b.name, a.folder, b.folder)
			}
		}
	}

	// Moving into the source folder would re-process the copy on every run.
	source := canonicalFolder(cfg.sourceFolder)
	for _, r := range cfg.rules.rules {
		if canonicalFolder(r.folder) == source {
			return fmt.Errorf("mapping: %s points to SOURCE_FOLDER %q", r.name, r.folder)
		}
	}
	return nil
}

// run performs one pass: connect, classify the unseen messages in the source
// folder and move each one to its destination (spam, unsure, a rule folder
// or the clean folder).
func run(st *state) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	client, err := dial(cfg.host, cfg.port)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.authenticatePlain(cfg.user, cfg.password); err != nil {
		return err
	}

	caps, err := client.capabilities()
	if err != nil {
		return err
	}
	useMove := caps["MOVE"]
	dryRun := cfg.dryRun
	if !useMove && !caps["UIDPLUS"] {
		// Copy, flag and expunge a single message needs UIDPLUS; without it
		// the sorter could only leave duplicates behind.
		dryRun = true
		if !cfg.dryRun && !st.warnedNoMove {
			log.Printf("server supports neither MOVE nor UIDPLUS; nothing will be moved (running as dry run)")
			st.warnedNoMove = true
		}
	}

	missingFolders := checkFolders(client, cfg)
	uidValidity, err := client.selectMailbox(cfg.sourceFolder)
	if err != nil {
		return err
	}
	st.sync(uidValidity, cfg.fingerprint, missingFolders, time.Now())

	uids, err := client.searchUnseen()
	if err != nil {
		return err
	}
	uids = st.pending(uids)
	if len(uids) > cfg.maxPerRun {
		uids = uids[:cfg.maxPerRun]
	}

	for _, uid := range uids {
		result, err := processMessage(client, cfg, uid, dryRun, useMove)
		switch result {
		case stayed:
			st.skip(uid)
		case copiedUnflagged:
			st.markCopiedUnflagged(uid)
		}
		if err != nil {
			return err
		}
	}

	client.logout()
	return nil
}

// checkFolders logs every destination folder that does not exist on the
// server and returns the missing folders as one comparable string. Mail for
// a missing folder fails to move and stays in the source folder, so it is
// retried once the folder has been created.
func checkFolders(client *imapClient, cfg config) string {
	usedBy := map[string][]string{
		cfg.spamFolder:  {"TARGET_FOLDER"},
		cfg.cleanFolder: {"CLEAN_FOLDER"},
	}
	if cfg.unsureFolder != "" {
		usedBy[cfg.unsureFolder] = append(usedBy[cfg.unsureFolder], "UNSURE_FOLDER")
	}
	for _, r := range cfg.rules.rules {
		usedBy[r.folder] = append(usedBy[r.folder], "mapping "+r.name)
	}

	existing, separator, err := client.listMailboxes()
	if err != nil {
		log.Printf("folder check failed: %v", err)
		return ""
	}

	folders := make([]string, 0, len(usedBy))
	for folder := range usedBy {
		folders = append(folders, folder)
	}
	sort.Strings(folders) // stable log order

	var missing []string
	for _, folder := range folders {
		if existing[canonicalFolder(folder)] {
			continue
		}
		users := usedBy[folder]
		sort.Strings(users)
		log.Printf("folder check: %q does not exist on the server (used by %s; server hierarchy separator is %q); mail for it stays in %q",
			folder, strings.Join(users, ", "), separator, cfg.sourceFolder)
		missing = append(missing, folder)
	}
	return strings.Join(missing, "\x00")
}

// verdict is the outcome of classifying one message.
type verdict struct {
	folder    string
	label     string
	spamScore *int   // nil if the spam check was skipped
	rule      string // best-scoring rule, "" without rules
	ruleScore *int
	reasons   []string
	markRead  bool // set \Seen before moving
}

// classify decides where a message belongs. First it is judged clean, spam
// or unsure: whitelist, then blacklist, then header score. Only clean mail
// is then routed by the mapping rules.
//
// The From header can be forged, so the whitelist and "from" rule conditions
// only apply when the sender is verified by DMARC, or by DKIM if the server
// reports no DMARC result (unless REQUIRE_DMARC_FOR_LISTS=false). An
// unverified whitelisted sender falls through to the remaining checks.
func classify(cfg config, h headers) verdict {
	sender := senderAddress(h.get("from"))
	auth := authenticationResults(h, cfg.authservID)
	verified := !cfg.requireDMARC || senderVerified(auth, sender)

	var notes []string
	if cfg.whitelist.contains(sender) {
		if verified {
			return routeClean(cfg, h, verified, verdict{reasons: []string{"WHITELISTED"}})
		}
		notes = append(notes, "WHITELIST_UNVERIFIED")
	}
	if cfg.blacklist.contains(sender) {
		return verdict{folder: cfg.spamFolder, label: "spam", reasons: append(notes, "BLACKLISTED")}
	}

	score, reasons := spamScore(h, auth)
	v := verdict{spamScore: &score, reasons: append(notes, reasons...)}
	switch {
	case score >= cfg.spamThreshold:
		v.folder, v.label = cfg.spamFolder, "spam"
		return v
	case cfg.spamUnsureThreshold > 0 && score >= cfg.spamUnsureThreshold:
		v.folder, v.label = cfg.unsureFolder, "unsure"
		v.reasons = append(v.reasons, "SPAM_UNSURE")
		return v
	}
	return routeClean(cfg, h, verified, v)
}

// routeClean sends clean mail to the folder of the best-scoring rule, to
// UNSURE_FOLDER if that rule only reaches the unsure zone, or else to
// CLEAN_FOLDER.
func routeClean(cfg config, h headers, verified bool, v verdict) verdict {
	v.folder, v.label = cfg.cleanFolder, "clean"

	best, score, ignoredSender := cfg.rules.bestMatch(messageFields(h), verified)
	if ignoredSender {
		v.reasons = append(v.reasons, "RULE_SENDER_UNVERIFIED")
	}
	if best == nil {
		return v
	}

	v.rule, v.ruleScore = best.name, &score
	switch {
	case score >= cfg.rules.threshold:
		v.folder, v.label, v.markRead = best.folder, "mapped", best.markRead
		v.reasons = append(v.reasons, fmt.Sprintf("RULE:%q=%d", best.name, score))
	case cfg.rules.unsureThreshold > 0 && score >= cfg.rules.unsureThreshold:
		v.folder, v.label = cfg.unsureFolder, "unsure"
		v.reasons = append(v.reasons, fmt.Sprintf("RULE_UNSURE:%q=%d", best.name, score))
	}
	return v
}

// moveResult says what happened to a message.
type moveResult int

const (
	stayed          moveResult = iota // still in the source folder, unchanged
	moved                             // left the source folder (or is flagged \Deleted there)
	copiedUnflagged                   // copied, but the original could not be flagged
)

// processMessage classifies a single message and moves it to the matching
// folder. Per-message problems are logged and skipped; only errors that
// leave the mailbox in an inconsistent state are returned.
func processMessage(client *imapClient, cfg config, uid string, dryRun, useMove bool) (moveResult, error) {
	if _, err := strconv.ParseUint(uid, 10, 32); err != nil {
		return stayed, nil // not a valid UID, never pass it to the server
	}

	rawHeader, err := client.fetchHeader(uid)
	if err != nil {
		log.Printf("uid=%s fetch error: %v", uid, err)
		return stayed, nil
	}
	if rawHeader == "" {
		return stayed, nil
	}

	h := parseHeaders(rawHeader)
	v := classify(cfg, h)
	log.Printf("uid=%s sender=%s verdict=%s spam_score=%s rule=%s rule_score=%s reasons=%s folder=%q mark_read=%t dry_run=%t",
		uid, orDash(senderAddress(h.get("from"))), v.label, scoreText(v.spamScore), ruleText(v.rule),
		scoreText(v.ruleScore), orDash(strings.Join(v.reasons, ",")), v.folder, v.markRead, dryRun)
	if dryRun {
		return stayed, nil
	}
	return moveMessage(client, uid, v.folder, useMove, v.markRead)
}

// moveMessage moves a message to folder, atomically with MOVE if available,
// otherwise by copy, flag and UID EXPUNGE. With markRead, \Seen is set
// first so that the moved message carries it.
func moveMessage(client *imapClient, uid, folder string, useMove, markRead bool) (moveResult, error) {
	if markRead {
		if err := client.setSeen(uid, true); err != nil {
			log.Printf("uid=%s could not be marked as read, mail stays in place: %v", uid, err)
			return stayed, nil
		}
	}

	action, err := "move", error(nil)
	if useMove {
		err = client.move(uid, folder)
	} else {
		action, err = "copy", client.copy(uid, folder)
	}
	if err != nil {
		log.Printf("uid=%s %s to %q failed, mail stays in place: %v", uid, action, folder, err)
		unmarkRead(client, uid, markRead)
		return stayed, nil
	}
	if useMove {
		return moved, nil
	}

	// A single failure is often transient, so flagging is tried twice.
	err = client.markDeleted(uid)
	if err != nil {
		err = client.markDeleted(uid)
	}
	if err != nil {
		log.Printf("uid=%s was copied to %q but the original could not be flagged; it will not be processed again until restart, remove one of the two copies manually", uid, folder)
		return copiedUnflagged, fmt.Errorf("copied uid %s but could not flag the original: %w", uid, err)
	}

	if err := client.expunge(uid); err != nil {
		return moved, fmt.Errorf("UID EXPUNGE failed; uid %s is copied and flagged \\Deleted: %w", uid, err)
	}
	return moved, nil
}

// unmarkRead removes \Seen again after a failed move, so that the message
// stays unread in the source folder and is picked up by the next run.
func unmarkRead(client *imapClient, uid string, markRead bool) {
	if !markRead {
		return
	}
	if err := client.setSeen(uid, false); err != nil {
		log.Printf("uid=%s is marked as read but was not moved; it will not be sorted until it is marked unread: %v", uid, err)
	}
}

func scoreText(score *int) string {
	if score == nil {
		return "-"
	}
	return strconv.Itoa(*score)
}

func ruleText(name string) string {
	if name == "" {
		return "-"
	}
	return strconv.Quote(name)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// envInt reads a whole number. An unset variable yields fallback, an
// invalid one an error.
func envInt(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number, got %q", key, value)
	}
	return n, nil
}

// envBool reads a switch (true/false, t/f or 1/0, in any case). An unset
// variable yields fallback, an invalid one an error.
func envBool(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(strings.ToLower(value))
	if err != nil {
		return false, fmt.Errorf("%s must be true or false, got %q", key, value)
	}
	return b, nil
}

// envReader reads several variables and keeps the first error.
type envReader struct{ err error }

func (r *envReader) int(key string, fallback int) int {
	n, err := envInt(key, fallback)
	if err != nil && r.err == nil {
		r.err = err
	}
	return n
}

func (r *envReader) bool(key string, fallback bool) bool {
	b, err := envBool(key, fallback)
	if err != nil && r.err == nil {
		r.err = err
	}
	return b
}

// logSettings logs the effective settings once at startup, without the password.
func logSettings(cfg config, pollSeconds int) {
	log.Printf("settings: host=%s port=%s user=%s source=%q target=%q clean=%q unsure=%q spam_score=%d spam_unsure_score=%d "+
		"max_per_run=%d poll_seconds=%d dry_run=%t require_dmarc=%t authserv_id=%q rules=%d whitelist=%d blacklist=%d",
		cfg.host, cfg.port, cfg.user, cfg.sourceFolder, cfg.spamFolder, cfg.cleanFolder, cfg.unsureFolder,
		cfg.spamThreshold, cfg.spamUnsureThreshold, cfg.maxPerRun, pollSeconds, cfg.dryRun, cfg.requireDMARC,
		cfg.authservID, len(cfg.rules.rules), len(cfg.whitelist), len(cfg.blacklist))
	if cfg.authservID == "" {
		log.Printf("warning: AUTHSERV_ID is not set, so all Authentication-Results headers are trusted, including ones a sender could forge")
	}
}

// pollInterval reads POLL_SECONDS. An invalid value or one below 30 falls
// back to the default with a warning, so a typo does not make the container
// restart in a loop.
func pollInterval() (seconds int, warning string) {
	const fallback = 300
	seconds, err := envInt("POLL_SECONDS", fallback)
	switch {
	case err != nil:
		return fallback, fmt.Sprintf("warning: %v; using %d", err, fallback)
	case seconds < 30:
		return fallback, fmt.Sprintf("warning: POLL_SECONDS must be >= 30, got %d; using %d", seconds, fallback)
	}
	return seconds, ""
}

func main() {
	interval, warning := pollInterval()
	if warning != "" {
		log.Print(warning)
	}

	if cfg, err := loadConfig(); err != nil {
		log.Printf("configuration error: %v", err)
	} else {
		logSettings(cfg, interval)
	}

	st := newState()
	for {
		if err := run(st); err != nil {
			log.Printf("run failed: %v", err)
		}
		time.Sleep(time.Duration(interval) * time.Second)
	}
}
