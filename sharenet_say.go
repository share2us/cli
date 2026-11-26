// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli/internal/daemon"
)

// sharenetSay is `s2u sharenet <net>[.<project>] say <text> [--reply ID] [--as-me]`: post a
// message in a channel (ADR-053 Phase 2). Inside an agent's session (S2U_AGENT_ID set) it
// posts as that agent, which needs can_post in the project; --as-me posts as the person.
// A Secure ShareNet's message is sealed on this device; the server never sees the words.
func (a app) sharenetSay(ctx context.Context, client *clicore.Client, n clicore.SharenetSummary, projectName string, args []string) int {
	var parent, editID, deleteID int64
	asMe := false
	var words []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--reply":
			if i+1 >= len(args) {
				return a.sharenetUsage(2)
			}
			v, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil || v <= 0 {
				return a.sharenetUsage(2)
			}
			parent, i = v, i+1
		case "--edit", "--delete":
			if i+1 >= len(args) {
				return a.sharenetUsage(2)
			}
			v, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil || v <= 0 {
				return a.sharenetUsage(2)
			}
			if args[i] == "--edit" {
				editID = v
			} else {
				deleteID = v
			}
			i++
		case "--as-me":
			asMe = true
		default:
			words = append(words, args[i])
		}
	}
	if deleteID > 0 {
		if err := client.DeleteSharenetMessage(ctx, n.ID, deleteID); err != nil {
			return a.fail("delete the message", err)
		}
		fmt.Fprintf(a.stdout, "✓ Deleted message %d in %s.\n", deleteID, n.Name)
		return 0
	}
	text := strings.TrimSpace(strings.Join(words, " "))
	if text == "" {
		fmt.Fprintf(a.stderr, "usage: %s sharenet %s[.<project>] say <text> [--reply ID] [--as-me] | say --edit ID <text> | say --delete ID\n", commandName, n.Name)
		return 2
	}
	// The words leave this device in a standard sharenet; never a credential.
	if res, err := clicore.ScanBytesForSecrets([]byte(text), clicore.SecretScanOptions{}); err == nil && len(res.Findings) > 0 {
		fmt.Fprintln(a.stderr, "Not sent: the message looks like it contains a secret (a key or token). Remove it and try again.")
		return 1
	}
	m := clicore.SharenetMessage{Channel: "general", Body: text, ParentID: parent}
	where := "#general"
	if projectName != "" && !strings.EqualFold(projectName, "general") {
		p, err := a.resolveProject(ctx, client, n, projectName)
		if err != nil {
			fmt.Fprintln(a.stderr, cliErrorMessage(err))
			return 1
		}
		m.Channel, where = p.ID, "#"+p.Name
		if agent := strings.TrimSpace(os.Getenv(daemon.AgentEnvVar)); agent != "" && !asMe {
			m.AgentID = agent
		}
	}
	if n.Secure {
		sn, _, err := a.secureSession(ctx)
		if err != nil {
			return a.fail("open the Secure ShareNet keys", err)
		}
		m.Mentions = mentionedNames(text) // the server can't read a sealed body to find them
		if err := sn.SealMessage(ctx, n.ID, &m); err != nil {
			return a.fail("encrypt the message", err)
		}
	}
	if editID > 0 {
		m.AgentID, m.ParentID = "", 0
		if err := client.EditSharenetMessage(ctx, n.ID, editID, m); err != nil {
			return a.fail("edit the message", err)
		}
		fmt.Fprintf(a.stdout, "✓ Edited message %d in %s %s.\n", editID, n.Name, where)
		return 0
	}
	id, err := client.PostSharenetMessage(ctx, n.ID, m)
	if err != nil {
		if strings.Contains(err.Error(), "agent_post_forbidden") {
			fmt.Fprintf(a.stderr, "This agent may not post in %s.%s: its owner can allow it in the portal (the project's agents, \"Can post\"), or post as yourself with --as-me.\n", n.Name, strings.TrimPrefix(where, "#"))
			return 1
		}
		return a.fail("post the message", err)
	}
	who := "you"
	if m.AgentID != "" {
		who = "agent " + m.AgentID
	}
	fmt.Fprintf(a.stdout, "✓ Posted in %s %s as %s (message %d).\n", n.Name, where, who, id)
	return 0
}

// mentionedNames: @usernames in a message, for Secure messages whose body the server can't read.
func mentionedNames(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.Fields(text) {
		if !strings.HasPrefix(f, "@") || len(f) < 3 {
			continue
		}
		name := strings.ToLower(strings.TrimRight(strings.TrimPrefix(f, "@"), ".,:;!?)"))
		if name != "" && !seen[name] && len(out) < 20 {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}
