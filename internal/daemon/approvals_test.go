// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli-core/daemonctl"
)

type approvalsFake struct {
	fakeAgentClient
	grants   []clicore.ActionGrant
	created  []clicore.ApprovalRequest
	state    clicore.ApprovalState
	consumed int
	cont     string
}

func (f *approvalsFake) CreateApproval(_ context.Context, in clicore.ApprovalRequest) (string, string, bool, error) {
	f.created = append(f.created, in)
	return "appr-1", "https://portal.example/approvals/appr-1/", true, nil
}
func (f *approvalsFake) GetApproval(context.Context, string) (clicore.ApprovalState, error) {
	return f.state, nil
}
func (f *approvalsFake) ConsumeApproval(context.Context, string) error { f.consumed++; return nil }
func (f *approvalsFake) ContinueApproval(context.Context, string) (string, error) {
	return f.cont, nil
}
func (f *approvalsFake) ListActionGrants(context.Context) ([]clicore.ActionGrant, error) {
	return f.grants, nil
}

func TestSelfProtectionIsNeverApprovable(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		tool  string
		input map[string]any
		want  bool
	}{
		{"Edit", map[string]any{"file_path": filepath.Join(dir, ".s2u.rules")}, true},
		{"Edit", map[string]any{"file_path": filepath.Join(dir, ".git", "config")}, true},
		{"Task", map[string]any{}, true},
		{"Bash", map[string]any{"command": "ip -4 addr"}, false},
		{"Bash", map[string]any{"command": "git push origin main"}, false},
	} {
		if got := SelfProtected(dir, c.tool, c.input); got != c.want {
			t.Errorf("%s %v: %v, want %v", c.tool, c.input, got, c.want)
		}
	}
}

func TestActionDisplayHidesSecrets(t *testing.T) {
	if got := actionDisplay("ip -4 addr"); got != "ip -4 addr" {
		t.Fatalf("plain: %q", got)
	}
	got := actionDisplay("curl -H 'Authorization: Bearer ghp_u8jzPde0IgxLd6GncfBAepfJBd0Kh8oOOL8d' https://api.github.com")
	if strings.Contains(got, "ghp_") || !strings.HasPrefix(got, "curl") {
		t.Fatalf("secret shown: %q", got)
	}
	if ActionHash("Bash", "a") == ActionHash("Edit", "a") {
		t.Fatal("hash ignores the tool")
	}
}

func TestApprovalCheckUsesGrantsElseFiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	b, _, err := BindSessionInPane(project, "claude", "", "win-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime := rt()
	f := &approvalsFake{}
	runtime.setApprovalBridge(f, nil, keyedDeps())
	check := func(text string) map[string]string {
		resp := runtime.approvalCheck(context.Background(), map[string]string{"session": "win-1", "tool": "Bash", "text": text, "reason": "guard"})
		var out map[string]string
		_ = json.Unmarshal(resp.Data, &out)
		return out
	}
	if got := check("ip -4 addr"); got["decision"] != "pending" || len(f.created) != 1 || f.created[0].AgentID != b.AgentID {
		t.Fatalf("no grant: %v %+v", got, f.created)
	}
	future := time.Now().Add(time.Hour)
	f.grants = []clicore.ActionGrant{{AgentID: b.AgentID, Scope: "command", Pattern: "ip ", ExpiresAt: &future}}
	runtime.forgetGrants()
	if got := check("ip route"); got["decision"] != "allow" {
		t.Fatalf("prefix grant: %v", got)
	}
	if got := check("ipconfig"); got["decision"] != "pending" {
		t.Fatalf("prefix must not match ipconfig: %v", got)
	}
	f.grants = []clicore.ActionGrant{{AgentID: "agt_SOMEONE_ELSE", Scope: "all", ExpiresAt: &future}}
	runtime.forgetGrants()
	if got := check("rm -rf build"); got["decision"] != "pending" {
		t.Fatalf("another agent's grant applied: %v", got)
	}
	// A once-approval is used up when its status is read.
	f.state = clicore.ApprovalState{ID: "appr-1", Status: "approved", Scope: "once"}
	resp := runtime.approvalStatusOp(context.Background(), "appr-1")
	if !strings.Contains(string(resp.Data), "approved") || f.consumed != 1 {
		t.Fatalf("status: %s consumed=%d", resp.Data, f.consumed)
	}
	if r, ok := runtime.approvalControl(daemonctl.Request{Op: "approval-park", Args: map[string]string{"id": "appr-2", "session": "win-1"}}); !ok || !r.OK {
		t.Fatal("park op")
	}
}

// Approved after the hold: the hop is reopened and the agent is told to continue, in the
// same window, as the same request (its answer goes to the original sender).
func TestLateApprovalContinuesTheTask(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := injectHoldPoll
	injectHoldPoll = time.Millisecond
	t.Cleanup(func() { injectHoldPoll = old })
	runtime, z, dir := typedRuntime(t, "│ ❯  │", "│ ❯  │", "│ ❯ [Pasted text #1 +3 lines] │")
	f := &approvalsFake{state: clicore.ApprovalState{ID: "appr-9", Status: "approved", Scope: "command", Minutes: 15}, cont: "req-9"}
	f.reportNotify = make(chan struct{}, 16)
	runner := typedRunner(dir)
	runtime.setApprovalBridge(f, map[string]AgentRunner{"claude": runner}, keyedDeps())
	runtime.parkApproval("appr-9", "win-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime.checkParked(ctx, runtime.approvals().parked["appr-9"])
	waitFor(t, func() bool { return z.enters() == 1 })
	if len(z.pasted) != 1 || !strings.Contains(z.pasted[0], "request req-9") || !strings.Contains(z.pasted[0], "for 15 minutes") {
		t.Fatalf("continuation typed = %q", z.pasted)
	}
	if _, still := runtime.approvals().parked["appr-9"]; still {
		t.Fatal("approval still parked after continuing")
	}
}
