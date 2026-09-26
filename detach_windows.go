// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// detach makes cmd outlive this process: no console, its own process group.
func detach(cmd *exec.Cmd) {
	const createNewProcessGroup, detachedProcess = 0x00000200, 0x00000008
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
}
