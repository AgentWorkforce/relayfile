//go:build !windows

package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestIntegrationMetadataFileRequiresPrivateUnixPermissions(t *testing.T) {
	_, _ = setupAdoptWorkspace(t)
	server, _ := newSetMetadataTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("metadata request escaped local validation")
		http.Error(w, "unexpected metadata write", http.StatusInternalServerError)
	})
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)
	path := filepath.Join(t.TempDir(), "metadata.json")
	if err := os.WriteFile(path, []byte(`{"webhookSecret":"private-fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := run([]string{"integration", "set-metadata", "gitlab", "--metadata-file", path, "--workspace", "demo", "--cloud-api-url", server.URL, "--yes"}, strings.NewReader(""), &output, &output)
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("expected private-file refusal before workspace/auth lookup, got %v", err)
	}
	if strings.Contains(output.String()+err.Error(), "private-fixture") {
		t.Fatal("permission refusal printed metadata contents")
	}
}

func TestIntegrationMetadataFileRejectsFIFOWithoutWaitingForWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.pipe")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := readIntegrationMetadataFile(path)
		finished <- err
	}()
	select {
	case err := <-finished:
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("expected regular-file refusal, got %v", err)
		}
	case <-time.After(time.Second):
		// Unblock a regressed reader before failing the test.
		writer, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			writer.Close()
		}
		t.Fatal("metadata file read blocked waiting for a pipe writer")
	}
}
