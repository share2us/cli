// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli-core/daemonctl"
)

func TestChannelHubDeliverPollReport(t *testing.T) {
	h := newChannelHub()
	if h.alive("s1") {
		t.Fatal("no channel has polled yet")
	}
	picked, result := h.deliver("s1", ChannelDelivery{RequestID: "r1", Prompt: "p", Strict: true})
	if got := h.poll("s2"); len(got) != 0 {
		t.Fatalf("another session got the delivery: %+v", got)
	}
	got := h.poll("s1")
	if len(got) != 1 || got[0].RequestID != "r1" || !h.alive("s1") {
		t.Fatalf("poll = %+v alive=%v", got, h.alive("s1"))
	}
	select {
	case <-picked:
	default:
		t.Fatal("pickup was not signalled")
	}
	if active, strict := h.guarded("s1"); !active || !strict {
		t.Fatalf("guarded = %v %v, want an in-progress strict hop", active, strict)
	}
	if !h.report("r1", "done it") || <-result != "done it" {
		t.Fatal("report did not reach the waiting hop")
	}
	if active, _ := h.guarded("s1"); active || h.report("r1", "again") {
		t.Fatal("a reported hop is still in progress")
	}
}

func TestChannelHubTurnEndedWithoutReport(t *testing.T) {
	h := newChannelHub()
	_, result := h.deliver("s1", ChannelDelivery{RequestID: "r1"})
	h.poll("s1")
	h.turnEnded("s1")
	if r := <-result; r != "" {
		t.Fatalf("result = %q, want empty (no report)", r)
	}
	if active, _ := h.guarded("s1"); active {
		t.Fatal("still guarded after the turn ended")
	}
}

func TestChannelHubWithdraw(t *testing.T) {
	h := newChannelHub()
	h.deliver("s1", ChannelDelivery{RequestID: "r1"})
	h.withdraw("s1", "r1")
	if got := h.poll("s1"); len(got) != 0 {
		t.Fatalf("a withdrawn delivery was handed out: %+v", got)
	}
}

// The MCP side: the capability is declared, a delivery becomes a channel
// notification, and the report tool reaches the daemon.
func TestChannelServerSpeaksTheChannelContract(t *testing.T) {
	var mu sync.Mutex
	var calls []daemonctl.Request
	pending := []ChannelDelivery{{RequestID: "r1", Prompt: "do it", From: "dev-1"}}
	call := func(req daemonctl.Request) (daemonctl.Response, bool) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, req)
		if req.Op == "channel-poll" {
			b, _ := json.Marshal(pending)
			pending = nil
			return daemonctl.Response{OK: true, Data: b}, true
		}
		return daemonctl.Response{OK: true}, true
	}
	srv := &ChannelServer{FindSession: func(context.Context) (string, error) { return "sess-1", nil }, Call: call, Poll: 5 * time.Millisecond}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, inR, outW) }()
	lines := bufio.NewScanner(outR)
	next := func() map[string]any {
		if !lines.Scan() {
			t.Fatal("server closed")
		}
		var m map[string]any
		_ = json.Unmarshal(lines.Bytes(), &m)
		return m
	}
	send := func(s string) { _, _ = io.WriteString(inW, s+"\n") }

	send(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`)
	var init map[string]any
	for init == nil {
		if m := next(); m["id"] != nil {
			init = m
		}
	}
	caps := init["result"].(map[string]any)["capabilities"].(map[string]any)
	if _, ok := caps["experimental"].(map[string]any)["claude/channel"]; !ok {
		t.Fatalf("no claude/channel capability: %v", caps)
	}
	// The delivery arrives as a channel event with its request id.
	var ev map[string]any
	for ev == nil {
		if m := next(); m["method"] == "notifications/claude/channel" {
			ev = m
		}
	}
	params := ev["params"].(map[string]any)
	if params["content"] != "do it" || params["meta"].(map[string]any)["request_id"] != "r1" {
		t.Fatalf("event = %v", params)
	}
	send(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"report","arguments":{"request_id":"r1","result":"all done"}}}`)
	for {
		if m := next(); m["id"] == float64(7) {
			break
		}
	}
	mu.Lock()
	defer mu.Unlock()
	var reported bool
	for _, c := range calls {
		if c.Op == "channel-poll" && c.Args["session"] != "sess-1" {
			t.Fatalf("polled for the wrong session: %v", c.Args)
		}
		if c.Op == "channel-report" && c.Args["request_id"] == "r1" && c.Args["result"] == "all done" {
			reported = true
		}
	}
	if !reported {
		t.Fatalf("the report never reached the daemon: %+v", calls)
	}
}

func TestHookDecisionEnforcesTheRules(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	bash := func(cmd string) map[string]any { return map[string]any{"command": cmd} }
	for _, c := range []struct {
		tool  string
		input map[string]any
		deny  bool
	}{
		{"Bash", bash("git push origin main"), true},
		{"Bash", bash("go test ./... && git push"), true},
		{"Bash", bash("FOO=1 rm -rf build"), true},
		{"Bash", bash("go test ./..."), true}, // not a read: a headless hop could not either
		{"Bash", bash("git status && ls"), false},
		{"WebFetch", map[string]any{"url": "https://example.com"}, true},
		{"mcp__share2us__report", map[string]any{"request_id": "r", "result": "x"}, false},
		{"Grep", map[string]any{"pattern": "x"}, false},
		{"Edit", map[string]any{"file_path": "/etc/hosts"}, true},
		{"Edit", map[string]any{"file_path": filepath.Join(dir, ".s2u.rules")}, true},
		{"Write", map[string]any{"file_path": filepath.Join(dir, ".claude", "settings.json")}, true},
		{"Edit", map[string]any{"file_path": filepath.Join(dir, "main.go")}, false},
		{"Read", map[string]any{"file_path": filepath.Join(dir, ".s2u.rules")}, false},
	} {
		if deny, why := HookDecision(dir, c.tool, c.input, false); deny != c.deny {
			t.Errorf("%s %v: deny=%v (%s), want %v", c.tool, c.input, deny, why, c.deny)
		}
	}
	// Read-only: no edits, only the read commands.
	if deny, _ := HookDecision(dir, "Edit", map[string]any{"file_path": filepath.Join(dir, "main.go")}, true); !deny {
		t.Error("a strict hop edited a file")
	}
	if deny, _ := HookDecision(dir, "Bash", bash("go build ./..."), true); !deny {
		t.Error("a strict hop ran a command that is not a read")
	}
	if deny, why := HookDecision(dir, "Bash", bash("grep -n foo main.go | head"), true); deny {
		t.Errorf("a strict hop was refused a read: %s", why)
	}
}

// End to end in the daemon: a live session with a listening channel gets the
// hop delivered INTO it (no wait, no headless run), and the report is the result.
func TestLiveSessionWithAChannelGetsTheHopDelivered(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	if _, _, err := BindSession(dir, "claude", "", "win-1"); err != nil {
		t.Fatal(err)
	}
	r := &sessionFake{discovered: []DiscoveredSession{{SessionID: "win-1", Tool: "claude", Project: dir, Live: true}}, heldFor: 1 << 30}
	c := &fakeAgentClient{}
	runtime := rt()
	runtime.hub().poll("win-1") // the channel is listening
	runtime.handleInject(context.Background(), c, r, keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-5", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "go"}))
	var got []ChannelDelivery
	deadline := time.Now().Add(5 * time.Second)
	for len(got) == 0 && time.Now().Before(deadline) {
		got = runtime.hub().poll("win-1")
		time.Sleep(5 * time.Millisecond)
	}
	if len(got) != 1 || got[0].RequestID != "req-5" || !strings.Contains(got[0].Prompt, "go") {
		t.Fatalf("delivery = %+v", got)
	}
	runtime.hub().report("req-5", "finished")
	for runtime.holding.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if r.ran != 0 {
		t.Fatal("a delivered hop also ran headlessly")
	}
	if len(c.reports) != 2 || c.reports[0][0] != "running" || c.reports[1] != [2]string{"done", "finished"} {
		t.Fatalf("reports = %v", c.reports)
	}
	hops, _ := LoadHops(0)
	if len(hops) != 1 || hops[0].Mode != "delivered" || hops[0].RanIn != "win-1" {
		t.Fatalf("hop log = %+v", hops)
	}
}

func TestChannelLaunchConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mcp, settings, err := WriteChannelLaunchConfig("/opt/s2u/share2us")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	b, _ := os.ReadFile(mcp)
	if err := json.Unmarshal(b, &m); err != nil || m.MCPServers["share2us"].Command != "/opt/s2u/share2us" || strings.Join(m.MCPServers["share2us"].Args, " ") != "agent channel" {
		t.Fatalf("mcp config = %s (%v)", b, err)
	}
	s, _ := os.ReadFile(settings)
	if !strings.Contains(string(s), `agent hook pre-tool-use`) || !strings.Contains(string(s), `agent hook stop`) {
		t.Fatalf("settings = %s", s)
	}
	args := strings.Join(ClaudeChannelArgs(mcp, settings), " ")
	if !strings.Contains(args, "--dangerously-load-development-channels server:share2us") {
		t.Fatalf("args = %s", args)
	}
}
