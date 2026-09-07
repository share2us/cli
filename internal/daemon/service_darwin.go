//go:build darwin

package daemon

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

const launchdLabel = "us.share2.daemon"

// plistPath is the per-user LaunchAgent path (~/Library/LaunchAgents/<label>.plist).
func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
}

// renderPlist builds the LaunchAgent. RunAtLoad starts it at login; KeepAlive
// with SuccessfulExit=false restarts it if it crashes but not after a clean stop
// (a `daemon stop` / bootout). ProcessType Background keeps it low priority.
// The daemon writes its own log to the user cache dir; StandardOut/Error capture
// anything before logging is up.
func renderPlist(exePath, destDir string) string {
	args := []string{exePath, "daemon", "run"}
	if destDir != "" {
		args = append(args, "--dest", destDir)
	}
	var argXML string
	for _, a := range args {
		argXML += "\n    <string>" + xmlEscape(a) + "</string>"
	}
	logPath := "/tmp/share2us-daemon.log"
	if cache, err := os.UserCacheDir(); err == nil {
		logPath = filepath.Join(cache, "share2us", "daemon.log")
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>%s
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>ProcessType</key>
  <string>Background</string>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, launchdLabel, argXML, logPath, logPath)
}

func ServiceSupported() bool { return true }

// ServiceInstall writes the LaunchAgent and bootstraps it into the user's GUI
// domain so it starts now and at every login.
func ServiceInstall(exePath, destDir string, out io.Writer) error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(renderPlist(exePath, destDir)), 0o644); err != nil {
		return err
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	// bootout first so a re-install reloads a changed plist; ignore "not loaded".
	_ = run("launchctl", "bootout", domain, path)
	if err := run("launchctl", "bootstrap", domain, path); err != nil {
		return err
	}
	fmt.Fprintf(out, "Installed and started %s\n", launchdLabel)
	fmt.Fprintf(out, "Logs: share2us daemon logs   Stop: share2us daemon stop\n")
	return nil
}

// ServiceUninstall boots the agent out and removes the plist.
func ServiceUninstall(out io.Writer) error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_ = run("launchctl", "bootout", domain, path)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintf(out, "Removed %s\n", launchdLabel)
	return nil
}

func ServiceStart() error {
	return run("launchctl", "kickstart", "gui/"+strconv.Itoa(os.Getuid())+"/"+launchdLabel)
}

func ServiceStop() error {
	// Stopping via the control socket is preferred (daemon.go tries that first);
	// this is the service-manager fallback.
	path, err := plistPath()
	if err != nil {
		return err
	}
	return run("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid()), path)
}

// ServiceLogs tails the daemon's log file (launchd has no journalctl).
func ServiceLogs(follow bool) error {
	logPath := "/tmp/share2us-daemon.log"
	if cache, err := os.UserCacheDir(); err == nil {
		logPath = filepath.Join(cache, "share2us", "daemon.log")
	}
	args := []string{"-n", "50"}
	if follow {
		args = append(args, "-f")
	}
	args = append(args, logPath)
	cmd := exec.Command("tail", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if msg := string(out); msg != "" {
			return fmt.Errorf("%s: %v: %s", name, err, msg)
		}
		return fmt.Errorf("%s: %v", name, err)
	}
	return nil
}

func xmlEscape(s string) string {
	repl := map[rune]string{'&': "&amp;", '<': "&lt;", '>': "&gt;", '"': "&quot;", '\'': "&apos;"}
	out := ""
	for _, r := range s {
		if e, ok := repl[r]; ok {
			out += e
		} else {
			out += string(r)
		}
	}
	return out
}
