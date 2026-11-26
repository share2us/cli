// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	clicore "github.com/share2us/cli-core"
)

func sharenetServer(t *testing.T) *clicore.Client {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/sharenets", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"sharenets":[{"id":"net-1","name":"Butterpos"}]}`)
	})
	mux.HandleFunc("/v1/sharenets/net-1/projects", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"projects":[{"id":"p-api","sharenet_id":"net-1","name":"api"},{"id":"p-web","sharenet_id":"net-1","name":"web"},{"id":"p-locked","sharenet_id":"net-1","name":"locked"}]}`)
	})
	agents := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }
	}
	mux.HandleFunc("/v1/agent/projects/p-api/agents", agents(`{"agents":[{"agent_id":"agt_REVIEWER0000000000001","function":"reviewer","status":"available"},{"agent_id":"agt_DEPLOY000000000000001","function":"deployer","status":"available"}]}`))
	mux.HandleFunc("/v1/agent/projects/p-web/agents", agents(`{"agents":[{"agent_id":"agt_REVIEWER0000000000002","function":"reviewer","status":"available"}]}`))
	mux.HandleFunc("/v1/agent/projects/p-locked/agents", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"code":"forbidden","message":"not a member"}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return clicore.NewClient(srv.URL, "tok")
}

func TestResolveSharenetAddress(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	a := app{}
	c := sharenetServer(t)
	ctx := context.Background()

	if pid, agent, ok, err := a.resolveSharenetAddress(ctx, c, "butterpos.deployer"); err != nil || !ok || pid != "p-api" || agent != "deployer" {
		t.Fatalf("unique function: %q %q %v %v", pid, agent, ok, err)
	}
	if pid, _, ok, err := a.resolveSharenetAddress(ctx, c, "butterpos.web.reviewer"); err != nil || !ok || pid != "p-web" {
		t.Fatalf("with project: %q %v %v", pid, ok, err)
	}
	if _, _, ok, err := a.resolveSharenetAddress(ctx, c, "butterpos.reviewer"); !ok || err == nil || !strings.Contains(err.Error(), "butterpos.<project>.reviewer") {
		t.Fatalf("ambiguous should ask for the project: %v %v", ok, err)
	}
	if _, _, ok, err := a.resolveSharenetAddress(ctx, c, "butterpos.nobody"); !ok || err == nil {
		t.Fatalf("missing agent in a known sharenet: %v %v", ok, err)
	}
	if _, _, ok, _ := a.resolveSharenetAddress(ctx, c, "homelab-70"); ok {
		t.Fatal("a plain name was taken for a sharenet address")
	}
	if _, _, ok, _ := a.resolveSharenetAddress(ctx, c, "other.reviewer"); ok {
		t.Fatal("an unknown sharenet prefix must fall back to an ordinary name")
	}
}
