// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/share2us/cli-core/daemonctl"
)

func TestChannelControlBindsPeerToSessionAndRequest(t *testing.T) {
	rt := &Runtime{}
	h := rt.hub()
	rt.channelCaller.discover = func(context.Context) (map[int]DiscoveredSession, error) {
		return map[int]DiscoveredSession{
			100: {PID: 100, SessionID: "s1", Tool: "claude"},
			200: {PID: 200, SessionID: "s2", Tool: "claude"},
		}, nil
	}
	rt.channelCaller.parent = func(pid int) (int, error) {
		switch pid {
		case 101:
			return 100, nil
		case 201:
			return 200, nil
		case 301:
			return 200, nil // a client in Claude s2, launched inside s1
		case 200:
			return 100, nil
		default:
			return 1, nil
		}
	}
	call := rt.control()
	request := func(pid int, op string, args map[string]string) daemonctl.Response {
		return call(daemonctl.Request{PeerPID: pid, Op: op, Args: args})
	}
	if rt.channelCaller.inSession(301, "s1") {
		t.Fatal("nested Claude session claimed its outer session")
	}
	if !rt.channelCaller.inSession(301, "s2") || rt.channelCaller.inSession(301, "s1") {
		t.Fatal("nested caller was not bound to its nearest session")
	}
	if _, _, ok := h.deliver("s1", ChannelDelivery{RequestID: "r1"}); !ok {
		t.Fatal("queue delivery")
	}
	for _, pid := range []int{0, 201} {
		if r := request(pid, "channel-poll", map[string]string{"session": "s1"}); r.OK || len(h.queue["s1"]) != 1 {
			t.Fatalf("peer %d stole another session's delivery: %+v", pid, r)
		}
		if r := request(pid, "channel-guard-ready", map[string]string{"session": "s1"}); r.OK || h.guard["s1"] {
			t.Fatalf("peer %d forged the hook proof: %+v", pid, r)
		}
	}
	if r := request(101, "channel-guard-ready", map[string]string{"session": "s1"}); !r.OK || !h.guard["s1"] {
		t.Fatalf("own hook proof refused: %+v", r)
	}
	if r := request(101, "channel-poll", map[string]string{"session": "s1"}); !r.OK || len(h.queue["s1"]) != 0 {
		t.Fatalf("own poll refused: %+v", r)
	}
	if r := request(201, "channel-report", map[string]string{"request_id": "r1", "result": "forged"}); r.OK || h.reported["r1"] != "" {
		t.Fatalf("other session reported request: %+v", r)
	}
	if r := request(101, "channel-report", map[string]string{"request_id": "r1", "result": "real"}); !r.OK {
		t.Fatalf("own report refused: %+v", r)
	}
	if r := request(201, "channel-turn-ended", map[string]string{"session": "s1", "request_id": "r1"}); r.OK || h.owner["r1"] != "s1" {
		t.Fatalf("other session completed request: %+v", r)
	}
	if r := request(101, "channel-turn-ended", map[string]string{"session": "s1", "request_id": "r1"}); !r.OK || h.owner["r1"] != "" {
		t.Fatalf("own turn end refused: %+v", r)
	}
	if _, _, ok := h.beginTypedForProcess("s1", ChannelDelivery{RequestID: "typed"}, 100); !ok {
		t.Fatal("begin typed")
	}
	if r := request(201, "channel-abandon-typed", map[string]string{"session": "s1", "request_id": "typed"}); r.OK || h.owner["typed"] != "s1" {
		t.Fatalf("other session abandoned typed hop: %+v", r)
	}
	if r := request(101, "channel-abandon-typed", map[string]string{"session": "s1", "request_id": "typed"}); !r.OK || h.owner["typed"] != "" {
		t.Fatalf("own typed abandon refused: %+v", r)
	}
}

func TestChannelCallerDiscoveryDoesNotBlockOtherPeers(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	v := &channelCallerVerifier{
		known: map[int]verifiedCaller{201: {session: "s2", claudePID: 200, parent: 200, expires: time.Now().Add(time.Minute)}},
		parent: func(pid int) (int, error) {
			if pid == 201 {
				return 200, nil
			}
			return 100, nil
		},
		discover: func(context.Context) (map[int]DiscoveredSession, error) {
			close(started)
			<-release
			return nil, errors.New("Claude discovery failed")
		},
	}
	slowDone := make(chan bool, 1)
	go func() { slowDone <- v.inSession(101, "s1") }()
	<-started
	fastDone := make(chan bool, 1)
	go func() { fastDone <- v.inSession(201, "s2") }()
	select {
	case ok := <-fastDone:
		if !ok {
			t.Fatal("cached peer was denied")
		}
	case <-time.After(time.Second):
		t.Fatal("one slow discovery blocked a different session")
	}
	close(release)
	if <-slowDone {
		t.Fatal("failed discovery authorized caller")
	}
}

func TestChannelCallerCachesFailedDiscoveryBriefly(t *testing.T) {
	calls := 0
	v := &channelCallerVerifier{
		parent: func(int) (int, error) { return 100, nil },
		discover: func(context.Context) (map[int]DiscoveredSession, error) {
			calls++
			return nil, errors.New("Claude unavailable")
		},
	}
	if v.inSession(101, "s1") || v.inSession(101, "s1") || calls != 1 {
		t.Fatalf("failed discovery retried without a pause: calls=%d", calls)
	}
}

func TestChannelProofCannotCarryToResumedClaudeProcess(t *testing.T) {
	rt := &Runtime{}
	h := rt.hub()
	rt.channelCaller.discover = func(context.Context) (map[int]DiscoveredSession, error) {
		return map[int]DiscoveredSession{
			100: {PID: 100, SessionID: "same-id", Tool: "claude"},
			300: {PID: 300, SessionID: "same-id", Tool: "claude"},
		}, nil
	}
	rt.channelCaller.parent = func(pid int) (int, error) {
		switch pid {
		case 101:
			return 100, nil
		case 301:
			return 300, nil
		default:
			return 1, nil
		}
	}
	call := rt.control()
	request := func(pid int, op string) daemonctl.Response {
		return call(daemonctl.Request{PeerPID: pid, Op: op, Args: map[string]string{"session": "same-id"}})
	}
	if !request(101, "channel-guard-ready").OK || !request(101, "channel-poll").OK {
		t.Fatal("first process did not register and poll")
	}
	// A real channel report in the first process proves delivery once.
	if _, _, ok := h.deliver("same-id", ChannelDelivery{RequestID: "proof"}); !ok {
		t.Fatal("queue proof")
	}
	h.poll("same-id", 100)
	if !h.report("proof", "confirmed") {
		t.Fatal("report proof")
	}
	h.turnEnded("same-id")
	if !h.channelReady("same-id") {
		t.Fatal("first process was not ready")
	}
	oldPicked, _, ok := h.deliver("same-id", ChannelDelivery{RequestID: "old-queued"})
	if !ok {
		t.Fatal("queue for first process")
	}
	if request(301, "channel-guard-registered").OK {
		t.Fatal("resumed process inherited the first process's hook proof")
	}
	resumedPoll := request(301, "channel-poll")
	if !resumedPoll.OK || h.channelReady("same-id") {
		t.Fatal("resumed process inherited the first process's channel proof")
	}
	select {
	case <-oldPicked:
		t.Fatal("resumed process picked up a delivery queued for the old guarded process")
	default:
	}
	if len(h.queue["same-id"]) != 1 {
		t.Fatal("old delivery was removed from the queue")
	}
	h.withdraw("same-id", "old-queued")
	if !request(301, "channel-guard-ready").OK || h.proven["same-id"] || !request(301, "channel-guard-registered").OK {
		t.Fatal("resumed process did not replace stale hook/channel proof")
	}
}

func TestSecondClaudeProcessCannotClearFirstProcessHop(t *testing.T) {
	rt := &Runtime{}
	h := rt.hub()
	rt.channelCaller.discover = func(context.Context) (map[int]DiscoveredSession, error) {
		return map[int]DiscoveredSession{
			100: {PID: 100, SessionID: "same-id", Tool: "claude"},
			300: {PID: 300, SessionID: "same-id", Tool: "claude"},
		}, nil
	}
	rt.channelCaller.parent = func(pid int) (int, error) {
		if pid == 101 {
			return 100, nil
		}
		if pid == 301 {
			return 300, nil
		}
		return 1, nil
	}
	request := func(pid int, op string, args map[string]string) daemonctl.Response {
		return rt.control()(daemonctl.Request{PeerPID: pid, Op: op, Args: args})
	}
	if !request(101, "channel-guard-ready", map[string]string{"session": "same-id"}).OK {
		t.Fatal("first process guard registration")
	}
	if _, _, ok := h.deliver("same-id", ChannelDelivery{RequestID: "channel-hop"}); !ok {
		t.Fatal("queue channel hop")
	}
	if !request(101, "channel-poll", map[string]string{"session": "same-id"}).OK {
		t.Fatal("first process poll")
	}
	if request(301, "channel-report", map[string]string{"request_id": "channel-hop", "result": "forged"}).OK {
		t.Fatal("second process reported first process hop")
	}
	if !request(101, "channel-report", map[string]string{"request_id": "channel-hop", "result": "real"}).OK {
		t.Fatal("first process report")
	}
	request(301, "channel-turn-ended", map[string]string{"session": "same-id"})
	if h.owner["channel-hop"] != "same-id" {
		t.Fatal("second process ended first process channel hop")
	}
	request(101, "channel-turn-ended", map[string]string{"session": "same-id"})
	if h.owner["channel-hop"] != "" {
		t.Fatal("first process could not end its channel hop")
	}
	if _, _, ok := h.beginTypedForProcess("same-id", ChannelDelivery{RequestID: "typed-hop"}, 100); !ok {
		t.Fatal("begin typed hop")
	}
	if request(301, "channel-abandon-typed", map[string]string{"session": "same-id", "request_id": "typed-hop"}).OK || h.owner["typed-hop"] != "same-id" {
		t.Fatal("second process abandoned first process typed hop")
	}
	if request(301, "channel-turn-ended", map[string]string{"session": "same-id", "request_id": "typed-hop"}).OK {
		t.Fatal("second process received an ACK that would clear the typed marker")
	}
	if h.owner["typed-hop"] != "same-id" {
		t.Fatal("second process ended first process typed hop")
	}
	if !request(101, "channel-abandon-typed", map[string]string{"session": "same-id", "request_id": "typed-hop"}).OK {
		t.Fatal("first process could not abandon its typed hop")
	}
}
