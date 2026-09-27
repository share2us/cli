// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"fmt"
	"strings"

	clicore "github.com/share2us/cli-core"
)

// Resolving `agent send`'s target (owner, 2026-09-27): whatever `agent list` or
// `agent project` prints must work when pasted into `send`, and a sender should
// not have to look an id up anywhere else. An agent id is the stable address: a
// Claude agent's session id changes when a hop has to fork its session.

// agentTarget is one reachable session, from either directory.
type agentTarget struct {
	AgentID, SessionID, DeviceID, DeviceName, Tool, Status, PublicKey string
}

func targetsFromSessions(list []clicore.AgentSessionInfo) []agentTarget {
	out := make([]agentTarget, 0, len(list))
	for _, s := range list {
		out = append(out, agentTarget{s.AgentID, s.SessionID, s.DeviceID, s.DeviceName, s.Tool, s.Status, s.DevicePublicKey})
	}
	return out
}

func targetsFromProject(list []clicore.ProjectAgentAddress) []agentTarget {
	out := make([]agentTarget, 0, len(list))
	for _, s := range list {
		out = append(out, agentTarget{s.AgentID, s.SessionID, s.DeviceID, "", s.Tool, s.Status, s.DevicePublicKey})
	}
	return out
}

// targetQuery is what the sender typed; any field may be a full id or a prefix.
type targetQuery struct{ Agent, Device, Session string }

// minPrefix keeps a typo from matching by accident: shorter than this, an id
// must be given in full.
const minPrefix = 6

func idMatches(full, q string) bool {
	if q == "" {
		return true
	}
	return full == q || (len(q) >= minPrefix && strings.HasPrefix(full, q))
}

// pickTarget returns the one target the query names, or an error saying why not:
// nothing matches, or more than one does (listing them, in full).
func pickTarget(targets []agentTarget, q targetQuery) (agentTarget, error) {
	var hits []agentTarget
	for _, t := range targets {
		if idMatches(t.AgentID, q.Agent) && idMatches(t.DeviceID, q.Device) && idMatches(t.SessionID, q.Session) {
			hits = append(hits, t)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		msg := "no reachable agent matches"
		if q.Agent == "" && q.Session != "" {
			msg += ". A Claude agent's session id changes when a hop has to fork its session, so address it by agent id instead: --agent AGENT-ID"
		}
		return agentTarget{}, fmt.Errorf("%s", msg)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d reachable sessions match; give more of an id:", len(hits))
	for _, t := range hits {
		fmt.Fprintf(&b, "\n  %s", targetFlags(t))
	}
	return agentTarget{}, fmt.Errorf("%s", b.String())
}

// targetFlags is how a target is written on the command line, in full.
func targetFlags(t agentTarget) string {
	if t.AgentID != "" {
		return fmt.Sprintf("--agent %s  (--device %s --session %s)", t.AgentID, t.DeviceID, t.SessionID)
	}
	return fmt.Sprintf("--device %s --session %s", t.DeviceID, t.SessionID)
}
