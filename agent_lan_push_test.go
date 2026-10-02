// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"testing"

	"github.com/share2us/cli-core/lanshare"
)

func TestSelectAgentFilePeer(t *testing.T) {
	target := "abc123"
	peers := []lanshare.Peer{
		{Instance: "host-agent-files", Host: "10.0.0.9", Port: 5, IdentityFingerprint: "other"},                   // wrong device
		{Instance: "host", Host: "10.0.0.9", Port: 6, IdentityFingerprint: target},                                // right device, ordinary receiver
		{Instance: "host-agent-files", Host: "10.0.0.9", Port: 7, IdentityFingerprint: target},                    // the one we want
		{Instance: "host-agent-files", Host: "10.0.0.9", Port: 8, IdentityFingerprint: target, IsBroadcast: true}, // broadcast offer
	}
	p, ok := selectAgentFilePeer(peers, "ABC123") // case-insensitive
	if !ok || p.Port != 7 {
		t.Fatalf("selected %+v ok=%v, want the agent-files receiver on port 7", p, ok)
	}
	// No agent-files receiver for the target: no selection (caller relays).
	if _, ok := selectAgentFilePeer(peers[:2], target); ok {
		t.Fatal("selected a non-agent-files receiver")
	}
	if _, ok := selectAgentFilePeer(peers, ""); ok {
		t.Fatal("selected with an empty target fingerprint")
	}
}
