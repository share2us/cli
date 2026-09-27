// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"os"
	"strings"
	"testing"

	clicore "github.com/share2us/cli-core"
)

// sessionFake is a Claude-like runner: it reports which session a hop ended in.
type sessionFake struct {
	discovered []DiscoveredSession
	gotLive    *bool
	after      string // "" = same session
}

func (f *sessionFake) Tool() string { return "claude" }
func (f *sessionFake) Discover(context.Context) ([]DiscoveredSession, error) {
	return f.discovered, nil
}
func (f *sessionFake) Run(context.Context, string, string, string) (string, error) {
	panic("a session runner must be driven through RunSession")
}
func (f *sessionFake) RunSession(_ context.Context, sessionID, _, _ string, live bool) (string, string, error) {
	f.gotLive = &live
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
	if r.gotLive == nil || *r.gotLive {
		t.Fatalf("an unlisted session must be resumed in place (live=false), got %v", r.gotLive)
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

// A session its window holds is forked, the binding follows, and the hop log says
// where the work went (the window never shows it).
func TestHopForksALiveSessionAndLogsWhere(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	if _, _, err := BindSession(dir, "claude", "", "win-1"); err != nil {
		t.Fatal(err)
	}
	r := &sessionFake{discovered: []DiscoveredSession{{SessionID: "win-1", Tool: "claude", Project: dir, Status: "busy", Live: true}}, after: "fork-9"}
	rt().handleInject(context.Background(), &fakeAgentClient{}, r, keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-2", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "go"}))
	if r.gotLive == nil || !*r.gotLive {
		t.Fatal("a session Claude lists must be forked (live=true)")
	}
	list, _ := LoadBindings()
	if _, ok := BindingForSession(list, "fork-9"); !ok {
		t.Fatalf("binding did not follow the fork: %+v", list)
	}
	hops, _ := LoadHops(0)
	if len(hops) != 1 || hops[0].Mode != "forked" || hops[0].Target != "win-1" || hops[0].RanIn != "fork-9" {
		t.Fatalf("hop log = %+v", hops)
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

func TestClaudeArgsForkOnlyWhenAsked(t *testing.T) {
	inPlace := strings.Join(buildClaudeInjectArgs("s1", "p", Policy{}, "acceptEdits", false), " ")
	if strings.Contains(inPlace, "--fork-session") || !strings.Contains(inPlace, "--resume s1") {
		t.Fatalf("in-place args: %s", inPlace)
	}
	if forked := strings.Join(buildClaudeInjectArgs("s1", "p", Policy{}, "acceptEdits", true), " "); !strings.Contains(forked, "--resume s1 --fork-session") {
		t.Fatalf("fork args: %s", forked)
	}
	if !claudeWantsFork([]byte("Error: session is running as a background session; add --fork-session to branch off a copy")) || claudeWantsFork([]byte(`{"result":"ok"}`)) {
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
