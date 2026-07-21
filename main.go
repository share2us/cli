package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"io"
	mrand "math/rand"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli-core/p2p"
	"github.com/share2us/cli/tui"
	localmcp "github.com/share2us/mcp/mcp"
)

const commandName = "share2us"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	app := app{
		stdin:        os.Stdin,
		stdout:       stdout,
		stderr:       stderr,
		sleep:        time.Sleep,
		runTUI:       tui.Run,
		stdinIsTTY:   isTerminalReader,
		stdoutIsTTY:  isTerminalWriter,
		readPassword: readPasswordNoEcho,
	}
	return app.run(context.Background(), args)
}

type app struct {
	stdin          io.Reader
	stdout         io.Writer
	stderr         io.Writer
	sleep          func(time.Duration)
	runTUI         func(context.Context, tui.Client, io.Reader, io.Writer) error
	stdinIsTTY     func(io.Reader) bool
	stdoutIsTTY    func(io.Writer) bool
	readPassword   func(prompt string, stderr io.Writer) (string, error)
	executablePath func() (string, error)
}

func (a app) run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.stdout, clicore.Usage(commandName))
		return 0
	}
	code := a.runCommand(ctx, args)
	a.maybeReportInstall(ctx)
	if code == 0 {
		a.maybeNotifyUpdate(ctx, args[0])
		a.maybeShowTip(args[0])
	}
	return code
}

// maybeReportInstall sends the one-time anonymous install ping (todo §J.4).
// Interactive TTY only: this keeps ephemeral CI containers (fresh config every
// run) from over-counting installs, and keeps it out of scripts and tests. Once
// per persistent config; opt out with SHARE2US_NO_TELEMETRY=1.
func (a app) maybeReportInstall(ctx context.Context) {
	if a.stdoutIsTTY == nil || !a.stdoutIsTTY(a.stdout) {
		return
	}
	if os.Getenv("SHARE2US_NO_TELEMETRY") == "1" {
		return
	}
	cfg, err := clicore.LoadConfig()
	if err != nil || cfg.InstallReported {
		return
	}
	if strings.TrimSpace(cfg.InstallID) == "" {
		cfg.InstallID = clicore.NewInstallID()
	}
	// Mark reported first so we attempt exactly once, even if the ping fails.
	cfg.InstallReported = true
	_ = clicore.SaveConfig(cfg)
	if cfg.InstallID == "" {
		return
	}
	apiBase, _, err := resolveAPIBase()
	if err != nil {
		return
	}
	reportCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
	defer cancel()
	_ = clicore.NewClient(apiBase, "").ReportInstall(reportCtx, clicore.InstallEvent{
		InstallID: cfg.InstallID,
		EventType: "install",
		Version:   clicore.FullVersion(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	})
}

// tipEveryN controls tip frequency: roughly one interactive command in N shows
// a one-line product tip.
const tipEveryN = 5

// maybeShowTip occasionally prints a short one-line product tip to stderr after
// a successful command. Interactive TTY only (never scripts, pipes, or --json
// output), suppressible via SHARE2US_NO_TIPS=1.
func (a app) maybeShowTip(command string) {
	if os.Getenv("SHARE2US_NO_TIPS") == "1" {
		return
	}
	if a.stdoutIsTTY == nil || !a.stdoutIsTTY(a.stdout) {
		return
	}
	switch command {
	case "", "help", "-h", "--help", "version", "-v", "--version", "tui", "mcp":
		return
	}
	if mrand.Intn(tipEveryN) != 0 {
		return
	}
	if tip := clicore.RandomTip(commandName); tip != "" {
		fmt.Fprintf(a.stderr, "\n%s\n", tip)
	}
}

func (a app) runCommand(ctx context.Context, args []string) int {
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(a.stdout, clicore.Usage(commandName))
		return 0
	case "version", "-v", "--version":
		fmt.Fprintf(a.stdout, "%s %s\n", commandName, clicore.FullVersion())
		return 0
	case "login":
		return a.login(ctx, args[1:])
	case "config":
		return a.config(args[1:])
	case "whoami":
		return a.whoami(ctx)
	case "logout":
		return a.logout(ctx)
	case "signout":
		return a.signout(ctx, args[1:])
	case "install-agent-tools":
		return a.installAgentTools(args[1:])
	case "update":
		return a.update(ctx, args[1:])
	case "get", "-g":
		return a.get(ctx, args[1:])
	case "pull":
		return a.pull(ctx, args[1:])
	case "devices":
		return a.devices(ctx)
	case "ls":
		return a.list(ctx, args[1:])
	case "rm", "delete":
		return a.remove(ctx, args[1:])
	case "stats":
		return a.stats(ctx, args[1:])
	case "revoke":
		return a.revoke(ctx, args[1:])
	case "revoke-all":
		return a.revoke(ctx, []string{"--all"})
	case "pause":
		return a.pause(ctx, args[1:], true)
	case "resume":
		return a.pause(ctx, args[1:], false)
	case "tui":
		return a.tui(ctx, args[1:])
	case "mcp":
		return a.mcp(ctx, args[1:])
	case "stream", "p2p":
		// Build-time feature flag, default OFF. The server gates P2P independently
		// (SHARE2US_P2P_ENABLED + the plan entitlement), so this only decides whether
		// this binary ships the commands at all.
		if !clicore.P2PStreamingEnabled() {
			fmt.Fprintln(a.stderr, "direct peer-to-peer streaming isn't available in this build yet.")
			return 1
		}
		if args[0] == "stream" {
			return a.p2pSend(ctx, args[1:])
		}
		return a.p2p(ctx, args[1:])
	case "receive":
		return a.receive(ctx, args[1:])
	case "reseal":
		return a.reseal(ctx, args[1:])
	case "inbound":
		return a.inbound(ctx, args[1:])
	case "contacts", "teammates": // "teammates" kept as a back-compat alias
		return a.teammates(ctx)
	case "trust":
		return a.setTeammateSender(ctx, args[1:], "auto")
	case "block":
		return a.setTeammateSender(ctx, args[1:], "disallowed")
	case "require-approval":
		return a.setTeammateSender(ctx, args[1:], "approvals")
	case "untrust", "unblock":
		return a.deleteTeammateSender(ctx, args[1:])
	case "incoming":
		return a.incoming(ctx, args[1:])
	default:
		// Offline local/LAN direct share (account-free, no cloud). Detected by
		// the --receive / --dest / --serve flags on a file-verb invocation.
		switch localShareMode(args) {
		case "receive":
			return a.lanReceive(ctx, args)
		case "send":
			return a.lanSend(ctx, args)
		case "serve":
			return a.lanServe(ctx, args)
		}
		return a.upload(ctx, args)
	}
}

func (a app) login(ctx context.Context, args []string) int {
	opts, err := parseLoginArgs(args)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	apiBase, _, err := resolveAPIBase()
	if opts.host != "" {
		apiBase = opts.host
		if err := saveAPIHost(opts.host); err != nil {
			return a.fail("save config", err)
		}
	} else if err != nil {
		return a.fail("resolve API host", err)
	}
	client := clicore.NewClient(apiBase, "")

	device, err := clicore.DetectDeviceMetadata(opts.deviceName)
	if err != nil {
		return a.fail("detect device", err)
	}
	code, err := client.StartDeviceCode(ctx, clicore.DeviceCodeRequest{
		DeviceName:    device.DeviceName,
		MachineID:     device.MachineID,
		OS:            device.OS,
		Arch:          device.Arch,
		ClientVersion: clicore.FullVersion(),
	})
	if err != nil {
		return a.fail("start login", err)
	}
	verificationURL := clicore.VerificationURL(code)
	if verificationURL != "" {
		fmt.Fprintf(a.stdout, "To authorize this device, visit:\n\n    %s\n\n", verificationURL)
		if code.VerificationURI != "" && code.UserCode != "" {
			fmt.Fprintf(a.stdout, "If the browser didn't open, enter code %s at %s.\n\n", code.UserCode, code.VerificationURI)
		}
		fmt.Fprintln(a.stdout, "Waiting for approval...")
		if shouldOpenBrowser(opts) {
			_ = clicore.OpenBrowser(verificationURL)
		}
	} else {
		fmt.Fprintf(a.stdout, "Open: %s\nEnter code: %s\n", code.VerificationURI, code.UserCode)
	}

	for {
		token, err := client.PollDeviceToken(ctx, code.DeviceCode)
		if clicore.IsAuthorizationPending(err) {
			a.sleep(clicore.SleepInterval(code.Interval))
			continue
		}
		if clicore.IsDeviceLimitReached(err) {
			if handled := a.handleLoginDeviceLimit(ctx, client, code, opts, err); handled != 0 {
				return handled
			}
			continue
		}
		if err != nil {
			return a.fail("poll login", err)
		}

		authClient := clicore.NewClient(apiBase, token.Credential)
		keyPair, err := reuseOrNewDeviceKeyPair()
		if err != nil {
			return a.fail("generate device key", err)
		}
		if err := authClient.RegisterDeviceKey(ctx, keyPair.PublicKey); err != nil {
			return a.fail("register device key", err)
		}
		me, err := authClient.Me(ctx)
		if err != nil {
			return a.fail("load account", err)
		}
		email := me.Email
		if email == "" {
			email = me.UserID
		}
		if err := clicore.SaveCredential(clicore.Credential{
			APIBase:          apiBase,
			Token:            token.Credential,
			Email:            email,
			DeviceSessionID:  token.DeviceSessionID,
			DevicePublicKey:  keyPair.PublicKey,
			DevicePrivateKey: keyPair.PrivateKey,
		}); err != nil {
			return a.fail("save credential", err)
		}
		fmt.Fprintf(a.stdout, "Logged in as %s\n", email)
		a.maybePromptConsent(ctx, authClient)
		return 0
	}
}

// maybePromptConsent asks a first-time (or post-terms-update) user to accept the
// Terms + Privacy/data-share policy right after login, and records acceptance.
// Interactive TTY only (a pipe/CI can't answer); a check failure never blocks login.
func (a app) maybePromptConsent(ctx context.Context, client *clicore.Client) {
	if !a.inputIsTTY() || a.stdoutIsTTY == nil || !a.stdoutIsTTY(a.stdout) {
		return
	}
	status, err := client.GetOnboarding(ctx)
	if err != nil || !status.ConsentRequired {
		return
	}
	fmt.Fprint(a.stdout, "\nBefore you start, please review and accept the Terms and Privacy/data-share policy:\n")
	fmt.Fprint(a.stdout, "  Terms:   https://share2.us/terms\n")
	fmt.Fprint(a.stdout, "  Privacy: https://share2.us/privacy\n\n")
	fmt.Fprint(a.stdout, "Share2Us processes the files and text you upload only to operate the service.\n")
	fmt.Fprint(a.stdout, "It does not sell your content or train models on it.\n\n")
	fmt.Fprint(a.stdout, "Do you accept the Terms and Privacy policy? [y/N] ")
	reader := bufio.NewReader(a.input())
	answer, rerr := reader.ReadString('\n')
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		return
	}
	if ans := strings.TrimSpace(strings.ToLower(answer)); ans != "y" && ans != "yes" {
		fmt.Fprintf(a.stderr, "Not accepted. You can accept later in the portal or by running `%s login` again; some features may be limited until you do.\n", commandName)
		return
	}
	if err := client.SubmitConsent(ctx, status.ConsentVersion); err != nil {
		fmt.Fprintln(a.stderr, "Couldn't record your consent right now — you can accept in the portal.")
		return
	}
	fmt.Fprintln(a.stdout, "Thanks — your consent has been recorded.")
}

// reuseOrNewDeviceKeyPair keeps the device's X25519 keypair stable across re-logins
// on the same machine, so end-to-end (teammate) shares sealed to this device stay
// decryptable after a re-login. A fresh keypair is generated only when none is stored
// locally (first login on this machine, or cleared credentials).
func reuseOrNewDeviceKeyPair() (clicore.DeviceKeyPair, error) {
	if cred, err := clicore.LoadCredential(); err == nil {
		if strings.TrimSpace(cred.DevicePublicKey) != "" && strings.TrimSpace(cred.DevicePrivateKey) != "" {
			return clicore.DeviceKeyPair{PublicKey: cred.DevicePublicKey, PrivateKey: cred.DevicePrivateKey}, nil
		}
	}
	return clicore.NewDeviceKeyPair()
}

type loginOptions struct {
	host       string
	deviceName string
	noBrowser  bool
	noInput    bool
}

func parseLoginArgs(args []string) (loginOptions, error) {
	var opts loginOptions
	opts.deviceName = strings.TrimSpace(os.Getenv("SHARE2US_DEVICE_NAME"))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--host":
			i++
			if i >= len(args) {
				return loginOptions{}, errors.New("--host requires a value")
			}
			host, err := clicore.NormalizeAPIHost(args[i])
			if err != nil {
				return loginOptions{}, err
			}
			opts.host = host
		case strings.HasPrefix(arg, "--host="):
			host, err := clicore.NormalizeAPIHost(strings.TrimPrefix(arg, "--host="))
			if err != nil {
				return loginOptions{}, err
			}
			opts.host = host
		case arg == "--device-name":
			i++
			if i >= len(args) {
				return loginOptions{}, errors.New("--device-name requires a value")
			}
			opts.deviceName = strings.TrimSpace(args[i])
		case strings.HasPrefix(arg, "--device-name="):
			opts.deviceName = strings.TrimSpace(strings.TrimPrefix(arg, "--device-name="))
		case arg == "--no-browser":
			opts.noBrowser = true
		case arg == "--no-input":
			opts.noInput = true
		default:
			return loginOptions{}, fmt.Errorf("unknown login argument: %s", arg)
		}
	}
	return opts, nil
}

func shouldOpenBrowser(opts loginOptions) bool {
	if opts.noBrowser {
		return false
	}
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return false
	}
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" {
		return false
	}
	return true
}

func (a app) handleLoginDeviceLimit(ctx context.Context, client *clicore.Client, code clicore.DeviceCodeResponse, opts loginOptions, err error) int {
	details, ok := clicore.DeviceLimitDetailsFromError(err)
	if !ok {
		return a.fail("poll login", &clicore.APIError{Code: "device_limit_reached", Message: "device/session limit reached"})
	}
	return a.handleLoginDeviceLimitDetails(ctx, client, code, opts, details)
}

func (a app) config(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(a.stderr, "usage: %s config set-base-url <domain>\n       %s config set-host <url>\n       %s config show\n       %s config set-default <key> <value>\n       %s config unset-default <key>\n       %s config defaults\n       %s config set device alias <name> <ip|pairing>\n       %s config set device trusted <alias|ip>\n       %s config delete device alias|trusted <name>\n", commandName, commandName, commandName, commandName, commandName, commandName, commandName, commandName, commandName)
		return 2
	}
	switch args[0] {
	case "set-base-url":
		if len(args) != 2 {
			fmt.Fprintf(a.stderr, "usage: %s config set-base-url <domain>\n", commandName)
			return 2
		}
		baseURL, err := clicore.NormalizeBaseURL(args[1])
		if err != nil {
			fmt.Fprintln(a.stderr, err)
			return 2
		}
		if err := saveBaseURL(baseURL); err != nil {
			return a.fail("save config", err)
		}
		apiBase, err := clicore.APIBaseFromBaseURL(baseURL)
		if err != nil {
			return a.fail("derive API base", err)
		}
		shareBase, err := clicore.ShareBaseFromBaseURL(baseURL)
		if err != nil {
			return a.fail("derive share base", err)
		}
		fmt.Fprintf(a.stdout, "Base URL set to %s\nAPI base: %s\nShare base: %s\n", baseURL, apiBase, shareBase)
		return 0
	case "set-host":
		if len(args) != 2 {
			fmt.Fprintf(a.stderr, "usage: %s config set-host <url>\n", commandName)
			return 2
		}
		host, err := clicore.NormalizeAPIHost(args[1])
		if err != nil {
			fmt.Fprintln(a.stderr, err)
			return 2
		}
		if err := saveAPIHost(host); err != nil {
			return a.fail("save config", err)
		}
		fmt.Fprintf(a.stdout, "API host set to %s\n", host)
		return 0
	case "show", "get-host":
		host, source, err := resolveAPIBase()
		if err != nil {
			return a.fail("resolve API host", err)
		}
		if args[0] == "get-host" {
			fmt.Fprintln(a.stdout, host)
			return 0
		}
		baseURL, baseSource, err := clicore.ResolveBaseURL()
		if err != nil {
			return a.fail("resolve base URL", err)
		}
		shareBase, shareSource, err := clicore.ResolveShareBase()
		if err != nil {
			return a.fail("resolve share base", err)
		}
		fmt.Fprintf(a.stdout, "Base URL: %s\nBase URL source: %s\nAPI base: %s\nAPI base source: %s\nShare base: %s\nShare base source: %s\n", baseURL, baseSource, host, source, shareBase, shareSource)
		return 0
	case "set-default":
		if len(args) != 3 {
			fmt.Fprintf(a.stderr, "usage: %s config set-default <key> <value>\n  keys: expires, reshare, encrypt, max-views, no-scan, allow-domains, deny-domains\n", commandName)
			return 2
		}
		if err := setUploadDefault(args[1], args[2]); err != nil {
			fmt.Fprintln(a.stderr, err)
			return 2
		}
		fmt.Fprintf(a.stdout, "Default %s set to %s\n", args[1], args[2])
		return 0
	case "unset-default":
		if len(args) != 2 {
			fmt.Fprintf(a.stderr, "usage: %s config unset-default <key>\n", commandName)
			return 2
		}
		if err := unsetUploadDefault(args[1]); err != nil {
			fmt.Fprintln(a.stderr, err)
			return 2
		}
		fmt.Fprintf(a.stdout, "Default %s cleared\n", args[1])
		return 0
	case "defaults":
		cfg, err := clicore.LoadConfig()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return a.fail("load config", err)
		}
		fmt.Fprint(a.stdout, formatUploadDefaults(cfg))
		return 0
	case "set":
		return a.configSetDevice(args[1:])
	case "delete":
		return a.configDeleteDevice(args[1:])
	default:
		fmt.Fprintf(a.stderr, "unknown config command: %s\n", args[0])
		return 2
	}
}

// mutateConfig loads, mutates, and saves the config (creating it if absent).
func mutateConfig(fn func(*clicore.Config) error) error {
	config, err := clicore.LoadConfig()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		config = clicore.Config{}
	}
	if config.Defaults == nil {
		config.Defaults = &clicore.UploadDefaults{}
	}
	if err := fn(&config); err != nil {
		return err
	}
	if config.Defaults.IsEmpty() {
		config.Defaults = nil // don't persist an empty "defaults": {}
	}
	return clicore.SaveConfig(config)
}

func parseConfigBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "on", "1":
		return true, nil
	case "false", "no", "off", "0":
		return false, nil
	}
	return false, fmt.Errorf("expected true or false, got %q", value)
}

// setUploadDefault validates + stores one standing upload default (O-C1). Only the
// SAFE options are defaultable; footgun options are intentionally absent.
func setUploadDefault(key, value string) error {
	return mutateConfig(func(c *clicore.Config) error {
		switch key {
		case "expires":
			if strings.TrimSpace(value) == "" {
				return errors.New("expires cannot be empty")
			}
			v := strings.TrimSpace(value)
			c.Defaults.Expires = &v
		case "reshare":
			b, err := parseConfigBool(value)
			if err != nil {
				return err
			}
			c.Defaults.Reshare = &b
		case "encrypt":
			b, err := parseConfigBool(value)
			if err != nil {
				return err
			}
			c.Defaults.Encrypt = &b
		case "no-scan":
			b, err := parseConfigBool(value)
			if err != nil {
				return err
			}
			c.Defaults.NoScan = &b
		case "max-views":
			n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
			if err != nil {
				return fmt.Errorf("max-views must be a non-negative integer: %w", err)
			}
			c.Defaults.MaxViews = &n
		case "allow-domains":
			c.Defaults.AllowedDomains = splitDomainList(value)
		case "deny-domains":
			c.Defaults.DeniedDomains = splitDomainList(value)
		default:
			return fmt.Errorf("unknown default key %q (keys: expires, reshare, encrypt, max-views, no-scan, allow-domains, deny-domains)", key)
		}
		return nil
	})
}

func unsetUploadDefault(key string) error {
	return mutateConfig(func(c *clicore.Config) error {
		switch key {
		case "expires":
			c.Defaults.Expires = nil
		case "reshare":
			c.Defaults.Reshare = nil
			c.Reshare = nil // also clear the legacy field
		case "encrypt":
			c.Defaults.Encrypt = nil
		case "no-scan":
			c.Defaults.NoScan = nil
		case "max-views":
			c.Defaults.MaxViews = nil
		case "allow-domains":
			c.Defaults.AllowedDomains = nil
		case "deny-domains":
			c.Defaults.DeniedDomains = nil
		default:
			return fmt.Errorf("unknown default key %q", key)
		}
		return nil
	})
}

func splitDomainList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if d := strings.ToLower(strings.TrimSpace(part)); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// formatUploadDefaults renders each defaultable option with its standing value.
func formatUploadDefaults(cfg clicore.Config) string {
	d := cfg.Defaults
	if d == nil {
		d = &clicore.UploadDefaults{}
	}
	str := func(p *string) string {
		if p == nil {
			return "(unset)"
		}
		return *p
	}
	boolStr := func(p *bool) string {
		if p == nil {
			return "(unset)"
		}
		if *p {
			return "true"
		}
		return "false"
	}
	uintStr := func(p *uint64) string {
		if p == nil {
			return "(unset)"
		}
		return strconv.FormatUint(*p, 10)
	}
	listStr := func(l []string) string {
		if len(l) == 0 {
			return "(unset)"
		}
		return strings.Join(l, ",")
	}
	reshare := cfg.ResolvedReshareDefault()
	var b strings.Builder
	fmt.Fprintln(&b, "Standing upload defaults (explicit flags always override):")
	fmt.Fprintf(&b, "  expires       %s\n", str(d.Expires))
	fmt.Fprintf(&b, "  reshare       %s\n", boolStr(reshare))
	fmt.Fprintf(&b, "  encrypt       %s\n", boolStr(d.Encrypt))
	fmt.Fprintf(&b, "  max-views     %s\n", uintStr(d.MaxViews))
	fmt.Fprintf(&b, "  no-scan       %s\n", boolStr(d.NoScan))
	fmt.Fprintf(&b, "  allow-domains %s\n", listStr(d.AllowedDomains))
	fmt.Fprintf(&b, "  deny-domains  %s\n", listStr(d.DeniedDomains))
	return b.String()
}

func (a app) whoami(ctx context.Context) int {
	client, credential, ok := a.authClient()
	if !ok {
		fmt.Fprintln(a.stdout, "not logged in")
		return 0
	}
	me, err := client.Me(ctx)
	if err != nil {
		return a.fail("whoami", err)
	}
	if credential.Email != "" {
		fmt.Fprintf(a.stdout, "Email: %s\n", credential.Email)
	}
	fmt.Fprintf(a.stdout, "Account: %s\nUser: %s\nPlan: %s\n", me.AccountID, me.UserID, me.PlanName)
	return 0
}

func (a app) logout(ctx context.Context) int {
	client, _, ok := a.authClient()
	if ok {
		if err := client.Logout(ctx); err != nil {
			var apiErr *clicore.APIError
			if !errors.As(err, &apiErr) {
				return a.fail("logout", err)
			}
		}
	}
	if err := clicore.DeleteCredential(); err != nil {
		return a.fail("delete credential", err)
	}
	fmt.Fprintln(a.stdout, "Logged out")
	return 0
}

func (a app) devices(ctx context.Context) int {
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	devices, err := client.ListDevices(ctx)
	if err != nil {
		return a.fail("list devices", err)
	}
	if len(devices.Sessions) == 0 {
		fmt.Fprintln(a.stdout, "No devices found")
		return 0
	}
	for _, device := range devices.Sessions {
		keyStatus := "no-key"
		if strings.TrimSpace(device.PublicKey) != "" {
			keyStatus = "key"
		}
		current := ""
		if device.Current {
			current = " current"
		}
		fmt.Fprintf(a.stdout, "%s\t%s\t%s%s\n", device.ID, device.DeviceName, keyStatus, current)
	}
	return 0
}

func (a app) signout(ctx context.Context, args []string) int {
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		devices, err := client.ListDevices(ctx)
		if err != nil {
			return a.fail("list devices", err)
		}
		printNumberedDeviceTable(a.stdout, devices.Sessions)
		fmt.Fprintf(a.stderr, "usage: %s signout <device-id|device-name>\n", commandName)
		return 2
	}
	target, err := resolveDeviceAlias(ctx, client, args[0])
	if err != nil {
		devices, listErr := client.ListDevices(ctx)
		if listErr == nil {
			printNumberedDeviceTable(a.stdout, devices.Sessions)
		}
		fmt.Fprintln(a.stderr, err)
		fmt.Fprintf(a.stderr, "usage: %s signout <device-id|device-name>\n", commandName)
		return 2
	}
	if err := client.RevokeDeviceSession(ctx, target.ID); err != nil {
		return a.fail("signout", err)
	}
	fmt.Fprintf(a.stdout, "Signed out %s (%s)\n", target.DeviceName, target.ID)
	return 0
}

func (a app) handleLoginDeviceLimitDetails(ctx context.Context, client *clicore.Client, code clicore.DeviceCodeResponse, opts loginOptions, details clicore.DeviceLimitDetails) int {
	fmt.Fprintf(a.stderr, "You're at your device limit")
	if details.Limit > 0 {
		fmt.Fprintf(a.stderr, " (%d)", details.Limit)
	}
	fmt.Fprintln(a.stderr, ".")
	printNumberedDeviceTable(a.stderr, details.Sessions)
	if opts.noInput || !a.inputIsTTY() {
		fmt.Fprintf(a.stderr, "Run 's2u signout <id>' from a signed-in device, or remove one at %s, then 's2u login' again.\n", portalDevicesURL(code))
		return 1
	}
	fmt.Fprintf(a.stderr, "You're at your device limit (%d). Sign out a device to continue - enter number(s) to sign out, or 'q' to cancel: ", details.Limit)
	reader := bufio.NewReader(a.input())
	answer, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return a.fail("read selection", err)
	}
	indexes, cancelled, err := parseDeviceSelection(answer, len(details.Sessions))
	if cancelled {
		fmt.Fprintln(a.stderr, "Login cancelled")
		return 1
	}
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	for _, index := range indexes {
		session := details.Sessions[index]
		if err := client.RevokeDeviceSessionWithDeviceCode(ctx, code.DeviceCode, session.ID); err != nil {
			return a.fail("sign out device", err)
		}
		fmt.Fprintf(a.stderr, "Signed out %s (%s)\n", session.DeviceName, session.ID)
	}
	fmt.Fprintln(a.stderr, "Device signed out. Continuing login...")
	return 0
}

func (a app) installAgentTools(args []string) int {
	opts, err := parseInstallAgentToolsArgs(args)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	fmt.Fprint(a.stdout, agentToolsGuide(opts.agent))
	return 0
}

type installAgentToolsOptions struct {
	agent string
}

func parseInstallAgentToolsArgs(args []string) (installAgentToolsOptions, error) {
	opts := installAgentToolsOptions{agent: "generic"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--print":
		case arg == "--agent":
			i++
			if i >= len(args) {
				return installAgentToolsOptions{}, errors.New("--agent requires a value")
			}
			opts.agent = normalizeAgent(args[i])
		case strings.HasPrefix(arg, "--agent="):
			opts.agent = normalizeAgent(strings.TrimPrefix(arg, "--agent="))
		case strings.HasPrefix(arg, "-"):
			return installAgentToolsOptions{}, fmt.Errorf("unknown flag: %s", arg)
		default:
			opts.agent = normalizeAgent(arg)
		}
	}
	return opts, nil
}

func normalizeAgent(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "claude", "claude-code", "claude_code":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "gemini", "gemini-cli", "gemini_cli":
		return "Gemini CLI"
	case "":
		return "generic"
	default:
		return value
	}
}

func agentToolsGuide(agent string) string {
	if agent == "" || agent == "generic" {
		agent = "your agent"
	}
	return fmt.Sprintf(`Share2Us agent tools for %s

Prerequisites:
  1. Install the share2us CLI.
  2. Add alias s2u=share2us to your shell profile; installers should add this by default.
     Optional: add alias share=share2us too if you want the shortest command.
  3. Run: share2us login
  4. Configure the agent to start the local MCP bridge over stdio.

MCP stdio config:
{
  "mcpServers": {
    "share2us": {
      "command": "share2us",
      "args": ["mcp", "serve"]
    }
  }
}

Behavior:
  - Agents must use share2us_preview_file before file upload.
  - Agents must show path, filename, size, expiry, and public-link reachability.
  - Agents must wait for explicit user confirmation before upload.
  - share2us_upload_file requires confirm: true.
  - No silent uploads. No guest uploads through agents.
  - Credentials are loaded by the local CLI and must never be printed.

Portable skill:
  Use the Share2Us agent-skill package SKILL.md as the agent instruction file.
`, agent)
}

type updateOptions struct {
	host    string
	version string
}

const updateCheckInterval = 24 * time.Hour

func (a app) maybeNotifyUpdate(ctx context.Context, command string) {
	if shouldSkipUpdateCheck(command) {
		return
	}
	if a.stdoutIsTTY == nil || !a.stdoutIsTTY(a.stdout) {
		return
	}
	cache, err := clicore.LoadUpdateCheckCache()
	if err == nil && !cache.LastCheckedAt.IsZero() && time.Since(cache.LastCheckedAt) < updateCheckInterval {
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	apiBase, _, err := resolveAPIBase()
	if err != nil {
		return
	}
	client := clicore.NewClient(apiBase, "")
	info, err := client.CheckUpdate(checkCtx, clicore.FullVersion(), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return
	}
	_ = clicore.SaveUpdateCheckCache(clicore.UpdateCheckCache{
		LastCheckedAt: time.Now().UTC(),
		LatestVersion: info.LatestVersion,
	})
	if info.UpdateAvailable && strings.TrimSpace(info.LatestVersion) != "" {
		fmt.Fprintf(a.stderr, "Update available: %s %s. Run: %s update\n", commandName, info.LatestVersion, commandName)
	}
}

func shouldSkipUpdateCheck(command string) bool {
	if os.Getenv("SHARE2US_NO_UPDATE_CHECK") == "1" {
		return true
	}
	switch command {
	case "", "help", "-h", "--help", "version", "-v", "--version", "update", "mcp", "tui", "stream", "p2p", "receive":
		return true
	default:
		return false
	}
}

func (a app) update(ctx context.Context, args []string) int {
	opts, err := parseUpdateArgs(args)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	apiBase, err := resolveUpdateAPIBase(opts.host)
	if err != nil {
		return a.fail("resolve update API", err)
	}
	target, err := a.currentExecutable()
	if err != nil {
		return a.fail("locate current executable", err)
	}
	client := clicore.NewClient(apiBase, "")
	updateInfo, err := client.CheckUpdate(ctx, clicore.FullVersion(), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return a.fail("check update", err)
	}
	if !updateInfo.UpdateAvailable {
		fmt.Fprintf(a.stdout, "%s is already up to date (%s)\n", commandName, updateInfo.LatestVersion)
		return 0
	}
	if updateInfo.Downloads.ArchiveURL == "" || updateInfo.Downloads.CRC32 == "" || updateInfo.Downloads.SizeBytes <= 0 {
		return a.fail("check update", errors.New("update manifest is missing archive URL, CRC, or size"))
	}
	fmt.Fprintf(a.stdout, "Updating %s %s -> %s\n", commandName, updateInfo.CurrentVersion, updateInfo.LatestVersion)
	fmt.Fprintf(a.stdout, "Downloading %s\n", updateInfo.Downloads.ArchiveURL)

	tmpDir, err := os.MkdirTemp("", "share2us-update-*")
	if err != nil {
		return a.fail("create temp dir", err)
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, filepath.Base(updateInfo.Downloads.ArchiveURL))
	if err := downloadFile(ctx, updateInfo.Downloads.ArchiveURL, archivePath); err != nil {
		return a.fail("download update", err)
	}
	if err := verifyUpdateArchive(archivePath, updateInfo.Downloads.CRC32, updateInfo.Downloads.SizeBytes); err != nil {
		return a.fail("verify CRC", err)
	}
	fmt.Fprintln(a.stdout, "CRC check passed")

	binPath, err := extractBinaryFromArchive(archivePath, tmpDir, binaryFileName())
	if err != nil {
		return a.fail("extract update", err)
	}
	if err := replaceExecutable(target, binPath); err != nil {
		return a.fail("install update", err)
	}
	fmt.Fprintf(a.stdout, "Updated %s to %s at %s\n", commandName, updateInfo.LatestVersion, target)
	return 0
}

func parseUpdateArgs(args []string) (updateOptions, error) {
	opts := updateOptions{version: "latest"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--host":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("usage: %s update [--host URL] [--version VERSION]", commandName)
			}
			opts.host = args[i+1]
			i++
		case "--version":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("usage: %s update [--host URL] [--version VERSION]", commandName)
			}
			opts.version = strings.TrimSpace(args[i+1])
			i++
		default:
			return opts, fmt.Errorf("unknown update argument: %s", args[i])
		}
	}
	if opts.version == "" {
		return opts, fmt.Errorf("version cannot be empty")
	}
	return opts, nil
}

func (a app) currentExecutable() (string, error) {
	executablePath := a.executablePath
	if executablePath == nil {
		executablePath = os.Executable
	}
	path, err := executablePath()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return path, nil
}

func resolveUpdateAPIBase(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return normalizeUpdateAPIBase(explicit)
	}
	apiBase, _, err := resolveAPIBase()
	return apiBase, err
}

func normalizeUpdateAPIBase(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("update host is required")
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	if parsed.Host == "" || parsed.Scheme == "" {
		return "", fmt.Errorf("update host must be an absolute URL or host name")
	}
	if strings.HasPrefix(parsed.Hostname(), "api.") || parsed.Port() != "" || parsed.Path != "" {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return clicore.NormalizeAPIHost(parsed.String())
	}
	baseURL, err := clicore.NormalizeBaseURL(value)
	if err != nil {
		return "", err
	}
	return clicore.APIBaseFromBaseURL(baseURL)
}

func downloadFile(ctx context.Context, sourceURL, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return err
	}
	resp, err := clicore.DefaultHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s returned HTTP %d", sourceURL, resp.StatusCode)
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

func verifyCRCFile(path, sidecar string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	actualCRC, size, err := posixCKSUM(file)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(sidecar)
	if err != nil {
		return err
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return fmt.Errorf("CRC sidecar is invalid")
	}
	expectedCRC, err := strconv.ParseUint(fields[0], 10, 32)
	if err != nil {
		return fmt.Errorf("CRC sidecar has invalid checksum: %w", err)
	}
	expectedSize, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return fmt.Errorf("CRC sidecar has invalid size: %w", err)
	}
	if uint32(expectedCRC) != actualCRC || expectedSize != size {
		return fmt.Errorf("CRC check failed for %s", filepath.Base(path))
	}
	return nil
}

func verifyUpdateArchive(path, expectedCRCText string, expectedSize int64) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	actualCRC, size, err := posixCKSUM(file)
	if err != nil {
		return err
	}
	expectedCRC, err := strconv.ParseUint(strings.TrimSpace(expectedCRCText), 10, 32)
	if err != nil {
		return fmt.Errorf("update manifest has invalid CRC: %w", err)
	}
	if uint32(expectedCRC) != actualCRC || expectedSize != size {
		return fmt.Errorf("CRC check failed for %s", filepath.Base(path))
	}
	return nil
}

func posixCKSUM(reader io.Reader) (uint32, int64, error) {
	var crc uint32
	var size int64
	buf := make([]byte, 32*1024)
	for {
		n, err := reader.Read(buf)
		for _, b := range buf[:n] {
			crc = posixCKSUMUpdate(crc, b)
			size++
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, 0, err
		}
	}
	for length := uint64(size); length > 0; length >>= 8 {
		crc = posixCKSUMUpdate(crc, byte(length&0xff))
	}
	return ^crc, size, nil
}

func posixCKSUMUpdate(crc uint32, b byte) uint32 {
	crc ^= uint32(b) << 24
	for range 8 {
		if crc&0x80000000 != 0 {
			crc = (crc << 1) ^ 0x04c11db7
		} else {
			crc <<= 1
		}
	}
	return crc
}

// binaryFileName is the name the CLI binary carries inside a release archive.
func binaryFileName() string {
	if runtime.GOOS == "windows" {
		return commandName + ".exe"
	}
	return commandName
}

// Windows releases ship as .zip; every other platform ships as .tar.gz.
func extractBinaryFromArchive(archivePath, destDir, binaryName string) (string, error) {
	if strings.HasSuffix(strings.ToLower(archivePath), ".zip") {
		return extractBinaryFromZip(archivePath, destDir, binaryName)
	}
	return extractBinaryFromTarGz(archivePath, destDir, binaryName)
}

func extractBinaryFromZip(archivePath, destDir, binaryName string) (string, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() || path.Base(entry.Name) != binaryName {
			continue
		}
		src, err := entry.Open()
		if err != nil {
			return "", err
		}
		outPath := filepath.Join(destDir, binaryName)
		out, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			src.Close()
			return "", err
		}
		_, copyErr := io.Copy(out, src)
		src.Close()
		if copyErr != nil {
			out.Close()
			return "", copyErr
		}
		if err := out.Close(); err != nil {
			return "", err
		}
		return outPath, nil
	}
	return "", fmt.Errorf("archive did not contain %s", binaryName)
}

func extractBinaryFromTarGz(archivePath, destDir, binaryName string) (string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != binaryName {
			continue
		}
		outPath := filepath.Join(destDir, binaryName)
		out, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(out, reader); err != nil {
			out.Close()
			return "", err
		}
		if err := out.Close(); err != nil {
			return "", err
		}
		if err := os.Chmod(outPath, 0o755); err != nil {
			return "", err
		}
		return outPath, nil
	}
	return "", fmt.Errorf("archive did not contain %s", binaryName)
}

func (a app) upload(ctx context.Context, args []string) int {
	opts, err := parseUploadArgs(args)
	if err != nil {
		fmt.Fprintf(a.stderr, "%v\n", err)
		return 2
	}
	if opts.qr && opts.qrLink {
		fmt.Fprintln(a.stderr, "--qr encodes the content offline and --qrl shares a link; use one or the other")
		return 2
	}
	// Offline content QR: encode the text/file content itself — no upload, no
	// login. Falls through to the normal upload only if the user opts into the
	// link-QR fallback (for a too-big / binary file).
	if opts.qr {
		if done, code := a.contentQR(&opts); done {
			return code
		}
	}
	client, credential, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	if (opts.device != "" || opts.teammate != "") && clicore.IsAPIToken(credential.Token) {
		fmt.Fprintf(a.stderr, "device/contact end-to-end sends need an interactive login; a personal API token (%s) can't do device-to-device encryption\n", clicore.APITokenEnv)
		return 1
	}

	path := opts.path
	info, err := os.Stat(path)
	if err != nil {
		return a.fail("stat file", err)
	}
	folderUpload := false
	if info.IsDir() {
		if opts.device == "" && opts.teammate == "" {
			fmt.Fprintln(a.stderr, "folders can only be shared to a device (add --device <alias> or --contact <email>; run 's2u devices' to list yours)")
			return 2
		}
		folderUpload = true
		zippedPath, err := zipDirectory(path)
		if err != nil {
			return a.fail("zip folder", err)
		}
		defer os.Remove(zippedPath)
		path = zippedPath
		info, err = os.Stat(path)
		if err != nil {
			return a.fail("stat folder zip", err)
		}
		if opts.name == "" {
			opts.name = directoryZipName(opts.path)
		}
	}

	fileName := opts.name
	if fileName == "" {
		fileName = filepath.Base(path)
	}
	expiresInput := opts.expires
	if opts.keep {
		// --keep wins; reject an explicit finite --expires to avoid ambiguity.
		if opts.expiresSet {
			if _, ne, _ := clicore.ExpiryForAPI(opts.expires); !ne {
				return a.fail("parse expiry", errors.New("use --keep or a finite --expires, not both"))
			}
		}
		expiresInput = "none"
	}
	apiExpiry, noExpiry, err := clicore.ExpiryForAPI(expiresInput)
	if err != nil {
		return a.fail("parse expiry", err)
	}
	sourceRef := ""
	absPath := ""
	if folderUpload {
		absPath, err = filepath.Abs(opts.path)
		if err != nil {
			return a.fail("resolve source path", err)
		}
	} else if !opts.newShare {
		sourceRef, absPath, err = clicore.SourceRefForPath(path)
		if err != nil {
			return a.fail("hash source path", err)
		}
	} else {
		absPath, err = filepath.Abs(path)
		if err != nil {
			return a.fail("resolve source path", err)
		}
	}

	uploadPath := path
	uploadSize := info.Size()
	contentType := contentTypeForName(fileName)
	requestedContentClass := clicore.ContentClassForNameAndType(fileName, contentType)
	if folderUpload {
		contentType = "application/zip"
		requestedContentClass = clicore.ContentClassFolder
	}
	var dataKey []byte
	if opts.live || opts.watch {
		if opts.encrypt {
			fmt.Fprintln(a.stderr, "--live/--watch cannot be combined with --encrypt because live updates are text-only")
			return 2
		}
		if opts.device != "" {
			fmt.Fprintln(a.stderr, "--live/--watch cannot be combined with --device")
			return 2
		}
		if err := validateLiveTextFile(path); err != nil {
			fmt.Fprintln(a.stderr, err)
			return 2
		}
		contentType = textContentType(contentType)
	}
	if opts.teammate != "" && opts.device != "" {
		fmt.Fprintln(a.stderr, "--contact and --device cannot be combined")
		return 2
	}
	var targetDevice clicore.DeviceSession
	if opts.device != "" {
		if opts.encrypt {
			fmt.Fprintln(a.stderr, "--device already uses end-to-end encryption; do not combine it with --encrypt")
			return 2
		}
		var err error
		targetDevice, err = resolveDeviceAlias(ctx, client, opts.device)
		if err != nil {
			return a.fail("resolve target device", err)
		}
		if strings.TrimSpace(targetDevice.PublicKey) == "" {
			fmt.Fprintf(a.stderr, "device %q does not have an encryption key; log in again on that device\n", opts.device)
			return 1
		}
	}
	var teammateDevices []clicore.TeammateDevice
	teammateMode := ""
	// --email / --to always creates a recipient-restricted LINK that opens in the
	// browser after the recipient verifies their identity. Device end-to-end
	// delivery is opt-in only, via --contact or --device, so a plain --email is
	// never silently upgraded to a device-sealed share whose emailed link would
	// 404 in a browser (the recipient can't open a device-E2E share on the web).
	if opts.teammate != "" {
		if opts.device != "" {
			fmt.Fprintln(a.stderr, "--contact and --device cannot be combined")
			return 2
		}
		if opts.encrypt {
			fmt.Fprintln(a.stderr, "--contact already uses end-to-end encryption; do not combine it with --encrypt")
			return 2
		}
		if len(opts.recipients) > 0 {
			fmt.Fprintln(a.stderr, "--contact (device end-to-end) cannot be combined with --to/--email (recipient link shares)")
			return 2
		}
		if opts.live || opts.watch {
			fmt.Fprintln(a.stderr, "--contact cannot be combined with --live/--watch")
			return 2
		}
		if teammateDevices == nil {
			// explicit --teammate path: fetch + hard-error if not eligible
			list, err := client.TeammateDevices(ctx, opts.teammate)
			if err != nil {
				if a.printTeammateAPIError(err, opts.teammate) {
					return 1
				}
				return a.fail("resolve contact", err)
			}
			if list.Code == "recipient_not_registered" {
				fmt.Fprintf(a.stderr, "%s isn't on Share2Us yet - invites are coming soon.\n", opts.teammate)
				return 1
			}
			if len(list.Devices) == 0 {
				fmt.Fprintf(a.stderr, "%s has no device with encryption set up yet; ask them to run 's2u login'.\n", opts.teammate)
				return 1
			}
			teammateDevices = list.Devices
			teammateMode = list.Mode
		}
	}
	if code := a.runSecretPreflight(path, opts); code != 0 {
		return code
	}
	if opts.encrypt || opts.device != "" || opts.teammate != "" {
		dataKey, err = clicore.NewDataKey()
		if err != nil {
			return a.fail("generate encryption key", err)
		}
		tmp, err := os.CreateTemp("", "share2us-enc-*")
		if err != nil {
			return a.fail("create encrypted temp file", err)
		}
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)
		src, err := os.Open(path)
		if err != nil {
			tmp.Close()
			return a.fail("open file", err)
		}
		if err := clicore.EncryptStream(tmp, src, dataKey); err != nil {
			src.Close()
			tmp.Close()
			return a.fail("encrypt file", err)
		}
		src.Close()
		if err := tmp.Close(); err != nil {
			return a.fail("close encrypted file", err)
		}
		encryptedInfo, err := os.Stat(tmpPath)
		if err != nil {
			return a.fail("stat encrypted file", err)
		}
		uploadPath = tmpPath
		uploadSize = encryptedInfo.Size()
		if !folderUpload {
			contentType = "application/octet-stream"
		}
	}
	sealedKey := ""
	if opts.device != "" {
		sealedKey, err = clicore.SealContentKeyForDevice(dataKey, targetDevice.PublicKey)
		if err != nil {
			return a.fail("seal content key", err)
		}
	}
	var teammateTargets []clicore.UploadTarget
	if opts.teammate != "" {
		for _, d := range teammateDevices {
			sealed, err := clicore.SealContentKeyForDevice(dataKey, d.PublicKey)
			if err != nil {
				return a.fail("seal content key", err)
			}
			teammateTargets = append(teammateTargets, clicore.UploadTarget{TargetDeviceSessionID: d.DeviceID, SealedKey: sealed})
		}
	}

	sum, err := fileSHA256(uploadPath)
	if err != nil {
		return a.fail("hash file", err)
	}
	password := opts.password
	if opts.promptPassword {
		reader := a.readPassword
		if reader == nil {
			reader = readPasswordNoEcho
		}
		password, err = reader("Password: ", a.stderr)
		if err != nil {
			return a.fail("read password", err)
		}
		if password == "" {
			fmt.Fprintln(a.stderr, "password cannot be empty")
			return 2
		}
	}

	created, err := client.CreateUpload(ctx, clicore.UploadCreateRequest{
		FileName:    fileName,
		SizeBytes:   uint64(uploadSize),
		ContentType: contentType,
		ExpiresIn:   apiExpiry,
		NoExpiry:    noExpiry,
		SHA256:      sum,
		SourceRef:   sourceRef,
		New:         opts.newShare,
		ContentClass: func() string {
			if opts.live {
				return "text"
			}
			if opts.watch {
				return "text"
			}
			return requestedContentClass
		}(),
		Password:  password,
		OneTime:   opts.oneTime,
		Encrypted: opts.encrypt || opts.device != "" || opts.teammate != "",
		EncryptionAlgo: func() string {
			if opts.encrypt {
				return clicore.EncryptionAlgoAES256GCM
			}
			if opts.device != "" || opts.teammate != "" {
				return clicore.EncryptionAlgoAES256GCM + "+sealedbox"
			}
			return ""
		}(),
		Recipients:     opts.recipients,
		MaxViews:       opts.maxViews,
		AllowedDomains: opts.allowedDomains,
		DeniedDomains:  opts.deniedDomains,
		Live:           opts.live || opts.watch,
		TargetDevice:   targetDevice.ID,
		SealedKey:      sealedKey,
		RecipientEmail: opts.teammate,
		Targets:        teammateTargets,
		AllowReshare:   resolveAllowReshare(opts),
	})
	if err != nil {
		if opts.teammate != "" && a.printTeammateAPIError(err, opts.teammate) {
			return 1
		}
		if a.printEmailShareAPIError(err) {
			return 1
		}
		if (opts.live || opts.watch) && a.printLiveAPIError(err) {
			return 1
		}
		a.hintLocalShareOnUnreachable(err, opts.path)
		return a.fail("create upload", err)
	}

	completed := clicore.UploadCompleteResponse{
		PublicID:  created.Share.PublicID,
		Status:    "ready",
		ExpiresAt: created.ExpiresAt,
		Version:   created.Share.Version,
	}
	if !created.SkippedUpload && created.Upload.URL != "" && created.UploadSessionID != "" {
		file, err := os.Open(uploadPath)
		if err != nil {
			return a.fail("open file", err)
		}
		defer file.Close()
		if err := client.PutUpload(ctx, created.Upload, file, uploadSize); err != nil {
			return a.fail("upload file", err)
		}

		completed, err = client.CompleteUpload(ctx, created.UploadSessionID)
		if err != nil {
			return a.fail("complete upload", err)
		}
	}
	publicID := completed.PublicID
	if publicID == "" {
		publicID = created.Share.PublicID
	}
	// Option B in-flight re-seal: retain the content key for device/contact E2E sends so we
	// can re-seal it to the recipient's new device key if they re-key before receiving. Only
	// sealed sends (never plain --encrypt link shares); best-effort (retention is a recovery
	// aid, not required for delivery).
	if (opts.teammate != "" || opts.device != "") && len(dataKey) == 32 && publicID != "" {
		if err := clicore.RetainContentKey(publicID, clicore.EncodeKey(dataKey), opts.teammate); err != nil {
			fmt.Fprintf(a.stderr, "warning: could not save re-seal key for %s: %v\n", publicID, err)
		}
	}
	expiresAt := completed.ExpiresAt
	if expiresAt == "" {
		expiresAt = created.ExpiresAt
	}
	canonicalLink := created.Link
	if canonicalLink == "" {
		canonicalLink = created.Share.Link
	}
	shareBase, _, err := clicore.ResolveShareBase()
	if err != nil {
		return a.fail("resolve share base", err)
	}
	link := shareBase + "/" + publicID
	if canonicalLink != "" {
		link = canonicalLink
	}
	if opts.encrypt {
		if canonicalLink != "" {
			link = shareURLWithKey(canonicalLink, dataKey)
		} else {
			link = clicore.ShareURLWithKey(shareBase, publicID, dataKey)
		}
	}
	if publicID != "" && link != "" && opts.device == "" && opts.teammate == "" {
		if err := saveSourceRegistryEntry(absPath, publicID, link); err != nil {
			return a.fail("save source registry", err)
		}
	}

	if opts.json {
		out := map[string]any{
			"public_id":  publicID,
			"link":       link,
			"status":     completed.Status,
			"expires_at": expiresAt,
			"live":       opts.live || opts.watch,
			"version":    completed.Version,
		}
		if opts.device != "" {
			out["target_device_id"] = targetDevice.ID
			out["target_device_name"] = targetDevice.DeviceName
			out["link"] = ""
		}
		if opts.teammate != "" {
			out["recipient_email"] = opts.teammate
			out["target_devices"] = len(teammateTargets)
			out["approval_required"] = teammateMode == "approvals"
			out["link"] = ""
		}
		if created.EmailSharesRemaining != nil {
			out["email_shares_remaining"] = *created.EmailSharesRemaining
		}
		writeJSON(a.stdout, out)
		if !opts.live && !opts.watch {
			return 0
		}
	} else {
		if opts.device != "" {
			fmt.Fprintf(a.stdout, "Sent %s to device %s\n", fileName, targetDevice.DeviceName)
		} else if opts.teammate != "" {
			fmt.Fprintf(a.stdout, "Sent %s to %s (%d device(s))\n", fileName, opts.teammate, len(teammateTargets))
			if teammateMode == "approvals" {
				fmt.Fprintf(a.stdout, "Waiting for %s to approve it before it lands on their device.\n", opts.teammate)
			}
		} else {
			fmt.Fprintln(a.stdout, "Uploaded successfully")
			fmt.Fprintf(a.stdout, "Link: %s\n", link)
			if opts.qrLink && link != "" {
				if art, qerr := clicore.RenderQR(link); qerr == nil {
					fmt.Fprint(a.stdout, art)
				}
			}
		}
		if opts.encrypt {
			fmt.Fprintln(a.stdout, "Encryption key is only in the URL fragment and was never sent to the server.")
		}
		fmt.Fprintf(a.stdout, "Expires: %s\n", expiresAt)
		if len(opts.recipients) > 0 {
			fmt.Fprintf(a.stdout, "Shared with %d recipient(s). They can only open it after signing in as that email - the link is safe to send directly.\n", len(opts.recipients))
			if created.EmailSharesRemaining != nil {
				fmt.Fprintf(a.stdout, "%d email-shares left this period\n", *created.EmailSharesRemaining)
			}
			fmt.Fprintln(a.stdout, "Tip: if the email doesn't arrive, ask them to check spam, or send them the link above.")
		}
	}
	if opts.live || opts.watch {
		return a.runLiveFile(ctx, client, publicID, path, contentType, opts.watch)
	}
	return 0
}

type uploadOptions struct {
	path           string
	expires        string
	expiresSet     bool
	keep           bool
	name           string
	password       string
	promptPassword bool
	oneTime        bool
	encrypt        bool
	recipients     []string
	maxViews       uint64
	allowedDomains []string
	deniedDomains  []string
	json           bool
	live           bool
	watch          bool
	device         string
	teammate       string
	newShare       bool
	allowSecrets   bool
	noScan         bool
	qr             bool
	qrLink         bool
	restrict       bool
	unrestrict     bool
}

func parseUploadArgs(args []string) (uploadOptions, error) {
	// Standing config defaults (O-C1) seed the options; explicit flags below
	// override. Env SHARE2US_DEFAULT_EXPIRY still beats the config expires default.
	cfg, _ := clicore.LoadConfig()
	opts := uploadOptions{expires: resolveDefaultExpiry(cfg)}
	if cfg.Defaults != nil {
		d := cfg.Defaults
		if d.Encrypt != nil {
			opts.encrypt = *d.Encrypt
		}
		if d.NoScan != nil {
			opts.noScan = *d.NoScan
		}
		if d.MaxViews != nil {
			opts.maxViews = *d.MaxViews
		}
		if len(d.AllowedDomains) > 0 {
			opts.allowedDomains = append(opts.allowedDomains, d.AllowedDomains...)
		}
		if len(d.DeniedDomains) > 0 {
			opts.deniedDomains = append(opts.deniedDomains, d.DeniedDomains...)
		}
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			opts.json = true
		case arg == "--expires":
			i++
			if i >= len(args) {
				return uploadOptions{}, errors.New("--expires requires a value")
			}
			opts.expires = args[i]
			opts.expiresSet = true
		case strings.HasPrefix(arg, "--expires="):
			opts.expires = strings.TrimPrefix(arg, "--expires=")
			opts.expiresSet = true
		case arg == "--keep" || arg == "-k":
			// Keep the share indefinitely (no expiry). Equivalent to --expires=none.
			opts.keep = true
		case arg == "--name":
			i++
			if i >= len(args) {
				return uploadOptions{}, errors.New("--name requires a value")
			}
			opts.name = args[i]
		case strings.HasPrefix(arg, "--name="):
			opts.name = strings.TrimPrefix(arg, "--name=")
		case arg == "--password":
			opts.promptPassword = true
		case strings.HasPrefix(arg, "--password="):
			opts.password = strings.TrimPrefix(arg, "--password=")
		case arg == "--one-time":
			opts.oneTime = true
		case arg == "--encrypt" || arg == "-e":
			opts.encrypt = true
		case arg == "--no-encrypt":
			opts.encrypt = false // reset a standing encrypt default for this upload
		case arg == "--scan":
			opts.noScan = false // reset a standing no-scan default for this upload
		case arg == "--live" || arg == "-l" || arg == "--keep-updated":
			opts.live = true
		case arg == "--watch" || arg == "-w":
			opts.watch = true
		case arg == "--device" || arg == "-d":
			i++
			if i >= len(args) {
				return uploadOptions{}, fmt.Errorf("%s requires a value", arg)
			}
			opts.device = strings.TrimSpace(args[i])
		case strings.HasPrefix(arg, "--device="):
			opts.device = strings.TrimSpace(strings.TrimPrefix(arg, "--device="))
		case arg == "--contact" || arg == "--teammate": // --teammate kept as a back-compat alias
			i++
			if i >= len(args) {
				return uploadOptions{}, errors.New("--contact requires an email")
			}
			opts.teammate = strings.ToLower(strings.TrimSpace(args[i]))
		case strings.HasPrefix(arg, "--contact="):
			opts.teammate = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(arg, "--contact=")))
		case strings.HasPrefix(arg, "--teammate="):
			opts.teammate = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(arg, "--teammate=")))
		case arg == "--new" || arg == "--fresh":
			opts.newShare = true
		case arg == "--allow-secrets" || arg == "--force":
			opts.allowSecrets = true
		case arg == "--no-scan":
			opts.noScan = true
		case arg == "--to" || arg == "--email":
			i++
			if i >= len(args) {
				return uploadOptions{}, fmt.Errorf("%s requires a value", arg)
			}
			opts.recipients = appendRecipients(opts.recipients, args[i])
		case strings.HasPrefix(arg, "--to="):
			opts.recipients = appendRecipients(opts.recipients, strings.TrimPrefix(arg, "--to="))
		case strings.HasPrefix(arg, "--email="):
			opts.recipients = appendRecipients(opts.recipients, strings.TrimPrefix(arg, "--email="))
		case arg == "--max-views":
			i++
			if i >= len(args) {
				return uploadOptions{}, errors.New("--max-views requires a value")
			}
			maxViews, err := parsePositiveUint(args[i], "--max-views")
			if err != nil {
				return uploadOptions{}, err
			}
			opts.maxViews = maxViews
		case strings.HasPrefix(arg, "--max-views="):
			maxViews, err := parsePositiveUint(strings.TrimPrefix(arg, "--max-views="), "--max-views")
			if err != nil {
				return uploadOptions{}, err
			}
			opts.maxViews = maxViews
		case arg == "--allow-domain":
			i++
			if i >= len(args) {
				return uploadOptions{}, errors.New("--allow-domain requires a value")
			}
			opts.allowedDomains = append(opts.allowedDomains, args[i])
		case strings.HasPrefix(arg, "--allow-domain="):
			opts.allowedDomains = append(opts.allowedDomains, strings.TrimPrefix(arg, "--allow-domain="))
		case arg == "--deny-domain":
			i++
			if i >= len(args) {
				return uploadOptions{}, errors.New("--deny-domain requires a value")
			}
			opts.deniedDomains = append(opts.deniedDomains, args[i])
		case strings.HasPrefix(arg, "--deny-domain="):
			opts.deniedDomains = append(opts.deniedDomains, strings.TrimPrefix(arg, "--deny-domain="))
		case arg == "--qr":
			opts.qr = true
		case arg == "--qrl" || arg == "--qr-link":
			opts.qrLink = true
		case arg == "--unrestrict":
			opts.unrestrict = true
		case arg == "--restrict":
			opts.restrict = true
		case strings.HasPrefix(arg, "-"):
			return uploadOptions{}, fmt.Errorf("unknown flag: %s", arg)
		default:
			if opts.path != "" {
				return uploadOptions{}, errors.New("upload accepts exactly one file")
			}
			opts.path = arg
		}
	}
	if opts.path == "" {
		return uploadOptions{}, errors.New("upload requires a file")
	}
	if opts.restrict && opts.unrestrict {
		return uploadOptions{}, errors.New("--restrict and --unrestrict cannot be combined")
	}
	return opts, nil
}

// resolveAllowReshare computes the allow_reshare value to send for a new share:
// --unrestrict → true, --restrict → false (explicit override), otherwise the
// standing s2u-config default (nil if unset, so the server default false applies).
// Only meaningful for PRIVATE shares; the server ignores it for public ones.
func resolveAllowReshare(opts uploadOptions) *bool {
	if opts.unrestrict {
		v := true
		return &v
	}
	if opts.restrict {
		v := false
		return &v
	}
	if cfg, err := clicore.LoadConfig(); err == nil {
		return cfg.ResolvedReshareDefault() // Defaults.Reshare, else legacy Reshare (O-C1)
	}
	return nil
}

// resolveDefaultExpiry picks the standing expiry: env SHARE2US_DEFAULT_EXPIRY wins,
// then the s2u-config default (O-C1), then the compiled fallback ("7d").
func resolveDefaultExpiry(cfg clicore.Config) string {
	if v := strings.TrimSpace(os.Getenv("SHARE2US_DEFAULT_EXPIRY")); v != "" {
		return v
	}
	if cfg.Defaults != nil && cfg.Defaults.Expires != nil {
		return *cfg.Defaults.Expires
	}
	return clicore.DefaultExpiry()
}

func parsePositiveUint(raw, flag string) (uint64, error) {
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("%s must be a positive integer", flag)
	}
	return value, nil
}

func (a app) runSecretPreflight(path string, opts uploadOptions) int {
	if opts.noScan {
		fmt.Fprintln(a.stderr, "secret scan skipped by --no-scan")
		return 0
	}
	result, err := clicore.ScanFileForSecrets(path, clicore.SecretScanOptions{})
	if err != nil {
		fmt.Fprintf(a.stderr, "secret scan unavailable: %v\n", err)
		if opts.allowSecrets {
			fmt.Fprintln(a.stderr, "proceeding because --allow-secrets was set")
			return 0
		}
		fmt.Fprintln(a.stderr, "share cancelled; rerun with --allow-secrets to proceed without a completed scan")
		return 1
	}
	if result.Skipped {
		fmt.Fprintf(a.stderr, "secret scan skipped: %s\n", result.SkipReason)
		return 0
	}
	if result.Truncated {
		fmt.Fprintf(a.stderr, "secret scan capped at %d bytes\n", result.ScannedBytes)
	}
	if len(result.Findings) == 0 {
		return 0
	}

	printSecretFindings(a.stderr, result.Findings)
	if opts.allowSecrets {
		fmt.Fprintln(a.stderr, "proceeding because --allow-secrets was set")
		return 0
	}
	if !a.inputIsTTY() {
		fmt.Fprintf(a.stderr, "%v\n", clicore.SecretFindingsError(len(result.Findings)))
		return 1
	}
	if a.confirmShareWithFindings(len(result.Findings)) {
		return 0
	}
	fmt.Fprintf(a.stderr, "%v\n", clicore.SecretFindingsError(len(result.Findings)))
	return 1
}

func printSecretFindings(w io.Writer, findings []clicore.SecretFinding) {
	fmt.Fprintf(w, "WARNING: %d potential secret(s) found by local gitleaks scan.\n", len(findings))
	for _, finding := range findings {
		rule := finding.RuleID
		if rule == "" {
			rule = "unknown-rule"
		}
		line := finding.Line
		if line <= 0 {
			line = 1
		}
		redacted := finding.Redacted
		if redacted == "" {
			redacted = "REDACTED"
		}
		fmt.Fprintf(w, "- rule=%s line=%d match=%s\n", rule, line, redacted)
	}
}

func (a app) inputIsTTY() bool {
	if a.stdinIsTTY == nil {
		return false
	}
	return a.stdinIsTTY(a.input())
}

func (a app) input() io.Reader {
	if a.stdin != nil {
		return a.stdin
	}
	return os.Stdin
}

func (a app) confirmShareWithFindings(count int) bool {
	fmt.Fprintf(a.stderr, "%d potential secret(s) found - share anyway? [y/N] ", count)
	reader := bufio.NewReader(a.input())
	answer, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "y" || answer == "yes"
}

// readYesNo reads a y/N answer from stdin (prompt is printed by the caller).
func (a app) readYesNo() bool {
	reader := bufio.NewReader(a.input())
	answer, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "y" || answer == "yes"
}

// contentQR handles `--qr`: render a QR of the file's content (or a literal text
// argument) directly in the terminal — offline, no upload, no login. Returns
// done=true with an exit code when it has fully handled the request; done=false
// (having set opts.qrLink) when the user opted into the upload-as-link fallback,
// so upload() should fall through to the normal path.
func (a app) contentQR(opts *uploadOptions) (bool, int) {
	if opts.json {
		fmt.Fprintln(a.stderr, "--qr renders a visual code and can't be combined with --json")
		return true, 2
	}

	var content []byte
	fromFile := false
	if info, statErr := os.Stat(opts.path); statErr == nil {
		if info.IsDir() {
			fmt.Fprintln(a.stderr, "--qr needs a text file or a text argument, not a directory")
			return true, 2
		}
		fromFile = true
		if info.Size() > clicore.QRContentMaxBytes {
			return a.qrFallback(opts, fmt.Sprintf("file is %d bytes — too large to embed in a scannable QR (max %d)", info.Size(), clicore.QRContentMaxBytes))
		}
		b, readErr := os.ReadFile(opts.path)
		if readErr != nil {
			return true, a.fail("read file", readErr)
		}
		content = b
	} else {
		// Not an existing path: treat the argument as literal text to encode.
		content = []byte(opts.path)
	}

	if !clicore.QRIsText(content) {
		if fromFile {
			return a.qrFallback(opts, "file is not text — binary content can't be embedded in a QR")
		}
		fmt.Fprintln(a.stderr, "the text contains characters that can't be encoded")
		return true, 2
	}
	if len(content) > clicore.QRContentMaxBytes {
		if fromFile {
			return a.qrFallback(opts, fmt.Sprintf("content is %d bytes — too large for a scannable QR (max %d)", len(content), clicore.QRContentMaxBytes))
		}
		fmt.Fprintf(a.stderr, "text is %d bytes — too long for a scannable QR (max %d)\n", len(content), clicore.QRContentMaxBytes)
		return true, 2
	}

	art, err := clicore.RenderQR(string(content))
	if err != nil {
		return true, a.fail("render qr", err)
	}
	if len(content) > clicore.QRContentWarnBytes {
		fmt.Fprintf(a.stderr, "note: %d bytes is large for a terminal QR; a shorter payload scans more reliably\n", len(content))
	}
	fmt.Fprint(a.stdout, art)
	return true, 0
}

// qrFallback is reached when `--qr` content can't be embedded (too big or
// binary). It never uploads without an explicit, interactive yes: non-TTY errors
// out; a TTY prompts to upload-as-link (optionally password-protected) or cancel.
func (a app) qrFallback(opts *uploadOptions, reason string) (bool, int) {
	if !a.inputIsTTY() {
		fmt.Fprintf(a.stderr, "%s\nnothing was uploaded. Re-run with --qrl to upload it and show a link QR (add --password to protect it).\n", reason)
		return true, 1
	}
	fmt.Fprintf(a.stderr, "%s\nUpload it and show a link QR instead? [y/N] ", reason)
	if !a.readYesNo() {
		fmt.Fprintln(a.stderr, "cancelled — nothing was uploaded")
		return true, 0
	}
	fmt.Fprint(a.stderr, "Password-protect the share? [y/N] ")
	if a.readYesNo() {
		reader := a.readPassword
		if reader == nil {
			reader = readPasswordNoEcho
		}
		pw, err := reader("Password: ", a.stderr)
		if err != nil {
			return true, a.fail("read password", err)
		}
		if pw == "" {
			fmt.Fprintln(a.stderr, "password cannot be empty")
			return true, 2
		}
		opts.password = pw
	}
	// Continue to the normal upload; the link QR is rendered after the link prints.
	opts.qrLink = true
	return false, 0
}

func printNumberedDeviceTable(w io.Writer, sessions []clicore.DeviceSession) {
	if len(sessions) == 0 {
		fmt.Fprintln(w, "No CLI devices found")
		return
	}
	fmt.Fprintf(w, "%-4s %-36s %-24s %-20s %-20s\n", "#", "ID", "Device", "Last used", "Created")
	for i, session := range sessions {
		name := session.DeviceName
		if name == "" {
			name = "Share2Us CLI"
		}
		marker := ""
		if session.Current {
			marker = " (this device)"
		}
		fmt.Fprintf(w, "%-4d %-36s %-24s %-20s %-20s%s\n", i+1, truncate(session.ID, 36), truncate(name, 24), formatDeviceTime(session.LastUsedAt), formatDeviceTime(session.CreatedAt), marker)
	}
}

func parseDeviceSelection(value string, max int) ([]int, bool, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "q" || value == "quit" || value == "cancel" {
		return nil, true, nil
	}
	if value == "" {
		return nil, false, errors.New("no device selected")
	}
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	seen := make(map[int]bool)
	var out []int
	for _, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil || n < 1 || n > max {
			return nil, false, fmt.Errorf("invalid device selection %q", field)
		}
		index := n - 1
		if !seen[index] {
			seen[index] = true
			out = append(out, index)
		}
	}
	if len(out) == 0 {
		return nil, false, errors.New("no device selected")
	}
	return out, false, nil
}

func formatDeviceTime(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return truncate(value, 20)
	}
	return parsed.Local().Format("2006-01-02 15:04")
}

func portalDevicesURL(code clicore.DeviceCodeResponse) string {
	base := strings.TrimSpace(code.VerificationURI)
	if base == "" {
		base = strings.TrimSpace(code.VerificationURIComplete)
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "https://app.share2.us/devices"
	}
	parsed.Path = "/devices"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func (a app) runLiveFile(ctx context.Context, client *clicore.Client, publicID, path, contentType string, durable bool) int {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	mode := "live"
	if durable {
		mode = "watch"
	}
	fmt.Fprintf(a.stdout, "%s: watching for changes. Press Ctrl-C to stop.\n", mode)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	flushTicker := (*time.Ticker)(nil)
	if durable {
		flushTicker = time.NewTicker(60 * time.Second)
		defer flushTicker.Stop()
	}

	var lastCRC string
	var pushed uint64
	push := func() error {
		crc, changed, err := putLiveFileIfChanged(ctx, client, publicID, path, contentType, lastCRC)
		if err != nil {
			return err
		}
		lastCRC = crc
		if !changed {
			fmt.Fprintln(a.stdout, "live: unchanged")
			return nil
		}
		pushed++
		fmt.Fprintf(a.stdout, "live: pushed v%d\n", pushed)
		return nil
	}
	if err := push(); err != nil {
		if a.printLiveAPIError(err) {
			return 1
		}
		return a.fail("push live share", err)
	}

	for {
		select {
		case <-ctx.Done():
			return a.flushLiveShare(context.Background(), client, publicID)
		case <-ticker.C:
			if err := push(); err != nil {
				if a.printLiveAPIError(err) {
					return 1
				}
				return a.fail("push live share", err)
			}
		case <-flushTick(flushTicker):
			if code := a.flushLiveShare(ctx, client, publicID); code != 0 {
				return code
			}
		}
	}
}

func flushTick(ticker *time.Ticker) <-chan time.Time {
	if ticker == nil {
		return nil
	}
	return ticker.C
}

func (a app) flushLiveShare(ctx context.Context, client *clicore.Client, publicID string) int {
	flushed, err := client.FlushShare(ctx, publicID)
	if err != nil {
		if a.printLiveAPIError(err) {
			return 1
		}
		return a.fail("flush live share", err)
	}
	if flushed.Version > 0 {
		fmt.Fprintf(a.stdout, "live: flushed v%d\n", flushed.Version)
	} else {
		fmt.Fprintln(a.stdout, "live: flushed")
	}
	return 0
}

func putLiveFileIfChanged(ctx context.Context, client *clicore.Client, publicID, path, contentType, lastCRC string) (string, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return lastCRC, false, err
	}
	if info.IsDir() {
		return lastCRC, false, errors.New("live source became a directory")
	}
	if err := validateLiveTextFile(path); err != nil {
		return lastCRC, false, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return lastCRC, false, err
	}
	crc := fmt.Sprintf("%08x", crc32.ChecksumIEEE(content))
	if crc == lastCRC {
		return lastCRC, false, nil
	}
	out, err := client.PutLive(ctx, publicID, clicore.LivePutRequest{
		Content:     string(content),
		CRC32:       crc,
		ContentType: textContentType(contentType),
	})
	if err != nil {
		return lastCRC, false, err
	}
	if !out.Changed {
		return crc, false, nil
	}
	return crc, true, nil
}

func (a app) watchLiveFile(ctx context.Context, client *clicore.Client, publicID, path, fileName, contentType, lastSHA string) int {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintln(a.stdout, "Watching for changes. Press Ctrl-C to stop.")
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(a.stdout, "Stopped watching")
			return 0
		case <-ticker.C:
			updatedSHA, version, changed, err := replaceLiveFileIfChanged(ctx, client, publicID, path, fileName, contentType, lastSHA)
			if err != nil {
				if a.printLiveAPIError(err) {
					return 1
				}
				return a.fail("update live share", err)
			}
			if !changed {
				continue
			}
			lastSHA = updatedSHA
			if version > 0 {
				fmt.Fprintf(a.stdout, "updated v%d\n", version)
			} else {
				fmt.Fprintln(a.stdout, "updated")
			}
		}
	}
}

func replaceLiveFileIfChanged(ctx context.Context, client *clicore.Client, publicID, path, fileName, contentType, lastSHA string) (string, uint64, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return lastSHA, 0, false, err
	}
	if info.IsDir() {
		return lastSHA, 0, false, errors.New("live source became a directory")
	}
	if err := validateLiveTextFile(path); err != nil {
		return lastSHA, 0, false, err
	}
	sum, err := fileSHA256(path)
	if err != nil {
		return lastSHA, 0, false, err
	}
	if sum == lastSHA {
		return lastSHA, 0, false, nil
	}
	created, err := client.CreateReplaceUpload(ctx, publicID, clicore.UploadCreateRequest{
		FileName:     fileName,
		SizeBytes:    uint64(info.Size()),
		ContentClass: "text",
		ContentType:  textContentType(contentType),
		SHA256:       sum,
	})
	if err != nil {
		return lastSHA, 0, false, err
	}
	file, err := os.Open(path)
	if err != nil {
		return lastSHA, 0, false, err
	}
	defer file.Close()
	if err := client.PutUpload(ctx, created.Upload, file, info.Size()); err != nil {
		return lastSHA, 0, false, err
	}
	completed, err := client.CompleteUpload(ctx, created.UploadSessionID)
	if err != nil {
		return lastSHA, 0, false, err
	}
	return sum, completed.Version, true, nil
}

func validateLiveTextFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer file.Close()
	buf := make([]byte, 8192)
	n, err := file.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read file: %w", err)
	}
	sample := buf[:n]
	if bytesContainNUL(sample) || !utf8.Valid(sample) {
		return errors.New("--live supports text files only; this file appears to be binary")
	}
	return nil
}

func bytesContainNUL(buf []byte) bool {
	for _, b := range buf {
		if b == 0 {
			return true
		}
	}
	return false
}

func textContentType(contentType string) string {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" || contentType == "application/octet-stream" {
		return "text/plain; charset=utf-8"
	}
	if strings.HasPrefix(contentType, "text/") || strings.Contains(contentType, "charset=") {
		return contentType
	}
	return "text/plain; charset=utf-8"
}

func (a app) printLiveAPIError(err error) bool {
	var apiErr *clicore.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case "live_update_limit":
		fmt.Fprintln(a.stderr, "live updates are limited on your current plan; upgrade to keep more files updated")
		return true
	case "live_update_not_allowed":
		fmt.Fprintln(a.stderr, "live updates are not available on your current plan; upgrade to use --live")
		return true
	default:
		return false
	}
}

func (a app) inbound(ctx context.Context, args []string) int {
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	if len(args) == 0 {
		return a.printTeammatePolicy(ctx, client)
	}
	mode := strings.ToLower(strings.TrimSpace(args[0]))
	switch mode {
	case "disallowed", "approvals", "auto":
	default:
		fmt.Fprintf(a.stderr, "usage: %s inbound [disallowed|approvals|auto]\n", commandName)
		return 2
	}
	if err := client.SetTeammatePolicy(ctx, mode); err != nil {
		if a.printTeammateAPIError(err, "") {
			return 1
		}
		return a.fail("set inbound policy", err)
	}
	fmt.Fprintf(a.stdout, "Inbound contact policy set to %s.\n", mode)
	return 0
}

func (a app) teammates(ctx context.Context) int {
	client, credential, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	// Opportunistically re-seal any in-flight shares waiting on this sender (Option B).
	// Quiet + best-effort; contacts is a natural sender touchpoint. Interactive logins only
	// (API tokens can't do device E2E and hold no retained keys).
	if !clicore.IsAPIToken(credential.Token) {
		_, _ = runResealOnce(ctx, client, a.stdout, a.stderr, false)
	}
	return a.printTeammatePolicy(ctx, client)
}

func (a app) printTeammatePolicy(ctx context.Context, client *clicore.Client) int {
	policy, err := client.GetTeammatePolicy(ctx)
	if err != nil {
		if a.printTeammateAPIError(err, "") {
			return 1
		}
		return a.fail("get contact policy", err)
	}
	fmt.Fprintf(a.stdout, "Default inbound policy: %s\n", policy.Mode)
	if len(policy.Senders) == 0 {
		fmt.Fprintln(a.stdout, "No per-sender overrides. Use 's2u trust|block|require-approval <email>'.")
		return 0
	}
	fmt.Fprintln(a.stdout, "Per-sender overrides:")
	for _, s := range policy.Senders {
		fmt.Fprintf(a.stdout, "  %s\t%s\n", s.Email, s.Mode)
	}
	return 0
}

func (a app) setTeammateSender(ctx context.Context, args []string, mode string) int {
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	if len(args) == 0 {
		fmt.Fprintf(a.stderr, "usage: %s %s <email>\n", commandName, senderVerbForMode(mode))
		return 2
	}
	email := strings.ToLower(strings.TrimSpace(args[0]))
	if err := client.SetTeammateSender(ctx, email, mode); err != nil {
		if a.printTeammateAPIError(err, email) {
			return 1
		}
		return a.fail("set contact sender", err)
	}
	switch mode {
	case "auto":
		fmt.Fprintf(a.stdout, "%s is now trusted - their files arrive automatically.\n", email)
	case "disallowed":
		fmt.Fprintf(a.stdout, "%s is now blocked - they can't send you files.\n", email)
	case "approvals":
		fmt.Fprintf(a.stdout, "%s now requires your approval for each file.\n", email)
	}
	return 0
}

func (a app) deleteTeammateSender(ctx context.Context, args []string) int {
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	if len(args) == 0 {
		fmt.Fprintf(a.stderr, "usage: %s untrust|unblock <email>\n", commandName)
		return 2
	}
	email := strings.ToLower(strings.TrimSpace(args[0]))
	if err := client.DeleteTeammateSender(ctx, email); err != nil {
		if a.printTeammateAPIError(err, email) {
			return 1
		}
		return a.fail("remove contact sender", err)
	}
	fmt.Fprintf(a.stdout, "%s reverted to your default inbound policy.\n", email)
	return 0
}

func senderVerbForMode(mode string) string {
	switch mode {
	case "auto":
		return "trust"
	case "disallowed":
		return "block"
	default:
		return "require-approval"
	}
}

func (a app) incoming(ctx context.Context, args []string) int {
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	if len(args) > 0 {
		action := strings.ToLower(strings.TrimSpace(args[0]))
		if action != "approve" && action != "reject" {
			fmt.Fprintf(a.stderr, "usage: %s incoming [approve|reject <id> [--block]]\n", commandName)
			return 2
		}
		var id string
		block := false
		for _, arg := range args[1:] {
			if arg == "--block" {
				block = true
			} else if id == "" {
				id = strings.TrimSpace(arg)
			}
		}
		if id == "" {
			fmt.Fprintf(a.stderr, "usage: %s incoming %s <id> [--block]\n", commandName, action)
			return 2
		}
		// resolve the sender up front (needed if --block, and it's gone after reject)
		sender := ""
		if block {
			if pending, err := client.ListPendingInbox(ctx); err == nil {
				for _, s := range pending.Shares {
					if s.PublicID == id {
						sender = s.SenderEmail
						break
					}
				}
			}
		}
		var err error
		if action == "approve" {
			err = client.ApprovePendingInbox(ctx, id)
		} else {
			err = client.RejectPendingInbox(ctx, id)
		}
		if err != nil {
			return a.fail("update pending share", err)
		}
		if action == "approve" {
			fmt.Fprintf(a.stdout, "Approved %s. Run '%s receive' to download it.\n", id, commandName)
		} else {
			fmt.Fprintf(a.stdout, "Rejected %s.\n", id)
		}
		if block && action == "reject" {
			if sender == "" {
				fmt.Fprintln(a.stderr, "could not determine the sender to block")
				return 1
			}
			if err := client.SetTeammateSender(ctx, sender, "disallowed"); err != nil {
				if a.printTeammateAPIError(err, sender) {
					return 1
				}
				return a.fail("block sender", err)
			}
			fmt.Fprintf(a.stdout, "%s is now blocked - they can't send you files.\n", sender)
		}
		return 0
	}
	pending, err := client.ListPendingInbox(ctx)
	if err != nil {
		return a.fail("list pending inbox", err)
	}
	if len(pending.Shares) == 0 {
		fmt.Fprintln(a.stdout, "No files awaiting your approval.")
		return 0
	}
	fmt.Fprintln(a.stdout, "Files awaiting your approval:")
	for _, s := range pending.Shares {
		from := s.SenderEmail
		if from == "" {
			from = s.FromDeviceName
		}
		fmt.Fprintf(a.stdout, "  %s\t%s\t%d B\t%s\n", s.PublicID, s.FileName, s.SizeBytes, from)
	}
	fmt.Fprintf(a.stdout, "Approve with '%s incoming approve <id>' or reject with '%s incoming reject <id>'.\n", commandName, commandName)
	return 0
}

func (a app) printTeammateAPIError(err error, email string) bool {
	var apiErr *clicore.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	message := strings.TrimSpace(apiErr.Message)
	switch apiErr.Code {
	case "recipient_not_accepting":
		fmt.Fprintf(a.stderr, "%s is not accepting files from you (their inbound policy blocks it).\n", email)
		return true
	case "teammate_targeting_not_allowed":
		fmt.Fprintln(a.stderr, "Contact sharing isn't included in your plan. Upgrade to Pro to send files to contacts' devices.")
		return true
	case "teammate_limit_reached":
		if message != "" {
			fmt.Fprintln(a.stderr, message)
		} else {
			fmt.Fprintln(a.stderr, "You've reached your plan's contact limit.")
		}
		fmt.Fprintln(a.stderr, "Upgrade to Pro to share with more contacts.")
		return true
	case "recipient_not_registered":
		fmt.Fprintf(a.stderr, "%s isn't on Share2Us yet - invites are coming soon.\n", email)
		return true
	case "unknown_device":
		fmt.Fprintf(a.stderr, "%s has no matching device for this transfer; ask them to sign in again.\n", email)
		return true
	default:
		return false
	}
}

func (a app) printEmailShareAPIError(err error) bool {
	var apiErr *clicore.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	message := strings.TrimSpace(apiErr.Message)
	if message == "" {
		message = strings.TrimSpace(apiErr.Code)
	}
	switch apiErr.Code {
	case "email_share_quota_exceeded":
		if message == "" {
			message = "email share quota exceeded"
		}
		fmt.Fprintln(a.stderr, message)
		fmt.Fprintln(a.stderr, "upgrade to Pro for more")
		return true
	case "invite_rate_limited":
		if message != "" {
			fmt.Fprintln(a.stderr, message)
		}
		fmt.Fprintln(a.stderr, "too many invites right now, try later")
		return true
	default:
		return false
	}
}

func appendRecipients(out []string, value string) []string {
	for _, part := range strings.Split(value, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (a app) get(ctx context.Context, args []string) int {
	opts, err := parseGetArgs(args)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	target := opts.target
	if isAllASCIIDigits(target) {
		resolved, err := resolveShareRef(target)
		if err != nil {
			fmt.Fprintln(a.stderr, err)
			return 2
		}
		target = resolved
	}
	key := []byte(nil)
	if opts.key != "" {
		key, err = clicore.DecodeKey(opts.key)
	} else if strings.Contains(target, "#") {
		key, err = clicore.KeyFromShareURL(target)
	} else {
		err = clicore.ErrInvalidKey
	}
	if err != nil {
		fmt.Fprintln(a.stderr, "missing or invalid encryption key")
		return 2
	}
	downloadURL := target
	if !strings.HasPrefix(downloadURL, "http://") && !strings.HasPrefix(downloadURL, "https://") {
		apiBase, err := a.downloadAPIBase()
		if err != nil {
			return a.fail("resolve API host", err)
		}
		downloadURL, err = clicore.DownloadGatewayURL(apiBase, downloadURL)
		if err != nil {
			fmt.Fprintln(a.stderr, err)
			return 2
		}
	}
	if i := strings.Index(downloadURL, "#"); i >= 0 {
		downloadURL = downloadURL[:i]
	}
	downloadURL, err = forceDownloadMode(downloadURL)
	if err != nil {
		return a.fail("prepare download URL", err)
	}
	client := clicore.NewClient("", "")
	var ciphertext strings.Builder
	if err := client.DownloadURL(ctx, downloadURL, &ciphertext); err != nil {
		return a.fail("download encrypted share", err)
	}
	var out io.Writer = a.stdout
	var file *os.File
	if opts.output != "" {
		file, err = os.Create(opts.output)
		if err != nil {
			return a.fail("create output", err)
		}
		defer file.Close()
		out = file
	}
	if err := clicore.DecryptStream(out, strings.NewReader(ciphertext.String()), key); err != nil {
		return a.fail("decrypt encrypted share", err)
	}
	return 0
}

type getOptions struct {
	target string
	key    string
	output string
}

func parseGetArgs(args []string) (getOptions, error) {
	var opts getOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--key":
			i++
			if i >= len(args) {
				return getOptions{}, errors.New("--key requires a value")
			}
			opts.key = args[i]
		case strings.HasPrefix(arg, "--key="):
			opts.key = strings.TrimPrefix(arg, "--key=")
		case arg == "--output":
			i++
			if i >= len(args) {
				return getOptions{}, errors.New("--output requires a value")
			}
			opts.output = args[i]
		case strings.HasPrefix(arg, "--output="):
			opts.output = strings.TrimPrefix(arg, "--output=")
		case strings.HasPrefix(arg, "-"):
			return getOptions{}, fmt.Errorf("unknown flag: %s", arg)
		default:
			if opts.target != "" {
				return getOptions{}, fmt.Errorf("usage: %s get <url-or-public-id> [--key KEY] [--output PATH]", commandName)
			}
			opts.target = arg
		}
	}
	if opts.target == "" {
		return getOptions{}, fmt.Errorf("usage: %s get <url-or-public-id> [--key KEY] [--output PATH]", commandName)
	}
	return opts, nil
}

func (a app) pull(ctx context.Context, args []string) int {
	opts, err := parsePullArgs(args)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	if opts.all {
		return a.pullAll(ctx, opts.output)
	}
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	target := opts.target
	if isAllASCIIDigits(target) {
		resolved, err := resolveShareRef(target)
		if err != nil {
			fmt.Fprintln(a.stderr, err)
			return 2
		}
		target = resolved
	}
	publicID, err := publicIDFromTarget(target)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	share, err := client.GetShare(ctx, publicID)
	if err != nil {
		return a.fail("load share", err)
	}
	entry, err := pullShare(ctx, client, share)
	if err != nil {
		return a.fail("pull share", err)
	}
	if opts.output != "" {
		if err := copyFile(entry.Path, opts.output); err != nil {
			return a.fail("copy output", err)
		}
	}
	fmt.Fprintf(a.stdout, "Pulled %s to %s\n", share.PublicID, entry.Path)
	return 0
}

type pullOptions struct {
	target string
	output string
	all    bool
}

func parsePullArgs(args []string) (pullOptions, error) {
	var opts pullOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--all":
			opts.all = true
		case arg == "--output":
			i++
			if i >= len(args) {
				return pullOptions{}, errors.New("--output requires a value")
			}
			opts.output = args[i]
		case strings.HasPrefix(arg, "--output="):
			opts.output = strings.TrimPrefix(arg, "--output=")
		case strings.HasPrefix(arg, "-"):
			return pullOptions{}, fmt.Errorf("unknown flag: %s", arg)
		default:
			if opts.target != "" {
				return pullOptions{}, fmt.Errorf("usage: %s pull <url-or-public-id> [--output PATH]", commandName)
			}
			opts.target = arg
		}
	}
	if opts.all {
		if opts.target != "" {
			return pullOptions{}, fmt.Errorf("usage: %s pull --all [--output DIR]", commandName)
		}
		return opts, nil
	}
	if opts.target == "" {
		return pullOptions{}, fmt.Errorf("usage: %s pull <url-or-public-id> [--output PATH]", commandName)
	}
	return opts, nil
}

func (a app) pullAll(ctx context.Context, outputDir string) int {
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	shares, err := client.ListShares(ctx)
	if err != nil {
		return a.fail("list shares", err)
	}
	manifest, err := clicore.LoadCacheManifest()
	if err != nil {
		return a.fail("load cache manifest", err)
	}
	if outputDir != "" {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			return a.fail("create output directory", err)
		}
	}
	pulled := 0
	for _, share := range shares.Shares {
		if clicore.CacheEntryIsLocal(manifest[share.PublicID]) {
			continue
		}
		entry, err := pullShare(ctx, client, share)
		if err != nil {
			return a.fail("pull share "+share.PublicID, err)
		}
		if outputDir != "" {
			if err := copyFile(entry.Path, filepath.Join(outputDir, safeOutputName(share))); err != nil {
				return a.fail("copy output", err)
			}
		}
		pulled++
	}
	fmt.Fprintf(a.stdout, "Pulled %d share(s)\n", pulled)
	return 0
}

func (a app) list(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(a.stderr, "ls does not accept positional arguments")
		return 2
	}

	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	shares, err := client.ListShares(ctx)
	if err != nil {
		return a.fail("list shares", err)
	}
	manifest, err := clicore.LoadCacheManifest()
	if err != nil {
		return a.fail("load cache manifest", err)
	}
	registry, err := clicore.LoadSourceRegistry()
	if err != nil {
		return a.fail("load source registry", err)
	}
	rows := annotateShares(shares.Shares, manifest, registry)
	if err := saveListIndex(rows); err != nil {
		return a.fail("save list index", err)
	}
	if *jsonOut {
		writeJSON(a.stdout, map[string]any{"shares": rows})
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintln(a.stdout, "No shares")
		return 0
	}
	fmt.Fprintf(a.stdout, "%-3s %-18s %-24s %-10s %10s %-11s %-20s %-18s %-6s %-4s %-34s\n", "#", "PUBLIC_ID", "NAME", "STATUS", "SIZE", "VIEWS", "EXPIRES", "ORIGIN", "CACHE", "LIVE", "PATH")
	for _, row := range rows {
		fmt.Fprintf(a.stdout, "%-3d %-18s %-24s %-10s %10d %-11s %-20s %-18s %-6s %-4s %-34s\n", row.Serial, truncate(row.PublicID, 18), truncate(row.FileName, 24), shareStatus(row.Share), row.SizeBytes, viewsUsage(row.Share), row.ExpiresAt, truncate(row.OriginDevice, 18), row.Availability, liveIndicator(row.LiveUpdate), truncateLeft(row.Path, 34))
	}
	return 0
}

func (a app) stats(ctx context.Context, args []string) int {
	if len(args) != 1 {
		fmt.Fprintf(a.stderr, "usage: %s stats <public-id>\n", commandName)
		return 2
	}
	publicID, err := resolveShareRef(args[0])
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	if publicID == "" {
		fmt.Fprintln(a.stderr, "public id is required")
		return 2
	}
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	stats, err := client.ShareAnalytics(ctx, publicID)
	if err != nil {
		return a.fail("load share stats", err)
	}
	fmt.Fprintf(a.stdout, "Share: %s\n", publicID)
	fmt.Fprintf(a.stdout, "Views: %d\nDownloads: %d\nUnique visitors: %d\n", stats.Views, stats.Downloads, stats.UniqueVisitors)
	if stats.LastAccessedAt != "" {
		fmt.Fprintf(a.stdout, "Last accessed: %s\n", stats.LastAccessedAt)
	}
	if len(stats.Timeline) > 0 {
		fmt.Fprintln(a.stdout, "\nDaily:")
		for _, point := range stats.Timeline {
			fmt.Fprintf(a.stdout, "  %s  views=%d downloads=%d\n", point.Date, point.Views, point.Downloads)
		}
	}
	if len(stats.Recent) > 0 {
		fmt.Fprintln(a.stdout, "\nRecent:")
		for _, event := range stats.Recent {
			fmt.Fprintf(a.stdout, "  %s  %-16s %-3s %-15s %s\n", event.OccurredAt, event.EventType, valueOrDash(event.Country), valueOrDash(event.IP), truncate(event.Client, 48))
		}
	}
	return 0
}

func (a app) remove(ctx context.Context, args []string) int {
	refs, yes, err := parseRemoveArgs(args)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	targets := make([]removeTarget, 0, len(refs))
	for _, arg := range refs {
		publicID, entry, err := resolveShareRefWithEntry(arg)
		if err != nil {
			fmt.Fprintln(a.stderr, err)
			return 2
		}
		targets = append(targets, removeTarget{Arg: arg, PublicID: publicID, Entry: entry})
	}
	if !yes && a.stdinIsTTY != nil && a.stdinIsTTY(a.stdin) {
		fmt.Fprintf(a.stderr, "Remove %d share(s): %s? [y/N] ", len(targets), formatRemoveTargets(targets))
		line, _ := bufio.NewReader(a.stdin).ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(a.stderr, "cancelled")
			return 1
		}
	}
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	failed := false
	for _, target := range targets {
		if err := client.DeleteShare(ctx, target.PublicID); err != nil {
			fmt.Fprintf(a.stderr, "remove %s: %v\n", target.PublicID, err)
			failed = true
			continue
		}
		fmt.Fprintf(a.stdout, "Removed %s\n", target.PublicID)
	}
	if failed {
		return 1
	}
	return 0
}

func parseRemoveArgs(args []string) ([]string, bool, error) {
	refs := make([]string, 0, len(args))
	yes := false
	for _, arg := range args {
		switch {
		case arg == "--yes":
			yes = true
		case strings.HasPrefix(arg, "-"):
			return nil, false, fmt.Errorf("unknown flag: %s", arg)
		default:
			refs = append(refs, arg)
		}
	}
	if len(refs) == 0 {
		return nil, false, fmt.Errorf("usage: %s rm|delete <serial|public-id> [<serial|public-id>...] [--yes]", commandName)
	}
	return refs, yes, nil
}

type removeTarget struct {
	Arg      string
	PublicID string
	Entry    *clicore.ListIndexEntry
}

func formatRemoveTargets(targets []removeTarget) string {
	parts := make([]string, 0, len(targets))
	for _, target := range targets {
		if target.Entry != nil {
			name := strings.TrimSpace(target.Entry.FileName)
			if name == "" {
				name = target.PublicID
			}
			parts = append(parts, fmt.Sprintf("#%d %s (%s)", target.Entry.Serial, truncate(name, 24), truncate(target.PublicID, 12)))
			continue
		}
		parts = append(parts, truncate(target.PublicID, 18))
	}
	return strings.Join(parts, ", ")
}

func (a app) revoke(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("revoke", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	all := fs.Bool("all", false, "revoke all shares")
	yes := fs.Bool("yes", false, "skip confirmation")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *all {
		if fs.NArg() != 0 {
			fmt.Fprintln(a.stderr, "revoke --all does not accept a public id")
			return 2
		}
		if !*yes && a.stdinIsTTY != nil && a.stdinIsTTY(a.stdin) {
			fmt.Fprint(a.stderr, "Revoke all active shares? [y/N] ")
			line, _ := bufio.NewReader(a.stdin).ReadString('\n')
			answer := strings.ToLower(strings.TrimSpace(line))
			if answer != "y" && answer != "yes" {
				fmt.Fprintln(a.stderr, "cancelled")
				return 1
			}
		}
		client, _, ok := a.authClient()
		if !ok {
			fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
			return 1
		}
		out, err := client.RevokeAllShares(ctx)
		if err != nil {
			return a.fail("revoke all shares", err)
		}
		fmt.Fprintf(a.stdout, "Revoked %d share(s)\n", out.Revoked)
		return 0
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(a.stderr, "usage: %s revoke <public-id>|--all [--yes]\n", commandName)
		return 2
	}
	publicID, err := resolveShareRef(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	if publicID == "" {
		fmt.Fprintln(a.stderr, "public id is required")
		return 2
	}
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	share, err := client.RevokeShare(ctx, publicID)
	if err != nil {
		return a.fail("revoke share", err)
	}
	fmt.Fprintf(a.stdout, "Revoked %s (%s)\n", share.PublicID, share.Status)
	return 0
}

func (a app) pause(ctx context.Context, args []string, disabled bool) int {
	command := "pause"
	action := "Paused"
	if !disabled {
		command = "resume"
		action = "Resumed"
	}
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintf(a.stderr, "usage: %s %s <public-id>\n", commandName, command)
		return 2
	}
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	publicID, err := resolveShareRef(args[0])
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	var (
		share clicore.Share
	)
	if disabled {
		share, err = client.DisableShare(ctx, publicID)
	} else {
		share, err = client.EnableShare(ctx, publicID)
	}
	if err != nil {
		return a.fail(command+" share", err)
	}
	fmt.Fprintf(a.stdout, "%s %s\n", action, share.PublicID)
	return 0
}

func valueOrDash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

type listShareRow struct {
	clicore.Share
	Serial       int    `json:"serial"`
	OriginDevice string `json:"origin_device"`
	Availability string `json:"availability"`
	Path         string `json:"path"`
}

func annotateShares(shares []clicore.Share, manifest clicore.CacheManifest, registry clicore.SourceRegistry) []listShareRow {
	pathsByPublicID := make(map[string]string, len(registry))
	for absPath, entry := range registry {
		publicID := strings.TrimSpace(entry.PublicID)
		if publicID == "" {
			continue
		}
		if _, exists := pathsByPublicID[publicID]; !exists {
			pathsByPublicID[publicID] = absPath
		}
	}
	rows := make([]listShareRow, 0, len(shares))
	for i, share := range shares {
		origin := strings.TrimSpace(share.DeviceName)
		if origin == "" {
			origin = "Portal"
		}
		availability := "REMOTE"
		if clicore.CacheEntryIsLocal(manifest[share.PublicID]) {
			availability = "LOCAL"
		}
		localPath := pathsByPublicID[share.PublicID]
		if localPath == "" {
			localPath = "-"
		}
		rows = append(rows, listShareRow{Share: share, Serial: i + 1, OriginDevice: origin, Availability: availability, Path: localPath})
	}
	return rows
}

func saveListIndex(rows []listShareRow) error {
	entries := make([]clicore.ListIndexEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, clicore.ListIndexEntry{
			Serial:   row.Serial,
			PublicID: row.PublicID,
			FileName: row.FileName,
		})
	}
	return clicore.SaveListIndex(entries)
}

func resolveShareRef(arg string) (string, error) {
	publicID, _, err := resolveShareRefWithEntry(arg)
	return publicID, err
}

func resolveShareRefWithEntry(arg string) (string, *clicore.ListIndexEntry, error) {
	trimmed := strings.TrimSpace(arg)
	if trimmed == "" {
		return "", nil, errors.New("public id is required")
	}
	if !isAllASCIIDigits(trimmed) {
		return trimmed, nil, nil
	}
	serial, err := strconv.Atoi(trimmed)
	if err != nil || serial <= 0 {
		return "", nil, fmt.Errorf("no share #%s in the last listing; run `%s ls` first", trimmed, commandName)
	}
	entries, err := clicore.LoadListIndex()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, fmt.Errorf("no share #%d in the last listing; run `%s ls` first", serial, commandName)
		}
		return "", nil, err
	}
	for _, entry := range entries {
		if entry.Serial == serial && strings.TrimSpace(entry.PublicID) != "" {
			copyEntry := entry
			return entry.PublicID, &copyEntry, nil
		}
	}
	return "", nil, fmt.Errorf("no share #%d in the last listing; run `%s ls` first", serial, commandName)
}

func isAllASCIIDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func liveIndicator(live bool) string {
	if live {
		return "LIVE"
	}
	return "-"
}

func shareStatus(share clicore.Share) string {
	if share.Disabled {
		return "paused"
	}
	if strings.TrimSpace(share.Status) == "" {
		return "-"
	}
	return truncate(share.Status, 10)
}

func viewsUsage(share clicore.Share) string {
	max := "-"
	if share.MaxViews > 0 {
		max = strconv.FormatUint(share.MaxViews, 10)
	}
	return strconv.FormatUint(share.ViewCount, 10) + "/" + max
}

func pullShare(ctx context.Context, client *clicore.Client, share clicore.Share) (clicore.CacheEntry, error) {
	if share.PublicID == "" {
		return clicore.CacheEntry{}, errors.New("share public_id is empty")
	}
	if share.SHA256 == "" {
		return clicore.CacheEntry{}, errors.New("share metadata is missing sha256")
	}
	objectsDir, err := clicore.CacheObjectsDir()
	if err != nil {
		return clicore.CacheEntry{}, err
	}
	if err := os.MkdirAll(objectsDir, 0o700); err != nil {
		return clicore.CacheEntry{}, err
	}
	tmp, err := os.CreateTemp(objectsDir, "."+share.PublicID+"-*.tmp")
	if err != nil {
		return clicore.CacheEntry{}, err
	}
	tmpPath := tmp.Name()
	downloadURL, err := clicore.DownloadGatewayURL(client.BaseURL, share.PublicID)
	if err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return clicore.CacheEntry{}, err
	}
	downloadURL, err = forceDownloadMode(downloadURL)
	if err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return clicore.CacheEntry{}, err
	}
	if err := client.DownloadURL(ctx, downloadURL, tmp); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return clicore.CacheEntry{}, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return clicore.CacheEntry{}, err
	}
	sum, err := clicore.FileSHA256(tmpPath)
	if err != nil {
		os.Remove(tmpPath)
		return clicore.CacheEntry{}, err
	}
	if sum != share.SHA256 {
		os.Remove(tmpPath)
		return clicore.CacheEntry{}, fmt.Errorf("sha256 mismatch: got %s want %s", sum, share.SHA256)
	}
	info, err := os.Stat(tmpPath)
	if err != nil {
		os.Remove(tmpPath)
		return clicore.CacheEntry{}, err
	}
	if share.SizeBytes != 0 && uint64(info.Size()) != share.SizeBytes {
		os.Remove(tmpPath)
		return clicore.CacheEntry{}, fmt.Errorf("size mismatch: got %d want %d", info.Size(), share.SizeBytes)
	}
	finalPath := filepath.Join(objectsDir, share.PublicID)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return clicore.CacheEntry{}, err
	}
	if err := os.Chmod(finalPath, 0o600); err != nil {
		return clicore.CacheEntry{}, err
	}
	entry := clicore.CacheEntry{
		Path:      finalPath,
		SizeBytes: uint64(info.Size()),
		SHA256:    sum,
		FileName:  share.FileName,
		PulledAt:  time.Now().UTC(),
	}
	manifest, err := clicore.LoadCacheManifest()
	if err != nil {
		return clicore.CacheEntry{}, err
	}
	manifest = clicore.AddCacheEntry(manifest, share.PublicID, entry)
	if err := clicore.SaveCacheManifest(manifest); err != nil {
		return clicore.CacheEntry{}, err
	}
	return entry, nil
}

func forceDownloadMode(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("m", "download")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func saveSourceRegistryEntry(absPath, publicID, link string) error {
	if absPath == "" {
		return nil
	}
	registry, err := clicore.LoadSourceRegistry()
	if err != nil {
		return err
	}
	registry = clicore.AddSourceRegistryEntry(registry, absPath, clicore.SourceRegistryEntry{
		PublicID: publicID,
		Link:     link,
	})
	return clicore.SaveSourceRegistry(registry)
}

func shareURLWithKey(raw string, key []byte) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		separator := "#"
		if strings.Contains(raw, "#") {
			separator = "&"
		}
		return raw + separator + "k=" + clicore.EncodeKey(key)
	}
	values, err := url.ParseQuery(parsed.Fragment)
	if err != nil {
		parsed.Fragment = "k=" + clicore.EncodeKey(key)
		return parsed.String()
	}
	values.Set("k", clicore.EncodeKey(key))
	parsed.Fragment = values.Encode()
	return parsed.String()
}

func publicIDFromTarget(target string) (string, error) {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return "", errors.New("public id is required")
	}
	if i := strings.Index(trimmed, "#"); i >= 0 {
		trimmed = trimmed[:i]
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return "", fmt.Errorf("invalid URL: %w", err)
		}
		trimmed = strings.Trim(strings.TrimSpace(parsed.Path), "/")
		if i := strings.LastIndex(trimmed, "/"); i >= 0 {
			trimmed = trimmed[i+1:]
		}
	}
	if trimmed == "" || strings.ContainsAny(trimmed, "/?#") {
		return "", errors.New("invalid public id")
	}
	return trimmed, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, 0o600)
}

func safeOutputName(share clicore.Share) string {
	name := filepath.Base(strings.TrimSpace(share.FileName))
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = share.PublicID
	}
	return name
}

func (a app) tui(ctx context.Context, args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(a.stderr, "tui does not accept arguments")
		return 2
	}
	if a.stdoutIsTTY != nil && !a.stdoutIsTTY(a.stdout) {
		fmt.Fprintln(a.stderr, "refusing to launch TUI: stdout is not a TTY")
		return 1
	}
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	if err := a.runTUI(ctx, client, os.Stdin, a.stdout); err != nil {
		return a.fail("tui", err)
	}
	return 0
}

func (a app) mcp(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(a.stderr, "usage: %s mcp serve|token [--url URL|--staging] [--json]\n", commandName)
		return 2
	}
	switch args[0] {
	case "serve":
		if len(args) != 1 {
			fmt.Fprintf(a.stderr, "usage: %s mcp serve\n", commandName)
			return 2
		}
	case "token":
		return a.mcpToken(ctx, args[1:])
	default:
		fmt.Fprintf(a.stderr, "usage: %s mcp serve|token [--url URL|--staging] [--json]\n", commandName)
		return 2
	}
	if err := localmcp.ServeStdio(ctx); err != nil {
		return a.fail("mcp serve", err)
	}
	return 0
}

func (a app) mcpToken(ctx context.Context, args []string) int {
	opts, err := parseMCPTokenArgs(args)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	client, credential, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	// Historically this printed the raw device-session credential — a long-lived,
	// unscoped, full-account secret. Instead mint a scoped, revocable personal API
	// token so a leaked MCP token can only read/write/revoke shares, never take over
	// the account. Minting is session-only, so an env PAT can't mint another.
	if clicore.IsAPIToken(credential.Token) {
		fmt.Fprintln(a.stderr, "minting an MCP token needs an interactive login (a personal API token cannot mint another token); run `"+commandName+" login`")
		return 1
	}
	label := "mcp-" + mcpHostLabel(opts.url)
	// account:read is required: the MCP server validates the token by calling
	// GET /v1/auth/me (which needs account:read) before honoring any share scope.
	resp, err := client.CreateAPIToken(ctx, label, []string{"shares:read", "shares:write", "shares:revoke", "account:read"}, nil)
	if err != nil {
		var apiErr *clicore.APIError
		if errors.As(err, &apiErr) && apiErr.Code == "api_token_limit_reached" {
			fmt.Fprintf(a.stderr, "your plan's API-token limit is reached. Revoke an unused token in the portal (Account → API tokens), then try again.\n")
			return 1
		}
		return a.fail("create MCP token", err)
	}
	token := resp.Token

	if opts.json {
		_ = json.NewEncoder(a.stdout).Encode(map[string]string{
			"url":   opts.url,
			"token": token,
		})
		return 0
	}
	fmt.Fprintln(a.stderr, "Warning: this token is a secret and is shown only once. It is scoped to shares (read/write/revoke) and revocable in the portal (Account → API tokens).")
	fmt.Fprintf(a.stdout, "Share2Us MCP endpoint: %s\n", opts.url)
	fmt.Fprintf(a.stdout, "Authorization: Bearer %s\n\n", token)
	fmt.Fprintln(a.stdout, "Generic MCP client config:")
	_ = json.NewEncoder(a.stdout).Encode(map[string]any{
		"url": opts.url,
		"headers": map[string]string{
			"Authorization": "Bearer " + token,
		},
	})
	return 0
}

// mcpHostLabel derives a short host label from the MCP URL for the token name.
func mcpHostLabel(rawURL string) string {
	if u, err := url.Parse(strings.TrimSpace(rawURL)); err == nil && u.Host != "" {
		return u.Hostname()
	}
	return "client"
}

type mcpTokenOptions struct {
	url  string
	json bool
}

func parseMCPTokenArgs(args []string) (mcpTokenOptions, error) {
	opts := mcpTokenOptions{url: "https://mcp.share2.us/mcp"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			opts.json = true
		case arg == "--staging":
			opts.url = "https://mcp.staging.share2.us/mcp"
		case arg == "--url":
			i++
			if i >= len(args) {
				return mcpTokenOptions{}, errors.New("--url requires a value")
			}
			opts.url = strings.TrimSpace(args[i])
		case strings.HasPrefix(arg, "--url="):
			opts.url = strings.TrimSpace(strings.TrimPrefix(arg, "--url="))
		default:
			return mcpTokenOptions{}, fmt.Errorf("unknown mcp token argument: %s", arg)
		}
	}
	if opts.url == "" {
		return mcpTokenOptions{}, errors.New("--url requires a value")
	}
	return opts, nil
}

// p2p dispatches the direct peer-to-peer streaming subcommands. `stream` is kept
// as an alias for `p2p send`; the device-inbox pull keeps the top-level `receive`.
func (a app) p2p(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(a.stderr, "usage: %s p2p send <file> | %s p2p recv <code>\n", commandName, commandName)
		return 2
	}
	switch args[0] {
	case "send":
		return a.p2pSend(ctx, args[1:])
	case "recv", "receive":
		return a.p2pRecv(ctx, args[1:])
	default:
		fmt.Fprintf(a.stderr, "unknown p2p subcommand %q; use `send` or `recv`\n", args[0])
		return 2
	}
}

// p2pSend streams a file directly to a peer over WebRTC (no bytes at rest). It
// prints a one-time pairing code; the receiver runs `p2p recv <code>`. The code
// is <room>-<secret>: the relay only ever sees the room half, and the secret
// half anchors the SAS verification that defeats a MITM relay.
func (a app) p2pSend(ctx context.Context, args []string) int {
	opts, err := parseStreamArgs(args)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	file, err := os.Open(opts.path)
	if err != nil {
		return a.fail("open file", err)
	}
	defer file.Close()

	code, room, secret, err := newPairingCode()
	if err != nil {
		return a.fail("generate pairing code", err)
	}
	relayCfg, code2 := a.authorizeP2PRoom(ctx, client, room, secret, "send", opts.relay, opts.turns)
	if code2 != 0 {
		return code2
	}
	session, err := p2p.NewSession(relayCfg, p2p.Sender)
	if err != nil {
		return a.fail("p2p send", err)
	}
	defer session.Close()

	fmt.Fprintf(a.stdout, "Pairing code: %s\n", code)
	fmt.Fprintf(a.stdout, "On the receiving machine run:  %s p2p recv %s\n", commandName, code)
	fmt.Fprintln(a.stdout, "Waiting for the receiver to connect…")
	if err := session.Connect(ctx); err != nil {
		return a.fail("p2p connect", err)
	}
	fmt.Fprintln(a.stdout, "Connected — sending…")
	if err := session.Send(ctx, file); err != nil {
		return a.fail("p2p send", err)
	}
	fmt.Fprintln(a.stdout, "Sent.")
	return 0
}

// p2pRecv joins the sender's pairing code and receives the streamed file.
func (a app) p2pRecv(ctx context.Context, args []string) int {
	opts, err := parseP2PRecvArgs(args)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	client, _, loggedIn := a.authClient()
	if !loggedIn {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	room, secret, ok := splitPairingCode(opts.code)
	if !ok {
		fmt.Fprintln(a.stderr, "invalid pairing code (expected <room>-<secret>)")
		return 2
	}
	relayCfg, code := a.authorizeP2PRoom(ctx, client, room, secret, "recv", opts.relay, opts.turns)
	if code != 0 {
		return code
	}
	session, err := p2p.NewSession(relayCfg, p2p.Receiver)
	if err != nil {
		return a.fail("p2p recv", err)
	}
	defer session.Close()

	fmt.Fprintf(a.stdout, "Connecting to %s…\n", opts.code)
	if err := session.Connect(ctx); err != nil {
		return a.fail("p2p connect", err)
	}
	out, name, closeOut, err := p2pOutputWriter(opts.output, room, a.stdout)
	if err != nil {
		return a.fail("open output", err)
	}
	defer closeOut()
	fmt.Fprintln(a.stdout, "Connected — receiving…")
	if err := session.Receive(ctx, out); err != nil {
		return a.fail("p2p receive", err)
	}
	fmt.Fprintf(a.stdout, "Received -> %s\n", name)
	return 0
}

// p2pOutputWriter resolves the receiver's --out into a writer. "" → a default
// file in cwd; "-" → stdout; a directory → the default name inside it; otherwise
// the given file path.
func p2pOutputWriter(output, room string, stdout io.Writer) (io.Writer, string, func(), error) {
	defaultName := "share2us-p2p-" + strings.ToLower(room) + ".bin"
	if output == "-" {
		return stdout, "(stdout)", func() {}, nil
	}
	target := output
	if target == "" {
		target = defaultName
	} else if info, err := os.Stat(target); err == nil && info.IsDir() {
		target = filepath.Join(target, defaultName)
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, "", func() {}, err
	}
	return f, target, func() { _ = f.Close() }, nil
}

func (a app) receive(ctx context.Context, args []string) int {
	opts, err := parseReceiveArgs(args)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	client, credential, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	credential, err = ensureDeviceKey(ctx, client, credential)
	if err != nil {
		return a.fail("ensure device key", err)
	}
	for {
		received, err := receiveInboxOnce(ctx, client, credential, opts.output, a.stdout)
		if err != nil {
			return a.fail("receive", err)
		}
		if !opts.watch {
			if received == 0 {
				fmt.Fprintln(a.stdout, "No new device shares")
				if pending, err := client.ListPendingInbox(ctx); err == nil && len(pending.Shares) > 0 {
					fmt.Fprintf(a.stdout, "%d file(s) awaiting your approval — run '%s incoming'\n", len(pending.Shares), commandName)
				}
			}
			return 0
		}
		a.sleep(5 * time.Second)
	}
}

// reseal re-seals in-flight end-to-end shares whose recipient re-keyed since the send, using
// the content keys retained locally at send time (Option B, docs/design/teammate-phase-c.md).
func (a app) reseal(ctx context.Context, _ []string) int {
	client, credential, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	if clicore.IsAPIToken(credential.Token) {
		fmt.Fprintln(a.stderr, "re-sealing needs an interactive login (a personal API token can't re-seal end-to-end shares)")
		return 1
	}
	n, err := runResealOnce(ctx, client, a.stdout, a.stderr, true)
	if err != nil {
		return a.fail("reseal", err)
	}
	if n == 0 {
		fmt.Fprintln(a.stdout, "Nothing to re-seal.")
	}
	return 0
}

// runResealOnce processes the sender's re-seal queue once: for each flagged share, load the
// retained content key, seal it to the recipient's new device public key, and submit it.
// Returns the number of shares re-sealed. When verbose is false (e.g. the opportunistic pass
// on login) it stays quiet except for successful re-seals.
func runResealOnce(ctx context.Context, client *clicore.Client, stdout, stderr io.Writer, verbose bool) (int, error) {
	queue, err := client.ResealQueue(ctx)
	if err != nil {
		return 0, err
	}
	if len(queue.Requests) == 0 {
		return 0, nil
	}
	store, err := clicore.LoadPendingReseal()
	if err != nil {
		return 0, err
	}
	resealed := 0
	for _, req := range queue.Requests {
		entry, ok := store[req.ShareID]
		if !ok {
			if verbose {
				fmt.Fprintf(stderr, "can't re-seal %s for %s: content key not retained on this machine (was it sent from another device?)\n", req.ShareID, req.RecipientEmail)
			}
			continue
		}
		key, err := clicore.DecodeKey(entry.ContentKey)
		if err != nil {
			if verbose {
				fmt.Fprintf(stderr, "can't re-seal %s: retained key is unreadable: %v\n", req.ShareID, err)
			}
			continue
		}
		sealed, err := clicore.SealContentKeyForDevice(key, req.PublicKey)
		if err != nil {
			if verbose {
				fmt.Fprintf(stderr, "can't re-seal %s: %v\n", req.ShareID, err)
			}
			continue
		}
		if err := client.SubmitReseal(ctx, req.ShareID, req.TargetSessionID, sealed); err != nil {
			if verbose {
				fmt.Fprintf(stderr, "re-seal %s failed: %v\n", req.ShareID, err)
			}
			continue
		}
		resealed++
		fmt.Fprintf(stdout, "Re-sealed %s for %s\n", req.ShareID, req.RecipientEmail)
	}
	return resealed, nil
}

type streamOptions struct {
	path  string
	relay string
	turns []string
}

func parseStreamArgs(args []string) (streamOptions, error) {
	opts := streamOptions{relay: defaultRelayURL()}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--relay":
			i++
			if i >= len(args) {
				return streamOptions{}, errors.New("--relay requires a value")
			}
			opts.relay = args[i]
		case strings.HasPrefix(arg, "--relay="):
			opts.relay = strings.TrimPrefix(arg, "--relay=")
		case arg == "--turn":
			i++
			if i >= len(args) {
				return streamOptions{}, errors.New("--turn requires a value")
			}
			opts.turns = append(opts.turns, args[i])
		case strings.HasPrefix(arg, "--turn="):
			opts.turns = append(opts.turns, strings.TrimPrefix(arg, "--turn="))
		case strings.HasPrefix(arg, "-"):
			return streamOptions{}, fmt.Errorf("unknown flag: %s", arg)
		default:
			if opts.path != "" {
				return streamOptions{}, errors.New("stream accepts exactly one file")
			}
			opts.path = arg
		}
	}
	if opts.path == "" {
		return streamOptions{}, fmt.Errorf("usage: %s stream <file> [--relay URL] [--turn URL]", commandName)
	}
	return opts, nil
}

type receiveOptions struct {
	output string
	watch  bool
}

func parseReceiveArgs(args []string) (receiveOptions, error) {
	var opts receiveOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--watch" || arg == "-w":
			opts.watch = true
		case arg == "--out":
			i++
			if i >= len(args) {
				return receiveOptions{}, errors.New("--out requires a value")
			}
			opts.output = args[i]
		case strings.HasPrefix(arg, "--out="):
			opts.output = strings.TrimPrefix(arg, "--out=")
		case strings.HasPrefix(arg, "-"):
			return receiveOptions{}, fmt.Errorf("unknown flag: %s", arg)
		default:
			return receiveOptions{}, fmt.Errorf("unexpected argument: %s", arg)
		}
	}
	return opts, nil
}

type p2pRecvOptions struct {
	code   string
	output string
	relay  string
	turns  []string
}

func parseP2PRecvArgs(args []string) (p2pRecvOptions, error) {
	opts := p2pRecvOptions{relay: defaultRelayURL()}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--out" || arg == "-o":
			i++
			if i >= len(args) {
				return p2pRecvOptions{}, errors.New("--out requires a value")
			}
			opts.output = args[i]
		case strings.HasPrefix(arg, "--out="):
			opts.output = strings.TrimPrefix(arg, "--out=")
		case arg == "--relay":
			i++
			if i >= len(args) {
				return p2pRecvOptions{}, errors.New("--relay requires a value")
			}
			opts.relay = args[i]
		case strings.HasPrefix(arg, "--relay="):
			opts.relay = strings.TrimPrefix(arg, "--relay=")
		case arg == "--turn":
			i++
			if i >= len(args) {
				return p2pRecvOptions{}, errors.New("--turn requires a value")
			}
			opts.turns = append(opts.turns, args[i])
		case strings.HasPrefix(arg, "--turn="):
			opts.turns = append(opts.turns, strings.TrimPrefix(arg, "--turn="))
		case strings.HasPrefix(arg, "-") && arg != "-":
			return p2pRecvOptions{}, fmt.Errorf("unknown flag: %s", arg)
		default:
			if opts.code != "" {
				return p2pRecvOptions{}, errors.New("p2p recv accepts exactly one pairing code")
			}
			opts.code = arg
		}
	}
	if opts.code == "" {
		return p2pRecvOptions{}, fmt.Errorf("usage: %s p2p recv <code> [--out PATH] [--relay URL] [--turn URL]", commandName)
	}
	return opts, nil
}

func ensureDeviceKey(ctx context.Context, client *clicore.Client, credential clicore.Credential) (clicore.Credential, error) {
	if clicore.IsAPIToken(credential.Token) {
		return clicore.Credential{}, fmt.Errorf("device end-to-end features need an interactive login; a personal API token (%s) can't send or receive device-to-device encrypted files", clicore.APITokenEnv)
	}
	if strings.TrimSpace(credential.DevicePublicKey) != "" && strings.TrimSpace(credential.DevicePrivateKey) != "" {
		return credential, nil
	}
	keyPair, err := clicore.NewDeviceKeyPair()
	if err != nil {
		return clicore.Credential{}, err
	}
	if err := client.RegisterDeviceKey(ctx, keyPair.PublicKey); err != nil {
		return clicore.Credential{}, err
	}
	credential.DevicePublicKey = keyPair.PublicKey
	credential.DevicePrivateKey = keyPair.PrivateKey
	if err := clicore.SaveCredential(credential); err != nil {
		return clicore.Credential{}, err
	}
	return credential, nil
}

func receiveInboxOnce(ctx context.Context, client *clicore.Client, credential clicore.Credential, output string, stdout io.Writer) (int, error) {
	inbox, err := client.Inbox(ctx)
	if err != nil {
		return 0, err
	}
	received, err := loadReceivedInbox()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, share := range inbox.Shares {
		if strings.TrimSpace(share.PublicID) == "" || strings.TrimSpace(share.SealedKey) == "" {
			continue
		}
		if _, ok := received[share.PublicID]; ok {
			continue
		}
		contentKey, err := clicore.OpenSealedContentKey(share.SealedKey, credential.DevicePublicKey, credential.DevicePrivateKey)
		if err != nil {
			// sealed to a different device key (e.g. this device re-keyed after the
			// send) - skip it so other shares still come through; ask for a resend.
			fmt.Fprintf(stdout, "skipping %s: can't decrypt on this device (key changed since it was sent — ask the sender to resend)\n", share.PublicID)
			continue
		}
		var encrypted bytes.Buffer
		if err := client.DownloadInboxContent(ctx, share.PublicID, &encrypted); err != nil {
			return count, err
		}
		outPath, err := inboxOutputPath(share, output)
		if err != nil {
			return count, err
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0o700); err != nil {
			return count, err
		}
		// Stage via a random-named O_CREATE|O_EXCL file (os.CreateTemp, mode 0600)
		// so a symlink pre-planted at the predictable ".tmp" path cannot redirect
		// the decrypted write.
		out, err := os.CreateTemp(filepath.Dir(outPath), "."+filepath.Base(outPath)+".tmp-*")
		if err != nil {
			return count, err
		}
		tmp := out.Name()
		decryptErr := clicore.DecryptStream(out, bytes.NewReader(encrypted.Bytes()), contentKey)
		closeErr := out.Close()
		if decryptErr != nil {
			os.Remove(tmp)
			fmt.Fprintf(stdout, "skipping %s: decryption failed (corrupt or tampered payload)\n", share.PublicID)
			continue
		}
		if closeErr != nil {
			os.Remove(tmp)
			return count, closeErr
		}
		if err := os.Rename(tmp, outPath); err != nil {
			os.Remove(tmp)
			return count, err
		}
		received[share.PublicID] = receivedInboxEntry{Path: outPath, ReceivedAt: time.Now().UTC()}
		count++
		fmt.Fprintf(stdout, "Received %s -> %s\n", share.FileName, outPath)
		// Acknowledge delivery so the server stops flagging this share for re-seal and the
		// sender can prune its retained content key (Option B). Best-effort.
		if err := client.AckInboxShare(ctx, share.PublicID); err != nil {
			fmt.Fprintf(stdout, "note: could not acknowledge %s (harmless): %v\n", share.PublicID, err)
		}
	}
	if count > 0 {
		if err := saveReceivedInbox(received); err != nil {
			return count, err
		}
	}
	return count, nil
}

type receivedInboxEntry struct {
	Path       string    `json:"path"`
	ReceivedAt time.Time `json:"received_at"`
}

type receivedInboxRegistry map[string]receivedInboxEntry

func inboxOutputPath(share clicore.InboxShare, output string) (string, error) {
	name := filepath.Base(strings.TrimSpace(share.FileName))
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = share.PublicID
	}
	if output == "" {
		return filepath.Abs(name)
	}
	info, err := os.Stat(output)
	if err == nil && info.IsDir() {
		return filepath.Abs(filepath.Join(output, name))
	}
	if errors.Is(err, os.ErrNotExist) && strings.HasSuffix(output, string(filepath.Separator)) {
		return filepath.Abs(filepath.Join(output, name))
	}
	return filepath.Abs(output)
}

func receivedInboxPath() (string, error) {
	base, err := clicore.CacheBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "received_inbox.json"), nil
}

func loadReceivedInbox() (receivedInboxRegistry, error) {
	path, err := receivedInboxPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return receivedInboxRegistry{}, nil
		}
		return nil, err
	}
	var registry receivedInboxRegistry
	if err := json.Unmarshal(raw, &registry); err != nil {
		return nil, err
	}
	if registry == nil {
		registry = receivedInboxRegistry{}
	}
	return registry, nil
}

func saveReceivedInbox(registry receivedInboxRegistry) error {
	path, err := receivedInboxPath()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

// defaultRelayURL returns an explicit relay override, or "" meaning "use whatever
// relay the API hands back with the room authorization". Not hard-coding a relay
// here is what lets staging and prod each point at their own without a rebuild.
func defaultRelayURL() string {
	return strings.TrimSpace(os.Getenv("SHARE2US_RELAY_URL"))
}

// authorizeP2PRoom asks the API to authorize this (room, role) and builds the relay
// config from the response. The API is the ONLY real gate on the p2p_streaming_enabled
// entitlement — the relay just verifies the signature on the token minted there.
//
// Only the PUBLIC room half of the pairing code is sent; the secret half never
// leaves this machine, which is what keeps the relay unable to MITM the transfer.
func (a app) authorizeP2PRoom(ctx context.Context, client *clicore.Client, room, secret, role, relayOverride string, turns []string) (p2p.RelayConfig, int) {
	grant, err := client.AuthorizeP2PRoom(ctx, room, role)
	if err != nil {
		var apiErr *clicore.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.Code {
			case "p2p_streaming_not_allowed":
				fmt.Fprintln(a.stderr, "direct peer-to-peer streaming isn't available on your plan — upgrade to Pro to use it.")
				return p2p.RelayConfig{}, 1
			case "p2p_disabled", "p2p_unavailable":
				fmt.Fprintln(a.stderr, "peer-to-peer streaming isn't enabled on this server yet.")
				return p2p.RelayConfig{}, 1
			case "rate_limited":
				fmt.Fprintln(a.stderr, "too many peer-to-peer rooms right now; try again shortly.")
				return p2p.RelayConfig{}, 1
			}
		}
		return p2p.RelayConfig{}, a.fail("authorize p2p room", err)
	}

	relay := strings.TrimSpace(relayOverride)
	if relay == "" {
		relay = strings.TrimSpace(grant.RelayURL)
	}
	if relay == "" {
		fmt.Fprintln(a.stderr, "no relay is configured; set SHARE2US_RELAY_URL or pass --relay")
		return p2p.RelayConfig{}, 1
	}

	ice := make([]p2p.ICEServer, 0, len(grant.ICEServers))
	for _, s := range grant.ICEServers {
		ice = append(ice, p2p.ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential})
	}
	return p2p.RelayConfig{
		SignalingURL: relay,
		TURNServers:  turns,
		ICEServers:   ice,
		PairingCode:  room,
		Secret:       secret,
		RoomToken:    grant.Token,
	}, 0
}

// pairingAlphabet is a Crockford-ish set (no 0/O/1/I) for readable codes. Its
// length (32) divides 256, so a byte % len is unbiased.
const pairingAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// newPairingCode returns (fullCode, room, secret). The full code is what the
// user shares out of band; the receiver types it back. Only the room half is
// ever sent to the relay; the secret half (never sent) anchors SAS verification.
func newPairingCode() (code, room, secret string, err error) {
	if room, err = randomToken(8); err != nil {
		return "", "", "", err
	}
	if secret, err = randomToken(12); err != nil {
		return "", "", "", err
	}
	return room + "-" + secret, room, secret, nil
}

func randomToken(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, v := range raw {
		out[i] = pairingAlphabet[int(v)%len(pairingAlphabet)]
	}
	return string(out), nil
}

// splitPairingCode parses a full pairing code back into its room and secret
// halves. Whitespace-tolerant and case-insensitive (codes are uppercase).
func splitPairingCode(code string) (room, secret string, ok bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	room, secret, found := strings.Cut(code, "-")
	if !found || room == "" || secret == "" {
		return "", "", false
	}
	return room, secret, true
}

func (a app) authClient() (*clicore.Client, clicore.Credential, bool) {
	// A personal access token in the environment (CI/automation) takes
	// precedence over the on-disk device login. It carries no device identity,
	// so device end-to-end commands guard against it separately.
	if pat := clicore.EnvAPIToken(); pat != "" {
		apiBase, _, err := resolveAPIBase()
		if err != nil {
			return nil, clicore.Credential{}, false
		}
		return clicore.NewClient(apiBase, pat), clicore.Credential{APIBase: apiBase, Token: pat}, true
	}
	credential, err := clicore.LoadCredential()
	if err != nil || credential.Token == "" {
		return nil, clicore.Credential{}, false
	}
	apiBase := credential.APIBase
	if apiBase == "" {
		resolved, _, err := resolveAPIBase()
		if err != nil {
			return nil, clicore.Credential{}, false
		}
		apiBase = resolved
	}
	return clicore.NewClient(apiBase, credential.Token), credential, true
}

func resolveDeviceAlias(ctx context.Context, client *clicore.Client, alias string) (clicore.DeviceSession, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return clicore.DeviceSession{}, errors.New("device alias is required")
	}
	devices, err := client.ListDevices(ctx)
	if err != nil {
		return clicore.DeviceSession{}, err
	}
	var matches []clicore.DeviceSession
	for _, device := range devices.Sessions {
		if device.ID == alias || strings.EqualFold(device.DeviceName, alias) {
			matches = append(matches, device)
		}
	}
	if len(matches) == 0 {
		return clicore.DeviceSession{}, fmt.Errorf("unknown device %q", alias)
	}
	if len(matches) > 1 {
		return clicore.DeviceSession{}, fmt.Errorf("ambiguous device %q; use the session id from `%s devices`", alias, commandName)
	}
	return matches[0], nil
}

func saveAPIHost(host string) error {
	config, err := clicore.LoadConfig()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		config = clicore.Config{}
	}
	config.Host = host
	return clicore.SaveConfig(config)
}

func saveBaseURL(baseURL string) error {
	config, err := clicore.LoadConfig()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		config = clicore.Config{}
	}
	config.BaseURL = baseURL
	config.Host = ""
	config.ShareBase = ""
	return clicore.SaveConfig(config)
}

func (a app) downloadAPIBase() (string, error) {
	credential, err := clicore.LoadCredential()
	if err == nil && strings.TrimSpace(credential.APIBase) != "" {
		return clicore.NormalizeAPIHost(credential.APIBase)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	resolved, _, err := resolveAPIBase()
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func isTerminalWriter(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func isTerminalReader(r io.Reader) bool {
	file, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func (a app) fail(action string, err error) int {
	fmt.Fprintf(a.stderr, "%s: %s\n", action, cliErrorMessage(err))
	return 1
}

func cliErrorMessage(err error) string {
	var apiErr *clicore.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "rate_limited", "cloudflare_challenge":
			if strings.TrimSpace(apiErr.Message) != "" {
				return strings.TrimSpace(apiErr.Message)
			}
		}
	}
	return err.Error()
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func contentTypeForName(name string) string {
	if contentType := mime.TypeByExtension(filepath.Ext(name)); contentType != "" {
		return contentType
	}
	return "application/octet-stream"
}

func directoryZipName(path string) string {
	name := filepath.Base(filepath.Clean(path))
	if name == "" || name == "." || name == string(os.PathSeparator) {
		return "folder.zip"
	}
	return name + ".zip"
}

func zipDirectory(root string) (string, error) {
	root = filepath.Clean(root)
	tmp, err := os.CreateTemp("", "share2us-folder-*.zip")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	zw := zip.NewWriter(tmp)
	cleanup := func(closeErr error) (string, error) {
		_ = zw.Close()
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", closeErr
	}

	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if entry.IsDir() {
			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			header.Name = strings.TrimRight(name, "/") + "/"
			_, err = zw.CreateHeader(header)
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = name
		header.Method = zip.Deflate
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			writer, err := zw.CreateHeader(header)
			if err != nil {
				return err
			}
			_, err = writer.Write([]byte(target))
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		writer, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(writer, file)
		return err
	})
	if err != nil {
		return cleanup(err)
	}
	if err := zw.Close(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return tmpPath, nil
}

func resolveAPIBase() (string, string, error) {
	return clicore.ResolveAPIBase()
}

func writeJSON(w io.Writer, value any) {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
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

func truncateLeft(value string, width int) string {
	if len(value) <= width {
		return value
	}
	if width <= 3 {
		return value[len(value)-width:]
	}
	return "..." + value[len(value)-(width-3):]
}
