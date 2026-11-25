// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli-core/daemonctl"
	"github.com/share2us/cli/internal/daemon"
)

// statusReport is what `s2u status` shows (and prints as JSON with --json).
type statusReport struct {
	Version       string `json:"version"`
	Channel       string `json:"channel"`
	LatestVersion string `json:"latest_version,omitempty"`
	Server        string `json:"server,omitempty"`

	LoggedIn   bool   `json:"logged_in"`
	AuthMethod string `json:"auth_method,omitempty"` // "device login" | "API token"
	Email      string `json:"email,omitempty"`
	AccountID  string `json:"account_id,omitempty"`
	UserID     string `json:"user_id,omitempty"`
	Plan       string `json:"plan,omitempty"`
	Error      string `json:"error,omitempty"`

	DeviceID      string `json:"device_id,omitempty"`
	DeviceName    string `json:"device_name,omitempty"`
	LoggedInAt    string `json:"logged_in_at,omitempty"`
	TokenLastUsed string `json:"token_last_used_at,omitempty"`
	TokenExpires  string `json:"token_expires_at,omitempty"`
	OtherDevices  int    `json:"other_devices"`
	EncryptionKey bool   `json:"device_encryption_key"`
	SigningKey    bool   `json:"device_signing_key"`

	DaemonRunning bool              `json:"daemon_running"`
	DaemonVersion string            `json:"daemon_version,omitempty"`
	DaemonSince   string            `json:"daemon_since,omitempty"`
	BoundAgents   int               `json:"bound_agents"`
	Skills        map[string]string `json:"skills"`
}

// status: one screen with who this CLI is signed in as and how this machine is set up.
func (a app) status(ctx context.Context, args []string) int {
	asJSON := hasFlag(args, "--json")
	r := statusReport{Version: clicore.FullVersion(), Channel: a.updateChannelFor(""), Skills: map[string]string{}}
	if c, err := clicore.LoadUpdateCheckCache(); err == nil {
		r.LatestVersion = c.LatestVersion
	}

	client, credential, ok := a.authClient()
	r.LoggedIn = ok
	if ok {
		r.Server = credential.APIBase
		r.AuthMethod = "device login"
		if clicore.EnvAPIToken() != "" {
			r.AuthMethod = "API token (SHARE2US_API_TOKEN)"
		}
		r.Email, r.DeviceID = credential.Email, credential.DeviceSessionID
		r.EncryptionKey = credential.DevicePublicKey != "" && credential.DevicePrivateKey != ""
		r.SigningKey = credential.DeviceSigningPublicKey != "" && credential.DeviceSigningPrivateKey != ""
		// Devices first: its last-used time is the closest to "before this check".
		if devices, err := client.ListDevices(ctx); err == nil {
			for _, d := range devices.Sessions {
				if d.ID == credential.DeviceSessionID || (credential.DeviceSessionID == "" && d.Current) {
					r.DeviceID, r.DeviceName = d.ID, d.DeviceName
					r.LoggedInAt, r.TokenLastUsed, r.TokenExpires = d.CreatedAt, d.LastUsedAt, d.ExpiresAt
				} else {
					r.OtherDevices++
				}
			}
		}
		if me, err := client.Me(ctx); err == nil {
			r.AccountID, r.UserID, r.Plan = me.AccountID, me.UserID, me.PlanName
			if me.Email != "" {
				r.Email = me.Email
			}
		} else {
			r.Error = cliErrorMessage(err)
		}
	}

	if st, ok := daemonctl.Query("status"); ok && st.OK {
		r.DaemonRunning, r.DaemonVersion, r.DaemonSince = true, st.Version, st.Since
	}
	if list, err := daemon.LoadBindings(); err == nil {
		r.BoundAgents = len(list)
	}
	for _, t := range skillTargets {
		if skillPresent(t) {
			r.Skills[t.Tool] = skillState(t)
		}
	}

	if asJSON {
		writeJSON(a.stdout, r)
		return 0
	}
	a.printStatus(r, time.Now())
	if r.Error != "" {
		return 1
	}
	return 0
}

func (a app) printStatus(r statusReport, now time.Time) {
	row := func(k, v string) { fmt.Fprintf(a.stdout, "  %-20s %s\n", k, v) }
	when := func(ts string) string {
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			return ts
		}
		local := t.Local().Format("2006-01-02 15:04")
		if d := now.Sub(t); d >= 0 {
			if d < time.Minute {
				return local + " (just now)"
			}
			return local + " (" + humanSpan(d) + " ago)"
		}
		return local + " (in " + humanSpan(t.Sub(now)) + ")"
	}

	fmt.Fprintf(a.stdout, "%s %s\n", commandName, r.Version)
	version := r.Version + " (" + r.Channel + ")"
	if r.LatestVersion != "" && r.LatestVersion > r.Version {
		version += "  update available: " + r.LatestVersion + " (`" + commandName + " update`)"
	}
	row("Version", version)

	fmt.Fprintln(a.stdout, "\nAccount")
	if !r.LoggedIn {
		row("Signed in", "no (`"+commandName+" login`)")
	} else {
		who := r.Email
		if who == "" {
			who = r.UserID
		}
		row("Signed in as", who)
		if r.Plan != "" {
			row("Plan", r.Plan)
		}
		row("Auth", r.AuthMethod)
		row("Server", r.Server)
		if r.AccountID != "" {
			row("Account", r.AccountID)
		}
		if r.Error != "" {
			row("Problem", r.Error)
		}
	}

	if r.LoggedIn && r.DeviceID != "" {
		fmt.Fprintln(a.stdout, "\nThis device")
		name := r.DeviceName
		if name == "" {
			name = "(unnamed)"
		}
		row("Device", name+"  "+r.DeviceID)
		if r.LoggedInAt != "" {
			row("Logged in", when(r.LoggedInAt))
		}
		if r.TokenLastUsed != "" {
			// The server stamps every authenticated call, this check included.
			row("Token last used", when(r.TokenLastUsed)+", by this check")
		}
		if r.TokenExpires != "" {
			row("Token expires", when(r.TokenExpires))
		}
		keys := []string{}
		if r.EncryptionKey {
			keys = append(keys, "encryption")
		}
		if r.SigningKey {
			keys = append(keys, "signing")
		}
		if len(keys) == 0 {
			row("Device keys", "none (`"+commandName+" login` again to create them)")
		} else {
			row("Device keys", strings.Join(keys, ", "))
		}
		row("Other devices", fmt.Sprintf("%d (`%s devices`)", r.OtherDevices, commandName))
	}

	fmt.Fprintln(a.stdout, "\nThis machine")
	if r.DaemonRunning {
		d := "running " + r.DaemonVersion
		if r.DaemonSince != "" {
			d += ", since " + when(r.DaemonSince)
		}
		if r.DaemonVersion != "" && r.DaemonVersion != r.Version {
			d += "  (older than this CLI: `" + commandName + " daemon restart`)"
		}
		row("Daemon", d)
	} else {
		row("Daemon", "not running (`"+commandName+" daemon start`)")
	}
	row("Bound agents", fmt.Sprintf("%d (`%s agent bindings`)", r.BoundAgents, commandName))
	if len(r.Skills) == 0 {
		row("/s2u skill", "no agent tools found")
	}
	for _, t := range skillTargets {
		if st, ok := r.Skills[t.Tool]; ok {
			hint := ""
			if st == "missing" || st == "outdated" {
				hint = "  (`" + commandName + " agent skill install`)"
			}
			row("/s2u in "+t.Label, st+hint)
		}
	}
}

// humanSpan: "5 minutes", "3 hours", "49 days", "2 years".
func humanSpan(d time.Duration) string {
	unit := func(n int, one string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %ss", n, one)
	}
	switch {
	case d < time.Hour:
		return unit(int(d.Minutes()), "minute")
	case d < 48*time.Hour:
		return unit(int(d.Hours()), "hour")
	case d < 730*24*time.Hour:
		return unit(int(d.Hours()/24), "day")
	}
	return unit(int(d.Hours()/24/365), "year")
}
