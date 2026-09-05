//go:build !windows

package daemon

import (
	"os"
	"path/filepath"
	"testing"

	clicore "github.com/share2us/cli-core"
)

// perUserDirs points the config and runtime dirs at temp dirs so the control
// socket and token never touch the real user environment.
func perUserDirs(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
}

func okHandler(req Request) Response {
	switch req.Op {
	case "ping":
		return Response{OK: true}
	case "status":
		return Response{OK: true, PID: 4321, Version: "test"}
	default:
		return Response{Err: "unknown op"}
	}
}

func TestListenSingleInstance(t *testing.T) {
	perUserDirs(t)

	closer, err := Listen(okHandler)
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	defer closer.Close()

	if _, err := Listen(okHandler); err != ErrAlreadyRunning {
		t.Fatalf("second Listen = %v, want ErrAlreadyRunning", err)
	}

	if !Running() {
		t.Fatal("Running() = false while a daemon holds the socket")
	}
	resp, ok := Query("status")
	if !ok || !resp.OK || resp.PID != 4321 {
		t.Fatalf("status query = %+v ok=%v", resp, ok)
	}
}

func TestListenClearsStaleSocket(t *testing.T) {
	perUserDirs(t)

	// A leftover socket file with nothing behind it must be cleared, not treated
	// as a live instance.
	sock, err := clicore.DaemonSocketPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sock, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	closer, err := Listen(okHandler)
	if err != nil {
		t.Fatalf("Listen over stale socket: %v", err)
	}
	closer.Close()
}

func TestControlRejectsBadToken(t *testing.T) {
	perUserDirs(t)
	closer, err := Listen(okHandler)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()

	sock, _ := clicore.DaemonSocketPath()
	resp, err := dial(sock, "wrong-token", Request{Op: "ping"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if resp.OK || resp.Err != "unauthorized" {
		t.Fatalf("bad-token response = %+v, want unauthorized", resp)
	}
}

func TestQueryNoDaemon(t *testing.T) {
	perUserDirs(t)
	if _, ok := Query("status"); ok {
		t.Fatal("Query returned ok with no daemon running")
	}
}
