package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/share2us/cli/internal/daemon"
)

const rulesTemplate = `# Share2Us agent rules (.s2u.rules) — plain-text dos and don'ts for prompts that
# other devices inject into this project's agent sessions (ADR-036).
#
# ENFORCED BY DEFAULT even with no rules here: push, delete (rm), and outbound
# network are blocked on injected runs, and an injected run can never edit these
# rules or the Claude settings. You do not need to write those lines.
#
# Add "don't"/"never"/"no" lines to block MORE. Ones s2u can map to a hard block
# are ENFORCED (the prompt cannot talk its way past them); the rest are ADVISORY
# (best-effort). Run "s2u agent rules" to see which is which.
#
# To OPT OUT of a default guardrail for this project, use an "allow" line:
#   allow push          # let injected runs push
#   allow network       # let injected runs reach the network
#   allow delete        # let injected runs delete files
# (Self-protection cannot be opted out of.)

# Examples — block more than the default:
never commit
don't touch the payment code
`

// setup writes a starter .s2u.rules for this project (or --global for the home
// one). It never overwrites an existing file.
func (a app) setup(args []string) int {
	global := false
	for _, arg := range args {
		if arg == "--global" {
			global = true
		} else {
			fmt.Fprintf(a.stderr, "unknown flag %q\n", arg)
			return 2
		}
	}
	path, err := rulesPath(global)
	if err != nil {
		return a.fail("resolve rules path", err)
	}
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(a.stdout, "%s already exists — leaving it as is.\nEdit it, then run `%s agent rules` to see what is hard-enforced.\n", path, commandName)
		return 0
	}
	if err := os.WriteFile(path, []byte(rulesTemplate), 0o644); err != nil {
		return a.fail("write .s2u.rules", err)
	}
	fmt.Fprintf(a.stdout, "Wrote %s.\nEdit it, then run `%s agent rules` to see which rules are hard-enforced vs advisory.\n", path, commandName)
	return 0
}

func rulesPath(global bool) (string, error) {
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".s2u.rules"), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, ".s2u.rules"), nil
}

// agentRules loads + compiles the rules that apply to a project and prints what
// is hard-enforced vs advisory, so the user knows exactly what a remote inject
// can and cannot do.
func (a app) agentRules(args []string) int {
	project, err := os.Getwd()
	if err != nil {
		project = ""
	}
	for i := 0; i < len(args); i++ {
		if args[i] == "--project" && i+1 < len(args) {
			i++
			project = args[i]
		}
	}
	policy := daemon.CompileRules(daemon.LoadRules(project))
	fmt.Fprintf(a.stdout, "Rules for %s (+ global ~/.s2u.rules)\n\n", project)
	fmt.Fprintln(a.stdout, "HARD (enforced on injected runs, cannot be overridden — includes the")
	fmt.Fprintln(a.stdout, "default baseline: push / delete / network, plus self-protection):")
	for _, d := range policy.DisallowedTools {
		fmt.Fprintf(a.stdout, "  deny  %s\n", d)
	}
	fmt.Fprintln(a.stdout, "\nADVISORY (best-effort, added to the prompt — NOT guaranteed):")
	if len(policy.Advisory) == 0 {
		fmt.Fprintln(a.stdout, "  (none)")
	}
	for _, adv := range policy.Advisory {
		fmt.Fprintf(a.stdout, "  - %s\n", adv)
	}
	fmt.Fprintln(a.stdout, "\nHow each tool enforces the HARD rules:")
	fmt.Fprintln(a.stdout, "  claude  permission denies    — the rule itself is enforced")
	fmt.Fprintln(a.stdout, "  gemini  policy-engine denies — the rule itself is enforced")
	fmt.Fprintln(a.stdout, "  codex   sandbox             — blocks writes outside the workspace and all")
	fmt.Fprintln(a.stdout, "                                network; individual rules are prompt-level only")
	return 0
}
