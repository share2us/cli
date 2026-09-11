package main

import (
	"testing"

	clicore "github.com/share2us/cli-core"
)

// A signed-in browser is not a device: it has no keypair, cannot be given one,
// and can never receive a sealed send. It used to be listed among the devices
// reading "can't receive yet - sign in with Share2Us on it", which is advice
// nobody can act on, because there is nothing to install on Chrome.
func TestBrowserSessionsAreNotDevices(t *testing.T) {
	for _, kind := range []string{"web", "WEB", " web "} {
		if !isBrowserSession(clicore.DeviceSession{ClientType: kind}) {
			t.Errorf("isBrowserSession(%q) = false, want true", kind)
		}
	}
	for _, kind := range []string{"cli", "gui", "", "android", "webhook"} {
		if isBrowserSession(clicore.DeviceSession{ClientType: kind}) {
			t.Errorf("isBrowserSession(%q) = true, want false", kind)
		}
	}
}
