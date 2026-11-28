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
	"os"
	"os/exec"
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
			Name: "channel_read",
			Description: "Read a sharenet channel's timeline (ADR-053): messages, agents added, files, and tasks sent inside a project with their replies, oldest first. " +
				"`channel` is \"<sharenet>\" (every channel), \"<sharenet>.general\" or \"<sharenet>.<project>\". Pass `since` (the last id you saw) to get only what's new.",
			Properties: map[string]any{
				"channel": str("<sharenet>, <sharenet>.general or <sharenet>.<project>"),
				"since":   map[string]any{"type": "integer", "description": "Only events after this id"},
			},
			Required: []string{"channel"},
			Run: func(ctx context.Context, raw json.RawMessage) (string, bool) {
				var in struct {
					Channel string `json:"channel"`
					Since   int64  `json:"since"`
				}
				if json.Unmarshal(raw, &in) != nil || strings.TrimSpace(in.Channel) == "" {
					return "channel_read needs `channel`, e.g. \"acme.build\".", true
				}
				args := []string{"sharenet", in.Channel, "activity"}
				if in.Since > 0 {
					args = append(args, "--since", fmt.Sprint(in.Since))
				}
				return a.runCaptured(ctx, args...)
			},
		},
		{
			Name: "channel_post",
			Description: "Post a message in a sharenet channel, as this session's agent (its owner must have allowed it to post in that project). " +
				"`channel` is \"<sharenet>.<project>\" (agents post in project channels). Post only when your owner asked you to, or the task says to report in the channel. " +
				"Never include secrets: a message that looks like it holds a key or token is refused.",
			Properties: map[string]any{
				"channel":  str("<sharenet>.<project>"),
				"text":     str("The message"),
				"reply_to": map[string]any{"type": "integer", "description": "Optional id of the message this replies to"},
			},
			Required:     []string{"channel", "text"},
			NotDuringHop: true,
			Run: func(ctx context.Context, raw json.RawMessage) (string, bool) {
				var in struct {
					Channel string `json:"channel"`
					Text    string `json:"text"`
					ReplyTo int64  `json:"reply_to"`
				}
				if json.Unmarshal(raw, &in) != nil || strings.TrimSpace(in.Channel) == "" || strings.TrimSpace(in.Text) == "" {
					return "channel_post needs `channel` and `text`.", true
				}
				args := []string{"sharenet", in.Channel, "say", in.Text}
				if in.ReplyTo > 0 {
					args = append(args, "--reply", fmt.Sprint(in.ReplyTo))
				}
				return a.runCaptured(ctx, args...)
			},
		},
		{
			Name: "dm_send",
			Description: "Message one member of a sharenet privately, as this session's agent (ADR-054): to ask a clarifying question, hand over a result only they should see, or ask for something sensitive. " +
				"`project` is \"<sharenet>.<project>\" (the project you work in; your owner must have allowed you to post there), `to` is their username. Their reply comes back to you as a task. " +
				"The project shows that you messaged them privately, never the content. Never put a secret in the text; to receive one, ask them to use the secret box in the conversation.",
			Properties: map[string]any{
				"project": str("<sharenet>.<project>"),
				"to":      str("The member's username"),
				"text":    str("The message"),
			},
			Required:     []string{"project", "to", "text"},
			NotDuringHop: true,
			Run: func(ctx context.Context, raw json.RawMessage) (string, bool) {
				var in struct {
					Project string `json:"project"`
					To      string `json:"to"`
					Text    string `json:"text"`
				}
				if json.Unmarshal(raw, &in) != nil || strings.TrimSpace(in.Project) == "" || strings.TrimSpace(in.To) == "" || strings.TrimSpace(in.Text) == "" {
					return "dm_send needs `project`, `to` and `text`.", true
				}
				return a.runCaptured(ctx, "sharenet", in.Project, "dm", "@"+strings.TrimPrefix(strings.TrimSpace(in.To), "@"), in.Text)
			},
		},
		{
			Name: "secret_get",
			Description: "Save a secret a member shared with you in a direct message (ADR-054) to a private file on this machine, and get back its label and path, never the value. " +
				"`id` is the secret id from the task (\"share2us secret get <id>\"). Read the file only where the task needs the value, and never repeat the value in chat, replies or commits.",
			Properties: map[string]any{
				"id":   str("The secret id"),
				"path": str("Optional file to save it to (default: the Share2Us config folder, secrets/<id>); must not exist yet"),
			},
			Required: []string{"id"},
			// Allowed during a hop: the secret arrives as a DM reply, which is a delivered task.
			Run: func(ctx context.Context, raw json.RawMessage) (string, bool) {
				var in struct {
					ID   string `json:"id"`
					Path string `json:"path"`
				}
				if json.Unmarshal(raw, &in) != nil || strings.TrimSpace(in.ID) == "" {
					return "secret_get needs `id`.", true
				}
				args := []string{"secret", "get", strings.TrimSpace(in.ID)}
				if strings.TrimSpace(in.Path) != "" {
					args = append(args, "-o", in.Path)
				}
				return a.runCaptured(ctx, args...)
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
var s2uSkillBody []byte

const skillManagedMarker = "share2us-managed"

const skillDescription = "Talk to other coding agents and share files through Share2Us: list reachable agents, send or ask an agent on another machine or project (e.g. \"ask homelab to...\"), check or cancel a request, read results, share a file as a link."

// skillTarget is one agent tool that loads Agent Skills (SKILL.md folders).
type skillTarget struct {
	Tool, Label string
	// Dir is the tool's config dir under $HOME; the skill goes to Dir/skills/s2u.
	Dir string
	// Bin is looked up on PATH to tell whether the tool is installed.
	Bin string
	// ArgumentHint: Claude Code shows it while typing /s2u. Codex and Gemini validate
	// the frontmatter, so they get only the standard name + description.
	ArgumentHint bool
}

var skillTargets = []skillTarget{
	{Tool: "claude", Label: "Claude Code", Dir: ".claude", Bin: "claude", ArgumentHint: true},
	{Tool: "codex", Label: "Codex", Dir: ".codex", Bin: "codex"},
	{Tool: "gemini", Label: "Gemini CLI", Dir: ".gemini", Bin: "gemini"},
}

func skillContent(t skillTarget) []byte {
	var b bytes.Buffer
	desc, _ := json.Marshal(skillDescription) // a JSON string is valid quoted YAML; the text has ": "
	b.WriteString("---\nname: s2u\ndescription: " + string(desc) + "\n")
	if t.ArgumentHint {
		b.WriteString("argument-hint: \"[agents | agent <agent> <prompt> | <sharenet>.<agent> send <prompt> | agent pause|resume | sharenet list | sharenet <name> | sharenet create <name> | sharenet <name> invite <email> | sharenet <name> code | agent add <agent> <sharenet> | status <id> | wait <id> | results | cancel <id> | pending | share <file> | devices | login | status | update | setup | guide | help]\"\n")
	}
	b.WriteString("---\n")
	b.Write(s2uSkillBody)
	return b.Bytes()
}

func skillPath(t skillTarget) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, t.Dir, "skills", "s2u", "SKILL.md"), nil
}

// skillPresent: the tool looks installed (its binary is on PATH or its config dir exists).
func skillPresent(t skillTarget) bool {
	if _, err := lookPath(t.Bin); err == nil {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	st, err := os.Stat(filepath.Join(home, t.Dir))
	return err == nil && st.IsDir()
}

var lookPath = exec.LookPath

// skillState: "current", "outdated" (our copy, older build), "edited" (the user's own),
// or "missing".
func skillState(t skillTarget) string {
	path, err := skillPath(t)
	if err != nil {
		return "missing"
	}
	cur, err := os.ReadFile(path)
	switch {
	case err != nil:
		return "missing"
	case bytes.Equal(cur, skillContent(t)):
		return "current"
	case bytes.Contains(cur, []byte(skillManagedMarker)):
		return "outdated"
	default:
		return "edited"
	}
}

// installSkill writes the skill for one tool. A hand-edited copy is left alone.
func installSkill(t skillTarget) (string, error) {
	path, err := skillPath(t)
	if err != nil {
		return "", err
	}
	if skillState(t) == "edited" {
		return path, errSkillUserOwned
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, err
	}
	if err := os.WriteFile(path, skillContent(t), 0o644); err != nil {
		return path, err
	}
	return path, nil
}

// installClaudeSkill keeps the Claude Code copy current (s2u claude, agent bind).
func installClaudeSkill() (string, error) { return installSkill(skillTargets[0]) }

var errSkillUserOwned = errors.New("left as is: it was edited by hand (the share2us-managed line is gone)")

// agentSkill: `s2u agent skill [install|status] [claude|codex|gemini|--all]`.
func (a app) agentSkill(args []string) int {
	action := "install"
	if len(args) > 0 && (args[0] == "install" || args[0] == "status") {
		action, args = args[0], args[1:]
	}
	var targets []skillTarget
	all := hasFlag(args, "--all")
	for _, t := range skillTargets {
		named := false
		for _, a := range args {
			if a == t.Tool {
				named = true
			}
		}
		if named || all || (len(args) == 0 && skillPresent(t)) {
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		if len(args) > 0 && !all {
			fmt.Fprintf(a.stderr, "usage: %s agent skill [install|status] [claude] [codex] [gemini] [--all]\n", commandName)
			return 2
		}
		fmt.Fprintln(a.stdout, "No Claude Code, Codex or Gemini CLI found here. Name one to install anyway, e.g. `"+commandName+" agent skill install codex`.")
		return 0
	}
	code := 0
	for _, t := range targets {
		if action == "status" {
			path, _ := skillPath(t)
			fmt.Fprintf(a.stdout, "%-12s %-9s %s\n", t.Label, skillState(t), path)
			continue
		}
		path, err := installSkill(t)
		if err != nil {
			fmt.Fprintf(a.stderr, "%s: %s (%s)\n", t.Label, err, path)
			code = 1
			continue
		}
		fmt.Fprintf(a.stdout, "%-12s /s2u installed: %s\n", t.Label, path)
	}
	if action == "install" && code == 0 {
		fmt.Fprintln(a.stdout, "New sessions pick it up. Try: /s2u list, /s2u send <agent> <task>, /s2u share <file> (in Codex: $s2u).")
	}
	return code
}

// skillUpdateHint runs in the NEW binary right after `s2u update`: refresh copies we
// manage, and point at tools that don't have the skill yet.
func (a app) skillUpdateHint() {
	var missing []string
	for _, t := range skillTargets {
		if !skillPresent(t) {
			continue
		}
		switch skillState(t) {
		case "outdated":
			if _, err := installSkill(t); err == nil {
				fmt.Fprintf(a.stdout, "Refreshed the /s2u skill for %s.\n", t.Label)
			}
		case "missing":
			missing = append(missing, t.Label)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(a.stdout, "Tip: let %s reach your other agents and share files: run `%s agent skill install` (adds the /s2u skill).\n", strings.Join(missing, ", "), commandName)
	}
}
