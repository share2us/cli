// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build windows

package daemon

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowsParentPID reads pid's parent from a process snapshot, so `agent join`
// can walk up to the Claude process on Windows too (owner, 2026-09-29).
func windowsParentPID(pid int) (int, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if int(e.ProcessID) == pid {
			return int(e.ParentProcessID), nil
		}
	}
	return 0, errors.New("process not found in the snapshot")
}
