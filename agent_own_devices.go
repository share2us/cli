// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli-core/lanshare"
)

// The scheduler refreshes hourly. An outage may reuse a recent answer, but
// never keep admitting a revoked device indefinitely.
const ownDeviceCacheTTL = 90 * time.Minute

// ownAccountDevices caches the LAN fingerprints of this account's own devices so
// the agent-file receiver can admit a direct LAN push from another of the user's
// machines without a separate ADR-034 pairing (see acceptAgentFilePush). It is
// refreshed from /v1/devices on the daemon's schedule; until the first refresh it
// is empty, so a push simply falls back to the relay. Matching is on the proven
// lanid identity fingerprint, never an address.
type ownAccountDevices struct {
	mu           sync.RWMutex
	fingerprints map[string]time.Time
	fetchedAt    time.Time
}

func newOwnAccountDevices() *ownAccountDevices {
	return &ownAccountDevices{fingerprints: map[string]time.Time{}}
}

// refresh replaces the cached set from the account's device list. Best-effort: on
// any error the previous set is kept within its bounded lifetime, so a short
// outage does not immediately reject a device the daemon already knew.
func (o *ownAccountDevices) refresh(ctx context.Context, client *clicore.Client) {
	if client == nil {
		return
	}
	resp, err := client.ListDevices(ctx)
	if err != nil {
		return
	}
	now := time.Now()
	set := make(map[string]time.Time, len(resp.Sessions))
	for _, d := range resp.Sessions {
		if d.ClientType != "cli" {
			continue
		}
		expires, err := time.Parse(time.RFC3339, d.ExpiresAt)
		if err != nil || !expires.After(now) {
			continue
		}
		fp := strings.ToLower(strings.TrimSpace(d.LanFingerprint))
		if decoded, err := hex.DecodeString(fp); err == nil && len(decoded) == 32 && expires.After(set[fp]) {
			set[fp] = expires
		}
	}
	o.mu.Lock()
	o.fingerprints = set
	o.fetchedAt = now
	o.mu.Unlock()
}

// contains reports whether a verified LAN sender key belongs to one of this
// account's devices. An empty/anonymous key is never a match.
func (o *ownAccountDevices) contains(senderKey []byte) bool {
	if len(senderKey) == 0 {
		return false
	}
	fp := strings.ToLower(lanshare.IdentityFingerprint(senderKey))
	if fp == "" {
		return false
	}
	o.mu.RLock()
	expires, ok := o.fingerprints[fp]
	fetchedAt := o.fetchedAt
	o.mu.RUnlock()
	now := time.Now()
	return ok && now.Before(expires) && !fetchedAt.IsZero() && now.Sub(fetchedAt) < ownDeviceCacheTTL
}
