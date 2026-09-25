package main

import (
	"encoding/base64"
	"strings"
	"unicode/utf16"
)

// encodeMailbox converts a folder name to IMAP modified UTF-7 (RFC 3501,
// section 5.1.3), e.g. "geschäftlich" -> "gesch&AOQ-ftlich". Names that are
// already valid modified UTF-7 are returned unchanged, so both forms work.
func encodeMailbox(name string) string {
	if isModifiedUTF7(name) {
		return name
	}

	var b strings.Builder
	var pending []rune // non-ASCII run waiting to be encoded
	flush := func() {
		if len(pending) == 0 {
			return
		}
		units := utf16.Encode(pending)
		raw := make([]byte, 0, 2*len(units))
		for _, u := range units {
			raw = append(raw, byte(u>>8), byte(u))
		}
		encoded := base64.RawStdEncoding.EncodeToString(raw)
		b.WriteString("&" + strings.ReplaceAll(encoded, "/", ",") + "-")
		pending = nil
	}

	for _, r := range name {
		switch {
		case r == '&':
			flush()
			b.WriteString("&-")
		case r >= 0x20 && r <= 0x7e:
			flush()
			b.WriteRune(r)
		default:
			pending = append(pending, r)
		}
	}
	flush()
	return b.String()
}

// isModifiedUTF7 reports whether name is printable ASCII in which every "&"
// starts a well-formed "&...-" sequence.
func isModifiedUTF7(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < 0x20 || c > 0x7e {
			return false
		}
		if c != '&' {
			continue
		}

		end := strings.IndexByte(name[i+1:], '-')
		if end < 0 {
			return false
		}
		for _, s := range name[i+1 : i+1+end] {
			isBase64 := s >= 'A' && s <= 'Z' || s >= 'a' && s <= 'z' || s >= '0' && s <= '9' || s == '+' || s == ','
			if !isBase64 {
				return false
			}
		}
		i += end + 1
	}
	return true
}
