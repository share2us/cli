// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A Binding is a session's permission to exist on the server: the daemon
// advertises a discovered agent session ONLY when its project and tool are bound
// (ADR-041 §1).
//
// Before this, registration was "everything the adapter can find", which the P0
// prototype showed for what it is (p0-orchestration-run.md, F2): running the
// bridge for two project directories advertised every other Claude and Codex
// session on the machine — unrelated client work, by name and working directory.
// On a shared sharenet that is another member's view of your whole desk.
//
// So a binding is a whitelist entry, and it is also where the privilege for that
// project lives (privilege.go). One object, three jobs, per ADR-041.
type Binding struct {
	// AgentID is this agent's stable identity (ADR-041 §1a): created once when the
	// binding is made and NEVER changed afterwards. Session ids rotate — a fork per
	// injected prompt, a new id on recreation — so anything that must outlive a
	// single prompt, above all an invitation into another owner's project, is keyed
	// to this. It lives here, outside the project, because the agent can edit its
	// own project and an identity it could rewrite is not an identity.
	AgentID string `json:"agent_id,omitempty"`
	// Project is the absolute, cleaned session working directory.
	Project string `json:"project"`
	// Tool is "claude", "codex" or "gemini". A binding is per tool: binding a
	// Claude session in a directory does not advertise a Codex one beside it.
	Tool string `json:"tool"`
	// Label is the sharenet project this is meant to join. Nothing consumes it
	// yet — server-side projects are ADR-041 P2 — but the owner names it when
	// binding, so it is recorded rather than asked for twice.
	Label   string    `json:"label,omitempty"`
	BoundAt time.Time `json:"bound_at"`
}

// bindingsFile is the on-disk shape, versioned so the format can move.
type bindingsFile struct {
	Version  int       `json:"version"`
	Bindings []Binding `json:"bindings"`
}

const bindingsVersion = 1

// BindingsPath is where the local whitelist lives: beside the enforced policies,
// outside any project, so an injected run cannot bind itself into visibility.
func BindingsPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "share2us", "agents", "bindings.json"), nil
}

// normalizeProject makes two spellings of the same directory compare equal.
func normalizeProject(project string) string {
	p := strings.TrimSpace(project)
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return filepath.Clean(p)
}

// LoadBindings reads the whitelist. A missing file is not an error: it means
// nothing is bound, which is the safe reading — no sessions are advertised.
func LoadBindings() ([]Binding, error) {
	path, err := BindingsPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f bindingsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	return f.Bindings, nil
}

func saveBindings(list []Binding) error {
	path, err := BindingsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Project != list[j].Project {
			return list[i].Project < list[j].Project
		}
		return list[i].Tool < list[j].Tool
	})
	out, err := json.MarshalIndent(bindingsFile{Version: bindingsVersion, Bindings: list}, "", "  ")
	if err != nil {
		return err
	}
	// Write-then-rename: a crash mid-write must not truncate the file that holds
	// every agent's identity.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// IsBound reports whether sessions of this tool in this project may be
// advertised. An empty project (a session whose directory we could not
// determine) is never bound: we will not advertise what we cannot name.
func IsBound(list []Binding, project, tool string) bool {
	p := normalizeProject(project)
	if p == "" {
		return false
	}
	for _, b := range list {
		if b.Tool == tool && normalizeProject(b.Project) == p {
			return true
		}
	}
	return false
}

// Bind adds a binding, or updates the label of one that already exists. It
// returns the stored binding and whether it was newly created.
func Bind(project, tool, label string) (Binding, bool, error) {
	p := normalizeProject(project)
	if p == "" || tool == "" {
		return Binding{}, false, os.ErrInvalid
	}
	list, err := LoadBindings()
	if err != nil {
		return Binding{}, false, err
	}
	for i := range list {
		if list[i].Tool == tool && normalizeProject(list[i].Project) == p {
			if label != "" {
				list[i].Label = label
			}
			// A binding made before agent ids existed gets one now — and only here,
			// in a path that already writes. Backfilling on READ would make the
			// daemon a writer racing this command, and a lost race regenerates the
			// id: an agent's identity silently changing is the one thing this field
			// must never do. An existing id is never replaced.
			if list[i].AgentID == "" {
				id, err := newAgentID()
				if err != nil {
					return Binding{}, false, err
				}
				list[i].AgentID = id
			}
			return list[i], false, saveBindings(list)
		}
	}
	id, err := newAgentID()
	if err != nil {
		return Binding{}, false, err
	}
	b := Binding{AgentID: id, Project: p, Tool: tool, Label: label, BoundAt: time.Now().UTC()}
	list = append(list, b)
	return b, true, saveBindings(list)
}

// Unbind removes bindings for a project, for one tool or (when tool is empty)
// for every tool. It returns how many were removed.
func Unbind(project, tool string) (int, error) {
	p := normalizeProject(project)
	if p == "" {
		return 0, os.ErrInvalid
	}
	list, err := LoadBindings()
	if err != nil {
		return 0, err
	}
	kept := make([]Binding, 0, len(list))
	removed := 0
	for _, b := range list {
		if normalizeProject(b.Project) == p && (tool == "" || b.Tool == tool) {
			removed++
			continue
		}
		kept = append(kept, b)
	}
	if removed == 0 {
		return 0, nil
	}
	return removed, saveBindings(kept)
}

// agentIDPrefix makes an agent id recognisable wherever it appears — in a log, an
// invitation, a support request — and lets the server reject anything that is not
// one before it reaches the database.
const agentIDPrefix = "agt_"

// newAgentID returns a random agent id: 128 bits, unguessable, so knowing one
// agent's id says nothing about another's.
func newAgentID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return agentIDPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// BindingFor returns the binding covering a session's project and tool.
func BindingFor(list []Binding, project, tool string) (Binding, bool) {
	p := normalizeProject(project)
	if p == "" {
		return Binding{}, false
	}
	for _, b := range list {
		if b.Tool == tool && normalizeProject(b.Project) == p {
			return b, true
		}
	}
	return Binding{}, false
}

// AgentIDForProject returns the stable agent id bound to a project directory,
// whatever its tool. It reports false when there is none, or when several tools
// are bound to the directory with different ids and the choice would be a guess.
func AgentIDForProject(list []Binding, project string) (string, bool) {
	p := normalizeProject(project)
	if p == "" {
		return "", false
	}
	id := ""
	for _, b := range list {
		if normalizeProject(b.Project) != p || b.AgentID == "" {
			continue
		}
		if id != "" && id != b.AgentID {
			return "", false
		}
		id = b.AgentID
	}
	return id, id != ""
}
