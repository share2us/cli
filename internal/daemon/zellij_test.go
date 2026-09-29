// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"errors"
	"testing"
)

type fakeZellij struct {
	sessions []string
	panes    map[string][]zellijPaneInfo
	screens  []string
	pasted   []string
	entered  int
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
	return nil
}
func (f *fakeZellij) Enter(context.Context, string, string) error { f.entered++; return nil }

func TestParseClaudeScreen(t *testing.T) {
	tests := []struct {
		name string
		in   string
		safe bool
	}{
		{"empty", "╭─╮\n│ ❯  │\n╰─╯", true},
		{"dim suggestion", "│ ❯ \x1b[2mTry \"fix typecheck errors\"\x1b[22m │", true},
		{"owner text", "│ ❯ do not touch this │", false},
		{"busy", "│ ❯  │\nesc to interrupt", false},
		{"dialog", "Do you want to continue?\n  1. Yes\n  2. No\n│ ❯  │", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := safeClaudeInput(tt.in); got != tt.safe {
				t.Fatalf("safe = %v, want %v; state=%+v", got, tt.safe, parseClaudeScreen(tt.in))
			}
		})
	}
}

func TestPastedClaudeInput(t *testing.T) {
	if !pastedClaudeInput("│ ❯ [Share2Us] request r1 │", "[Share2Us] request r1") {
		t.Fatal("exact pasted text was not recognised")
	}
	if !pastedClaudeInput("│ ❯ [Pasted text +30 lines] │", "many\nlines") {
		t.Fatal("folded paste was not recognised")
	}
	if pastedClaudeInput("│ ❯ owner text [Pasted text +30 lines] │", "many\nlines") {
		t.Fatal("mixed owner input was accepted")
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
