// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	clicore "github.com/share2us/cli-core"
)

const (
	pasteVerifyWindow = time.Second
	pasteVerifyPoll   = 25 * time.Millisecond
)

// tryTypedInject attempts guarded delivery into an idle Claude input box. It
// returns true once it has taken ownership of the hop. False means it typed
// nothing and the caller may use the normal channel/wait path.
func (rt *Runtime) tryTypedInject(ctx context.Context, client AgentClient, runner AgentRunner, deps Deps, req clicore.AgentRequest, binding Binding, session DiscoveredSession, shown, prompt, cwd string, waited time.Duration) bool {
	// A recent channel poll is proof this is an `s2u claude` process with the
	// guardrail and Stop hooks loaded. Plain Claude must never be typed into.
	if binding.Zellij == nil || !rt.hub().guardedAlive(req.TargetSessionID) {
		return false
	}
	z := rt.paneDriver()
	pane, err := resolveZellijPaneWith(ctx, z, binding, session, rt.processZellijPane)
	if err != nil {
		deps.logf("agent-bridge: zellij pane unavailable for %s: %v", req.ID, err)
		return false
	}
	before, err := z.Dump(ctx, pane.Session, pane.Pane)
	if err != nil || !safeClaudeInput(before) {
		return false
	}
	strict := false
	if cr, ok := runner.(ClaudeRunner); ok {
		strict = cr.Strict
	}
	result, accepted := rt.hub().beginTyped(req.TargetSessionID, ChannelDelivery{RequestID: req.ID, From: req.SenderDeviceID, Strict: strict})
	if !accepted {
		return false
	}
	visible := fmt.Sprintf("[Share2Us] from device %s, request %s:\n\n%s", req.SenderDeviceID, req.ID, prompt)
	if err := z.Paste(ctx, pane.Session, pane.Pane, visible); err != nil {
		rt.hub().withdraw(req.TargetSessionID, req.ID)
		return false
	}
	if !waitForPastedClaudeInput(ctx, z, pane, visible) {
		// Something changed after the empty-input check. Never press Enter and
		// keep the guard active: the pasted remote text may still be submitted
		// manually, and it must not run with the window's unrestricted policy.
		rt.watchUnverifiedPaste(ctx, client, runner, deps, req, shown, prompt, cwd, pane, result, waited)
		return true
	}
	if err := z.Enter(ctx, pane.Session, pane.Pane); err != nil {
		// send-keys may have reached zellij even when its client reports an
		// error. Retrying could run the same hop twice, so enter the same guarded
		// uncertain state and never paste this request again.
		rt.watchUnverifiedPaste(ctx, client, runner, deps, req, shown, prompt, cwd, pane, result, waited)
		return true
	}
	rt.finishTypedInject(ctx, client, deps, req, shown, result, waited)
	return true
}

// Claude renders a bracketed multiline paste asynchronously: immediately after
// zellij reports success, dump-screen can still show the old dim suggestion or
// only the first visible prefix, before Claude folds it into one [Pasted text]
// marker. Give that render transition a short bound. Any unrelated owner text,
// dialog, busy state, dump failure, or timeout still fails closed without Enter.
func waitForPastedClaudeInput(ctx context.Context, z zellijDriver, pane resolvedZellijPane, visible string) bool {
	deadline := time.Now().Add(pasteVerifyWindow)
	want := strings.TrimSpace(visible)
	for {
		after, err := z.Dump(ctx, pane.Session, pane.Pane)
		if err != nil {
			return false
		}
		if pastedClaudeInput(after, visible) {
			return true
		}
		state := parseClaudeScreen(after)
		got := strings.TrimSpace(state.Input)
		transitioning := state.InputEmpty || (got != "" && strings.HasPrefix(want, got))
		if state.Busy || state.Dialog || !transitioning || !time.Now().Before(deadline) {
			return false
		}
		timer := time.NewTimer(pasteVerifyPoll)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return false
		}
	}
}

func (rt *Runtime) finishTypedInject(ctx context.Context, client AgentClient, deps Deps, req clicore.AgentRequest, shown string, result <-chan string, waited time.Duration) {
	rt.holding.Add(1)
	go func() {
		defer rt.holding.Add(-1)
		deps.logf("agent-bridge: typed inject %s into open session %s", req.ID, req.TargetSessionID)
		rt.notify("Share2Us", "A prompt was typed into your open "+req.Tool+" session")
		_ = client.AgentReportResult(ctx, req.ID, "running", "Delivered into the open "+req.Tool+" session.")
		hop := HopRecord{Time: time.Now().UTC(), RequestID: req.ID, Tool: req.Tool, From: req.SenderDeviceID,
			Target: req.TargetSessionID, RanIn: req.TargetSessionID, Mode: "typed", Prompt: shown}
		if waited > 0 {
			hop.Waited = waited.Round(time.Second).String()
		}
		deadline := requestDeadline(req)
		var out string
		select {
		case out = <-result:
		case <-time.After(time.Until(deadline)):
			rt.hub().withdraw(req.TargetSessionID, req.ID)
			hop.Status = "failed"
			_ = AppendHop(hop)
			_ = client.AgentReportResult(ctx, req.ID, "failed", "The typed agent turn did not finish within "+injectHoldMax.String()+".")
			return
		case <-ctx.Done():
			return
		}
		hop.Status = "done"
		_ = AppendHop(hop)
		if strings.TrimSpace(out) == "" {
			out = "The agent finished its turn without a report. Its work is in the session."
		}
		if len(out) > maxReportedResult {
			out = out[:maxReportedResult]
		}
		_ = client.AgentReportResult(ctx, req.ID, "done", out)
	}()
}

// watchUnverifiedPaste owns a hop after Paste succeeded but safe submission
// could not be proved. It never presses Enter and never pastes again. The hook
// remains active until the owner clears the input, submits it, or the request
// expires.
func (rt *Runtime) watchUnverifiedPaste(ctx context.Context, client AgentClient, runner AgentRunner, deps Deps, req clicore.AgentRequest, shown, prompt, cwd string, pane resolvedZellijPane, result <-chan string, waited time.Duration) {
	rt.holding.Add(1)
	_ = client.AgentReportResult(ctx, req.ID, "waiting", "Waiting: text reached the Claude input, but Share2Us did not press Enter because the input changed. Review or clear it in that window.")
	go func() {
		defer rt.holding.Add(-1)
		ticker := time.NewTicker(injectHoldPoll)
		defer ticker.Stop()
		deadline := requestDeadline(req)
		for {
			select {
			case out := <-result:
				// The owner submitted it and the guarded turn ended.
				hop := HopRecord{Time: time.Now().UTC(), RequestID: req.ID, Tool: req.Tool, From: req.SenderDeviceID,
					Target: req.TargetSessionID, RanIn: req.TargetSessionID, Mode: "typed", Status: "done", Prompt: shown}
				if waited > 0 {
					hop.Waited = waited.Round(time.Second).String()
				}
				_ = AppendHop(hop)
				if strings.TrimSpace(out) == "" {
					out = "The guarded agent turn finished in the session."
				}
				_ = client.AgentReportResult(ctx, req.ID, "done", out)
				return
			case <-ticker.C:
				screen, err := rt.paneDriver().Dump(ctx, pane.Session, pane.Pane)
				if err == nil {
					state := parseClaudeScreen(screen)
					if !state.Busy && state.InputEmpty {
						// The owner cleared the unsubmitted text. Release the guard
						// and return to the ordinary wait/retry path.
						rt.hub().withdraw(req.TargetSessionID, req.ID)
						rt.holdInject(ctx, client, runner, deps, req, shown, prompt, cwd)
						return
					}
				}
				if time.Now().After(deadline) {
					rt.hub().withdraw(req.TargetSessionID, req.ID)
					_ = AppendHop(HopRecord{Time: time.Now().UTC(), RequestID: req.ID, Tool: req.Tool, From: req.SenderDeviceID,
						Target: req.TargetSessionID, Mode: "expired", Status: "failed", Prompt: shown})
					_ = client.AgentReportResult(ctx, req.ID, "failed", "The changed Claude input was not submitted or cleared within "+injectHoldMax.String()+".")
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func requestDeadline(req clicore.AgentRequest) time.Time {
	deadline := time.Now().Add(injectHoldMax)
	if created, err := time.Parse(time.RFC3339, req.CreatedAt); err == nil {
		deadline = created.Add(injectHoldMax)
	}
	return deadline
}
