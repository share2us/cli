package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli-core/lanshare"
)

// localShareMode inspects a default (file-verb) invocation and reports whether it
// is really an offline local-share command. Empty string => fall through to a
// normal cloud upload. These are all account-free (no login).
func localShareMode(args []string) string {
	hasServe, hasReceive, hasDest := false, false, false
	for _, a := range args {
		switch {
		case a == "--serve":
			hasServe = true
		case a == "--receive" || a == "-r":
			hasReceive = true
		case a == "--dest" || strings.HasPrefix(a, "--dest="):
			hasDest = true
		}
	}
	switch {
	case hasServe:
		return "serve"
	case hasReceive:
		return "receive"
	case hasDest:
		return "send"
	default:
		return ""
	}
}

// ---- receive ----

type lanReceiveOpts struct {
	name       string
	password   string
	prompt     bool
	noPassword bool
	port       int
	portSet    bool
	allowIPs   []string
	path       string
	overwrite  bool
	bind       string
	qr         bool
}

func (a app) lanReceive(ctx context.Context, args []string) int {
	opts, err := parseLanReceiveArgs(args)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	if opts.password == "" && opts.prompt {
		pw, perr := a.readPassword("Receive password: ", a.stderr)
		if perr != nil {
			fmt.Fprintln(a.stderr, perr)
			return 1
		}
		opts.password = pw
	}
	if opts.noPassword && opts.password == "" && len(opts.allowIPs) == 0 {
		fmt.Fprintln(a.stderr, "WARNING: --no-password means any device that can reach this port may send you a file. Prefer a password or --allow-ip.")
	}

	trustedIPs := loadLocalConfig().TrustedIPs()
	printed := false
	var mdnsCloser io.Closer
	defer func() {
		if mdnsCloser != nil {
			_ = mdnsCloser.Close()
		}
	}()
	ropts := lanshare.ReceiveOptions{
		Bind:       opts.bind,
		Port:       opts.port,
		Password:   opts.password,
		NoPassword: opts.noPassword,
		AllowIPs:   opts.allowIPs,
		TrustedIPs: trustedIPs,
		DestDir:    opts.path,
		Overwrite:  opts.overwrite,
		OnListen: func(info lanshare.ListenInfo) {
			printed = true
			a.printReceiveBanner(info, opts)
			// Advertise on the LAN so a sender can find us by name.
			instance := opts.name
			if instance == "" {
				instance, _ = os.Hostname()
			}
			if c, err := lanshare.Advertise(instance, info); err == nil {
				mdnsCloser = c
				fmt.Fprintf(a.stderr, "Discoverable as: %s  (s2u <file> --dest=%s)\n", instance, instance)
			}
		},
	}
	prog := newProgressPrinter(a.stderr, "receiving")
	ropts.OnProgress = prog.update

	res, err := lanshare.Receive(ctx, ropts)
	prog.finish()
	if err != nil {
		if !printed {
			fmt.Fprintln(a.stderr, err)
			return 1
		}
		fmt.Fprintf(a.stderr, "receive failed: %v\n", err)
		return 1
	}
	from := res.PeerIP
	if from == "" {
		from = "peer"
	}
	fmt.Fprintf(a.stdout, "Received %s (%s) from %s -> %s\n",
		res.Name, humanBytes(res.Bytes), from, res.Path)
	return 0
}

func (a app) printReceiveBanner(info lanshare.ListenInfo, opts lanReceiveOpts) {
	ip := opts.bind
	if ip == "" || ip == "0.0.0.0" || ip == "::" {
		ip = primaryLANIP()
	}
	fmt.Fprintf(a.stderr, "S2U receiving on %s:%d  (mode: %s)\n", ip, info.Port, info.Mode)
	dest := ip
	sender := fmt.Sprintf("s2u <file> --dest=%s", dest)
	switch info.Mode {
	case lanshare.ModePassword:
		fmt.Fprintf(a.stderr, "Passphrase: %s\n", info.Passphrase)
		sender += fmt.Sprintf(" --password=%q", info.Passphrase)
	case lanshare.ModeAllowIP:
		fmt.Fprintf(a.stderr, "Only accepting from: %s\n", strings.Join(opts.allowIPs, ", "))
	}
	fmt.Fprintf(a.stderr, "On the sender:  %s\n", sender)

	// A pairing string bundles address + fingerprint (+ passphrase); one paste on
	// the sender's --dest is all that's needed and it pins the receiver identity.
	pairing := lanshare.BuildPairingString(ip, info)
	fmt.Fprintf(a.stderr, "Or paste this pairing string into --dest:\n  %s\n", pairing)
	if opts.qr {
		if art, err := clicore.RenderQR(pairing); err == nil {
			fmt.Fprintln(a.stderr, art)
		}
	}
	fmt.Fprintln(a.stderr, "Waiting for a sender... (Ctrl-C to cancel)")
}

func parseLanReceiveArgs(args []string) (lanReceiveOpts, error) {
	var o lanReceiveOpts
	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func() (string, bool) {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				return args[i], true
			}
			return "", false
		}
		switch {
		case arg == "--receive" || arg == "-r":
			// mode marker, no value
		case arg == "--no-password" || arg == "-np":
			o.noPassword = true
		case arg == "--overwrite":
			o.overwrite = true
		case arg == "--qr" || arg == "--qrl":
			o.qr = true
		case arg == "--password" || arg == "-p":
			if v, ok := next(); ok {
				o.password = v
			} else {
				o.prompt = true
			}
		case strings.HasPrefix(arg, "--password="):
			o.password = strings.TrimPrefix(arg, "--password=")
		case arg == "--port" || arg == "-P":
			v, ok := next()
			if !ok {
				return o, fmt.Errorf("%s needs a port number", arg)
			}
			if err := o.setPort(v); err != nil {
				return o, err
			}
		case strings.HasPrefix(arg, "--port="):
			if err := o.setPort(strings.TrimPrefix(arg, "--port=")); err != nil {
				return o, err
			}
		case arg == "--allow-ip" || arg == "--src" || arg == "-a":
			v, ok := next()
			if !ok {
				return o, fmt.Errorf("%s needs an IP address", arg)
			}
			o.allowIPs = append(o.allowIPs, splitList(v)...)
		case strings.HasPrefix(arg, "--allow-ip="):
			o.allowIPs = append(o.allowIPs, splitList(strings.TrimPrefix(arg, "--allow-ip="))...)
		case strings.HasPrefix(arg, "--src="):
			o.allowIPs = append(o.allowIPs, splitList(strings.TrimPrefix(arg, "--src="))...)
		case arg == "--path":
			v, ok := next()
			if !ok {
				return o, fmt.Errorf("--path needs a directory")
			}
			o.path = v
		case strings.HasPrefix(arg, "--path="):
			o.path = strings.TrimPrefix(arg, "--path=")
		case arg == "--bind":
			v, ok := next()
			if !ok {
				return o, fmt.Errorf("--bind needs an address")
			}
			o.bind = v
		case strings.HasPrefix(arg, "--bind="):
			o.bind = strings.TrimPrefix(arg, "--bind=")
		case strings.HasPrefix(arg, "-"):
			return o, fmt.Errorf("unknown flag: %s", arg)
		default:
			if o.name != "" {
				return o, fmt.Errorf("receive accepts at most one name label")
			}
			o.name = arg
		}
	}
	if o.password != "" && o.noPassword {
		return o, fmt.Errorf("--no-password cannot be combined with --password")
	}
	return o, nil
}

func (o *lanReceiveOpts) setPort(v string) error {
	p, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("invalid port %q", v)
	}
	o.port = p
	o.portSet = true
	return nil
}

// ---- send ----

type lanSendOpts struct {
	path     string
	dest     string
	password string
	prompt   bool
	pin      string
}

func (a app) lanSend(ctx context.Context, args []string) int {
	opts, err := parseLanSendArgs(args)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	if opts.path == "" {
		fmt.Fprintln(a.stderr, "send needs a file or folder: s2u <path> --dest=<ip>")
		return 2
	}
	if opts.dest == "" {
		fmt.Fprintln(a.stderr, "send needs --dest=<ip|alias|pairing-string>")
		return 2
	}
	// A bare name resolves via a saved alias first, then LAN mDNS discovery.
	if !lanshare.IsPairingString(opts.dest) && !looksLikeAddr(opts.dest) {
		if addr, ok := loadLocalConfig().ResolveDeviceAlias(opts.dest); ok {
			opts.dest = addr
		} else {
			fmt.Fprintf(a.stderr, "Looking for %q on the local network...\n", opts.dest)
			pi, derr := lanshare.Discover(ctx, opts.dest, 4*time.Second)
			if derr != nil {
				fmt.Fprintf(a.stderr, "%v (not an IP, a saved alias, or discoverable). Use --dest=<ip> or a pairing string.\n", derr)
				return 1
			}
			opts.dest = pi.Addr()
			if opts.pin == "" {
				opts.pin = pi.Fingerprint
			}
		}
	}
	// A pasted/scanned pairing string carries the address, fingerprint, and (in
	// password mode) the passphrase — expand it into the discrete fields.
	if lanshare.IsPairingString(opts.dest) {
		pi, perr := lanshare.ParsePairingString(opts.dest)
		if perr != nil {
			fmt.Fprintln(a.stderr, perr)
			return 2
		}
		opts.dest = pi.Addr()
		if opts.pin == "" {
			opts.pin = pi.Fingerprint
		}
		if opts.password == "" && !opts.prompt {
			opts.password = pi.Password
		}
	}
	if opts.password == "" && opts.prompt {
		pw, perr := a.readPassword("Send password: ", a.stderr)
		if perr != nil {
			fmt.Fprintln(a.stderr, perr)
			return 1
		}
		opts.password = pw
	}

	info, statErr := os.Stat(opts.path)
	if statErr != nil {
		fmt.Fprintf(a.stderr, "cannot read %s: %v\n", opts.path, statErr)
		return 1
	}

	var (
		body    *os.File
		name    string
		size    int64
		isDir   bool
		cleanup func()
	)
	if info.IsDir() {
		zipPath, zerr := zipDirectory(opts.path)
		if zerr != nil {
			fmt.Fprintf(a.stderr, "zip folder: %v\n", zerr)
			return 1
		}
		cleanup = func() { _ = os.Remove(zipPath) }
		f, oerr := os.Open(zipPath)
		if oerr != nil {
			cleanup()
			fmt.Fprintf(a.stderr, "open zip: %v\n", oerr)
			return 1
		}
		body = f
		name = directoryZipName(opts.path)
		if zi, e := f.Stat(); e == nil {
			size = zi.Size()
		}
		isDir = true
	} else {
		f, oerr := os.Open(opts.path)
		if oerr != nil {
			fmt.Fprintf(a.stderr, "open %s: %v\n", opts.path, oerr)
			return 1
		}
		body = f
		name = filepath.Base(opts.path)
		size = info.Size()
	}
	defer body.Close()
	if cleanup != nil {
		defer cleanup()
	}

	prog := newProgressPrinter(a.stderr, "sending")
	fmt.Fprintf(a.stderr, "Sending %s (%s) to %s...\n", name, humanBytes(size), opts.dest)
	_, err = lanshare.Send(ctx, name, size, isDir, body, lanshare.SendOptions{
		Dest:           opts.dest,
		Password:       opts.password,
		PinFingerprint: opts.pin,
		OnProgress:     prog.update,
	})
	prog.finish()
	if err != nil {
		fmt.Fprintf(a.stderr, "send failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(a.stdout, "Sent %s (%s) to %s\n", name, humanBytes(size), opts.dest)
	return 0
}

func parseLanSendArgs(args []string) (lanSendOpts, error) {
	var o lanSendOpts
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
		case arg == "--dest":
			v, ok := next()
			if !ok {
				return o, fmt.Errorf("--dest needs an ip or alias")
			}
			o.dest = v
		case strings.HasPrefix(arg, "--dest="):
			o.dest = strings.TrimPrefix(arg, "--dest=")
		case arg == "--password" || arg == "-p":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				o.password = args[i]
			} else {
				o.prompt = true
			}
		case strings.HasPrefix(arg, "--password="):
			o.password = strings.TrimPrefix(arg, "--password=")
		case strings.HasPrefix(arg, "-"):
			return o, fmt.Errorf("unknown flag: %s", arg)
		default:
			if o.path != "" {
				return o, fmt.Errorf("send accepts exactly one file or folder")
			}
			o.path = arg
		}
	}
	return o, nil
}

// ---- serve ----

type lanServeOpts struct {
	path string
	bind string
	port int
	qr   bool
}

const (
	servePortLo = 12000
	servePortHi = 17000
)

func (a app) lanServe(ctx context.Context, args []string) int {
	opts, err := parseLanServeArgs(args)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 2
	}
	if opts.path == "" {
		opts.path = "."
	}
	abs, err := filepath.Abs(opts.path)
	if err != nil {
		fmt.Fprintf(a.stderr, "resolve %s: %v\n", opts.path, err)
		return 1
	}
	info, err := os.Stat(abs)
	if err != nil {
		fmt.Fprintf(a.stderr, "cannot read %s: %v\n", opts.path, err)
		return 1
	}

	// `s2u --serve` publishes a directory over unauthenticated HTTP on the LAN,
	// so refuse to expose the home directory or known credential stores (~/.ssh,
	// ~/.aws, ~/.config, /etc, ...) even if the user points --serve there by
	// mistake. Serving a specific project subfolder is still fine.
	if serr := guardServePath(abs); serr != nil {
		fmt.Fprintln(a.stderr, serr)
		return 1
	}

	var handler http.Handler
	if info.IsDir() {
		// http.FileServer cleans paths and rejects traversal; index.html is
		// served automatically when present.
		handler = http.FileServer(http.Dir(abs))
	} else {
		name := filepath.Base(abs)
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" && strings.TrimPrefix(r.URL.Path, "/") != name {
				http.NotFound(w, r)
				return
			}
			http.ServeFile(w, r, abs)
		})
	}

	bind := opts.bind
	if bind == "" {
		bind = "0.0.0.0"
	}
	ln, port, err := serveListen(bind, opts.port)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 1
	}
	defer ln.Close()

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	a.printServeBanner(abs, info.IsDir(), bind, port, opts.qr)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		fmt.Fprintln(a.stderr, "\nserver stopped")
		return 0
	case serr := <-errCh:
		if serr != nil && serr != http.ErrServerClosed {
			fmt.Fprintf(a.stderr, "serve error: %v\n", serr)
			return 1
		}
		return 0
	}
}

// guardServePath refuses to serve a path that would expose secrets over the
// LAN: the home directory itself (or any ancestor of it, e.g. / or /home), and
// anything at or under a known credential store. Symlinks are resolved first so
// a link into a sensitive location cannot slip past. It intentionally allows a
// normal subfolder of home (e.g. ~/Downloads, ~/projects/site).
func guardServePath(abs string) error {
	real := filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		real = filepath.Clean(resolved)
	}

	home := ""
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		home = filepath.Clean(h)
	}

	// Serving home (or a directory that contains home, like / or /home) would
	// expose every dotfile and credential store under it.
	if home != "" && pathAtOrUnder(home, real) {
		return fmt.Errorf("refusing to serve %s: it is (or contains) your home directory, which holds credentials and dotfiles; point --serve at a specific subfolder instead", real)
	}

	for _, s := range sensitiveServeRoots(home) {
		if pathAtOrUnder(real, s) {
			return fmt.Errorf("refusing to serve %s: it is inside a sensitive location (%s); s2u will not expose credentials over the network", real, s)
		}
	}
	return nil
}

// sensitiveServeRoots lists credential stores and system directories that must
// never be published. Home-relative entries are skipped when home is unknown.
func sensitiveServeRoots(home string) []string {
	var roots []string
	if home != "" {
		for _, rel := range []string{
			".ssh", ".aws", ".gnupg", ".config", ".kube", ".docker", ".azure",
			".password-store", ".mozilla", ".local/share/keyrings",
			".netrc", ".git-credentials", ".pgpass", ".npmrc",
		} {
			roots = append(roots, filepath.Join(home, rel))
		}
	}
	for _, sys := range []string{"/etc", "/root", "/var", "/proc", "/sys", "/dev", "/boot"} {
		roots = append(roots, sys)
	}
	return roots
}

// pathAtOrUnder reports whether path is base or a descendant of base. Both are
// expected to be cleaned absolute paths; the separator check stops /etc from
// matching /etchings.
func pathAtOrUnder(path, base string) bool {
	if path == base {
		return true
	}
	sep := string(filepath.Separator)
	if base == sep { // filesystem root: every absolute path is under it
		return strings.HasPrefix(path, sep)
	}
	return strings.HasPrefix(path, base+sep)
}

func (a app) printServeBanner(abs string, isDir bool, bind string, port int, qr bool) {
	kind := "file"
	if isDir {
		kind = "directory"
	}
	fmt.Fprintf(a.stderr, "Serving %s %s over HTTP (Ctrl-C to stop)\n", kind, abs)
	primary := bind
	if bind == "0.0.0.0" || bind == "::" {
		primary = primaryLANIP()
		for _, ip := range allIPv4() {
			fmt.Fprintf(a.stderr, "  http://%s:%d/\n", ip, port)
		}
	} else {
		fmt.Fprintf(a.stderr, "  http://%s:%d/\n", bind, port)
	}
	url := fmt.Sprintf("http://%s:%d/", primary, port)
	if qr {
		if art, err := clicore.RenderQR(url); err == nil {
			fmt.Fprintln(a.stderr, art)
		}
	}
}

// serveListen binds the pinned port (hard error if taken) or a random free port
// in [servePortLo, servePortHi].
func serveListen(bind string, port int) (net.Listener, int, error) {
	if port != 0 {
		ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(port)))
		if err != nil {
			return nil, 0, fmt.Errorf("port %d unavailable: %v", port, err)
		}
		return ln, servePort(ln), nil
	}
	span := servePortHi - servePortLo + 1
	start := int(time.Now().UnixNano()%int64(span)) + servePortLo
	for i := 0; i < span; i++ {
		p := servePortLo + (start-servePortLo+i)%span
		ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(p)))
		if err == nil {
			return ln, servePort(ln), nil
		}
	}
	return nil, 0, fmt.Errorf("no free port in %d-%d", servePortLo, servePortHi)
}

func servePort(ln net.Listener) int {
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		return tcp.Port
	}
	return 0
}

func allIPv4() []string {
	var out []string
	addrs, _ := net.InterfaceAddrs()
	for _, addr := range addrs {
		if ipn, ok := addr.(*net.IPNet); ok && !ipn.IP.IsLoopback() {
			if v4 := ipn.IP.To4(); v4 != nil {
				out = append(out, v4.String())
			}
		}
	}
	if len(out) == 0 {
		out = append(out, "127.0.0.1")
	}
	return out
}

func parseLanServeArgs(args []string) (lanServeOpts, error) {
	var o lanServeOpts
	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func() (string, bool) {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				return args[i], true
			}
			return "", false
		}
		switch {
		case arg == "--serve":
			// mode marker
		case arg == "--qr" || arg == "--qrl":
			o.qr = true
		case arg == "--bind":
			v, ok := next()
			if !ok {
				return o, fmt.Errorf("--bind needs an address")
			}
			o.bind = v
		case strings.HasPrefix(arg, "--bind="):
			o.bind = strings.TrimPrefix(arg, "--bind=")
		case arg == "--port" || arg == "-P":
			v, ok := next()
			if !ok {
				return o, fmt.Errorf("%s needs a port number", arg)
			}
			p, err := strconv.Atoi(v)
			if err != nil || p < 1 || p > 65535 {
				return o, fmt.Errorf("invalid port %q", v)
			}
			o.port = p
		case strings.HasPrefix(arg, "--port="):
			p, err := strconv.Atoi(strings.TrimPrefix(arg, "--port="))
			if err != nil || p < 1 || p > 65535 {
				return o, fmt.Errorf("invalid --port value")
			}
			o.port = p
		case strings.HasPrefix(arg, "-"):
			return o, fmt.Errorf("unknown flag: %s", arg)
		default:
			if o.path != "" {
				return o, fmt.Errorf("serve accepts one file or directory")
			}
			o.path = arg
		}
	}
	return o, nil
}

// ---- config: device aliases + trusted peers ----

func loadLocalConfig() clicore.Config {
	cfg, err := clicore.LoadConfig()
	if err != nil {
		return clicore.Config{}
	}
	return cfg
}

func (a app) configSetDevice(args []string) int {
	if len(args) < 2 || args[0] != "device" {
		fmt.Fprintf(a.stderr, "usage: %s config set device alias <name> <ip|pairing>\n       %s config set device trusted <alias|ip>\n", commandName, commandName)
		return 2
	}
	cfg := loadLocalConfig()
	switch args[1] {
	case "alias":
		if len(args) != 4 {
			fmt.Fprintf(a.stderr, "usage: %s config set device alias <name> <ip|pairing>\n", commandName)
			return 2
		}
		cfg.SetDeviceAlias(args[2], args[3])
		if err := clicore.SaveConfig(cfg); err != nil {
			return a.fail("save config", err)
		}
		fmt.Fprintf(a.stdout, "device alias %q -> %s\n", args[2], args[3])
		return 0
	case "trusted":
		if len(args) != 3 {
			fmt.Fprintf(a.stderr, "usage: %s config set device trusted <alias|ip>\n", commandName)
			return 2
		}
		cfg.SetTrustedPeer(args[2])
		if err := clicore.SaveConfig(cfg); err != nil {
			return a.fail("save config", err)
		}
		fmt.Fprintf(a.stdout, "device %q trusted: inbound transfers from it are auto-accepted without a password.\n", args[2])
		fmt.Fprintln(a.stderr, "WARNING: trust is by IP and can be spoofed on an untrusted network. Untrust it with: "+commandName+" config delete device trusted "+args[2])
		return 0
	default:
		fmt.Fprintf(a.stderr, "unknown: config set device %s (want alias|trusted)\n", args[1])
		return 2
	}
}

func (a app) configDeleteDevice(args []string) int {
	if len(args) != 3 || args[0] != "device" {
		fmt.Fprintf(a.stderr, "usage: %s config delete device alias <name>\n       %s config delete device trusted <alias|ip>\n", commandName, commandName)
		return 2
	}
	cfg := loadLocalConfig()
	var removed bool
	switch args[1] {
	case "alias":
		removed = cfg.DeleteDeviceAlias(args[2])
	case "trusted":
		removed = cfg.DeleteTrustedPeer(args[2])
	default:
		fmt.Fprintf(a.stderr, "unknown: config delete device %s (want alias|trusted)\n", args[1])
		return 2
	}
	if !removed {
		fmt.Fprintf(a.stderr, "no %s %q found\n", args[1], args[2])
		return 1
	}
	if err := clicore.SaveConfig(cfg); err != nil {
		return a.fail("save config", err)
	}
	fmt.Fprintf(a.stdout, "removed %s %q\n", args[1], args[2])
	return 0
}

// ---- shared helpers ----

// hintLocalShareOnUnreachable prints a tip about offline direct sharing when a
// cloud upload fails because the server can't be reached — the exact case where
// a LAN/Tailscale/WireGuard direct transfer is the resilient alternative.
func (a app) hintLocalShareOnUnreachable(err error, path string) {
	if !isUnreachableError(err) {
		return
	}
	name := "<file>"
	if path != "" {
		name = path
	}
	fmt.Fprintf(a.stderr, "Tip: the server is unreachable. If your recipient is on the same LAN, Tailscale, or WireGuard network, share directly with no cloud:\n  s2u %s --dest=<their-ip>     (they run: s2u --receive)\n", name)
}

// isUnreachableError reports whether err looks like a network/connectivity
// failure to the API (vs. an application-level rejection).
func isUnreachableError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"connection refused", "no such host", "network is unreachable", "no route to host", "i/o timeout", "dial tcp", "server misbehaving", "timeout"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// looksLikeAddr reports whether dest is already an IP or host:port (vs a bare
// alias name like "work-laptop"), so alias resolution only runs on plain names.
func looksLikeAddr(dest string) bool {
	if net.ParseIP(dest) != nil {
		return true
	}
	if _, _, err := net.SplitHostPort(dest); err == nil {
		return true
	}
	return strings.Contains(dest, ".")
}

func splitList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// primaryLANIP returns the host's primary outbound IPv4 (via a UDP dial that
// sends no packets), falling back to the first non-loopback interface address.
func primaryLANIP() string {
	if conn, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		defer conn.Close()
		if ua, ok := conn.LocalAddr().(*net.UDPAddr); ok && ua.IP != nil {
			return ua.IP.String()
		}
	}
	addrs, _ := net.InterfaceAddrs()
	for _, addr := range addrs {
		if ipn, ok := addr.(*net.IPNet); ok && !ipn.IP.IsLoopback() {
			if v4 := ipn.IP.To4(); v4 != nil {
				return v4.String()
			}
		}
	}
	return "127.0.0.1"
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// progressPrinter renders a throttled single-line transfer progress bar to a
// writer (stderr). It is a no-op when the writer is not provided.
type progressPrinter struct {
	w        io.Writer
	label    string
	start    time.Time
	lastDraw time.Time
	drawn    bool
}

func newProgressPrinter(w io.Writer, label string) *progressPrinter {
	return &progressPrinter{w: w, label: label, start: time.Now()}
}

func (p *progressPrinter) update(done, total int64) {
	if p == nil || p.w == nil {
		return
	}
	now := time.Now()
	if p.drawn && now.Sub(p.lastDraw) < 100*time.Millisecond && done != total {
		return
	}
	p.lastDraw = now
	p.drawn = true
	elapsed := now.Sub(p.start).Seconds()
	var speed float64
	if elapsed > 0 {
		speed = float64(done) / elapsed
	}
	if total > 0 {
		pct := int(float64(done) / float64(total) * 100)
		fmt.Fprintf(p.w, "\r%s %s / %s (%d%%) %s/s      ",
			p.label, humanBytes(done), humanBytes(total), pct, humanBytes(int64(speed)))
	} else {
		fmt.Fprintf(p.w, "\r%s %s %s/s      ", p.label, humanBytes(done), humanBytes(int64(speed)))
	}
}

func (p *progressPrinter) finish() {
	if p != nil && p.w != nil && p.drawn {
		fmt.Fprintln(p.w)
	}
}
