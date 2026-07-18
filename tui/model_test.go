package tui

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	clicore "github.com/share2us/cli-core"
)

func TestModelListDetailAndBack(t *testing.T) {
	model := newTestModel(t)

	model = updateOnly(t, model, key("enter"))
	if model.state != viewDetail || model.actionShare.PublicID != "pub-1" {
		t.Fatalf("state=%v actionShare=%+v, want detail pub-1", model.state, model.actionShare)
	}

	model = updateOnly(t, model, key("b"))
	if model.state != viewList {
		t.Fatalf("state=%v, want list", model.state)
	}
}

func TestModelUsageViewLoadsProfile(t *testing.T) {
	model := newTestModel(t)

	got, cmd := update(t, model, key("u"))
	if got.state != viewUsage || cmd == nil {
		t.Fatalf("state=%v cmd nil=%v, want usage with load command", got.state, cmd == nil)
	}
	msg := cmd()
	got = updateOnly(t, got, msg)

	if got.me.Email != "user@example.test" || got.usage.ActiveShares != 1 {
		t.Fatalf("profile not loaded: me=%+v usage=%+v", got.me, got.usage)
	}
}

func TestModelActionsTriggerClientCalls(t *testing.T) {
	model := newTestModel(t)
	fake := model.client.(*fakeClient)
	model = updateOnly(t, model, key("enter"))

	got, cmd := update(t, model, key("r"))
	if cmd == nil {
		t.Fatalf("revoke command is nil")
	}
	got = updateOnly(t, got, cmd())
	if fake.revoked != "pub-1" || got.actionShare.Status != "revoked" {
		t.Fatalf("revoked=%q share=%+v", fake.revoked, got.actionShare)
	}

	got, _ = update(t, got, key("e"))
	got.extendInput.SetValue("2d")
	got, cmd = update(t, got, key("enter"))
	if cmd == nil {
		t.Fatalf("extend command is nil")
	}
	got = updateOnly(t, got, cmd())
	if fake.extendedID != "pub-1" || fake.extendedBy != 48*time.Hour {
		t.Fatalf("extendedID=%q extendedBy=%v", fake.extendedID, fake.extendedBy)
	}

	got, _ = update(t, got, key("d"))
	if got.state != viewDelete {
		t.Fatalf("state=%v, want delete confirm", got.state)
	}
	got, cmd = update(t, got, key("y"))
	if cmd == nil {
		t.Fatalf("delete command is nil")
	}
	got = updateOnly(t, got, cmd())
	if fake.deleted != "pub-1" || got.state != viewList {
		t.Fatalf("deleted=%q state=%v", fake.deleted, got.state)
	}
}

func newTestModel(t *testing.T) Model {
	t.Helper()
	client := &fakeClient{
		shares: []clicore.Share{{
			PublicID:      "pub-1",
			FileName:      "a.txt",
			SizeBytes:     12,
			Status:        "ready",
			ExpiresAt:     "2026-07-03T00:00:00Z",
			DownloadCount: 1,
			MaxDownloads:  10,
			ContentClass:  "text",
		}},
		me:    clicore.MeResponse{Email: "user@example.test", PlanName: "Free"},
		usage: clicore.UsageResponse{ActiveShares: 1, MaxActiveShares: 25},
	}
	model := NewModel(context.Background(), client)
	return updateOnly(t, model, sharesMsg(clicore.ListSharesResponse{Shares: client.shares}))
}

func updateOnly(t *testing.T, model Model, msg tea.Msg) Model {
	t.Helper()
	got, _ := update(t, model, msg)
	return got
}

func update(t *testing.T, model Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := model.Update(msg)
	got, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model = %T, want tui.Model", updated)
	}
	return got, cmd
}

func key(value string) tea.KeyMsg {
	switch value {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
	}
}

type fakeClient struct {
	shares     []clicore.Share
	me         clicore.MeResponse
	usage      clicore.UsageResponse
	revoked    string
	extendedID string
	extendedBy time.Duration
	deleted    string
}

func (c *fakeClient) Me(context.Context) (clicore.MeResponse, error) {
	return c.me, nil
}

func (c *fakeClient) ListShares(context.Context) (clicore.ListSharesResponse, error) {
	return clicore.ListSharesResponse{Shares: c.shares}, nil
}

func (c *fakeClient) RevokeShare(_ context.Context, publicID string) (clicore.Share, error) {
	c.revoked = publicID
	share := c.shares[0]
	share.Status = "revoked"
	return share, nil
}

func (c *fakeClient) ExtendExpiry(_ context.Context, publicID string, expiresIn time.Duration) (clicore.Share, error) {
	c.extendedID = publicID
	c.extendedBy = expiresIn
	share := c.shares[0]
	share.ExpiresAt = "2026-07-05T00:00:00Z"
	return share, nil
}

func (c *fakeClient) DeleteShare(_ context.Context, publicID string) error {
	c.deleted = publicID
	return nil
}

func (c *fakeClient) Usage(context.Context) (clicore.UsageResponse, error) {
	return c.usage, nil
}
