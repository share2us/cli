// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	clicore "github.com/share2us/cli-core"
)

// A window that is working (input empty) gets the prompt typed anyway once the wait is
// up; Claude queues it behind the running turn (owner, 2026-11-26).
func TestBusyWindowGetsThePromptQueued(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := typedQueueAfter
	typedQueueAfter = 0
	t.Cleanup(func() { typedQueueAfter = old })
	runtime, z, dir := typedRuntime(t,
		"│ ❯  │\nesc to interrupt",                          // working, input empty
		"│ ❯ [Pasted text #1 +5 lines] │\nesc to interrupt", // our paste, still working
	)
	c := &fakeAgentClient{reportNotify: make(chan struct{}, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime.handleInject(ctx, c, typedRunner(dir), keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-queued", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "ping"}))
	waitForReport(t, c, "running", "runs right after the current turn")
	if z.enters() != 1 {
		t.Fatalf("enter=%d, want the queued prompt submitted once", z.enters())
	}
	// Until Claude submits it, the turn running now is the owner's.
	tr := filepath.Join(t.TempDir(), "t.jsonl")
	_ = os.WriteFile(tr, []byte(`{"type":"user","message":{"role":"user","content":"owner's own prompt"}}`+"\n"), 0o600)
	if !TypedHopNotStarted("win-1", tr) {
		t.Fatal("the owner's running turn was treated as the hop")
	}
	_ = os.WriteFile(tr, []byte(`{"type":"user","message":{"role":"user","content":"[Share2Us] from device d, request req-queued:\n\nping"}}`+"\n"), 0o600)
	if TypedHopNotStarted("win-1", tr) {
		t.Fatal("the submitted hop ran without its guard")
	}
	if TypedHopNotStarted("win-1", filepath.Join(t.TempDir(), "missing.jsonl")) {
		t.Fatal("an unreadable transcript must count as started (fail closed)")
	}
}

// Someone's unsent text in the input is never typed over; the sender is told what blocks.
func TestDraftInTheInputIsNeverTypedOver(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	oldQ, oldP := typedQueueAfter, injectHoldPoll
	typedQueueAfter, injectHoldPoll = 0, time.Millisecond
	t.Cleanup(func() { typedQueueAfter, injectHoldPoll = oldQ, oldP })
	screens := []string{}
	for i := 0; i < 20; i++ {
		screens = append(screens, "│ ❯ my half-written draft │\nesc to interrupt")
	}
	runtime, z, dir := typedRuntime(t, screens...)
	c := &fakeAgentClient{reportNotify: make(chan struct{}, 32)}
	ctx, cancel := context.WithCancel(context.Background())
	runtime.handleInject(ctx, c, typedRunner(dir), keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-draft", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "ping"}))
	waitForReport(t, c, "waiting", `its input holds unsent text ("my half-written draft")`)
	cancel()
	waitIdle(t, runtime)
	if z.enters() != 0 {
		t.Fatalf("pressed Enter over the owner's draft (%d)", z.enters())
	}
}
