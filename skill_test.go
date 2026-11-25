// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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
	if got, _ := os.ReadFile(path); !bytes.Equal(got, skillContent(skillTargets[0])) || !bytes.Contains(got, []byte("name: s2u")) {
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

func TestSkillPerToolFrontmatter(t *testing.T) {
	claude, codex := string(skillContent(skillTargets[0])), string(skillContent(skillTargets[1]))
	if !strings.Contains(claude, "argument-hint:") || strings.Contains(codex, "argument-hint:") {
		t.Fatal("argument-hint must be Claude-only (Codex/Gemini validate frontmatter)")
	}
	for _, c := range []string{claude, codex} {
		if !strings.HasPrefix(c, "---\nname: s2u\ndescription: ") || strings.Count(strings.SplitN(c, "\n---\n", 2)[0], "\n") > 3 {
			t.Fatalf("frontmatter: %q", c[:120])
		}
	}
	if len(skillDescription) > 300 || strings.Contains(skillDescription, "\n") {
		t.Fatal("Codex rejects long or multi-line descriptions")
	}
}

func TestAgentSkillInstallsForNamedTools(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	var out, errOut bytes.Buffer
	a := app{stdout: &out, stderr: &errOut}
	if code := a.agentSkill([]string{"install", "codex", "gemini"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	for _, d := range []string{".codex", ".gemini"} {
		if _, err := os.Stat(filepath.Join(home, d, "skills", "s2u", "SKILL.md")); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "s2u", "SKILL.md")); err == nil {
		t.Fatal("installed for a tool that wasn't named")
	}
}
