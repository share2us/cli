// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	clicore "github.com/share2us/cli-core"
)

// Moki's report (2026-09-27): ids printed by `agent list` did not work in
// `agent send`. These pin that everything list prints can be pasted into send.

var twoSessions = []clicore.AgentSessionInfo{
	{AgentID: "agt_AAAAAAAAAAAAAAAAAAAAAA", SessionID: "62575d1d-6822-4437-b11e-7d25e210cf8d", Tool: "claude", Name: "one",
		Status: "busy", DeviceID: "3cd1bca4-15d0-4a3e-9d7e-000000000001", DeviceName: "openclaw", DevicePublicKey: "pkA"},
	{AgentID: "agt_BBBBBBBBBBBBBBBBBBBBBB", SessionID: "62575d1d-ffff-4437-b11e-7d25e210cf8d", Tool: "codex", Name: "two",
		Status: "available", DeviceID: "3cd1bca4-15d0-4a3e-9d7e-000000000001", DeviceName: "openclaw", DevicePublicKey: "pkA"},
}

// flagValue reads `--name value` out of a printed line.
func flagValue(t *testing.T, line, name string) string {
	t.Helper()
	f := strings.Fields(line)
	for i := range f {
		if f[i] == name && i+1 < len(f) {
			return f[i+1]
		}
	}
	t.Fatalf("%s not in %q", name, line)
	return ""
}

func TestListOutputPastesIntoSend(t *testing.T) {
	targets := targetsFromSessions(twoSessions)
	for _, s := range twoSessions {
		line := sessionLine(s)
		if strings.Fields(line)[0] != s.AgentID {
			t.Fatalf("list row must start with the agent id: %q", line)
		}
		for name, q := range map[string]targetQuery{
			"agent id":         {Agent: strings.Fields(line)[0]},
			"device + session": {Device: flagValue(t, line, "--device"), Session: flagValue(t, line, "--session")},
			"session only":     {Session: flagValue(t, line, "--session")},
		} {
			got, err := pickTarget(targets, q)
			if err != nil || got.SessionID != s.SessionID || got.DeviceID != s.DeviceID || got.Tool != s.Tool {
				t.Fatalf("%s from %q: got %+v, %v", name, line, got, err)
			}
		}
	}
}

func TestPickTargetPrefixes(t *testing.T) {
	targets := targetsFromSessions(twoSessions)
	// A unique session prefix names the session, and the device is inferred.
	got, err := pickTarget(targets, targetQuery{Session: "62575d1d-68"})
	if err != nil || got.AgentID != "agt_AAAAAAAAAAAAAAAAAAAAAA" || got.DeviceID == "" {
		t.Fatalf("unique prefix: %+v, %v", got, err)
	}
	if got, err := pickTarget(targets, targetQuery{Agent: "agt_BBBB"}); err != nil || got.Tool != "codex" {
		t.Fatalf("agent prefix: %+v, %v", got, err)
	}
	// An ambiguous prefix fails and lists every candidate in full.
	_, err = pickTarget(targets, targetQuery{Session: "62575d1d"})
	if err == nil || !strings.Contains(err.Error(), "2 reachable sessions match") ||
		!strings.Contains(err.Error(), "agt_AAAAAAAAAAAAAAAAAAAAAA") || !strings.Contains(err.Error(), "agt_BBBBBBBBBBBBBBBBBBBBBB") {
		t.Fatalf("ambiguous prefix: %v", err)
	}
	// Too short to be a prefix: must be exact.
	if _, err := pickTarget(targets, targetQuery{Session: "6257"}); err == nil {
		t.Fatal("a 4-character prefix matched")
	}
	// A session that is gone points at the stable address.
	_, err = pickTarget(targets, targetQuery{Session: "c5f7f2d3-a444-4ee0-883f-d4ce231627df"})
	if err == nil || !strings.Contains(err.Error(), "--agent AGENT-ID") {
		t.Fatalf("not found should suggest --agent: %v", err)
	}
	// A device prefix that matches no session's device narrows to nothing.
	if _, err := pickTarget(targets, targetQuery{Agent: "agt_AAAAAAAAAAAAAAAAAAAAAA", Device: "ffffffff-0000"}); err == nil {
		t.Fatal("a wrong device still matched")
	}
}

// Through the real command: an ambiguous or unknown target stops before
// anything is sealed or sent, and says what to do.
func TestAgentSendRefusesUnclearTargets(t *testing.T) {
	withCredential(t, "https://api.example.test")
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agent/sessions":
			writeTestJSON(w, map[string]any{"sessions": twoSessions})
		default:
			t.Fatalf("send must stop before %s %s", r.Method, r.URL.Path)
		}
	}))
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--session", "62575d1d", "--prompt", "hi"}, "2 reachable sessions match"},
		{[]string{"--session", "deadbeef-0000", "--prompt", "hi"}, "--agent AGENT-ID"},
		{[]string{"--agent", "agt_ZZZZZZZZ", "--prompt", "hi"}, "no reachable agent matches"},
	} {
		var out, errb bytes.Buffer
		code := app{stdout: &out, stderr: &errb}.run(context.Background(), append([]string{"agent", "send"}, tc.args...))
		if code == 0 || !strings.Contains(errb.String(), tc.want) || !strings.Contains(errb.String(), "agent list") {
			t.Fatalf("%v: code %d stderr %q", tc.args, code, errb.String())
		}
	}
	var out, errb bytes.Buffer
	if code := (app{stdout: &out, stderr: &errb}).run(context.Background(), []string{"agent", "send", "--prompt", "hi"}); code != 2 {
		t.Fatalf("no target should be a usage error, got %d: %s", code, errb.String())
	}
}
