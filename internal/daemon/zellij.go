// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// zellijDriver is the narrow terminal-control surface used by live delivery.
// Tests provide a fake; the real driver always addresses a specific session
// and pane and bounds every command.
type zellijDriver interface {
	Sessions(context.Context) ([]string, error)
	Panes(context.Context, string) ([]zellijPaneInfo, error)
	Dump(context.Context, string, string) (string, error)
	Paste(context.Context, string, string, string) error
	Enter(context.Context, string, string) error
	// NewTab opens a new, named tab in a session. Used only to surface a loud
	// foreground warning when a bound pane cannot be located for a waiting prompt.
	NewTab(ctx context.Context, session, name string) error
}

type zellijPaneInfo struct {
	ID                int    `json:"id"`
	Plugin            bool   `json:"is_plugin"`
	TabName           string `json:"tab_name"`
	Title             string `json:"title"`
	Command           string `json:"pane_command"`
	CWD               string `json:"pane_cwd"`
	Exited            bool   `json:"exited"`
	CursorCoordinates []int  `json:"cursor_coordinates_in_pane"`
}

func (p zellijPaneInfo) paneID() string { return strconv.Itoa(p.ID) }

type resolvedZellijPane struct {
	Session string
	Pane    string
	TabName string
}

// ZellijLocation is safe display metadata for a pane whose process identity
// was revalidated. TabName is presentation only and is never used as an id.
type ZellijLocation struct {
	Session string
	Pane    string
	TabName string
}

// FindZellijLocation resolves a binding for CLI display using the same strict
// identity checks as delivery.
func FindZellijLocation(ctx context.Context, binding Binding, session DiscoveredSession) (ZellijLocation, bool) {
	pane, err := resolveZellijPane(ctx, newSystemZellij(), binding, session)
	if err != nil {
		return ZellijLocation{}, false
	}
	return ZellijLocation{Session: pane.Session, Pane: pane.Pane, TabName: pane.TabName}, true
}

type systemZellij struct{ executable string }

func newSystemZellij() *systemZellij {
	path, _ := exec.LookPath("zellij")
	// Snap's launcher refuses to run in some service/sandbox contexts, while
	// the versioned binary speaks to the same user socket directly.
	if strings.HasPrefix(path, "/snap/bin/") {
		if direct := "/snap/zellij/current/bin/zellij"; executable(direct) {
			path = direct
		}
	}
	return &systemZellij{executable: path}
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func (z *systemZellij) run(ctx context.Context, args ...string) ([]byte, error) {
	if z.executable == "" {
		return nil, errors.New("zellij is not installed")
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, z.executable, args...).Output()
}

func (z *systemZellij) Sessions(ctx context.Context) ([]string, error) {
	raw, err := z.run(ctx, "list-sessions", "--no-formatting")
	if err != nil {
		return nil, err
	}
	return parseZellijSessions(string(raw)), nil
}

func parseZellijSessions(raw string) []string {
	var sessions []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "(EXITED") {
			continue
		}
		if i := strings.Index(line, " [Created "); i > 0 {
			line = line[:i]
		}
		if line != "" {
			sessions = append(sessions, line)
		}
	}
	return sessions
}

func (z *systemZellij) Panes(ctx context.Context, session string) ([]zellijPaneInfo, error) {
	raw, err := z.run(ctx, "--session", session, "action", "list-panes", "--all", "--json")
	if err != nil {
		return nil, err
	}
	var panes []zellijPaneInfo
	if err := json.Unmarshal(raw, &panes); err != nil {
		return nil, err
	}
	return panes, nil
}

func (z *systemZellij) Dump(ctx context.Context, session, pane string) (string, error) {
	raw, err := z.run(ctx, "--session", session, "action", "dump-screen", "--ansi", "--pane-id", "terminal_"+pane)
	return string(raw), err
}

func (z *systemZellij) Paste(ctx context.Context, session, pane, text string) error {
	_, err := z.run(ctx, "--session", session, "action", "paste", "--pane-id", "terminal_"+pane, text)
	return err
}

func (z *systemZellij) Enter(ctx context.Context, session, pane string) error {
	_, err := z.run(ctx, "--session", session, "action", "send-keys", "--pane-id", "terminal_"+pane, "Enter")
	return err
}

func (z *systemZellij) NewTab(ctx context.Context, session, name string) error {
	_, err := z.run(ctx, "--session", session, "action", "new-tab", "--name", name)
	return err
}

// resolveZellijPane proves that the bound Claude process still owns exactly
// one matching pane. The recorded session name is deliberately not used to
// address zellij because it can be stale after a rename.
func resolveZellijPane(ctx context.Context, z zellijDriver, binding Binding, session DiscoveredSession) (resolvedZellijPane, error) {
	return resolveZellijPaneWith(ctx, z, binding, session, ProcessZellijPane)
}

func resolveZellijPaneWith(ctx context.Context, z zellijDriver, binding Binding, session DiscoveredSession, processPane func(int) *ZellijPane) (resolvedZellijPane, error) {
	if binding.Zellij == nil || session.PID <= 1 || binding.SessionID != session.SessionID || session.Tool != "claude" {
		return resolvedZellijPane{}, errors.New("bound session has no live zellij identity")
	}
	currentPane := processPane(session.PID)
	if currentPane == nil || *currentPane != *binding.Zellij {
		return resolvedZellijPane{}, errors.New("session process no longer matches its bound zellij pane")
	}
	sessions, err := z.Sessions(ctx)
	if err != nil {
		return resolvedZellijPane{}, err
	}
	var matches []resolvedZellijPane
	for _, zsession := range sessions {
		panes, err := z.Panes(ctx, zsession)
		if err != nil {
			// Fail closed. If even one listed session cannot be inspected, it
			// could contain another pane with the same id/project identity.
			return resolvedZellijPane{}, fmt.Errorf("inspect zellij session %q: %w", zsession, err)
		}
		for _, pane := range panes {
			if pane.Plugin || pane.Exited || pane.paneID() != binding.Zellij.Pane || !SameProject(pane.CWD, binding.Project) || !isClaudePaneCommand(pane.Command) {
				continue
			}
			matches = append(matches, resolvedZellijPane{Session: zsession, Pane: pane.paneID(), TabName: pane.TabName})
		}
	}
	if len(matches) != 1 {
		return resolvedZellijPane{}, fmt.Errorf("zellij pane match is ambiguous: found %d", len(matches))
	}
	return matches[0], nil
}

func isClaudePaneCommand(command string) bool {
	wrapper := ""
	if exe, err := os.Executable(); err == nil {
		wrapper = strings.ToLower(filepath.Base(exe))
	}
	return isClaudePaneCommandFor(command, wrapper)
}

func isClaudePaneCommandFor(command, currentExecutable string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	base := strings.ToLower(filepath.Base(strings.Trim(fields[0], "'\"")))
	if base == "claude" || base == "claude.exe" {
		return true
	}
	knownWrapper := base == "s2u" || base == "s2u.exe" || base == "share2us" || base == "share2us.exe" || base == strings.ToLower(currentExecutable)
	return knownWrapper && len(fields) > 1 && strings.Trim(fields[1], "'\"") == "claude"
}

var (
	ansiSequence      = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	foldedClaudePaste = regexp.MustCompile(`^\[Pasted text[^]]*\]$`)
)

type claudeScreenState struct {
	Busy       bool
	Dialog     bool
	Input      string
	InputEmpty bool
	InputDim   bool
}

func parseClaudeScreen(screen string) claudeScreenState {
	active := strings.ToLower(activeClaudeUI(screen))
	state := claudeScreenState{
		Busy: strings.Contains(active, "esc to interrupt"),
		Dialog: strings.Contains(active, "do you want") || strings.Contains(active, "enter to confirm") ||
			strings.Contains(active, "press enter to confirm") || strings.Contains(active, "esc to cancel"),
	}
	if line, ok := lastPromptLine(screen); ok {
		state.Input, state.InputDim = promptInput(line)
		if !state.InputDim {
			state.Input = claudeInputText(screen, state.Input)
		}
		state.InputEmpty = state.Input == "" || state.InputDim
	}
	return state
}

func safeClaudeInput(screen string) bool {
	s := parseClaudeScreen(screen)
	return claudeUIReady(screen) && !s.Busy && !s.Dialog && s.InputEmpty
}

func pastedClaudeInput(screen, prompt string) bool {
	s := parseClaudeScreen(screen)
	if !claudeUIReady(screen) || s.Busy || s.Dialog || s.Input == "" {
		return false
	}
	if withoutWhitespace(s.Input) == withoutWhitespace(prompt) {
		return true
	}
	return foldedClaudePaste.MatchString(strings.TrimSpace(s.Input))
}

// A shell prompt can also use ❯. Require Claude's own input divider and
// status/help chrome around the active input, not just a matching prompt glyph.
func claudeUIReady(screen string) bool {
	lines := strings.Split(ansiSequence.ReplaceAllString(screen, ""), "\n")
	prompt := lastPromptIndex(lines)
	if prompt < 0 {
		return false
	}
	divider := false
	for i := prompt - 1; i >= 0 && i >= prompt-12; i-- {
		if claudeHorizontalRule(lines[i]) {
			divider = true
			break
		}
	}
	if !divider {
		return false
	}
	active := strings.ToLower(activeClaudeUI(screen))
	return strings.Contains(active, "mode on") || strings.Contains(active, "? for shortcuts")
}

// activeClaudeUI excludes prior transcript from state detection. Claude draws a
// horizontal rule immediately before its current input/status area; words such
// as "do you want" or "esc to interrupt" above it are merely conversation.
func activeClaudeUI(screen string) string {
	lines := strings.Split(ansiSequence.ReplaceAllString(screen, ""), "\n")
	prompt := lastPromptIndex(lines)
	if prompt < 0 {
		// A permission dialog can replace the prompt. Keep a small tail rather
		// than treating the entire transcript as live UI.
		start := len(lines) - 12
		if start < 0 {
			start = 0
		}
		return strings.Join(lines[start:], "\n")
	}
	start := prompt - 12
	if start < 0 {
		start = 0
	}
	for i := prompt - 1; i >= 0; i-- {
		if claudeHorizontalRule(lines[i]) {
			start = i + 1
			break
		}
	}
	return strings.Join(lines[start:], "\n")
}

// claudeInputText reconstructs an unfolded multiline paste. A normal Claude
// input is bounded below by a horizontal rule. Without that boundary (as in
// partial dumps and simple fixtures), only the prompt line is trusted.
func claudeInputText(screen, first string) string {
	lines := strings.Split(ansiSequence.ReplaceAllString(screen, ""), "\n")
	prompt := lastPromptIndex(lines)
	if prompt < 0 {
		return first
	}
	end := -1
	for i := prompt + 1; i < len(lines); i++ {
		if claudeHorizontalRule(lines[i]) {
			end = i
			break
		}
	}
	if end < 0 {
		return first
	}
	parts := []string{first}
	for _, line := range lines[prompt+1 : end] {
		line = strings.TrimSpace(strings.TrimRight(line, "│"))
		if line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, "\n")
}

func lastPromptIndex(lines []string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "❯") {
			return i
		}
	}
	return -1
}

func claudeHorizontalRule(line string) bool {
	return strings.Count(line, "─") >= 5
}

func withoutWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

func lastPromptLine(screen string) (string, bool) {
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(ansiSequence.ReplaceAllString(lines[i], ""), "❯") {
			return lines[i], true
		}
	}
	return "", false
}

// promptInput returns visible text after the input marker and whether every
// non-space character is dim (Claude's suggestion placeholder).
func promptInput(line string) (string, bool) {
	plain := ansiSequence.ReplaceAllString(line, "")
	marker := strings.LastIndex(plain, "❯")
	if marker < 0 {
		return "", false
	}
	want := strings.TrimSpace(strings.TrimRight(plain[marker+len("❯"):], "│"))
	if want == "" {
		return "", false
	}
	// Track SGR dim state while collecting visible bytes after the marker.
	rawMarker := strings.LastIndex(line, "❯")
	if rawMarker < 0 {
		return want, false
	}
	dim, saw, allDim := false, false, true
	rest := line[rawMarker+len("❯"):]
	for len(rest) > 0 {
		if loc := ansiSequence.FindStringIndex(rest); loc != nil && loc[0] == 0 {
			seq := rest[:loc[1]]
			if strings.HasSuffix(seq, "m") {
				params := strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m")
				for _, p := range strings.Split(params, ";") {
					switch p {
					case "", "0", "22":
						dim = false
					case "2":
						dim = true
					}
				}
			}
			rest = rest[loc[1]:]
			continue
		}
		r, n := utf8.DecodeRuneInString(rest)
		rest = rest[n:]
		if !strings.ContainsRune(" \t│", r) {
			saw = true
			allDim = allDim && dim
		}
	}
	return want, saw && allDim
}
