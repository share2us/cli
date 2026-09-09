// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"fmt"
	"sync"
	"time"

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

// A REFUSED attempt is attacker-controlled: anything on the network can open a
// connection and be declined, as fast as it likes. Firing a desktop
// notification per refusal turned that into a notification flood -- unusable
// desktop, and the real "approve it in Share2Us" message buried among hundreds
// (§AJ #31). Refusals are therefore collapsed per peer: one notification, then
// silence for the cooldown however many more arrive. Accepted transfers, which
// only a trusted device can cause, are not rate limited.
const refusalNotifyCooldown = 5 * time.Minute

type refusalNotices struct {
	mu   sync.Mutex
	last map[string]time.Time
	now  func() time.Time
}

// shouldNotify reports whether this peer's refusal is worth a notification now.
func (n *refusalNotices) shouldNotify(peer string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.now == nil {
		n.now = time.Now
	}
	now := n.now()
	if n.last == nil {
		n.last = map[string]time.Time{}
	}
	// Bound the map: a peer that has been quiet for a cooldown is forgotten.
	if len(n.last) > 1024 {
		for k, t := range n.last {
			if now.Sub(t) >= refusalNotifyCooldown {
				delete(n.last, k)
			}
		}
	}
	if at, ok := n.last[peer]; ok && now.Sub(at) < refusalNotifyCooldown {
		return false
	}
	n.last[peer] = now
	return true
}

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
				if rt.refusals.shouldNotify(label) {
					rt.notify("Share2Us", fmt.Sprintf("%s tried to send %q — approve it in Share2Us", label, r.Name))
				}
				_ = policy // notify-wait vs strict diverge here once action buttons exist
				return false
			}
		}
		deps.logf("blocked %s from untrusted %s", r.Name, label)
		if rt.refusals.shouldNotify(label) {
			rt.notify("Share2Us", fmt.Sprintf("Blocked a file from %s — open Share2Us to trust it", label))
		}
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
