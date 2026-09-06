package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseCodexIndex(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-09-07T12:00:00Z")
	lines := []string{
		`{"id":"a","thread_name":"recent one","updated_at":"2026-09-07T10:00:00Z"}`,
		`{"id":"b","thread_name":"too old","updated_at":"2026-08-01T10:00:00Z"}`,
		`{"id":"a","thread_name":"a revised","updated_at":"2026-09-07T11:00:00Z"}`, // newer dup of a
		`not json`,
		`{"id":"","thread_name":"noid","updated_at":"2026-09-07T11:00:00Z"}`,
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "session_index.jsonl")
	os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600)

	got, err := parseCodexIndex(p, now)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]DiscoveredSession{}
	for _, s := range got {
		byID[s.SessionID] = s
	}
	if _, ok := byID["b"]; ok {
		t.Error("stale session should be filtered")
	}
	if _, ok := byID[""]; ok {
		t.Error("empty id should be skipped")
	}
	if len(byID) != 1 {
		t.Fatalf("want 1 session (a), got %d", len(byID))
	}
	if byID["a"].Tool != "codex" || byID["a"].Name != "a revised" || byID["a"].Status != "unknown" {
		t.Fatalf("session a = %+v (want codex/'a revised'/unknown)", byID["a"])
	}
}

func TestParseCodexIndexMissingFile(t *testing.T) {
	got, err := parseCodexIndex(filepath.Join(t.TempDir(), "nope.jsonl"), time.Now())
	if err != nil || got != nil {
		t.Fatalf("missing index should be (nil,nil), got (%v,%v)", got, err)
	}
}

func TestBuildCodexInjectArgs(t *testing.T) {
	args := buildCodexInjectArgs("sess-9", "/repo", "do it")
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
	if args[len(args)-2] != "sess-9" || args[len(args)-1] != "do it" {
		t.Fatalf("session id + prompt must be the trailing positionals: %v", args)
	}
}
