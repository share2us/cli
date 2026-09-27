// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/share2us/cli-core/daemonctl"
	"github.com/share2us/cli/internal/daemon"
)

// Keeping the receiver running without anyone starting it (owner, 2026-09-27):
// after `s2u agent join` or `s2u agent bind`, the agent must be able to receive
// work, which needs the daemon. So those commands make sure it runs:
//
//  1. already running (a service or a hand-started `daemon run`): nothing to do;
//  2. otherwise install and start the per-user service (systemd --user, launchd,
//     or the Windows equivalent), which survives reboots and logouts where the
//     platform allows;
//  3. if the service cannot be installed (no service manager, e.g. a container),
//     start `daemon run` detached so it at least runs now.
//
// The service starts with the agent bridge on (it is the default) and, as the
// install is unattended, with file receiving on its safe default: arrivals wait
// until `s2u receive`. Nothing here asks a question.

type daemonState int

const (
	daemonAlreadyRunning daemonState = iota
	daemonServiceStarted
	daemonDetachedStarted
	daemonNotStarted
)

func daemonRunning() bool {
	resp, ok := daemonctl.Query("status")
	return ok && resp.OK
}

// waitForDaemon polls the control endpoint until the daemon answers or d passes.
func waitForDaemon(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if daemonRunning() {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

func (a app) ensureDaemon() (daemonState, error) {
	if daemonRunning() {
		return daemonAlreadyRunning, nil
	}
	exe, err := a.currentExecutable()
	if err != nil {
		return daemonNotStarted, err
	}
	var serviceErr error
	if daemon.ServiceSupported() {
		if serviceErr = daemon.ServiceInstall(exe, "", io.Discard); serviceErr == nil && waitForDaemon(8*time.Second) {
			return daemonServiceStarted, nil
		}
	}
	// No service manager (or it would not start): run it detached for now.
	if err := startDetachedDaemon(exe); err != nil {
		return daemonNotStarted, errors.Join(serviceErr, err)
	}
	if waitForDaemon(8 * time.Second) {
		return daemonDetachedStarted, nil
	}
	return daemonNotStarted, errors.Join(serviceErr, errors.New("the background receiver did not start"))
}

// startDetachedDaemon starts `daemon run` in the background, logging to the
// cache dir, marked so a later update can restart it the same way.
func startDetachedDaemon(exe string) error {
	cmd := exec.Command(exe, "daemon", "run")
	cmd.Env = append(os.Environ(), detachedMarker)
	if logf, lerr := daemonLogFile(); lerr == nil {
		cmd.Stdout, cmd.Stderr = logf, logf
		defer logf.Close()
	}
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// daemonLogFile is where a detached daemon writes, since it has no terminal.
func daemonLogFile() (*os.File, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	dir = filepath.Join(dir, "share2us")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// reportDaemon tells the person what happened, in one line.
func (a app) reportDaemon(state daemonState, err error) {
	switch state {
	case daemonAlreadyRunning:
		fmt.Fprintln(a.stdout, "The Share2Us background service is running, so this agent can receive work now.")
	case daemonServiceStarted:
		fmt.Fprintf(a.stdout, "Started the Share2Us background service, so this agent can receive work (it keeps running; see %s daemon status).\n", commandName)
	case daemonDetachedStarted:
		fmt.Fprintf(a.stdout, "Started the Share2Us receiver in the background (no service manager here, so it stops at reboot; %s daemon install makes it permanent where supported).\n", commandName)
	default:
		fmt.Fprintf(a.stderr, "Could not start the background receiver (%v). Start it with: %s daemon run\n", err, commandName)
	}
}

// Keeping the service running after logout, on Linux (owner, 2026-09-27).
//
// A systemd --user service stops when its user's last session ends, so on a
// server where agents run in tmux over SSH the agent outlives the logout but
// becomes unreachable. For the agent flows (join, bind), where the person has
// just asked for this agent to be reachable, we enable lingering for their OWN
// user and say so; if the system refuses, we print the one line to run. Plain
// `daemon install` does not do this: there, nobody asked for an always-on agent.

type lingerOutcome int

const (
	lingerNotNeeded lingerOutcome = iota // not Linux, no service, or already on
	lingerEnabledNow
	lingerRefused
)

// shouldEnableLinger is the decision, kept pure so it can be tested.
func shouldEnableLinger(supported bool, state daemonState, serviceActive, lingerOn bool) bool {
	if !supported || lingerOn {
		return false
	}
	return state == daemonServiceStarted || (state == daemonAlreadyRunning && serviceActive)
}

func (a app) ensureLinger(state daemonState) (lingerOutcome, error) {
	if !shouldEnableLinger(daemon.LingerSupported(), state, daemon.ServiceActive(), daemon.LingerEnabled()) {
		return lingerNotNeeded, nil
	}
	if err := daemon.EnableLinger(); err != nil {
		return lingerRefused, err
	}
	return lingerEnabledNow, nil
}

func (a app) reportLinger(outcome lingerOutcome, err error) {
	switch outcome {
	case lingerEnabledNow:
		fmt.Fprintln(a.stdout, "Kept the service running after you log out, so this agent stays reachable (undo: loginctl disable-linger).")
	case lingerRefused:
		fmt.Fprintf(a.stderr, "The service stops when you log out. To keep this agent reachable after logout, run once:\n  sudo loginctl enable-linger %s\n", os.Getenv("USER"))
		_ = err
	}
}

// ensureAgentReachable is what the agent flows call: a running daemon, and on
// Linux a service that outlives the logout.
func (a app) ensureAgentReachable() {
	state, err := a.ensureDaemon()
	a.reportDaemon(state, err)
	if state == daemonNotStarted || state == daemonDetachedStarted {
		return
	}
	if state == daemonAlreadyRunning {
		a.refreshServiceIfStale()
	}
	a.reportLinger(a.ensureLinger(state))
}

// refreshServiceIfStale rewrites an installed service written by an older build
// or before agents were bound here, and restarts it, so it can run agents: the
// tools on the user's PATH, and write access to their projects.
func (a app) refreshServiceIfStale() {
	exe, err := a.currentExecutable()
	if err != nil || !daemon.ServiceNeedsRefresh(exe) {
		return
	}
	if err := daemon.ServiceRefresh(exe, io.Discard); err != nil {
		fmt.Fprintf(a.stderr, "Could not update the background service for agents (%v). Reinstall it with: %s daemon install\n", err, commandName)
		return
	}
	if waitForDaemon(8 * time.Second) {
		fmt.Fprintln(a.stdout, "Updated the background service so it can run your agents.")
	}
}
