// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build windows

package main

// oNoFollow is zero on Windows: the platform has no O_NOFOLLOW and Go exposes
// no equivalent flag. The guard is therefore POSIX-only, which is worth stating
// rather than implying a protection that is not there. Creating a symlink on
// Windows needs either administrator rights or Developer Mode, so the exposure
// is narrower to begin with (§AJ low batch).
const oNoFollow = 0
