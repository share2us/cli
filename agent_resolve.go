// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/share2us/cli/internal/daemon"
)

// Resolving "." (or a folder path) to one of this device's own agents.
//
// "." used to mean only "the agent bound to the current directory", which breaks
// for the very thing it names: a coding agent cds around constantly, so from a
// subfolder "." found nothing and the error told the person to bind again, a
// session that was bound seconds earlier. Now "." is this session's agent first,
// then the folder's, then the nearest bound parent folder's.

// ownSessionID is the agent session this process runs inside, found from the process
// tree like `agent bind` does. A variable so tests never see the developer's session.
var ownSessionID = func(ctx context.Context) string {
	s, err := daemon.FindOwnSession(ctx)
	if err != nil {
		return ""
	}
	return s.SessionID
}

// isAgentPathArg reports whether an agent argument is clearly a folder path rather
// than a name or id: absolute, ~-relative, ./ or ../ relative, or containing a path
// separator (agent names never do).
func isAgentPathArg(q string) bool {
	if q == "." || q == ".." || q == "~" || filepath.IsAbs(q) {
		return true
	}
	if strings.ContainsRune(q, '/') {
		return true
	}
	return runtime.GOOS == "windows" && strings.ContainsRune(q, '\\')
}

// expandAgentPath turns a path argument into an absolute, cleaned folder.
func expandAgentPath(q string) (string, error) {
	if q == "~" || strings.HasPrefix(q, "~/") || strings.HasPrefix(q, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		q = filepath.Join(home, q[1:])
	}
	return filepath.Abs(q)
}

// resolveLocalAgent resolves "." or a folder path to the id of an agent bound on
// this device. "." prefers the session this runs in; a path does not (it names a
// folder, so the folder decides).
func resolveLocalAgent(ctx context.Context, q string) (string, error) {
	list, err := daemon.LoadBindings()
	if err != nil {
		return "", fmt.Errorf("could not read this device's agent bindings: %w", err)
	}
	var dir string
	if q == "." {
		if id, ok := sessionAgent(ctx, list); ok {
			return id, nil
		}
		if dir, err = os.Getwd(); err != nil {
			return "", err
		}
	} else if dir, err = expandAgentPath(q); err != nil {
		return "", err
	}
	return agentForFolder(list, dir)
}

// sessionAgent is the agent of the session this process runs inside: the id
// `s2u claude` (and the daemon) put in the environment, or the bound session found
// from the process tree.
func sessionAgent(ctx context.Context, list []daemon.Binding) (string, bool) {
	if env := strings.TrimSpace(os.Getenv(daemon.AgentEnvVar)); env != "" {
		for _, b := range list {
			if b.AgentID == env {
				return env, true
			}
		}
	}
	// The process-tree walk runs `claude agents`; skip it when no binding names a
	// session, since then there is nothing it could match.
	hasSession := false
	for _, b := range list {
		if b.SessionID != "" {
			hasSession = true
			break
		}
	}
	if !hasSession {
		return "", false
	}
	if b, ok := daemon.BindingForSession(list, ownSessionID(ctx)); ok && b.AgentID != "" {
		return b.AgentID, true
	}
	return "", false
}

// errNoLocalAgent marks "nothing is bound there", so callers can tell it from a
// failure to read the bindings.
var errNoLocalAgent = errors.New("no agent is bound there")

type noLocalAgentError struct{ msg string }

func (e noLocalAgentError) Error() string { return e.msg }
func (e noLocalAgentError) Unwrap() error { return errNoLocalAgent }

// agentForFolder returns the agent bound to dir, or else to its nearest bound parent
// folder. Several different agents bound to that one folder (one per tool) is
// ambiguous and listed rather than guessed.
func agentForFolder(list []daemon.Binding, dir string) (string, error) {
	dir = filepath.Clean(dir)
	for d := dir; ; {
		var ids []string
		var where []string
		for _, b := range list {
			if b.AgentID == "" || !daemon.SameProject(b.Project, d) || contains(ids, b.AgentID) {
				continue
			}
			ids = append(ids, b.AgentID)
			where = append(where, fmt.Sprintf("%s (%s, %s)", b.AgentID, b.Tool, b.Project))
		}
		switch {
		case len(ids) == 1:
			return ids[0], nil
		case len(ids) > 1:
			return "", fmt.Errorf("several agents are bound at %s: %s; name the one you mean by its id", d, strings.Join(where, ", "))
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return "", noLocalAgentError{msg: notBoundMessage(list, dir)}
}

// notBoundMessage says what is actually true: which agents ARE bound on this device
// (so the person can use an id or cd there), and only suggests binding when none are.
func notBoundMessage(list []daemon.Binding, dir string) string {
	var bound []string
	for _, b := range list {
		if b.AgentID != "" {
			bound = append(bound, fmt.Sprintf("%s (%s)", b.Project, b.AgentID))
		}
	}
	if len(bound) == 0 {
		return fmt.Sprintf("no agent is bound at %s, and none is bound on this device yet; run `%s agent bind` inside the agent session you want to add", dir, commandName)
	}
	sort.Strings(bound)
	return fmt.Sprintf("no agent is bound at %s or any folder above it; agents bound on this device: %s\nuse one of those agent ids, or run the command from inside that folder", dir, strings.Join(bound, ", "))
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
