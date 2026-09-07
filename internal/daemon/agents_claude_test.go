package daemon

import (
	"sort"
	"testing"
)

// realistic `claude agents --json`: interactive (status), background (state), and
// a duplicate sessionId across a background + interactive view.
const claudeAgentsSample = `[
  {"id":"8ed387c0","cwd":"/p/a","kind":"background","sessionId":"dup-1","name":"bg","state":"blocked"},
  {"pid":1,"cwd":"/p/b","kind":"interactive","sessionId":"live-idle","name":"one","status":"idle"},
  {"pid":2,"cwd":"/p/c","kind":"interactive","sessionId":"live-busy","name":"two","status":"busy"},
  {"pid":3,"cwd":"/p/a","kind":"interactive","sessionId":"dup-1","name":"dup-live","status":"idle"},
  {"cwd":"/p/d","kind":"background","sessionId":"bg-run","name":"three","state":"running"},
  {"cwd":"/p/e","kind":"background","sessionId":"","name":"noid","state":"blocked"}
]`

func TestParseClaudeAgents(t *testing.T) {
	got, err := parseClaudeAgents([]byte(claudeAgentsSample))
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]DiscoveredSession{}
	for _, s := range got {
		byID[s.SessionID] = s
	}
	if _, ok := byID[""]; ok {
		t.Error("empty sessionId should be skipped")
	}
	if len(byID) != 4 {
		ids := []string{}
		for id := range byID {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		t.Fatalf("want 4 unique sessions, got %d: %v", len(byID), ids)
	}
	if byID["live-idle"].Status != "idle" || byID["live-busy"].Status != "busy" {
		t.Errorf("interactive status mapping wrong: %+v", byID)
	}
	if byID["bg-run"].Status != "busy" {
		t.Errorf("background 'running' should map to busy, got %q", byID["bg-run"].Status)
	}
	// dup-1: the interactive (idle) view should win over the background (blocked→idle),
	// both concrete; the point is it dedupes to one and never loses a concrete status.
	if byID["dup-1"].Status == "unknown" {
		t.Errorf("dup-1 lost its concrete status")
	}
	if byID["live-idle"].Tool != "claude" || byID["live-idle"].Project != "/p/b" || byID["live-idle"].Name != "one" {
		t.Errorf("field mapping wrong: %+v", byID["live-idle"])
	}
}
