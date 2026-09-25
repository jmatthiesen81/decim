package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseHeaders(t *testing.T) {
	h := parseHeaders("From: a@example.com\r\nSubject: long\r\n folded line\r\nReceived: one\r\nReceived: two\r\n\r\nBody: ignored\r\n")

	if got := h.get("subject"); got != "long folded line" {
		t.Errorf("subject = %q", got)
	}
	if got := h["received"]; len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("received = %q", got)
	}
	if got := h.get("received"); got != "one two" {
		t.Errorf("joined received = %q", got)
	}
	if _, ok := h["body"]; ok {
		t.Error("parsed past the end of the header block")
	}
}

func TestDomainOf(t *testing.T) {
	tests := map[string]string{
		`"Name" <User@Example.COM>`: "example.com",
		"a@b.de; comment":           "b.de",
		"no address":                "",
	}
	for in, want := range tests {
		if got := domainOf(in); got != want {
			t.Errorf("domainOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAuthenticationResults(t *testing.T) {
	h := parseHeaders("Authentication-Results: mx.own.example; dmarc=fail\r\n" +
		"Authentication-Results: mx.own.example; dmarc=pass\r\n" +
		"Authentication-Results: forged.example; dmarc=pass\r\n\r\n")

	if got := authenticationResults(h, ""); !strings.Contains(got, "dmarc=fail") || !strings.Contains(got, "dmarc=pass") {
		t.Errorf("without authserv-id all headers should count, got %q", got)
	}
	if got := authenticationResults(h, "MX.own.example"); got != "mx.own.example; dmarc=fail" {
		t.Errorf("topmost own header expected, got %q", got)
	}

	forged := parseHeaders("Authentication-Results: forged.example; dmarc=pass\r\n\r\n")
	if got := authenticationResults(forged, "mx.own.example"); got != "" {
		t.Errorf("forged header must be ignored, got %q", got)
	}

	versioned := parseHeaders("Authentication-Results: mx.own.example 1; dmarc=pass\r\n\r\n")
	if !dmarcPassed(authenticationResults(versioned, "mx.own.example")) {
		t.Error("authserv-id with version number not recognized")
	}
}

func TestSpamScore(t *testing.T) {
	tests := []struct {
		raw     string
		score   int
		reasons string
	}{
		{"From: a@x.de\r\nAuthentication-Results: mx; dmarc=fail; spf=fail; dkim=fail\r\n", 7, "DMARC_FAIL,SPF_FAIL,DKIM_FAIL"},
		{"From: a@x.de\r\nReply-To: b@y.de\r\nX-Spam-Flag: YES\r\n", 6, "REPLY_TO_DIFFERS,UPSTREAM_SPAM_FLAG"},
		{"From: a@x.de\r\nX-Spam-Flag: YES\r\nAuthentication-Results: mx; dmarc=pass\r\n", 3, "UPSTREAM_SPAM_FLAG"},
		{"From: a@x.de\r\nX-Spam-Status: Yes, score=9\r\n", 5, "UPSTREAM_SPAM_STATUS"},
	}
	for _, tt := range tests {
		h := parseHeaders(tt.raw)
		score, reasons := spamScore(h, authenticationResults(h, ""))
		if score != tt.score || strings.Join(reasons, ",") != tt.reasons {
			t.Errorf("%q: got %d %v, want %d %s", tt.raw, score, reasons, tt.score, tt.reasons)
		}
	}
}

func testConfig(t *testing.T) config {
	t.Helper()
	rules, err := parseRules("test", []byte(`{
		"threshold": 5,
		"unsure_threshold": 3,
		"rules": [
			{"name": "Invoices", "folder": "Finance", "conditions": [
				{"field": "from", "pattern": "%@shop.example", "score": 3},
				{"field": "subject", "pattern": "*invoice*", "score": 3}
			]}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	return config{
		sourceFolder: "INBOX", spamFolder: "Junk", cleanFolder: "Clean", unsureFolder: "Unsure",
		spamThreshold: 5, requireDMARC: true, rules: rules,
		whitelist: testAddressList(t, "friend@example.com", "both@example.com"),
		blacklist: testAddressList(t, "bad@spam.example", "both@example.com", "%@blocked.example"),
	}
}

func testAddressList(t *testing.T, addresses ...string) addressList {
	t.Helper()
	content, err := json.Marshal(addresses)
	if err != nil {
		t.Fatal(err)
	}
	list, err := parseAddressList("test", content)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestClassify(t *testing.T) {
	const pass = "Authentication-Results: mx; dmarc=pass\r\n"
	const flagged = "X-Spam-Flag: YES\r\nX-Spam-Status: Yes\r\n"

	tests := []struct {
		name, raw, folder, reason string
		markSpam                  bool
	}{
		{"whitelisted skips spam check", "From: friend@example.com\r\n" + pass + flagged, "Clean", "WHITELISTED", false},
		{"unverified whitelist falls through", "From: friend@example.com\r\n" + flagged, "Junk", "WHITELIST_UNVERIFIED", true},
		{"whitelist without DMARC reaches blacklist", "From: both@example.com\r\n", "Junk", "WHITELIST_UNVERIFIED,BLACKLISTED", true},
		{"blacklisted", "From: bad@spam.example\r\n" + pass, "Junk", "BLACKLISTED", true},
		{"blacklisted by wildcard", "From: Anyone@Blocked.example\r\n" + pass, "Junk", "BLACKLISTED", true},
		{"wildcard skips subdomains", "From: a@sub.blocked.example\r\n" + pass, "Clean", "", false},
		{"rule match", "From: a@shop.example\r\nSubject: Your invoice\r\n" + pass, "Finance", `RULE:"Invoices"=6`, false},
		{"rule never rescues spam", "From: a@shop.example\r\nSubject: invoice\r\n" + pass + flagged, "Junk", "UPSTREAM_SPAM_FLAG", true},
		{"partial rule is unsure", "From: a@shop.example\r\nSubject: Hello\r\n" + pass, "Unsure", `RULE_UNSURE:"Invoices"=3`, false},
		{"unverified sender does not score", "From: a@shop.example\r\nSubject: invoice\r\n", "Unsure", "RULE_SENDER_UNVERIFIED", false},
		{"unverified sender below unsure zone is unsure", "From: a@shop.example\r\nSubject: hi\r\n", "Unsure", "RULE_SENDER_UNVERIFIED", false},
		{"no rule matches", "From: x@y.example\r\nSubject: hi\r\n", "Clean", "", false},
	}
	cfg := testConfig(t)
	for _, tt := range tests {
		v := classify(cfg, parseHeaders(tt.raw))
		if v.folder != tt.folder || !strings.Contains(strings.Join(v.reasons, ","), tt.reason) || v.markSpam != tt.markSpam {
			t.Errorf("%s: got %s %v mark_spam=%t", tt.name, v.folder, v.reasons, v.markSpam)
		}
	}
}

func TestClassifyUnverifiedSender(t *testing.T) {
	cfg := testConfig(t)
	cfg.rules.unsureThreshold = 0
	raw := "From: a@shop.example\r\nSubject: hi\r\n"
	if v := classify(cfg, parseHeaders(raw)); v.folder != "Unsure" || v.label != "unsure" {
		t.Errorf("got %s %s %v", v.folder, v.label, v.reasons)
	}

	cfg.unsureFolder = ""
	if v := classify(cfg, parseHeaders(raw)); v.folder != "Clean" {
		t.Errorf("without UNSURE_FOLDER: got %s %v", v.folder, v.reasons)
	}

	cfg = testConfig(t)
	cfg.rules.threshold = 3
	raw = "From: a@shop.example\r\nSubject: invoice\r\n"
	if v := classify(cfg, parseHeaders(raw)); v.folder != "Finance" {
		t.Errorf("rule reaching threshold without from: got %s %v", v.folder, v.reasons)
	}
}

func TestClassifySpamUnsureZone(t *testing.T) {
	cfg := testConfig(t)
	cfg.spamUnsureThreshold = 3
	v := classify(cfg, parseHeaders("From: x@y.example\r\nAuthentication-Results: mx; spf=fail; dkim=fail\r\n"))
	if v.folder != "Unsure" || v.label != "unsure" || v.markSpam {
		t.Errorf("got %s %s mark_spam=%t", v.folder, v.label, v.markSpam)
	}
}

func TestClassifyWithAuthservID(t *testing.T) {
	cfg := testConfig(t)
	cfg.authservID = "mx.own.example"
	raw := "From: friend@example.com\r\nAuthentication-Results: forged.example; dmarc=pass\r\n" +
		"Authentication-Results: mx.own.example; dmarc=none\r\nX-Spam-Flag: YES\r\n"
	if v := classify(cfg, parseHeaders(raw)); v.folder != "Junk" {
		t.Errorf("forged dmarc=pass opened the whitelist: %s %v", v.folder, v.reasons)
	}
}

func TestVerdictScores(t *testing.T) {
	cfg := testConfig(t)
	whitelisted := classify(cfg, parseHeaders("From: friend@example.com\r\nAuthentication-Results: mx; dmarc=pass\r\n"))
	if whitelisted.spamScore != nil {
		t.Error("spam score must be empty when the spam check is skipped")
	}
	clean := classify(cfg, parseHeaders("From: x@y.example\r\nSubject: hi\r\n"))
	if clean.spamScore == nil || clean.ruleScore != nil || clean.rule != "" {
		t.Errorf("unexpected scores: %+v", clean)
	}
}

// A header from a server that checks DKIM but not DMARC (OpenDKIM only).
const dkimOnlyHeader = "Authentication-Results: mail.netinventors.de;\r\n" +
	"    dkim=pass (2048-bit key; unprotected) header.d=6506294t.shopware.com header.i=@6506294t.shopware.com header.b=\"hJQ0fY8R\";\r\n" +
	"    dkim=pass (2048-bit key; unprotected) header.d=shopware.com header.i=@shopware.com header.b=\"K5xGDCaB\";\r\n" +
	"    dkim-atps=neutral\r\n"

func TestSenderVerified(t *testing.T) {
	tests := []struct {
		name, header, sender string
		want                 bool
	}{
		{"dkim pass for the From domain", dkimOnlyHeader, "no-reply@shopware.com", true},
		{"dkim pass only for another domain", dkimOnlyHeader, "no-reply@example.com", false},
		{"subdomain signature does not count for parent", "Authentication-Results: mx; dkim=pass header.d=mail.shopware.com\r\n", "a@shopware.com", false},
		{"dkim fail", "Authentication-Results: mx; dkim=fail header.d=shopware.com\r\n", "a@shopware.com", false},
		{"dmarc result decides over dkim", "Authentication-Results: mx; dkim=pass header.d=shopware.com; dmarc=fail\r\n", "a@shopware.com", false},
		{"dmarc pass", "Authentication-Results: mx; dmarc=pass header.from=example.com\r\n", "a@example.com", true},
		{"no sender", dkimOnlyHeader, "", false},
		{"no results", "Subject: x\r\n", "a@shopware.com", false},
	}
	for _, tt := range tests {
		h := parseHeaders(tt.header + "\r\n")
		if got := senderVerified(authenticationResults(h, ""), tt.sender); got != tt.want {
			t.Errorf("%s: got %t", tt.name, got)
		}
	}
}

func TestClassifyWithDKIMOnlyServer(t *testing.T) {
	cfg := testConfig(t)
	cfg.authservID = "mail.netinventors.de"
	cfg.whitelist = testAddressList(t, "no-reply@shopware.com")

	v := classify(cfg, parseHeaders("From: shopware AG <no-reply@shopware.com>\r\n"+dkimOnlyHeader+"X-Spam-Flag: YES\r\nX-Spam-Status: Yes\r\n\r\n"))
	if v.folder != "Clean" || v.reasons[0] != "WHITELISTED" {
		t.Errorf("DKIM-verified whitelisted sender: got %s %v", v.folder, v.reasons)
	}

	forged := "From: no-reply@shopware.com\r\nAuthentication-Results: mail.netinventors.de; dkim=pass header.d=spammer.example\r\n" +
		"Authentication-Results: mail.netinventors.de; dkim=pass header.d=shopware.com\r\nX-Spam-Flag: YES\r\nX-Spam-Status: Yes\r\n\r\n"
	if v := classify(cfg, parseHeaders(forged)); v.folder != "Junk" {
		t.Errorf("only the topmost own header may count: got %s %v", v.folder, v.reasons)
	}
}
