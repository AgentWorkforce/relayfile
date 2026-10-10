package mountsync

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type snapshotReadCounter struct {
	mu    sync.Mutex
	reads map[string]int
	full  map[string]int
}

func countSnapshotReads(syncer *Syncer) *snapshotReadCounter {
	counter := &snapshotReadCounter{reads: map[string]int{}, full: map[string]int{}}
	syncer.readLocalSnapshotFn = func(path string, includeContent bool) (localSnapshot, error) {
		counter.mu.Lock()
		counter.reads[filepath.Base(path)]++
		if includeContent {
			counter.full[filepath.Base(path)]++
		}
		counter.mu.Unlock()
		return readLocalSnapshotLimitedUnderRoot(syncer.localRoot, path, includeContent, maxWritebackBytes())
	}
	return counter
}

// A new (untracked) file used to be read three times by
// PushLocalAndFlushOnce: hashed by pushLocal's scan, re-read whole to build
// the upload, then hashed again by saveState's public-state scan. It is now
// read exactly once, and the uploaded bytes are the bytes on disk.
func TestPushLocalAndFlushOnceReadsNewFileOnce(t *testing.T) {
	t.Setenv("RELAYFILE_MAX_WRITEBACK_BYTES", "4096")
	localDir := t.TempDir()
	newBody := []byte("package main\n\nfunc main() { println(\"<&>\") }\n")
	if err := os.WriteFile(filepath.Join(localDir, "new.go"), newBody, 0o644); err != nil {
		t.Fatal(err)
	}
	// Oversized files are never read whole and must still surface as
	// writeback-skipped in the public state derived from the reused scan.
	if err := os.WriteFile(filepath.Join(localDir, "huge.bin"), bytes.Repeat([]byte("h"), 8192), 0o644); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{files: map[string]RemoteFile{}}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_read_once",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	counter := countSnapshotReads(syncer)

	if err := syncer.PushLocalAndFlushOnce(context.Background()); err != nil {
		t.Fatalf("PushLocalAndFlushOnce: %v", err)
	}

	if got := counter.reads["new.go"]; got != 1 {
		t.Fatalf("new.go read %d times, want 1", got)
	}
	if got := counter.full["new.go"]; got != 1 {
		t.Fatalf("new.go full reads = %d, want 1", got)
	}
	if got := counter.reads["huge.bin"]; got != 0 {
		t.Fatalf("oversized huge.bin read %d times, want 0", got)
	}
	if client.bulkWriteCalls != 1 || len(client.bulkWriteBatches[0]) != 1 {
		t.Fatalf("bulk writes = %d %+v, want one batch with new.go", client.bulkWriteCalls, client.bulkWriteBatches)
	}
	uploaded := client.bulkWriteBatches[0][0]
	if uploaded.Path != "/new.go" || uploaded.Content != string(newBody) {
		t.Fatalf("uploaded %s %q, want /new.go with on-disk bytes", uploaded.Path, uploaded.Content)
	}
	if got := syncer.state.Files["/new.go"].Hash; got != hashBytes(newBody) {
		t.Fatalf("tracked hash = %s, want hash of on-disk bytes", got)
	}
	state := readPublicState(t, localDir)
	if got := state.Files["/huge.bin"].Status; got != "writeback-skipped" {
		t.Fatalf("huge.bin public status = %q, want writeback-skipped", got)
	}
	if got := state.Files["/new.go"].Status; got != "ready" {
		t.Fatalf("new.go public status = %q, want ready", got)
	}
}

// Tracked dirty files are not captured by the scan (their hash decides
// whether they changed at all), so they keep the hash-then-read path.
func TestPushLocalCapturesOnlyUntrackedFiles(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "tracked.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{files: map[string]RemoteFile{}}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_capture_untracked",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	syncer.state.Files["/tracked.txt"] = trackedFile{Revision: "rev_1", Hash: hashBytes([]byte("base\n")), Type: remoteTypeFile, Mode: 0o644}
	counter := countSnapshotReads(syncer)

	if _, err := syncer.pushLocal(context.Background()); err != nil {
		t.Fatalf("pushLocal: %v", err)
	}
	if got, full := counter.reads["tracked.txt"], counter.full["tracked.txt"]; got != 2 || full != 1 {
		t.Fatalf("tracked.txt reads=%d full=%d, want hash scan + one full read", got, full)
	}
	if client.bulkWriteCalls != 1 || client.bulkWriteBatches[0][0].Content != "edited\n" {
		t.Fatalf("tracked edit not uploaded: %+v", client.bulkWriteBatches)
	}
}

// In low-memory mode the scan never retains file content.
func TestPushLocalLowMemoryDoesNotCaptureContent(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "new.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lowMemory := true
	syncer, err := NewSyncer(&fakeClient{files: map[string]RemoteFile{}}, SyncerOptions{
		WorkspaceID: "ws_capture_low_memory",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
		LowMemory:   &lowMemory,
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	_, captured, err := syncer.scanLocalFilesCapturing(true)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(captured) != 0 {
		t.Fatalf("low-memory scan captured %d files", len(captured))
	}
}
