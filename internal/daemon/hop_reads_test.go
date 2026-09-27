// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"strings"
	"testing"
)

// Item 7 (owner, 2026-09-27): an unattended hop may run read-only commands, so a
// chained or piped read is not refused for want of an approval nobody can give.
func TestHopMayReadButNothingThatWritesOrRuns(t *testing.T) {
	p := CompileRules(nil, PrivilegeStandard)
	allowed := strings.Join(p.AllowedTools, " ")
	for _, want := range []string{"Bash(grep:*)", "Bash(cat:*)", "Bash(git status:*)"} {
		if !strings.Contains(allowed, want) {
			t.Errorf("missing %s in %v", want, p.AllowedTools)
		}
	}
	// Each of these has a flag that writes a file or runs a program, or (git -C)
	// would admit every git subcommand, push included.
	for _, never := range []string{"git diff", "git log", "git show", "git -C", "rg", "find", "sed", "tree", "file", "Bash(git:*)", "Bash(*)"} {
		for _, a := range p.AllowedTools {
			if a == never || strings.HasPrefix(a, "Bash("+never+":") || strings.HasPrefix(a, "Bash("+never+")") {
				t.Errorf("%s must not be allowlisted (got %s)", never, a)
			}
		}
	}
	if r := CompileRules(nil, PrivilegeRestricted); len(r.AllowedTools) != 0 {
		t.Errorf("restricted runs read-only in plan mode and needs no allowlist: %v", r.AllowedTools)
	}
}

// Whatever the rules compile to, the compiled policy never both allows and
// denies a pattern.
func TestAllowAndDenyStayDisjoint(t *testing.T) {
	for _, priv := range []Privilege{PrivilegeStandard, PrivilegePrivileged} {
		p := CompileRules([]string{"don't push", "never delete files", "do not use the network", "don't read secrets"}, priv)
		deny := map[string]bool{}
		for _, d := range p.DisallowedTools {
			deny[d] = true
		}
		for _, a := range p.AllowedTools {
			if deny[a] {
				t.Fatalf("%s: %s is both allowed and denied", priv, a)
			}
		}
	}
}

func TestHopArgsCarryReadsAndTheUnattendedNote(t *testing.T) {
	args := buildClaudeInjectArgs("s1", "the prompt", CompileRules(nil, PrivilegeStandard), "acceptEdits", false)
	joined := strings.Join(args, "\x00")
	if !strings.Contains(joined, "--allowedTools\x00Bash(ls:*)") {
		t.Fatalf("no allowlist in %q", args)
	}
	if args[len(args)-2] != "-p" || args[len(args)-1] != "the prompt" {
		t.Fatalf("-p must close the variadic lists: %q", args)
	}
	i := strings.Index(joined, "--append-system-prompt\x00")
	if i < 0 || !strings.Contains(joined[i:], "nobody is at the keyboard") {
		t.Fatalf("hop note missing: %q", args)
	}
}
