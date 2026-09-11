package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// `receive 1` used to mean "save into a folder named 1", silently, while the
// command's own picker prints "Select (1-3, …)". One keystroke, two meanings,
// no error either way. It is refused now.
func TestReceiveRefusesABareNumber(t *testing.T) {
	for _, arg := range []string{"1", "2", "03", "12"} {
		_, err := parseReceiveArgs([]string{arg})
		if err == nil {
			t.Fatalf("parseReceiveArgs(%q) = nil error, want a refusal: a number here is not a file choice", arg)
		}
		msg := err.Error()
		for _, want := range []string{"--id", "./" + arg} {
			if !strings.Contains(msg, want) {
				t.Errorf("refusal for %q does not mention %q, so it does not say what to type instead:\n%s", arg, want, msg)
			}
		}
	}
}

// ...and a folder that really is called 1 is still reachable, written the way a
// path is written. That is the escape hatch the refusal points at, so it has to
// work.
func TestReceiveAcceptsANumericFolderWrittenAsAPath(t *testing.T) {
	opts, err := parseReceiveArgs([]string{"./1"})
	if err != nil {
		t.Fatalf("parseReceiveArgs(./1) error = %v, want it accepted", err)
	}
	if opts.output != "./1" || !opts.explicitDest {
		t.Fatalf("output = %q explicitDest = %v, want ./1 and true", opts.output, opts.explicitDest)
	}
}

// A name that merely starts with digits is a normal folder, not a choice.
func TestReceiveAcceptsNamesThatAreNotJustDigits(t *testing.T) {
	for _, arg := range []string{"1inbox", "2026-09-11", "inbox"} {
		opts, err := parseReceiveArgs([]string{arg})
		if err != nil {
			t.Fatalf("parseReceiveArgs(%q) error = %v, want it treated as a folder", arg, err)
		}
		if opts.output != arg {
			t.Fatalf("output = %q, want %q", opts.output, arg)
		}
	}
}

// The combination the refusal recommends for "one file, where I am standing".
func TestReceiveAcceptsCurrentFolderWithAnID(t *testing.T) {
	opts, err := parseReceiveArgs([]string{".", "--id", "kVqJEeJhHWMqu_bHAwWHkg"})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if opts.output != "." || len(opts.ids) != 1 || opts.ids[0] != "kVqJEeJhHWMqu_bHAwWHkg" {
		t.Fatalf("output = %q ids = %v, want . and one id", opts.output, opts.ids)
	}
}

// Where a foreground save lands. The configured receive folder exists for the
// UNATTENDED case -- the daemon taking an arrival from a trusted device with
// nobody watching -- and inheriting it for a command a person just typed is how
// a file requested from ~/work landed in ~/Downloads.
func TestReceiveDestinationDependsOnWhetherSomebodyIsThere(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tty      bool
		args     []string
		wantHere bool
	}{
		{"a person naming one file gets it here", true, []string{"--id", "pub-1"}, true},
		{"a person taking everything gets it here", true, []string{"--all"}, true},
		{"a script keeps the configured folder", false, []string{"--all"}, false},
		{"a named destination always wins", true, []string{"/tmp/elsewhere", "--all"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseReceiveArgs(tc.args)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			a := app{stdinIsTTY: func(io.Reader) bool { return tc.tty }}
			dest, _ := a.receiveDir(opts.output)
			// Mirrors the rule in receive(): foreground + nothing named = here.
			if !opts.explicitDest && a.inputIsTTY() && (len(opts.ids) > 0 || opts.all) {
				dest = "."
			}
			if got := dest == "."; got != tc.wantHere {
				t.Fatalf("dest = %q, wantHere = %v", dest, tc.wantHere)
			}
		})
	}
}

// `s2u receive .` with ONE file waiting takes it. A picker offering a single
// choice -- "Select (1-1, a=all, q=quit)" -- is a question with one possible
// answer, and this is meant to be the quick way to collect what somebody just
// sent you.
func TestReceiveWithDestinationTakesTheOnlyWaitingFile(t *testing.T) {
	withCredential(t, "https://api.staging.example.test")
	withMockAPI(t, fakeInboxAPI(t, []map[string]any{
		{"public_id": "pub-1", "file_name": "report.pdf", "size_bytes": 10, "sealed_key": "sealed"},
	}))
	var stdout, stderr bytes.Buffer
	// Empty stdin: if this still prompted, the read would return nothing and the
	// picker would cancel, so the assertions below would fail. That is the point.
	a := app{stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr, sleep: func(time.Duration) {},
		stdinIsTTY: func(io.Reader) bool { return true }}

	a.run(context.Background(), []string{"receive", t.TempDir()})

	if strings.Contains(stderr.String(), "Select (1-1") {
		t.Fatalf("asked which of the one files was meant:\n%s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "One file waiting: report.pdf") {
		t.Fatalf("did not say what it was taking:\n%s", stdout.String())
	}
}

// Two or more still ask, offering all or a number, which is the whole reason the
// picker exists.
func TestReceiveWithDestinationStillAsksWhenThereIsAChoice(t *testing.T) {
	withCredential(t, "https://api.staging.example.test")
	withMockAPI(t, fakeInboxAPI(t, []map[string]any{
		{"public_id": "pub-1", "file_name": "report.pdf", "size_bytes": 10, "sealed_key": "sealed"},
		{"public_id": "pub-2", "file_name": "photos.zip", "size_bytes": 20, "sealed_key": "sealed"},
	}))
	var stdout, stderr bytes.Buffer
	a := app{stdin: strings.NewReader("q\n"), stdout: &stdout, stderr: &stderr, sleep: func(time.Duration) {},
		stdinIsTTY: func(io.Reader) bool { return true }}

	a.run(context.Background(), []string{"receive", t.TempDir()})

	if !strings.Contains(stderr.String(), "Select (1-2, a=all, q=quit)") {
		t.Fatalf("no choice offered when there was one to make:\n%s", stderr.String())
	}
}

// A bare `receive` still only LISTS, even with one file waiting. "Show me" and
// "save it here" are different sentences, and the bare form is the one people
// run to look.
func TestReceiveBareStillOnlyListsWithOneWaiting(t *testing.T) {
	withCredential(t, "https://api.staging.example.test")
	withMockAPI(t, fakeInboxAPI(t, []map[string]any{
		{"public_id": "pub-1", "file_name": "report.pdf", "size_bytes": 10, "sealed_key": "sealed"},
	}))
	var stdout, stderr bytes.Buffer
	a := app{stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr, sleep: func(time.Duration) {},
		stdinIsTTY: func(io.Reader) bool { return true }}

	a.run(context.Background(), []string{"receive"})

	if strings.Contains(stdout.String(), "One file waiting:") {
		t.Fatalf("a bare receive saved something:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "1 file(s) waiting") {
		t.Fatalf("a bare receive did not list:\n%s", stdout.String())
	}
}
