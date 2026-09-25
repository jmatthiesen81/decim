package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime"
	"regexp"
	"strings"
)

const defaultRuleThreshold = 5

// ruleset routes clean mail into folders. Each rule adds up the scores of
// its matching conditions; the best rule wins if it reaches threshold.
// Scores from unsureThreshold up to threshold mean "not sure".
type ruleset struct {
	threshold       int
	unsureThreshold int // 0 disables the unsure zone
	rules           []rule
}

type rule struct {
	name       string
	folder     string
	conditions []condition
	// trustUnverifiedFrom lets "from" conditions count for unverified
	// senders, for rules whose folder is not more trusted (e.g. Trash).
	trustUnverifiedFrom bool
	markRead            bool // set \Seen on mail moved by this rule
	markSpam            bool // set the spam keywords on mail moved by this rule
}

type condition struct {
	field   string // lower-cased header name; "from" is the bare sender address
	pattern *regexp.Regexp
	score   int
}

// rulesFile is the JSON layout of mapping.json.
type rulesFile struct {
	Threshold       *int `json:"threshold"`
	UnsureThreshold *int `json:"unsure_threshold"`
	Rules           []struct {
		Name                string `json:"name"`
		Folder              string `json:"folder"`
		TrustUnverifiedFrom bool   `json:"trust_unverified_from"`
		MarkAsRead          bool   `json:"mark_as_read"`
		MarkAsSpam          bool   `json:"mark_as_spam"`
		Conditions          []struct {
			Field   string `json:"field"`
			Pattern string `json:"pattern"`
			Score   int    `json:"score"`
		} `json:"conditions"`
	} `json:"rules"`
}

// parseRules parses the rules file. Nil content yields an empty ruleset.
func parseRules(path string, content []byte) (ruleset, error) {
	rs := ruleset{threshold: defaultRuleThreshold}
	if content == nil {
		return rs, nil
	}

	var file rulesFile
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields() // catch typos and the old address->folder format
	if err := decoder.Decode(&file); err != nil {
		return ruleset{}, fmt.Errorf("%s: invalid rules file (see mapping.example.json): %w", path, err)
	}

	if file.Threshold != nil {
		rs.threshold = *file.Threshold
	}
	if rs.threshold <= 0 {
		return ruleset{}, fmt.Errorf("%s: threshold must be > 0", path)
	}
	if file.UnsureThreshold != nil {
		rs.unsureThreshold = *file.UnsureThreshold
		if rs.unsureThreshold <= 0 || rs.unsureThreshold >= rs.threshold {
			return ruleset{}, fmt.Errorf("%s: unsure_threshold must be between 1 and threshold-1", path)
		}
	}

	for i, fileRule := range file.Rules {
		r := rule{name: fileRule.Name, folder: fileRule.Folder, trustUnverifiedFrom: fileRule.TrustUnverifiedFrom, markRead: fileRule.MarkAsRead, markSpam: fileRule.MarkAsSpam}
		if r.name == "" {
			r.name = fmt.Sprintf("rule %d", i+1)
		}
		if r.folder == "" {
			return ruleset{}, fmt.Errorf("%s: %s has no folder", path, r.name)
		}
		if len(fileRule.Conditions) == 0 {
			return ruleset{}, fmt.Errorf("%s: %s has no conditions", path, r.name)
		}

		for _, fileCondition := range fileRule.Conditions {
			field := strings.ToLower(strings.TrimSpace(fileCondition.Field))
			if field == "" || fileCondition.Pattern == "" {
				return ruleset{}, fmt.Errorf("%s: %s has a condition without field or pattern", path, r.name)
			}
			r.conditions = append(r.conditions, condition{
				field:   field,
				pattern: wildcardPattern(fileCondition.Pattern),
				score:   fileCondition.Score,
			})
		}
		rs.rules = append(rs.rules, r)
	}
	return rs, nil
}

// wildcardPattern compiles a pattern in which "*" and "%" match any
// characters into a case-insensitive regexp that must match the whole value.
func wildcardPattern(pattern string) *regexp.Regexp {
	parts := strings.Split(strings.ReplaceAll(pattern, "%", "*"), "*")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	return regexp.MustCompile(`(?is)^` + strings.Join(parts, ".*") + `$`)
}

// bestMatch scores every rule against the message fields and returns the
// highest-scoring rule (the first one on ties), or nil if no rule has a
// matching condition that counts.
// Unless senderVerified or the rule sets trustUnverifiedFrom, "from"
// conditions do not count, since From can be forged; ignoredSender reports
// whether that dropped a matching condition.
func (rs ruleset) bestMatch(fields map[string]string, senderVerified bool) (best *rule, bestScore int, ignoredSender bool) {
	for i := range rs.rules {
		r := &rs.rules[i]

		score, matched := 0, false
		for _, c := range r.conditions {
			value, ok := fields[c.field]
			if !ok || !c.pattern.MatchString(value) {
				continue
			}
			if c.field == "from" && !senderVerified && !r.trustUnverifiedFrom {
				ignoredSender = true
				continue
			}
			score += c.score
			matched = true
		}

		if matched && (best == nil || score > bestScore) {
			best, bestScore = r, score
		}
	}
	return best, bestScore, ignoredSender
}

// messageFields prepares header values for rule matching: repeated fields
// are joined, RFC 2047 encoded words (e.g. in Subject) are decoded and
// "from" holds the bare address.
func messageFields(h headers) map[string]string {
	decoder := mime.WordDecoder{}
	fields := make(map[string]string, len(h))
	for name := range h {
		value := h.get(name)
		if decoded, err := decoder.DecodeHeader(value); err == nil {
			value = decoded
		}
		fields[name] = value
	}

	if sender := senderAddress(h.get("from")); sender != "" {
		fields["from"] = sender
	} else {
		delete(fields, "from")
	}
	return fields
}
