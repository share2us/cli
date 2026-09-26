// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"errors"
	"os"
	"testing"
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
	s, err := ownSession(900, sessions, parent)
	if err != nil || s.SessionID != "sess-b" || s.Project != "/p/b" {
		t.Fatalf("own session = %+v, %v; want sess-b (the ancestor), not sess-a", s, err)
	}
	if _, err := ownSession(700, sessions, parent); !errors.Is(err, ErrNotInSession) {
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
