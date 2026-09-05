package daemon

import (
	"crypto/ed25519"
	"testing"

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
