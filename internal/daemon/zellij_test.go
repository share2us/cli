// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeZellij struct {
	sessions []string
	panes    map[string][]zellijPaneInfo
	screens  []string
	pasted   []string
	entered  int
	onPaste  func()
}

func claudeFixture(input string) string {
	return "────────────────────────\n" + input + "\n⏵⏵ auto mode on"
}

func (f *fakeZellij) Sessions(context.Context) ([]string, error) { return f.sessions, nil }
func (f *fakeZellij) Panes(_ context.Context, session string) ([]zellijPaneInfo, error) {
	p, ok := f.panes[session]
	if !ok {
		return nil, errors.New("gone")
	}
	return p, nil
}
func (f *fakeZellij) Dump(context.Context, string, string) (string, error) {
	if len(f.screens) == 0 {
		return "", errors.New("no screen")
	}
	s := f.screens[0]
	f.screens = f.screens[1:]
	return s, nil
}
func (f *fakeZellij) Paste(_ context.Context, _, _, text string) error {
	f.pasted = append(f.pasted, text)
	if f.onPaste != nil {
		f.onPaste()
	}
	return nil
}
func (f *fakeZellij) Enter(context.Context, string, string) error { f.entered++; return nil }

func TestParseClaudeScreen(t *testing.T) {
	tests := []struct {
		name string
		in   string
		safe bool
	}{
		{"empty", claudeFixture("│ ❯  │"), true},
		{"dim suggestion", claudeFixture("│ ❯ \x1b[2mTry \"fix typecheck errors\"\x1b[22m │"), true},
		{"owner text", claudeFixture("│ ❯ do not touch this │"), false},
		{"busy", claudeFixture("│ ❯  │\nesc to interrupt"), false},
		{"dialog", claudeFixture("Do you want to continue?\n  1. Yes\n  2. No\n│ ❯  │"), false},
		// Captured read-only from Claude Code 2.1.284 in the owner's scratch
		// pane. Its empty input uses a non-breaking space and a bare ANSI reset,
		// not the bordered form used by the synthetic fixtures above.
		{"real idle ansi", "\x1b[38;5;244m────────────────\n\x1b[m❯\u00a0\n\x1b[38;5;220m⏵⏵ auto mode on\x1b[m", true},
		{"shell prompt", "❯\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := safeClaudeInput(tt.in); got != tt.safe {
				t.Fatalf("safe = %v, want %v; state=%+v", got, tt.safe, parseClaudeScreen(tt.in))
			}
		})
	}
}

func TestClaudeStateIgnoresTranscriptWords(t *testing.T) {
	for _, transcript := range []string{"Do you want to continue?", "The UI said esc to interrupt"} {
		screen := transcript + "\nold answer\n────────────────────────\n❯\u00a0\n────────────────────────\n⏵⏵ auto mode on"
		if !safeClaudeInput(screen) {
			t.Fatalf("transcript %q made the idle input unsafe: %+v", transcript, parseClaudeScreen(screen))
		}
	}
	if safeClaudeInput("old answer\n────────────────────────\n❯\u00a0\nesc to interrupt\n────────────────────────") {
		t.Fatal("active busy status was ignored")
	}
}

func TestPastedClaudeInput(t *testing.T) {
	if !pastedClaudeInput(claudeFixture("│ ❯ [Share2Us] request r1 │"), "[Share2Us] request r1") {
		t.Fatal("exact pasted text was not recognised")
	}
	if !pastedClaudeInput(claudeFixture("│ ❯ [Pasted text +30 lines] │"), "many\nlines") {
		t.Fatal("folded paste was not recognised")
	}
	if !pastedClaudeInput(claudeFixture("\x1b[m❯\u00a0[Pasted text #1 +3 lines]"), "many\nlines") {
		t.Fatal("real Claude 2.1.284 folded paste was not recognised")
	}
	if pastedClaudeInput(claudeFixture("│ ❯ owner text [Pasted text +30 lines] │"), "many\nlines") {
		t.Fatal("mixed owner input was accepted")
	}
	unfolded := "────────────────────────\n❯ [Share2Us] from device d1, request r1:\n\n  fix\n────────────────────────\n⏵⏵ auto mode on"
	if !pastedClaudeInput(claudeFixture(unfolded), "[Share2Us] from device d1, request r1:\n\nfix") {
		t.Fatalf("unfolded multiline paste was not recognised: %+v", parseClaudeScreen(unfolded))
	}
	mixed := "────────────────────────\n❯ [Share2Us] from device d1, request r1:\n\n  owner text fix\n────────────────────────"
	if pastedClaudeInput(claudeFixture(mixed), "[Share2Us] from device d1, request r1:\n\nfix") {
		t.Fatal("owner text in an unfolded paste was accepted")
	}
}

func TestClaudePaneCommand(t *testing.T) {
	for _, command := range []string{"claude --resume id", "/usr/bin/claude", "s2u claude --resume id", "share2us claude", "/tmp/s2u-zellij-live claude"} {
		if !isClaudePaneCommandFor(command, "s2u-zellij-live") {
			t.Errorf("rejected %q", command)
		}
	}
	for _, command := range []string{"bash", "codex", "echo claude"} {
		if isClaudePaneCommandFor(command, "s2u-zellij-live") {
			t.Errorf("accepted %q", command)
		}
	}
}

func TestParseActiveZellijSessions(t *testing.T) {
	raw := "share2us [Created 2h ago] (current)\nold [Created 2d ago] (EXITED - attach to resurrect)\nother [Created now]\n"
	got := parseZellijSessions(raw)
	if len(got) != 2 || got[0] != "share2us" || got[1] != "other" {
		t.Fatalf("active sessions = %v", got)
	}
}

func TestResolveZellijPaneRequiresOneExactLiveMatch(t *testing.T) {
	dir := t.TempDir()
	binding := Binding{SessionID: "session-1", Project: dir, Tool: "claude", Zellij: &ZellijPane{Session: "stale-name", Pane: "4"}}
	session := DiscoveredSession{SessionID: "session-1", Project: dir, Tool: "claude", PID: 123, Live: true}
	z := &fakeZellij{sessions: []string{"renamed", "other"}, panes: map[string][]zellijPaneInfo{
		"renamed": {{ID: 4, CWD: dir, Command: "claude --resume session-1", TabName: "work"}},
		"other":   {{ID: 4, CWD: "/elsewhere", Command: "claude"}},
	}}
	process := func(pid int) *ZellijPane {
		if pid != 123 {
			t.Fatalf("pid = %d", pid)
		}
		return &ZellijPane{Session: "stale-name", Pane: "4"}
	}
	got, err := resolveZellijPaneWith(context.Background(), z, binding, session, process)
	if err != nil || got.Session != "renamed" || got.Pane != "4" || got.TabName != "work" {
		t.Fatalf("resolved = %+v, %v", got, err)
	}

	z.panes["other"] = []zellijPaneInfo{{ID: 4, CWD: dir, Command: "claude"}}
	if _, err := resolveZellijPaneWith(context.Background(), z, binding, session, process); err == nil {
		t.Fatal("duplicate pane ids across sessions were accepted")
	}
	z.panes["other"] = nil
	z.panes["renamed"][0].Exited = true
	if _, err := resolveZellijPaneWith(context.Background(), z, binding, session, process); err == nil {
		t.Fatal("exited pane was accepted")
	}
	z.panes["renamed"][0].Exited = false
	wrongProcess := func(int) *ZellijPane { return &ZellijPane{Session: "another", Pane: "4"} }
	if _, err := resolveZellijPaneWith(context.Background(), z, binding, session, wrongProcess); err == nil {
		t.Fatal("a process that moved panes was accepted")
	}
}

func TestResolveZellijPaneFailsClosedWhenAnySessionIsUnreadable(t *testing.T) {
	dir := t.TempDir()
	binding := Binding{SessionID: "session-1", Project: dir, Tool: "claude", Zellij: &ZellijPane{Session: "bound", Pane: "4"}}
	session := DiscoveredSession{SessionID: "session-1", Project: dir, Tool: "claude", PID: 123, Live: true}
	z := &fakeZellij{sessions: []string{"matching", "unreadable"}, panes: map[string][]zellijPaneInfo{
		"matching": {{ID: 4, CWD: dir, Command: "claude --resume session-1"}},
	}}
	process := func(int) *ZellijPane { return &ZellijPane{Session: "bound", Pane: "4"} }
	if _, err := resolveZellijPaneWith(context.Background(), z, binding, session, process); err == nil {
		t.Fatal("a match was trusted while another listed session could not be inspected")
	}
}

// Opt-in acceptance check against a disposable real Claude pane. The task is
// deliberately one word: this is the case Claude leaves unfolded instead of
// replacing it with a [Pasted text ...] marker.
//
// S2U_LIVE_ZELLIJ_SESSION=name S2U_LIVE_ZELLIJ_PANE=1 go test ./internal/daemon -run LiveZellijOneWord -v
func TestLiveZellijOneWordPaste(t *testing.T) {
	session, paneID := os.Getenv("S2U_LIVE_ZELLIJ_SESSION"), os.Getenv("S2U_LIVE_ZELLIJ_PANE")
	if session == "" || paneID == "" {
		t.Skip("set S2U_LIVE_ZELLIJ_SESSION and S2U_LIVE_ZELLIJ_PANE for a disposable idle Claude pane")
	}
	z := newSystemZellij()
	pane := resolvedZellijPane{Session: session, Pane: paneID}
	before, err := z.Dump(t.Context(), session, paneID)
	if err != nil {
		t.Fatal(err)
	}
	state := parseClaudeScreen(before)
	idle := !state.Busy && !state.Dialog && state.InputEmpty
	// Claude 2.1.284 sometimes renders its untouched suggestion without the
	// expected dim SGR bytes in dump-screen. For this opt-in live check only,
	// accept that known placeholder when the real cursor is still parked at the
	// prompt (owner text would move it right).
	if !idle && !state.Busy && !state.Dialog && strings.HasPrefix(state.Input, `Try "`) {
		if panes, err := z.Panes(t.Context(), session); err == nil {
			for _, p := range panes {
				if p.paneID() == paneID && len(p.CursorCoordinates) == 2 && p.CursorCoordinates[0] <= 3 {
					idle = true
				}
			}
		}
	}
	if !idle {
		t.Fatalf("Claude pane is not idle: %+v", state)
	}
	visible := "[Share2Us] from device live-test, request live-one-word:\n\nok"
	if err := z.Paste(t.Context(), session, paneID, visible); err != nil {
		t.Fatal(err)
	}
	if !waitForPastedClaudeInput(t.Context(), z, pane, visible) {
		after, _ := z.Dump(t.Context(), session, paneID)
		got := parseClaudeScreen(after)
		t.Fatalf("one-word prompt was not verified in the real input: %+v normalized=%q want=%q", got, withoutWhitespace(got.Input), withoutWhitespace(visible))
	}
	if err := z.Enter(t.Context(), session, paneID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		after, err := z.Dump(t.Context(), session, paneID)
		if err == nil && !pastedClaudeInput(after, visible) {
			t.Log("one-word prompt was verified and submitted in the real Claude pane")
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("Enter did not submit the verified one-word prompt")
}
