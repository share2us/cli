// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli/internal/daemon"
)

// Sharenet management from the CLI (owner, 2026-11-26: full CLI management; it was
// portal-only from 2026-09-26). The server applies the same role checks as the portal.
// Changes to who is in a sharenet ask for confirmation on a terminal and need --yes
// otherwise, so an agent can't add or remove people unless its user said so explicitly.

// manageClient is the signed-in client, or an error message already printed.
func (a app) manageClient() (*clicore.Client, bool) {
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return nil, false
	}
	if clicore.EnvAPIToken() != "" {
		fmt.Fprintln(a.stderr, "managing sharenets needs a signed-in device, not SHARE2US_API_TOKEN")
		return nil, false
	}
	return client, true
}

// confirmChange asks before changing membership. Non-interactive runs (agents, scripts)
// must pass --yes.
func (a app) confirmChange(yes bool, what string) bool {
	if yes {
		return true
	}
	if a.stdinIsTTY == nil || !a.stdinIsTTY(a.stdin) {
		fmt.Fprintf(a.stderr, "%s: add --yes to confirm (asked on a terminal otherwise)\n", what)
		return false
	}
	fmt.Fprintf(a.stderr, "%s? [y/N] ", what)
	line, _ := bufio.NewReader(a.input()).ReadString('\n')
	return isAffirmative(line)
}

// splitNetProject reads "sharenet" or "sharenet.project".
func splitNetProject(s string) (net, project string) {
	if i := strings.Index(s, "."); i > 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// resolveProject finds a project in a sharenet by name or id; with no name, the
// sharenet's only project.
func (a app) resolveProject(ctx context.Context, client *clicore.Client, n clicore.SharenetSummary, q string) (clicore.SharenetProject, error) {
	projects, err := client.ListSharenetProjects(ctx, n.ID)
	if err != nil {
		return clicore.SharenetProject{}, err
	}
	if q == "" {
		switch len(projects) {
		case 1:
			return projects[0], nil
		case 0:
			return clicore.SharenetProject{}, fmt.Errorf("sharenet %q has no projects yet: %s sharenet %s project create <name>", n.Name, commandName, n.Name)
		}
		names := make([]string, 0, len(projects))
		for _, p := range projects {
			names = append(names, p.Name)
		}
		return clicore.SharenetProject{}, fmt.Errorf("sharenet %q has several projects (%s): name one, as %s.<project>", n.Name, strings.Join(names, ", "), n.Name)
	}
	for _, p := range projects {
		if p.ID == q || strings.EqualFold(p.Name, q) {
			return p, nil
		}
	}
	return clicore.SharenetProject{}, fmt.Errorf("no project %q in sharenet %q", q, n.Name)
}

// sharenetCreate: `s2u sharenet create <name> [--secure] [--project NAME] [--add-my-agents] [--can-post]`.
// --add-my-agents adds the agents bound on this machine to the first project.
func (a app) sharenetCreate(ctx context.Context, args []string) int {
	var name, project string
	var secure, addMine, canPost bool
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--secure":
			secure = true
		case "--add-my-agents":
			addMine = true
		case "--can-post":
			canPost = true
		case "--project":
			if i+1 < len(args) {
				project, i = args[i+1], i+1
			}
		default:
			if name != "" || strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(a.stderr, "usage: %s sharenet create <name> [--secure] [--project NAME] [--add-my-agents] [--can-post]\n", commandName)
				return 2
			}
			name = args[i]
		}
	}
	if name == "" {
		fmt.Fprintf(a.stderr, "usage: %s sharenet create <name> [--secure] [--project NAME] [--add-my-agents] [--can-post]\n", commandName)
		return 2
	}
	client, ok := a.manageClient()
	if !ok {
		return 1
	}
	n, err := client.CreateSharenet(ctx, name, secure)
	if err != nil {
		return a.fail("create sharenet", err)
	}
	fmt.Fprintf(a.stdout, "✓ Created sharenet %q\n", n.Name)
	if secure {
		fmt.Fprintf(a.stdout, "  Secure (end-to-end): members need account keys; set yours up with `%s keys enrol` if you haven't.\n", commandName)
	}
	if project == "" && addMine {
		project = "General"
	}
	if project == "" {
		fmt.Fprintf(a.stdout, "Next: %s sharenet %s project create <name>\n", commandName, n.Name)
		return 0
	}
	p, err := client.CreateSharenetProject(ctx, n.ID, project)
	if err != nil {
		return a.fail("create project", err)
	}
	fmt.Fprintf(a.stdout, "✓ Created project %q\n", p.Name)
	if addMine {
		ids := localAgentIDs()
		if len(ids) == 0 {
			fmt.Fprintf(a.stdout, "  No agents are bound on this machine yet (run `%s setup` inside an agent session), so none were added.\n", commandName)
		} else if code := a.addAgents(ctx, client, n, p, ids, canPost); code != 0 {
			return code
		}
	}
	fmt.Fprintf(a.stdout, "Invite people: %s sharenet %s invite <email>   Agents of others: %s sharenet %s code\n", commandName, n.Name, commandName, n.Name)
	return 0
}

// localAgentIDs are the agent ids bound on this machine.
func localAgentIDs() []string {
	list, err := daemon.LoadBindings()
	if err != nil {
		return nil
	}
	var ids []string
	for _, b := range list {
		if b.AgentID != "" {
			ids = append(ids, b.AgentID)
		}
	}
	return ids
}

func (a app) addAgents(ctx context.Context, client *clicore.Client, n clicore.SharenetSummary, p clicore.SharenetProject, ids []string, canPost bool) int {
	res, err := client.AddMyAgents(ctx, p.ID, ids, "", canPost)
	if err != nil {
		return a.fail("add agents", err)
	}
	failed := 0
	for _, r := range res {
		switch {
		case r.Error == "already_member":
			fmt.Fprintf(a.stdout, "  %s is already in %s\n", r.AgentID, p.Name)
		case r.Error != "":
			fmt.Fprintf(a.stdout, "  %s: %s\n", r.AgentID, r.Error)
			failed++
		default:
			post := ""
			if r.CanPost {
				post = " (may post files)"
			}
			fmt.Fprintf(a.stdout, "✓ Added %s to %s.%s%s\n", r.AgentID, n.Name, p.Name, post)
		}
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// sharenetManage runs `s2u sharenet <net>[.<project>] <command> ...`.
func (a app) sharenetManage(ctx context.Context, target, cmd string, args []string) int {
	client, ok := a.manageClient()
	if !ok {
		return 1
	}
	netName, projectName := splitNetProject(target)
	n, err := a.resolveSharenet(ctx, client, netName)
	if err != nil {
		fmt.Fprintln(a.stderr, cliErrorMessage(err))
		return 1
	}
	yes := hasFlag(args, "--yes", "-y")
	args = withoutFlags(args, "--yes", "-y")
	switch cmd {
	case "members":
		return a.sharenetMembers(ctx, client, n)
	case "invite":
		return a.sharenetInvite(ctx, client, n, args, yes)
	case "remove":
		return a.sharenetRemove(ctx, client, n, args, yes)
	case "project", "projects":
		if len(args) >= 2 && args[0] == "create" {
			p, err := client.CreateSharenetProject(ctx, n.ID, strings.Join(args[1:], " "))
			if err != nil {
				return a.fail("create project", err)
			}
			fmt.Fprintf(a.stdout, "✓ Created project %q in %s. Add your agents: %s agent add <agent> %s.%s\n", p.Name, n.Name, commandName, n.Name, p.Name)
			return 0
		}
		return a.sharenetShow(ctx, n.ID)
	case "code":
		return a.sharenetCode(ctx, client, n, projectName, args)
	case "activity":
		return a.sharenetActivity(ctx, client, n, projectName, args)
	case "say":
		return a.sharenetSay(ctx, client, n, projectName, args)
	case "dm":
		return a.sharenetDM(ctx, client, n, projectName, args)
	case "requests", "approve", "reject":
		p, err := a.resolveProject(ctx, client, n, projectName)
		if err != nil {
			fmt.Fprintln(a.stderr, cliErrorMessage(err))
			return 1
		}
		if cmd == "requests" {
			return a.sharenetRequests(ctx, client, n, p)
		}
		if len(args) != 1 {
			fmt.Fprintf(a.stderr, "usage: %s sharenet %s %s <request-id> [--yes]\n", commandName, target, cmd)
			return 2
		}
		if !a.confirmChange(yes, cmd+" join request "+args[0]+" in "+n.Name+"."+p.Name) {
			return 1
		}
		if err := client.DecideJoinRequest(ctx, p.ID, args[0], cmd == "approve"); err != nil {
			return a.fail(cmd, err)
		}
		if cmd == "approve" {
			fmt.Fprintln(a.stdout, "✓ Approved: the requester is a contributor and their agent is in the project.")
		} else {
			fmt.Fprintln(a.stdout, "✓ Rejected.")
		}
		return 0
	}
	return a.sharenetUsage(2)
}

func withoutFlags(args []string, flags ...string) []string {
	out := args[:0:0]
	for _, x := range args {
		skip := false
		for _, f := range flags {
			if x == f {
				skip = true
			}
		}
		if !skip {
			out = append(out, x)
		}
	}
	return out
}

func (a app) sharenetMembers(ctx context.Context, client *clicore.Client, n clicore.SharenetSummary) int {
	d, err := client.GetSharenet(ctx, n.ID)
	if err != nil {
		return a.fail("sharenet members", err)
	}
	for _, m := range d.Members {
		who := m.Email
		if who == "" {
			who = m.Name
		}
		state := ""
		if m.Pending {
			state = "  (invited)"
		}
		if m.You {
			state += "  (you)"
		}
		fmt.Fprintf(a.stdout, "%-34s %-12s%s\n", who, m.Role, state)
	}
	return 0
}

// sharenetInvite: `s2u sharenet <net> invite <email>... [--role contributor] [--bring-agents PROJECT]`.
func (a app) sharenetInvite(ctx context.Context, client *clicore.Client, n clicore.SharenetSummary, args []string, yes bool) int {
	role, bring := "contributor", ""
	var emails []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--role":
			if i+1 < len(args) {
				role, i = args[i+1], i+1
			}
		case "--bring-agents":
			if i+1 < len(args) {
				bring, i = args[i+1], i+1
			}
		default:
			emails = append(emails, args[i])
		}
	}
	if len(emails) == 0 {
		fmt.Fprintf(a.stderr, "usage: %s sharenet %s invite <email>... [--role contributor|viewer|manager] [--bring-agents PROJECT] [--yes]\n", commandName, n.Name)
		return 2
	}
	var grant []string
	if bring != "" {
		p, err := a.resolveProject(ctx, client, n, bring)
		if err != nil {
			fmt.Fprintln(a.stderr, cliErrorMessage(err))
			return 1
		}
		grant = []string{p.ID}
	}
	if !a.confirmChange(yes, fmt.Sprintf("Invite %s to %s as %s", strings.Join(emails, ", "), n.Name, role)) {
		return 1
	}
	failed := 0
	for _, e := range emails {
		if _, err := client.InviteToSharenet(ctx, n.ID, e, role, grant); err != nil {
			fmt.Fprintf(a.stdout, "  %s: %s\n", e, cliErrorMessage(err))
			failed++
			continue
		}
		fmt.Fprintf(a.stdout, "✓ Invited %s (they get an email; it works only when they sign in with that address)\n", e)
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func (a app) sharenetRemove(ctx context.Context, client *clicore.Client, n clicore.SharenetSummary, args []string, yes bool) int {
	if len(args) != 1 {
		fmt.Fprintf(a.stderr, "usage: %s sharenet %s remove <email> [--yes]\n", commandName, n.Name)
		return 2
	}
	d, err := client.GetSharenet(ctx, n.ID)
	if err != nil {
		return a.fail("sharenet members", err)
	}
	var target *clicore.SharenetMember
	for i := range d.Members {
		if strings.EqualFold(d.Members[i].Email, args[0]) || d.Members[i].ID == args[0] {
			target = &d.Members[i]
		}
	}
	if target == nil {
		fmt.Fprintf(a.stderr, "%s is not a member or invitee of %s (see `%s sharenet %s members`)\n", args[0], n.Name, commandName, n.Name)
		return 1
	}
	if !a.confirmChange(yes, fmt.Sprintf("Remove %s from %s", args[0], n.Name)) {
		return 1
	}
	if err := client.RemoveSharenetMember(ctx, n.ID, target.ID); err != nil {
		return a.fail("remove member", err)
	}
	fmt.Fprintf(a.stdout, "✓ Removed %s from %s\n", args[0], n.Name)
	return 0
}

// sharenetCode: `s2u sharenet <net>[.<project>] code [--uses N] [--expires 7d|48h] [--function F]`.
func (a app) sharenetCode(ctx context.Context, client *clicore.Client, n clicore.SharenetSummary, projectName string, args []string) int {
	uses, hours := 0, 0
	function := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--uses" && i+1 < len(args):
			v, err := strconv.Atoi(args[i+1])
			if err != nil || v < 1 {
				fmt.Fprintln(a.stderr, "--uses takes a number from 1 to 100")
				return 2
			}
			uses, i = v, i+1
		case args[i] == "--expires" && i+1 < len(args):
			d, err := parseDays(args[i+1])
			if err != nil {
				fmt.Fprintln(a.stderr, "--expires takes a duration like 48h or 7d (at most 30d)")
				return 2
			}
			hours, i = int(d.Hours()), i+1
		case args[i] == "--function" && i+1 < len(args):
			function, i = args[i+1], i+1
		default:
			fmt.Fprintf(a.stderr, "usage: %s sharenet %s[.<project>] code [--uses N] [--expires 7d] [--function NAME]\n", commandName, n.Name)
			return 2
		}
	}
	p, err := a.resolveProject(ctx, client, n, projectName)
	if err != nil {
		fmt.Fprintln(a.stderr, cliErrorMessage(err))
		return 1
	}
	code, expires, err := client.CreateJoinCode(ctx, p.ID, function, uses, hours)
	if err != nil {
		return a.fail("join code", err)
	}
	fmt.Fprintf(a.stdout, "Join code for %s.%s (expires %s", n.Name, p.Name, expires)
	if uses > 1 {
		fmt.Fprintf(a.stdout, ", admits %d agents", uses)
	}
	fmt.Fprintln(a.stdout, "):")
	fmt.Fprintf(a.stdout, "  !s2u agent join %s\n", code)
	fmt.Fprintln(a.stdout, "Send that line to the agent's owner; they type it inside the agent's Claude Code, Codex or Gemini CLI session. You approve their request with `requests` / `approve` (members you allowed to bring agents are admitted directly).")
	return 0
}

// parseDays accepts Go durations plus a "d" (days) suffix.
func parseDays(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n < 1 {
			return 0, errors.New("bad days")
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

func (a app) sharenetRequests(ctx context.Context, client *clicore.Client, n clicore.SharenetSummary, p clicore.SharenetProject) int {
	reqs, err := client.ListJoinRequests(ctx, p.ID)
	if err != nil {
		return a.fail("join requests", err)
	}
	if len(reqs) == 0 {
		fmt.Fprintf(a.stdout, "No join requests waiting in %s.%s.\n", n.Name, p.Name)
		return 0
	}
	for _, r := range reqs {
		fmt.Fprintf(a.stdout, "%s  %s wants to add %s (%s)  expires %s\n", r.ID, r.Email, r.AgentName, r.Tool, r.ExpiresAt)
	}
	fmt.Fprintf(a.stdout, "Decide: %s sharenet %s.%s approve|reject <id>\n", commandName, n.Name, p.Name)
	return 0
}

// agentAdd: `s2u agent add <agent|.> <net>[.<project>] [--can-post]` adds your own agent
// (`.` = the one bound in this folder) to a sharenet project.
func (a app) agentAdd(ctx context.Context, args []string) int {
	canPost := hasFlag(args, "--can-post")
	args = withoutFlags(args, "--can-post")
	if len(args) != 2 {
		fmt.Fprintf(a.stderr, "usage: %s agent add <agent|.> <sharenet>[.<project>] [--can-post]\n", commandName)
		return 2
	}
	client, ok := a.manageClient()
	if !ok {
		return 1
	}
	agentID, err := a.ownAgentID(ctx, client, args[0])
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 1
	}
	netName, projectName := splitNetProject(args[1])
	n, err := a.resolveSharenet(ctx, client, netName)
	if err != nil {
		fmt.Fprintln(a.stderr, cliErrorMessage(err))
		return 1
	}
	p, err := a.resolveProject(ctx, client, n, projectName)
	if err != nil {
		fmt.Fprintln(a.stderr, cliErrorMessage(err))
		return 1
	}
	return a.addAgents(ctx, client, n, p, []string{agentID}, canPost)
}

// ownAgentID resolves "." (this folder's agent), an agent id, or a name of one of the
// caller's own agents.
func (a app) ownAgentID(ctx context.Context, client *clicore.Client, q string) (string, error) {
	if q == "." {
		list, _ := daemon.LoadBindings()
		cwd, _ := os.Getwd()
		if id, ok := daemon.AgentIDForProject(list, cwd); ok {
			return id, nil
		}
		return "", fmt.Errorf("no agent is bound in this folder; run `%s agent bind` inside its session", commandName)
	}
	if strings.HasPrefix(q, "agt_") && len(q) > 10 {
		return q, nil
	}
	targets, err := a.reachableTargets(ctx, client, "")
	if err != nil {
		return "", err
	}
	t, err := pickTarget(targets, targetQuery{Agent: q})
	if err != nil {
		return "", err
	}
	return t.AgentID, nil
}
