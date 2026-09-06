//go:build windows

package daemon

import (
	"io"
	"os"
	"path/filepath"
)

// LogWriter returns where the daemon should write its operational log. On Windows
// a Scheduled Task has no journal and discards stderr, so the log is tee'd to a
// file (windowsLogPath) that `share2us daemon logs` tails. The file handle lives
// for the daemon's lifetime (closed by the OS on exit).
func LogWriter(stderr io.Writer) io.Writer {
	path := windowsLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return stderr
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return stderr
	}
	return io.MultiWriter(stderr, f)
}
