// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestOwnSessionWalksUpToTheSessionProcess(t *testing.T) {
	sessions, err := claudeSessionsByPID([]byte(`[
	  {"pid":500,"cwd":"/p/a","kind":"interactive","sessionId":"sess-a","name":"a","status":"idle"},
	  {"pid":600,"cwd":"/p/b","kind":"interactive","sessionId":"sess-b","status":"busy"},
	  {"cwd":"/p/c","kind":"background","sessionId":"sess-c","state":"blocked"}
	]`))
	if err != nil || len(sessions) != 2 {
		t.Fatalf("sessions = %v, %v (entries without a pid are not processes)", sessions, err)
	}
	tree := map[int]int{900: 800, 800: 600, 600: 1}
	parent := func(p int) (int, error) {
		if q, ok := tree[p]; ok {
			return q, nil
		}
		return 0, errors.New("gone")
	}
	match := func(p int) (DiscoveredSession, bool) { s, ok := sessions[p]; return s, ok }
	s, err := ownSession(900, match, parent)
	if err != nil || s.SessionID != "sess-b" || s.Project != "/p/b" {
		t.Fatalf("own session = %+v, %v; want sess-b (the ancestor), not sess-a", s, err)
	}
	if _, err := ownSession(700, match, parent); !errors.Is(err, ErrNotInSession) {
		t.Fatalf("a process outside any session = %v, want ErrNotInSession", err)
	}
}

// The real process tree on this machine: our parent chain can be read.
func TestParentPIDReadsTheRealTree(t *testing.T) {
	pp, err := parentPID(os.Getpid())
	if err != nil {
		t.Skipf("parentPID unsupported here: %v", err)
	}
	if pp != os.Getppid() {
		t.Fatalf("parentPID = %d, os.Getppid = %d", pp, os.Getppid())
	}
}

// A Codex session is the process holding its rollout file open. Hold one open in
// a real child process and find the session from that pid alone.
func TestCodexSessionIsFoundFromTheOpenRollout(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("open-file listing is linux/darwin only")
	}
	dir := filepath.Join(t.TempDir(), ".codex", "sessions", "2026", "09", "27")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-09-27T10-00-00-0199aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee.jsonl")
	meta := `{"type":"session_meta","payload":{"session_id":"0199aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee","cwd":"/work/proj"}}` + "\n"
	if err := os.WriteFile(path, []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "5")
	cmd.ExtraFiles = []*os.File{f} // the child holds the rollout open, as codex does
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	_ = f.Close() // only the child holds it now
	time.Sleep(200 * time.Millisecond)
	s, ok := codexSessionOf(cmd.Process.Pid)
	if !ok || s.SessionID != "0199aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee" || s.Tool != "codex" || s.Project != "/work/proj" {
		t.Fatalf("codex session = %+v, %v", s, ok)
	}
	// A process with no rollout open is not a Codex session.
	if _, ok := codexSessionOf(os.Getpid()); ok {
		t.Fatal("the test process was mistaken for a Codex session")
	}
}
