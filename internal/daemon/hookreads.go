// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"strconv"
	"strings"
)

// An allowlisted verb is not sufficient: shell expansion can turn a read into
// execution or a write before the hook sees the expanded arguments.
func unsafeReadShell(cmd string) bool {
	if strings.TrimSpace(cmd) == "" || strings.ContainsAny(cmd, "$`<>\\#()*?[]{}~\r\x00") {
		return true
	}
	return strings.Contains(strings.ReplaceAll(cmd, "&&", ""), "&")
}

// shellWords handles literal single/double quoted paths without evaluating
// anything. The unsafe constructs above are refused before this parser runs.
func shellWords(part string) ([]string, bool) {
	var words []string
	var b strings.Builder
	var quote rune
	started := false
	for _, r := range part {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			b.WriteRune(r)
		case r == '\'' || r == '"':
			quote, started = r, true
		case r == ' ' || r == '\t':
			if started {
				words = append(words, b.String())
				b.Reset()
				started = false
			}
		default:
			b.WriteRune(r)
			started = true
		}
	}
	if quote != 0 {
		return nil, false
	}
	if started {
		words = append(words, b.String())
	}
	return words, len(words) > 0
}

func readCommandInside(cwd, part string) bool {
	words, ok := shellWords(part)
	if !ok {
		return false
	}
	verb := words[0]
	args := words[1:]
	paths := func(items []string) bool {
		for _, path := range items {
			if path == "-" { // stdin is not an outside file
				continue
			}
			if path == "" || strings.HasPrefix(path, "-") || strings.HasPrefix(path, "~") || strings.ContainsAny(path, "*?[]{}") || !insideDir(cwd, path) {
				return false
			}
		}
		return true
	}
	peelFlags := func(allowed map[string]bool) ([]string, bool) {
		for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "-" {
			if !allowed[args[0]] && !shortFlagsAllowed(args[0], allowed) {
				return nil, false
			}
			args = args[1:]
		}
		return args, true
	}
	switch verb {
	case "pwd":
		return len(args) == 0
	case "which":
		return len(args) == 1 && args[0] != "" && !strings.ContainsAny(args[0], "/-~*?[]{}")
	case "date":
		// Reading the clock only: -s / --set (which change it) are not accepted.
		for _, a := range args {
			if a != "-u" && a != "--utc" && a != "-R" && !strings.HasPrefix(a, "-I") && !strings.HasPrefix(a, "+") {
				return false
			}
		}
		return true
	case "uname":
		_, ok := peelFlags(map[string]bool{"-a": true, "-s": true, "-r": true, "-m": true, "-n": true, "-v": true, "-o": true})
		return ok && len(args) == 0
	case "uptime":
		return len(args) == 0
	case "ls":
		files, ok := peelFlags(map[string]bool{"-l": true, "-a": true, "-A": true, "-h": true, "-1": true, "-t": true, "-r": true,
			"-S": true, "-R": true, "-d": true, "-F": true, "-i": true, "--full-time": true,
			"--time-style=full-iso": true, "--time-style=long-iso": true, "--time-style=iso": true})
		return ok && paths(files)
	case "cat":
		files, ok := peelFlags(map[string]bool{"-n": true})
		return ok && paths(files)
	case "stat":
		return paths(args)
	case "wc":
		files, ok := peelFlags(map[string]bool{"-l": true, "-w": true, "-c": true, "-m": true})
		return ok && paths(files)
	case "head", "tail":
		if len(args) >= 2 && (args[0] == "-n" || args[0] == "-c") {
			if _, err := strconv.ParseUint(args[1], 10, 64); err != nil {
				return false
			}
			args = args[2:]
		}
		return paths(args)
	case "grep":
		// Search flags only. -r/-R recurse, but every path (default: the project) is still
		// checked; -f (patterns from a file) and anything that writes are not accepted.
		flags := map[string]bool{"-n": true, "-i": true, "-F": true, "-E": true, "-v": true, "-w": true, "-x": true,
			"-c": true, "-l": true, "-L": true, "-r": true, "-R": true, "-h": true, "-H": true, "-o": true, "-s": true, "-q": true}
		patterns := 0
		for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "-" {
			switch {
			case args[0] == "-e" && len(args) > 1:
				patterns++
				args = args[2:]
				continue
			case flags[args[0]] || shortFlagsAllowed(args[0], flags):
				args = args[1:]
				continue
			}
			return false
		}
		if patterns == 0 {
			if len(args) == 0 {
				return false
			}
			args = args[1:] // the pattern
		}
		return paths(args)
	case "git":
		if len(args) == 0 {
			return false
		}
		sub := args[0]
		args = args[1:]
		switch sub {
		case "status":
			files, ok := peelFlags(map[string]bool{"--short": true, "--porcelain": true, "--branch": true, "-sb": true})
			return ok && paths(files)
		case "blame":
			return len(args) == 1 && paths(args)
		}
	}
	return false
}

// shortFlagsAllowed accepts a bundle like "-rni" when every letter is an allowed flag.
func shortFlagsAllowed(arg string, allowed map[string]bool) bool {
	if len(arg) < 3 || arg[0] != '-' || arg[1] == '-' {
		return false
	}
	for _, r := range arg[1:] {
		if !allowed["-"+string(r)] {
			return false
		}
	}
	return true
}
