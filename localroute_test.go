package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli-core/lanshare"
)

// The routing DECISION is what these pin: when a --device send goes straight
// across the network, and when it quietly falls back to an upload. The transfer
// itself is exercised for real by the two-node harness in develop/.
//
// The central rule is that every "cannot" is a fallback, never a failure. A
// device answers a probe only while it is actually listening — the desktop app
// with "discoverable on local network" on, or the daemon — so a machine that is
// merely signed in does not answer, and that is the ORDINARY case.

func routeTestApp() (*app, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return &app{stdout: &out, stderr: &errb, stdin: strings.NewReader("")}, &out, &errb
}

func withSeams(t *testing.T, match func(context.Context, []clicore.DeviceRef, clicore.MatchOptions) ([]clicore.LocalMatch, error), send func(context.Context, string, int64, bool, io.Reader, lanshare.SendOptions) (string, error)) {
	t.Helper()
	om, os_ := matchLocalDevices, lanSend
	if match != nil {
		matchLocalDevices = match
	}
	if send != nil {
		lanSend = send
	}
	t.Cleanup(func() { matchLocalDevices, lanSend = om, os_ })
}

func tempFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(p, []byte("hello world"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

const routeFP = "aaaaaaaabbbbbbbbccccccccddddddddeeeeeeeeffffffff0000000011111111"

// A device with no fingerprint cannot be matched, and must not cost a scan.
// Older clients and browser sessions are in this state, so it is the path most
// users are on until the fleet updates.
func TestNoFingerprintSkipsDiscoveryEntirely(t *testing.T) {
	scanned := false
	withSeams(t, func(context.Context, []clicore.DeviceRef, clicore.MatchOptions) ([]clicore.LocalMatch, error) {
		scanned = true
		return nil, nil
	}, nil)

	a, _, errb := routeTestApp()
	if a.tryLocalDelivery(t.Context(), tempFile(t), clicore.DeviceSession{ID: "s1", DeviceName: "laptop"}) {
		t.Fatal("claimed local delivery for a device with nothing to match on")
	}
	if scanned {
		t.Error("scanned the network for a device that has no fingerprint")
	}
	if errb.Len() != 0 {
		t.Errorf("stderr = %q, want silence: this is the ordinary case, not a warning", errb.String())
	}
}

// The common case: the device is signed in but not listening, so nothing
// answers and the file is uploaded.
//
// This is the ONE moment the user can act, so it is worth a line: the upload
// about to happen spends quota the direct path would not have, and the remedy is
// a setting on the other machine. Saying only "uploading" would charge them
// without telling them there was a free path; saying nothing would hide it
// entirely (P1-5).
func TestNothingAnsweringExplainsTheCostAndTheRemedy(t *testing.T) {
	withSeams(t, func(context.Context, []clicore.DeviceRef, clicore.MatchOptions) ([]clicore.LocalMatch, error) {
		return nil, nil
	}, nil)

	a, _, errb := routeTestApp()
	if a.tryLocalDelivery(t.Context(), tempFile(t), clicore.DeviceSession{ID: "s1", DeviceName: "laptop", LanFingerprint: routeFP}) {
		t.Fatal("claimed local delivery with no peer answering")
	}
	got := errb.String()
	if !strings.Contains(got, "laptop") {
		t.Errorf("stderr = %q, want it to name the device", got)
	}
	if !strings.Contains(got, "quota") {
		t.Errorf("stderr = %q, want it to say the upload spends quota", got)
	}
	if !strings.Contains(got, "discoverable") {
		t.Errorf("stderr = %q, want it to say how to get the direct path", got)
	}
}

// A device with no fingerprint gets NO such note. It cannot be reached directly
// however the user configures it, so advice about "discoverable" would be a
// suggestion they cannot act on — the same fault as telling someone to install
// the app on their browser.
func TestAnUnmatchableDeviceGetsNoAdviceItCannotActOn(t *testing.T) {
	withSeams(t, func(context.Context, []clicore.DeviceRef, clicore.MatchOptions) ([]clicore.LocalMatch, error) {
		return nil, nil
	}, nil)

	a, _, errb := routeTestApp()
	a.tryLocalDelivery(t.Context(), tempFile(t), clicore.DeviceSession{ID: "s1", DeviceName: "old-laptop"})
	if strings.Contains(errb.String(), "discoverable") {
		t.Errorf("stderr = %q, want no advice for a device that cannot be matched at all", errb.String())
	}
}

func TestAMatchedDeviceIsSentToDirectly(t *testing.T) {
	var got lanshare.SendOptions
	var gotName string
	withSeams(t,
		func(context.Context, []clicore.DeviceRef, clicore.MatchOptions) ([]clicore.LocalMatch, error) {
			return []clicore.LocalMatch{{SessionID: "s1", Peer: lanshare.ScannedPeer{
				Host: "192.168.1.9", Port: 7345, Fingerprint: "cert-fp", IdentityFingerprint: routeFP, Name: "laptop",
			}}}, nil
		},
		func(_ context.Context, name string, _ int64, _ bool, _ io.Reader, o lanshare.SendOptions) (string, error) {
			gotName, got = name, o
			return "sha", nil
		})

	a, out, _ := routeTestApp()
	if !a.tryLocalDelivery(t.Context(), tempFile(t), clicore.DeviceSession{ID: "s1", DeviceName: "laptop", LanFingerprint: routeFP}) {
		t.Fatal("want direct delivery")
	}
	if gotName != "note.txt" {
		t.Errorf("sent name = %q, want note.txt", gotName)
	}
	if got.Dest != "192.168.1.9:7345" {
		t.Errorf("dest = %q, want the peer that answered", got.Dest)
	}
	// THE PIN IS THE AUTHENTICATION. The scan read a device card out of this
	// peer's certificate and the card's signature covers that certificate's own
	// public key, so pinning its fingerprint is what makes a passwordless send
	// safe. Without the pin, TLS would accept any certificate and an on-path
	// attacker could take the transfer.
	if got.PinFingerprint != "cert-fp" {
		t.Errorf("PinFingerprint = %q, want the scanned certificate fingerprint", got.PinFingerprint)
	}
	if got.Password != "" {
		t.Errorf("Password = %q, want empty: the pin authenticates this send", got.Password)
	}
	if got.Identity == nil {
		t.Error("no sender identity: the receiver cannot recognise this device")
	}
	if !strings.Contains(out.String(), "directly") {
		t.Errorf("stdout = %q, want it to say the file went directly", out.String())
	}
}

// A peer answered a probe and then would not take the file — it stopped
// listening, or is waiting on an approval nobody is there to give. The user must
// end up with the file delivered, so this falls back to the upload and says so.
func TestASendThatFailsFallsBackAndSaysSo(t *testing.T) {
	withSeams(t,
		func(context.Context, []clicore.DeviceRef, clicore.MatchOptions) ([]clicore.LocalMatch, error) {
			return []clicore.LocalMatch{{SessionID: "s1", Peer: lanshare.ScannedPeer{Host: "10.0.0.9", Port: 7345, Fingerprint: "cert-fp"}}}, nil
		},
		func(context.Context, string, int64, bool, io.Reader, lanshare.SendOptions) (string, error) {
			return "", errors.New("connection reset")
		})

	a, _, errb := routeTestApp()
	if a.tryLocalDelivery(t.Context(), tempFile(t), clicore.DeviceSession{ID: "s1", LanFingerprint: routeFP}) {
		t.Fatal("reported delivery for a send that failed")
	}
	if !strings.Contains(errb.String(), "uploading instead") {
		t.Errorf("stderr = %q, want it to say it is uploading instead", errb.String())
	}
}

// Discovery itself failing (no route, tailscale absent) must not break the send.
func TestADiscoveryErrorFallsBackToTheUpload(t *testing.T) {
	withSeams(t, func(context.Context, []clicore.DeviceRef, clicore.MatchOptions) ([]clicore.LocalMatch, error) {
		return nil, errors.New("network unreachable")
	}, nil)

	a, _, errb := routeTestApp()
	if a.tryLocalDelivery(t.Context(), tempFile(t), clicore.DeviceSession{ID: "s1", LanFingerprint: routeFP}) {
		t.Fatal("reported delivery after discovery failed")
	}
	if !strings.Contains(errb.String(), "uploading instead") {
		t.Errorf("stderr = %q, want it to explain the fallback", errb.String())
	}
}

// A folder is zipped before it goes, the same as the `--send` path does, and the
// receiver is told it is an archive.
func TestAFolderIsZippedForADirectSend(t *testing.T) {
	var gotIsDir bool
	var gotName string
	withSeams(t,
		func(context.Context, []clicore.DeviceRef, clicore.MatchOptions) ([]clicore.LocalMatch, error) {
			return []clicore.LocalMatch{{SessionID: "s1", Peer: lanshare.ScannedPeer{Host: "10.0.0.9", Port: 7345, Fingerprint: "cert-fp"}}}, nil
		},
		func(_ context.Context, name string, _ int64, isDir bool, _ io.Reader, _ lanshare.SendOptions) (string, error) {
			gotName, gotIsDir = name, isDir
			return "sha", nil
		})

	dir := filepath.Join(t.TempDir(), "docs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	a, _, _ := routeTestApp()
	if !a.tryLocalDelivery(t.Context(), dir, clicore.DeviceSession{ID: "s1", LanFingerprint: routeFP}) {
		t.Fatal("want direct delivery of the folder")
	}
	if !gotIsDir {
		t.Error("isDir = false, want the receiver told this is an archive")
	}
	if !strings.HasSuffix(gotName, ".zip") {
		t.Errorf("name = %q, want a .zip", gotName)
	}
}
