// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/share2us/cli-core/lanshare"
)

const testStageNonce = "abcdefghijklmnopqrstuv"
const testStageFingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestAgentFileStageMatchesPeerAndSweepsOrphans(t *testing.T) {
	stage, err := newAgentFileStage(filepath.Join(t.TempDir(), "pending"))
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.reserve(testStageNonce, testStageFingerprint, 5); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage.dir, testStageNonce), []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	wrong := strings.Repeat("b", 64)
	if _, found, err := stage.open(testStageNonce, wrong); found || err != nil {
		t.Fatalf("wrong sender opened staged file: found=%v err=%v", found, err)
	}
	r, found, err := stage.open(testStageNonce, testStageFingerprint)
	if err != nil || !found {
		t.Fatalf("matching sender could not open stage: found=%v err=%v", found, err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || string(got) != "bytes" {
		t.Fatalf("staged bytes = %q, %v", got, err)
	}
	if err := stage.sweep(time.Now().Add(agentStageTTL + time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stage.dir, testStageNonce)); !os.IsNotExist(err) {
		t.Fatalf("orphaned stage survived TTL sweep: %v", err)
	}
}

func TestAgentFileStageRejectsUnsafeNamesAndSymlinks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pending")
	if err := os.Symlink(t.TempDir(), dir); err == nil {
		if _, err := newAgentFileStage(dir); err == nil {
			t.Fatal("accepted symlinked stage directory")
		}
	} else {
		t.Logf("symlink test skipped: %v", err)
	}
	stage, err := newAgentFileStage(filepath.Join(t.TempDir(), "pending"))
	if err != nil {
		t.Fatal(err)
	}
	for _, nonce := range []string{"../escape", "short", testStageNonce + ".peer"} {
		if err := stage.reserve(nonce, testStageFingerprint, 5); err == nil {
			t.Fatalf("accepted unsafe nonce %q", nonce)
		}
	}
	if err := stage.reserve(testStageNonce, testStageFingerprint, agentStageMaxBytes+1); err == nil {
		t.Fatal("accepted oversized stage")
	}
	if err := stage.reserve(testStageNonce, testStageFingerprint, 51<<20); err != nil {
		t.Fatalf("LAN transfer over the relay's 50 MiB cap was refused: %v", err)
	}
	if err := stage.reserve(testStageNonce, testStageFingerprint, 5); !os.IsExist(err) {
		t.Fatalf("duplicate stage accepted: %v", err)
	}
}

func TestAgentFileLANPushRequiresTrustedIdentity(t *testing.T) {
	stage, err := newAgentFileStage(filepath.Join(t.TempDir(), "pending"))
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("verified LAN public key")
	r := lanshare.RequestInfo{Name: testStageNonce, Size: 5, SenderKey: key}
	if err := acceptAgentFilePush(stage, func([]byte) bool { return false }, r); err == nil {
		t.Fatal("untrusted LAN sender was admitted")
	}
	if err := acceptAgentFilePush(stage, func([]byte) bool { return true }, r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stage.dir, testStageNonce+".peer")); err != nil {
		t.Fatalf("trusted sender was not reserved: %v", err)
	}
}

func TestAgentFileLANPushStagesCiphertextBeforeAcknowledgement(t *testing.T) {
	stage, err := newAgentFileStage(filepath.Join(t.TempDir(), "pending"))
	if err != nil {
		t.Fatal(err)
	}
	_, receiverKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	senderPub, senderKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	listening := make(chan lanshare.ListenInfo, 1)
	done := make(chan error, 1)
	go func() {
		_, err := lanshare.Receive(ctx, lanshare.ReceiveOptions{
			Bind: "127.0.0.1", NoPassword: true, Identity: receiverKey,
			DestDir: stage.dir, OnListen: func(info lanshare.ListenInfo) { listening <- info },
			OnRequest: func(r lanshare.RequestInfo) bool {
				return acceptAgentFilePush(stage, func(key []byte) bool { return bytes.Equal(key, senderPub) }, r) == nil
			},
		})
		done <- err
	}()
	var info lanshare.ListenInfo
	select {
	case info = <-listening:
	case <-time.After(5 * time.Second):
		t.Fatal("LAN receiver did not listen")
	}
	ciphertext := []byte("ciphertext on the wire")
	_, err = lanshare.Send(ctx, testStageNonce, int64(len(ciphertext)), false, bytes.NewReader(ciphertext),
		lanshare.SendOptions{Dest: "127.0.0.1:" + strconv.Itoa(info.Port), PinFingerprint: info.Fingerprint, Identity: senderKey})
	if err != nil {
		t.Fatal(err)
	}
	reader, found, err := stage.open(testStageNonce, lanshare.IdentityFingerprint(senderPub))
	if err != nil || !found {
		t.Fatalf("ack arrived before staged bytes: found=%v err=%v", found, err)
	}
	got, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || !bytes.Equal(got, ciphertext) {
		t.Fatalf("staged ciphertext = %q, %v", got, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LAN receiver did not finish")
	}
}
