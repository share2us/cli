// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build darwin

package daemon

import (
	"strings"
	"testing"
)

func TestRenderPlist(t *testing.T) {
	p := renderPlist("/usr/local/bin/share2us", "/Users/x/Downloads")
	for _, want := range []string{
		"<string>us.share2.daemon</string>",
		"<string>/usr/local/bin/share2us</string>",
		"<string>daemon</string>",
		"<string>run</string>",
		"<string>--dest</string>",
		"<string>/Users/x/Downloads</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q:\n%s", want, p)
		}
	}
}

func TestRenderPlistNoDest(t *testing.T) {
	p := renderPlist("/usr/local/bin/share2us", "")
	if strings.Contains(p, "--dest") {
		t.Error("no dest should omit the --dest arg")
	}
}

func TestXMLEscape(t *testing.T) {
	if got := xmlEscape(`a&b<c>"d'`); got != "a&amp;b&lt;c&gt;&quot;d&apos;" {
		t.Fatalf("xmlEscape = %q", got)
	}
}

func TestPlistCarriesPATH(t *testing.T) {
	p := renderPlistEnv("/usr/local/bin/share2us", "", "/opt/homebrew/bin:/Users/me/.nvm/bin&x")
	if !strings.Contains(p, "<key>PATH</key>") || !strings.Contains(p, "/opt/homebrew/bin:/Users/me/.nvm/bin&amp;x") {
		t.Fatalf("plist PATH missing or unescaped:\n%s", p)
	}
}
