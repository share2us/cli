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
	args := buildCodexInjectArgs("sess-9", "do it", codexSandbox(false))
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
	if got := codexSandbox(true); got != "read-only" {
		t.Fatalf("strict must be read-only, got %q", got)
	}
}
