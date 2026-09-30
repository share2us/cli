// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/share2us/cli/internal/daemon"
)

func TestActiveTypedHopFailsClosedWithoutDaemon(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := daemon.BeginTypedGuard("session-1", "request-1"); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	defer func() { os.Stdin = previous }()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	os.Stdin = reader
	if _, err := writer.WriteString(`{"session_id":"session-1","cwd":"/tmp","tool_name":"Bash","tool_input":{"command":"echo unsafe"}}`); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	var out bytes.Buffer
	if code := (app{stdout: &out}).agentHook([]string{"pre-tool-use"}); code != 0 {
		t.Fatalf("hook exit = %d", code)
	}
	if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("daemon-unreachable active hop was allowed: %q", out.String())
	}
}
