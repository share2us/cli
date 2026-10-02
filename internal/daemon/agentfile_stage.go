// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// The ordinary LAN receiver already has a 1 TiB absolute transfer limit.
	// Agent files stream to disk and are never buffered in full by this store.
	agentStageMaxBytes int64 = 1 << 40
	agentStageTTL            = 24 * time.Hour
)

var agentNonceName = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)
var agentFingerprint = regexp.MustCompile(`^[0-9a-f]{64}$`)

// AgentFileStage holds only encrypted bytes until a signed inject names the
// nonce and the LAN peer's fingerprint. Its files are not an agent inbox: an
// orphaned push is never placed or run, and is swept after the TTL.
type AgentFileStage struct {
	dir string
}

func newAgentFileStage(dir string) (*AgentFileStage, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("agent file stage is not a real directory")
	}
	return &AgentFileStage{dir: dir}, nil
}

func defaultAgentFileStage() (*AgentFileStage, error) {
	base, err := userConfigDir()
	if err != nil {
		return nil, err
	}
	return newAgentFileStage(filepath.Join(base, "share2us", "agents", "pending-files"))
}

// reserve records the TLS-proven sender identity before the byte transfer.
// The sender's successful LAN acknowledgement then implies both files exist:
// the receiver writes the ciphertext before acknowledging its completion.
func (s *AgentFileStage) reserve(nonce, fingerprint string, size int64) error {
	if !agentNonceName.MatchString(nonce) || !agentFingerprint.MatchString(fingerprint) {
		return errors.New("invalid agent file nonce or sender fingerprint")
	}
	if size < 0 || size > agentStageMaxBytes {
		return fmt.Errorf("agent file size is outside the direct-transfer limit")
	}
	if _, err := os.Lstat(filepath.Join(s.dir, nonce)); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	meta := filepath.Join(s.dir, nonce+".peer")
	f, err := os.OpenFile(meta, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := io.WriteString(f, fingerprint)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(meta)
		return errors.Join(writeErr, closeErr)
	}
	return nil
}

// open releases staged bytes only for the sender fingerprint in the signed,
// verified envelope. A mismatched or absent stage is not a usable LAN source.
func (s *AgentFileStage) open(nonce, fingerprint string) (io.ReadCloser, bool, error) {
	if !agentNonceName.MatchString(nonce) || !agentFingerprint.MatchString(fingerprint) {
		return nil, false, nil
	}
	meta := filepath.Join(s.dir, nonce+".peer")
	info, err := os.Lstat(meta)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Size() != 64 || time.Since(info.ModTime()) > agentStageTTL {
		return nil, false, nil
	}
	peer, err := os.ReadFile(meta)
	if err != nil {
		return nil, false, err
	}
	if !strings.EqualFold(string(peer), fingerprint) {
		return nil, false, nil
	}
	path := filepath.Join(s.dir, nonce)
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > agentStageMaxBytes {
		return nil, false, errors.New("staged agent file is not a valid regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	return f, true, nil
}

func (s *AgentFileStage) remove(nonce string) {
	if !agentNonceName.MatchString(nonce) {
		return
	}
	_ = os.Remove(filepath.Join(s.dir, nonce))
	_ = os.Remove(filepath.Join(s.dir, nonce+".peer"))
}

func (s *AgentFileStage) sweep(now time.Time) error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".s2u-partial-") {
			info, err := entry.Info()
			if err == nil && info.Mode().IsRegular() && now.Sub(info.ModTime()) > agentStageTTL {
				_ = os.Remove(filepath.Join(s.dir, name))
			}
			continue
		}
		if strings.HasSuffix(name, ".peer") {
			name = strings.TrimSuffix(name, ".peer")
		}
		if !agentNonceName.MatchString(name) {
			continue
		}
		info, err := entry.Info()
		if err == nil && now.Sub(info.ModTime()) > agentStageTTL {
			s.remove(name)
		}
	}
	return nil
}
