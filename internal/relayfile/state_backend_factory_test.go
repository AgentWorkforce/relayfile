package relayfile

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestBuildStateBackendFromDSNMemory(t *testing.T) {
	backend, err := BuildStateBackendFromDSN("memory://")
	if err != nil {
		t.Fatalf("build state backend failed: %v", err)
	}
	if backend == nil {
		t.Fatalf("expected non-nil memory state backend")
	}
	if err := backend.Save(&persistedState{RevCounter: 3}); err != nil {
		t.Fatalf("memory backend save failed: %v", err)
	}
	snapshot, err := backend.Load()
	if err != nil {
		t.Fatalf("memory backend load failed: %v", err)
	}
	if snapshot == nil || snapshot.RevCounter != 3 {
		t.Fatalf("expected revCounter 3, got %+v", snapshot)
	}
}

func TestBuildStateBackendFromDSNFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state-backend.json")
	backend, err := BuildStateBackendFromDSN("file://" + path)
	if err != nil {
		t.Fatalf("build file state backend failed: %v", err)
	}
	if backend == nil {
		t.Fatalf("expected non-nil file state backend")
	}
	if err := backend.Save(&persistedState{RevCounter: 7}); err != nil {
		t.Fatalf("file backend save failed: %v", err)
	}
	snapshot, err := backend.Load()
	if err != nil {
		t.Fatalf("file backend load failed: %v", err)
	}
	if snapshot == nil || snapshot.RevCounter != 7 {
		t.Fatalf("expected revCounter 7, got %+v", snapshot)
	}
}

func TestBuildStateBackendFromDSNUnsupported(t *testing.T) {
	if _, err := BuildStateBackendFromDSN("postgres://localhost/relayfile"); err == nil {
		t.Fatalf("expected unsupported scheme error")
	} else if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("expected not implemented error for postgres state backend, got %v", err)
	}
}
