package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/share2us/cli-core/lanshare"
)

// guardServePath must refuse to publish the home directory, any credential
// store, and system directories over `s2u --serve`, while still allowing an
// ordinary project subfolder.
func TestGuardServePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	home = filepath.Clean(home)

	blocked := []string{
		home,                                  // the whole home dir
		filepath.Dir(home),                    // an ancestor of home (e.g. /home)
		"/",                                   // filesystem root
		filepath.Join(home, ".ssh"),           // credential store
		filepath.Join(home, ".ssh", "id_rsa"), // a file inside one
		filepath.Join(home, ".aws", "credentials"),
		filepath.Join(home, ".config"),
		filepath.Join(home, ".config", "gcloud"),
		filepath.Join(home, ".gnupg"),
		filepath.Join(home, ".npmrc"),
		"/etc",
		"/etc/passwd",
		"/root",
	}
	for _, p := range blocked {
		if err := guardServePath(p); err == nil {
			t.Errorf("guardServePath(%q) = nil, want a refusal", p)
		}
	}

	allowed := []string{
		filepath.Join(home, "projects", "mysite"),
		filepath.Join(home, "Downloads"),
		filepath.Join(home, "s2u-share"),
	}
	for _, p := range allowed {
		if err := guardServePath(p); err != nil {
			t.Errorf("guardServePath(%q) = %v, want nil", p, err)
		}
	}
}

// --keep turns one-shot receiving into a persistent listener. The primitive
// already existed in cli-core (ReceiveOptions.Loop); this guards the CLI wiring
// that was missing.
func TestParseLanReceiveKeepFlag(t *testing.T) {
	for _, arg := range []string{"--keep", "-k"} {
		opts, err := parseLanReceiveArgs([]string{"--receive", arg})
		if err != nil {
			t.Fatalf("%s: parse error = %v", arg, err)
		}
		if !opts.keep {
			t.Fatalf("%s did not set keep", arg)
		}
	}

	opts, err := parseLanReceiveArgs([]string{"--receive"})
	if err != nil {
		t.Fatalf("parse error = %v", err)
	}
	if opts.keep {
		t.Fatal("keep must default to false (one-shot stays the default)")
	}
}

// The receive banner prints a copy-paste command for the sender. It must NOT
// embed the passphrase: that put it in the sender's shell history and in `ps`
// output, and it was the exact form we told every user to copy. The bare flag
// prompts with echo off instead.
func TestReceiveBannerDoesNotEmbedPassphraseInSenderCommand(t *testing.T) {
	var out bytes.Buffer
	a := app{stdout: io.Discard, stderr: &out}
	const secret = "correct-horse-battery-staple"

	a.printReceiveBanner(lanshare.ListenInfo{
		Port:        4444,
		Mode:        lanshare.ModePassword,
		Passphrase:  secret,
		Fingerprint: "ab:cd",
	}, lanReceiveOpts{bind: "192.0.2.10"})

	senderLine := ""
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "On the sender:") {
			senderLine = line
		}
	}
	if senderLine == "" {
		t.Fatalf("no sender command in banner:\n%s", out.String())
	}
	if strings.Contains(senderLine, secret) {
		t.Fatalf("sender command leaks the passphrase: %q", senderLine)
	}
	if !strings.Contains(senderLine, "--password") {
		t.Fatalf("sender command should still advertise --password: %q", senderLine)
	}
}

// guardServePath blocks pointing --serve AT a credential store, but within an
// allowed tree two holes remained: dotfiles were served and listed like any
// other file, and a symlink out of the tree was followed transparently — so one
// link could re-expose exactly what the guard refuses.
func TestServeHidesDotfilesAndSymlinkEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	write := func(dir, name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	write(root, "public.txt", "fine to share")
	write(root, ".env", "SECRET=leaked")
	write(root, ".git/config", "[remote]")
	secret := write(outside, "id_rsa", "PRIVATE KEY")
	if err := os.Symlink(secret, filepath.Join(root, "escape.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escapedir")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	srv := httptest.NewServer(serveHandler(root, true))
	defer srv.Close()

	get := func(p string) (int, string) {
		t.Helper()
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	if code, body := get("/public.txt"); code != http.StatusOK || body != "fine to share" {
		t.Fatalf("ordinary file should still serve: %d %q", code, body)
	}
	for _, blocked := range []string{"/.env", "/.git/config", "/escape.txt", "/escapedir/id_rsa"} {
		if code, _ := get(blocked); code != http.StatusNotFound {
			t.Fatalf("%s served with status %d, want 404", blocked, code)
		}
	}

	// A symlink that stays INSIDE the tree is legitimate and must still work —
	// the check is "escapes the root", not "is a symlink".
	if err := os.Symlink(filepath.Join(root, "public.txt"), filepath.Join(root, "alias.txt")); err == nil {
		if code, body := get("/alias.txt"); code != http.StatusOK || body != "fine to share" {
			t.Fatalf("within-tree symlink should still serve: %d %q", code, body)
		}
	}

	// A listing must not name what Open refuses to serve.
	code, listing := get("/")
	if code != http.StatusOK {
		t.Fatalf("directory listing status = %d", code)
	}
	if !strings.Contains(listing, "public.txt") {
		t.Fatalf("listing lost the ordinary file:\n%s", listing)
	}
	for _, hidden := range []string{".env", ".git", "escape.txt", "escapedir"} {
		if strings.Contains(listing, hidden) {
			t.Fatalf("listing leaked %q:\n%s", hidden, listing)
		}
	}
}
