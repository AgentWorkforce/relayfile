package mountsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/relayfile"
	"github.com/fsnotify/fsnotify"
)

type delayedIncrementalReadClient struct {
	*fakeClient
	delay     time.Duration
	active    atomic.Int32
	maxActive atomic.Int32
}

type blockingReceiptClient struct {
	*fakeClient
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func TestUntrackedAtomicSaveStagingFileNeverWritesBack(t *testing.T) {
	client := &fakeClient{files: map[string]RemoteFile{}}
	localDir := t.TempDir()
	relativePath := "src/main.go.writer-tmp-692"
	localPath := filepath.Join(localDir, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("create staging parent: %v", err)
	}
	if err := os.WriteFile(localPath, []byte("transient bytes"), 0o644); err != nil {
		t.Fatalf("write staging file: %v", err)
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_atomic_staging",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
		StateFile:   filepath.Join(t.TempDir(), "state.json"),
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	if err := syncer.HandleLocalChange(context.Background(), relativePath, fsnotify.Create); err != nil {
		t.Fatalf("handle staging create: %v", err)
	}
	if client.bulkWriteCalls != 0 || client.writeFileCalls != 0 {
		t.Fatalf("staging path wrote remotely: bulk=%d single=%d", client.bulkWriteCalls, client.writeFileCalls)
	}
	if _, tracked := syncer.state.Files["/"+relativePath]; tracked {
		t.Fatal("untracked staging path became tracked")
	}
	scanned, err := syncer.scanLocalFiles()
	if err != nil {
		t.Fatalf("scan local files: %v", err)
	}
	if _, found := scanned["/"+relativePath]; found {
		t.Fatal("polling reconciliation admitted untracked staging path")
	}
}

func (c *blockingReceiptClient) GetOperation(ctx context.Context, workspaceID, opID string) (OperationStatus, error) {
	c.once.Do(func() { close(c.started) })
	select {
	case <-c.release:
		return c.fakeClient.GetOperation(ctx, workspaceID, opID)
	case <-ctx.Done():
		return OperationStatus{}, ctx.Err()
	}
}

func (c *delayedIncrementalReadClient) ReadFile(ctx context.Context, workspaceID, path string) (RemoteFile, error) {
	active := c.active.Add(1)
	defer c.active.Add(-1)
	for {
		maximum := c.maxActive.Load()
		if active <= maximum || c.maxActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	timer := time.NewTimer(c.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return c.fakeClient.ReadFile(ctx, workspaceID, path)
	case <-ctx.Done():
		return RemoteFile{}, ctx.Err()
	}
}

func TestWebSocketInlineContentAppliesWithoutReadAndPersistsCursor(t *testing.T) {
	client := &fakeClient{files: map[string]RemoteFile{}}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_inline",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	content := "agent edit\n"
	if err := syncer.applyWebSocketEvent(context.Background(), websocketEvent{
		EventID:       "evt_42",
		Type:          "file.updated",
		Path:          "/shared/note.txt",
		Revision:      "rev_42",
		ContentHash:   hashString(content),
		ContentType:   "text/plain",
		Content:       content,
		InlineContent: true,
		Timestamp:     "2026-08-21T12:00:00Z",
	}); err != nil {
		t.Fatalf("apply inline websocket event: %v", err)
	}

	if client.requestedReadCalls() != 0 {
		t.Fatalf("inline event performed %d ReadFile calls, want 0", client.requestedReadCalls())
	}
	assertLocalFileContent(t, filepath.Join(localDir, "shared", "note.txt"), content)
	if got := syncer.state.EventsCursor; got != "evt_42" {
		t.Fatalf("events cursor = %q, want evt_42", got)
	}

	reloaded, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_inline",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("reload syncer: %v", err)
	}
	reloaded.mu.Lock()
	err = reloaded.loadState()
	reloaded.mu.Unlock()
	if err != nil {
		t.Fatalf("reload mount state: %v", err)
	}
	if got := reloaded.state.EventsCursor; got != "evt_42" {
		t.Fatalf("persisted events cursor = %q, want evt_42", got)
	}
}

func TestWebSocketInlineContentRejectsHashMismatchWithoutAdvancingCursor(t *testing.T) {
	syncer, err := NewSyncer(&fakeClient{files: map[string]RemoteFile{}}, SyncerOptions{
		WorkspaceID: "ws_inline_hash",
		RemoteRoot:  "/",
		LocalRoot:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	err = syncer.applyWebSocketEvent(context.Background(), websocketEvent{
		EventID:       "evt_7",
		Type:          "file.updated",
		Path:          "/shared/note.txt",
		Revision:      "rev_7",
		ContentHash:   hashString("different"),
		ContentType:   "text/plain",
		Content:       "payload",
		InlineContent: true,
	})
	if err == nil {
		t.Fatal("expected inline content hash mismatch")
	}
	if syncer.state.EventsCursor != "" {
		t.Fatalf("cursor advanced past corrupt event: %q", syncer.state.EventsCursor)
	}
}

func TestWebSocketSamePathEventWaitsForLocalUploadBeforeCheckpoint(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	client := &fakeClient{
		files:                          map[string]RemoteFile{},
		bulkWriteResponseFuncOwnsWrite: true,
	}
	client.bulkWriteResponseFunc = func(ctx context.Context, _ string, files []BulkWriteFile) (BulkWriteResponse, error) {
		once.Do(func() { close(started) })
		select {
		case <-release:
		case <-ctx.Done():
			return BulkWriteResponse{}, ctx.Err()
		}
		return BulkWriteResponse{
			Written: len(files),
			Results: []BulkWriteResult{{Path: files[0].Path, Revision: "rev_local"}},
		}, nil
	}

	localDir := t.TempDir()
	localPath := filepath.Join(localDir, "shared", "same.txt")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir local file: %v", err)
	}
	if err := os.WriteFile(localPath, []byte("local pending"), 0o644); err != nil {
		t.Fatalf("write local file: %v", err)
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_same_path_pending",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	uploadDone := make(chan error, 1)
	go func() {
		uploadDone <- syncer.HandleLocalChanges(context.Background(), []LocalChange{{
			RelativePath: "shared/same.txt",
			Op:           fsnotify.Create,
		}})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("local upload did not reach blocking response")
	}

	peerContent := "peer committed while local upload was in flight"
	eventDone := make(chan error, 1)
	go func() {
		eventDone <- syncer.applyWebSocketEvent(context.Background(), websocketEvent{
			EventID:       "evt_peer",
			Type:          "file.updated",
			Path:          "/shared/same.txt",
			Revision:      "rev_peer",
			ContentType:   "text/plain",
			Content:       peerContent,
			ContentHash:   hashString(peerContent),
			InlineContent: true,
		})
	}()
	select {
	case err := <-eventDone:
		t.Fatalf("same-path event checkpointed before upload settled: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	syncer.mu.Lock()
	if got := syncer.state.EventsCursor; got != "" {
		syncer.mu.Unlock()
		t.Fatalf("cursor advanced while local upload was pending: %q", got)
	}
	syncer.mu.Unlock()

	close(release)
	if err := <-uploadDone; err != nil {
		t.Fatalf("local upload: %v", err)
	}
	select {
	case err := <-eventDone:
		if err != nil {
			t.Fatalf("apply retained same-path event: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("same-path event did not resume after upload settled")
	}
	assertLocalFileContent(t, localPath, peerContent)
	if tracked := syncer.state.Files["/shared/same.txt"]; tracked.Revision != "rev_peer" || tracked.Dirty {
		t.Fatalf("tracked same-path state = %+v, want clean rev_peer", tracked)
	}
	if got := syncer.state.EventsCursor; got != "evt_peer" {
		t.Fatalf("events cursor = %q, want evt_peer", got)
	}
}

func TestWebSocketSupersededCreateAppliesCurrentAbsenceAndAdvancesCursor(t *testing.T) {
	client := &fakeClient{files: map[string]RemoteFile{}}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_superseded_create",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	remotePath := "/shared/note.txt.writer-tmp-42"
	localPath := filepath.Join(localDir, "shared", "note.txt.writer-tmp-42")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir local parent: %v", err)
	}
	oldContent := []byte("historical temporary content")
	if err := os.WriteFile(localPath, oldContent, 0o644); err != nil {
		t.Fatalf("write stale local file: %v", err)
	}
	syncer.state.Files[remotePath] = trackedFile{
		Revision:    "rev_old",
		ContentType: "text/plain",
		Hash:        hashBytes(oldContent),
	}

	// The event is historical, but the authoritative current read is 404
	// because a later atomic rename already deleted the temporary path.
	if err := syncer.applyWebSocketEvent(context.Background(), websocketEvent{
		EventID:  "evt_42",
		Type:     "file.created",
		Path:     remotePath,
		Revision: "rev_created",
	}); err != nil {
		t.Fatalf("apply superseded websocket event: %v", err)
	}
	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Fatalf("superseded temporary path still exists: %v", err)
	}
	if _, ok := syncer.state.Files[remotePath]; ok {
		t.Fatal("superseded temporary path remains tracked")
	}
	if got := syncer.state.EventsCursor; got != "evt_42" {
		t.Fatalf("events cursor = %q, want evt_42", got)
	}

	reloaded, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_superseded_create",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("reload syncer: %v", err)
	}
	reloaded.mu.Lock()
	err = reloaded.loadState()
	reloaded.mu.Unlock()
	if err != nil {
		t.Fatalf("reload mount state: %v", err)
	}
	if got := reloaded.state.EventsCursor; got != "evt_42" {
		t.Fatalf("persisted events cursor = %q, want evt_42", got)
	}
}

func TestWebSocketBurstDefersWholeStateCheckpointBeyondLiveBuffer(t *testing.T) {
	client := &fakeClient{files: map[string]RemoteFile{}}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_burst_checkpoint",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	const eventCount = 300 // deliberately exceeds the server's 256-event live buffer
	for index := 1; index <= eventCount; index++ {
		content := fmt.Sprintf("agent burst %03d\n", index)
		if err := syncer.applyWebSocketEventWithPersistence(context.Background(), websocketEvent{
			EventID:       fmt.Sprintf("evt_%06d", index),
			Type:          "file.created",
			Path:          fmt.Sprintf("/shared/burst-%03d.txt", index),
			Revision:      fmt.Sprintf("rev_%03d", index),
			ContentHash:   hashString(content),
			ContentType:   "text/plain",
			Content:       content,
			InlineContent: true,
		}, false); err != nil {
			t.Fatalf("apply deferred event %d: %v", index, err)
		}
	}
	if got := syncer.state.EventsCursor; got != "evt_000300" {
		t.Fatalf("in-memory events cursor = %q, want evt_000300", got)
	}
	if _, err := os.Stat(syncer.stateFile); !os.IsNotExist(err) {
		t.Fatalf("deferred burst unexpectedly persisted per-event state: %v", err)
	}

	syncer.mu.Lock()
	err = syncer.savePrivateState()
	syncer.mu.Unlock()
	if err != nil {
		t.Fatalf("flush burst checkpoint: %v", err)
	}

	reloaded, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_burst_checkpoint",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("reload syncer: %v", err)
	}
	reloaded.mu.Lock()
	err = reloaded.loadState()
	reloaded.mu.Unlock()
	if err != nil {
		t.Fatalf("reload mount state: %v", err)
	}
	if got := reloaded.state.EventsCursor; got != "evt_000300" {
		t.Fatalf("persisted events cursor = %q, want evt_000300", got)
	}
	if got := len(reloaded.state.Files); got != eventCount {
		t.Fatalf("persisted tracked files = %d, want %d", got, eventCount)
	}
}

func TestHandleLocalChangesBatchesElevenFilesAndDefersPendingReceipts(t *testing.T) {
	client := &blockingReceiptClient{
		fakeClient: &fakeClient{
			files:      map[string]RemoteFile{},
			operations: map[string]OperationStatus{},
		},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	client.bulkWriteResponseFunc = func(_ context.Context, _ string, files []BulkWriteFile) (BulkWriteResponse, error) {
		results := make([]BulkWriteResult, 0, len(files))
		for i, file := range files {
			results = append(results, BulkWriteResult{
				Path:     file.Path,
				Revision: fmt.Sprintf("rev_batch_%d", i),
				OpID:     fmt.Sprintf("op_batch_%d", i),
				Writeback: &relayfile.BulkWriteWritebackResult{
					State: "pending",
				},
			})
		}
		return BulkWriteResponse{Written: len(files), Results: results, CorrelationID: "corr_batch"}, nil
	}

	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_batch",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	changes := make([]LocalChange, 0, 11)
	for i := 0; i < 11; i++ {
		relativePath := fmt.Sprintf("shared/%02d.txt", i)
		localPath := filepath.Join(localDir, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			t.Fatalf("mkdir %d: %v", i, err)
		}
		if err := os.WriteFile(localPath, []byte(fmt.Sprintf("edit-%02d", i)), 0o644); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		changes = append(changes, LocalChange{RelativePath: relativePath, Op: fsnotify.Write})
	}

	if err := syncer.HandleLocalChanges(context.Background(), changes); err != nil {
		t.Fatalf("handle local changes: %v", err)
	}
	if client.bulkWriteCalls != 1 {
		t.Fatalf("bulk write calls = %d, want 1", client.bulkWriteCalls)
	}
	if got := len(client.bulkWriteBatches[0]); got != 11 {
		t.Fatalf("bulk batch size = %d, want 11", got)
	}
	select {
	case <-client.started:
		// The receipt poll started, but HandleLocalChanges already returned
		// without waiting for the deliberately blocked GET.
	case <-time.After(time.Second):
		t.Fatal("asynchronous receipt settlement did not start")
	}
	if client.getOperationCalls != 0 {
		t.Fatalf("blocked receipt unexpectedly completed: calls=%d", client.getOperationCalls)
	}
	close(client.release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		syncer.receiptMu.Lock()
		active := len(syncer.receiptActive)
		syncer.receiptMu.Unlock()
		if active == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWatcherBulkUploadDoesNotBlockInlinePeerEvent(t *testing.T) {
	bulkStarted := make(chan struct{})
	bulkRelease := make(chan struct{})
	client := &fakeClient{files: map[string]RemoteFile{}}
	client.bulkWriteResponseFunc = func(_ context.Context, _ string, files []BulkWriteFile) (BulkWriteResponse, error) {
		close(bulkStarted)
		<-bulkRelease
		return BulkWriteResponse{
			Written: len(files),
			Results: []BulkWriteResult{{Path: files[0].Path, Revision: "rev_10"}},
		}, nil
	}

	localDir := t.TempDir()
	localPath := filepath.Join(localDir, "shared", "local.txt")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir local file: %v", err)
	}
	if err := os.WriteFile(localPath, []byte("local edit"), 0o644); err != nil {
		t.Fatalf("write local file: %v", err)
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_nonblocking_upload",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- syncer.HandleLocalChanges(context.Background(), []LocalChange{{
			RelativePath: "shared/local.txt",
			Op:           fsnotify.Create,
		}})
	}()
	select {
	case <-bulkStarted:
	case <-time.After(time.Second):
		t.Fatal("bulk write did not start")
	}

	peerContent := "peer edit"
	eventDone := make(chan error, 1)
	go func() {
		eventDone <- syncer.applyWebSocketEvent(context.Background(), websocketEvent{
			Type:          "file.created",
			Path:          "/shared/peer.txt",
			Revision:      "rev_11",
			ContentType:   "text/plain",
			Content:       peerContent,
			Encoding:      "utf-8",
			ContentHash:   hashBytes([]byte(peerContent)),
			InlineContent: true,
			EventID:       "evt_11",
		})
	}()
	select {
	case err := <-eventDone:
		if err != nil {
			t.Fatalf("apply peer event while upload blocked: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("inline peer event waited for the local bulk network response")
	}
	if got, err := os.ReadFile(filepath.Join(localDir, "shared", "peer.txt")); err != nil || string(got) != peerContent {
		t.Fatalf("peer file = %q, err=%v", got, err)
	}

	close(bulkRelease)
	if err := <-writeDone; err != nil {
		t.Fatalf("handle local changes: %v", err)
	}
}

func TestDuplicateWatcherBurstSkipsIdenticalInflightOutbox(t *testing.T) {
	bulkStarted := make(chan struct{})
	bulkRelease := make(chan struct{})
	client := &fakeClient{files: map[string]RemoteFile{}}
	client.bulkWriteResponseFunc = func(_ context.Context, _ string, files []BulkWriteFile) (BulkWriteResponse, error) {
		close(bulkStarted)
		<-bulkRelease
		return BulkWriteResponse{
			Written: len(files),
			Results: []BulkWriteResult{{Path: files[0].Path, Revision: "rev_10"}},
		}, nil
	}

	localDir := t.TempDir()
	localPath := filepath.Join(localDir, "shared", "local.txt")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir local file: %v", err)
	}
	if err := os.WriteFile(localPath, []byte("local edit"), 0o644); err != nil {
		t.Fatalf("write local file: %v", err)
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_duplicate_watcher",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- syncer.HandleLocalChanges(context.Background(), []LocalChange{{
			RelativePath: "shared/local.txt",
			Op:           fsnotify.Create,
		}})
	}()
	select {
	case <-bulkStarted:
	case <-time.After(time.Second):
		t.Fatal("bulk write did not start")
	}

	duplicateDone := make(chan error, 1)
	go func() {
		duplicateDone <- syncer.HandleLocalChanges(context.Background(), []LocalChange{{
			RelativePath: "shared/local.txt",
			Op:           fsnotify.Write,
		}})
	}()
	select {
	case duplicateErr := <-duplicateDone:
		t.Fatalf("duplicate watcher burst overtook the in-flight save: %v", duplicateErr)
	case <-time.After(50 * time.Millisecond):
	}
	if client.bulkWriteCalls != 1 {
		t.Fatalf("bulk write calls = %d, want 1", client.bulkWriteCalls)
	}
	if client.requestedReadCalls() != 0 {
		t.Fatalf("duplicate watcher burst performed %d remote reads, want 0", client.requestedReadCalls())
	}

	close(bulkRelease)
	if err := <-firstDone; err != nil {
		t.Fatalf("first local change: %v", err)
	}
	if err := <-duplicateDone; err != nil {
		t.Fatalf("duplicate watcher burst: %v", err)
	}
	if client.bulkWriteCalls != 1 {
		t.Fatalf("bulk write calls after duplicate settled = %d, want 1", client.bulkWriteCalls)
	}
}

func TestRapidWatcherSavesPreserveObservedOrder(t *testing.T) {
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	var (
		callsMu sync.Mutex
		batches [][]BulkWriteFile
	)
	client := &fakeClient{
		files:                          map[string]RemoteFile{},
		bulkWriteResponseFuncOwnsWrite: true,
	}
	client.bulkWriteResponseFunc = func(_ context.Context, _ string, files []BulkWriteFile) (BulkWriteResponse, error) {
		callsMu.Lock()
		call := len(batches) + 1
		batches = append(batches, append([]BulkWriteFile(nil), files...))
		callsMu.Unlock()
		if call == 1 {
			close(firstStarted)
			<-firstRelease
		}
		return BulkWriteResponse{
			Written: len(files),
			Results: []BulkWriteResult{{Path: files[0].Path, Revision: fmt.Sprintf("rev_%d", call)}},
		}, nil
	}

	localDir := t.TempDir()
	localPath := filepath.Join(localDir, "shared", "ordered.txt")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir local file: %v", err)
	}
	if err := os.WriteFile(localPath, []byte("first"), 0o644); err != nil {
		t.Fatalf("write first content: %v", err)
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_ordered_watcher",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- syncer.HandleLocalChanges(context.Background(), []LocalChange{{
			RelativePath: "shared/ordered.txt",
			Op:           fsnotify.Create,
		}})
	}()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first save did not start")
	}
	if err := os.WriteFile(localPath, []byte("second"), 0o644); err != nil {
		t.Fatalf("write second content: %v", err)
	}
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- syncer.HandleLocalChanges(context.Background(), []LocalChange{{
			RelativePath: "shared/ordered.txt",
			Op:           fsnotify.Write,
		}})
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("second save overtook the first: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	callsMu.Lock()
	callsBeforeRelease := len(batches)
	callsMu.Unlock()
	if callsBeforeRelease != 1 {
		t.Fatalf("bulk writes before first release = %d, want 1", callsBeforeRelease)
	}

	close(firstRelease)
	if err := <-firstDone; err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second save: %v", err)
	}
	callsMu.Lock()
	defer callsMu.Unlock()
	if len(batches) != 2 {
		t.Fatalf("bulk writes = %d, want 2", len(batches))
	}
	if got := batches[0][0].Content; got != "first" {
		t.Fatalf("first bulk content = %q", got)
	}
	if got := batches[1][0].Content; got != "second" {
		t.Fatalf("second bulk content = %q", got)
	}
	if got := batches[1][0].IfMatch; got != "rev_1" {
		t.Fatalf("second save ifMatch = %q, want rev_1", got)
	}
}

func TestBackgroundLocalTreeHashDoesNotBlockInlinePeerEvent(t *testing.T) {
	client := &fakeClient{files: map[string]RemoteFile{}}
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "scan.txt"), []byte("scan me"), 0o644); err != nil {
		t.Fatalf("write scan fixture: %v", err)
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_nonblocking_scan",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	scanStarted := make(chan struct{})
	scanRelease := make(chan struct{})
	var startOnce sync.Once
	syncer.readLocalSnapshotFn = func(path string, includeContent bool) (localSnapshot, error) {
		startOnce.Do(func() { close(scanStarted) })
		<-scanRelease
		return readLocalSnapshot(path, includeContent)
	}
	scanDone := make(chan error, 1)
	syncer.mu.Lock()
	syncer.syncActive = true
	go func() {
		_, scanErr := syncer.scanLocalFiles()
		syncer.syncActive = false
		syncer.mu.Unlock()
		scanDone <- scanErr
	}()
	select {
	case <-scanStarted:
	case <-time.After(time.Second):
		t.Fatal("background scan did not reach the hash barrier")
	}

	peerContent := "peer edit during scan"
	eventDone := make(chan error, 1)
	go func() {
		eventDone <- syncer.applyWebSocketEvent(context.Background(), websocketEvent{
			Type:          "file.created",
			Path:          "/peer.txt",
			Revision:      "rev_peer",
			ContentType:   "text/plain",
			Content:       peerContent,
			Encoding:      "utf-8",
			ContentHash:   hashBytes([]byte(peerContent)),
			InlineContent: true,
			EventID:       "evt_peer",
		})
	}()
	select {
	case eventErr := <-eventDone:
		if eventErr != nil {
			t.Fatalf("apply peer event while scan blocked: %v", eventErr)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("inline peer event waited for background file hashing")
	}
	if got, readErr := os.ReadFile(filepath.Join(localDir, "peer.txt")); readErr != nil || string(got) != peerContent {
		t.Fatalf("peer file = %q, err=%v", got, readErr)
	}

	close(scanRelease)
	if scanErr := <-scanDone; scanErr != nil {
		t.Fatalf("background scan: %v", scanErr)
	}
}

func TestBulkRevisionConflictUsesConflictRecovery(t *testing.T) {
	err := bulkWriteErrorAsError(BulkWriteError{
		Path:    "/shared/file.txt",
		Code:    "revision_conflict",
		Message: "revision conflict",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("bulk revision_conflict = %v, want ErrConflict", err)
	}
}

func TestLocalChangeBatcherCoalescesBurst(t *testing.T) {
	batches := make(chan []LocalChange, 2)
	batcher := NewLocalChangeBatcher(5*time.Millisecond, func(changes []LocalChange) {
		batches <- changes
	})
	defer batcher.Close()

	for i := 0; i < 11; i++ {
		batcher.Add(fmt.Sprintf("shared/%02d.txt", i), fsnotify.Write)
	}
	select {
	case batch := <-batches:
		if len(batch) != 11 {
			t.Fatalf("batch size = %d, want 11", len(batch))
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for local-change batch")
	}
}

func TestLocalChangeBatcherUsesQuietWindowAcrossStaggeredSave(t *testing.T) {
	batches := make(chan []LocalChange, 2)
	batcher := NewLocalChangeBatcher(5*time.Millisecond, func(changes []LocalChange) {
		batches <- changes
	})
	defer batcher.Close()

	for i := 0; i < 5; i++ {
		batcher.Add(fmt.Sprintf("shared/%02d.txt", i), fsnotify.Write)
		time.Sleep(3 * time.Millisecond)
	}
	select {
	case batch := <-batches:
		if len(batch) != 5 {
			t.Fatalf("staggered save split into a %d-file batch, want 5", len(batch))
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for staggered local-change batch")
	}
	select {
	case extra := <-batches:
		t.Fatalf("staggered save produced an extra batch: %+v", extra)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestWatcherBatchCheckpointsPrivateStateAfterVisibilityPath(t *testing.T) {
	client := &fakeClient{files: map[string]RemoteFile{}}
	client.bulkWriteResponseFunc = func(_ context.Context, _ string, files []BulkWriteFile) (BulkWriteResponse, error) {
		results := make([]BulkWriteResult, 0, len(files))
		for _, file := range files {
			results = append(results, BulkWriteResult{Path: file.Path, Revision: "rev_checkpoint"})
		}
		return BulkWriteResponse{Written: len(files), Results: results}, nil
	}
	localDir := t.TempDir()
	localPath := filepath.Join(localDir, "shared", "checkpoint.txt")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir local file: %v", err)
	}
	if err := os.WriteFile(localPath, []byte("checkpointed"), 0o644); err != nil {
		t.Fatalf("write local file: %v", err)
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_local_checkpoint",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	if err := syncer.HandleLocalChanges(context.Background(), []LocalChange{{
		RelativePath: "shared/checkpoint.txt",
		Op:           fsnotify.Create,
	}}); err != nil {
		t.Fatalf("handle watcher batch: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(syncer.stateFile)
		if readErr == nil {
			var state mountState
			if json.Unmarshal(data, &state) == nil && state.Files["/shared/checkpoint.txt"].Revision == "rev_checkpoint" {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("coalesced checkpoint did not persist accepted revision to %s", syncer.stateFile)
}

func TestIncrementalMaterializationUsesBoundedParallelReads(t *testing.T) {
	files := make(map[string]RemoteFile, 50)
	changed := make(map[string]FilesystemEvent, 50)
	for i := 0; i < 50; i++ {
		path := fmt.Sprintf("/shared/%02d.txt", i)
		content := fmt.Sprintf("remote-%02d", i)
		files[path] = RemoteFile{Path: path, Revision: fmt.Sprintf("rev_%02d", i), ContentType: "text/plain", Content: content}
		changed[path] = FilesystemEvent{EventID: fmt.Sprintf("evt_%02d", i), Type: "file.updated", Path: path, Revision: fmt.Sprintf("rev_%02d", i)}
	}
	client := &delayedIncrementalReadClient{
		fakeClient: &fakeClient{files: files},
		delay:      25 * time.Millisecond,
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_incremental_parallel",
		RemoteRoot:  "/",
		LocalRoot:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	started := time.Now()
	if err := syncer.applyIncrementalChanges(context.Background(), changed, nil, nil, "evt_start", "evt_end", incrementalCheckpoint{}); err != nil {
		t.Fatalf("apply incremental changes: %v", err)
	}
	elapsed := time.Since(started)
	if elapsed >= 700*time.Millisecond {
		t.Fatalf("50 delayed reads took %s; expected bounded parallel materialization", elapsed)
	}
	if maximum := client.maxActive.Load(); maximum <= 1 || maximum > defaultIncrementalReadWorkers {
		t.Fatalf("max concurrent reads = %d, want 2..%d", maximum, defaultIncrementalReadWorkers)
	}
}

func TestSaveStateWithoutLocalScanKeepsTrackedPublicFiles(t *testing.T) {
	localDir := t.TempDir()
	syncer, err := NewSyncer(&fakeClient{files: map[string]RemoteFile{}}, SyncerOptions{
		WorkspaceID: "ws_public_fast_state",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	syncer.state.Files["/shared/note.txt"] = trackedFile{
		Revision:    "rev_9",
		ContentType: "text/plain",
		Hash:        hashString("note"),
	}
	if err := syncer.saveStateWithoutLocalScan(); err != nil {
		t.Fatalf("save state without local scan: %v", err)
	}
	data, err := os.ReadFile(syncer.publicStatePath)
	if err != nil {
		t.Fatalf("read public state: %v", err)
	}
	var public publicState
	if err := json.Unmarshal(data, &public); err != nil {
		t.Fatalf("decode public state: %v", err)
	}
	tracked, ok := public.Files["/shared/note.txt"]
	if !ok || tracked.Revision != "rev_9" || tracked.Status != "ready" {
		t.Fatalf("tracked public file missing from no-scan refresh: %+v", tracked)
	}
}

func TestWebSocketReconnectURLCarriesDurableCursor(t *testing.T) {
	client := NewHTTPClient("https://relay.example", "secret", nil)
	raw, err := client.websocketURL("ws_cursor", "evt_42")
	if err != nil {
		t.Fatalf("websocket url: %v", err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse websocket url: %v", err)
	}
	if got := parsed.Query().Get("cursor"); got != "evt_42" {
		t.Fatalf("cursor query = %q, want evt_42", got)
	}
	if got := parsed.Query().Get("token"); got != "secret" {
		t.Fatalf("token query = %q, want secret", got)
	}
}

func TestAdvanceEventCursorNeverRegressesRelayfileOrdinal(t *testing.T) {
	if got := advanceEventCursor("evt_42", "evt_7"); got != "evt_42" {
		t.Fatalf("cursor regressed to %q", got)
	}
	if got := advanceEventCursor("evt_42", "evt_43"); got != "evt_43" {
		t.Fatalf("cursor did not advance: %q", got)
	}
}
