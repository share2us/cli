// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package main

import (
	"context"
	"sync"
	"time"

	clicore "github.com/share2us/cli-core"
	"github.com/share2us/cli/internal/daemon"
)

// projectSecureCache remembers whether a project's sharenet is Secure (it can't change after
// creation), so every reply doesn't list the account's sharenets again.
var projectSecureCache = struct {
	sync.Mutex
	m map[string]clicore.SharenetSummary
	t map[string]time.Time
}{m: map[string]clicore.SharenetSummary{}, t: map[string]time.Time{}}

// projectSharenet finds the caller's sharenet holding projectID. found=false when this
// account isn't a member (an agent of a non-member in someone else's project): it can't hold
// that sharenet's key, and the reply goes as is (ADR-053 gap, noted in the ADR).
func (a app) projectSharenet(ctx context.Context, client *clicore.Client, projectID string) (clicore.SharenetSummary, bool, error) {
	projectSecureCache.Lock()
	if n, ok := projectSecureCache.m[projectID]; ok && time.Since(projectSecureCache.t[projectID]) < 10*time.Minute {
		projectSecureCache.Unlock()
		return n, n.ID != "", nil
	}
	projectSecureCache.Unlock()
	n, found, err := client.SharenetForProject(ctx, projectID, false)
	if err != nil {
		return n, false, err
	}
	projectSecureCache.Lock()
	projectSecureCache.m[projectID], projectSecureCache.t[projectID] = n, time.Now()
	projectSecureCache.Unlock()
	return n, found, nil
}

// sealTaskReply is the daemon's Deps.SealTaskReply.
func (a app) sealTaskReply(ctx context.Context, projectID, requestID, reply string) (string, bool, error) {
	client, _, ok := a.authClient()
	if !ok {
		return reply, false, nil
	}
	n, found, err := a.projectSharenet(ctx, client, projectID)
	if err != nil {
		return "", false, err
	}
	if !found || !n.Secure {
		return reply, false, nil
	}
	sn, _, err := a.secureSessionMode(ctx, true)
	if err != nil {
		return "", true, err
	}
	sealed, err := sn.SealTaskReply(ctx, n.ID, requestID, reply)
	return sealed, true, err
}

// openTaskReply opens a sealed reply with this device's keys, else a placeholder.
func (a app) openTaskReply(ctx context.Context, requestID, result string) string {
	if !clicore.IsSealedReply(result) {
		return result
	}
	sn, _, err := a.secureSession(ctx)
	if err != nil {
		return daemon.SealedReplyPlaceholder
	}
	pt, err := sn.OpenTaskReply(ctx, requestID, result)
	if err != nil {
		return daemon.SealedReplyPlaceholder
	}
	return pt
}
