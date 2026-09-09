// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"crypto/ed25519"
	clicore "github.com/share2us/cli-core"
	"testing"
	"time"

	"github.com/share2us/cli-core/lanid"
	"github.com/share2us/cli-core/lanshare"
)

type recordingNotifier struct{ msgs []string }

func (r *recordingNotifier) Info(_, message string) { r.msgs = append(r.msgs, message) }
func (r *recordingNotifier) SupportsActions() bool  { return false }

func TestApprovePolicyHonorsADR034(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	fp := lanshare.IdentityFingerprint(pub)

	cases := []struct {
		name   string
		lookup func(string) (lanid.TrustedDevice, bool)
		accept bool
	}{
		{
			name:   "trusted auto -> accept",
			lookup: func(string) (lanid.TrustedDevice, bool) { return lanid.TrustedDevice{Mode: lanid.ModeAuto}, true },
			accept: true,
		},
		{
			name:   "trusted ask -> reject (no human at a terminal)",
			lookup: func(string) (lanid.TrustedDevice, bool) { return lanid.TrustedDevice{Mode: lanid.ModeAsk}, true },
			accept: false,
		},
		{
			name:   "untrusted -> reject",
			lookup: func(string) (lanid.TrustedDevice, bool) { return lanid.TrustedDevice{}, false },
			accept: false,
		},
	}

	orig := lookupTrusted
	defer func() { lookupTrusted = orig }()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookupTrusted = tc.lookup
			rt := &Runtime{notifier: &recordingNotifier{}}
			decide := rt.approve("strict", Deps{})
			got := decide(lanshare.RequestInfo{Name: "secret.pdf", SenderKey: pub, PeerIP: "10.0.0.9"})
			if got != tc.accept {
				t.Fatalf("approve = %v, want %v (fp=%s)", got, tc.accept, fp)
			}
		})
	}
}

func TestApproveAnonymousSenderRejected(t *testing.T) {
	// A sender with no identity key can never be trusted, so it is always
	// rejected by the headless daemon.
	rt := &Runtime{notifier: &recordingNotifier{}}
	decide := rt.approve("notify-wait", Deps{})
	if decide(lanshare.RequestInfo{Name: "x", SenderKey: nil}) {
		t.Fatal("anonymous sender was accepted")
	}
}

func TestApprovalPolicyValid(t *testing.T) {
	if !approvalPolicyValid("strict") || !approvalPolicyValid("notify-wait") {
		t.Fatal("known policies rejected")
	}
	if approvalPolicyValid("bogus") {
		t.Fatal("bogus policy accepted")
	}
}

// countingNotifier records what reached the desktop.
type countingNotifier struct{ messages []string }

func (n *countingNotifier) Info(_, message string) { n.messages = append(n.messages, message) }
func (n *countingNotifier) SupportsActions() bool  { return false }

// §AJ #31: a REFUSED attempt is attacker-controlled -- anything on the network
// can connect and be declined as fast as it likes -- and each one fired a
// desktop notification. That is a notification flood, and it buries the one
// message that matters.
func TestRepeatedRefusalsFromOnePeerNotifyOnce(t *testing.T) {
	notifier := &countingNotifier{}
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	rt := &Runtime{notifier: notifier}
	rt.refusals.now = func() time.Time { return now }
	decide := rt.approve(clicore.ApprovalPolicyStrict, Deps{Logf: func(string, ...any) {}})

	req := lanshare.RequestInfo{Name: "payload.bin", PeerIP: "192.168.1.50"}
	for i := 0; i < 200; i++ {
		if decide(req) {
			t.Fatal("an untrusted sender was accepted")
		}
	}
	if len(notifier.messages) != 1 {
		t.Fatalf("%d notifications for 200 refusals from one peer", len(notifier.messages))
	}

	// A different peer is still worth telling the user about.
	other := lanshare.RequestInfo{Name: "payload.bin", PeerIP: "192.168.1.99"}
	decide(other)
	if len(notifier.messages) != 2 {
		t.Fatalf("a second peer produced %d notifications total, want 2", len(notifier.messages))
	}

	// After the cooldown the first peer can raise its hand again.
	now = now.Add(refusalNotifyCooldown + time.Second)
	decide(req)
	if len(notifier.messages) != 3 {
		t.Fatalf("after the cooldown: %d notifications, want 3", len(notifier.messages))
	}
}
