// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	clicore "github.com/share2us/cli-core"
)

// Receiver-side hop verification (ADR-041 §5). This is the check that matters.
//
// The server verifies a hop's signature before queuing it, which stops a forgery
// at the edge — but a compromised server could simply skip that check, or queue
// a hop it wrote itself. The only defence against a lying server is a check on
// the RECEIVING machine, against a key the server cannot change after the fact.
//
// So the daemon pins each sender's signing key the first time it sees a signed
// hop from that sender (trust on first use, like SSH's known_hosts), and from
// then on:
//
//   - every hop from that sender must be signed, and verify against the PINNED
//     key — never against the key the server delivers alongside it;
//   - a hop arriving with a different key is refused: the server is either lying
//     or has been told something it should not believe;
//   - an UNSIGNED hop from a pinned sender is refused, which is what stops a
//     compromised server downgrading by stripping signatures off;
//   - a nonce already seen from that sender is refused, so a validly signed hop
//     cannot be replayed at this machine and run twice.
//
// Pinning on first delivery rather than at approval is not weaker: at approval
// time the key would also come from the server. What makes trust-on-first-use
// safe is not when the pin is taken but that it can never change afterwards.

var (
	// ErrSenderKeyChanged is a hop whose delivered key differs from the pinned one.
	ErrSenderKeyChanged = errors.New("the sender's signing key does not match the one this machine pinned")
	// ErrUnsignedFromPinned is an unsigned hop from a sender that has signed before.
	ErrUnsignedFromPinned = errors.New("this sender signs its hops, but this one is unsigned")
	// ErrHopReplayed is a nonce this machine has already run from this sender.
	ErrHopReplayed = errors.New("this hop has already been delivered once")
)

// seenNonceTTL bounds how long a nonce is remembered. The server refuses a hop
// older than its freshness window at submission and the queue holds a hop for a
// day at most, so a month is comfortably longer than any hop can legitimately
// take to arrive, and the file stays small.
const seenNonceTTL = 30 * 24 * time.Hour

type pinnedSender struct {
	SigningPublicKey string    `json:"signing_public_key"`
	PinnedAt         time.Time `json:"pinned_at"`
}

type senderStore struct {
	Version int                     `json:"version"`
	Pinned  map[string]pinnedSender `json:"pinned"`
	// Seen maps "sender|nonce" to when it was run.
	Seen map[string]time.Time `json:"seen"`
}

// SenderPins is the on-disk store of pinned sender keys and seen nonces. It is
// shared by concurrent inject handlers, so every read-modify-write holds mu.
type SenderPins struct {
	mu   sync.Mutex
	path string
}

// SenderPinsPath lives beside the bindings and enforced policies, outside any
// project, so an injected run cannot rewrite who this machine trusts.
func SenderPinsPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "share2us", "agents", "pinned_senders.json"), nil
}

// NewSenderPins opens (lazily) the store at its default path.
func NewSenderPins() (*SenderPins, error) {
	p, err := SenderPinsPath()
	if err != nil {
		return nil, err
	}
	return &SenderPins{path: p}, nil
}

func (s *SenderPins) load() (senderStore, error) {
	st := senderStore{Version: 1, Pinned: map[string]pinnedSender{}, Seen: map[string]time.Time{}}
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	// A store we cannot parse must not silently become "nobody is pinned": that
	// would let every downgrade and key change through. Refuse to proceed.
	if err := json.Unmarshal(raw, &st); err != nil {
		return st, err
	}
	if st.Pinned == nil {
		st.Pinned = map[string]pinnedSender{}
	}
	if st.Seen == nil {
		st.Seen = map[string]time.Time{}
	}
	return st, nil
}

func (s *SenderPins) save(st senderStore, now time.Time) error {
	for k, at := range st.Seen {
		if now.Sub(at) > seenNonceTTL {
			delete(st.Seen, k)
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	out, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	// Write-then-rename, so a crash mid-write cannot leave a truncated store —
	// which load() would, correctly, refuse to trust.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// VerifyDelivered decides whether a delivered hop may run on this machine. It
// returns nil to run it, or the reason it must not. On success it records the
// nonce (and, on a sender's first signed hop, pins its key), so calling it twice
// for the same hop refuses the second — which is the point.
//
// selfDeviceID is THIS device's session id: the target the sender signed for.
func (s *SenderPins) VerifyDelivered(req clicore.AgentRequest, selfDeviceID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := s.load()
	if err != nil {
		return err
	}
	pin, pinned := st.Pinned[req.SenderDeviceID]

	if req.Signature == "" {
		if pinned {
			return ErrUnsignedFromPinned
		}
		// A sender that has never signed: the legacy path, until every sender has
		// a key. Nothing to pin and nothing to check a nonce against.
		return nil
	}

	key := pin.SigningPublicKey
	if !pinned {
		key = req.SenderSigningPublicKey
		if key == "" {
			return clicore.ErrHopSignature
		}
	} else if req.SenderSigningPublicKey != "" && req.SenderSigningPublicKey != pin.SigningPublicKey {
		return ErrSenderKeyChanged
	}

	issuedAt, err := time.Parse(time.RFC3339, req.IssuedAt)
	if err != nil {
		return clicore.ErrHopSignature
	}
	claims := clicore.HopClaims{
		SenderDeviceID:  req.SenderDeviceID,
		TargetDeviceID:  selfDeviceID,
		TargetSessionID: req.TargetSessionID,
		Tool:            req.Tool,
		SealedPrompt:    req.SealedPrompt,
		SealedFileKey:   req.SealedFileKey,
		GoalID:          req.GoalID,
		IssuedAt:        issuedAt,
		Nonce:           req.Nonce,
	}
	// Signature only, not freshness: a hop can wait in the queue for hours while
	// this machine is off, and the server already refused anything stale at
	// submission. Replay is caught by the nonce below instead.
	if err := clicore.VerifyHop(claims, req.Signature, key); err != nil {
		return err
	}
	seenKey := req.SenderDeviceID + "|" + req.Nonce
	if _, dup := st.Seen[seenKey]; dup {
		return ErrHopReplayed
	}

	st.Seen[seenKey] = now
	if !pinned {
		st.Pinned[req.SenderDeviceID] = pinnedSender{SigningPublicKey: key, PinnedAt: now}
	}
	return s.save(st, now)
}
