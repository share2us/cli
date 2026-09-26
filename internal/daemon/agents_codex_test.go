// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeRollout drops a rollout file with the given header + body lines and mtime.
func writeRollout(t *testing.T, root, name string, mtime time.Time, lines ...string) string {
	t.Helper()
	dir := filepath.Join(root, "2026", "09", "07")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

func meta(id, cwd string) string {
	return `{"type":"session_meta","payload":{"session_id":"` + id + `","cwd":"` + cwd + `"}}`
}

func userMsg(text string) string {
	return `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` + text + `"}]}}`
}

// The real store shape (verified against codex 0.152.0): the header carries the
// cwd, which is what makes `codex exec resume` able to find the session at all.
func TestDiscoverCodexReadsRolloutStore(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-09-07T12:00:00Z")
	root := t.TempDir()
	writeRollout(t, root, "rollout-2026-09-07T11-00-00-aaa.jsonl", now.Add(-1*time.Hour),
		meta("aaa", "/home/x/proj"),
		`{"type":"response_item","payload":{"role":"developer","content":[{"text":"system"}]}}`,
		userMsg("<recommended_plugins>synthetic block</recommended_plugins>"),
		userMsg("fix the failing test"),
	)
	writeRollout(t, root, "rollout-2026-08-01T10-00-00-old.jsonl", now.Add(-30*24*time.Hour),
		meta("old", "/home/x/stale"), userMsg("ancient"))
	writeRollout(t, root, "notes.txt", now, "not a rollout")

	got, err := discoverCodexIn(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want only the recent rollout, got %+v", got)
	}
	s := got[0]
	if s.SessionID != "aaa" || s.Tool != "codex" {
		t.Fatalf("bad session identity: %+v", s)
	}
	// The cwd is the whole point of switching stores: without it, resume fails.
	if s.Project != "/home/x/proj" {
		t.Fatalf("Project must carry the session cwd, got %q", s.Project)
	}
	// Synthetic <...> context blocks are not a usable display name.
	if s.Name != "fix the failing test" {
		t.Fatalf("name should come from the first real user message, got %q", s.Name)
	}
}

func TestDiscoverCodexMissingRoot(t *testing.T) {
	got, err := discoverCodexIn(filepath.Join(t.TempDir(), "nope"), time.Now())
	if err != nil || got != nil {
		t.Fatalf("missing store should be (nil,nil), got (%v,%v)", got, err)
	}
}

func TestBuildCodexInjectArgs(t *testing.T) {
	args := buildCodexInjectArgs("sess-9", "do it", codexSandbox(PrivilegeStandard))
	joined := strings.Join(args, " ")
	if !strings.HasPrefix(joined, "exec resume") {
		t.Fatalf("must use exec resume: %v", args)
	}
	if !strings.Contains(joined, "sandbox_mode=workspace-write") {
		t.Fatalf("must set a restricted sandbox: %v", args)
	}
	if strings.Contains(joined, "dangerously") {
		t.Fatalf("must never bypass the sandbox: %v", args)
	}
	// Regression: sandbox_mode alone is escalatable. Verified 2026-09-07 that a
	// read-only inject WROTE a file until approval_policy=never was pinned.
	if !strings.Contains(joined, "approval_policy=never") {
		t.Fatalf("sandbox is escalatable without approval_policy=never: %v", args)
	}
	// Verified 2026-09-07: `codex exec resume` rejects -C ("unexpected argument")
	// and has no --sandbox; cwd comes from the process dir instead.
	for _, bad := range []string{"-C", "--sandbox"} {
		for _, a := range args {
			if a == bad {
				t.Fatalf("%s is not accepted by `codex exec resume`: %v", bad, args)
			}
		}
	}
	if args[len(args)-2] != "sess-9" || args[len(args)-1] != "do it" {
		t.Fatalf("session id + prompt must be the trailing positionals: %v", args)
	}
}

func TestCodexStrictIsReadOnly(t *testing.T) {
	if got := codexSandbox(PrivilegeRestricted); got != "read-only" {
		t.Fatalf("strict must be read-only, got %q", got)
	}
}

// Presence for Codex: busy while its rollout is being written, available
// otherwise (the daemon can resume a quiet session headlessly).
func TestCodexPresenceFromRolloutWrites(t *testing.T) {
	now := time.Now()
	if got := codexStatus(now.Add(-3*time.Second), now); got != "busy" {
		t.Fatalf("just written = %q, want busy", got)
	}
	if got := codexStatus(now.Add(-5*time.Minute), now); got != "available" {
		t.Fatalf("quiet for minutes = %q, want available", got)
	}

	root := t.TempDir()
	dir := filepath.Join(root, "2026", "09", "27")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, id string, mod time.Time) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(`{"type":"session_meta","payload":{"session_id":"`+id+`","cwd":"/p"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	write("rollout-a.jsonl", "working", now.Add(-2*time.Second))
	write("rollout-b.jsonl", "idle", now.Add(-10*time.Minute))
	got, err := discoverCodexIn(root, now)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, s := range got {
		status[s.SessionID] = s.Status
	}
	if status["working"] != "busy" || status["idle"] != "available" {
		t.Fatalf("discovered presence = %v, want working=busy idle=available (never unknown)", status)
	}
}

// A sub-agent (codex 0.156.1's "guardian" review) writes its own rollout that
// repeats the parent's session_id. Listing it made one session look like two, so
// `agent bind <id>` refused it as ambiguous.
func TestDiscoverCodexSkipsSubagentRollouts(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-09-07T12:00:00Z")
	root := t.TempDir()
	writeRollout(t, root, "rollout-2026-09-07T11-00-00-parent.jsonl", now.Add(-time.Hour),
		`{"type":"session_meta","payload":{"session_id":"parent","id":"parent","parent_thread_id":null,"cwd":"/home/x/proj","source":"cli","thread_source":"user"}}`,
		userMsg("# AGENTS.md instructions for /home/x/proj"), userMsg("real work"))
	writeRollout(t, root, "rollout-2026-09-07T11-30-00-guard.jsonl", now.Add(-time.Minute),
		`{"type":"session_meta","payload":{"session_id":"parent","id":"guard","parent_thread_id":"parent","cwd":"/home/x/proj","source":{"subagent":{"other":"guardian"}}}}`)
	writeRollout(t, root, "rollout-2026-09-07T11-40-00-sub2.jsonl", now.Add(-time.Minute),
		`{"type":"session_meta","payload":{"session_id":"parent","id":"sub2","cwd":"/home/x/proj","source":{"subagent":"review"}}}`)

	got, err := discoverCodexIn(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "parent" || got[0].Name != "real work" {
		t.Fatalf("want only the parent session, got %+v", got)
	}
}
