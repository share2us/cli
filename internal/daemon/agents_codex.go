// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Codex adapter (ADR-036 P5). Codex has no `--json` discovery, so sessions are
// read from the rollout store at ~/.codex/sessions/<yyyy>/<mm>/<dd>/rollout-*.jsonl.
// Each rollout's first line is a `session_meta` record carrying the session id and
// the session's cwd. Injection uses `codex exec resume <id> <prompt>` (headless)
// under a restricted sandbox (never --dangerously-bypass-...).
//
// Verified 2026-09-07 against codex 0.152.0 on a live session:
//   - ~/.codex/session_index.jsonl is NOT the session store (5 entries vs 157
//     rollouts, and a `codex exec` session never appears in it) — reading it meant
//     discovery missed essentially every session. The rollout store is the source.
//   - `codex exec resume` accepts NEITHER `-C` nor `--sandbox`; it takes the cwd
//     from the process working directory and the sandbox via `-c sandbox_mode=`.
//     Resume also filters candidates by cwd (cf. its `--all` flag), so running in
//     the session's own directory is required, not just cosmetic.
//   - THE SANDBOX ALONE IS NOT A GUARDRAIL. With only sandbox_mode set, a denied
//     command is escalated through Codex's approval flow, and a host whose
//     config.toml enables auto-approval (e.g. approvals_reviewer = "auto_review")
//     silently re-runs it UNSANDBOXED — a read-only inject wrote a file in the
//     workspace. Pinning approval_policy=never makes the sandbox actually hold
//     ("read-only file system"). Both must be set on every injected run.

// codexRecentWindow bounds how recently a session must have been touched to still
// be advertised (Codex has no live status, so this stands in for "live-ish").
const codexRecentWindow = 48 * time.Hour

// codexMetaScanLimit bounds how far into a rollout we read looking for a display
// name, so discovery stays cheap on large transcripts.
const codexMetaScanLimit = 400

// codexRollout is the first line of a rollout file.
type codexRollout struct {
	Type    string `json:"type"`
	Payload struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
		Role      string `json:"role"`
		Content   []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"payload"`
}

// DiscoverCodex lists recent Codex sessions from the rollout store.
func DiscoverCodex(ctx context.Context) ([]DiscoveredSession, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return discoverCodexIn(filepath.Join(home, ".codex", "sessions"), time.Now())
}

// discoverCodexIn walks the rollout tree, keeping files touched inside the recent
// window. Only the header (plus a bounded prefix, for the name) is parsed.
func discoverCodexIn(root string, now time.Time) ([]DiscoveredSession, error) {
	var out []DiscoveredSession
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort: skip unreadable dirs
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || now.Sub(info.ModTime()) > codexRecentWindow {
			return nil
		}
		if s, ok := parseCodexRollout(path); ok {
			out = append(out, s)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return out, nil
}

// parseCodexRollout reads a rollout's session_meta header and derives a display
// name from the first real user message (synthetic <...> context blocks skipped).
func parseCodexRollout(path string) (DiscoveredSession, bool) {
	f, err := os.Open(path)
	if err != nil {
		return DiscoveredSession{}, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	if !sc.Scan() {
		return DiscoveredSession{}, false
	}
	var head codexRollout
	if err := json.Unmarshal(sc.Bytes(), &head); err != nil || head.Type != "session_meta" || head.Payload.SessionID == "" {
		return DiscoveredSession{}, false
	}
	s := DiscoveredSession{
		SessionID: head.Payload.SessionID,
		Tool:      "codex",
		Project:   head.Payload.Cwd,
		Status:    "unknown",
	}
	for i := 0; i < codexMetaScanLimit && sc.Scan(); i++ {
		var rec codexRollout
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil || rec.Payload.Role != "user" || len(rec.Payload.Content) == 0 {
			continue
		}
		text := strings.TrimSpace(rec.Payload.Content[0].Text)
		if text == "" || strings.HasPrefix(text, "<") {
			continue // synthetic context block, not something the user typed
		}
		s.Name = codexTruncate(text, 60)
		break
	}
	if s.Name == "" {
		s.Name = filepath.Base(head.Payload.Cwd)
	}
	return s, true
}

func codexTruncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// RunCodexInject resumes a Codex session non-interactively with the injected
// prompt, under a workspace-write sandbox (writes confined to the workspace, no
// network — which blocks push/deploy/fetch). Never bypasses the sandbox.
//
// Codex has no per-tool deny layer to compile .s2u.rules into, so the rules ride
// in the prompt (best-effort) and the sandbox is the hard gate.
func RunCodexInject(ctx context.Context, sessionID, cwd, prompt string, strict bool) (string, error) {
	if preamble := CompileRules(LoadRules(cwd)).PromptPreamble(); preamble != "" {
		prompt = preamble + "\n" + prompt
	}
	cctx, cancel := context.WithTimeout(ctx, injectRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "codex", buildCodexInjectArgs(sessionID, prompt, codexSandbox(strict))...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// buildCodexInjectArgs assembles the codex args. The sandbox is the hard gate;
// `-c sandbox_mode=` is the only way to set it on `resume`, and it is only load
// bearing together with approval_policy=never (see the escalation note above).
// There is no cwd flag — the caller sets the process directory.
// --skip-git-repo-check keeps injection working in a project that is not a git
// repo (codex otherwise refuses headlessly).
func buildCodexInjectArgs(sessionID, prompt, sandbox string) []string {
	return []string{
		"exec", "resume",
		"-c", "sandbox_mode=" + sandbox,
		// Without this the sandbox is advisory: a denied command escalates to the
		// approval flow, which the host's config may auto-approve, re-running it
		// with no sandbox at all. Verified 2026-09-07.
		"-c", "approval_policy=never",
		"--skip-git-repo-check",
		sessionID, prompt,
	}
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
