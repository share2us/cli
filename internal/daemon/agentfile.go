// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	clicore "github.com/share2us/cli-core"
)

// InjectEnvelope is the sealed payload of an inject: the prompt and, when a file
// rides along, its name. Sealing the whole envelope keeps the filename E2E too
// (the server sees neither). A P4a raw-string prompt (no envelope) is handled by
// ParseEnvelope's fallback.
type InjectEnvelope struct {
	Prompt           string `json:"prompt"`
	FileName         string `json:"file_name,omitempty"`
	Deliver          string `json:"deliver,omitempty"`
	SenderDeviceName string `json:"sender_device_name,omitempty"`
}

// ParseEnvelope reads a decrypted inject payload. If it isn't a JSON envelope it
// is treated as a bare prompt (back-compat).
func ParseEnvelope(raw string) InjectEnvelope {
	var e InjectEnvelope
	if err := json.Unmarshal([]byte(raw), &e); err == nil && (e.Prompt != "" || e.FileName != "" || e.Deliver != "") {
		return e
	}
	return InjectEnvelope{Prompt: raw}
}

// placeInjectedFile decrypts ciphertext into the session's private inbox. An
// existing file is never replaced (including a symlink), and a failed decrypt
// removes only the new file. The caller must not run the prompt on an error.
func placeInjectedFile(cwd, name string, ciphertext, contentKey []byte) (string, error) {
	if cwd == "" {
		return "", errors.New("session has no project directory")
	}
	dir := filepath.Join(cwd, ".s2u-inbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("session inbox is not a real directory")
	}
	safe := filepath.Base(name)
	if safe == "." || safe == ".." || safe == string(filepath.Separator) || safe == "" {
		safe = "injected-file"
	}
	for n := 0; n < 1000; n++ {
		candidate := safe
		if n > 0 {
			ext := filepath.Ext(safe)
			candidate = fmt.Sprintf("%s-%d%s", safe[:len(safe)-len(ext)], n, ext)
		}
		path := filepath.Join(dir, candidate)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		decryptErr := clicore.DecryptStream(f, bytes.NewReader(ciphertext), contentKey)
		closeErr := f.Close()
		if decryptErr != nil || closeErr != nil {
			_ = os.Remove(path)
			return "", errors.Join(decryptErr, closeErr)
		}
		return path, nil
	}
	return "", errors.New("session inbox has too many files with this name")
}
