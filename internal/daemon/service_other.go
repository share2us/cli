//go:build !linux && !darwin

package daemon

import "io"

func ServiceSupported() bool { return false }

func ServiceInstall(string, string, io.Writer) error { return ErrServiceUnsupported }
func ServiceUninstall(io.Writer) error               { return ErrServiceUnsupported }
func ServiceStart() error                            { return ErrServiceUnsupported }
func ServiceStop() error                             { return ErrServiceUnsupported }
func ServiceLogs(bool) error                         { return ErrServiceUnsupported }
