// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

// agentsBound reports whether this machine has bound agent sessions, which
// decides whether the service is installed to run agents (see renderUnit).
func agentsBound() bool {
	list, err := LoadBindings()
	return err == nil && len(list) > 0
}
