// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Finding the agent session a command is running INSIDE (phase-7 §10).
//
// `!s2u agent join <code>` is typed into a Claude session, and Claude runs it
// as a child process of that session's `claude` process. So the session is the
// ancestor of this process whose pid `claude agents --json` reports. Nothing is
// looked up by name or directory, and nothing has to be copied: the process
// tree is the proof of which session asked.

// ErrNotInSession means no ancestor of this process is a live agent session.
var ErrNotInSession = errors.New("not running inside an agent session")

// maxAncestry bounds the walk (shells, wrappers, sudo...).
const maxAncestry = 24

// FindOwnSession returns the agent session this process is running inside.
func FindOwnSession(ctx context.Context) (DiscoveredSession, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "claude", "agents", "--json").Output()
	if err != nil {
		return DiscoveredSession{}, ErrNotInSession
	}
	byPID, err := claudeSessionsByPID(out)
	if err != nil || len(byPID) == 0 {
		return DiscoveredSession{}, ErrNotInSession
	}
	return ownSession(os.Getpid(), byPID, parentPID)
}

// ownSession walks from pid up through its ancestors to the first one that is a
// session's process. parent is injected for tests.
func ownSession(pid int, byPID map[int]DiscoveredSession, parent func(int) (int, error)) (DiscoveredSession, error) {
	for i := 0; i < maxAncestry && pid > 1; i++ {
		if s, ok := byPID[pid]; ok {
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
