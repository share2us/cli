// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRepoPolicy puts an in-repo (agent-writable) policy in place.
func writeRepoPolicy(t *testing.T, project, body string) {
	t.Helper()
	dir := filepath.Join(project, ".s2u")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newProject gives each test its own project dir AND its own config dir, so the
// enforced policy of one test cannot leak into another (or into the developer's
// real ~/.config).
func newProject(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return t.TempDir()
}

func TestAgentPolicyDefaultsToStandard(t *testing.T) {
	// No policy anywhere is the ADR-036 behaviour this replaced: work in the
	// workspace, no network. A missing file must never mean "privileged".
	if got := AgentPolicy(newProject(t), false); got != PrivilegeStandard {
		t.Fatalf("no policy => %s, want standard", got)
	}
}

func TestEnforcedPolicyCanRaiseAndLower(t *testing.T) {
	for _, want := range []Privilege{PrivilegeRestricted, PrivilegeStandard, PrivilegePrivileged} {
		project := newProject(t)
		if _, err := WriteEnforcedPolicy(project, want); err != nil {
			t.Fatal(err)
		}
		if got := AgentPolicy(project, false); got != want {
			t.Fatalf("enforced %s => %s", want, got)
		}
	}
}

// The asymmetry is the whole security property: the agent can write its own
// repo, so an in-repo policy must be able to tighten and never to loosen.
func TestInRepoPolicyMayTightenOnly(t *testing.T) {
	project := newProject(t)
	if _, err := WriteEnforcedPolicy(project, PrivilegePrivileged); err != nil {
		t.Fatal(err)
	}
	writeRepoPolicy(t, project, "privilege: restricted\n")
	if got := AgentPolicy(project, false); got != PrivilegeRestricted {
		t.Fatalf("in-repo tightening ignored: got %s, want restricted", got)
	}

	project2 := newProject(t)
	if _, err := WriteEnforcedPolicy(project2, PrivilegeStandard); err != nil {
		t.Fatal(err)
	}
	// This is the escalation attempt: an injected run writes its own policy file.
	writeRepoPolicy(t, project2, "privilege: privileged\n")
	if got := AgentPolicy(project2, false); got != PrivilegeStandard {
		t.Fatalf("an in-repo file promoted the agent to %s — it must only tighten", got)
	}
}

func TestForceRestrictedBeatsEverything(t *testing.T) {
	project := newProject(t)
	if _, err := WriteEnforcedPolicy(project, PrivilegePrivileged); err != nil {
		t.Fatal(err)
	}
	if got := AgentPolicy(project, true); got != PrivilegeRestricted {
		t.Fatalf("--agent-strict did not win: got %s", got)
	}
}

func TestMalformedPolicyFallsBackRatherThanPromotes(t *testing.T) {
	for _, body := range []string{
		"privilege: root\n",      // unknown level
		"privilege:\n",           // empty
		"{{{ not yaml at all",    // unparseable
		"something_else: true\n", // no privilege key
	} {
		project := newProject(t)
		path, err := EnforcedPolicyPath(project)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := AgentPolicy(project, false); got != PrivilegeStandard {
			t.Fatalf("policy %q resolved to %s, want the standard fallback", body, got)
		}
	}
}

func TestEnforcedPolicyLivesOutsideTheProject(t *testing.T) {
	project := newProject(t)
	path, err := EnforcedPolicyPath(project)
	if err != nil {
		t.Fatal(err)
	}
	// If the enforced policy were inside the project, the agent could edit the
	// thing that governs it (ADR-041 §5).
	if strings.HasPrefix(path, project) {
		t.Fatalf("enforced policy %q is inside the project %q", path, project)
	}
	if _, err := WriteEnforcedPolicy(project, PrivilegeStandard); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("policy mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestTwoProjectsWithTheSameNameDoNotShareAPolicy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	a := filepath.Join(root, "a", "docs")
	b := filepath.Join(root, "b", "docs")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := WriteEnforcedPolicy(a, PrivilegePrivileged); err != nil {
		t.Fatal(err)
	}
	if got := AgentPolicy(b, false); got != PrivilegeStandard {
		t.Fatalf("a second project named %q inherited %s", filepath.Base(b), got)
	}
}

// Privilege changes the deny list, not just the mode: this is what actually lets
// a devops agent push (p0-orchestration-run.md, F1).
func TestPrivilegedDropsBaselineDeniesButKeepsSelfProtection(t *testing.T) {
	std := CompileRules(nil, PrivilegeStandard)
	if !containsPattern(std.DisallowedTools, "Bash(git push:*)") {
		t.Fatal("standard privilege lost its baseline push deny")
	}

	priv := CompileRules(nil, PrivilegePrivileged)
	if containsPattern(priv.DisallowedTools, "Bash(git push:*)") {
		t.Fatal("privileged still denies git push, so a deploying agent cannot deploy")
	}
	for _, must := range selfProtection {
		if !containsPattern(priv.DisallowedTools, must) {
			t.Fatalf("privileged dropped self-protection %q — an agent could rewrite its own rules", must)
		}
	}
	// An explicit prohibition is still hard, whatever the privilege.
	explicit := CompileRules([]string{"never push"}, PrivilegePrivileged)
	if !containsPattern(explicit.DisallowedTools, "Bash(git push:*)") {
		t.Fatal("an explicit `never push` must still compile to a deny at privileged")
	}
}

func containsPattern(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
