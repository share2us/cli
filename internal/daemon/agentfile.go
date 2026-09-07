package daemon

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"

	clicore "github.com/share2us/cli-core"
)

// InjectEnvelope is the sealed payload of an inject: the prompt and, when a file
// rides along, its name. Sealing the whole envelope keeps the filename E2E too
// (the server sees neither). A P4a raw-string prompt (no envelope) is handled by
// ParseEnvelope's fallback.
type InjectEnvelope struct {
	Prompt   string `json:"prompt"`
	FileName string `json:"file_name,omitempty"`
}

// ParseEnvelope reads a decrypted inject payload. If it isn't a JSON envelope it
// is treated as a bare prompt (back-compat).
func ParseEnvelope(raw string) InjectEnvelope {
	var e InjectEnvelope
	if err := json.Unmarshal([]byte(raw), &e); err == nil && e.Prompt != "" {
		return e
	}
	return InjectEnvelope{Prompt: raw}
}

// placeInjectedFile decrypts ciphertext with contentKey and writes it under
// <cwd>/.s2u-inbox/<name>, returning the path the agent can read. The name is
// base-sanitised so it can't escape the inbox dir.
func placeInjectedFile(cwd, name string, ciphertext, contentKey []byte) (string, error) {
	dir := filepath.Join(cwd, ".s2u-inbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	safe := filepath.Base(name)
	if safe == "." || safe == string(filepath.Separator) || safe == "" {
		safe = "injected-file"
	}
	path := filepath.Join(dir, safe)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := clicore.DecryptStream(f, bytes.NewReader(ciphertext), contentKey); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}
