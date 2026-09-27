// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"os"
	"path/filepath"
	"runtime"
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
	// On Windows the config and cache dirs are %AppData% and %LocalAppData%,
	// which ignore XDG_*: without this, tests wrote config.json and
	// credentials.json into the developer's real profile (found on the Windows
	// VM, 2026-09-28). Point both at scratch for the whole run.
	var scratch string
	if runtime.GOOS == "windows" {
		var err error
		if scratch, err = os.MkdirTemp("", "s2u-cli-test-profile-*"); err != nil {
			panic(err)
		}
		for _, k := range []string{"APPDATA", "LOCALAPPDATA"} {
			_ = os.Setenv(k, filepath.Join(scratch, k))
		}
	}
	code := m.Run()
	if scratch != "" {
		_ = os.RemoveAll(scratch)
	}
	os.Exit(code)
}

// setConfigHome gives a test its own config dir on every OS: XDG_CONFIG_HOME,
// and on Windows %AppData%, which is what os.UserConfigDir reads there.
func setConfigHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", dir)
	}
}
