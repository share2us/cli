// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"path/filepath"
	"runtime"
	"strings"
)

// Claude runs hook commands through a shell. PowerShell needs the call
// operator before a quoted executable; POSIX shells must not receive it.
func claudeHookCommand(exe, event string) string {
	return claudeHookCommandForOS(exe, event, runtime.GOOS)
}

func claudeHookCommandForOS(exe, event, goos string) string {
	quoted := `"` + strings.ReplaceAll(filepath.ToSlash(exe), `"`, `\"`) + `"`
	if goos == "windows" {
		return `& ` + quoted + " agent hook " + event
	}
	return quoted + " agent hook " + event
}
