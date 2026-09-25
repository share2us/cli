// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
		return a.agentBindings()
	case "goal", "goals":
		return a.agentGoal(ctx, args[1:])
	default:
		return a.agentUsage()
	}
}

func (a app) agentUsage() int {
	fmt.Fprintf(a.stderr, "usage: %s agent <list|send|status|pending|approve|allow|revoke|allowed|bind|unbind|bindings|goal|rules|policy>\n", commandName)
	fmt.Fprintf(a.stderr, "  list                                       reachable agent sessions across your devices\n")
	fmt.Fprintf(a.stderr, "  send --device ID --session ID --prompt P [--file PATH] [--goal ID]\n                                             inject a prompt (+ optional file). With --goal it\n                                             is a counted hop against that goal's budget.\n")
	fmt.Fprintf(a.stderr, "  status <request-id>                        status/result of a sent request\n")
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
	b, created, err := daemon.Bind(s.Project, s.Tool, label)
	if err != nil {
		return a.fail("bind", err)
	}
	verb := "already bound"
	if created {
		verb = "bound"
	}
	fmt.Fprintf(a.stdout, "%s: %s sessions in %s\n", verb, b.Tool, b.Project)
	if b.Label != "" {
		fmt.Fprintf(a.stdout, "project name: %s\n", b.Label)
	}
	fmt.Fprintf(a.stdout, "privilege: %s (change with `%s agent policy --project %s <level>`)\n",
		daemon.AgentPolicy(b.Project, false), commandName, b.Project)
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

func (a app) agentBindings() int {
	list, err := daemon.LoadBindings()
	if err != nil {
		return a.fail("read bindings", err)
	}
	if len(list) == 0 {
		fmt.Fprintln(a.stdout, "Nothing is bound, so this machine advertises no agent sessions.")
		fmt.Fprintf(a.stdout, "Start a session, then: %s agent bind <session-id> [PROJECT-NAME]\n", commandName)
		return 0
	}
	for _, b := range list {
		label := b.Label
		if label == "" {
			label = "-"
		}
		fmt.Fprintf(a.stdout, "%-8s  %-12s  %-10s  %s\n", b.Tool, label, daemon.AgentPolicy(b.Project, false), b.Project)
	}
	return 0
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
	for _, s := range sessions {
		fmt.Fprintf(a.stdout, "%s  %-8s  %-6s  %s  (device %s / %s, session %s)\n",
			s.DeviceName, s.Tool, s.Status, s.Name, shorten(s.DeviceID), s.DeviceName, shorten(s.SessionID))
	}
	return 0
}

func (a app) agentSend(ctx context.Context, args []string) int {
	var deviceID, sessionID, prompt, tool, file, goalID string
	tool = "claude"
	for i := 0; i < len(args); i++ {
		switch args[i] {
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
		default:
			fmt.Fprintf(a.stderr, "unknown flag %q\n", args[i])
			return 2
		}
	}
	if deviceID == "" || sessionID == "" || strings.TrimSpace(prompt) == "" {
		fmt.Fprintf(a.stderr, "usage: %s agent send --device ID --session ID --prompt \"...\"\n", commandName)
		return 2
	}
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	// E2E (ADR-036 P4): find the target session in the directory, take its device
	// public key, and seal the prompt to it so the server relays ciphertext only.
	sessions, err := client.ListAgentSessions(ctx)
	if err != nil {
		return a.fail("resolve target", err)
	}
	targetPub := ""
	found := false
	for _, s := range sessions {
		if s.DeviceID == deviceID && s.SessionID == sessionID {
			targetPub, found = s.DevicePublicKey, true
			break
		}
	}
	if !found {
		fmt.Fprintln(a.stderr, "no such reachable session; run `"+commandName+" agent list`")
		return 1
	}
	if targetPub == "" {
		fmt.Fprintln(a.stderr, "target device has no encryption key; cannot inject (end-to-end encryption required)")
		return 1
	}
	// A file rides along end-to-end: a fresh content key encrypts it, the ciphertext
	// goes to R2 (object_key), and the content key is sealed to the target device.
	env := daemon.InjectEnvelope{Prompt: prompt}
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
	res, err := client.AgentInject(ctx, clicore.AgentInjectInput{
		TargetDeviceID:  deviceID,
		TargetSessionID: sessionID,
		Tool:            tool,
		SealedPrompt:    sealed,
		ObjectKey:       objectKey,
		SealedFileKey:   sealedFileKey,
		GoalID:          goalID,
	})
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
