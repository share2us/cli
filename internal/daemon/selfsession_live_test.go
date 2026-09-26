// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"os"
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
