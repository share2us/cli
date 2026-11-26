// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	clicore "github.com/share2us/cli-core"
)

// sharenetActivity is `s2u sharenet <net>[.<project>] activity [--since ID] [--limit N] [--json]`:
// a channel's timeline (ADR-053), oldest first. Without a project it shows every channel.
// --since prints only what came after an event id, so an agent can follow a channel.
func (a app) sharenetActivity(ctx context.Context, client *clicore.Client, n clicore.SharenetSummary, projectName string, args []string) int {
	q := clicore.SharenetEventsQuery{Channel: "all", Limit: 30}
	asJSON := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			asJSON = true
		case "--since", "--limit":
			if i+1 >= len(args) {
				return a.sharenetUsage(2)
			}
			v, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil || v < 0 {
				return a.sharenetUsage(2)
			}
			if args[i] == "--since" {
				q.After = v
			} else {
				q.Limit = int(v)
			}
			i++
		default:
			return a.sharenetUsage(2)
		}
	}
	channel := "all channels"
	if projectName != "" {
		if strings.EqualFold(projectName, "general") {
			q.Channel, channel = "general", "#general"
		} else {
			p, err := a.resolveProject(ctx, client, n, projectName)
			if err != nil {
				fmt.Fprintln(a.stderr, cliErrorMessage(err))
				return 1
			}
			q.Channel, channel = p.ID, "#"+p.Name
		}
	}
	events, _, err := client.ListSharenetEvents(ctx, n.ID, q)
	if err != nil {
		return a.fail("read the activity", err)
	}
	if q.After == 0 { // the newest page comes newest first
		for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
			events[i], events[j] = events[j], events[i]
		}
	}
	if asJSON {
		out, _ := json.MarshalIndent(map[string]any{"events": events}, "", "  ")
		fmt.Fprintln(a.stdout, string(out))
		return 0
	}
	if len(events) == 0 {
		fmt.Fprintf(a.stdout, "Nothing new in %s, %s.\n", n.Name, channel)
		return 0
	}
	fmt.Fprintf(a.stdout, "%s, %s:\n", n.Name, channel)
	for _, e := range events {
		where := ""
		if q.Channel == "all" {
			where = "#general "
			if e.ProjectName != "" {
				where = "#" + e.ProjectName + " "
			}
		}
		fmt.Fprintf(a.stdout, "%6d  %s  %s%s\n", e.ID, e.CreatedAt.Local().Format("Jan 02 15:04"), where, describeEvent(e))
	}
	fmt.Fprintf(a.stdout, "\nNewer than this: %s sharenet %s activity --since %d\n", commandName, n.Name, events[len(events)-1].ID)
	return 0
}

// describeEvent is one timeline line in plain words.
func describeEvent(e clicore.SharenetEvent) string {
	who := e.ActorName
	agent := e.ActorAgent
	switch e.Kind {
	case "task":
		if e.Task == nil {
			return who + " sent a task"
		}
		line := fmt.Sprintf("%s's %s asked %s: %s", who, firstNonEmptyStr(agent, "agent"), e.Task.Target, e.Task.Status)
		if r := strings.TrimSpace(e.Task.Reply); r != "" {
			first, _, _ := strings.Cut(r, "\n")
			if len([]rune(first)) > 140 {
				first = string([]rune(first)[:140]) + "…"
			}
			line += " | " + first
		}
		return line
	case "file.posted":
		name := "a file"
		if e.File != nil && e.File.Name != "" {
			name = e.File.Name
		} else if e.File != nil && e.File.Secure {
			name = "an encrypted file"
		}
		if agent != "" {
			return fmt.Sprintf("%s's %s posted %s", who, agent, name)
		}
		return who + " posted " + name
	}
	phrases := map[string]string{
		"sharenet.created": "created the sharenet", "member.invited": "invited someone", "member.joined": "joined",
		"member.removed": "removed a member", "member.left": "left", "owner.transferred": "handed over ownership",
		"project.created": "created the project", "project.renamed": "renamed the project", "project.deleted": "deleted a project",
		"agent.invited": "invited agent " + agent, "agent.added": "added agent " + agent, "agent.removed": "removed an agent",
		"join.approved": "approved a join request (" + agent + ")", "join.rejected": "rejected a join request",
		"file.deleted": "deleted a file", "settings.changed": "changed the settings", "message": "said: " + e.Body,
	}
	if p, ok := phrases[e.Kind]; ok {
		return who + " " + p
	}
	return who + ": " + e.Kind
}

func firstNonEmptyStr(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
