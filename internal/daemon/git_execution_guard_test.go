// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func gitForGuardTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func installFSMonitorMarker(t *testing.T, project string) (marker, command string) {
	t.Helper()
	marker = filepath.Join(project, "fsmonitor-ran")
	t.Setenv("S2U_TEST_FSMONITOR_MARKER", marker)
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "hook"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	from, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer from.Close()
	to, err := os.OpenFile(filepath.Join(project, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(to, from); err != nil {
		to.Close()
		t.Fatal(err)
	}
	if err := to.Close(); err != nil {
		t.Fatal(err)
	}
	return marker, "./" + name
}

// The second half deliberately runs real Git without the guard. It proves the
// fixture is exploitable: removing the guard turns the first assertion red,
// while Git still writes the marker for include.path, includeIf and a direct
// relative core.fsmonitor path.
func TestDeliveredGitReadCanExecuteConfiguredPrograms(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	for _, mode := range []string{"include.path", "includeIf", "relative-fsmonitor"} {
		t.Run(mode, func(t *testing.T) {
			project := t.TempDir()
			gitForGuardTest(t, project, "init", "-q")
			if err := os.WriteFile(filepath.Join(project, "sample.txt"), []byte("sample\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			gitForGuardTest(t, project, "add", "sample.txt")
			gitForGuardTest(t, project, "-c", "user.name=S12 Test", "-c", "user.email=s12@example.invalid", "commit", "-qm", "seed")
			marker, hook := installFSMonitorMarker(t, project)
			config := filepath.Join(project, ".git", "config")
			var addition string
			switch mode {
			case "include.path":
				addition = "\n[include]\n path = ../gitconfig.local\n"
			case "includeIf":
				addition = fmt.Sprintf("\n[includeIf %q]\n path = ../gitconfig.local\n", "gitdir:"+filepath.ToSlash(filepath.Join(project, ".git")))
			case "relative-fsmonitor":
				addition = "\n[core]\n fsmonitor = " + hook + "\n"
			}
			f, err := os.OpenFile(config, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteString(addition); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if mode != "relative-fsmonitor" {
				if err := os.WriteFile(filepath.Join(project, "gitconfig.local"), []byte("[core]\n fsmonitor = "+hook+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if deny, reason := HookDecision(project, "Bash", map[string]any{"command": "git status"}, false); !deny || !strings.Contains(strings.ToLower(reason), "git") {
				t.Fatalf("guard allowed git status: %s", reason)
			}
			if deny, _ := HeadlessGitDecision(project, "Bash", map[string]any{"command": "git status"}); !deny {
				t.Fatal("headless guard allowed git status")
			}
			if deny, _ := HookDecision(project, "Bash", map[string]any{"command": "git blame sample.txt"}, false); !deny {
				t.Fatal("live guard allowed git blame")
			}
			for _, rule := range []string{"Bash(git status:*)", "Bash(git blame:*)"} {
				if !slices.Contains(CompileRules(nil, PrivilegePrivileged).DisallowedTools, rule) {
					t.Fatalf("headless hard deny of %s is missing", rule)
				}
			}
			gitForGuardTest(t, project, "status", "--short")
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("real git status did not execute core.fsmonitor: %v", err)
			}
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			gitForGuardTest(t, project, "blame", "sample.txt")
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("real git blame did not execute core.fsmonitor: %v", err)
			}
		})
	}
}

func TestHeadlessGuardResolvesSeparateGitdirAndAlias(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	project := t.TempDir()
	gitForGuardTest(t, project, "init", "-q", "--separate-git-dir="+filepath.Join(project, "meta"))
	marker, hook := installFSMonitorMarker(t, project)
	gitForGuardTest(t, project, "config", "--local", "core.fsmonitor", hook)
	for _, path := range []string{"meta/config", ".git"} {
		if deny, reason := HeadlessGitDecision(project, "Edit", map[string]any{"file_path": path}); !deny {
			t.Fatalf("headless edit of %q allowed: %s", path, reason)
		}
	}
	if err := os.Symlink("meta", filepath.Join(project, "alias")); err == nil {
		if deny, reason := HeadlessGitDecision(project, "Edit", map[string]any{"file_path": "alias/config"}); !deny {
			t.Fatalf("headless symlink edit allowed: %s", reason)
		}
	}
	if deny, reason := HeadlessGitDecision(project, "Edit", map[string]any{"file_path": "src/main.go"}); deny {
		t.Fatalf("ordinary edit denied: %s", reason)
	}
	gitForGuardTest(t, project, "status", "--short")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("real git status did not execute linked gitdir's fsmonitor: %v", err)
	}
}
