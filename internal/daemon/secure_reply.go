// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"sync"

	clicore "github.com/share2us/cli-core"
)

// A task sent inside a Secure ShareNet project gets its reply sealed here, on the target,
// before it leaves the device (ADR-053): the server and the channel only ever hold
// ciphertext. When this device can't seal it (no key yet) the reply is withheld, never
// sent in clear.

// ReplySealFailed is what the sender reads when the target couldn't seal the reply.
const ReplySealFailed = "The task ran, but its reply was withheld: this is a Secure ShareNet and the target device doesn't hold the sharenet's key yet (a member's device hands it over). Ask its owner to open the sharenet on that device, then send again."

// ReplyGuestSecure: the target's owner isn't a member of the Secure ShareNet, so it holds no key.
const ReplyGuestSecure = "The task ran, but its reply was withheld: this is a Secure ShareNet project and this agent's owner isn't a member of the sharenet, so it can't encrypt the reply. Its owner needs to join the sharenet (and receive the key) for replies to come back."

// sealingClient seals the final reply of a Secure project task on its way to the server.
type sealingClient struct {
	AgentClient
	rt   *Runtime
	deps Deps
}

func (c sealingClient) AgentReportResult(ctx context.Context, id, status, result string) error {
	if (status == "done" || status == "failed") && c.deps.SealTaskReply != nil && result != "" && !clicore.IsSealedReply(result) {
		if project, serverSecure := c.rt.taskProject(id); project != "" {
			sealed, secure, err := c.deps.SealTaskReply(ctx, project, id, result)
			switch {
			case err == nil && !secure && serverSecure:
				// The server says Secure but this account can't see it (an agent whose owner
				// isn't a member): it can't hold the key, so the reply stays here.
				c.deps.logf("agent-bridge: %s is in a Secure ShareNet this device can't seal for; withholding the reply", id)
				status, result = "failed", ReplyGuestSecure
			case err != nil && secure:
				c.deps.logf("agent-bridge: could not seal the reply to %s: %v", id, err)
				status, result = "failed", ReplySealFailed
			case err != nil:
				c.deps.logf("agent-bridge: could not tell whether %s's project is Secure: %v; withholding the reply", id, err)
				status, result = "failed", ReplySealFailed
			case secure:
				result = sealed
			}
		}
	}
	return c.AgentClient.AgentReportResult(ctx, id, status, result)
}

type taskProject struct {
	id     string
	secure bool // server-asserted
}

type taskProjects struct {
	mu sync.Mutex
	m  map[string]taskProject
}

// noteTaskProject remembers a request's project, so its reply can be sealed when due.
func (rt *Runtime) noteTaskProject(requestID, projectID string, serverSecure bool) {
	if projectID == "" {
		return
	}
	rt.taskProjectsOnce.Do(func() { rt.taskProjectsSt = &taskProjects{m: map[string]taskProject{}} })
	rt.taskProjectsSt.mu.Lock()
	defer rt.taskProjectsSt.mu.Unlock()
	if len(rt.taskProjectsSt.m) > 2000 {
		rt.taskProjectsSt.m = map[string]taskProject{}
	}
	rt.taskProjectsSt.m[requestID] = taskProject{id: projectID, secure: serverSecure}
}

func (rt *Runtime) taskProject(requestID string) (string, bool) {
	rt.taskProjectsOnce.Do(func() { rt.taskProjectsSt = &taskProjects{m: map[string]taskProject{}} })
	rt.taskProjectsSt.mu.Lock()
	defer rt.taskProjectsSt.mu.Unlock()
	p := rt.taskProjectsSt.m[requestID]
	return p.id, p.secure
}

// openReply turns a sealed reply this device can open into its text.
func (d Deps) openReply(ctx context.Context, requestID, result string) string {
	if !clicore.IsSealedReply(result) {
		return result
	}
	if d.OpenTaskReply == nil {
		return SealedReplyPlaceholder
	}
	return d.OpenTaskReply(ctx, requestID, result)
}

// SealedReplyPlaceholder stands in for a sealed reply this device can't open.
const SealedReplyPlaceholder = "(Encrypted reply: this device doesn't hold the Secure ShareNet key yet.)"
