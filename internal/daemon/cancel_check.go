// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"

	clicore "github.com/share2us/cli-core"
)

// agentStatusClient is the optional read of a request's status (the real client has it).
type agentStatusClient interface {
	AgentInjectStatus(ctx context.Context, id string) (clicore.AgentInjectState, error)
}

// cancelledBySender reports whether the sender withdrew this request (s2u agent cancel).
// Read-only, and fails open to "not cancelled": an unreachable server must not drop a
// hop, and a cancel that raced delivery is still refused by the server's 409 later.
func cancelledBySender(ctx context.Context, client AgentClient, id string) bool {
	sc, ok := client.(agentStatusClient)
	if !ok {
		return false
	}
	st, err := sc.AgentInjectStatus(ctx, id)
	return err == nil && st.Status == "cancelled"
}
