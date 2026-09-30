// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"os"
	"strings"
	"testing"
)

func TestReleaseRequiresExplicitDispatchAndTagsItsBuild(t *testing.T) {
	raw, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(raw), "\r\n", "\n")
	start := strings.Index(s, "\non:\n")
	end := strings.Index(s, "\npermissions:")
	if start < 0 || end <= start {
		t.Fatal("release workflow trigger block not found")
	}
	triggers := s[start:end]
	if !strings.Contains(triggers, "\n  workflow_dispatch:") || strings.Contains(triggers, "\n  push:") {
		t.Fatal("CLI release must be dispatched, never triggered by a merge")
	}
	if !strings.Contains(s, "gh release create") || !strings.Contains(s, `--target "$GITHUB_SHA"`) {
		t.Fatal("release tag must point to the build's exact commit")
	}
}
