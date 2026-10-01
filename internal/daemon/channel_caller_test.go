// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"testing"

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
		default:
			return 1, nil
		}
	}
	call := rt.control()
	request := func(pid int, op string, args map[string]string) daemonctl.Response {
		return call(daemonctl.Request{PeerPID: pid, Op: op, Args: args})
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
	if _, _, ok := h.beginTyped("s1", ChannelDelivery{RequestID: "typed"}); !ok {
		t.Fatal("begin typed")
	}
	if r := request(201, "channel-abandon-typed", map[string]string{"session": "s1", "request_id": "typed"}); r.OK || h.owner["typed"] != "s1" {
		t.Fatalf("other session abandoned typed hop: %+v", r)
	}
	if r := request(101, "channel-abandon-typed", map[string]string{"session": "s1", "request_id": "typed"}); !r.OK || h.owner["typed"] != "" {
		t.Fatalf("own typed abandon refused: %+v", r)
	}
}
