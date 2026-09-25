package main

import "testing"

func TestEncodeMailbox(t *testing.T) {
	tests := map[string]string{
		"INBOX":                 "INBOX",
		"geschäftlich":          "gesch&AOQ-ftlich",
		"gesch&AOQ-ftlich":      "gesch&AOQ-ftlich", // already encoded
		"Größe/Übersicht":       "Gr&APYA3w-e/&ANw-bersicht",
		"Q&A":                   "Q&-A",
		"&-":                    "&-",
		"日本語":                   "&ZeVnLIqe-",
		"Finance/Invoices 2026": "Finance/Invoices 2026",
	}
	for in, want := range tests {
		if got := encodeMailbox(in); got != want {
			t.Errorf("encodeMailbox(%q) = %q, want %q", in, got, want)
		}
	}
}
