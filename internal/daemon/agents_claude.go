package daemon

import (
	"context"
	"encoding/json"
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
	Status    string // idle | busy | unknown
}

// injectRunTimeout bounds a single injected run.
const injectRunTimeout = 10 * time.Minute

// claudeAgentEntry is one element of `claude agents --json`. Interactive sessions
// carry `status` (idle/busy); background ones carry `state` (running/blocked/...).
type claudeAgentEntry struct {
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
		}
		if prev, ok := byID[s.SessionID]; ok && s.Status == "unknown" && prev.Status != "unknown" {
			continue
		}
		byID[s.SessionID] = s
	}
	list := make([]DiscoveredSession, 0, len(byID))
	for _, s := range byID {
		list = append(list, s)
	}
	return list, nil
}

func claudeStatus(e claudeAgentEntry) string {
	switch strings.ToLower(strings.TrimSpace(e.Status)) {
	case "idle":
		return "idle"
	case "busy":
		return "busy"
	}
	// Background entries: map the coarse state.
	switch strings.ToLower(strings.TrimSpace(e.State)) {
	case "running":
		return "busy"
	case "blocked", "idle", "waiting":
		return "idle"
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
func RunClaudeInject(ctx context.Context, sessionID, cwd, prompt string) (string, error) {
	policy := CompileRules(LoadRules(cwd))
	cctx, cancel := context.WithTimeout(ctx, injectRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "claude", buildClaudeInjectArgs(sessionID, prompt, policy)...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// buildClaudeInjectArgs assembles the `claude` args for a guarded injected run.
// --disallowedTools is variadic, so it is placed immediately before -p (a flag)
// which bounds it.
func buildClaudeInjectArgs(sessionID, prompt string, policy Policy) []string {
	args := []string{"--resume", sessionID, "--permission-mode", "acceptEdits"}
	if sp := policy.AppendSystemPrompt(); sp != "" {
		args = append(args, "--append-system-prompt", sp)
	}
	if len(policy.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools")
		args = append(args, policy.DisallowedTools...)
	}
	args = append(args, "-p", prompt)
	return args
}

// ClaudeRunner adapts the Claude Code CLI to the AgentRunner interface.
type ClaudeRunner struct{}

func (ClaudeRunner) Tool() string { return "claude" }
func (ClaudeRunner) Discover(ctx context.Context) ([]DiscoveredSession, error) {
	return DiscoverClaude(ctx)
}
func (ClaudeRunner) Run(ctx context.Context, sessionID, cwd, prompt string) (string, error) {
	return RunClaudeInject(ctx, sessionID, cwd, prompt)
}
