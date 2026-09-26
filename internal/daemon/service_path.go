// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"os"
	"path/filepath"
	"strings"
)

// servicePATH is the PATH recorded into the service definition: the installing
// shell's PATH, so the service finds claude/codex where the user does, minus
// what must not outlive that shell. Temp folders go (a test's or a tool's scratch
// bin would otherwise shadow the real claude, and can be recreated by anyone who
// can write to /tmp), as do relative, missing and repeated entries.
func servicePATH() string {
	return cleanServicePATH(os.Getenv("PATH"), os.TempDir(), dirExists)
}

func cleanServicePATH(path, tmp string, exists func(string) bool) string {
	temps := []string{"/tmp", "/var/tmp"}
	if tmp != "" {
		temps = append(temps, filepath.Clean(tmp))
	}
	seen := map[string]bool{}
	var keep []string
	for _, dir := range filepath.SplitList(path) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		dir = filepath.Clean(dir)
		if seen[dir] || underAny(dir, temps) || !exists(dir) {
			continue
		}
		seen[dir] = true
		keep = append(keep, dir)
	}
	return strings.Join(keep, string(os.PathListSeparator))
}

func underAny(dir string, roots []string) bool {
	for _, r := range roots {
		if dir == r || strings.HasPrefix(dir, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func dirExists(dir string) bool {
	st, err := os.Stat(dir)
	return err == nil && st.IsDir()
}
