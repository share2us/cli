// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/share2us/cli-core/daemonctl"
)

// The Share2Us channel (owner, 2026-09-29): a Claude session started through
// `s2u claude` loads `s2u agent channel`, an MCP channel server. It polls the
// daemon for hops addressed to its session and pushes them INTO that open
// session, where its owner reads the prompt and the work. The hub below is the
// daemon's side of that: who is listening, what is queued for them, and what
// came back.

// ChannelDelivery is one hop handed to a session's channel.
type ChannelDelivery struct {
	RequestID string `json:"request_id"`
	Prompt    string `json:"prompt"`
	From      string `json:"from,omitempty"`
	// Strict: the daemon runs agents read-only (--agent-strict); the hook
	// enforces that too while this hop is in progress.
	Strict bool `json:"-"`
}

// channelAlive is how recently a channel must have polled to count as listening.
const channelAlive = 5 * time.Second

type channelHub struct {
	mu       sync.Mutex
	seen     map[string]time.Time         // session -> last poll
	queue    map[string][]ChannelDelivery // session -> not yet picked up
	picked   map[string]chan struct{}     // request -> closed when picked up
	result   map[string]chan string       // request -> the result, once
	aborted  map[string]chan struct{}     // typed request -> closed if owner abandons the turn
	active   map[string]map[string]bool   // session -> requests in progress
	owner    map[string]string            // request -> session
	guard    map[string]bool              // session -> SessionStart hook proved loaded
	proven   map[string]bool              // session -> a channel-delivered request was answered via report
	channel  map[string]bool              // request -> requires an explicit report, never Stop
	reported map[string]string            // request -> result, held until the guarded turn ends
}

func newChannelHub() *channelHub {
	return &channelHub{
		seen: map[string]time.Time{}, queue: map[string][]ChannelDelivery{},
		picked: map[string]chan struct{}{}, result: map[string]chan string{}, aborted: map[string]chan struct{}{},
		active: map[string]map[string]bool{}, owner: map[string]string{},
		guard: map[string]bool{}, proven: map[string]bool{}, channel: map[string]bool{},
		reported: map[string]string{},
	}
}

func (h *channelHub) markGuardReady(session string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if session != "" {
		h.guard[session] = true
		delete(h.proven, session)
	}
}

// channelReady is separate from guardedAlive: polling and hooks do not prove
// Claude can receive notifications. A typed request's report proves only the
// report tool, not channel delivery; only a channel-delivered request can
// establish readiness. Each channel request still needs its own report.
func (h *channelHub) channelReady(session string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	seen, ok := h.seen[session]
	return ok && time.Since(seen) < channelAlive && h.guard[session] && h.proven[session]
}

// guardedAlive proves both halves started by `s2u claude` are present: the
// channel is polling now and its settings ran the SessionStart guard hook.
func (h *channelHub) guardedAlive(session string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	seen, ok := h.seen[session]
	return ok && time.Since(seen) < channelAlive && h.guard[session]
}

// alive reports whether a channel for session polled recently.
func (h *channelHub) alive(session string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.seen[session]
	return ok && time.Since(t) < channelAlive
}

// deliver queues d for session's channel. picked closes when the channel takes
// it; result yields what the agent explicitly reported for this request.
func (h *channelHub) deliver(session string, d ChannelDelivery) (picked <-chan struct{}, result <-chan string, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.occupied(session) {
		return nil, nil, false
	}
	p, r := make(chan struct{}), make(chan string, 1)
	h.queue[session] = append(h.queue[session], d)
	h.picked[d.RequestID], h.result[d.RequestID], h.owner[d.RequestID] = p, r, session
	h.channel[d.RequestID] = true
	return p, r, true
}

// beginTyped marks a hop active before any bytes are pasted. That makes the
// PreToolUse hook enforce its guardrails even if the owner submits the text in
// the small interval before the daemon's post-paste check. One session accepts
// exactly one queued or active hop.
func (h *channelHub) beginTyped(session string, d ChannelDelivery) (<-chan string, <-chan struct{}, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.occupied(session) {
		return nil, nil, false
	}
	r := make(chan string, 1)
	abort := make(chan struct{})
	if h.active[session] == nil {
		h.active[session] = map[string]bool{}
	}
	h.active[session][d.RequestID] = d.Strict
	h.result[d.RequestID], h.owner[d.RequestID] = r, session
	h.aborted[d.RequestID] = abort
	return r, abort, true
}

// abandonTyped ends only the typed request whose prompt is no longer the last
// human turn. It never reports success and cannot alter a channel delivery.
func (h *channelHub) abandonTyped(session, requestID string) bool {
	if session == "" || requestID == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.owner[requestID] != session || h.channel[requestID] {
		return true // idempotent after daemon restart or an earlier abandonment
	}
	if abort, ok := h.aborted[requestID]; ok {
		close(abort)
	}
	h.forget(requestID)
	return true
}

// occupied is called with h.mu held.
func (h *channelHub) occupied(session string) bool {
	return len(h.queue[session]) != 0 || len(h.active[session]) != 0
}

// withdraw takes back a delivery its channel never picked up.
func (h *channelHub) withdraw(session, requestID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	q := h.queue[session][:0]
	for _, d := range h.queue[session] {
		if d.RequestID != requestID {
			q = append(q, d)
		}
	}
	h.queue[session] = q
	h.forget(requestID)
}

// poll is the channel asking for its session's hops: it marks the channel
// alive and hands over (and marks in progress) whatever is queued.
func (h *channelHub) poll(session string) []ChannelDelivery {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seen[session] = time.Now()
	out := h.queue[session]
	delete(h.queue, session)
	for _, d := range out {
		if p, ok := h.picked[d.RequestID]; ok {
			close(p)
			delete(h.picked, d.RequestID)
		}
		if h.active[session] == nil {
			h.active[session] = map[string]bool{}
		}
		h.active[session][d.RequestID] = d.Strict
	}
	return out
}

// report records the agent's result for a request (the channel's report tool).
func (h *channelHub) report(requestID, text string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.result[requestID]
	if !ok {
		return false
	}
	session := h.owner[requestID]
	if _, active := h.active[session][requestID]; !active || strings.TrimSpace(text) == "" {
		return false
	}
	if _, already := h.reported[requestID]; already {
		return false
	}
	if h.channel[requestID] {
		h.proven[session] = true
	}
	h.reported[requestID] = text
	return true
}

// turnEnded releases the guard only after the turn ends. A report may be sent
// early in the turn and must not make subsequent tools unguarded. Channel
// requests require their own explicit report before any Stop can complete them.
func (h *channelHub) turnEnded(session string) {
	h.turnEndedForHook(session, "", false)
}

// turnEndedForHook requires the last human transcript prompt to match a typed
// request before that request can complete. Channel requests still require an
// explicit report, independently of Stop.
func (h *channelHub) turnEndedForHook(session, requestID string, verifyTyped bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id := range h.active[session] {
		if verifyTyped && !h.channel[id] && id != requestID {
			continue
		}
		out, reported := h.reported[id]
		if h.channel[id] && !reported {
			continue // an unrelated owner turn is not this request's report
		}
		if r, ok := h.result[id]; ok {
			r <- out
		}
		h.forget(id)
	}
}

// guarded reports whether a delivered hop is in progress in session, so the
// PreToolUse hook applies the hop's guardrails to what the agent does now, and
// whether any of them must run read-only.
func (h *channelHub) guarded(session string) (active, strict bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.active[session] {
		strict = strict || s
	}
	return len(h.active[session]) > 0, strict
}

func (h *channelHub) requestSession(requestID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.owner[requestID]
}

// forget drops every record of a request. Callers hold h.mu.
func (h *channelHub) forget(requestID string) {
	delete(h.channel, requestID)
	delete(h.aborted, requestID)
	delete(h.reported, requestID)
	if s, ok := h.owner[requestID]; ok {
		delete(h.active[s], requestID)
		if len(h.active[s]) == 0 {
			delete(h.active, s)
		}
	}
	delete(h.picked, requestID)
	delete(h.result, requestID)
	delete(h.owner, requestID)
}

// channelControl accepts a state-changing channel op only from a process inside
// the Claude session it names. The control token alone authenticates the user,
// not the session: another same-user process can read it too.
func (h *channelHub) channelControl(req daemonctl.Request, callerInSession func(int, string) bool) (daemonctl.Response, bool) {
	authorized := func(session string) bool {
		return session != "" && callerInSession != nil && callerInSession(req.PeerPID, session)
	}
	switch req.Op {
	case "channel-poll":
		session := req.Args["session"]
		if !authorized(session) {
			return daemonctl.Response{Err: "caller is not in the claimed Claude session"}, true
		}
		b, _ := json.Marshal(h.poll(session))
		return daemonctl.Response{OK: true, Data: b}, true
	case "channel-report":
		if !authorized(h.requestSession(req.Args["request_id"])) {
			return daemonctl.Response{Err: "caller does not own this request"}, true
		}
		return daemonctl.Response{OK: h.report(req.Args["request_id"], req.Args["result"])}, true
	case "channel-turn-ended":
		if !authorized(req.Args["session"]) {
			return daemonctl.Response{Err: "caller is not in the claimed Claude session"}, true
		}
		h.turnEndedForHook(req.Args["session"], req.Args["request_id"], true)
		return daemonctl.Response{OK: true}, true
	case "channel-abandon-typed":
		if !authorized(req.Args["session"]) {
			return daemonctl.Response{Err: "caller is not in the claimed Claude session"}, true
		}
		return daemonctl.Response{OK: h.abandonTyped(req.Args["session"], req.Args["request_id"])}, true
	case "channel-alive":
		return daemonctl.Response{OK: h.guardedAlive(req.Args["session"])}, true
	case "channel-ready":
		return daemonctl.Response{OK: h.channelReady(req.Args["session"])}, true
	case "channel-guard-ready":
		if !authorized(req.Args["session"]) {
			return daemonctl.Response{Err: "caller is not in the claimed Claude session"}, true
		}
		h.markGuardReady(req.Args["session"])
		return daemonctl.Response{OK: req.Args["session"] != ""}, true
	case "channel-guarded":
		active, strict := h.guarded(req.Args["session"])
		b, _ := json.Marshal(map[string]bool{"strict": strict})
		return daemonctl.Response{OK: active, Data: b}, true
	}
	return daemonctl.Response{}, false
}
