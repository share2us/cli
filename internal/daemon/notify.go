package daemon

// Notifier delivers native desktop notifications. The daemon runs with no
// terminal and (in this phase) no window, so a notification is the only way it
// reaches the user. Implementations must be safe to call from any goroutine and
// must never block the caller: a stuck notification backend cannot be allowed to
// stall a transfer or the run loop.
type Notifier interface {
	// Info posts a fire-and-forget notification. Errors are swallowed (a missing
	// notification daemon is not an error worth failing a receive over).
	Info(title, message string)
	// SupportsActions reports whether this backend can present Accept/Reject
	// buttons and return the choice. Today no backend does (see notify_beeep.go),
	// so approval falls back to notify-then-decline; the hook exists so a richer
	// backend can light up ask-mode approvals without touching the runtime.
	SupportsActions() bool
}

// NoopNotifier drops every notification. Used when --no-notify is set or on a
// build without the notification backend.
type NoopNotifier struct{}

func (NoopNotifier) Info(string, string)   {}
func (NoopNotifier) SupportsActions() bool { return false }
