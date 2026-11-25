// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	clicore "github.com/share2us/cli-core"
)

// Final states of requests THIS device sent (todo ZT item 2). The server hands each one
// back once (GET /v1/agent/updates); the daemon shows a notification and keeps a local
// log that `s2u agent results` reads, so the sender learns the outcome without polling
// `agent status`. Results are deliberately NOT typed into the sending agent's session:
// they are text from the other side, and injecting them would be an unguarded reverse hop.

// agentUpdatesClient is the optional capability (the real client has it).
type agentUpdatesClient interface {
	AgentUpdates(ctx context.Context) ([]clicore.AgentUpdate, error)
}

var sentResultsPoll = 10 * time.Second

const sentResultsKeep = 200

// SentResult is one line of the local sent-results log.
type SentResult struct {
	Time          time.Time `json:"time"`
	RequestID     string    `json:"request_id"`
	Status        string    `json:"status"`
	Result        string    `json:"result"`
	Tool          string    `json:"tool"`
	TargetSession string    `json:"target_session"`
	SenderSession string    `json:"sender_session,omitempty"`
}

func sentResultsPath() (string, error) {
	base, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "share2us", "agents", "sent-results.jsonl"), nil
}

// LoadSentResults returns the newest n results (all when n <= 0), oldest first.
func LoadSentResults(n int) ([]SentResult, error) {
	path, err := sentResultsPath()
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
	var out []SentResult
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var r SentResult
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			out = append(out, r)
		}
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out, sc.Err()
}

func appendSentResults(add []SentResult) error {
	path, err := sentResultsPath()
	if err != nil {
		return err
	}
	all, _ := LoadSentResults(0)
	all = append(all, add...)
	if len(all) > sentResultsKeep {
		all = all[len(all)-sentResultsKeep:]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sent-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	w := bufio.NewWriter(tmp)
	for _, r := range all {
		b, _ := json.Marshal(r)
		w.Write(append(b, '\n'))
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// watchSentResults collects final states of requests this device sent.
func (rt *Runtime) watchSentResults(ctx context.Context, client AgentClient, deps Deps) {
	uc, ok := client.(agentUpdatesClient)
	if !ok {
		return
	}
	poll := sentResultsPoll
	for {
		rt.collectSentResults(ctx, uc, deps)
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

func (rt *Runtime) collectSentResults(ctx context.Context, uc agentUpdatesClient, deps Deps) {
	updates, err := uc.AgentUpdates(ctx)
	if err != nil || len(updates) == 0 {
		return // an older server has no updates endpoint: stay quiet
	}
	if err := RecordSentUpdates(updates); err != nil {
		deps.logf("agent-bridge: could not save sent results: %v", err)
	}
	for _, u := range updates {
		rt.notify("Share2Us agent request "+u.Status, sentResultSummary(u))
		if u.SenderSessionID != "" {
			rt.hub().notice(u.SenderSessionID, resultNotice(u))
		}
	}
}

// RecordSentUpdates appends claimed final states to the local sent-results log. The
// server hands each one out once, so whoever claims it (daemon or `agent results`) must
// record it.
func RecordSentUpdates(updates []clicore.AgentUpdate) error {
	recs := make([]SentResult, 0, len(updates))
	for _, u := range updates {
		recs = append(recs, SentResult{Time: time.Now().UTC(), RequestID: u.ID, Status: u.Status, Result: CleanRemoteText(u.Result),
			Tool: u.Tool, TargetSession: u.TargetSessionID, SenderSession: u.SenderSessionID})
	}
	return appendSentResults(recs)
}

// sentResultSummary is a one-line notification body.
func sentResultSummary(u clicore.AgentUpdate) string {
	id := u.ID
	if len(id) > 8 {
		id = id[:8]
	}
	first := strings.TrimSpace(strings.SplitN(strings.TrimSpace(CleanRemoteText(u.Result)), "\n", 2)[0])
	if r := []rune(first); len(r) > 140 {
		first = string(r[:140]) + "…"
	}
	if first == "" {
		return "Request " + id + " is " + u.Status + "."
	}
	return "Request " + id + " (" + u.Status + "): " + first
}

// CleanRemoteText drops control characters (ANSI escapes included) from text another
// device wrote, before it reaches a terminal or a desktop notification.
func CleanRemoteText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, s)
}

// resultNotice tells the sending session its request finished. It never carries the
// target's reply: that text comes from another agent, and a channel event reads as a
// task, so the session fetches the reply itself (a command's output is data to it).
func resultNotice(u clicore.AgentUpdate) ChannelDelivery {
	target := u.TargetSessionID
	if target == "" {
		target = "the target session"
	}
	return ChannelDelivery{RequestID: u.ID, Prompt: fmt.Sprintf(
		"Your Share2Us request %s to %s finished with status %q. This is a status notice, not a task. To read the reply, run: s2u agent status %s",
		u.ID, target, u.Status, u.ID)}
}
