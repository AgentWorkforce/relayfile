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

// A file captured by the scan and edited before the push loop reaches it
// must upload its latest bytes: the capture is revalidated (inode, size,
// mtime) and a changed file is re-read. The one-shot drain does not rescan,
// so stale captured bytes would otherwise be the final remote content.
func TestPushLocalAndFlushOnceUploadsFileEditedAfterScan(t *testing.T) {
	localDir := t.TempDir()
	aPath := filepath.Join(localDir, "a.txt")
	bPath := filepath.Join(localDir, "b.txt")
	if err := os.WriteFile(aPath, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{files: map[string]RemoteFile{}}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_capture_stale",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	edited := false
	syncer.readLocalSnapshotFn = func(path string, includeContent bool) (localSnapshot, error) {
		snapshot, err := readLocalSnapshotLimitedUnderRoot(localDir, path, includeContent, maxWritebackBytes())
		// The walk is lexical: a.txt is captured first, then b.txt. Edit
		// a.txt while b.txt is being scanned, i.e. after a.txt's capture and
		// before the push loop uses it.
		if path == bPath && !edited {
			edited = true
			if err := os.WriteFile(aPath, []byte("v2 edited after scan\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return snapshot, err
	}

	if err := syncer.PushLocalAndFlushOnce(context.Background()); err != nil {
		t.Fatalf("PushLocalAndFlushOnce: %v", err)
	}
	if !edited {
		t.Fatal("test hook never edited a.txt")
	}
	got, ok := client.files["/a.txt"]
	if !ok || got.Content != "v2 edited after scan\n" {
		t.Fatalf("remote /a.txt = %q (present=%v), want the post-scan edit", got.Content, ok)
	}
	if hash := syncer.state.Files["/a.txt"].Hash; hash != hashBytes([]byte("v2 edited after scan\n")) {
		t.Fatalf("tracked hash %s is not the uploaded latest content", hash)
	}
}

// When the flush removes a local file — a new draft rejected with
// schema_validation_failed that has no remote version to restore — the
// public state derived from the reused pre-flush scan must not list it.
func TestPushLocalAndFlushOnceOmitsFilesRemovedByFlushFromPublicState(t *testing.T) {
	const draft = "/github/repos/acme/api/pulls/42/reviews/draft.json"
	client := &fakeClient{
		files: map[string]RemoteFile{},
		bulkWriteResponseFunc: func(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
			return BulkWriteResponse{ErrorCount: 1, Errors: []BulkWriteError{{
				Path: draft, Code: "schema_validation_failed", Message: "body.event is required",
			}}}, nil
		},
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_schema_removed_public_state",
		RemoteRoot:  "/github",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	localPath := filepath.Join(localDir, "repos", "acme", "api", "pulls", "42", "reviews", "draft.json")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localPath, []byte(`{"body":"no event"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(localDir, "notes.txt")
	if err := os.WriteFile(keep, []byte("kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := syncer.PushLocalAndFlushOnce(context.Background()); err != nil {
		t.Fatalf("PushLocalAndFlushOnce: %v", err)
	}
	if _, err := os.Lstat(localPath); !os.IsNotExist(err) {
		t.Fatalf("schema-rejected new draft should have been removed locally, stat err=%v", err)
	}
	state := readPublicState(t, localDir)
	if entry, listed := state.Files[draft]; listed {
		t.Fatalf("removed draft still published as %q", entry.Status)
	}
	if state.PendingWriteback != 0 {
		t.Fatalf("pendingWriteback = %d, want 0 after the drain", state.PendingWriteback)
	}
}
