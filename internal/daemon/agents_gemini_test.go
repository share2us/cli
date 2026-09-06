package daemon

import (
	"strings"
	"testing"
)

// the real `gemini --list-sessions` shape.
const geminiListing = `
Available sessions for this project (2):
  1. Respond only as JSON: {"ok":true} (140 days ago) [60907978-fdfe-43e0-8e17-479001ebd4f5]
  2. Fix the auth bug (2 days ago) [11111111-2222-3333-4444-555555555555]
`

func TestParseGeminiSessions(t *testing.T) {
	got := parseGeminiSessions(geminiListing, "/home/x/proj")
	if len(got) != 2 {
		t.Fatalf("want 2 sessions, got %d: %+v", len(got), got)
	}
	if got[0].SessionID != "60907978-fdfe-43e0-8e17-479001ebd4f5" || got[0].Tool != "gemini" || got[0].Project != "/home/x/proj" {
		t.Fatalf("session 0 = %+v", got[0])
	}
	if got[1].Name != "Fix the auth bug" {
		t.Fatalf("session 1 name = %q", got[1].Name)
	}
}

func TestGeminiIndexForUUID(t *testing.T) {
	if idx := geminiIndexForUUID(geminiListing, "11111111-2222-3333-4444-555555555555"); idx != "2" {
		t.Fatalf("index = %q, want 2", idx)
	}
	if idx := geminiIndexForUUID(geminiListing, "does-not-exist"); idx != "" {
		t.Fatalf("unknown uuid should give empty index, got %q", idx)
	}
}

func TestBuildGeminiInjectArgs(t *testing.T) {
	args := buildGeminiInjectArgs("3", "do the task")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-p do the task") || !strings.Contains(joined, "-r 3") {
		t.Fatalf("args missing headless prompt/resume: %v", args)
	}
	if !strings.Contains(joined, "--approval-mode auto_edit") {
		t.Fatalf("must set a restricted approval mode: %v", args)
	}
	if strings.Contains(joined, "yolo") || strings.Contains(joined, "-y") {
		t.Fatalf("must never use yolo: %v", args)
	}
}
