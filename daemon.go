package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli/internal/daemon"
)

// daemon is the sub-dispatcher for the optional background service (ADR-035).
// `run` is the long-lived process the service manager invokes; the rest are thin
// wrappers over the platform service manager, with `status`/`stop` also reaching
// a hand-started `run` over the control socket.
func (a app) daemon(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return a.daemonUsage()
	}
	switch args[0] {
	case "run":
		return a.daemonRun(ctx, args[1:])
	case "status":
		return a.daemonStatus()
	case "start":
		if err := daemon.ServiceStart(); err != nil {
			return a.fail("daemon start", err)
		}
		fmt.Fprintln(a.stdout, "Started.")
		return 0
	case "stop":
		return a.daemonStop()
	case "install":
		return a.daemonInstall(args[1:])
	case "uninstall":
		if err := daemon.ServiceUninstall(a.stdout); err != nil {
			return a.fail("daemon uninstall", err)
		}
		return 0
	case "logs":
		if err := daemon.ServiceLogs(hasFlag(args[1:], "-f", "--follow")); err != nil {
			return a.fail("daemon logs", err)
		}
		return 0
	default:
		return a.daemonUsage()
	}
}

func (a app) daemonUsage() int {
	fmt.Fprintf(a.stderr, "usage: %s daemon <run|status|start|stop|install|uninstall|logs>\n", commandName)
	fmt.Fprintf(a.stderr, "  run [--dest DIR] [--no-lan] [--no-notify]   run the background receiver (foreground)\n")
	fmt.Fprintf(a.stderr, "  install [--dest DIR]                         install + start the per-user service\n")
	fmt.Fprintf(a.stderr, "  status | stop | start | logs [-f] | uninstall\n")
	return 2
}

// daemonRun is the resident process. It composes the existing receive/trust/
// update helpers and hands them to the daemon runtime, installing a
// SIGTERM/SIGINT-cancelled context (the CLI otherwise never cancels ctx).
func (a app) daemonRun(ctx context.Context, args []string) int {
	opts, err := parseDaemonRunArgs(args)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}

	cfg, _ := clicore.LoadConfig()
	settings := cfg.DaemonSettings()
	destDir := opts.dest
	if destDir == "" {
		destDir = settings.DestDir
	}

	client, credential, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}

	runOpts := daemon.Options{
		DestDir:         destDir,
		LANDiscoverable: settings.LANDiscoverable && !opts.noLAN,
		Notify:          settings.Notify && !opts.noNotify,
		ApprovalPolicy:  settings.ApprovalPolicy,
		TrustedIPs:      loadLocalConfig().TrustedIPs(),
	}

	// Account-inbox e2e receive needs the device key, which a PAT cannot provide
	// (ADR-035). Disable inbox under a PAT rather than failing outright — the LAN
	// receiver still works. Only error if inbox is the only thing left to do.
	deviceCred, keyErr := ensureDeviceKey(ctx, client, credential)
	if keyErr == nil {
		runOpts.RunInbox = true
		credential = deviceCred
	} else {
		fmt.Fprintf(a.stderr, "note: account-inbox receive is off (%v)\n", keyErr)
		if !runOpts.LANDiscoverable {
			fmt.Fprintln(a.stderr, "nothing to do: inbox needs an interactive login and LAN is disabled")
			return 4
		}
	}

	deps := daemon.Deps{
		ReceiveOnce: func(c context.Context, dir string) (int, error) {
			return receiveInboxOnce(c, client, credential, dir, a.stdout)
		},
		RefreshTrust: a.refreshTrustList,
		CheckUpdate:  a.daemonUpdateCheck,
		Cleanup:      func(c context.Context) error { return cleanupStaging(destDir) },
		Logf:         func(format string, args ...any) { fmt.Fprintf(a.stderr, format+"\n", args...) },
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := daemon.Run(ctx, runOpts, deps); err != nil {
		if errors.Is(err, daemon.ErrAlreadyRunning) {
			fmt.Fprintln(a.stderr, "a share2us daemon is already running for this user")
			return 1
		}
		return a.fail("daemon run", err)
	}
	return 0
}

func (a app) daemonStatus() int {
	resp, ok := daemon.Query("status")
	if !ok || !resp.OK {
		fmt.Fprintln(a.stdout, "not running")
		return 3
	}
	fmt.Fprintf(a.stdout, "running (pid %d, %s)\n", resp.PID, resp.Version)
	fmt.Fprintf(a.stdout, "  inbox receive: %s\n", onOff(resp.OwnsInbox))
	fmt.Fprintf(a.stdout, "  LAN receiver:  %s\n", onOff(resp.OwnsLAN))
	if resp.Since != "" {
		fmt.Fprintf(a.stdout, "  since:         %s\n", resp.Since)
	}
	return 0
}

func (a app) daemonStop() int {
	// Prefer the control socket so a hand-started `run` also stops; fall back to
	// the service manager for an installed unit.
	if resp, ok := daemon.Query("stop"); ok && resp.OK {
		fmt.Fprintln(a.stdout, "Stopped.")
		return 0
	}
	if err := daemon.ServiceStop(); err != nil {
		fmt.Fprintln(a.stdout, "not running")
		return 3
	}
	fmt.Fprintln(a.stdout, "Stopped.")
	return 0
}

func (a app) daemonInstall(args []string) int {
	opts, err := parseDaemonRunArgs(args)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	if !daemon.ServiceSupported() {
		fmt.Fprintln(a.stderr, daemon.ErrServiceUnsupported)
		return 1
	}
	exe, err := a.currentExecutable()
	if err != nil {
		return a.fail("resolve executable", err)
	}
	dest := opts.dest
	if dest != "" {
		if abs, err := filepath.Abs(dest); err == nil {
			dest = abs
		}
	}
	if err := daemon.ServiceInstall(exe, dest, a.stdout); err != nil {
		return a.fail("daemon install", err)
	}
	return 0
}

// daemonUpdateCheck reports whether a newer build exists, with the right upgrade
// line for a managed install. It shares the 24h update-check cache with the
// interactive path so the two never double-check.
func (a app) daemonUpdateCheck(ctx context.Context) (bool, string) {
	cache, err := clicore.LoadUpdateCheckCache()
	if err == nil && !cache.LastCheckedAt.IsZero() && time.Since(cache.LastCheckedAt) < updateCheckInterval {
		return false, ""
	}
	apiBase, _, err := resolveAPIBase()
	if err != nil {
		return false, ""
	}
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	info, err := clicore.NewClient(apiBase, "").CheckUpdateChannel(checkCtx, clicore.FullVersion(), runtime.GOOS, runtime.GOARCH, a.updateChannelFor(""))
	if err != nil {
		return false, ""
	}
	_ = clicore.SaveUpdateCheckCache(clicore.UpdateCheckCache{LastCheckedAt: time.Now().UTC(), LatestVersion: info.LatestVersion})
	if !info.UpdateAvailable || strings.TrimSpace(info.LatestVersion) == "" {
		return false, ""
	}
	how := commandName + " update"
	if m, ok := managedInstall(); ok {
		how = m.upgradeCommand
	}
	return true, fmt.Sprintf("%s %s is available. Run: %s", commandName, info.LatestVersion, how)
}

type daemonRunOpts struct {
	dest     string
	noLAN    bool
	noNotify bool
}

func parseDaemonRunArgs(args []string) (daemonRunOpts, error) {
	var o daemonRunOpts
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--dest" || arg == "-o":
			if i+1 >= len(args) {
				return o, errors.New("--dest needs a directory")
			}
			i++
			o.dest = args[i]
		case strings.HasPrefix(arg, "--dest="):
			o.dest = strings.TrimPrefix(arg, "--dest=")
		case arg == "--no-lan":
			o.noLAN = true
		case arg == "--no-notify":
			o.noNotify = true
		case arg == "--foreground":
			// accepted and ignored: `run` is always foreground; the service
			// manager backgrounds it.
		default:
			return o, fmt.Errorf("unknown flag %q", arg)
		}
	}
	return o, nil
}

// cleanupStaging removes stale decrypt-staging temp files (".<name>.tmp-*", left
// by an interrupted inbox receive) older than 24h under destDir and the cache
// objects dir. Name- and age-gated so it never touches a user's own files.
func cleanupStaging(destDir string) error {
	dirs := []string{}
	if destDir != "" {
		dirs = append(dirs, destDir)
	}
	if obj, err := clicore.CacheObjectsDir(); err == nil {
		dirs = append(dirs, obj)
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasPrefix(name, ".") || !strings.Contains(name, ".tmp-") {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
	return nil
}

func hasFlag(args []string, names ...string) bool {
	for _, a := range args {
		for _, n := range names {
			if a == n {
				return true
			}
		}
	}
	return false
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
