// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	clicore "github.com/share2us/cli-core"
)

func typedRuntime(t *testing.T, screens ...string) (*Runtime, *fakeZellij, string) {
	t.Helper()
	dir := t.TempDir()
	pane := &ZellijPane{Session: "stale-name", Pane: "4"}
	if _, _, err := BindSessionInPane(dir, "claude", "", "win-1", pane); err != nil {
		t.Fatal(err)
	}
	z := &fakeZellij{sessions: []string{"renamed"}, panes: map[string][]zellijPaneInfo{
		"renamed": {{ID: 4, CWD: dir, Command: "claude --resume win-1", TabName: "agent"}},
	}, screens: screens}
	runtime := rt()
	runtime.zellij = z
	runtime.processPane = func(int) *ZellijPane { return &ZellijPane{Session: "stale-name", Pane: "4"} }
	runtime.hub().poll("win-1") // proves s2u claude's hook/channel process is alive
	runtime.hub().markGuardReady("win-1")
	return runtime, z, dir
}

func typedRunner(dir string) *sessionFake {
	return &sessionFake{discovered: []DiscoveredSession{{SessionID: "win-1", Tool: "claude", Project: dir, PID: 123, Status: "available", Live: true}}, heldFor: 1 << 30}
}

func TestLiveS2UClaudeHopIsTypedAndReported(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// The first post-paste snapshot can still be Claude's old empty input;
	// delivery must wait for its asynchronous folded-paste marker before Enter.
	runtime, z, dir := typedRuntime(t, "│ ❯  │", "│ ❯  │", "│ ❯ [Pasted text #1 +3 lines] │")
	c, r := &fakeAgentClient{}, typedRunner(dir)
	key, _ := clicore.NewContentKey()
	var encrypted bytes.Buffer
	if err := clicore.EncryptStream(&encrypted, strings.NewReader("file contents"), key); err != nil {
		t.Fatal(err)
	}
	deps := keyedDeps()
	deps.DownloadContent = func(context.Context, string) ([]byte, error) { return encrypted.Bytes(), nil }
	deps.OpenContentKey = func(string) ([]byte, error) { return key, nil }
	runtime.handleInject(context.Background(), c, r, deps,
		signedReq(t, clicore.AgentRequest{ID: "req-typed", Tool: "claude", TargetSessionID: "win-1",
			SealedPrompt: `{"prompt":"inspect the file","file_name":"note.txt","sender_device_name":"jarvis"}`, SealedFileKey: "sealed-key", HasFile: true}))
	if len(z.pasted) != 1 || !strings.Contains(z.pasted[0], `[Share2Us] from device "jarvis" (`+bridgeSender.id+`), request req-typed:`) || !strings.Contains(z.pasted[0], "inspect the file") {
		t.Fatalf("pasted = %q", z.pasted)
	}
	if z.entered != 1 || r.ran != 0 {
		t.Fatalf("Enter=%d headless runs=%d", z.entered, r.ran)
	}
	filePath := filepath.Join(dir, ".s2u-inbox", "note.txt")
	if got, err := os.ReadFile(filePath); err != nil || string(got) != "file contents" || !strings.Contains(z.pasted[0], filePath) {
		t.Fatalf("inbox file=%q err=%v prompt=%q", got, err, z.pasted[0])
	}
	if active, _ := runtime.hub().guarded("win-1"); !active {
		t.Fatal("hook guard was not active after submission")
	}
	runtime.hub().report("req-typed", "finished safely")
	deadline := time.Now().Add(5 * time.Second)
	for runtime.holding.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(c.reports) != 2 || c.reports[0][0] != "running" || c.reports[1] != [2]string{"done", "finished safely"} {
		t.Fatalf("reports = %v", c.reports)
	}
	hops, _ := LoadHops(0)
	if len(hops) != 1 || hops[0].Mode != "typed" || hops[0].RanIn != "win-1" {
		t.Fatalf("hops = %+v", hops)
	}
}

func TestPlainClaudeIsNeverTypedInto(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	runtime, z, dir := typedRuntime(t, "│ ❯  │", "│ ❯ [Pasted text +3 lines] │")
	// Expire the proof that s2u claude is running; the bound pane alone grants
	// no permission to type into a plain Claude process.
	runtime.hub().seen["win-1"] = time.Now().Add(-channelAlive - time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	runtime.handleInject(ctx, &fakeAgentClient{}, typedRunner(dir), keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-plain", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "do it"}))
	cancel()
	if len(z.pasted) != 0 || z.entered != 0 {
		t.Fatalf("plain Claude received paste=%q enter=%d", z.pasted, z.entered)
	}
}

func TestOwnerInputRaceNeverPressesEnterAndKeepsGuard(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	runtime, z, dir := typedRuntime(t, "│ ❯  │", "│ ❯ owner text [Pasted text +3 lines] │")
	ctx, cancel := context.WithCancel(context.Background())
	c := &fakeAgentClient{}
	runtime.handleInject(ctx, c, typedRunner(dir), keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-race", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "remote text"}))
	if len(z.pasted) != 1 || z.entered != 0 {
		t.Fatalf("paste=%q enter=%d", z.pasted, z.entered)
	}
	if active, _ := runtime.hub().guarded("win-1"); !active {
		t.Fatal("guard was dropped while remote text remained in the input")
	}
	if len(c.reports) != 1 || c.reports[0][0] != "waiting" {
		t.Fatalf("reports = %v", c.reports)
	}
	cancel()
}

func TestUnsafeOrWrongPaneTypesNothing(t *testing.T) {
	for name, change := range map[string]func(*Runtime, *fakeZellij){
		"owner input": func(_ *Runtime, z *fakeZellij) { z.screens = []string{"│ ❯ my draft │"} },
		"wrong cwd":   func(_ *Runtime, z *fakeZellij) { z.panes["renamed"][0].CWD = "/another/project" },
		"exited":      func(_ *Runtime, z *fakeZellij) { z.panes["renamed"][0].Exited = true },
		"moved pid": func(rt *Runtime, _ *fakeZellij) {
			rt.processPane = func(int) *ZellijPane { return &ZellijPane{Session: "stale-name", Pane: "9"} }
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			runtime, z, dir := typedRuntime(t, "│ ❯  │")
			change(runtime, z)
			ctx, cancel := context.WithCancel(context.Background())
			runtime.handleInject(ctx, &fakeAgentClient{}, typedRunner(dir), keyedDeps(),
				signedReq(t, clicore.AgentRequest{ID: "req-safe", Tool: "claude", TargetSessionID: "win-1", SealedPrompt: "do it"}))
			cancel()
			if len(z.pasted) != 0 || z.entered != 0 {
				t.Fatalf("unsafe pane received paste=%q enter=%d", z.pasted, z.entered)
			}
		})
	}
}
