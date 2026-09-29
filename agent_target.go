// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	clicore "github.com/share2us/cli-core"
)

// Resolving `agent send`'s target (owner, 2026-09-27): whatever `agent list` or
// `agent project` prints must work when pasted into `send`, and a sender should
// not have to look an id up anywhere else. An agent id is the stable address: a
// session id changes when the agent is bound to a new session.

// agentTarget is one reachable session, from either directory.
type agentTarget struct {
	AgentID, SessionID, DeviceID, DeviceName, Tool, Status, PublicKey string
	LastSeen                                                          string
}

func targetsFromSessions(list []clicore.AgentSessionInfo) []agentTarget {
	out := make([]agentTarget, 0, len(list))
	for _, s := range list {
		out = append(out, agentTarget{s.AgentID, s.SessionID, s.DeviceID, s.DeviceName, s.Tool, s.Status, s.DevicePublicKey, s.LastSeen})
	}
	return out
}

func targetsFromProject(list []clicore.ProjectAgentAddress) []agentTarget {
	out := make([]agentTarget, 0, len(list))
	for _, s := range list {
		out = append(out, agentTarget{s.AgentID, s.SessionID, s.DeviceID, "", s.Tool, s.Status, s.DevicePublicKey, s.LastSeen})
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

// errNoTarget marks "nothing matched", as opposed to "several did": only then is
// it worth asking whether the agent is merely offline.
var errNoTarget = errors.New("no reachable agent matches")

// pickTarget returns the one target the query names, or an error saying why not:
// nothing matches, or more than one does (listing them, in full).
func pickTarget(targets []agentTarget, q targetQuery) (agentTarget, error) {
	var hits []agentTarget
	for _, t := range targets {
		if t.Status == "offline" {
			continue // listed only to explain a miss; not a target
		}
		if idMatches(t.AgentID, q.Agent) && idMatches(t.DeviceID, q.Device) && idMatches(t.SessionID, q.Session) {
			hits = append(hits, t)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		if q.Agent == "" && q.Session != "" {
			return agentTarget{}, fmt.Errorf("%w. A session id changes when the agent is bound to a new session, so address it by agent id instead: --agent AGENT-ID", errNoTarget)
		}
		return agentTarget{}, errNoTarget
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

// offlineNote explains a miss when the agent exists but its daemon has stopped
// heartbeating: the listing that includes offline sessions has it. "" when it
// does not, so the caller keeps its "no such agent" message.
func offlineNote(listed []agentTarget, q targetQuery, now time.Time) string {
	for _, t := range listed {
		if t.Status != "offline" || !idMatches(t.AgentID, q.Agent) || !idMatches(t.DeviceID, q.Device) || !idMatches(t.SessionID, q.Session) {
			continue
		}
		who := t.AgentID
		if who == "" {
			who = "session " + t.SessionID
		}
		when := "a while ago"
		if seen, err := time.Parse(time.RFC3339, t.LastSeen); err == nil {
			when = agoString(now.Sub(seen)) + " ago"
		}
		where := ""
		if t.DeviceName != "" {
			where = " on " + t.DeviceName
		}
		return fmt.Sprintf("%s is offline: last seen %s%s. A hop cannot reach it until its machine's Share2Us service is running again.", who, when, where)
	}
	return ""
}

func agoString(d time.Duration) string {
	switch {
	case d < 2*time.Minute:
		return "a minute"
	case d < 2*time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	}
}
