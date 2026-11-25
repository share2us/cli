// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"testing"
)

// /clear (or a new claude in the same tab) gives the window a new session id. The agent
// follows it, keeping its id, but only to the same pane and project, and only once the
// old session is gone.
func TestBindingFollowsTheSessionInItsPane(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	pane := &ZellijPane{Session: "homelab", Pane: "terminal_0"}
	b, _, err := BindSessionInPane(project, "claude", "", "old-sess", pane)
	if err != nil {
		t.Fatal(err)
	}
	runtime := rt()
	runtime.processPane = func(pid int) *ZellijPane {
		switch pid {
		case 100:
			return &ZellijPane{Session: "homelab", Pane: "terminal_0"}
		case 200:
			return &ZellijPane{Session: "homelab", Pane: "terminal_5"}
		}
		return nil
	}
	newSess := DiscoveredSession{SessionID: "new-sess", Tool: "claude", Project: project, PID: 100, Live: true}
	otherPane := DiscoveredSession{SessionID: "elsewhere", Tool: "claude", Project: project, PID: 200, Live: true}

	if _, ok := runtime.followMovedSession(b, []DiscoveredSession{{SessionID: "old-sess", Tool: "claude", Project: project, PID: 50, Live: true}, newSess}, keyedDeps()); ok {
		t.Fatal("followed while the old session is still live")
	}
	if _, ok := runtime.followMovedSession(b, []DiscoveredSession{otherPane}, keyedDeps()); ok {
		t.Fatal("followed into another pane")
	}
	got, ok := runtime.followMovedSession(b, []DiscoveredSession{otherPane, newSess}, keyedDeps())
	if !ok || got.SessionID != "new-sess" {
		t.Fatalf("did not follow: %+v %v", got, ok)
	}
	list, _ := LoadBindings()
	nb, found := BindingForSession(list, "new-sess")
	if !found || nb.AgentID != b.AgentID || nb.Zellij == nil || *nb.Zellij != *pane {
		t.Fatalf("binding after follow: %+v (agent was %s)", nb, b.AgentID)
	}
}
