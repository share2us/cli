// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import "os"

// userConfigDir is where the daemon keeps bindings, policies, pins and the hop
// log. A variable so the test suite can keep every test out of the real one: on
// Windows os.UserConfigDir is %AppData% and ignores XDG_CONFIG_HOME, so tests
// that set only that were writing into the developer's real bindings
// (found on the Windows VM, 2026-09-28).
var userConfigDir = os.UserConfigDir
