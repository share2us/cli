// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/share2us/cli/internal/daemon"
)

func runCodexHook(t *testing.T, project string, in hookInput, f *fakeApprovalDaemon) string {
	t.Helper()
	t.Setenv(daemon.CodexGuardProjectEnv, project)
	t.Setenv(daemon.CodexGuardSessionEnv, "codex-thread")
	t.Setenv(daemon.CodexGuardStrictEnv, "0")
	var out bytes.Buffer
	(app{stdout: &out}).codexPreToolUse(in, f.call, func(time.Duration) {})
	return out.String()
}

func TestCodexHook(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	bash := func(cmd string) hookInput {
		return hookInput{SessionID: "x", ToolName: "Bash", ToolInput: map[string]any{"command": cmd}}
	}
	patch := func(body string) hookInput {
		return hookInput{SessionID: "x", ToolName: "apply_patch", ToolInput: map[string]any{"command": body}}
	}
	denied := func(s string) bool { return strings.Contains(s, `"permissionDecision":"deny"`) }

	f := &fakeApprovalDaemon{}
	if out := runCodexHook(t, project, bash("ls"), f); out != "" || len(f.calls) != 0 {
		t.Fatalf("a plain read was not just allowed: %q %v", out, f.calls)
	}
	if out := runCodexHook(t, project, bash("ip -4 addr"), &fakeApprovalDaemon{decision: "allow"}); denied(out) {
		t.Fatalf("a granted command was refused: %q", out)
	}
	out := runCodexHook(t, project, bash("ip -4 addr"), &fakeApprovalDaemon{decision: "pending", statuses: []string{"approved"}})
	if denied(out) || !strings.Contains(out, "systemMessage") {
		t.Fatalf("an approval during the hold: %q", out)
	}
	if out := runCodexHook(t, project, bash("ip -4 addr"), &fakeApprovalDaemon{decision: "pending", statuses: []string{"denied"}}); !denied(out) || !strings.Contains(out, "declined") {
		t.Fatalf("a declined command: %q", out)
	}

	f = &fakeApprovalDaemon{decision: "allow"}
	selfEdit := patch("*** Begin Patch\n*** Add File: .codex/hooks.json\n+{}\n*** End Patch")
	if out := runCodexHook(t, project, selfEdit, f); !denied(out) || len(f.calls) != 0 {
		t.Fatalf("a .codex edit was askable or allowed: %q %v", out, f.calls)
	}
	if out := runCodexHook(t, project, patch("*** Begin Patch\n*** Update File: main.go\n*** End Patch"), &fakeApprovalDaemon{}); denied(out) {
		t.Fatalf("an edit inside the project was refused: %q", out)
	}
	f = &fakeApprovalDaemon{decision: "pending", statuses: []string{"approved"}}
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	if out := runCodexHook(t, project, patch("*** Begin Patch\n*** Update File: main.go\n*** Delete File: "+outside+"\n*** End Patch"), f); denied(out) || f.calls[0] != "approval-check" {
		t.Fatalf("an outside edit wasn't asked for: %q %v", out, f.calls)
	}
	if out := runCodexHook(t, project, patch("no headers"), &fakeApprovalDaemon{decision: "allow"}); !denied(out) {
		t.Fatalf("a patch naming no file was allowed: %q", out)
	}
	if out := runCodexHook(t, "", bash("ls"), &fakeApprovalDaemon{decision: "allow"}); !denied(out) {
		t.Fatalf("no trusted project, still allowed: %q", out)
	}
}
