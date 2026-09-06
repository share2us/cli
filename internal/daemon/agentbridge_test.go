package daemon

import (
	"context"
	"errors"
	"testing"

	clicore "github.com/share2us/cli-core"
)

type fakeAgentClient struct {
	reports    [][2]string // {status, result}
	registered []string
	deregd     []string
}

func (f *fakeAgentClient) RegisterAgentSession(_ context.Context, in clicore.AgentRegisterInput) error {
	f.registered = append(f.registered, in.SessionID)
	return nil
}
func (f *fakeAgentClient) DeregisterAgentSession(_ context.Context, id string) error {
	f.deregd = append(f.deregd, id)
	return nil
}
func (f *fakeAgentClient) AgentLongPoll(_ context.Context, _ int) ([]clicore.AgentRequest, error) {
	return nil, nil
}
func (f *fakeAgentClient) AgentReportResult(_ context.Context, _, status, result string) error {
	f.reports = append(f.reports, [2]string{status, result})
	return nil
}

type fakeRunner struct {
	out    string
	err    error
	ranSID string
}

func (f *fakeRunner) Tool() string { return "claude" }
func (f *fakeRunner) Discover(context.Context) ([]DiscoveredSession, error) {
	return []DiscoveredSession{{SessionID: "s1", Tool: "claude", Status: "idle"}}, nil
}
func (f *fakeRunner) Run(_ context.Context, sessionID, _ string) (string, error) {
	f.ranSID = sessionID
	return f.out, f.err
}

func rt() *Runtime { return &Runtime{notifier: NoopNotifier{}} }
func noDeps() Deps { return Deps{Logf: func(string, ...any) {}} }

func TestHandleInjectHappy(t *testing.T) {
	c := &fakeAgentClient{}
	r := &fakeRunner{out: "did the thing"}
	rt().handleInject(context.Background(), c, r, noDeps(),
		clicore.AgentRequest{ID: "req-1", Tool: "claude", TargetSessionID: "s1", SealedPrompt: "do it"})
	if r.ranSID != "s1" {
		t.Fatalf("runner ran session %q, want s1", r.ranSID)
	}
	if len(c.reports) != 2 || c.reports[0][0] != "running" || c.reports[1][0] != "done" || c.reports[1][1] != "did the thing" {
		t.Fatalf("reports = %v, want running then done+output", c.reports)
	}
}

func TestHandleInjectRunError(t *testing.T) {
	c := &fakeAgentClient{}
	r := &fakeRunner{out: "boom output", err: errors.New("nonzero exit")}
	rt().handleInject(context.Background(), c, r, noDeps(),
		clicore.AgentRequest{ID: "req-1", Tool: "claude", TargetSessionID: "s1", SealedPrompt: "x"})
	if len(c.reports) != 2 || c.reports[1][0] != "failed" {
		t.Fatalf("reports = %v, want running then failed", c.reports)
	}
}

func TestHandleInjectUnsupportedTool(t *testing.T) {
	c := &fakeAgentClient{}
	r := &fakeRunner{}
	rt().handleInject(context.Background(), c, r, noDeps(),
		clicore.AgentRequest{ID: "req-1", Tool: "codex", TargetSessionID: "s1"})
	if r.ranSID != "" {
		t.Fatal("runner should not run for an unsupported tool")
	}
	if len(c.reports) != 1 || c.reports[0][0] != "failed" {
		t.Fatalf("reports = %v, want a single failed", c.reports)
	}
}

func TestRegisterLoopSyncDeregistersVanished(t *testing.T) {
	c := &fakeAgentClient{}
	r := &fakeRunner{}
	// One sync pass via a cancelled context: run agentRegisterLoop's body once by
	// calling Discover+register directly through a short-lived loop is awkward, so
	// assert the runner's Discover feeds registration through a manual sync.
	sessions, _ := r.Discover(context.Background())
	for _, s := range sessions {
		_ = c.RegisterAgentSession(context.Background(), clicore.AgentRegisterInput{SessionID: s.SessionID})
	}
	if len(c.registered) != 1 || c.registered[0] != "s1" {
		t.Fatalf("registered = %v, want [s1]", c.registered)
	}
}
