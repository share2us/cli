// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	clicore "github.com/share2us/cli-core"
)

// AgentClient is the subset of the server API the bridge loop drives (implemented
// by *clicore.Client). Kept an interface so the loop is unit-testable.
type AgentClient interface {
	RegisterAgentSession(ctx context.Context, in clicore.AgentRegisterInput) error
	// ListAgentSessions is used once at startup to retire sessions this device
	// advertised before they were bound (ADR-041 §1 / F2).
	ListAgentSessions(ctx context.Context) ([]clicore.AgentSessionInfo, error)
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
//
// The bridge is on by default (owner, 2026-09-27: joining must not require
// starting anything by hand), but it stays IDLE, with no network calls at all,
// until this machine has at least one binding. Binding is the explicit opt-in,
// and nothing is advertised without one. Bindings made after the daemon started
// (`s2u agent bind`, `s2u agent join`) wake it within agentBindingPoll.
func (rt *Runtime) agentBridge(ctx context.Context, client AgentClient, runners []AgentRunner, deps Deps) {
	if !waitForBinding(ctx, agentBindingPoll) {
		return
	}
	deps.logf("agent-bridge: a session is bound; advertising bound sessions and receiving requests")
	go rt.agentRegisterLoop(ctx, client, runners, deps)
	rt.agentReceiveLoop(ctx, client, runners, deps)
}

// agentBindingPoll is how often an idle bridge checks for a first binding.
var agentBindingPoll = 15 * time.Second

// waitForBinding blocks until at least one binding exists (true) or ctx ends.
func waitForBinding(ctx context.Context, every time.Duration) bool {
	for {
		if list, err := LoadBindings(); err == nil && len(list) > 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(every):
		}
	}
}

// coveringBinding finds the binding a session belongs to: its own single-session
// binding, or a folder-wide one from before single-session binding existed.
func coveringBinding(list []Binding, s DiscoveredSession) (Binding, bool) {
	for _, b := range list {
		if b.Covers(s) {
			return b, true
		}
	}
	return Binding{}, false
}

// forkingRunner is a runner whose hops create a new session (Claude forks on
// every headless resume) and can say which.
type forkingRunner interface {
	RunForked(ctx context.Context, sessionID, cwd, prompt string) (output, newSessionID string, err error)
}

// bridgeRefused reports a server answer that retrying soon cannot change: the
// plan does not include agents, or the bridge is off on this server. The daemon
// then waits agentRefusedBackoff instead of hammering the API every few seconds.
func bridgeRefused(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "agent_bridge_not_allowed") || strings.Contains(m, "agent_bridge_disabled")
}

const agentRefusedBackoff = 15 * time.Minute

// onceLogger logs a message only when it differs from the previous one, so a
// persistent error is reported once rather than every cycle.
type onceLogger struct {
	last string
	logf func(string, ...any)
}

func (o *onceLogger) log(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if msg == o.last {
		return
	}
	o.last = msg
	o.logf("%s", msg)
}

// agentRegisterLoop discovers local sessions and registers/heartbeats them,
// deregistering ones that have gone away.
func (rt *Runtime) agentRegisterLoop(ctx context.Context, client AgentClient, runners []AgentRunner, deps Deps) {
	rt.retireUnboundSessions(ctx, client, deps)
	known := map[string]bool{}
	quiet := &onceLogger{logf: deps.logf}
	sync := func() {
		seen := map[string]bool{}
		var sessions []DiscoveredSession
		for _, runner := range runners {
			found, err := runner.Discover(ctx)
			if err != nil {
				deps.logf("agent-bridge discover (%s): %v", runner.Tool(), err)
				continue
			}
			sessions = append(sessions, found...)
		}
		// Bindings are re-read every sync so `s2u agent bind` takes effect within
		// one tick, without restarting the daemon.
		bindings, berr := LoadBindings()
		if berr != nil {
			// Fail CLOSED. An unreadable whitelist must not mean "advertise
			// everything" — that is the state this replaced.
			deps.logf("agent-bridge: cannot read bindings, advertising nothing: %v", berr)
			bindings = nil
		}
		// A single-session binding is advertised even when discovery does not
		// list its session (a fork made by a hop often is not listed): the
		// binding knows where it lives.
		for _, b := range bindings {
			if b.SessionID == "" {
				continue
			}
			listed := false
			for _, s := range sessions {
				if s.SessionID == b.SessionID {
					listed = true
					break
				}
			}
			if !listed {
				sessions = append(sessions, DiscoveredSession{SessionID: b.SessionID, Tool: b.Tool, Name: b.Label, Project: b.Project, Status: "available"})
			}
		}
		for _, s := range sessions {
			b, bound := coveringBinding(bindings, s)
			if !bound {
				continue
			}
			seen[s.SessionID] = true
			// The binding's agent id rides with every registration, so the server
			// can tell that a forked or recreated session is still the same agent.
			if err := client.RegisterAgentSession(ctx, clicore.AgentRegisterInput{
				AgentID: b.AgentID, SessionID: s.SessionID, Tool: s.Tool, Name: s.Name, Project: s.Project, Status: s.Status,
			}); err != nil {
				quiet.log("agent-bridge register: %v", err)
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

// retireUnboundSessions deregisters anything this device advertised before
// bindings existed (or before the owner unbound a project). The in-memory
// `known` map only covers one daemon lifetime, so without this a session
// registered by an older build stays visible on the server forever.
//
// Best-effort by design: a listing failure must not stop the bridge starting.
func (rt *Runtime) retireUnboundSessions(ctx context.Context, client AgentClient, deps Deps) {
	if deps.DeviceSessionID == "" {
		return // cannot tell our own sessions from another device's
	}
	remote, err := client.ListAgentSessions(ctx)
	if err != nil {
		deps.logf("agent-bridge: could not list existing sessions to retire: %v", err)
		return
	}
	bindings, berr := LoadBindings()
	if berr != nil {
		deps.logf("agent-bridge: cannot read bindings while retiring: %v", berr)
		bindings = nil
	}
	for _, s := range remote {
		if s.DeviceID != deps.DeviceSessionID {
			continue // another device's session; not ours to retire
		}
		if _, bound := coveringBinding(bindings, DiscoveredSession{SessionID: s.SessionID, Tool: s.Tool, Project: s.Project}); bound {
			continue
		}
		if err := client.DeregisterAgentSession(ctx, s.SessionID); err != nil {
			deps.logf("agent-bridge: retire %s: %v", s.SessionID, err)
			continue
		}
		deps.logf("agent-bridge: retired unbound session %s (%s in %s)", s.SessionID, s.Tool, s.Project)
	}
}

// agentReceiveLoop long-polls for inject requests and runs each one.
func (rt *Runtime) agentReceiveLoop(ctx context.Context, client AgentClient, runners []AgentRunner, deps Deps) {
	byTool := map[string]AgentRunner{}
	for _, r := range runners {
		byTool[r.Tool()] = r
	}
	for {
		if ctx.Err() != nil {
			return
		}
		reqs, err := client.AgentLongPoll(ctx, agentLongPollWait)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			wait := 5 * time.Second // back off on error
			if bridgeRefused(err) {
				// Nothing will change for a while (plan or server setting): say so
				// once and check back rarely.
				deps.logf("agent-bridge: the server refused (%v); checking again in %s", err, agentRefusedBackoff)
				wait = agentRefusedBackoff
			} else {
				deps.logf("agent-bridge long-poll: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		}
		for _, req := range reqs {
			runner := byTool[req.Tool]
			if runner == nil {
				deps.logf("agent-bridge: no runner for tool %q (request %s)", req.Tool, req.ID)
				_ = client.AgentReportResult(ctx, req.ID, "failed", "this machine does not run "+req.Tool+" sessions")
				continue
			}
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
	// Authenticate the sender FIRST (ADR-041 §5), before anything is unsealed or
	// run. The server checked this hop's signature too, but a compromised server
	// could skip that check or queue a hop it wrote itself; this check, against a
	// key the server cannot change after the fact, is the one that defends against
	// it. Fail closed: with no pin store there is nothing to enforce against.
	if deps.SenderPins == nil {
		deps.logf("agent-bridge: refusing inject %s: sender verification is unavailable", req.ID)
		_ = client.AgentReportResult(ctx, req.ID, "failed", "the receiving device cannot verify who sent this, so it will not run it")
		return
	}
	if verr := deps.SenderPins.VerifyDelivered(req, deps.DeviceSessionID, time.Now()); verr != nil {
		deps.logf("agent-bridge: refusing inject %s from %s: %v", req.ID, req.SenderDeviceID, verr)
		rt.notify("Share2Us", "Refused a prompt that could not be verified as coming from its sender")
		_ = client.AgentReportResult(ctx, req.ID, "failed", "refused by the receiving device: "+verr.Error())
		return
	}

	// E2E (ADR-036 P4): the prompt is sealed to this device's key; unseal it before
	// running. A decryption failure is fatal for the request (never run a garbled
	// or unexpectedly-plaintext prompt). No key at all is fatal too: a prompt this
	// device cannot unseal is a prompt whose author it cannot vouch for, and the
	// runner executes it with edits pre-approved in the user's project. The old
	// code ran req.SealedPrompt AS-IS when Unseal was nil (§AJ #8).
	if deps.Unseal == nil {
		deps.logf("agent-bridge: refusing inject %s: this device has no encryption key", req.ID)
		_ = client.AgentReportResult(ctx, req.ID, "failed", "the receiving device has no encryption key and will not run an unsealed prompt")
		return
	}
	raw, uerr := deps.Unseal(req.SealedPrompt)
	if uerr != nil {
		deps.logf("agent-bridge: cannot decrypt inject %s: %v", req.ID, uerr)
		_ = client.AgentReportResult(ctx, req.ID, "failed", "the receiving device could not decrypt the prompt")
		return
	}
	env := ParseEnvelope(raw)
	prompt := env.Prompt
	cwd := ""
	// The binding knows where its session lives even when discovery does not list
	// it (a fork from an earlier hop).
	if list, lerr := LoadBindings(); lerr == nil {
		if b, ok := BindingForSession(list, req.TargetSessionID); ok {
			cwd = b.Project
		}
	}
	if sessions, derr := runner.Discover(ctx); derr == nil {
		for _, s := range sessions {
			if s.SessionID == req.TargetSessionID {
				cwd = s.Project
				break
			}
		}
	}
	// A delivered file (ADR-036 P4b): download the ciphertext, open its content
	// key with this device's key, decrypt it into the session's .s2u-inbox, and
	// point the prompt at it.
	if req.HasFile && env.FileName != "" && deps.DownloadContent != nil && deps.OpenContentKey != nil {
		ciphertext, derr := deps.DownloadContent(ctx, req.ID)
		if derr != nil {
			deps.logf("agent-bridge: download file for %s: %v", req.ID, derr)
			_ = client.AgentReportResult(ctx, req.ID, "failed", "could not download the attached file")
			return
		}
		ck, kerr := deps.OpenContentKey(req.SealedFileKey)
		if kerr != nil {
			_ = client.AgentReportResult(ctx, req.ID, "failed", "could not decrypt the attached file key")
			return
		}
		path, perr := placeInjectedFile(cwd, env.FileName, ciphertext, ck)
		if perr != nil {
			_ = client.AgentReportResult(ctx, req.ID, "failed", "could not write the attached file")
			return
		}
		prompt = prompt + "\n\n(A file for this task was placed at " + path + ".)"
	}
	rt.notify("Share2Us", "Running a prompt in your "+req.Tool+" session")
	deps.logf("agent-bridge: running inject %s in session %s (cwd %s)", req.ID, req.TargetSessionID, cwd)
	_ = client.AgentReportResult(ctx, req.ID, "running", "")
	var out string
	var err error
	if fr, ok := runner.(forkingRunner); ok {
		var forked string
		out, forked, err = fr.RunForked(ctx, req.TargetSessionID, cwd, prompt)
		// The agent now IS the fork: move its binding, so the next hop continues
		// this conversation and only the fork is advertised.
		if moved, merr := MoveSession(req.TargetSessionID, forked); merr != nil {
			deps.logf("agent-bridge: could not move the binding to session %s: %v", forked, merr)
		} else if moved {
			deps.logf("agent-bridge: agent continues in forked session %s", forked)
		}
	} else {
		out, err = runner.Run(ctx, req.TargetSessionID, cwd, prompt)
	}
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
