// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"encoding/json"
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
	mu     sync.Mutex
	seen   map[string]time.Time         // session -> last poll
	queue  map[string][]ChannelDelivery // session -> not yet picked up
	picked map[string]chan struct{}     // request -> closed when picked up
	result map[string]chan string       // request -> the result, once
	active map[string]map[string]bool   // session -> requests in progress
	owner  map[string]string            // request -> session
}

func newChannelHub() *channelHub {
	return &channelHub{
		seen: map[string]time.Time{}, queue: map[string][]ChannelDelivery{},
		picked: map[string]chan struct{}{}, result: map[string]chan string{},
		active: map[string]map[string]bool{}, owner: map[string]string{},
	}
}

// alive reports whether a channel for session polled recently.
func (h *channelHub) alive(session string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.seen[session]
	return ok && time.Since(t) < channelAlive
}

// deliver queues d for session's channel. picked closes when the channel takes
// it; result yields what the agent reported (or "" when its turn ended without
// a report).
func (h *channelHub) deliver(session string, d ChannelDelivery) (picked <-chan struct{}, result <-chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, r := make(chan struct{}), make(chan string, 1)
	h.queue[session] = append(h.queue[session], d)
	h.picked[d.RequestID], h.result[d.RequestID], h.owner[d.RequestID] = p, r, session
	return p, r
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
	r, ok := h.result[requestID]
	if !ok {
		return false
	}
	r <- text
	h.forget(requestID)
	return true
}

// turnEnded is the session's Stop hook: work the agent never reported is done
// now, with no result text.
func (h *channelHub) turnEnded(session string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id := range h.active[session] {
		if r, ok := h.result[id]; ok {
			r <- ""
		}
		h.forget(id)
	}
	delete(h.active, session)
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

// forget drops every record of a request. Callers hold h.mu.
func (h *channelHub) forget(requestID string) {
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

// channelControl answers the channel ops on the control endpoint. The endpoint
// is authenticated with the per-user token, so only this user's processes (the
// channel and hook s2u starts inside their own Claude sessions) reach it.
func (h *channelHub) channelControl(req daemonctl.Request) (daemonctl.Response, bool) {
	switch req.Op {
	case "channel-poll":
		session := req.Args["session"]
		if session == "" {
			return daemonctl.Response{Err: "session required"}, true
		}
		b, _ := json.Marshal(h.poll(session))
		return daemonctl.Response{OK: true, Data: b}, true
	case "channel-report":
		return daemonctl.Response{OK: h.report(req.Args["request_id"], req.Args["result"])}, true
	case "channel-turn-ended":
		h.turnEnded(req.Args["session"])
		return daemonctl.Response{OK: true}, true
	case "channel-alive":
		return daemonctl.Response{OK: h.alive(req.Args["session"])}, true
	case "channel-guarded":
		active, strict := h.guarded(req.Args["session"])
		b, _ := json.Marshal(map[string]bool{"strict": strict})
		return daemonctl.Response{OK: active, Data: b}, true
	}
	return daemonctl.Response{}, false
}
