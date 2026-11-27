package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	clicore "github.com/share2us/cli-core"

	"github.com/share2us/cli/internal/daemon"
)

// sharenetDM: `s2u sharenet <name>.<project> dm @<user> <text>` (ADR-054 phase 2). The
// session's agent takes a member aside: a private conversation only they and this agent's
// owner can read. Their replies come back to this session as tasks. The project shows that
// the member was approached (never what was said).
func (a app) sharenetDM(ctx context.Context, client *clicore.Client, n clicore.SharenetSummary, projectName string, args []string) int {
	usage := func() int {
		fmt.Fprintf(a.stderr, "usage: %s sharenet %s.<project> dm @<user> <text>\n", commandName, n.Name)
		return 2
	}
	if len(args) < 2 || projectName == "" || strings.EqualFold(projectName, "general") {
		return usage()
	}
	agent := strings.TrimSpace(os.Getenv(daemon.AgentEnvVar))
	if agent == "" {
		fmt.Fprintln(a.stderr, "dm is sent as this session's agent: run it inside an agent session started with `s2u claude` or `s2u codex`.")
		return 1
	}
	user := strings.TrimPrefix(strings.TrimSpace(args[0]), "@")
	text := strings.TrimSpace(strings.Join(args[1:], " "))
	if user == "" || text == "" {
		return usage()
	}
	// Never a credential in chat, even a private one: ask for it, and the person hands it
	// over through the portal's secret box instead.
	if res, err := clicore.ScanBytesForSecrets([]byte(text), clicore.SecretScanOptions{}); err == nil && len(res.Findings) > 0 {
		fmt.Fprintln(a.stderr, "Not sent: the message looks like it contains a secret (a key or token). Remove it and try again.")
		return 1
	}
	p, err := a.resolveProject(ctx, client, n, projectName)
	if err != nil {
		fmt.Fprintln(a.stderr, cliErrorMessage(err))
		return 1
	}
	dm, err := client.OpenAgentDM(ctx, n.ID, agent, p.ID, user)
	if err != nil {
		return a.fail("open the direct message", err)
	}
	m := clicore.SharenetMessage{Channel: dm.Channel, Body: text, AgentID: agent}
	if n.Secure {
		sn, _, err := a.secureSession(ctx)
		if err != nil {
			return a.fail("open the Secure ShareNet keys", err)
		}
		if err := sn.SealMessage(ctx, n.ID, &m); err != nil {
			return a.fail("encrypt the message", err)
		}
	}
	id, err := client.PostSharenetMessage(ctx, n.ID, m)
	if err != nil {
		return a.fail("send the direct message", err)
	}
	fmt.Fprintf(a.stdout, "✓ Sent to @%s privately (message %d in %s). Their reply comes back to this session as a task.\n", user, id, n.Name+"."+p.Name)
	return 0
}
