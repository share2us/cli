package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	clicore "github.com/share2us/cli-core"
)

const secretTestValue = "ghp_TOPSECRETvalue123"

// secretTestEnv logs this test into a fake API whose account key is wrapped to this
// device, and serves one DM secret sealed to that key.
func secretTestEnv(t *testing.T, enrolled bool) (configDir string, hits *int) {
	t.Helper()
	configDir = t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHARE2US_API_TOKEN", "")
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("os.UserConfigDir ignores XDG_CONFIG_HOME here")
	}
	const acct = "acct-1"
	kp, err := clicore.NewDeviceKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	ak, _ := clicore.NewAccountKey()
	kr, err := clicore.NewKeyring(nil, acct, kp.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	devPub := kr.DevicePublicKey()
	pkcs8, _ := clicore.MarshalAccountKey(ak)
	wrap, err := clicore.WrapV1(ak, devPub, pkcs8, clicore.PurposeAccountKeyToDevice, clicore.DeviceWrapAAD(acct, kr.DeviceID()))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := clicore.SealDMSecret(ak.PublicKey().Bytes(), "sec-1", "dm-1", acct, "GitHub token", secretTestValue)
	if err != nil {
		t.Fatal(err)
	}
	sealed.SharenetID, sealed.Label = "net-1", "GitHub token"
	hits = new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/keys/devices/"+kr.DeviceID():
			if !enrolled {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":{"code":"not_enrolled","message":"no key"}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"approved": true, "device_public_key": devPub, "wrap": wrap, "account_public_key": ak.PublicKey().Bytes()})
		case r.URL.Path == "/v1/secrets/sec-1":
			*hits++
			_ = json.NewEncoder(w).Encode(sealed)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"not found"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	if err := clicore.SaveCredential(clicore.Credential{APIBase: srv.URL, Token: "s2s_test", AccountID: acct, DevicePublicKey: kp.PublicKey, DevicePrivateKey: kp.PrivateKey}); err != nil {
		t.Fatal(err)
	}
	return configDir, hits
}

func runSecret(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := app{stdout: &out, stderr: &errb}.run(context.Background(), args)
	return code, out.String(), errb.String()
}

func TestSecretGetDefaultPath(t *testing.T) {
	cfg, _ := secretTestEnv(t, true)
	code, out, errOut := runSecret("secret", "get", "sec-1")
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, out, errOut)
	}
	if strings.Contains(out+errOut, secretTestValue) {
		t.Fatal("the secret value was printed")
	}
	want := filepath.Join(cfg, "share2us", "secrets", "sec-1")
	if !strings.Contains(out, want) || !strings.Contains(out, "GitHub token") {
		t.Fatalf("output = %q", out)
	}
	got, err := os.ReadFile(want)
	if err != nil || string(got) != secretTestValue {
		t.Fatalf("file = %q, %v", got, err)
	}
	if st, _ := os.Stat(want); st.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Dir(want)); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v", st.Mode().Perm())
	}

	// No silent overwrite; --force replaces it (still 0600).
	if code, _, errOut := runSecret("secret", "get", "sec-1"); code == 0 || !strings.Contains(errOut, "--force") {
		t.Fatalf("overwrite: exit %d, %q", code, errOut)
	}
	_ = os.WriteFile(want, []byte("old"), 0o644)
	if code, out, errOut := runSecret("secret", "get", "sec-1", "--force"); code != 0 {
		t.Fatalf("--force: exit %d %s%s", code, out, errOut)
	}
	got, _ = os.ReadFile(want)
	st, _ := os.Stat(want)
	if string(got) != secretTestValue || st.Mode().Perm() != 0o600 {
		t.Fatalf("after --force: %q %v", got, st.Mode().Perm())
	}
}

func TestSecretGetOutputPath(t *testing.T) {
	_, hits := secretTestEnv(t, true)
	dest := filepath.Join(t.TempDir(), "token.txt")
	code, out, errOut := runSecret("secret", "get", "sec-1", "-o", dest)
	if code != 0 || !strings.Contains(out, dest) {
		t.Fatalf("exit %d: %s%s", code, out, errOut)
	}
	if got, _ := os.ReadFile(dest); string(got) != secretTestValue {
		t.Fatalf("file = %q", got)
	}
	// An existing -o target is refused before the secret is fetched again.
	before := *hits
	if code, _, _ := runSecret("secret", "get", "sec-1", "-o", dest); code == 0 || *hits != before {
		t.Fatalf("existing -o: exit %d, fetches %d -> %d", code, before, *hits)
	}
}

func TestSecretGetRefusals(t *testing.T) {
	secretTestEnv(t, true)
	if code, _, errOut := runSecret("secret", "get", "../etc"); code == 0 || !strings.Contains(errOut, "not a secret id") {
		t.Fatalf("bad id: exit %d %q", code, errOut)
	}
	if code, _, errOut := runSecret("secret", "get", "sec-2"); code == 0 || !strings.Contains(errOut, "not found") {
		t.Fatalf("missing: exit %d %q", code, errOut)
	}
	if code, _, _ := runSecret("secret"); code != 2 {
		t.Fatalf("bare secret: exit %d", code)
	}
}

func TestSecretGetWithoutAccountKey(t *testing.T) {
	secretTestEnv(t, false)
	code, _, errOut := runSecret("secret", "get", "sec-1")
	if code == 0 || !strings.Contains(errOut, "this device has no account key; run `"+commandName+" keys` (or approve it in the portal)") {
		t.Fatalf("exit %d %q", code, errOut)
	}
}

func TestSecretGetTool(t *testing.T) {
	cfg, _ := secretTestEnv(t, true)
	var tool func(context.Context, json.RawMessage) (string, bool)
	for _, tl := range (app{}).channelAgentTools() {
		if tl.Name == "secret_get" {
			if tl.NotDuringHop {
				t.Fatal("secret_get must work inside a delivered task")
			}
			tool = tl.Run
		}
	}
	if tool == nil {
		t.Fatal("no secret_get tool")
	}
	if text, isErr := tool(context.Background(), json.RawMessage(`{}`)); !isErr || !strings.Contains(text, "needs `id`") {
		t.Fatalf("no id: %q", text)
	}
	text, isErr := tool(context.Background(), json.RawMessage(`{"id":"sec-1"}`))
	if isErr || strings.Contains(text, secretTestValue) || !strings.Contains(text, filepath.Join(cfg, "share2us", "secrets", "sec-1")) || !strings.Contains(text, "GitHub token") {
		t.Fatalf("tool = %q (err %v)", text, isErr)
	}
}
