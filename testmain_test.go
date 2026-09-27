// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"os"
	"testing"

	"github.com/share2us/cli-core/daemonctl"
)

// TestMain keeps the update path away from the developer's real daemon and
// service manager: by default no daemon answers, and restarting is refused
// loudly. Tests that exercise the restart replace these per test.
func TestMain(m *testing.M) {
	queryDaemon = func(string) (daemonctl.Response, bool) { return daemonctl.Response{}, false }
	serviceActive = func() bool { return false }
	serviceRestart = func() error { panic("a test tried to restart the real service") }
	startDetached = func(string) error { panic("a test tried to start a real daemon") }
	os.Exit(m.Run())
}
