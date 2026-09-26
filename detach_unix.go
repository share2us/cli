// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detach makes cmd outlive this process and its terminal: its own session, so a
// closed agent session or shell does not take the daemon with it.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
