package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestProgressBar(t *testing.T) {
	if got := progressBar(0, 100, 10); got != "[----------]" {
		t.Fatalf("empty bar = %q", got)
	}
	if got := progressBar(50, 100, 10); got != "[#####-----]" {
		t.Fatalf("half bar = %q", got)
	}
	if got := progressBar(100, 100, 10); got != "[##########]" {
		t.Fatalf("full bar = %q", got)
	}
	// Overshoot and unknown totals must not panic or overflow the width.
	if got := progressBar(150, 100, 10); got != "[##########]" {
		t.Fatalf("overshoot bar = %q", got)
	}
	if got := progressBar(1, 0, 10); got != "" {
		t.Fatalf("unknown-total bar = %q", got)
	}
}

func TestFmtDur(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "00:00"},
		{5 * time.Second, "00:05"},
		{95 * time.Second, "01:35"},
		{3725 * time.Second, "1:02:05"},
		{-1 * time.Second, "00:00"},
	} {
		if got := fmtDur(tc.d); got != tc.want {
			t.Errorf("fmtDur(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// The progress line must carry a bar, a percentage, both byte counts, a speed, an
// ETA, and the elapsed time; finish() must replace it with a one-line summary.
func TestProgressPrinterShowsBarPercentAndSummary(t *testing.T) {
	var buf bytes.Buffer
	p := newProgressPrinter(&buf, "receiving")
	p.update(50, 100)
	p.update(100, 100) // final frame is never throttled
	out := buf.String()
	if !strings.Contains(out, "[") || !strings.Contains(out, "]") {
		t.Errorf("no progress bar in %q", out)
	}
	if !strings.Contains(out, "100%") {
		t.Errorf("no final percentage in %q", out)
	}
	if !strings.Contains(out, "ETA") {
		t.Errorf("no ETA in %q", out)
	}
	buf.Reset()
	p.finish()
	sum := buf.String()
	if !strings.Contains(sum, "receiving done:") || !strings.Contains(sum, "avg") {
		t.Errorf("finish summary = %q, want a 'done ... avg' line", sum)
	}
}
