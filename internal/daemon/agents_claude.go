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
// prompt and returns its output. Phase 2 runs a plain resume; Phase 3 adds the
// compiled guardrail profile (--permission-mode / --allowedTools / hooks) and
// Phase 4 the delivered file.
func RunClaudeInject(ctx context.Context, sessionID, prompt string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, injectRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "claude", "--resume", sessionID, "-p", prompt)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ClaudeRunner adapts the Claude Code CLI to the AgentRunner interface.
type ClaudeRunner struct{}

func (ClaudeRunner) Tool() string { return "claude" }
func (ClaudeRunner) Discover(ctx context.Context) ([]DiscoveredSession, error) {
	return DiscoverClaude(ctx)
}
func (ClaudeRunner) Run(ctx context.Context, sessionID, prompt string) (string, error) {
	return RunClaudeInject(ctx, sessionID, prompt)
}
