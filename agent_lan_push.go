// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/share2us/cli-core/lanid"
	"github.com/share2us/cli-core/lanshare"
)

// agentFilesSuffix is how a device's agent-file receiver names its mDNS instance
// (internal/daemon/agentfile_lan.go: "<instance>-agent-files"). The sender uses
// it to pick that receiver over the device's ordinary file receiver.
const agentFilesSuffix = "-agent-files"

var (
	agentLANBrowseTimeout = 3 * time.Second
	agentLANSendTimeout   = 30 * time.Second
)

// pushAgentFileLAN streams the sealed ciphertext straight to the target device's
// agent-file receiver over LAN/Tailscale, keyed by the signed hop nonce, so the
// receiver can stage it and match it to the inject that follows. It returns true
// only when the bytes landed; any failure (no identity, target not found, not
// reachable, refused) returns false so the caller falls back to the relay.
//
// targetFingerprint is the target's lanid fingerprint from the directory. The
// bytes are already sealed to the target's device key, so a misroute is only
// wasted effort, never a disclosure.
func pushAgentFileLAN(ctx context.Context, targetFingerprint, senderName, nonce string, ciphertext []byte) bool {
	targetFingerprint = strings.ToLower(strings.TrimSpace(targetFingerprint))
	if targetFingerprint == "" || nonce == "" {
		return false
	}
	identity, err := lanid.Identity()
	if err != nil {
		return false
	}
	bctx, cancel := context.WithTimeout(ctx, agentLANBrowseTimeout+time.Second)
	defer cancel()
	peers, err := lanshare.Browse(bctx, agentLANBrowseTimeout)
	if err != nil {
		return false
	}
	p, ok := selectAgentFilePeer(peers, targetFingerprint)
	if ok {
		sctx, scancel := context.WithTimeout(ctx, agentLANSendTimeout)
		_, serr := lanshare.Send(sctx, nonce, int64(len(ciphertext)), false, bytes.NewReader(ciphertext), lanshare.SendOptions{
			Dest:           net.JoinHostPort(p.Host, strconv.Itoa(p.Port)),
			PinFingerprint: p.Fingerprint,
			Identity:       identity,
			SenderName:     senderName,
		})
		scancel()
		if serr == nil {
			return true
		}
	}
	return false
}

// selectAgentFilePeer picks the target device's agent-file receiver from a browse
// result: the entry whose identity fingerprint matches and whose instance is the
// "<host>-agent-files" stage (not the device's ordinary file receiver, nor a
// broadcast offer). Reachable address required.
func selectAgentFilePeer(peers []lanshare.Peer, targetFingerprint string) (lanshare.Peer, bool) {
	targetFingerprint = strings.ToLower(strings.TrimSpace(targetFingerprint))
	if targetFingerprint == "" {
		return lanshare.Peer{}, false
	}
	for _, p := range peers {
		if p.IsBroadcast || p.Host == "" || p.Port == 0 {
			continue
		}
		if !strings.EqualFold(p.IdentityFingerprint, targetFingerprint) {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(p.Instance), agentFilesSuffix) {
			continue // the device's ordinary receiver, not its agent-file stage
		}
		return p, true
	}
	return lanshare.Peer{}, false
}
