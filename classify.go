package main

import "strings"

// headers maps lower-cased field names to their values, in the order they
// appear in the message (topmost first).
type headers map[string][]string

// get returns all values of a field joined with a space, or "" if missing.
func (h headers) get(name string) string {
	return strings.Join(h[name], " ")
}

// parseHeaders parses a raw RFC 5322 header block. Folded lines are unfolded;
// repeated fields are kept as separate values.
func parseHeaders(raw string) headers {
	h := headers{}

	var key string
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if line == "" {
			break // end of header block
		}

		isContinuation := strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
		if isContinuation && key != "" {
			values := h[key]
			values[len(values)-1] += " " + strings.TrimSpace(line)
			continue
		}

		colon := strings.IndexByte(line, ':')
		if colon < 1 {
			continue
		}
		key = strings.ToLower(line[:colon])
		h[key] = append(h[key], strings.TrimSpace(line[colon+1:]))
	}
	return h
}

// domainOf extracts the lower-cased domain from an address header value
// such as `"Name" <user@example.com>`. It returns "" if there is no "@".
func domainOf(address string) string {
	at := strings.LastIndex(address, "@")
	if at < 0 {
		return ""
	}

	domain := strings.TrimSpace(address[at+1:])
	if end := strings.IndexAny(domain, "> ;"); end >= 0 {
		domain = domain[:end]
	}
	return strings.ToLower(strings.Trim(domain, `<>"`))
}

// authenticationResults returns the lower-cased Authentication-Results that
// should be trusted. With an authservID, only the topmost header added by
// that server counts; others may have been forged by the sender. Without
// one, all headers are used.
func authenticationResults(h headers, authservID string) string {
	values := h["authentication-results"]
	if authservID == "" {
		return strings.ToLower(strings.Join(values, " "))
	}

	for _, value := range values {
		id, _, _ := strings.Cut(value, ";")
		if fields := strings.Fields(id); len(fields) > 0 && strings.EqualFold(fields[0], authservID) {
			return strings.ToLower(value)
		}
	}
	return ""
}

// dmarcPassed reports whether the trusted Authentication-Results contain
// dmarc=pass, meaning SPF or DKIM succeeded for the domain shown in From.
func dmarcPassed(auth string) bool {
	return strings.Contains(auth, "dmarc=pass")
}

// senderVerified reports whether the trusted, lower-cased
// Authentication-Results confirm the sender's domain. A DMARC result decides
// if there is one. Servers without DMARC checking only report DKIM; then a
// dkim=pass whose signing domain (header.d) equals the From domain counts,
// which is DMARC's DKIM check with strict alignment.
func senderVerified(auth, sender string) bool {
	if strings.Contains(auth, "dmarc=") {
		return dmarcPassed(auth)
	}

	_, fromDomain, ok := strings.Cut(sender, "@")
	if !ok || fromDomain == "" {
		return false
	}
	for _, result := range strings.Split(withoutComments(auth), ";") {
		fields := strings.Fields(result)
		if len(fields) == 0 || fields[0] != "dkim=pass" {
			continue
		}
		for _, field := range fields[1:] {
			if domain, ok := strings.CutPrefix(field, "header.d="); ok && strings.Trim(domain, `"`) == fromDomain {
				return true
			}
		}
	}
	return false
}

// withoutComments removes parenthesized comments, which may contain ";"
// (e.g. "dkim=pass (2048-bit key; unprotected) header.d=example.com").
func withoutComments(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '(':
			depth++
		case r == ')' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// spamScore rates a message by its headers and the trusted, lower-cased
// Authentication-Results. A higher score means more likely spam; reasons
// lists the signals that added points.
func spamScore(h headers, auth string) (score int, reasons []string) {
	add := func(points int, reason string) {
		score += points
		reasons = append(reasons, reason)
	}

	if strings.Contains(auth, "dmarc=fail") {
		add(4, "DMARC_FAIL")
	}
	if strings.Contains(auth, "spf=fail") {
		add(2, "SPF_FAIL")
	}
	if strings.Contains(auth, "dkim=fail") {
		add(1, "DKIM_FAIL")
	}

	from := domainOf(h.get("from"))
	replyTo := domainOf(h.get("reply-to"))
	if from != "" && replyTo != "" && from != replyTo {
		add(1, "REPLY_TO_DIFFERS")
	}

	if strings.Contains(strings.ToLower(h.get("x-spam-flag")), "yes") {
		add(5, "UPSTREAM_SPAM_FLAG")
	}
	if strings.Contains(strings.ToLower(h.get("x-spam-status")), "yes") {
		add(5, "UPSTREAM_SPAM_STATUS")
	}

	if dmarcPassed(auth) {
		score -= 2
	}
	return score, reasons
}
