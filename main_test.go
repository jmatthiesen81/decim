package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// setBaseEnv sets a valid configuration with config files in a temp dir.
func setBaseEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("IMAP_DSN", "imaps://user%40example.org:secret@imap.example.org")
	t.Setenv("CLEAN_FOLDER", "Clean")
	t.Setenv("MAPPING_FILE", filepath.Join(dir, "mapping.json"))
	t.Setenv("WHITELIST_FILE", filepath.Join(dir, "whitelist.json"))
	t.Setenv("BLACKLIST_FILE", filepath.Join(dir, "blacklist.json"))
	return dir
}

func TestLoadConfigDefaults(t *testing.T) {
	setBaseEnv(t)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.user != "user@example.org" || cfg.port != "993" || cfg.sourceFolder != "INBOX" || cfg.spamFolder != "Junk" ||
		!cfg.dryRun || !cfg.requireDMARC || cfg.maxPerRun != 100 || cfg.spamThreshold != 5 {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	tests := map[string]string{
		"SPAM_SCORE":              "5,5",
		"SPAM_SCORE=0":            "0",
		"SPAM_SCORE=-3":           "-3",
		"MAX_PER_RUN":             "-1",
		"DRY_RUN":                 "no",
		"REQUIRE_DMARC_FOR_LISTS": "maybe",
		"SPAM_UNSURE_SCORE":       "5",
		"CLEAN_FOLDER":            "INBOX",
		"IMAP_DSN":                "imap://user:secret@host",
	}
	for name, value := range tests {
		key, _, _ := strings.Cut(name, "=") // "SPAM_SCORE=0" tests SPAM_SCORE
		t.Run(name, func(t *testing.T) {
			setBaseEnv(t)
			t.Setenv(key, value)
			if _, err := loadConfig(); err == nil {
				t.Errorf("%s=%q: want error", key, value)
			}
		})
	}
}

func TestEnvBoolAcceptsCommonForms(t *testing.T) {
	for value, want := range map[string]bool{"false": false, "False": false, "0": false, "TRUE": true, "1": true, "tRuE": true, "fAlSe": false, " T ": true} {
		t.Setenv("SWITCH", value)
		if got, err := envBool("SWITCH", !want); err != nil || got != want {
			t.Errorf("%q: got %t %v", value, got, err)
		}
	}
}

func TestFingerprintChangesWithConfigFiles(t *testing.T) {
	dir := setBaseEnv(t)
	first, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "whitelist.json"), []byte(`["a@example.com"]`), 0o644)
	second, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if first.fingerprint == second.fingerprint {
		t.Error("fingerprint did not change after editing the whitelist")
	}
}

func TestValidateFolders(t *testing.T) {
	cfg := testConfig(t)
	if err := validateFolders(cfg); err != nil {
		t.Fatal(err)
	}

	noUnsure := cfg
	noUnsure.unsureFolder = ""
	if validateFolders(noUnsure) == nil {
		t.Error("want error: unsure zone without UNSURE_FOLDER")
	}

	same := cfg
	same.unsureFolder = "Clean"
	if validateFolders(same) == nil {
		t.Error("want error: UNSURE_FOLDER equals CLEAN_FOLDER")
	}

	for _, folder := range []string{"INBOX", "inbox", "Inbox"} {
		loop := cfg
		loop.rules.rules = append([]rule(nil), cfg.rules.rules...)
		loop.rules.rules[0].folder = folder
		if validateFolders(loop) == nil {
			t.Errorf("want error: rule folder %q is SOURCE_FOLDER", folder)
		}
	}

	encoded := cfg
	encoded.sourceFolder = "Amazon/gesch&AOQ-ftlich"
	encoded.rules.rules = append([]rule(nil), cfg.rules.rules...)
	encoded.rules.rules[0].folder = "Amazon/geschäftlich"
	if validateFolders(encoded) == nil {
		t.Error("want error: encoded and plain spelling of SOURCE_FOLDER")
	}

	cleanIsSource := cfg
	cleanIsSource.cleanFolder = "inbox"
	if validateFolders(cleanIsSource) == nil {
		t.Error("want error: CLEAN_FOLDER is SOURCE_FOLDER in another spelling")
	}
}

func TestCanonicalFolder(t *testing.T) {
	tests := map[string]string{
		"inbox":         "INBOX",
		"Inbox/Archive": "INBOX/Archive",
		"INBOX.Archive": "INBOX.Archive", // "." separator is left as is
		"geschäftlich":  "gesch&AOQ-ftlich",
		"Inboxes":       "Inboxes",
	}
	for in, want := range tests {
		if got := canonicalFolder(in); got != want {
			t.Errorf("canonicalFolder(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPollInterval(t *testing.T) {
	tests := []struct {
		value   string
		seconds int
		warns   bool
	}{
		{"", 300, false},
		{"60", 60, false},
		{"10", 300, true},
		{"5m", 300, true},
	}
	for _, tt := range tests {
		t.Setenv("POLL_SECONDS", tt.value)
		seconds, warning := pollInterval()
		if seconds != tt.seconds || (warning != "") != tt.warns {
			t.Errorf("POLL_SECONDS=%q: got %d %q", tt.value, seconds, warning)
		}
	}
}

func TestStateSkipsAndRetries(t *testing.T) {
	st := newState()
	now := time.Now()
	st.sync("1", "fp", "", now)
	st.skip("7")

	if got := st.pending([]string{"5", "7", "9"}); strings.Join(got, ",") != "5,9" {
		t.Errorf("pending = %v", got)
	}

	st.sync("1", "fp", "", now.Add(time.Minute))
	if len(st.pending([]string{"7"})) != 0 {
		t.Error("skipped UID came back too early")
	}

	for name, sync := range map[string]func(){
		"config changed":    func() { st.sync("1", "other", "", now.Add(time.Minute)) },
		"folder appeared":   func() { st.sync("1", "fp", "Work", now.Add(time.Minute)) },
		"mailbox recreated": func() { st.sync("2", "fp", "", now.Add(time.Minute)) },
		"retry time passed": func() { st.sync("1", "fp", "", now.Add(retrySkippedAfter+time.Minute)) },
	} {
		st = newState()
		st.sync("1", "fp", "", now)
		st.skip("7")
		sync()
		if len(st.pending([]string{"7"})) != 1 {
			t.Errorf("%s: skipped UID was not retried", name)
		}
	}
}

func TestStateKeepsCopiedUnflagged(t *testing.T) {
	st := newState()
	now := time.Now()
	st.sync("1", "fp", "", now)
	st.markCopiedUnflagged("7")

	// Neither time, config changes nor folder changes may bring it back.
	st.sync("1", "other", "Work", now.Add(2*retrySkippedAfter))
	if len(st.pending([]string{"7"})) != 0 {
		t.Error("copied but unflagged UID must not be processed again")
	}

	st.sync("2", "other", "Work", now.Add(2*retrySkippedAfter))
	if len(st.pending([]string{"7"})) != 1 {
		t.Error("a recreated mailbox must forget the UID")
	}
}

// commandServer answers every command with OK, except those whose prefix
// is listed in fail, and returns fetchHeader for FETCH.
func commandServer(fetchHeader string, fail ...string) func(string) (string, string) {
	return func(cmd string) (string, string) {
		for _, prefix := range fail {
			if strings.HasPrefix(cmd, prefix) {
				return "", "NO"
			}
		}
		if strings.HasPrefix(cmd, "UID FETCH") {
			return "* 1 FETCH (BODY[HEADER] {" + strconv.Itoa(len(fetchHeader)) + "}\r\n" + fetchHeader + ")\r\n", "OK"
		}
		if strings.HasPrefix(cmd, "LIST") {
			return `* LIST () "/" Junk` + "\r\n" + `* LIST () "/" Clean` + "\r\n", "OK"
		}
		return "", "OK"
	}
}

func TestMoveMessageUsesMove(t *testing.T) {
	client, server, _ := startFakeServer(t, "* OK ready", commandServer(""))
	result, err := moveMessage(client, "5", "Work", true, false)
	if result != moved || err != nil {
		t.Fatalf("result=%d err=%v", result, err)
	}
	if got := server.recorded(); len(got) != 1 || got[0] != `UID MOVE 5 "Work"` {
		t.Errorf("commands = %q", got)
	}
}

func TestMoveMessageCopyFallback(t *testing.T) {
	client, server, _ := startFakeServer(t, "* OK ready", commandServer(""))
	if result, err := moveMessage(client, "5", "Work", false, false); result != moved || err != nil {
		t.Fatalf("result=%d err=%v", result, err)
	}
	want := `UID COPY 5 "Work"|UID STORE 5 +FLAGS.SILENT (\Deleted)|UID EXPUNGE 5`
	if got := strings.Join(server.recorded(), "|"); got != want {
		t.Errorf("commands = %q", got)
	}
}

func TestMoveMessageMarkRead(t *testing.T) {
	client, server, _ := startFakeServer(t, "* OK ready", commandServer(""))
	if result, err := moveMessage(client, "5", "Work", true, true); result != moved || err != nil {
		t.Fatalf("result=%d err=%v", result, err)
	}
	want := `UID STORE 5 +FLAGS.SILENT (\Seen)|UID MOVE 5 "Work"`
	if got := strings.Join(server.recorded(), "|"); got != want {
		t.Errorf("commands = %q", got)
	}

	// A failed move must leave the mail unread, so it is retried.
	client, server, _ = startFakeServer(t, "* OK ready", commandServer("", "UID MOVE"))
	if result, err := moveMessage(client, "5", "Missing", true, true); result != stayed || err != nil {
		t.Fatalf("move failure: result=%d err=%v", result, err)
	}
	want = `UID STORE 5 +FLAGS.SILENT (\Seen)|UID MOVE 5 "Missing"|UID STORE 5 -FLAGS.SILENT (\Seen)`
	if got := strings.Join(server.recorded(), "|"); got != want {
		t.Errorf("commands = %q", got)
	}

	// If \Seen cannot be set, the mail is not moved.
	client, server, _ = startFakeServer(t, "* OK ready", commandServer("", "UID STORE"))
	if result, err := moveMessage(client, "5", "Work", true, true); result != stayed || err != nil {
		t.Fatalf("store failure: result=%d err=%v", result, err)
	}
	if len(server.recorded()) != 1 {
		t.Errorf("must not move after a failed STORE, got %q", server.recorded())
	}
}

func TestMoveMessageFailures(t *testing.T) {
	client, server, _ := startFakeServer(t, "* OK ready", commandServer("", "UID COPY"))
	if result, err := moveMessage(client, "5", "Missing", false, false); result != stayed || err != nil {
		t.Errorf("copy failure: result=%d err=%v", result, err)
	}
	if len(server.recorded()) != 1 {
		t.Error("must not flag the original after a failed copy")
	}

	client, _, _ = startFakeServer(t, "* OK ready", commandServer("", "UID EXPUNGE"))
	if result, err := moveMessage(client, "5", "Work", false, false); result != moved || err == nil {
		t.Errorf("expunge failure: result=%d err=%v (want moved and error)", result, err)
	}
}

func TestMoveMessageFlagFailure(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	client, server, _ := startFakeServer(t, "* OK ready", commandServer("", "UID STORE"))
	result, err := moveMessage(client, "5", "Work", false, false)
	if result != copiedUnflagged || err == nil {
		t.Fatalf("result=%d err=%v (want copiedUnflagged and error)", result, err)
	}
	want := `UID COPY 5 "Work"|UID STORE 5 +FLAGS.SILENT (\Deleted)|UID STORE 5 +FLAGS.SILENT (\Deleted)`
	if got := strings.Join(server.recorded(), "|"); got != want {
		t.Errorf("commands = %q (want one retry, no expunge)", got)
	}
	if !strings.Contains(logs.String(), "remove one of the two copies manually") {
		t.Errorf("log = %q", logs.String())
	}
}

func TestMoveMessageFlagRetrySucceeds(t *testing.T) {
	failures := 0
	client, server, _ := startFakeServer(t, "* OK ready", func(cmd string) (string, string) {
		if strings.HasPrefix(cmd, "UID STORE") && failures == 0 {
			failures++
			return "", "NO"
		}
		return "", "OK"
	})
	if result, err := moveMessage(client, "5", "Work", false, false); result != moved || err != nil {
		t.Fatalf("result=%d err=%v", result, err)
	}
	if got := server.recorded(); len(got) != 4 || got[3] != "UID EXPUNGE 5" {
		t.Errorf("commands = %q", got)
	}
}

func TestProcessMessageDryRun(t *testing.T) {
	header := "From: x@y.example\r\nSubject: hi\r\n\r\n"
	client, server, _ := startFakeServer(t, "* OK ready", commandServer(header))

	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	result, err := processMessage(client, testConfig(t), "5", true, true)
	if result != stayed || err != nil {
		t.Fatalf("result=%d err=%v", result, err)
	}
	if len(server.recorded()) != 1 {
		t.Errorf("dry run must only fetch, got %q", server.recorded())
	}
	want := `uid=5 sender=x@y.example verdict=clean spam_score=0 rule="Invoices" rule_score=0 reasons=- folder="Clean" mark_read=false dry_run=true`
	if !strings.Contains(logs.String(), want) {
		t.Errorf("log = %q", logs.String())
	}
}

func TestCheckFoldersReportsMissing(t *testing.T) {
	client, _, _ := startFakeServer(t, "* OK ready", commandServer(""))
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	missing := checkFolders(client, testConfig(t))
	if missing != "Finance\x00Unsure" {
		t.Errorf("missing = %q", missing)
	}
	if !strings.Contains(logs.String(), `"Finance" does not exist on the server (used by mapping Invoices; server hierarchy separator is "/")`) {
		t.Errorf("log = %q", logs.String())
	}
}
