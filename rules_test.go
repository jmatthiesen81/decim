package main

import (
	"os"
	"strings"
	"testing"
)

func TestWildcardPattern(t *testing.T) {
	tests := []struct {
		pattern, value string
		match          bool
	}{
		{"%@example.com", "Alice@EXAMPLE.com", true},
		{"%@example.com", "a@sub.example.com.evil", false},
		{"%@example.com", "a@sub.example.com", false}, // no subdomains
		{"*invoice*", "Your INVOICE 42", true},
		{"[project-x]*", "[project-x] update", true},
		{"[project-x]*", "project-x update", false},
		{"exact@example.com", "exact@example.com", true},
		{"exact@example.com", "xexact@example.com", false},
	}
	for _, tt := range tests {
		if got := wildcardPattern(tt.pattern).MatchString(tt.value); got != tt.match {
			t.Errorf("%q vs %q: got %t", tt.pattern, tt.value, got)
		}
	}
}

func TestParseRulesErrors(t *testing.T) {
	bad := []string{
		`{"a@b.c": "Work"}`, // old format
		`{"threshold": 0}`,
		`{"threshold": 5, "unsure_threshold": 5}`,
		`{"rules": [{"conditions": [{"field": "from", "pattern": "x", "score": 1}]}]}`,
		`{"rules": [{"folder": "X", "conditions": []}]}`,
		`{"rules": [{"folder": "X", "conditions": [{"field": "from", "pattern": "", "score": 1}]}]}`,
		`{"rules": [{"folder": "X", "conditons": []}]}`, // typo
	}
	for _, content := range bad {
		if _, err := parseRules("test", []byte(content)); err == nil {
			t.Errorf("want error for %s", content)
		}
	}
}

func TestParseRulesDefaults(t *testing.T) {
	rs, err := parseRules("test", nil)
	if err != nil || rs.threshold != defaultRuleThreshold || len(rs.rules) != 0 {
		t.Fatalf("missing file: %+v %v", rs, err)
	}

	rs, err = parseRules("test", []byte(`{"rules": [{"folder": "X", "conditions": [{"field": " Subject ", "pattern": "x", "score": 1}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if rs.rules[0].name != "rule 1" || rs.rules[0].conditions[0].field != "subject" {
		t.Errorf("unexpected rule: %+v", rs.rules[0])
	}
}

func TestBestMatch(t *testing.T) {
	rs, err := parseRules("test", []byte(`{"rules": [
		{"name": "first", "folder": "A", "conditions": [{"field": "subject", "pattern": "*x*", "score": 5}]},
		{"name": "second", "folder": "B", "conditions": [
			{"field": "subject", "pattern": "*x*", "score": 5},
			{"field": "subject", "pattern": "*reminder*", "score": -3}
		]},
		{"name": "sender", "folder": "C", "conditions": [{"field": "from", "pattern": "%@c.example", "score": 9}]},
		{"name": "trusted", "folder": "Trash", "trust_unverified_from": true, "conditions": [{"field": "from", "pattern": "%@d.example", "score": 9}]}
	]}`))
	if err != nil {
		t.Fatal(err)
	}

	best, score, _ := rs.bestMatch(map[string]string{"subject": "x"}, true)
	if best.name != "first" || score != 5 {
		t.Errorf("tie should go to the first rule, got %s %d", best.name, score)
	}

	fields := map[string]string{"subject": "none", "from": "a@c.example"}
	if best, _, ignored := rs.bestMatch(fields, false); best.name == "sender" || !ignored {
		t.Error("unverified from condition must not count")
	}
	if best, score, _ := rs.bestMatch(fields, true); best.name != "sender" || score != 9 {
		t.Errorf("verified sender: got %s %d", best.name, score)
	}

	fields = map[string]string{"subject": "none", "from": "a@d.example"}
	if best, score, ignored := rs.bestMatch(fields, false); best.name != "trusted" || score != 9 || ignored {
		t.Errorf("trust_unverified_from: got %s %d ignored=%v", best.name, score, ignored)
	}
}

func TestParseRulesMarkAsRead(t *testing.T) {
	rs, err := parseRules("test", []byte(`{"rules": [
		{"folder": "A", "mark_as_read": true, "conditions": [{"field": "subject", "pattern": "*", "score": 5}]},
		{"folder": "B", "conditions": [{"field": "subject", "pattern": "*", "score": 5}]}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !rs.rules[0].markRead || rs.rules[1].markRead {
		t.Errorf("markRead = %v, %v (want true, false by default)", rs.rules[0].markRead, rs.rules[1].markRead)
	}
}

func TestMessageFields(t *testing.T) {
	h := parseHeaders("From: \"Shop\" <Info@Shop.example>\r\nSubject: =?UTF-8?B?SWhyZSBSZWNobnVuZw==?=\r\n\r\n")
	fields := messageFields(h)
	if fields["from"] != "info@shop.example" || fields["subject"] != "Ihre Rechnung" {
		t.Errorf("fields = %v", fields)
	}
	if _, ok := messageFields(parseHeaders("Subject: x\r\n\r\n"))["from"]; ok {
		t.Error("missing From must not become a field")
	}
}

func TestExampleFilesAreValid(t *testing.T) {
	content, err := os.ReadFile("config/mapping.example.json")
	if err != nil {
		t.Skip("config/mapping.example.json not available:", err)
	}
	if _, err := parseRules("mapping.example.json", content); err != nil {
		t.Error(err)
	}
	for _, name := range []string{"whitelist", "blacklist"} {
		content, err := os.ReadFile("config/" + name + ".example.json")
		if err != nil {
			t.Fatal(err)
		}
		if list, err := parseAddressList(name, content); err != nil || len(list) == 0 {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestParseAddressList(t *testing.T) {
	list, err := parseAddressList("test", []byte(`[" Alice@Example.com ", ""]`))
	if err != nil || !list.contains("alice@example.com") || len(list) != 1 {
		t.Errorf("list = %v, err = %v", list, err)
	}
	if _, err := parseAddressList("test", []byte("alice@example.com")); err == nil || !strings.Contains(err.Error(), "JSON array") {
		t.Errorf("want JSON error, got %v", err)
	}
}

func TestSenderAddress(t *testing.T) {
	tests := map[string]string{
		`"Alice" <Alice@Example.com>`:           "alice@example.com",
		"bob@example.org":                       "bob@example.org",
		"=?koi8-r?B?8NLJ18XU?= <c@example.net>": "c@example.net",
		"undisclosed-recipients:;":              "",
		"":                                      "",
	}
	for in, want := range tests {
		if got := senderAddress(in); got != want {
			t.Errorf("senderAddress(%q) = %q, want %q", in, got, want)
		}
	}
}
