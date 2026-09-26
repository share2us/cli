// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import "testing"

func TestShouldEnableLinger(t *testing.T) {
	cases := []struct {
		name                          string
		supported                     bool
		state                         daemonState
		serviceActive, lingerOn, want bool
	}{
		{"we just started the service", true, daemonServiceStarted, true, false, true},
		{"service already running", true, daemonAlreadyRunning, true, false, true},
		{"a hand-started daemon, no service", true, daemonAlreadyRunning, false, false, false},
		{"detached fallback: no service to keep", true, daemonDetachedStarted, false, false, false},
		{"nothing started", true, daemonNotStarted, false, false, false},
		{"already on", true, daemonServiceStarted, true, true, false},
		{"not linux", false, daemonServiceStarted, true, false, false},
	}
	for _, c := range cases {
		if got := shouldEnableLinger(c.supported, c.state, c.serviceActive, c.lingerOn); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
