package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	clicore "github.com/share2us/cli-core"
)

func TestRunHelpAndVersion(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{name: "prints help by default", args: nil, wantCode: 0, wantOut: "share2us login"},
		{name: "prints version", args: []string{"version"}, wantCode: 0, wantOut: "share2us " + clicore.FullVersion()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			gotCode := run(tt.args, &stdout, &stderr)
			if gotCode != tt.wantCode {
				t.Fatalf("code = %d, want %d stderr=%s", gotCode, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.wantOut) {
				t.Fatalf("stdout missing %q in:\n%s", tt.wantOut, stdout.String())
			}
		})
	}
}

func TestCLIErrorMessageFor429AndCloudflare(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "rate limited wait hint",
			err:  &clicore.APIError{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "rate limited by the server; try again in ~3s"},
			want: "rate limited by the server; try again in ~3s",
		},
		{
			name: "blocked appeal message",
			err:  &clicore.APIError{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "Access temporarily blocked due to suspicious activity. If this is a mistake, appeal at https://app.share2.us/appeal"},
			want: "Access temporarily blocked due to suspicious activity. If this is a mistake, appeal at https://app.share2.us/appeal",
		},
		{
			name: "cloudflare challenge",
			err:  &clicore.APIError{Status: http.StatusForbidden, Code: "cloudflare_challenge", Message: "the server returned a Cloudflare challenge that the CLI can't solve - open the share link in a browser, or if this persists contact support@share2.us"},
			want: "the server returned a Cloudflare challenge that the CLI can't solve - open the share link in a browser, or if this persists contact support@share2.us",
		},
		{
			name: "existing api errors unchanged",
			err:  &clicore.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "still json"},
			want: "bad_request: still json",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cliErrorMessage(tt.err); got != tt.want {
				t.Fatalf("cliErrorMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAuthClientHonorsAPITokenEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no on-disk credential
	t.Setenv("SHARE2US_API_BASE", "")
	t.Setenv("SHARE2US_SHARE_BASE_URL", "")
	t.Setenv("SHARE2US_BASE_URL", "")
	t.Setenv("SHARE2US_API_TOKEN", "s2u_pat_env123")

	a := app{stdout: io.Discard, stderr: io.Discard}
	client, cred, ok := a.authClient()
	if !ok || client == nil {
		t.Fatal("authClient should succeed using SHARE2US_API_TOKEN even with no saved login")
	}
	if cred.Token != "s2u_pat_env123" {
		t.Fatalf("cred.Token = %q, want the env PAT", cred.Token)
	}
	if !clicore.IsAPIToken(cred.Token) {
		t.Fatal("env credential should be recognised as an API token")
	}

	// The device-key path must refuse a PAT (no device identity).
	if _, err := ensureDeviceKey(context.Background(), client, cred); err == nil {
		t.Fatal("ensureDeviceKey should reject a personal API token")
	}
}

func TestConfigCommands(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SHARE2US_API_BASE", "")
	t.Setenv("SHARE2US_SHARE_BASE_URL", "")
	t.Setenv("SHARE2US_SHARE_BASE", "")
	t.Setenv("SHARE2US_BASE_URL", "")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"config", "set-base-url", "https://api.staging.example.test/path"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("set-base-url code = %d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"Base URL set to staging.example.test", "API base: https://api.staging.example.test", "Share base: https://s.staging.example.test"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("set-base-url stdout missing %q in:\n%s", want, stdout.String())
		}
	}
	config, err := clicore.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if config.BaseURL != "staging.example.test" || config.Host != "" || config.ShareBase != "" {
		t.Fatalf("config after set-base-url = %+v", config)
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"config", "show"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("show code = %d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"Base URL: staging.example.test", "Base URL source: config", "API base: https://api.staging.example.test", "API base source: config", "Share base: https://s.staging.example.test", "Share base source: config"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("show output missing %q in:\n%s", want, stdout.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"config", "set-host", "https://api.staging.example.test/"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("set-host code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "https://api.staging.example.test") {
		t.Fatalf("set-host stdout = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"config", "show"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("show code = %d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"Base URL: staging.example.test", "API base: https://api.staging.example.test", "API base source: config", "Share base: https://s.staging.example.test"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("show output missing %q in:\n%s", want, stdout.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"config", "set-host", "ftp://api.example.test"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("invalid set-host code = %d, want 2", code)
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"config", "set-base-url", "not a host"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("invalid set-base-url code = %d, want 2", code)
	}
}

func TestUpdateDownloadsVerifiesAndReplacesCurrentBinary(t *testing.T) {
	target := filepath.Join(t.TempDir(), "share2us")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	archive := testUpdateArchive(t, []byte("new binary"))
	checksum, size, err := posixCKSUM(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("cksum archive: %v", err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/cli/update":
			writeTestJSON(w, map[string]any{
				"current_version":  clicore.FullVersion(),
				"latest_version":   "20260708123045",
				"update_available": true,
				"platform":         runtime.GOOS + "/" + runtime.GOARCH,
				"downloads": map[string]any{
					"archive_url": server.URL + "/downloads/share2us_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz",
					"crc32":       fmt.Sprint(checksum),
					"size_bytes":  size,
				},
			})
		case strings.HasSuffix(r.URL.Path, ".tar.gz"):
			w.Write(archive)
		default:
			t.Fatalf("unexpected update path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	testApp := app{
		stdout: &stdout,
		stderr: &stderr,
		executablePath: func() (string, error) {
			return target, nil
		},
	}
	code := testApp.run(context.Background(), []string{"update", "--host", server.URL})

	if code != 0 {
		t.Fatalf("code = %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	updated, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(updated) != "new binary" {
		t.Fatalf("updated target = %q", updated)
	}
	for _, want := range []string{"Updating share2us", "Downloading " + server.URL, "CRC check passed", "Updated share2us to 20260708123045"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q in:\n%s", want, stdout.String())
		}
	}
}

func TestSuccessfulInteractiveCommandPrintsUpdateNotice(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	withCredential(t, "https://api.example.test")
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/account/onboarding":
			writeTestJSON(w, map[string]any{"onboarded": true, "consent_required": false, "consent_version": "2026-07-14"})
		case "/v1/auth/me":
			writeTestJSON(w, map[string]string{"user_id": "user-1", "email": "user@example.test"})
		case "/v1/cli/install":
			writeTestJSON(w, map[string]any{"recorded": true})
		case "/v1/cli/update":
			writeTestJSON(w, map[string]any{
				"current_version":  clicore.FullVersion(),
				"latest_version":   "20260708123045",
				"update_available": true,
				"platform":         runtime.GOOS + "/" + runtime.GOARCH,
				"downloads":        map[string]any{"archive_url": "https://share2.us/downloads/share2us_linux_amd64.tar.gz", "crc32": "1", "size_bytes": 2},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	testApp := app{
		stdout: &stdout,
		stderr: &stderr,
		stdoutIsTTY: func(io.Writer) bool {
			return true
		},
	}
	code := testApp.run(context.Background(), []string{"whoami"})

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Update available: share2us 20260708123045. Run: share2us update") {
		t.Fatalf("stderr missing update notice: %s", stderr.String())
	}
	cache, err := clicore.LoadUpdateCheckCache()
	if err != nil {
		t.Fatalf("LoadUpdateCheckCache() error = %v", err)
	}
	if cache.LatestVersion != "20260708123045" || cache.LastCheckedAt.IsZero() {
		t.Fatalf("cache = %+v", cache)
	}
}

func TestLoginHostPersistsConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SHARE2US_API_BASE", "")
	t.Setenv("SHARE2US_DEVICE_NAME", "Env Device")
	var deviceRequest struct {
		DeviceName    string `json:"device_name"`
		MachineID     string `json:"machine_id"`
		OS            string `json:"os"`
		Arch          string `json:"arch"`
		ClientVersion string `json:"client_version"`
	}
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/device-codes":
			if err := json.NewDecoder(r.Body).Decode(&deviceRequest); err != nil {
				t.Fatalf("decode device request: %v", err)
			}
			writeTestJSON(w, map[string]any{
				"device_code":               "dev-1",
				"user_code":                 "ABCD-1234",
				"verification_uri":          "https://app.example.test/activate",
				"verification_uri_complete": "https://app.example.test/activate?code=ABCD-1234",
				"interval":                  1,
				"expires_in":                600,
			})
		case "/v1/auth/device-codes/dev-1/token":
			writeTestJSON(w, map[string]any{"credential": "s2s_test", "device_session_id": "sess-1"})
		case "/v1/auth/devices/key":
			if r.Header.Get("Authorization") != "Bearer s2s_test" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			var body struct {
				PublicKey string `json:"public_key"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode device key request: %v", err)
			}
			if body.PublicKey == "" {
				t.Fatal("missing device public key")
			}
			writeTestJSON(w, map[string]string{"status": "ok"})
		case "/v1/account/onboarding":
			writeTestJSON(w, map[string]any{"onboarded": true, "consent_required": false, "consent_version": "2026-07-14"})
		case "/v1/auth/me":
			if r.Header.Get("Authorization") != "Bearer s2s_test" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			writeTestJSON(w, map[string]string{"user_id": "user-1", "email": "user@example.test"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"login", "--host", "https://api.login.example.test/", "--device-name", "Flag Device", "--no-browser"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("login code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Logged in as user@example.test") {
		t.Fatalf("login stdout = %q", stdout.String())
	}
	for _, want := range []string{"To authorize this device, visit:", "https://app.example.test/activate?code=ABCD-1234", "Waiting for approval..."} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("login stdout missing %q in:\n%s", want, stdout.String())
		}
	}
	config, err := clicore.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if config.Host != "https://api.login.example.test" {
		t.Fatalf("config host = %q", config.Host)
	}
	credential, err := clicore.LoadCredential()
	if err != nil {
		t.Fatalf("LoadCredential() error = %v", err)
	}
	if credential.APIBase != "https://api.login.example.test" {
		t.Fatalf("credential APIBase = %q", credential.APIBase)
	}
	if deviceRequest.DeviceName != "Flag Device" || deviceRequest.MachineID == "" || deviceRequest.OS == "" || deviceRequest.Arch == "" {
		t.Fatalf("device request = %+v", deviceRequest)
	}
	if deviceRequest.ClientVersion != clicore.FullVersion() {
		t.Fatalf("client version = %q", deviceRequest.ClientVersion)
	}
}

func TestLoginDeviceLimitCanSignOutAndContinue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SHARE2US_API_BASE", "")
	polls := 0
	revoked := false
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/device-codes":
			writeTestJSON(w, map[string]any{
				"device_code":               "dev-1",
				"user_code":                 "ABCD-1234",
				"verification_uri":          "https://app.example.test/activate",
				"verification_uri_complete": "https://app.example.test/activate?code=ABCD-1234",
				"interval":                  1,
				"expires_in":                600,
			})
		case "/v1/auth/device-codes/dev-1/token":
			polls++
			if polls == 1 {
				w.WriteHeader(http.StatusForbidden)
				writeTestJSON(w, map[string]any{
					"error": map[string]string{"code": "device_limit_reached", "message": "device/session limit reached"},
					"limit": 2,
					"sessions": []map[string]any{{
						"id":           "sess-old",
						"device_name":  "Old laptop",
						"client_type":  "cli",
						"created_at":   "2026-07-10T10:00:00Z",
						"last_used_at": "2026-07-10T11:00:00Z",
					}},
				})
				return
			}
			writeTestJSON(w, map[string]any{"credential": "s2s_test", "device_session_id": "sess-new"})
		case "/v1/auth/device-codes/dev-1/revoke-session":
			var body struct {
				SessionID string `json:"session_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode revoke body: %v", err)
			}
			if body.SessionID != "sess-old" {
				t.Fatalf("session_id = %q", body.SessionID)
			}
			revoked = true
			writeTestJSON(w, map[string]string{"status": "revoked"})
		case "/v1/auth/devices/key":
			if r.Header.Get("Authorization") != "Bearer s2s_test" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			writeTestJSON(w, map[string]string{"status": "ok"})
		case "/v1/account/onboarding":
			writeTestJSON(w, map[string]any{"onboarded": true, "consent_required": false, "consent_version": "2026-07-14"})
		case "/v1/auth/me":
			writeTestJSON(w, map[string]string{"user_id": "user-1", "email": "user@example.test"})
		case "/v1/cli/install":
			writeTestJSON(w, map[string]any{"recorded": true})
		case "/v1/cli/update":
			writeTestJSON(w, map[string]any{"current_version": clicore.FullVersion(), "latest_version": clicore.FullVersion(), "update_available": false})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	testApp := app{
		stdin:        strings.NewReader("1\n"),
		stdout:       &stdout,
		stderr:       &stderr,
		sleep:        func(time.Duration) {},
		stdinIsTTY:   func(io.Reader) bool { return true },
		stdoutIsTTY:  func(io.Writer) bool { return true },
		readPassword: func(string, io.Writer) (string, error) { return "", nil },
	}
	code := testApp.run(context.Background(), []string{"login", "--host", "https://api.login.example.test", "--no-browser"})
	if code != 0 {
		t.Fatalf("login code = %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !revoked {
		t.Fatal("device-code revoke endpoint was not called")
	}
	if !strings.Contains(stderr.String(), "Old laptop") || !strings.Contains(stderr.String(), "Continuing login") {
		t.Fatalf("stderr missing device-limit flow:\n%s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Logged in as user@example.test") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestSignoutResolvesDeviceNameAndRevokesSession(t *testing.T) {
	withCredential(t, "https://api.example.test")
	revoked := false
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/devices":
			if r.Header.Get("Authorization") != "Bearer s2s_test" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			writeTestJSON(w, map[string]any{"sessions": []map[string]any{{
				"id":           "sess-1",
				"device_name":  "laptop",
				"client_type":  "cli",
				"created_at":   "2026-07-10T10:00:00Z",
				"last_used_at": "2026-07-10T11:00:00Z",
			}}})
		case "/v1/auth/sessions/sess-1/revoke":
			if r.Header.Get("Authorization") != "Bearer s2s_test" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			revoked = true
			writeTestJSON(w, map[string]string{"status": "revoked"})
		case "/v1/cli/install":
			writeTestJSON(w, map[string]any{"recorded": true})
		case "/v1/cli/update":
			writeTestJSON(w, map[string]any{"current_version": clicore.FullVersion(), "latest_version": clicore.FullVersion(), "update_available": false})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"signout", "laptop"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !revoked {
		t.Fatal("session revoke endpoint was not called")
	}
	if !strings.Contains(stdout.String(), "Signed out laptop (sess-1)") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestUploadSuccessText(t *testing.T) {
	withMockAPI(t, uploadHandler(t))
	withCredential(t, "https://api.example.test")
	file := writeTempFile(t, "hello.txt", "hello")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{file, "--expires", "24h"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"Uploaded successfully", "Link: https://s.share2.us/pub-1", "Expires: 2026-07-03T00:00:00Z"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q in:\n%s", want, stdout.String())
		}
	}
}

func TestUploadSuccessJSON(t *testing.T) {
	withMockAPI(t, uploadHandler(t))
	withCredential(t, "https://api.example.test")
	file := writeTempFile(t, "hello.txt", "hello")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{file, "--json", "--name", "renamed.txt"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	var decoded struct {
		PublicID  string `json:"public_id"`
		Link      string `json:"link"`
		Status    string `json:"status"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("decode stdout: %v\n%s", err, stdout.String())
	}
	if decoded.PublicID != "pub-1" || decoded.Link != "https://s.share2.us/pub-1" || decoded.Status != "ready" {
		t.Fatalf("json output = %+v", decoded)
	}
}

func TestZipDirectoryProducesValidArchive(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatalf("write root file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "b.txt"), []byte("bravo"), 0o600); err != nil {
		t.Fatalf("write nested file: %v", err)
	}

	archivePath, err := zipDirectory(root)
	if err != nil {
		t.Fatalf("zipDirectory() error = %v", err)
	}
	defer os.Remove(archivePath)

	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer reader.Close()

	got := map[string]string{}
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			t.Fatalf("open zip entry %s: %v", file.Name, err)
		}
		body, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read zip entry %s: %v", file.Name, err)
		}
		got[file.Name] = string(body)
	}
	want := map[string]string{"a.txt": "alpha", "nested/b.txt": "bravo"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("zip entries = %#v, want %#v", got, want)
	}
}

func TestUploadDirectoryWithoutDeviceRequiresDevice(t *testing.T) {
	withCredential(t, "https://api.example.test")
	dir := t.TempDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{dir}, &stdout, &stderr)

	if code == 0 {
		t.Fatalf("code = %d, want non-zero", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	want := "folders can only be shared to a device (add --device <alias> or --contact <email>; run 's2u devices' to list yours)"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr missing %q in:\n%s", want, stderr.String())
	}
}

func TestUploadDirectoryWithDeviceDeclaresFolder(t *testing.T) {
	deviceKey, err := clicore.NewDeviceKeyPair()
	if err != nil {
		t.Fatalf("NewDeviceKeyPair() error = %v", err)
	}
	dir := filepath.Join(t.TempDir(), "dist")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("mkdir dist: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log('ok')\n"), 0o600); err != nil {
		t.Fatalf("write app.js: %v", err)
	}
	var sawCreate bool
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/devices":
			writeTestJSON(w, map[string]any{
				"sessions": []map[string]any{{
					"id":          "dev-1",
					"device_name": "laptop",
					"public_key":  deviceKey.PublicKey,
				}},
			})
		case "/v1/uploads":
			sawCreate = true
			var req clicore.UploadCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode create request: %v", err)
			}
			if req.FileName != "dist.zip" || req.ContentType != "application/zip" || req.ContentClass != clicore.ContentClassFolder {
				t.Fatalf("folder metadata = name %q type %q class %q", req.FileName, req.ContentType, req.ContentClass)
			}
			if !req.Encrypted || req.EncryptionAlgo != clicore.EncryptionAlgoAES256GCM+"+sealedbox" {
				t.Fatalf("encryption metadata not sent: %+v", req)
			}
			if req.TargetDevice != "dev-1" || req.SealedKey == "" {
				t.Fatalf("target metadata not sent: %+v", req)
			}
			if req.SourceRef != "" {
				t.Fatalf("source_ref = %q, want empty for folder device upload", req.SourceRef)
			}
			if req.SizeBytes == 0 || req.SHA256 == "" {
				t.Fatalf("size/hash not sent: %+v", req)
			}
			writeTestJSON(w, map[string]any{
				"upload": map[string]any{
					"url":     "https://upload.example.test/put",
					"method":  "PUT",
					"headers": map[string]string{"X-Test": "yes"},
				},
				"share":             map[string]any{"public_id": "pub-folder", "targeted": true},
				"upload_session_id": "upload-folder",
				"expires_at":        "2026-07-03T00:00:00Z",
			})
		case "/put":
			if r.Method != http.MethodPut || r.Header.Get("X-Test") != "yes" {
				t.Fatalf("put method/header = %s %q", r.Method, r.Header.Get("X-Test"))
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read put body: %v", err)
			}
			if len(body) == 0 {
				t.Fatal("put body was empty")
			}
			w.WriteHeader(http.StatusOK)
		case "/v1/uploads/upload-folder/complete":
			writeTestJSON(w, map[string]string{"public_id": "pub-folder", "status": "ready", "expires_at": "2026-07-03T00:00:00Z"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	withCredential(t, "https://api.example.test")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{dir, "--device", "laptop", "--no-scan"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !sawCreate {
		t.Fatal("create request was not sent")
	}
	if !strings.Contains(stdout.String(), "Sent dist.zip to device laptop") {
		t.Fatalf("stdout missing device sent message:\n%s", stdout.String())
	}
}

func TestUploadTeammateFansOutToDevices(t *testing.T) {
	key1, err := clicore.NewDeviceKeyPair()
	if err != nil {
		t.Fatalf("keypair1: %v", err)
	}
	key2, err := clicore.NewDeviceKeyPair()
	if err != nil {
		t.Fatalf("keypair2: %v", err)
	}
	file := writeTempFile(t, "report.png", "\x89PNG\r\n\x1a\nhello")
	var sawCreate bool
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/contacts/devices":
			if got := r.URL.Query().Get("email"); got != "alice@corp.com" {
				t.Fatalf("directory email = %q", got)
			}
			writeTestJSON(w, map[string]any{
				"mode": "auto",
				"devices": []map[string]any{
					{"device_id": "d1", "public_key": key1.PublicKey},
					{"device_id": "d2", "public_key": key2.PublicKey},
				},
			})
		case "/v1/uploads":
			sawCreate = true
			var req clicore.UploadCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode create: %v", err)
			}
			if req.RecipientEmail != "alice@corp.com" {
				t.Fatalf("recipient_email = %q", req.RecipientEmail)
			}
			if len(req.Targets) != 2 {
				t.Fatalf("targets = %d, want 2 (%+v)", len(req.Targets), req.Targets)
			}
			if req.Targets[0].TargetDeviceSessionID != "d1" || req.Targets[1].TargetDeviceSessionID != "d2" {
				t.Fatalf("target ids = %+v", req.Targets)
			}
			if req.Targets[0].SealedKey == "" || req.Targets[1].SealedKey == "" || req.Targets[0].SealedKey == req.Targets[1].SealedKey {
				t.Fatalf("sealed keys not distinct/non-empty: %+v", req.Targets)
			}
			if !req.Encrypted || req.EncryptionAlgo != clicore.EncryptionAlgoAES256GCM+"+sealedbox" {
				t.Fatalf("encryption metadata: %+v", req)
			}
			if req.TargetDevice != "" || req.SealedKey != "" {
				t.Fatalf("single-device fields should be empty for teammate: %+v", req)
			}
			writeTestJSON(w, map[string]any{
				"upload":            map[string]any{"url": "https://upload.example.test/put", "method": "PUT", "headers": map[string]string{"X-Test": "yes"}},
				"share":             map[string]any{"public_id": "pub-tm", "targeted": true},
				"upload_session_id": "up-tm",
				"expires_at":        "2026-07-03T00:00:00Z",
			})
		case "/put":
			io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		case "/v1/uploads/up-tm/complete":
			writeTestJSON(w, map[string]string{"public_id": "pub-tm", "status": "ready", "expires_at": "2026-07-03T00:00:00Z"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	withCredential(t, "https://api.example.test")

	var stdout, stderr bytes.Buffer
	code := run([]string{file, "--teammate", "alice@corp.com", "--no-scan"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !sawCreate {
		t.Fatal("create not sent")
	}
	if !strings.Contains(stdout.String(), "Sent report.png to alice@corp.com (2 device(s))") {
		t.Fatalf("stdout missing teammate sent message:\n%s", stdout.String())
	}
}

func TestEmailRoutesToTeammateWhenEligible(t *testing.T) {
	key1, err := clicore.NewDeviceKeyPair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	file := writeTempFile(t, "doc.pdf", "%PDF-1.4 hello")
	var sawCreate bool
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/contacts/devices":
			writeTestJSON(w, map[string]any{
				"mode":    "auto",
				"devices": []map[string]any{{"device_id": "d1", "public_key": key1.PublicKey}},
			})
		case "/v1/uploads":
			sawCreate = true
			var req clicore.UploadCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if req.RecipientEmail != "alice@corp.com" || len(req.Targets) != 1 {
				t.Fatalf("expected teammate E2E routing, got recipient=%q targets=%+v recipients=%+v", req.RecipientEmail, req.Targets, req.Recipients)
			}
			if len(req.Recipients) != 0 {
				t.Fatalf("recipients should be empty when routed to teammate: %+v", req.Recipients)
			}
			writeTestJSON(w, map[string]any{
				"upload":            map[string]any{"url": "https://upload.example.test/put", "method": "PUT", "headers": map[string]string{"X-Test": "yes"}},
				"share":             map[string]any{"public_id": "pub-e", "targeted": true},
				"upload_session_id": "up-e",
				"expires_at":        "2026-07-03T00:00:00Z",
			})
		case "/put":
			io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		case "/v1/uploads/up-e/complete":
			writeTestJSON(w, map[string]string{"public_id": "pub-e", "status": "ready", "expires_at": "2026-07-03T00:00:00Z"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	withCredential(t, "https://api.example.test")
	var stdout, stderr bytes.Buffer
	code := run([]string{file, "--email", "alice@corp.com", "--no-scan"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !sawCreate {
		t.Fatal("create not sent")
	}
	if !strings.Contains(stdout.String(), "Sent doc.pdf to alice@corp.com") {
		t.Fatalf("stdout missing teammate sent message:\n%s", stdout.String())
	}
}

func TestEmailFallsBackToLinkShareWhenNotTeammate(t *testing.T) {
	file := writeTempFile(t, "doc.pdf", "%PDF-1.4 hello")
	var sawCreate bool
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/contacts/devices":
			writeTestJSON(w, map[string]any{"code": "recipient_not_registered", "devices": []map[string]any{}})
		case "/v1/uploads":
			sawCreate = true
			var req clicore.UploadCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if req.RecipientEmail != "" || len(req.Targets) != 0 {
				t.Fatalf("should NOT route to teammate: recipient=%q targets=%+v", req.RecipientEmail, req.Targets)
			}
			if len(req.Recipients) != 1 || req.Recipients[0] != "stranger@corp.com" {
				t.Fatalf("email-share recipients = %+v", req.Recipients)
			}
			writeTestJSON(w, map[string]any{
				"upload":            map[string]any{"url": "https://upload.example.test/put", "method": "PUT", "headers": map[string]string{"X-Test": "yes"}},
				"share":             map[string]any{"public_id": "pub-l", "link": "https://s.example.test/pub-l"},
				"upload_session_id": "up-l",
				"expires_at":        "2026-07-03T00:00:00Z",
			})
		case "/put":
			io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		case "/v1/uploads/up-l/complete":
			writeTestJSON(w, map[string]string{"public_id": "pub-l", "status": "ready", "expires_at": "2026-07-03T00:00:00Z"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	withCredential(t, "https://api.example.test")
	var stdout, stderr bytes.Buffer
	code := run([]string{file, "--email", "stranger@corp.com", "--no-scan"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !sawCreate {
		t.Fatal("create not sent")
	}
	if !strings.Contains(stdout.String(), "Uploaded successfully") {
		t.Fatalf("stdout missing link-share message:\n%s", stdout.String())
	}
}

func TestEmailBlockedRecipientStopsSend(t *testing.T) {
	file := writeTempFile(t, "doc.pdf", "%PDF-1.4 hello")
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/contacts/devices" {
			t.Fatalf("blocked recipient must not proceed to upload; got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusForbidden)
		writeTestJSON(w, map[string]any{"error": map[string]string{"code": "recipient_not_accepting", "message": "blocked"}})
	}))
	withCredential(t, "https://api.example.test")
	var stdout, stderr bytes.Buffer
	code := run([]string{file, "--email", "blocked@corp.com", "--no-scan"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("want non-zero, got 0")
	}
	if !strings.Contains(stderr.String(), "not accepting files from you") {
		t.Fatalf("stderr missing block message:\n%s", stderr.String())
	}
}

func TestIncomingRejectBlockCallsBothEndpoints(t *testing.T) {
	var sawReject, sawBlock bool
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/inbox/pending" && r.Method == http.MethodGet:
			writeTestJSON(w, map[string]any{"shares": []map[string]any{
				{"public_id": "pub-1", "file_name": "f.png", "size_bytes": 10, "sender_email": "spammer@x.com"},
			}})
		case r.URL.Path == "/v1/inbox/pending/pub-1/reject" && r.Method == http.MethodPost:
			sawReject = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v1/contacts/senders" && r.Method == http.MethodPut:
			sawBlock = true
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["email"] != "spammer@x.com" || body["mode"] != "disallowed" {
				t.Fatalf("block body = %+v", body)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	withCredential(t, "https://api.example.test")
	var stdout, stderr bytes.Buffer
	code := run([]string{"incoming", "reject", "pub-1", "--block"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !sawReject || !sawBlock {
		t.Fatalf("reject=%v block=%v", sawReject, sawBlock)
	}
}

func TestUploadTeammateAndDeviceConflict(t *testing.T) {
	withCredential(t, "https://api.example.test")
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("no API call expected on conflict; got %s %s", r.Method, r.URL.Path)
	}))
	file := writeTempFile(t, "x.txt", "hello")
	var stdout, stderr bytes.Buffer
	code := run([]string{file, "--teammate", "a@b.com", "--device", "laptop", "--no-scan"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("want non-zero, got 0")
	}
	if !strings.Contains(stderr.String(), "cannot be combined") {
		t.Fatalf("stderr missing conflict message:\n%s", stderr.String())
	}
}

func TestTrustCallsSenderEndpoint(t *testing.T) {
	var sawPut bool
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/contacts/senders" || r.Method != http.MethodPut {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		sawPut = true
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body["email"] != "bob@corp.com" || body["mode"] != "auto" {
			t.Fatalf("body = %+v", body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	withCredential(t, "https://api.example.test")
	var stdout, stderr bytes.Buffer
	code := run([]string{"trust", "bob@corp.com"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !sawPut {
		t.Fatal("PUT /v1/contacts/senders not called")
	}
}

func TestIncomingApproveCallsEndpoint(t *testing.T) {
	var sawApprove bool
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/inbox/pending/pub-1/approve" || r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		sawApprove = true
		w.WriteHeader(http.StatusOK)
	}))
	withCredential(t, "https://api.example.test")
	var stdout, stderr bytes.Buffer
	code := run([]string{"incoming", "approve", "pub-1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !sawApprove {
		t.Fatal("approve endpoint not called")
	}
}

func TestUploadSecretScanCancelsNonTTYWithoutCreate(t *testing.T) {
	withCredential(t, "https://api.example.test")
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("secret scan cancellation should not call API; got %s %s", r.Method, r.URL.Path)
	}))
	file := writeTempFile(t, "secret.txt", fakeSecretFileContent())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{file}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("code = %d, want 1; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "WARNING:") || !strings.Contains(stderr.String(), "REDACTED") || !strings.Contains(stderr.String(), "share cancelled") {
		t.Fatalf("stderr missing warning/redaction/cancel:\n%s", stderr.String())
	}
	if strings.Contains(stderr.String(), fakePrivateKeyMaterial()) {
		t.Fatalf("stderr leaked full secret:\n%s", stderr.String())
	}
}

func TestUploadAllowSecretsProceedsAfterFinding(t *testing.T) {
	var sawCreate bool
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/uploads":
			sawCreate = true
			writeTestJSON(w, map[string]any{
				"upload":            map[string]any{"url": "https://upload.example.test/put", "method": "PUT"},
				"share":             map[string]string{"public_id": "pub-1"},
				"upload_session_id": "upload-1",
				"expires_at":        "2026-07-03T00:00:00Z",
			})
		case "/put":
			w.WriteHeader(http.StatusOK)
		case "/v1/uploads/upload-1/complete":
			writeTestJSON(w, map[string]string{"public_id": "pub-1", "status": "ready", "expires_at": "2026-07-03T00:00:00Z"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	withCredential(t, "https://api.example.test")
	file := writeTempFile(t, "secret.txt", fakeSecretFileContent())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{file, "--allow-secrets"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !sawCreate {
		t.Fatal("create request was not sent")
	}
	if !strings.Contains(stderr.String(), "proceeding because --allow-secrets was set") {
		t.Fatalf("stderr missing allow note:\n%s", stderr.String())
	}
}

func TestUploadNoScanSkipsSecretScan(t *testing.T) {
	withMockAPI(t, uploadHandlerForSize(t, int64(len(fakeSecretFileContent()))))
	withCredential(t, "https://api.example.test")
	file := writeTempFile(t, "secret.txt", fakeSecretFileContent())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{file, "--no-scan"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "secret scan skipped by --no-scan") {
		t.Fatalf("stderr missing no-scan note:\n%s", stderr.String())
	}
}

func TestUploadSecretScanTTYYesProceeds(t *testing.T) {
	withMockAPI(t, uploadHandlerForSize(t, int64(len(fakeSecretFileContent()))))
	withCredential(t, "https://api.example.test")
	file := writeTempFile(t, "secret.txt", fakeSecretFileContent())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := app{
		stdin:      strings.NewReader("y\n"),
		stdout:     &stdout,
		stderr:     &stderr,
		stdinIsTTY: func(io.Reader) bool { return true },
	}.run(context.Background(), []string{file})

	if code != 0 {
		t.Fatalf("code = %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "share anyway? [y/N]") {
		t.Fatalf("stderr missing prompt:\n%s", stderr.String())
	}
}

func TestUploadSendsSourceRefUsesCanonicalLinkAndSavesRegistry(t *testing.T) {
	var gotSourceRef string
	var gotNew bool
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/uploads":
			var req clicore.UploadCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode create request: %v", err)
			}
			gotSourceRef = req.SourceRef
			gotNew = req.New
			writeTestJSON(w, map[string]any{
				"upload": map[string]any{
					"url":     "https://upload.example.test/put",
					"method":  "PUT",
					"headers": map[string]string{"X-Test": "yes"},
				},
				"share":             map[string]string{"public_id": "pub-1", "link": "https://share.example.test/canonical/pub-1"},
				"upload_session_id": "upload-1",
				"expires_at":        "2026-07-03T00:00:00Z",
				"link":              "https://share.example.test/canonical/pub-1",
			})
		case "/put":
			w.WriteHeader(http.StatusOK)
		case "/v1/uploads/upload-1/complete":
			writeTestJSON(w, map[string]string{"public_id": "pub-1", "status": "ready", "expires_at": "2026-07-03T00:00:00Z"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	withCredential(t, "https://api.example.test")
	file := writeTempFile(t, "hello.txt", "hello")
	absPath, err := filepath.Abs(file)
	if err != nil {
		t.Fatalf("Abs() error = %v", err)
	}
	pathDigest := sha256.Sum256([]byte(absPath))
	wantSourceRef := fmt.Sprintf("%x", pathDigest[:])

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{file}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if gotSourceRef != wantSourceRef || gotNew {
		t.Fatalf("source_ref/new = %q/%v, want %q/false", gotSourceRef, gotNew, wantSourceRef)
	}
	if !strings.Contains(stdout.String(), "Link: https://share.example.test/canonical/pub-1") {
		t.Fatalf("stdout missing canonical link:\n%s", stdout.String())
	}
	registry, err := clicore.LoadSourceRegistry()
	if err != nil {
		t.Fatalf("LoadSourceRegistry() error = %v", err)
	}
	if registry[absPath].PublicID != "pub-1" || registry[absPath].Link != "https://share.example.test/canonical/pub-1" {
		t.Fatalf("source registry = %+v", registry)
	}
}

func TestUploadNewFlagOmitsSourceRef(t *testing.T) {
	var gotSourceRef string
	var gotNew bool
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/uploads":
			var req clicore.UploadCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode create request: %v", err)
			}
			gotSourceRef = req.SourceRef
			gotNew = req.New
			writeTestJSON(w, map[string]any{
				"upload":            map[string]any{"url": "https://upload.example.test/put", "method": "PUT"},
				"share":             map[string]string{"public_id": "pub-2"},
				"upload_session_id": "upload-2",
				"expires_at":        "2026-07-03T00:00:00Z",
			})
		case "/put":
			w.WriteHeader(http.StatusOK)
		case "/v1/uploads/upload-2/complete":
			writeTestJSON(w, map[string]string{"public_id": "pub-2", "status": "ready", "expires_at": "2026-07-03T00:00:00Z"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	withCredential(t, "https://api.example.test")
	file := writeTempFile(t, "hello.txt", "hello")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{file, "--fresh"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if gotSourceRef != "" || !gotNew {
		t.Fatalf("source_ref/new = %q/%v, want empty/true", gotSourceRef, gotNew)
	}
}

func TestUploadSkippedStableShareDoesNotPutOrComplete(t *testing.T) {
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/uploads":
			var req clicore.UploadCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode create request: %v", err)
			}
			if req.SourceRef == "" || req.New {
				t.Fatalf("source_ref/new = %q/%v, want stable source_ref", req.SourceRef, req.New)
			}
			writeTestJSON(w, map[string]any{
				"upload":            map[string]any{"url": "", "method": "", "headers": map[string]string{}},
				"share":             map[string]any{"public_id": "pub-1", "link": "https://share.example.test/canonical/pub-1", "version": 3},
				"upload_session_id": "",
				"expires_at":        "2026-07-04T00:00:00Z",
				"skipped_upload":    true,
			})
		default:
			t.Fatalf("unexpected upload/complete path on skipped stable share: %s", r.URL.Path)
		}
	}))
	withCredential(t, "https://api.example.test")
	file := writeTempFile(t, "hello.txt", "hello")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{file}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Link: https://share.example.test/canonical/pub-1") {
		t.Fatalf("stdout missing canonical link:\n%s", stdout.String())
	}
}

func TestUploadPasswordAndOneTimeFlags(t *testing.T) {
	var sawCreate bool
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/put" && r.Header.Get("Authorization") != "Bearer s2s_test" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/v1/uploads":
			sawCreate = true
			var req clicore.UploadCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode create request: %v", err)
			}
			if req.Password != "secret" || !req.OneTime {
				t.Fatalf("password/one_time not sent: %+v", req)
			}
			writeTestJSON(w, map[string]any{
				"upload": map[string]any{
					"url":     "https://upload.example.test/put",
					"method":  "PUT",
					"headers": map[string]string{"X-Test": "yes"},
				},
				"share":             map[string]string{"public_id": "pub-1"},
				"upload_session_id": "upload-1",
				"expires_at":        "2026-07-03T00:00:00Z",
			})
		case "/put":
			w.WriteHeader(http.StatusOK)
		case "/v1/uploads/upload-1/complete":
			writeTestJSON(w, map[string]string{"public_id": "pub-1", "status": "ready", "expires_at": "2026-07-03T00:00:00Z"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	withCredential(t, "https://api.example.test")
	file := writeTempFile(t, "hello.txt", "hello")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{file, "--password=secret", "--one-time"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !sawCreate {
		t.Fatal("create request was not sent")
	}
}

func TestUploadRecipientFlags(t *testing.T) {
	var sawCreate bool
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/uploads":
			sawCreate = true
			var req clicore.UploadCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode create request: %v", err)
			}
			want := []string{"a@example.test", "b@example.test", "c@example.test"}
			if strings.Join(req.Recipients, ",") != strings.Join(want, ",") {
				t.Fatalf("recipients = %#v, want %#v", req.Recipients, want)
			}
			writeTestJSON(w, map[string]any{
				"upload": map[string]any{
					"url":     "https://upload.example.test/put",
					"method":  "PUT",
					"headers": map[string]string{"X-Test": "yes"},
				},
				"share":                  map[string]string{"public_id": "pub-1"},
				"upload_session_id":      "upload-1",
				"expires_at":             "2026-07-03T00:00:00Z",
				"email_shares_remaining": 17,
			})
		case "/put":
			w.WriteHeader(http.StatusOK)
		case "/v1/uploads/upload-1/complete":
			writeTestJSON(w, map[string]string{"public_id": "pub-1", "status": "ready", "expires_at": "2026-07-03T00:00:00Z"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	withCredential(t, "https://api.example.test")
	file := writeTempFile(t, "hello.txt", "hello")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{file, "--email", "a@example.test,b@example.test", "--to=c@example.test"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !sawCreate {
		t.Fatal("create request was not sent")
	}
	for _, want := range []string{
		"Shared with 3 recipient(s).",
		"They can only open it after signing in as that email",
		"17 email-shares left this period",
		"Tip: if the email doesn't arrive",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q in:\n%s", want, stdout.String())
		}
	}
}

func TestUploadRecipientToAlias(t *testing.T) {
	var recipients []string
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/contacts/devices":
			writeTestJSON(w, map[string]any{"code": "recipient_not_registered", "devices": []map[string]any{}})
		case "/v1/uploads":
			var req clicore.UploadCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode create request: %v", err)
			}
			recipients = append([]string(nil), req.Recipients...)
			writeTestJSON(w, map[string]any{
				"upload":            map[string]any{"url": "https://upload.example.test/put", "method": "PUT"},
				"share":             map[string]string{"public_id": "pub-1"},
				"upload_session_id": "upload-1",
				"expires_at":        "2026-07-03T00:00:00Z",
			})
		case "/put":
			w.WriteHeader(http.StatusOK)
		case "/v1/uploads/upload-1/complete":
			writeTestJSON(w, map[string]string{"public_id": "pub-1", "status": "ready", "expires_at": "2026-07-03T00:00:00Z"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	withCredential(t, "https://api.example.test")
	file := writeTempFile(t, "hello.txt", "hello")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{file, "--to", "alias@example.test"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if strings.Join(recipients, ",") != "alias@example.test" {
		t.Fatalf("recipients = %#v", recipients)
	}
}

func TestUploadEmailShareAPIErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		code       string
		message    string
		wantStderr []string
	}{
		{
			name:       "quota exceeded",
			status:     http.StatusForbidden,
			code:       "email_share_quota_exceeded",
			message:    "You have no email shares left this period.",
			wantStderr: []string{"You have no email shares left this period.", "upgrade to Pro for more"},
		},
		{
			name:       "rate limited",
			status:     http.StatusTooManyRequests,
			code:       "invite_rate_limited",
			message:    "Invite limit reached.",
			wantStderr: []string{"Invite limit reached.", "too many invites right now, try later"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/contacts/devices" {
					writeTestJSON(w, map[string]any{"code": "recipient_not_registered", "devices": []map[string]any{}})
					return
				}
				if r.URL.Path != "/v1/uploads" {
					t.Fatalf("unexpected path %s", r.URL.Path)
				}
				w.WriteHeader(tt.status)
				writeTestJSON(w, map[string]any{"error": map[string]string{"code": tt.code, "message": tt.message}})
			}))
			withCredential(t, "https://api.example.test")
			file := writeTempFile(t, "hello.txt", "hello")

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := run([]string{file, "--email", "a@example.test"}, &stdout, &stderr)

			if code != 1 {
				t.Fatalf("code = %d, want 1; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			for _, want := range tt.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Fatalf("stderr missing %q in:\n%s", want, stderr.String())
				}
			}
			if strings.Contains(stderr.String(), "create upload:") {
				t.Fatalf("stderr has generic action prefix:\n%s", stderr.String())
			}
		})
	}
}

func TestUploadEncryptFlagSendsCiphertextMetadata(t *testing.T) {
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/uploads":
			var req clicore.UploadCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode create request: %v", err)
			}
			if !req.Encrypted || req.EncryptionAlgo != clicore.EncryptionAlgoAES256GCM {
				t.Fatalf("encryption metadata not sent: %+v", req)
			}
			if req.SizeBytes <= 5 {
				t.Fatalf("encrypted size = %d, want larger than plaintext", req.SizeBytes)
			}
			writeTestJSON(w, map[string]any{
				"upload": map[string]any{
					"url":     "https://upload.example.test/put",
					"method":  "PUT",
					"headers": map[string]string{"X-Test": "yes"},
				},
				"share":             map[string]string{"public_id": "pub-1"},
				"upload_session_id": "upload-1",
				"expires_at":        "2026-07-03T00:00:00Z",
			})
		case "/put":
			w.WriteHeader(http.StatusOK)
		case "/v1/uploads/upload-1/complete":
			writeTestJSON(w, map[string]string{"public_id": "pub-1", "status": "ready", "expires_at": "2026-07-03T00:00:00Z"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	withCredential(t, "https://api.example.test")
	file := writeTempFile(t, "hello.txt", "hello")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{file, "--encrypt"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "#k=") || !strings.Contains(stdout.String(), "never sent to the server") {
		t.Fatalf("stdout missing encrypted URL/note:\n%s", stdout.String())
	}
}

func TestPullCachesShareAndCopiesOutput(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	body := []byte("hello")
	digest := sha256.Sum256(body)
	sum := fmt.Sprintf("%x", digest[:])
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/shares/pub-1":
			if r.Header.Get("Authorization") != "Bearer s2s_test" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			writeTestJSON(w, map[string]any{"public_id": "pub-1", "file_name": "hello.txt", "size_bytes": len(body), "sha256": sum, "status": "ready"})
		case "/d/pub-1":
			if r.URL.Host != "api.example.test" {
				t.Fatalf("download host = %q", r.URL.Host)
			}
			if got := r.URL.Query().Get("m"); got != "download" {
				t.Fatalf("download mode = %q, want download", got)
			}
			w.Write(body)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	withCredential(t, "https://api.example.test")
	output := filepath.Join(t.TempDir(), "hello.txt")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"pull", "https://s.share2.us/pub-1#ignored", "--output", output}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	copied, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(copied) != "hello" {
		t.Fatalf("output = %q", copied)
	}
	manifest, err := clicore.LoadCacheManifest()
	if err != nil {
		t.Fatalf("LoadCacheManifest() error = %v", err)
	}
	if !clicore.CacheEntryIsLocal(manifest["pub-1"]) {
		t.Fatalf("cached entry not local: %+v", manifest["pub-1"])
	}
}

func TestGetBarePublicIDUsesConfiguredGateway(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	var ciphertext bytes.Buffer
	if err := clicore.EncryptStream(&ciphertext, strings.NewReader("secret"), key); err != nil {
		t.Fatalf("EncryptStream() error = %v", err)
	}
	withCredential(t, "https://api.staging.example.test")
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/d/pub-1" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Host != "api.staging.example.test" {
			t.Fatalf("download host = %q", r.URL.Host)
		}
		if got := r.URL.Query().Get("m"); got != "download" {
			t.Fatalf("download mode = %q, want download", got)
		}
		w.Write(ciphertext.Bytes())
	}))
	output := filepath.Join(t.TempDir(), "secret.txt")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"get", "pub-1", "--key", clicore.EncodeKey(key), "--output", output}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	plaintext, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(plaintext) != "secret" {
		t.Fatalf("output = %q", plaintext)
	}
}

func TestListJSONIncludesOriginAndAvailability(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cacheFile := writeTempFile(t, "cached.txt", "hello")
	digest := sha256.Sum256([]byte("hello"))
	sum := fmt.Sprintf("%x", digest[:])
	if err := clicore.SaveCacheManifest(clicore.CacheManifest{
		"pub-local": {Path: cacheFile, SizeBytes: 5, SHA256: sum, FileName: "cached.txt"},
	}); err != nil {
		t.Fatalf("SaveCacheManifest() error = %v", err)
	}
	withCredential(t, "https://api.example.test")
	sourcePath := filepath.Join(t.TempDir(), "projects", "share2us", "cached.txt")
	if err := clicore.SaveSourceRegistry(clicore.SourceRegistry{
		sourcePath: {PublicID: "pub-local", Link: "https://s.share2.us/pub-local"},
	}); err != nil {
		t.Fatalf("SaveSourceRegistry() error = %v", err)
	}
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/shares" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		writeTestJSON(w, map[string]any{"shares": []map[string]any{
			{"public_id": "pub-local", "file_name": "cached.txt", "size_bytes": 5, "sha256": sum, "status": "ready", "expires_at": "2026-07-03T00:00:00Z", "device_name": "Laptop", "live_update": true, "version": 3},
			{"public_id": "pub-remote", "file_name": "remote.txt", "size_bytes": 7, "sha256": "abc", "status": "ready", "expires_at": "2026-07-03T00:00:00Z"},
		}})
	}))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"ls", "--json"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	var decoded struct {
		Shares []struct {
			Serial       int    `json:"serial"`
			PublicID     string `json:"public_id"`
			OriginDevice string `json:"origin_device"`
			Availability string `json:"availability"`
			Path         string `json:"path"`
			LiveUpdate   bool   `json:"live_update"`
			Version      uint64 `json:"version"`
		} `json:"shares"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("decode stdout: %v\n%s", err, stdout.String())
	}
	if len(decoded.Shares) != 2 {
		t.Fatalf("shares = %+v", decoded.Shares)
	}
	if decoded.Shares[0].Serial != 1 || decoded.Shares[0].PublicID != "pub-local" || decoded.Shares[0].OriginDevice != "Laptop" || decoded.Shares[0].Availability != "LOCAL" || decoded.Shares[0].Path != sourcePath || !decoded.Shares[0].LiveUpdate || decoded.Shares[0].Version != 3 {
		t.Fatalf("local row = %+v", decoded.Shares[0])
	}
	if decoded.Shares[1].Serial != 2 || decoded.Shares[1].OriginDevice != "Portal" || decoded.Shares[1].Availability != "REMOTE" || decoded.Shares[1].Path != "-" {
		t.Fatalf("remote row = %+v", decoded.Shares[1])
	}
	index, err := clicore.LoadListIndex()
	if err != nil {
		t.Fatalf("LoadListIndex() error = %v", err)
	}
	if len(index) != 2 || index[0].Serial != 1 || index[0].PublicID != "pub-local" || index[0].FileName != "cached.txt" || index[1].PublicID != "pub-remote" {
		t.Fatalf("list index = %+v", index)
	}
}

func TestListTableIncludesSerialPathAndWritesIndex(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	withCredential(t, "https://api.example.test")
	sourcePath := filepath.Join(t.TempDir(), "very", "long", "projects", "share2us", "test.md")
	if err := clicore.SaveSourceRegistry(clicore.SourceRegistry{
		sourcePath: {PublicID: "pub-1", Link: "https://s.share2.us/pub-1"},
	}); err != nil {
		t.Fatalf("SaveSourceRegistry() error = %v", err)
	}
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/shares" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		writeTestJSON(w, map[string]any{"shares": []map[string]any{
			{"public_id": "pub-1", "file_name": "test.md", "size_bytes": 5, "status": "ready", "expires_at": "2026-07-03T00:00:00Z"},
		}})
	}))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"ls"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"#   PUBLIC_ID", "PATH", "1   pub-1", truncateLeft(sourcePath, 34)} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q in:\n%s", want, stdout.String())
		}
	}
	index, err := clicore.LoadListIndex()
	if err != nil {
		t.Fatalf("LoadListIndex() error = %v", err)
	}
	if len(index) != 1 || index[0].Serial != 1 || index[0].PublicID != "pub-1" || index[0].FileName != "test.md" {
		t.Fatalf("list index = %+v", index)
	}
}

func TestResolveShareRef(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got, err := resolveShareRef("pub-1"); err != nil || got != "pub-1" {
		t.Fatalf("public id resolve = %q, %v", got, err)
	}
	if _, err := resolveShareRef("1"); err == nil || !strings.Contains(err.Error(), "no share #1 in the last listing") {
		t.Fatalf("missing index err = %v", err)
	}
	if err := clicore.SaveListIndex([]clicore.ListIndexEntry{{Serial: 2, PublicID: "pub-2", FileName: "two.txt"}}); err != nil {
		t.Fatalf("SaveListIndex() error = %v", err)
	}
	if got, err := resolveShareRef("2"); err != nil || got != "pub-2" {
		t.Fatalf("serial resolve = %q, %v", got, err)
	}
	if _, err := resolveShareRef("3"); err == nil || !strings.Contains(err.Error(), "no share #3 in the last listing") {
		t.Fatalf("unknown serial err = %v", err)
	}
}

func TestRemoveUsesResolvedSerialAndYesSkipsPrompt(t *testing.T) {
	withCredential(t, "https://api.example.test")
	if err := clicore.SaveListIndex([]clicore.ListIndexEntry{{Serial: 2, PublicID: "pub-2", FileName: "two.txt"}}); err != nil {
		t.Fatalf("SaveListIndex() error = %v", err)
	}
	var deleted string
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/shares/pub-2" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		deleted = "pub-2"
		w.WriteHeader(http.StatusNoContent)
	}))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"rm", "2", "--yes"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if deleted != "pub-2" {
		t.Fatalf("deleted = %q", deleted)
	}
	if !strings.Contains(stdout.String(), "Removed pub-2") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestDeleteAliasUsesResolvedSerialAndYesSkipsPrompt(t *testing.T) {
	withCredential(t, "https://api.example.test")
	if err := clicore.SaveListIndex([]clicore.ListIndexEntry{{Serial: 4, PublicID: "pub-4", FileName: "four.txt"}}); err != nil {
		t.Fatalf("SaveListIndex() error = %v", err)
	}
	var deleted string
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/shares/pub-4" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		deleted = "pub-4"
		w.WriteHeader(http.StatusNoContent)
	}))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"delete", "4", "--yes"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if deleted != "pub-4" {
		t.Fatalf("deleted = %q", deleted)
	}
	if !strings.Contains(stdout.String(), "Removed pub-4") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestLiveUploadRejectsNonTextFile(t *testing.T) {
	withCredential(t, "https://api.example.test")
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("live non-text upload should not call API; got %s %s", r.Method, r.URL.Path)
	}))
	path := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(path, []byte{0x00, 0x01, 0x02}, 0o600); err != nil {
		t.Fatalf("write binary file: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{path, "--live"}, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("code = %d, want 2; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "text files only") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunLiveFilePushesThenFlushesOnCancel(t *testing.T) {
	var sawPut bool
	var sawFlush bool
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer s2s_test" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/shares/pub-live/live":
			sawPut = true
			var req clicore.LivePutRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode live put: %v", err)
			}
			if req.Content != "hello" || req.CRC32 != "3610a686" {
				t.Fatalf("live request = %+v", req)
			}
			writeTestJSON(w, map[string]any{"changed": true, "crc32": req.CRC32, "size": 5, "ttl_seconds": 60})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/shares/pub-live/flush":
			sawFlush = true
			writeTestJSON(w, map[string]any{"public_id": "pub-live", "version": 2, "sha256": strings.Repeat("a", 64), "size": 5})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	path := writeTempFile(t, "live.txt", "hello")
	client := clicore.NewClient("https://api.example.test", "s2s_test")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := app{stdout: &stdout, stderr: &stderr}.runLiveFile(ctx, client, "pub-live", path, "text/plain", false)

	if code != 0 {
		t.Fatalf("code = %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !sawPut || !sawFlush {
		t.Fatalf("sawPut=%v sawFlush=%v", sawPut, sawFlush)
	}
	if !strings.Contains(stdout.String(), "live: pushed v1") || !strings.Contains(stdout.String(), "live: flushed v2") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestPutLiveFileSkipsUnchangedCRC(t *testing.T) {
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unchanged live file should not call API; got %s %s", r.Method, r.URL.Path)
	}))
	path := writeTempFile(t, "live.txt", "hello")
	client := clicore.NewClient("https://api.example.test", "s2s_test")

	crc, changed, err := putLiveFileIfChanged(context.Background(), client, "pub-live", path, "text/plain", "3610a686")

	if err != nil {
		t.Fatalf("putLiveFileIfChanged() error = %v", err)
	}
	if changed || crc != "3610a686" {
		t.Fatalf("crc=%q changed=%v", crc, changed)
	}
}

func TestParseUploadLiveAndWatchAliases(t *testing.T) {
	liveOpts, err := parseUploadArgs([]string{"file.txt", "-l"})
	if err != nil {
		t.Fatalf("parse live alias: %v", err)
	}
	if !liveOpts.live || liveOpts.watch {
		t.Fatalf("live opts = %+v", liveOpts)
	}
	watchOpts, err := parseUploadArgs([]string{"file.txt", "-w"})
	if err != nil {
		t.Fatalf("parse watch alias: %v", err)
	}
	if !watchOpts.watch || watchOpts.live {
		t.Fatalf("watch opts = %+v", watchOpts)
	}
}

func TestStatsCommandPrintsShareAnalytics(t *testing.T) {
	withCredential(t, "https://api.example.test")
	withMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/shares/pub-1/analytics" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer s2s_test" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		writeTestJSON(w, map[string]any{
			"views":            4,
			"downloads":        2,
			"unique_visitors":  3,
			"last_accessed_at": "2026-07-02T01:00:00Z",
			"timeline":         []map[string]any{{"date": "2026-07-02", "views": 4, "downloads": 2}},
			"recent":           []map[string]any{{"occurred_at": "2026-07-02T01:00:00Z", "ip": "203.0.113.9", "country": "US", "client": "curl/8", "event_type": "share.downloaded"}},
		})
	}))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{"stats", "pub-1"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"Share: pub-1", "Views: 4", "Downloads: 2", "Unique visitors: 3", "share.downloaded", "203.0.113.9"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q in:\n%s", want, stdout.String())
		}
	}
}

func TestParseUploadPasswordPromptFlag(t *testing.T) {
	opts, err := parseUploadArgs([]string{"file.txt", "--password"})
	if err != nil {
		t.Fatalf("parseUploadArgs() error = %v", err)
	}
	if !opts.promptPassword {
		t.Fatal("promptPassword = false, want true")
	}
}

func TestParseUploadNewAliases(t *testing.T) {
	for _, flag := range []string{"--new", "--fresh"} {
		t.Run(flag, func(t *testing.T) {
			opts, err := parseUploadArgs([]string{"file.txt", flag})
			if err != nil {
				t.Fatalf("parseUploadArgs() error = %v", err)
			}
			if !opts.newShare {
				t.Fatal("newShare = false, want true")
			}
		})
	}
}

func TestParseUploadRejectsMissingPasswordValue(t *testing.T) {
	opts, err := parseUploadArgs([]string{"file.txt", "--password="})
	if err != nil {
		t.Fatalf("parseUploadArgs() error = %v", err)
	}
	if opts.password != "" {
		t.Fatalf("password = %q, want empty explicit password", opts.password)
	}
}

func TestWhoamiNotLoggedIn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{"whoami"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "not logged in" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

// mcpTokenServer serves the PAT-mint endpoint `mcp token` now calls, returning a
// fixed scoped token. It records the scopes it was asked to mint.
func mcpTokenServer(t *testing.T, gotScopes *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/account/api-tokens" && r.Method == http.MethodPost {
			var body struct {
				Label  string   `json:"label"`
				Scopes []string `json:"scopes"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if gotScopes != nil {
				*gotScopes = body.Scopes
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":     "s2u_pat_MINTED123",
				"api_token": map[string]any{"id": "tok-1", "label": body.Label, "scopes": body.Scopes},
			})
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
}

func TestMCPTokenMintsScopedPAT(t *testing.T) {
	var scopes []string
	srv := mcpTokenServer(t, &scopes)
	defer srv.Close()
	withCredential(t, srv.URL)
	var stdout, stderr bytes.Buffer

	code := run([]string{"mcp", "token"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	// Prints the MINTED scoped PAT, never the raw device credential (s2s_test).
	for _, want := range []string{
		"Share2Us MCP endpoint: https://mcp.share2.us/mcp",
		"Authorization: Bearer s2u_pat_MINTED123",
		`"Authorization":"Bearer s2u_pat_MINTED123"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "s2s_test") {
		t.Fatalf("must NOT print the device credential:\n%s", out)
	}
	if !strings.Contains(stderr.String(), "scoped") {
		t.Fatalf("stderr missing scope note: %s", stderr.String())
	}
	want := map[string]bool{"shares:read": true, "shares:write": true, "shares:revoke": true, "account:read": true}
	if len(scopes) != len(want) {
		t.Fatalf("minted scopes = %v, want read/write/revoke/account:read", scopes)
	}
	for _, s := range scopes {
		if !want[s] {
			t.Fatalf("unexpected scope %q", s)
		}
	}
}

func TestMCPTokenJSONAndURLOverrides(t *testing.T) {
	tests := []struct {
		name string
		args []string
		url  string
	}{
		{name: "staging", args: []string{"mcp", "token", "--staging", "--json"}, url: "https://mcp.staging.share2.us/mcp"},
		{name: "url flag", args: []string{"mcp", "token", "--url", "https://mcp.custom.example.test/mcp", "--json"}, url: "https://mcp.custom.example.test/mcp"},
		{name: "url equals", args: []string{"mcp", "token", "--url=https://mcp.equals.example.test/mcp", "--json"}, url: "https://mcp.equals.example.test/mcp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := mcpTokenServer(t, nil)
			defer srv.Close()
			withCredential(t, srv.URL)
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("code = %d stderr=%s", code, stderr.String())
			}
			var got struct {
				URL   string `json:"url"`
				Token string `json:"token"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("decode json: %v\n%s", err, stdout.String())
			}
			if got.URL != tt.url || got.Token != "s2u_pat_MINTED123" {
				t.Fatalf("json = %+v", got)
			}
		})
	}
}

func TestMCPTokenNotLoggedIn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{"mcp", "token"}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("code = %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "not logged in; run `share2us login`") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestTUIRefusesNonTTYStdout(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{"tui"}, &stdout, &stderr)

	if code == 0 {
		t.Fatalf("code = %d, want non-zero", code)
	}
	if !strings.Contains(stderr.String(), "stdout is not a TTY") {
		t.Fatalf("stderr missing TTY refusal:\n%s", stderr.String())
	}
}

func TestInstallAgentToolsPrintsMCPConfig(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{"install-agent-tools", "--agent", "codex"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{
		"Share2Us agent tools for Codex",
		`"command": "share2us"`,
		"alias s2u=share2us",
		"alias share=share2us",
		`"args": ["mcp", "serve"]`,
		"share2us_preview_file",
		"confirm: true",
		"No silent uploads",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q in:\n%s", want, output)
		}
	}
}

// enableP2P flips the build-time flag on for a test and restores it after.
func enableP2P(t *testing.T) {
	t.Helper()
	prev := clicore.P2PEnabled
	clicore.P2PEnabled = "true"
	t.Cleanup(func() { clicore.P2PEnabled = prev })
}

// The build-time flag defaults OFF, so a stock binary refuses the P2P commands
// BEFORE it even looks at credentials — and it must do so for both the `p2p`
// group and the legacy `stream` alias.
func TestP2PDisabledByDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, args := range [][]string{
		{"stream", "movie.mov"},
		{"p2p", "send", "movie.mov"},
		{"p2p", "recv", "ABCD1234-SECRET789012"},
	} {
		var stdout, stderr bytes.Buffer
		code := run(args, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("%v: code = %d, want 1 while P2P is disabled", args, code)
		}
		if !strings.Contains(stderr.String(), "isn't available in this build") {
			t.Fatalf("%v: stderr = %q, want the disabled-build message", args, stderr.String())
		}
	}
}

// The usage text must not advertise a command the build will refuse to run.
func TestP2PHiddenFromUsageWhenDisabled(t *testing.T) {
	usage := clicore.Usage("share2us")
	if strings.Contains(usage, "p2p ") || strings.Contains(usage, "stream <file>") {
		t.Fatalf("usage advertises P2P while disabled:\n%s", usage)
	}
	enableP2P(t)
	if !strings.Contains(clicore.Usage("share2us"), "p2p send") {
		t.Fatal("usage should list p2p once the build enables it")
	}
}

func TestStreamRequiresLogin(t *testing.T) {
	enableP2P(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	// `stream` is an alias for `p2p send`; it requires an interactive login.
	code := run([]string{"stream", "movie.mov", "--relay", "wss://relay.example.test"}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("code = %d, want login failure", code)
	}
	if !strings.Contains(stderr.String(), "not logged in") {
		t.Fatalf("stderr missing login error:\n%s", stderr.String())
	}
}

func TestP2PRecvRequiresLogin(t *testing.T) {
	enableP2P(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{"p2p", "recv", "ABCD1234-SECRET789012"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code = %d, want login failure", code)
	}
	if !strings.Contains(stderr.String(), "not logged in") {
		t.Fatalf("stderr missing login error:\n%s", stderr.String())
	}
}

func TestPairingCodeRoundTrip(t *testing.T) {
	code, room, secret, err := newPairingCode()
	if err != nil {
		t.Fatalf("newPairingCode: %v", err)
	}
	if code != room+"-"+secret {
		t.Fatalf("code %q != %s-%s", code, room, secret)
	}
	gotRoom, gotSecret, ok := splitPairingCode("  " + strings.ToLower(code) + "  ")
	if !ok {
		t.Fatalf("splitPairingCode(%q) not ok", code)
	}
	if gotRoom != room || gotSecret != secret {
		t.Fatalf("round-trip mismatch: got %s-%s want %s-%s", gotRoom, gotSecret, room, secret)
	}
	if _, _, ok := splitPairingCode("nodash"); ok {
		t.Fatal("splitPairingCode should reject a code with no separator")
	}
}

func TestReceiveCommandRequiresLogin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{"receive", "--out", "received.bin"}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("code = %d, want login failure", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "not logged in") {
		t.Fatalf("stderr missing login error:\n%s", stderr.String())
	}
}

func TestP2PRelayEnvDefaultAndFlagOverride(t *testing.T) {
	t.Setenv("SHARE2US_RELAY_URL", "wss://relay.env.example.test")

	stream, err := parseStreamArgs([]string{"movie.mov"})
	if err != nil {
		t.Fatalf("parseStreamArgs() error = %v", err)
	}
	if stream.relay != "wss://relay.env.example.test" {
		t.Fatalf("stream relay = %q", stream.relay)
	}
	stream, err = parseStreamArgs([]string{"movie.mov", "--relay", "wss://relay.flag.example.test"})
	if err != nil {
		t.Fatalf("parseStreamArgs() flag error = %v", err)
	}
	if stream.relay != "wss://relay.flag.example.test" {
		t.Fatalf("stream relay flag override = %q", stream.relay)
	}

	receive, err := parseReceiveArgs([]string{"--watch", "--out", "received.bin"})
	if err != nil {
		t.Fatalf("parseReceiveArgs() error = %v", err)
	}
	if !receive.watch || receive.output != "received.bin" {
		t.Fatalf("receive options = %+v", receive)
	}
}

func uploadHandler(t *testing.T) http.Handler {
	t.Helper()
	return uploadHandlerForSize(t, 5)
}

func uploadHandlerForSize(t *testing.T, wantSize int64) http.Handler {
	t.Helper()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/put" && r.Header.Get("Authorization") != "Bearer s2s_test" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/v1/uploads":
			var req clicore.UploadCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode create request: %v", err)
			}
			if req.SizeBytes != uint64(wantSize) || req.SHA256 == "" || req.ExpiresIn == "" {
				t.Fatalf("create request = %+v", req)
			}
			writeTestJSON(w, map[string]any{
				"upload": map[string]any{
					"url":     "https://upload.example.test/put",
					"method":  "PUT",
					"headers": map[string]string{"X-Test": "yes"},
				},
				"share":             map[string]string{"public_id": "pub-1"},
				"upload_session_id": "upload-1",
				"expires_at":        "2026-07-03T00:00:00Z",
			})
		case "/put":
			if r.Method != http.MethodPut || r.Header.Get("X-Test") != "yes" {
				t.Fatalf("put method/header = %s %q", r.Method, r.Header.Get("X-Test"))
			}
			w.WriteHeader(http.StatusOK)
		case "/v1/uploads/upload-1/complete":
			writeTestJSON(w, map[string]string{"public_id": "pub-1", "status": "ready", "expires_at": "2026-07-03T00:00:00Z"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	})
}

func fakeSecretFileContent() string {
	return strings.Join([]string{
		"-----BEGIN " + "RSA PRIVATE KEY-----",
		fakePrivateKeyMaterial(),
		"-----END " + "RSA PRIVATE KEY-----",
		"",
	}, "\n")
}

func fakePrivateKeyMaterial() string {
	return "MIIEpAIBAAKCAQEA0" + strings.Repeat("testredacted", 4)
}

func withMockAPI(t *testing.T, handler http.Handler) {
	t.Helper()
	previous := clicore.DefaultHTTPClient
	clicore.DefaultHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder.Result(), nil
	})}
	t.Cleanup(func() {
		clicore.DefaultHTTPClient = previous
	})
}

func withCredential(t *testing.T, apiBase string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := clicore.SaveCredential(clicore.Credential{APIBase: apiBase, Token: "s2s_test", Email: "user@example.test"}); err != nil {
		t.Fatalf("save credential: %v", err)
	}
}

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func testUpdateArchive(t *testing.T, binary []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "share2us",
		Mode: 0o755,
		Size: int64(len(binary)),
	}); err != nil {
		t.Fatalf("write archive header: %v", err)
	}
	if _, err := tw.Write(binary); err != nil {
		t.Fatalf("write archive binary: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func writeTestJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
