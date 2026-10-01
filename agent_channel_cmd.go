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
	"time"

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
	SessionID      string         `json:"session_id"`
	PromptID       string         `json:"prompt_id"`
	Prompt         string         `json:"prompt"`
	TranscriptPath string         `json:"transcript_path"`
	CWD            string         `json:"cwd"`
	ToolName       string         `json:"tool_name"`
	ToolUseID      string         `json:"tool_use_id"`
	ToolInput      map[string]any `json:"tool_input"`
}

// agentHook is `s2u agent hook <event>`, run by Claude in an `s2u claude`
// session. SessionStart proves the guard settings loaded; PreToolUse applies a
// delivered hop's guardrails; Stop verifies the request before ending the hop.
// A daemon failure during a typed hop fails closed using its on-disk marker.
func (a app) agentHook(args []string) int {
	if len(args) != 1 {
		fmt.Fprintf(a.stderr, "usage: %s agent hook <session-start|user-prompt-submit|pre-tool-use|stop|session-end>\n", commandName)
		return 2
	}
	var in hookInput
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 8<<20)).Decode(&in); err != nil || in.SessionID == "" {
		if args[0] == "headless-pre-tool-use" {
			fmt.Fprintln(a.stdout, hookDeny("Share2Us: the headless Git guard could not identify this tool call."))
		}
		return 0
	}
	if args[0] == "headless-pre-tool-use" {
		if deny, reason := daemon.HeadlessGitDecision(os.Getenv("S2U_HEADLESS_GUARD_PROJECT"), in.ToolName, in.ToolInput); deny {
			fmt.Fprintln(a.stdout, hookDeny(reason))
		}
		return 0
	}
	switch args[0] {
	case "session-start":
		a.proveTerminalHook(in.SessionID)
		markChannelGuardReady(in.SessionID, daemonctl.Call, time.Sleep)
	case "user-prompt-submit":
		// A just-started Claude process may not appear in `claude agents` at
		// SessionStart yet. This later hook gives the guard one more chance.
		ensureChannelGuardReady(in.SessionID, daemonctl.Call)
		if in.PromptID != "" {
			a.proveTerminalPromptHook(in.SessionID)
		}
		_ = daemon.BindTypedPromptID(in.SessionID, in.Prompt, in.PromptID)
	case "pre-tool-use":
		activeRequest := daemon.TypedGuardRequest(in.SessionID)
		if activeRequest != "" {
			bound := daemon.TypedGuardPromptID(in.SessionID)
			if bound != "" && in.PromptID == "" {
				fmt.Fprintln(a.stdout, hookDeny("Share2Us: the guarded turn has no prompt identity; tool use is blocked."))
				return 0
			}
			if bound != "" && in.PromptID != bound &&
				daemon.TranscriptToolIsLaterOwnerTurn(in.TranscriptPath, activeRequest, in.ToolUseID) {
				a.abandonTypedTurn(in.SessionID, activeRequest)
				activeRequest = "" // a different turn; still check for an active channel hop
			}
		}
		resp, ok := daemonctl.Call(daemonctl.Request{Op: "channel-guarded", Args: map[string]string{"session": in.SessionID}})
		if !ok || !resp.OK {
			if activeRequest != "" {
				fmt.Fprintln(a.stdout, hookDeny("Share2Us: the guarded delivered turn cannot verify its policy; tool use is blocked until the turn ends."))
			}
			return 0
		}
		var g struct {
			Strict bool `json:"strict"`
		}
		_ = json.Unmarshal(resp.Data, &g)
		if deny, reason := daemon.HookDecision(in.CWD, in.ToolName, in.ToolInput, g.Strict); deny {
			fmt.Fprintln(a.stdout, hookDeny(reason))
		}
	case "stop":
		a.proveTerminalHook(in.SessionID)
		request := daemon.TypedGuardRequest(in.SessionID)
		if request != "" {
			if bound := daemon.TypedGuardPromptID(in.SessionID); bound != "" {
				if in.PromptID != bound {
					if in.PromptID != "" {
						a.abandonTypedTurn(in.SessionID, request)
					}
					request = ""
				}
			} else {
				matches, known := daemon.TranscriptRequestState(in.TranscriptPath, request)
				if known && !matches {
					a.abandonTypedTurn(in.SessionID, request)
				}
				if !matches {
					request = ""
				}
			}
		}
		completeHookTurn(in.SessionID, request, daemonctl.Call)
	case "session-end":
		a.endTerminalSession(in.SessionID)
	}
	return 0
}

// Claude may invoke SessionStart before its process appears in `claude agents`.
// Only an acknowledged registration proves this hook to the daemon.
func markChannelGuardReady(session string, call daemon.ChannelCaller, wait func(time.Duration)) bool {
	for attempt := 0; attempt < 3; attempt++ {
		resp, ok := call(daemonctl.Request{Op: "channel-guard-ready", Args: map[string]string{"session": session}})
		if ok && resp.OK {
			return true
		}
		if wait == nil {
			break
		}
		if attempt < 2 {
			wait(time.Duration(attempt+1) * 500 * time.Millisecond)
		}
	}
	return false
}

// Most owner prompts need no process discovery. Register again only if the
// daemon missed SessionStart or restarted since this Claude session began.
func ensureChannelGuardReady(session string, call daemon.ChannelCaller) bool {
	if resp, ok := call(daemonctl.Request{Op: "channel-guard-registered", Args: map[string]string{"session": session}}); ok && resp.OK {
		return true
	}
	return markChannelGuardReady(session, call, nil)
}

// The local typed marker is the fail-closed fallback if the daemon vanishes
// or refuses the Stop control. Clearing it before an acknowledgement would
// leave the daemon's request active while the hook stops enforcing it.
func completeHookTurn(session, request string, call daemon.ChannelCaller) bool {
	resp, ok := call(daemonctl.Request{Op: "channel-turn-ended", Args: map[string]string{"session": session, "request_id": request}})
	if !ok || !resp.OK {
		return false
	}
	if request != "" {
		_ = daemon.EndTypedGuard(session, request)
	}
	return true
}

// A distinct Claude prompt_id means the typed turn is no longer the turn
// executing tools. Only an acknowledged daemon cancellation clears the marker;
// without the daemon, the later turn bypasses that stale marker by prompt id.
func (a app) abandonTypedTurn(session, request string) {
	resp, ok := daemonctl.Call(daemonctl.Request{Op: "channel-abandon-typed", Args: map[string]string{
		"session": session, "request_id": request,
	}})
	if ok && resp.OK {
		_ = daemon.EndTypedGuard(session, request)
	}
}

func (a app) proveTerminalHook(sessionID string) {
	s, err := daemon.FindOwnSession(context.Background())
	if err == nil && s.Tool == "claude" && s.SessionID == sessionID {
		_ = daemon.ProveTerminalHook(sessionID, s.PID, daemon.CurrentZellijPane())
	}
}

func (a app) proveTerminalPromptHook(sessionID string) {
	s, err := daemon.FindOwnSession(context.Background())
	if err == nil && s.Tool == "claude" && s.SessionID == sessionID {
		_ = daemon.ProveTerminalPromptHook(sessionID, s.PID, daemon.CurrentZellijPane())
	}
}

func (a app) endTerminalSession(sessionID string) {
	s, err := daemon.FindOwnSession(context.Background())
	if err == nil && s.Tool == "claude" && s.SessionID == sessionID {
		daemon.EndTerminalSession(sessionID, s.PID)
	}
}

func hookDeny(reason string) string {
	out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason,
	}})
	return string(out)
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

// channelHint reports the available delivery path. A verified Zellij pane can
// receive guarded prompts even when this organisation disables Claude channels.
func (a app) channelHint(sess daemon.DiscoveredSession) {
	if sess.Tool != "claude" {
		return
	}
	if resp, ok := daemonctl.Call(daemonctl.Request{Op: "channel-ready", Args: map[string]string{"session": sess.SessionID}}); ok && resp.OK {
		fmt.Fprintln(a.stdout, "Prompts sent to this agent can appear in this window through the Share2Us channel.")
		return
	}
	if bindings, err := daemon.LoadBindings(); err == nil {
		if binding, ok := daemon.BindingForSession(bindings, sess.SessionID); ok &&
			daemon.TerminalTypedReady(sess.SessionID, sess.PID) {
			if pane, ok := daemon.FindZellijLocation(context.Background(), binding, sess); ok {
				fmt.Fprintf(a.stdout, "Prompts sent to this agent appear in this window through guarded Zellij delivery (tab %q, pane terminal_%s).\n", pane.TabName, pane.Pane)
				return
			}
		}
	}
	if sess.PID <= 1 {
		fmt.Fprintln(a.stdout, "Prompts wait until this session is live and its hooks are verified.")
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
