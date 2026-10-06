// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/share2us/cli-core/lanid"
	"github.com/share2us/cli-core/lanshare"
)

// agentFileLANLoop is a separate, discoverable receiver for ciphertext named
// by a hop nonce. The ordinary LAN receiver writes user files to Downloads;
// this one only stages encrypted bytes for a later signed agent inject.
func (rt *Runtime) agentFileLANLoop(ctx context.Context, opts Options, deps Deps) {
	if !waitForBinding(ctx, agentBindingPoll) {
		return
	}
	// A binding may have appeared after the scheduler's startup pass. Populate
	// membership before opening the endpoint rather than waiting for its next tick.
	if deps.RefreshOwnDevices != nil {
		refreshCtx, cancel := context.WithTimeout(ctx, jobTimeout)
		deps.RefreshOwnDevices(refreshCtx)
		cancel()
	}
	identity, err := lanid.Identity()
	if err != nil {
		deps.logf("agent-file LAN receiver is off: no device identity: %v", err)
		return
	}
	instance := opts.Instance
	if instance == "" {
		instance, _ = os.Hostname()
	}
	name := instance + "-agent-files"
	var advertisement io.Closer
	ropts := lanshare.ReceiveOptions{
		Bind:       opts.Bind,
		NoPassword: true, // OnRequest admits only server-signed trusted device keys.
		Identity:   identity,
		DeviceName: name,
		DestDir:    deps.AgentFiles.dir,
		Loop:       true,
		OnRequest: func(r lanshare.RequestInfo) bool {
			if err := acceptAgentFilePush(deps.AgentFiles, opts.IsTrustedSender, opts.IsOwnAccountDevice, r); err != nil {
				deps.logf("agent-file LAN push refused: %v", err)
				return false
			}
			return true
		},
		OnListen: func(info lanshare.ListenInfo) {
			if adv, err := lanshare.Advertise(name, info); err == nil {
				advertisement = adv
			}
			deps.logf("agent-file LAN receiver listening on :%d as %q", info.Port, name)
		},
		OnReceived: func(r lanshare.ReceiveResult) {
			deps.logf("agent-file LAN ciphertext staged for nonce %s (%d bytes)", r.Name, r.Bytes)
		},
	}
	_, err = lanshare.Receive(ctx, ropts)
	if advertisement != nil {
		_ = advertisement.Close()
	}
	if err != nil && ctx.Err() == nil {
		deps.logf("agent-file LAN receiver stopped: %v", err)
	}
}

// acceptAgentFilePush decides whether to stage an incoming LAN push. A sender is
// admitted when it is either a server-signed trusted nearby device (ADR-034) OR
// another device on this same account (ownAccount). The same-account path is a
// deliberate loosening for the agent-file feature, which is about a user's own
// devices talking to each other, without a separate device-pairing ceremony.
// Admission permits staging disk use, but does not permit placement or execution.
// Staging is
// inert on its own — the bytes are E2E-sealed to the target and are only ever
// claimed when a SIGNED, approved inject arrives carrying the matching
// (nonce, sender LAN fingerprint); an un-claimed push expires on the TTL sweep.
// Cross-account senders are not admitted this way and fall back to the relay.
func acceptAgentFilePush(stage *AgentFileStage, trusted, ownAccount func([]byte) bool, r lanshare.RequestInfo) error {
	if r.IsDir {
		return errors.New("agent-file push cannot be a directory")
	}
	admitted := (trusted != nil && trusted(r.SenderKey)) || (ownAccount != nil && ownAccount(r.SenderKey))
	if !admitted {
		return errors.New("agent-file sender is neither a trusted device nor one of this account's devices")
	}
	return stage.reserve(r.Name, lanshare.IdentityFingerprint(r.SenderKey), r.Size)
}
