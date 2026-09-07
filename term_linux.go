// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build linux

package main

import "golang.org/x/sys/unix"

// Terminal ioctl requests for termios get/set. Linux uses TCGETS/TCSETS;
// Darwin uses TIOCGETA/TIOCSETA (see term_darwin.go).
const (
	ioctlReadTermios  = unix.TCGETS
	ioctlWriteTermios = unix.TCSETS
)
