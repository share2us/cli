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
	if !denies(p, "Edit(**/.s2u.rules)") || !denies(p, "Write(**/.claude/settings.json)") {
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
	args := buildClaudeInjectArgs("sess-1", "do the thing", p)
	joined := strings.Join(args, " ")
	// resume + restricted mode, never bypass.
	if !strings.Contains(joined, "--resume sess-1") || !strings.Contains(joined, "--permission-mode acceptEdits") {
		t.Fatalf("args missing resume/mode: %v", args)
	}
	if strings.Contains(joined, "bypassPermissions") || strings.Contains(joined, "dangerously") {
		t.Fatalf("must never bypass permissions: %v", args)
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
