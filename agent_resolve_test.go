// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/share2us/cli/internal/daemon"
)

// resolveFixture isolates the bindings file, the agent env and the process-tree
// lookup, and returns a scratch tree to bind folders in.
func resolveFixture(t *testing.T) string {
	t.Helper()
	setConfigHome(t, t.TempDir())
	t.Setenv(daemon.AgentEnvVar, "")
	prev := ownSessionID
	ownSessionID = func(context.Context) string { return "" }
	t.Cleanup(func() { ownSessionID = prev })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func bindDir(t *testing.T, dir, tool, session string) string {
	t.Helper()
	b, _, err := daemon.BindSession(dir, tool, "", session)
	if err != nil {
		t.Fatal(err)
	}
	return b.AgentID
}

func TestResolveDotUsesSessionEnvFirst(t *testing.T) {
	root := resolveFixture(t)
	proj := mkdir(t, root, "proj")
	other := mkdir(t, root, "other")
	want := bindDir(t, proj, "claude", "sess-1")
	bindDir(t, other, "claude", "sess-2")
	t.Chdir(other) // cwd has its own binding; the session's agent still wins
	t.Setenv(daemon.AgentEnvVar, want)
	got, err := resolveLocalAgent(context.Background(), ".")
	if err != nil || got != want {
		t.Fatalf("session env: got %q %v, want %q", got, err, want)
	}
}

func TestResolveDotUsesOwnSessionFromProcessTree(t *testing.T) {
	root := resolveFixture(t)
	proj := mkdir(t, root, "proj")
	elsewhere := mkdir(t, root, "elsewhere")
	want := bindDir(t, proj, "claude", "sess-1")
	ownSessionID = func(context.Context) string { return "sess-1" }
	t.Chdir(elsewhere)
	got, err := resolveLocalAgent(context.Background(), ".")
	if err != nil || got != want {
		t.Fatalf("own session: got %q %v, want %q", got, err, want)
	}
}

func TestResolveDotExactFolder(t *testing.T) {
	root := resolveFixture(t)
	proj := mkdir(t, root, "proj")
	want := bindDir(t, proj, "claude", "")
	t.Chdir(proj)
	got, err := resolveLocalAgent(context.Background(), ".")
	if err != nil || got != want {
		t.Fatalf("exact: got %q %v, want %q", got, err, want)
	}
}

func TestResolveDotNearestBoundAncestor(t *testing.T) {
	root := resolveFixture(t)
	proj := mkdir(t, root, "proj")
	sub := mkdir(t, proj, "repos", "sub")
	bindDir(t, root, "claude", "")
	want := bindDir(t, proj, "claude", "")
	t.Chdir(sub)
	got, err := resolveLocalAgent(context.Background(), ".")
	if err != nil || got != want {
		t.Fatalf("ancestor: got %q %v, want nearest %q", got, err, want)
	}
}

func TestResolveDotAmbiguousListsCandidates(t *testing.T) {
	root := resolveFixture(t)
	proj := mkdir(t, root, "proj")
	a := bindDir(t, proj, "claude", "")
	b := bindDir(t, proj, "codex", "")
	t.Chdir(proj)
	_, err := resolveLocalAgent(context.Background(), ".")
	if err == nil || !strings.Contains(err.Error(), a) || !strings.Contains(err.Error(), b) {
		t.Fatalf("ambiguous must list both agents: %v", err)
	}
}

func TestResolveDotNoneButBoundElsewhere(t *testing.T) {
	root := resolveFixture(t)
	proj := mkdir(t, root, "proj")
	elsewhere := mkdir(t, root, "elsewhere")
	id := bindDir(t, proj, "claude", "")
	t.Chdir(elsewhere)
	_, err := resolveLocalAgent(context.Background(), ".")
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	for _, want := range []string{"no agent is bound at " + elsewhere, proj, id} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in %q", want, msg)
		}
	}
	if strings.Contains(msg, "agent bind") {
		t.Fatalf("must not suggest bind when agents are bound elsewhere: %q", msg)
	}
}

func TestResolveDotNoBindingsAtAll(t *testing.T) {
	root := resolveFixture(t)
	t.Chdir(root)
	_, err := resolveLocalAgent(context.Background(), ".")
	if err == nil || !strings.Contains(err.Error(), "agent bind") {
		t.Fatalf("no bindings: should suggest bind: %v", err)
	}
}

func TestResolvePathArgument(t *testing.T) {
	root := resolveFixture(t)
	proj := mkdir(t, root, "proj")
	sub := mkdir(t, proj, "repos", "sub")
	unbound := mkdir(t, root, "unbound")
	want := bindDir(t, proj, "claude", "")
	t.Chdir(root)
	ctx := context.Background()

	for _, q := range []string{proj, sub, "./proj", "proj/repos/sub"} {
		if !isAgentPathArg(q) {
			t.Fatalf("%q should be read as a path", q)
		}
		got, err := resolveLocalAgent(ctx, q)
		if err != nil || got != want {
			t.Fatalf("path %q: got %q %v, want %q", q, got, err, want)
		}
	}
	t.Chdir(sub)
	if got, err := resolveLocalAgent(ctx, "../.."); err != nil || got != want {
		t.Fatalf("relative ../..: got %q %v", got, err)
	}
	_, err := resolveLocalAgent(ctx, unbound)
	if err == nil || !strings.Contains(err.Error(), "no agent is bound at "+unbound) {
		t.Fatalf("unbound path: %v", err)
	}
	if strings.Contains(err.Error(), "no reachable agent matches") {
		t.Fatalf("a path must never be reported as an agent name: %v", err)
	}
	for _, q := range []string{"reviewer", "agt_x", "butterpos.api"} {
		if isAgentPathArg(q) {
			t.Fatalf("%q is a name, not a path", q)
		}
	}
}

func TestOwnAgentIDPathNeverFallsToNameLookup(t *testing.T) {
	root := resolveFixture(t)
	unbound := mkdir(t, root, "unbound")
	_, err := app{}.ownAgentID(context.Background(), nil, unbound)
	if err == nil || !strings.Contains(err.Error(), "no agent is bound at "+unbound) {
		t.Fatalf("got %v", err)
	}
}
