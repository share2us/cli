// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The bridge is on by default but idle until something is bound, and wakes when
// a binding appears later (owner, 2026-09-27).
func TestBridgeWaitsForABindingThenWakes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- waitForBinding(ctx, 20*time.Millisecond) }()
	select {
	case <-done:
		t.Fatal("the bridge woke with nothing bound")
	case <-time.After(150 * time.Millisecond):
	}
	if _, _, err := Bind(t.TempDir(), "claude", "later"); err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("waitForBinding returned false after a binding appeared")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the bridge did not wake after a binding appeared")
	}
}

func TestBridgeStopsWaitingOnShutdown(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitForBinding(ctx, time.Hour) {
		t.Fatal("waitForBinding reported a binding after shutdown")
	}
}

func TestBridgeRefusalsAreRecognised(t *testing.T) {
	for msg, want := range map[string]bool{
		"agent_bridge_not_allowed: the agent bridge is not available on this plan": true,
		"agent_bridge_disabled: the agent bridge is not available yet":             true,
		"dial tcp: connection refused":                                             false,
	} {
		if got := bridgeRefused(errors.New(msg)); got != want {
			t.Errorf("bridgeRefused(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestOnceLoggerSuppressesRepeats(t *testing.T) {
	var got []string
	o := &onceLogger{logf: func(f string, a ...any) { got = append(got, f) }}
	o.log("register: %v", "plan")
	o.log("register: %v", "plan")
	o.log("register: %v", "network")
	o.log("register: %v", "plan")
	if len(got) != 3 {
		t.Fatalf("logged %d times, want 3 (repeats suppressed, changes logged)", len(got))
	}
}
