// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli-core/lanid"
	"github.com/share2us/cli-core/lanshare"
)

// Local-first routing for `--device` (ADR-040).
//
// Sending to one of your own machines used to mean uploading the file, having
// the recipient download it, and spending quota on both — even when the machine
// was on the same network. The bytes never needed to leave the building. This
// tries the direct path first and falls back to the cloud when it cannot.
//
// WHY THE FALLBACK IS THE COMMON CASE, not an error: discovery finds RECEIVERS.
// A device answers a probe only while it is actually listening — the desktop app
// with "discoverable on local network" on, or the daemon running. A laptop that
// is merely signed in does not answer. So "not reachable locally" is the ordinary
// outcome and must never look like a failure.

// Seams for the tests. Both of these reach the network, and the behaviour worth
// pinning is the DECISION — when to go direct, and when to fall back — not the
// transfer itself, which the two-node docker harness in develop/ exercises for
// real. Kept package-private here rather than exported from cli-core, so a
// published module's API does not grow a test hook.
var (
	matchLocalDevices = clicore.MatchLocalDevices
	lanSend           = lanshare.Send
)

// tryLocalDelivery attempts to hand path straight to target over the local
// network. It reports whether the file was delivered.
//
// It returns false for every "cannot", never an error: no fingerprint to match
// on, nothing answering, or a direct send that did not complete. The caller then
// uploads exactly as it always did. A send must never fail because discovery
// did.
func (a *app) tryLocalDelivery(ctx context.Context, path string, target clicore.DeviceSession) bool {
	if target.LanFingerprint == "" {
		// A client older than the fingerprint, or a session that never published
		// one. Nothing to match against; not worth a word to the user.
		return false
	}

	matches, err := matchLocalDevices(ctx,
		[]clicore.DeviceRef{{SessionID: target.ID, LanFingerprint: target.LanFingerprint}},
		clicore.MatchOptions{})
	if err != nil {
		// Discovery failed (no route, a hostile network, tailscale absent). The
		// upload still works, so this is a note rather than a failure.
		fmt.Fprintf(a.stderr, "note: could not check the local network (%v); uploading instead\n", err)
		return false
	}
	if len(matches) == 0 {
		// The device could have been reached directly — it publishes a fingerprint,
		// so it is a current client — and simply is not listening. Say so, because
		// this is the one moment the user can do something about it: the upload
		// about to happen spends quota that the direct path would not have. One
		// actionable line, only in the case where the free path was genuinely
		// available (P1-5, ADR-040).
		fmt.Fprintf(a.stderr, "%s isn't listening on this network, so the file will be uploaded (this uses your quota).\n", deviceLabelFor(target))
		fmt.Fprintf(a.stderr, "  to send directly next time, turn on \"discoverable on local network\" there, or run `%s receive` on it\n", commandName)
		return false
	}
	peer := matches[0].Peer

	body, name, size, isDir, cleanup, ok := a.openForLanSend(path)
	if !ok {
		return false
	}
	defer body.Close()
	if cleanup != nil {
		defer cleanup()
	}

	// THE PIN IS THE AUTHENTICATION, which is why no password is needed here.
	//
	// The scan completed a TLS handshake and read a device card out of the peer's
	// certificate. cardFromCert only returns a card whose signature covers THAT
	// certificate's own public key, so a verified card binds the identity key to
	// that exact certificate. The identity matched one of this account's devices,
	// therefore pinning the certificate fingerprint from the same handshake gives
	// an authenticated channel to that device and nothing else. An impostor would
	// need the certificate's private key.
	//
	// The certificate is per-session, so a peer that restarted between the scan
	// and this send presents a different one and the pin fails — which falls back
	// to the cloud, exactly as it should.
	id, idErr := lanid.Identity()
	if idErr != nil {
		// Without an identity the receiver cannot recognise this device, and a
		// trusted-sender receiver would refuse. Upload instead of sending
		// something the far end will probably reject.
		return false
	}
	hostname, _ := os.Hostname()

	prog := newProgressPrinter(a.stderr, "sending directly")
	fmt.Fprintf(a.stderr, "%s is on this network; sending directly (no upload, no quota used)...\n", displayDeviceName(target, peer))
	_, err = lanSend(ctx, name, size, isDir, body, lanshare.SendOptions{
		Dest:           peer.Addr(),
		PinFingerprint: peer.Fingerprint,
		OnProgress:     prog.update,
		Identity:       id,
		SenderName:     hostname,
	})
	prog.finish()
	if err != nil {
		// The peer answered a probe and then would not take the file: it may have
		// stopped listening, or be waiting on an approval nobody is there to give.
		// Say so plainly and upload, rather than leaving the user with nothing.
		fmt.Fprintf(a.stderr, "direct send did not complete (%v); uploading instead\n", err)
		return false
	}
	fmt.Fprintf(a.stdout, "Sent %s (%s) directly to %s\n", name, humanBytes(size), displayDeviceName(target, peer))
	return true
}

// openForLanSend prepares path the way the `--send` path does: a folder is
// zipped to a temp file first, a file is opened as-is.
func (a *app) openForLanSend(path string) (body *os.File, name string, size int64, isDir bool, cleanup func(), ok bool) {
	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintf(a.stderr, "note: %v; uploading instead\n", err)
		return nil, "", 0, false, nil, false
	}
	if info.IsDir() {
		zipPath, zerr := zipDirectory(path)
		if zerr != nil {
			fmt.Fprintf(a.stderr, "note: could not zip the folder for a direct send (%v); uploading instead\n", zerr)
			return nil, "", 0, false, nil, false
		}
		cleanup = func() { _ = os.Remove(zipPath) }
		f, oerr := os.Open(zipPath)
		if oerr != nil {
			cleanup()
			fmt.Fprintf(a.stderr, "note: %v; uploading instead\n", oerr)
			return nil, "", 0, false, nil, false
		}
		size := int64(0)
		if zi, e := f.Stat(); e == nil {
			size = zi.Size()
		}
		return f, directoryZipName(path), size, true, cleanup, true
	}
	f, oerr := os.Open(path)
	if oerr != nil {
		fmt.Fprintf(a.stderr, "note: %v; uploading instead\n", oerr)
		return nil, "", 0, false, nil, false
	}
	return f, filepath.Base(path), info.Size(), false, nil, true
}

// deviceLabelFor names the device the way the user just referred to it.
func deviceLabelFor(target clicore.DeviceSession) string {
	if n := target.DeviceName; n != "" {
		return n
	}
	return "that device"
}

// displayDeviceName prefers the name the account knows the device by, because
// that is the one the user typed after --device. The peer's own card name is the
// fallback, and the address is the last resort.
func displayDeviceName(target clicore.DeviceSession, peer lanshare.ScannedPeer) string {
	if n := target.DeviceName; n != "" {
		return n
	}
	if peer.Name != "" {
		return peer.Name
	}
	return peer.Addr()
}
