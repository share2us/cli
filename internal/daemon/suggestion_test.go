// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"testing"
	"time"

	clicore "github.com/share2us/cli-core"
)

// Claude's Tab-to-accept suggestion looks like typed text in a dump. One probe key shows it
// is a suggestion (the input becomes just "x"); the prompt is then typed and sent (owner
// report 2026-11-26: "the daemon does not write").
func TestSuggestionIsTypedOver(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	oldP, oldW := injectHoldPoll, suggestionProbeWait
	injectHoldPoll, suggestionProbeWait = time.Millisecond, 0
	t.Cleanup(func() { injectHoldPoll, suggestionProbeWait = oldP, oldW })
	runtime, z, dir := typedRuntime(t,
		"│ ❯ ! ip -4 addr │", // first look: not yet known to be stable
		"│ ❯ ! ip -4 addr │", // same text: probe it
		"│ ❯ x │",            // the probe key replaced it: a suggestion
		"│ ❯ [Pasted text #1 +5 lines] │",
	)
	c := &fakeAgentClient{reportNotify: make(chan struct{}, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime.handleInject(ctx, c, typedRunner(dir), keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-sugg", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "ping"}))
	waitFor(t, func() bool { return z.enters() == 1 })
	z.mu.Lock()
	keys := append([]string(nil), z.keys...)
	z.mu.Unlock()
	if len(keys) != 2 || keys[0] != "x" || keys[1] != "Backspace" {
		t.Fatalf("probe keys = %v", keys)
	}
}

// A real draft keeps its text (the probe character is removed) and is probed only once.
func TestDraftIsProbedOnceAndKept(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	oldP, oldW := injectHoldPoll, suggestionProbeWait
	injectHoldPoll, suggestionProbeWait = time.Millisecond, 0
	t.Cleanup(func() { injectHoldPoll, suggestionProbeWait = oldP, oldW })
	screens := []string{"│ ❯ my draft │", "│ ❯ my draft │", "│ ❯ my draftx │"}
	for i := 0; i < 15; i++ {
		screens = append(screens, "│ ❯ my draft │")
	}
	runtime, z, dir := typedRuntime(t, screens...)
	c := &fakeAgentClient{reportNotify: make(chan struct{}, 32)}
	ctx, cancel := context.WithCancel(context.Background())
	runtime.handleInject(ctx, c, typedRunner(dir), keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-draft2", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "ping"}))
	waitForReport(t, c, "waiting", "unsent text")
	time.Sleep(30 * time.Millisecond)
	cancel()
	waitIdle(t, runtime)
	z.mu.Lock()
	keys := append([]string(nil), z.keys...)
	z.mu.Unlock()
	if z.enters() != 0 || len(keys) != 2 {
		t.Fatalf("enters=%d keys=%v (want no Enter and a single x+Backspace probe)", z.enters(), keys)
	}
}
