// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/share2us/cli-core/daemonctl"
)

func TestTerminalHookProofIsProcessBoundAndChannelIndependent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if TerminalHookReady("s1", 123) {
		t.Fatal("unproved session was ready")
	}
	pane := &ZellijPane{Session: "test", Pane: "1"}
	if err := ProveTerminalHook("s1", 123, pane); err != nil {
		t.Fatal(err)
	}
	if !TerminalHookReady("s1", 123) || TerminalHookReady("s1", 456) || TerminalHookReady("s2", 123) {
		t.Fatal("proof was not tied to session and process")
	}
	if got := TerminalHookPane("s1", 123); got == nil || *got != *pane || TerminalHookPane("s1", 456) != nil {
		t.Fatalf("pane proof = %+v", got)
	}
	path, _ := terminalGuardPath("s1", "proof")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("hook proof permissions = %v, %v", info, err)
	}
}

func TestTypedGuardMarkerAndTranscriptMatch(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := BeginTypedGuard("s1", "req-1"); err != nil {
		t.Fatal(err)
	}
	if TypedGuardRequest("s1") != "req-1" || TypedGuardRequest("s2") != "" {
		t.Fatal("active marker was not session-specific")
	}
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := `{"type":"user","message":{"role":"user","content":"[Share2Us] from device test, request req-1:\n\nwork"}}` + "\n" +
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"done"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if !TranscriptHasLastUserRequest(path, "req-1") || TranscriptHasLastUserRequest(path, "req-2") {
		t.Fatal("transcript request was not checked")
	}
	if err := os.WriteFile(path, []byte(content+`{"type":"user","message":{"role":"user","content":"owner's own turn"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if TranscriptHasLastUserRequest(path, "req-1") {
		t.Fatal("an unrelated owner turn matched the old delivered request")
	}
	if err := EndTypedGuard("s1", "wrong"); err != nil || TypedGuardRequest("s1") == "" {
		t.Fatal("wrong request removed the active marker")
	}
	if err := EndTypedGuard("s1", "req-1"); err != nil || TypedGuardRequest("s1") != "" {
		t.Fatal("matching request did not remove the marker")
	}
	if err := BeginTypedGuard("s1", "req-2"); err != nil {
		t.Fatal(err)
	}
	if err := ProveTerminalHook("s1", 123, &ZellijPane{Session: "test", Pane: "1"}); err != nil {
		t.Fatal(err)
	}
	EndTerminalSession("s1")
	if TypedGuardRequest("s1") != "" || TerminalHookReady("s1", 123) {
		t.Fatal("SessionEnd retained stale guard records")
	}
}

func TestStopHookCannotFinishUnrelatedTypedTurn(t *testing.T) {
	h := newChannelHub()
	result, ok := h.beginTyped("s1", ChannelDelivery{RequestID: "req-1"})
	if !ok {
		t.Fatal("begin typed")
	}
	h.turnEndedForHook("s1", "", true)
	select {
	case <-result:
		t.Fatal("unrelated Stop completed typed hop")
	default:
	}
	h.turnEndedForHook("s1", "req-1", true)
	select {
	case <-result:
	default:
		t.Fatal("matching Stop did not finish typed hop")
	}
}

func TestChannelReportStillCompletesThroughVerifiedStopControl(t *testing.T) {
	h := newChannelHub()
	_, result, ok := h.deliver("s1", ChannelDelivery{RequestID: "channel-1"})
	if !ok {
		t.Fatal("channel setup")
	}
	h.poll("s1")
	if !h.report("channel-1", "reported") {
		t.Fatal("channel report")
	}
	h.channelControl(daemonctl.Request{Op: "channel-turn-ended", Args: map[string]string{"session": "s1"}})
	select {
	case got := <-result:
		if got != "reported" {
			t.Fatalf("channel result = %q", got)
		}
	default:
		t.Fatal("verified Stop broke reported channel delivery")
	}
}
