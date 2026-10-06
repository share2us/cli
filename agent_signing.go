// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
//
// The hop signing itself lives in cli-core (Client.SendAgentFile), so the CLI and
// the desktop app sign and send the same way.
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
