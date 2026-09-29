// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
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

// HookDecision says whether the policy for cwd refuses this tool call, and why.
func HookDecision(cwd, tool string, input map[string]any, forceRestricted bool) (deny bool, reason string) {
	priv := AgentPolicy(cwd, forceRestricted)
	policy := CompileRules(LoadRules(cwd), priv)
	for _, rule := range policy.DisallowedTools {
		if ruleMatches(rule, tool, input) {
			return true, "Share2Us: this project's rules for delivered prompts do not allow " + describeRule(rule) + "."
		}
	}
	if priv == PrivilegeRestricted {
		if fileEditTools[tool] {
			return true, "Share2Us: this agent is read-only for delivered prompts, so it may not edit files."
		}
		if tool == "Bash" {
			cmd, _ := input["command"].(string)
			for _, part := range splitShell(cmd) {
				// Plan mode is what makes a headless restricted hop read-only, so
				// the compiled policy lists no reads for it; here the read list
				// itself is the gate (denies were checked above).
				if !anyRuleMatches(hopReadOnly, "Bash", map[string]any{"command": part}) {
					return true, "Share2Us: this agent is read-only for delivered prompts; `" + part + "` is not a read-only command it may run."
				}
			}
		}
	}
	return false, ""
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
