// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"os"
	"strconv"
	"strings"
)

const (
	zellijSessionEnv = "ZELLIJ_SESSION_NAME"
	zellijPaneEnv    = "ZELLIJ_PANE_ID"
)

// CurrentZellijPane returns the pane inherited by the command currently
// running inside an agent. Only the two zellij identifiers are read.
func CurrentZellijPane() *ZellijPane {
	return zellijPaneFromValues(os.Getenv(zellijSessionEnv), os.Getenv(zellijPaneEnv))
}

// ProcessZellijPane reads only the two zellij identifiers from pid's
// environment. Unsupported or inaccessible processes simply have no pane.
func ProcessZellijPane(pid int) *ZellijPane {
	if pid <= 1 {
		return nil
	}
	values, err := processZellijEnv(pid)
	if err != nil {
		return nil
	}
	return zellijPaneFromValues(values[zellijSessionEnv], values[zellijPaneEnv])
}

func zellijPaneFromValues(session, pane string) *ZellijPane {
	session, pane = strings.TrimSpace(session), strings.TrimSpace(pane)
	if session == "" || pane == "" {
		return nil
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(pane, "terminal_")); err != nil || n < 0 {
		return nil
	}
	return &ZellijPane{Session: session, Pane: strings.TrimPrefix(pane, "terminal_")}
}
