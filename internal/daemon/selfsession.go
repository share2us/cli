// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Finding the agent session a command is running INSIDE (phase-7 §10).
//
// `!s2u agent join <code>` is typed into an agent session, and the tool runs it
// as a child of that session's process. So the session is found by walking up
// this process's ancestors to the first one that IS a session:
//
//   - Claude: an ancestor whose pid `claude agents --json` reports;
//   - Codex: an ancestor that holds its session's rollout file open
//     (~/.codex/sessions/.../rollout-*.jsonl), whose header names the session.
//
// Nothing is looked up by name or directory, and nothing has to be copied: the
// process tree is the proof of which session asked.

// ErrNotInSession means no ancestor of this process is a live agent session.
var ErrNotInSession = errors.New("not running inside an agent session")

// maxAncestry bounds the walk (shells, wrappers, sudo...).
const maxAncestry = 24

// FindOwnSession returns the agent session this process is running inside.
func FindOwnSession(ctx context.Context) (DiscoveredSession, error) {
	claude := map[int]DiscoveredSession{}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(cctx, "claude", "agents", "--json").Output(); err == nil {
		if m, err := claudeSessionsByPID(out); err == nil {
			claude = m
		}
	}
	match := func(pid int) (DiscoveredSession, bool) {
		if s, ok := claude[pid]; ok {
			return s, true
		}
		return codexSessionOf(pid)
	}
	return ownSession(os.Getpid(), match, parentPID)
}

// ownSession walks from pid up through its ancestors to the first one match
// recognises as a session's process. match and parent are injected for tests.
func ownSession(pid int, match func(int) (DiscoveredSession, bool), parent func(int) (int, error)) (DiscoveredSession, error) {
	for i := 0; i < maxAncestry && pid > 1; i++ {
		if s, ok := match(pid); ok {
			return s, nil
		}
		next, err := parent(pid)
		if err != nil || next == pid {
			break
		}
		pid = next
	}
	return DiscoveredSession{}, ErrNotInSession
}

func claudeSessionsByPID(out []byte) (map[int]DiscoveredSession, error) {
	var entries []claudeAgentEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, err
	}
	m := map[int]DiscoveredSession{}
	for _, e := range entries {
		if e.PID <= 1 || strings.TrimSpace(e.SessionID) == "" {
			continue
		}
		m[e.PID] = DiscoveredSession{
			SessionID: e.SessionID, Tool: "claude", Name: e.Name, Project: e.CWD, Status: claudeStatus(e),
		}
	}
	return m, nil
}

// codexSessionOf reports the Codex session a process is, if it holds a rollout
// file open. Only files under a `.codex/sessions/` directory named rollout-*.jsonl
// count, and the session is read from the file's own header, never the name.
func codexSessionOf(pid int) (DiscoveredSession, bool) {
	for _, path := range openFiles(pid) {
		if !strings.Contains(path, string(filepath.Separator)+".codex"+string(filepath.Separator)+"sessions"+string(filepath.Separator)) {
			continue
		}
		base := filepath.Base(path)
		if !strings.HasPrefix(base, "rollout-") || !strings.HasSuffix(base, ".jsonl") {
			continue
		}
		if s, ok := parseCodexRollout(path); ok {
			return s, true
		}
	}
	return DiscoveredSession{}, false
}

// openFiles lists the paths a process holds open: /proc on Linux, lsof on macOS.
// Best-effort: an unreadable process (not ours) simply has none.
func openFiles(pid int) []string {
	var out []string
	switch runtime.GOOS {
	case "linux":
		dir := "/proc/" + strconv.Itoa(pid) + "/fd"
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil
		}
		for _, e := range entries {
			if target, err := os.Readlink(filepath.Join(dir, e.Name())); err == nil {
				out = append(out, target)
			}
		}
	case "darwin":
		raw, err := exec.Command("lsof", "-p", strconv.Itoa(pid), "-Fn").Output()
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "n/") {
				out = append(out, line[1:])
			}
		}
	}
	return out
}

// parentPID reads a process's parent: /proc on Linux, ps elsewhere.
func parentPID(pid int) (int, error) {
	if runtime.GOOS == "linux" {
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return 0, err
		}
		// "pid (comm) state ppid ...": comm may contain spaces or parentheses,
		// so split after the LAST closing parenthesis.
		s := string(raw)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			return 0, errors.New("unparsable stat")
		}
		fields := strings.Fields(s[i+1:])
		if len(fields) < 2 {
			return 0, errors.New("unparsable stat")
		}
		return strconv.Atoi(fields[1])
	}
	if runtime.GOOS == "windows" {
		return 0, errors.New("process ancestry is not supported on windows yet")
	}
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}
