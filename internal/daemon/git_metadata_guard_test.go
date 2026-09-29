// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDeliveredHopCannotEditGitMetadata(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	gitDir := filepath.Join(project, ".git")
	if err := os.Mkdir(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gitDir, filepath.Join(project, "git-alias")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		tool, path string
		deny       bool
	}{
		{"Edit", ".git/config", true},
		{"Write", ".git/hooks/fsmonitor-watchman", true},
		{"MultiEdit", "git-alias/config", true},
		{"NotebookEdit", "git-alias/hooks/new.ipynb", true},
		{"Write", ".git", true}, // worktree gitdir pointer file
		{"Edit", "nested/.git/config", true},
		{"Edit", ".GIT/config", true},  // case-insensitive filesystems
		{"Edit", ".git./config", true}, // Windows ignores trailing dots
		{"Edit", ".git /config", true}, // Windows ignores trailing spaces
		{"Edit", "GIT~1/config", true}, // possible Windows 8.3 alias
		{"Edit", "src/config.go", false},
	} {
		key := "file_path"
		if tc.tool == "NotebookEdit" {
			key = "notebook_path"
		}
		if deny, reason := HookDecision(project, tc.tool, map[string]any{key: tc.path}, false); deny != tc.deny {
			t.Errorf("%s %q: deny=%v, want %v (%s)", tc.tool, tc.path, deny, tc.deny, reason)
		}
	}
}

func TestDeliveredHopCannotEditLinkedGitMetadata(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	gitDir := filepath.Join(project, "meta")
	commonDir := filepath.Join(project, "shared")
	for _, dir := range []string{gitDir, commonDir} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(project, ".git"), []byte("gitdir: ./meta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../shared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"meta/config", "meta/hooks/pre-commit", "shared/config"} {
		if deny, reason := HookDecision(project, "Edit", map[string]any{"file_path": path}, false); !deny {
			t.Errorf("linked metadata %q allowed: %s", path, reason)
		}
	}
	if deny, reason := HookDecision(project, "Edit", map[string]any{"file_path": "src/main.go"}, false); deny {
		t.Errorf("ordinary source denied: %s", reason)
	}
}

func TestDeliveredHopChecksEveryEditPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	input := map[string]any{"file_path": "src/safe.go", "notebook_path": ".git/config"}
	if deny, reason := HookDecision(project, "NotebookEdit", input, false); !deny {
		t.Errorf("second path bypassed Git metadata guard: %s", reason)
	}
	if !ruleMatches("Edit(**/.git/**)", "NotebookEdit", input) {
		t.Error("second path bypassed compiled deny rule")
	}
}

func TestHeadlessPolicyAlsoProtectsGitMetadata(t *testing.T) {
	rules := CompileRules(nil, PrivilegePrivileged).DisallowedTools
	for _, want := range []string{"Edit(**/.git)", "Edit(**/.git/**)"} {
		if !slices.Contains(rules, want) {
			t.Errorf("privileged headless policy lacks non-removable %s", want)
		}
	}
}
