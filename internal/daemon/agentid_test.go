// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The agent id is the thing invitations into another owner's project are granted
// against (ADR-041 §1a). The one property that matters above all: once created it
// never changes. Every test here is some way it might.

func TestBindCreatesAnAgentID(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b, _, err := Bind(t.TempDir(), "claude", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.AgentID, agentIDPrefix) || len(b.AgentID) < len(agentIDPrefix)+20 {
		t.Fatalf("agent id %q is not a full agt_ id", b.AgentID)
	}
}

// Re-binding the same project — which people do, and which `s2u agent bind` must
// tolerate — must not mint a new identity.
func TestRebindingKeepsTheSameAgentID(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	first, _, _ := Bind(project, "claude", "")
	again, created, _ := Bind(project, "claude", "renamed")
	if created {
		t.Fatal("re-binding created a second binding")
	}
	if again.AgentID != first.AgentID {
		t.Fatalf("re-binding changed the agent id: %s -> %s", first.AgentID, again.AgentID)
	}
}

// Two clones of one repository are two agents. This is the collision that
// ruled out keeping the id in the committed .s2u/ directory.
func TestTwoClonesAreTwoAgents(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	a, _, _ := Bind(filepath.Join(root, "clone-a"), "claude", "")
	b, _, _ := Bind(filepath.Join(root, "clone-b"), "claude", "")
	if a.AgentID == b.AgentID {
		t.Fatal("two clones share one agent id")
	}
	// And one project with two tools is two agents too.
	c, _, _ := Bind(filepath.Join(root, "clone-a"), "codex", "")
	if c.AgentID == a.AgentID {
		t.Fatal("two tools in one project share one agent id")
	}
}

// Reading must not change anything. The daemon re-reads bindings every sync; if
// a read could mint or rewrite an id, a daemon racing `agent bind` could change an
// agent's identity under it.
func TestLoadingNeverChangesAnID(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	b, _, _ := Bind(project, "claude", "")
	path, _ := BindingsPath()
	before, _ := os.ReadFile(path)
	for i := 0; i < 5; i++ {
		list, err := LoadBindings()
		if err != nil {
			t.Fatal(err)
		}
		got, ok := BindingFor(list, project, "claude")
		if !ok || got.AgentID != b.AgentID {
			t.Fatalf("load %d returned id %q, want %q", i, got.AgentID, b.AgentID)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("loading bindings rewrote the file")
	}
}

// A binding written before agent ids existed has none. It gets one the next time
// it is bound — in a path that already writes — and keeps it from then on.
func TestLegacyBindingIsBackfilledOnceOnBind(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	path, _ := BindingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy, _ := json.Marshal(bindingsFile{Version: 1, Bindings: []Binding{{Project: normalizeProject(project), Tool: "claude"}}})
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	// Reading a legacy binding leaves it id-less: no write on read.
	list, _ := LoadBindings()
	if b, _ := BindingFor(list, project, "claude"); b.AgentID != "" {
		t.Fatal("a read backfilled an id")
	}
	// Binding again backfills it.
	b1, created, err := Bind(project, "claude", "")
	if err != nil || created {
		t.Fatalf("re-bind of a legacy binding = created %v, %v", created, err)
	}
	if b1.AgentID == "" {
		t.Fatal("the legacy binding was not given an id")
	}
	// And that id is now permanent.
	b2, _, _ := Bind(project, "claude", "")
	if b2.AgentID != b1.AgentID {
		t.Fatalf("the backfilled id changed on the next bind: %s -> %s", b1.AgentID, b2.AgentID)
	}
}

// The identity file must not be left truncated by a crash mid-write.
func TestBindingsAreWrittenAtomically(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, _, err := Bind(t.TempDir(), "claude", ""); err != nil {
		t.Fatal(err)
	}
	path, _ := BindingsPath()
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("a temporary file was left behind")
	}
}

// A project hop names its sending agent from the directory it is run in.
func TestAgentIDForProject(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	list := []Binding{
		{Project: dir, Tool: "claude", AgentID: "agt_aaaaaaaaaaaaaaaaaaaaaa"},
		{Project: other, Tool: "codex", AgentID: "agt_bbbbbbbbbbbbbbbbbbbbbb"},
	}
	if id, ok := AgentIDForProject(list, dir+"/"); !ok || id != "agt_aaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("got %q %v", id, ok)
	}
	if _, ok := AgentIDForProject(list, t.TempDir()); ok {
		t.Fatal("an unbound directory produced an agent id")
	}
	// Two tools bound to one directory with different ids: refuse to guess.
	list = append(list, Binding{Project: dir, Tool: "codex", AgentID: "agt_cccccccccccccccccccccc"})
	if _, ok := AgentIDForProject(list, dir); ok {
		t.Fatal("guessed between two agents bound to the same directory")
	}
	// A binding made before agent ids existed does not count.
	if _, ok := AgentIDForProject([]Binding{{Project: other, Tool: "claude"}}, other); ok {
		t.Fatal("an id-less binding produced an agent id")
	}
}
