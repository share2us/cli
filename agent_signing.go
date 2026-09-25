// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	clicore "github.com/share2us/cli-core"
)

// ensureSigningKey makes sure this device has an Ed25519 signing key and that the
// server knows it (ADR-041 §5). Safe to call on every daemon start and every send:
// the key is generated once, saved beside the login, and registration is
// idempotent on the server for the same key.
//
// The server treats the key as WRITE-ONCE, so the order matters: the key is saved
// locally before it is registered. The other way round, a crash between the two
// would leave the server holding a key this machine had lost, with no way to
// replace it short of signing in again.
func ensureSigningKey(ctx context.Context, client *clicore.Client, credential clicore.Credential) (clicore.Credential, error) {
	if credential.DeviceSessionID == "" {
		return credential, errors.New("this login has no device session; sign in again to send signed hops")
	}
	if credential.DeviceSigningPrivateKey == "" || credential.DeviceSigningPublicKey == "" {
		kp, err := clicore.NewSigningKeyPair()
		if err != nil {
			return credential, err
		}
		credential.DeviceSigningPublicKey = kp.PublicKey
		credential.DeviceSigningPrivateKey = kp.PrivateKey
		if err := clicore.SaveCredential(credential); err != nil {
			return credential, fmt.Errorf("save signing key: %w", err)
		}
	}
	if err := client.RegisterSigningKey(ctx, credential.DeviceSigningPublicKey); err != nil {
		// A 409 means the server holds a DIFFERENT key for this device session —
		// most likely credentials copied from another machine. Say what to do
		// rather than surfacing a bare conflict.
		if strings.Contains(err.Error(), "signing_key_already_set") {
			return credential, errors.New("the server already holds a different signing key for this device; run `" + commandName + " login` to get a fresh device identity")
		}
		return credential, fmt.Errorf("register signing key: %w", err)
	}
	return credential, nil
}

// signHop signs an inject request in place. The claims use the values exactly as
// the server will normalize them — trimmed ids and a lowercase tool — because the
// server verifies over what it is about to act on, not over what was typed.
func signHop(in *clicore.AgentInjectInput, credential clicore.Credential, now time.Time) error {
	nonce, err := clicore.NewHopNonce()
	if err != nil {
		return err
	}
	in.TargetDeviceID = strings.TrimSpace(in.TargetDeviceID)
	in.TargetSessionID = strings.TrimSpace(in.TargetSessionID)
	in.Tool = strings.ToLower(strings.TrimSpace(in.Tool))
	in.SealedFileKey = strings.TrimSpace(in.SealedFileKey)
	in.GoalID = strings.TrimSpace(in.GoalID)
	issued := now.UTC().Truncate(time.Second)

	sig, err := clicore.SignHop(clicore.HopClaims{
		SenderDeviceID:  credential.DeviceSessionID,
		TargetDeviceID:  in.TargetDeviceID,
		TargetSessionID: in.TargetSessionID,
		Tool:            in.Tool,
		SealedPrompt:    in.SealedPrompt,
		SealedFileKey:   in.SealedFileKey,
		GoalID:          in.GoalID,
		IssuedAt:        issued,
		Nonce:           nonce,
	}, credential.DeviceSigningPrivateKey)
	if err != nil {
		return err
	}
	in.Signature = sig
	in.IssuedAt = issued.Format(time.RFC3339)
	in.Nonce = nonce
	return nil
}
