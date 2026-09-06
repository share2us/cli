package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	clicore "github.com/share2us/cli-core"
)

func TestParseEnvelope(t *testing.T) {
	e := ParseEnvelope(`{"prompt":"do it","file_name":"shot.png"}`)
	if e.Prompt != "do it" || e.FileName != "shot.png" {
		t.Fatalf("envelope = %+v", e)
	}
	// bare prompt (P4a raw string) falls back to a prompt-only envelope.
	b := ParseEnvelope("just a prompt")
	if b.Prompt != "just a prompt" || b.FileName != "" {
		t.Fatalf("fallback = %+v", b)
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
