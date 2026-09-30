// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	typedDeliveryCap      = 3
	typedDeliveryWindow   = time.Hour
	typedDeliveryCooldown = 5 * time.Minute
)

// typedUsage survives a daemon restart; restarting the service must not reset
// a member's ability to spend the owner's attention and tokens by typing.
type typedUsage map[string][]time.Time

func typedUsagePath() (string, error) {
	bindings, err := BindingsPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(bindings), "typed-usage.json"), nil
}

// reserveTypedDeliveryAt counts an attempt immediately before Paste. An
// uncertain paste may still be submitted by the owner, so it keeps its slot.
// A corrupt/unwritable counter fails closed: channel or waiting delivery may
// still proceed, but automatic typing does not.
func reserveTypedDeliveryAt(path, session string, now time.Time) (count int, reason string, err error) {
	if session == "" {
		return 0, "session required", os.ErrInvalid
	}
	usage := typedUsage{}
	if raw, readErr := os.ReadFile(path); readErr == nil {
		if err := json.Unmarshal(raw, &usage); err != nil {
			return 0, "", fmt.Errorf("parse typed delivery counter: %w", err)
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return 0, "", readErr
	}
	if usage == nil {
		usage = typedUsage{}
	}
	// Trim every session so old bindings do not accumulate indefinitely.
	for id, times := range usage {
		kept := times[:0]
		for _, at := range times {
			if at.After(now) || now.Sub(at) < typedDeliveryWindow {
				kept = append(kept, at)
			}
		}
		if len(kept) == 0 {
			delete(usage, id)
		} else {
			usage[id] = kept
		}
	}
	times := usage[session]
	if len(times) >= typedDeliveryCap {
		return len(times), "hourly cap reached", nil
	}
	if len(times) != 0 && now.Sub(times[len(times)-1]) < typedDeliveryCooldown {
		return len(times), "cooldown active", nil
	}
	usage[session] = append(times, now.UTC())
	raw, err := json.Marshal(usage)
	if err != nil {
		return 0, "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return 0, "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".typed-usage-*.tmp")
	if err != nil {
		return 0, "", err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return 0, "", err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return 0, "", err
	}
	if err := tmp.Close(); err != nil {
		return 0, "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return 0, "", err
	}
	return len(usage[session]), "", nil
}

func (rt *Runtime) reserveTypedDelivery(session string) (int, string, error) {
	rt.typedMu.Lock()
	defer rt.typedMu.Unlock()
	path, err := typedUsagePath()
	if err != nil {
		return 0, "", err
	}
	return reserveTypedDeliveryAt(path, session, time.Now())
}
