// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build !windows

package daemon

import "testing"

func TestCleanServicePATHDropsTempMissingAndRepeats(t *testing.T) {
	missing := map[string]bool{"/opt/gone": true}
	exists := func(d string) bool { return !missing[d] }
	in := "/tmp/claude-1000/x/scratchpad/bin:/var/folders/ab/T/tmp.1/bin:/home/u/.nvm/bin:relative/bin::/usr/bin:/home/u/.local/bin/:/home/u/.local/bin:/opt/gone:/var/tmp/b:/tmpfoo/bin"
	got := cleanServicePATH(in, "/var/folders/ab/T", exists)
	want := "/home/u/.nvm/bin:/usr/bin:/home/u/.local/bin:/tmpfoo/bin"
	if got != want {
		t.Fatalf("cleaned PATH\n got %s\nwant %s", got, want)
	}
}
