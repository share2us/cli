package daemon

import (
	"os/exec"
	"strings"
	"testing"
)

func envValue(env []string, key string) (string, int) {
	val, n := "", 0
	for _, e := range env {
		if strings.HasPrefix(e, key+"=") {
			val, n = strings.TrimPrefix(e, key+"="), n+1
		}
	}
	return val, n
}

// ADR-050: an agent's process carries the id of the agent bound to its directory.
func TestSetAgentEnvNamesTheBoundAgent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	b, _, err := Bind(dir, "claude", "")
	if err != nil || b.AgentID == "" {
		t.Fatalf("bind: %+v %v", b, err)
	}
	cmd := exec.Command("true")
	SetAgentEnv(cmd, dir)
	if got, n := envValue(cmd.Env, AgentEnvVar); got != b.AgentID || n != 1 {
		t.Fatalf("%s = %q (x%d), want %q once", AgentEnvVar, got, n, b.AgentID)
	}
}

// An inherited value (e.g. a run nested inside another agent) is dropped, and an
// unbound directory gets none: a human's process never claims to be an agent.
func TestSetAgentEnvDropsAnInheritedValue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(AgentEnvVar, "agt_someone_else_entirely")
	cmd := exec.Command("true")
	SetAgentEnv(cmd, t.TempDir())
	if _, n := envValue(cmd.Env, AgentEnvVar); n != 0 {
		t.Fatalf("unbound directory still carries %s: %v", AgentEnvVar, cmd.Env)
	}
}
