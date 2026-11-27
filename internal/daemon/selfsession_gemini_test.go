// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeChat(t *testing.T, dir, name, id, kind string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(`{"sessionId":"`+id+`","kind":"`+kind+`","messages":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestGeminiSessionOfFindsTheWindowsChat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := "/work/app"
	started := time.Now().Add(-10 * time.Minute)
	named := filepath.Join(home, ".gemini", "tmp", "app", "chats")
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini", "projects.json"), []byte(`{"projects":{"/work/app":"app"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeChat(t, named, "session-old.json", "11111111-old", "main", started.Add(-time.Hour))      // an earlier run
	writeChat(t, named, "session-now.json", "22222222-now", "main", time.Now().Add(-time.Second)) // this window
	writeChat(t, named, "session-sub.json", "33333333-sub", "subagent", time.Now())                // not a main chat

	readGeminiProc = func(pid int) (geminiProc, bool) {
		switch pid {
		case 40:
			return geminiProc{Args: []string{"node", "/usr/lib/node_modules/@google/gemini-cli/bundle/gemini.js"}, CWD: project, Started: started}, true
		case 41:
			return geminiProc{Args: []string{"bash", "-c", "s2u agent join X"}, CWD: project, Started: started}, true
		}
		return geminiProc{}, false
	}
	t.Cleanup(func() { readGeminiProc = procInfo })

	if _, ok := geminiSessionOf(41); ok {
		t.Fatal("a shell was taken for a Gemini process")
	}
	s, ok := geminiSessionOf(40)
	if !ok || s.SessionID != "22222222-now" || s.Tool != "gemini" || s.Project != project || s.PID != 40 {
		t.Fatalf("session = %+v %v", s, ok)
	}
	// Walking up from the shell finds it too.
	parents := map[int]int{41: 40, 40: 1}
	got, err := ownSession(41, geminiSessionOf, func(p int) (int, error) { return parents[p], nil })
	if err != nil || got.SessionID != "22222222-now" {
		t.Fatalf("ownSession = %+v %v", got, err)
	}

	// Older Gemini keeps chats under the folder's sha256; nothing written since the
	// process started means no session (not a stale one).
	sum := sha256.Sum256([]byte("/work/other"))
	hashed := filepath.Join(home, ".gemini", "tmp", hex.EncodeToString(sum[:]), "chats")
	writeChat(t, hashed, "session-a.json", "44444444-a", "", started.Add(-time.Hour))
	readGeminiProc = func(int) (geminiProc, bool) {
		return geminiProc{Args: []string{"gemini"}, CWD: "/work/other", Started: started}, true
	}
	if s, ok := geminiSessionOf(7); ok {
		t.Fatalf("a chat from before the process started was picked: %+v", s)
	}
	writeChat(t, hashed, "session-b.json", "55555555-b", "", time.Now())
	if s, ok := geminiSessionOf(7); !ok || s.SessionID != "55555555-b" {
		t.Fatalf("hashed folder = %+v %v", s, ok)
	}
}

func TestIsGeminiCommand(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"gemini"}, true},
		{[]string{"/home/u/.nvm/bin/node", "/home/u/.nvm/bin/gemini", "--yolo"}, true},
		{[]string{"node", "/x/@google/gemini-cli/dist/index.js"}, true},
		{[]string{"bash", "-c", "echo gemini"}, false},
		{[]string{"claude"}, false},
	} {
		if got := isGeminiCommand(c.args); got != c.want {
			t.Errorf("%v = %v, want %v", c.args, got, c.want)
		}
	}
}
