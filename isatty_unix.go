// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build !windows

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// isCharTerminal reports whether f is a real terminal.
//
// The obvious test — os.ModeCharDevice — is WRONG, and was: /dev/null is a
// character device, so `s2u receive < /dev/null` looked interactive. Every
// prompt guarded by it then fired at a caller with no way to answer, which is
// exactly what those guards exist to prevent. A termios ioctl is the real
// question ("does this fd have terminal semantics"), and it is what the password
// reader in this package has always used.
func isCharTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), ioctlReadTermios)
	return err == nil
}
