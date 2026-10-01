// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

// channelCallerVerifier binds each control socket's kernel-attested peer PID
// to its NEAREST live agent session. Caching by the exact peer process (not by
// the claimed session) keeps a nested Claude session from claiming its parent.
type channelCallerVerifier struct {
	mu       sync.Mutex
	known    map[int]verifiedCaller
	discover func(context.Context) (map[int]DiscoveredSession, error)
	parent   func(int) (int, error)
}

type verifiedCaller struct {
	session   string
	claudePID int
	parent    int
	expires   time.Time
}

const callerCacheLifetime = 10 * time.Second
const callerMissLifetime = 500 * time.Millisecond
const maxCallerCacheEntries = 1024

func discoverClaudeProcesses(ctx context.Context) (map[int]DiscoveredSession, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "claude", "agents", "--json").Output()
	if err != nil {
		return nil, err
	}
	return claudeSessionsByPID(out)
}

func (v *channelCallerVerifier) inSession(peerPID int, session string) bool {
	return v.sessionPID(peerPID, session) > 1
}

// sessionPID returns the nearest Claude process PID only when it owns session.
// Zero is a denial, including unsupported transports and discovery failures.
func (v *channelCallerVerifier) sessionPID(peerPID int, session string) int {
	if peerPID <= 1 || session == "" {
		return 0
	}
	parent := v.parent
	if parent == nil {
		parent = parentPID
	}
	directParent, err := parent(peerPID)
	if err != nil {
		return 0
	}
	v.mu.Lock()
	if known, ok := v.known[peerPID]; ok && known.parent == directParent && time.Now().Before(known.expires) {
		v.mu.Unlock()
		if known.session == session {
			return known.claudePID
		}
		return 0
	}
	v.mu.Unlock()

	// A slow or failing discovery must not block unrelated sessions' polls.
	discover := v.discover
	if discover == nil {
		discover = discoverClaudeProcesses
	}
	sessions, discoverErr := discover(context.Background())
	var matched DiscoveredSession
	if discoverErr == nil {
		matched, _ = ownSession(peerPID, func(pid int) (DiscoveredSession, bool) {
			if s, ok := sessions[pid]; ok {
				return s, true
			}
			return codexSessionOf(pid)
		}, parent)
	}
	verifiedSession := ""
	verifiedPID := 0
	lifetime := callerMissLifetime
	if matched.Tool == "claude" && matched.PID > 1 {
		verifiedSession = matched.SessionID
		verifiedPID = matched.PID
		lifetime = callerCacheLifetime
	}
	v.mu.Lock()
	if v.known == nil {
		v.known = make(map[int]verifiedCaller)
	}
	if len(v.known) >= maxCallerCacheEntries {
		for pid, entry := range v.known {
			if time.Now().After(entry.expires) {
				delete(v.known, pid)
			}
		}
		if len(v.known) >= maxCallerCacheEntries {
			v.known = make(map[int]verifiedCaller) // force fresh attestation next time
		}
	}
	v.known[peerPID] = verifiedCaller{session: verifiedSession, claudePID: verifiedPID, parent: directParent, expires: time.Now().Add(lifetime)}
	v.mu.Unlock()
	if verifiedSession == session {
		return verifiedPID
	}
	return 0
}
