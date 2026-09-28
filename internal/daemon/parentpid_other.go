// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build !windows

package daemon

import "errors"

func windowsParentPID(int) (int, error) { return 0, errors.New("not windows") }
