// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build !linux && !darwin

package daemon

import "io"

// Windows and the rest: the service inherits the user's environment and runs
// unsandboxed, so there is nothing to refresh.
func ServiceNeedsRefresh(string) bool        { return false }
func ServiceRefresh(string, io.Writer) error { return nil }
