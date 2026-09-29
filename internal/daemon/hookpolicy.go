// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"path/filepath"
	"regexp"
	"strings"
)

// The guardrails of a hop delivered into an open window (owner, 2026-09-29). A
// headless hop runs with --permission-mode and --disallowedTools; a prompt
// delivered live runs under the window's own mode, which may be auto. So the
// `s2u claude` session carries a PreToolUse hook (`s2u agent hook
// pre-tool-use`), and while a delivered hop is in progress in that session it
// applies the same policy: the compiled .s2u.rules denies, and read-only when
// the agent is restricted.

// fileEditTools are the tools Claude's Edit(...) rules cover.
var fileEditTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}

// hookFreeTools are what a headless hop could use without an approval: Claude
// asks for none of these (reads, search, planning, subagents, whose own tool
// calls pass through this hook too).
var hookFreeTools = map[string]bool{
	"Read": true, "Glob": true, "Grep": true, "LS": true, "NotebookRead": true,
	"TodoWrite": true, "TodoRead": true, "ToolSearch": true, "Task": true, "Agent": true,
}

// HookDecision says whether the policy for cwd refuses this tool call, and why.
//
// Parity with a headless hop, whatever the window's mode (owner, 2026-09-29): a
// headless hop ran in acceptEdits (plan when restricted), so it could edit
// inside the project and run only the allowlisted reads, and anything else
// needed an approval nobody was there to give. A prompt delivered into an
// auto-mode window would otherwise run any command; this keeps it to what the
// headless hop could do.
func HookDecision(cwd, tool string, input map[string]any, forceRestricted bool) (deny bool, reason string) {
	priv := AgentPolicy(cwd, forceRestricted)
	policy := CompileRules(LoadRules(cwd), priv)
	for _, rule := range policy.DisallowedTools {
		if ruleMatches(rule, tool, input) {
			return true, "Share2Us: this project's rules for delivered prompts do not allow " + describeRule(rule) + "."
		}
	}
	switch {
	case strings.HasPrefix(tool, "mcp__"+ChannelServerName+"__"):
		return false, "" // the report tool
	case fileEditTools[tool]:
		if priv == PrivilegeRestricted {
			return true, "Share2Us: this agent is read-only for delivered prompts, so it may not edit files."
		}
		path, _ := input["file_path"].(string)
		if path == "" {
			path, _ = input["notebook_path"].(string)
		}
		if !insideDir(cwd, path) {
			return true, "Share2Us: a delivered prompt may edit files only inside this project (" + cwd + ")."
		}
		return false, ""
	case tool == "Bash":
		allowed := policy.AllowedTools
		if priv == PrivilegeRestricted {
			// Plan mode made a headless restricted hop read-only, so the compiled
			// policy lists no reads for it; here the read list is the gate.
			allowed = hopReadOnly
		}
		cmd, _ := input["command"].(string)
		for _, part := range splitShell(cmd) {
			if !anyRuleMatches(allowed, "Bash", map[string]any{"command": part}) {
				return true, "Share2Us: a delivered prompt may run only read-only commands (ls, cat, grep, git status and similar); `" + part + "` would need your approval. Run it yourself if you want it."
			}
		}
		return false, ""
	case hookFreeTools[tool]:
		return false, ""
	}
	return true, "Share2Us: a delivered prompt may not use " + tool + " without your approval. Use it yourself if you want it."
}

// insideDir reports whether path is dir or below it.
func insideDir(dir, path string) bool {
	if dir == "" || path == "" {
		return false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func anyRuleMatches(rules []string, tool string, input map[string]any) bool {
	for _, r := range rules {
		if ruleMatches(r, tool, input) {
			return true
		}
	}
	return false
}

// ruleMatches applies one Claude permission pattern: "Tool", "Bash(cmd)",
// "Bash(prefix:*)", or "Edit(glob)" (which covers every file-editing tool).
func ruleMatches(rule, tool string, input map[string]any) bool {
	name, spec, hasSpec := strings.Cut(rule, "(")
	if hasSpec {
		spec = strings.TrimSuffix(spec, ")")
	}
	switch {
	case name == "Bash" && tool == "Bash":
		if !hasSpec {
			return true
		}
		cmd, _ := input["command"].(string)
		// A chained command is refused when any piece of it is.
		for _, part := range splitShell(cmd) {
			if bashMatches(spec, part) {
				return true
			}
		}
		return false
	case name == "Edit" && fileEditTools[tool]:
		if !hasSpec {
			return true
		}
		path, _ := input["file_path"].(string)
		if path == "" {
			path, _ = input["notebook_path"].(string)
		}
		return path != "" && globMatch(spec, path)
	case !hasSpec:
		return name == tool
	}
	return false
}

func bashMatches(spec, cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if prefix, ok := strings.CutSuffix(spec, ":*"); ok {
		return cmd == prefix || strings.HasPrefix(cmd, prefix+" ")
	}
	return cmd == spec
}

// splitShell splits a command line at &&, ||, ;, | and newlines, so each piece
// is checked on its own.
var shellSeparators = regexp.MustCompile(`&&|\|\||[;|\n]`)

func splitShell(cmd string) []string {
	var out []string
	for _, p := range shellSeparators.Split(cmd, -1) {
		p = strings.TrimSpace(p)
		// A leading env assignment or subshell paren does not hide the command.
		p = strings.TrimLeft(p, "( ")
		for {
			f, rest, ok := strings.Cut(p, " ")
			if !ok || !strings.Contains(f, "=") || strings.HasPrefix(f, "-") {
				break
			}
			p = strings.TrimSpace(rest)
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// globMatch matches a path against a pattern where ** spans directories and *
// and ? stay inside one path segment.
func globMatch(pattern, path string) bool {
	path = strings.ReplaceAll(path, `\`, "/")
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case c == '*' && i+1 < len(pattern) && pattern[i+1] == '*':
			i++
			if i+1 < len(pattern) && pattern[i+1] == '/' {
				i++
				b.WriteString("(?:.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	// A relative pattern such as **/x matches anywhere in an absolute path.
	return re.MatchString(path) || re.MatchString(strings.TrimPrefix(path, "/"))
}

// describeRule turns a pattern into words for the refusal.
func describeRule(rule string) string {
	name, spec, ok := strings.Cut(rule, "(")
	if !ok {
		return "the " + name + " tool"
	}
	spec = strings.TrimSuffix(strings.TrimSuffix(spec, ")"), ":*")
	if name == "Bash" {
		return "running `" + spec + "`"
	}
	return "editing " + spec
}
