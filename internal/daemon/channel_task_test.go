package daemon

import (
	"strings"
	"testing"

	clicore "github.com/share2us/cli-core"
)

func TestChannelEnvelope(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ok := clicore.AgentRequest{ID: "r", Kind: "channel", ProjectID: "p", TargetSessionID: "s1", ChannelPrompt: "run the smoke tests", SenderLabel: "Alice"}
	env, why := channelEnvelope(ok)
	if why != "" || !strings.Contains(env.Prompt, "run the smoke tests") || !strings.Contains(env.Prompt, "Alice") || env.SenderDeviceName != "Alice" {
		t.Fatalf("envelope = %+v %q", env, why)
	}
	for _, bad := range []clicore.AgentRequest{
		{Kind: "channel", ChannelPrompt: "x"},                                // no project
		{Kind: "channel", ProjectID: "p"},                                    // no message
		{Kind: "channel", ProjectID: "p", ChannelPrompt: "x", HasFile: true}, // a file
	} {
		if _, why := channelEnvelope(bad); why == "" {
			t.Fatalf("%+v was accepted", bad)
		}
	}
	dir := t.TempDir()
	if _, _, err := BindSession(dir, "claude", "", "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := SetMentionTasks(dir, false); err != nil {
		t.Fatal(err)
	}
	if _, why := channelEnvelope(ok); why != MentionTasksRefused {
		t.Fatalf("mentions off: %q", why)
	}
}

func TestChannelTaskSkipsDeviceVerification(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &fakeAgentClient{}
	r := &fakeRunner{out: "smoke tests passed", project: t.TempDir()}
	if _, _, err := BindSession(r.project, "claude", "", "s1"); err != nil {
		t.Fatal(err)
	}
	deps := noDeps() // no Unseal: a signed hop would be refused; a channel task needs neither
	rt().handleInject(t.Context(), c, r, deps, clicore.AgentRequest{ID: "ct-1", Kind: "channel", Tool: "claude",
		TargetSessionID: "s1", ProjectID: "p-1", ChannelPrompt: "run the smoke tests", SenderLabel: "Alice"})
	if !strings.Contains(r.ranPrompt, "run the smoke tests") {
		t.Fatalf("ran %q; reports %v", r.ranPrompt, c.reports)
	}
	if last := c.reports[len(c.reports)-1]; last != [2]string{"done", "smoke tests passed"} {
		t.Fatalf("reports %v", c.reports)
	}
}
