// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/share2us/cli-core/daemonctl"
	"github.com/share2us/cli/internal/daemon"
)

// After `s2u update` replaces the binary, a running daemon still runs the old
// one until it restarts (found 2026-09-27: the service ran a stale build for
// hours). update restarts it when it can do so safely, and says how otherwise.
//
// These are variables so tests never reach the machine's real daemon or service
// manager (see TestMain).
var (
	queryDaemon    = daemonctl.Query
	serviceActive  = daemon.ServiceActive
	serviceRestart = daemon.ServiceRestart
	startDetached  = startDetachedDaemon
)

// detachedMarker is set in the environment of a daemon this CLI started in the
// background (no service manager), so update knows it may restart it the same way.
const detachedMarker = "S2U_DAEMON_DETACHED=1"

func (a app) restartDaemonAfterUpdate(newVersion string) {
	st, ok := queryDaemon("status")
	if !ok || !st.OK || strings.Contains(st.Version, newVersion) {
		return // nothing running, or it already runs the new build
	}
	manual := fmt.Sprintf("run `%s daemon stop` and then `%s daemon start`", commandName, commandName)
	// A hop's agent run dies with the daemon, so never restart under one. An
	// older daemon does not know "busy" and cannot say; it is restarted.
	if b, ok := queryDaemon("busy"); ok && b.OK {
		fmt.Fprintf(a.stdout, "The background service is running a hop, so it keeps the old version for now. When it is done, %s.\n", manual)
		return
	}
	// One daemon per user holds the control socket, so if the service manager
	// says the service is running, the daemon that answered IS the service.
	if serviceActive() {
		if err := serviceRestart(); err != nil || !waitForDaemonVersion(newVersion, 10*time.Second) {
			fmt.Fprintf(a.stderr, "Could not restart the background service on the new version; %s.\n", manual)
			return
		}
		fmt.Fprintf(a.stdout, "Restarted the background service on %s.\n", newVersion)
		return
	}
	if d, ok := queryDaemon("detached"); ok && d.OK {
		exe, err := a.currentExecutable()
		if err == nil {
			_, _ = queryDaemon("stop")
			if waitForDaemonGone(8*time.Second) && startDetached(exe) == nil && waitForDaemonVersion(newVersion, 10*time.Second) {
				fmt.Fprintf(a.stdout, "Restarted the background receiver on %s.\n", newVersion)
				return
			}
		}
		fmt.Fprintf(a.stderr, "Could not restart the background receiver on the new version; run `%s daemon run` again.\n", commandName)
		return
	}
	// Started by hand in a terminal: that is the person's to restart.
	fmt.Fprintf(a.stdout, "The %s daemon running on this machine is still %s; restart it to use %s.\n", commandName, st.Version, newVersion)
}

func waitForDaemonVersion(version string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if st, ok := queryDaemon("status"); ok && st.OK && strings.Contains(st.Version, version) {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

func waitForDaemonGone(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if st, ok := queryDaemon("status"); !ok || !st.OK {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
