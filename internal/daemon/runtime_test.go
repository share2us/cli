package daemon

import (
	"context"
	"errors"
	"testing"
)

func TestRunJobRecoversPanic(t *testing.T) {
	rt := &Runtime{}
	// A panicking job must not crash the scheduler; runJob recovers it.
	rt.runJob(context.Background(), Deps{Logf: func(string, ...any) {}}, job{
		name: "boom",
		run:  func(context.Context) error { panic("kaboom") },
	})
	// An erroring job is logged, not propagated.
	rt.runJob(context.Background(), Deps{Logf: func(string, ...any) {}}, job{
		name: "err",
		run:  func(context.Context) error { return errors.New("nope") },
	})
}

func TestPlural(t *testing.T) {
	if got := plural(1, "Received %d file", "Received %d files"); got != "Received 1 file" {
		t.Fatalf("plural(1) = %q", got)
	}
	if got := plural(3, "Received %d file", "Received %d files"); got != "Received 3 files" {
		t.Fatalf("plural(3) = %q", got)
	}
}
