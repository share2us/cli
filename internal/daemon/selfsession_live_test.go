// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"os"
	"strconv"
	"testing"
)

// Run by hand from inside a Claude session: S2U_LIVE_SESSION=1 go test -run Live
func TestLiveFindOwnSession(t *testing.T) {
	if os.Getenv("S2U_LIVE_SESSION") == "" {
		t.Skip("set S2U_LIVE_SESSION=1 inside a Claude session")
	}
	s, err := FindOwnSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("found session %s (%s) in %s, status %s", s.SessionID, s.Tool, s.Project, s.Status)
}

// By hand, against a running Codex: S2U_CODEX_PID=<pid of the codex process> go test -run LiveCodex
func TestLiveCodexSessionOf(t *testing.T) {
	pid, _ := strconv.Atoi(os.Getenv("S2U_CODEX_PID"))
	if pid == 0 {
		t.Skip("set S2U_CODEX_PID to a running codex process")
	}
	s, ok := codexSessionOf(pid)
	if !ok {
		t.Fatal("no Codex session found for that pid")
	}
	t.Logf("found codex session %s in %s", s.SessionID, s.Project)
}
