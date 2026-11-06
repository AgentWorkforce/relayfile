package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIntegrationMetadataFilePreservesGitLabTypesAndMasksSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("metadata files require Unix permission validation")
	}
	_, _ = setupAdoptWorkspace(t)
	const secret = "private-webhook-secret-not-for-terminal-output"
	input := `{"projectIds":["86233786"],"manageWebhooks":false,"webhookSecret":"` + secret + `","api_key":"root-private","nested":{"accessToken":"also-private","api_key":"underscore-private","api-key":"hyphen-private"},"\u001b[31mspoof\nContinue?":"value"}`
	file := filepath.Join(t.TempDir(), "metadata.json")
	if err := os.WriteFile(file, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	server, seen := newSetMetadataTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspaces/ws_123/integrations/gitlab/metadata" || r.Header.Get("Authorization") != "Bearer cld_new" {
			t.Fatalf("unexpected identity: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body struct {
			Metadata map[string]any `json:"metadata"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		ids, ok := body.Metadata["projectIds"].([]any)
		if !ok || len(ids) != 1 || ids[0] != "86233786" || body.Metadata["manageWebhooks"] != false || body.Metadata["webhookSecret"] != secret {
			t.Fatalf("typed payload changed: keys=%v", len(body.Metadata))
		}
		_, _ = w.Write([]byte(`{"ok":true,"metadata":{"projectIds":["86233786"],"manageWebhooks":false,"webhookSecret":"[redacted]"}}`))
	})
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)
	var output bytes.Buffer
	err := run([]string{"integration", "set-metadata", "gitlab-pat", "--metadata-file", file,
		"--workspace", "demo", "--cloud-api-url", server.URL}, strings.NewReader("yes\n"), &output, &output)
	if err != nil {
		t.Fatalf("set-metadata failed: %v\n%s", err, output.String())
	}
	if seen["metadata"] != 1 || !strings.Contains(output.String(), "gitlab metadata updated") {
		t.Fatalf("expected exactly one successful PUT, got %v: %s", seen, output.String())
	}
	for _, private := range []string{secret, "also-private", "root-private", "underscore-private", "hyphen-private"} {
		if strings.Contains(output.String(), private) {
			t.Fatal("metadata preview exposed a secret")
		}
	}
	if strings.ContainsRune(output.String(), '\x1b') || strings.Contains(output.String(), "\nContinue? =") {
		t.Fatal("metadata preview printed terminal control characters from a key")
	}
	if !strings.Contains(output.String(), "86233786") || !strings.Contains(output.String(), "false") {
		t.Fatalf("preview omitted selected project or boolean: %s", output.String())
	}
}

func TestIntegrationMetadataFileRejectsInvalidInputBeforeNetwork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("metadata files require Unix permission validation")
	}
	for _, input := range []string{`null`, `[]`, `"secret-value"`, `{"projectIds":`, `{} {}`} {
		t.Run(input, func(t *testing.T) {
			_, _ = setupAdoptWorkspace(t)
			file := filepath.Join(t.TempDir(), "metadata.json")
			if err := os.WriteFile(file, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			err := run([]string{"integration", "set-metadata", "gitlab", "--metadata-file", file, "--yes"}, strings.NewReader(""), &output, &output)
			if err == nil || !strings.Contains(err.Error(), "metadata JSON") {
				t.Fatalf("expected local JSON refusal, got %v", err)
			}
			if strings.Contains(output.String()+err.Error(), "secret-value") {
				t.Fatal("invalid input was echoed")
			}
		})
	}
}

func TestIntegrationMetadataFileRefusesMixedAssignments(t *testing.T) {
	_, _ = setupAdoptWorkspace(t)
	var output bytes.Buffer
	err := run([]string{"integration", "set-metadata", "gitlab", "projectIds=86233786", "--metadata-file", "unused.json", "--yes"}, strings.NewReader(""), &output, &output)
	if err == nil || !strings.Contains(err.Error(), "cannot combine") {
		t.Fatalf("expected ambiguous input refusal before file/network access, got %v", err)
	}
}

func TestIntegrationSetMetadataResolvesLegacyRelayfileWorkspace(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		t.Run(map[bool]string{true: "authorized", false: "forbidden"}[allowed], func(t *testing.T) {
			record, _ := setupAdoptWorkspace(t)
			record.ID = "rw_legacy"
			if _, err := upsertWorkspaceDetails(record); err != nil {
				t.Fatal(err)
			}
			resolved, wrote := false, false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer cld_new" {
					t.Error("request did not use normal Cloud credentials")
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/workspaces/rw_legacy/resolve":
					resolved = true
					if !allowed {
						http.Error(w, `{"code":"forbidden"}`, http.StatusForbidden)
						return
					}
					_, _ = w.Write([]byte(`{"cloudWorkspaceId":"cloud_canonical","relayfileWorkspaceId":"rw_legacy"}`))
				case "PUT /api/v1/workspaces/cloud_canonical/integrations/gitlab/metadata":
					wrote = true
					_, _ = w.Write([]byte(`{"ok":true}`))
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			pointAdoptCloudCredentials(t, server.URL)
			var output bytes.Buffer
			err := run([]string{"integration", "set-metadata", "gitlab", "note=fixture", "--workspace", "demo", "--cloud-api-url", server.URL, "--yes"}, strings.NewReader(""), &output, &output)
			if !resolved || wrote != allowed || (err == nil) != allowed {
				t.Fatalf("resolution=%v write=%v allowed=%v error=%v", resolved, wrote, allowed, err)
			}
		})
	}
}
