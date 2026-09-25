// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Privilege is how much an injected run may do, chosen PER AGENT by the machine's
// owner (ADR-041 §6). It used to be one `--agent-strict` flag for every runner on
// the machine, which could not express the thing the design needs: a locked-down
// QA agent and a deploying devops agent side by side on one laptop.
//
// The P0 prototype is the evidence (docs/ideas/inbox/p0-orchestration-run.md, F1):
// Codex could not push, and the reason was not Codex — it was our adapter
// hardcoding `workspace-write` from a machine-wide flag. Agents may legitimately
// run with full privilege on their owner's laptop or an SSH session.
type Privilege string

const (
	// PrivilegeRestricted is read-only: Claude `plan`, Codex `read-only`,
	// Gemini `plan`. What `--agent-strict` used to mean.
	PrivilegeRestricted Privilege = "restricted"
	// PrivilegeStandard is the DEFAULT and is unchanged from ADR-036: edit inside
	// the workspace, no network, baseline denies (push/delete/network) enforced.
	PrivilegeStandard Privilege = "standard"
	// PrivilegePrivileged is the explicit opt-out an owner sets for an agent that
	// is supposed to deploy: network and pushes allowed, baseline denies dropped.
	// It never reaches a tool's "no guardrails at all" mode (Claude
	// bypassPermissions, Gemini yolo) — see privilegedNote in the adapters.
	PrivilegePrivileged Privilege = "privileged"
)

// rank orders privileges so the most restrictive can win a conflict.
func (p Privilege) rank() int {
	switch p {
	case PrivilegeRestricted:
		return 0
	case PrivilegePrivileged:
		return 2
	default:
		return 1
	}
}

// Valid reports whether p is a privilege we understand. An unknown value in a
// policy file is treated as absent rather than as an escalation.
func (p Privilege) Valid() bool {
	switch p {
	case PrivilegeRestricted, PrivilegeStandard, PrivilegePrivileged:
		return true
	}
	return false
}

// tightest returns the most restrictive of the given privileges, ignoring empties.
// Every source can TIGHTEN and only the enforced file can LOOSEN, which is what
// makes an in-repo policy safe to read at all (see AgentPolicy).
func tightest(ps ...Privilege) Privilege {
	out := PrivilegeStandard
	seen := false
	for _, p := range ps {
		if !p.Valid() {
			continue
		}
		if !seen || p.rank() < out.rank() {
			out, seen = p, true
		}
	}
	return out
}

// agentPolicyFile is the on-disk shape. Only `privilege` is read today; the file
// is where an agent's rules and disclosure policy will join it (ADR-041 §5).
type agentPolicyFile struct {
	// Project is written on create purely so a human can tell the directories
	// apart; nothing reads it.
	Project   string `yaml:"project,omitempty"`
	Privilege string `yaml:"privilege"`
}

// EnforcedPolicyPath is where the policy the daemon OBEYS lives: outside the
// agent's writable tree, keyed by the project directory (ADR-041 §5, Q119).
//
// It is deliberately not inside the repo. An agent runs with write access to its
// own project, so a policy it can edit is a policy it can widen — and `.s2u/`
// inside a git clone also gets committed, which is fine for team defaults and
// wrong for anything enforced.
func EnforcedPolicyPath(projectDir string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "share2us", "agents", projectSlug(projectDir), "policy.yaml"), nil
}

// projectSlug keys a policy directory by project path: a readable basename for
// humans plus a hash so two clones with the same basename never collide.
func projectSlug(projectDir string) string {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		abs = projectDir
	}
	sum := sha256.Sum256([]byte(abs))
	name := filepath.Base(abs)
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = "project"
	}
	return name + "-" + hex.EncodeToString(sum[:])[:12]
}

// repoPolicyPath is the in-repo defaults file. It may only TIGHTEN (ADR-041 §5:
// "an in-repo .s2u/ may carry team defaults only").
func repoPolicyPath(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	return filepath.Join(projectDir, ".s2u", "policy.yaml")
}

// readPolicyFile returns the privilege declared by a policy file, or "" when the
// file is missing, unreadable or malformed. Failing to a zero value matters: a
// corrupt policy must not silently promote an agent, and tightest() treats the
// empty value as absent, so the effective privilege falls back to the default.
func readPolicyFile(path string) Privilege {
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var f agentPolicyFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return ""
	}
	p := Privilege(strings.ToLower(strings.TrimSpace(f.Privilege)))
	if !p.Valid() {
		return ""
	}
	return p
}

// AgentPolicy resolves the privilege for one injected run in projectDir.
//
// Three sources, and the rule between them is asymmetric on purpose:
//   - the ENFORCED file (outside the tree) may loosen or tighten — it is the
//     owner speaking;
//   - the IN-REPO file may only tighten, because the agent can write it;
//   - forceRestricted (the daemon's `--agent-strict`) always wins, as a
//     machine-wide safety override.
//
// With no files at all this returns PrivilegeStandard, which is exactly what
// ADR-036 did before this existed.
func AgentPolicy(projectDir string, forceRestricted bool) Privilege {
	if forceRestricted {
		return PrivilegeRestricted
	}
	enforced := PrivilegeStandard
	if path, err := EnforcedPolicyPath(projectDir); err == nil {
		if p := readPolicyFile(path); p.Valid() {
			enforced = p
		}
	}
	return tightest(enforced, readPolicyFile(repoPolicyPath(projectDir)))
}

// WriteEnforcedPolicy records an agent's privilege for projectDir. This is the
// owner's act, run from their own shell — never something an injected run can do
// (the enforced file sits outside the project the agent can write).
func WriteEnforcedPolicy(projectDir string, p Privilege) (string, error) {
	if !p.Valid() {
		return "", os.ErrInvalid
	}
	path, err := EnforcedPolicyPath(projectDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	abs, aerr := filepath.Abs(projectDir)
	if aerr != nil {
		abs = projectDir
	}
	out, err := yaml.Marshal(agentPolicyFile{Project: abs, Privilege: string(p)})
	if err != nil {
		return "", err
	}
	header := "# Share2Us agent policy (ADR-041 §6). The daemon reads this; the agent cannot.\n" +
		"# privilege: restricted | standard | privileged\n"
	if err := os.WriteFile(path, append([]byte(header), out...), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
