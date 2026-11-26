package daemon

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	clicore "github.com/share2us/cli-core"
)

func fakeCodexWindow(t *testing.T, pid *atomic.Int64, queue func(session, msg string) error) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	oldReady, oldQueue, oldPoll := codexLiveReady, codexQueue, codexLivePoll
	codexLiveReady = func(string) int { return int(pid.Load()) }
	codexQueue = func(_ context.Context, session, _, msg string) error { return queue(session, msg) }
	codexLivePoll = 20 * time.Millisecond
	t.Cleanup(func() { codexLiveReady, codexQueue, codexLivePoll = oldReady, oldQueue, oldPoll })
}

func waitCodexIdle(t *testing.T, r *Runtime) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.holding.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if r.holding.Load() != 0 {
		t.Fatal("the live hop never finished")
	}
}

const codexHopPrompt = "check the disk"

func TestCodexLiveDeliversAndReportsTheTurnReply(t *testing.T) {
	var pid atomic.Int64
	pid.Store(4242)
	r := rt()
	fakeCodexWindow(t, &pid, func(session, msg string) error {
		go func() {
			if !BindTypedPromptID(session, msg, "turn-1") {
				t.Error("the queued prompt didn't bind to its turn")
			}
			if CodexTurnGuarded(session, "turn-0", "") || !CodexTurnGuarded(session, "turn-1", "") {
				t.Error("only the hop's turn is guarded")
			}
			if r.codexTurnFinished(session, "turn-0", "owner's turn") {
				t.Error("another turn's reply was taken")
			}
			r.codexTurnFinished(session, "turn-1", "disk is 40% full")
		}()
		return nil
	})
	c := &fakeAgentClient{}
	req := clicore.AgentRequest{ID: "r1", Tool: "codex", TargetSessionID: "cx1"}
	r.runInject(context.Background(), c, &fakeRunner{out: "headless"}, keyedDeps(), req, "check the disk", codexHopPrompt, "laptop", t.TempDir(), 0)
	waitCodexIdle(t, r)
	if len(c.reports) != 2 || c.reports[0][0] != "running" || c.reports[1] != [2]string{"done", "disk is 40% full"} {
		t.Fatalf("reports %v", c.reports)
	}
	if TypedGuardRequest("cx1") != "" {
		t.Fatal("the guard marker outlived the turn")
	}
}

func TestCodexLiveFallsBackOrFails(t *testing.T) {
	var pid atomic.Int64
	pid.Store(4242)
	fakeCodexWindow(t, &pid, func(string, string) error { return errors.New("no such thread") })
	r := rt()
	c := &fakeAgentClient{}
	runner := &fakeRunner{out: "ran headless"}
	r.runInject(context.Background(), c, runner, keyedDeps(), clicore.AgentRequest{ID: "r1", Tool: "codex", TargetSessionID: "cx1"}, "x", codexHopPrompt, "laptop", t.TempDir(), 0)
	waitCodexIdle(t, r)
	if runner.ranSID != "cx1" || c.reports[len(c.reports)-1] != [2]string{"done", "ran headless"} {
		t.Fatalf("a failed queue didn't fall back to the background run: %v", c.reports)
	}

	codexQueue = func(context.Context, string, string, string) error { pid.Store(0); return nil } // window closes
	c = &fakeAgentClient{}
	r.runInject(context.Background(), c, &fakeRunner{}, keyedDeps(), clicore.AgentRequest{ID: "r2", Tool: "codex", TargetSessionID: "cx1"}, "x", codexHopPrompt, "laptop", t.TempDir(), 0)
	waitCodexIdle(t, r)
	last := c.reports[len(c.reports)-1]
	if last[0] != "failed" {
		t.Fatalf("a closed window didn't fail the hop: %v", c.reports)
	}
}
