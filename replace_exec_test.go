package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// replaceExecutable must atomically swap in the new binary (preserving the exec
// bit) and leave no predictable staging file behind — the O_EXCL/random-name
// staging is what prevents a symlink pre-plant from redirecting the write.
func TestReplaceExecutable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "s2u")
	source := filepath.Join(dir, "s2u.new")

	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new-binary"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := replaceExecutable(target, source); err != nil {
		t.Fatalf("replaceExecutable() error = %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-binary" {
		t.Fatalf("target content = %q, want %q", got, "new-binary")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("target is not executable: mode %v", info.Mode())
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".s2u.new") {
			t.Errorf("leftover staging file: %s", e.Name())
		}
	}
}
