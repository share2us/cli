package daemon

import (
	"fmt"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli-core/lanid"
	"github.com/share2us/cli-core/lanshare"
)

// approve builds the headless LAN approval decider (ADR-035). It never grants
// trust and never weakens ADR-034: trust comes only from the server-signed cache
// that lanid.Lookup reads.
//
//   - trusted + auto: accepted silently (a notification records it).
//   - trusted + ask:  no human is at a terminal, so it cannot be one-tap
//     approved here; notified and declined. (A future action-button backend,
//     Notifier.SupportsActions, will let this wait for a real choice.)
//   - untrusted:      declined, with a notification pointing the user to the app.
//
// The only way to have a device's transfers land without a prompt is to give it
// "auto" mode, which is MFA-gated in the CLI/GUI. The daemon offers no bypass.
// lookupTrusted resolves a fingerprint against the server-signed trust cache. It
// is a package var only so tests can inject a fixture; production always uses
// lanid.Lookup, which never grants trust (ADR-034).
var lookupTrusted = lanid.Lookup

func (rt *Runtime) approve(policy string, deps Deps) func(lanshare.RequestInfo) bool {
	return func(r lanshare.RequestInfo) bool {
		fp := lanshare.IdentityFingerprint(r.SenderKey)
		label := peerLabel(r)
		if fp != "" {
			if d, ok := lookupTrusted(fp); ok {
				if d.AutoAccept() {
					deps.logf("accepting %s from trusted device %s (auto)", r.Name, label)
					rt.notify("Share2Us", "Receiving "+r.Name+" from "+label)
					return true
				}
				// trusted + ask: needs a human decision the daemon can't render.
				deps.logf("declined %s from %s (ask mode; needs approval in the app)", r.Name, label)
				rt.notify("Share2Us", fmt.Sprintf("%s tried to send %q — approve it in Share2Us", label, r.Name))
				_ = policy // notify-wait vs strict diverge here once action buttons exist
				return false
			}
		}
		deps.logf("blocked %s from untrusted %s", r.Name, label)
		rt.notify("Share2Us", fmt.Sprintf("Blocked a file from %s — open Share2Us to trust it", label))
		return false
	}
}

func peerLabel(r lanshare.RequestInfo) string {
	if r.SenderName != "" && r.PeerIP != "" {
		return fmt.Sprintf("%s (%s)", r.SenderName, r.PeerIP)
	}
	if r.SenderName != "" {
		return r.SenderName
	}
	if r.PeerIP != "" {
		return r.PeerIP
	}
	return "a nearby device"
}

// approvalPolicyValid reports whether p is a known policy string.
func approvalPolicyValid(p string) bool {
	return p == clicore.ApprovalPolicyStrict || p == clicore.ApprovalPolicyNotifyWait
}
