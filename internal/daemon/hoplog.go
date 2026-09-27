// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// The hop log: what each hop that reached this machine asked, and which session
// it ran in. A hop into a session whose window is open runs in a fork the window
// never shows; this is how its owner finds it (`claude --resume <ran_in>`). It
// holds prompts in the clear, so it lives beside the bindings, 0600, and only
// the newest hopLogKeep records are kept.

const (
	hopLogKeep     = 200
	hopPromptChars = 300
)

// HopRecord is one hop as this machine ran it.
type HopRecord struct {
	Time      time.Time `json:"time"`
	RequestID string    `json:"request_id"`
	Tool      string    `json:"tool"`
	From      string    `json:"from_device"`
	Target    string    `json:"target_session"`
	RanIn     string    `json:"ran_in,omitempty"`
	Mode      string    `json:"mode"`   // "resumed" (same session), "forked" (new session) or "ran"
	Status    string    `json:"status"` // "done" or "failed"
	Prompt    string    `json:"prompt"`
}

func hopLogPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "share2us", "agents", "hops.jsonl"), nil
}

// AppendHop records a hop, trimming the log to the newest hopLogKeep records.
func AppendHop(r HopRecord) error {
	if rs := []rune(r.Prompt); len(rs) > hopPromptChars {
		r.Prompt = string(rs[:hopPromptChars]) + "…"
	}
	path, err := hopLogPath()
	if err != nil {
		return err
	}
	records, _ := LoadHops(0)
	records = append(records, r)
	if len(records) > hopLogKeep {
		records = records[len(records)-hopLogKeep:]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, rec := range records {
		if err := enc.Encode(rec); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadHops returns the logged hops, oldest first; the newest n when n > 0.
func LoadHops(n int) ([]HopRecord, error) {
	path, err := hopLogPath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []HopRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var r HopRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			out = append(out, r)
		}
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out, sc.Err()
}
