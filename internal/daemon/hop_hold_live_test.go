// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"os"
	"testing"
	"time"

	clicore "github.com/share2us/cli-core"
)

// By hand, against a real Claude session that a window has open:
//
//	S2U_LIVE_HOLD_SESSION=<session id> S2U_LIVE_HOLD_DIR=<its folder> go test -run LiveHold -v
//
// The hop must wait while the window is open, and run IN that session (same id,
// binding unchanged) once the window closes. Close the window within 10 minutes.
func TestLiveHoldThenRunInPlace(t *testing.T) {
	sid, dir := os.Getenv("S2U_LIVE_HOLD_SESSION"), os.Getenv("S2U_LIVE_HOLD_DIR")
	if sid == "" || dir == "" {
		t.Skip("set S2U_LIVE_HOLD_SESSION and S2U_LIVE_HOLD_DIR to a session a Claude window has open")
	}
	poll := injectHoldPoll
	injectHoldPoll = 2 * time.Second
	t.Cleanup(func() { injectHoldPoll = poll })
	if _, _, err := BindSession(dir, "claude", "", sid); err != nil {
		t.Fatal(err)
	}
	r := ClaudeRunner{}
	if !sessionHeld(context.Background(), r, sid) {
		t.Fatal("the session is not open in a window: open it first")
	}
	c := &fakeAgentClient{}
	runtime := rt()
	runtime.handleInject(context.Background(), c, r, keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "live-1", Tool: "claude", TargetSessionID: sid, SealedPrompt: "Reply with exactly: from s2u"}))
	if runtime.holding.Load() != 1 {
		t.Fatalf("the hop did not wait for the open window (holding %d)", runtime.holding.Load())
	}
	t.Logf("%s waiting: the hop is held while the window is open", time.Now().Format(time.TimeOnly))
	deadline := time.Now().Add(10 * time.Minute)
	for runtime.holding.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Second)
	}
	t.Logf("%s released: reports %v", time.Now().Format(time.TimeOnly), c.reports)
	if len(c.reports) != 3 || c.reports[0][0] != "waiting" || c.reports[2][0] != "done" {
		t.Fatalf("reports = %v, want waiting, running, done", c.reports)
	}
	hops, _ := LoadHops(0)
	if len(hops) != 1 || hops[0].RanIn != sid || hops[0].Mode != "resumed" || hops[0].Waited == "" {
		t.Fatalf("hop log = %+v", hops)
	}
	list, _ := LoadBindings()
	if _, ok := BindingForSession(list, sid); !ok {
		t.Fatalf("the binding moved: %+v", list)
	}
	t.Logf("ran in %s after waiting %s", hops[0].RanIn, hops[0].Waited)
}
