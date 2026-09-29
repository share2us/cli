// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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
	picked, result, ok := h.deliver("s1", ChannelDelivery{RequestID: "r1", Prompt: "p", Strict: true})
	if !ok {
		t.Fatal("first delivery was refused")
	}
	if _, _, ok := h.deliver("s1", ChannelDelivery{RequestID: "r2"}); ok {
		t.Fatal("a second delivery entered the same session")
	}
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
	_, result, _ := h.deliver("s1", ChannelDelivery{RequestID: "r1"})
	h.poll("s1")
	h.turnEnded("s1")
	select {
	case r := <-result:
		t.Fatalf("Stop completed a channel request: %q", r)
	default:
	}
	if active, _ := h.guarded("s1"); !active {
		t.Fatal("channel guard was removed by an unrelated Stop")
	}
	h.withdraw("s1", "r1")
}

func TestChannelHubWithdraw(t *testing.T) {
	h := newChannelHub()
	h.deliver("s1", ChannelDelivery{RequestID: "r1"})
	h.withdraw("s1", "r1")
	if got := h.poll("s1"); len(got) != 0 {
		t.Fatalf("a withdrawn delivery was handed out: %+v", got)
	}
}

func TestChannelHubTypedHopIsGuardedAndExclusive(t *testing.T) {
	h := newChannelHub()
	result, ok := h.beginTyped("s1", ChannelDelivery{RequestID: "typed-1", Strict: true})
	if !ok {
		t.Fatal("first typed hop was refused")
	}
	if active, strict := h.guarded("s1"); !active || !strict {
		t.Fatalf("typed guard = %v/%v", active, strict)
	}
	if _, ok := h.beginTyped("s1", ChannelDelivery{RequestID: "typed-2"}); ok {
		t.Fatal("a second typed hop entered the same session")
	}
	if _, _, ok := h.deliver("s1", ChannelDelivery{RequestID: "channel-2"}); ok {
		t.Fatal("a channel hop entered beside a typed hop")
	}
	h.turnEnded("s1")
	if got := <-result; got != "" {
		t.Fatalf("stop result = %q", got)
	}
}

func TestGuardedAliveNeedsChannelAndSessionStartHook(t *testing.T) {
	h := newChannelHub()
	h.poll("s1")
	if h.guardedAlive("s1") {
		t.Fatal("a channel without the guard settings was trusted")
	}
	h.markGuardReady("s1")
	if !h.guardedAlive("s1") {
		t.Fatal("the polling channel with its SessionStart hook was not recognised")
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
	r := &sessionFake{discovered: []DiscoveredSession{{SessionID: "win-1", Tool: "claude", Project: dir, Status: "available", Live: true}}, heldFor: 1 << 30}
	c := &fakeAgentClient{reportNotify: make(chan struct{}, 1)}
	runtime := rt()
	runtime.hub().poll("win-1") // the channel is listening
	runtime.hub().markGuardReady("win-1")
	proveReportTool(t, runtime.hub(), "win-1")
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
	if len(c.reports) != 3 || c.reports[0][0] != "waiting" || c.reports[1][0] != "running" || c.reports[2] != [2]string{"done", "finished"} {
		t.Fatalf("reports = %v", c.reports)
	}
	hops, _ := LoadHops(0)
	if len(hops) != 1 || hops[0].Mode != "delivered" || hops[0].RanIn != "win-1" {
		t.Fatalf("hop log = %+v", hops)
	}
}

func TestChannelPickupWithoutClaudeStartingFallsBackToWaiting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	pickup, holdPoll := channelPickup, injectHoldPoll
	channelPickup, injectHoldPoll = 30*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { channelPickup, injectHoldPoll = pickup, holdPoll })
	dir := t.TempDir()
	if _, _, err := BindSession(dir, "claude", "", "win-1"); err != nil {
		t.Fatal(err)
	}
	r := &sessionFake{discovered: []DiscoveredSession{{SessionID: "win-1", Tool: "claude", Project: dir, Status: "available", Live: true}}, heldFor: 1 << 30}
	c := &fakeAgentClient{reportNotify: make(chan struct{}, 1)}
	runtime := rt()
	runtime.hub().poll("win-1")
	runtime.hub().markGuardReady("win-1")
	proveReportTool(t, runtime.hub(), "win-1")
	ctx, cancel := context.WithCancel(context.Background())
	runtime.handleInject(ctx, c, r, keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-dropped", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "go"}))
	deadline := time.Now().Add(time.Second)
	deliveries := 0
	for time.Now().Before(deadline) {
		if got := runtime.hub().poll("win-1"); len(got) != 0 {
			deliveries += len(got) // MCP picks it up, but Claude never becomes busy
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-c.reportNotify:
	case <-time.After(time.Second):
		t.Fatal("channel fallback did not report waiting")
	}
	// Keep the guarded channel visibly alive for several hold polls. This hop
	// must not be queued into it a second time.
	until := time.Now().Add(6 * injectHoldPoll)
	for time.Now().Before(until) {
		deliveries += len(runtime.hub().poll("win-1"))
		time.Sleep(time.Millisecond)
	}
	cancel()
	for runtime.holding.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if deliveries != 1 || len(c.reports) != 1 || c.reports[0][0] != "waiting" || r.ran != 0 {
		t.Fatalf("deliveries=%d reports=%v ran=%d; a silently dropped channel event must wait once", deliveries, c.reports, r.ran)
	}
}

func TestBusySessionCannotConfirmDroppedChannelDelivery(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	holdPoll := injectHoldPoll
	injectHoldPoll = time.Hour
	t.Cleanup(func() { injectHoldPoll = holdPoll })
	dir := t.TempDir()
	if _, _, err := BindSession(dir, "claude", "", "win-1"); err != nil {
		t.Fatal(err)
	}
	r := &sessionFake{discovered: []DiscoveredSession{{SessionID: "win-1", Tool: "claude", Project: dir, Status: "busy", Live: true}}, heldFor: 1 << 30}
	c := &fakeAgentClient{reportNotify: make(chan struct{}, 1)}
	runtime := rt()
	runtime.hub().poll("win-1")
	runtime.hub().markGuardReady("win-1")
	proveReportTool(t, runtime.hub(), "win-1")
	ctx, cancel := context.WithCancel(context.Background())
	runtime.handleInject(ctx, c, r, keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-owner-busy", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "go"}))
	select {
	case <-c.reportNotify:
	case <-time.After(time.Second):
		t.Fatal("busy session did not report the hop waiting")
	}
	if got := runtime.hub().poll("win-1"); len(got) != 0 {
		t.Fatalf("queued into an already-busy session: %+v", got)
	}
	runtime.hub().turnEnded("win-1") // the owner's turn, not this hop
	time.Sleep(10 * time.Millisecond)
	cancel()
	deadline := time.Now().Add(time.Second)
	for runtime.holding.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(c.reports) != 1 || c.reports[0][0] != "waiting" {
		t.Fatalf("reports=%v; the owner's Stop falsely completed the hop", c.reports)
	}
}

func TestStopBeforeBusyTransitionCannotCompleteChannelHop(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	pickup := channelPickup
	channelPickup = 30 * time.Millisecond
	t.Cleanup(func() { channelPickup = pickup })
	dir := t.TempDir()
	if _, _, err := BindSession(dir, "claude", "", "win-1"); err != nil {
		t.Fatal(err)
	}
	r := &sessionFake{discovered: []DiscoveredSession{{SessionID: "win-1", Tool: "claude", Project: dir, Status: "available", Live: true}}, heldFor: 1 << 30}
	c := &fakeAgentClient{reportNotify: make(chan struct{}, 2)}
	runtime := rt()
	runtime.hub().poll("win-1")
	runtime.hub().markGuardReady("win-1")
	proveReportTool(t, runtime.hub(), "win-1")
	ctx, cancel := context.WithCancel(context.Background())
	runtime.handleInject(ctx, c, r, keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-early-stop", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "go"}))
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := runtime.hub().poll("win-1"); len(got) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	runtime.hub().turnEnded("win-1") // no available-to-busy transition occurred
	select {
	case <-c.reportNotify:
	case <-time.After(time.Second):
		t.Fatal("early Stop did not move the hop to waiting")
	}
	cancel()
	for runtime.holding.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(c.reports) != 1 || c.reports[0][0] != "waiting" {
		t.Fatalf("reports=%v; Stop before busy transition falsely completed the hop", c.reports)
	}
}

// Even after readiness was established, a dropped event plus the owner's own
// turn must never complete the request. Before readiness it must not be queued.
func TestOwnerTurnCannotCompleteUnreportedChannelHop(t *testing.T) {
	for _, proven := range []bool{false, true} {
		t.Run(fmt.Sprint(proven), func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			pickup, poll := channelPickup, injectHoldPoll
			channelPickup, injectHoldPoll = 30*time.Millisecond, 5*time.Millisecond
			t.Cleanup(func() { channelPickup, injectHoldPoll = pickup, poll })
			dir := t.TempDir()
			if _, _, err := BindSession(dir, "claude", "", "win-1"); err != nil {
				t.Fatal(err)
			}
			r := &sessionFake{discovered: []DiscoveredSession{{SessionID: "win-1", Tool: "claude", Project: dir, Live: true}}, statuses: []string{"available", "available", "busy"}, heldFor: 1 << 30}
			c := &fakeAgentClient{}
			runtime := rt()
			runtime.hub().poll("win-1")
			runtime.hub().markGuardReady("win-1")
			if proven {
				proveReportTool(t, runtime.hub(), "win-1")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runtime.handleInject(ctx, c, r, keyedDeps(), signedReq(t, clicore.AgentRequest{ID: "dropped", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "go"}))
			deliveries := 0
			until := time.Now().Add(100 * time.Millisecond)
			for time.Now().Before(until) {
				deliveries += len(runtime.hub().poll("win-1"))
				runtime.hub().turnEnded("win-1")
				time.Sleep(time.Millisecond)
			}
			cancel()
			deadline := time.Now().Add(time.Second)
			for runtime.holding.Load() != 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if runtime.holding.Load() != 0 {
				t.Fatal("worker did not stop")
			}
			if (!proven && deliveries != 0) || (proven && deliveries != 1) {
				t.Fatalf("proven=%v deliveries=%d", proven, deliveries)
			}
			if len(c.reports) != 1 || c.reports[0][0] != "waiting" || r.ran != 0 {
				t.Fatalf("reports=%v ran=%d", c.reports, r.ran)
			}
		})
	}
}

func proveReportTool(t *testing.T, h *channelHub, session string) {
	t.Helper()
	if _, ok := h.beginTyped(session, ChannelDelivery{RequestID: "proof"}); !ok {
		t.Fatal("proof setup")
	}
	if !h.report("proof", "confirmed") {
		t.Fatal("proof report")
	}
}

func TestUnreportedChannelExpiresWithoutCompletion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	if _, _, err := BindSession(dir, "claude", "", "win-1"); err != nil {
		t.Fatal(err)
	}
	r := &sessionFake{discovered: []DiscoveredSession{{SessionID: "win-1", Tool: "claude", Project: dir, Status: "available", Live: true}}, heldFor: 1 << 30}
	c := &fakeAgentClient{}
	runtime := rt()
	runtime.hub().poll("win-1")
	runtime.hub().markGuardReady("win-1")
	proveReportTool(t, runtime.hub(), "win-1")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runtime.handleInject(ctx, c, r, keyedDeps(), signedReq(t, clicore.AgentRequest{ID: "expired", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "go", CreatedAt: time.Now().Add(-injectHoldMax - time.Second).UTC().Format(time.RFC3339)}))
	runtime.hub().poll("win-1")
	deadline := time.Now().Add(time.Second)
	for runtime.holding.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runtime.holding.Load() != 0 {
		t.Fatal("expiry did not finish")
	}
	if len(c.reports) != 2 || c.reports[0][0] != "waiting" || c.reports[1][0] != "failed" {
		t.Fatalf("reports=%v", c.reports)
	}
	if active, _ := runtime.hub().guarded("win-1"); active {
		t.Fatal("expired request still guarded")
	}
}

func TestChannelReadinessRequiresReportAndResetsOnStart(t *testing.T) {
	h := newChannelHub()
	h.poll("s1")
	h.markGuardReady("s1")
	if h.channelReady("s1") {
		t.Fatal("poll and guard incorrectly prove channel readiness")
	}
	if h.report("unknown", "forged") || h.channelReady("s1") {
		t.Fatal("unknown report proved readiness")
	}
	_, _ = h.beginTyped("s1", ChannelDelivery{RequestID: "typed"})
	if h.report("typed", "  ") {
		t.Fatal("blank report proved readiness")
	}
	h.turnEnded("s1")
	if h.channelReady("s1") {
		t.Fatal("Stop proved readiness")
	}
	proveReportTool(t, h, "s1")
	if !h.channelReady("s1") || h.channelReady("s2") {
		t.Fatal("readiness is not session-specific")
	}
	h.markGuardReady("s1")
	if h.channelReady("s1") {
		t.Fatal("restart retained readiness")
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
	if !strings.Contains(string(s), `agent hook session-start`) || !strings.Contains(string(s), `agent hook pre-tool-use`) || !strings.Contains(string(s), `agent hook stop`) {
		t.Fatalf("settings = %s", s)
	}
	args := strings.Join(ClaudeChannelArgs(mcp, settings), " ")
	if !strings.Contains(args, "--dangerously-load-development-channels server:share2us") {
		t.Fatalf("args = %s", args)
	}
}
