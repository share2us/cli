package main

import (
	"testing"
	"time"

	"github.com/share2us/cli-core/lanshare"
)

func TestParseLanBroadcastArgs(t *testing.T) {
	o, err := parseLanBroadcastArgs([]string{"file.md", "--broadcast"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if o.path != "file.md" || o.access != lanshare.AccessApprove {
		t.Fatalf("defaults wrong: %+v", o)
	}

	o, err = parseLanBroadcastArgs([]string{"-b", "f", "--access", "all", "--once", "--yes", "--name", "n", "--port", "5000"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if o.access != "all" || !o.once || !o.yes || o.name != "n" || o.port != 5000 {
		t.Fatalf("parsed wrong: %+v", o)
	}

	if _, err := parseLanBroadcastArgs([]string{"f", "-b", "--access", "bogus"}); err == nil {
		t.Fatal("expected error for invalid access")
	}
	if _, err := parseLanBroadcastArgs([]string{"a", "b", "-b"}); err == nil {
		t.Fatal("expected error for two paths")
	}
}

func TestParseDiscoverArgs(t *testing.T) {
	o, err := parseDiscoverArgs(nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if o.timeout != 10*time.Second || o.path != "." {
		t.Fatalf("defaults wrong: %+v", o)
	}
	o, err = parseDiscoverArgs([]string{"--download", "a.txt", "--path", "/tmp/x", "--trust", "--timeout", "3s"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if o.download != "a.txt" || o.path != "/tmp/x" || !o.trust || o.timeout != 3*time.Second {
		t.Fatalf("parsed wrong: %+v", o)
	}
	if _, err := parseDiscoverArgs([]string{"--timeout", "nope"}); err == nil {
		t.Fatal("expected error for bad timeout")
	}
}

func TestFindBroadcastAndSort(t *testing.T) {
	peers := []lanshare.Peer{
		{Name: "recv", IsBroadcast: false},
		{Name: "boxB", IsBroadcast: true, FileName: "report.pdf", FileSize: 100},
		{Name: "boxA", IsBroadcast: true, FileName: "notes.md", FileSize: 50},
	}
	sortPeers(peers)
	// offers first, then by name
	if !peers[0].IsBroadcast || !peers[1].IsBroadcast || peers[2].IsBroadcast {
		t.Fatalf("sort put receivers first: %+v", peers)
	}
	if peers[0].Name != "boxA" || peers[1].Name != "boxB" {
		t.Fatalf("offers not name-sorted: %+v", peers)
	}
	if p, ok := findBroadcast(peers, "report.pdf"); !ok || p.Name != "boxB" {
		t.Fatalf("exact filename match failed: %+v %v", p, ok)
	}
	if p, ok := findBroadcast(peers, "NOTES"); !ok || p.Name != "boxA" {
		t.Fatalf("substring match failed: %+v %v", p, ok)
	}
	if _, ok := findBroadcast(peers, "missing"); ok {
		t.Fatal("unexpected match")
	}
	if _, ok := findBroadcast(peers, "recv"); ok {
		t.Fatal("receiver should not be a broadcast match")
	}
}
