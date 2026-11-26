// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/share2us/cli-core/daemonctl"
)

// TestMain keeps the update path away from the developer's real daemon and
// service manager, and keeps platform config/cache paths in scratch.
func TestMain(m *testing.M) {
	queryDaemon = func(string) (daemonctl.Response, bool) { return daemonctl.Response{}, false }
	serviceActive = func() bool { return false }
	serviceRestart = func() error { panic("a test tried to restart the real service") }
	startDetached = func(string) error { panic("a test tried to start a real daemon") }
	runAfterUpdate = func(app, string) {}
	stopReplyWait = time.Millisecond
	// Windows config/cache dirs ignore XDG_*; macOS uses HOME/Library rather
	// than XDG_*. Isolate both so tests cannot read or write a real profile.
	var scratch string
	var err error
	if scratch, err = os.MkdirTemp("", "s2u-cli-test-profile-*"); err != nil {
		panic(err)
	}
	// Every OS: no test may read the developer's login, reach their running daemon's
	// socket, or drive their user service manager (a setup test once rewrote the real
	// systemd unit to point at the test binary). XDG_RUNTIME_DIR also cuts off
	// `systemctl --user`.
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR"} {
		_ = os.Setenv(k, filepath.Join(scratch, k))
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		if runtime.GOOS == "darwin" {
			_ = os.Setenv("HOME", scratch)
		} else {
			for _, k := range []string{"APPDATA", "LOCALAPPDATA"} {
				_ = os.Setenv(k, filepath.Join(scratch, k))
			}
		}
	}
	code := m.Run()
	_ = os.RemoveAll(scratch)
	os.Exit(code)
}

// setConfigHome gives a test its own config dir on every OS. Windows uses
// %AppData%; macOS uses HOME/Library; Linux uses XDG_CONFIG_HOME.
func setConfigHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", dir)
	} else if runtime.GOOS == "darwin" {
		t.Setenv("HOME", dir)
	}
}
