package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDigestFunctionDeployHappy(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "eng-roadmap.ts")
	source := []byte(`export const digest = async () => ({ provider: "Eng Roadmap", bullets: [] });`)
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	var posted digestFunctionDeployRequest
	server := newDigestFunctionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/workspaces/ws_digest/digest-functions" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(digestFunctionDeployResponse{
			DigestFunctionID: "df_123",
			Version:          1,
			SHA256:           "sha256:abcdef1234567890",
			Status:           "active",
		})
	})
	defer server.Close()
	setupDigestFunctionTestAuth(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{"digest", "function", "deploy", sourcePath, "--name", "roadmap"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}
	if posted.Slug != "roadmap" {
		t.Fatalf("expected posted slug roadmap, got %q", posted.Slug)
	}
	if posted.Source.Runtime != "node20" || posted.Source.Entrypoint != "eng-roadmap.ts" {
		t.Fatalf("unexpected source metadata: %+v", posted.Source)
	}
	if len(posted.Source.Files) != 1 || posted.Source.Files[0].Path != "eng-roadmap.ts" || posted.Source.Files[0].Contents != string(source) {
		t.Fatalf("unexpected posted files: %+v", posted.Source.Files)
	}
	if got := stdout.String(); !strings.Contains(got, "function_id: df_123") || !strings.Contains(got, "sha256: sha256:") {
		t.Fatalf("unexpected deploy output: %q", got)
	}
}

func TestDigestFunctionDeployRejectsNonTSJSBeforeNetwork(t *testing.T) {
	serverCalled := false
	server := newDigestFunctionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		serverCalled = true
		t.Fatalf("server should not be called")
	})
	defer server.Close()
	setupDigestFunctionTestAuth(t, server.URL)

	var stdout bytes.Buffer
	err := run([]string{"digest", "function", "deploy", "bad.go"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil || !strings.Contains(err.Error(), "only .ts/.js sources are supported") {
		t.Fatalf("expected extension error, got %v", err)
	}
	if serverCalled {
		t.Fatalf("server was called")
	}
}

func TestDigestFunctionDeployMissingFileBeforeNetwork(t *testing.T) {
	serverCalled := false
	server := newDigestFunctionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		serverCalled = true
		t.Fatalf("server should not be called")
	})
	defer server.Close()
	setupDigestFunctionTestAuth(t, server.URL)

	var stdout bytes.Buffer
	err := run([]string{"digest", "function", "deploy", filepath.Join(t.TempDir(), "missing.ts")}, strings.NewReader(""), &stdout, &stdout)
	if err == nil || !strings.Contains(err.Error(), "read digest function") {
		t.Fatalf("expected missing file error, got %v", err)
	}
	if serverCalled {
		t.Fatalf("server was called")
	}
}

func TestDigestFunctionListEmpty(t *testing.T) {
	server := newDigestFunctionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/workspaces/ws_digest/digest-functions" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"digestFunctions":[],"nextCursor":null}`))
	})
	defer server.Close()
	setupDigestFunctionTestAuth(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{"digest", "function", "list"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "no digest functions deployed" {
		t.Fatalf("unexpected list output: %q", got)
	}
}

func TestDigestFunctionListJSON(t *testing.T) {
	server := newDigestFunctionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"digestFunctions":[{"digestFunctionId":"df_123","name":"roadmap","status":"active","sha256":"sha256:abcdef1234567890","bytes":64,"createdAt":"2026-05-13T00:00:00Z"}],"nextCursor":null}`))
	})
	defer server.Close()
	setupDigestFunctionTestAuth(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{"digest", "function", "list", "--json"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("list --json failed: %v", err)
	}
	var records []digestFunctionRecord
	if err := json.Unmarshal(stdout.Bytes(), &records); err != nil {
		t.Fatalf("decode json output: %v", err)
	}
	if len(records) != 1 || records[0].Name != "roadmap" {
		t.Fatalf("unexpected json records: %+v", records)
	}
}

func TestDigestFunctionShowNotFound(t *testing.T) {
	server := newDigestFunctionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workspaces/ws_digest/digest-functions" {
			_, _ = w.Write([]byte(`{"digestFunctions":[],"nextCursor":null}`))
			return
		}
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	})
	defer server.Close()
	setupDigestFunctionTestAuth(t, server.URL)

	var stdout bytes.Buffer
	err := run([]string{"digest", "function", "show", "missing"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil || !strings.Contains(err.Error(), `digest function "missing" was not found`) {
		t.Fatalf("expected friendly not found error, got %v", err)
	}
}

func TestDigestFunctionDisableHappy(t *testing.T) {
	server := newDigestFunctionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/ws_digest/digest-functions/roadmap" {
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/ws_digest/digest-functions" {
			_, _ = w.Write([]byte(`{"digestFunctions":[{"digestFunctionId":"df_123","name":"roadmap","status":"active","sha256":"sha256:abc","bytes":64,"createdAt":"2026-05-13T00:00:00Z"}]}`))
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/workspaces/ws_digest/digest-functions/df_123/disable" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"digestFunctionId":"df_123","status":"disabled","disabledAt":"2026-05-13T00:00:00Z","alreadyDisabled":false}`))
	})
	defer server.Close()
	setupDigestFunctionTestAuth(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{"digest", "function", "disable", "roadmap"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("disable failed: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "status: disabled") {
		t.Fatalf("unexpected disable output: %q", got)
	}
}

func TestDigestFunctionLogsNoTail(t *testing.T) {
	server := newDigestFunctionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/ws_digest/digest-functions/roadmap" {
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/ws_digest/digest-functions" {
			_, _ = w.Write([]byte(`{"digestFunctions":[{"digestFunctionId":"df_123","name":"roadmap","status":"active","sha256":"sha256:abc","bytes":64,"createdAt":"2026-05-13T00:00:00Z"}]}`))
			return
		}
		if r.URL.Path != "/api/v1/workspaces/ws_digest/digest-functions/df_123/logs" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.URL.RawQuery != "" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"digestFunctionId":"df_123","logs":[{"invocationId":"inv_1","occurredAt":"2026-05-13T00:00:00Z","level":"warn","message":"timeout"}],"nextCursor":null}`))
	})
	defer server.Close()
	setupDigestFunctionTestAuth(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{"digest", "function", "logs", "roadmap"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("logs failed: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "warn\ttimeout") {
		t.Fatalf("unexpected logs output: %q", got)
	}
}

func TestDigestFunctionLogsTailFlagParsed(t *testing.T) {
	server := newDigestFunctionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/ws_digest/digest-functions/roadmap" {
			_, _ = w.Write([]byte(`{"digestFunctionId":"df_123","name":"roadmap","status":"active","sha256":"sha256:abc","bytes":64,"createdAt":"2026-05-13T00:00:00Z","updatedAt":"2026-05-13T00:00:00Z","entrypoint":"eng-roadmap.ts","runtime":"node20"}`))
			return
		}
		if r.URL.Path != "/api/v1/workspaces/ws_digest/digest-functions/df_123/logs" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("tail") != "true" {
			t.Fatalf("expected tail=true, got %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte("stream unavailable\n"))
	})
	defer server.Close()
	setupDigestFunctionTestAuth(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{"digest", "function", "logs", "roadmap", "--tail"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("logs --tail failed: %v", err)
	}
	if got := stdout.String(); got != "stream unavailable\n" {
		t.Fatalf("unexpected tail output: %q", got)
	}
}

func TestDigestFunctionTestOfflineRendersSection(t *testing.T) {
	serverCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCalled = true
		t.Fatalf("digest function test should not call the control plane")
	}))
	defer server.Close()
	t.Setenv("RELAYFILE_SERVER", server.URL)

	sourcePath := filepath.Join("testdata", "digest", "eng-roadmap.ts")
	fixturePath := filepath.Join("testdata", "digest", "events.json")
	var stdout bytes.Buffer
	if err := run([]string{"digest", "function", "test", sourcePath, "--fixture", fixturePath}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("test failed: %v\noutput:\n%s", err, stdout.String())
	}
	if serverCalled {
		t.Fatalf("server was called")
	}
	got := stdout.String()
	for _, want := range []string{
		"events: 2",
		"## Eng Roadmap",
		"- spec.md changed - [/notion/databases/eng-roadmap/spec.md]",
		"- rollout.md changed - [/notion/databases/eng-roadmap/rollout.md]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestDigestFunctionTestBadFixture(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "eng-roadmap.ts")
	if err := os.WriteFile(sourcePath, []byte(`export const digest = async () => null;`), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	fixturePath := filepath.Join(dir, "events.json")
	if err := os.WriteFile(fixturePath, []byte(`{"not":"an array"}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var stdout bytes.Buffer
	err := run([]string{"digest", "function", "test", sourcePath, "--fixture", fixturePath}, strings.NewReader(""), &stdout, &stdout)
	if err == nil || !strings.Contains(err.Error(), "expected JSON array") {
		t.Fatalf("expected fixture parse error, got %v", err)
	}
}

func newDigestFunctionTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		handler(w, r)
	}))
}

func setupDigestFunctionTestAuth(t *testing.T, serverURL string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Setenv("RELAYFILE_CLOUD_API_URL", serverURL)
	t.Setenv("RELAYFILE_CLOUD_TOKEN", "token")
	t.Setenv("RELAYFILE_WORKSPACE", "ws_digest")
}
