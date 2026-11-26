// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"path/filepath"
	"runtime"
	"testing"
)

// The reads a delivered prompt actually tries (homelab, 2026-11-26) pass inside the
// project; outside paths, writing flags and clock changes stay refused.
func TestGuardAllowsEverydayReadsInsideTheProject(t *testing.T) {
	dir := t.TempDir()
	in := "docs" // project-relative: a Windows temp path has backslashes, which the guard refuses
	cases := map[string]bool{
		`grep -rniE "gateway|wan ip" ` + in:         false,
		`grep -rni -e gateway -e "public ip" ` + in: false,
		`grep -rni gateway`:                         false, // recursive over the project
		`ls -la --full-time ` + in:                  false,
		`ls -lat ` + in:                             false,
		`date`:                                      false,
		`date -u +%H:%M`:                            false,
		`uname -a`:                                  false,
		`uptime`:                                    false,
		`date -s 2020-01-01`:                        true,
		`ls -la --color=always ` + in:               true,
		`ip -4 addr`:                                true, // network identity: privileged only
		`curl -s ifconfig.me`:                       true,
	}
	if runtime.GOOS != "windows" { // "/etc" is a POSIX absolute path
		cases[`cat /etc/timezone`] = true // outside the project
		cases[`grep -rni gateway /etc`] = true
		cases[`grep -f /etc/shadow `+in] = true
	}
	for cmd, wantDeny := range cases {
		if deny, reason := HookDecision(dir, "Bash", map[string]any{"command": cmd}, false); deny != wantDeny {
			t.Errorf("%q: deny=%v (%s), want %v", cmd, deny, reason, wantDeny)
		}
	}
}

// Privileged (set by the owner, for an agent meant to deploy) lifts the read-only list for
// typed hops; self-protection still holds.
func TestGuardPrivilegedRunsCommands(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := WriteEnforcedPolicy(dir, PrivilegePrivileged); err != nil {
		t.Fatal(err)
	}
	if deny, reason := HookDecision(dir, "Bash", map[string]any{"command": "ip -4 addr; curl -s ifconfig.me"}, false); deny {
		t.Fatalf("privileged refused: %s", reason)
	}
	if deny, _ := HookDecision(dir, "Edit", map[string]any{"file_path": filepath.Join(dir, ".s2u.rules")}, false); !deny {
		t.Fatal("self-protection lifted at privileged")
	}
}
