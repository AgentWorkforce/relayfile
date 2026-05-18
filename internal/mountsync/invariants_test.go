package mountsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckMountRootInvariant_OK exercises the happy path.
func TestCheckMountRootInvariant_OK(t *testing.T) {
	root := t.TempDir()
	if err := CheckMountRootInvariant(root); err != nil {
		t.Fatalf("expected nil for directory: %v", err)
	}
}

// TestCheckMountRootInvariant_Missing returns a typed missing error.
func TestCheckMountRootInvariant_Missing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-there")
	err := CheckMountRootInvariant(missing)
	if err == nil {
		t.Fatalf("expected error for missing root")
	}
	if !errors.Is(err, ErrMountRootInvariantBroken) {
		t.Fatalf("expected ErrMountRootInvariantBroken; got %v", err)
	}
	var inv *MountRootInvariantError
	if !errors.As(err, &inv) {
		t.Fatalf("expected MountRootInvariantError; got %T", err)
	}
	if inv.Kind != "missing" {
		t.Fatalf("expected kind=missing, got %q", inv.Kind)
	}
}

// TestCheckMountRootInvariant_RegularFile is the clobber signature.
func TestCheckMountRootInvariant_RegularFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mount")
	if err := os.WriteFile(root, []byte("clobbered"), 0o644); err != nil {
		t.Fatalf("write fake clobber file: %v", err)
	}
	err := CheckMountRootInvariant(root)
	if err == nil {
		t.Fatalf("expected error for regular file at mount root")
	}
	var inv *MountRootInvariantError
	if !errors.As(err, &inv) || inv.Kind != "regular_file" {
		t.Fatalf("expected kind=regular_file, got %v", err)
	}
}

// TestWriteIncidentReport_FallsBackToParent when the mount root is missing.
func TestWriteIncidentReport_FallsBackToParent(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "mount")
	// root does not exist
	err := CheckMountRootInvariant(root)
	if err == nil {
		t.Fatalf("setup: expected invariant violation")
	}
	var inv *MountRootInvariantError
	errors.As(err, &inv)
	path, werr := WriteIncidentReport(root, inv)
	if werr != nil {
		t.Fatalf("write incident report: %v", werr)
	}
	if path == "" {
		t.Fatalf("expected an incident path")
	}
	if !strings.HasPrefix(path, parent) {
		t.Fatalf("expected report under parent %s, got %s", parent, path)
	}
	if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
		t.Fatalf("incident report must not recreate missing mount root; stat err=%v", statErr)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "mount-root invariant incident") {
		t.Fatalf("incident body missing header: %s", string(body))
	}
}

// TestSyncRefusesWhenMountRootIsRegularFile asserts SyncOnce returns the
// typed error and does NOT touch state when the root has been clobbered.
func TestSyncRefusesWhenMountRootIsRegularFile(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "mount")
	// Seed: clobber the mount root with a regular file.
	if err := os.WriteFile(root, []byte("oops"), 0o644); err != nil {
		t.Fatalf("seed clobber: %v", err)
	}

	disableWS := false
	fc := &fakeClient{files: map[string]RemoteFile{}, eventsUnsupported: true}
	syncer, err := NewSyncer(fc, SyncerOptions{
		WorkspaceID: "ws_clobbered",
		LocalRoot:   root,
		WebSocket:   &disableWS,
	})
	if err != nil {
		// NewSyncer calls os.MkdirAll which may itself fail because root
		// is a file. Either outcome is acceptable defense — if NewSyncer
		// errored, the daemon never starts.
		if strings.Contains(err.Error(), "mkdir") || strings.Contains(err.Error(), "not a directory") {
			return
		}
		t.Fatalf("new syncer: %v", err)
	}
	err = syncer.SyncOnce(context.Background())
	if err == nil {
		t.Fatalf("expected SyncOnce to refuse when mount root is a regular file")
	}
	if !errors.Is(err, ErrMountRootInvariantBroken) {
		t.Fatalf("expected ErrMountRootInvariantBroken, got %v", err)
	}
}
