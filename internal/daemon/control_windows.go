//go:build windows

package daemon

import (
	"errors"
	"net"
	"time"
)

// Windows named-pipe control transport is Phase 3 (ADR-035). Until then the
// daemon does not run on Windows; installs there use the GUI tray.
var errWindowsControl = errors.New("share2us daemon is not supported on Windows in this release; use the desktop app")

func listenSocket(string) (net.Listener, error)          { return nil, errWindowsControl }
func dialSocket(string, time.Duration) (net.Conn, error) { return nil, errWindowsControl }
func isAddrInUse(error) bool                             { return false }
