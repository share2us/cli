package daemon

import (
	"slices"
	"strings"
	"testing"
)

func denies(p Policy, pat string) bool { return slices.Contains(p.DisallowedTools, pat) }

func TestCompileRulesHardVsAdvisory(t *testing.T) {
	p := CompileRules([]string{
		"# my project rules",
		"Never push to git",
		"Don't force push",
		"Do not delete any files",
		"no network access from the agent",
		"don't refactor the auth module", // fuzzy -> advisory
		"always write tests",             // allowance -> ignored
		"",
	})
	if !denies(p, "Bash(git push:*)") {
		t.Errorf("push should compile to a hard deny; got %v", p.DisallowedTools)
	}
	if !denies(p, "Bash(git push --force:*)") {
		t.Errorf("force push should add a force deny; got %v", p.DisallowedTools)
	}
	if !denies(p, "Bash(rm:*)") {
		t.Errorf("delete should deny rm; got %v", p.DisallowedTools)
	}
	if !denies(p, "Bash(curl:*)") || !denies(p, "Bash(wget:*)") {
		t.Errorf("network should deny curl/wget; got %v", p.DisallowedTools)
	}
	// self-protection is always present.
	if !denies(p, "Edit(**/.s2u.rules)") || !denies(p, "Edit(**/.claude/settings.json)") {
		t.Errorf("self-protection denies missing; got %v", p.DisallowedTools)
	}
	// fuzzy rule is advisory, not a deny; allowance is neither.
	if len(p.Advisory) != 1 || !strings.Contains(p.Advisory[0], "refactor the auth") {
		t.Fatalf("advisory = %v, want the auth-refactor line only", p.Advisory)
	}
}

func TestCompileRulesNoDupes(t *testing.T) {
	p := CompileRules([]string{"never push", "do not push either"})
	n := 0
	for _, d := range p.DisallowedTools {
		if d == "Bash(git push:*)" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("git push deny appeared %d times, want deduped to 1", n)
	}
}

func TestAppendSystemPrompt(t *testing.T) {
	empty := Policy{}
	if empty.AppendSystemPrompt() != "" {
		t.Error("no advisory -> empty system prompt")
	}
	p := Policy{Advisory: []string{"don't touch prod config"}}
	sp := p.AppendSystemPrompt()
	if !strings.Contains(sp, "don't touch prod config") || !strings.Contains(sp, "enforced separately") {
		t.Fatalf("system prompt = %q", sp)
	}
}

func TestBuildClaudeInjectArgs(t *testing.T) {
	p := Policy{DisallowedTools: []string{"Bash(git push:*)", "Bash(rm:*)"}, Advisory: []string{"be careful"}}
	args := buildClaudeInjectArgs("sess-1", "do the thing", p, claudeMode(false))
	joined := strings.Join(args, " ")
	// resume + restricted mode, never bypass.
	if !strings.Contains(joined, "--resume sess-1") || !strings.Contains(joined, "--permission-mode acceptEdits") {
		t.Fatalf("args missing resume/mode: %v", args)
	}
	if strings.Contains(joined, "bypassPermissions") || strings.Contains(joined, "dangerously") {
		t.Fatalf("must never bypass permissions: %v", args)
	}
	if !strings.Contains(joined, "--fork-session") {
		t.Fatalf("a live session can only be injected via a fork: %v", args)
	}
	if !strings.Contains(joined, "--disallowedTools Bash(git push:*) Bash(rm:*)") {
		t.Fatalf("disallowedTools not passed as a bounded variadic: %v", args)
	}
	if !strings.Contains(joined, "--append-system-prompt") {
		t.Fatalf("advisory should append a system prompt: %v", args)
	}
	// -p must come last so it bounds the variadic --disallowedTools.
	if args[len(args)-2] != "-p" || args[len(args)-1] != "do the thing" {
		t.Fatalf("-p <prompt> must be last to bound --disallowedTools: %v", args)
	}
}

func TestStrictModesAreReadOnlyAndNeverBypass(t *testing.T) {
	// permissive defaults
	if claudeMode(false) != "acceptEdits" || codexSandbox(false) != "workspace-write" || geminiApproval(false) != "auto_edit" {
		t.Fatalf("default modes changed: %s/%s/%s", claudeMode(false), codexSandbox(false), geminiApproval(false))
	}
	// --agent-strict => read-only across all three
	if claudeMode(true) != "plan" || codexSandbox(true) != "read-only" || geminiApproval(true) != "plan" {
		t.Fatalf("strict modes wrong: %s/%s/%s", claudeMode(true), codexSandbox(true), geminiApproval(true))
	}
	// no mode may ever be a bypass
	for _, m := range []string{claudeMode(false), claudeMode(true), codexSandbox(false), codexSandbox(true), geminiApproval(false), geminiApproval(true)} {
		if strings.Contains(m, "bypass") || strings.Contains(m, "yolo") || strings.Contains(m, "danger") {
			t.Fatalf("mode %q is a bypass", m)
		}
	}
}

func TestSelfProtectionUsesEditNotWrite(t *testing.T) {
	// Claude ignores Write(path) deny rules; only Edit(path) is enforced. A
	// Write(...) rule here would be a silent no-op — i.e. no self-protection.
	p := CompileRules(nil)
	for _, d := range p.DisallowedTools {
		if strings.HasPrefix(d, "Write(") {
			t.Fatalf("Write(...) deny is a no-op in Claude; use Edit(...): %q", d)
		}
	}
	if !denies(p, "Edit(**/.s2u.rules)") {
		t.Fatalf("self-protection missing: %v", p.DisallowedTools)
	}
}

func TestBaselineIsEnforcedWithNoRulesFile(t *testing.T) {
	// The default state (no .s2u.rules) must NOT be wide open.
	p := CompileRules(nil)
	for _, must := range []string{"Bash(git push:*)", "Bash(rm:*)", "Bash(curl:*)", "Edit(**/.s2u.rules)"} {
		if !denies(p, must) {
			t.Fatalf("baseline missing %q — default would be unrestricted: %v", must, p.DisallowedTools)
		}
	}
}

func TestBaselineOptOut(t *testing.T) {
	p := CompileRules([]string{"allow push", "# but nothing else"})
	if denies(p, "Bash(git push:*)") {
		t.Fatal("explicit `allow push` should opt out of the push baseline")
	}
	// other baseline items stay enforced
	if !denies(p, "Bash(rm:*)") || !denies(p, "Bash(curl:*)") {
		t.Fatalf("opting out of push must not drop other baselines: %v", p.DisallowedTools)
	}
}

func TestSelfProtectionCannotBeOptedOut(t *testing.T) {
	// No phrasing may disable the guardrails' own protection.
	for _, line := range []string{"allow editing .s2u.rules", "allow rules", "permit settings edit", "enable everything"} {
		p := CompileRules([]string{line})
		if !denies(p, "Edit(**/.s2u.rules)") || !denies(p, "Edit(**/.claude/settings.json)") {
			t.Fatalf("self-protection was removed by %q: %v", line, p.DisallowedTools)
		}
	}
}

// Codex and Gemini have no deny layer, so the compiled rules must survive into
// the prompt — otherwise .s2u.rules silently applied to Claude only.
func TestPromptPreambleCarriesRulesForToolsWithoutDenyLayer(t *testing.T) {
	p := CompileRules([]string{"never touch the payment code"})
	got := p.PromptPreamble()
	if got == "" {
		t.Fatal("baseline guardrails must always produce a preamble")
	}
	if !strings.Contains(got, "run `git push`") {
		t.Errorf("baseline push deny must be humanized into the preamble:\n%s", got)
	}
	if !strings.Contains(got, "edit **/.s2u.rules") {
		t.Errorf("self-protection must be stated in the preamble:\n%s", got)
	}
	if !strings.Contains(got, "never touch the payment code") {
		t.Errorf("advisory rules must ride along:\n%s", got)
	}
}

func TestHumanizeDeny(t *testing.T) {
	for pattern, want := range map[string]string{
		"Bash(git push:*)":    "run `git push`",
		"Edit(**/.s2u.rules)": "edit **/.s2u.rules",
		"WeirdPattern":        "WeirdPattern",
	} {
		if got := humanizeDeny(pattern); got != want {
			t.Errorf("humanizeDeny(%q) = %q, want %q", pattern, got, want)
		}
	}
}
