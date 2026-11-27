// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Gemini CLI runs `!command` lines as children of its own process but tells them nothing
// about the session (GEMINI_SESSION_ID is only given to hooks). So a Gemini session is
// found in two steps:
//
//  1. an ancestor process IS the Gemini CLI (its command line runs the `gemini` program);
//  2. the session is the chat file in that process's project folder
//     (~/.gemini/tmp/<project>/chats/session-*.json) updated most recently since the
//     process started: typing the command just wrote to it.
//
// Two Gemini windows in the same folder could be confused only if both were written in the
// same moment; the join/bind output names the session it picked.

// geminiProc is what selfsession needs to know about a process.
type geminiProc struct {
	Args    []string
	CWD     string
	Started time.Time
}

// readGeminiProc is replaced in tests.
var readGeminiProc = procInfo

// geminiSessionOf reports the Gemini session a process runs, if it is the Gemini CLI.
func geminiSessionOf(pid int) (DiscoveredSession, bool) {
	p, ok := readGeminiProc(pid)
	if !ok || !isGeminiCommand(p.Args) || p.CWD == "" {
		return DiscoveredSession{}, false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return DiscoveredSession{}, false
	}
	s, ok := latestGeminiSession(geminiChatsDirs(home, p.CWD), p.Started)
	if !ok {
		return DiscoveredSession{}, false
	}
	s.Project, s.PID = p.CWD, pid
	return s, true
}

// isGeminiCommand: `gemini ...`, or `node /…/gemini ...` / `node …/@google/gemini-cli/…`.
func isGeminiCommand(args []string) bool {
	for i, a := range args {
		if i > 2 {
			break
		}
		base := strings.ToLower(filepath.Base(a))
		if base == "gemini" || base == "gemini.js" || base == "gemini.exe" || strings.Contains(a, "@google/gemini-cli") {
			return true
		}
	}
	return false
}

// geminiChatsDirs lists where a project's chats can live: the name projects.json gives the
// folder (newer Gemini), then the folder's sha256 (older Gemini).
func geminiChatsDirs(home, project string) []string {
	base := filepath.Join(home, ".gemini", "tmp")
	var out []string
	if raw, err := os.ReadFile(filepath.Join(home, ".gemini", "projects.json")); err == nil {
		var gp geminiProjects
		if json.Unmarshal(raw, &gp) == nil {
			if name, ok := gp.Projects[project]; ok && name != "" && !strings.ContainsAny(name, `/\`) {
				out = append(out, filepath.Join(base, name, "chats"))
			}
		}
	}
	sum := sha256.Sum256([]byte(project))
	return append(out, filepath.Join(base, hex.EncodeToString(sum[:]), "chats"))
}

// latestGeminiSession picks the most recently written session file, written no earlier than
// the process started (a session from an older run in the same folder is not this one). The
// session id is read from the file, never from its name.
func latestGeminiSession(dirs []string, since time.Time) (DiscoveredSession, bool) {
	var best DiscoveredSession
	var bestMod time.Time
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasPrefix(name, "session-") || !strings.HasSuffix(name, ".json") {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().Before(since.Add(-2*time.Second)) || !info.ModTime().After(bestMod) {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			var head struct {
				SessionID string `json:"sessionId"`
				Kind      string `json:"kind"`
			}
			if json.Unmarshal(raw, &head) != nil || strings.TrimSpace(head.SessionID) == "" || (head.Kind != "" && head.Kind != "main") {
				continue
			}
			best = DiscoveredSession{SessionID: head.SessionID, Tool: "gemini", Status: "available"}
			bestMod = info.ModTime()
		}
	}
	return best, best.SessionID != ""
}

// procInfo reads a process's command line, working folder and start time: /proc on Linux,
// ps + lsof on macOS. Best-effort: anything unreadable means "not a Gemini process".
func procInfo(pid int) (geminiProc, bool) {
	switch runtime.GOOS {
	case "linux":
		dir := "/proc/" + strconv.Itoa(pid)
		raw, err := os.ReadFile(dir + "/cmdline")
		if err != nil {
			return geminiProc{}, false
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		cwd, _ := os.Readlink(dir + "/cwd")
		var started time.Time
		if info, err := os.Stat(dir); err == nil {
			started = info.ModTime()
		}
		return geminiProc{Args: args, CWD: cwd, Started: started}, true
	case "darwin":
		out, err := exec.Command("ps", "-o", "lstart=,command=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return geminiProc{}, false
		}
		line := strings.TrimSpace(string(out))
		// lstart is a fixed 24-character date ("Fri Nov 27 10:58:46 2026").
		if len(line) < 25 {
			return geminiProc{}, false
		}
		started, _ := time.ParseInLocation("Mon Jan _2 15:04:05 2006", strings.Join(strings.Fields(line[:24]), " "), time.Local)
		p := geminiProc{Args: strings.Fields(line[24:]), Started: started}
		if raw, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output(); err == nil {
			for _, l := range strings.Split(string(raw), "\n") {
				if strings.HasPrefix(l, "n/") {
					p.CWD = l[1:]
				}
			}
		}
		return p, true
	}
	return geminiProc{}, false
}
