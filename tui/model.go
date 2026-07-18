package tui

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	clicore "github.com/share2us/cli-core"
)

type Client interface {
	Me(context.Context) (clicore.MeResponse, error)
	ListShares(context.Context) (clicore.ListSharesResponse, error)
	RevokeShare(context.Context, string) (clicore.Share, error)
	ExtendExpiry(context.Context, string, time.Duration) (clicore.Share, error)
	DeleteShare(context.Context, string) error
	Usage(context.Context) (clicore.UsageResponse, error)
}

type viewState int

const (
	viewList viewState = iota
	viewDetail
	viewUsage
	viewExtend
	viewDelete
)

type Model struct {
	ctx         context.Context
	client      Client
	state       viewState
	table       table.Model
	extendInput textinput.Model
	shares      []clicore.Share
	me          clicore.MeResponse
	usage       clicore.UsageResponse
	status      string
	err         error
	loading     bool
	actionShare clicore.Share
}

type sharesMsg clicore.ListSharesResponse
type profileMsg struct {
	me    clicore.MeResponse
	usage clicore.UsageResponse
}
type actionMsg struct {
	action string
	share  clicore.Share
}
type deleteMsg struct {
	publicID string
}
type errMsg struct {
	action string
	err    error
}

var (
	titleStyle  = lipgloss.NewStyle().Bold(true)
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

func NewModel(ctx context.Context, client Client) Model {
	columns := []table.Column{
		{Title: "Name", Width: 28},
		{Title: "Size", Width: 10},
		{Title: "Status", Width: 10},
		{Title: "Expires", Width: 20},
		{Title: "Downloads", Width: 10},
	}
	t := table.New(
		table.WithColumns(columns),
		table.WithFocused(true),
		table.WithHeight(12),
	)
	ti := textinput.New()
	ti.Placeholder = "24h"
	ti.CharLimit = 24
	ti.Prompt = "Extend by: "
	return Model{
		ctx:         ctx,
		client:      client,
		table:       t,
		extendInput: ti,
		status:      "loading shares",
		loading:     true,
	}
}

func Run(ctx context.Context, client Client, in io.Reader, out io.Writer) error {
	program := tea.NewProgram(
		NewModel(ctx, client),
		tea.WithContext(ctx),
		tea.WithInput(in),
		tea.WithOutput(out),
		tea.WithAltScreen(),
	)
	_, err := program.Run()
	return err
}

func (m Model) Init() tea.Cmd {
	return m.loadShares()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.updateKey(msg)
	case sharesMsg:
		m.loading = false
		m.err = nil
		m.shares = clicore.ListSharesResponse(msg).Shares
		m.table.SetRows(shareRows(m.shares))
		m.status = fmt.Sprintf("loaded %d shares", len(m.shares))
		return m, nil
	case profileMsg:
		m.loading = false
		m.err = nil
		m.me = msg.me
		m.usage = msg.usage
		m.status = "usage refreshed"
		return m, nil
	case actionMsg:
		m.err = nil
		m.loading = false
		if msg.share.PublicID != "" {
			m.replaceShare(msg.share)
			m.status = fmt.Sprintf("%s %s", msg.action, msg.share.PublicID)
			m.actionShare = msg.share
			m.state = viewDetail
			return m, nil
		}
		m.status = msg.action
		m.state = viewList
		return m, m.loadShares()
	case deleteMsg:
		m.loading = false
		m.err = nil
		m.status = fmt.Sprintf("deleted %s", msg.publicID)
		m.state = viewList
		return m, m.loadShares()
	case errMsg:
		m.loading = false
		m.err = msg.err
		m.status = fmt.Sprintf("%s failed: %v", msg.action, msg.err)
		return m, nil
	}

	if m.state == viewList {
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		return m, cmd
	}
	if m.state == viewExtend {
		var cmd tea.Cmd
		m.extendInput, cmd = m.extendInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.state {
	case viewList:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "r":
			m.loading = true
			m.status = "refreshing shares"
			return m, m.loadShares()
		case "u":
			m.state = viewUsage
			m.loading = true
			m.status = "loading usage"
			return m, m.loadProfile()
		case "enter":
			if share, ok := m.selectedShare(); ok {
				m.actionShare = share
				m.state = viewDetail
				m.status = "selected " + share.PublicID
				return m, nil
			}
		}
	case viewDetail:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc", "b":
			m.state = viewList
			m.status = "shares"
			return m, nil
		case "c":
			m.status = "link: " + publicLink(m.actionShare)
			return m, nil
		case "r":
			m.loading = true
			m.status = "revoking " + m.actionShare.PublicID
			return m, m.revokeShare(m.actionShare.PublicID)
		case "e":
			m.state = viewExtend
			m.extendInput.SetValue("")
			m.extendInput.Focus()
			m.status = "enter duration, for example 24h or 7d"
			return m, textinput.Blink
		case "d":
			m.state = viewDelete
			m.status = "confirm delete"
			return m, nil
		}
	case viewUsage:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc", "b":
			m.state = viewList
			m.status = "shares"
			return m, nil
		case "r", "u":
			m.loading = true
			m.status = "refreshing usage"
			return m, m.loadProfile()
		}
	case viewExtend:
		switch msg.String() {
		case "esc":
			m.state = viewDetail
			m.extendInput.Blur()
			m.status = "extension cancelled"
			return m, nil
		case "enter":
			duration, err := parseDuration(m.extendInput.Value())
			if err != nil {
				m.err = err
				m.status = "invalid duration: " + err.Error()
				return m, nil
			}
			m.extendInput.Blur()
			m.loading = true
			m.status = "extending " + m.actionShare.PublicID
			return m, m.extendShare(m.actionShare.PublicID, duration)
		}
	case viewDelete:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc", "n":
			m.state = viewDetail
			m.status = "delete cancelled"
			return m, nil
		case "y":
			m.loading = true
			m.status = "deleting " + m.actionShare.PublicID
			return m, m.deleteShare(m.actionShare.PublicID)
		}
	}
	return m.updateChild(msg)
}

func (m Model) updateChild(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.state == viewList {
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		return m, cmd
	}
	if m.state == viewExtend {
		var cmd tea.Cmd
		m.extendInput, cmd = m.extendInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Share2Us"))
	b.WriteString("\n\n")

	switch m.state {
	case viewList:
		b.WriteString(m.table.View())
	case viewDetail:
		b.WriteString(m.detailView())
	case viewUsage:
		b.WriteString(m.usageView())
	case viewExtend:
		b.WriteString("Extend expiry\n\n")
		b.WriteString(m.extendInput.View())
	case viewDelete:
		b.WriteString("Delete share\n\n")
		b.WriteString(fmt.Sprintf("Delete %s permanently? y/n", m.actionShare.PublicID))
	}

	b.WriteString("\n\n")
	if m.err != nil {
		b.WriteString(errorStyle.Render(m.status))
	} else {
		b.WriteString(statusStyle.Render(m.status))
	}
	if m.loading {
		b.WriteString(" ...")
	}
	b.WriteString("\n")
	b.WriteString(helpStyle.Render(m.help()))
	return b.String()
}

func (m Model) detailView() string {
	share := m.actionShare
	return strings.Join([]string{
		"Name:       " + valueOr(share.FileName, "-"),
		"Public ID:  " + share.PublicID,
		"Link:       " + publicLink(share),
		"Size:       " + strconv.FormatUint(share.SizeBytes, 10),
		"Status:     " + valueOr(share.Status, "-"),
		"Class:      " + valueOr(share.ContentClass, "-"),
		"Created:    " + valueOr(share.CreatedAt, "-"),
		"Expires:    " + valueOr(share.ExpiresAt, "-"),
		"Downloads:  " + fmt.Sprintf("%d / %s", share.DownloadCount, maxDownloads(share.MaxDownloads)),
	}, "\n")
}

func (m Model) usageView() string {
	email := m.me.Email
	if email == "" {
		email = "-"
	}
	plan := m.me.PlanName
	if plan == "" {
		plan = m.me.PlanID
	}
	return strings.Join([]string{
		"Email:            " + email,
		"Plan:             " + valueOr(plan, "-"),
		"Storage:          " + fmt.Sprintf("%d / %d bytes", m.usage.StorageUsedBytes, m.usage.StorageQuotaBytes),
		"Monthly uploads:  " + fmt.Sprintf("%d / %d bytes", m.usage.MonthlyUploadBytes, m.usage.MonthlyUploadLimitBytes),
		"Active shares:    " + fmt.Sprintf("%d / %d", m.usage.ActiveShares, m.usage.MaxActiveShares),
		"Max file size:    " + strconv.FormatUint(m.usage.MaxFileSizeBytes, 10) + " bytes",
		"Max expiry:       " + strconv.FormatUint(m.usage.MaximumExpiryHours, 10) + " hours",
	}, "\n")
}

func (m Model) help() string {
	switch m.state {
	case viewList:
		return "enter detail | u usage | r refresh | q quit"
	case viewDetail:
		return "b back | c copy link | r revoke | e extend | d delete | q quit"
	case viewUsage:
		return "b back | r refresh | q quit"
	case viewExtend:
		return "enter apply | esc cancel"
	case viewDelete:
		return "y delete | n cancel | esc cancel"
	default:
		return "q quit"
	}
}

func (m Model) selectedShare() (clicore.Share, bool) {
	if len(m.shares) == 0 {
		return clicore.Share{}, false
	}
	cursor := m.table.Cursor()
	if cursor < 0 || cursor >= len(m.shares) {
		return clicore.Share{}, false
	}
	return m.shares[cursor], true
}

func (m *Model) replaceShare(share clicore.Share) {
	for i, existing := range m.shares {
		if existing.PublicID == share.PublicID {
			m.shares[i] = share
			m.table.SetRows(shareRows(m.shares))
			return
		}
	}
	m.shares = append(m.shares, share)
	m.table.SetRows(shareRows(m.shares))
}

func (m Model) loadShares() tea.Cmd {
	return func() tea.Msg {
		shares, err := m.client.ListShares(m.ctx)
		if err != nil {
			return errMsg{action: "load shares", err: err}
		}
		return sharesMsg(shares)
	}
}

func (m Model) loadProfile() tea.Cmd {
	return func() tea.Msg {
		me, err := m.client.Me(m.ctx)
		if err != nil {
			return errMsg{action: "load profile", err: err}
		}
		usage, err := m.client.Usage(m.ctx)
		if err != nil {
			return errMsg{action: "load usage", err: err}
		}
		return profileMsg{me: me, usage: usage}
	}
}

func (m Model) revokeShare(publicID string) tea.Cmd {
	return func() tea.Msg {
		share, err := m.client.RevokeShare(m.ctx, publicID)
		if err != nil {
			return errMsg{action: "revoke", err: err}
		}
		return actionMsg{action: "revoked", share: share}
	}
}

func (m Model) extendShare(publicID string, duration time.Duration) tea.Cmd {
	return func() tea.Msg {
		share, err := m.client.ExtendExpiry(m.ctx, publicID, duration)
		if err != nil {
			return errMsg{action: "extend expiry", err: err}
		}
		return actionMsg{action: "extended", share: share}
	}
}

func (m Model) deleteShare(publicID string) tea.Cmd {
	return func() tea.Msg {
		if err := m.client.DeleteShare(m.ctx, publicID); err != nil {
			return errMsg{action: "delete", err: err}
		}
		return deleteMsg{publicID: publicID}
	}
}

func shareRows(shares []clicore.Share) []table.Row {
	rows := make([]table.Row, 0, len(shares))
	for _, share := range shares {
		rows = append(rows, table.Row{
			truncate(share.FileName, 28),
			strconv.FormatUint(share.SizeBytes, 10),
			share.Status,
			share.ExpiresAt,
			strconv.FormatUint(share.DownloadCount, 10),
		})
	}
	return rows
}

func publicLink(share clicore.Share) string {
	if share.PublicID == "" {
		return ""
	}
	base, _, err := clicore.ResolveShareBase()
	if err != nil {
		base = clicore.DefaultShareBase
	}
	return strings.TrimRight(base, "/") + "/" + share.PublicID
}

func parseDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("duration is required")
	}
	if strings.HasSuffix(value, "d") {
		days, err := strconv.ParseFloat(strings.TrimSuffix(value, "d"), 64)
		if err != nil {
			return 0, err
		}
		duration := time.Duration(days * 24 * float64(time.Hour))
		if duration <= 0 {
			return 0, fmt.Errorf("duration must be positive")
		}
		return duration, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}
	return duration, nil
}

func maxDownloads(value uint64) string {
	if value == 0 {
		return "unlimited"
	}
	return strconv.FormatUint(value, 10)
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func truncate(value string, width int) string {
	if len(value) <= width {
		return value
	}
	if width <= 3 {
		return value[:width]
	}
	return value[:width-3] + "..."
}
