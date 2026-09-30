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

func TestTypedDeliveryCapCooldownAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "typed-usage.json")
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		at     time.Duration
		count  int
		reason string
	}{
		{0, 1, ""},
		{time.Minute, 1, "cooldown active"},
		{5 * time.Minute, 2, ""},
		{10 * time.Minute, 3, ""},
		{20 * time.Minute, 3, "hourly cap reached"},
		{time.Hour, 3, ""}, // first entry aged out; restart still enforces the others
	} {
		count, reason, err := reserveTypedDeliveryAt(path, "session-1", start.Add(tc.at))
		if err != nil || count != tc.count || reason != tc.reason {
			t.Errorf("at %s: count=%d reason=%q err=%v, want %d %q", tc.at, count, reason, err, tc.count, tc.reason)
		}
	}
	if count, reason, err := reserveTypedDeliveryAt(path, "session-2", start.Add(time.Minute)); err != nil || count != 1 || reason != "" {
		t.Errorf("another session: count=%d reason=%q err=%v", count, reason, err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reserveTypedDeliveryAt(path, "session-1", start.Add(2*time.Hour)); err == nil {
		t.Fatal("corrupt persisted counter reset the delivery limit")
	}
}

func TestTypedDeliveryOwnerSwitchBlocksPaste(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	runtime, z, dir := typedRuntime(t, "│ ❯  │")
	old, ok := BindingForSession(mustBindings(t), "win-1")
	if !ok {
		t.Fatal("test binding missing")
	}
	b, err := SetTypedDelivery(dir, false)
	if err != nil || !b.TypedDeliveryDisabled || b.AgentID != old.AgentID {
		t.Fatalf("switch off: binding=%+v err=%v", b, err)
	}
	b, ok = BindingForSession(mustBindings(t), "win-1")
	if !ok || !b.TypedDeliveryDisabled {
		t.Fatal("typed switch was not persisted")
	}
	req := signedReq(t, clicore.AgentRequest{ID: "req-switch-off", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "read only"})
	if runtime.tryTypedInject(context.Background(), &fakeAgentClient{}, typedRunner(dir), keyedDeps(), req, b,
		DiscoveredSession{SessionID: "win-1", Tool: "claude", Project: dir, PID: 123, Status: "available", Live: true}, "read only", "read only", "sender", dir, 0) {
		t.Fatal("owner-disabled binding accepted automatic typing")
	}
	if len(z.pasted) != 0 || z.entered != 0 {
		t.Fatalf("owner-disabled binding touched pane: pasted=%v entered=%d", z.pasted, z.entered)
	}
	b, err = SetTypedDelivery(dir, true)
	if err != nil || b.TypedDeliveryDisabled || b.AgentID != old.AgentID {
		t.Fatalf("switch on: binding=%+v err=%v", b, err)
	}
}

func TestTypedDeliveryCountsReachedInput(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	runtime, z, dir := typedRuntime(t, "│ ❯  │", "│ ❯  │", "│ ❯ [Pasted text #1 +3 lines] │")
	b, _ := BindingForSession(mustBindings(t), "win-1")
	req := signedReq(t, clicore.AgentRequest{ID: "req-count", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "read only"})
	usagePath, err := typedUsagePath()
	if err != nil {
		t.Fatal(err)
	}
	// A pre-existing recent delivery makes this request wait instead of typing.
	if _, reason, err := reserveTypedDeliveryAt(usagePath, "win-1", time.Now()); err != nil || reason != "" {
		t.Fatalf("seed counter: reason=%q err=%v", reason, err)
	}
	if runtime.tryTypedInject(context.Background(), &fakeAgentClient{}, typedRunner(dir), keyedDeps(), req, b,
		DiscoveredSession{SessionID: "win-1", Tool: "claude", Project: dir, PID: 123, Status: "available", Live: true}, "read only", "read only", "sender", dir, 0) {
		t.Fatal("cooldown accepted a second typed prompt")
	}
	if len(z.pasted) != 0 || z.entered != 0 {
		t.Fatalf("cooldown touched pane: pasted=%v entered=%d", z.pasted, z.entered)
	}
	if got := TypedGuardRequest("win-1"); strings.Contains(got, "req-count") {
		t.Fatalf("cooldown left a guard marker for %q", got)
	}
}

func mustBindings(t *testing.T) []Binding {
	t.Helper()
	list, err := LoadBindings()
	if err != nil {
		t.Fatal(err)
	}
	return list
}
