// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestCurrentZellijPaneReadsOnlyValidIdentifiers(t *testing.T) {
	t.Setenv(zellijSessionEnv, "share2us")
	t.Setenv(zellijPaneEnv, "terminal_3")
	got := CurrentZellijPane()
	if got == nil || got.Session != "share2us" || got.Pane != "3" {
		t.Fatalf("pane = %+v", got)
	}
	t.Setenv(zellijPaneEnv, "not-a-pane")
	if got := CurrentZellijPane(); got != nil {
		t.Fatalf("invalid pane accepted: %+v", got)
	}
}

func TestProcessZellijPaneReadsThisProcessOnSupportedSystems(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("process environment lookup is unsupported here")
	}
	cmd := exec.Command("sleep", "5")
	cmd.Env = append(os.Environ(), zellijSessionEnv+"=test-session", zellijPaneEnv+"=9", "S2U_MUST_NOT_BE_READ=secret")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	var got *ZellijPane
	for deadline := time.Now().Add(time.Second); got == nil && time.Now().Before(deadline); {
		got = ProcessZellijPane(cmd.Process.Pid)
		if got == nil {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if got == nil || got.Session != "test-session" || got.Pane != "9" {
		t.Fatalf("process pane = %+v", got)
	}
}
