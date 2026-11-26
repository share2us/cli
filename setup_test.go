// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupWithoutLoginAndNoTerminalSaysSignInFirst(t *testing.T) {
	t.Setenv("SHARE2US_API_TOKEN", "")
	var out bytes.Buffer
	a := app{stdout: &out, stderr: &out, stdin: strings.NewReader(""), stdinIsTTY: func(io.Reader) bool { return false }}
	if code := a.setup(context.Background(), nil); code != 1 || !strings.Contains(out.String(), "Sign in first") {
		t.Fatalf("exit %d: %s", code, out.String())
	}
}

func TestSetupRulesKeepsTheOldBehaviour(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	var out bytes.Buffer
	a := app{stdout: &out, stderr: &out}
	if code := a.setup(context.Background(), []string{"--rules"}); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".s2u.rules")); err != nil {
		t.Fatalf("rules file: %v", err)
	}
	if code := a.setup(context.Background(), []string{"--bogus"}); code != 2 {
		t.Fatal("unknown flag accepted")
	}
}
