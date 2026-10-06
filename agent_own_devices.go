// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"strings"
	"sync"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli-core/lanshare"
)

// ownAccountDevices caches the LAN fingerprints of this account's own devices so
// the agent-file receiver can admit a direct LAN push from another of the user's
// machines without a separate ADR-034 pairing (see acceptAgentFilePush). It is
// refreshed from /v1/devices on the daemon's schedule; until the first refresh it
// is empty, so a push simply falls back to the relay. Matching is on the proven
// lanid identity fingerprint, never an address.
type ownAccountDevices struct {
	mu           sync.RWMutex
	fingerprints map[string]struct{}
}

func newOwnAccountDevices() *ownAccountDevices {
	return &ownAccountDevices{fingerprints: map[string]struct{}{}}
}

// refresh replaces the cached set from the account's device list. Best-effort: on
// any error the previous set is kept, so a transient outage does not suddenly
// reject a device the daemon already knew.
func (o *ownAccountDevices) refresh(ctx context.Context, client *clicore.Client) {
	if client == nil {
		return
	}
	resp, err := client.ListDevices(ctx)
	if err != nil {
		return
	}
	set := make(map[string]struct{}, len(resp.Sessions))
	for _, d := range resp.Sessions {
		if fp := strings.ToLower(strings.TrimSpace(d.LanFingerprint)); fp != "" {
			set[fp] = struct{}{}
		}
	}
	o.mu.Lock()
	o.fingerprints = set
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
	_, ok := o.fingerprints[fp]
	o.mu.RUnlock()
	return ok
}
