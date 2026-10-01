// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	clicore "github.com/share2us/cli-core"
)

func TestParseEnvelope(t *testing.T) {
	e := ParseEnvelope(`{"prompt":"do it","file_name":"shot.png","sender_device_name":"jarvis"}`)
	if e.Prompt != "do it" || e.FileName != "shot.png" || e.SenderDeviceName != "jarvis" {
		t.Fatalf("envelope = %+v", e)
	}
	// bare prompt (P4a raw string) falls back to a prompt-only envelope.
	b := ParseEnvelope("just a prompt")
	if b.Prompt != "just a prompt" || b.FileName != "" {
		t.Fatalf("fallback = %+v", b)
	}
	inbox := ParseEnvelope(`{"prompt":"","file_name":"shot.png","deliver":"inbox"}`)
	if inbox.Prompt != "" || inbox.FileName != "shot.png" || inbox.Deliver != "inbox" {
		t.Fatalf("inbox envelope = %+v", inbox)
	}
}

func TestPlaceInjectedFile(t *testing.T) {
	cwd := t.TempDir()
	ck, _ := clicore.NewContentKey()
	plain := []byte("the screenshot bytes")
	var enc bytes.Buffer
	if err := clicore.EncryptStream(&enc, bytes.NewReader(plain), ck); err != nil {
		t.Fatal(err)
	}
	path, err := placeInjectedFile(cwd, "shot.png", enc.Bytes(), ck)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != filepath.Join(cwd, ".s2u-inbox") {
		t.Fatalf("placed outside .s2u-inbox: %s", path)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, plain) {
		t.Fatalf("decrypted file mismatch: %q", got)
	}
}

func TestPlaceInjectedFileSanitizesName(t *testing.T) {
	cwd := t.TempDir()
	ck, _ := clicore.NewContentKey()
	var enc bytes.Buffer
	_ = clicore.EncryptStream(&enc, bytes.NewReader([]byte("x")), ck)
	// a path-escaping name must be reduced to its base and stay in the inbox.
	path, err := placeInjectedFile(cwd, "../../etc/evil", enc.Bytes(), ck)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != filepath.Join(cwd, ".s2u-inbox") || filepath.Base(path) != "evil" {
		t.Fatalf("name not sanitized: %s", path)
	}
}

func TestPlaceInjectedFileDoesNotOverwrite(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".s2u-inbox")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(existing, []byte("owner's original"), 0o600); err != nil {
		t.Fatal(err)
	}
	ck, _ := clicore.NewContentKey()
	var enc bytes.Buffer
	if err := clicore.EncryptStream(&enc, bytes.NewReader([]byte("new attachment")), ck); err != nil {
		t.Fatal(err)
	}
	path, err := placeInjectedFile(cwd, "shot.png", enc.Bytes(), ck)
	if err != nil {
		t.Fatal(err)
	}
	if path == existing || filepath.Dir(path) != dir {
		t.Fatalf("attachment path = %q; should be a distinct inbox file", path)
	}
	if got, _ := os.ReadFile(existing); string(got) != "owner's original" {
		t.Fatalf("overwrote existing file: %q", got)
	}
	if got, _ := os.ReadFile(path); string(got) != "new attachment" {
		t.Fatalf("attachment = %q", got)
	}
}

func TestPlaceInjectedFileRejectsSymlinkedInbox(t *testing.T) {
	cwd := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(cwd, ".s2u-inbox")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	ck, _ := clicore.NewContentKey()
	var enc bytes.Buffer
	_ = clicore.EncryptStream(&enc, bytes.NewReader([]byte("secret")), ck)
	if _, err := placeInjectedFile(cwd, "shot.png", enc.Bytes(), ck); err == nil {
		t.Fatal("accepted a symlinked inbox")
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "shot.png")); !os.IsNotExist(err) {
		t.Fatalf("wrote outside the project: %v", err)
	}
}

func TestPlaceInjectedFileDoesNotFollowFilenameSymlink(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".s2u-inbox")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("outside original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "shot.png")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	ck, _ := clicore.NewContentKey()
	var enc bytes.Buffer
	_ = clicore.EncryptStream(&enc, bytes.NewReader([]byte("new attachment")), ck)
	path, err := placeInjectedFile(cwd, "shot.png", enc.Bytes(), ck)
	if err != nil {
		t.Fatal(err)
	}
	if path == filepath.Join(dir, "shot.png") {
		t.Fatal("used a symlink as the received file")
	}
	if got, _ := os.ReadFile(outside); string(got) != "outside original" {
		t.Fatalf("overwrote symlink target: %q", got)
	}
}

func TestPlaceInjectedFileDecryptFailureDoesNotPublish(t *testing.T) {
	cwd := t.TempDir()
	ck, _ := clicore.NewContentKey()
	if _, err := placeInjectedFile(cwd, "shot.png", []byte("not ciphertext"), ck); err == nil {
		t.Fatal("accepted invalid ciphertext")
	}
	entries, err := os.ReadDir(filepath.Join(cwd, ".s2u-inbox"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed decrypt left inbox entries %v: %v", entries, err)
	}
}

func TestPlaceInjectedFileRequiresProjectDirectory(t *testing.T) {
	if _, err := placeInjectedFile("", "shot.png", nil, nil); err == nil {
		t.Fatal("accepted a file without a project directory")
	}
}
