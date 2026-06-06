package relayfile

import (
	"testing"
	"time"
)

// Boot staleness gate (#249, op_20440 class): an op found "running" at boot
// whose last update is older than the configured threshold is definitionally
// abandoned — its executor died mid-flight in a previous process life — and
// re-arming it resurrects an action (e.g. a chat.postMessage upsert) whose
// context is hours gone. Verb-agnostic: op_20440 was an UPSERT.
//
// Fresh running ops (within the threshold) keep the existing at-least-once
// crash semantics: they re-arm exactly as before. Disabled when
// StaleRunningOpThreshold is 0.

func seedRunningOp(t *testing.T, backend *InMemoryStateBackend, workspaceID, opID, action, path, updatedAt string) {
	t.Helper()
	store := NewStoreWithOptions(StoreOptions{ExternalWritebackMode: true, StateBackend: backend})
	if _, err := store.WriteFile(WriteRequest{
		WorkspaceID:   workspaceID,
		Path:          path,
		IfMatch:       "0",
		ContentType:   "application/json",
		Content:       `{"text":"x"}`,
		CorrelationID: "corr_" + opID,
	}); err != nil {
		t.Fatalf("seed write failed: %v", err)
	}
	// Flip the freshly created op into the desired shape directly — the
	// incident state we are reproducing (stuck running, stale UpdatedAt)
	// cannot be produced through the public API on purpose.
	store.mu.Lock()
	ws := store.ensureWorkspaceLocked(workspaceID)
	for id, op := range ws.Ops {
		if op.Path == path {
			op.Status = "running"
			op.Action = action
			op.UpdatedAt = updatedAt
			ws.Ops[id] = op
		}
	}
	_ = store.saveLocked()
	store.mu.Unlock()
	store.Close()
}

func TestBootQuarantinesStaleRunningOpsVerbAgnostic(t *testing.T) {
	backend := NewInMemoryStateBackend()
	staleTS := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339Nano)
	// The op_20440 shape: a stuck-running UPSERT of a draft at a canonical
	// message path — a chat.postMessage duplicate armed for the restart.
	seedRunningOp(t, backend, "ws_stale", "op_upsert", string(WritebackActionFileUpsert),
		"/slack/channels/C0STALE/messages/messages 0e89a031-65f0-480e-a823-ab1d94b324ea.json", staleTS)

	store := NewStoreWithOptions(StoreOptions{
		ExternalWritebackMode:   true,
		StateBackend:            backend,
		StaleRunningOpThreshold: 15 * time.Minute,
	})
	t.Cleanup(store.Close)

	if pending := store.GetPendingWritebacks("ws_stale"); len(pending) != 0 {
		t.Fatalf("stale running op must not re-arm at boot, got %d queued", len(pending))
	}
	feed, err := store.ListOperations("ws_stale", "quarantined", "", "", "", 10)
	if err != nil || len(feed.Items) != 1 {
		t.Fatalf("expected exactly the stale running op quarantined, err=%v items=%d", err, len(feed.Items))
	}
	if feed.Items[0].Action != string(WritebackActionFileUpsert) {
		t.Fatalf("gate must be verb-agnostic; quarantined op action=%q", feed.Items[0].Action)
	}
}

func TestBootKeepsFreshRunningOpsArmed(t *testing.T) {
	backend := NewInMemoryStateBackend()
	freshTS := time.Now().UTC().Add(-30 * time.Second).Format(time.RFC3339Nano)
	seedRunningOp(t, backend, "ws_fresh", "op_fresh", string(WritebackActionFileUpsert),
		"/external/fresh.json", freshTS)

	store := NewStoreWithOptions(StoreOptions{
		ExternalWritebackMode:   true,
		StateBackend:            backend,
		StaleRunningOpThreshold: 15 * time.Minute,
	})
	t.Cleanup(store.Close)

	// At-least-once crash semantics preserved: a running op inside the
	// threshold re-arms exactly as it always has.
	if pending := store.GetPendingWritebacks("ws_fresh"); len(pending) != 1 {
		t.Fatalf("fresh running op must keep re-arming at boot, got %d queued", len(pending))
	}
}

func TestBootStaleGateDisabledByDefault(t *testing.T) {
	backend := NewInMemoryStateBackend()
	staleTS := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339Nano)
	seedRunningOp(t, backend, "ws_off", "op_off", string(WritebackActionFileUpsert),
		"/external/off.json", staleTS)

	store := NewStoreWithOptions(StoreOptions{
		ExternalWritebackMode: true,
		StateBackend:          backend,
	})
	t.Cleanup(store.Close)

	// Unset threshold = existing semantics, no behavior change.
	if pending := store.GetPendingWritebacks("ws_off"); len(pending) != 1 {
		t.Fatalf("gate must be inert when unconfigured, got %d queued", len(pending))
	}
}
