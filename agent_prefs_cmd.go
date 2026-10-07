// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"fmt"
	"strings"

	clicore "github.com/share2us/cli-core"
)

// Pin, hide and rename are LOCAL preferences on the stable AgentID, stored in
// config.json and shared with the desktop app. They never leave the machine and
// never change what anyone else sees: a rename is your own label. The store lives
// in cli-core so the CLI and the app stay in lockstep.

// resolveAgentID turns a user-typed agent id (or a unique prefix, as `agent list`
// prints it) into a full, stable AgentID. It matches against the whole directory
// INCLUDING offline sessions, so an agent can be pinned, hidden or renamed even
// when it is not online right now. A full, well-formed id that is not in the
// directory at all is accepted as-is, so a pref can still be set (or cleared) for
// an agent that has gone away.
func (a app) resolveAgentID(ctx context.Context, idOrPrefix string) (string, bool) {
	idOrPrefix = strings.TrimSpace(idOrPrefix)
	if idOrPrefix == "" {
		fmt.Fprintln(a.stderr, "an agent id is required")
		return "", false
	}
	client, ok := a.agentClient()
	if !ok {
		return "", false
	}
	sessions, err := client.ListAgentSessionsIncludingOffline(ctx)
	if err != nil {
		_ = a.fail("list agents", err)
		return "", false
	}
	seen := map[string]bool{}
	var hits []string
	for _, s := range sessions {
		if s.AgentID == "" || seen[s.AgentID] {
			continue
		}
		if s.AgentID == idOrPrefix || (len(idOrPrefix) >= 6 && strings.HasPrefix(s.AgentID, idOrPrefix)) {
			seen[s.AgentID] = true
			hits = append(hits, s.AgentID)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], true
	case 0:
		if strings.HasPrefix(idOrPrefix, "agt_") && len(idOrPrefix) >= 20 {
			return idOrPrefix, true // exact id for an agent not in the directory
		}
		fmt.Fprintf(a.stderr, "no agent matches %q; see `%s agent list --all`\n", idOrPrefix, commandName)
		return "", false
	default:
		fmt.Fprintf(a.stderr, "%q matches %d agents; give more of the id\n", idOrPrefix, len(hits))
		return "", false
	}
}

func (a app) agentPin(ctx context.Context, args []string, pin bool) int {
	verb := "pin"
	if !pin {
		verb = "unpin"
	}
	if len(args) != 1 {
		fmt.Fprintf(a.stderr, "usage: %s agent %s <agent-id>\n", commandName, verb)
		return 2
	}
	id, ok := a.resolveAgentID(ctx, args[0])
	if !ok {
		return 1
	}
	if err := clicore.UpdateAgentPref(id, func(p *clicore.AgentPrefs) { p.Pinned = pin }); err != nil {
		return a.fail("save preference", err)
	}
	if pin {
		fmt.Fprintf(a.stdout, "Pinned %s.\n", id)
	} else {
		fmt.Fprintf(a.stdout, "Unpinned %s.\n", id)
	}
	return 0
}

func (a app) agentHide(ctx context.Context, args []string, hide bool) int {
	verb := "hide"
	if !hide {
		verb = "unhide"
	}
	if len(args) != 1 {
		fmt.Fprintf(a.stderr, "usage: %s agent %s <agent-id>\n", commandName, verb)
		return 2
	}
	id, ok := a.resolveAgentID(ctx, args[0])
	if !ok {
		return 1
	}
	if err := clicore.UpdateAgentPref(id, func(p *clicore.AgentPrefs) { p.Hidden = hide }); err != nil {
		return a.fail("save preference", err)
	}
	if hide {
		fmt.Fprintf(a.stdout, "Hid %s. It still receives sends; `%s agent list --all` shows it.\n", id, commandName)
	} else {
		fmt.Fprintf(a.stdout, "Unhid %s.\n", id)
	}
	return 0
}

func (a app) agentRename(ctx context.Context, args []string) int {
	if len(args) < 1 {
		fmt.Fprintf(a.stderr, "usage: %s agent rename <agent-id> [NAME]   (no NAME clears the alias)\n", commandName)
		return 2
	}
	id, ok := a.resolveAgentID(ctx, args[0])
	if !ok {
		return 1
	}
	alias := strings.TrimSpace(strings.Join(args[1:], " "))
	if err := clicore.UpdateAgentPref(id, func(p *clicore.AgentPrefs) { p.Alias = alias }); err != nil {
		return a.fail("save alias", err)
	}
	if alias == "" {
		fmt.Fprintf(a.stdout, "Cleared the alias for %s.\n", id)
	} else {
		fmt.Fprintf(a.stdout, "Renamed %s to %q (your label only).\n", id, alias)
	}
	return 0
}
