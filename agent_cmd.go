// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli/internal/daemon"
)

// agent is the user-facing surface of the agent-session bridge (ADR-036): list
// reachable sessions, send a prompt to one, and (target side) see/approve
// incoming requests. Discovery + injection are server-mediated; this drives the
// /v1/agent/* endpoints via the authenticated device client.
func (a app) agent(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return a.agentUsage()
	}
	switch args[0] {
	case "list", "ls":
		return a.agentList(ctx)
	case "send", "inject":
		return a.agentSend(ctx, args[1:])
	case "status":
		return a.agentStatus(ctx, args[1:])
	case "pending":
		return a.agentPending(ctx)
	case "approve":
		return a.agentApprove(ctx, args[1:])
	case "allow":
		return a.agentAllow(ctx, args[1:])
	case "revoke":
		return a.agentRevoke(ctx, args[1:])
	case "allowed":
		return a.agentAllowed(ctx)
	case "rules":
		return a.agentRules(args[1:])
	case "policy":
		return a.agentPolicy(args[1:])
	case "bind":
		return a.agentBind(ctx, args[1:])
	case "unbind":
		return a.agentUnbind(args[1:])
	case "bindings":
		return a.agentBindings(ctx)
	case "goal", "goals":
		return a.agentGoal(ctx, args[1:])
	case "project":
		return a.agentProject(ctx, args[1:])
	case "hops":
		return a.agentHops()
	case "join":
		return a.agentJoin(ctx, args[1:])
	case "channel":
		return a.agentChannel(ctx)
	case "hook":
		return a.agentHook(args[1:])
	case "invites", "withdraw":
		// Sharenet membership (inviting, accepting, removing) is managed in the
		// portal; the CLI binds sessions and sends between agents.
		fmt.Fprintf(a.stderr, "agent invitations and memberships are managed in the portal: %s\n", portalSharenetsURL())
		return 2
	default:
		return a.agentUsage()
	}
}

func (a app) agentUsage() int {
	fmt.Fprintf(a.stderr, "usage: %s agent <list|send|status|hops|pending|approve|allow|revoke|allowed|bind|unbind|bindings|goal|rules|policy>\n", commandName)
	fmt.Fprintf(a.stderr, "  list                                       reachable agent sessions across your devices\n")
	fmt.Fprintf(a.stderr, "  send --agent ID --prompt P [--file PATH] [--goal ID]\n                                             inject a prompt (+ optional file). With --goal it\n                                             is a counted hop against that goal's budget.\n")
	fmt.Fprintf(a.stderr, "       [--device ID] [--session ID]         or name the session instead; any id may be a\n                                             unique prefix, as `agent list` prints it\n")
	fmt.Fprintf(a.stderr, "       [--project ID [--as AGENT-ID]]       to an agent in another account: both agents must\n                                             be members of that project. The sending agent is\n                                             the one bound to this directory unless --as names it.\n")
	fmt.Fprintf(a.stderr, "  join <code>                                join a sharenet project with THIS session: type\n                                             !s2u agent join <code> in Claude Code or Codex\n")
	fmt.Fprintf(a.stderr, "  project <project-id>                       a project's reachable member agents\n")
	fmt.Fprintf(a.stderr, "  status <request-id>                        status/result of a sent request\n")
	fmt.Fprintf(a.stderr, "  hops                                       hops this machine ran, and the session each ran in\n")
	fmt.Fprintf(a.stderr, "  pending                                    requests awaiting your approval (this device)\n")
	fmt.Fprintf(a.stderr, "  approve <request-id>                       approve ONE pending request (no standing access)\n")
	fmt.Fprintf(a.stderr, "  allow <sender-device-id>                   ALWAYS-allow a device (standing access)\n")
	fmt.Fprintf(a.stderr, "  revoke <sender-device-id>                  withdraw a device's standing access\n")
	fmt.Fprintf(a.stderr, "  allowed                                    list devices with standing access\n")
	fmt.Fprintf(a.stderr, "  bind <session-id> [PROJECT-NAME]           let this machine advertise that session (nothing\n")
	fmt.Fprintf(a.stderr, "                                             is advertised until you bind it)\n")
	fmt.Fprintf(a.stderr, "  unbind <session-id|--project DIR>          stop advertising it\n")
	fmt.Fprintf(a.stderr, "  bindings                                   what this machine advertises\n")
	fmt.Fprintf(a.stderr, "  goal <new|list|show|close|wait>            a unit of autonomous work, with a budget\n")
	fmt.Fprintf(a.stderr, "  rules [--project DIR]                      show which .s2u.rules are hard-enforced vs advisory\n")
	fmt.Fprintf(a.stderr, "  policy [--project DIR] [LEVEL]             show or set this agent's privilege\n")
	fmt.Fprintf(a.stderr, "                                             (restricted | standard | privileged)\n")
	return 2
}

// printJoinCandidates lists the live sessions in the current folder with the
// exact line that joins each, for when the session could not be found from the
// process tree (Codex on Windows has no way to tell which session file a process
// holds).
func (a app) printJoinCandidates(ctx context.Context, code string) {
	cwd, _ := os.Getwd()
	var here []daemon.DiscoveredSession
	for _, r := range []daemon.AgentRunner{daemon.ClaudeRunner{}, daemon.CodexRunner{}, daemon.GeminiRunner{}} {
		found, err := r.Discover(ctx)
		if err != nil {
			continue
		}
		for _, s := range found {
			if daemon.SameProject(s.Project, cwd) {
				here = append(here, s)
			}
		}
	}
	if len(here) == 0 {
		return
	}
	fmt.Fprintln(a.stderr, "Or name the session. Live sessions in this folder:")
	for _, s := range here {
		fmt.Fprintf(a.stderr, "  %-6s %s  (%s)\n    s2u agent join %s --session %s\n", s.Tool, s.SessionID, s.Name, code, s.SessionID)
	}
}

// localSession finds a live session on THIS machine by id (or unique prefix),
// across every tool adapter. Binding is a local act: it decides what this
// machine is willing to advertise, so it must not require the server.
func (a app) localSession(ctx context.Context, id string) (daemon.DiscoveredSession, bool) {
	var matches []daemon.DiscoveredSession
	for _, r := range []daemon.AgentRunner{daemon.ClaudeRunner{}, daemon.CodexRunner{}, daemon.GeminiRunner{}} {
		found, err := r.Discover(ctx)
		if err != nil {
			continue
		}
		for _, s := range found {
			if s.SessionID == id || (len(id) >= 6 && strings.HasPrefix(s.SessionID, id)) {
				matches = append(matches, s)
			}
		}
	}
	if len(matches) != 1 {
		return daemon.DiscoveredSession{}, false
	}
	return matches[0], true
}

// agentBind whitelists a session's project + tool so the daemon may advertise it
// (ADR-041 §1). Nothing is advertised until this runs: before bindings, starting
// the bridge published every session on the machine, including unrelated work.
func (a app) agentBind(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(a.stderr, "usage: %s agent bind <session-id> [PROJECT-NAME]\n", commandName)
		fmt.Fprintf(a.stderr, "  session ids come from `claude agents`, or from this machine's running codex/gemini sessions\n")
		return 2
	}
	s, ok := a.localSession(ctx, args[0])
	if !ok {
		fmt.Fprintf(a.stderr, "no single live session here matches %q; start it first, then bind it\n", args[0])
		return 1
	}
	label := ""
	if len(args) > 1 {
		label = args[1]
	}
	b, created, err := daemon.BindSessionInPane(s.Project, s.Tool, label, s.SessionID, daemon.ProcessZellijPane(s.PID))
	if err != nil {
		return a.fail("bind", err)
	}
	verb := "already bound"
	if created {
		verb = "bound"
	}
	fmt.Fprintf(a.stdout, "%s: %s session %s in %s (only this session is advertised)\n", verb, b.Tool, s.SessionID, b.Project)
	// The id is what another owner invites into their project, so it is shown —
	// it names the agent, and grants nothing on its own.
	fmt.Fprintf(a.stdout, "agent id: %s\n", b.AgentID)
	if b.Label != "" {
		fmt.Fprintf(a.stdout, "project name: %s\n", b.Label)
	}
	fmt.Fprintf(a.stdout, "privilege: %s (change with `%s agent policy --project %s <level>`)\n",
		daemon.AgentPolicy(b.Project, false), commandName, b.Project)
	a.printZellijDelivery(ctx, b, s)
	// A bound session is only reachable while the daemon runs: make sure it does.
	a.ensureAgentReachable()
	a.channelHint(s)
	return 0
}

// agentUnbind stops this machine advertising a project. The daemon retires the
// session on its next pass.
func (a app) agentUnbind(args []string) int {
	project, tool := "", ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--project" && i+1 < len(args):
			i++
			project = args[i]
		case args[i] == "--tool" && i+1 < len(args):
			i++
			tool = args[i]
		case strings.HasPrefix(args[i], "-"):
			fmt.Fprintf(a.stderr, "unknown flag %q\n", args[i])
			return 2
		default:
			if s, ok := a.localSession(context.Background(), args[i]); ok {
				project, tool = s.Project, s.Tool
			} else {
				fmt.Fprintf(a.stderr, "no single live session matches %q; use --project DIR instead\n", args[i])
				return 1
			}
		}
	}
	if project == "" {
		fmt.Fprintf(a.stderr, "usage: %s agent unbind <session-id> | --project DIR [--tool claude|codex|gemini]\n", commandName)
		return 2
	}
	n, err := daemon.Unbind(project, tool)
	if err != nil {
		return a.fail("unbind", err)
	}
	if n == 0 {
		fmt.Fprintln(a.stdout, "nothing was bound for that project")
		return 0
	}
	fmt.Fprintf(a.stdout, "unbound %d binding(s); the daemon retires those sessions on its next pass\n", n)
	return 0
}

func (a app) agentBindings(ctx context.Context) int {
	list, err := daemon.LoadBindings()
	if err != nil {
		return a.fail("read bindings", err)
	}
	if len(list) == 0 {
		fmt.Fprintln(a.stdout, "Nothing is bound, so this machine advertises no agent sessions.")
		fmt.Fprintf(a.stdout, "Start a session, then: %s agent bind <session-id> [PROJECT-NAME]\n", commandName)
		return 0
	}
	locations := a.localZellijLocations(ctx)
	for _, b := range list {
		label := b.Label
		if label == "" {
			label = "-"
		}
		id := b.AgentID
		if id == "" {
			// Made before agent ids existed; `agent bind` on it again assigns one.
			id = "(none - re-bind)"
		}
		where := ""
		if current := locations[b.SessionID]; current != "" {
			where = "  [" + current + "]"
		} else if b.Zellij != nil {
			where = "  [zellij pane " + b.Zellij.Pane + "]"
		}
		fmt.Fprintf(a.stdout, "%-8s  %-12s  %-10s  %-26s  %s%s\n", b.Tool, label, daemon.AgentPolicy(b.Project, false), id, b.Project, where)
	}
	return 0
}

func (a app) printZellijDelivery(ctx context.Context, b daemon.Binding, session daemon.DiscoveredSession) {
	if b.Zellij == nil {
		return
	}
	if location, ok := daemon.FindZellijLocation(ctx, b, session); ok {
		fmt.Fprintf(a.stdout, "zellij: tab %q, pane terminal_%s (revalidated before every delivery)\n", location.TabName, location.Pane)
		return
	}
	fmt.Fprintf(a.stdout, "zellij: recorded pane terminal_%s; it will be revalidated before any delivery\n", b.Zellij.Pane)
}

func (a app) localZellijLocations(ctx context.Context) map[string]string {
	out := map[string]string{}
	bindings, err := daemon.LoadBindings()
	if err != nil {
		return out
	}
	bySession := map[string]daemon.Binding{}
	for _, binding := range bindings {
		if binding.SessionID != "" && binding.Zellij != nil {
			bySession[binding.SessionID] = binding
		}
	}
	if len(bySession) == 0 {
		return out
	}
	sessions, err := (daemon.ClaudeRunner{}).Discover(ctx)
	if err != nil {
		return out
	}
	for _, session := range sessions {
		binding, ok := bySession[session.SessionID]
		if !ok {
			continue
		}
		if location, ok := daemon.FindZellijLocation(ctx, binding, session); ok {
			out[session.SessionID] = fmt.Sprintf("zellij tab %q pane terminal_%s", location.TabName, location.Pane)
		}
	}
	return out
}

func (a app) agentClient() (*clicore.Client, bool) {
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
	}
	return client, ok
}

func (a app) agentList(ctx context.Context) int {
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	sessions, err := client.ListAgentSessions(ctx)
	if err != nil {
		return a.fail("list agent sessions", err)
	}
	if len(sessions) == 0 {
		fmt.Fprintln(a.stdout, "No reachable agent sessions. Start the daemon with --agent-bridge on your other devices.")
		return 0
	}
	// Full ids only: everything printed here can be pasted into `agent send`.
	locations := a.localZellijLocations(ctx)
	for _, s := range sessions {
		line := sessionLine(s)
		if where := locations[s.SessionID]; where != "" {
			line += "  [" + where + "]"
		}
		fmt.Fprintln(a.stdout, line)
	}
	return 0
}

func (a app) agentSend(ctx context.Context, args []string) int {
	var deviceID, sessionID, agentID, prompt, tool, file, goalID, projectID, asAgent string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent", "--to":
			i++
			if i < len(args) {
				agentID = args[i]
			}
		case "--device":
			i++
			if i < len(args) {
				deviceID = args[i]
			}
		case "--session":
			i++
			if i < len(args) {
				sessionID = args[i]
			}
		case "--prompt":
			i++
			if i < len(args) {
				prompt = args[i]
			}
		case "--tool":
			i++
			if i < len(args) {
				tool = args[i]
			}
		case "--file":
			i++
			if i < len(args) {
				file = args[i]
			}
		case "--goal":
			i++
			if i < len(args) {
				goalID = args[i]
			}
		case "--project":
			i++
			if i < len(args) {
				projectID = args[i]
			}
		case "--as":
			i++
			if i < len(args) {
				asAgent = args[i]
			}
		default:
			fmt.Fprintf(a.stderr, "unknown flag %q\n", args[i])
			return 2
		}
	}
	if (agentID == "" && sessionID == "") || strings.TrimSpace(prompt) == "" {
		fmt.Fprintf(a.stderr, "usage: %s agent send --agent ID --prompt \"...\"   (or --session ID [--device ID])\n", commandName)
		return 2
	}
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	// A project hop names the sending agent: the one bound to this directory,
	// unless --as says otherwise. The server checks it really runs on this device.
	senderAgent := strings.TrimSpace(asAgent)
	if projectID != "" && senderAgent == "" {
		list, _ := daemon.LoadBindings()
		cwd, _ := os.Getwd()
		id, ok := daemon.AgentIDForProject(list, cwd)
		if !ok {
			fmt.Fprintf(a.stderr, "no bound agent for this directory; run this from the agent's project, or pass --as AGENT-ID (see `%s agent bindings`)\n", commandName)
			return 2
		}
		senderAgent = id
	}
	// E2E (ADR-036 P4): find the target session in the directory, take its device
	// public key, and seal the prompt to it so the server relays ciphertext only.
	// A project hop looks in the PROJECT's directory, which is where another
	// account's agents appear; your own sessions list never shows them.
	targets, err := a.reachableTargets(ctx, client, projectID)
	if err != nil {
		return a.fail("resolve target", err)
	}
	query := targetQuery{Agent: agentID, Device: deviceID, Session: sessionID}
	target, perr := pickTarget(targets, query)
	if perr != nil {
		note := ""
		if errors.Is(perr, errNoTarget) {
			note = a.offlineNoteFor(ctx, client, projectID, query)
		}
		if note != "" {
			fmt.Fprintln(a.stderr, note)
		} else {
			fmt.Fprintln(a.stderr, perr)
		}
		if projectID != "" {
			fmt.Fprintf(a.stderr, "see `%s agent project %s`\n", commandName, projectID)
		} else {
			fmt.Fprintf(a.stderr, "see `%s agent list`\n", commandName)
		}
		return 1
	}
	deviceID, sessionID = target.DeviceID, target.SessionID
	targetPub := target.PublicKey
	if tool == "" {
		tool = target.Tool
	}
	if tool == "" {
		tool = "claude"
	}
	if targetPub == "" {
		fmt.Fprintln(a.stderr, "target device has no encryption key; cannot inject (end-to-end encryption required)")
		return 1
	}
	credential, cerr := clicore.LoadCredential()
	if cerr != nil {
		return a.fail("load login", cerr)
	}
	// A file rides along end-to-end: a fresh content key encrypts it, the ciphertext
	// goes to R2 (object_key), and the content key is sealed to the target device.
	// The display name stays inside that encrypted, signed envelope. The relay
	// server cannot rewrite it after sealing, and older receivers ignore it.
	env := daemon.InjectEnvelope{Prompt: prompt, SenderDeviceName: currentDeviceName(ctx, client, credential.DeviceSessionID)}
	var objectKey, sealedFileKey string
	if file != "" {
		data, rerr := os.ReadFile(file)
		if rerr != nil {
			return a.fail("read file", rerr)
		}
		ck, kerr := clicore.NewContentKey()
		if kerr != nil {
			return a.fail("content key", kerr)
		}
		var enc bytes.Buffer
		if eerr := clicore.EncryptStream(&enc, bytes.NewReader(data), ck); eerr != nil {
			return a.fail("encrypt file", eerr)
		}
		objectKey, err = client.AgentUploadContent(ctx, enc.Bytes())
		if err != nil {
			return a.fail("upload file", err)
		}
		sealedFileKey, err = clicore.SealContentKeyForDevice(ck, targetPub)
		if err != nil {
			return a.fail("seal file key", err)
		}
		env.FileName = filepath.Base(file)
	}
	envBytes, _ := json.Marshal(env)
	sealed, err := clicore.SealForDevice(envBytes, targetPub)
	if err != nil {
		return a.fail("seal prompt", err)
	}
	injectIn := clicore.AgentInjectInput{
		TargetDeviceID:  deviceID,
		TargetSessionID: sessionID,
		Tool:            tool,
		SealedPrompt:    sealed,
		ObjectKey:       objectKey,
		SealedFileKey:   sealedFileKey,
		GoalID:          goalID,
		ProjectID:       strings.TrimSpace(projectID),
		SenderAgentID:   senderAgent,
		TargetAgentID:   target.AgentID,
	}
	// Sign the hop (ADR-041 §5), so the server can refuse a forgery and — the part
	// that matters — the receiving machine can check it came from this device even
	// if the server lies.
	if credential, cerr = ensureSigningKey(ctx, client, credential); cerr != nil {
		return a.fail("signing key", cerr)
	}
	if serr := signHop(&injectIn, credential, time.Now()); serr != nil {
		return a.fail("sign hop", serr)
	}
	res, err := client.AgentInject(ctx, injectIn)
	if err != nil {
		return a.fail("send", err)
	}
	if res.Busy {
		fmt.Fprintln(a.stderr, "warning: that session is busy — injecting now may cause unintended results; it will run once the session is idle")
	}
	switch res.Status {
	case "pending":
		fmt.Fprintf(a.stdout, "Sent (%s). Waiting for approval on the target device. Track: %s agent status %s\n", res.ID, commandName, res.ID)
	default:
		fmt.Fprintf(a.stdout, "Sent (%s), queued for delivery. Track: %s agent status %s\n", res.ID, commandName, res.ID)
	}
	return 0
}

func currentDeviceName(ctx context.Context, client *clicore.Client, deviceID string) string {
	devices, err := client.ListDevices(ctx)
	if err != nil {
		return ""
	}
	return deviceNameForID(devices.Sessions, deviceID)
}

func deviceNameForID(devices []clicore.DeviceSession, deviceID string) string {
	for _, device := range devices {
		if (deviceID != "" && device.ID == deviceID) || (deviceID == "" && device.Current) {
			return strings.TrimSpace(device.DeviceName)
		}
	}
	return ""
}

// sessionLine is one `agent list` row: agent id first, then the full --device
// and --session, so any part of it pastes into `agent send`.
func sessionLine(s clicore.AgentSessionInfo) string {
	agent := s.AgentID
	if agent == "" {
		agent = "-"
	}
	return fmt.Sprintf("%s  %-8s  %-11s  %s  (on %s)  --device %s --session %s",
		agent, s.Tool, s.Status, s.Name, s.DeviceName, s.DeviceID, s.SessionID)
}

// reachableTargets is the directory a send picks from: the project's member
// agents for a project hop (where another account's agents appear), your own
// sessions otherwise.
func (a app) reachableTargets(ctx context.Context, client *clicore.Client, projectID string) ([]agentTarget, error) {
	if projectID != "" {
		agents, err := client.ListProjectAgents(ctx, projectID)
		if err != nil {
			return nil, err
		}
		return targetsFromProject(agents), nil
	}
	sessions, err := client.ListAgentSessions(ctx)
	if err != nil {
		return nil, err
	}
	return targetsFromSessions(sessions), nil
}

// offlineNoteFor asks the directory again, this time with offline sessions, to
// tell an agent that is offline from one that does not exist. Best effort: any
// error just keeps the plain "no such agent" answer.
func (a app) offlineNoteFor(ctx context.Context, client *clicore.Client, projectID string, q targetQuery) string {
	var listed []agentTarget
	if projectID != "" {
		agents, err := client.ListProjectAgentsIncludingOffline(ctx, projectID)
		if err != nil {
			return ""
		}
		listed = targetsFromProject(agents)
	} else {
		sessions, err := client.ListAgentSessionsIncludingOffline(ctx)
		if err != nil {
			return ""
		}
		listed = targetsFromSessions(sessions)
	}
	return offlineNote(listed, q, time.Now())
}

// agentHops shows the hops this machine ran, newest first, with the session each
// ran in (always the bound one) and how long it waited for an open window.
func (a app) agentHops() int {
	hops, err := daemon.LoadHops(30)
	if err != nil {
		return a.fail("read hop log", err)
	}
	if len(hops) == 0 {
		fmt.Fprintln(a.stdout, "No hops have run on this machine.")
		return 0
	}
	for i := len(hops) - 1; i >= 0; i-- {
		h := hops[i]
		where := h.Mode
		if h.Waited != "" {
			where += " after waiting " + h.Waited
		}
		if h.Mode == "typed" && h.RanIn != "" {
			where += "  session " + h.RanIn
		} else if h.Tool == "claude" && h.RanIn != "" {
			where += "  claude --resume " + h.RanIn
		} else if h.RanIn != "" {
			where += "  session " + h.RanIn
		}
		fmt.Fprintf(a.stdout, "%s  %-6s  %s\n    %q\n", h.Time.Local().Format("2006-01-02 15:04"), h.Status, where, h.Prompt)
	}
	return 0
}

// agentProject lists a project's reachable member agents, with what `agent send
// --project` needs to address them.
func (a app) agentProject(ctx context.Context, args []string) int {
	if len(args) != 1 {
		fmt.Fprintf(a.stderr, "usage: %s agent project <project-id>\n", commandName)
		return 2
	}
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	agents, err := client.ListProjectAgents(ctx, args[0])
	if err != nil {
		return a.fail("list project agents", err)
	}
	if len(agents) == 0 {
		fmt.Fprintln(a.stdout, "No member agents are online in that project.")
		return 0
	}
	for _, ag := range agents {
		whose := "theirs"
		if ag.Yours {
			whose = "yours"
		}
		fn := ag.Function
		if fn == "" {
			fn = "-"
		}
		fmt.Fprintf(a.stdout, "%s  %-10s  %-8s  %-11s  %-6s  --device %s --session %s\n",
			ag.AgentID, fn, ag.Tool, ag.Status, whose, ag.DeviceID, ag.SessionID)
	}
	return 0
}

// portalSharenetsURL is where sharenets, invitations and agent memberships are
// managed (owner, 2026-09-26: the portal only), for the server this CLI is
// logged in to.
func portalSharenetsURL() string {
	apiBase := ""
	if c, err := clicore.LoadCredential(); err == nil {
		apiBase = c.APIBase
	}
	return portalURLFor(apiBase) + "/sharenets"
}

// portalURLFor maps an API base to its portal: https://api.X -> https://portal.X.
// Anything else (a bare host, localhost, no login) falls back to production.
func portalURLFor(apiBase string) string {
	u, err := url.Parse(strings.TrimSpace(apiBase))
	if err != nil || u.Scheme != "https" || !strings.HasPrefix(u.Host, "api.") {
		return "https://portal.share2.us"
	}
	return "https://portal." + strings.TrimPrefix(u.Host, "api.")
}

// agentJoin redeems a sharenet join code for the session it runs inside
// (phase-7 §10, owner 2026-09-27). Typed as `!s2u agent join <code>` in a Claude
// session, it finds that session through the process tree, binds it (the CLI's
// job), registers it so the server can see this device runs it, and files a join
// request that a host approves in the portal. This is the CLI's one exception to
// portal-only sharenet management: it binds and asks; it decides nothing.
func (a app) agentJoin(ctx context.Context, args []string) int {
	code, sessionArg := "", ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session":
			i++
			if i < len(args) {
				sessionArg = strings.TrimSpace(args[i])
			}
		default:
			if code == "" {
				code = strings.TrimSpace(args[i])
			} else {
				code = ""
				i = len(args)
			}
		}
	}
	if code == "" {
		fmt.Fprintf(a.stderr, "usage: %s agent join <code> [--session ID]   (type it inside the agent session: !s2u agent join <code>)\n", commandName)
		return 2
	}
	var sess daemon.DiscoveredSession
	if sessionArg != "" {
		// Named explicitly: where the session cannot be found from the process
		// tree (Codex on Windows, owner 2026-09-29), or to pick one on purpose.
		found, ok := a.localSession(ctx, sessionArg)
		if !ok {
			fmt.Fprintf(a.stderr, "No single live session here matches %q.\n", sessionArg)
			a.printJoinCandidates(ctx, code)
			return 1
		}
		sess = found
	} else {
		found, err := daemon.FindOwnSession(ctx)
		if err != nil {
			// The line people paste is s2u (the installed alias), whatever this binary is called.
			fmt.Fprintf(a.stderr, "Could not find the agent session this is running in. Paste it into the Claude Code or Codex session you want to add:\n  !s2u agent join %s\n", code)
			a.printJoinCandidates(ctx, code)
			return 1
		}
		sess = found
	}
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	credential, err := clicore.LoadCredential()
	if err != nil {
		return a.fail("load login", err)
	}
	// Bind: this machine may now advertise this session, under a stable agent id.
	// Only THIS session is bound (owner, 2026-09-27): other sessions in the same
	// folder stay private.
	pane := daemon.ProcessZellijPane(sess.PID)
	if sessionArg == "" {
		// The join command is a child of the chosen agent, so its inherited
		// zellij identifiers are stronger than a second process lookup.
		pane = daemon.CurrentZellijPane()
	}
	binding, _, err := daemon.BindSessionInPane(sess.Project, sess.Tool, sess.Name, sess.SessionID, pane)
	if err != nil {
		return a.fail("bind session", err)
	}
	// Register it now rather than waiting for the daemon, so the server can check
	// that this device runs the agent that is asking.
	status := sess.Status
	if status == "" || status == "idle" {
		status = "available"
	}
	if err := client.RegisterAgentSession(ctx, clicore.AgentRegisterInput{
		AgentID: binding.AgentID, SessionID: sess.SessionID, Tool: sess.Tool,
		Name: sess.Name, Project: sess.Project, Status: status,
	}); err != nil {
		return a.fail("register session", err)
	}
	if _, err := ensureSigningKey(ctx, client, credential); err != nil {
		return a.fail("signing key", err)
	}
	res, err := client.AgentJoin(ctx, code, binding.AgentID)
	if err != nil {
		return a.fail("join", err)
	}
	fmt.Fprintf(a.stdout, "Bound this %s session (agent %s) in %s.\n", sess.Tool, binding.AgentID, sess.Project)
	a.printZellijDelivery(ctx, binding, sess)
	switch res.Status {
	case "admitted":
		fmt.Fprintf(a.stdout, "Joined %q in %q. Other agents in the project can now reach this one.\n", res.ProjectName, res.SharenetName)
	default:
		fmt.Fprintf(a.stdout, "Asked to join %q in %q. A host approves it in the portal; they see your email (%s).\n", res.ProjectName, res.SharenetName, credential.Email)
		fmt.Fprintln(a.stdout, "Once approved, you are a member of the sharenet and this agent is in the project.")
		fmt.Fprintln(a.stdout, "If nobody approves it within 60 minutes the request expires; ask the host for a new code then.")
	}
	// The agent can only receive work while the daemon runs: make sure it does.
	a.ensureAgentReachable()
	a.channelHint(sess)
	return 0
}

func (a app) agentStatus(ctx context.Context, args []string) int {
	if len(args) != 1 {
		fmt.Fprintf(a.stderr, "usage: %s agent status <request-id>\n", commandName)
		return 2
	}
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	st, err := client.AgentInjectStatus(ctx, args[0])
	if err != nil {
		return a.fail("status", err)
	}
	fmt.Fprintf(a.stdout, "status: %s\n", st.Status)
	if strings.TrimSpace(st.Result) != "" {
		fmt.Fprintf(a.stdout, "%s\n", st.Result)
	}
	return 0
}

func (a app) agentPending(ctx context.Context) int {
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	reqs, err := client.AgentPending(ctx)
	if err != nil {
		return a.fail("pending", err)
	}
	if len(reqs) == 0 {
		fmt.Fprintln(a.stdout, "No requests awaiting approval.")
		return 0
	}
	for _, q := range reqs {
		fmt.Fprintf(a.stdout, "%s  from device %s  -> %s session %s\n",
			shorten(q.ID), shorten(q.SenderDeviceID), q.Tool, shorten(q.TargetSessionID))
		fmt.Fprintf(a.stdout, "    just this one : %s agent approve %s\n", commandName, q.ID)
		fmt.Fprintf(a.stdout, "    always allow  : %s agent allow %s   (standing access until revoked)\n", commandName, q.SenderDeviceID)
	}
	return 0
}

func (a app) agentAllow(ctx context.Context, args []string) int {
	if len(args) != 1 {
		fmt.Fprintf(a.stderr, "usage: %s agent allow <sender-device-id>\n", commandName)
		return 2
	}
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	if err := client.AgentAllow(ctx, args[0]); err != nil {
		return a.fail("allow", err)
	}
	fmt.Fprintf(a.stdout, "Allowed. That device now has STANDING access — its prompts inject without further approval.\n")
	fmt.Fprintf(a.stdout, "Withdraw it any time: %s agent revoke %s\n", commandName, args[0])
	return 0
}

func shorten(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// agentApprove approves ONE pending request without granting standing access.
func (a app) agentApprove(ctx context.Context, args []string) int {
	if len(args) != 1 {
		fmt.Fprintf(a.stderr, "usage: %s agent approve <request-id>\n", commandName)
		return 2
	}
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	if err := client.AgentApproveOnce(ctx, args[0]); err != nil {
		return a.fail("approve", err)
	}
	fmt.Fprintln(a.stdout, "Approved this request only. The sender was NOT given standing access.")
	return 0
}

// agentRevoke withdraws a sender device's standing access to this device.
func (a app) agentRevoke(ctx context.Context, args []string) int {
	if len(args) != 1 {
		fmt.Fprintf(a.stderr, "usage: %s agent revoke <sender-device-id>\n", commandName)
		return 2
	}
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	if err := client.AgentRevoke(ctx, args[0]); err != nil {
		return a.fail("revoke", err)
	}
	fmt.Fprintln(a.stdout, "Revoked. That device can no longer inject without a fresh approval.")
	return 0
}

// agentAllowed lists devices holding standing access to this device.
func (a app) agentAllowed(ctx context.Context) int {
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	grants, err := client.AgentAllowed(ctx)
	if err != nil {
		return a.fail("list grants", err)
	}
	if len(grants) == 0 {
		fmt.Fprintln(a.stdout, "No devices have standing access to this one.")
		return 0
	}
	for _, g := range grants {
		fmt.Fprintf(a.stdout, "%s  since %s   (revoke: %s agent revoke %s)\n",
			g.SenderDeviceID, g.ApprovedAt, commandName, g.SenderDeviceID)
	}
	return 0
}
