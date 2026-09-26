// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import "testing"

// The CLI points people at the portal of the server they are logged in to.
func TestPortalURLFor(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.share2.us":             "https://portal.share2.us",
		"https://api.staging.share2.us/":    "https://portal.staging.share2.us",
		"http://api:8080":                   "https://portal.share2.us", // in-cluster, not https
		"":                                  "https://portal.share2.us", // not logged in
		"https://share2us.example.test/api": "https://portal.share2.us", // no api. host
	} {
		if got := portalURLFor(in); got != want {
			t.Errorf("portalURLFor(%q) = %q, want %q", in, got, want)
		}
	}
}
