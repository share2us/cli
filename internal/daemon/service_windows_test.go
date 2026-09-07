//go:build windows

package daemon

import (
	"strings"
	"testing"
)

func TestRenderTaskXML(t *testing.T) {
	x := renderTaskXML(`C:\Program Files\Share2Us\share2us.exe`, `C:\Users\x\Downloads`)
	for _, want := range []string{
		"<LogonTrigger>",
		"<RunLevel>LeastPrivilege</RunLevel>",
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
		"<Command>C:\\Program Files\\Share2Us\\share2us.exe</Command>",
		"daemon run --dest",
		"C:\\Users\\x\\Downloads",
	} {
		if !strings.Contains(x, want) {
			t.Errorf("task XML missing %q:\n%s", want, x)
		}
	}
}

func TestRenderTaskXMLNoDest(t *testing.T) {
	x := renderTaskXML(`C:\a\share2us.exe`, "")
	if strings.Contains(x, "--dest") {
		t.Error("no dest should omit --dest")
	}
	if !strings.Contains(x, "<Arguments>daemon run</Arguments>") {
		t.Errorf("expected bare 'daemon run' arguments:\n%s", x)
	}
}

func TestXMLEscapeWindows(t *testing.T) {
	if got := xmlEscape(`a&b<c>`); got != "a&amp;b&lt;c&gt;" {
		t.Fatalf("xmlEscape = %q", got)
	}
}
