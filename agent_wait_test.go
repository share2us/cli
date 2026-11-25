// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	clicore "github.com/share2us/cli-core"
)

func TestWaitForResultReturnsTheReplyAsSoonAsItIsDone(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) < 3 {
			fmt.Fprint(w, `{"id":"r1","status":"running","result":"Delivered by typing"}`)
			return
		}
		fmt.Fprint(w, `{"id":"r1","status":"done","result":"pong\u001b[31m"}`)
	}))
	defer srv.Close()
	old := agentWaitPoll
	agentWaitPoll = time.Millisecond
	defer func() { agentWaitPoll = old }()
	var out, errOut bytes.Buffer
	a := app{stdout: &out, stderr: &errOut}
	code := a.waitForResult(context.Background(), clicore.NewClient(srv.URL, "tok"), "r1", time.Minute)
	if code != 0 || out.String() != "status: done\npong[31m\n" {
		t.Fatalf("exit %d, stdout %q", code, out.String())
	}
	if strings.Count(errOut.String(), "running") != 1 {
		t.Fatalf("progress should print once per change: %q", errOut.String())
	}
}

func TestWaitForResultTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"r1","status":"queued","result":""}`)
	}))
	defer srv.Close()
	old := agentWaitPoll
	agentWaitPoll = time.Millisecond
	defer func() { agentWaitPoll = old }()
	var out, errOut bytes.Buffer
	a := app{stdout: &out, stderr: &errOut}
	if code := a.waitForResult(context.Background(), clicore.NewClient(srv.URL, "tok"), "r1", 20*time.Millisecond); code != 124 || !strings.Contains(errOut.String(), "agent wait r1") {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
}

func TestRequestTiming(t *testing.T) {
	st := clicore.AgentInjectState{CreatedAt: "2026-11-25T20:58:35Z", DeliveredAt: "2026-11-25T20:58:38Z", CompletedAt: "2026-11-25T20:58:59Z"}
	got := requestTiming(st)
	if !strings.Contains(got, "delivered after 3s") || !strings.Contains(got, "finished after 24s") || !strings.HasPrefix(got, "sent ") {
		t.Fatalf("got %q", got)
	}
	if requestTiming(clicore.AgentInjectState{}) != "" {
		t.Fatal("an older server sends no times")
	}
}
