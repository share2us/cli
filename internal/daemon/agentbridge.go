package daemon

import (
	"context"
	"time"

	clicore "github.com/share2us/cli-core"
)

// AgentClient is the subset of the server API the bridge loop drives (implemented
// by *clicore.Client). Kept an interface so the loop is unit-testable.
type AgentClient interface {
	RegisterAgentSession(ctx context.Context, in clicore.AgentRegisterInput) error
	DeregisterAgentSession(ctx context.Context, sessionID string) error
	AgentLongPoll(ctx context.Context, waitSeconds int) ([]clicore.AgentRequest, error)
	AgentReportResult(ctx context.Context, id, status, result string) error
}

// AgentRunner discovers this machine's sessions for one tool and runs an injected
// prompt into one of them (implemented by the Claude adapter in agents_claude.go).
type AgentRunner interface {
	Tool() string
	Discover(ctx context.Context) ([]DiscoveredSession, error)
	Run(ctx context.Context, sessionID, cwd, prompt string) (string, error)
}

const (
	agentRegisterEvery = 30 * time.Second
	agentLongPollWait  = 25
	maxReportedResult  = 4096
)

// agentBridge runs the two agent-bridge loops (ADR-036 P2): one keeps the server
// directory in sync with local sessions, the other receives relayed inject
// requests and runs them. Both stop on ctx cancel.
func (rt *Runtime) agentBridge(ctx context.Context, client AgentClient, runner AgentRunner, deps Deps) {
	go rt.agentRegisterLoop(ctx, client, runner, deps)
	rt.agentReceiveLoop(ctx, client, runner, deps)
}

// agentRegisterLoop discovers local sessions and registers/heartbeats them,
// deregistering ones that have gone away.
func (rt *Runtime) agentRegisterLoop(ctx context.Context, client AgentClient, runner AgentRunner, deps Deps) {
	known := map[string]bool{}
	sync := func() {
		sessions, err := runner.Discover(ctx)
		if err != nil {
			deps.logf("agent-bridge discover: %v", err)
			return
		}
		seen := map[string]bool{}
		for _, s := range sessions {
			seen[s.SessionID] = true
			if err := client.RegisterAgentSession(ctx, clicore.AgentRegisterInput{
				SessionID: s.SessionID, Tool: s.Tool, Name: s.Name, Project: s.Project, Status: s.Status,
			}); err != nil {
				deps.logf("agent-bridge register %s: %v", s.SessionID, err)
			}
		}
		for id := range known {
			if !seen[id] {
				_ = client.DeregisterAgentSession(ctx, id)
			}
		}
		known = seen
	}
	sync()
	t := time.NewTicker(agentRegisterEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sync()
		}
	}
}

// agentReceiveLoop long-polls for inject requests and runs each one.
func (rt *Runtime) agentReceiveLoop(ctx context.Context, client AgentClient, runner AgentRunner, deps Deps) {
	for {
		if ctx.Err() != nil {
			return
		}
		reqs, err := client.AgentLongPoll(ctx, agentLongPollWait)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			deps.logf("agent-bridge long-poll: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second): // back off on error
			}
			continue
		}
		for _, req := range reqs {
			rt.handleInject(ctx, client, runner, deps, req)
		}
	}
}

func (rt *Runtime) handleInject(ctx context.Context, client AgentClient, runner AgentRunner, deps Deps, req clicore.AgentRequest) {
	if req.Tool != runner.Tool() {
		deps.logf("agent-bridge: unsupported tool %q for request %s", req.Tool, req.ID)
		_ = client.AgentReportResult(ctx, req.ID, "failed", "this daemon does not run "+req.Tool+" sessions")
		return
	}
	// Phase 2 relays the prompt as-is; Phase 4 unseals it with the device key.
	prompt := unsealPrompt(req.SealedPrompt)
	cwd := ""
	if sessions, derr := runner.Discover(ctx); derr == nil {
		for _, s := range sessions {
			if s.SessionID == req.TargetSessionID {
				cwd = s.Project
				break
			}
		}
	}
	rt.notify("Share2Us", "Running a prompt in your "+req.Tool+" session")
	deps.logf("agent-bridge: running inject %s in session %s (cwd %s)", req.ID, req.TargetSessionID, cwd)
	_ = client.AgentReportResult(ctx, req.ID, "running", "")
	out, err := runner.Run(ctx, req.TargetSessionID, cwd, prompt)
	if len(out) > maxReportedResult {
		out = out[:maxReportedResult]
	}
	if err != nil {
		deps.logf("agent-bridge: inject %s failed: %v", req.ID, err)
		_ = client.AgentReportResult(ctx, req.ID, "failed", out)
		return
	}
	_ = client.AgentReportResult(ctx, req.ID, "done", out)
}

// unsealPrompt returns the plaintext prompt. Phase 2 passes the field through;
// Phase 4 replaces this with device-key unsealing (E2E).
func unsealPrompt(sealed string) string { return sealed }
