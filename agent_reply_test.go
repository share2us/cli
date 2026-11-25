// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/share2us/cli/internal/daemon"
)

func TestWithReplyFooter(t *testing.T) {
	got := withReplyFooter("check nginx\n", false)
	if !strings.HasPrefix(got, "check nginx\n\n") || !strings.HasSuffix(got, replyFooter) {
		t.Fatalf("got %q", got)
	}
	if withReplyFooter(got, false) != got {
		t.Fatal("footer added twice")
	}
	if withReplyFooter("drop this", true) != "drop this" {
		t.Fatal("an inbox drop runs nothing, so it gets no reply instruction")
	}
}

// The Stop hook can run before the final message reaches the transcript: fall back to
// the hook's own last-message field rather than reporting "no report".
func TestStopHookReplyFallsBackToLastAssistantMessage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	_ = os.WriteFile(p, []byte(`{"type":"user","message":{"role":"user","content":"[Share2Us] from device d, request r1:\n\nping"}}`+"\n"), 0o600)
	if got := stopHookReply(hookInput{TranscriptPath: p, LastAssistantMessage: " pong "}, "r1"); got != "pong" {
		t.Fatalf("got %q", got)
	}
	if got := stopHookReply(hookInput{LastAssistantMessage: "x"}, ""); got != "" {
		t.Fatalf("no request, no reply: %q", got)
	}
}

func TestLaunchSettingsUseTheV2SessionHook(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, settings, err := daemon.WriteChannelLaunchConfig("/usr/bin/s2u")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(settings)
	if !strings.Contains(string(b), "agent hook "+daemon.SessionStartHookV2) || !strings.Contains(string(b), "agent hook user-prompt-submit") {
		t.Fatalf("settings = %s", b)
	}
}
