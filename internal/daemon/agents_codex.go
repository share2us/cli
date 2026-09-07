package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Codex adapter (ADR-036 P5). Codex has no `--json` discovery, so sessions are
// read from ~/.codex/session_index.jsonl ({id, thread_name, updated_at}); it has
// no live busy/idle signal and the index carries no cwd, so both are reported
// "unknown"/empty for now. Injection uses `codex exec resume <id> <prompt>`
// (headless) under a restricted sandbox (never --dangerously-bypass...).
//
// NOTE: the injection path is not yet verified against a live Codex session (the
// exact sandbox flag/mode and resume behaviour need a real run); discovery is.

// codexIndexEntry is one line of ~/.codex/session_index.jsonl.
type codexIndexEntry struct {
	ID         string `json:"id"`
	ThreadName string `json:"thread_name"`
	UpdatedAt  string `json:"updated_at"`
}

// codexRecentWindow bounds how far back a session may have been updated to still
// be advertised (Codex has no live status, so this stands in for "live-ish").
const codexRecentWindow = 48 * time.Hour

// DiscoverCodex lists recent Codex sessions from the session index.
func DiscoverCodex(ctx context.Context) ([]DiscoveredSession, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return parseCodexIndex(filepath.Join(home, ".codex", "session_index.jsonl"), time.Now())
}

func parseCodexIndex(path string, now time.Time) ([]DiscoveredSession, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	// Keep the most recent entry per id (the index appends revisions).
	byID := map[string]DiscoveredSession{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var e codexIndexEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil || e.ID == "" {
			continue
		}
		if ts, err := time.Parse(time.RFC3339Nano, e.UpdatedAt); err == nil && now.Sub(ts) > codexRecentWindow {
			continue
		}
		byID[e.ID] = DiscoveredSession{
			SessionID: e.ID,
			Tool:      "codex",
			Name:      e.ThreadName,
			Project:   "",
			Status:    "unknown",
		}
	}
	out := make([]DiscoveredSession, 0, len(byID))
	for _, s := range byID {
		out = append(out, s)
	}
	return out, nil
}

// RunCodexInject resumes a Codex session non-interactively with the injected
// prompt, under a workspace-write sandbox (writes confined to the workspace, no
// network — which blocks push/deploy/fetch). Never bypasses the sandbox.
func RunCodexInject(ctx context.Context, sessionID, cwd, prompt string, strict bool) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, injectRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "codex", buildCodexInjectArgs(sessionID, cwd, prompt, codexSandbox(strict))...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// buildCodexInjectArgs assembles the codex args. The sandbox is the hard gate;
// -c sandbox_mode is a config override that resume accepts.
func buildCodexInjectArgs(sessionID, cwd, prompt, sandbox string) []string {
	args := []string{"exec", "resume", "-c", "sandbox_mode=" + sandbox}
	if cwd != "" {
		args = append(args, "-C", cwd)
	}
	args = append(args, sessionID, prompt)
	return args
}

// codexSandbox picks the sandbox: read-only under --agent-strict, else
// workspace-write (writes in the workspace, no network). Never bypasses.
func codexSandbox(strict bool) string {
	if strict {
		return "read-only"
	}
	return "workspace-write"
}

// CodexRunner adapts the Codex CLI. Strict selects the read-only sandbox.
type CodexRunner struct{ Strict bool }

func (CodexRunner) Tool() string { return "codex" }
func (CodexRunner) Discover(ctx context.Context) ([]DiscoveredSession, error) {
	return DiscoverCodex(ctx)
}
func (r CodexRunner) Run(ctx context.Context, sessionID, cwd, prompt string) (string, error) {
	return RunCodexInject(ctx, sessionID, cwd, prompt, r.Strict)
}
