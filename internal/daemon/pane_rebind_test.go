package daemon

import (
	"context"
	"testing"

	clicore "github.com/share2us/cli-core"
)

// a runner that reports exactly the sessions it is given.
type rebindRunner struct{ sessions []DiscoveredSession }

func (r rebindRunner) Tool() string { return "claude" }
func (r rebindRunner) Discover(context.Context) ([]DiscoveredSession, error) {
	return r.sessions, nil
}
func (r rebindRunner) Run(context.Context, string, string, string) (string, error) {
	return "", nil
}

func TestPaneRebindNeeded(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	proj := t.TempDir()
	bind := func(disabled bool) {
		if err := saveBindings([]Binding{{
			AgentID: "agt_x", Project: proj, Tool: "claude", SessionID: "sess-1",
			Zellij: &ZellijPane{Session: "s", Pane: "1"}, TypedDeliveryDisabled: disabled,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	req := clicore.AgentRequest{TargetSessionID: "sess-1", Tool: "claude"}
	live := rebindRunner{sessions: []DiscoveredSession{
		{SessionID: "sess-1", Tool: "claude", PID: 123, Live: true, Project: proj},
	}}
	// The live session's process resolves to a DIFFERENT pane than the binding.
	mismatch := &Runtime{notifier: NoopNotifier{}, zellij: &fakeZellij{},
		processPane: func(int) *ZellijPane { return &ZellijPane{Session: "s", Pane: "9"} }}

	bind(false)
	if !mismatch.paneRebindNeeded(context.Background(), live, req) {
		t.Error("a bound typed session whose pane no longer matches should need a re-bind")
	}

	// No live session discovered: not a re-bind case (nothing to type into).
	if mismatch.paneRebindNeeded(context.Background(), rebindRunner{}, req) {
		t.Error("no live session should not report needs-rebind")
	}

	// Typed delivery turned off by the owner: not a re-bind case.
	bind(true)
	if mismatch.paneRebindNeeded(context.Background(), live, req) {
		t.Error("typed-delivery-disabled should not report needs-rebind")
	}
}
