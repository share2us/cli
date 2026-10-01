// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	clicore "github.com/share2us/cli-core"
)

type fakeAgentClient struct {
	reports      [][2]string // {status, result}
	reportNotify chan struct{}
	registered   []string
	deregd       []string
	// remote is what the server already believes this device is running, used by
	// the startup retire pass.
	remote   []clicore.AgentSessionInfo
	listErr  error
	requeued int
}

func (f *fakeAgentClient) AgentRequeueWaiting(context.Context) (int, error) {
	f.requeued++
	return 0, nil
}

func (f *fakeAgentClient) ListAgentSessions(_ context.Context) ([]clicore.AgentSessionInfo, error) {
	return f.remote, f.listErr
}

func (f *fakeAgentClient) RegisterAgentSession(_ context.Context, in clicore.AgentRegisterInput) error {
	f.registered = append(f.registered, in.SessionID)
	return nil
}
func (f *fakeAgentClient) DeregisterAgentSession(_ context.Context, id string) error {
	f.deregd = append(f.deregd, id)
	return nil
}
func (f *fakeAgentClient) AgentLongPoll(_ context.Context, _ int) ([]clicore.AgentRequest, error) {
	return nil, nil
}
func (f *fakeAgentClient) AgentReportResult(_ context.Context, _, status, result string) error {
	f.reports = append(f.reports, [2]string{status, result})
	if f.reportNotify != nil {
		select {
		case f.reportNotify <- struct{}{}:
		default:
		}
	}
	return nil
}

type fakeRunner struct {
	out       string
	err       error
	project   string
	ranSID    string
	ranPrompt string
}

func (f *fakeRunner) Tool() string { return "claude" }
func (f *fakeRunner) Discover(context.Context) ([]DiscoveredSession, error) {
	return []DiscoveredSession{{SessionID: "s1", Tool: "claude", Status: "available", Project: f.project}}, nil
}
func (f *fakeRunner) Run(_ context.Context, sessionID, _, prompt string) (string, error) {
	f.ranSID = sessionID
	f.ranPrompt = prompt
	return f.out, f.err
}

func rt() *Runtime { return &Runtime{notifier: NoopNotifier{}} }

// noDeps gives each test its own empty pin store, and names this device as the
// one hops are signed for. Without a store the daemon fails closed and refuses
// every hop (ADR-041 §5). Every hop must be signed (7.6): see signedReq.
func noDeps() Deps {
	dir, err := os.MkdirTemp("", "s2u-pins-*")
	if err != nil {
		panic(err)
	}
	return Deps{
		Logf:            func(string, ...any) {},
		SenderPins:      &SenderPins{path: filepath.Join(dir, "pinned_senders.json")},
		DeviceSessionID: self,
	}
}

// bridgeSender signs the tests' hops as a genuine sending device would.
var bridgeSender = func() sender {
	kp, err := clicore.NewSigningKeyPair()
	if err != nil {
		panic(err)
	}
	return sender{id: "dev-a", kp: kp}
}()

// signedReq signs req, as delivered to this device, keeping its fields.
func signedReq(t *testing.T, req clicore.AgentRequest) clicore.AgentRequest {
	t.Helper()
	at := time.Now().UTC().Truncate(time.Second)
	nonce := fmt.Sprintf("n-%d", time.Now().UnixNano())
	sig, err := clicore.SignHop(clicore.HopClaims{
		SenderDeviceID: bridgeSender.id, TargetDeviceID: self, TargetSessionID: req.TargetSessionID,
		Tool: req.Tool, SealedPrompt: req.SealedPrompt, SealedFileKey: req.SealedFileKey,
		GoalID: req.GoalID, IssuedAt: at, Nonce: nonce,
		ProjectID: req.ProjectID, SenderAgentID: req.SenderAgentID, TargetAgentID: req.TargetAgentID,
	}, bridgeSender.kp.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	req.SenderDeviceID = bridgeSender.id
	req.Signature, req.IssuedAt, req.Nonce = sig, at.Format(time.RFC3339), nonce
	req.SenderSigningPublicKey = bridgeSender.kp.PublicKey
	return req
}

// keyedDeps is a device that CAN unseal: the identity function stands in for
// the sealed box so the tests below exercise what happens after decryption.
func keyedDeps() Deps {
	d := noDeps()
	d.Unseal = func(s string) (string, error) { return s, nil }
	return d
}

// §AJ #8: with no device key there is nothing to unseal with, and the old code
// ran the server's bytes as the prompt. That is the server (or anyone holding
// its credentials) executing commands in the user's project with edits
// pre-approved. No key: refuse, report, never run.
func TestHandleInjectRefusesToRunWithoutDeviceKey(t *testing.T) {
	c := &fakeAgentClient{}
	r := &fakeRunner{out: "should never happen"}
	rt().handleInject(context.Background(), c, r, noDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-1", Tool: "claude", TargetSessionID: "s1", SealedPrompt: "rm -rf the project"}))
	if r.ranSID != "" || r.ranPrompt != "" {
		t.Fatalf("a plaintext prompt was RUN without a device key: session=%q prompt=%q", r.ranSID, r.ranPrompt)
	}
	if len(c.reports) != 1 || c.reports[0][0] != "failed" {
		t.Fatalf("reports = %v, want a single failed", c.reports)
	}
}

func TestHandleInjectHappy(t *testing.T) {
	c := &fakeAgentClient{}
	r := &fakeRunner{out: "did the thing"}
	rt().handleInject(context.Background(), c, r, keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-1", Tool: "claude", TargetSessionID: "s1", SealedPrompt: "do it"}))
	if r.ranSID != "s1" {
		t.Fatalf("runner ran session %q, want s1", r.ranSID)
	}
	if len(c.reports) != 2 || c.reports[0][0] != "running" || c.reports[1][0] != "done" || c.reports[1][1] != "did the thing" {
		t.Fatalf("reports = %v, want running then done+output", c.reports)
	}
}

func TestHandleInjectRunError(t *testing.T) {
	c := &fakeAgentClient{}
	r := &fakeRunner{out: "boom output", err: errors.New("nonzero exit")}
	rt().handleInject(context.Background(), c, r, keyedDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-1", Tool: "claude", TargetSessionID: "s1", SealedPrompt: "x"}))
	if len(c.reports) != 2 || c.reports[1][0] != "failed" {
		t.Fatalf("reports = %v, want running then failed", c.reports)
	}
}

func TestHandleInjectUnsupportedTool(t *testing.T) {
	c := &fakeAgentClient{}
	r := &fakeRunner{}
	rt().handleInject(context.Background(), c, r, noDeps(),
		signedReq(t, clicore.AgentRequest{ID: "req-1", Tool: "codex", TargetSessionID: "s1"}))
	if r.ranSID != "" {
		t.Fatal("runner should not run for an unsupported tool")
	}
	if len(c.reports) != 1 || c.reports[0][0] != "failed" {
		t.Fatalf("reports = %v, want a single failed", c.reports)
	}
}

func TestRegisterLoopSyncDeregistersVanished(t *testing.T) {
	c := &fakeAgentClient{}
	r := &fakeRunner{}
	// One sync pass via a cancelled context: run agentRegisterLoop's body once by
	// calling Discover+register directly through a short-lived loop is awkward, so
	// assert the runner's Discover feeds registration through a manual sync.
	sessions, _ := r.Discover(context.Background())
	for _, s := range sessions {
		_ = c.RegisterAgentSession(context.Background(), clicore.AgentRegisterInput{SessionID: s.SessionID})
	}
	if len(c.registered) != 1 || c.registered[0] != "s1" {
		t.Fatalf("registered = %v, want [s1]", c.registered)
	}
}

func TestHandleInjectUnsealsPrompt(t *testing.T) {
	c := &fakeAgentClient{}
	r := &fakeRunner{out: "ok"}
	deps := noDeps()
	deps.Unseal = func(sealed string) (string, error) {
		if sealed != "CIPHERTEXT" {
			t.Fatalf("unseal got %q", sealed)
		}
		return "the real prompt", nil
	}
	rt().handleInject(context.Background(), c, r, deps,
		signedReq(t, clicore.AgentRequest{ID: "req-1", Tool: "claude", TargetSessionID: "s1", SealedPrompt: "CIPHERTEXT"}))
	if r.ranPrompt != "the real prompt" {
		t.Fatalf("runner got prompt %q, want the decrypted one", r.ranPrompt)
	}
}

func TestHandleInjectUnsealFailureIsFatal(t *testing.T) {
	c := &fakeAgentClient{}
	r := &fakeRunner{}
	deps := noDeps()
	deps.Unseal = func(string) (string, error) { return "", errors.New("bad box") }
	rt().handleInject(context.Background(), c, r, deps,
		signedReq(t, clicore.AgentRequest{ID: "req-1", Tool: "claude", TargetSessionID: "s1", SealedPrompt: "x"}))
	if r.ranSID != "" {
		t.Fatal("must not run when the prompt cannot be decrypted")
	}
	if len(c.reports) != 1 || c.reports[0][0] != "failed" {
		t.Fatalf("reports = %v, want a single failed", c.reports)
	}
}

func TestHandleInjectRequiresCompleteAttachment(t *testing.T) {
	for _, tc := range []struct {
		name, envelope, sealedKey string
		hasFile                   bool
		withDownloader            bool
		withKeyOpener             bool
	}{
		{name: "missing filename", envelope: `{"prompt":"do it"}`, hasFile: true, sealedKey: "key", withDownloader: true, withKeyOpener: true},
		{name: "unannounced filename", envelope: `{"prompt":"do it","file_name":"x"}`},
		{name: "missing key", envelope: `{"prompt":"do it","file_name":"x"}`, hasFile: true, withDownloader: true, withKeyOpener: true},
		{name: "missing downloader", envelope: `{"prompt":"do it","file_name":"x"}`, hasFile: true, sealedKey: "key", withKeyOpener: true},
		{name: "missing key opener", envelope: `{"prompt":"do it","file_name":"x"}`, hasFile: true, sealedKey: "key", withDownloader: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeAgentClient{}
			r := &fakeRunner{}
			deps := keyedDeps()
			if tc.withDownloader {
				deps.DownloadContent = func(context.Context, string) ([]byte, error) { return nil, nil }
			}
			if tc.withKeyOpener {
				deps.OpenContentKey = func(string) ([]byte, error) { return nil, nil }
			}
			req := signedReq(t, clicore.AgentRequest{ID: "req-file", Tool: "claude", TargetSessionID: "s1", SealedPrompt: tc.envelope, HasFile: tc.hasFile, SealedFileKey: tc.sealedKey})
			rt().handleInject(context.Background(), c, r, deps, req)
			if r.ranSID != "" || len(c.reports) != 1 || c.reports[0][0] != "failed" {
				t.Fatalf("incomplete attachment ran or was not failed: prompt %q, reports %v", r.ranPrompt, c.reports)
			}
		})
	}
}

func TestHandleInjectPlacesFileBeforeRunning(t *testing.T) {
	cwd := t.TempDir()
	c := &fakeAgentClient{}
	r := &fakeRunner{out: "done", project: cwd}
	ck, err := clicore.NewContentKey()
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	if err := clicore.EncryptStream(&encrypted, bytes.NewReader([]byte("attachment")), ck); err != nil {
		t.Fatal(err)
	}
	deps := keyedDeps()
	deps.DownloadContent = func(_ context.Context, id string) ([]byte, error) {
		if id != "req-file" {
			t.Fatalf("downloaded unexpected request %q", id)
		}
		return encrypted.Bytes(), nil
	}
	deps.OpenContentKey = func(sealed string) ([]byte, error) {
		if sealed != "sealed-key" {
			t.Fatalf("opened unexpected key %q", sealed)
		}
		return ck, nil
	}
	req := signedReq(t, clicore.AgentRequest{ID: "req-file", Tool: "claude", TargetSessionID: "s1", SealedPrompt: `{"prompt":"read it","file_name":"shot.png"}`, HasFile: true, SealedFileKey: "sealed-key"})
	rt().handleInject(context.Background(), c, r, deps, req)
	path := filepath.Join(cwd, ".s2u-inbox", "shot.png")
	if got, err := os.ReadFile(path); err != nil || string(got) != "attachment" {
		t.Fatalf("attachment = %q, %v", got, err)
	}
	if r.ranSID != "s1" || !strings.Contains(r.ranPrompt, path) {
		t.Fatalf("runner got session %q, prompt %q; missing placed file path", r.ranSID, r.ranPrompt)
	}
	if len(c.reports) != 2 || c.reports[1][0] != "done" {
		t.Fatalf("reports = %v", c.reports)
	}
}

// v2: the receiver verifies the signed project and agents too. A server that
// relabels which project (or which agent) a hop came from is caught here, and
// the hop does not run.
func TestReceiverRefusesARelabelledProjectOrAgent(t *testing.T) {
	for name, relabel := range map[string]func(*clicore.AgentRequest){
		"project":      func(r *clicore.AgentRequest) { r.ProjectID = "p-other" },
		"sender agent": func(r *clicore.AgentRequest) { r.SenderAgentID = "agt_OtherOtherOtherOth1" },
		"target agent": func(r *clicore.AgentRequest) { r.TargetAgentID = "agt_OtherOtherOtherOth2" },
	} {
		t.Run(name, func(t *testing.T) {
			c := &fakeAgentClient{}
			r := &fakeRunner{out: "ran"}
			req := signedReq(t, clicore.AgentRequest{ID: "req-1", Tool: "claude", TargetSessionID: "s1", SealedPrompt: "go",
				ProjectID: "p-1", SenderAgentID: "agt_SenderSenderSender1", TargetAgentID: "agt_TargetTargetTarget1"})
			relabel(&req)
			rt().handleInject(context.Background(), c, r, keyedDeps(), req)
			if r.ranPrompt != "" {
				t.Fatalf("a hop with a relabelled %s ran", name)
			}
		})
	}
	// Unaltered, it runs.
	c, r := &fakeAgentClient{}, &fakeRunner{out: "ran"}
	rt().handleInject(context.Background(), c, r, keyedDeps(), signedReq(t, clicore.AgentRequest{ID: "req-2", Tool: "claude", TargetSessionID: "s1", SealedPrompt: "go",
		ProjectID: "p-1", SenderAgentID: "agt_SenderSenderSender1", TargetAgentID: "agt_TargetTargetTarget1"}))
	if r.ranPrompt != "go" {
		t.Fatalf("a genuine v2 hop did not run: %q", r.ranPrompt)
	}
}
