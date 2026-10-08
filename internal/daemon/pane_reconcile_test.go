package daemon

import (
	"context"
	"strings"
	"testing"

	clicore "github.com/share2us/cli-core"
)

func TestReconcileZellij(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	proj := t.TempDir()
	write := func(pane *ZellijPane) {
		if err := saveBindings([]Binding{{
			AgentID: "agt_x", Project: proj, Tool: "claude", SessionID: "sess-1", Zellij: pane,
		}}); err != nil {
			t.Fatal(err)
		}
	}

	// Pane drifted: update it, and persist.
	write(&ZellijPane{Session: "butterpos", Pane: "1"})
	changed, err := ReconcileZellij("sess-1", &ZellijPane{Session: "butterpos", Pane: "0"})
	if err != nil || !changed {
		t.Fatalf("drift should update: changed=%v err=%v", changed, err)
	}
	list, _ := LoadBindings()
	if list[0].Zellij == nil || list[0].Zellij.Pane != "0" {
		t.Fatalf("pane not persisted: %+v", list[0].Zellij)
	}

	// Same pane: no-op.
	if changed, _ := ReconcileZellij("sess-1", &ZellijPane{Session: "butterpos", Pane: "0"}); changed {
		t.Error("identical pane should be a no-op")
	}

	// A binding bound OUTSIDE zellij (no pane) must never gain a typing target.
	write(nil)
	if changed, _ := ReconcileZellij("sess-1", &ZellijPane{Session: "butterpos", Pane: "0"}); changed {
		t.Error("must not add a pane to a non-zellij binding")
	}
	if list, _ := LoadBindings(); list[0].Zellij != nil {
		t.Error("non-zellij binding should stay paneless")
	}

	// Unknown session: no-op.
	write(&ZellijPane{Session: "butterpos", Pane: "0"})
	if changed, _ := ReconcileZellij("nope", &ZellijPane{Session: "x", Pane: "1"}); changed {
		t.Error("unknown session should be a no-op")
	}
}

func TestReconcileBindingPaneSelfHeals(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	proj := t.TempDir()
	if err := saveBindings([]Binding{{
		AgentID: "agt_x", Project: proj, Tool: "claude", SessionID: "sess-1",
		Zellij: &ZellijPane{Session: "butterpos", Pane: "1"},
	}}); err != nil {
		t.Fatal(err)
	}
	// The session's process now reports pane 0 (its zellij session was recreated).
	rt := &Runtime{processPane: func(int) *ZellijPane { return &ZellijPane{Session: "butterpos", Pane: "0"} }}
	rt.reconcileBindingPane(DiscoveredSession{SessionID: "sess-1", Tool: "claude", PID: 123, Live: true}, Deps{})
	list, _ := LoadBindings()
	if list[0].Zellij.Pane != "0" {
		t.Fatalf("self-heal did not update pane: %+v", list[0].Zellij)
	}
	// A session with no usable PID must not touch the store.
	rt.reconcileBindingPane(DiscoveredSession{SessionID: "sess-1", PID: 0}, Deps{})
}

func TestOpenRebindWarningTab(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	proj := t.TempDir()
	bind := func(pane *ZellijPane) {
		if err := saveBindings([]Binding{{
			AgentID: "agt_x", Project: proj, Tool: "claude", SessionID: "sess-1", Label: "butterpos-6a", Zellij: pane,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	req := clicore.AgentRequest{TargetSessionID: "sess-1", Tool: "claude"}

	// Live zellij session + warn-tab on: exactly one loud tab.
	bind(&ZellijPane{Session: "butterpos", Pane: "1"})
	fz := &fakeZellij{sessions: []string{"butterpos"}}
	rt := &Runtime{warnTab: true, zellij: fz}
	rt.openRebindWarningTab(context.Background(), req, Deps{})
	if len(fz.newTabs) != 1 {
		t.Fatalf("want 1 warning tab, got %v", fz.newTabs)
	}
	if !strings.Contains(fz.newTabs[0], "re-bind") || !strings.Contains(fz.newTabs[0], "butterpos-6a") {
		t.Errorf("tab title should name the agent and say re-bind: %q", fz.newTabs[0])
	}

	// Warn-tab disabled: nothing.
	fz2 := &fakeZellij{sessions: []string{"butterpos"}}
	(&Runtime{warnTab: false, zellij: fz2}).openRebindWarningTab(context.Background(), req, Deps{})
	if len(fz2.newTabs) != 0 {
		t.Error("warn-tab off must open no tab")
	}

	// The zellij session is gone: no tab (the desktop notification stands).
	fz3 := &fakeZellij{sessions: []string{"something-else"}}
	(&Runtime{warnTab: true, zellij: fz3}).openRebindWarningTab(context.Background(), req, Deps{})
	if len(fz3.newTabs) != 0 {
		t.Error("a vanished zellij session must open no tab")
	}

	// Bound outside zellij: no tab.
	bind(nil)
	fz4 := &fakeZellij{sessions: []string{"butterpos"}}
	(&Runtime{warnTab: true, zellij: fz4}).openRebindWarningTab(context.Background(), req, Deps{})
	if len(fz4.newTabs) != 0 {
		t.Error("a paneless binding must open no tab")
	}
}
