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
var selfProtection = []string{
	"Edit(**/.s2u.rules)", "Write(**/.s2u.rules)",
	"Edit(**/.claude/settings.json)", "Write(**/.claude/settings.json)",
	"Edit(**/.claude/settings.local.json)", "Write(**/.claude/settings.local.json)",
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

// prohibitionPrefixes mark a line as a "don't".
var prohibitionPrefixes = []string{"don't", "dont", "do not", "never", "no ", "disallow", "block", "forbid"}

// CompileRules turns plain-text rules into a Policy. Lines starting with a
// prohibition word are matched against the hard-rule vocabulary; matched ones
// become deny patterns, unmatched prohibitions become advisory. Self-protection
// deny patterns are always included. Comments (#) and blank lines are ignored.
func CompileRules(lines []string) Policy {
	p := Policy{DisallowedTools: append([]string{}, selfProtection...)}
	seen := map[string]bool{}
	for _, d := range p.DisallowedTools {
		seen[d] = true
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
