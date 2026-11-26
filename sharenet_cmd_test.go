// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	clicore "github.com/share2us/cli-core"
)

type manageFake struct {
	codeBody  map[string]any
	mineBody  map[string]any
	invited   []string
	decisions []string
}

func (f *manageFake) server(t *testing.T) *clicore.Client {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sharenets", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"sharenets":[{"id":"net-1","name":"Shop","role":"owner"}]}`)
	})
	mux.HandleFunc("GET /v1/sharenets/net-1/projects", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"projects":[{"id":"p-1","sharenet_id":"net-1","name":"api"}]}`)
	})
	mux.HandleFunc("GET /v1/sharenets/net-1", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"net-1","name":"Shop","members":[{"id":"m-1","role":"owner","you":true,"email":"me@x.test"},{"id":"m-2","role":"contributor","pending":true,"email":"dev@x.test"}]}`)
	})
	mux.HandleFunc("POST /v1/sharenets/net-1/invites", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.invited = append(f.invited, b["email"].(string))
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":"inv-1"}`)
	})
	mux.HandleFunc("POST /v1/projects/p-1/join-codes", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&f.codeBody)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"code":"S2U-AAAA-BBBB-CCCC","expires_at":"2026-12-03T00:00:00Z"}`)
	})
	mux.HandleFunc("POST /v1/projects/p-1/agents/mine", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&f.mineBody)
		fmt.Fprint(w, `{"agents":[{"agent_id":"agt_MINE00000000000000001","id":"row-1","admitted":true,"can_post":true}]}`)
	})
	mux.HandleFunc("POST /v1/projects/p-1/join-requests/{id}/{verb}", func(w http.ResponseWriter, r *http.Request) {
		f.decisions = append(f.decisions, r.PathValue("id")+":"+r.PathValue("verb"))
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return clicore.NewClient(srv.URL, "tok")
}

func manageApp(out *bytes.Buffer) app {
	return app{stdout: out, stderr: out, stdin: strings.NewReader(""), stdinIsTTY: func(io.Reader) bool { return false }}
}

func TestSharenetCodeAndMembers(t *testing.T) {
	f := &manageFake{}
	c := f.server(t)
	var out bytes.Buffer
	a := manageApp(&out)
	ctx := context.Background()
	n, _ := a.resolveSharenet(ctx, c, "shop")
	if code := a.sharenetCode(ctx, c, n, "", []string{"--uses", "5", "--expires", "7d"}); code != 0 {
		t.Fatalf("code: %d %s", code, out.String())
	}
	if f.codeBody["max_uses"] != float64(5) || f.codeBody["expires_in_hours"] != float64(168) || !strings.Contains(out.String(), "!s2u agent join S2U-AAAA-BBBB-CCCC") {
		t.Fatalf("body %v out %s", f.codeBody, out.String())
	}
	out.Reset()
	if code := a.sharenetMembers(ctx, c, n); code != 0 || !strings.Contains(out.String(), "dev@x.test") || !strings.Contains(out.String(), "(invited)") {
		t.Fatalf("members: %s", out.String())
	}
}

// Changing who is in a sharenet needs --yes when nobody can be asked (an agent, a script).
func TestSharenetMembershipChangesNeedConfirmation(t *testing.T) {
	f := &manageFake{}
	c := f.server(t)
	var out bytes.Buffer
	a := manageApp(&out)
	ctx := context.Background()
	n, _ := a.resolveSharenet(ctx, c, "Shop")
	if code := a.sharenetInvite(ctx, c, n, []string{"dev@x.test"}, false); code == 0 || len(f.invited) != 0 {
		t.Fatalf("invited without confirmation: %v", f.invited)
	}
	if code := a.sharenetInvite(ctx, c, n, []string{"dev@x.test", "qa@x.test"}, true); code != 0 || len(f.invited) != 2 {
		t.Fatalf("invite with --yes: %d %v %s", code, f.invited, out.String())
	}
}

func TestAddAgentsReportsEach(t *testing.T) {
	f := &manageFake{}
	c := f.server(t)
	var out bytes.Buffer
	a := manageApp(&out)
	ctx := context.Background()
	n, _ := a.resolveSharenet(ctx, c, "Shop")
	p, err := a.resolveProject(ctx, c, n, "")
	if err != nil {
		t.Fatal(err)
	}
	if code := a.addAgents(ctx, c, n, p, []string{"agt_MINE00000000000000001"}, true); code != 0 {
		t.Fatalf("add: %s", out.String())
	}
	if f.mineBody["can_post"] != true || !strings.Contains(out.String(), "Added agt_MINE00000000000000001 to Shop.api (may post files)") {
		t.Fatalf("body %v out %s", f.mineBody, out.String())
	}
}

func TestWaitForAdmission(t *testing.T) {
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/agent/projects/p-1/agents", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusForbidden) // not a member yet: the device can't look
			fmt.Fprint(w, `{"error":{"code":"forbidden","message":"no"}}`)
			return
		}
		fmt.Fprint(w, `{"agents":[{"agent_id":"agt_ME0000000000000000001"}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	old := joinWaitPoll
	joinWaitPoll = time.Millisecond
	defer func() { joinWaitPoll = old }()
	var out bytes.Buffer
	a := manageApp(&out)
	if !a.waitForAdmission(context.Background(), clicore.NewClient(srv.URL, "tok"), "p-1", "agt_ME0000000000000000001", time.Second) {
		t.Fatal("approval not seen")
	}
	if a.waitForAdmission(context.Background(), clicore.NewClient(srv.URL, "tok"), "p-1", "agt_OTHER00000000000000001", 20*time.Millisecond) {
		t.Fatal("another agent's admission counted")
	}
}
