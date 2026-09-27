// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/share2us/cli-core/daemonctl"
)

// fakeDaemon answers the control socket as a daemon at version would, and
// switches to the new version when restarted.
type fakeDaemon struct {
	version            string
	busy, detached     bool
	active             bool
	restarts, stops    int
	startedDetachedExe string
}

func (f *fakeDaemon) install(t *testing.T) {
	t.Helper()
	q, a, r, s := queryDaemon, serviceActive, serviceRestart, startDetached
	t.Cleanup(func() { queryDaemon, serviceActive, serviceRestart, startDetached = q, a, r, s })
	running := true
	queryDaemon = func(op string) (daemonctl.Response, bool) {
		if !running {
			return daemonctl.Response{}, false
		}
		switch op {
		case "status":
			return daemonctl.Response{OK: true, Version: f.version}, true
		case "busy":
			return daemonctl.Response{OK: f.busy}, true
		case "detached":
			return daemonctl.Response{OK: f.detached}, true
		case "stop":
			f.stops++
			running = false
			return daemonctl.Response{OK: true}, true
		}
		return daemonctl.Response{Err: "unknown op"}, true
	}
	serviceActive = func() bool { return f.active }
	serviceRestart = func() error { f.restarts++; f.version = "20990101000000"; return nil }
	startDetached = func(exe string) error {
		f.startedDetachedExe, f.version, running = exe, "20990101000000", true
		return nil
	}
}

func runRestart(t *testing.T) (string, string) {
	var out, errb bytes.Buffer
	a := app{stdout: &out, stderr: &errb, executablePath: func() (string, error) { return "/bin/s2u", nil }}
	a.restartDaemonAfterUpdate("20990101000000")
	return out.String(), errb.String()
}

func TestUpdateRestartsTheService(t *testing.T) {
	f := &fakeDaemon{version: "20260101000000", active: true}
	f.install(t)
	out, _ := runRestart(t)
	if f.restarts != 1 || !strings.Contains(out, "Restarted the background service on 20990101000000") {
		t.Fatalf("restarts %d, out %q", f.restarts, out)
	}
}

func TestUpdateNeverRestartsUnderAHop(t *testing.T) {
	f := &fakeDaemon{version: "20260101000000", active: true, busy: true}
	f.install(t)
	out, _ := runRestart(t)
	if f.restarts != 0 || f.stops != 0 || !strings.Contains(out, "running a hop") {
		t.Fatalf("restarts %d stops %d, out %q", f.restarts, f.stops, out)
	}
}

func TestUpdateRestartsADetachedDaemonTheSameWay(t *testing.T) {
	f := &fakeDaemon{version: "20260101000000", detached: true}
	f.install(t)
	out, _ := runRestart(t)
	if f.stops != 1 || f.startedDetachedExe != "/bin/s2u" || !strings.Contains(out, "Restarted the background receiver") {
		t.Fatalf("stops %d exe %q, out %q", f.stops, f.startedDetachedExe, out)
	}
}

// A daemon started by hand in a terminal is the person's to restart.
func TestUpdateLeavesAHandStartedDaemon(t *testing.T) {
	f := &fakeDaemon{version: "20260101000000"}
	f.install(t)
	out, _ := runRestart(t)
	if f.restarts != 0 || f.stops != 0 || !strings.Contains(out, "restart it to use 20990101000000") {
		t.Fatalf("restarts %d stops %d, out %q", f.restarts, f.stops, out)
	}
}

func TestUpdateSkipsWhenCurrentOrNotRunning(t *testing.T) {
	f := &fakeDaemon{version: "20990101000000", active: true}
	f.install(t)
	if out, _ := runRestart(t); f.restarts != 0 || out != "" {
		t.Fatalf("already current: restarts %d, out %q", f.restarts, out)
	}
	queryDaemon = func(string) (daemonctl.Response, bool) { return daemonctl.Response{}, false }
	if out, _ := runRestart(t); out != "" {
		t.Fatalf("no daemon: out %q", out)
	}
}
