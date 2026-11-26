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
	"sync"
	"time"

	clicore "github.com/share2us/cli-core"
)

// Live delivery into an open Codex window (`s2u codex`). `codex queue` hands the prompt to
// the open session natively (no typing into a terminal), and the window's hooks do the rest:
// UserPromptSubmit ties the queued prompt to its Codex turn, PreToolUse guards only that
// turn (the owner's own turns stay free), and Stop reports the turn's final message.
// Verified with codex-cli 0.172.0 (2026-11-26): with -c hooks the TUI runs the session in
// its own process, the hooks are its children, and a queued message runs in the window.
const (
	CodexSessionStartHook = "codex-session-start"
	CodexPromptHook       = "codex-user-prompt-submit"
	CodexStopHook         = "codex-stop"
	// CodexLiveEnv marks a window started by `s2u codex`: its PreToolUse hook guards only
	// delivered turns, not every tool call as in a headless run.
	CodexLiveEnv = "S2U_CODEX_LIVE"
)

// CodexLiveArgs are the codex flags `s2u codex` adds, or nil when this Codex can't run
// hooks this way (live delivery is then off; tasks run in the background as before).
func CodexLiveArgs(exe string) []string {
	if runtime.GOOS == "windows" || !codexHookSupported() {
		return nil
	}
	return []string{
		"--dangerously-bypass-hook-trust",
		"-c", codexHookEntry(exe, "SessionStart", "", CodexSessionStartHook, 30, ""),
		"-c", codexHookEntry(exe, "UserPromptSubmit", "", CodexPromptHook, 30, ""),
		"-c", codexHookConfig(exe),
		"-c", codexHookEntry(exe, "Stop", "", CodexStopHook, 30, ""),
	}
}

// CodexLiveEnvFor is the environment of an `s2u codex` window in project. The session is
// left empty: the hook takes Codex's own.
func CodexLiveEnvFor(env []string, project string) []string {
	kept := make([]string, 0, len(env))
	for _, e := range env {
		if k, _, _ := strings.Cut(e, "="); k != CodexLiveEnv {
			kept = append(kept, e)
		}
	}
	return append(codexGuardEnv(kept, project, "", false), CodexLiveEnv+"=1")
}

// ProveCodexWindow records, from inside a hook, which Codex process holds session: the
// nearest ancestor with the session's transcript open. Process ancestry, not hook JSON.
func ProveCodexWindow(session string) error {
	pid := os.Getpid()
	for i := 0; i < maxAncestry && pid > 1; i++ {
		if s, ok := codexSessionOf(pid); ok && s.SessionID == session {
			forgetOtherCodexProofs(session, pid)
			return ProveTerminalPromptHook(session, pid, nil)
		}
		next, err := parentPID(pid)
		if err != nil || next == pid {
			break
		}
		pid = next
	}
	return errors.New("no Codex process holds this session")
}

// CodexLiveReady is the pid of the `s2u codex` window holding session now, or 0.
func CodexLiveReady(session string) int { return codexLiveReady(session) }

var codexLiveReady = func(session string) int {
	record, ok := readTerminalGuard(session, "proof")
	if !ok || record.PID <= 1 || !record.PromptHook {
		return 0
	}
	// Codex makes its process unreadable to others (no /proc/PID/fd for the service), so the
	// check is the command line: still an `s2u codex` window (its hooks are on it).
	if !strings.Contains(processCommandLine(record.PID), "agent hook "+CodexStopHook) {
		return 0 // closed, or the pid now belongs to something else
	}
	return record.PID
}

// processCommandLine is pid's command line, "" when unknown.
func processCommandLine(pid int) string {
	switch runtime.GOOS {
	case "linux":
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		if err != nil {
			return ""
		}
		return strings.ReplaceAll(string(raw), "\x00", " ")
	case "darwin":
		raw, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
		if err != nil {
			return ""
		}
		return string(raw)
	}
	return ""
}

// forgetOtherCodexProofs: one window shows one session. When it starts or resumes another
// (/new, the session switcher), the proof for the one it left must go, or a hop would be
// queued to a session nobody has open.
func forgetOtherCodexProofs(session string, pid int) {
	base, err := userConfigDir()
	if err != nil {
		return
	}
	dir := filepath.Join(base, "share2us", "agents", "terminal-guards")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "proof-") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r terminalGuardRecord
		if json.Unmarshal(raw, &r) == nil && r.PID == pid && r.Session != session {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// CodexTurnGuarded: this Codex turn is a delivered hop's. Bound turns match exactly; a hop
// whose header is already in the transcript but whose turn didn't bind stays guarded.
func CodexTurnGuarded(session, turn, transcript string) bool {
	record, ok := readTerminalGuard(session, "active")
	if !ok || record.Request == "" {
		return false
	}
	if record.PromptID != "" {
		return record.PromptID == turn
	}
	return !TypedHopNotStarted(session, transcript)
}

var codexQueue = func(ctx context.Context, session, cwd, message string) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "codex", "queue", "--thread", session, "--message", message)
	if cwd != "" {
		cmd.Dir = cwd
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New(err.Error() + ": " + string(out))
	}
	return nil
}

type codexLiveState struct {
	mu      sync.Mutex
	locks   map[string]*sync.Mutex // session -> one live hop at a time
	waiting map[string]chan string // request -> its turn's final message
	session map[string]string      // request -> session
}

func (rt *Runtime) codexLive() *codexLiveState {
	rt.codexLiveOnce.Do(func() {
		rt.codexLiveSt = &codexLiveState{locks: map[string]*sync.Mutex{}, waiting: map[string]chan string{}, session: map[string]string{}}
	})
	return rt.codexLiveSt
}

func (s *codexLiveState) lock(session string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks[session] == nil {
		s.locks[session] = &sync.Mutex{}
	}
	return s.locks[session]
}

func (s *codexLiveState) expect(session, request string) chan string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan string, 1)
	s.waiting[request], s.session[request] = ch, session
	return ch
}

func (s *codexLiveState) drop(request string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.waiting, request)
	delete(s.session, request)
}

// finished is the Stop hook's report for session's turn: the reply, if that turn was a hop's.
func (rt *Runtime) codexTurnFinished(session, turn, reply string) bool {
	record, ok := readTerminalGuard(session, "active")
	if !ok || record.Request == "" || record.PromptID == "" || record.PromptID != turn {
		return false
	}
	s := rt.codexLive()
	s.mu.Lock()
	ch := s.waiting[record.Request]
	s.mu.Unlock()
	_ = EndTypedGuard(session, record.Request)
	if ch == nil {
		return false
	}
	select {
	case ch <- reply:
	default:
	}
	return true
}

var codexLivePoll = 3 * time.Second

// startCodexLive delivers req into the open window and waits for its answer off the receive
// loop. A window that is gone (or a queue that fails) falls back to the background run.
func (rt *Runtime) startCodexLive(ctx context.Context, client AgentClient, runner AgentRunner, deps Deps, req clicore.AgentRequest, shown, prompt, senderName, cwd string, waited time.Duration) {
	session := req.TargetSessionID
	rt.holding.Add(1)
	go func() {
		defer rt.holding.Add(-1)
		l := rt.codexLive().lock(session) // a second hop waits for the first one's turn to end
		l.Lock()
		defer l.Unlock()
		pid := CodexLiveReady(session)
		if pid == 0 {
			rt.runHeadless(ctx, client, runner, deps, req, shown, prompt, senderName, cwd, waited)
			return
		}
		if err := beginTypedGuard(session, req.ID, pid, true); err != nil {
			rt.runHeadless(ctx, client, runner, deps, req, shown, prompt, senderName, cwd, waited)
			return
		}
		done := rt.codexLive().expect(session, req.ID)
		defer rt.codexLive().drop(req.ID)
		defer rt.runningHeadless(session, req.ID)()
		if err := codexQueue(ctx, session, cwd, deliveredPromptText(req, senderName, prompt)); err != nil {
			deps.logf("agent-bridge: codex queue for %s failed, running it in the background: %v", req.ID, err)
			_ = EndTypedGuard(session, req.ID)
			rt.runHeadless(ctx, client, runner, deps, req, shown, prompt, senderName, cwd, waited)
			return
		}
		deps.logf("agent-bridge: inject %s queued into the open Codex window (session %s)", req.ID, session)
		rt.notify("Share2Us", "A task from "+senderName+" arrived in your Codex window")
		_ = client.AgentReportResult(ctx, req.ID, "running", "Delivered into the open Codex window; it runs there after any turn in progress.")
		hop := HopRecord{Time: time.Now().UTC(), RequestID: req.ID, Tool: req.Tool, From: req.SenderDeviceID,
			Target: session, RanIn: session, Mode: "typed", Prompt: shown}
		if waited > 0 {
			hop.Waited = waited.Round(time.Second).String()
		}
		deadline := requestDeadline(req)
		t := time.NewTicker(codexLivePoll)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case reply := <-done:
				hop.Status = "done"
				_ = AppendHop(hop)
				if len(reply) > maxReportedResult {
					reply = reply[:maxReportedResult]
				}
				_ = client.AgentReportResult(ctx, req.ID, "done", replyForSender(reply))
				return
			case <-t.C:
			}
			fail := ""
			switch {
			case CodexLiveReady(session) != pid:
				fail = "the Codex window was closed before it answered"
			case time.Now().After(deadline):
				fail = "the Codex window didn't finish the task in time"
			}
			if fail != "" {
				_ = EndTypedGuard(session, req.ID)
				hop.Status = "failed"
				_ = AppendHop(hop)
				_ = client.AgentReportResult(ctx, req.ID, "failed", fail)
				return
			}
		}
	}()
}
