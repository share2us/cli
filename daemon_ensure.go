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
	cmd := exec.Command(exe, "daemon", "run")
	if logf, lerr := daemonLogFile(); lerr == nil {
		cmd.Stdout, cmd.Stderr = logf, logf
		defer logf.Close()
	}
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return daemonNotStarted, errors.Join(serviceErr, err)
	}
	_ = cmd.Process.Release()
	if waitForDaemon(8 * time.Second) {
		return daemonDetachedStarted, nil
	}
	return daemonNotStarted, errors.Join(serviceErr, errors.New("the background receiver did not start"))
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
