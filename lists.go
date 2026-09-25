package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/mail"
	"os"
	"regexp"
	"strings"
)

// readConfigFile returns the file's content, or nil if it does not exist.
func readConfigFile(path string) ([]byte, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return content, err
}

// addressList holds lower-cased e-mail addresses and wildcard patterns.
type addressList struct {
	exact    map[string]struct{}
	patterns []*regexp.Regexp
}

func (l addressList) contains(address string) bool {
	if address == "" {
		return false
	}
	if _, ok := l.exact[address]; ok {
		return true
	}
	for _, pattern := range l.patterns {
		if pattern.MatchString(address) {
			return true
		}
	}
	return false
}

func (l addressList) len() int {
	return len(l.exact) + len(l.patterns)
}

// parseAddressList parses a JSON array of e-mail addresses, e.g.
// ["alice@example.com", "%@example.org"]. Entries containing "*" or "%" are
// wildcard patterns like in rule conditions. Nil content yields an empty list.
func parseAddressList(path string, content []byte) (addressList, error) {
	list := addressList{exact: map[string]struct{}{}}
	if content == nil {
		return list, nil
	}

	var addresses []string
	if err := json.Unmarshal(content, &addresses); err != nil {
		return addressList{}, fmt.Errorf("%s: expected a JSON array of e-mail addresses: %w", path, err)
	}
	for _, address := range addresses {
		address = strings.ToLower(strings.TrimSpace(address))
		switch {
		case address == "":
		case strings.ContainsAny(address, "*%"):
			list.patterns = append(list.patterns, wildcardPattern(address))
		default:
			list.exact[address] = struct{}{}
		}
	}
	return list, nil
}

// senderAddress extracts the lower-cased bare address from a From header
// value such as `"Name" <user@example.com>`. It returns "" if none is found.
func senderAddress(from string) string {
	from = strings.TrimSpace(from)
	if addr, err := mail.ParseAddress(from); err == nil {
		return strings.ToLower(addr.Address)
	}

	// Fallback for headers net/mail rejects, e.g. unknown charsets in the name.
	if open := strings.LastIndex(from, "<"); open >= 0 {
		if end := strings.Index(from[open:], ">"); end > 0 {
			from = from[open+1 : open+end]
		}
	}
	if !strings.Contains(from, "@") || strings.ContainsAny(from, " \t") {
		return ""
	}
	return strings.ToLower(from)
}
