// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/share2us/cli/internal/daemon"
)

// The channel's agent tools run the CLI's own commands in-process, so a Claude session
// gets exactly what `s2u agent list|send|status|cancel` do, without being told how.
func (a app) channelAgentTools() []daemon.ChannelTool {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	return []daemon.ChannelTool{
		{
			Name:        "list_agents",
			Description: "List the agent sessions this session can send work to: the owner's other machines, and agents in shared projects. Shows each agent's id, tool, status, name and device.",
			Properties:  map[string]any{},
			Run: func(ctx context.Context, _ json.RawMessage) (string, bool) {
				return a.runCaptured(ctx, "agent", "list")
			},
		},
		{
			Name: "send_to_agent",
			Description: "Send a task to another agent session through Share2Us (end-to-end encrypted). " +
				"`to` is the agent's name or alias, its device's name when that device runs one agent (e.g. \"homelab\"), or an agent id from list_agents. " +
				"The prompt must stand alone (the other agent sees nothing else) and say what to report back. " +
				"The result comes back to this session by itself as a kind=\"result\" event; don't poll.",
			Properties: map[string]any{
				"to":      str("Agent name, alias, device name, or agent id"),
				"prompt":  str("The task, written for an agent that has no other context"),
				"file":    str("Optional path of a file to send with it"),
				"project": str("Optional sharenet project id, for an agent in another account"),
			},
			Required:     []string{"to", "prompt"},
			NotDuringHop: true,
			Run: func(ctx context.Context, raw json.RawMessage) (string, bool) {
				var in struct{ To, Prompt, File, Project string }
				if err := json.Unmarshal(raw, &in); err != nil || strings.TrimSpace(in.To) == "" || strings.TrimSpace(in.Prompt) == "" {
					return "send_to_agent needs `to` and `prompt`.", true
				}
				args := []string{"agent", "send", "--to", in.To, "--prompt", in.Prompt}
				if in.File != "" {
					args = append(args, "--file", in.File)
				}
				if in.Project != "" {
					args = append(args, "--project", in.Project)
				}
				return a.runCaptured(ctx, args...)
			},
		},
		{
			Name:        "request_status",
			Description: "Read the status and result of a Share2Us request this session sent (or of a request by id).",
			Properties:  map[string]any{"request_id": str("The request id send_to_agent returned")},
			Required:    []string{"request_id"},
			Run: func(ctx context.Context, raw json.RawMessage) (string, bool) {
				var in struct {
					RequestID string `json:"request_id"`
				}
				if json.Unmarshal(raw, &in) != nil || in.RequestID == "" {
					return "request_status needs `request_id`.", true
				}
				return a.runCaptured(ctx, "agent", "status", in.RequestID)
			},
		},
		{
			Name:         "cancel_request",
			Description:  "Withdraw a request this session sent, if it hasn't started running.",
			Properties:   map[string]any{"request_id": str("The request id to cancel")},
			Required:     []string{"request_id"},
			NotDuringHop: true,
			Run: func(ctx context.Context, raw json.RawMessage) (string, bool) {
				var in struct {
					RequestID string `json:"request_id"`
				}
				if json.Unmarshal(raw, &in) != nil || in.RequestID == "" {
					return "cancel_request needs `request_id`.", true
				}
				return a.runCaptured(ctx, "agent", "cancel", in.RequestID)
			},
		},
	}
}

// runCaptured runs a CLI command with its output captured for a tool result.
func (a app) runCaptured(ctx context.Context, args ...string) (string, bool) {
	var out, errOut bytes.Buffer
	sub := a
	sub.stdin = strings.NewReader("")
	sub.stdout, sub.stderr = &out, &errOut
	sub.stdinIsTTY = func(io.Reader) bool { return false }
	sub.stdoutIsTTY = func(io.Writer) bool { return false }
	code := sub.run(ctx, args)
	text := strings.TrimSpace(strings.TrimSpace(out.String()) + "\n" + strings.TrimSpace(errOut.String()))
	if text == "" {
		text = fmt.Sprintf("(exit %d, no output)", code)
	}
	return text, code != 0
}

//go:embed skills/s2u/SKILL.md
var s2uSkill []byte

const skillManagedMarker = "share2us-managed"

// installClaudeSkill writes the /s2u skill to ~/.claude/skills/s2u. A copy the user
// edited (marker line removed) is left alone. Quiet: a failure only loses the skill.
func installClaudeSkill() (path string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".claude", "skills", "s2u")
	path = filepath.Join(dir, "SKILL.md")
	if cur, rerr := os.ReadFile(path); rerr == nil {
		if bytes.Equal(cur, s2uSkill) {
			return path, nil
		}
		if !bytes.Contains(cur, []byte(skillManagedMarker)) {
			return path, errSkillUserOwned
		}
	} else if !errors.Is(rerr, fs.ErrNotExist) {
		return path, rerr
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return path, err
	}
	return path, os.WriteFile(path, s2uSkill, 0o644)
}

var errSkillUserOwned = errors.New("left as is: it was edited by hand (the share2us-managed line is gone)")

// agentSkill: `s2u agent skill [install]` installs or refreshes the /s2u Claude skill.
func (a app) agentSkill(args []string) int {
	if len(args) > 0 && args[0] != "install" {
		fmt.Fprintf(a.stderr, "usage: %s agent skill [install]\n", commandName)
		return 2
	}
	path, err := installClaudeSkill()
	if err != nil {
		return a.fail("install the /s2u skill ("+path+")", err)
	}
	fmt.Fprintf(a.stdout, "Installed the /s2u skill for Claude Code: %s\nClaude sessions now know how to reach your other agents through Share2Us (new sessions pick it up).\n", path)
	return 0
}
