// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build !linux

package daemon

// Outside Linux the per-user service already outlives a logout where the
// platform allows it (launchd agents, Windows tasks), so there is nothing to do.

func LingerSupported() bool { return false }
func LingerEnabled() bool   { return true }
func EnableLinger() error   { return nil }
func ServiceActive() bool   { return false }
