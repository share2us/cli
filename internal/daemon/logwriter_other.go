//go:build !windows

package daemon

import "io"

// LogWriter returns the log sink. On non-Windows the service manager (systemd
// journal / launchd StandardOutPath) captures stderr, so no extra file is needed.
func LogWriter(stderr io.Writer) io.Writer { return stderr }
