package main

import (
	"testing"

	clicore "github.com/share2us/cli-core"
)

func TestOnePerAgentKeepsTheBusiestSession(t *testing.T) {
	rows := []clicore.ProjectAgentAddress{
		{AgentID: "agt_a", Status: "available", SessionID: "old"},
		{AgentID: "agt_b", Status: "available", SessionID: "b1"},
		{AgentID: "agt_a", Status: "busy", SessionID: "live"},
		{AgentID: "agt_b", Status: "unknown", SessionID: "b2"},
	}
	got := onePerAgent(rows)
	if len(got) != 2 {
		t.Fatalf("rows = %+v, want one per agent", got)
	}
	if got[0].AgentID != "agt_a" || got[0].Status != "busy" || got[0].SessionID != "live" {
		t.Fatalf("agt_a kept %+v, want the busy session", got[0])
	}
	if got[1].AgentID != "agt_b" || got[1].Status != "available" {
		t.Fatalf("agt_b kept %+v, want the available session", got[1])
	}
}
