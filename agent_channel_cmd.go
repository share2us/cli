// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"

	"github.com/share2us/cli-core/daemonctl"

	"github.com/share2us/cli/internal/daemon"
)

// agentChannel is `s2u agent channel`: the MCP channel server Claude starts in
// a session launched with `s2u claude`. Not meant to be run by hand.
func (a app) agentChannel(ctx context.Context) int {
	srv := &daemon.ChannelServer{
		FindSession: func(ctx context.Context) (string, error) {
			s, err := daemon.FindOwnSession(ctx)
			if err != nil {
				return "", err
			}
			return s.SessionID, nil
		},
		Call: daemonctl.Call,
		Logf: func(format string, args ...any) { fmt.Fprintf(a.stderr, format+"\n", args...) },
	}
	if err := srv.Serve(ctx, os.Stdin, a.stdout); err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintf(a.stderr, "share2us channel: %v\n", err)
		return 1
	}
	return 0
}

// hookInput is the part of a Claude hook's stdin the guardrail uses.
type hookInput struct {
	SessionID string         `json:"session_id"`
	CWD       string         `json:"cwd"`
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
}

// agentHook is `s2u agent hook <event>`, run by Claude in an `s2u claude`
// session. session-start proves the guard settings loaded; pre-tool-use applies
// a delivered hop's guardrails while one is in progress; stop tells the daemon
// the agent's turn ended. It
// never fails a turn the user started: anything unexpected allows the call.
func (a app) agentHook(args []string) int {
	if len(args) != 1 {
		fmt.Fprintf(a.stderr, "usage: %s agent hook <session-start|pre-tool-use|stop>\n", commandName)
		return 2
	}
	var in hookInput
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 8<<20)).Decode(&in); err != nil || in.SessionID == "" {
		return 0
	}
	switch args[0] {
	case "session-start":
		daemonctl.Call(daemonctl.Request{Op: "channel-guard-ready", Args: map[string]string{"session": in.SessionID}})
	case "pre-tool-use":
		resp, ok := daemonctl.Call(daemonctl.Request{Op: "channel-guarded", Args: map[string]string{"session": in.SessionID}})
		if !ok || !resp.OK {
			return 0 // no delivered hop in progress: the user's own turn
		}
		var g struct {
			Strict bool `json:"strict"`
		}
		_ = json.Unmarshal(resp.Data, &g)
		if deny, reason := daemon.HookDecision(in.CWD, in.ToolName, in.ToolInput, g.Strict); deny {
			out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
				"hookEventName":            "PreToolUse",
				"permissionDecision":       "deny",
				"permissionDecisionReason": reason,
			}})
			fmt.Fprintln(a.stdout, string(out))
		}
	case "stop":
		daemonctl.Call(daemonctl.Request{Op: "channel-turn-ended", Args: map[string]string{"session": in.SessionID}})
	}
	return 0
}

// claude is `s2u claude [claude args...]`: start Claude Code with the share2us
// channel and the guardrail hooks, so prompts sent to this session's agent
// appear in this window instead of waiting for it to close.
func (a app) claude(ctx context.Context, args []string) int {
	exe, err := os.Executable()
	if err != nil {
		return a.fail("find this program", err)
	}
	mcp, settings, err := daemon.WriteChannelLaunchConfig(exe)
	if err != nil {
		return a.fail("write the channel config", err)
	}
	cmd := exec.CommandContext(ctx, "claude", append(daemon.ClaudeChannelArgs(mcp, settings), args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// Ctrl-C belongs to Claude; this wrapper only waits for it.
	signal.Ignore(os.Interrupt)
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		if errors.Is(err, exec.ErrNotFound) {
			fmt.Fprintln(a.stderr, "Claude Code is not installed (no `claude` on PATH).")
			return 1
		}
		return a.fail("run claude", err)
	}
	return 0
}

// channelHint is what `agent join` prints for a Claude session that has no
// share2us channel: prompts then wait until the window closes.
func (a app) channelHint(sess daemon.DiscoveredSession) {
	if sess.Tool != "claude" {
		return
	}
	if resp, ok := daemonctl.Call(daemonctl.Request{Op: "channel-alive", Args: map[string]string{"session": sess.SessionID}}); ok && resp.OK {
		fmt.Fprintln(a.stdout, "Prompts sent to this agent appear in this window.")
		return
	}
	fmt.Fprintln(a.stdout, "Prompts sent to this agent wait while this window is open, and run in this session once you exit Claude.")
	fmt.Fprintln(a.stdout, "To get them in this window instead, start Claude with Share2Us next time:")
	fmt.Fprintf(a.stdout, "  s2u claude --resume %s\n", sess.SessionID)
	if exe, err := os.Executable(); err == nil {
		if mcp, settings, err := daemon.WriteChannelLaunchConfig(exe); err == nil {
			fmt.Fprintln(a.stdout, "or, the same with claude itself:")
			fmt.Fprintf(a.stdout, "  claude %s --resume %s\n", shellJoin(daemon.ClaudeChannelArgs(mcp, settings)), sess.SessionID)
		}
	}
	fmt.Fprintln(a.stdout, "(That loads the share2us channel, a Claude Code research preview. On a Team or Enterprise plan an admin must allow channels first.)")
}

// shellJoin quotes args that need it, for a line to paste into a shell.
func shellJoin(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		if a == "" || strings.ContainsAny(a, " \t'\"$`\\") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		out += a
	}
	return out
}
