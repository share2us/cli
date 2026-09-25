// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	clicore "github.com/share2us/cli-core"
)

// Goals (ADR-041 §4): a unit of autonomous work with a budget. Agents hand work
// to each other inside one, every hop is counted against it, and it stops when
// the budget runs out rather than when someone notices.

func (a app) agentGoal(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return a.agentGoalUsage()
	}
	switch args[0] {
	case "new", "create":
		return a.agentGoalNew(ctx, args[1:])
	case "list", "ls":
		return a.agentGoalList(ctx, args[1:])
	case "show", "status":
		return a.agentGoalShow(ctx, args[1:])
	case "close":
		return a.agentGoalClose(ctx, args[1:])
	case "wait", "hold":
		return a.agentGoalState(ctx, args[1:])
	default:
		return a.agentGoalUsage()
	}
}

func (a app) agentGoalUsage() int {
	fmt.Fprintf(a.stderr, "usage: %s agent goal <new|list|show|close|wait>\n", commandName)
	fmt.Fprintf(a.stderr, "  new --objective \"...\" --hops N --time DURATION [--project DIR]\n")
	fmt.Fprintf(a.stderr, "      [--acceptance \"...\"] [--branch NAME] [--risk 1-5]\n")
	fmt.Fprintf(a.stderr, "  list [--all]                       live goals, or every goal with --all\n")
	fmt.Fprintf(a.stderr, "  show <goal-id>                     objective, state, and budget spent\n")
	fmt.Fprintf(a.stderr, "  close <goal-id> --state completed|failed|canceled [--reason \"...\"]\n")
	fmt.Fprintf(a.stderr, "      [--evidence \"...\"]             completing REQUIRES evidence: the command\n")
	fmt.Fprintf(a.stderr, "                                     that was run and its output\n")
	fmt.Fprintf(a.stderr, "  wait <goal-id> [--for input|auth|working]   record that it is waiting for a human\n")
	fmt.Fprintf(a.stderr, "\nBoth ceilings are required on `new`. A goal without a budget is unbounded\n")
	fmt.Fprintf(a.stderr, "work, so there is no default to fall back on.\n")
	return 2
}

func (a app) agentGoalNew(ctx context.Context, args []string) int {
	project, _ := os.Getwd()
	var objective, acceptance, branch, timeCap string
	hops := 0
	risk := 0
	for i := 0; i < len(args); i++ {
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch args[i] {
		case "--project":
			project = next()
		case "--objective", "--goal":
			objective = next()
		case "--acceptance", "--done-when":
			acceptance = next()
		case "--branch":
			branch = next()
		case "--hops", "--hop-cap":
			hops, _ = strconv.Atoi(next())
		case "--time", "--time-cap":
			timeCap = next()
		case "--risk":
			risk, _ = strconv.Atoi(next())
		default:
			fmt.Fprintf(a.stderr, "unknown flag %q\n", args[i])
			return 2
		}
	}
	if strings.TrimSpace(objective) == "" {
		fmt.Fprintln(a.stderr, "an --objective is required: what are the agents trying to achieve?")
		return 2
	}
	if hops <= 0 || timeCap == "" {
		fmt.Fprintln(a.stderr, "both --hops and --time are required; a goal with no ceiling is unbounded work")
		fmt.Fprintf(a.stderr, "  e.g. %s agent goal new --objective \"fix the flaky test\" --hops 20 --time 2h\n", commandName)
		return 2
	}
	d, err := time.ParseDuration(timeCap)
	if err != nil || d <= 0 {
		fmt.Fprintf(a.stderr, "--time must be a duration like 90m or 2h (got %q)\n", timeCap)
		return 2
	}

	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	g, err := client.CreateGoal(ctx, clicore.NewGoal{
		Project:     project,
		Objective:   objective,
		Acceptance:  acceptance,
		Branch:      branch,
		HopCap:      int32(hops),
		TimeCapSecs: int64(d / time.Second),
		RiskCeiling: int16(risk),
	})
	if err != nil {
		return a.fail("create goal", err)
	}
	fmt.Fprintf(a.stdout, "%s\n%s\n", g.ID, g.Objective)
	fmt.Fprintf(a.stdout, "budget: %d hops, until %s\n", g.HopCap, g.ExpiresAt)
	fmt.Fprintf(a.stdout, "\nHand work to an agent inside it:\n  %s agent send --device ID --session ID --prompt \"...\" --goal %s\n", commandName, g.ID)
	return 0
}

func (a app) agentGoalList(ctx context.Context, args []string) int {
	live := true
	for _, arg := range args {
		if arg == "--all" {
			live = false
		} else {
			fmt.Fprintf(a.stderr, "unknown flag %q\n", arg)
			return 2
		}
	}
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	goals, err := client.ListGoals(ctx, live, 50)
	if err != nil {
		return a.fail("list goals", err)
	}
	if len(goals) == 0 {
		if live {
			fmt.Fprintf(a.stdout, "No goals running. Start one: %s agent goal new --objective \"...\" --hops N --time 2h\n", commandName)
		} else {
			fmt.Fprintln(a.stdout, "No goals yet.")
		}
		return 0
	}
	for _, g := range goals {
		fmt.Fprintf(a.stdout, "%s  %-16s  %-6d  %s\n", shorten(g.ID), g.State, g.HopCap, truncate(oneLine(g.Objective), 60))
	}
	return 0
}

func (a app) agentGoalShow(ctx context.Context, args []string) int {
	if len(args) != 1 {
		fmt.Fprintf(a.stderr, "usage: %s agent goal show <goal-id>\n", commandName)
		return 2
	}
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	g, err := client.GetGoal(ctx, args[0])
	if err != nil {
		return a.fail("read goal", err)
	}
	fmt.Fprintf(a.stdout, "%s\n%s\n\n", g.ID, g.Objective)
	fmt.Fprintf(a.stdout, "state:      %s\n", g.State)
	fmt.Fprintf(a.stdout, "project:    %s\n", g.Project)
	if g.Acceptance != "" {
		fmt.Fprintf(a.stdout, "done when:  %s\n", g.Acceptance)
	}
	fmt.Fprintf(a.stdout, "budget:     %d of %d hops used, until %s\n", g.HopsUsed, g.HopCap, g.ExpiresAt)
	if g.CloseReason != "" {
		fmt.Fprintf(a.stdout, "closed:     %s — %s\n", g.ClosedAt, g.CloseReason)
	}
	if g.CloseEvidence != "" {
		fmt.Fprintf(a.stdout, "\nevidence:\n%s\n", g.CloseEvidence)
	} else if !g.Live {
		// Saying so is the point of the verified/asserted distinction: a closed
		// goal with nothing behind it is a claim, not a result.
		fmt.Fprintln(a.stdout, "\nno evidence was recorded for this outcome (asserted, not verified)")
	}
	return 0
}

func (a app) agentGoalClose(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(a.stderr, "usage: %s agent goal close <goal-id> --state completed|failed|canceled [--reason ...] [--evidence ...]\n", commandName)
		return 2
	}
	id := args[0]
	var state, reason, evidence string
	for i := 1; i < len(args); i++ {
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch args[i] {
		case "--state":
			state = strings.ToLower(next())
		case "--reason":
			reason = next()
		case "--evidence":
			evidence = next()
		default:
			fmt.Fprintf(a.stderr, "unknown flag %q\n", args[i])
			return 2
		}
	}
	switch state {
	case "completed", "failed", "canceled":
	default:
		fmt.Fprintln(a.stderr, "--state must be completed, failed or canceled")
		return 2
	}
	// Caught here as well as on the server, so the person gets the reason rather
	// than a 400 — and the reason is the interesting part.
	if state == "completed" && strings.TrimSpace(evidence) == "" {
		fmt.Fprintln(a.stderr, "completing a goal requires --evidence: the command that was run and its output.")
		fmt.Fprintln(a.stderr, "A completion is a claim about the world; failing or cancelling is not, and needs none.")
		return 2
	}
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	g, err := client.CloseGoal(ctx, id, state, reason, evidence)
	if err != nil {
		return a.fail("close goal", err)
	}
	fmt.Fprintf(a.stdout, "%s is %s (%d hops used)\n", shorten(g.ID), g.State, g.HopsUsed)
	return 0
}

func (a app) agentGoalState(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(a.stderr, "usage: %s agent goal wait <goal-id> [--for input|auth|working]\n", commandName)
		return 2
	}
	id := args[0]
	state := "input_required"
	for i := 1; i < len(args); i++ {
		if args[i] == "--for" && i+1 < len(args) {
			i++
			switch args[i] {
			case "input":
				state = "input_required"
			case "auth":
				state = "auth_required"
			case "working":
				state = "working"
			default:
				fmt.Fprintln(a.stderr, "--for must be input, auth or working")
				return 2
			}
		} else {
			fmt.Fprintf(a.stderr, "unknown flag %q\n", args[i])
			return 2
		}
	}
	client, ok := a.agentClient()
	if !ok {
		return 1
	}
	g, err := client.SetGoalState(ctx, id, state)
	if err != nil {
		return a.fail("set goal state", err)
	}
	fmt.Fprintf(a.stdout, "%s is %s\n", shorten(g.ID), g.State)
	return 0
}

// oneLine flattens an objective for a listing row. main.go's truncate() shortens
// it; this is only about newlines, which would otherwise break the column layout.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
