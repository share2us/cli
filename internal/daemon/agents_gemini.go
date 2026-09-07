// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

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
// Guardrails are enforced through Gemini's POLICY ENGINE (admin tier, per-run
// TOML), not --approval-mode: a `deny` rule removes the tool from the model's
// list outright, whereas the approval mode only decides what is auto-approved.
//
// NOTE: the model round-trip is still unverified against a live session (this dev
// host has no valid Gemini credential). Flag acceptance, session resolution and
// resume-by-index are verified; whether a deny actually blocks a determined
// prompt is NOT — see ADR-036.

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
func RunGeminiInject(ctx context.Context, sessionID, cwd, prompt string, strict bool) (string, error) {
	if cwd == "" {
		return "", fmt.Errorf("gemini inject needs the session's project directory")
	}
	// Guardrails: the compiled rules become admin-tier policy-engine denies (the
	// hard gate), and also ride in the prompt for the advisory ones that cannot be
	// compiled. --approval-mode is NOT the gate; it only decides auto-approval.
	policy := CompileRules(LoadRules(cwd))
	if preamble := policy.PromptPreamble(); preamble != "" {
		prompt = preamble + "\n" + prompt
	}
	// Fail closed: with a system policy present, Gemini ignores supplemental
	// --admin-policy paths, so our denies would silently not apply.
	if geminiSystemPolicyPresent() {
		return "", fmt.Errorf("refusing to inject: %s holds system policies, so Share2Us guardrails would be ignored (add the rules there instead)", geminiAdminPolicyDir)
	}
	policyDir, cleanupPolicy, err := writeGeminiPolicy(policy)
	if err != nil {
		return "", fmt.Errorf("write gemini policy: %w", err)
	}
	defer cleanupPolicy()
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
	cmd := exec.CommandContext(cctx, "gemini", buildGeminiInjectArgs(idx, prompt, geminiApproval(strict), policyDir)...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func buildGeminiInjectArgs(index, prompt, approval, policyDir string) []string {
	// -p headless, -r <index> resume, restricted approval (never -y/yolo), and the
	// admin-tier policy file that carries the actual denies.
	args := []string{"-p", prompt, "-r", index, "--approval-mode", approval}
	if policyDir != "" {
		args = append(args, "--admin-policy", policyDir)
	}
	return args
}

// geminiApproval picks the approval mode: "plan" (read-only) under --agent-strict,
// else "auto_edit". Never yolo.
func geminiApproval(strict bool) string {
	if strict {
		return "plan"
	}
	return "auto_edit"
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

// GeminiRunner adapts the Gemini CLI. Strict selects the read-only plan mode.
type GeminiRunner struct{ Strict bool }

func (GeminiRunner) Tool() string { return "gemini" }
func (GeminiRunner) Discover(ctx context.Context) ([]DiscoveredSession, error) {
	return DiscoverGemini(ctx)
}
func (r GeminiRunner) Run(ctx context.Context, sessionID, cwd, prompt string) (string, error) {
	return RunGeminiInject(ctx, sessionID, cwd, prompt, r.Strict)
}
