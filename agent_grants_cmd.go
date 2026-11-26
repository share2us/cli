// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"fmt"
	"time"

	clicore "github.com/share2us/cli-core"
)

// Owner approvals (ADR-052): approving happens in the portal only (an agent runs on this
// device, so this device must never approve). From here you can see the permissions your
// agents hold and take them back.

func portalApprovalsURL() string {
	apiBase := ""
	if c, err := clicore.LoadCredential(); err == nil {
		apiBase = c.APIBase
	}
	return portalURLFor(apiBase) + "/approvals"
}

// agentApprovals: `s2u agent approvals` points at the portal, where requests are decided.
func (a app) agentApprovals() int {
	fmt.Fprintf(a.stdout, "Requests from your agents to run blocked actions are decided in the portal (signed in): %s\n", portalApprovalsURL())
	fmt.Fprintf(a.stdout, "Permissions your agents hold now: %s agent grants\n", commandName)
	return 0
}

// agentGrants: `s2u agent grants [revoke <id>]`.
func (a app) agentGrants(ctx context.Context, args []string) int {
	client, ok := a.manageClient()
	if !ok {
		return 1
	}
	if len(args) == 2 && args[0] == "revoke" {
		if err := client.RevokeActionGrant(ctx, args[1]); err != nil {
			return a.fail("revoke", err)
		}
		fmt.Fprintln(a.stdout, "✓ Revoked: that agent asks again next time.")
		return 0
	}
	if len(args) != 0 {
		fmt.Fprintf(a.stderr, "usage: %s agent grants [revoke <id>]\n", commandName)
		return 2
	}
	gs, err := client.ListActionGrants(ctx)
	if err != nil {
		return a.fail("grants", err)
	}
	if len(gs) == 0 {
		fmt.Fprintln(a.stdout, "No permissions granted to agents on this device.")
		return 0
	}
	for _, g := range gs {
		what := "everything (guard lifted)"
		if g.Scope == "command" {
			what = g.DisplayText
			if g.Pattern != "" {
				what = "commands starting with `" + g.Pattern + "`"
			}
		}
		until := "always"
		if g.ExpiresAt != nil {
			until = "until " + g.ExpiresAt.Local().Format("15:04") + " (" + humanSpan(time.Until(*g.ExpiresAt)) + " left)"
		}
		fmt.Fprintf(a.stdout, "%s  %s  %s  %s\n", g.ID, g.AgentID, what, until)
	}
	fmt.Fprintf(a.stdout, "Take one back: %s agent grants revoke <id>\n", commandName)
	return 0
}
