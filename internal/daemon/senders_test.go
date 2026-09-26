// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	clicore "github.com/share2us/cli-core"
)

// These tests stand in for a compromised or confused server: it controls every
// field of the delivered request, including the key it claims the sender has.
// The receiver's pin is the only thing it cannot rewrite.

const self = "dev-self"

type sender struct {
	id string
	kp clicore.SigningKeyPair
}

func newSender(t *testing.T, id string) sender {
	t.Helper()
	kp, err := clicore.NewSigningKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	return sender{id: id, kp: kp}
}

// signed builds a delivered request exactly as a genuine sender's hop arrives.
func (s sender) signed(t *testing.T, session, nonce string) clicore.AgentRequest {
	t.Helper()
	at := time.Now().UTC().Truncate(time.Second)
	c := clicore.HopClaims{
		SenderDeviceID: s.id, TargetDeviceID: self, TargetSessionID: session,
		Tool: "claude", SealedPrompt: "SEALED", IssuedAt: at, Nonce: nonce,
	}
	sig, err := clicore.SignHop(c, s.kp.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	return clicore.AgentRequest{
		SenderDeviceID: s.id, TargetSessionID: session, Tool: "claude", SealedPrompt: "SEALED",
		Signature: sig, IssuedAt: at.Format(time.RFC3339), Nonce: nonce,
		SenderSigningPublicKey: s.kp.PublicKey,
	}
}

func newPins(t *testing.T) *SenderPins {
	t.Helper()
	return &SenderPins{path: filepath.Join(t.TempDir(), "pinned_senders.json")}
}

func TestFirstSignedHopPinsTheSender(t *testing.T) {
	pins := newPins(t)
	a := newSender(t, "dev-a")
	if err := pins.VerifyDelivered(a.signed(t, "s1", "n1"), self, time.Now()); err != nil {
		t.Fatalf("a genuine first hop was refused: %v", err)
	}
	// A second genuine hop verifies against the pin.
	if err := pins.VerifyDelivered(a.signed(t, "s1", "n2"), self, time.Now()); err != nil {
		t.Fatalf("a genuine later hop was refused: %v", err)
	}
}

// The attack this whole feature exists for: after the first contact, the server
// swaps in a key it controls and a hop signed with it.
func TestKeyChangeAfterPinningIsRefused(t *testing.T) {
	pins := newPins(t)
	a := newSender(t, "dev-a")
	if err := pins.VerifyDelivered(a.signed(t, "s1", "n1"), self, time.Now()); err != nil {
		t.Fatal(err)
	}
	forger := newSender(t, "dev-a") // same sender id, the server's own key
	if err := pins.VerifyDelivered(forger.signed(t, "s1", "n2"), self, time.Now()); !errors.Is(err, ErrSenderKeyChanged) {
		t.Fatalf("a hop under a swapped key = %v, want ErrSenderKeyChanged", err)
	}
}

// The subtler version: keep the PINNED key in the delivered field (so the key
// looks unchanged) but sign with the server's own. Only a real signature check
// against the pin catches this one.
func TestForgeryUnderThePinnedKeyIsRefused(t *testing.T) {
	pins := newPins(t)
	a := newSender(t, "dev-a")
	if err := pins.VerifyDelivered(a.signed(t, "s1", "n1"), self, time.Now()); err != nil {
		t.Fatal(err)
	}
	forged := newSender(t, "dev-a").signed(t, "s1", "n2")
	forged.SenderSigningPublicKey = a.kp.PublicKey
	if err := pins.VerifyDelivered(forged, self, time.Now()); !errors.Is(err, clicore.ErrHopSignature) {
		t.Fatalf("a forgery presenting the pinned key = %v, want ErrHopSignature", err)
	}
}

// Downgrade: the server strips the signature off a pinned sender's hop.
func TestUnsignedHopFromAPinnedSenderIsRefused(t *testing.T) {
	pins := newPins(t)
	a := newSender(t, "dev-a")
	if err := pins.VerifyDelivered(a.signed(t, "s1", "n1"), self, time.Now()); err != nil {
		t.Fatal(err)
	}
	stripped := clicore.AgentRequest{SenderDeviceID: "dev-a", TargetSessionID: "s1", Tool: "claude", SealedPrompt: "SEALED"}
	if err := pins.VerifyDelivered(stripped, self, time.Now()); !errors.Is(err, ErrUnsignedFromPinned) {
		t.Fatalf("a stripped hop = %v, want ErrUnsignedFromPinned", err)
	}
}

// Replay: the server re-delivers a hop this machine already ran. It is validly
// signed, so only the local nonce record stops it running twice.
func TestReplayAtTheReceiverIsRefused(t *testing.T) {
	pins := newPins(t)
	a := newSender(t, "dev-a")
	hop := a.signed(t, "s1", "n1")
	if err := pins.VerifyDelivered(hop, self, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := pins.VerifyDelivered(hop, self, time.Now()); !errors.Is(err, ErrHopReplayed) {
		t.Fatalf("a replayed hop = %v, want ErrHopReplayed", err)
	}
}

// A hop signed for another machine must not run on this one.
func TestHopSignedForAnotherDeviceIsRefused(t *testing.T) {
	pins := newPins(t)
	a := newSender(t, "dev-a")
	if err := pins.VerifyDelivered(a.signed(t, "s1", "n1"), "dev-someone-else", time.Now()); !errors.Is(err, clicore.ErrHopSignature) {
		t.Fatalf("a hop for another target = %v, want ErrHopSignature", err)
	}
}

// Redirected to a different session than the one the sender signed for.
func TestRedirectedSessionIsRefused(t *testing.T) {
	pins := newPins(t)
	hop := newSender(t, "dev-a").signed(t, "s1", "n1")
	hop.TargetSessionID = "s-privileged"
	if err := pins.VerifyDelivered(hop, self, time.Now()); !errors.Is(err, clicore.ErrHopSignature) {
		t.Fatalf("a redirected hop = %v, want ErrHopSignature", err)
	}
}

// 7.6: there is no legacy path any more. An unsigned hop is refused even from a
// sender this machine has never seen, so a server cannot slip one in by picking
// a sender id nobody has pinned.
func TestUnsignedFromANeverPinnedSenderIsRefused(t *testing.T) {
	pins := newPins(t)
	legacy := clicore.AgentRequest{SenderDeviceID: "dev-old", TargetSessionID: "s1", Tool: "claude", SealedPrompt: "SEALED"}
	if err := pins.VerifyDelivered(legacy, self, time.Now()); !errors.Is(err, ErrUnsignedHop) {
		t.Fatalf("an unsigned hop from a never-seen sender = %v, want ErrUnsignedHop", err)
	}
}

// A queued hop can wait hours while this machine is off. The receiver checks the
// signature, not freshness — the server refused anything stale at submission.
func TestAnOldButGenuineHopStillVerifies(t *testing.T) {
	pins := newPins(t)
	a := newSender(t, "dev-a")
	hop := a.signed(t, "s1", "n1")
	if err := pins.VerifyDelivered(hop, self, time.Now().Add(12*time.Hour)); err != nil {
		t.Fatalf("a genuine hop delivered 12h later was refused: %v", err)
	}
}

// A pin store that cannot be read must not become "nobody is pinned" — that
// would let every downgrade and key change through.
func TestCorruptPinStoreFailsClosed(t *testing.T) {
	pins := newPins(t)
	if err := os.WriteFile(pins.path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := clicore.AgentRequest{SenderDeviceID: "dev-old", TargetSessionID: "s1", Tool: "claude", SealedPrompt: "SEALED"}
	if err := pins.VerifyDelivered(legacy, self, time.Now()); err == nil {
		t.Fatal("a corrupt pin store let a hop through")
	}
}

func TestPinStoreIsPrivateAndOutsideProjects(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	pins, err := NewSenderPins()
	if err != nil {
		t.Fatal(err)
	}
	if err := pins.VerifyDelivered(newSender(t, "dev-a").signed(t, "s1", "n1"), self, time.Now()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(pins.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("pin store mode = %v, want 0600", info.Mode().Perm())
	}
}
