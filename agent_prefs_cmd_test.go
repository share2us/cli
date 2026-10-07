// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"strings"
	"testing"

	clicore "github.com/share2us/cli-core"
)

func TestSessionLineAliasAndTags(t *testing.T) {
	s := clicore.AgentSessionInfo{
		AgentID: "agt_abcdefghijklmnop", Tool: "claude", Status: "online",
		Name: "claude-1", DeviceName: "laptop", DeviceID: "dev1", SessionID: "sess1",
	}

	// Alias replaces the server name; the id stays the first field (pasteable);
	// pinned/hidden render as trailing tags, not disturbing the --device/--session.
	line := sessionLine(s, "Reviewer", clicore.AgentPrefs{Alias: "Reviewer", Pinned: true, Hidden: true})
	if strings.Fields(line)[0] != s.AgentID {
		t.Errorf("agent id must be first field: %q", line)
	}
	if !strings.Contains(line, "Reviewer") || strings.Contains(line, "claude-1") {
		t.Errorf("alias should replace the server name: %q", line)
	}
	if !strings.Contains(line, "[pinned,hidden]") {
		t.Errorf("want pinned,hidden tags: %q", line)
	}
	if !strings.Contains(line, "--device dev1") || !strings.Contains(line, "--session sess1") {
		t.Errorf("flags must survive: %q", line)
	}

	// No prefs: no trailing tag bracket, server name shown.
	plain := sessionLine(s, s.Name, clicore.AgentPrefs{})
	if strings.Contains(plain, "[") {
		t.Errorf("no tags expected without prefs: %q", plain)
	}
	if !strings.Contains(plain, "claude-1") {
		t.Errorf("server name expected: %q", plain)
	}
}
