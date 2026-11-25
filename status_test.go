// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestPrintStatus(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	a := app{stdout: &out, stderr: &out}
	a.printStatus(statusReport{
		Version: "20261126000000", Channel: "stable", LatestVersion: "20261127000000",
		LoggedIn: true, AuthMethod: "device login", Email: "a@example.com", Plan: "Max", Server: "https://api.example",
		DeviceID: "dev-1", DeviceName: "laptop", LoggedInAt: "2026-10-07T10:31:00Z", TokenLastUsed: "2026-11-26T11:59:30Z",
		TokenExpires: "2027-01-05T10:31:00Z", EncryptionKey: true, OtherDevices: 2,
		DaemonRunning: true, DaemonVersion: "20261120000000", BoundAgents: 1, Skills: map[string]string{"claude": "current"},
	}, now)
	got := out.String()
	for _, want := range []string{
		"Signed in as         a@example.com", "update available: 20261127000000", "Logged in            2026-10-07",
		"(50 days ago)", "Token last used", "(just now), by this check", "(in 39 days)", "Device keys          encryption",
		"older than this CLI", "/s2u in Claude Code  current",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	out.Reset()
	a.printStatus(statusReport{Version: "1", Channel: "stable"}, now)
	if !strings.Contains(out.String(), "Signed in            no (`share2us login`)") {
		t.Fatalf("logged-out view:\n%s", out.String())
	}
}
