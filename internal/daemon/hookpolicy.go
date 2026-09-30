// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"os"
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

// hookFreeTools are local reads, search, and planning that a delivered hop may
// use without an approval. Subagents are excluded: their tool calls do not
// reliably inherit this session's guard.
var hookFreeTools = map[string]bool{
	"Read": true, "Glob": true, "Grep": true, "LS": true, "NotebookRead": true,
	"TodoWrite": true, "TodoRead": true, "ToolSearch": true,
}

var readToolPathKey = map[string]string{
	"Read": "file_path", "NotebookRead": "notebook_path",
	"Grep": "path", "Glob": "path", "LS": "path",
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
	case tool == "Agent" || tool == "Task":
		return true, "Share2Us: a delivered prompt may not start a subagent because its tool calls may run outside this turn's guard."
	case fileEditTools[tool]:
		if priv == PrivilegeRestricted {
			return true, "Share2Us: this agent is read-only for delivered prompts, so it may not edit files."
		}
		paths := editPaths(input)
		if len(paths) == 0 {
			return true, "Share2Us: a delivered prompt must identify the file it edits."
		}
		for _, path := range paths {
			if !insideDir(cwd, path) {
				return true, "Share2Us: a delivered prompt may edit files only inside this project (" + cwd + ")."
			}
			if insideGitMetadata(cwd, path) {
				return true, "Share2Us: a delivered prompt may not edit Git metadata, which can change what allowed Git commands execute."
			}
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
		if unsafeReadShell(cmd) {
			return true, "Share2Us: delivered prompts may run only plain read commands without shell expansion, substitution, backgrounding or redirection."
		}
		for _, part := range splitShell(cmd) {
			if !anyRuleMatches(allowed, "Bash", map[string]any{"command": part}) || !readCommandInside(cwd, part) {
				return true, "Share2Us: a delivered prompt may run only read-only commands (ls, cat, grep, git status and similar); `" + part + "` would need your approval. Run it yourself if you want it."
			}
		}
		return false, ""
	case readToolPathKey[tool] != "":
		path, _ := input[readToolPathKey[tool]].(string)
		if path == "" && tool != "Read" && tool != "NotebookRead" {
			path = cwd
		}
		if strings.HasPrefix(path, "~") || strings.ContainsAny(path, "*?[]{}") || !insideDir(cwd, path) {
			return true, "Share2Us: delivered prompts may read only inside this project (" + cwd + ")."
		}
		return false, ""
	case hookFreeTools[tool]:
		return false, ""
	}
	return true, "Share2Us: a delivered prompt may not use " + tool + " without your approval. Use it yourself if you want it."
}

// HeadlessGitDecision is the non-removable Git boundary for unattended Claude
// runs. Literal --disallowedTools paths cannot describe symlink aliases or a
// linked worktree's gitdir, so a PreToolUse hook checks the resolved target.
func HeadlessGitDecision(project, tool string, input map[string]any) (bool, string) {
	if project == "" || !filepath.IsAbs(project) {
		return true, "Share2Us: the headless Git guard has no trusted project path."
	}
	if fileEditTools[tool] {
		paths := editPaths(input)
		if len(paths) == 0 {
			return true, "Share2Us: a delivered prompt must identify the file it edits."
		}
		for _, path := range paths {
			if !insideDir(project, path) || insideGitMetadata(project, path) {
				return true, "Share2Us: a delivered prompt may not edit Git metadata or files outside its project."
			}
		}
	}
	if tool == "Bash" {
		cmd, _ := input["command"].(string)
		for _, part := range splitShell(cmd) {
			if bashMatches("git status:*", part) || bashMatches("git blame:*", part) {
				return true, "Share2Us: git status and git blame may execute configured programs and are unavailable in delivered prompts."
			}
		}
	}
	return false, ""
}

// insideDir reports whether a path resolves to dir or below it. Resolve the
// nearest existing parent too, so a new file below an escaping symlink is not
// accepted as an in-project path.
func insideDir(dir, path string) bool {
	if dir == "" || path == "" {
		return false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	base, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	resolved, err := resolvedOrParent(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(base, resolved)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// insideGitMetadata checks the resolved target, not just the spelling of the
// requested path. An in-project symlink to .git must not bypass self-protection.
func insideGitMetadata(dir, path string) bool {
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	base, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return true
	}
	resolved, err := resolvedOrParent(path)
	if err != nil {
		return true
	}
	rel, err := filepath.Rel(base, resolved)
	if err != nil {
		return true
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if gitMetadataSegment(part) {
			return true
		}
	}
	gitDirs, err := projectGitDirs(base)
	if err != nil {
		return true // malformed Git metadata fails closed for edit calls
	}
	for _, gitDir := range gitDirs {
		if pathWithin(gitDir, resolved) {
			return true
		}
	}
	return false
}

func gitMetadataSegment(part string) bool {
	part = strings.ToLower(strings.TrimRight(part, ". "))
	if part == ".git" {
		return true
	}
	// Windows may expose .git through an 8.3 alias. Denying GIT~N is
	// conservative on other platforms too and avoids depending on FS settings.
	if strings.HasPrefix(part, "git~") && len(part) > len("git~") {
		for _, r := range part[len("git~"):] {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	return false
}

func editPaths(input map[string]any) []string {
	var paths []string
	for _, key := range []string{"file_path", "notebook_path"} {
		if raw, present := input[key]; present {
			path, ok := raw.(string)
			if !ok || path == "" {
				return nil
			}
			paths = append(paths, path)
		}
	}
	return paths
}

func pathWithin(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// projectGitDirs includes a linked worktree's actual gitdir and common dir.
// Parsing the .git pointer ourselves avoids invoking Git on mutable config.
func projectGitDirs(project string) ([]string, error) {
	marker := filepath.Join(project, ".git")
	info, err := os.Stat(marker)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	gitDir := marker
	if !info.IsDir() {
		raw, err := smallGitPointer(marker, "gitdir:")
		if err != nil {
			return nil, err
		}
		gitDir = raw
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(project, gitDir)
		}
	}
	gitDir, err = resolvedOrParent(gitDir)
	if err != nil {
		return nil, err
	}
	dirs := []string{gitDir}
	common := filepath.Join(gitDir, "commondir")
	if _, err := os.Stat(common); os.IsNotExist(err) {
		return dirs, nil
	} else if err != nil {
		return nil, err
	}
	commonDir, err := smallGitPointer(common, "")
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitDir, commonDir)
	}
	commonDir, err = resolvedOrParent(commonDir)
	if err != nil {
		return nil, err
	}
	return append(dirs, commonDir), nil
}

func smallGitPointer(path, prefix string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > 4096 || info.IsDir() {
		return "", os.ErrInvalid
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if prefix != "" {
		if !strings.HasPrefix(value, prefix) {
			return "", os.ErrInvalid
		}
		value = strings.TrimSpace(strings.TrimPrefix(value, prefix))
	}
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", os.ErrInvalid
	}
	return value, nil
}

func resolvedOrParent(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := path
	for {
		if _, err := os.Lstat(current); err == nil {
			root, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			rel, err := filepath.Rel(current, path)
			if err != nil {
				return "", err
			}
			return filepath.Join(root, rel), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", os.ErrNotExist
		}
		current = parent
	}
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
		for _, path := range editPaths(input) {
			if globMatch(spec, path) {
				return true
			}
		}
		return false
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
