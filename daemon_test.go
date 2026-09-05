package main

import (
	"bytes"
	"testing"
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
