// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	clicore "github.com/share2us/cli-core"
)

func TestNothingIsBoundByDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	list, err := LoadBindings()
	if err != nil {
		t.Fatal(err)
	}
	// The whole point of F2: a fresh machine advertises nothing at all.
	if IsBound(list, "/anywhere", "claude") {
		t.Fatal("a project was bound before anyone bound it")
	}
}

func TestBindIsPerProjectAndPerTool(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	if _, _, err := Bind(project, "claude", "docs"); err != nil {
		t.Fatal(err)
	}
	list, err := LoadBindings()
	if err != nil {
		t.Fatal(err)
	}
	if !IsBound(list, project, "claude") {
		t.Fatal("the bound project is not bound")
	}
	// Binding Claude in a directory must not advertise a Codex session beside it.
	if IsBound(list, "", "claude") || IsBound(list, project, "codex") {
		t.Fatal("binding leaked to another tool or to an unnamed project")
	}
	// A different project is unaffected.
	if IsBound(list, t.TempDir(), "claude") {
		t.Fatal("binding leaked to another project")
	}
}

func TestBindNormalizesThePath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	if _, _, err := Bind(project, "codex", ""); err != nil {
		t.Fatal(err)
	}
	list, _ := LoadBindings()
	// Same directory, a spelling the discovery layer might hand us.
	for _, variant := range []string{project + "/", filepath.Join(project, "."), filepath.Join(project, "sub", "..")} {
		if !IsBound(list, variant, "codex") {
			t.Fatalf("%q did not match the bound project", variant)
		}
	}
}

func TestBindIsIdempotentAndUpdatesTheLabel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	if _, created, _ := Bind(project, "claude", "first"); !created {
		t.Fatal("first bind should report creation")
	}
	b, created, err := Bind(project, "claude", "second")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("re-binding the same project created a duplicate")
	}
	if b.Label != "second" {
		t.Fatalf("label = %q, want it updated", b.Label)
	}
	list, _ := LoadBindings()
	if len(list) != 1 {
		t.Fatalf("%d bindings, want 1", len(list))
	}
}

func TestUnbind(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	if _, _, err := Bind(project, "claude", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Bind(project, "codex", ""); err != nil {
		t.Fatal(err)
	}
	n, err := Unbind(project, "claude")
	if err != nil || n != 1 {
		t.Fatalf("unbind one tool = %d, %v", n, err)
	}
	list, _ := LoadBindings()
	if IsBound(list, project, "claude") || !IsBound(list, project, "codex") {
		t.Fatal("unbind removed the wrong tool")
	}
	// No tool means every tool.
	if n, _ = Unbind(project, ""); n != 1 {
		t.Fatalf("unbind all = %d, want 1", n)
	}
	list, _ = LoadBindings()
	if len(list) != 0 {
		t.Fatalf("%d bindings left", len(list))
	}
	// Unbinding what was never bound is not an error.
	if n, err = Unbind(project, ""); n != 0 || err != nil {
		t.Fatalf("redundant unbind = %d, %v", n, err)
	}
}

func TestBindingsFileIsPrivateAndOutsideTheProject(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	if _, _, err := Bind(project, "claude", ""); err != nil {
		t.Fatal(err)
	}
	path, err := BindingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) == project {
		t.Fatal("the whitelist lives inside the project it governs")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("bindings mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestCorruptBindingsFileDoesNotAdvertiseEverything(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := BindingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, lerr := LoadBindings()
	if lerr == nil {
		t.Fatal("a corrupt whitelist should report an error so the caller can fail closed")
	}
	// And whatever the caller does with that error, the list itself grants nothing.
	if IsBound(list, "/anywhere", "claude") {
		t.Fatal("a corrupt whitelist granted a binding")
	}
}

// The startup pass must retire sessions this device advertised before bindings
// existed — otherwise an older build's registrations stay visible forever.
func TestRetireUnboundSessionsDeregistersOnlyOurUnboundOnes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bound := t.TempDir()
	if _, _, err := Bind(bound, "claude", ""); err != nil {
		t.Fatal(err)
	}
	client := &fakeAgentClient{remote: []clicore.AgentSessionInfo{
		{SessionID: "keep-1", Tool: "claude", Project: bound, DeviceID: "this-device"},
		{SessionID: "retire-1", Tool: "claude", Project: t.TempDir(), DeviceID: "this-device"},
		{SessionID: "retire-2", Tool: "codex", Project: bound, DeviceID: "this-device"},
		{SessionID: "other-device", Tool: "claude", Project: t.TempDir(), DeviceID: "someone-else"},
	}}
	deps := Deps{DeviceSessionID: "this-device", Logf: func(string, ...any) {}}
	(&Runtime{}).retireUnboundSessions(context.Background(), client, deps)

	got := map[string]bool{}
	for _, id := range client.deregd {
		got[id] = true
	}
	if !got["retire-1"] || !got["retire-2"] {
		t.Fatalf("unbound sessions were not retired: %v", client.deregd)
	}
	if got["keep-1"] {
		t.Fatal("a bound session was retired")
	}
	if got["other-device"] {
		t.Fatal("another device's session was retired — not ours to touch")
	}
}

func TestRetireDoesNothingWithoutADeviceID(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	client := &fakeAgentClient{remote: []clicore.AgentSessionInfo{
		{SessionID: "x", Tool: "claude", Project: t.TempDir(), DeviceID: "this-device"},
	}}
	// Without knowing our own device we cannot tell our sessions from anyone
	// else's, so the safe move is to touch nothing.
	(&Runtime{}).retireUnboundSessions(context.Background(), client, Deps{Logf: func(string, ...any) {}})
	if len(client.deregd) != 0 {
		t.Fatalf("deregistered %v without knowing which device we are", client.deregd)
	}
}
