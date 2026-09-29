// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	clicore "github.com/share2us/cli-core"
)

// sessionFake is a Claude-like runner. heldFor is how many discoveries report
// the target as open in a window before it lets the session go.
type sessionFake struct {
	discovered []DiscoveredSession
	heldFor    int
	discovers  int
	ran        int
	ranSID     string
	after      string // "" = same session
}

func (f *sessionFake) Tool() string { return "claude" }
func (f *sessionFake) Discover(context.Context) ([]DiscoveredSession, error) {
	f.discovers++
	out := append([]DiscoveredSession(nil), f.discovered...)
	for i := range out {
		out[i].Live = out[i].Live && f.discovers <= f.heldFor
	}
	return out, nil
}
func (f *sessionFake) Run(context.Context, string, string, string) (string, error) {
	panic("a session runner must be driven through RunSession")
}
func (f *sessionFake) RunSession(_ context.Context, sessionID, _, _ string) (string, string, error) {
	f.ran++
	f.ranSID = sessionID
	if f.after == "" {
		return "ok", sessionID, nil
	}
	return "ok", f.after, nil
}

// A session no process holds (the fork an earlier hop made) is resumed in place:
// its id stays, so a sender's address keeps working, and nothing moves.
func TestHopResumesAnUnheldSessionInPlace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, _, err := BindSession(t.TempDir(), "claude", "", "fork-1"); err != nil {
		t.Fatal(err)
	}
	r := &sessionFake{} // discovery does not list fork-1
	rt().handleInject(context.Background(), &fakeAgentClient{}, r, keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-1", Tool: "claude", TargetSessionID: "fork-1", SealedPrompt: "go"}))
	if r.ran != 1 || r.ranSID != "fork-1" {
		t.Fatalf("an unlisted session must be resumed in place at once: ran %d in %q", r.ran, r.ranSID)
	}
	list, _ := LoadBindings()
	if _, ok := BindingForSession(list, "fork-1"); !ok {
		t.Fatalf("binding moved although the session did not change: %+v", list)
	}
	hops, _ := LoadHops(0)
	if len(hops) != 1 || hops[0].Mode != "resumed" || hops[0].RanIn != "fork-1" || hops[0].Status != "done" {
		t.Fatalf("hop log = %+v", hops)
	}
}

// A session its window holds is never forked: the hop waits until the window
// lets the session go, then runs IN it, and the binding does not move.
func TestHopWaitsForAnOpenWindowThenRunsInPlace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	poll := injectHoldPoll
	injectHoldPoll = 5 * time.Millisecond
	t.Cleanup(func() { injectHoldPoll = poll })
	dir := t.TempDir()
	if _, _, err := BindSession(dir, "claude", "", "win-1"); err != nil {
		t.Fatal(err)
	}
	r := &sessionFake{discovered: []DiscoveredSession{{SessionID: "win-1", Tool: "claude", Project: dir, Status: "busy", Live: true}}, heldFor: 3}
	c := &fakeAgentClient{}
	runtime := rt()
	runtime.handleInject(context.Background(), c, r, keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-2", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "go"}))
	// The run counter belongs to the waiting goroutine now; the hold count is
	// the safe thing to look at.
	if runtime.holding.Load() != 1 {
		t.Fatalf("a held session must wait, not run: holding %d", runtime.holding.Load())
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.holding.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if r.ran != 1 || r.ranSID != "win-1" {
		t.Fatalf("after the window let go: ran %d in %q, want once in win-1", r.ran, r.ranSID)
	}
	if len(c.reports) != 2 || c.reports[1] != [2]string{"done", "ok"} {
		t.Fatalf("reports = %v", c.reports)
	}
	list, _ := LoadBindings()
	if _, ok := BindingForSession(list, "win-1"); !ok {
		t.Fatalf("the binding moved: %+v", list)
	}
	hops, _ := LoadHops(0)
	if len(hops) != 1 || hops[0].Mode != "resumed" || hops[0].RanIn != "win-1" || hops[0].Waited == "" {
		t.Fatalf("hop log = %+v", hops)
	}
}

// A hop that waited too long fails with a reason, and never runs.
func TestHeldHopGivesUp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	poll, max := injectHoldPoll, injectHoldMax
	injectHoldPoll, injectHoldMax = 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { injectHoldPoll, injectHoldMax = poll, max })
	dir := t.TempDir()
	if _, _, err := BindSession(dir, "claude", "", "win-1"); err != nil {
		t.Fatal(err)
	}
	r := &sessionFake{discovered: []DiscoveredSession{{SessionID: "win-1", Tool: "claude", Project: dir, Live: true}}, heldFor: 1 << 30}
	c := &fakeAgentClient{}
	runtime := rt()
	runtime.handleInject(context.Background(), c, r, keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-3", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "go"}))
	deadline := time.Now().Add(5 * time.Second)
	for runtime.holding.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if r.ran != 0 || len(c.reports) != 1 || c.reports[0][0] != "failed" {
		t.Fatalf("ran %d, reports %v; want no run and one failure", r.ran, c.reports)
	}
}

func TestHopLogTrimsAndTruncates(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	long := strings.Repeat("é", hopPromptChars+50)
	for i := 0; i < hopLogKeep+5; i++ {
		if err := AppendHop(HopRecord{RequestID: string(rune('a' + i%26)), Prompt: long}); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := LoadHops(0)
	if len(all) != hopLogKeep {
		t.Fatalf("kept %d records, want %d", len(all), hopLogKeep)
	}
	if n := len([]rune(all[0].Prompt)); n != hopPromptChars+1 {
		t.Fatalf("prompt kept %d runes, want %d plus the ellipsis", n, hopPromptChars)
	}
	if last, _ := LoadHops(3); len(last) != 3 {
		t.Fatalf("LoadHops(3) = %d", len(last))
	}
	path, _ := hopLogPath()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	wantPrivate(t, st.Mode(), "hop log (it holds prompts in the clear)")
}

// Moki's point 6: one session listed twice (its window and a background entry)
// showed different presence depending on order. Busy now wins either way.
func TestClaudeDuplicateEntriesBusyWins(t *testing.T) {
	for _, raw := range []string{
		`[{"sessionId":"s","pid":1,"status":"busy"},{"sessionId":"s","state":"blocked"}]`,
		`[{"sessionId":"s","state":"blocked"},{"sessionId":"s","pid":1,"status":"busy"}]`,
	} {
		got, err := parseClaudeAgents([]byte(raw))
		if err != nil || len(got) != 1 || got[0].Status != "busy" || !got[0].Live {
			t.Fatalf("%s -> %+v, %v", raw, got, err)
		}
	}
}

func TestClaudeArgsNeverFork(t *testing.T) {
	args := strings.Join(buildClaudeInjectArgs("s1", "p", Policy{}, "acceptEdits"), " ")
	if strings.Contains(args, "--fork-session") || !strings.Contains(args, "--resume s1") {
		t.Fatalf("args: %s", args)
	}
	if !claudeRefusedHeld([]byte("Error: session is running as a background session; add --fork-session to branch off a copy")) || claudeRefusedHeld([]byte(`{"result":"ok"}`)) {
		t.Fatal("refusal detection")
	}
}

// Moki's point 5: a single-session binding advertises that session and none of
// its siblings in the same folder, whatever discovery lists.
func TestSiblingSessionsAreNotAdvertised(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	if _, _, err := BindSession(dir, "claude", "", "chosen"); err != nil {
		t.Fatal(err)
	}
	r := &sessionFake{discovered: []DiscoveredSession{
		{SessionID: "chosen", Tool: "claude", Project: dir, Status: "available", Live: true},
		{SessionID: "sibling-1", Tool: "claude", Project: dir, Status: "available", Live: true},
		{SessionID: "sibling-2", Tool: "claude", Project: dir, Status: "busy", Live: true},
	}}
	c := &fakeAgentClient{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // one sync pass, then the loop returns
	rt().agentRegisterLoop(ctx, c, []AgentRunner{r}, keyedDeps())
	if len(c.registered) != 1 || c.registered[0] != "chosen" {
		t.Fatalf("registered %v, want only the chosen session", c.registered)
	}
}

// countingRunner counts Discover calls.
type countingRunner struct {
	tool  string
	calls int
}

func (c *countingRunner) Tool() string { return c.tool }
func (c *countingRunner) Discover(context.Context) ([]DiscoveredSession, error) {
	c.calls++
	return nil, nil
}
func (c *countingRunner) Run(context.Context, string, string, string) (string, error) { return "", nil }

// Discovery costs real CPU (Gemini's took ~4-5 s a call), so a tool with no
// binding is never asked: nothing it reports could be advertised anyway.
func TestOnlyBoundToolsAreDiscovered(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, _, err := BindSession(t.TempDir(), "claude", "", "s1"); err != nil {
		t.Fatal(err)
	}
	claude, gemini, codex := &countingRunner{tool: "claude"}, &countingRunner{tool: "gemini"}, &countingRunner{tool: "codex"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rt().agentRegisterLoop(ctx, &fakeAgentClient{}, []AgentRunner{claude, gemini, codex}, keyedDeps())
	if claude.calls != 1 || gemini.calls != 0 || codex.calls != 0 {
		t.Fatalf("discover calls: claude %d gemini %d codex %d, want 1 0 0", claude.calls, gemini.calls, codex.calls)
	}
}
