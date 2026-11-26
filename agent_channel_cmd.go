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
		Call:  daemonctl.Call,
		Logf:  func(format string, args ...any) { fmt.Fprintf(a.stderr, format+"\n", args...) },
		Tools: a.channelAgentTools(),
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
	// LastAssistantMessage: newer Claude Code passes the turn's final text to Stop hooks.
	LastAssistantMessage string `json:"last_assistant_message"`
}

// stopHookReply is a typed request's reply for its sender. Claude can run the Stop hook
// before the final message reaches the transcript file: reading too early returned "no
// report", or an EARLIER progress line as the answer (2026-11-26). So the hook's own
// last-message field wins when Claude passes it; otherwise the transcript is re-read
// until its last reply stops changing.
func stopHookReply(in hookInput, request string) string {
	if request == "" {
		return ""
	}
	if r := strings.TrimSpace(in.LastAssistantMessage); r != "" {
		return r
	}
	last, stable := "", 0
	for i := 0; i < stopReplyTries; i++ {
		r := daemon.TranscriptLastReply(in.TranscriptPath, request)
		if r != "" && r == last {
			if stable++; stable >= 2 {
				return r
			}
		} else {
			stable = 0
		}
		last = r
		time.Sleep(stopReplyWait)
	}
	return last
}

var (
	stopReplyTries = 12
	stopReplyWait  = 250 * time.Millisecond
)

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
	case "session-start", daemon.SessionStartHookV2:
		a.proveTerminalHook(in.SessionID)
		a.followFolderAgent(in.SessionID)
		if args[0] == daemon.SessionStartHookV2 {
			a.proveTerminalPromptHook(in.SessionID)
		}
		markChannelGuardReady(in.SessionID, daemonctl.Call, time.Sleep)
	case "user-prompt-submit":
		// A just-started Claude process may not appear in `claude agents` at
		// SessionStart yet. This later hook gives the guard one more chance.
		ensureChannelGuardReady(in.SessionID, daemonctl.Call)
		if in.PromptID != "" {
			a.proveTerminalPromptHook(in.SessionID)
		}
		_ = daemon.BindTypedPromptID(in.SessionID, in.Prompt, in.PromptID)
	case "pre-tool-use", daemon.PreToolUseHookV2:
		if daemon.TypedHopNotStarted(in.SessionID, in.TranscriptPath) {
			return 0 // the owner's own turn; the queued hop hasn't been submitted yet
		}
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
			// Anything but self-protection can be allowed by the agent's owner (ADR-052): a
			// grant covers it, or the owner is asked and the command waits for the answer.
			awaiting := ""
			if !daemon.SelfProtected(in.CWD, in.ToolName, in.ToolInput) {
				hold := approvalHoldShort
				if args[0] == daemon.PreToolUseHookV2 {
					hold = approvalHold
				}
				allowed, why, url := askOwner(in, reason, hold, daemonctl.Call, time.Sleep)
				if allowed {
					if why != "" { // allowed by the owner just now: say so in the window
						out, _ := json.Marshal(map[string]any{"systemMessage": why})
						fmt.Fprintln(a.stdout, string(out))
					}
					return 0
				}
				reason, awaiting = why, url
			}
			// The sender must learn its request hit a wall, not just read a vague "done" — and,
			// when the owner is still deciding, that it continues by itself if allowed.
			_, _ = daemonctl.Call(daemonctl.Request{Op: "channel-denied", Args: map[string]string{
				"session": in.SessionID, "what": daemon.DescribeToolUse(in.ToolName, in.ToolInput), "approval": awaiting}})
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
			} else if daemon.TypedHopNotStarted(in.SessionID, in.TranscriptPath) {
				request = "" // the owner's turn ended; the queued hop runs next
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
		completeHookTurn(in.SessionID, request, stopHookReply(in, request), daemonctl.Call)
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
//
// reply is the agent's last message from the transcript: the typed turn's result when the
// agent didn't report through the channel.
func completeHookTurn(session, request, reply string, call daemon.ChannelCaller) bool {
	args := map[string]string{"session": session, "request_id": request}
	if request != "" && reply != "" {
		args["reply"] = reply
	}
	resp, ok := call(daemonctl.Request{Op: "channel-turn-ended", Args: args})
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

// approvalHold is how long a blocked command waits for its owner's answer before the agent
// is told to carry on without it (Share2Us continues the task if it is allowed later). The
// PreToolUse hook timeout (WriteChannelLaunchConfig) must stay above this.
var (
	approvalHold = 3 * time.Minute
	// approvalHoldShort is for windows whose hook runs under Claude's default timeout
	// (older launches): safely below the 60 s some Claude versions use.
	approvalHoldShort = 40 * time.Second
	approvalPoll      = 2 * time.Second
)

// askOwner checks the agent's grants, else files an approval request and holds. It returns
// whether to allow, and otherwise the refusal text for the agent.
// The third result is the approval page when the owner hasn't decided yet (the request is
// parked and continues if allowed).
func askOwner(in hookInput, reason string, hold time.Duration, call func(daemonctl.Request) (daemonctl.Response, bool), sleep func(time.Duration)) (bool, string, string) {
	text := daemon.ActionText(in.ToolName, in.ToolInput)
	// The guard's message speaks to the agent ("run it yourself"); the owner sees the why only.
	ownerReason := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(reason, "Share2Us: "), " Run it yourself if you want it."))
	resp, ok := call(daemonctl.Request{Op: "approval-check", Args: map[string]string{
		"session": in.SessionID, "tool": in.ToolName, "text": text, "reason": ownerReason}})
	var check struct {
		Decision, ID, URL, Display string
	}
	if !ok || !resp.OK || json.Unmarshal(resp.Data, &check) != nil {
		return false, reason, ""
	}
	switch check.Decision {
	case "allow":
		return true, "", ""
	case "pending":
	default:
		return false, reason, ""
	}
	deadline := time.Now().Add(hold)
	for time.Now().Before(deadline) {
		sleep(approvalPoll)
		st, ok := call(daemonctl.Request{Op: "approval-status", Args: map[string]string{"id": check.ID}})
		var s struct{ Status string }
		if !ok || json.Unmarshal(st.Data, &s) != nil {
			continue
		}
		switch s.Status {
		case "approved":
			return true, "Share2Us: the owner allowed `" + check.Display + "`.", ""
		case "denied":
			return false, "Share2Us: the owner of this machine declined `" + check.Display + "`. Don't try it another way; finish what you can without it and say what you couldn't do.", ""
		case "expired", "used":
			return false, reason, ""
		}
	}
	_, _ = call(daemonctl.Request{Op: "approval-park", Args: map[string]string{"id": check.ID, "session": in.SessionID}})
	return false, "Share2Us: this needs the machine owner's approval, and they've been asked (by email and in the Share2Us portal) but haven't answered yet. Don't retry it another way: finish what you can, say it is waiting for the owner's approval, and end your turn. If they allow it, Share2Us continues this task here.", check.URL
}

// followFolderAgent: a window started with `s2u claude` in a folder whose Claude agent is
// bound becomes that agent when the bound session is no longer open (a resume in another
// tab, a new session after /clear), keeping the agent id (todo ZW-10). It never takes over
// from a bound session that is still open in another window.
func (a app) followFolderAgent(sessionID string) {
	ctx := context.Background()
	s, err := daemon.FindOwnSession(ctx)
	if err != nil || s.Tool != "claude" || s.SessionID != sessionID {
		return
	}
	list, err := daemon.LoadBindings()
	if err != nil {
		return
	}
	b, ok := daemon.BindingFor(list, s.Project, "claude")
	if !ok || b.SessionID == "" || b.SessionID == sessionID {
		return
	}
	if live, err := (daemon.ClaudeRunner{}).Discover(ctx); err == nil {
		for _, d := range live {
			if d.SessionID == b.SessionID && d.Live {
				return // the agent's own window is still open: leave it
			}
		}
	}
	_, _, _ = daemon.BindSessionInPane(b.Project, "claude", b.Label, sessionID, daemon.CurrentZellijPane())
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
	_, _ = installClaudeSkill() // keep /s2u current with this build; best effort
	cmd := exec.CommandContext(ctx, "claude", append(daemon.ClaudeChannelArgs(mcp, settings), args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// The agent bound to this directory knows who it is, so its posts into a
	// sharenet project are held to its owner's grant (ADR-050).
	if cwd, err := os.Getwd(); err == nil {
		daemon.SetAgentEnv(cmd, cwd)
	}
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
