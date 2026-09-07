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
