//go:build !windows

package daemon

import (
	"errors"
	"net"
	"syscall"
	"time"
)

// listenSocket binds a unix-domain stream socket. Binding is the single-instance
// lock (a second bind fails with EADDRINUSE).
func listenSocket(path string) (net.Listener, error) {
	return net.Listen("unix", path)
}

func dialSocket(path string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", path, timeout)
}

// isAddrInUse reports whether err is the "address already in use" a leftover
// socket file produces, so Listen can distinguish a live daemon from a stale
// socket to clear.
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}
