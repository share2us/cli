// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build linux

package daemon

import (
	"bytes"
	"os"
	"strconv"
)

func processZellijEnv(pid int) (map[string]string, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, entry := range bytes.Split(raw, []byte{0}) {
		for _, key := range []string{zellijSessionEnv, zellijPaneEnv} {
			prefix := []byte(key + "=")
			if bytes.HasPrefix(entry, prefix) {
				out[key] = string(entry[len(prefix):])
			}
		}
	}
	return out, nil
}
