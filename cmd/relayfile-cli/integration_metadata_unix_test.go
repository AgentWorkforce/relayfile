//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

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
