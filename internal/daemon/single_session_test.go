// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"testing"

	clicore "github.com/share2us/cli-core"
)

// Single-session binding (owner, 2026-09-27): one session is the agent, not
// every session in its folder.
func TestSingleSessionBindingCoversOnlyThatSession(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	b, _, err := BindSession(dir, "claude", "", "sess-A")
	if err != nil || b.SessionID != "sess-A" || b.AgentID == "" {
		t.Fatalf("bind = %+v, %v", b, err)
	}
	if !b.Covers(DiscoveredSession{SessionID: "sess-A", Tool: "claude", Project: dir}) {
		t.Fatal("the bound session is not covered")
	}
	if b.Covers(DiscoveredSession{SessionID: "sess-B", Tool: "claude", Project: dir}) {
		t.Fatal("another session in the same folder is covered")
	}
	// Binding another session in the folder moves the agent; its id stays.
	b2, _, err := BindSession(dir, "claude", "", "sess-B")
	if err != nil || b2.SessionID != "sess-B" || b2.AgentID != b.AgentID {
		t.Fatalf("rebind = %+v (agent id must not change), %v", b2, err)
	}
	// A binding from before single-session binding still covers the folder.
	legacy := Binding{Project: dir, Tool: "claude"}
	if !legacy.Covers(DiscoveredSession{SessionID: "anything", Tool: "claude", Project: dir}) {
		t.Fatal("a legacy folder binding stopped covering its folder")
	}
}

func TestMoveSessionFollowsTheFork(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b, _, _ := BindSession(t.TempDir(), "claude", "", "sess-A")
	moved, err := MoveSession("sess-A", "sess-A-fork")
	if err != nil || !moved {
		t.Fatalf("move = %v, %v", moved, err)
	}
	list, _ := LoadBindings()
	got, ok := BindingForSession(list, "sess-A-fork")
	if !ok || got.AgentID != b.AgentID {
		t.Fatalf("after the move the fork is not the same agent: %+v", got)
	}
	if moved, _ := MoveSession("not-bound", "x"); moved {
		t.Fatal("moved a binding that did not point at the source session")
	}
}

func TestParseClaudeResult(t *testing.T) {
	out, sid := parseClaudeResult([]byte(`{"type":"result","is_error":false,"result":"done it","session_id":"fork-1"}`))
	if out != "done it" || sid != "fork-1" {
		t.Fatalf("parsed %q %q", out, sid)
	}
	out, sid = parseClaudeResult([]byte("warning: something\n{\"result\":\"ok\",\"session_id\":\"f2\"}"))
	if out != "ok" || sid != "f2" {
		t.Fatalf("with a leading line: %q %q", out, sid)
	}
	out, sid = parseClaudeResult([]byte("plain text, not json"))
	if out != "plain text, not json" || sid != "" {
		t.Fatalf("non-json: %q %q", out, sid)
	}
}

type forkRunner struct{ fakeRunner }

func (f *forkRunner) RunSession(_ context.Context, sessionID, _, prompt string, _ bool) (string, string, error) {
	f.ranSID, f.ranPrompt = sessionID, prompt
	return "forked ok", sessionID + "-fork", nil
}

// After a hop, the agent IS the fork: the binding moves, the result is reported.
func TestHopMovesTheBindingToTheFork(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, _, err := BindSession(t.TempDir(), "claude", "", "s1"); err != nil {
		t.Fatal(err)
	}
	c := &fakeAgentClient{}
	r := &forkRunner{}
	rt().handleInject(context.Background(), c, r, keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-1", Tool: "claude", TargetSessionID: "s1", SealedPrompt: "go"}))
	if r.ranSID != "s1" {
		t.Fatalf("ran session %q", r.ranSID)
	}
	if len(c.reports) != 2 || c.reports[1] != [2]string{"done", "forked ok"} {
		t.Fatalf("reports = %v", c.reports)
	}
	list, _ := LoadBindings()
	if _, ok := BindingForSession(list, "s1-fork"); !ok {
		t.Fatalf("binding did not move to the fork: %+v", list)
	}
}
