package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Headless Codex runs carry the same guard as a delivered Claude turn (ADR-052): a
// PreToolUse hook (`s2u agent hook codex-pre-tool-use`) applies HookDecision and asks the
// owner when it refuses. The sandbox stays the hard gate underneath it: Codex lets a tool
// run when a hook errors, times out or answers malformed, and a sandbox-blocked command
// (network, writes outside the workspace) still fails after an approval.
const (
	CodexPreToolUseHook  = "codex-pre-tool-use"
	CodexGuardProjectEnv = "S2U_CODEX_GUARD_PROJECT"
	CodexGuardSessionEnv = "S2U_CODEX_GUARD_SESSION"
	CodexGuardStrictEnv  = "S2U_CODEX_GUARD_STRICT"
	// codexHookTimeout is above the 3 minute approval hold, so Codex never times the hook
	// out while the owner is deciding.
	codexHookTimeout = 240
	// spawn_agent: a subagent's tools may not reach this hook. MCP tools go through the
	// same rules as in Claude. Other local tools (update_plan, view_image) stay free.
	codexHookMatcher = `^(Bash|apply_patch|spawn_agent)$|^mcp__`
)

// CodexToolCall is one guard check: Codex's tool translated to the Claude tool names
// HookDecision knows. A patch becomes one Edit per file it touches.
type CodexToolCall struct {
	Tool  string
	Input map[string]any
}

// CodexToolCalls maps a Codex PreToolUse call onto guard checks.
func CodexToolCalls(project, tool string, input map[string]any) ([]CodexToolCall, error) {
	switch tool {
	case "apply_patch", "Edit", "Write":
		patch, _ := input["command"].(string)
		paths := codexPatchPaths(patch)
		if len(paths) == 0 {
			return nil, errors.New("Share2Us: a delivered prompt must identify the files its patch edits.")
		}
		calls := make([]CodexToolCall, 0, len(paths))
		for _, p := range paths {
			if !filepath.IsAbs(p) {
				p = filepath.Join(project, p)
			}
			calls = append(calls, CodexToolCall{Tool: "Edit", Input: map[string]any{"file_path": filepath.Clean(p)}})
		}
		return calls, nil
	case "spawn_agent":
		return []CodexToolCall{{Tool: "Agent", Input: input}}, nil
	case "Bash":
		// Codex often edits through the shell (`apply_patch <<'PATCH' ... PATCH`) and applies
		// that form itself, so it is checked as the edit it is.
		cmd, _ := input["command"].(string)
		if patch, ok := codexShellPatch(cmd); ok {
			return CodexToolCalls(project, "apply_patch", map[string]any{"command": patch})
		}
	}
	return []CodexToolCall{{Tool: tool, Input: input}}, nil
}

// codexShellPatch accepts only the exact heredoc form, the whole command: anything around it
// (`; rm ...`, `cd x &&`) would run as shell, so that stays a Bash command for the guard.
func codexShellPatch(cmd string) (string, bool) {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(cmd, "\r\n", "\n")), "\n")
	if len(lines) < 3 {
		return "", false
	}
	head := strings.Fields(lines[0])
	if len(head) != 2 || (head[0] != "apply_patch" && head[0] != "applypatch") || !strings.HasPrefix(head[1], "<<") {
		return "", false
	}
	tag := strings.Trim(strings.TrimPrefix(head[1], "<<"), `'"`)
	if tag == "" || strings.TrimSpace(lines[len(lines)-1]) != tag {
		return "", false
	}
	body := lines[1 : len(lines)-1]
	for _, l := range body {
		if strings.TrimSpace(l) == tag {
			return "", false // a second terminator: text after it runs as shell
		}
	}
	return strings.Join(body, "\n"), true
}

// codexPatchPaths lists every file an apply_patch body adds, updates, deletes or moves to.
func codexPatchPaths(patch string) []string {
	var paths []string
	seen := map[string]bool{}
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimRight(line, "\r")
		for _, prefix := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "} {
			if p, ok := strings.CutPrefix(line, prefix); ok {
				if p = strings.TrimSpace(p); p != "" && !seen[p] {
					seen[p] = true
					paths = append(paths, p)
				}
			}
		}
	}
	return paths
}

var (
	codexHookOnce sync.Once
	codexHookOK   bool
	// codexHookSupported: hooks passed with -c only run with --dangerously-bypass-hook-trust
	// (untrusted hooks are skipped silently, verified with codex-cli 0.172.0, 2026-11-26),
	// and an older Codex without that flag would refuse the whole run.
	codexHookSupported = func() bool {
		codexHookOnce.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "codex", "exec", "--help").CombinedOutput()
			codexHookOK = err == nil && strings.Contains(string(out), "--dangerously-bypass-hook-trust")
		})
		return codexHookOK
	}
)

// codexHookConfig is the `-c hooks.PreToolUse=...` value running exe's guard hook. The
// values are JSON strings, which TOML reads as basic strings.
func codexHookConfig(exe string) string {
	q := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	return "hooks.PreToolUse=[{matcher=" + q(codexHookMatcher) + ",hooks=[{type=\"command\",command=" +
		q(claudeHookCommand(exe, CodexPreToolUseHook)) + ",timeout=" + strconv.Itoa(codexHookTimeout) +
		",statusMessage=" + q("Share2Us guard: if this needs approval, waiting for the owner (asked by email + portal)") + "}]}]"
}

// codexGuardEnv sets the hook's trusted context: the project, the session and strict mode.
// The delivered prompt can't change these (they are the Codex process's own environment).
func codexGuardEnv(env []string, project, session string, strict bool) []string {
	out := make([]string, 0, len(env)+3)
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if k == CodexGuardProjectEnv || k == CodexGuardSessionEnv || k == CodexGuardStrictEnv {
			continue
		}
		out = append(out, e)
	}
	if abs, err := filepath.Abs(project); err == nil && project != "" {
		project = abs
	}
	s := "0"
	if strict {
		s = "1"
	}
	return append(out, CodexGuardProjectEnv+"="+project, CodexGuardSessionEnv+"="+session, CodexGuardStrictEnv+"="+s)
}

// codexHookArgs is what RunCodexInject adds for the guard, or nothing when this Codex can't
// run it (the sandbox alone guards then, as before).
func codexHookArgs() []string {
	// Not on Windows yet: which shell Codex runs a hook command in there is unverified, and a
	// hook that fails to start lets every tool run (the sandbox alone guards, as before).
	if runtime.GOOS == "windows" || !codexHookSupported() {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	return []string{"--dangerously-bypass-hook-trust", "-c", codexHookConfig(exe)}
}
