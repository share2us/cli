package main

import "testing"

func TestSafeLocalName(t *testing.T) {
	for in, want := range map[string]string{
		"../../etc/passwd":   "passwd",
		"a\\b\\evil.txt":     "evil.txt",
		"report\x1b[31m.pdf": "report[31m.pdf",
		"file.txt:stream":    "file.txt_stream",
		"CON.txt":            "_CON.txt",
		"..":                 "sharenet-file",
		"notes. ":            "notes",
		"plan.pdf":           "plan.pdf",
	} {
		if got := safeLocalName(in); got != want {
			t.Errorf("safeLocalName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSharenetPostRefusesLinkOptionAliases(t *testing.T) {
	for _, args := range [][]string{
		{"f.txt", "--sharenet", "n", "-d", "laptop"},
		{"f.txt", "--project", "p", "--teammate", "x@example.test"},
		{"f.txt", "--sharenet", "n", "--keep-updated"},
	} {
		if _, err := parseUploadArgs(args); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
}
