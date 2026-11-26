// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// codexSetupTips: Codex differs from Claude in ways users trip over (owner, 2026-11-26).
const codexSetupTips = `  Codex tips:
    - The skill is $s2u in Codex (type $s2u, not /s2u): e.g. $s2u agents, $s2u send homelab "check disk space".
    - Start Codex with "s2u codex" (or "s2u codex resume <id>") so tasks from other agents show up in
      the window. Otherwise they run in the background and only their replies are visible.
    - Make a Codex session reachable: type "! s2u agent bind" inside it.
    - Blocked actions ask you (email + portal) before they run, as with Claude.
`

// setup writes a starter .s2u.rules for this project (or --global for the home
// one). It never overwrites an existing file.
// setup is the guided first run: one command takes a machine from nothing to "my agents
// can reach each other", printing a ✓ per step and what (if anything) is left. Each step
// is idempotent, so running it again only reports. `--rules [--global]` keeps the old
// behaviour of writing a starter .s2u.rules.
func (a app) setup(ctx context.Context, args []string) int {
	rules, global := false, false
	for _, arg := range args {
		switch arg {
		case "--rules":
			rules = true
		case "--global":
			rules, global = true, true
		default:
			fmt.Fprintf(a.stderr, "unknown flag %q (usage: %s setup [--rules [--global]])\n", arg, commandName)
			return 2
		}
	}
	if rules {
		return a.setupRules(global)
	}
	ok := func(format string, v ...any) { fmt.Fprintf(a.stdout, "✓ "+format+"\n", v...) }
	todo := func(format string, v ...any) { fmt.Fprintf(a.stdout, "• "+format+"\n", v...) }

	// 1. Signed in.
	client, credential, signedIn := a.authClient()
	if !signedIn {
		if a.stdinIsTTY == nil || !a.stdinIsTTY(a.stdin) {
			todo("Sign in first: %s login   (then run %s setup again)", commandName, commandName)
			return 1
		}
		fmt.Fprintln(a.stdout, "Signing in (a browser opens to approve this device)…")
		if code := a.login(ctx, nil); code != 0 {
			return code
		}
		if client, credential, signedIn = a.authClient(); !signedIn {
			return 1
		}
	}
	ok("Signed in as %s", credential.Email)

	// 2. Background service (receives prompts, collects replies).
	a.ensureAgentReachable()
	if daemonRunning() {
		ok("Background service running")
	} else {
		todo("Background service isn't running: %s daemon start", commandName)
	}

	// 3. /s2u skill for the agent tools on this machine.
	installed := []string{}
	for _, t := range skillTargets {
		if !skillPresent(t) {
			continue
		}
		if _, err := installSkill(t); err == nil {
			installed = append(installed, t.Label)
		}
	}
	if len(installed) > 0 {
		ok("/s2u skill installed for %s", strings.Join(installed, ", "))
	}
	for _, label := range installed {
		if label == "Codex" {
			fmt.Fprint(a.stdout, codexSetupTips)
		}
	}

	// 4. The agent session this runs in, if any.
	if sess, err := daemon.FindOwnSession(ctx); err == nil {
		var out bytes.Buffer
		quiet := a
		quiet.stdout = &out
		if code := quiet.agentBind(ctx, nil); code == 0 {
			ok("This %s session is reachable as an agent (details: %s agent bindings)", sess.Tool, commandName)
			// Bind explains when prompts can't appear live; that part matters here.
			for _, line := range strings.Split(out.String(), "\n") {
				if strings.Contains(line, "wait") || strings.Contains(line, "s2u claude") ||
					strings.Contains(line, "codex") || strings.Contains(line, "$s2u") || strings.Contains(line, "background") {
					fmt.Fprintln(a.stdout, "  "+line)
				}
			}
		} else {
			fmt.Fprint(a.stdout, out.String())
		}
	} else {
		todo("To make an agent reachable, type `! %s setup` inside its Claude Code or Codex session (best started with `%s claude` or `%s codex`, so tasks show up in the window).", commandName, commandName, commandName)
	}

	// 5. Who it can talk to.
	if sessions, err := client.ListAgentSessions(ctx); err == nil {
		others := 0
		for _, s := range sessions {
			if s.DeviceID != credential.DeviceSessionID {
				others++
			}
		}
		if others > 0 {
			ok("%d agent(s) on your other machines can be reached", others)
			fmt.Fprintf(a.stdout, "\nTry it: %s agent send --to <name> --prompt \"ping: reply with pong\" --wait   (names: %s agent list)\n", commandName, commandName)
		} else {
			todo("No agents on your other machines yet: run `%s setup` there too.", commandName)
		}
	}
	fmt.Fprintf(a.stdout, "Sharenets (agents of other people): %s sharenet list\n", commandName)
	return 0
}

func (a app) setupRules(global bool) int {
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

// agentPolicy shows or sets an agent's privilege for one project (ADR-041 §6).
// This is the owner's act: the file it writes lives outside the project, so an
// injected run cannot promote itself by editing its own repo.
func (a app) agentPolicy(args []string) int {
	project, err := os.Getwd()
	if err != nil {
		project = ""
	}
	level := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--project" && i+1 < len(args):
			i++
			project = args[i]
		case strings.HasPrefix(args[i], "-"):
			fmt.Fprintf(a.stderr, "unknown flag %q\n", args[i])
			return 2
		default:
			level = args[i]
		}
	}
	if level == "" {
		priv := daemon.AgentPolicy(project, false)
		path, _ := daemon.EnforcedPolicyPath(project)
		fmt.Fprintf(a.stdout, "%s  %s\n", priv, project)
		fmt.Fprintf(a.stdout, "stored at: %s\n", path)
		fmt.Fprintf(a.stdout, "\nlevels:\n")
		fmt.Fprintf(a.stdout, "  restricted  read-only; the agent may look, not touch\n")
		fmt.Fprintf(a.stdout, "  standard    edit inside the workspace, no network (default)\n")
		fmt.Fprintf(a.stdout, "  privileged  network and pushes; for an agent meant to deploy\n")
		return 0
	}
	p := daemon.Privilege(strings.ToLower(level))
	if !p.Valid() {
		fmt.Fprintf(a.stderr, "unknown level %q; use restricted, standard or privileged\n", level)
		return 2
	}
	path, werr := daemon.WriteEnforcedPolicy(project, p)
	if werr != nil {
		return a.fail("write policy", werr)
	}
	fmt.Fprintf(a.stdout, "%s is now %s\n%s\n", project, p, path)
	if p == daemon.PrivilegePrivileged {
		fmt.Fprintf(a.stdout, "\nThis agent may now reach the network and push. Its baseline denies are gone;\nonly self-protection and any explicit `don't ...` lines in .s2u.rules remain.\n")
	}
	return 0
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
	priv := daemon.AgentPolicy(project, false)
	policy := daemon.CompileRules(daemon.LoadRules(project), priv)
	fmt.Fprintf(a.stdout, "Rules for %s (+ global ~/.s2u.rules)\n\n", project)
	fmt.Fprintf(a.stdout, "PRIVILEGE: %s", priv)
	switch priv {
	case daemon.PrivilegeRestricted:
		fmt.Fprintln(a.stdout, "  (read-only: claude plan / codex read-only / gemini plan)")
	case daemon.PrivilegePrivileged:
		fmt.Fprintln(a.stdout, "  (network + pushes allowed; baseline denies dropped)")
	default:
		fmt.Fprintln(a.stdout, "  (edit in the workspace, no network — the default)")
	}
	if path, err := daemon.EnforcedPolicyPath(project); err == nil {
		fmt.Fprintf(a.stdout, "  set with: %s agent policy --project %s <level>\n  stored at: %s\n", commandName, project, path)
	}
	fmt.Fprintln(a.stdout, "")
	if priv == daemon.PrivilegePrivileged {
		fmt.Fprintln(a.stdout, "HARD (enforced on injected runs, cannot be overridden. At `privileged` the")
		fmt.Fprintln(a.stdout, "baseline push / delete / network denies are DROPPED; self-protection stays):")
	} else {
		fmt.Fprintln(a.stdout, "HARD (enforced on injected runs, cannot be overridden — includes the")
		fmt.Fprintln(a.stdout, "default baseline: push / delete / network, plus self-protection):")
	}
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
