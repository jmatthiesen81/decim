package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/mail"
	"os"
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

// addressList is a set of lower-cased e-mail addresses.
type addressList map[string]struct{}

func (l addressList) contains(address string) bool {
	_, ok := l[address]
	return ok
}

// parseAddressList parses a JSON array of e-mail addresses, e.g.
// ["alice@example.com", "bob@example.org"]. Nil content yields an empty list.
func parseAddressList(path string, content []byte) (addressList, error) {
	list := addressList{}
	if content == nil {
		return list, nil
	}

	var addresses []string
	if err := json.Unmarshal(content, &addresses); err != nil {
		return nil, fmt.Errorf("%s: expected a JSON array of e-mail addresses: %w", path, err)
	}
	for _, address := range addresses {
		if address = strings.TrimSpace(address); address != "" {
			list[strings.ToLower(address)] = struct{}{}
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
