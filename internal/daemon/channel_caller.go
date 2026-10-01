// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

// channelCallerVerifier binds a control socket's kernel-attested peer PID to
// one live Claude process. A short cache avoids running `claude agents` for
// every poll; each request still has to descend from that same process.
type channelCallerVerifier struct {
	mu       sync.Mutex
	known    map[string]verifiedClaudeProcess
	discover func(context.Context) (map[int]DiscoveredSession, error)
	parent   func(int) (int, error)
}

type verifiedClaudeProcess struct {
	pid     int
	expires time.Time
}

const callerCacheLifetime = 10 * time.Second

func discoverClaudeProcesses(ctx context.Context) (map[int]DiscoveredSession, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "claude", "agents", "--json").Output()
	if err != nil {
		return nil, err
	}
	return claudeSessionsByPID(out)
}

func (v *channelCallerVerifier) inSession(peerPID int, session string) bool {
	if peerPID <= 1 || session == "" {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	parent := v.parent
	if parent == nil {
		parent = parentPID
	}
	if known, ok := v.known[session]; ok && time.Now().Before(known.expires) {
		if _, err := ownSession(peerPID, func(pid int) (DiscoveredSession, bool) {
			return DiscoveredSession{}, pid == known.pid
		}, parent); err == nil {
			return true
		}
	}
	discover := v.discover
	if discover == nil {
		discover = discoverClaudeProcesses
	}
	sessions, err := discover(context.Background())
	if err != nil {
		return false
	}
	matched, err := ownSession(peerPID, func(pid int) (DiscoveredSession, bool) {
		s, ok := sessions[pid]
		return s, ok && s.SessionID == session && s.Tool == "claude"
	}, parent)
	if err != nil || matched.PID <= 1 {
		return false
	}
	if v.known == nil {
		v.known = make(map[string]verifiedClaudeProcess)
	}
	v.known[session] = verifiedClaudeProcess{pid: matched.PID, expires: time.Now().Add(callerCacheLifetime)}
	return true
}
