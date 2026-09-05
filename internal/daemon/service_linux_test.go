//go:build linux

package daemon

import (
	"strings"
	"testing"
)

func TestRenderUnit(t *testing.T) {
	unit := renderUnit("/usr/bin/share2us", "/srv/incoming")
	for _, want := range []string{
		"ExecStart=/usr/bin/share2us daemon run",
		"WantedBy=default.target",
		"NoNewPrivileges=true",
		"ProtectHome=read-only",
		"/srv/incoming", // dest dir folded into ReadWritePaths
		"%t/share2us",   // runtime dir for the control socket
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
}

func TestRenderUnitNoDest(t *testing.T) {
	unit := renderUnit("/usr/bin/share2us", "")
	if strings.Contains(unit, "ReadWritePaths= \n") {
		t.Error("empty dest left a trailing space in ReadWritePaths")
	}
}
