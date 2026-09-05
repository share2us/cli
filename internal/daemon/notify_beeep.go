//go:build !nonotify

package daemon

import "github.com/gen2brain/beeep"

// beeepNotifier delivers notifications via github.com/gen2brain/beeep, the same
// backend the GUI uses (Linux D-Bus, Windows toast, macOS osascript). beeep has
// no reliable cross-platform action-button callback, so SupportsActions is false
// and ask-mode approvals fall back to notify-then-decline (ADR-035).
type beeepNotifier struct{}

// NewNotifier returns the platform notification backend. Build with the
// "nonotify" tag to drop beeep and its transitive deps for a headless/server
// binary (see notify_stub.go).
func NewNotifier() Notifier { return beeepNotifier{} }

func (beeepNotifier) Info(title, message string) {
	// Never block the caller (a transfer completion or the run loop): a wedged
	// notification daemon must not stall receiving.
	go func() { _ = beeep.Notify(title, message, "") }()
}

func (beeepNotifier) SupportsActions() bool { return false }
