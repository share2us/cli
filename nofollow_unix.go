// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build !windows

package main

import "syscall"

// oNoFollow makes an open REFUSE a symlink at the target path rather than
// following it and truncating whatever is on the other end (§AJ low batch).
const oNoFollow = syscall.O_NOFOLLOW
