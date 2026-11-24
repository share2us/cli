package daemon

import (
	"os"
	"os/exec"
	"strings"
)

// AgentEnvVar names, inside an agent's own process, the agent it is (ADR-050). The
// CLI sends it as agent_id when that process posts into a sharenet project, so the
// server can hold an agent to its owner's "can post" grant and record it as the
// poster. A human's terminal never has it, so the human's own posts are unaffected.
// It is a guardrail for honest agents, not a security boundary: the agent controls
// its environment.
const AgentEnvVar = "S2U_AGENT_ID"

// SetAgentEnv sets AgentEnvVar on cmd to the agent bound to dir, dropping any value
// inherited from this process, so a nested run never carries another agent's id. No
// binding for dir: the variable is simply absent.
func SetAgentEnv(cmd *exec.Cmd, dir string) {
	base := cmd.Env
	if base == nil {
		base = os.Environ()
	}
	env := make([]string, 0, len(base)+1)
	for _, entry := range base {
		// Case-insensitive: Windows environment names are, so s2u_agent_id is the same.
		if key, _, _ := strings.Cut(entry, "="); !strings.EqualFold(key, AgentEnvVar) {
			env = append(env, entry)
		}
	}
	if dir != "" {
		if list, err := LoadBindings(); err == nil {
			if id, ok := AgentIDForProject(list, dir); ok {
				env = append(env, AgentEnvVar+"="+id)
			}
		}
	}
	cmd.Env = env
}
