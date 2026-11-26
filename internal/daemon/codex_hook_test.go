package daemon

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCodexToolCalls(t *testing.T) {
	project := t.TempDir()
	calls, err := CodexToolCalls(project, "apply_patch", map[string]any{"command": "*** Begin Patch\n*** Update File: a.go\n*** Move to: b/c.go\n*** Delete File: /etc/x\n*** Update File: a.go\n*** End Patch"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range calls {
		if c.Tool != "Edit" {
			t.Fatalf("tool %q", c.Tool)
		}
		got = append(got, c.Input["file_path"].(string))
	}
	want := []string{filepath.Join(project, "a.go"), filepath.Join(project, "b", "c.go"), "/etc/x"}
	if !slices.Equal(got, want) {
		t.Fatalf("paths %v, want %v", got, want)
	}
	if _, err := CodexToolCalls(project, "apply_patch", map[string]any{}); err == nil {
		t.Fatal("a patch without files passed")
	}
	if c, _ := CodexToolCalls(project, "spawn_agent", nil); c[0].Tool != "Agent" {
		t.Fatalf("spawn_agent maps to %q", c[0].Tool)
	}
	if c, _ := CodexToolCalls(project, "Bash", map[string]any{"command": "ls"}); c[0].Tool != "Bash" || c[0].Input["command"] != "ls" {
		t.Fatalf("bash: %+v", c)
	}
}

func TestCodexHookArgsAndEnv(t *testing.T) {
	cfg := codexHookConfig("/opt/s2u/share2us")
	for _, want := range []string{`hooks.PreToolUse=[{matcher="^(Bash|apply_patch|spawn_agent)$|^mcp__"`, `"\"/opt/s2u/share2us\" agent hook codex-pre-tool-use"`, `timeout=240`} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("config %s lacks %s", cfg, want)
		}
	}
	args := buildCodexInjectArgs("sess", "do it", "workspace-write", []string{"--dangerously-bypass-hook-trust", "-c", cfg})
	if n := len(args); args[n-2] != "sess" || args[n-1] != "do it" || !slices.Contains(args, "approval_policy=never") || !slices.Contains(args, cfg) {
		t.Fatalf("args %v", args)
	}
	env := codexGuardEnv([]string{"PATH=/bin", CodexGuardProjectEnv + "=/evil"}, "/p", "sess", true)
	if slices.Contains(env, CodexGuardProjectEnv+"=/evil") || !slices.Contains(env, CodexGuardProjectEnv+"=/p") || !slices.Contains(env, CodexGuardStrictEnv+"=1") || !slices.Contains(env, CodexGuardSessionEnv+"=sess") {
		t.Fatalf("env %v", env)
	}
}

func TestCodexDirIsSelfProtected(t *testing.T) {
	project := t.TempDir()
	if !SelfProtected(project, "Edit", map[string]any{"file_path": filepath.Join(project, ".codex", "hooks.json")}) {
		t.Fatal(".codex is not self-protected")
	}
}

func TestCodexShellPatch(t *testing.T) {
	project := t.TempDir()
	ok := "apply_patch <<'PATCH'\n*** Begin Patch\n*** Add File: notes.txt\n+ok\n*** End Patch\nPATCH"
	calls, err := CodexToolCalls(project, "Bash", map[string]any{"command": ok})
	if err != nil || len(calls) != 1 || calls[0].Tool != "Edit" || calls[0].Input["file_path"] != filepath.Join(project, "notes.txt") {
		t.Fatalf("heredoc patch: %+v %v", calls, err)
	}
	for _, bad := range []string{
		ok + "; rm -rf ~",
		"cd sub && " + ok,
		"apply_patch <<'PATCH'\n*** Add File: a\nPATCH\nrm -rf ~\nPATCH",
		"apply_patch notes.txt",
	} {
		if c, _ := CodexToolCalls(project, "Bash", map[string]any{"command": bad}); len(c) != 1 || c[0].Tool != "Bash" {
			t.Fatalf("%q was treated as a patch: %+v", bad, c)
		}
	}
}
