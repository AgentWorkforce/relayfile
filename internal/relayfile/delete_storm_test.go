package relayfile

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Issue #249 (service-side layer): the 2026-06-06 delete storm destroyed 149+
// canonical records when stale private mount state met a layout change — the
// service admitted ~186 file_delete ops from one mount correlation family in
// minutes without blinking. The binary-side guard refuses false diffs at the
// client; this breaker is the layer that survives future client bugs.
//
// Contract (from the in-issue design, ratified in-run):
//   - Sliding-window burst detection at the write API (DeleteFile/BulkWrite)
//     BEFORE recordWriteLocked: a workspace exceeding the configured number
//     of file_delete admissions within the window gets ErrDeleteStormRejected
//     — the fs mutation itself is refused (the incident's real harm was the
//     fs damage; every provider call failed and records were still
//     destroyed).
//   - The breaker also covers BOOT RE-ENQUEUE (store.go boot scan re-arms
//     every pending/running op at every restart — including deploys): a
//     rehydrated burst of pending file_delete ops above the threshold is
//     quarantined (status "quarantined": outside the boot allowlist, not
//     replayable, not shown to external consumers) instead of re-enqueued.
//   - Disabled unless DeleteStormThreshold > 0 — defaults are a deploy
//     decision, not a library surprise.

func newStormStore(t *testing.T, threshold int, window time.Duration) *Store {
	t.Helper()
	store := NewStoreWithOptions(StoreOptions{
		ExternalWritebackMode: true,
		DeleteStormThreshold:  threshold,
		DeleteStormWindow:     window,
	})
	t.Cleanup(store.Close)
	return store
}

func seedStormFiles(t *testing.T, store *Store, workspaceID string, count int) []string {
	t.Helper()
	paths := make([]string, 0, count)
	for i := 0; i < count; i++ {
		path := fmt.Sprintf("/slack/channels/C0STORM/messages/%d.json", i)
		if _, err := store.WriteFile(WriteRequest{
			WorkspaceID:   workspaceID,
			Path:          path,
			IfMatch:       "0",
			ContentType:   "application/json",
			Content:       `{"text":"x"}`,
			CorrelationID: fmt.Sprintf("corr_seed_%d", i),
		}); err != nil {
			t.Fatalf("seed write %d failed: %v", i, err)
		}
		paths = append(paths, path)
	}
	return paths
}

func deleteStormPath(store *Store, workspaceID, path string, n int) error {
	_, err := store.DeleteFile(DeleteRequest{
		WorkspaceID:   workspaceID,
		Path:          path,
		IfMatch:       "*",
		CorrelationID: fmt.Sprintf("mount_1780738999_storm_%d", n),
	})
	return err
}

func TestDeleteStormBreakerRefusesBurst(t *testing.T) {
	store := newStormStore(t, 5, time.Minute)
	paths := seedStormFiles(t, store, "ws_storm", 8)

	for i := 0; i < 5; i++ {
		if err := deleteStormPath(store, "ws_storm", paths[i], i); err != nil {
			t.Fatalf("delete %d under threshold must succeed: %v", i, err)
		}
	}
	err := deleteStormPath(store, "ws_storm", paths[5], 5)
	if err != ErrDeleteStormRejected {
		t.Fatalf("delete above threshold must trip the breaker, got %v", err)
	}

	// The refused delete must not have mutated the fs: the record survives.
	if _, err := store.ReadFile("ws_storm", paths[5]); err != nil {
		t.Fatalf("refused delete must leave the record intact: %v", err)
	}
	// And no operation/writeback may exist for it.
	feed, err := store.ListOperations("ws_storm", "", string(WritebackActionFileDelete), "", "", 1000)
	if err != nil {
		t.Fatalf("list operations failed: %v", err)
	}
	for _, op := range feed.Items {
		if op.Path == paths[5] {
			t.Fatalf("refused delete must not create an operation, got %+v", op)
		}
	}
}

func TestDeleteStormBreakerHTTPErrorCode(t *testing.T) {
	// The sentinel must be distinguishable so httpapi can map it (429-class)
	// instead of a generic 500.
	if ErrDeleteStormRejected == nil {
		t.Fatalf("ErrDeleteStormRejected must be defined")
	}
	if !strings.Contains(ErrDeleteStormRejected.Error(), "delete storm") {
		t.Fatalf("sentinel should self-describe, got %q", ErrDeleteStormRejected.Error())
	}
}

func TestDeleteStormBreakerDisabledWhenUnconfigured(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{ExternalWritebackMode: true})
	t.Cleanup(store.Close)
	paths := seedStormFiles(t, store, "ws_storm", 12)
	for i, path := range paths {
		if err := deleteStormPath(store, "ws_storm", path, i); err != nil {
			t.Fatalf("breaker must be inert when threshold unset; delete %d failed: %v", i, err)
		}
	}
}

// Note for review: bulk deletes flow through fork commits (CommitFork →
// recordWriteLocked with file.deleted), not BulkWrite. The storm came through
// the direct DeleteFile path, which this breaker covers; whether fork-commit
// deletes should share the window is an explicit open question in the PR.

func TestDeleteStormBreakerWindowExpires(t *testing.T) {
	store := newStormStore(t, 3, 50*time.Millisecond)
	paths := seedStormFiles(t, store, "ws_storm", 7)

	for i := 0; i < 3; i++ {
		if err := deleteStormPath(store, "ws_storm", paths[i], i); err != nil {
			t.Fatalf("delete %d failed: %v", i, err)
		}
	}
	if err := deleteStormPath(store, "ws_storm", paths[3], 3); err != ErrDeleteStormRejected {
		t.Fatalf("4th delete inside window must trip, got %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	if err := deleteStormPath(store, "ws_storm", paths[4], 4); err != nil {
		t.Fatalf("delete after window expiry must succeed: %v", err)
	}
}

func TestBootRequeueQuarantinesPendingDeleteStorm(t *testing.T) {
	backend := NewInMemoryStateBackend()

	// Phase 1: a store WITHOUT the breaker admits a storm of deletes whose
	// ops stay pending (external mode, never acked) — the incident shape
	// persisted to the state backend.
	first := NewStoreWithOptions(StoreOptions{
		ExternalWritebackMode: true,
		StateBackend:          backend,
	})
	paths := seedStormFiles(t, first, "ws_storm", 10)
	for i, path := range paths {
		if err := deleteStormPath(first, "ws_storm", path, i); err != nil {
			t.Fatalf("storm delete %d failed: %v", i, err)
		}
	}
	first.Close()

	// Phase 2: a new store (a deploy) boots over that state WITH the breaker
	// configured. The boot scan must quarantine the pending delete burst
	// instead of re-arming it into the queue.
	second := NewStoreWithOptions(StoreOptions{
		ExternalWritebackMode: true,
		StateBackend:          backend,
		DeleteStormThreshold:  5,
		DeleteStormWindow:     time.Minute,
	})
	t.Cleanup(second.Close)

	// The seed upserts legitimately re-arm (that's existing boot behavior);
	// the breaker must remove exactly the DELETE burst from the armed set.
	armedDeletes, err := second.ListOperations("ws_storm", "pending", string(WritebackActionFileDelete), "", "", 1000)
	if err != nil {
		t.Fatalf("list pending deletes failed: %v", err)
	}
	if len(armedDeletes.Items) != 0 {
		t.Fatalf("boot must not re-arm a pending delete storm, got %d armed deletes", len(armedDeletes.Items))
	}
	feed, err := second.ListOperations("ws_storm", "quarantined", "", "", "", 1000)
	if err != nil {
		t.Fatalf("list quarantined failed: %v", err)
	}
	quarantinedDeletes := 0
	for _, op := range feed.Items {
		if op.Action == string(WritebackActionFileDelete) {
			quarantinedDeletes++
		}
	}
	if quarantinedDeletes != 10 {
		t.Fatalf("expected all 10 storm deletes quarantined at boot, got %d", quarantinedDeletes)
	}
	// And none of the quarantined deletes may sit in the external queue.
	for _, item := range second.GetPendingWritebacks("ws_storm") {
		for _, op := range feed.Items {
			if item["id"] == op.OpID {
				t.Fatalf("quarantined op %s leaked into the pending queue", op.OpID)
			}
		}
	}
}

func TestQuarantinedOpsAreNotReplayable(t *testing.T) {
	backend := NewInMemoryStateBackend()
	first := NewStoreWithOptions(StoreOptions{ExternalWritebackMode: true, StateBackend: backend})
	paths := seedStormFiles(t, first, "ws_storm", 6)
	for i, path := range paths {
		if err := deleteStormPath(first, "ws_storm", path, i); err != nil {
			t.Fatalf("storm delete %d failed: %v", i, err)
		}
	}
	first.Close()

	second := NewStoreWithOptions(StoreOptions{
		ExternalWritebackMode: true,
		StateBackend:          backend,
		DeleteStormThreshold:  3,
		DeleteStormWindow:     time.Minute,
	})
	t.Cleanup(second.Close)

	feed, err := second.ListOperations("ws_storm", "quarantined", "", "", "", 10)
	if err != nil || len(feed.Items) == 0 {
		t.Fatalf("expected quarantined ops, err=%v items=%d", err, len(feed.Items))
	}
	if _, err := second.ReplayOperation("ws_storm", feed.Items[0].OpID, "corr_replay"); err != ErrInvalidState {
		t.Fatalf("quarantined ops must not be replayable, got %v", err)
	}
}
