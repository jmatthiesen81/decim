package main

import (
	"bufio"
	"encoding/base64"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeServer is a scripted IMAP server. handle receives each command
// without its tag and returns the untagged response text (CRLF-terminated
// lines, may contain literals) and the completion status, e.g. "OK" or "NO".
type fakeServer struct {
	mu       sync.Mutex
	commands []string
}

func (s *fakeServer) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func startFakeServer(t *testing.T, greeting string, handle func(cmd string) (untagged, status string)) (*imapClient, *fakeServer, error) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { clientConn.Close(); serverConn.Close() })

	server := &fakeServer{}
	go func() {
		r := bufio.NewReader(serverConn)
		serverConn.Write([]byte(greeting + "\r\n"))
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			tag, cmd, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
			if cmd == "AUTHENTICATE PLAIN" {
				serverConn.Write([]byte("+ \r\n"))
				credentials, err := r.ReadString('\n')
				if err != nil {
					return
				}
				cmd = "AUTH " + strings.TrimRight(credentials, "\r\n")
			}

			server.mu.Lock()
			server.commands = append(server.commands, cmd)
			server.mu.Unlock()

			untagged, status := handle(cmd)
			serverConn.Write([]byte(untagged + tag + " " + status + " done\r\n"))
		}
	}()

	client, err := newClient(clientConn)
	return client, server, err
}

func okServer(cmd string) (string, string) { return "", "OK" }

func TestGreetingErrorShowsLine(t *testing.T) {
	_, _, err := startFakeServer(t, "* BYE go away", okServer)
	if err == nil || !strings.Contains(err.Error(), "* BYE go away") {
		t.Fatalf("got %v, want error containing the greeting", err)
	}
}

func TestAuthenticateSkipsUntaggedLines(t *testing.T) {
	client, server, err := startFakeServer(t, "* OK ready", func(cmd string) (string, string) {
		return "* CAPABILITY IMAP4rev1 MOVE\r\n", "OK"
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.authenticatePlain("user", "secret"); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	encoded := strings.TrimPrefix(server.recorded()[0], "AUTH ")
	credentials, _ := base64.StdEncoding.DecodeString(encoded)
	if string(credentials) != "\x00user\x00secret" {
		t.Errorf("credentials = %q", credentials)
	}
}

func TestAuthenticateFailure(t *testing.T) {
	client, _, err := startFakeServer(t, "* OK ready", func(cmd string) (string, string) { return "", "NO" })
	if err != nil {
		t.Fatal(err)
	}
	if err := client.authenticatePlain("user", "wrong"); err == nil {
		t.Fatal("want authentication error")
	}
}

func TestCapabilities(t *testing.T) {
	client, _, _ := startFakeServer(t, "* OK ready", func(cmd string) (string, string) {
		return "* CAPABILITY IMAP4rev1 uidplus MOVE\r\n", "OK"
	})
	caps, err := client.capabilities()
	if err != nil {
		t.Fatal(err)
	}
	if !caps["UIDPLUS"] || !caps["MOVE"] || caps["IDLE"] {
		t.Errorf("caps = %v", caps)
	}
}

func TestListMailboxes(t *testing.T) {
	client, server, _ := startFakeServer(t, "* OK ready", func(cmd string) (string, string) {
		return `* LIST (\HasNoChildren) "/" "INBOX/Server"` + "\r\n" +
			`* LIST () "/" Junk` + "\r\n" +
			`* LIST () "/" {16}` + "\r\n" + "gesch&AOQ-ftlich" + "\r\n" +
			`* LIST (\Noselect) NIL inbox` + "\r\n" +
			`* LIST () "/" "With \"quote\""` + "\r\n", "OK"
	})
	names, separator, err := client.listMailboxes()
	if err != nil {
		t.Fatal(err)
	}
	if server.recorded()[0] != `LIST "" "*"` {
		t.Errorf("command = %q", server.recorded()[0])
	}
	for _, want := range []string{"INBOX/Server", "Junk", "gesch&AOQ-ftlich", "INBOX", `With "quote"`} {
		if !names[want] {
			t.Errorf("missing %q in %v", want, names)
		}
	}
	if separator != "/" {
		t.Errorf("separator = %q", separator)
	}
}

func TestSelectEncodesNameAndReturnsUIDValidity(t *testing.T) {
	client, server, _ := startFakeServer(t, "* OK ready", func(cmd string) (string, string) {
		return "* 3 EXISTS\r\n* OK [UIDVALIDITY 1234] UIDs valid\r\n", "OK"
	})
	validity, err := client.selectMailbox("Amazon/geschäftlich")
	if err != nil {
		t.Fatal(err)
	}
	if validity != "1234" {
		t.Errorf("uidvalidity = %q", validity)
	}
	if got := server.recorded()[0]; got != `SELECT "Amazon/gesch&AOQ-ftlich"` {
		t.Errorf("command = %q", got)
	}
}

func TestSearchUnseenExcludesDeleted(t *testing.T) {
	client, server, _ := startFakeServer(t, "* OK ready", func(cmd string) (string, string) {
		return "* SEARCH 4 7 9\r\n", "OK"
	})
	uids, err := client.searchUnseen()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(uids, ",") != "4,7,9" {
		t.Errorf("uids = %v", uids)
	}
	if got := server.recorded()[0]; got != "UID SEARCH UNSEEN UNDELETED" {
		t.Errorf("command = %q", got)
	}
}

func TestFetchHeaderReadsLiteral(t *testing.T) {
	header := "From: a@example.com\r\nSubject: Hi\r\n\r\n"
	client, _, _ := startFakeServer(t, "* OK ready", func(cmd string) (string, string) {
		return "* 1 FETCH (UID 5 BODY[HEADER] {" + strconv.Itoa(len(header)) + "}\r\n" + header + ")\r\n", "OK"
	})
	raw, err := client.fetchHeader("5")
	if err != nil {
		t.Fatal(err)
	}
	if raw != header {
		t.Errorf("header = %q", raw)
	}
}

func TestOversizedLiteralIsRejected(t *testing.T) {
	client, _, _ := startFakeServer(t, "* OK ready", func(cmd string) (string, string) {
		return "* 1 FETCH (BODY[HEADER] {99999999}\r\n", "OK"
	})
	if _, err := client.fetchHeader("5"); err == nil {
		t.Fatal("want error for oversized literal")
	}
}

func TestParseListResponse(t *testing.T) {
	tests := []struct {
		in, separator, name string
		literal, ok         bool
	}{
		{`(\HasChildren) "." "INBOX.Archiv"`, ".", "INBOX.Archiv", false, true},
		{`() NIL Junk`, "", "Junk", false, true},
		{`() "/" {5}`, "/", "", true, true},
		{`no flags`, "", "", false, false},
	}
	for _, tt := range tests {
		separator, name, literal, ok := parseListResponse(tt.in)
		if separator != tt.separator || name != tt.name || literal != tt.literal || ok != tt.ok {
			t.Errorf("%q: got (%q, %q, %t, %t)", tt.in, separator, name, literal, ok)
		}
	}
}

func TestCopyUID(t *testing.T) {
	cases := map[string]string{
		"* OK [COPYUID 1231948073 729 49753] Moved UIDs.": "49753",
		"A00001 OK [COPYUID 7 5 42] Copy completed":       "42",
		"A00001 OK [COPYUID 7 5:6 42:43] Copy completed":  "",
		"A00001 OK Copy completed":                        "",
	}
	for line, want := range cases {
		if got := copyUID([]string{line}); got != want {
			t.Errorf("copyUID(%q) = %q, want %q", line, got, want)
		}
	}
}
