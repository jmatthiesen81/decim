package main

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	dialTimeout    = 15 * time.Second
	commandTimeout = 30 * time.Second // per command, so long runs do not time out
	maxLiteralSize = 1024 * 1024
)

// imapClient is a minimal IMAP client that speaks just enough of the
// protocol to search, fetch headers and move messages.
type imapClient struct {
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
	tagSeq int
}

// dial opens a TLS connection to the server and checks the greeting.
func dial(host, port string) (*imapClient, error) {
	dialer := &net.Dialer{Timeout: dialTimeout}
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}

	conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(host, port), tlsConfig)
	if err != nil {
		return nil, err
	}
	return newClient(conn)
}

// newClient wraps an established connection and checks the greeting.
func newClient(conn net.Conn) (*imapClient, error) {
	c := &imapClient{
		conn:   conn,
		reader: bufio.NewReader(conn),
		writer: bufio.NewWriter(conn),
	}
	c.extendDeadline()

	greeting, err := c.readLine()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("IMAP greeting: %w", err)
	}
	if !strings.HasPrefix(greeting, "* OK") {
		conn.Close()
		return nil, fmt.Errorf("IMAP greeting: unexpected %q", greeting)
	}
	return c, nil
}

func (c *imapClient) Close() error {
	return c.conn.Close()
}

func (c *imapClient) extendDeadline() {
	c.conn.SetDeadline(time.Now().Add(commandTimeout))
}

func (c *imapClient) nextTag() string {
	c.tagSeq++
	return fmt.Sprintf("A%05d", c.tagSeq)
}

func (c *imapClient) readLine() (string, error) {
	line, err := c.reader.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

func (c *imapClient) writeLine(line string) error {
	if _, err := c.writer.WriteString(line + "\r\n"); err != nil {
		return err
	}
	return c.writer.Flush()
}

// command sends a tagged command and returns its untagged responses.
func (c *imapClient) command(cmd string) ([]string, error) {
	c.extendDeadline()
	tag := c.nextTag()
	if err := c.writeLine(tag + " " + cmd); err != nil {
		return nil, err
	}
	return c.readResponse(tag)
}

// readResponse collects all untagged response lines until the tagged
// completion for tag. Literals ({n}) are read and appended as a separate entry.
func (c *imapClient) readResponse(tag string) ([]string, error) {
	var responses []string
	for {
		line, err := c.readLine()
		if err != nil {
			return nil, err
		}

		if strings.HasPrefix(line, tag+" ") {
			if !strings.HasPrefix(line, tag+" OK") {
				return responses, fmt.Errorf("IMAP command failed: %s", line)
			}
			return responses, nil
		}
		responses = append(responses, line)

		if size, ok := literalSize(line); ok {
			if size < 0 || size > maxLiteralSize {
				return nil, errors.New("invalid or oversized literal")
			}
			literal := make([]byte, size)
			if _, err := io.ReadFull(c.reader, literal); err != nil {
				return nil, err
			}
			responses = append(responses, string(literal))
		}
	}
}

// literalSize reports whether line announces a literal ("... {123}") and its
// size. A malformed size is reported as -1 so the caller rejects it.
func literalSize(line string) (int, bool) {
	open := strings.LastIndex(line, "{")
	if open < 0 || !strings.HasSuffix(line, "}") {
		return 0, false
	}
	size, err := strconv.Atoi(line[open+1 : len(line)-1])
	if err != nil {
		return -1, true
	}
	return size, true
}

// authenticatePlain logs in via SASL PLAIN. The credentials are sent base64
// encoded in the continuation response and never logged.
func (c *imapClient) authenticatePlain(user, password string) error {
	c.extendDeadline()
	tag := c.nextTag()
	if err := c.writeLine(tag + " AUTHENTICATE PLAIN"); err != nil {
		return err
	}

	// Wait for the continuation request, skipping untagged lines.
	for {
		line, err := c.readLine()
		if err != nil {
			return err
		}
		if line == "+" || strings.HasPrefix(line, "+ ") {
			break
		}
		if strings.HasPrefix(line, tag+" ") {
			return errors.New("server refused AUTHENTICATE PLAIN")
		}
	}

	credentials := base64.StdEncoding.EncodeToString([]byte("\x00" + user + "\x00" + password))
	if err := c.writeLine(credentials); err != nil {
		return err
	}

	// Servers may send untagged lines (e.g. CAPABILITY) before the result.
	if _, err := c.readResponse(tag); err != nil {
		return errors.New("IMAP authentication failed")
	}
	return nil
}

// capabilities returns the upper-cased capabilities the server advertises.
func (c *imapClient) capabilities() (map[string]bool, error) {
	lines, err := c.command("CAPABILITY")
	if err != nil {
		return nil, err
	}

	caps := map[string]bool{}
	for _, line := range lines {
		if rest, ok := strings.CutPrefix(line, "* CAPABILITY "); ok {
			for _, capability := range strings.Fields(rest) {
				caps[strings.ToUpper(capability)] = true
			}
		}
	}
	return caps, nil
}

// listMailboxes returns the names of all mailboxes on the server (in their
// canonical form, see canonicalFolder) and the hierarchy separator.
func (c *imapClient) listMailboxes() (names map[string]bool, separator string, err error) {
	lines, err := c.command(`LIST "" "*"`)
	if err != nil {
		return nil, "", err
	}

	names = map[string]bool{}
	for i, line := range lines {
		rest, ok := strings.CutPrefix(line, "* LIST ")
		if !ok {
			continue
		}
		sep, name, isLiteral, ok := parseListResponse(rest)
		if !ok {
			continue
		}
		if isLiteral {
			if i+1 >= len(lines) {
				continue
			}
			name = lines[i+1]
		}
		names[canonicalFolder(name)] = true
		if separator == "" {
			separator = sep
		}
	}
	return names, separator, nil
}

// parseListResponse parses `(\flags) "/" "name"` from a LIST response. The
// separator may be NIL and the name quoted, an atom or a literal ({n}).
func parseListResponse(s string) (separator, name string, isLiteral, ok bool) {
	if !strings.HasPrefix(s, "(") {
		return "", "", false, false
	}
	closing := strings.IndexByte(s, ')')
	if closing < 0 {
		return "", "", false, false
	}
	s = strings.TrimSpace(s[closing+1:])

	if rest, isNil := strings.CutPrefix(s, "NIL"); isNil {
		s = rest
	} else if separator, s, ok = parseQuoted(s); !ok {
		return "", "", false, false
	}
	s = strings.TrimSpace(s)

	switch {
	case strings.HasPrefix(s, `"`):
		name, _, ok = parseQuoted(s)
		return separator, name, false, ok
	case strings.HasPrefix(s, "{"):
		return separator, "", true, true
	default:
		return separator, s, false, s != ""
	}
}

// parseQuoted reads an IMAP quoted string from the start of s and returns
// its value and the remaining input.
func parseQuoted(s string) (value, rest string, ok bool) {
	if !strings.HasPrefix(s, `"`) {
		return "", s, false
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		case '"':
			return b.String(), s[i+1:], true
		default:
			b.WriteByte(s[i])
		}
	}
	return "", s, false
}

// canonicalFolder returns a folder name as the server sees it, so that two
// spellings of the same folder compare equal: the name is encoded (see
// encodeMailbox) and INBOX, which is case-insensitive in IMAP, is upper-cased,
// also as the first level of a path such as "inbox/Archive".
func canonicalFolder(name string) string {
	encoded := encodeMailbox(name)
	if strings.EqualFold(encoded, "INBOX") {
		return "INBOX"
	}
	if head, rest, ok := strings.Cut(encoded, "/"); ok && strings.EqualFold(head, "INBOX") {
		return "INBOX/" + rest
	}
	return encoded
}

// selectMailbox opens a mailbox and returns its UIDVALIDITY ("" if the
// server did not report one).
func (c *imapClient) selectMailbox(name string) (string, error) {
	lines, err := c.command("SELECT " + quote(encodeMailbox(name)))
	if err != nil {
		return "", err
	}
	for _, line := range lines {
		if _, rest, ok := strings.Cut(line, "[UIDVALIDITY "); ok {
			if value, _, ok := strings.Cut(rest, "]"); ok {
				return value, nil
			}
		}
	}
	return "", nil
}

// searchUnseen returns the UIDs of unseen messages in the selected mailbox.
// Messages already flagged \Deleted are left out, so a message whose expunge
// failed is not copied again.
func (c *imapClient) searchUnseen() ([]string, error) {
	lines, err := c.command("UID SEARCH UNSEEN UNDELETED")
	if err != nil {
		return nil, err
	}

	var uids []string
	for _, line := range lines {
		if rest, ok := strings.CutPrefix(line, "* SEARCH"); ok {
			uids = append(uids, strings.Fields(rest)...)
		}
	}
	return uids, nil
}

// fetchHeader returns the raw header block of a message without setting \Seen.
// An empty string means the response contained no recognizable header.
func (c *imapClient) fetchHeader(uid string) (string, error) {
	responses, err := c.command("UID FETCH " + uid + " (BODY.PEEK[HEADER])")
	if err != nil {
		return "", err
	}
	for _, r := range responses {
		if strings.Contains(r, "\n") && strings.Contains(r, ":") {
			return r, nil
		}
	}
	return "", nil
}

// move moves a message atomically (requires MOVE, RFC 6851).
func (c *imapClient) move(uid, mailbox string) error {
	_, err := c.command("UID MOVE " + uid + " " + quote(encodeMailbox(mailbox)))
	return err
}

func (c *imapClient) copy(uid, mailbox string) error {
	_, err := c.command("UID COPY " + uid + " " + quote(encodeMailbox(mailbox)))
	return err
}

func (c *imapClient) markDeleted(uid string) error {
	_, err := c.command("UID STORE " + uid + " +FLAGS.SILENT (\\Deleted)")
	return err
}

// setSeen adds or removes the \Seen flag.
func (c *imapClient) setSeen(uid string, seen bool) error {
	op := "-"
	if seen {
		op = "+"
	}
	_, err := c.command("UID STORE " + uid + " " + op + "FLAGS.SILENT (\\Seen)")
	return err
}

// setJunk marks a message as spam with the $Junk keyword (RFC 5788) and
// Thunderbird's Junk, and removes the opposite $NotJunk and NonJunk. With
// junk=false it only removes $Junk and Junk again.
func (c *imapClient) setJunk(uid string, junk bool) error {
	if !junk {
		_, err := c.command("UID STORE " + uid + " -FLAGS.SILENT ($Junk Junk)")
		return err
	}
	if _, err := c.command("UID STORE " + uid + " +FLAGS.SILENT ($Junk Junk)"); err != nil {
		return err
	}
	_, err := c.command("UID STORE " + uid + " -FLAGS.SILENT ($NotJunk NonJunk)")
	return err
}

// expunge removes only the given UID (requires UIDPLUS), leaving other
// messages flagged \Deleted untouched.
func (c *imapClient) expunge(uid string) error {
	_, err := c.command("UID EXPUNGE " + uid)
	return err
}

func (c *imapClient) logout() {
	_, _ = c.command("LOGOUT")
}

// quote encodes s as an IMAP quoted string.
func quote(s string) string {
	escaper := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + escaper.Replace(s) + `"`
}
