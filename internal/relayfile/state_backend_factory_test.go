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
	backend, err := BuildStateBackendFromDSN("postgres://localhost/relayfile?sslmode=disable")
	if err != nil {
		t.Fatalf("expected postgres state backend to be available, got %v", err)
	}
	if backend == nil {
		t.Fatalf("expected non-nil postgres state backend")
	}
	if _, err := BuildStateBackendFromDSN("mysql://localhost/relayfile"); err == nil {
		t.Fatalf("expected not implemented error for mysql state backend")
	} else if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("expected not implemented error for mysql state backend, got %v", err)
	}
}
