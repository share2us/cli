// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli-core/lanid"
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
	client = sealingClient{AgentClient: client, rt: rt, deps: deps}
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
	injectHoldPoll = 3 * time.Second // one check: `claude agents` (~0.2 s) + a screen read
	injectHoldMax  = 24 * time.Hour
	// injectPaneRebindTimeout: how long a hop may sit undeliverable because the
	// target session's bound Zellij pane can no longer be verified before the
	// daemon tells the sender it needs a re-bind. Short, because this is a
	// stuck state a person must act on, not a transient wait for a window.
	injectPaneRebindTimeout = 2 * time.Minute
)

// AgentNeedsRebindMarker prefixes a reported "waiting" result when a prompt could
// not be typed into its target session because that session's bound window/pane
// can no longer be verified (it moved panes, left Zellij, or was never re-bound
// after a restart). Senders detect this prefix to tell the user to re-bind the
// session. The hop is NOT abandoned: once the session is re-bound it delivers on
// the next poll, no re-send needed. Keep this string in sync with the desktop app
// (share2us-gui internal/core/agent.go) and the CLI `agent status` display.
const AgentNeedsRebindMarker = "S2U_NEEDS_REBIND: "

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
// reconcileBindingPane refreshes a bound session's stored zellij pane when its
// process has moved to a different pane (the zellij session was recreated, or tabs
// were added/removed). It reads the pane from the session's OWN process — the same
// source `s2u agent bind` uses — so it tracks the same session, never a different
// one, and only updates a binding already bound in zellij (ReconcileZellij gates
// that). This is the self-heal that removes the manual re-bind step.
func (rt *Runtime) reconcileBindingPane(s DiscoveredSession, deps Deps) {
	if s.SessionID == "" || s.PID <= 1 {
		return
	}
	want := rt.processZellijPane(s.PID)
	if want == nil {
		want = TerminalHookPane(s.SessionID, s.PID)
	}
	if want == nil {
		return
	}
	changed, err := ReconcileZellij(s.SessionID, want)
	if err != nil {
		deps.logf("agent-bridge: pane reconcile for %s failed: %v", s.SessionID, err)
		return
	}
	if changed {
		deps.logf("agent-bridge: refreshed zellij pane for %s -> %s/%s", s.SessionID, want.Session, want.Pane)
	}
}

// sessionNowInPane finds the session that replaced a binding's session in the SAME zellij
// pane and project: `/clear` and a fresh `claude` in that tab start a new session id, and
// without this the agent's hops ran headless against the old, closed session while its
// owner watched an idle window (owner report 2026-11-26). Only an exact pane match counts,
// and only when the old session is no longer live, so the agent never jumps elsewhere.
func (rt *Runtime) sessionNowInPane(b Binding, sessions []DiscoveredSession) (DiscoveredSession, *ZellijPane, bool) {
	if b.Zellij == nil || b.SessionID == "" {
		return DiscoveredSession{}, nil, false
	}
	var hit DiscoveredSession
	var hitPane *ZellijPane
	n := 0
	for _, s := range sessions {
		if s.SessionID == b.SessionID && s.Live {
			return DiscoveredSession{}, nil, false // still there
		}
		if !s.Live || s.SessionID == b.SessionID || s.PID <= 1 || s.Tool != b.Tool ||
			normalizeProject(s.Project) != normalizeProject(b.Project) {
			continue
		}
		pane := rt.processZellijPane(s.PID)
		if pane == nil {
			pane = TerminalHookPane(s.SessionID, s.PID)
		}
		if pane != nil && *pane == *b.Zellij {
			hit, hitPane = s, pane
			n++
		}
	}
	return hit, hitPane, n == 1
}

// followMovedSession moves the binding (same agent id) to the session now in its pane.
func (rt *Runtime) followMovedSession(b Binding, sessions []DiscoveredSession, deps Deps) (DiscoveredSession, bool) {
	s, pane, ok := rt.sessionNowInPane(b, sessions)
	if !ok {
		return DiscoveredSession{}, false
	}
	if _, _, err := BindSessionInPane(b.Project, b.Tool, b.Label, s.SessionID, pane); err != nil {
		deps.logf("agent-bridge: could not follow %s to session %s: %v", b.AgentID, s.SessionID, err)
		return DiscoveredSession{}, false
	}
	deps.logf("agent-bridge: agent %s followed its pane %s/%s from session %s to %s", b.AgentID, pane.Session, pane.Pane, b.SessionID, s.SessionID)
	return s, true
}

// openRebindWarningTab surfaces a loud, foreground warning that a prompt is stuck
// because the bound session's pane can't be located — a new zellij tab whose title
// says to re-bind. It is best-effort and deduped by the caller (rebindReported), so
// it fires once per stuck episode, never on every poll. If the zellij session is
// gone (or warn-tab is off), the already-sent desktop notification is the signal.
func (rt *Runtime) openRebindWarningTab(ctx context.Context, req clicore.AgentRequest, deps Deps) {
	if !rt.warnTab {
		return
	}
	list, err := LoadBindings()
	if err != nil {
		return
	}
	binding, ok := BindingForSession(list, req.TargetSessionID)
	if !ok || binding.Zellij == nil || binding.Zellij.Session == "" {
		return // bound outside zellij: nothing to open a tab in
	}
	z := rt.paneDriver()
	sessions, err := z.Sessions(ctx)
	if err != nil {
		return
	}
	target := binding.Zellij.Session
	live := false
	for _, s := range sessions {
		if s == target {
			live = true
			break
		}
	}
	if !live {
		return // the whole zellij session is gone; the desktop notification stands
	}
	label := binding.Label
	if label == "" {
		label = req.TargetSessionID
	}
	title := "⚠ SHARE2US: re-bind " + label + " — prompt waiting"
	if err := z.NewTab(ctx, target, title); err != nil {
		deps.logf("agent-bridge: could not open re-bind warning tab in %s: %v", target, err)
	}
}

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
		moved := false
		for _, b := range bindings {
			if _, ok := rt.followMovedSession(b, sessions, deps); ok {
				moved = true
			}
		}
		if moved {
			if fresh, err := LoadBindings(); err == nil {
				bindings = fresh
			}
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
			// Keep the bound zellij pane in step with where the session's process
			// actually lives, so a recreated/rearranged zellij session recovers
			// without a manual `s2u agent bind` (ADR-041 §1).
			rt.reconcileBindingPane(s, deps)
			// The binding's agent id rides with every registration, so the server
			// can tell that a recreated session is still the same agent.
			if err := client.RegisterAgentSession(ctx, clicore.AgentRegisterInput{
				AgentID: b.AgentID, SessionID: s.SessionID, Tool: s.Tool, Name: s.Name, Project: s.Project, Status: s.Status,
				// This device's LAN fingerprint, so a sender can find and pin it
				// for a direct LAN agent-file transfer (the directory returns it).
				LANFingerprint: lanid.Fingerprint(),
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
	// Owner approvals (ADR-052): continuations after a late approval need these.
	rt.setApprovalBridge(client, byTool, deps)
	go rt.watchParkedApprovals(ctx)
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
	rt.noteTaskProject(req.ID, req.ProjectID, req.ProjectSecure)
	if req.Tool != runner.Tool() {
		deps.logf("agent-bridge: unsupported tool %q for request %s", req.Tool, req.ID)
		_ = client.AgentReportResult(ctx, req.ID, "failed", "this daemon does not run "+req.Tool+" sessions")
		return
	}
	var env InjectEnvelope
	if req.Kind == "channel" {
		// An @mention in a sharenet project (ADR-053): no sending device to verify; the server
		// vouches for the author, and the prompt is the message the project's members see.
		// It runs under this agent's guard like any delivered prompt; its owner can refuse
		// these tasks (s2u agent mentions off).
		e, why := channelEnvelope(req)
		if why != "" {
			deps.logf("agent-bridge: refusing channel task %s: %s", req.ID, why)
			_ = client.AgentReportResult(ctx, req.ID, "failed", why)
			return
		}
		env = e
	} else {
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
		env = ParseEnvelope(raw)
	}
	if env.Deliver != "" && env.Deliver != "run" && env.Deliver != "inbox" {
		_ = client.AgentReportResult(ctx, req.ID, "failed", "unknown file delivery mode")
		return
	}
	if env.Deliver == "inbox" && !req.HasFile {
		_ = client.AgentReportResult(ctx, req.ID, "failed", "inbox delivery requires an attached file")
		return
	}
	prompt := env.Prompt
	if name := strings.TrimSpace(env.SenderDeviceName); name != "" {
		rt.noteHopSender(req.ID, name)
	} else {
		rt.noteHopSender(req.ID, req.SenderDeviceID)
	}
	cwd := ""
	var binding Binding
	bound := false
	// The binding knows where its session lives even when discovery does not list
	// it (one no window has open).
	if list, lerr := LoadBindings(); lerr == nil {
		if b, ok := BindingForSession(list, req.TargetSessionID); ok {
			binding, bound = b, true
			cwd = b.Project
		}
	}
	live := false
	var discovered DiscoveredSession
	if sessions, derr := runner.Discover(ctx); derr == nil {
		for _, s := range sessions {
			if s.SessionID == req.TargetSessionID {
				discovered = s
				cwd = s.Project
				live = s.Live
				break
			}
		}
		// The agent's window moved on to a new session (/clear, a new claude in the same
		// pane): deliver there, in the foreground, not headless into the old session.
		if !live && bound {
			if s, ok := rt.followMovedSession(binding, sessions, deps); ok {
				req.TargetSessionID, discovered, cwd, live = s.SessionID, s, s.Project, true
				if list, lerr := LoadBindings(); lerr == nil {
					binding, bound = BindingForSession(list, s.SessionID)
				}
			}
		}
	}
	// A delivered file (ADR-036 P4b): download the ciphertext, open its content
	// key with this device's key, decrypt it into the session's .s2u-inbox, and
	// point the prompt at it.
	if req.HasFile != (env.FileName != "") {
		_ = client.AgentReportResult(ctx, req.ID, "failed", "the attached file description is incomplete")
		return
	}
	filePath := ""
	if req.HasFile {
		if req.SealedFileKey == "" || deps.OpenContentKey == nil {
			_ = client.AgentReportResult(ctx, req.ID, "failed", "the receiving device cannot open the attached file")
			return
		}
		var ciphertext io.Reader
		if deps.AgentFiles != nil && env.SenderLANFingerprint != "" {
			staged, found, serr := deps.AgentFiles.open(req.Nonce, env.SenderLANFingerprint)
			if serr != nil {
				deps.logf("agent-bridge: read staged file for %s: %v", req.ID, serr)
				_ = client.AgentReportResult(ctx, req.ID, "failed", "could not read the staged attached file")
				return
			}
			if found {
				defer deps.AgentFiles.remove(req.Nonce)
				defer staged.Close() // close before removing, including on Windows
				ciphertext = staged
			}
		}
		if ciphertext == nil {
			if deps.DownloadContent == nil {
				_ = client.AgentReportResult(ctx, req.ID, "failed", "the receiving device cannot download the attached file")
				return
			}
			remote, derr := deps.DownloadContent(ctx, req.ID)
			if derr != nil {
				deps.logf("agent-bridge: download file for %s: %v", req.ID, derr)
				_ = client.AgentReportResult(ctx, req.ID, "failed", "could not download the attached file")
				return
			}
			ciphertext = bytes.NewReader(remote)
		}
		ck, kerr := deps.OpenContentKey(req.SealedFileKey)
		if kerr != nil {
			_ = client.AgentReportResult(ctx, req.ID, "failed", "could not decrypt the attached file key")
			return
		}
		path, perr := placeInjectedFileFromReader(cwd, env.FileName, ciphertext, ck)
		if perr != nil {
			_ = client.AgentReportResult(ctx, req.ID, "failed", "could not write the attached file")
			return
		}
		filePath = path
		prompt = prompt + "\n\n(A file for this task was placed at " + path + ".)"
	}
	if env.Deliver == "inbox" {
		name := discovered.Name
		if name == "" {
			name = req.Tool
		}
		rt.notify("Share2Us", "A file arrived for agent "+name+" in "+cwd)
		_ = AppendHop(HopRecord{Time: time.Now().UTC(), RequestID: req.ID, Tool: req.Tool,
			From: req.SenderDeviceID, Target: req.TargetSessionID, Mode: "inbox", Status: "done", Prompt: env.Prompt})
		_ = client.AgentReportResult(ctx, req.ID, "done", filePath)
		return
	}
	if bound && binding.Paused {
		// The owner asked not to be disturbed: hold it (never headless) until resumed.
		rt.holdInject(ctx, client, runner, deps, req, env.Prompt, prompt, env.SenderDeviceName, cwd)
		return
	}
	if live {
		if discovered.Status == "available" && rt.hub().channelReady(req.TargetSessionID) {
			rt.deliverViaChannel(ctx, client, runner, deps, req, env.Prompt, prompt, env.SenderDeviceName, cwd, 0, false)
			return
		}
		if bound && rt.tryTypedInject(ctx, client, runner, deps, req, binding, discovered, env.Prompt, prompt, env.SenderDeviceName, cwd, 0) {
			return
		}
		rt.holdInject(ctx, client, runner, deps, req, env.Prompt, prompt, env.SenderDeviceName, cwd)
		return
	}
	rt.runInject(ctx, client, runner, deps, req, env.Prompt, prompt, env.SenderDeviceName, cwd, 0)
}

// paneRebindNeeded reports whether this hop is stuck because typed delivery is
// expected for the target session but its bound Zellij pane can no longer be
// resolved (the needs-rebind condition). It reuses the exact resolution the
// delivery path uses, so it agrees with why delivery keeps failing. False when
// typed delivery is not expected, the session is not live, or the pane resolves.
func (rt *Runtime) paneRebindNeeded(ctx context.Context, runner AgentRunner, req clicore.AgentRequest) bool {
	list, err := LoadBindings()
	if err != nil {
		return false
	}
	binding, ok := BindingForSession(list, req.TargetSessionID)
	if !ok || binding.Zellij == nil || binding.TypedDeliveryDisabled {
		return false
	}
	sessions, err := runner.Discover(ctx)
	if err != nil {
		return false
	}
	for _, session := range sessions {
		if session.SessionID != req.TargetSessionID || !session.Live {
			continue
		}
		z := rt.paneDriver()
		processPane := func(pid int) *ZellijPane {
			if current := rt.processZellijPane(pid); current != nil {
				return current
			}
			return TerminalHookPane(req.TargetSessionID, pid)
		}
		if _, perr := resolveZellijPaneWith(ctx, z, binding, session, processPane); perr != nil {
			return true
		}
		return false
	}
	return false
}

// holdMessages says what a held hop waits for, naming the condition that blocks typing it
// into the open window (owner report 2026-11-26: "it only works in the background" was a
// window started as plain claude, while the status said it waited for idle).
func holdMessages(req clicore.AgentRequest, pid int) (notice, status string) {
	tool := req.Tool
	exitNote := "It runs in that session once " + tool + " is exited there."
	list, err := LoadBindings()
	b, ok := BindingForSession(list, req.TargetSessionID)
	switch {
	case ok && b.Paused:
		return "A prompt for your " + tool + " session is held: you paused deliveries there (s2u agent resume).", pausedStatus
	case err != nil || !ok || b.Zellij == nil:
		return "A prompt is waiting for your " + tool + " session. " + exitNote,
			"Waiting: the " + tool + " session is open in a window. " + exitNote
	case b.TypedDeliveryDisabled:
		return "A prompt is waiting for your " + tool + " session: typing into it is paused (s2u agent typed on).",
			"Waiting: typing into the open " + tool + " window is paused there (`s2u agent typed on` turns it back on). " + exitNote
	case tool == "claude" && !TerminalTypedReady(req.TargetSessionID, pid):
		fix := "restart it there with `s2u claude --resume " + req.TargetSessionID + "`"
		return "A prompt is waiting: this Claude window wasn't started with s2u claude, so Share2Us can't type into it. To get prompts live, " + fix + ".",
			"Waiting: the claude window on that device wasn't started with `s2u claude`, so Share2Us can't type into it safely. " + exitNote + " For live delivery, " + fix + "."
	}
	return "A prompt is waiting for your " + tool + " session. Share2Us types it in when the window is idle.",
		"Waiting: the " + tool + " session is busy (or its input holds other text). Share2Us types the prompt in as soon as it is idle."
}

const pausedStatus = "Waiting: the target's owner paused deliveries to this agent (do not disturb). It is delivered when they resume."

// livePID is the process holding a session now, 0 when none is known.
func livePID(ctx context.Context, runner AgentRunner, session string) int {
	if found, err := runner.Discover(ctx); err == nil {
		for _, s := range found {
			if s.SessionID == session && s.Live {
				return s.PID
			}
		}
	}
	return 0
}

// holdInject waits, off the receive loop, until nothing holds the session, then
// runs the hop in it. The server already handed the hop over (it stays
// "delivered"), so it lives here until then. It gives up after injectHoldMax.
func (rt *Runtime) holdInject(ctx context.Context, client AgentClient, runner AgentRunner, deps Deps, req clicore.AgentRequest, shown, prompt, senderName, cwd string) {
	rt.holdInjectState(ctx, client, runner, deps, req, shown, prompt, senderName, cwd, true, false)
}

// holdInjectState carries two facts across channel fallback: whether this hop
// may still try the channel, and whether its waiting status was already sent.
// Once a channel picked up a hop but did not start it, that hop never enters the
// same channel again; doing so every poll can duplicate the prompt and notices.
func (rt *Runtime) holdInjectState(ctx context.Context, client AgentClient, runner AgentRunner, deps Deps, req clicore.AgentRequest, shown, prompt, senderName, cwd string, allowChannel, waitingReported bool) {
	if !waitingReported {
		deps.logf("agent-bridge: inject %s waits: session %s is open in a window", req.ID, req.TargetSessionID)
		notice, status := holdMessages(req, livePID(ctx, runner, req.TargetSessionID))
		if why := rt.takeTypedBlock(req.ID); why != "" && strings.Contains(status, "is busy") {
			status = "Waiting: " + why + ". Share2Us types it in as soon as it can; a busy window with an empty input gets it queued after " + typedQueueAfter.String() + "."
		}
		rt.notify("Share2Us", notice)
		_ = client.AgentReportResult(ctx, req.ID, "waiting", status)
	}
	rt.holding.Add(1)
	// The limit counts from when the hop was sent, so a restart does not reset it.
	began := time.Now()
	if t, err := time.Parse(time.RFC3339, req.CreatedAt); err == nil && t.Before(began) {
		began = t
	}
	// Tracks a stuck typed-delivery pane so we can tell the sender to re-bind
	// after injectPaneRebindTimeout, without abandoning the hop.
	var paneBlockedSince time.Time
	rebindReported := false
	lastBlock := ""
	if SessionPaused(req.TargetSessionID) {
		lastBlock = "paused" // holdMessages already said so
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
			if cancelledBySender(ctx, client, req.ID) {
				deps.logf("agent-bridge: waiting inject %s cancelled by its sender", req.ID)
				_ = AppendHop(HopRecord{Time: time.Now().UTC(), RequestID: req.ID, Tool: req.Tool, From: req.SenderDeviceID,
					Target: req.TargetSessionID, Mode: "waiting", Status: "cancelled"})
				return
			}
			if SessionPaused(req.TargetSessionID) {
				if lastBlock != "paused" {
					lastBlock = "paused"
					_ = client.AgentReportResult(ctx, req.ID, "waiting", pausedStatus)
				}
				continue
			}
			if lastBlock == "paused" {
				lastBlock = ""
				_ = client.AgentReportResult(ctx, req.ID, "waiting", "Waiting: its owner resumed deliveries; it is delivered next.")
			}
			// Prefer a proven channel; if the organisation blocks it, a verified
			// Zellij pane and hook proof provide the local fallback.
			if allowChannel && rt.hub().channelReady(req.TargetSessionID) && sessionAvailable(ctx, runner, req.TargetSessionID) {
				rt.deliverViaChannel(ctx, client, runner, deps, req, shown, prompt, senderName, cwd, time.Since(began), true)
				return
			}
			if list, err := LoadBindings(); err == nil {
				if binding, ok := BindingForSession(list, req.TargetSessionID); ok {
					if sessions, err := runner.Discover(ctx); err == nil {
						for _, session := range sessions {
							if session.SessionID == req.TargetSessionID && session.Live && rt.tryTypedInject(ctx, client, runner, deps, req, binding, session, shown, prompt, senderName, cwd, time.Since(began)) {
								return
							}
							if why := rt.takeTypedBlock(req.ID); why != "" && why != lastBlock {
								lastBlock = why
								_ = client.AgentReportResult(ctx, req.ID, "waiting", "Waiting: "+why+". Share2Us types it in as soon as it can.")
							}
						}
					}
				}
			}
			// Typed delivery is expected but its pane can't be verified: after a
			// short grace, tell the sender to re-bind. The hop keeps waiting, so a
			// re-bind recovers it on the next poll with no re-send.
			if rt.paneRebindNeeded(ctx, runner, req) {
				if paneBlockedSince.IsZero() {
					paneBlockedSince = time.Now()
				}
				if !rebindReported && time.Since(paneBlockedSince) > injectPaneRebindTimeout {
					rebindReported = true
					deps.logf("agent-bridge: inject %s needs re-bind: target %s bound pane unverifiable", req.ID, req.TargetSessionID)
					rt.notify("Share2Us", "A prompt can't reach your "+req.Tool+" session: re-bind it with `s2u agent bind`.")
					rt.openRebindWarningTab(ctx, req, deps)
					_ = client.AgentReportResult(ctx, req.ID, "waiting", AgentNeedsRebindMarker+"this agent's session cannot receive the prompt: its bound window could not be verified (its Zellij pane changed, or the session was not re-bound after a restart). Re-bind it by running `s2u agent bind "+req.TargetSessionID+"` in that session; the prompt then delivers on its own, no re-send needed. Any attached file is already in the session's inbox.")
				}
			} else {
				paneBlockedSince = time.Time{}
				rebindReported = false
			}
			if !sessionHeld(ctx, runner, req.TargetSessionID) {
				rt.runInject(ctx, client, runner, deps, req, shown, prompt, senderName, cwd, time.Since(began))
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

// channelPickup is how long a listening channel has to take a delivery before
// the hop falls back to waiting for the window.
var channelPickup = 30 * time.Second

// deliverViaChannel hands the hop to the Share2Us channel of the open session:
// the prompt appears in that window and runs there, under the window's own mode
// with the hop's guardrails enforced by the s2u PreToolUse hook. The result is
// what the agent reports with the channel's report tool. Presence and Stop
// are not request-specific and cannot confirm delivery. It runs off the receive loop.
func (rt *Runtime) deliverViaChannel(ctx context.Context, client AgentClient, runner AgentRunner, deps Deps, req clicore.AgentRequest, shown, prompt, senderName, cwd string, waited time.Duration, waitingReported bool) {
	// Recheck report readiness and availability immediately before queueing.
	if !rt.hub().channelReady(req.TargetSessionID) || !sessionAvailable(ctx, runner, req.TargetSessionID) {
		rt.holdInjectState(ctx, client, runner, deps, req, shown, prompt, senderName, cwd, true, waitingReported)
		return
	}
	rt.holding.Add(1) // busy until the agent reports: a restart would lose the result
	strict := false
	if cr, ok := runner.(ClaudeRunner); ok {
		strict = cr.Strict
	}
	picked, result, accepted := rt.hub().deliver(req.TargetSessionID, ChannelDelivery{RequestID: req.ID, Prompt: prompt, From: req.SenderDeviceID, Strict: strict})
	if !accepted {
		rt.holding.Add(-1)
		rt.holdInjectState(ctx, client, runner, deps, req, shown, prompt, senderName, cwd, true, waitingReported)
		return
	}
	go func() {
		defer rt.holding.Add(-1)
		select {
		case <-picked:
		case <-time.After(channelPickup):
			// The channel went away between the check and the pickup.
			rt.hub().withdraw(req.TargetSessionID, req.ID)
			deps.logf("agent-bridge: the channel of session %s did not take inject %s; waiting for the window instead", req.TargetSessionID, req.ID)
			rt.holdInjectState(ctx, client, runner, deps, req, shown, prompt, senderName, cwd, false, waitingReported)
			return
		case <-ctx.Done():
			return
		}
		// A report is tied to this request. Busy/Stop may belong to the owner.
		// Once picked up, it might be executing: retain the guard and never
		// redeliver on a short timeout, which could execute the task twice.
		if !waitingReported {
			_ = client.AgentReportResult(ctx, req.ID, "waiting", "Waiting for the open session to report this channel request.")
		}
		var out string
		select {
		case out = <-result:
		case <-time.After(time.Until(requestDeadline(req))):
			rt.hub().withdraw(req.TargetSessionID, req.ID)
			_ = client.AgentReportResult(ctx, req.ID, "failed", "The channel request was not confirmed by a report before its deadline.")
			return
		case <-ctx.Done():
			return
		}
		deps.logf("agent-bridge: delivered inject %s into the open session %s", req.ID, req.TargetSessionID)
		rt.notify("Share2Us", "A prompt was delivered into your open "+req.Tool+" session")
		_ = client.AgentReportResult(ctx, req.ID, "running", "Delivered through the Claude channel into the open "+req.Tool+" session.")
		hop := HopRecord{Time: time.Now().UTC(), RequestID: req.ID, Tool: req.Tool, From: req.SenderDeviceID,
			Target: req.TargetSessionID, RanIn: req.TargetSessionID, Mode: "delivered", Prompt: shown}
		if waited > 0 {
			hop.Waited = waited.Round(time.Second).String()
		}
		hop.Status = "done"
		_ = AppendHop(hop)
		if len(out) > maxReportedResult {
			out = out[:maxReportedResult]
		}
		_ = client.AgentReportResult(ctx, req.ID, "done", out)
	}()
}

func sessionAvailable(ctx context.Context, runner AgentRunner, sessionID string) bool {
	sessions, err := runner.Discover(ctx)
	if err != nil {
		return false
	}
	for _, session := range sessions {
		if session.SessionID == sessionID {
			return session.Status == "available"
		}
	}
	return false
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

// runInject runs the hop in the target session itself and reports the result: in its open
// `s2u codex` window when there is one, else headless.
func (rt *Runtime) runInject(ctx context.Context, client AgentClient, runner AgentRunner, deps Deps, req clicore.AgentRequest, shown, prompt, senderName, cwd string, waited time.Duration) {
	if req.Tool == "codex" && CodexLiveReady(req.TargetSessionID) > 0 {
		rt.startCodexLive(ctx, client, runner, deps, req, shown, prompt, senderName, cwd, waited)
		return
	}
	rt.runHeadless(ctx, client, runner, deps, req, shown, prompt, senderName, cwd, waited)
}

// runHeadless resumes the session without a window (codex exec resume, claude -p).
func (rt *Runtime) runHeadless(ctx context.Context, client AgentClient, runner AgentRunner, deps Deps, req clicore.AgentRequest, shown, prompt, senderName, cwd string, waited time.Duration) {
	rt.hopMu.Lock()
	defer rt.hopMu.Unlock()
	rt.notify("Share2Us", "Running a prompt in your "+req.Tool+" session")
	deps.logf("agent-bridge: running inject %s in session %s (cwd %s)", req.ID, req.TargetSessionID, cwd)
	if isCancelledErr(client.AgentReportResult(ctx, req.ID, "running", "Running headless in the "+req.Tool+" session (no window open).")) {
		deps.logf("agent-bridge: inject %s cancelled by its sender before it ran", req.ID)
		return
	}
	var out string
	var err error
	rt.hopRunning.Store(true)
	defer rt.hopRunning.Store(false)
	defer rt.runningHeadless(req.TargetSessionID, req.ID)()
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
			rt.holdInject(ctx, client, runner, deps, req, shown, prompt, senderName, cwd)
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
