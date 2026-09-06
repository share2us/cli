//go:build nonotify

package daemon

// NewNotifier returns a no-op notifier for headless builds compiled with the
// "nonotify" tag, dropping the beeep dependency entirely.
func NewNotifier() Notifier { return NoopNotifier{} }
