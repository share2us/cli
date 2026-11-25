// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallClaudeSkillKeepsHandEdits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	path, err := installClaudeSkill()
	if err != nil || path != filepath.Join(home, ".claude", "skills", "s2u", "SKILL.md") {
		t.Fatalf("install: %s %v", path, err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, s2uSkill) || !bytes.Contains(got, []byte("name: s2u")) {
		t.Fatal("skill not written")
	}
	_ = os.WriteFile(path, []byte("my own skill"), 0o644)
	if _, err := installClaudeSkill(); err == nil {
		t.Fatal("overwrote a hand-edited skill without saying so")
	}
	if got, _ := os.ReadFile(path); string(got) != "my own skill" {
		t.Fatal("hand-edited skill replaced")
	}
}
