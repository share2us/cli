// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// The hook proves it loaded in a particular Claude process independently of
// the optional MCP channel. Both records live outside the editable project.
type terminalGuardRecord struct {
	Session string      `json:"session"`
	PID     int         `json:"pid,omitempty"`
	Zellij  *ZellijPane `json:"zellij,omitempty"`
	Request string      `json:"request,omitempty"`
}

func terminalGuardPath(session, kind string) (string, error) {
	if session == "" {
		return "", os.ErrInvalid
	}
	base, err := userConfigDir()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(session))
	return filepath.Join(base, "share2us", "agents", "terminal-guards", kind+"-"+hex.EncodeToString(hash[:])+".json"), nil
}

func writeTerminalGuard(session, kind string, record terminalGuardRecord) error {
	path, err := terminalGuardPath(session, kind)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".guard-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func readTerminalGuard(session, kind string) (terminalGuardRecord, bool) {
	path, err := terminalGuardPath(session, kind)
	if err != nil {
		return terminalGuardRecord{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return terminalGuardRecord{}, false
	}
	var record terminalGuardRecord
	if json.Unmarshal(data, &record) != nil || record.Session != session {
		return terminalGuardRecord{}, false
	}
	return record, true
}

// ProveTerminalHook is called from inside Claude's SessionStart or Stop hook.
// The PID comes from process ancestry, not from hook JSON supplied by Claude.
func ProveTerminalHook(session string, pid int, pane *ZellijPane) error {
	if pid <= 1 {
		return os.ErrInvalid
	}
	return writeTerminalGuard(session, "proof", terminalGuardRecord{Session: session, PID: pid, Zellij: pane})
}

// TerminalHookReady is deliberately independent of MCP channel polling.
func TerminalHookReady(session string, pid int) bool {
	record, ok := readTerminalGuard(session, "proof")
	return ok && pid > 1 && record.PID == pid
}

// TerminalHookPane is the two inherited Zellij identifiers attested by the
// hook. The service may be unable to read /proc/PID/environ under systemd's
// ProtectSystem=full; this stays tied to the same Claude session and PID.
func TerminalHookPane(session string, pid int) *ZellijPane {
	record, ok := readTerminalGuard(session, "proof")
	if !ok || pid <= 1 || record.PID != pid {
		return nil
	}
	return record.Zellij
}

// EndTerminalSession clears stale proof and active markers only after Claude
// exits; no tool call from that process can follow SessionEnd.
func EndTerminalSession(session string) {
	for _, kind := range []string{"proof", "active"} {
		if path, err := terminalGuardPath(session, kind); err == nil {
			_ = os.Remove(path)
		}
	}
}

// BeginTypedGuard persists the active hop before any remote text is pasted.
// If the daemon dies, the hook can still deny tool use until the turn ends.
func BeginTypedGuard(session, request string) error {
	if request == "" {
		return os.ErrInvalid
	}
	return writeTerminalGuard(session, "active", terminalGuardRecord{Session: session, Request: request})
}

func TypedGuardRequest(session string) string {
	record, ok := readTerminalGuard(session, "active")
	if !ok {
		return ""
	}
	return record.Request
}

// EndTypedGuard only removes the marker for the request that owned it.
func EndTypedGuard(session, request string) error {
	record, ok := readTerminalGuard(session, "active")
	if !ok || record.Request != request || request == "" {
		return nil
	}
	path, err := terminalGuardPath(session, "active")
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// TranscriptRequestState distinguishes the delivered turn from a later owner
// turn. An unreadable transcript is unknown and must never release the guard.
func TranscriptRequestState(path, request string) (matches, known bool) {
	if path == "" || request == "" {
		return false, false
	}
	f, err := os.Open(path)
	if err != nil {
		return false, false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	last := ""
	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Type != "user" || entry.Message.Role != "user" {
			continue
		}
		var content string
		if json.Unmarshal(entry.Message.Content, &content) == nil {
			last, known = content, true
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(entry.Message.Content, &blocks) == nil {
			parts := make([]string, 0, len(blocks))
			human := false
			for _, block := range blocks {
				if block.Type == "tool_result" {
					continue
				}
				human = true
				parts = append(parts, block.Text)
			}
			if human {
				last, known = strings.Join(parts, "\n"), true
			}
		}
	}
	if scanner.Err() != nil || !known {
		return false, false
	}
	return strings.Contains(last, "[Share2Us] from device ") && strings.Contains(last, ", request "+request+":"), true
}

func TranscriptHasLastUserRequest(path, request string) bool {
	matches, _ := TranscriptRequestState(path, request)
	return matches
}
