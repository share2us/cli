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
	"time"

	"github.com/share2us/cli-core/daemonctl"
	"github.com/share2us/cli/internal/daemon"
)

func TestActiveTypedHopFailsClosedWithoutDaemon(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := daemon.BeginTypedGuard("session-1", "request-1", 123); err != nil {
		t.Fatal(err)
	}
	if !daemon.BindTypedPromptID("session-1", "[Share2Us] from device test, request request-1:\n\nwork", "prompt-remote") {
		t.Fatal("remote prompt id was not bound")
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

func TestHeadlessHookDeniesSeparateGitdirEdit(t *testing.T) {
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, "meta"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".git"), []byte("gitdir: ./meta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("S2U_HEADLESS_GUARD_PROJECT", project)
	previous := os.Stdin
	defer func() { os.Stdin = previous }()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := json.NewEncoder(writer).Encode(hookInput{SessionID: "headless-session", CWD: project, ToolName: "Edit",
		ToolInput: map[string]any{"file_path": filepath.Join(project, "meta", "config")}}); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	os.Stdin = reader
	var out bytes.Buffer
	if code := (app{stdout: &out}).agentHook([]string{"headless-pre-tool-use"}); code != 0 || !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("headless metadata edit was allowed: exit=%d output=%q", code, out.String())
	}
}

func TestInterruptedTypedTurnDoesNotLockLaterOwnerToolsWithoutDaemon(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := daemon.BeginTypedGuard("session-1", "request-1", 123); err != nil {
		t.Fatal(err)
	}
	if !daemon.BindTypedPromptID("session-1", "[Share2Us] from device test, request request-1:\n\nwork", "prompt-remote") {
		t.Fatal("remote prompt id was not bound")
	}
	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"user","uuid":"remote","parentUuid":"root","message":{"role":"user","content":"[Share2Us] from device test, request request-1:\n\nwork"}}`+"\n"+
		`{"type":"user","uuid":"owner","parentUuid":"root","message":{"role":"user","content":"my own next turn"}}`+"\n"+
		`{"type":"assistant","uuid":"owner-tool","parentUuid":"owner","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-owner"}]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	defer func() { os.Stdin = previous }()
	for _, event := range []string{"pre-tool-use", "stop", "pre-tool-use"} {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		in := hookInput{SessionID: "session-1", PromptID: "prompt-owner", ToolUseID: "tool-owner", TranscriptPath: transcript, CWD: t.TempDir(), ToolName: "Bash",
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

func TestCompactionAndQueuedMessageCannotReleaseGuardMidTurn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := daemon.BeginTypedGuard("session-1", "request-1", 123); err != nil {
		t.Fatal(err)
	}
	if !daemon.BindTypedPromptID("session-1", "[Share2Us] from device test, request request-1:\n\nwork", "prompt-remote") {
		t.Fatal("remote prompt id was not bound")
	}
	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := `{"type":"user","uuid":"remote","parentUuid":"root","message":{"role":"user","content":"[Share2Us] from device test, request request-1:\n\nwork"}}` + "\n" +
		`{"type":"user","uuid":"meta","parentUuid":"remote","isMeta":true,"message":{"role":"user","content":"internal context"}}` + "\n" +
		`{"type":"user","uuid":"compact","parentUuid":"meta","isCompactSummary":true,"message":{"role":"user","content":"compaction summary"}}` + "\n" +
		`{"type":"assistant","uuid":"remote-tool","parentUuid":"compact","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-remote"}]}}` + "\n" +
		`{"type":"user","uuid":"queued","parentUuid":"remote-tool","message":{"role":"user","content":"queued owner message"}}` + "\n"
	if err := os.WriteFile(transcript, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	defer func() { os.Stdin = previous }()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	in := hookInput{SessionID: "session-1", PromptID: "prompt-owner", ToolUseID: "tool-remote", TranscriptPath: transcript,
		CWD: t.TempDir(), ToolName: "Bash", ToolInput: map[string]any{"command": "echo unsafe"}}
	if err := json.NewEncoder(writer).Encode(in); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	os.Stdin = reader
	var out bytes.Buffer
	if code := (app{stdout: &out}).agentHook([]string{"pre-tool-use"}); code != 0 || !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("remote tool escaped after transcript entries: exit=%d output=%q", code, out.String())
	}
	if got := daemon.TypedGuardPromptID("session-1"); got != "prompt-remote" {
		t.Fatalf("compaction released guard: %q", got)
	}
}

func TestSubmitBindsPromptAndStopWaitsForDaemonAck(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := daemon.BeginTypedGuard("session-1", "request-1", 123); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	defer func() { os.Stdin = previous }()
	runHook := func(event string, in hookInput) {
		t.Helper()
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(writer).Encode(in); err != nil {
			t.Fatal(err)
		}
		writer.Close()
		os.Stdin = reader
		if code := (app{stdout: &bytes.Buffer{}}).agentHook([]string{event}); code != 0 {
			t.Fatalf("%s exit = %d", event, code)
		}
		reader.Close()
	}
	runHook("user-prompt-submit", hookInput{SessionID: "session-1", PromptID: "prompt-remote",
		Prompt: "[Share2Us] from device test, request request-1:\n\nwork"})
	if got := daemon.TypedGuardPromptID("session-1"); got != "prompt-remote" {
		t.Fatalf("submitted prompt id = %q", got)
	}
	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"user","message":{"role":"user","content":"[Share2Us] from device test, request request-1:\n\nwork"}}`+"\n"+
		`{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"summary"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runHook("stop", hookInput{SessionID: "session-1", PromptID: "prompt-remote", TranscriptPath: transcript})
	if got := daemon.TypedGuardRequest("session-1"); got != "request-1" {
		t.Fatalf("unacknowledged Stop dropped marker: %q", got)
	}
	if !completeHookTurn("session-1", "request-1", func(req daemonctl.Request) (daemonctl.Response, bool) {
		if req.Op != "channel-turn-ended" || req.Args["request_id"] != "request-1" {
			t.Fatalf("wrong completion request: %+v", req)
		}
		return daemonctl.Response{OK: true}, true
	}) || daemon.TypedGuardRequest("session-1") != "" {
		t.Fatal("acknowledged Stop did not clear the marker")
	}
}

func TestGuardReadyRetriesUntilAcknowledged(t *testing.T) {
	tries, waits := 0, 0
	call := func(req daemonctl.Request) (daemonctl.Response, bool) {
		if req.Op != "channel-guard-ready" || req.Args["session"] != "s1" {
			t.Fatalf("wrong guard registration: %+v", req)
		}
		tries++
		return daemonctl.Response{OK: tries == 3}, true
	}
	if !markChannelGuardReady("s1", call, func(time.Duration) { waits++ }) || tries != 3 || waits != 2 {
		t.Fatalf("registration did not retry to an acknowledgement: tries=%d waits=%d", tries, waits)
	}
	tries = 0
	if markChannelGuardReady("s1", call, nil) || tries != 1 {
		t.Fatalf("later hook did not make exactly one attempt: tries=%d", tries)
	}
}

func TestOwnerPromptRegistersGuardOnlyWhenMissing(t *testing.T) {
	registered, registrationCalls := true, 0
	call := func(req daemonctl.Request) (daemonctl.Response, bool) {
		switch req.Op {
		case "channel-guard-registered":
			return daemonctl.Response{OK: registered}, true
		case "channel-guard-ready":
			registrationCalls++
			registered = true
			return daemonctl.Response{OK: true}, true
		default:
			t.Fatalf("unexpected channel op: %s", req.Op)
			return daemonctl.Response{}, false
		}
	}
	if !ensureChannelGuardReady("s1", call) || registrationCalls != 0 {
		t.Fatal("owner prompt re-registered an already proven hook")
	}
	registered = false // daemon restarted after SessionStart
	if !ensureChannelGuardReady("s1", call) || registrationCalls != 1 {
		t.Fatal("owner prompt did not recover missing daemon registration")
	}
}
