// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	clicore "github.com/share2us/cli-core"
)

func waitForReport(t *testing.T, c *fakeAgentClient, status, contains string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		for _, r := range c.reportsSnapshot() {
			if r[0] == status && strings.Contains(r[1], contains) {
				return
			}
		}
		select {
		case <-c.reportNotify:
		case <-deadline:
			t.Fatalf("no %q report containing %q; reports = %v", status, contains, c.reportsSnapshot())
		}
	}
}

// The paste could not be confirmed at once (Claude was mid-render or the owner had typed
// something), but a re-check finds exactly our text: Share2Us presses Enter, once. The
// bug was giving up on "input changed" forever.
func TestUnconfirmedPasteIsSubmittedWhenTheInputIsExactlyOurs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := unverifiedRecheck
	unverifiedRecheck = []time.Duration{5 * time.Millisecond, 5 * time.Millisecond}
	t.Cleanup(func() { unverifiedRecheck = old })
	runtime, z, dir := typedRuntime(t,
		"│ ❯  │", // idle before the paste
		"│ ❯ owner text [Pasted text +3 lines] │", // verification fails
		"│ ❯ [Pasted text +5 lines] │",            // re-check: only our text (header, prompt, reply note)
	)
	c := &fakeAgentClient{reportNotify: make(chan struct{}, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime.handleInject(ctx, c, typedRunner(dir), keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-recheck", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "remote text"}))
	waitForReport(t, c, "running", "Delivered")
	waitFor(t, func() bool { return z.enters() == 1 })
	if len(z.pasted) != 1 {
		t.Fatalf("paste=%d, want no second paste", len(z.pasted))
	}
	// The receiving agent must learn that its reply returns by itself (bug 2026-11-26).
	if !strings.Contains(z.pasted[0], "request req-recheck:") || !strings.HasSuffix(z.pasted[0], typedReplyNote) {
		t.Fatalf("typed text = %q", z.pasted[0])
	}
}

// Someone else's text stays in the input: never submitted, and after a few checks the
// sender hears the delivery is stuck.
func TestForeignInputIsNeverSubmittedAndReportsStuck(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := unverifiedRecheck
	unverifiedRecheck = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { unverifiedRecheck = old })
	screens := []string{"│ ❯  │", "│ ❯ owner text [Pasted text +3 lines] │"}
	for i := 0; i < stuckAfterChecks+1; i++ {
		screens = append(screens, "│ ❯ owner text [Pasted text +3 lines] │")
	}
	runtime, z, dir := typedRuntime(t, screens...)
	c := &fakeAgentClient{reportNotify: make(chan struct{}, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime.handleInject(ctx, c, typedRunner(dir), keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-stuck", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "remote text"}))
	waitForReport(t, c, "waiting", "Stuck: input not clean")
	if z.enters() != 0 {
		t.Fatalf("pressed Enter on someone else's text (%d)", z.enters())
	}
	if active, _ := runtime.hub().guarded("win-1"); !active {
		t.Fatal("guard dropped while remote text may still be in the input")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// A folded paste marker proves nothing once the owner could have pasted their own text:
// it counts only during the first re-checks, and only with our line count.
func TestOurInputExactly(t *testing.T) {
	visible := "line one\nline two\nline three"
	tests := []struct {
		name  string
		input string
		early bool
		want  bool
	}{
		{"exact text", "line one line two line three", false, true},
		{"marker early, our count", "[Pasted text #1 +3 lines]", true, true},
		{"marker early, count minus one", "[Pasted text +2 lines]", true, true},
		{"marker late", "[Pasted text +3 lines]", false, false},
		{"marker with other count", "[Pasted text +9 lines]", true, false},
		{"marker plus owner text", "hi [Pasted text +3 lines]", true, false},
		{"owner text", "something else", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ourInputExactly(claudeFixture("│ ❯ "+tt.input+" │"), visible, tt.early); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTranscriptLastReply(t *testing.T) {
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"earlier owner prompt"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"old answer"}]}}`,
		`{"type":"user","message":{"role":"user","content":"[Share2Us] from device dev-1, request req-9:\n\ndo it"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"working on it"},{"type":"tool_use","name":"Bash"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done: 3 files changed."}]}}`,
		`{"type":"user","message":{"role":"user","content":"a later owner prompt"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"unrelated"}]}}`,
	}
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := TranscriptLastReply(p, "req-9"); got != "Done: 3 files changed." {
		t.Fatalf("reply = %q", got)
	}
	if got := TranscriptLastReply(p, "req-other"); got != "" {
		t.Fatalf("a reply for a request that isn't in the transcript: %q", got)
	}
}
