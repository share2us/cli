// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import "testing"

func TestClaudeHookCommandForOS(t *testing.T) {
	const exe = "C:/Program Files/Share2Us/s2u.exe"
	for _, event := range []string{"session-start", "user-prompt-submit", "pre-tool-use", "stop", "session-end", "headless-pre-tool-use"} {
		t.Run(event, func(t *testing.T) {
			wantWindows := `& "C:/Program Files/Share2Us/s2u.exe" agent hook ` + event
			if got := claudeHookCommandForOS(exe, event, "windows"); got != wantWindows {
				t.Fatalf("Windows hook = %q, want %q", got, wantWindows)
			}
			wantUnix := `"C:/Program Files/Share2Us/s2u.exe" agent hook ` + event
			if got := claudeHookCommandForOS(exe, event, "linux"); got != wantUnix {
				t.Fatalf("Unix hook = %q, want %q", got, wantUnix)
			}
		})
	}
}
