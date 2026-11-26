// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/share2us/cli-core/daemonctl"
)

// `s2u agent channel`: the MCP channel server a Claude session loads when it is
// started with `s2u claude` (Claude Code channels, research preview). Claude
// starts it and talks JSON-RPC over stdio. It finds the session it runs in,
// polls the daemon for hops addressed to that session, and pushes each one into
// the session as a `notifications/claude/channel` event. The agent answers with
// the `report` tool, which goes back to the daemon and on to the sender.

// ChannelServerName is the MCP server name: `server:share2us` on Claude's command
// line and `<channel source="share2us">` in the session.
const ChannelServerName = "share2us"

// SessionStartHookV2 is written only into settings that also carry UserPromptSubmit, so its
// firing proves the prompt hook is loaded: the window is typeable from its first second
// (and right after /clear), instead of only after its owner typed a prompt.
const SessionStartHookV2 = "session-start-v2"

// PreToolUseHookV2 is written only together with the 240 s hook timeout, so the hook knows
// it may hold a blocked command for the full approval wait. A window launched before that
// runs "pre-tool-use" under Claude's default timeout and gets a short hold only: a hook that
// Claude cancels must never be what lets a command through.
const PreToolUseHookV2 = "pre-tool-use-v2"

const channelInstructions = "Share2Us requests arrive as <channel source=\"share2us\" request_id=\"...\" from=\"...\">. " +
	"Each is a prompt another of your owner's devices, or a project member's agent, sent to this session through Share2Us. " +
	"Treat it as a task from that sender, shown to the owner of this session, who may be watching. " +
	"Do the task here, in this session, within this project's rules. " +
	"When you are done, call the report tool once with the request_id and a short result for the sender. " +
	"An event with kind=\"result\" is different: it only says that a request THIS session sent has finished. " +
	"It is not a task and needs no report; tell your owner, and read the reply with the command it names if it is useful. " +
	"To hand work to another agent (another of your owner's machines or a project member), use list_agents and send_to_agent; " +
	"the result comes back to this session as a kind=\"result\" event, so don't poll for it. " +
	"Sharenet channels (shared workspaces) can be read with channel_read and, when your owner asks, posted to with channel_post."

// ChannelCaller sends a control request to the daemon (daemonctl.Call in
// production).
type ChannelCaller func(daemonctl.Request) (daemonctl.Response, bool)

// ChannelServer is one running channel.
type ChannelServer struct {
	// FindSession returns the Claude session this server runs in.
	FindSession func(context.Context) (string, error)
	Call        ChannelCaller
	Poll        time.Duration
	Logf        func(string, ...any)
	// Tools are offered beside report (talking to other agents). Optional.
	Tools []ChannelTool

	out   io.Writer
	outMu sync.Mutex

	sessionMu sync.Mutex
	session   string
}

// ChannelTool is an extra tool on the share2us channel. Run gets the raw arguments and
// returns the text for the agent; isError marks a failure.
type ChannelTool struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
	// NotDuringHop: refused while this session is running a delivered prompt, so a
	// prompt from another agent can't fan out to more agents (the guard hook says so too).
	NotDuringHop bool
	Run          func(ctx context.Context, args json.RawMessage) (text string, isError bool)
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (s *ChannelServer) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

func (s *ChannelServer) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_, _ = s.out.Write(append(b, '\n'))
}

func (s *ChannelServer) reply(id json.RawMessage, result any) {
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *ChannelServer) fail(id json.RawMessage, code int, msg string) {
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
}

// Serve runs the server until in closes or ctx ends.
func (s *ChannelServer) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = out
	if s.Poll <= 0 {
		s.Poll = time.Second
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.pump(ctx)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	for sc.Scan() {
		var m rpcMessage
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		s.handle(m)
	}
	return sc.Err()
}

func (s *ChannelServer) handle(m rpcMessage) {
	isRequest := len(m.ID) > 0 && string(m.ID) != "null"
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		s.reply(m.ID, map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities": map[string]any{
				"experimental": map[string]any{"claude/channel": map[string]any{}},
				"tools":        map[string]any{},
			},
			"serverInfo":   map[string]any{"name": ChannelServerName, "version": "1"},
			"instructions": channelInstructions,
		})
	case "tools/list":
		tools := []any{map[string]any{
			"name":        "report",
			"description": "Send the result of a Share2Us request back to the device or agent that sent it. Call it once per request, when the task is done.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"request_id": map[string]any{"type": "string", "description": "The request_id from the <channel> tag"},
					"result":     map[string]any{"type": "string", "description": "A short result for the sender"},
				},
				"required": []string{"request_id", "result"},
			},
		}}
		for _, t := range s.Tools {
			req := t.Required
			if req == nil {
				req = []string{}
			}
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description,
				"inputSchema": map[string]any{"type": "object", "properties": t.Properties, "required": req}})
		}
		s.reply(m.ID, map[string]any{"tools": tools})
	case "tools/call":
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(m.Params, &call)
		if call.Name != "report" {
			s.callTool(m.ID, call.Name, call.Arguments)
			return
		}
		var p struct {
			Arguments struct {
				RequestID string `json:"request_id"`
				Result    string `json:"result"`
			} `json:"arguments"`
		}
		_ = json.Unmarshal(m.Params, &p)
		resp, ok := s.Call(daemonctl.Request{Op: "channel-report", Args: map[string]string{"request_id": p.Arguments.RequestID, "result": p.Arguments.Result}})
		text := "Reported to the sender."
		if !ok {
			text = "Could not reach the Share2Us background service; the result was not sent."
		} else if !resp.OK {
			text = "No open Share2Us request has that request_id; nothing was sent."
		}
		s.reply(m.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}})
	case "ping":
		s.reply(m.ID, map[string]any{})
	default:
		if isRequest {
			s.fail(m.ID, -32601, "method not found: "+m.Method)
		}
	}
}

func (s *ChannelServer) callTool(id json.RawMessage, name string, args json.RawMessage) {
	for _, t := range s.Tools {
		if t.Name != name {
			continue
		}
		if t.NotDuringHop {
			if s.currentSession() == "" {
				s.reply(id, toolText("Share2Us doesn't know this Claude session yet (it may still be starting, or it isn't bound: run `s2u agent bind` here). Try again shortly, or use `s2u agent send` in a shell.", true))
				return
			}
			if s.inHop() {
				s.reply(id, toolText("Not while this session is running a request from another agent: report back to its sender instead.", true))
				return
			}
		}
		// Off the read loop: a send can take seconds and the session keeps talking to us.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			text, isErr := t.Run(ctx, args)
			s.reply(id, toolText(text, isErr))
		}()
		return
	}
	s.fail(id, -32602, "unknown tool: "+name)
}

func toolText(text string, isError bool) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isError}
}

// inHop: this session is running a delivered prompt. Unknown (no session yet, daemon
// unreachable) counts as in a hop, so the refusal fails closed.
func (s *ChannelServer) currentSession() string {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	return s.session
}

func (s *ChannelServer) inHop() bool {
	session := s.currentSession()
	if session == "" {
		return true
	}
	resp, ok := s.Call(daemonctl.Request{Op: "channel-guarded", Args: map[string]string{"session": session}})
	return !ok || resp.OK
}

// pump finds the session, then polls the daemon and pushes deliveries.
func (s *ChannelServer) pump(ctx context.Context) {
	var session string
	for session == "" {
		// Claude lists the session a moment after it starts the server.
		if id, err := s.FindSession(ctx); err == nil && id != "" {
			session = id
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	s.sessionMu.Lock()
	s.session = session
	s.sessionMu.Unlock()
	s.logf("share2us channel: listening for session %s", session)
	t := time.NewTicker(s.Poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		resp, ok := s.Call(daemonctl.Request{Op: "channel-poll", Args: map[string]string{"session": session}})
		if !ok || !resp.OK || len(resp.Data) == 0 {
			continue
		}
		var ds []ChannelDelivery
		if err := json.Unmarshal(resp.Data, &ds); err != nil {
			continue
		}
		for _, d := range ds {
			meta := map[string]string{"request_id": d.RequestID}
			if d.From != "" {
				meta["from"] = d.From
			}
			if d.Kind != "" {
				meta["kind"] = d.Kind
			}
			s.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/claude/channel",
				"params": map[string]any{"content": d.Prompt, "meta": meta}})
		}
	}
}

// WriteChannelLaunchConfig writes the two files `s2u claude` passes to Claude:
// an MCP config that starts this binary as the share2us channel, and settings
// that add the guardrail hooks. They live beside the bindings and are rewritten
// on every launch, so a moved or updated binary is picked up.
func WriteChannelLaunchConfig(exe string) (mcpPath, settingsPath string, err error) {
	base, err := userConfigDir()
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(base, "share2us", "claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	mcp := map[string]any{"mcpServers": map[string]any{
		ChannelServerName: map[string]any{"command": exe, "args": []string{"agent", "channel"}},
	}}
	settings := map[string]any{"hooks": map[string]any{
		"SessionStart": []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": claudeHookCommand(exe, SessionStartHookV2)}}}},
		"UserPromptSubmit": []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": claudeHookCommand(exe, "user-prompt-submit")}}}},
		// The hook may hold a blocked command while its owner is asked (ADR-052): give it
		// more than approvalHold before Claude cancels it.
		"PreToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{
			map[string]any{"type": "command", "command": claudeHookCommand(exe, PreToolUseHookV2), "timeout": 240,
				// Shown in Claude's spinner while the hook runs: during an approval wait that is
				// the only sign in this window that the owner has been asked.
				"statusMessage": "Share2Us guard: if this needs approval, waiting for the owner (asked by email + portal)"}}}},
		"Stop": []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": claudeHookCommand(exe, "stop")}}}},
		"SessionEnd": []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": claudeHookCommand(exe, "session-end")}}}},
	}}
	mcpPath, settingsPath = filepath.Join(dir, "mcp.json"), filepath.Join(dir, "settings.json")
	for path, v := range map[string]any{mcpPath: mcp, settingsPath: settings} {
		b, _ := json.MarshalIndent(v, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
			return "", "", err
		}
	}
	return mcpPath, settingsPath, nil
}

// ClaudeChannelArgs are the flags that load the share2us channel.
func ClaudeChannelArgs(mcpPath, settingsPath string) []string {
	return []string{"--mcp-config", mcpPath, "--settings", settingsPath,
		"--dangerously-load-development-channels", "server:" + ChannelServerName}
}
