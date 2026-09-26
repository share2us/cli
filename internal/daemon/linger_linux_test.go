// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EnableLinger must never block on a password prompt (agent join has no
// terminal) and must only ever name the current user. A stand-in loginctl
// records how it was called; the real machine is not touched.
func TestEnableLingerAsksForNothingAndOnlyForMe(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\n"
	if err := os.WriteFile(filepath.Join(dir, "loginctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("USER", "alice")
	if err := EnableLinger(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(log)
	got := strings.TrimSpace(string(raw))
	if got != "--no-ask-password enable-linger alice" {
		t.Fatalf("loginctl called as %q", got)
	}
}

func TestEnableLingerReportsRefusal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "loginctl"), []byte("#!/bin/sh\necho 'Interactive authentication required.' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := EnableLinger(); err == nil {
		t.Fatal("a refused enable-linger reported success")
	}
}
