package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationMetadataFilePreservesGitLabTypesAndMasksSecrets(t *testing.T) {
	_, _ = setupAdoptWorkspace(t)
	const secret = "private-webhook-secret-not-for-terminal-output"
	input := `{"projectIds":["86233786"],"manageWebhooks":false,"webhookSecret":"` + secret + `","nested":{"accessToken":"also-private"}}`
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
	if strings.Contains(output.String(), secret) || strings.Contains(output.String(), "also-private") {
		t.Fatal("metadata preview exposed a secret")
	}
	if !strings.Contains(output.String(), "86233786") || !strings.Contains(output.String(), "false") {
		t.Fatalf("preview omitted selected project or boolean: %s", output.String())
	}
}

func TestIntegrationMetadataFileRejectsInvalidInputBeforeNetwork(t *testing.T) {
	for _, input := range []string{`null`, `[]`, `"secret-value"`, `{"projectIds":`, `{} {}`} {
		t.Run(input, func(t *testing.T) {
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
	var output bytes.Buffer
	err := run([]string{"integration", "set-metadata", "gitlab", "projectIds=86233786", "--metadata-file", "unused.json", "--yes"}, strings.NewReader(""), &output, &output)
	if err == nil || !strings.Contains(err.Error(), "cannot combine") {
		t.Fatalf("expected ambiguous input refusal before file/network access, got %v", err)
	}
}
