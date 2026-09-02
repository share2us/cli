//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// createNoWindowFW suppresses the console window that would otherwise flash when
// this process spawns powershell.
const createNoWindowFW = 0x08000000 // CREATE_NO_WINDOW

// inboundLikelyBlocked reports whether Windows Firewall will probably drop
// inbound connections to this executable.
//
// Why this exists: a receiver still ADVERTISES over mDNS even when the firewall
// drops its port, so a peer discovers the machine by name and then times out
// connecting to it. Discoverable-but-unreachable reads as a product bug rather
// than a firewall prompt nobody saw. Observed on a real Windows 10 box
// 2026-09-02: `--dest=DESKTOP-CK60S3D` resolved, then i/o timeout.
//
// A GUI process triggers the Windows firewall prompt when it binds. A CLI
// receive started from a terminal, a service, or SSH does not, so nothing tells
// the user anything at all.
//
// Deliberately advisory: it never blocks the listener and never changes firewall
// state. Adding a rule from a shipped binary is the same shape of behaviour that
// got the installer flagged as malware for importing a root certificate
// (installer/DISTRIBUTION.md), so the fix is to TELL the user, precisely, and let
// them decide.
//
// Returns false when it cannot tell — an advisory that guesses is worse than
// silence.
func inboundLikelyBlocked() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	// One query, two facts: is the firewall on for any profile, and does an
	// enabled inbound Allow rule already name this exact program?
	script := `$ErrorActionPreference='SilentlyContinue';` +
		`$on = (Get-NetFirewallProfile | Where-Object {$_.Enabled -eq 'True'} | Measure-Object).Count;` +
		`$r = Get-NetFirewallApplicationFilter -Program ` + psQuoteFW(exe) +
		` | Get-NetFirewallRule | Where-Object {$_.Direction -eq 'Inbound' -and $_.Enabled -eq 'True' -and $_.Action -eq 'Allow'};` +
		`Write-Output $on; Write-Output (@($r).Count)`

	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindowFW}
	out, err := cmd.Output()
	if err != nil {
		return false // cannot tell
	}
	fields := strings.Fields(strings.ReplaceAll(string(out), "\r\n", "\n"))
	if len(fields) < 2 {
		return false
	}
	profilesOn, rules := fields[0], fields[1]
	// Blocked only when the firewall is on somewhere AND no rule names this exe.
	return profilesOn != "0" && rules == "0"
}

// firewallHint is the exact command that fixes it, with the running binary's
// real path substituted so it can be pasted verbatim.
func firewallHint() string {
	exe, err := os.Executable()
	if err != nil {
		exe = "s2u.exe"
	}
	// Deliberately states only what was checked. A rule that opens the PORT (rather
	// than naming the program) also permits the traffic and is not inspected here,
	// so asserting "senders will time out" would sometimes be wrong. Report the
	// fact, make the consequence conditional.
	return "Windows Firewall is on and no inbound rule names this program.\n" +
		"  If another device can SEE this one but times out connecting, that is why.\n" +
		"  To allow it, in an elevated PowerShell:\n" +
		"    New-NetFirewallRule -DisplayName 'Share2Us' -Direction Inbound -Action Allow \\\n" +
		"      -Program '" + exe + "' -Profile Private"
}

func psQuoteFW(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
