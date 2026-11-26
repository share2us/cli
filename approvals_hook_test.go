// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/share2us/cli-core/daemonctl"
)

type fakeApprovalDaemon struct {
	decision string
	statuses []string
	calls    []string
}

func (f *fakeApprovalDaemon) call(req daemonctl.Request) (daemonctl.Response, bool) {
	f.calls = append(f.calls, req.Op)
	var v any
	switch req.Op {
	case "approval-check":
		v = map[string]string{"decision": f.decision, "id": "appr-1", "display": "ip -4 addr"}
	case "approval-status":
		st := "pending"
		if len(f.statuses) > 0 {
			st, f.statuses = f.statuses[0], f.statuses[1:]
		}
		v = map[string]string{"status": st}
	default:
		v = map[string]string{}
	}
	b, _ := json.Marshal(v)
	return daemonctl.Response{OK: true, Data: b}, true
}

func TestAskOwner(t *testing.T) {
	in := hookInput{SessionID: "s", ToolName: "Bash", ToolInput: map[string]any{"command": "ip -4 addr"}}
	noSleep := func(time.Duration) {}
	old := approvalHold
	approvalHold = 50 * time.Millisecond
	defer func() { approvalHold = old }()

	if ok, _ := askOwner(in, "guard", (&fakeApprovalDaemon{decision: "allow"}).call, noSleep); !ok {
		t.Fatal("a grant didn't allow")
	}
	if ok, _ := askOwner(in, "guard", (&fakeApprovalDaemon{decision: "pending", statuses: []string{"pending", "approved"}}).call, noSleep); !ok {
		t.Fatal("an approval during the hold didn't allow")
	}
	ok, why := askOwner(in, "guard", (&fakeApprovalDaemon{decision: "pending", statuses: []string{"denied"}}).call, noSleep)
	if ok || !strings.Contains(why, "declined") {
		t.Fatalf("deny: %v %q", ok, why)
	}
	f := &fakeApprovalDaemon{decision: "pending"}
	ok, why = askOwner(in, "guard", f.call, func(time.Duration) { time.Sleep(10 * time.Millisecond) })
	if ok || !strings.Contains(why, "haven't answered yet") || f.calls[len(f.calls)-1] != "approval-park" {
		t.Fatalf("timeout: %v %q %v", ok, why, f.calls)
	}
	if ok, why := askOwner(in, "guard", (&fakeApprovalDaemon{decision: "deny"}).call, noSleep); ok || why != "guard" {
		t.Fatalf("no binding: %v %q", ok, why)
	}
}
