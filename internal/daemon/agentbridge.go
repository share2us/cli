// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"errors"
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
	// AgentRequeueWaiting gets back, once at startup, the hops this device was
	// holding for an open window when it last stopped.
	AgentRequeueWaiting(ctx context.Context) (int, error)
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
// toolBound reports whether any binding is for tool.
func toolBound(list []Binding, tool string) bool {
	for _, b := range list {
		if b.Tool == tool {
			return true
		}
	}
	return false
}

func coveringBinding(list []Binding, s DiscoveredSession) (Binding, bool) {
	for _, b := range list {
		if b.Covers(s) {
			return b, true
		}
	}
	return Binding{}, false
}

// sessionRunner is a runner that resumes a session in place and reports the
// session the agent is in after the hop (Claude). A hop never forks (owner,
// 2026-09-29): the prompt goes into the bound session, where its owner reads it
// and what the agent did, or it waits (see holdInject).
type sessionRunner interface {
	RunSession(ctx context.Context, sessionID, cwd, prompt string) (output, sessionAfter string, err error)
}

// Holding a hop for a session a window has open: Claude cannot run a prompt
// headlessly in a session a running process holds, and forking it is not
// allowed, so the hop waits until the window lets the session go.
var (
	injectHoldPoll = 20 * time.Second
	injectHoldMax  = 24 * time.Hour
)

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
		// Bindings are re-read every sync so `s2u agent bind` takes effect within
		// one tick, without restarting the daemon.
		bindings, berr := LoadBindings()
		if berr != nil {
			// Fail CLOSED. An unreadable whitelist must not mean "advertise
			// everything" — that is the state this replaced.
			deps.logf("agent-bridge: cannot read bindings, advertising nothing: %v", berr)
			bindings = nil
		}
		var sessions []DiscoveredSession
		for _, runner := range runners {
			// Only a tool with a binding can have anything advertised, so only
			// those are asked. Discovery is not free: `gemini --list-sessions`
			// cost ~4-5 s of CPU per call, every 30 s, on a machine with no
			// Gemini binding (measured 2026-09-28: 16% of a core, ~90% of it
			// Gemini).
			if !toolBound(bindings, runner.Tool()) {
				continue
			}
			found, err := runner.Discover(ctx)
			if err != nil {
				deps.logf("agent-bridge discover (%s): %v", runner.Tool(), err)
				continue
			}
			sessions = append(sessions, found...)
		}
		// A single-session binding is advertised even when discovery does not
		// list its session (one no window has open is often not listed): the
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
			// can tell that a recreated session is still the same agent.
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
	// Hops held for an open window live only in memory: a restart asks the
	// server for them back. An older server does not know the call; that is fine.
	if n, err := client.AgentRequeueWaiting(ctx); err != nil {
		deps.logf("agent-bridge: could not ask for waiting hops back: %v", err)
	} else if n > 0 {
		deps.logf("agent-bridge: %d waiting hop(s) came back after a restart", n)
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
	// it (one no window has open).
	if list, lerr := LoadBindings(); lerr == nil {
		if b, ok := BindingForSession(list, req.TargetSessionID); ok {
			cwd = b.Project
		}
	}
	live := false
	if sessions, derr := runner.Discover(ctx); derr == nil {
		for _, s := range sessions {
			if s.SessionID == req.TargetSessionID {
				cwd = s.Project
				live = s.Live
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
	if live {
		rt.holdInject(ctx, client, runner, deps, req, env.Prompt, prompt, cwd)
		return
	}
	rt.runInject(ctx, client, runner, deps, req, env.Prompt, prompt, cwd, 0)
}

// holdInject waits, off the receive loop, until nothing holds the session, then
// runs the hop in it. The server already handed the hop over (it stays
// "delivered"), so it lives here until then. It gives up after injectHoldMax.
func (rt *Runtime) holdInject(ctx context.Context, client AgentClient, runner AgentRunner, deps Deps, req clicore.AgentRequest, shown, prompt, cwd string) {
	deps.logf("agent-bridge: inject %s waits: session %s is open in a window", req.ID, req.TargetSessionID)
	rt.notify("Share2Us", "A prompt is waiting for your "+req.Tool+" session. It runs in that session once you exit "+req.Tool+" there.")
	_ = client.AgentReportResult(ctx, req.ID, "waiting", "Waiting: the "+req.Tool+" session is open in a window. The prompt runs in that session once "+req.Tool+" is exited there.")
	rt.holding.Add(1)
	// The limit counts from when the hop was sent, so a restart does not reset it.
	began := time.Now()
	if t, err := time.Parse(time.RFC3339, req.CreatedAt); err == nil && t.Before(began) {
		began = t
	}
	go func() {
		defer rt.holding.Add(-1)
		t := time.NewTicker(injectHoldPoll)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if !sessionHeld(ctx, runner, req.TargetSessionID) {
				rt.runInject(ctx, client, runner, deps, req, shown, prompt, cwd, time.Since(began))
				return
			}
			if time.Since(began) > injectHoldMax {
				deps.logf("agent-bridge: inject %s gave up: session %s stayed open", req.ID, req.TargetSessionID)
				_ = AppendHop(HopRecord{Time: time.Now().UTC(), RequestID: req.ID, Tool: req.Tool, From: req.SenderDeviceID,
					Target: req.TargetSessionID, Mode: "expired", Status: "failed", Prompt: shown, Waited: time.Since(began).Round(time.Second).String()})
				_ = client.AgentReportResult(ctx, req.ID, "failed", "the session stayed open in a window for "+injectHoldMax.String()+", so the prompt did not run")
				return
			}
		}
	}()
}

// sessionHeld reports whether a running process (an open window) holds the
// session. A failed discovery counts as held: running beside a window that has
// the session open is the thing to avoid.
func sessionHeld(ctx context.Context, runner AgentRunner, sessionID string) bool {
	sessions, err := runner.Discover(ctx)
	if err != nil {
		return true
	}
	for _, s := range sessions {
		if s.SessionID == sessionID {
			return s.Live
		}
	}
	return false
}

// runInject runs the hop in the target session itself and reports the result.
func (rt *Runtime) runInject(ctx context.Context, client AgentClient, runner AgentRunner, deps Deps, req clicore.AgentRequest, shown, prompt, cwd string, waited time.Duration) {
	rt.hopMu.Lock()
	defer rt.hopMu.Unlock()
	rt.notify("Share2Us", "Running a prompt in your "+req.Tool+" session")
	deps.logf("agent-bridge: running inject %s in session %s (cwd %s)", req.ID, req.TargetSessionID, cwd)
	_ = client.AgentReportResult(ctx, req.ID, "running", "")
	var out string
	var err error
	rt.hopRunning.Store(true)
	defer rt.hopRunning.Store(false)
	hop := HopRecord{Time: time.Now().UTC(), RequestID: req.ID, Tool: req.Tool, From: req.SenderDeviceID,
		Target: req.TargetSessionID, RanIn: req.TargetSessionID, Mode: "ran", Prompt: shown}
	if waited > 0 {
		hop.Waited = waited.Round(time.Second).String()
	}
	if sr, ok := runner.(sessionRunner); ok {
		var after string
		out, after, err = sr.RunSession(ctx, req.TargetSessionID, cwd, prompt)
		if errors.Is(err, ErrSessionHeld) {
			// A window opened the session since the check: wait again.
			rt.hopRunning.Store(false)
			rt.holdInject(ctx, client, runner, deps, req, shown, prompt, cwd)
			return
		}
		hop.Mode = "resumed"
		if err == nil && after != "" && after != req.TargetSessionID {
			// Never followed: the agent is the bound session and nothing else.
			deps.logf("agent-bridge: inject %s ran in session %s, not the bound %s", req.ID, after, req.TargetSessionID)
			hop.RanIn = after
			err = fmt.Errorf("the agent tool ran the prompt in another session (%s) instead of the bound one", after)
		}
	} else {
		out, err = runner.Run(ctx, req.TargetSessionID, cwd, prompt)
	}
	hop.Status = "done"
	if err != nil {
		hop.Status = "failed"
	}
	if herr := AppendHop(hop); herr != nil {
		deps.logf("agent-bridge: could not record hop %s: %v", req.ID, herr)
	}
	if len(out) > maxReportedResult {
		out = out[:maxReportedResult]
	}
	if err != nil {
		deps.logf("agent-bridge: inject %s failed: %v", req.ID, err)
		if out == "" {
			out = err.Error()
		}
		_ = client.AgentReportResult(ctx, req.ID, "failed", out)
		return
	}
	_ = client.AgentReportResult(ctx, req.ID, "done", out)
}
