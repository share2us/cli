// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

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
// validUnitPath rejects a path that cannot be written into a systemd unit
// safely. A newline would end the ReadWritePaths line and start a new
// DIRECTIVE, so a folder name could add ExecStartPre to a service that runs on
// every login. This is self-inflicted -- the value comes from the operator's own
// --dest flag -- but a unit file is not the place to find that out (§AJ low
// batch). The macOS plist already XML-escapes, so only systemd needed this.
func validUnitPath(p string) error {
	for _, r := range p {
		if r == '\n' || r == '\r' || r < 0x20 || r == 0x7f {
			return fmt.Errorf("daemon: the receive folder path contains a control character (%U) and cannot be written into a systemd unit", r)
		}
	}
	if strings.Contains(p, `"`) {
		return errors.New(`daemon: the receive folder path contains a quote and cannot be written into a systemd unit`)
	}
	return nil
}

// unitEnvPath makes a PATH value safe inside a quoted systemd Environment=
// assignment: control characters and quotes are refused (nothing is written),
// and backslash, % (specifier) and $ (variable) are escaped.
func unitEnvPath(p string) (string, bool) {
	if p == "" || strings.ContainsAny(p, "\"\n\r") {
		return "", false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return strings.NewReplacer(`\`, `\\`, "%", "%%", "$", "$$").Replace(p), true
}

// renderUnit writes the per-user unit. Two shapes (owner, 2026-09-27):
//
//   - receiver only (no bound agents): tightly sandboxed. It can write only its
//     own folders and the receive folder; home is read-only, /tmp is private.
//   - agents bound: the daemon launches Claude/Codex in the user's projects, which
//     write project files and the tools' own state all over home, so home is
//     writable and /tmp is shared. System directories stay read-only
//     (ProtectSystem=full) and it can never gain privileges (NoNewPrivileges).
//     What an agent may DO is governed by its own local rules and privilege
//     level (ADR-041 §5-§6), not by this sandbox.
//
// PATH is the installing user's, so the tools the daemon runs are found: a
// systemd --user service otherwise gets a bare PATH without ~/.local/bin or nvm.
func renderUnit(exePath, destDir string, agents bool, pathEnv string) string {
	env := ""
	if v, ok := unitEnvPath(pathEnv); ok {
		env = "Environment=\"PATH=" + v + "\"\n"
	}
	if agents {
		return fmt.Sprintf(`[Unit]
Description=Share2Us background receiver and agent bridge (s2u daemon)
Documentation=https://share2.us
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s daemon run
%sRestart=on-failure
RestartSec=5
NoNewPrivileges=true
ProtectSystem=full
ProtectControlGroups=true

[Install]
WantedBy=default.target
`, exePath, env)
	}
	rwPaths := "%h/.config/share2us %h/.cache/share2us %t/share2us"
	if d := strings.TrimSpace(destDir); d != "" {
		// Quoted, so a path with spaces is one entry rather than several.
		rwPaths += ` "` + d + `"`
	}
	return fmt.Sprintf(`[Unit]
Description=Share2Us background receiver (s2u daemon)
Documentation=https://share2.us
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s daemon run
%sRestart=on-failure
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
`, exePath, env, rwPaths)
}

// ServiceSupported reports that per-OS service integration exists here.
func ServiceSupported() bool { return true }

// ServiceInstall writes the user unit and enables+starts it. exePath is the
// share2us binary to run; destDir (may be "") is added to ReadWritePaths.
func ServiceInstall(exePath, destDir string, out io.Writer) error {
	if err := validUnitPath(exePath); err != nil {
		return err
	}
	if err := validUnitPath(strings.TrimSpace(destDir)); err != nil {
		return err
	}
	path, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(renderUnit(exePath, destDir, agentsBound(), servicePATH())), 0o644); err != nil {
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
	// See the note in service_darwin.go: idempotent exit code, honest message.
	removed := true
	if err := os.Remove(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		removed = false
	}
	_ = run("systemctl", "--user", "daemon-reload")
	if removed {
		fmt.Fprintf(out, "Removed %s\n", unitName)
	} else {
		fmt.Fprintf(out, "%s was not installed; nothing to remove\n", unitName)
	}
	return nil
}

// ServiceStart starts the installed unit.
func ServiceStart() error { return run("systemctl", "--user", "start", unitName) }

// ServiceStop stops the installed unit.
func ServiceStop() error { return run("systemctl", "--user", "stop", unitName) }

// ServiceRestart restarts the unit, so it runs the binary now on disk.
func ServiceRestart() error { return run("systemctl", "--user", "restart", unitName) }

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

// run executes a service-manager command. It is a variable so the test suite can
// replace it: a test must never reach the developer's real systemctl, launchctl
// or schtasks (one test disabled the real s2u-daemon.service on 2026-09-27).
var run = runCommand

func runCommand(name string, args ...string) error {
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

// LingerSupported reports whether "keep running after logout" is a thing here:
// only for a systemd --user service.
func LingerSupported() bool { return true }

// LingerEnabled reports whether this user's services keep running after logout.
func LingerEnabled() bool { return lingerEnabled() }

// EnableLinger keeps this user's services running after logout (and starts them
// at boot). For your OWN user, systemd's default policy allows this from an
// active session (SSH included) without sudo. --no-ask-password makes it fail
// fast instead of hanging on a password prompt nobody can answer (agent join
// runs inside an agent session, with no terminal to type into).
func EnableLinger() error {
	return run("loginctl", "--no-ask-password", "enable-linger", currentUser())
}

// ServiceActive reports whether the per-user service unit is running.
func ServiceActive() bool {
	return exec.Command("systemctl", "--user", "is-active", "--quiet", unitName).Run() == nil
}

// unitDestDir recovers the receive folder an existing unit was written with (the
// quoted ReadWritePaths entry), so a refresh keeps it.
func unitDestDir(unit string) string {
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "ReadWritePaths=") {
			if i := strings.Index(line, `"`); i >= 0 {
				if j := strings.LastIndex(line, `"`); j > i {
					return line[i+1 : j]
				}
			}
		}
	}
	return ""
}

// ServiceNeedsRefresh reports whether the installed unit is not what this build
// would write now (a new PATH, agents bound since it was written, or an older
// template). Only an installed unit can be stale.
func ServiceNeedsRefresh(exePath string) bool {
	path, err := unitPath()
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	unit := string(raw)
	return unit != renderUnit(exePath, unitDestDir(unit), agentsBound(), servicePATH())
}

// ServiceRefresh rewrites the unit (keeping its receive folder) and restarts it.
func ServiceRefresh(exePath string, out io.Writer) error {
	path, err := unitPath()
	if err != nil {
		return err
	}
	raw, _ := os.ReadFile(path)
	if err := ServiceInstall(exePath, unitDestDir(string(raw)), out); err != nil {
		return err
	}
	return run("systemctl", "--user", "restart", unitName)
}
