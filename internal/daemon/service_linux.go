//go:build linux

package daemon

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const unitName = "s2u-daemon.service"

// unitPath returns the per-user systemd unit path
// ($XDG_CONFIG_HOME/systemd/user/s2u-daemon.service, default ~/.config/...).
func unitPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "systemd", "user", unitName), nil
}

// renderUnit builds the systemd --user unit. It is hardened for a headless
// network service (ADR-035): no new privileges, a strict read-only view of the
// system, home read-only except the share2us state/runtime dirs, and only the
// address families the LAN receiver and control socket need. destDir, when set
// and outside the default state dirs, is added to ReadWritePaths so received
// files can be written.
func renderUnit(exePath, destDir string) string {
	rwPaths := "%h/.config/share2us %h/.cache/share2us %t/share2us"
	if d := strings.TrimSpace(destDir); d != "" {
		rwPaths += " " + d
	}
	return fmt.Sprintf(`[Unit]
Description=Share2Us background receiver (s2u daemon)
Documentation=https://share2.us
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s daemon run
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=%s
ProtectControlGroups=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX

[Install]
WantedBy=default.target
`, exePath, rwPaths)
}

// ServiceSupported reports that per-OS service integration exists here.
func ServiceSupported() bool { return true }

// ServiceInstall writes the user unit and enables+starts it. exePath is the
// share2us binary to run; destDir (may be "") is added to ReadWritePaths.
func ServiceInstall(exePath, destDir string, out io.Writer) error {
	path, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(renderUnit(exePath, destDir)), 0o644); err != nil {
		return err
	}
	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "--user", "enable", "--now", unitName); err != nil {
		return err
	}
	fmt.Fprintf(out, "Installed and started %s\n", unitName)
	fmt.Fprintf(out, "Logs: share2us daemon logs   Stop: share2us daemon stop\n")
	// A --user unit only starts at login unless lingering is enabled. Offer it;
	// do not enable it silently (it means a network service runs with nobody
	// logged in).
	if !lingerEnabled() {
		fmt.Fprintf(out, "\nTo keep it running after you log out (and start it at boot), enable lingering:\n  sudo loginctl enable-linger %s\n", currentUser())
	}
	return nil
}

// ServiceUninstall disables+stops the unit and removes it.
func ServiceUninstall(out io.Writer) error {
	path, err := unitPath()
	if err != nil {
		return err
	}
	_ = run("systemctl", "--user", "disable", "--now", unitName)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = run("systemctl", "--user", "daemon-reload")
	fmt.Fprintf(out, "Removed %s\n", unitName)
	return nil
}

// ServiceStart starts the installed unit.
func ServiceStart() error { return run("systemctl", "--user", "start", unitName) }

// ServiceStop stops the installed unit.
func ServiceStop() error { return run("systemctl", "--user", "stop", unitName) }

// ServiceLogs tails the unit's journal, inheriting stdio.
func ServiceLogs(follow bool) error {
	args := []string{"--user", "-u", unitName, "-n", "50"}
	if follow {
		args = append(args, "-f")
	}
	cmd := exec.Command("journalctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, msg)
		}
		return fmt.Errorf("%s %s: %v", name, strings.Join(args, " "), err)
	}
	return nil
}

func lingerEnabled() bool {
	// loginctl show-user prints "Linger=yes" when enabled.
	out, err := exec.Command("loginctl", "show-user", currentUser(), "--property=Linger").CombinedOutput()
	return err == nil && strings.Contains(string(out), "Linger=yes")
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "$USER"
}
