// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli-core/lanshare"
)

func TestOwnAccountDevicesRefresh(t *testing.T) {
	key := []byte("account device identity")
	fp := lanshare.IdentityFingerprint(key)
	now := time.Now()
	sessions := []clicore.DeviceSession{
		{ClientType: "cli", LanFingerprint: " " + strings.ToUpper(fp) + " ", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)},
		{ClientType: "cli", LanFingerprint: fp, ExpiresAt: now.Add(-time.Hour).Format(time.RFC3339)},
		{ClientType: "cli", LanFingerprint: lanshare.IdentityFingerprint([]byte("expired")), ExpiresAt: now.Add(-time.Hour).Format(time.RFC3339)},
		{ClientType: "web", LanFingerprint: lanshare.IdentityFingerprint([]byte("browser")), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)},
		{ClientType: "cli", LanFingerprint: lanshare.IdentityFingerprint([]byte("invalid expiry")), ExpiresAt: "invalid"},
	}
	fail := false
	var responseMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responseMu.Lock()
		defer responseMu.Unlock()
		if r.URL.Path != "/v1/devices" || r.Header.Get("Authorization") != "Bearer test-session" {
			t.Errorf("unexpected devices request: %s %s", r.Method, r.URL.Path)
		}
		if fail {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(clicore.ListDevicesResponse{Sessions: sessions})
	}))
	defer srv.Close()
	client := clicore.NewClient(srv.URL, "test-session")
	cache := newOwnAccountDevices()
	if cache.contains(key) || cache.contains(nil) {
		t.Fatal("empty cache admitted a sender")
	}
	cache.refresh(context.Background(), client)
	if !cache.contains(key) {
		t.Fatal("active same-account CLI device rejected")
	}
	for _, rejected := range []string{"expired", "browser", "invalid expiry", "other account"} {
		if cache.contains([]byte(rejected)) {
			t.Fatalf("%s identity admitted", rejected)
		}
	}
	responseMu.Lock()
	fail = true
	responseMu.Unlock()
	cache.refresh(context.Background(), client)
	if !cache.contains(key) {
		t.Fatal("recent cache lost on transient refresh failure")
	}
	cache.mu.Lock()
	cache.fetchedAt = now.Add(-ownDeviceCacheTTL - time.Second)
	cache.mu.Unlock()
	if cache.contains(key) {
		t.Fatal("stale account membership admitted after refresh failure")
	}
	responseMu.Lock()
	fail = false
	sessions = nil // revoked devices are omitted by /v1/devices
	responseMu.Unlock()
	cache.refresh(context.Background(), client)
	if cache.contains(key) {
		t.Fatal("removed account device retained by successful refresh")
	}
}

func TestOwnAccountDevicesSessionExpiresBetweenRefreshes(t *testing.T) {
	key := []byte("expired cached device")
	cache := newOwnAccountDevices()
	cache.fetchedAt = time.Now()
	cache.fingerprints[lanshare.IdentityFingerprint(key)] = time.Now().Add(-time.Second)
	if cache.contains(key) {
		t.Fatal("session remained admitted past its expiry")
	}
}
