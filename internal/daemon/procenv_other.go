// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

//go:build !linux && !darwin

package daemon

import "errors"

func processZellijEnv(int) (map[string]string, error) {
	return nil, errors.New("process environment is unsupported on this platform")
}
