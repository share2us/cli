package daemon

import (
	"context"
	"errors"
	"testing"
)

func TestSealingClient(t *testing.T) {
	r := rt()
	inner := &fakeAgentClient{}
	deps := noDeps()
	deps.SealTaskReply = func(_ context.Context, project, id, reply string) (string, bool, error) {
		switch project {
		case "p-secure":
			return "s2u-sealed-reply/v1:n:1:QUJD", true, nil
		case "p-nokey":
			return "", true, errors.New("no key")
		}
		return reply, false, nil
	}
	c := sealingClient{AgentClient: inner, rt: r, deps: deps}
	r.noteTaskProject("r-secure", "p-secure", true)
	r.noteTaskProject("r-nokey", "p-nokey", true)
	r.noteTaskProject("r-plain", "p-plain", false)
	r.noteTaskProject("r-guest", "p-guest", true) // server: Secure; this account can't see it
	ctx := context.Background()
	_ = c.AgentReportResult(ctx, "r-secure", "done", "all green")
	_ = c.AgentReportResult(ctx, "r-secure", "running", "Waiting for the owner")
	_ = c.AgentReportResult(ctx, "r-nokey", "done", "secret findings")
	_ = c.AgentReportResult(ctx, "r-plain", "done", "plain reply")
	_ = c.AgentReportResult(ctx, "r-noproject", "done", "own-device reply")
	_ = c.AgentReportResult(ctx, "r-guest", "done", "guest reply")
	want := [][2]string{
		{"done", "s2u-sealed-reply/v1:n:1:QUJD"},
		{"running", "Waiting for the owner"},
		{"failed", ReplySealFailed},
		{"done", "plain reply"},
		{"done", "own-device reply"},
		{"failed", ReplyGuestSecure},
	}
	if len(inner.reports) != len(want) {
		t.Fatalf("reports = %v", inner.reports)
	}
	for i, w := range want {
		if inner.reports[i] != w {
			t.Fatalf("report %d = %v, want %v", i, inner.reports[i], w)
		}
	}
	if got := deps.openReply(ctx, "x", "s2u-sealed-reply/v1:n:1:QUJD"); got != SealedReplyPlaceholder {
		t.Fatalf("openReply without a key = %q", got)
	}
}
