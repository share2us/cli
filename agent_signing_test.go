// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"strings"
	"testing"
	"time"

	clicore "github.com/share2us/cli-core"
)

// The server verifies a hop over the values AFTER its own normalization — trimmed
// ids, a lowercase tool. If the CLI signed the raw typed values instead, every real
// hop with a stray space or a capital letter would be rejected as forged. This
// reproduces the server's normalization and checks the CLI signed exactly that.
func TestSignHopSignsWhatTheServerWillVerify(t *testing.T) {
	kp, err := clicore.NewSigningKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	cred := clicore.Credential{DeviceSessionID: "dev-sender", DeviceSigningPrivateKey: kp.PrivateKey}
	in := clicore.AgentInjectInput{
		TargetDeviceID:  "  dev-target ",
		TargetSessionID: " sess-1\t",
		Tool:            " Claude ",
		SealedPrompt:    "SEALED",
		SealedFileKey:   " KEY ",
		GoalID:          " goal-1 ",
	}
	now := time.Now()
	if err := signHop(&in, cred, now); err != nil {
		t.Fatal(err)
	}

	// Exactly what the API does before verifying (agent/handler.go inject).
	issued, err := time.Parse(time.RFC3339, in.IssuedAt)
	if err != nil {
		t.Fatalf("issued_at %q is not RFC3339: %v", in.IssuedAt, err)
	}
	server := clicore.HopClaims{
		SenderDeviceID:  cred.DeviceSessionID,
		TargetDeviceID:  strings.TrimSpace("  dev-target "),
		TargetSessionID: strings.TrimSpace(" sess-1\t"),
		Tool:            strings.TrimSpace(strings.ToLower(" Claude ")),
		SealedPrompt:    "SEALED",
		SealedFileKey:   strings.TrimSpace(" KEY "),
		GoalID:          strings.TrimSpace(" goal-1 "),
		IssuedAt:        issued,
		Nonce:           in.Nonce,
	}
	if err := clicore.VerifyHopFresh(server, in.Signature, kp.PublicKey, now, 5*time.Minute); err != nil {
		t.Fatalf("the server would reject this CLI-signed hop: %v", err)
	}
}

func TestEverySignedHopGetsItsOwnNonce(t *testing.T) {
	kp, _ := clicore.NewSigningKeyPair()
	cred := clicore.Credential{DeviceSessionID: "dev", DeviceSigningPrivateKey: kp.PrivateKey}
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		in := clicore.AgentInjectInput{TargetDeviceID: "d", TargetSessionID: "s", Tool: "claude", SealedPrompt: "x"}
		if err := signHop(&in, cred, time.Now()); err != nil {
			t.Fatal(err)
		}
		// A reused nonce would make the server treat a NEW hop as a retry of an
		// old one and silently drop it.
		if seen[in.Nonce] {
			t.Fatalf("nonce reused after %d hops", i)
		}
		seen[in.Nonce] = true
	}
}
