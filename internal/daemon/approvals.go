// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli-core/daemonctl"
)

// Owner approvals for actions the guard refuses in a delivered task (ADR-052, owner
// 2026-11-26). The hook asks the daemon; the daemon checks the agent's grants, else files a
// request with the API (the owner gets an email and a portal alert) and the hook holds the
// command for a few minutes. A late approval continues the task in the same session.

// approvalClient is the part of the cli-core client the approvals flow needs.
type approvalClient interface {
	CreateApproval(ctx context.Context, in clicore.ApprovalRequest) (string, string, bool, error)
	GetApproval(ctx context.Context, id string) (clicore.ApprovalState, error)
	ConsumeApproval(ctx context.Context, id string) error
	ContinueApproval(ctx context.Context, id string) (string, error)
	ListActionGrants(ctx context.Context) ([]clicore.ActionGrant, error)
}

var (
	grantCacheFor  = 30 * time.Second
	parkedPoll     = 10 * time.Second
	parkedLifetime = 24 * time.Hour
)

// ActionText is what an approval and a grant are about: a Bash command, or a tool on a path.
func ActionText(tool string, input map[string]any) string {
	if cmd, ok := input["command"].(string); ok && tool == "Bash" {
		return strings.TrimSpace(cmd)
	}
	for _, k := range []string{"file_path", "notebook_path", "path"} {
		if p, ok := input[k].(string); ok && p != "" {
			return tool + " " + p
		}
	}
	return tool
}

// ActionHash identifies an action exactly without sending it anywhere.
func ActionHash(tool, text string) string {
	sum := sha256.Sum256([]byte(tool + "\x00" + text))
	return hex.EncodeToString(sum[:])
}

// actionDisplay is what the owner is shown: the action, or its program name only when it
// looks like it carries a secret (the scanner gives no positions, so it isn't partly shown).
func actionDisplay(text string) string {
	if res, err := clicore.ScanBytesForSecrets([]byte(text), clicore.SecretScanOptions{}); err == nil && len(res.Findings) > 0 {
		first := strings.Fields(text)
		name := "command"
		if len(first) > 0 {
			name = first[0]
		}
		return name + " … (hidden: it looks like it contains a secret)"
	}
	if r := []rune(text); len(r) > 2000 {
		return string(r[:2000]) + "…"
	}
	return text
}

// SelfProtected: refusals no approval can lift (the guard protecting itself and Git).
func SelfProtected(cwd, tool string, input map[string]any) bool {
	for _, rule := range selfProtection {
		if ruleMatches(rule, tool, input) {
			return true
		}
	}
	if tool == "Agent" || tool == "Task" {
		return true
	}
	if fileEditTools[tool] {
		for _, p := range editPaths(input) {
			if insideGitMetadata(cwd, p) {
				return true
			}
		}
	}
	return false
}

type parkedApproval struct {
	ID, Session, RequestID, AgentID, Display string
	Since                                    time.Time
}

type approvalState struct {
	mu       sync.Mutex
	grants   []clicore.ActionGrant
	grantsAt time.Time
	parked   map[string]parkedApproval
	senders  map[string]string // request -> sender label, for the owner's email
	headless map[string]string // session -> the request running headless in it (one at a time)
	// The bridge's client, runners and deps, for continuations started by the watcher.
	client  AgentClient
	runners map[string]AgentRunner
	deps    Deps
}

func (rt *Runtime) approvals() *approvalState {
	rt.approvalsOnce.Do(func() {
		rt.approvalsState = &approvalState{parked: map[string]parkedApproval{}, senders: map[string]string{}}
	})
	return rt.approvalsState
}

func (rt *Runtime) noteHopSender(requestID, label string) {
	s := rt.approvals()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.senders) > 500 {
		s.senders = map[string]string{}
	}
	s.senders[requestID] = label
}

func (rt *Runtime) approvalAPI() (approvalClient, AgentClient, bool) {
	s := rt.approvals()
	s.mu.Lock()
	c := s.client
	s.mu.Unlock()
	ac, ok := c.(approvalClient)
	return ac, c, ok
}

// grantCovers reports whether an active grant lets agentID run this action.
func (rt *Runtime) grantCovers(ctx context.Context, agentID, tool, text string) bool {
	ac, _, ok := rt.approvalAPI()
	if !ok || agentID == "" {
		return false
	}
	s := rt.approvals()
	s.mu.Lock()
	stale := time.Since(s.grantsAt) > grantCacheFor
	s.mu.Unlock()
	if stale {
		if gs, err := ac.ListActionGrants(ctx); err == nil {
			s.mu.Lock()
			s.grants, s.grantsAt = gs, time.Now()
			s.mu.Unlock()
		}
	}
	hash := ActionHash(tool, text)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.grants {
		if g.AgentID != agentID || (g.ExpiresAt != nil && !g.ExpiresAt.After(time.Now())) {
			continue
		}
		switch {
		case g.Scope == "all":
			return true
		case g.Scope == "command" && g.CommandHash == hash:
			return true
		case g.Scope == "command" && g.Pattern != "" && strings.HasPrefix(text, g.Pattern):
			return true
		}
	}
	return false
}

func (rt *Runtime) forgetGrants() {
	s := rt.approvals()
	s.mu.Lock()
	s.grantsAt = time.Time{}
	s.mu.Unlock()
}

// approvalControl serves the hook's approval ops.
func (rt *Runtime) approvalControl(req daemonctl.Request) (daemonctl.Response, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	switch req.Op {
	case "approval-check":
		return rt.approvalCheck(ctx, req.Args), true
	case "approval-status":
		return rt.approvalStatusOp(ctx, req.Args["id"]), true
	case "approval-park":
		rt.parkApproval(req.Args["id"], req.Args["session"])
		return daemonctl.Response{OK: true}, true
	}
	return daemonctl.Response{}, false
}

func jsonResp(v any) daemonctl.Response {
	b, _ := json.Marshal(v)
	return daemonctl.Response{OK: true, Data: b}
}

// approvalCheck: "allow" (a grant covers it), or "pending" with the request it filed.
func (rt *Runtime) approvalCheck(ctx context.Context, args map[string]string) daemonctl.Response {
	session, tool, text, reason := args["session"], args["tool"], args["text"], args["reason"]
	list, _ := LoadBindings()
	b, bound := BindingForSession(list, session)
	if !bound || b.AgentID == "" {
		return jsonResp(map[string]string{"decision": "deny"})
	}
	if rt.grantCovers(ctx, b.AgentID, tool, text) {
		return jsonResp(map[string]string{"decision": "allow"})
	}
	ac, client, ok := rt.approvalAPI()
	if !ok {
		return jsonResp(map[string]string{"decision": "deny"})
	}
	requestID := rt.hub().activeRequest(session)
	s := rt.approvals()
	s.mu.Lock()
	if requestID == "" {
		requestID = s.headless[session]
	}
	sender := s.senders[requestID]
	s.mu.Unlock()
	display := actionDisplay(text)
	id, url, created, err := ac.CreateApproval(ctx, clicore.ApprovalRequest{
		AgentID: b.AgentID, SessionID: session, RequestID: requestID, SenderLabel: sender,
		Tool: tool, DisplayText: display, CommandHash: ActionHash(tool, text), Reason: reason,
	})
	if err != nil {
		return jsonResp(map[string]string{"decision": "deny", "error": err.Error()})
	}
	if created && requestID != "" {
		_ = client.AgentReportResult(ctx, requestID, "running",
			"Waiting for the target's owner to allow `"+display+"` (asked by email and in the Share2Us portal).")
	}
	if created {
		// The owner may be at this machine: tell them here too.
		rt.notify("Share2Us: approval needed", "An agent task wants to run "+display+". Approve or decline: "+url)
	}
	return jsonResp(map[string]string{"decision": "pending", "id": id, "url": url, "display": display, "agent": b.AgentID, "request": requestID})
}

// approvalStatusOp polls one request; a fresh approval refreshes grants, and a once-approval
// is used up here so it can't run twice.
func (rt *Runtime) approvalStatusOp(ctx context.Context, id string) daemonctl.Response {
	ac, _, ok := rt.approvalAPI()
	if !ok || id == "" {
		return jsonResp(map[string]string{"status": "unknown"})
	}
	st, err := ac.GetApproval(ctx, id)
	if err != nil {
		return jsonResp(map[string]string{"status": "unknown"})
	}
	if st.Status == "approved" {
		rt.forgetGrants()
		if st.Scope == "once" && !st.Consumed {
			if err := ac.ConsumeApproval(ctx, id); err != nil {
				return jsonResp(map[string]string{"status": "used"})
			}
		}
	}
	return jsonResp(map[string]string{"status": st.Status, "scope": st.Scope, "note": st.Note})
}

// parkApproval: the hold ran out; watch the request and continue the task when it's allowed.
func (rt *Runtime) parkApproval(id, session string) {
	if id == "" {
		return
	}
	s := rt.approvals()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parked[id] = parkedApproval{ID: id, Session: session, Since: time.Now()}
}

// watchParkedApprovals continues a task whose blocked action was allowed after the hold.
func (rt *Runtime) watchParkedApprovals(ctx context.Context) {
	t := time.NewTicker(parkedPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s := rt.approvals()
		s.mu.Lock()
		todo := make([]parkedApproval, 0, len(s.parked))
		for _, p := range s.parked {
			todo = append(todo, p)
		}
		s.mu.Unlock()
		for _, p := range todo {
			rt.checkParked(ctx, p)
		}
	}
}

func (rt *Runtime) checkParked(ctx context.Context, p parkedApproval) {
	ac, client, ok := rt.approvalAPI()
	if !ok {
		return
	}
	drop := func() {
		s := rt.approvals()
		s.mu.Lock()
		delete(s.parked, p.ID)
		s.mu.Unlock()
	}
	if time.Since(p.Since) > parkedLifetime {
		drop()
		return
	}
	st, err := ac.GetApproval(ctx, p.ID)
	if err != nil {
		return
	}
	switch st.Status {
	case "pending":
		return
	case "approved":
		drop()
		rt.forgetGrants()
		if st.Scope == "once" && !st.Consumed {
			if ac.ConsumeApproval(ctx, p.ID) != nil {
				return
			}
		}
		requestID, err := ac.ContinueApproval(ctx, p.ID)
		if err != nil || requestID == "" {
			return
		}
		rt.continueAfterApproval(ctx, client, p.Session, requestID, st)
	default:
		drop()
	}
}

// continueAfterApproval delivers a local continuation into the session that was blocked, as
// part of the same request, so its answer goes back to the original sender.
func (rt *Runtime) continueAfterApproval(ctx context.Context, client AgentClient, session, requestID string, st clicore.ApprovalState) {
	list, _ := LoadBindings()
	b, ok := BindingForSession(list, session)
	if !ok {
		return
	}
	s := rt.approvals()
	s.mu.Lock()
	runner := s.runners[b.Tool]
	deps := s.deps
	s.mu.Unlock()
	if runner == nil {
		return
	}
	scope := map[string]string{"once": "once", "command": fmt.Sprintf("for %d minutes", st.Minutes),
		"all": fmt.Sprintf("everything, for %d minutes", st.Minutes), "always": "always"}[st.Scope]
	text := fmt.Sprintf("Your owner has now allowed the action that was blocked (%s). Continue the task from request %s where it stopped, and give your final answer; it goes back to the sender.", scope, requestID)
	if note := strings.TrimSpace(st.Note); note != "" {
		text += " Your owner's note: " + note
	}
	req := clicore.AgentRequest{ID: requestID, Tool: b.Tool, TargetSessionID: session, SenderDeviceID: "share2us-approval"}
	rt.holdInject(ctx, client, runner, deps, req, text, text, "Share2Us (owner approval)", b.Project)
}

// runningHeadless notes the request a headless run serves, so an approval its hook files is
// tied to that request (status for the sender, continuation after a late approval).
func (rt *Runtime) runningHeadless(session, requestID string) func() {
	s := rt.approvals()
	s.mu.Lock()
	if s.headless == nil {
		s.headless = map[string]string{}
	}
	s.headless[session] = requestID
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		if s.headless[session] == requestID {
			delete(s.headless, session)
		}
		s.mu.Unlock()
	}
}

// setApprovalBridge records what continuations need; called when the bridge starts.
func (rt *Runtime) setApprovalBridge(client AgentClient, runners map[string]AgentRunner, deps Deps) {
	s := rt.approvals()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.client, s.runners, s.deps = client, runners, deps
}
