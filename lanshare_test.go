// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/share2us/cli-core/lanshare"
)

// W-M4: a fingerprint learned from mDNS is attacker-choosable, so a password-less
// send to a DISCOVERED peer must be confirmed against the code on the receiver's
// own screen. These cover the gate itself.
func TestConfirmDiscoveredPeerRequiresMatchingAnswer(t *testing.T) {
	const fp = "aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99"
	for _, tc := range []struct {
		name   string
		answer string
		want   bool
	}{
		{"yes proceeds", "y\n", true},
		{"explicit yes proceeds", "yes\n", true},
		{"no refuses", "n\n", false},
		{"empty defaults to no", "\n", false},
		{"garbage refuses", "maybe\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			a := app{stdout: io.Discard, stderr: &stderr, stdin: strings.NewReader(tc.answer)}
			if got := a.confirmDiscoveredPeerWith("192.168.1.9:7000", fp, true); got != tc.want {
				t.Fatalf("confirmDiscoveredPeer = %v, want %v", got, tc.want)
			}
			// The user can only compare what they are shown.
			if code := lanshare.VerifyCode(fp); !strings.Contains(stderr.String(), code) {
				t.Fatalf("the verify code %q was never shown:\n%s", code, stderr.String())
			}
		})
	}
}

// Fails closed: with no terminal there is no one to compare codes, so it must
// refuse rather than send to whoever answered to that name.
func TestConfirmDiscoveredPeerRefusesWithoutATerminal(t *testing.T) {
	var stderr bytes.Buffer
	// strings.Reader is not an *os.File, so it is not a terminal.
	a := app{stdout: io.Discard, stderr: &stderr, stdin: strings.NewReader("y\n")}
	if a.confirmDiscoveredPeerWith("192.168.1.9:7000", "aa:bb:cc:dd", false) {
		t.Fatal("sent to a discovered peer with no terminal to confirm on")
	}
	if !strings.Contains(stderr.String(), "pairing string") {
		t.Fatalf("the refusal must name a non-impersonable alternative:\n%s", stderr.String())
	}
}

func TestConfirmDiscoveredPeerRefusesWithoutAFingerprint(t *testing.T) {
	var stderr bytes.Buffer
	a := app{stdout: io.Discard, stderr: &stderr, stdin: strings.NewReader("y\n")}
	if a.confirmDiscoveredPeerWith("192.168.1.9:7000", "", true) {
		t.Fatal("accepted a peer that advertised nothing to verify")
	}
}

// The receiver must print the code the sender is asked to match, or the whole
// comparison is impossible.
func TestReceiveBannerShowsTheVerifyCode(t *testing.T) {
	const fp = "aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99"
	var stderr bytes.Buffer
	a := app{stdout: io.Discard, stderr: &stderr}
	a.printReceiveBanner(lanshare.ListenInfo{
		BindAddr: "127.0.0.1", Port: 7000, Fingerprint: fp, Mode: lanshare.ModeOpen,
	}, lanReceiveOpts{bind: "127.0.0.1"})
	code := lanshare.VerifyCode(fp)
	if !strings.Contains(stderr.String(), code) {
		t.Fatalf("banner omits the verify code %q:\n%s", code, stderr.String())
	}
}

// The approval prompt is the strongest thing about open mode, and the banner
// never mentioned it — it advertised the VERIFY code, which is what the SENDER
// is asked to confirm, so nothing on screen told the receiver a prompt was
// coming (todo W-L2).
func TestReceiveBannerAdvertisesTheApprovalPrompt(t *testing.T) {
	var stderr bytes.Buffer
	a := app{stdout: io.Discard, stderr: &stderr}
	a.printReceiveBanner(lanshare.ListenInfo{
		BindAddr: "127.0.0.1", Port: 7000, Fingerprint: "aa:bb", Mode: lanshare.ModeOpen,
	}, lanReceiveOpts{bind: "127.0.0.1"})
	out := stderr.String()
	if !strings.Contains(out, "asked to accept or reject") {
		t.Fatalf("banner does not say a transfer is approved before it lands:\n%s", out)
	}
	if !strings.Contains(out, "before anything is written") {
		t.Errorf("the promise that matters is that nothing is written first:\n%s", out)
	}
}

// With --yes there is no prompt, so the banner must not claim there is one.
func TestReceiveBannerIsHonestUnderYes(t *testing.T) {
	var stderr bytes.Buffer
	a := app{stdout: io.Discard, stderr: &stderr}
	a.printReceiveBanner(lanshare.ListenInfo{
		BindAddr: "127.0.0.1", Port: 7000, Fingerprint: "aa:bb", Mode: lanshare.ModeOpen,
	}, lanReceiveOpts{bind: "127.0.0.1", yes: true})
	out := stderr.String()
	if strings.Contains(out, "asked to accept") {
		t.Fatalf("--yes accepts automatically; the banner must not promise a prompt:\n%s", out)
	}
	if !strings.Contains(out, "accepted automatically") {
		t.Errorf("--yes should say so plainly:\n%s", out)
	}
}

// Password and allow-ip modes are not open mode; the prompt line belongs only to
// the mode that has a prompt.
func TestReceiveBannerOmitsTheApprovalLineOutsideOpenMode(t *testing.T) {
	for _, mode := range []string{lanshare.ModePassword, lanshare.ModeAllowIP} {
		var stderr bytes.Buffer
		a := app{stdout: io.Discard, stderr: &stderr}
		a.printReceiveBanner(lanshare.ListenInfo{
			BindAddr: "127.0.0.1", Port: 7000, Fingerprint: "aa:bb",
			Mode: mode, Passphrase: "swift-otter-lamp",
		}, lanReceiveOpts{bind: "127.0.0.1", allowIPs: []string{"10.0.0.2"}})
		if strings.Contains(stderr.String(), "asked to accept or reject") {
			t.Errorf("mode %q has no approval prompt but the banner claims one:\n%s", mode, stderr.String())
		}
	}
}
