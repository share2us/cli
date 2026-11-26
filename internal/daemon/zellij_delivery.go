// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	clicore "github.com/share2us/cli-core"
)

const (
	pasteVerifyWindow = time.Second
	pasteVerifyPoll   = 25 * time.Millisecond
)

// tryTypedInject attempts guarded delivery into an idle Claude input box. It
// returns true once it has taken ownership of the hop. False means it typed
// nothing and the caller may use the normal channel/wait path.
func (rt *Runtime) tryTypedInject(ctx context.Context, client AgentClient, runner AgentRunner, deps Deps, req clicore.AgentRequest, binding Binding, session DiscoveredSession, shown, prompt, senderName, cwd string, waited time.Duration) bool {
	// Terminal controls in sender-controlled text must never reach Zellij.
	// ESC is especially dangerous: it can end bracketed paste before our
	// post-paste screen check runs. Let the ordinary waiting path handle it.
	var safe bool
	if prompt, safe = safeTerminalText(prompt); !safe {
		return false
	}
	if senderName, safe = safeTerminalText(senderName); !safe {
		return false
	}
	// Claude channels are optional. A hook proof tied to this exact process is
	// sufficient for guarded terminal delivery when the organisation disables
	// development channels. Plain Claude never writes that proof.
	if binding.TypedDeliveryDisabled || binding.Zellij == nil || !TerminalTypedReady(req.TargetSessionID, session.PID) {
		return false
	}
	z := rt.paneDriver()
	processPane := func(pid int) *ZellijPane {
		if current := rt.processZellijPane(pid); current != nil {
			return current
		}
		return TerminalHookPane(req.TargetSessionID, pid)
	}
	pane, err := resolveZellijPaneWith(ctx, z, binding, session, processPane)
	if err != nil {
		deps.logf("agent-bridge: zellij pane unavailable for %s: %v", req.ID, err)
		return false
	}
	before, err := z.Dump(ctx, pane.Session, pane.Pane)
	if err != nil {
		return false
	}
	queued := false
	if !safeClaudeInput(before) {
		// A turn is running but the input is empty: after a short wait, type it anyway and
		// let Claude queue it behind the turn (owner, 2026-11-26). Text in the input is
		// never typed over: Enter would send the owner's draft too.
		if waited < typedQueueAfter || !queueableClaudeInput(before) {
			rt.noteTypedBlock(req.ID, typedBlockReason(before))
			return false
		}
		queued = true
	}
	strict := false
	if cr, ok := runner.(ClaudeRunner); ok {
		strict = cr.Strict
	}
	result, aborted, accepted := rt.hub().beginTypedForProcess(req.TargetSessionID, ChannelDelivery{RequestID: req.ID, From: req.SenderDeviceID, Strict: strict}, session.PID)
	if !accepted {
		return false
	}
	if err := beginTypedGuard(req.TargetSessionID, req.ID, session.PID, queued); err != nil {
		rt.hub().withdraw(req.TargetSessionID, req.ID)
		return false // no persistent fail-closed marker: type nothing
	}
	count, limitReason, limitErr := rt.reserveTypedDelivery(req.TargetSessionID)
	if limitErr != nil || limitReason != "" {
		rt.hub().withdraw(req.TargetSessionID, req.ID)
		_ = EndTypedGuard(req.TargetSessionID, req.ID)
		deps.logf("agent-bridge: typed delivery paused for %s: %s %v", req.TargetSessionID, limitReason, limitErr)
		return false
	}
	sender := req.SenderDeviceID
	if name := strings.TrimSpace(senderName); name != "" {
		sender = fmt.Sprintf("%q (%s)", name, req.SenderDeviceID)
	}
	visible := fmt.Sprintf("[Share2Us] from device %s, request %s:\n\n%s", sender, req.ID, prompt)
	if !strings.Contains(prompt, "returns your final message to them automatically") {
		visible += "\n\n" + typedReplyNote // an older sender didn't add its own reply instruction
	}
	if visible, safe = safeTerminalText(visible); !safe {
		rt.hub().withdraw(req.TargetSessionID, req.ID)
		_ = EndTypedGuard(req.TargetSessionID, req.ID)
		return false
	}
	if cancelledBySender(ctx, client, req.ID) {
		rt.hub().withdraw(req.TargetSessionID, req.ID)
		_ = EndTypedGuard(req.TargetSessionID, req.ID)
		return false // the hold path sees the cancel and drops it
	}
	if err := z.Paste(ctx, pane.Session, pane.Pane, visible); err != nil {
		rt.hub().withdraw(req.TargetSessionID, req.ID)
		_ = EndTypedGuard(req.TargetSessionID, req.ID)
		return false
	}
	rt.notify("Share2Us", fmt.Sprintf("A prompt reached your open %s input (%d/%d typed deliveries this hour)", req.Tool, count, typedDeliveryCap))
	if !waitForPastedClaudeInput(ctx, z, pane, visible) {
		// Something changed after the empty-input check. Never press Enter and
		// keep the guard active: the pasted remote text may still be submitted
		// manually, and it must not run with the window's unrestricted policy.
		rt.watchUnverifiedPaste(ctx, client, runner, deps, req, shown, prompt, senderName, cwd, pane, result, aborted, waited,
			unverifiedPaste{visible: visible, allowEnter: true, current: func() (resolvedZellijPane, error) {
				return resolveZellijPaneWith(ctx, z, binding, session, processPane)
			}})
		return true
	}
	// The owner may exit Claude between the initial pane check and Enter. A
	// shell can display the same prompt marker, so revalidate both process and
	// pane identity immediately before sending the key.
	current, err := resolveZellijPaneWith(ctx, z, binding, session, processPane)
	if err != nil || current != pane {
		rt.watchUnverifiedPaste(ctx, client, runner, deps, req, shown, prompt, senderName, cwd, pane, result, aborted, waited,
			unverifiedPaste{visible: visible, allowEnter: true, current: func() (resolvedZellijPane, error) {
				return resolveZellijPaneWith(ctx, z, binding, session, processPane)
			}})
		return true
	}
	// Claim "running" BEFORE Enter: the server refuses it once the sender cancelled, and
	// refuses a cancel once it is running, so the two can never both win.
	running := typedDeliveredMsg(req.Tool)
	if queued {
		running = "Typed into the open " + req.Tool + " window while it was working: it runs right after the current turn."
	}
	if isCancelledErr(client.AgentReportResult(ctx, req.ID, "running", running)) {
		rt.dropCancelledPaste(req, deps)
		return true
	}
	if err := z.Enter(ctx, pane.Session, pane.Pane); err != nil {
		// send-keys may have reached zellij even when its client reports an
		// error. Retrying could run the same hop twice, so enter the same guarded
		// uncertain state and never paste this request again (nor press Enter again).
		rt.watchUnverifiedPaste(ctx, client, runner, deps, req, shown, prompt, senderName, cwd, pane, result, aborted, waited,
			unverifiedPaste{visible: visible})
		return true
	}
	rt.finishTypedInject(ctx, client, deps, req, shown, result, aborted, waited, pane)
	return true
}

func safeTerminalText(s string) (string, bool) {
	if strings.ContainsRune(s, '\x1b') {
		return "", false
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return -1
		}
		return r
	}, s), true
}

// Claude renders a bracketed multiline paste asynchronously: immediately after
// zellij reports success, dump-screen can still show the old dim suggestion or
// only the first visible prefix, before Claude folds it into one [Pasted text]
// marker. Give that render transition a short bound. Any unrelated owner text,
// dialog, busy state, dump failure, or timeout still fails closed without Enter.
func waitForPastedClaudeInput(ctx context.Context, z zellijDriver, pane resolvedZellijPane, visible string) bool {
	deadline := time.Now().Add(pasteVerifyWindow)
	want := withoutWhitespace(visible)
	for {
		after, err := z.Dump(ctx, pane.Session, pane.Pane)
		if err != nil {
			return false
		}
		if pastedClaudeInput(after, visible) {
			return true
		}
		state := parseClaudeScreen(after)
		got := withoutWhitespace(state.Input)
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

func (rt *Runtime) finishTypedInject(ctx context.Context, client AgentClient, deps Deps, req clicore.AgentRequest, shown string, result <-chan string, aborted <-chan struct{}, waited time.Duration, pane resolvedZellijPane) {
	rt.holding.Add(1)
	poll := approvalPoll
	go func() {
		defer rt.holding.Add(-1)
		deps.logf("agent-bridge: typed inject %s into open session %s", req.ID, req.TargetSessionID)
		hop := HopRecord{Time: time.Now().UTC(), RequestID: req.ID, Tool: req.Tool, From: req.SenderDeviceID,
			Target: req.TargetSessionID, RanIn: req.TargetSessionID, Mode: "typed", Prompt: shown}
		if waited > 0 {
			hop.Waited = waited.Round(time.Second).String()
		}
		deadline := time.NewTimer(time.Until(requestDeadline(req)))
		defer deadline.Stop()
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		approvals := approvalWatch{}
		var out string
	wait:
		for {
			select {
			case out = <-result:
				break wait
			case <-aborted:
				hop.Status = "failed"
				_ = AppendHop(hop)
				_ = client.AgentReportResult(ctx, req.ID, "failed", "The typed turn was interrupted by a later owner turn.")
				return
			case <-deadline.C:
				rt.hub().withdraw(req.TargetSessionID, req.ID)
				hop.Status = "failed"
				_ = AppendHop(hop)
				_ = client.AgentReportResult(ctx, req.ID, "failed", "The typed agent turn did not finish within "+injectHoldMax.String()+".")
				return
			case <-ticker.C:
				approvals.check(ctx, rt, client, req, pane)
			case <-ctx.Done():
				return
			}
		}
		hop.Status = "done"
		_ = AppendHop(hop)
		if strings.TrimSpace(out) == "" {
			out = noReportResult
		}
		out = approvals.annotate(out)
		if len(out) > maxReportedResult {
			out = out[:maxReportedResult]
		}
		_ = client.AgentReportResult(ctx, req.ID, "done", out)
		_ = EndTypedGuard(req.TargetSessionID, req.ID)
	}()
}

// approvalPoll is how often a running typed turn's window is checked for Claude's
// approval dialog (todo ZT item 3: a turn blocked on approvals must not read as done).
var approvalPoll = 5 * time.Second

// approvalWatch tracks Claude's "Do you want to ...?" dialog during a typed turn.
type approvalWatch struct {
	showing, seen bool
}

func (w *approvalWatch) check(ctx context.Context, rt *Runtime, client AgentClient, req clicore.AgentRequest, pane resolvedZellijPane) {
	screen, err := rt.paneDriver().Dump(ctx, pane.Session, pane.Pane)
	if err != nil {
		return
	}
	dialog := parseClaudeScreen(screen).Dialog
	switch {
	case dialog && !w.showing:
		w.showing, w.seen = true, true
		// "running", not "waiting": the turn has started, so it must not look cancellable.
		_ = client.AgentReportResult(ctx, req.ID, "running", "Waiting on approval: the "+req.Tool+" session is asking its owner to approve a step. The request continues once they answer.")
	case !dialog && w.showing:
		w.showing = false
		_ = client.AgentReportResult(ctx, req.ID, "running", typedDeliveredMsg(req.Tool))
	}
}

// annotate tells the sender that the turn needed approvals, so a turn that was blocked
// or partly refused doesn't read as a plain success.
func (w *approvalWatch) annotate(out string) string {
	if !w.seen {
		return out
	}
	return out + "\n\n(Note: the session asked its owner to approve steps during this turn; some may have been declined.)"
}

// noReportResult is the result of a turn that ended without a channel report and without
// a final agent message in the transcript (it should be rare: the Stop hook passes the
// last reply along).
const noReportResult = "Done, but no report: the agent ended its turn without a reply. Its work is in the session."

// unverifiedRecheck is how soon the input is re-read after an unconfirmed paste (Claude
// can take a moment to fold a long paste into its [Pasted text] marker), before falling
// back to the ordinary hold poll.
var unverifiedRecheck = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

// stuckAfterChecks: re-reads that found someone else's text in the input before the
// sender is told the delivery is stuck.
const stuckAfterChecks = 4

// unverifiedPaste is what watchUnverifiedPaste needs to finish a delivery safely.
type unverifiedPaste struct {
	// visible is the exact text Share2Us typed.
	visible string
	// allowEnter is false once an Enter may already have reached the pane (send-keys
	// reported an error): pressing again could run the request twice.
	allowEnter bool
	// current re-resolves the pane right before Enter (the owner may have quit Claude).
	current func() (resolvedZellijPane, error)
}

// watchUnverifiedPaste owns a hop after Paste succeeded but safe submission could not be
// proved yet. It never pastes again and never submits anyone else's text. It re-reads the
// input with a short backoff and presses Enter once, only when the input holds exactly
// the text Share2Us typed and the pane is still the same Claude. Text that differs is
// left alone; after a few checks the sender is told the delivery is stuck. The guard
// stays active until the turn ends, the owner clears the input, or the request expires.
func (rt *Runtime) watchUnverifiedPaste(ctx context.Context, client AgentClient, runner AgentRunner, deps Deps, req clicore.AgentRequest, shown, prompt, senderName, cwd string, pane resolvedZellijPane, result <-chan string, aborted <-chan struct{}, waited time.Duration, u unverifiedPaste) {
	rt.holding.Add(1)
	if u.allowEnter {
		_ = client.AgentReportResult(ctx, req.ID, "waiting", "Waiting: the text reached the Claude input but could not be confirmed yet. Share2Us presses Enter only if the input holds exactly this request.")
	} else {
		// Enter may already have reached the window, so this may be running: never offer
		// it as cancellable ("waiting" is).
		_ = client.AgentReportResult(ctx, req.ID, "running", "Typed into the open "+req.Tool+" window; Share2Us could not confirm the Enter key, so it will not press it again.")
	}
	recheck, holdPoll := append([]time.Duration(nil), unverifiedRecheck...), injectHoldPoll
	go func() {
		defer rt.holding.Add(-1)
		deadline := requestDeadline(req)
		// entered: Enter can no longer be pressed by us. pressed: WE pressed it here (a
		// cleared input then means the turn ran, not that the owner discarded our text).
		checks, unclean, entered, pressed, stuckReported := 0, 0, !u.allowEnter, false, false
		approvals := approvalWatch{}
		next := func() time.Duration {
			if checks < len(recheck) {
				return recheck[checks]
			}
			return holdPoll
		}
		timer := time.NewTimer(next())
		defer timer.Stop()
		for {
			select {
			case <-aborted:
				_ = AppendHop(HopRecord{Time: time.Now().UTC(), RequestID: req.ID, Tool: req.Tool, From: req.SenderDeviceID,
					Target: req.TargetSessionID, Mode: "typed", Status: "failed", Prompt: shown})
				_ = client.AgentReportResult(ctx, req.ID, "failed", "The typed turn was interrupted by a later owner turn.")
				return
			case out := <-result:
				// The turn ended (submitted by Share2Us below, or by the owner).
				hop := HopRecord{Time: time.Now().UTC(), RequestID: req.ID, Tool: req.Tool, From: req.SenderDeviceID,
					Target: req.TargetSessionID, RanIn: req.TargetSessionID, Mode: "typed", Status: "done", Prompt: shown}
				if waited > 0 {
					hop.Waited = waited.Round(time.Second).String()
				}
				_ = AppendHop(hop)
				if strings.TrimSpace(out) == "" {
					out = noReportResult
				}
				out = approvals.annotate(out)
				if len(out) > maxReportedResult {
					out = out[:maxReportedResult]
				}
				_ = client.AgentReportResult(ctx, req.ID, "done", out)
				_ = EndTypedGuard(req.TargetSessionID, req.ID)
				return
			case <-timer.C:
				checks++
				if entered {
					approvals.check(ctx, rt, client, req, pane)
				}
				if !entered && cancelledBySender(ctx, client, req.ID) {
					rt.dropCancelledPaste(req, deps)
					return
				}
				screen, err := rt.paneDriver().Dump(ctx, pane.Session, pane.Pane)
				if err == nil {
					state := parseClaudeScreen(screen)
					switch {
					case !entered && u.visible != "" && ourInputExactly(screen, u.visible, checks <= len(recheck)):
						// Exactly our text and nothing else: safe to submit, once.
						if u.current != nil {
							if cur, cerr := u.current(); cerr != nil || cur != pane {
								break // a different process or pane now: never press Enter there
							}
						}
						entered = true
						if isCancelledErr(client.AgentReportResult(ctx, req.ID, "running", typedDeliveredMsg(req.Tool))) {
							rt.dropCancelledPaste(req, deps)
							return
						}
						if rt.paneDriver().Enter(ctx, pane.Session, pane.Pane) == nil {
							pressed = true
							deps.logf("agent-bridge: typed inject %s submitted after re-check", req.ID)
						}
					case !pressed && !state.Busy && state.InputEmpty:
						// The owner cleared the unsubmitted text (or, after an uncertain
						// Enter, nothing is pending). Release the guard and return to the
						// ordinary wait/retry path.
						rt.hub().withdraw(req.TargetSessionID, req.ID)
						_ = EndTypedGuard(req.TargetSessionID, req.ID)
						rt.holdInject(ctx, client, runner, deps, req, shown, prompt, senderName, cwd)
						return
					case !entered && !state.Busy && !state.InputEmpty:
						unclean++
						if unclean >= stuckAfterChecks && !stuckReported {
							stuckReported = true
							_ = client.AgentReportResult(ctx, req.ID, "waiting", "Stuck: input not clean. The Claude input holds other text, so Share2Us will not submit it. Clear it or submit it in that window.")
						}
					}
				}
				if time.Now().After(deadline) {
					rt.hub().withdraw(req.TargetSessionID, req.ID)
					_ = AppendHop(HopRecord{Time: time.Now().UTC(), RequestID: req.ID, Tool: req.Tool, From: req.SenderDeviceID,
						Target: req.TargetSessionID, Mode: "expired", Status: "failed", Prompt: shown})
					_ = client.AgentReportResult(ctx, req.ID, "failed", "The Claude input was not submitted or cleared within "+injectHoldMax.String()+".")
					return
				}
				timer.Reset(next())
			case <-ctx.Done():
				return
			}
		}
	}()
}

// dropCancelledPaste ends a typed hop the sender cancelled before Enter was pressed. The
// pasted text stays in the input (clearing someone's input box is not ours to do); the
// owner is told it was not submitted.
func (rt *Runtime) dropCancelledPaste(req clicore.AgentRequest, deps Deps) {
	rt.hub().withdraw(req.TargetSessionID, req.ID)
	_ = EndTypedGuard(req.TargetSessionID, req.ID)
	deps.logf("agent-bridge: inject %s cancelled by its sender before submission", req.ID)
	rt.notify("Share2Us", "A prompt in your "+req.Tool+" input was cancelled by its sender and was NOT submitted. You can clear it.")
	_ = AppendHop(HopRecord{Time: time.Now().UTC(), RequestID: req.ID, Tool: req.Tool, From: req.SenderDeviceID,
		Target: req.TargetSessionID, Mode: "typed", Status: "cancelled"})
}

// typedQueueAfter: how long a busy window (empty input) is waited for before the prompt is
// typed anyway and queued behind the running turn.
var typedQueueAfter = 10 * time.Second

func (rt *Runtime) noteTypedBlock(id, reason string) {
	if reason == "" {
		return
	}
	rt.typedBlockMu.Lock()
	defer rt.typedBlockMu.Unlock()
	if rt.typedBlocks == nil {
		rt.typedBlocks = map[string]string{}
	}
	rt.typedBlocks[id] = reason
}

// takeTypedBlock returns (and clears) why the last typed attempt for id didn't happen.
func (rt *Runtime) takeTypedBlock(id string) string {
	rt.typedBlockMu.Lock()
	defer rt.typedBlockMu.Unlock()
	r := rt.typedBlocks[id]
	delete(rt.typedBlocks, id)
	return r
}

// typedReplyNote closes a typed prompt. Without it the agent saw a bare message, decided
// it had no way to answer the sender, and told its owner to relay the result by hand
// (bug report 2026-11-26), although the Stop hook returns its last reply.
const typedReplyNote = "(Share2Us: your final reply in this turn is sent back to the sender automatically. Just answer here; there is nothing to report or relay. If the share2us report tool is available, you may use it with this request id instead.)"

// foldedPasteLines reads Claude's "[Pasted text #N +L lines]" marker.
var foldedPasteLines = regexp.MustCompile(`^\[Pasted text[^]]*\+(\d+) lines\]$`)

// ourInputExactly: the Claude input holds exactly the text Share2Us typed. Claude may show
// a long paste as a folded marker; that counts only early (right after our paste, before
// the owner could plausibly replace it) and only when its line count is ours, because any
// later paste by the owner folds into the same kind of marker.
func ourInputExactly(screen, visible string, early bool) bool {
	s := parseClaudeScreen(screen)
	if !claudeUIReady(screen) || s.Busy || s.Dialog || s.Input == "" {
		return false
	}
	if withoutWhitespace(s.Input) == withoutWhitespace(visible) {
		return true
	}
	if !early {
		return false
	}
	m := foldedPasteLines.FindStringSubmatch(strings.TrimSpace(s.Input))
	if m == nil {
		return false
	}
	n, err := strconv.Atoi(m[1])
	lines := strings.Count(visible, "\n") + 1
	return err == nil && (n == lines || n == lines-1)
}

// isCancelledErr: the server refused a report because the sender cancelled the request.
func isCancelledErr(err error) bool {
	var apiErr *clicore.APIError
	return errors.As(err, &apiErr) && apiErr.Status == 409 && apiErr.Code == "cancelled"
}

func typedDeliveredMsg(tool string) string {
	return "Delivered by typing into the open " + tool + " window (Zellij)."
}

func requestDeadline(req clicore.AgentRequest) time.Time {
	deadline := time.Now().Add(injectHoldMax)
	if created, err := time.Parse(time.RFC3339, req.CreatedAt); err == nil {
		deadline = created.Add(injectHoldMax)
	}
	return deadline
}
