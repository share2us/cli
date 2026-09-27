// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"os"
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
	os.Exit(m.Run())
}

// realRunForTest lets one test use the real runner, for a test that puts its own
// stand-in command first on PATH. Never use it with the real service manager.
func realRunForTest(t *testing.T) {
	t.Helper()
	stub := run
	run = runCommand
	t.Cleanup(func() { run = stub })
}
