// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// DiscoveredSession is a live coding-agent session found on this machine.
type DiscoveredSession struct {
	SessionID string
	Tool      string
	Name      string
	Project   string // cwd
	Status    string // available | busy | unknown
	// PID is the process currently holding the interactive session, when the
	// adapter can identify it. It is used only for local identity checks (for
	// example, proving which zellij pane owns a bound session).
	PID int
	// Live: a running tool process holds the session (Claude lists it in
	// `claude agents`), so a hop waits until it lets the session go.
	Live bool
}

// injectRunTimeout bounds a single injected run.
const injectRunTimeout = 10 * time.Minute

// claudeAgentEntry is one element of `claude agents --json`. Interactive sessions
// carry `status` (idle/busy); background ones carry `state` (running/blocked/...).
type claudeAgentEntry struct {
	// PID is the Claude process of a live session; background entries without a
	// running process have none.
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	State     string `json:"state"`
}

// DiscoverClaude lists this machine's live Claude Code sessions via the supported
// `claude agents --json` (no TTY needed). Deduped by sessionId, preferring an
// entry that reports a concrete idle/busy status.
func DiscoverClaude(ctx context.Context) ([]DiscoveredSession, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "claude", "agents", "--json").Output()
	if err != nil {
		return nil, err
	}
	return parseClaudeAgents(out)
}

// parseClaudeAgents turns `claude agents --json` output into discovered sessions,
// deduped by sessionId (preferring an entry with a concrete idle/busy status).
func parseClaudeAgents(out []byte) ([]DiscoveredSession, error) {
	var entries []claudeAgentEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, err
	}
	byID := map[string]DiscoveredSession{}
	for _, e := range entries {
		if strings.TrimSpace(e.SessionID) == "" {
			continue
		}
		s := DiscoveredSession{
			SessionID: e.SessionID,
			Tool:      "claude",
			Name:      e.Name,
			Project:   e.CWD,
			Status:    claudeStatus(e),
			PID:       e.PID,
			Live:      true,
		}
		// One session can be listed twice (its window and a background entry).
		// The busier report wins, so every path shows the same presence.
		if prev, ok := byID[s.SessionID]; ok {
			prevRank, nextRank := presenceRank(prev.Status), presenceRank(s.Status)
			if prevRank > nextRank || (prevRank == nextRank && (prev.PID > 0 || s.PID <= 0)) {
				continue
			}
		}
		byID[s.SessionID] = s
	}
	list := make([]DiscoveredSession, 0, len(byID))
	for _, s := range byID {
		list = append(list, s)
	}
	return list, nil
}

// presenceRank orders presence for merging duplicate reports: busy beats
// available (sending work to a busy session is the mistake to avoid), and any
// concrete report beats unknown.
func presenceRank(status string) int {
	switch status {
	case "busy":
		return 2
	case "available":
		return 1
	}
	return 0
}

// claudeStatus is the one mapping from Claude's words to presence (ADR-041 §8).
// Interactive entries: idle -> available, busy -> busy. Background entries:
// running -> busy; blocked, idle, waiting -> available (a hop runs as its own
// process, so a session waiting on its user can still take one).
func claudeStatus(e claudeAgentEntry) string {
	switch strings.ToLower(strings.TrimSpace(e.Status)) {
	// The server's word for a session ready for work is "available" (ADR-041 §8);
	// "idle" is Claude's.
	case "idle":
		return "available"
	case "busy":
		return "busy"
	}
	// Background entries: map the coarse state.
	switch strings.ToLower(strings.TrimSpace(e.State)) {
	case "running":
		return "busy"
	case "blocked", "idle", "waiting":
		return "available"
	}
	return "unknown"
}

// RunClaudeInject continues a Claude session non-interactively with the injected
// prompt, under the compiled guardrail profile for the session's project (ADR-036
// P3): a restricted permission mode (never bypassPermissions / auto-accept from
// the live session), hard --disallowedTools denies compiled from .s2u.rules, and
// the advisory rules in the system prompt. cwd is the session's project dir, used
// to locate .s2u.rules. Returns the run's combined output. (Phase 4 adds the
// delivered file.)
func RunClaudeInject(ctx context.Context, sessionID, cwd, prompt string, forceRestricted bool) (string, error) {
	priv := AgentPolicy(cwd, forceRestricted)
	policy := CompileRules(LoadRules(cwd), priv)
	cctx, cancel := context.WithTimeout(ctx, injectRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "claude", buildClaudeInjectArgs(sessionID, prompt, policy, claudeMode(priv))...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ErrSessionHeld: Claude will not run a prompt headlessly in a session that a
// running process (an open window) holds. The hop waits instead of forking.
var ErrSessionHeld = errors.New("the session is open in a window")

// RunClaudeInjectSession runs a hop IN sessionID and reports the session the
// agent is in afterwards, read from Claude's JSON result. It never forks: if a
// window holds the session, Claude refuses and this returns ErrSessionHeld.
func RunClaudeInjectSession(ctx context.Context, sessionID, cwd, prompt string, forceRestricted bool) (string, string, error) {
	priv := AgentPolicy(cwd, forceRestricted)
	policy := CompileRules(LoadRules(cwd), priv)
	cctx, cancel := context.WithTimeout(ctx, injectRunTimeout)
	defer cancel()
	args := append(buildClaudeInjectArgs(sessionID, prompt, policy, claudeMode(priv)), "--output-format", "json")
	cmd := exec.CommandContext(cctx, "claude", args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	raw, err := cmd.CombinedOutput()
	if err != nil && claudeRefusedHeld(raw) {
		return "", "", ErrSessionHeld
	}
	out, after := parseClaudeResult(raw)
	return out, after, err
}

// claudeRefusedHeld recognises Claude refusing a session a running process
// holds ("... add --fork-session to branch off a copy", verified 2026-09-07).
func claudeRefusedHeld(out []byte) bool {
	return strings.Contains(string(out), "--fork-session")
}

// parseClaudeResult reads `claude -p --output-format json`: the result text and
// the session id of the run. Anything unparsable is returned as-is, with no id.
func parseClaudeResult(raw []byte) (string, string) {
	var r struct {
		Result    string `json:"result"`
		SessionID string `json:"session_id"`
	}
	trimmed := strings.TrimSpace(string(raw))
	if i := strings.LastIndex(trimmed, "\n{"); i >= 0 {
		trimmed = trimmed[i+1:] // stray lines before the JSON object
	}
	if err := json.Unmarshal([]byte(trimmed), &r); err != nil || r.SessionID == "" {
		return string(raw), ""
	}
	return r.Result, r.SessionID
}

// hopNote tells the agent it is unattended. Verified 2026-09-27: Claude refuses,
// even with an allowlist, a command that changes into another git repository and
// then runs git (its hooks could run), and it refuses chained commands it cannot
// check piece by piece. Refused means stuck, since nobody can approve.
const hopNote = "This prompt reached you through Share2Us while nobody is at the keyboard: a command that needs approval is refused, not asked. " +
	"Run commands one at a time from the project directory instead of chaining them with variables or `cd`. " +
	"To look at files, including another repository's, use your Read, Grep and Glob tools rather than changing into it.\n"

// buildClaudeInjectArgs assembles the `claude` args for a guarded injected run.
// --disallowedTools is variadic, so it is placed immediately before -p (a flag)
// which bounds it.
func buildClaudeInjectArgs(sessionID, prompt string, policy Policy, mode string) []string {
	// Always in place: the hop goes into the bound session, never a fork (owner,
	// 2026-09-29). A session a running process holds cannot be resumed
	// headlessly, so the daemon waits for it rather than calling this.
	args := []string{"--resume", sessionID}
	args = append(args, "--permission-mode", mode)
	args = append(args, "--append-system-prompt", hopNote+policy.AppendSystemPrompt())
	if len(policy.AllowedTools) > 0 {
		args = append(args, "--allowedTools")
		args = append(args, policy.AllowedTools...)
	}
	if len(policy.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools")
		args = append(args, policy.DisallowedTools...)
	}
	args = append(args, "-p", prompt)
	return args
}

// claudeMode picks the injected run's permission mode from the agent's privilege
// (ADR-041 §6): "plan" (read-only) when restricted, else "acceptEdits" — do the
// work, with deny-listed tools still hard blocked.
//
// NOTE there is no third mode here. Claude's only step beyond acceptEdits is
// bypassPermissions, which ADR-036 forbids and this does not reach. What
// PrivilegePrivileged changes for Claude is the DENY LIST, not the mode: the
// baseline `never push` / `never network` patterns are dropped in CompileRules,
// which is what a deploying agent actually needs.
func claudeMode(priv Privilege) string {
	if priv == PrivilegeRestricted {
		return "plan"
	}
	return "acceptEdits"
}

// ClaudeRunner adapts the Claude Code CLI. Strict selects the read-only mode.
type ClaudeRunner struct{ Strict bool }

func (ClaudeRunner) Tool() string { return "claude" }
func (ClaudeRunner) Discover(ctx context.Context) ([]DiscoveredSession, error) {
	return DiscoverClaude(ctx)
}
func (r ClaudeRunner) RunSession(ctx context.Context, sessionID, cwd, prompt string) (string, string, error) {
	return RunClaudeInjectSession(ctx, sessionID, cwd, prompt, r.Strict)
}

func (r ClaudeRunner) Run(ctx context.Context, sessionID, cwd, prompt string) (string, error) {
	return RunClaudeInject(ctx, sessionID, cwd, prompt, r.Strict)
}
