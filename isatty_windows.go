// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// isCharTerminal reports whether f is a real console. GetConsoleMode succeeds
// only for a console handle, so a redirect from NUL or a pipe is correctly not a
// terminal — see the comment in isatty_unix.go for why os.ModeCharDevice is not
// good enough.
func isCharTerminal(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}
