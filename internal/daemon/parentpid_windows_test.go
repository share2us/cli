// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build windows

package daemon

import (
	"os"
	"os/exec"
	"testing"
)

// agent join walks up the process tree to the Claude process; on Windows that
// needs the snapshot-based parent lookup. A child we start must name us.
func TestWindowsParentPIDFindsOurChildsParent(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "ping", "-n", "3", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	got, err := parentPID(cmd.Process.Pid)
	if err != nil || got != os.Getpid() {
		t.Fatalf("parent of our child = %d, %v; want %d", got, err, os.Getpid())
	}
	if p, err := parentPID(os.Getpid()); err != nil || p <= 0 {
		t.Fatalf("our own parent = %d, %v", p, err)
	}
}
