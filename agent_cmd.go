// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/share2us/cli-core/daemonctl"
	"net/url"
	"os"
	"sort"
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
		return a.agentList(ctx, args[1:])
	case "pin":
		return a.agentPin(ctx, args[1:], true)
	case "unpin":
		return a.agentPin(ctx, args[1:], false)
	case "hide":
		return a.agentHide(ctx, args[1:], true)
	case "unhide", "show":
		return a.agentHide(ctx, args[1:], false)
	case "rename", "alias":
		return a.agentRename(ctx, args[1:])
	case "send", "inject":
		return a.agentSend(ctx, args[1:])
	case "status":
		return a.agentStatus(ctx, args[1:])
	case "cancel":
		return a.agentCancel(ctx, args[1:])
	case "results":
		return a.agentResults(ctx, args[1:])
	case "wait":
		return a.agentWait(ctx, args[1:])
	case "add":
		return a.agentAdd(ctx, args[1:])
	case "approvals":
		return a.agentApprovals()
	case "grants":
		return a.agentGrants(ctx, args[1:])
	case "pause", "disable", "resume", "enable":
		return a.agentPause(args[0] == "pause" || args[0] == "disable", args[1:])
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
	case "typed":
		return a.agentTyped(args[1:])
	case "bindings":
		return a.agentBindings(ctx)
	case "skill", "skills":
		if len(args) > 1 && args[1] == "--after-update" {
			a.skillUpdateHint()
			return 0
		}
		return a.agentSkill(args[1:])
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
		fmt.Fprintf(a.stderr, "agent invitations: add your own agents with `%s agent add <agent> <sharenet>[.<project>]`; invitations from others are in the portal: %s\n", commandName, portalSharenetsURL())
		return 2
	default:
		return a.agentUsage()
	}
}

func (a app) agentUsage() int {
	fmt.Fprintf(a.stderr, "usage: %s agent <list|pin|hide|rename|send|status|hops|pending|approve|allow|revoke|allowed|bind|unbind|typed|bindings|skill|goal|rules|policy>\n", commandName)
	fmt.Fprintf(a.stderr, "  list [--all] [--sharenets]                 reachable agent sessions across your devices\n                                             (--all includes hidden ones; --sharenets adds the\n                                             agents in your sharenet projects, as sharenet.agent)\n")
	fmt.Fprintf(a.stderr, "  pin|unpin <agent-id>                       pin an agent to the top of the list (or undo)\n")
	fmt.Fprintf(a.stderr, "  hide|unhide <agent-id>                     hide an agent from the default list (or undo)\n")
	fmt.Fprintf(a.stderr, "  rename <agent-id> [NAME]                   give an agent a local name (empty NAME clears it);\n                                             this is your label only, nobody else sees it\n")
	fmt.Fprintf(a.stderr, "  send --to NAME|ID --prompt P [--file PATH] [--goal ID]\n                                             NAME is the agent's name or alias, or its device's\n                                             name when that device has one agent (e.g. --to homelab)\n                                             inject a prompt (+ optional file). With --goal it\n                                             is a counted hop against that goal's budget.\n")
	fmt.Fprintf(a.stderr, "       --inbox --file PATH                   drop a file in the agent's inbox without running it\n")
	fmt.Fprintf(a.stderr, "       [--device ID] [--session ID]         or name the session instead; any id may be a\n                                             unique prefix, as `agent list` prints it\n")
	fmt.Fprintf(a.stderr, "       [--project ID [--as AGENT-ID]]       to an agent in another account: both agents must\n                                             be members of that project. The sending agent is\n                                             the one bound to this directory unless --as names it.\n")
	fmt.Fprintf(a.stderr, "  join <code> [--wait]                       join a sharenet project with THIS session (--wait:\n                                             until a host approves); type\n                                             !s2u agent join <code> in Claude Code or Codex\n")
	fmt.Fprintf(a.stderr, "  project <project-id>                       a project's reachable member agents\n")
	fmt.Fprintf(a.stderr, "  status <request-id> [--json]               status, timing and result of a sent request\n")
	fmt.Fprintf(a.stderr, "  approvals                                  where blocked actions are approved (the portal)\n")
	fmt.Fprintf(a.stderr, "  grants [revoke <id>]                       permissions your agents on this device hold\n")
	fmt.Fprintf(a.stderr, "  pause|resume [--project DIR]               do not disturb: hold incoming prompts for the agent\n                                             bound here (default: this folder) until resumed\n")
	fmt.Fprintf(a.stderr, "  wait <request-id> [--timeout 9m]           block until it finishes, then print the result\n                                             (also: send ... --wait)\n")
	fmt.Fprintf(a.stderr, "  hops                                       hops this machine ran, and the session each ran in\n")
	fmt.Fprintf(a.stderr, "  pending                                    requests awaiting your approval (this device)\n")
	fmt.Fprintf(a.stderr, "  approve <request-id>                       approve ONE pending request (no standing access)\n")
	fmt.Fprintf(a.stderr, "  allow <sender-device-id>                   ALWAYS-allow a device (standing access)\n")
	fmt.Fprintf(a.stderr, "  revoke <sender-device-id>                  withdraw a device's standing access\n")
	fmt.Fprintf(a.stderr, "  allowed                                    list devices with standing access\n")
	fmt.Fprintf(a.stderr, "  bind [<session-id> [PROJECT-NAME]]         let this machine advertise that session (nothing\n")
	fmt.Fprintf(a.stderr, "                                             is advertised until you bind it); with no id, the\n                                             session it is typed in (`! s2u agent bind`)\n")
	fmt.Fprintf(a.stderr, "  unbind <session-id|--project DIR>          stop advertising it\n")
	fmt.Fprintf(a.stderr, "  typed <on|off> [--project DIR]              allow or pause automatic Zellij typing for a Claude binding\n")
	fmt.Fprintf(a.stderr, "  bindings                                   what this machine advertises\n")
	fmt.Fprintf(a.stderr, "  skill [install|status] [claude|codex|gemini|--all]\n                                             install the /s2u skill (default: every agent tool\n                                             found here), so sessions can reach your other agents\n")
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
	var s daemon.DiscoveredSession
	pane := (*daemon.ZellijPane)(nil)
	if len(args) == 0 {
		// No id: bind the agent session this runs in (`! s2u agent bind` typed inside
		// Claude Code or Codex), found from the process tree like `agent join`.
		found, err := daemon.FindOwnSession(ctx)
		if err != nil {
			fmt.Fprintf(a.stderr, "Run it inside the agent session you want to make reachable (type `! s2u agent bind` there), or name it: %s agent bind <session-id> [PROJECT-NAME]\n", commandName)
			fmt.Fprintf(a.stderr, "  session ids come from `claude agents`, or from this machine's running codex/gemini sessions\n")
			return 2
		}
		s, pane = found, daemon.CurrentZellijPane()
	} else {
		found, ok := a.localSession(ctx, args[0])
		if !ok {
			fmt.Fprintf(a.stderr, "no single live session here matches %q; start it first, then bind it\n", args[0])
			return 1
		}
		s, pane = found, daemon.ProcessZellijPane(found.PID)
	}
	label := ""
	if len(args) > 1 {
		label = args[1]
	}
	b, created, err := daemon.BindSessionInPane(s.Project, s.Tool, label, s.SessionID, pane)
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
	if s.Tool == "claude" || b.Tool == "claude" {
		if path, err := installClaudeSkill(); err == nil {
			fmt.Fprintf(a.stdout, "Claude skill /s2u installed (%s): sessions know how to reach your other agents.\n", path)
		}
	}
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

func (a app) agentTyped(args []string) int {
	if len(args) != 1 && len(args) != 3 {
		fmt.Fprintf(a.stderr, "usage: %s agent typed <on|off> [--project DIR]\n", commandName)
		return 2
	}
	if args[0] != "on" && args[0] != "off" {
		fmt.Fprintf(a.stderr, "typed delivery must be on or off\n")
		return 2
	}
	project, err := os.Getwd()
	if err != nil {
		return a.fail("current directory", err)
	}
	if len(args) == 3 {
		if args[1] != "--project" || args[2] == "" {
			fmt.Fprintf(a.stderr, "usage: %s agent typed <on|off> [--project DIR]\n", commandName)
			return 2
		}
		project = args[2]
	}
	b, err := daemon.SetTypedDelivery(project, args[0] == "on")
	if err != nil {
		return a.fail("typed delivery setting", err)
	}
	fmt.Fprintf(a.stdout, "automatic Zellij typing %s for Claude binding %s (%s)\n", args[0], b.AgentID, b.Project)
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
		if b.Paused {
			where += "  [PAUSED: prompts held until `" + commandName + " agent resume`]"
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

func (a app) agentList(ctx context.Context, args []string) int {
	showAll := hasFlag(args, "--all", "-a")
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	sessions, err := client.ListAgentSessions(ctx)
	if err != nil {
		return a.fail("list agent sessions", err)
	}
	// Local prefs (alias/pin/hide) decorate and order the list; a missing config
	// is fine (everything is just unset).
	config, _ := clicore.LoadConfig()
	type row struct {
		s clicore.AgentSessionInfo
		p clicore.AgentPrefs
	}
	var rows []row
	hidden := 0
	for _, s := range sessions {
		p := config.AgentPref(s.AgentID)
		if p.Hidden && !showAll {
			hidden++
			continue
		}
		rows = append(rows, row{s, p})
	}
	// Pinned agents first, order otherwise preserved (stable).
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].p.Pinned && !rows[j].p.Pinned
	})
	if len(rows) == 0 {
		if hidden > 0 {
			fmt.Fprintf(a.stdout, "No visible agent sessions (%d hidden; run `%s agent list --all`).\n", hidden, commandName)
		} else {
			fmt.Fprintf(a.stdout, "No reachable agent sessions yet. On each machine, run `%s setup` (or `%s agent bind` inside the agent's session).\n", commandName, commandName)
		}
		if hasFlag(args, "--sharenets") {
			a.printSharenetAgents(ctx, client)
		}
		return 0
	}
	// Full ids only: everything printed here can be pasted into `agent send`.
	locations := a.localZellijLocations(ctx)
	for _, r := range rows {
		line := sessionLine(r.s, config.AgentDisplayName(r.s.AgentID, r.s.Name), r.p)
		if where := locations[r.s.SessionID]; where != "" {
			line += "  [" + where + "]"
		}
		fmt.Fprintln(a.stdout, line)
	}
	if hidden > 0 {
		fmt.Fprintf(a.stdout, "(%d hidden; run `%s agent list --all`)\n", hidden, commandName)
	}
	if hasFlag(args, "--sharenets") {
		a.printSharenetAgents(ctx, client)
	}
	return 0
}

// printSharenetAgents lists the member agents of the caller's sharenet projects, each with
// the address `agent send --to` takes: sharenet.agent.
func (a app) printSharenetAgents(ctx context.Context, client *clicore.Client) {
	nets, err := client.ListSharenets(ctx)
	if err != nil {
		fmt.Fprintf(a.stderr, "sharenets: %s\n", cliErrorMessage(err))
		return
	}
	config, _ := clicore.LoadConfig()
	shown := 0
	for _, n := range nets {
		projects, err := client.ListSharenetProjects(ctx, n.ID)
		if err != nil {
			continue
		}
		for _, p := range projects {
			members, err := client.ListProjectAgents(ctx, p.ID)
			if err != nil {
				continue // only visible from a device running a member agent of the project
			}
			for _, m := range members {
				name := config.AgentDisplayName(m.AgentID, m.Function)
				if name == "" {
					name = m.AgentID
				}
				if shown == 0 {
					fmt.Fprintln(a.stdout, "\nSharenet agents (send with --to <address>):")
				}
				owner := ""
				if m.Yours {
					owner = "  (yours)"
				}
				fmt.Fprintf(a.stdout, "  %-32s %-8s %-10s project %s%s\n", n.Name+"."+name, m.Tool, m.Status, p.Name, owner)
				shown++
			}
		}
	}
	if shown == 0 {
		fmt.Fprintln(a.stdout, "\nNo sharenet agents reachable from this device (bind an agent here that is a member of a sharenet project).")
	}
}

func (a app) agentSend(ctx context.Context, args []string) int {
	var deviceID, sessionID, agentID, prompt, tool, file, goalID, projectID, asAgent string
	var inbox bool
	var waitFor time.Duration
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
		case "--wait":
			waitFor = defaultAgentWait
		case "--inbox":
			// Deliver the file to the agent's inbox without running it (the agent
			// or its owner picks it up). Requires --file; the prompt is optional.
			inbox = true
		default:
			fmt.Fprintf(a.stderr, "unknown flag %q\n", args[i])
			return 2
		}
	}
	if agentID == "" && sessionID == "" {
		fmt.Fprintf(a.stderr, "usage: %s agent send --agent ID --prompt \"...\"   (or --session ID [--device ID])\n", commandName)
		return 2
	}
	if inbox && strings.TrimSpace(file) == "" {
		fmt.Fprintf(a.stderr, "--inbox delivers a file: pass --file PATH\n")
		return 2
	}
	if !inbox && strings.TrimSpace(prompt) == "" {
		fmt.Fprintf(a.stderr, "usage: %s agent send --agent ID --prompt \"...\" [--file PATH]   (or --inbox --file PATH to drop a file without running)\n", commandName)
		return 2
	}
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	// "sharenet.agent": find the project in that sharenet whose member agent matches.
	if projectID == "" && deviceID == "" && sessionID == "" {
		if pid, agent, ok, err := a.resolveSharenetAddress(ctx, client, agentID); err != nil {
			fmt.Fprintln(a.stderr, err)
			return 1
		} else if ok {
			projectID, agentID = pid, agent
		}
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
	// Ensure this device has a registered hop signing key, then hand the whole
	// send (seal, transport choice, sign, inject) to the shared cli-core path so
	// the CLI and the desktop app behave identically.
	if credential, cerr = ensureSigningKey(ctx, client, credential); cerr != nil {
		return a.fail("signing key", cerr)
	}
	res, err := client.SendAgentFile(ctx, credential, clicore.SendAgentFileParams{
		TargetDeviceID:       deviceID,
		TargetSessionID:      sessionID,
		Tool:                 tool,
		TargetPublicKey:      targetPub,
		TargetLANFingerprint: target.LANFingerprint,
		TargetAgentID:        target.AgentID,
		Prompt:               withReplyFooter(prompt, inbox),
		FilePath:             file,
		Inbox:                inbox,
		GoalID:               goalID,
		ProjectID:            projectID,
		SenderAgentID:        senderAgent,
		SenderDeviceName:     currentDeviceName(ctx, client, credential.DeviceSessionID),
		SenderSessionID:      sendingSession(senderAgent),
	})
	if err != nil {
		return a.fail("send", err)
	}
	if res.Busy {
		fmt.Fprintln(a.stderr, "warning: that session is busy; injecting now may cause unintended results, so it runs once the session is idle")
	}
	switch res.Status {
	case "pending":
		fmt.Fprintf(a.stdout, "Sent (%s). It waits for approval on the target device: there, run `%s agent pending` and `%s agent approve %s`. Track: %s agent status %s\n", res.RequestID, commandName, commandName, res.RequestID, commandName, res.RequestID)
	default:
		via := ""
		if res.Transport != "" {
			via = " via " + res.Transport
		}
		fmt.Fprintf(a.stdout, "Sent (%s)%s, queued for delivery. Track: %s agent status %s\n", res.RequestID, via, commandName, res.RequestID)
	}
	// The target picks the path when it delivers; it can't be known here.
	fmt.Fprintf(a.stdout, "Results: `%s agent results`. Cancel before it runs: %s agent cancel %s\n", commandName, commandName, res.RequestID)
	_, _ = daemonctl.Query("agent-sent") // the daemon watches closely for this result
	if waitFor > 0 {
		return a.waitForResult(ctx, client, res.RequestID, waitFor)
	}
	if note := staleDaemonNote(); note != "" {
		fmt.Fprintln(a.stderr, note)
	}
	return 0
}

// resolveSharenetAddress turns "sharenet.agent" (or "sharenet.project.agent") into a
// project id and an agent query. ok=false when the text before the first dot isn't one of
// the caller's sharenets: it is then an ordinary agent name.
func (a app) resolveSharenetAddress(ctx context.Context, client *clicore.Client, to string) (projectID, agent string, ok bool, err error) {
	parts := strings.Split(strings.TrimSpace(to), ".")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[len(parts)-1] == "" {
		return "", "", false, nil
	}
	nets, lerr := client.ListSharenets(ctx)
	if lerr != nil {
		return "", "", false, nil
	}
	var net *clicore.SharenetSummary
	for i := range nets {
		if strings.EqualFold(nets[i].Name, parts[0]) || nets[i].ID == parts[0] {
			net = &nets[i]
			break
		}
	}
	if net == nil {
		return "", "", false, nil
	}
	agent = parts[len(parts)-1]
	projects, perr := client.ListSharenetProjects(ctx, net.ID)
	if perr != nil {
		return "", "", true, fmt.Errorf("sharenet %q: %s", net.Name, cliErrorMessage(perr))
	}
	var hits []string
	for _, p := range projects {
		if len(parts) == 3 && !strings.EqualFold(p.Name, parts[1]) && p.ID != parts[1] {
			continue
		}
		members, merr := client.ListProjectAgents(ctx, p.ID)
		if merr != nil {
			continue // only a device running a member agent of the project may look
		}
		if _, terr := pickTarget(targetsFromProject(members), targetQuery{Agent: agent}); terr == nil {
			hits = append(hits, p.ID+" ("+p.Name+")")
			projectID = p.ID
		}
	}
	switch len(hits) {
	case 1:
		return projectID, agent, true, nil
	case 0:
		return "", "", true, fmt.Errorf("no reachable agent %q in sharenet %q (your agent here must be a member of one of its projects; see `%s agent list --sharenets`)", agent, net.Name, commandName)
	}
	return "", "", true, fmt.Errorf("agent %q is in several projects of sharenet %q: name one, as %s.<project>.%s (%s)", agent, net.Name, parts[0], agent, strings.Join(hits, ", "))
}

// agentPause: do-not-disturb for the agent(s) bound in a folder. Prompts are held, not
// dropped or run in the background, and delivered on resume.
func (a app) agentPause(pause bool, args []string) int {
	project := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--project" && i+1 < len(args) {
			project, i = args[i+1], i+1
			continue
		}
		fmt.Fprintf(a.stderr, "usage: %s agent pause|resume [--project DIR]\n", commandName)
		return 2
	}
	if project == "" {
		project, _ = os.Getwd()
	}
	changed, err := daemon.SetPaused(project, pause)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(a.stderr, "no agent is bound in %s (see `%s agent bindings`)\n", project, commandName)
		return 1
	}
	if err != nil {
		return a.fail("agent pause", err)
	}
	for _, b := range changed {
		if pause {
			fmt.Fprintf(a.stdout, "Paused %s agent %s: incoming prompts are held until `%s agent resume`.\n", b.Tool, b.AgentID, commandName)
		} else {
			fmt.Fprintf(a.stdout, "Resumed %s agent %s: held prompts are delivered now.\n", b.Tool, b.AgentID)
		}
	}
	return 0
}

var joinWaitPoll = 3 * time.Second

// waitForAdmission returns once agentID is a member of the project: a device sees a
// project's agents only when one of its own agents is in it.
func (a app) waitForAdmission(ctx context.Context, client *clicore.Client, projectID, agentID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if agents, err := client.ListProjectAgents(ctx, projectID); err == nil {
			for _, m := range agents {
				if m.AgentID == agentID {
					return true
				}
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(joinWaitPoll):
		}
	}
	return false
}

// defaultAgentWait stays under a coding agent's 10-minute shell limit.
const defaultAgentWait = 9 * time.Minute

// agentWait blocks until a sent request reaches a final state: an agent in a plain shell
// gets the reply the moment it exists instead of sleeping and polling (2026-11-26).
func (a app) agentWait(ctx context.Context, args []string) int {
	var id string
	timeout := defaultAgentWait
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--timeout" && i+1 < len(args):
			d, err := time.ParseDuration(args[i+1])
			if err != nil || d <= 0 {
				fmt.Fprintf(a.stderr, "bad --timeout %q (e.g. 90s, 5m)\n", args[i+1])
				return 2
			}
			timeout, i = d, i+1
		case id == "" && !strings.HasPrefix(args[i], "-"):
			id = args[i]
		default:
			fmt.Fprintf(a.stderr, "usage: %s agent wait <request-id> [--timeout 9m]\n", commandName)
			return 2
		}
	}
	if id == "" {
		fmt.Fprintf(a.stderr, "usage: %s agent wait <request-id> [--timeout 9m]\n", commandName)
		return 2
	}
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	return a.waitForResult(ctx, client, id, timeout)
}

var agentWaitPoll = time.Second

func agentFinal(status string) bool {
	switch status {
	case "done", "failed", "rejected", "cancelled", "expired":
		return true
	}
	return false
}

// waitForResult prints progress to stderr and the final state + result to stdout. Exit 0
// for done, 1 for any other final state, 124 when the wait ran out first.
func (a app) waitForResult(ctx context.Context, client *clicore.Client, id string, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	last := ""
	errs := 0
	for {
		st, err := client.AgentInjectStatus(ctx, id)
		switch {
		case err == nil:
			errs = 0
			if agentFinal(st.Status) {
				fmt.Fprintf(a.stdout, "status: %s\n", st.Status)
				if t := requestTiming(st); t != "" {
					fmt.Fprintln(a.stdout, t)
				}
				if r := strings.TrimSpace(daemon.CleanRemoteText(st.Result)); r != "" {
					fmt.Fprintln(a.stdout, r)
				}
				if st.Status == "done" {
					return 0
				}
				return 1
			}
			if line := st.Status + ": " + strings.TrimSpace(daemon.CleanRemoteText(st.Result)); line != last {
				fmt.Fprintf(a.stderr, "... %s\n", strings.TrimSuffix(line, ": "))
				last = line
			}
		case isServerError(err) && errs < 10:
			errs++ // a proxy hiccup: keep waiting
		default:
			return a.fail("agent wait failed", err)
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(a.stderr, "still %s after %s; keep waiting with: %s agent wait %s\n", st.Status, timeout, commandName, id)
			return 124
		}
		select {
		case <-ctx.Done():
			return 130
		case <-time.After(agentWaitPoll):
		}
	}
}

// replyFooter goes with every prompt, so the receiving agent knows how its answer gets back
// even when its own Share2Us is older (owner, 2026-11-26).
const replyFooter = "When you're done, end your turn with a short reply for the sender: Share2Us returns your final message to them automatically (or call the share2us `report` tool with the request id if you have it). Don't ask your user to relay it."

func withReplyFooter(prompt string, inbox bool) string {
	if inbox || strings.TrimSpace(prompt) == "" || strings.Contains(prompt, replyFooter) {
		return prompt
	}
	return strings.TrimRight(prompt, "\n") + "\n\n" + replyFooter
}

// staleDaemonNote warns when the running daemon is another build than this CLI: an old
// daemon doesn't collect results or push them to the sending session.
func staleDaemonNote() string {
	resp, ok := daemonctl.Query("status")
	if !ok || !resp.OK {
		return "note: the Share2Us daemon is not running, so you won't be notified when this finishes. Start it: " + commandName + " daemon start"
	}
	if resp.Version != "" && resp.Version != clicore.FullVersion() {
		return fmt.Sprintf("note: the running daemon is %s but this CLI is %s; restart it to get result notices: %s daemon restart", resp.Version, clicore.FullVersion(), commandName)
	}
	return ""
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

// sessionLine is one `agent list` row: agent id FIRST (so it still pastes into
// `agent send`), then the display name (the local alias when set, else the
// server name), the full --device and --session, and any local [pinned]/[hidden]
// tags at the end so they never disturb the pasteable fields.
func sessionLine(s clicore.AgentSessionInfo, displayName string, p clicore.AgentPrefs) string {
	agent := s.AgentID
	if agent == "" {
		agent = "-"
	}
	line := fmt.Sprintf("%s  %-8s  %-11s  %s  (on %s)  --device %s --session %s",
		agent, s.Tool, s.Status, displayName, s.DeviceName, s.DeviceID, s.SessionID)
	var tags []string
	if p.Pinned {
		tags = append(tags, "pinned")
	}
	if p.Hidden {
		tags = append(tags, "hidden")
	}
	if len(tags) > 0 {
		line += "  [" + strings.Join(tags, ",") + "]"
	}
	return line
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
	waitJoin := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--wait":
			waitJoin = true
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
		fmt.Fprintf(a.stdout, "Asked to join %q in %q. A host approves it (portal or CLI); they see your email (%s).\n", res.ProjectName, res.SharenetName, credential.Email)
		fmt.Fprintln(a.stdout, "Once approved, you are a member of the sharenet and this agent is in the project.")
		fmt.Fprintln(a.stdout, "If nobody approves it within 24 hours the request expires; ask the host for a new code then.")
		if waitJoin && res.ProjectID != "" {
			if a.waitForAdmission(ctx, client, res.ProjectID, binding.AgentID, defaultAgentWait) {
				fmt.Fprintf(a.stdout, "✓ Approved: this agent is in %q now.\n", res.ProjectName)
			} else {
				fmt.Fprintf(a.stdout, "Still waiting for a host after %s; you'll get a notification when it's decided.\n", defaultAgentWait)
			}
		} else if res.ProjectID != "" {
			fmt.Fprintf(a.stdout, "Wait here until it's approved: %s agent join %s --wait (or watch your notifications)\n", commandName, code)
		}
	}
	// The agent can only receive work while the daemon runs: make sure it does.
	a.ensureAgentReachable()
	a.channelHint(sess)
	return 0
}

func (a app) agentStatus(ctx context.Context, args []string) int {
	asJSON := hasFlag(args, "--json")
	var ids []string
	for _, x := range args {
		if x != "--json" {
			ids = append(ids, x)
		}
	}
	if len(ids) != 1 {
		fmt.Fprintf(a.stderr, "usage: %s agent status <request-id> [--json]\n", commandName)
		return 2
	}
	args = ids
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	// A 5xx from the API (or its proxy) is a hiccup, not a request state: retry briefly,
	// then report it on stderr with a non-zero exit. stdout only ever holds states.
	var st clicore.AgentInjectState
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if st, err = client.AgentInjectStatus(ctx, args[0]); err == nil || !isServerError(err) {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
	if err != nil {
		// Not "status:", which is how a request's state is printed.
		return a.fail("agent status failed", err)
	}
	if asJSON {
		writeJSON(a.stdout, st)
		return 0
	}
	// A waiting hop whose target session cannot be verified carries a re-bind
	// marker; surface it as an action, not a bare status line.
	if reason, needsRebind := strings.CutPrefix(st.Result, daemon.AgentNeedsRebindMarker); needsRebind {
		fmt.Fprintf(a.stdout, "status: %s  (action needed: re-bind the target session)\n", st.Status)
		fmt.Fprintf(a.stdout, "%s\n", reason)
		return 0
	}
	fmt.Fprintf(a.stdout, "status: %s\n", st.Status)
	if t := requestTiming(st); t != "" {
		fmt.Fprintln(a.stdout, t)
	}
	if strings.TrimSpace(st.Result) != "" {
		fmt.Fprintf(a.stdout, "%s\n", daemon.CleanRemoteText(st.Result))
	}
	return 0
}

// requestTiming: "sent 20:58:35, delivered after 3s, finished after 24s". "" from an
// older server that sends no times.
func requestTiming(st clicore.AgentInjectState) string {
	sent, err := time.Parse(time.RFC3339, st.CreatedAt)
	if err != nil {
		return ""
	}
	parts := []string{"sent " + sent.Local().Format("15:04:05")}
	if d, err := time.Parse(time.RFC3339, st.DeliveredAt); err == nil {
		parts = append(parts, "delivered after "+d.Sub(sent).Round(time.Second).String())
	}
	if c, err := time.Parse(time.RFC3339, st.CompletedAt); err == nil {
		parts = append(parts, "finished after "+c.Sub(sent).Round(time.Second).String())
	}
	return strings.Join(parts, ", ")
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
	fmt.Fprintf(a.stdout, "Allowed. (Your own signed-in devices already trust each other, so this is only needed for a device the server doesn't count as active.)\n")
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
		var apiErr *clicore.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			// Approval happens on the device the request was sent to. Say so instead of a
			// bare not_found when it exists but is waiting somewhere else.
			if st, serr := client.AgentInjectStatus(ctx, args[0]); serr == nil && st.Status != "" {
				where := "the device it was sent to"
				if st.TargetDeviceName != "" {
					where = fmt.Sprintf("device %q", st.TargetDeviceName)
				}
				fmt.Fprintf(a.stderr, "approve: request %s is %s and waits on %s, not this one. Approve it there (run `%s agent pending` on that device).\n", args[0], st.Status, where, commandName)
				return 1
			}
		}
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
	fmt.Fprintln(a.stdout, "Revoked. Note: your own signed-in devices trust each other anyway; to cut a device off, sign it out (`share2us signout <device>`).")
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

func isServerError(err error) bool {
	var apiErr *clicore.APIError
	return errors.As(err, &apiErr) && apiErr.Status >= 500
}

// sendingSession is the agent session this send comes from (the bound session of the
// sending agent, or of this directory), so the outcome can be routed back to it.
func sendingSession(senderAgent string) string {
	list, err := daemon.LoadBindings()
	if err != nil {
		return ""
	}
	agent := strings.TrimSpace(senderAgent)
	if agent == "" {
		agent = strings.TrimSpace(os.Getenv(daemon.AgentEnvVar))
	}
	if agent == "" {
		cwd, _ := os.Getwd()
		agent, _ = daemon.AgentIDForProject(list, cwd)
	}
	for _, b := range list {
		if agent != "" && b.AgentID == agent {
			return b.SessionID
		}
	}
	return ""
}

// agentCancel withdraws a request this device sent that hasn't started running.
func (a app) agentCancel(ctx context.Context, args []string) int {
	if len(args) != 1 {
		fmt.Fprintf(a.stderr, "usage: %s agent cancel <request-id>\n", commandName)
		return 2
	}
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	if err := client.AgentCancel(ctx, args[0]); err != nil {
		var apiErr *clicore.APIError
		if errors.As(err, &apiErr) && apiErr.Code == "not_cancellable" {
			fmt.Fprintf(a.stderr, "cancel: too late, %s\n", strings.TrimPrefix(apiErr.Message, "the request is "))
			return 1
		}
		return a.fail("cancel", err)
	}
	fmt.Fprintln(a.stdout, "Cancelled. The target will not run it.")
	return 0
}

// agentResults lists the outcomes of requests this device sent, newest last.
func (a app) agentResults(ctx context.Context, args []string) int {
	asJSON := len(args) > 0 && args[0] == "--json"
	// Collect anything the daemon hasn't (it may be stopped, or an older build without
	// the collector). Best effort: offline, the local log is still shown.
	if client, _, ok := a.authClient(); ok {
		if updates, err := client.AgentUpdates(ctx); err == nil && len(updates) > 0 {
			if rerr := daemon.RecordSentUpdates(updates); rerr != nil {
				fmt.Fprintf(a.stderr, "warning: could not save %d result(s) locally: %v\n", len(updates), rerr)
			}
		}
	}
	results, err := daemon.LoadSentResults(50)
	if err != nil {
		return a.fail("results", err)
	}
	if asJSON {
		writeJSON(a.stdout, results)
		return 0
	}
	if len(results) == 0 {
		fmt.Fprintln(a.stdout, "No finished requests yet.")
		return 0
	}
	for _, r := range results {
		fmt.Fprintf(a.stdout, "%s  %-9s  %s  -> %s %s\n", r.Time.Local().Format("2006-01-02 15:04"), r.Status, r.RequestID, r.Tool, shorten(r.TargetSession))
		if res := strings.TrimSpace(daemon.CleanRemoteText(r.Result)); res != "" {
			for _, line := range strings.Split(res, "\n") {
				fmt.Fprintf(a.stdout, "    %s\n", line)
			}
		}
	}
	return 0
}
