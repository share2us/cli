// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/share2us/cli-core/daemonctl"
	"github.com/share2us/cli/internal/daemon"
)

// codexSessionContext is what an `s2u codex` window's model learns at startup, so it can
// handle delivered tasks and guide its user through Share2Us without being told.
const codexSessionContext = "Share2Us: this Codex window is connected to Share2Us (started with `s2u codex`). " +
	"Tasks from the owner's other agents or from sharenet members arrive here as prompts starting with " +
	"`[Share2Us] from device ..., request <id>:`. Do them within this project's rules; your final reply goes back " +
	"to the sender by itself. While such a task runs, a guard allows read-only commands and asks the owner " +
	"(email + Share2Us portal) for anything else; if it says the owner hasn't answered yet, finish what you can " +
	"and end your turn. For Share2Us commands (agents, send, sharenets, sharing files) use the `$s2u` skill; " +
	"its Setup section says how to guide the user through connecting machines and sharenets."

// codex is `s2u codex [codex args...]`: Codex with the Share2Us hooks, so tasks sent to
// this session's agent appear in this window (queued by `codex queue`) under the guard.
func (a app) codex(ctx context.Context, args []string) int {
	exe, err := os.Executable()
	if err != nil {
		return a.fail("find this program", err)
	}
	for _, t := range skillTargets {
		if t.Tool == "codex" {
			_, _ = installSkill(t) // keep $s2u current with this build; best effort
		}
	}
	live := daemon.CodexLiveArgs(exe)
	if live == nil {
		fmt.Fprintln(a.stderr, "Share2Us: this Codex can't load the Share2Us hooks (needs Codex CLI 0.172 or later; not on Windows yet), so tasks for this session run in the background instead of in this window.")
	}
	cmd := exec.CommandContext(ctx, "codex", append(live, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if cwd, err := os.Getwd(); err == nil {
		daemon.SetAgentEnv(cmd, cwd)
		if live != nil {
			cmd.Env = daemon.CodexLiveEnvFor(cmd.Env, cwd)
		}
	}
	signal.Ignore(os.Interrupt) // Ctrl-C belongs to Codex
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		if errors.Is(err, exec.ErrNotFound) {
			fmt.Fprintln(a.stderr, "Codex is not installed (no `codex` on PATH).")
			return 1
		}
		return a.fail("run codex", err)
	}
	return 0
}

// codexWindowHook handles the hooks of an `s2u codex` window (not PreToolUse).
func (a app) codexWindowHook(event string, in hookInput, call func(daemonctl.Request) (daemonctl.Response, bool)) {
	switch event {
	case daemon.CodexSessionStartHook:
		proveCodexWindow(in.SessionID)
		fmt.Fprintln(a.stdout, codexSessionContext)
	case daemon.CodexPromptHook:
		if daemon.CodexLiveReady(in.SessionID) == 0 {
			proveCodexWindow(in.SessionID) // SessionStart may have run before the transcript opened
		}
		_ = daemon.BindTypedPromptID(in.SessionID, in.Prompt, in.TurnID)
	case daemon.CodexStopHook:
		if in.TurnID != "" && daemon.TypedGuardPromptID(in.SessionID) == in.TurnID {
			_, _ = call(daemonctl.Request{Op: "codex-turn-done", Args: map[string]string{
				"session": in.SessionID, "turn": in.TurnID, "reply": strings.TrimSpace(in.LastAssistantMessage)}})
		}
		fmt.Fprintln(a.stdout, "{}") // Stop must answer JSON
	}
}

func proveCodexWindow(session string) {
	for i := 0; i < 10; i++ {
		if daemon.ProveCodexWindow(session) == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// codexHint says where tasks for a bound Codex session show up, and how to get them live.
func (a app) codexHint(sess daemon.DiscoveredSession) {
	if daemon.CodexLiveReady(sess.SessionID) > 0 {
		fmt.Fprintln(a.stdout, "Tasks sent to this agent appear in this window (started with s2u codex), under the Share2Us guard.")
	} else {
		fmt.Fprintln(a.stdout, "Tasks sent to this agent run in the background (codex exec resume) and their replies go back by themselves.")
		fmt.Fprintln(a.stdout, "To see them arrive in this window instead, start Codex with Share2Us next time:")
		fmt.Fprintf(a.stdout, "  %s codex resume %s\n", commandName, sess.SessionID)
	}
	fmt.Fprintln(a.stdout, "In Codex, Share2Us commands are the $s2u skill (type $s2u, not /s2u), e.g. `$s2u agents` or `$s2u send homelab check disk space`.")
}
