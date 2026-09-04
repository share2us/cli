package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli-core/lanid"
	"github.com/share2us/cli-core/lanshare"
)

// ---- discover: find nearby broadcasts/receivers and pull an offered file ----

type discoverOpts struct {
	plain    bool
	json     bool
	timeout  time.Duration
	download string // non-interactive: download the offer whose file name matches
	path     string // download destination dir
	trust    bool   // auto-trust the source after a download
	interval time.Duration
}

func parseDiscoverArgs(args []string) (discoverOpts, error) {
	o := discoverOpts{timeout: 10 * time.Second, path: "."}
	if iv := lanid.GetScanInterval(); iv > 0 {
		o.interval = time.Duration(iv) * time.Second
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func() (string, bool) {
			if i+1 < len(args) {
				i++
				return args[i], true
			}
			return "", false
		}
		switch {
		case arg == "--plain":
			o.plain = true
		case arg == "--json":
			o.json = true
		case arg == "--trust":
			o.trust = true
		case arg == "--timeout":
			v, ok := next()
			if !ok {
				return o, fmt.Errorf("--timeout needs a duration (e.g. 10s)")
			}
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return o, fmt.Errorf("invalid --timeout %q", v)
			}
			o.timeout = d
		case strings.HasPrefix(arg, "--timeout="):
			d, err := time.ParseDuration(strings.TrimPrefix(arg, "--timeout="))
			if err != nil || d <= 0 {
				return o, fmt.Errorf("invalid --timeout")
			}
			o.timeout = d
		case arg == "--download":
			v, ok := next()
			if !ok {
				return o, fmt.Errorf("--download needs a file name")
			}
			o.download = v
		case strings.HasPrefix(arg, "--download="):
			o.download = strings.TrimPrefix(arg, "--download=")
		case arg == "--path":
			v, ok := next()
			if !ok {
				return o, fmt.Errorf("--path needs a directory")
			}
			o.path = v
		case strings.HasPrefix(arg, "--path="):
			o.path = strings.TrimPrefix(arg, "--path=")
		case arg == "--interval":
			v, ok := next()
			if !ok {
				return o, fmt.Errorf("--interval needs seconds")
			}
			s, err := strconv.Atoi(v)
			if err != nil || s < 0 {
				return o, fmt.Errorf("invalid --interval %q", v)
			}
			o.interval = time.Duration(s) * time.Second
		default:
			return o, fmt.Errorf("unknown flag: %s", arg)
		}
	}
	return o, nil
}

func (a app) discover(ctx context.Context, args []string) int {
	opts, err := parseDiscoverArgs(args)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}

	// Non-interactive download by name: one scan, then pull the matching offer.
	if opts.download != "" {
		peers, err := scanPeers(ctx, opts.timeout)
		if err != nil {
			return a.fail("scan", err)
		}
		peer, ok := findBroadcast(peers, opts.download)
		if !ok {
			fmt.Fprintf(a.stderr, "no broadcast offering %q found nearby\n", opts.download)
			return 1
		}
		return a.downloadOffer(ctx, peer, opts.path, opts.trust)
	}

	// Plain/JSON or no TTY -> one-shot scan + print (no interactive UI).
	if opts.plain || opts.json || a.stdoutIsTTY == nil || !a.stdoutIsTTY(a.stdout) {
		fmt.Fprintf(a.stderr, "Scanning the local network for %s...\n", opts.timeout)
		peers, err := scanPeers(ctx, opts.timeout)
		if err != nil {
			return a.fail("scan", err)
		}
		if opts.json {
			enc := json.NewEncoder(a.stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(peers)
			return 0
		}
		a.printPeers(peers)
		return 0
	}

	// Interactive TUI.
	return a.discoverTUI(ctx, opts)
}

// scanPeers browses the LAN and returns discovered peers, offers first, sorted.
func scanPeers(ctx context.Context, timeout time.Duration) ([]lanshare.Peer, error) {
	peers, err := lanshare.Browse(ctx, timeout)
	if err != nil {
		return nil, err
	}
	sortPeers(peers)
	return peers, nil
}

func sortPeers(peers []lanshare.Peer) {
	sort.SliceStable(peers, func(i, j int) bool {
		if peers[i].IsBroadcast != peers[j].IsBroadcast {
			return peers[i].IsBroadcast // offers first
		}
		return peers[i].Name < peers[j].Name
	})
}

func findBroadcast(peers []lanshare.Peer, name string) (lanshare.Peer, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, p := range peers {
		if p.IsBroadcast && (strings.ToLower(p.FileName) == name || strings.ToLower(p.Name) == name) {
			return p, true
		}
	}
	// fall back to a substring match on the offered file name
	for _, p := range peers {
		if p.IsBroadcast && strings.Contains(strings.ToLower(p.FileName), name) {
			return p, true
		}
	}
	return lanshare.Peer{}, false
}

func (a app) printPeers(peers []lanshare.Peer) {
	if len(peers) == 0 {
		fmt.Fprintln(a.stdout, "No nearby devices found.")
		return
	}
	fmt.Fprintf(a.stdout, "%-20s %-21s %-9s %-24s %s\n", "NAME", "ADDRESS", "KIND", "FILE", "SIZE")
	for _, p := range peers {
		kind := "receiver"
		file, size := "-", "-"
		if p.IsBroadcast {
			kind = "offer"
			file = p.FileName
			size = humanBytes(p.FileSize)
		}
		fmt.Fprintf(a.stdout, "%-20s %-21s %-9s %-24s %s\n",
			truncate(p.Name, 20), net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), kind, truncate(file, 24), size)
	}
	fmt.Fprintln(a.stderr, "\nPull an offer with: s2u discover --download <file>")
}

// downloadOffer pulls a broadcast offer into destDir and (optionally / on prompt)
// trusts the verified source device.
func (a app) downloadOffer(ctx context.Context, peer lanshare.Peer, destDir string, autoTrust bool) int {
	id, idErr := lanid.Identity()
	if idErr != nil {
		return a.fail("load device identity", idErr)
	}
	hostname, _ := os.Hostname()
	addr := net.JoinHostPort(peer.Host, strconv.Itoa(peer.Port))
	prog := newProgressPrinter(a.stderr, "downloading")
	fmt.Fprintf(a.stderr, "Downloading %s (%s) from %s...\n", peer.FileName, humanBytes(peer.FileSize), peer.Name)
	res, err := lanshare.Download(ctx, lanshare.DownloadOptions{
		Dest:           addr,
		PinFingerprint: peer.Fingerprint,
		Name:           peer.FileName,
		Size:           peer.FileSize,
		DestDir:        destDir,
		Identity:       id,
		DownloaderName: hostname,
		OnProgress:     prog.update,
	})
	prog.finish()
	if err != nil {
		return a.fail("download", err)
	}
	srcFP := lanshare.IdentityFingerprint(res.SenderKey)
	fmt.Fprintf(a.stdout, "Received %s (%s) from %s -> %s\n", res.Name, humanBytes(res.Bytes), peer.Name, res.Path)
	lanid.ActivityAppend(lanid.ActivityEntry{Kind: "downloaded", Peer: peer.Name, Name: res.Name, Size: res.Bytes})

	// Offer to trust the (now verified) source. Trust needs the account's second
	// factor (ADR-034), so this is always interactive: --trust skips the y/N but
	// not the code.
	if srcFP != "" {
		if _, already := lanid.Lookup(srcFP); !already && a.inputIsTTY() {
			r := bufio.NewReader(a.input())
			want := autoTrust
			if !want {
				fmt.Fprintf(a.stderr, "Trust %s (code %s) for future transfers? [y/N] ", peer.Name, lanshare.VerifyCode(srcFP))
				ans, _ := r.ReadString('\n')
				want = isYes(ans)
			}
			if want {
				if a.trustDeviceWithMFA(ctx, r, srcFP, peer.Name, lanid.ModeAsk) {
					fmt.Fprintf(a.stderr, "Trusted %s (code %s); it will still ask before each transfer. Change: %s lan trusted mode %s auto\n", peer.Name, lanshare.VerifyCode(srcFP), commandName, srcFP)
				} else {
					fmt.Fprintln(a.stderr, "Not trusted.")
				}
			}
		} else if !already && autoTrust {
			fmt.Fprintln(a.stderr, "--trust needs a terminal to enter the verification code; not trusted.")
		}
	}
	return 0
}

// ---- discover TUI (bubbletea) ----

type peersMsg []lanshare.Peer
type scanTick struct{}

type discoverModel struct {
	ctx      context.Context
	timeout  time.Duration
	interval time.Duration
	peers    []lanshare.Peer
	tbl      table.Model
	status   string
	selected *lanshare.Peer // set when the user picks an offer to download
}

func (a app) discoverTUI(ctx context.Context, opts discoverOpts) int {
	cols := []table.Column{
		{Title: "Name", Width: 18},
		{Title: "Address", Width: 20},
		{Title: "Kind", Width: 8},
		{Title: "File", Width: 22},
		{Title: "Size", Width: 9},
	}
	t := table.New(table.WithColumns(cols), table.WithFocused(true), table.WithHeight(12))
	interval := opts.interval
	if interval > 0 && interval < 2*time.Second {
		interval = 2 * time.Second
	}
	m := discoverModel{ctx: ctx, timeout: opts.timeout, interval: interval, tbl: t, status: "scanning..."}
	prog := tea.NewProgram(m, tea.WithContext(ctx))
	res, err := prog.Run()
	if err != nil {
		return a.fail("discover", err)
	}
	fm := res.(discoverModel)
	if fm.selected != nil {
		return a.downloadOffer(ctx, *fm.selected, opts.path, opts.trust)
	}
	return 0
}

func (m discoverModel) Init() tea.Cmd {
	return tea.Batch(scanCmd(m.ctx, m.timeout), m.tickCmd())
}

func (m discoverModel) tickCmd() tea.Cmd {
	if m.interval <= 0 {
		return nil
	}
	return tea.Tick(m.interval, func(time.Time) tea.Msg { return scanTick{} })
}

func scanCmd(ctx context.Context, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		peers, err := scanPeers(ctx, timeout)
		if err != nil {
			return peersMsg(nil)
		}
		return peersMsg(peers)
	}
}

func (m discoverModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case peersMsg:
		m.peers = []lanshare.Peer(msg)
		m.tbl.SetRows(peerRows(m.peers))
		m.status = fmt.Sprintf("%d device(s) nearby", len(m.peers))
		return m, nil
	case scanTick:
		return m, tea.Batch(scanCmd(m.ctx, m.timeout), m.tickCmd())
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "r":
			m.status = "rescanning..."
			return m, scanCmd(m.ctx, 3*time.Second)
		case "enter":
			if p := m.current(); p != nil {
				if !p.IsBroadcast {
					m.status = fmt.Sprintf("%s is a receiver (nothing to pull) — use `s2u <file> --dest`", p.Name)
					return m, nil
				}
				sel := *p
				m.selected = &sel
				return m, tea.Quit
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.tbl, cmd = m.tbl.Update(msg)
	return m, cmd
}

func (m discoverModel) current() *lanshare.Peer {
	i := m.tbl.Cursor()
	if i < 0 || i >= len(m.peers) {
		return nil
	}
	return &m.peers[i]
}

var (
	titleStyle = lipgloss.NewStyle().Bold(true)
	dimStyle   = lipgloss.NewStyle().Faint(true)
)

func (m discoverModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("s2u discover — nearby devices"))
	b.WriteString("\n")
	b.WriteString(m.tbl.View())
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(m.status))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("↑/↓ select · enter download offer · r rescan · q quit"))
	b.WriteString("\n")
	return b.String()
}

func peerRows(peers []lanshare.Peer) []table.Row {
	rows := make([]table.Row, 0, len(peers))
	for _, p := range peers {
		kind, file, size := "receiver", "-", "-"
		if p.IsBroadcast {
			kind, file, size = "offer", p.FileName, humanBytes(p.FileSize)
		}
		rows = append(rows, table.Row{
			truncate(p.Name, 18), net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), kind, truncate(file, 22), size,
		})
	}
	return rows
}

// ---- `s2u lan` management (trusted devices, activity, scan interval, identity) ----

func (a app) lanAdmin(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return a.lanAdminUsage()
	}
	switch args[0] {
	case "id", "whoami":
		fmt.Fprintf(a.stdout, "This device fingerprint: %s\n", lanid.Fingerprint())
		fmt.Fprintf(a.stdout, "Verify code:             %s\n", lanid.Code())
		fmt.Fprintf(a.stdout, "Safety number:           %s\n", lanid.SafetyNumber())
		fmt.Fprintf(a.stdout, "\nThe verify code is for per-transfer prompts. Before TRUSTING this device from another\nmachine, compare the safety number: it is shown there at trust time and in the email.\n")
		return 0
	case "trusted":
		return a.lanTrusted(args[1:])
	case "activity":
		return a.lanActivity(args[1:])
	case "scan-interval":
		return a.lanScanInterval(args[1:])
	default:
		return a.lanAdminUsage()
	}
}

func (a app) lanAdminUsage() int {
	fmt.Fprintf(a.stderr, "usage: %s lan id | trusted [list|mode <fp> ask|auto|revoke <fp>] | activity [list|clear] | scan-interval [get|set <sec>]\n", commandName)
	return 2
}

func (a app) lanTrusted(args []string) int {
	ctx := context.Background()
	action := "list"
	if len(args) > 0 {
		action = args[0]
	}
	printList := func(list []lanid.TrustedDevice, source string) int {
		if len(list) == 0 {
			fmt.Fprintf(a.stdout, "No trusted devices (%s).\n", source)
			return 0
		}
		for _, d := range list {
			fmt.Fprintf(a.stdout, "%s  %s  mode: %s\n    safety number %s\n", d.Fingerprint, d.Name, d.EffectiveMode(), lanshare.SafetyNumber(d.Fingerprint))
		}
		fmt.Fprintf(a.stdout, "\n(%s)  mode ask = approve each transfer, no code to compare;  mode auto = saved without asking\nChange with: %s lan trusted mode <fingerprint> ask|auto   (auto needs verification)\n", source, commandName)
		return 0
	}
	switch action {
	case "list":
		a.refreshTrustList(ctx)
		ok, exp := lanid.TrustCacheStatus()
		source := "from your account"
		if !ok {
			source = "no verified list; sign in to sync"
		} else if client, _ := a.trustClient(); client == nil {
			source = "cached copy, valid until " + exp.Local().Format("2006-01-02 15:04")
		}
		return printList(lanid.List(), source)
	case "mode":
		if len(args) < 3 {
			fmt.Fprintf(a.stderr, "usage: %s lan trusted mode <fingerprint> ask|auto\n", commandName)
			return 2
		}
		mode, err := lanid.NormalizeMode(args[2])
		if err != nil {
			return a.fail("set trust mode", err)
		}
		client, why := a.trustClient()
		if client == nil {
			return a.fail("set trust mode", errors.New(why))
		}
		if mode == lanid.ModeAuto {
			// Widening trust: verified like a new trust.
			d, ok := lanid.Lookup(args[1])
			if !ok {
				return a.fail("set trust mode", errors.New("this device is not trusted; trust it first"))
			}
			if !a.inputIsTTY() {
				return a.fail("set trust mode", errors.New("switching to auto needs a terminal to enter the verification code"))
			}
			if a.trustDeviceWithMFA(ctx, bufio.NewReader(a.input()), args[1], d.Name, lanid.ModeAuto) {
				fmt.Fprintln(a.stdout, "Set to auto: transfers from this device are saved without asking.")
				return 0
			}
			return 1
		}
		list, err := client.LanTrustSetMode(ctx, args[1], mode)
		if err != nil {
			return a.fail("set trust mode", err)
		}
		_ = clicore.SaveTrustList(list)
		fmt.Fprintln(a.stdout, "Set to ask: you approve each transfer from this device (no code to compare).")
		return 0
	case "revoke", "remove", "rm":
		if len(args) < 2 {
			fmt.Fprintf(a.stderr, "usage: %s lan trusted revoke <fingerprint>\n", commandName)
			return 2
		}
		client, why := a.trustClient()
		if client == nil {
			return a.fail("revoke", errors.New(why))
		}
		list, err := client.LanTrustRevoke(ctx, args[1])
		if err != nil {
			return a.fail("revoke", err)
		}
		_ = clicore.SaveTrustList(list)
		fmt.Fprintln(a.stdout, "Revoked.")
		return 0
	case "reset":
		if err := lanid.ResetTrust(); err != nil {
			return a.fail("reset", err)
		}
		fmt.Fprintln(a.stdout, "Cleared the cached trusted list and the pinned server key. Run `lan trusted list` to fetch it again.")
		return 0
	default:
		fmt.Fprintf(a.stderr, "usage: %s lan trusted [list|mode <fingerprint> ask|auto|revoke <fingerprint>|reset]\n", commandName)
		return 2
	}
}

func (a app) lanActivity(args []string) int {
	if len(args) > 0 && (args[0] == "clear" || args[0] == "--clear") {
		lanid.ActivityClear()
		fmt.Fprintln(a.stdout, "Activity cleared.")
		return 0
	}
	list := lanid.ActivityList()
	if len(list) == 0 {
		fmt.Fprintln(a.stdout, "No LAN activity yet.")
		return 0
	}
	for _, e := range list {
		when := time.Unix(e.TS, 0).Format("2006-01-02 15:04")
		fmt.Fprintf(a.stdout, "%s  %-11s %-20s %10s  %s\n", when, e.Kind, truncate(e.Name, 20), humanBytes(e.Size), e.Peer)
	}
	return 0
}

func (a app) lanScanInterval(args []string) int {
	if len(args) == 0 || args[0] == "get" {
		fmt.Fprintf(a.stdout, "%d\n", lanid.GetScanInterval())
		return 0
	}
	if args[0] == "set" {
		if len(args) < 2 {
			fmt.Fprintf(a.stderr, "usage: %s lan scan-interval set <seconds>\n", commandName)
			return 2
		}
		sec, err := strconv.Atoi(args[1])
		if err != nil || sec < 0 {
			fmt.Fprintf(a.stderr, "invalid seconds %q\n", args[1])
			return 2
		}
		if err := lanid.SetScanInterval(sec); err != nil {
			return a.fail("set scan-interval", err)
		}
		fmt.Fprintf(a.stdout, "Scan interval set to %d seconds.\n", sec)
		return 0
	}
	fmt.Fprintf(a.stderr, "usage: %s lan scan-interval [get|set <sec>]\n", commandName)
	return 2
}
