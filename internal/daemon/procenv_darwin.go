// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build darwin

package daemon

import (
	"os/exec"
	"regexp"
	"strconv"
)

func processZellijEnv(pid int) (map[string]string, error) {
	raw, err := exec.Command("ps", "-E", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, key := range []string{zellijSessionEnv, zellijPaneEnv} {
		re := regexp.MustCompile(`(?:^|[[:space:]])` + key + `=([^[:space:]]+)`)
		if match := re.FindSubmatch(raw); len(match) == 2 {
			out[key] = string(match[1])
		}
	}
	return out, nil
}
