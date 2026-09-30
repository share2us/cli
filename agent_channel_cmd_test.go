// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/share2us/cli/internal/daemon"
)

func TestActiveTypedHopFailsClosedWithoutDaemon(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := daemon.BeginTypedGuard("session-1", "request-1"); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	defer func() { os.Stdin = previous }()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	os.Stdin = reader
	if _, err := writer.WriteString(`{"session_id":"session-1","cwd":"/tmp","tool_name":"Bash","tool_input":{"command":"echo unsafe"}}`); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	var out bytes.Buffer
	if code := (app{stdout: &out}).agentHook([]string{"pre-tool-use"}); code != 0 {
		t.Fatalf("hook exit = %d", code)
	}
	if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("daemon-unreachable active hop was allowed: %q", out.String())
	}
}

func TestInterruptedTypedTurnDoesNotLockLaterOwnerToolsWithoutDaemon(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := daemon.BeginTypedGuard("session-1", "request-1"); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"user","message":{"role":"user","content":"[Share2Us] from device test, request request-1:\n\nwork"}}`+"\n"+
		`{"type":"user","message":{"role":"user","content":"my own next turn"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	defer func() { os.Stdin = previous }()
	for _, event := range []string{"pre-tool-use", "stop", "pre-tool-use"} {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		in := hookInput{SessionID: "session-1", TranscriptPath: transcript, CWD: t.TempDir(), ToolName: "Bash",
			ToolInput: map[string]any{"command": "echo owner"}}
		if err := json.NewEncoder(writer).Encode(in); err != nil {
			t.Fatal(err)
		}
		writer.Close()
		os.Stdin = reader
		var out bytes.Buffer
		if code := (app{stdout: &out}).agentHook([]string{event}); code != 0 || strings.Contains(out.String(), `"permissionDecision":"deny"`) {
			t.Fatalf("later owner %s was locked: exit=%d output=%q", event, code, out.String())
		}
		reader.Close()
	}
	if got := daemon.TypedGuardRequest("session-1"); got != "request-1" {
		t.Fatalf("unreachable daemon lost cancellation identity: %q", got)
	}
}
