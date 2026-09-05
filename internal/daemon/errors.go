package daemon

import "errors"

// ErrServiceUnsupported is returned by the service-manager operations on
// platforms whose integration is not built yet (macOS launchd and Windows
// Scheduled Task are ADR-035 Phase 3). `daemon run` itself still works there
// (launch it under your own service manager); only install/uninstall/start/logs
// are gated.
var ErrServiceUnsupported = errors.New("share2us daemon service install is not available on this OS yet; run `share2us daemon run` under your own service manager")
