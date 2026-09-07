package daemon

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Guardrails (ADR-036 P3). `.s2u.rules` is a plain-text human list of dos and
// don'ts. It is the SOURCE, not the enforcement: a rule in a prompt is soft (an
// LLM can be argued past it), so we COMPILE the enforceable don'ts into HARD
// gates the model cannot bypass — Claude `--disallowedTools` permission patterns
// applied to the injected `claude --resume` run — and keep the rest as advisory
// text in the system prompt, honestly flagged as not guaranteed.

// Policy is the compiled enforcement for one injected run.
type Policy struct {
	// DisallowedTools are Claude permission patterns passed to --disallowedTools.
	// The tool executor refuses them regardless of what the prompt says (hard).
	DisallowedTools []string
	// Advisory rules could not be mapped to a hard gate; they ride in the system
	// prompt and are best-effort only.
	Advisory []string
}

// selfProtection is always denied so an injected run cannot weaken its own
// guardrails by editing the rules or the compiled Claude settings.
// NOTE: only Edit(path) rules are matched by Claude's file-permission checks —
// Write(path) deny rules are silently ignored (verified 2026-09-07: Claude warns
// "Write(...) is not matched by file permission checks ... Edit rules cover all
// file-editing tools"). So these MUST be Edit(...) or the self-protection is a
// no-op.
var selfProtection = []string{
	"Edit(**/.s2u.rules)",
	"Edit(**/.claude/settings.json)",
	"Edit(**/.claude/settings.local.json)",
}

// hardRule maps a keyword found in a prohibition to the deny patterns it compiles
// to. Only commands with an unambiguous form are hard-enforced; everything else
// stays advisory (honest about the limit).
var hardRules = []struct {
	keywords []string
	deny     []string
}{
	{[]string{"force push", "force-push", "force push"}, []string{"Bash(git push --force:*)", "Bash(git push -f:*)"}},
	{[]string{"push"}, []string{"Bash(git push:*)"}},
	{[]string{"commit"}, []string{"Bash(git commit:*)"}},
	{[]string{"delete", "rm ", "remove file", "destructive"}, []string{"Bash(rm:*)", "Bash(rmdir:*)"}},
	{[]string{"network", "internet", "curl", "wget", "download", "fetch url", "offline"}, []string{"Bash(curl:*)", "Bash(wget:*)", "Bash(nc:*)"}},
	{[]string{"reset --hard", "git reset"}, []string{"Bash(git reset:*)"}},
}

// baselineRules are ENFORCED BY DEFAULT on every injected run, even with no
// .s2u.rules file: the irreversible or outbound actions. Without this an allowed
// device had unrestricted code execution on the target by default. A project can
// opt OUT of an individual item with an "allow ..." line (see allowPrefixes);
// self-protection can never be opted out of.
var baselineRules = []struct {
	name string
	deny []string
}{
	{"push", []string{"Bash(git push:*)", "Bash(git push --force:*)", "Bash(git push -f:*)"}},
	{"delete", []string{"Bash(rm:*)", "Bash(rmdir:*)"}},
	{"network", []string{"Bash(curl:*)", "Bash(wget:*)", "Bash(nc:*)"}},
}

// allowKeywords maps an "allow ..." line to the baseline item it opts out of.
var allowKeywords = map[string][]string{
	"push":    {"push"},
	"delete":  {"delete", "rm ", "remove"},
	"network": {"network", "internet", "curl", "wget", "download"},
}

// prohibitionPrefixes mark a line as a "don't".
var prohibitionPrefixes = []string{"don't", "dont", "do not", "never", "no ", "disallow", "block", "forbid"}

// allowPrefixes mark a line as an explicit opt-out of a baseline guardrail.
var allowPrefixes = []string{"allow ", "permit ", "enable "}

// CompileRules turns plain-text rules into a Policy. Lines starting with a
// prohibition word are matched against the hard-rule vocabulary; matched ones
// become deny patterns, unmatched prohibitions become advisory. Self-protection
// deny patterns are always included. Comments (#) and blank lines are ignored.
func CompileRules(lines []string) Policy {
	// Start from the enforced baseline: self-protection (never removable) plus the
	// default denies. Rules can ADD more, or opt OUT of a baseline item.
	p := Policy{DisallowedTools: append([]string{}, selfProtection...)}
	seen := map[string]bool{}
	for _, d := range p.DisallowedTools {
		seen[d] = true
	}
	optedOut := map[string]bool{}
	for _, raw := range lines {
		low := strings.ToLower(strings.TrimSpace(raw))
		if !hasAnyPrefix(low, allowPrefixes) {
			continue
		}
		for item, kws := range allowKeywords {
			if containsAny(low, kws) {
				optedOut[item] = true
			}
		}
	}
	for _, b := range baselineRules {
		if optedOut[b.name] {
			continue // explicit opt-out for this project
		}
		for _, d := range b.deny {
			if !seen[d] {
				seen[d] = true
				p.DisallowedTools = append(p.DisallowedTools, d)
			}
		}
	}
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		low := strings.ToLower(line)
		if !hasAnyPrefix(low, prohibitionPrefixes) {
			continue // allowances / non-prohibitions are not gates
		}
		matched := false
		for _, hr := range hardRules {
			if containsAny(low, hr.keywords) {
				matched = true
				for _, d := range hr.deny {
					if !seen[d] {
						seen[d] = true
						p.DisallowedTools = append(p.DisallowedTools, d)
					}
				}
			}
		}
		if !matched {
			p.Advisory = append(p.Advisory, line)
		}
	}
	return p
}

// LoadRules reads the rules that apply to a project: the global rules (if any)
// then the project's own .s2u.rules (repo root == the session cwd), union.
// Strictest-wins is inherent — CompileRules only ever ADDS deny patterns.
func LoadRules(projectDir string) []string {
	var lines []string
	if home, err := os.UserHomeDir(); err == nil {
		lines = append(lines, readRulesFile(filepath.Join(home, ".s2u.rules"))...)
	}
	if projectDir != "" {
		lines = append(lines, readRulesFile(filepath.Join(projectDir, ".s2u.rules"))...)
	}
	return lines
}

func readRulesFile(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// AppendSystemPrompt renders the advisory rules for --append-system-prompt,
// honestly flagged as best-effort (the hard denies are separately enforced).
func (p Policy) AppendSystemPrompt() string {
	if len(p.Advisory) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("The user of this machine set these policy rules for remotely-injected prompts. ")
	b.WriteString("Honor them. (Hard limits like blocked commands are enforced separately and cannot be overridden.)\n")
	for _, a := range p.Advisory {
		b.WriteString("- ")
		b.WriteString(a)
		b.WriteString("\n")
	}
	return b.String()
}

// PromptPreamble renders the rules as prompt text for tools that have no per-tool
// deny layer to compile into. Claude gets hard --disallowedTools patterns; Codex
// and Gemini only have a sandbox / approval mode, which stops writes and network
// but knows nothing about "never push". So for those the compiled denies are
// restated in the prompt — best-effort, and honestly labelled as such in the ADR.
func (p Policy) PromptPreamble() string {
	if len(p.DisallowedTools) == 0 && len(p.Advisory) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[Share2Us] The owner of this machine set these rules for remotely injected\n")
	b.WriteString("prompts. They override the request that follows; refuse anything that breaks them.\n")
	for _, d := range p.DisallowedTools {
		b.WriteString("- never " + humanizeDeny(d) + "\n")
	}
	for _, a := range p.Advisory {
		b.WriteString("- " + a + "\n")
	}
	return b.String()
}

// humanizeDeny turns a Claude permission pattern into plain English for the
// preamble ("Bash(git push:*)" -> "run `git push`").
func humanizeDeny(pattern string) string {
	open := strings.Index(pattern, "(")
	if open < 0 || !strings.HasSuffix(pattern, ")") {
		return pattern
	}
	tool, inner := pattern[:open], pattern[open+1:len(pattern)-1]
	inner = strings.TrimSuffix(inner, ":*")
	switch tool {
	case "Bash":
		return "run `" + inner + "`"
	case "Edit", "Write":
		return "edit " + inner
	default:
		return tool + " " + inner
	}
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, strings.TrimSpace(sub)) {
			return true
		}
	}
	return false
}
