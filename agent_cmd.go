package main

import (
	"context"
	"fmt"
	"strings"

	clicore "github.com/share2us/cli-core"
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
	case "allow":
		return a.agentAllow(ctx, args[1:])
	case "rules":
		return a.agentRules(args[1:])
	default:
		return a.agentUsage()
	}
}

func (a app) agentUsage() int {
	fmt.Fprintf(a.stderr, "usage: %s agent <list|send|status|pending|allow>\n", commandName)
	fmt.Fprintf(a.stderr, "  list                                       reachable agent sessions across your devices\n")
	fmt.Fprintf(a.stderr, "  send --device ID --session ID --prompt P   inject a prompt into a session (--tool claude)\n")
	fmt.Fprintf(a.stderr, "  status <request-id>                        status/result of a sent request\n")
	fmt.Fprintf(a.stderr, "  pending                                    requests awaiting your approval (this device)\n")
	fmt.Fprintf(a.stderr, "  allow <sender-device-id>                   always-allow a device to inject into this one\n")
	fmt.Fprintf(a.stderr, "  rules [--project DIR]                      show which .s2u.rules are hard-enforced vs advisory\n")
	return 2
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
	var deviceID, sessionID, prompt, tool string
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
	sealed, err := clicore.SealForDevice([]byte(prompt), targetPub)
	if err != nil {
		return a.fail("seal prompt", err)
	}
	res, err := client.AgentInject(ctx, clicore.AgentInjectInput{
		TargetDeviceID:  deviceID,
		TargetSessionID: sessionID,
		Tool:            tool,
		SealedPrompt:    sealed,
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
		fmt.Fprintf(a.stdout, "%s  from device %s  -> %s session %s   (approve: %s agent allow %s)\n",
			shorten(q.ID), shorten(q.SenderDeviceID), q.Tool, shorten(q.TargetSessionID), commandName, q.SenderDeviceID)
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
	fmt.Fprintln(a.stdout, "Allowed. That device's requests will now inject without per-request approval.")
	return 0
}

func shorten(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
