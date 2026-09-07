//go:build windows

package daemon

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

const taskName = "Share2Us-Daemon"

// windowsLogPath is where the daemon appends its log on Windows (a Scheduled
// Task has no journal). daemonRun opens this too, and `daemon logs` tails it.
func windowsLogPath() string {
	if cache, err := os.UserCacheDir(); err == nil {
		return filepath.Join(cache, "share2us", "daemon.log")
	}
	return filepath.Join(os.TempDir(), "share2us-daemon.log")
}

// renderTaskXML builds a Task Scheduler definition: a logon-triggered task that
// runs `<exe> daemon run [--dest <destDir>]` at the user's own privilege level,
// with no time limit (it is long-running) and a single-instance policy. XML
// avoids the notorious `schtasks /tr` quoting problems.
// utf16LEWithBOM encodes the task definition the way schtasks demands.
//
// `schtasks /create /xml` does NOT read UTF-8: it wants UTF-16LE with a byte
// order mark, and rejects anything else with the unhelpful
//
//	ERROR: The task XML is malformed.
//	(1,40)::ERROR: unable to switch the encoding
//
// where 1,40 is the position of the encoding declaration. Verified on Windows 10
// 19045 (2026-09-07): identical XML fails as UTF-8 and is accepted as UTF-16LE.
func utf16LEWithBOM(s string) []byte {
	codes := utf16.Encode([]rune(s))
	out := make([]byte, 0, 2+len(codes)*2)
	out = append(out, 0xFF, 0xFE) // BOM: little-endian
	for _, c := range codes {
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}

func renderTaskXML(exePath, destDir string) string {
	args := "daemon run"
	if strings.TrimSpace(destDir) != "" {
		args += ` --dest "` + xmlEscape(destDir) + `"`
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Share2Us background receiver (s2u daemon)</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Enabled>true</Enabled>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlEscape(exePath) + `</Command>
      <Arguments>` + xmlEscape(args) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

func ServiceSupported() bool { return true }

// ServiceInstall registers the per-user Scheduled Task from an XML definition and
// starts it. Store/MSIX-packaged installs cannot register a service and should
// use the desktop app instead (ADR-035).
func ServiceInstall(exePath, destDir string, out io.Writer) error {
	xmlPath := filepath.Join(os.TempDir(), "share2us-daemon-task.xml")
	if err := os.WriteFile(xmlPath, utf16LEWithBOM(renderTaskXML(exePath, destDir)), 0o600); err != nil {
		return err
	}
	defer os.Remove(xmlPath)
	if err := run("schtasks", "/create", "/tn", taskName, "/xml", xmlPath, "/f"); err != nil {
		return err
	}
	if err := run("schtasks", "/run", "/tn", taskName); err != nil {
		return err
	}
	fmt.Fprintf(out, "Installed and started scheduled task %q\n", taskName)
	fmt.Fprintf(out, "Logs: share2us daemon logs   Stop: share2us daemon stop\n")
	return nil
}

func ServiceUninstall(out io.Writer) error {
	// Stop the running instance FIRST. Deleting the task alone leaves the daemon
	// running until the user logs out, so "uninstall" would appear to succeed
	// while the thing it uninstalled kept receiving files (seen on Windows 10
	// 19045, 2026-09-07). Linux gets this right via `systemctl --user disable
	// --now`; /end is the schtasks equivalent. Best-effort: it fails harmlessly
	// when the task is not currently running.
	_ = run("schtasks", "/end", "/tn", taskName)
	if err := run("schtasks", "/delete", "/tn", taskName, "/f"); err != nil {
		return err
	}
	fmt.Fprintf(out, "Removed scheduled task %q\n", taskName)
	return nil
}

func ServiceStart() error { return run("schtasks", "/run", "/tn", taskName) }

// ServiceStop ends the running task instance. The control-pipe stop (tried first
// in daemon.go) is preferred; this is the service-manager fallback.
func ServiceStop() error { return run("schtasks", "/end", "/tn", taskName) }

// ServiceLogs tails the daemon's log file via PowerShell (Windows has no tail).
func ServiceLogs(follow bool) error {
	log := windowsLogPath()
	psArgs := "Get-Content -LiteralPath '" + strings.ReplaceAll(log, "'", "''") + "' -Tail 50"
	if follow {
		psArgs += " -Wait"
	}
	cmd := exec.Command("powershell", "-NoProfile", "-Command", psArgs)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%s: %v: %s", name, err, msg)
		}
		return fmt.Errorf("%s: %v", name, err)
	}
	return nil
}

func xmlEscape(s string) string {
	repl := map[rune]string{'&': "&amp;", '<': "&lt;", '>': "&gt;", '"': "&quot;", '\'': "&apos;"}
	var b strings.Builder
	for _, r := range s {
		if e, ok := repl[r]; ok {
			b.WriteString(e)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
