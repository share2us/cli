package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

// Gemini adapter (ADR-036 P5). Gemini CLI has `--list-sessions` (per project,
// each line carries a stable UUID) and `-p/--prompt -r/--resume <index>` for a
// headless resume, plus `--approval-mode plan|auto_edit|yolo` (the guardrail).
// Discovery runs `gemini --list-sessions` in each project from ~/.gemini/
// projects.json. Resume is by INDEX, so injection re-lists to map UUID -> index.
//
// NOTE: the injection path is not yet verified against a live Gemini session
// (resume-by-index + approval-mode behaviour need a real run); it is marked held,
// like the Codex inject path.

// geminiSessionLine parses "  N. <title> (<age>) [<uuid>]".
var geminiSessionLine = regexp.MustCompile(`^\s*(\d+)\.\s+(.+?)\s+\(([^)]+)\)\s+\[([0-9a-fA-F-]{36})\]\s*$`)

// geminiProjects is ~/.gemini/projects.json.
type geminiProjects struct {
	Projects map[string]string `json:"projects"`
}

// DiscoverGemini lists Gemini sessions across the known projects.
func DiscoverGemini(ctx context.Context) ([]DiscoveredSession, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(home, ".gemini", "projects.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var gp geminiProjects
	if err := json.Unmarshal(raw, &gp); err != nil {
		return nil, err
	}
	var out []DiscoveredSession
	for project := range gp.Projects {
		listing, err := geminiListSessions(ctx, project)
		if err != nil {
			continue // best-effort per project
		}
		out = append(out, parseGeminiSessions(listing, project)...)
	}
	return out, nil
}

// geminiListSessions runs `gemini --list-sessions` in the project dir.
func geminiListSessions(ctx context.Context, projectDir string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "gemini", "--list-sessions")
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// parseGeminiSessions turns the --list-sessions output into sessions (keyed by
// the stable UUID; index/status are not carried since indices shift).
func parseGeminiSessions(listing, project string) []DiscoveredSession {
	var out []DiscoveredSession
	for _, line := range splitLines(listing) {
		m := geminiSessionLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		out = append(out, DiscoveredSession{
			SessionID: m[4], // uuid
			Tool:      "gemini",
			Name:      m[2],
			Project:   project,
			Status:    "unknown",
		})
	}
	return out
}

// RunGeminiInject resumes a Gemini session headlessly with the prompt, under a
// restricted approval mode (auto-approve edits, never yolo). Resume is by index,
// so it re-lists to find the index of the target UUID.
func RunGeminiInject(ctx context.Context, sessionID, cwd, prompt string) (string, error) {
	if cwd == "" {
		return "", fmt.Errorf("gemini inject needs the session's project directory")
	}
	listing, err := geminiListSessions(ctx, cwd)
	if err != nil {
		return "", fmt.Errorf("list gemini sessions: %w", err)
	}
	idx := geminiIndexForUUID(listing, sessionID)
	if idx == "" {
		return "", fmt.Errorf("gemini session %s not found in %s", sessionID, cwd)
	}
	cctx, cancel := context.WithTimeout(ctx, injectRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "gemini", buildGeminiInjectArgs(idx, prompt)...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func buildGeminiInjectArgs(index, prompt string) []string {
	// -p headless, -r <index> resume, restricted approval (never -y/yolo).
	return []string{"-p", prompt, "-r", index, "--approval-mode", "auto_edit"}
}

// geminiIndexForUUID finds the list index whose line carries the given UUID.
func geminiIndexForUUID(listing, uuid string) string {
	for _, line := range splitLines(listing) {
		m := geminiSessionLine.FindStringSubmatch(line)
		if m != nil && m[4] == uuid {
			return m[1]
		}
	}
	return ""
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// GeminiRunner adapts the Gemini CLI to the AgentRunner interface.
type GeminiRunner struct{}

func (GeminiRunner) Tool() string { return "gemini" }
func (GeminiRunner) Discover(ctx context.Context) ([]DiscoveredSession, error) {
	return DiscoverGemini(ctx)
}
func (GeminiRunner) Run(ctx context.Context, sessionID, cwd, prompt string) (string, error) {
	return RunGeminiInject(ctx, sessionID, cwd, prompt)
}
