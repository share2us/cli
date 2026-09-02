//go:build !windows

package main

// Only Windows silently drops inbound connections to an unlisted program; on
// Linux and macOS a blocked port is either an explicit local firewall the user
// configured or not blocked at all, so there is nothing useful to advise.
func inboundLikelyBlocked() bool { return false }

func firewallHint() string { return "" }
