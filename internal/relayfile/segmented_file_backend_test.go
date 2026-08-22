package relayfile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSegmentedFileStateBackendRoundTripAndRevisionIsolation(t *testing.T) {
	backend := NewSegmentedFileStateBackend(filepath.Join(t.TempDir(), "state"))
	first := &persistedState{
		RevCounter:   2,
		EventCounter: 2,
		Workspaces: map[string]*workspaceState{
			"ws_large": {
				Revision: "rev_2",
				Files: map[string]File{
					"/a.txt": {
						Path:        "/a.txt",
						Revision:    "rev_1",
						ContentHash: "hash_a",
						ContentType: "text/plain",
						Content:     "payload-that-must-not-be-in-metadata",
					},
					"/binary.dat": {
						Path:        "/binary.dat",
						Revision:    "rev_2",
						ContentHash: "hash_binary",
						ContentType: "application/octet-stream",
						Content:     "AAEC/w==",
						Encoding:    "base64",
					},
				},
				Events: []Event{{EventID: "evt_1", Type: "file.created", Path: "/a.txt", Revision: "rev_1"}},
				Ops:    map[string]OperationStatus{},
			},
		},
		Suppressions: map[string]time.Time{
			"suppression-old": time.Date(2026, 8, 21, 20, 0, 0, 0, time.UTC),
		},
	}
	if err := backend.Save(first); err != nil {
		t.Fatalf("save first state: %v", err)
	}
	metadata, err := os.ReadFile(backend.metadataPath())
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if strings.Contains(string(metadata), "payload-that-must-not-be-in-metadata") ||
		strings.Contains(string(metadata), "AAEC/w==") ||
		strings.Contains(string(metadata), "/a.txt") ||
		strings.Contains(string(metadata), "evt_1") {
		t.Fatal("segmented metadata contains file, event, or payload records")
	}
	loaded, err := backend.Load()
	if err != nil {
		t.Fatalf("load first state: %v", err)
	}
	if !reflect.DeepEqual(loaded, first) {
		t.Fatalf("round trip mismatch:\n got: %#v\nwant: %#v", loaded, first)
	}

	second := *first
	second.RevCounter = 3
	second.Workspaces = map[string]*workspaceState{
		"ws_large": {
			Revision: "rev_3",
			Files: map[string]File{
				"/a.txt": {
					Path:        "/a.txt",
					Revision:    "rev_3",
					ContentHash: "hash_a_next",
					ContentType: "text/plain",
					Content:     "next payload",
				},
			},
			Events: []Event{{EventID: "evt_3", Type: "file.updated", Path: "/a.txt", Revision: "rev_3"}},
			Ops:    map[string]OperationStatus{},
		},
	}
	if err := backend.Save(&second); err != nil {
		t.Fatalf("save second state: %v", err)
	}
	loaded, err = backend.Load()
	if err != nil {
		t.Fatalf("load second state: %v", err)
	}
	if !reflect.DeepEqual(loaded, &second) {
		t.Fatalf("second round trip mismatch:\n got: %#v\nwant: %#v", loaded, &second)
	}
	if _, exists := loaded.Workspaces["ws_large"].Files["/binary.dat"]; exists {
		t.Fatal("deleted file returned from an orphaned old-revision blob")
	}
}

func TestSegmentedFileStateBackendPersistsOnlyChangedMetadata(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	backend := NewSegmentedFileStateBackend(root)
	first := &persistedState{
		RevCounter:   1,
		OpCounter:    1,
		EventCounter: 1,
		Workspaces: map[string]*workspaceState{
			"ws_delta": {
				Revision: "rev_1",
				Files: map[string]File{
					"/a.txt": {Path: "/a.txt", Revision: "rev_1", Content: "first"},
				},
				Events: []Event{{EventID: "evt_1", Type: "file.created", Path: "/a.txt", Revision: "rev_1"}},
				Ops: map[string]OperationStatus{
					"op_1": {OpID: "op_1", Path: "/a.txt", Revision: "rev_1", Status: "pending"},
				},
			},
		},
		Suppressions: map[string]time.Time{
			"suppression-old": time.Date(2026, 8, 21, 20, 0, 0, 0, time.UTC),
		},
	}
	if err := backend.Save(first); err != nil {
		t.Fatalf("save first state: %v", err)
	}

	second := &persistedState{
		RevCounter:   2,
		OpCounter:    1,
		EventCounter: 2,
		Workspaces: map[string]*workspaceState{
			"ws_delta": {
				Revision: "rev_2",
				Files: map[string]File{
					"/a.txt": {Path: "/a.txt", Revision: "rev_2", Content: "second"},
				},
				Events: []Event{
					{EventID: "evt_1", Type: "file.created", Path: "/a.txt", Revision: "rev_1"},
					{EventID: "evt_2", Type: "file.updated", Path: "/a.txt", Revision: "rev_2"},
				},
				Ops: map[string]OperationStatus{
					"op_1": {OpID: "op_1", Path: "/a.txt", Revision: "rev_1", Status: "succeeded"},
				},
			},
		},
		Suppressions: map[string]time.Time{
			"suppression-new": time.Date(2026, 8, 21, 21, 0, 0, 0, time.UTC),
		},
	}
	if err := backend.Save(second); err != nil {
		t.Fatalf("save second state: %v", err)
	}

	eventRecords, err := os.ReadDir(filepath.Join(backend.generationPath(2), "events"))
	if err != nil {
		t.Fatalf("read second event generation: %v", err)
	}
	if len(eventRecords) != 1 {
		t.Fatalf("second event generation records = %d, want 1", len(eventRecords))
	}
	eventData, err := os.ReadFile(filepath.Join(backend.generationPath(2), "events", eventRecords[0].Name()))
	if err != nil {
		t.Fatalf("read second event delta: %v", err)
	}
	if strings.Contains(string(eventData), "evt_1") || !strings.Contains(string(eventData), "evt_2") {
		t.Fatalf("second event delta is not append-only: %s", eventData)
	}
	metadata, err := os.ReadFile(backend.metadataPath())
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if len(metadata) > 1_024 || strings.Contains(string(metadata), "/a.txt") || strings.Contains(string(metadata), "op_1") || strings.Contains(string(metadata), "suppression-new") {
		t.Fatalf("commit metadata is not bounded: %d bytes: %s", len(metadata), metadata)
	}

	restarted := NewSegmentedFileStateBackend(root)
	loaded, err := restarted.Load()
	if err != nil {
		t.Fatalf("load delta state: %v", err)
	}
	if !reflect.DeepEqual(loaded, second) {
		t.Fatalf("delta round trip mismatch:\n got: %#v\nwant: %#v", loaded, second)
	}
}

func TestSegmentedFileStateBackendMigratesLegacySnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	backend := NewSegmentedFileStateBackend(root)
	legacy := &persistedState{
		RevCounter:   1,
		EventCounter: 1,
		Workspaces: map[string]*workspaceState{
			"ws_legacy": {
				Revision: "rev_1",
				Files: map[string]File{
					"/legacy.txt": {Path: "/legacy.txt", Revision: "rev_1", Content: ""},
				},
				Events: []Event{{EventID: "evt_1", Type: "file.created", Path: "/legacy.txt", Revision: "rev_1"}},
				Ops:    map[string]OperationStatus{},
			},
		},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy state: %v", err)
	}
	if err := writeSegmentedFileAtomic(backend.metadataPath(), data, 0o600); err != nil {
		t.Fatalf("write legacy metadata: %v", err)
	}
	if err := writeSegmentedFileAtomic(backend.blobPath("ws_legacy", "/legacy.txt", "rev_1"), []byte("legacy payload"), 0o600); err != nil {
		t.Fatalf("write legacy payload: %v", err)
	}

	loaded, err := backend.Load()
	if err != nil {
		t.Fatalf("load legacy state: %v", err)
	}
	legacy.Workspaces["ws_legacy"].Files["/legacy.txt"] = File{Path: "/legacy.txt", Revision: "rev_1", Content: "legacy payload"}
	if !reflect.DeepEqual(loaded, legacy) {
		t.Fatalf("legacy load mismatch:\n got: %#v\nwant: %#v", loaded, legacy)
	}
	if err := backend.Save(loaded); err != nil {
		t.Fatalf("migrate legacy state: %v", err)
	}
	metadata, err := os.ReadFile(backend.metadataPath())
	if err != nil {
		t.Fatalf("read migrated metadata: %v", err)
	}
	var envelope segmentedMetadataEnvelope
	if err := json.Unmarshal(metadata, &envelope); err != nil {
		t.Fatalf("decode migrated metadata: %v", err)
	}
	if envelope.Version != segmentedStateVersion || envelope.Generation != 1 {
		t.Fatalf("migrated envelope = version %d generation %d", envelope.Version, envelope.Generation)
	}

	restarted := NewSegmentedFileStateBackend(root)
	reloaded, err := restarted.Load()
	if err != nil {
		t.Fatalf("reload migrated state: %v", err)
	}
	if !reflect.DeepEqual(reloaded, legacy) {
		t.Fatalf("migrated round trip mismatch:\n got: %#v\nwant: %#v", reloaded, legacy)
	}
}

func TestSegmentedFileStateBackendRejectsMissingCommittedBlob(t *testing.T) {
	backend := NewSegmentedFileStateBackend(filepath.Join(t.TempDir(), "state"))
	state := &persistedState{Workspaces: map[string]*workspaceState{
		"ws": {Files: map[string]File{
			"/a": {Path: "/a", Revision: "rev_1", Content: "a"},
		}},
	}}
	if err := backend.Save(state); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if err := os.Remove(backend.blobPath("ws", "/a", "rev_1")); err != nil {
		t.Fatalf("remove committed blob: %v", err)
	}
	if _, err := backend.Load(); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("load error = %v, want missing blob", err)
	}
}

func TestSegmentedFileStateBackendIgnoresUncommittedGeneration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	backend := NewSegmentedFileStateBackend(root)
	committed := &persistedState{
		RevCounter: 1,
		Workspaces: map[string]*workspaceState{
			"ws_committed": {
				Revision: "rev_1",
				Files: map[string]File{
					"/committed.txt": {Path: "/committed.txt", Revision: "rev_1", Content: "committed"},
				},
				Events: []Event{},
				Ops:    map[string]OperationStatus{},
			},
		},
	}
	if err := backend.Save(committed); err != nil {
		t.Fatalf("save committed generation: %v", err)
	}

	// Simulate a process dying after it created the next generation but before
	// metadata.json atomically advanced the committed generation pointer.
	uncommittedPath := filepath.Join(backend.generationPath(2), "files", "uncommitted.json")
	if err := writeSegmentedFileAtomic(uncommittedPath, []byte("not valid committed data"), 0o600); err != nil {
		t.Fatalf("write uncommitted generation: %v", err)
	}

	restarted := NewSegmentedFileStateBackend(root)
	loaded, err := restarted.Load()
	if err != nil {
		t.Fatalf("load with uncommitted future generation: %v", err)
	}
	if !reflect.DeepEqual(loaded, committed) {
		t.Fatalf("uncommitted generation changed state:\n got: %#v\nwant: %#v", loaded, committed)
	}
}

func TestStoreRestartsFromSegmentedFileState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	first := NewStoreWithOptions(StoreOptions{
		StateBackend:   NewSegmentedFileStateBackend(root),
		DisableWorkers: true,
	})
	write, err := first.WriteFile(WriteRequest{
		WorkspaceID:   "ws_restart",
		Path:          "/repo/readme.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# durable segmented state\n",
		CorrelationID: "corr_segmented_restart",
	})
	if err != nil {
		t.Fatalf("write before restart: %v", err)
	}
	first.Close()

	second := NewStoreWithOptions(StoreOptions{
		StateBackend:   NewSegmentedFileStateBackend(root),
		DisableWorkers: true,
	})
	t.Cleanup(second.Close)
	file, err := second.ReadFile("ws_restart", "/repo/readme.md")
	if err != nil {
		t.Fatalf("read after restart: %v", err)
	}
	if file.Content != "# durable segmented state\n" || file.Revision != write.TargetRevision {
		t.Fatalf("file after restart = %+v, want original content and revision %q", file, write.TargetRevision)
	}
	events, err := second.GetEvents("ws_restart", "", "", 10)
	if err != nil {
		t.Fatalf("events after restart: %v", err)
	}
	if len(events.Events) != 1 || events.Events[0].EventID == "" {
		t.Fatalf("events after restart = %+v, want one durable event", events)
	}
}
