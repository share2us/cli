package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	clicore "github.com/share2us/cli-core"
)

// captureCreate wraps the mock upload API and records the create request.
func captureCreate(t *testing.T, got *clicore.UploadCreateRequest, seen *bool) http.Handler {
	t.Helper()
	inner := uploadHandler(t)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A project post first looks for the project among the caller's Secure ShareNets.
		if r.URL.Path == "/v1/sharenets" {
			writeTestJSON(w, map[string]any{"sharenets": []any{}})
			return
		}
		if r.URL.Path == "/v1/uploads" {
			if err := json.NewDecoder(r.Body).Decode(got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			*seen = true
			r.Body = io.NopCloser(bytes.NewReader(mustMarshal(t, *got)))
		}
		inner.ServeHTTP(w, r)
	})
}

func TestArtifactFlagSendsTheArtifactClass(t *testing.T) {
	var got clicore.UploadCreateRequest
	var seen bool
	withMockAPI(t, captureCreate(t, &got, &seen))
	withCredential(t, "https://api.example.test")
	file := writeTempFile(t, "chart.html", "hello")

	var stdout, stderr bytes.Buffer
	if code := run([]string{file, "--artifact"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !seen || got.ContentClass != "artifact" || got.ContentType != "text/html" || got.Encrypted || got.SourceRef != "" {
		t.Fatalf("create request = %+v", got)
	}
	if !strings.Contains(stdout.String(), "Interactive artifact") {
		t.Fatalf("stdout lacks the artifact notice:\n%s", stdout.String())
	}
}

// Without --artifact an .html file is just a file: nothing about it runs.
func TestHTMLWithoutTheFlagIsNotAnArtifact(t *testing.T) {
	var got clicore.UploadCreateRequest
	var seen bool
	withMockAPI(t, captureCreate(t, &got, &seen))
	withCredential(t, "https://api.example.test")
	file := writeTempFile(t, "chart.html", "hello")

	var stdout, stderr bytes.Buffer
	if code := run([]string{file}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !seen || got.ContentClass == "artifact" || strings.Contains(stdout.String(), "Interactive artifact") {
		t.Fatalf("plain .html became an artifact: %+v", got)
	}
}

func TestArtifactFlagRefusesWhatCannotRun(t *testing.T) {
	cases := map[string][]string{
		"not html":  {"notes.txt"},
		"password":  {"chart.html", "--password"},
		"password=": {"chart.html", "--password=x"},
		"encrypt":   {"chart.html", "-e"},
		"device":    {"chart.html", "--device", "laptop"},
		"contact":   {"chart.html", "--contact=a@example.com"},
		"live":      {"chart.html", "--live"},
		"sharenet":  {"chart.html", "--sharenet", "sn_1"},
		"project":   {"chart.html", "--project=p_1"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var got clicore.UploadCreateRequest
			var seen bool
			withMockAPI(t, captureCreate(t, &got, &seen))
			withCredential(t, "https://api.example.test")
			file := writeTempFile(t, args[0], "hello")
			var stdout, stderr bytes.Buffer
			code := run(append([]string{file, "--artifact"}, args[1:]...), &stdout, &stderr)
			if code == 0 || seen {
				t.Fatalf("code = %d, create sent = %v; stderr=%s", code, seen, stderr.String())
			}
			if !strings.Contains(stderr.String(), "artifact") {
				t.Fatalf("stderr does not explain: %s", stderr.String())
			}
		})
	}
}

// Recipient links and "only me" links are fine: the gateway gates decide who opens it.
func TestArtifactFlagAllowsPrivateAndRecipientLinks(t *testing.T) {
	cfg, err := parseUploadArgs([]string{"chart.html", "--artifact", "--private", "--to", "a@example.com", "--max-views", "3"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.artifact || !cfg.private || len(cfg.recipients) != 1 || cfg.encrypt {
		t.Fatalf("opts = %+v", cfg)
	}
}

// A standing `config set-default encrypt true` describes ordinary link shares; it is
// dropped for an artifact rather than making every artifact upload fail.
func TestArtifactDropsAStandingEncryptDefault(t *testing.T) {
	var got clicore.UploadCreateRequest
	var seen bool
	withMockAPI(t, captureCreate(t, &got, &seen))
	withCredential(t, "https://api.example.test")
	var out, errb bytes.Buffer
	if code := run([]string{"config", "set-default", "encrypt", "true"}, &out, &errb); code != 0 {
		t.Fatalf("set-default: %d %s", code, errb.String())
	}
	file := writeTempFile(t, "chart.html", "hello")
	var stdout, stderr bytes.Buffer
	if code := run([]string{file, "--artifact"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !seen || got.Encrypted || got.ContentClass != "artifact" {
		t.Fatalf("create request = %+v", got)
	}
}

// ADR-050: inside an agent's process, a sharenet project post says which agent is
// posting; a human's (no S2U_AGENT_ID) and any non-sharenet upload say nothing.
func TestSharenetPostCarriesTheAgentID(t *testing.T) {
	for name, tc := range map[string]struct {
		env  string
		args []string
		want string
	}{
		"agent project post": {"agt_abcdefghijklmnop", []string{"--project", "11111111-1111-1111-1111-111111111111"}, "agt_abcdefghijklmnop"},
		"human project post": {"", []string{"--project", "11111111-1111-1111-1111-111111111111"}, ""},
		"agent plain upload": {"agt_abcdefghijklmnop", nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("S2U_AGENT_ID", tc.env)
			var got clicore.UploadCreateRequest
			var seen bool
			withMockAPI(t, captureCreate(t, &got, &seen))
			withCredential(t, "https://api.example.test")
			file := writeTempFile(t, "notes.txt", "hello")
			var stdout, stderr bytes.Buffer
			run(append([]string{file}, tc.args...), &stdout, &stderr)
			if !seen {
				t.Fatalf("no create sent; stderr=%s", stderr.String())
			}
			if got.AgentID != tc.want {
				t.Fatalf("agent_id = %q, want %q", got.AgentID, tc.want)
			}
		})
	}
}
