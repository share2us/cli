// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	clicore "github.com/share2us/cli-core"
)

func TestParseDaemonRunArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    daemonRunOpts
		wantErr bool
	}{
		{name: "empty", args: nil, want: daemonRunOpts{}},
		{name: "dest space", args: []string{"--dest", "/x"}, want: daemonRunOpts{dest: "/x"}},
		{name: "dest equals", args: []string{"--dest=/y"}, want: daemonRunOpts{dest: "/y"}},
		{name: "flags", args: []string{"--no-lan", "--no-notify"}, want: daemonRunOpts{noLAN: true, noNotify: true}},
		{name: "foreground ignored", args: []string{"--foreground"}, want: daemonRunOpts{}},
		{name: "unknown", args: []string{"--nope"}, wantErr: true},
		{name: "dest missing value", args: []string{"--dest"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDaemonRunArgs(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDaemonUnknownSubcommand(t *testing.T) {
	var out, errb bytes.Buffer
	a := app{stdout: &out, stderr: &errb}
	if code := a.daemon(t.Context(), []string{"frobnicate"}); code != 2 {
		t.Fatalf("unknown subcommand exit = %d, want 2", code)
	}
}

// ---- §AG D2: the daemon is headless, so install does the asking -------------

// The resident service can never put the first-arrival question to anybody.
// Install is the one moment a human is definitely present, so it asks there and
// stores the answer; the service then starts with a decided setting instead of
// silently holding files, or silently writing them, forever.
func TestDaemonInstallAsksAndRecordsReceiveSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var errOut bytes.Buffer
	a := app{stdout: io.Discard, stderr: &errOut, stdin: strings.NewReader("y\n" + filepath.Join(home, "inbox") + "\n"),
		stdinIsTTY: func(io.Reader) bool { return true }}
	a.askReceiveSettingsForDaemon("")

	prompt := errOut.String()
	if !strings.Contains(prompt, "automatically?") {
		t.Fatalf("the auto question was not asked:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Receive folder") {
		t.Fatalf("the folder question was not asked:\n%s", prompt)
	}
	config, err := clicore.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	settings := config.ReceiveSettings()
	if !settings.Auto || !settings.AutoAnswered {
		t.Fatalf("the answer was not recorded: %+v", settings)
	}
	if want := filepath.Join(home, "inbox"); settings.Dir != want {
		t.Fatalf("dir = %q, want %q", settings.Dir, want)
	}
}

// Anything that is not clearly a yes is a no: this gates whether files land on
// disk with nobody watching. Answering it at all still counts as an answer, so
// the question does not come back.
func TestDaemonInstallDefaultsToNotAutoDownloading(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var errOut bytes.Buffer
	a := app{stdout: io.Discard, stderr: &errOut, stdin: strings.NewReader("\n"),
		stdinIsTTY: func(io.Reader) bool { return true }}
	a.askReceiveSettingsForDaemon("")

	settings, _ := clicore.LoadConfig()
	got := settings.ReceiveSettings()
	if got.Auto {
		t.Fatal("a bare Enter must not turn on automatic downloads")
	}
	if !got.AutoAnswered {
		t.Fatal("declining IS an answer; the prompt must not come back")
	}
	// With nothing landing automatically there is no folder to ask about yet.
	if strings.Contains(errOut.String(), "Receive folder") {
		t.Fatalf("asked where files go after being told not to save them:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "receive") {
		t.Fatalf("did not say how to get files instead:\n%s", errOut.String())
	}
}

// An unattended install (a package postinst, a provisioning script) has nobody
// to ask and MUST NOT hang on a prompt.
func TestDaemonInstallDoesNotPromptWithoutATerminal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var errOut bytes.Buffer
	a := app{stdout: io.Discard, stderr: &errOut, stdin: strings.NewReader(""),
		stdinIsTTY: func(io.Reader) bool { return false }}
	a.askReceiveSettingsForDaemon("")

	if errOut.String() != "" {
		t.Fatalf("prompted with no terminal:\n%s", errOut.String())
	}
	config, _ := clicore.LoadConfig()
	if config.ReceiveSettings().AutoAnswered {
		t.Fatal("an unattended install must not answer the question on the user's behalf")
	}
}

// Somebody who has already chosen must not be asked again on every reinstall.
func TestDaemonInstallDoesNotReAskAnAnsweredQuestion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := clicore.SetReceiveAuto(false); err != nil {
		t.Fatal(err)
	}

	var errOut bytes.Buffer
	a := app{stdout: io.Discard, stderr: &errOut, stdin: strings.NewReader("y\n"),
		stdinIsTTY: func(io.Reader) bool { return true }}
	a.askReceiveSettingsForDaemon("")

	if strings.Contains(errOut.String(), "automatically?") {
		t.Fatalf("re-asked a question that was already answered:\n%s", errOut.String())
	}
	config, _ := clicore.LoadConfig()
	if config.ReceiveSettings().Auto {
		t.Fatal("the stored answer was overwritten")
	}
}

// --dest answers the folder question on the command line.
func TestDaemonInstallSkipsFolderQuestionWhenDestGiven(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var errOut bytes.Buffer
	a := app{stdout: io.Discard, stderr: &errOut, stdin: strings.NewReader("y\n"),
		stdinIsTTY: func(io.Reader) bool { return true }}
	a.askReceiveSettingsForDaemon("/explicit/dest")

	if strings.Contains(errOut.String(), "Receive folder") {
		t.Fatalf("asked for a folder that was already given on the command line:\n%s", errOut.String())
	}
}
