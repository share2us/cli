package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli-core/daemonctl"
	"github.com/share2us/cli-core/lanshare"
)

// defaultInboxInterval matches the CLI's `receive --watch` cadence.
const defaultInboxInterval = 5 * time.Second

// Scheduler intervals for the maintenance jobs. Coarse on purpose: none of these
// is latency-sensitive.
const (
	trustRefreshEvery = 24 * time.Hour
	updateCheckEvery  = 6 * time.Hour
	cleanupEvery      = 6 * time.Hour
	schedulerTick     = 30 * time.Second
	jobTimeout        = 60 * time.Second
)

// Options configures a daemon run. Zero values fall back to sensible defaults.
type Options struct {
	DestDir         string        // where received files land ("" = lanshare/receiveInboxOnce default)
	LANDiscoverable bool          // run the background LAN receiver
	RunInbox        bool          // run the account-inbox poll (false under a PAT)
	Notify          bool          // post desktop notifications
	Instance        string        // mDNS advertised name ("" = hostname)
	Bind            string        // LAN bind address ("" = all interfaces)
	Port            int           // LAN port (0 = auto)
	TrustedIPs      []string      // IPs auto-accepted for LAN (trust-by-IP)
	InboxInterval   time.Duration // inbox poll cadence (0 = default 5s)
	ApprovalPolicy  string        // LAN approval policy (clicore.ApprovalPolicy*)
	AgentBridge     bool          // ADR-036: register sessions + receive inject requests
}

// Deps are the behaviours the daemon composes, injected from package main so this
// package stays free of the CLI's app god-struct and is unit-testable.
type Deps struct {
	// ReceiveOnce runs one account-inbox poll, saving new files to destDir, and
	// returns how many arrived. Wraps receiveInboxOnce.
	ReceiveOnce func(ctx context.Context, destDir string) (int, error)
	// RefreshTrust refreshes the server-signed LAN trust cache (best-effort).
	RefreshTrust func(ctx context.Context)
	// CheckUpdate returns whether a newer build exists plus a human upgrade line.
	CheckUpdate func(ctx context.Context) (available bool, message string)
	// Cleanup removes stale temp/staging files (best-effort).
	Cleanup func(ctx context.Context) error
	// AgentClient + AgentRunner drive the agent-session bridge (ADR-036); nil when
	// the bridge is off.
	AgentClient AgentClient
	AgentRunner AgentRunner
	// Unseal opens a prompt sealed to this device (ADR-036 E2E); nil = plaintext.
	Unseal func(sealed string) (string, error)
	// Logf writes an operational log line (to stderr/journal).
	Logf func(format string, args ...any)
}

// Runtime holds run state (start time, what it owns, the stop hook) so the
// control server can answer status/owns-receiver and honour a stop request.
type Runtime struct {
	notifier  Notifier
	startedAt time.Time
	ownsLAN   bool
	ownsInbox bool
	stop      context.CancelFunc
}

func (d Deps) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf(one, n)
	}
	return fmt.Sprintf(many, n)
}

// Run starts the daemon: the single-instance lock + control server, then the
// inbox poll, the LAN receiver, and the maintenance scheduler, each under ctx.
// It returns when ctx is cancelled (SIGTERM/SIGINT) or a fatal error occurs.
func Run(ctx context.Context, opts Options, deps Deps) error {
	if opts.InboxInterval <= 0 {
		opts.InboxInterval = defaultInboxInterval
	}
	var notifier Notifier = NoopNotifier{}
	if opts.Notify {
		notifier = NewNotifier()
	}
	// A cancelable child of the caller's ctx so a control "stop" request can shut
	// the daemon down cleanly, just like SIGTERM cancels the parent.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rt := &Runtime{
		notifier:  notifier,
		startedAt: time.Now().UTC(),
		ownsLAN:   opts.LANDiscoverable,
		ownsInbox: opts.RunInbox,
		stop:      cancel,
	}

	// Single-instance lock + control endpoint. Binding it is the lock.
	closer, err := daemonctl.Listen(rt.control())
	if err != nil {
		return err
	}
	defer closer.Close()

	deps.logf("share2us daemon started (inbox=%v lan=%v notify=%v)", opts.RunInbox, opts.LANDiscoverable, opts.Notify)

	var wg sync.WaitGroup
	if opts.RunInbox {
		wg.Add(1)
		go func() { defer wg.Done(); rt.inboxLoop(ctx, opts, deps) }()
	}
	if opts.LANDiscoverable {
		wg.Add(1)
		go func() { defer wg.Done(); rt.lanLoop(ctx, opts, deps) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); rt.scheduler(ctx, opts, deps) }()
	if opts.AgentBridge && deps.AgentClient != nil && deps.AgentRunner != nil {
		wg.Add(1)
		go func() { defer wg.Done(); rt.agentBridge(ctx, deps.AgentClient, deps.AgentRunner, deps) }()
	}

	<-ctx.Done()
	deps.logf("share2us daemon stopping")
	wg.Wait()
	return nil
}

// control returns the handler backing the control endpoint.
func (rt *Runtime) control() func(daemonctl.Request) daemonctl.Response {
	return func(req daemonctl.Request) daemonctl.Response {
		switch req.Op {
		case "ping":
			return daemonctl.Response{OK: true}
		case "status":
			return daemonctl.Response{
				OK: true, PID: os.Getpid(), Version: clicore.FullVersion(),
				OwnsLAN: rt.ownsLAN, OwnsInbox: rt.ownsInbox,
				Since: rt.startedAt.Format(time.RFC3339),
			}
		case "owns-receiver":
			return daemonctl.Response{OK: rt.ownsLAN || rt.ownsInbox, OwnsLAN: rt.ownsLAN, OwnsInbox: rt.ownsInbox}
		case "stop":
			if rt.stop != nil {
				rt.stop()
			}
			return daemonctl.Response{OK: true}
		default:
			return daemonctl.Response{Err: "unknown op"}
		}
	}
}

// inboxLoop polls the account inbox on opts.InboxInterval, notifying on arrivals.
func (rt *Runtime) inboxLoop(ctx context.Context, opts Options, deps Deps) {
	poll := func() {
		pctx, cancel := context.WithTimeout(ctx, jobTimeout)
		defer cancel()
		n, err := deps.ReceiveOnce(pctx, opts.DestDir)
		if err != nil {
			deps.logf("inbox poll: %v", err)
			return
		}
		if n > 0 {
			rt.notify("Share2Us", plural(n, "Received %d file", "Received %d files"))
		}
	}
	poll() // immediate first poll, like the tray receiver
	t := time.NewTicker(opts.InboxInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			poll()
		}
	}
}

// lanLoop runs the persistent discoverable LAN receiver. lanshare.Receive with
// Loop:true returns only when ctx is cancelled (or on a fatal listen error).
func (rt *Runtime) lanLoop(ctx context.Context, opts Options, deps Deps) {
	instance := opts.Instance
	if instance == "" {
		instance, _ = os.Hostname()
	}
	var mdns io.Closer
	ropts := lanshare.ReceiveOptions{
		Bind:       opts.Bind,
		Port:       opts.Port,
		NoPassword: true,
		TrustedIPs: opts.TrustedIPs,
		DestDir:    opts.DestDir,
		Loop:       true,
		OnRequest:  rt.approve(opts.ApprovalPolicy, deps),
		OnListen: func(info lanshare.ListenInfo) {
			if c, err := lanshare.Advertise(instance, info); err == nil {
				mdns = c
			}
			deps.logf("LAN receiver listening on :%d as %q", info.Port, instance)
		},
		OnReceived: func(r lanshare.ReceiveResult) {
			deps.logf("received %s (%d bytes) from %s", r.Name, r.Bytes, r.PeerIP)
			rt.notify("Share2Us", "Received "+r.Name)
		},
	}
	_, err := lanshare.Receive(ctx, ropts)
	if mdns != nil {
		_ = mdns.Close()
	}
	if err != nil && ctx.Err() == nil {
		deps.logf("LAN receiver stopped: %v", err)
	}
}

// scheduler runs the maintenance jobs on a coarse ticker, each panic-isolated and
// ctx-bounded so one failing job never wedges the loop.
func (rt *Runtime) scheduler(ctx context.Context, opts Options, deps Deps) {
	jobs := []job{
		{name: "trust-refresh", every: trustRefreshEvery, run: func(c context.Context) error {
			if deps.RefreshTrust != nil {
				deps.RefreshTrust(c)
			}
			return nil
		}},
		{name: "update-check", every: updateCheckEvery, run: func(c context.Context) error {
			if deps.CheckUpdate == nil {
				return nil
			}
			if available, msg := deps.CheckUpdate(c); available && msg != "" {
				rt.notify("Share2Us update", msg)
			}
			return nil
		}},
		{name: "cleanup", every: cleanupEvery, run: func(c context.Context) error {
			if deps.Cleanup != nil {
				return deps.Cleanup(c)
			}
			return nil
		}},
	}
	// Run each once at startup (last set in the past), then on its interval.
	now := time.Now()
	for i := range jobs {
		jobs[i].last = now.Add(-jobs[i].every)
	}
	t := time.NewTicker(schedulerTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			for i := range jobs {
				if now.Sub(jobs[i].last) < jobs[i].every {
					continue
				}
				jobs[i].last = now
				rt.runJob(ctx, deps, jobs[i])
			}
		}
	}
}

type job struct {
	name  string
	every time.Duration
	last  time.Time
	run   func(context.Context) error
}

func (rt *Runtime) runJob(ctx context.Context, deps Deps, j job) {
	defer func() {
		if r := recover(); r != nil {
			deps.logf("job %s panicked: %v", j.name, r)
		}
	}()
	jctx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()
	if err := j.run(jctx); err != nil {
		deps.logf("job %s: %v", j.name, err)
	}
}

func (rt *Runtime) notify(title, message string) {
	if rt.notifier != nil {
		rt.notifier.Info(title, message)
	}
}
