// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// serviceCalls records the service-manager commands tests would have run.
var (
	serviceCallsMu sync.Mutex
	serviceCalls   []string
)

// TestMain keeps every test away from the machine's real service manager. The
// unit file follows XDG_CONFIG_HOME, but `systemctl --user disable --now
// s2u-daemon` acts on the unit by NAME, so a test calling ServiceUninstall used
// to stop and disable the developer's own daemon.
func TestMain(m *testing.M) {
	run = func(name string, args ...string) error {
		serviceCallsMu.Lock()
		serviceCalls = append(serviceCalls, name+" "+strings.Join(args, " "))
		serviceCallsMu.Unlock()
		return nil
	}
	// Config lives where the test says (XDG_CONFIG_HOME, on every OS), else in a
	// scratch dir: never the machine's real config.
	scratch, err := os.MkdirTemp("", "s2u-daemon-test-config-*")
	if err != nil {
		panic(err)
	}
	userConfigDir = func() (string, error) {
		if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
			return x, nil
		}
		return scratch, nil
	}
	code := m.Run()
	_ = os.RemoveAll(scratch)
	os.Exit(code)
}

// realRunForTest lets one test use the real runner, for a test that puts its own
// stand-in command first on PATH. Never use it with the real service manager.
func realRunForTest(t *testing.T) {
	t.Helper()
	stub := run
	run = runCommand
	t.Cleanup(func() { run = stub })
}

// wantPrivate checks a file is 0600 where the OS has Unix permissions. Windows
// has none to check (Go reports -rw-rw-rw-); there the file is private because
// it lives in the user's own profile.
func wantPrivate(t *testing.T, mode os.FileMode, what string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	if mode.Perm() != 0o600 {
		t.Fatalf("%s mode = %v, want 0600", what, mode.Perm())
	}
}
