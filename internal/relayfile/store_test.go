package relayfile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type memoryStateBackend struct {
	snapshot  persistedState
	loaded    bool
	saveCalls int32
}

type blockingEnvelopeQueue struct {
	capacity int
	enqueue  int32
	release  chan struct{}
}

func (q *blockingEnvelopeQueue) TryEnqueue(envelopeID string) bool {
	_ = envelopeID
	return false
}

func (q *blockingEnvelopeQueue) Enqueue(ctx context.Context, envelopeID string) bool {
	_ = envelopeID
	atomic.AddInt32(&q.enqueue, 1)
	select {
	case <-q.release:
		return true
	case <-ctx.Done():
		return false
	}
}

func (q *blockingEnvelopeQueue) Dequeue(ctx context.Context) (string, bool) {
	<-ctx.Done()
	return "", false
}

func (q *blockingEnvelopeQueue) Depth() int {
	return 0
}

func (q *blockingEnvelopeQueue) Capacity() int {
	return q.capacity
}

func (q *blockingEnvelopeQueue) Close() error {
	return nil
}

type blockingWritebackQueue struct {
	capacity int
	enqueue  int32
	release  chan struct{}
}

func (q *blockingWritebackQueue) TryEnqueue(task WritebackQueueItem) bool {
	_ = task
	return false
}

func (q *blockingWritebackQueue) Enqueue(ctx context.Context, task WritebackQueueItem) bool {
	_ = task
	atomic.AddInt32(&q.enqueue, 1)
	select {
	case <-q.release:
		return true
	case <-ctx.Done():
		return false
	}
}

func (q *blockingWritebackQueue) Dequeue(ctx context.Context) (WritebackQueueItem, bool) {
	<-ctx.Done()
	return WritebackQueueItem{}, false
}

func (q *blockingWritebackQueue) Depth() int {
	return 0
}

func (q *blockingWritebackQueue) Capacity() int {
	return q.capacity
}

func (q *blockingWritebackQueue) Close() error {
	return nil
}

type countingEnvelopeQueue struct {
	inner        EnvelopeQueue
	tryCalls     int32
	enqueueCalls int32
	dequeueCalls int32
}

func (q *countingEnvelopeQueue) TryEnqueue(envelopeID string) bool {
	atomic.AddInt32(&q.tryCalls, 1)
	if q.inner == nil {
		return false
	}
	return q.inner.TryEnqueue(envelopeID)
}

func (q *countingEnvelopeQueue) Enqueue(ctx context.Context, envelopeID string) bool {
	atomic.AddInt32(&q.enqueueCalls, 1)
	if q.inner == nil {
		return false
	}
	return q.inner.Enqueue(ctx, envelopeID)
}

func (q *countingEnvelopeQueue) Dequeue(ctx context.Context) (string, bool) {
	atomic.AddInt32(&q.dequeueCalls, 1)
	if q.inner == nil {
		return "", false
	}
	return q.inner.Dequeue(ctx)
}

func (q *countingEnvelopeQueue) Depth() int {
	if q.inner == nil {
		return 0
	}
	return q.inner.Depth()
}

func (q *countingEnvelopeQueue) Capacity() int {
	if q.inner == nil {
		return 0
	}
	return q.inner.Capacity()
}

func (q *countingEnvelopeQueue) Close() error {
	if q.inner == nil {
		return nil
	}
	return q.inner.Close()
}

type countingWritebackQueue struct {
	inner        WritebackQueue
	tryCalls     int32
	enqueueCalls int32
	dequeueCalls int32
}

func (q *countingWritebackQueue) TryEnqueue(task WritebackQueueItem) bool {
	atomic.AddInt32(&q.tryCalls, 1)
	if q.inner == nil {
		return false
	}
	return q.inner.TryEnqueue(task)
}

func (q *countingWritebackQueue) Enqueue(ctx context.Context, task WritebackQueueItem) bool {
	atomic.AddInt32(&q.enqueueCalls, 1)
	if q.inner == nil {
		return false
	}
	return q.inner.Enqueue(ctx, task)
}

func (q *countingWritebackQueue) Dequeue(ctx context.Context) (WritebackQueueItem, bool) {
	atomic.AddInt32(&q.dequeueCalls, 1)
	if q.inner == nil {
		return WritebackQueueItem{}, false
	}
	return q.inner.Dequeue(ctx)
}

func (q *countingWritebackQueue) Depth() int {
	if q.inner == nil {
		return 0
	}
	return q.inner.Depth()
}

func (q *countingWritebackQueue) Capacity() int {
	if q.inner == nil {
		return 0
	}
	return q.inner.Capacity()
}

func (q *countingWritebackQueue) Close() error {
	if q.inner == nil {
		return nil
	}
	return q.inner.Close()
}

func (m *memoryStateBackend) Load() (*persistedState, error) {
	if !m.loaded {
		return nil, nil
	}
	data, err := json.Marshal(m.snapshot)
	if err != nil {
		return nil, err
	}
	var clone persistedState
	if err := json.Unmarshal(data, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}

func (m *memoryStateBackend) Save(state *persistedState) error {
	atomic.AddInt32(&m.saveCalls, 1)
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	var clone persistedState
	if err := json.Unmarshal(data, &clone); err != nil {
		return err
	}
	m.snapshot = clone
	m.loaded = true
	return nil
}

func TestStoreWriteReadConflictDeleteLifecycle(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)

	write1, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_1",
		Path:          "/notion/Engineering/Auth.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# v1",
		CorrelationID: "corr_1",
	})
	if err != nil {
		t.Fatalf("write create failed: %v", err)
	}
	if write1.TargetRevision == "" {
		t.Fatalf("expected target revision")
	}
	if write1.Writeback.Provider != "notion" {
		t.Fatalf("expected writeback provider notion, got %s", write1.Writeback.Provider)
	}

	file, err := store.ReadFile("ws_1", "/notion/Engineering/Auth.md")
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if file.Content != "# v1" {
		t.Fatalf("unexpected content: %q", file.Content)
	}

	_, err = store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_1",
		Path:          "/notion/Engineering/Auth.md",
		IfMatch:       "rev_stale",
		ContentType:   "text/markdown",
		Content:       "# stale",
		CorrelationID: "corr_2",
	})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected revision conflict, got: %v", err)
	}

	write2, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_1",
		Path:          "/notion/Engineering/Auth.md",
		IfMatch:       file.Revision,
		ContentType:   "text/markdown",
		Content:       "# v2",
		CorrelationID: "corr_3",
	})
	if err != nil {
		t.Fatalf("write update failed: %v", err)
	}

	_, err = store.DeleteFile(DeleteRequest{
		WorkspaceID:   "ws_1",
		Path:          "/notion/Engineering/Auth.md",
		IfMatch:       write2.TargetRevision,
		CorrelationID: "corr_4",
	})
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
}

func TestStoreUsesCustomStateBackend(t *testing.T) {
	backend := &memoryStateBackend{}
	store := NewStoreWithOptions(StoreOptions{
		StateBackend:   backend,
		DisableWorkers: true,
	})

	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_backend",
		Path:          "/notion/Backend.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# backend",
		CorrelationID: "corr_backend_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if write.TargetRevision == "" {
		t.Fatalf("expected target revision from write")
	}
	if atomic.LoadInt32(&backend.saveCalls) < 1 {
		t.Fatalf("expected custom backend Save to be called")
	}
	store.Close()

	recovered := NewStoreWithOptions(StoreOptions{
		StateBackend:   backend,
		DisableWorkers: true,
	})
	t.Cleanup(recovered.Close)

	file, err := recovered.ReadFile("ws_backend", "/notion/Backend.md")
	if err != nil {
		t.Fatalf("read from recovered store failed: %v", err)
	}
	if file.Content != "# backend" {
		t.Fatalf("expected recovered content '# backend', got %q", file.Content)
	}
}

func TestStoreUsesCustomQueues(t *testing.T) {
	envelopeQueue := &countingEnvelopeQueue{
		inner: NewInMemoryEnvelopeQueue(4),
	}
	writebackQueue := &countingWritebackQueue{
		inner: NewInMemoryWritebackQueue(4),
	}
	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers: true,
		EnvelopeQueue:  envelopeQueue,
		WritebackQueue: writebackQueue,
	})
	t.Cleanup(store.Close)

	_, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_custom_queue",
		Path:          "/notion/Queue.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# queue",
		CorrelationID: "corr_queue_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}

	_, err = store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_custom_queue_1",
		WorkspaceID:   "ws_custom_queue",
		Provider:      "notion",
		DeliveryID:    "delivery_custom_queue_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Payload:       map[string]any{"type": "notion.page.upsert", "objectId": "obj_custom_queue_1", "path": "/notion/Queue.md", "content": "# queue"},
		CorrelationID: "corr_queue_2",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}

	if atomic.LoadInt32(&writebackQueue.tryCalls) < 1 {
		t.Fatalf("expected custom writeback queue TryEnqueue to be used")
	}
	if atomic.LoadInt32(&envelopeQueue.tryCalls) < 1 {
		t.Fatalf("expected custom envelope queue TryEnqueue to be used")
	}

	status, err := store.GetIngressStatus("ws_custom_queue")
	if err != nil {
		t.Fatalf("ingress status failed: %v", err)
	}
	if status.QueueCapacity != 4 {
		t.Fatalf("expected queue capacity from custom queue (4), got %d", status.QueueCapacity)
	}
}

func TestProcessWritebackSkipsNonPendingOperation(t *testing.T) {
	var writeCalls int32
	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers: true,
		ProviderWriteAction: func(action WritebackAction) error {
			atomic.AddInt32(&writeCalls, 1)
			return nil
		},
	})
	t.Cleanup(store.Close)

	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_writeback_guard",
		Path:          "/notion/Guard.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# guard",
		CorrelationID: "corr_writeback_guard_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}

	store.mu.Lock()
	ws := store.workspaces["ws_writeback_guard"]
	op := ws.Ops[write.OpID]
	op.Status = "succeeded"
	ws.Ops[write.OpID] = op
	store.mu.Unlock()

	store.processWriteback(writebackTask{
		WorkspaceID:   "ws_writeback_guard",
		OpID:          write.OpID,
		Path:          "/notion/Guard.md",
		Revision:      write.TargetRevision,
		CorrelationID: "corr_writeback_guard_2",
	})

	if atomic.LoadInt32(&writeCalls) != 0 {
		t.Fatalf("expected no provider write for non-pending op, got %d calls", atomic.LoadInt32(&writeCalls))
	}
}

func TestEnqueueEnvelopeDeduplicatesQueuedEnvelopeID(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers:    true,
		EnvelopeQueueSize: 4,
	})
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_queue_dedupe_1",
		WorkspaceID:   "ws_queue_dedupe",
		Provider:      "notion",
		DeliveryID:    "delivery_queue_dedupe_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Payload:       map[string]any{"type": "notion.page.upsert", "objectId": "obj_queue_dedupe_1", "path": "/notion/Q.md", "content": "# q"},
		CorrelationID: "corr_queue_dedupe_1",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}

	_, err = store.ReplayEnvelope("env_queue_dedupe_1", "corr_queue_dedupe_2")
	if err != nil {
		t.Fatalf("first replay failed: %v", err)
	}
	_, err = store.ReplayEnvelope("env_queue_dedupe_1", "corr_queue_dedupe_3")
	if err != nil {
		t.Fatalf("second replay failed: %v", err)
	}

	status, err := store.GetIngressStatus("ws_queue_dedupe")
	if err != nil {
		t.Fatalf("ingress status failed: %v", err)
	}
	if status.QueueDepth != 1 {
		t.Fatalf("expected envelope queue depth to remain 1 after duplicate replay enqueues, got %d", status.QueueDepth)
	}
}

func TestEnqueueWritebackDeduplicatesByOpID(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers: true,
	})
	t.Cleanup(store.Close)

	task := writebackTask{
		WorkspaceID:   "ws_writeback_dedupe",
		OpID:          "op_writeback_dedupe_1",
		Path:          "/notion/W.md",
		Revision:      "rev_1",
		CorrelationID: "corr_writeback_dedupe_1",
	}
	store.enqueueWriteback(task)
	store.enqueueWriteback(task)

	if depth := store.writebackQueue.Depth(); depth != 1 {
		t.Fatalf("expected writeback queue depth 1 for duplicate op enqueue, got %d", depth)
	}
}

func TestStoreSeedsQueuedIndexesFromFileQueueSnapshots(t *testing.T) {
	dir := t.TempDir()
	envelopeQueuePath := filepath.Join(dir, "seed-envelope-queue.json")
	writebackQueuePath := filepath.Join(dir, "seed-writeback-queue.json")

	envelopeQueue, err := NewFileEnvelopeQueue(envelopeQueuePath, 8)
	if err != nil {
		t.Fatalf("new file envelope queue failed: %v", err)
	}
	writebackQueue, err := NewFileWritebackQueue(writebackQueuePath, 8)
	if err != nil {
		t.Fatalf("new file writeback queue failed: %v", err)
	}
	if !envelopeQueue.TryEnqueue("env_seed_1") {
		t.Fatalf("seed envelope enqueue failed")
	}
	if !writebackQueue.TryEnqueue(WritebackQueueItem{
		WorkspaceID:   "ws_seed",
		OpID:          "op_seed_1",
		Path:          "/notion/Seed.md",
		Revision:      "rev_seed_1",
		CorrelationID: "corr_seed_1",
	}) {
		t.Fatalf("seed writeback enqueue failed")
	}

	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers: true,
		EnvelopeQueue:  envelopeQueue,
		WritebackQueue: writebackQueue,
	})
	t.Cleanup(store.Close)

	store.enqueueEnvelope("env_seed_1")
	store.enqueueWriteback(writebackTask{
		WorkspaceID:   "ws_seed",
		OpID:          "op_seed_1",
		Path:          "/notion/Seed.md",
		Revision:      "rev_seed_1",
		CorrelationID: "corr_seed_1",
	})

	if depth := store.envelopeQueue.Depth(); depth != 1 {
		t.Fatalf("expected seeded envelope queue depth to remain 1, got %d", depth)
	}
	if depth := store.writebackQueue.Depth(); depth != 1 {
		t.Fatalf("expected seeded writeback queue depth to remain 1, got %d", depth)
	}
}

func TestEnqueueEnvelopeDeduplicatesBlockingEnqueuePath(t *testing.T) {
	envelopeQueue := &blockingEnvelopeQueue{
		capacity: 4,
		release:  make(chan struct{}),
	}
	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers: true,
		EnvelopeQueue:  envelopeQueue,
	})
	t.Cleanup(store.Close)

	store.enqueueEnvelope("env_blocking_dedupe_1")
	store.enqueueEnvelope("env_blocking_dedupe_1")

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&envelopeQueue.enqueue) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&envelopeQueue.enqueue) != 1 {
		t.Fatalf("expected single blocking envelope enqueue call, got %d", atomic.LoadInt32(&envelopeQueue.enqueue))
	}
	close(envelopeQueue.release)
}

func TestEnqueueWritebackDeduplicatesBlockingEnqueuePath(t *testing.T) {
	writebackQueue := &blockingWritebackQueue{
		capacity: 4,
		release:  make(chan struct{}),
	}
	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers: true,
		WritebackQueue: writebackQueue,
	})
	t.Cleanup(store.Close)

	task := writebackTask{
		WorkspaceID:   "ws_blocking_dedupe",
		OpID:          "op_blocking_dedupe_1",
		Path:          "/notion/Blocking.md",
		Revision:      "rev_blocking_1",
		CorrelationID: "corr_blocking_dedupe_1",
	}
	store.enqueueWriteback(task)
	store.enqueueWriteback(task)

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&writebackQueue.enqueue) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&writebackQueue.enqueue) != 1 {
		t.Fatalf("expected single blocking writeback enqueue call, got %d", atomic.LoadInt32(&writebackQueue.enqueue))
	}
	close(writebackQueue.release)
}

func TestGetBackendStatus(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers: true,
		StateBackend:   NewInMemoryStateBackend(),
		EnvelopeQueue:  NewInMemoryEnvelopeQueue(11),
		WritebackQueue: NewInMemoryWritebackQueue(13),
		BackendProfile: "durable-local",
	})
	t.Cleanup(store.Close)

	store.enqueueEnvelope("env_backend_status_1")
	store.enqueueWriteback(writebackTask{
		WorkspaceID:   "ws_backend_status",
		OpID:          "op_backend_status_1",
		Path:          "/notion/BackendStatus.md",
		Revision:      "rev_backend_status_1",
		CorrelationID: "corr_backend_status_1",
	})

	status := store.GetBackendStatus()
	if status.StateBackend == "" || status.EnvelopeQueue == "" || status.WritebackQueue == "" {
		t.Fatalf("expected backend type names, got %+v", status)
	}
	if status.BackendProfile != "durable-local" {
		t.Fatalf("expected backend profile durable-local, got %q", status.BackendProfile)
	}
	if status.EnvelopeQueueCap != 11 || status.WritebackQueueCap != 13 {
		t.Fatalf("expected queue capacities 11/13, got %+v", status)
	}
	if status.EnvelopeQueueDepth < 1 || status.WritebackQueueDepth < 1 {
		t.Fatalf("expected non-zero queue depths after enqueue, got %+v", status)
	}
}

func TestWriteCreateInfersProviderFromPath(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)

	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_provider_infer",
		Path:          "/custom/ProviderInfer.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# custom",
		CorrelationID: "corr_provider_infer_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if write.Writeback.Provider != "custom" {
		t.Fatalf("expected writeback provider custom, got %s", write.Writeback.Provider)
	}

	file, err := store.ReadFile("ws_provider_infer", "/custom/ProviderInfer.md")
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if file.Provider != "custom" {
		t.Fatalf("expected file provider custom, got %s", file.Provider)
	}

	op, err := store.GetOperation("ws_provider_infer", write.OpID)
	if err != nil {
		t.Fatalf("get op failed: %v", err)
	}
	if op.Provider != "custom" {
		t.Fatalf("expected op provider custom, got %s", op.Provider)
	}
}

func TestListTreeHonorsDepth(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)

	writes := []WriteRequest{
		{
			WorkspaceID:   "ws_tree_depth",
			Path:          "/notion/Engineering/Auth.md",
			IfMatch:       "0",
			ContentType:   "text/markdown",
			Content:       "# auth",
			CorrelationID: "corr_tree_depth_1",
		},
		{
			WorkspaceID:   "ws_tree_depth",
			Path:          "/notion/Engineering/Security/Policy.md",
			IfMatch:       "0",
			ContentType:   "text/markdown",
			Content:       "# policy",
			CorrelationID: "corr_tree_depth_2",
		},
		{
			WorkspaceID:   "ws_tree_depth",
			Path:          "/notion/Product/Roadmap.md",
			IfMatch:       "0",
			ContentType:   "text/markdown",
			Content:       "# roadmap",
			CorrelationID: "corr_tree_depth_3",
		},
	}
	for _, writeReq := range writes {
		if _, err := store.WriteFile(writeReq); err != nil {
			t.Fatalf("write failed for %s: %v", writeReq.Path, err)
		}
	}

	depth1, err := store.ListTree("ws_tree_depth", "/notion", 1, "")
	if err != nil {
		t.Fatalf("list tree depth=1 failed: %v", err)
	}
	if len(depth1.Entries) != 2 {
		t.Fatalf("expected 2 depth-1 entries, got %d", len(depth1.Entries))
	}
	if depth1.Entries[0].Path != "/notion/Engineering" || depth1.Entries[0].Type != "dir" {
		t.Fatalf("unexpected first depth-1 entry: %+v", depth1.Entries[0])
	}
	if depth1.Entries[1].Path != "/notion/Product" || depth1.Entries[1].Type != "dir" {
		t.Fatalf("unexpected second depth-1 entry: %+v", depth1.Entries[1])
	}

	depth2, err := store.ListTree("ws_tree_depth", "/notion", 2, "")
	if err != nil {
		t.Fatalf("list tree depth=2 failed: %v", err)
	}
	expected := map[string]string{
		"/notion/Engineering":          "dir",
		"/notion/Engineering/Auth.md":  "file",
		"/notion/Engineering/Security": "dir",
		"/notion/Product":              "dir",
		"/notion/Product/Roadmap.md":   "file",
	}
	if len(depth2.Entries) != len(expected) {
		t.Fatalf("expected %d depth-2 entries, got %d", len(expected), len(depth2.Entries))
	}
	for _, entry := range depth2.Entries {
		expectedType, ok := expected[entry.Path]
		if !ok {
			t.Fatalf("unexpected depth-2 entry: %+v", entry)
		}
		if expectedType != entry.Type {
			t.Fatalf("expected %s to be %s, got %s", entry.Path, expectedType, entry.Type)
		}
	}
}

func TestStoreEventsAndOps(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)

	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_2",
		Path:          "/notion/Product/Roadmap.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# roadmap",
		CorrelationID: "corr_evt",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}

	feed, err := store.GetEvents("ws_2", "", "", 100)
	if err != nil {
		t.Fatalf("events failed: %v", err)
	}
	if len(feed.Events) == 0 {
		t.Fatalf("expected at least one event")
	}

	op, err := store.GetOperation("ws_2", write.OpID)
	if err != nil {
		t.Fatalf("op lookup failed: %v", err)
	}
	if op.Status == "" {
		t.Fatalf("expected op status")
	}
	if op.Provider != "notion" {
		t.Fatalf("expected op provider notion, got %q", op.Provider)
	}
}

func TestGetEventsSupportsProviderFilter(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)

	if _, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_events_provider",
		Path:          "/notion/EventsProvider.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# notion",
		CorrelationID: "corr_events_provider_1",
	}); err != nil {
		t.Fatalf("notion write failed: %v", err)
	}
	if _, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_events_provider",
		Path:          "/custom/EventsProvider.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# custom",
		CorrelationID: "corr_events_provider_2",
	}); err != nil {
		t.Fatalf("custom write failed: %v", err)
	}

	customFeed, err := store.GetEvents("ws_events_provider", "custom", "", 1000)
	if err != nil {
		t.Fatalf("custom filtered events failed: %v", err)
	}
	if len(customFeed.Events) == 0 {
		t.Fatalf("expected custom filtered events to be non-empty")
	}
	for _, event := range customFeed.Events {
		if event.Provider != "custom" {
			t.Fatalf("expected custom provider events only, got provider=%q type=%q path=%q", event.Provider, event.Type, event.Path)
		}
	}

	notionFeed, err := store.GetEvents("ws_events_provider", "notion", "", 1000)
	if err != nil {
		t.Fatalf("notion filtered events failed: %v", err)
	}
	if len(notionFeed.Events) == 0 {
		t.Fatalf("expected notion filtered events to be non-empty")
	}
	for _, event := range notionFeed.Events {
		if event.Provider != "notion" {
			t.Fatalf("expected notion provider events only, got provider=%q type=%q path=%q", event.Provider, event.Type, event.Path)
		}
	}
}

func TestProviderUpsertWithoutPathUsesObjectIdentity(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)

	firstReceivedAt := time.Now().UTC().Add(-2 * time.Second).Format(time.RFC3339Nano)
	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:  "env_object_identity_1",
		WorkspaceID: "ws_object_identity",
		Provider:    "notion",
		DeliveryID:  "delivery_object_identity_1",
		ReceivedAt:  firstReceivedAt,
		Payload: map[string]any{
			"type":     "notion.page.upsert",
			"objectId": "notion_obj_identity_1",
			"path":     "/notion/ObjectIdentity.md",
			"content":  "# initial",
		},
		CorrelationID: "corr_object_identity_1",
	})
	if err != nil {
		t.Fatalf("first ingest failed: %v", err)
	}

	var firstRevision string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		file, readErr := store.ReadFile("ws_object_identity", "/notion/ObjectIdentity.md")
		if readErr == nil && file.Content == "# initial" {
			firstRevision = file.Revision
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if firstRevision == "" {
		t.Fatalf("expected initial provider upsert to materialize file")
	}

	secondReceivedAt := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:  "env_object_identity_2",
		WorkspaceID: "ws_object_identity",
		Provider:    "notion",
		DeliveryID:  "delivery_object_identity_2",
		ReceivedAt:  secondReceivedAt,
		Payload: map[string]any{
			"type":     "notion.page.upsert",
			"objectId": "notion_obj_identity_1",
			"content":  "# updated",
		},
		CorrelationID: "corr_object_identity_2",
	})
	if err != nil {
		t.Fatalf("second ingest failed: %v", err)
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		file, readErr := store.ReadFile("ws_object_identity", "/notion/ObjectIdentity.md")
		if readErr == nil && file.Content == "# updated" && file.Revision != firstRevision {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected object-identity upsert without path to update existing projected file")
}

func TestPendingWritebacksRecoveredOnRestart(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "relayfile-state.json")

	store := NewStoreWithOptions(StoreOptions{
		StateFile:         stateFile,
		DisableWorkers:    true,
		EnvelopeQueueSize: 8,
	})
	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_restart_recovery",
		Path:          "/notion/RestartRecovery.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# pending",
		CorrelationID: "corr_restart_recovery_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	op, err := store.GetOperation("ws_restart_recovery", write.OpID)
	if err != nil {
		t.Fatalf("get pending operation failed: %v", err)
	}
	if op.Status != "pending" {
		t.Fatalf("expected pending status before restart, got %s", op.Status)
	}
	store.Close()

	var writeCalls int32
	recovered := NewStoreWithOptions(StoreOptions{
		StateFile: stateFile,
		ProviderWriteAction: func(action WritebackAction) error {
			atomic.AddInt32(&writeCalls, 1)
			return nil
		},
		WritebackDelay: 5 * time.Millisecond,
	})
	t.Cleanup(recovered.Close)

	waitForOpStatus(t, recovered, "ws_restart_recovery", write.OpID, "succeeded")
	if atomic.LoadInt32(&writeCalls) < 1 {
		t.Fatalf("expected recovered store to execute pending writeback at least once")
	}
}

func TestRecoveredWritebackRespectsPersistedNextAttemptAt(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "relayfile-next-attempt.json")

	store := NewStoreWithOptions(StoreOptions{
		StateFile:            stateFile,
		MaxWritebackAttempts: 3,
		WritebackDelay:       400 * time.Millisecond,
		ProviderWriteAction: func(action WritebackAction) error {
			return fmt.Errorf("transient writeback failure")
		},
	})
	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_next_attempt",
		Path:          "/notion/NextAttempt.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# retry",
		CorrelationID: "corr_next_attempt_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		op, opErr := store.GetOperation("ws_next_attempt", write.OpID)
		if opErr == nil && op.Status == "pending" && op.AttemptCount >= 1 && op.NextAttemptAt != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	store.Close()

	var recoveredCalls int32
	recoveryStart := time.Now()
	recovered := NewStoreWithOptions(StoreOptions{
		StateFile:            stateFile,
		MaxWritebackAttempts: 3,
		WritebackDelay:       400 * time.Millisecond,
		ProviderWriteAction: func(action WritebackAction) error {
			atomic.AddInt32(&recoveredCalls, 1)
			return nil
		},
	})
	t.Cleanup(recovered.Close)

	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt32(&recoveredCalls) != 0 {
		t.Fatalf("expected recovered writeback to wait for persisted nextAttemptAt delay")
	}

	waitForOpStatus(t, recovered, "ws_next_attempt", write.OpID, "succeeded")
	if atomic.LoadInt32(&recoveredCalls) < 1 {
		t.Fatalf("expected recovered writeback to execute after delay")
	}
	if time.Since(recoveryStart) < 200*time.Millisecond {
		t.Fatalf("expected recovered writeback to respect backoff timing")
	}
}

func TestEnvelopeRetryDelayPersistsAcrossRestart(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "relayfile-envelope-retry.json")

	var initialParseCalls int32
	store := NewStoreWithOptions(StoreOptions{
		StateFile:           stateFile,
		MaxEnvelopeAttempts: 3,
		EnvelopeRetryDelay:  400 * time.Millisecond,
		Adapters: []ProviderAdapter{
			testAdapter{
				provider: "custom",
				parseEnvelope: func(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
					atomic.AddInt32(&initialParseCalls, 1)
					return nil, fmt.Errorf("transient parse failure")
				},
			},
		},
	})
	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_retry_persist_1",
		WorkspaceID:   "ws_retry_persist",
		Provider:      "custom",
		DeliveryID:    "delivery_retry_persist_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Payload:       map[string]any{"type": "custom.retry"},
		CorrelationID: "corr_retry_persist_1",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&initialParseCalls) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	store.Close()

	var recoveredParseCalls int32
	recovered := NewStoreWithOptions(StoreOptions{
		StateFile:           stateFile,
		MaxEnvelopeAttempts: 3,
		EnvelopeRetryDelay:  400 * time.Millisecond,
		Adapters: []ProviderAdapter{
			testAdapter{
				provider: "custom",
				parseEnvelope: func(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
					atomic.AddInt32(&recoveredParseCalls, 1)
					return nil, fmt.Errorf("transient parse failure")
				},
			},
		},
	})
	t.Cleanup(recovered.Close)

	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt32(&recoveredParseCalls) != 0 {
		t.Fatalf("expected recovered envelope retry to respect persisted delay")
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&recoveredParseCalls) >= 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected recovered envelope retry to execute after persisted delay")
}

func TestEnvelopeWorkersRespectProviderConcurrencyLimit(t *testing.T) {
	var active int32
	var maxActive int32

	store := NewStoreWithOptions(StoreOptions{
		EnvelopeWorkers:        4,
		ProviderMaxConcurrency: 1,
		EnvelopeRetryDelay:     5 * time.Millisecond,
		MaxEnvelopeAttempts:    1,
		Adapters: []ProviderAdapter{
			testAdapter{
				provider: "custom",
				parseEnvelope: func(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
					current := atomic.AddInt32(&active, 1)
					for {
						existing := atomic.LoadInt32(&maxActive)
						if current <= existing {
							break
						}
						if atomic.CompareAndSwapInt32(&maxActive, existing, current) {
							break
						}
					}
					time.Sleep(40 * time.Millisecond)
					atomic.AddInt32(&active, -1)
					return []ApplyAction{
						{
							Type:             ActionFileUpsert,
							Path:             toString(req.Payload["path"]),
							Content:          toString(req.Payload["content"]),
							ContentType:      "text/markdown",
							ProviderObjectID: toString(req.Payload["objectId"]),
						},
					}, nil
				},
			},
		},
	})
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_provider_concurrency_1",
		WorkspaceID:   "ws_provider_concurrency",
		Provider:      "custom",
		DeliveryID:    "delivery_provider_concurrency_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Payload:       map[string]any{"type": "custom.upsert", "path": "/custom/A.md", "content": "# a", "objectId": "obj_a"},
		CorrelationID: "corr_provider_concurrency_1",
	})
	if err != nil {
		t.Fatalf("ingest 1 failed: %v", err)
	}
	_, err = store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_provider_concurrency_2",
		WorkspaceID:   "ws_provider_concurrency",
		Provider:      "custom",
		DeliveryID:    "delivery_provider_concurrency_2",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Payload:       map[string]any{"type": "custom.upsert", "path": "/custom/B.md", "content": "# b", "objectId": "obj_b"},
		CorrelationID: "corr_provider_concurrency_2",
	})
	if err != nil {
		t.Fatalf("ingest 2 failed: %v", err)
	}

	waitForFileContent(t, store, "ws_provider_concurrency", "/custom/A.md", "# a")
	waitForFileContent(t, store, "ws_provider_concurrency", "/custom/B.md", "# b")

	if atomic.LoadInt32(&maxActive) > 1 {
		t.Fatalf("expected max concurrent provider processing <= 1, got %d", atomic.LoadInt32(&maxActive))
	}
}

func TestAcquireProviderSlotScopedByWorkspaceAndProvider(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers:         true,
		ProviderMaxConcurrency: 1,
	})
	t.Cleanup(store.Close)

	store.mu.Lock()
	store.envelopesByID["env_scope_1"] = WebhookEnvelopeRequest{
		EnvelopeID:  "env_scope_1",
		WorkspaceID: "ws_scope_1",
		Provider:    "custom",
	}
	store.envelopesByID["env_scope_2"] = WebhookEnvelopeRequest{
		EnvelopeID:  "env_scope_2",
		WorkspaceID: "ws_scope_2",
		Provider:    "custom",
	}
	store.envelopesByID["env_scope_3"] = WebhookEnvelopeRequest{
		EnvelopeID:  "env_scope_3",
		WorkspaceID: "ws_scope_1",
		Provider:    "custom",
	}
	store.mu.Unlock()

	release1 := store.acquireProviderSlot("env_scope_1")
	t.Cleanup(release1)

	acquiredDifferentWorkspace := make(chan struct{}, 1)
	go func() {
		release := store.acquireProviderSlot("env_scope_2")
		release()
		acquiredDifferentWorkspace <- struct{}{}
	}()
	select {
	case <-acquiredDifferentWorkspace:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("expected slot acquisition for different workspace to proceed without waiting")
	}

	acquiredSameWorkspace := make(chan struct{}, 1)
	go func() {
		release := store.acquireProviderSlot("env_scope_3")
		release()
		acquiredSameWorkspace <- struct{}{}
	}()
	select {
	case <-acquiredSameWorkspace:
		t.Fatalf("expected same-workspace slot acquisition to block until release")
	case <-time.After(60 * time.Millisecond):
	}

	release1()
	select {
	case <-acquiredSameWorkspace:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("expected same-workspace slot acquisition to unblock after release")
	}
}

func TestEnvelopeWorkersParseInParallelAcrossWorkspaceScope(t *testing.T) {
	var active int32
	var maxActive int32

	store := NewStoreWithOptions(StoreOptions{
		EnvelopeWorkers:        4,
		ProviderMaxConcurrency: 1,
		Adapters: []ProviderAdapter{
			testAdapter{
				provider: "custom",
				parseEnvelope: func(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
					current := atomic.AddInt32(&active, 1)
					for {
						existing := atomic.LoadInt32(&maxActive)
						if current <= existing {
							break
						}
						if atomic.CompareAndSwapInt32(&maxActive, existing, current) {
							break
						}
					}
					time.Sleep(50 * time.Millisecond)
					atomic.AddInt32(&active, -1)
					return []ApplyAction{
						{
							Type:             ActionFileUpsert,
							Path:             toString(req.Payload["path"]),
							Content:          toString(req.Payload["content"]),
							ContentType:      "text/markdown",
							ProviderObjectID: toString(req.Payload["objectId"]),
						},
					}, nil
				},
			},
		},
	})
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_parallel_scope_1",
		WorkspaceID:   "ws_parallel_scope_1",
		Provider:      "custom",
		DeliveryID:    "delivery_parallel_scope_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Payload:       map[string]any{"type": "custom.upsert", "path": "/custom/A.md", "content": "# a", "objectId": "obj_a"},
		CorrelationID: "corr_parallel_scope_1",
	})
	if err != nil {
		t.Fatalf("ingest 1 failed: %v", err)
	}
	_, err = store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_parallel_scope_2",
		WorkspaceID:   "ws_parallel_scope_2",
		Provider:      "custom",
		DeliveryID:    "delivery_parallel_scope_2",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Payload:       map[string]any{"type": "custom.upsert", "path": "/custom/B.md", "content": "# b", "objectId": "obj_b"},
		CorrelationID: "corr_parallel_scope_2",
	})
	if err != nil {
		t.Fatalf("ingest 2 failed: %v", err)
	}

	waitForFileContent(t, store, "ws_parallel_scope_1", "/custom/A.md", "# a")
	waitForFileContent(t, store, "ws_parallel_scope_2", "/custom/B.md", "# b")

	if atomic.LoadInt32(&maxActive) < 2 {
		t.Fatalf("expected at least 2 parallel parse operations across workspaces, got %d", atomic.LoadInt32(&maxActive))
	}
}

func TestSuppressionMarkersPersistAcrossRestart(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "relayfile-suppressions.json")

	store := NewStoreWithOptions(StoreOptions{
		StateFile:         stateFile,
		SuppressionWindow: 5 * time.Minute,
		WritebackDelay:    5 * time.Millisecond,
	})
	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_loop_persist",
		Path:          "/notion/LoopPersist.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# initial",
		CorrelationID: "corr_loop_persist_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	waitForOpStatus(t, store, "ws_loop_persist", write.OpID, "succeeded")
	store.Close()

	recovered := NewStoreWithOptions(StoreOptions{
		StateFile:         stateFile,
		SuppressionWindow: 5 * time.Minute,
	})
	t.Cleanup(recovered.Close)

	_, err = recovered.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:  "env_loop_persist_1",
		WorkspaceID: "ws_loop_persist",
		Provider:    "notion",
		DeliveryID:  "delivery_loop_persist_1",
		ReceivedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		Payload: map[string]any{
			"type":          "notion.page.upsert",
			"objectId":      "notion_loop_persist_1",
			"path":          "/notion/LoopPersist.md",
			"content":       "# echo",
			"origin":        "relayfile",
			"opId":          write.OpID,
			"correlationId": "corr_loop_persist_1",
		},
		CorrelationID: "corr_loop_persist_2",
	})
	if err != nil {
		t.Fatalf("ingest loop echo failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		feed, feedErr := recovered.GetEvents("ws_loop_persist", "", "", 1000)
		if feedErr != nil {
			t.Fatalf("events lookup failed: %v", feedErr)
		}
		for _, event := range feed.Events {
			if event.Type == "sync.suppressed" && event.CorrelationID == "corr_loop_persist_2" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected sync.suppressed event after restart for loop echo")
}

func TestGetSyncStatusMarksProviderLaggingFromPendingEnvelope(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers: true,
	})
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_sync_lag_1",
		WorkspaceID:   "ws_sync_lag",
		Provider:      "notion",
		DeliveryID:    "delivery_sync_lag_1",
		ReceivedAt:    time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339Nano),
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_sync_lag_1",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}

	status, err := store.GetSyncStatus("ws_sync_lag", "")
	if err != nil {
		t.Fatalf("sync status failed: %v", err)
	}
	if len(status.Providers) == 0 {
		t.Fatalf("expected at least one provider status")
	}
	var notionStatus *SyncProviderStatus
	for i := range status.Providers {
		if status.Providers[i].Provider == "notion" {
			notionStatus = &status.Providers[i]
			break
		}
	}
	if notionStatus == nil {
		t.Fatalf("expected notion provider in sync status: %+v", status.Providers)
	}
	if notionStatus.Status != "lagging" {
		t.Fatalf("expected notion status lagging, got %s", notionStatus.Status)
	}
	if notionStatus.LagSeconds < 100 {
		t.Fatalf("expected lag seconds >= 100, got %d", notionStatus.LagSeconds)
	}
}

func TestGetSyncStatusMarksProviderErrorFromDeadLetter(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		MaxEnvelopeAttempts: 1,
		Adapters: []ProviderAdapter{
			testAdapter{
				provider: "custom",
				parseEnvelope: func(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
					return nil, fmt.Errorf("sync status parse failure")
				},
			},
		},
	})
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_sync_error_1",
		WorkspaceID:   "ws_sync_error",
		Provider:      "custom",
		DeliveryID:    "delivery_sync_error_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Payload:       map[string]any{"type": "custom.dead"},
		CorrelationID: "corr_sync_error_1",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := store.GetSyncStatus("ws_sync_error", "custom")
		if err != nil {
			t.Fatalf("sync status failed: %v", err)
		}
		if len(status.Providers) != 1 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		providerStatus := status.Providers[0]
		if providerStatus.Status == "error" {
			if providerStatus.LastError == nil || *providerStatus.LastError == "" {
				t.Fatalf("expected provider last error when status=error")
			}
			if providerStatus.FailureCodes["unknown"] < 1 {
				t.Fatalf("expected failure code aggregation, got %+v", providerStatus.FailureCodes)
			}
			if providerStatus.DeadLetteredEnvelopes < 1 {
				t.Fatalf("expected dead-lettered envelope count, got %d", providerStatus.DeadLetteredEnvelopes)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected provider status to transition to error from dead-letter state")
}

func TestGetSyncStatusMarksProviderErrorFromWritebackDeadLetter(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		MaxWritebackAttempts: 1,
		WritebackDelay:       5 * time.Millisecond,
		ProviderWrite: func(workspaceID, path, revision string) error {
			return fmt.Errorf("writeback provider failure")
		},
	})
	t.Cleanup(store.Close)

	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_sync_writeback_error",
		Path:          "/notion/SyncWritebackError.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# writeback error",
		CorrelationID: "corr_sync_writeback_error_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	waitForOpStatus(t, store, "ws_sync_writeback_error", write.OpID, "dead_lettered")

	status, err := store.GetSyncStatus("ws_sync_writeback_error", "notion")
	if err != nil {
		t.Fatalf("sync status failed: %v", err)
	}
	if len(status.Providers) != 1 {
		t.Fatalf("expected exactly one provider status, got %d", len(status.Providers))
	}
	providerStatus := status.Providers[0]
	if providerStatus.Status != "error" {
		t.Fatalf("expected provider status error, got %s", providerStatus.Status)
	}
	if providerStatus.LastError == nil || *providerStatus.LastError == "" {
		t.Fatalf("expected provider lastError from writeback dead-letter")
	}
	if providerStatus.FailureCodes["unknown"] < 1 {
		t.Fatalf("expected writeback failure code aggregation, got %+v", providerStatus.FailureCodes)
	}
	if providerStatus.DeadLetteredOps < 1 {
		t.Fatalf("expected dead-lettered ops count, got %d", providerStatus.DeadLetteredOps)
	}
}

func TestGetSyncStatusIncludesProviderPresentOnlyInOperations(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		MaxWritebackAttempts: 1,
		WritebackDelay:       5 * time.Millisecond,
		ProviderWriteAction: func(action WritebackAction) error {
			if action.Type == WritebackActionFileDelete {
				return fmt.Errorf("delete failure for provider discovery")
			}
			return nil
		},
	})
	t.Cleanup(store.Close)

	create, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_sync_provider_discovery",
		Path:          "/custom/ProviderFromOps.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# custom",
		CorrelationID: "corr_sync_provider_discovery_1",
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	waitForOpStatus(t, store, "ws_sync_provider_discovery", create.OpID, "succeeded")

	file, err := store.ReadFile("ws_sync_provider_discovery", "/custom/ProviderFromOps.md")
	if err != nil {
		t.Fatalf("read after create failed: %v", err)
	}
	del, err := store.DeleteFile(DeleteRequest{
		WorkspaceID:   "ws_sync_provider_discovery",
		Path:          "/custom/ProviderFromOps.md",
		IfMatch:       file.Revision,
		CorrelationID: "corr_sync_provider_discovery_2",
	})
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	waitForOpStatus(t, store, "ws_sync_provider_discovery", del.OpID, "dead_lettered")
	waitForNotFound(t, store, "ws_sync_provider_discovery", "/custom/ProviderFromOps.md")

	status, err := store.GetSyncStatus("ws_sync_provider_discovery", "")
	if err != nil {
		t.Fatalf("sync status failed: %v", err)
	}
	foundCustom := false
	for _, providerStatus := range status.Providers {
		if providerStatus.Provider != "custom" {
			continue
		}
		foundCustom = true
		if providerStatus.Status != "error" {
			t.Fatalf("expected custom provider status error, got %s", providerStatus.Status)
		}
		if providerStatus.LastError == nil || *providerStatus.LastError == "" {
			t.Fatalf("expected custom provider lastError to be populated")
		}
		if providerStatus.DeadLetteredOps < 1 {
			t.Fatalf("expected custom provider dead-lettered ops count, got %d", providerStatus.DeadLetteredOps)
		}
		break
	}
	if !foundCustom {
		t.Fatalf("expected custom provider to be included from operations, got %+v", status.Providers)
	}
}

func TestListSyncStatusesAggregatesWorkspacesAndAppliesProviderFilter(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers: true,
	})
	t.Cleanup(store.Close)

	if _, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_sync_list_ws_only",
		Path:          "/notion/Seed.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# seed",
		CorrelationID: "corr_sync_list_ws_only",
	}); err != nil {
		t.Fatalf("write seed file failed: %v", err)
	}

	if _, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_sync_list_1",
		WorkspaceID:   "ws_sync_list_1",
		Provider:      "notion",
		DeliveryID:    "delivery_sync_list_1",
		ReceivedAt:    time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339Nano),
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_sync_list_1",
	}); err != nil {
		t.Fatalf("ingest ws_sync_list_1 failed: %v", err)
	}
	if _, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_sync_list_2",
		WorkspaceID:   "ws_sync_list_2",
		Provider:      "custom",
		DeliveryID:    "delivery_sync_list_2",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_sync_list_2",
	}); err != nil {
		t.Fatalf("ingest ws_sync_list_2 failed: %v", err)
	}

	statuses := store.ListSyncStatuses("")
	if len(statuses) < 3 {
		t.Fatalf("expected at least three workspaces in sync status map, got %d", len(statuses))
	}
	if _, ok := statuses["ws_sync_list_ws_only"]; !ok {
		t.Fatalf("expected workspace created from local write to be present in sync status map")
	}
	if _, ok := statuses["ws_sync_list_1"]; !ok {
		t.Fatalf("expected ws_sync_list_1 in sync status map")
	}
	if _, ok := statuses["ws_sync_list_2"]; !ok {
		t.Fatalf("expected ws_sync_list_2 in sync status map")
	}

	filtered := store.ListSyncStatuses("custom")
	if len(filtered) < 3 {
		t.Fatalf("expected provider-filtered sync status map to retain workspace set, got %d", len(filtered))
	}
	for workspaceID, status := range filtered {
		if len(status.Providers) != 1 {
			t.Fatalf("expected one provider entry for workspace %s after provider filter, got %+v", workspaceID, status.Providers)
		}
		if status.Providers[0].Provider != "custom" {
			t.Fatalf("expected provider-filtered entry to be custom for workspace %s, got %+v", workspaceID, status.Providers[0])
		}
	}
}

func TestNormalizeFailureCode(t *testing.T) {
	cases := []struct {
		errText string
		code    string
	}{
		{errText: "429 too many requests", code: "rate_limited"},
		{errText: "request timeout", code: "timeout"},
		{errText: "401 unauthorized", code: "unauthorized"},
		{errText: "403 forbidden", code: "forbidden"},
		{errText: "404 not found", code: "not_found"},
		{errText: "409 conflict", code: "conflict"},
		{errText: "503 upstream unavailable", code: "provider_unavailable"},
		{errText: "some unknown failure", code: "unknown"},
	}
	for _, tc := range cases {
		if got := normalizeFailureCode(tc.errText); got != tc.code {
			t.Fatalf("normalizeFailureCode(%q) = %q, want %q", tc.errText, got, tc.code)
		}
	}
}

func TestTriggerSyncRefreshRejectsUnknownProvider(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)

	_, err := store.TriggerSyncRefresh("ws_sync_refresh", "unknown-provider", "manual", "corr_sync_refresh_1")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected invalid input for unknown provider, got %v", err)
	}

	resp, err := store.TriggerSyncRefresh("ws_sync_refresh", "notion", "manual", "corr_sync_refresh_2")
	if err != nil {
		t.Fatalf("expected known provider to queue refresh, got %v", err)
	}
	if resp.Status != "queued" || resp.ID == "" {
		t.Fatalf("unexpected queued response: %+v", resp)
	}
}

func TestListOperationsFiltersByStatus(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		MaxWritebackAttempts: 1,
		WritebackDelay:       5 * time.Millisecond,
		ProviderWrite: func(workspaceID, path, revision string) error {
			return fmt.Errorf("forced failure")
		},
	})
	t.Cleanup(store.Close)

	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_ops_list",
		Path:          "/notion/OpsList.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# ops",
		CorrelationID: "corr_ops_list_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	waitForOpStatus(t, store, "ws_ops_list", write.OpID, "dead_lettered")

	feed, err := store.ListOperations("ws_ops_list", "dead_lettered", "", "", "", 10)
	if err != nil {
		t.Fatalf("list operations failed: %v", err)
	}
	if len(feed.Items) == 0 {
		t.Fatalf("expected dead-lettered operation in list")
	}
	if feed.Items[0].Status != "dead_lettered" {
		t.Fatalf("expected dead_lettered status, got %s", feed.Items[0].Status)
	}
}

func TestListOperationsFiltersByAction(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)

	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_ops_action",
		Path:          "/notion/Action.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# action",
		CorrelationID: "corr_ops_action_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	waitForOpStatus(t, store, "ws_ops_action", write.OpID, "succeeded")

	file, err := store.ReadFile("ws_ops_action", "/notion/Action.md")
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	del, err := store.DeleteFile(DeleteRequest{
		WorkspaceID:   "ws_ops_action",
		Path:          "/notion/Action.md",
		IfMatch:       file.Revision,
		CorrelationID: "corr_ops_action_2",
	})
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	waitForOpStatus(t, store, "ws_ops_action", del.OpID, "succeeded")

	feed, err := store.ListOperations("ws_ops_action", "succeeded", "file_delete", "", "", 10)
	if err != nil {
		t.Fatalf("list operations failed: %v", err)
	}
	if len(feed.Items) != 1 {
		t.Fatalf("expected one file_delete operation, got %d", len(feed.Items))
	}
	if feed.Items[0].Action != "file_delete" {
		t.Fatalf("expected file_delete action, got %s", feed.Items[0].Action)
	}
}

func TestListOperationsFiltersByProvider(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		Adapters: []ProviderAdapter{
			testAdapter{
				provider: "custom",
				actions: []ApplyAction{
					{
						Type:             ActionFileUpsert,
						Path:             "/custom/ProviderFilter.md",
						Content:          "# custom",
						ContentType:      "text/markdown",
						ProviderObjectID: "custom_provider_filter_1",
					},
				},
			},
		},
	})
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_ops_provider_1",
		WorkspaceID:   "ws_ops_provider",
		Provider:      "custom",
		DeliveryID:    "delivery_ops_provider_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Payload:       map[string]any{"type": "custom.upsert"},
		CorrelationID: "corr_ops_provider_1",
	})
	if err != nil {
		t.Fatalf("custom ingest failed: %v", err)
	}
	waitForFileContent(t, store, "ws_ops_provider", "/custom/ProviderFilter.md", "# custom")

	customFile, err := store.ReadFile("ws_ops_provider", "/custom/ProviderFilter.md")
	if err != nil {
		t.Fatalf("read custom file failed: %v", err)
	}
	customWrite, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_ops_provider",
		Path:          "/custom/ProviderFilter.md",
		IfMatch:       customFile.Revision,
		ContentType:   "text/markdown",
		Content:       "# custom updated",
		CorrelationID: "corr_ops_provider_2",
	})
	if err != nil {
		t.Fatalf("custom write failed: %v", err)
	}
	waitForOpStatus(t, store, "ws_ops_provider", customWrite.OpID, "succeeded")

	notionWrite, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_ops_provider",
		Path:          "/notion/ProviderFilter.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# notion",
		CorrelationID: "corr_ops_provider_3",
	})
	if err != nil {
		t.Fatalf("notion write failed: %v", err)
	}
	waitForOpStatus(t, store, "ws_ops_provider", notionWrite.OpID, "succeeded")

	customFeed, err := store.ListOperations("ws_ops_provider", "succeeded", "", "custom", "", 20)
	if err != nil {
		t.Fatalf("list custom operations failed: %v", err)
	}
	if len(customFeed.Items) == 0 {
		t.Fatalf("expected custom operations in provider-filtered list")
	}
	for _, item := range customFeed.Items {
		if item.Provider != "custom" {
			t.Fatalf("expected only custom provider ops, got %s", item.Provider)
		}
	}

	notionFeed, err := store.ListOperations("ws_ops_provider", "succeeded", "", "notion", "", 20)
	if err != nil {
		t.Fatalf("list notion operations failed: %v", err)
	}
	if len(notionFeed.Items) == 0 {
		t.Fatalf("expected notion operations in provider-filtered list")
	}
	for _, item := range notionFeed.Items {
		if item.Provider != "notion" {
			t.Fatalf("expected only notion provider ops, got %s", item.Provider)
		}
	}
}

func TestListOperationsSortsByOperationSequenceDescending(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)

	for i := 0; i < 12; i++ {
		_, err := store.WriteFile(WriteRequest{
			WorkspaceID:   "ws_ops_sort",
			Path:          fmt.Sprintf("/notion/Sort-%02d.md", i),
			IfMatch:       "0",
			ContentType:   "text/markdown",
			Content:       fmt.Sprintf("# %d", i),
			CorrelationID: fmt.Sprintf("corr_ops_sort_%d", i),
		})
		if err != nil {
			t.Fatalf("write %d failed: %v", i, err)
		}
	}

	feed, err := store.ListOperations("ws_ops_sort", "", "", "", "", 5)
	if err != nil {
		t.Fatalf("list operations failed: %v", err)
	}
	if len(feed.Items) < 2 {
		t.Fatalf("expected at least 2 operations")
	}
	first := operationIDSeq(feed.Items[0].OpID)
	second := operationIDSeq(feed.Items[1].OpID)
	if first < second {
		t.Fatalf("expected descending operation sequence, got %s then %s", feed.Items[0].OpID, feed.Items[1].OpID)
	}
}

func TestListOperationsRejectsInvalidCursor(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)

	_, err := store.ListOperations("ws_invalid_cursor", "", "", "", "op_missing", 10)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected invalid input for unknown cursor, got %v", err)
	}
}

func TestStorePersistsStateAcrossRestart(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	store := NewStoreWithOptions(StoreOptions{StateFile: stateFile})
	t.Cleanup(store.Close)

	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_persist",
		Path:          "/notion/Engineering/Persist.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# persisted",
		CorrelationID: "corr_persist_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if write.OpID == "" {
		t.Fatalf("expected op id")
	}

	reloaded := NewStoreWithOptions(StoreOptions{StateFile: stateFile})
	t.Cleanup(reloaded.Close)
	file, err := reloaded.ReadFile("ws_persist", "/notion/Engineering/Persist.md")
	if err != nil {
		t.Fatalf("read after reload failed: %v", err)
	}
	if file.Content != "# persisted" {
		t.Fatalf("unexpected content after reload: %q", file.Content)
	}
}

func TestDeadLettersPersistAcrossRestart(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state-deadletters.json")
	writerStore := NewStoreWithOptions(StoreOptions{
		StateFile:           stateFile,
		MaxEnvelopeAttempts: 1,
		Adapters: []ProviderAdapter{
			testAdapter{
				provider: "custom",
				parseEnvelope: func(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
					return nil, fmt.Errorf("parse fail for persistence")
				},
			},
		},
	})
	t.Cleanup(writerStore.Close)

	_, err := writerStore.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_persist_dead_1",
		WorkspaceID:   "ws_persist_dead",
		Provider:      "custom",
		DeliveryID:    "delivery_persist_dead_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339),
		Payload:       map[string]any{"type": "custom.dead"},
		CorrelationID: "corr_persist_dead_1",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		feed, err := writerStore.ListDeadLetters("ws_persist_dead", "", "", 10)
		if err == nil && len(feed.Items) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	reloaded := NewStoreWithOptions(StoreOptions{
		StateFile:      stateFile,
		DisableWorkers: true,
	})
	t.Cleanup(reloaded.Close)

	feed, err := reloaded.ListDeadLetters("ws_persist_dead", "", "", 10)
	if err != nil {
		t.Fatalf("list dead letters after restart failed: %v", err)
	}
	if len(feed.Items) != 1 || feed.Items[0].EnvelopeID != "env_persist_dead_1" {
		t.Fatalf("expected persisted dead-letter envelope, got %+v", feed.Items)
	}
}

func TestStoreIngestEnvelopeAndReplayOp(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		MaxWritebackAttempts: 1,
		WritebackDelay:       5 * time.Millisecond,
		ProviderWrite: func(workspaceID, path, revision string) error {
			return fmt.Errorf("forced replay precondition failure")
		},
	})
	t.Cleanup(store.Close)
	receivedAt := time.Now().UTC().Format(time.RFC3339)
	resp, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_1",
		WorkspaceID:   "ws_env",
		Provider:      "notion",
		DeliveryID:    "delivery_1",
		ReceivedAt:    receivedAt,
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_env_1",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}
	if resp.Status != "queued" {
		t.Fatalf("expected queued status")
	}

	// Duplicate delivery should be idempotent and not fail.
	dup, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_2",
		WorkspaceID:   "ws_env",
		Provider:      "notion",
		DeliveryID:    "delivery_1",
		ReceivedAt:    receivedAt,
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_env_2",
	})
	if err != nil {
		t.Fatalf("duplicate ingest should not fail: %v", err)
	}
	if dup.Status != "queued" {
		t.Fatalf("expected queued duplicate status")
	}

	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_env",
		Path:          "/notion/X.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# x",
		CorrelationID: "corr_env_3",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	waitForOpStatus(t, store, "ws_env", write.OpID, "dead_lettered")

	replayed, err := store.ReplayOperation("ws_env", write.OpID, "corr_env_4")
	if err != nil {
		t.Fatalf("replay op failed: %v", err)
	}
	if replayed.Status != "queued" {
		t.Fatalf("expected queued replay status")
	}
}

func TestReplayOperationRequiresDeadLetteredState(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)

	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_replay_state",
		Path:          "/notion/ReplayState.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# replay-state",
		CorrelationID: "corr_replay_state_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	waitForOpStatus(t, store, "ws_replay_state", write.OpID, "succeeded")

	_, err = store.ReplayOperation("ws_replay_state", write.OpID, "corr_replay_state_2")
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expected invalid state for non-dead-letter op replay, got %v", err)
	}
}

func TestReplayOperationResetsAttemptCount(t *testing.T) {
	var shouldFail atomic.Bool
	shouldFail.Store(true)
	store := NewStoreWithOptions(StoreOptions{
		MaxWritebackAttempts: 1,
		WritebackDelay:       5 * time.Millisecond,
		ProviderWrite: func(workspaceID, path, revision string) error {
			if shouldFail.Load() {
				return fmt.Errorf("forced failure")
			}
			return nil
		},
	})
	t.Cleanup(store.Close)

	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_replay_attempts",
		Path:          "/notion/ReplayAttempts.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# replay-attempts",
		CorrelationID: "corr_replay_attempts_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	waitForOpStatus(t, store, "ws_replay_attempts", write.OpID, "dead_lettered")

	shouldFail.Store(false)
	_, err = store.ReplayOperation("ws_replay_attempts", write.OpID, "corr_replay_attempts_2")
	if err != nil {
		t.Fatalf("replay operation failed: %v", err)
	}
	waitForOpStatus(t, store, "ws_replay_attempts", write.OpID, "succeeded")

	op, err := store.GetOperation("ws_replay_attempts", write.OpID)
	if err != nil {
		t.Fatalf("get operation failed: %v", err)
	}
	if op.AttemptCount != 1 {
		t.Fatalf("expected attempt count to reset and finish at 1, got %d", op.AttemptCount)
	}
}

func TestReplayEnvelopeReprocessesProcessedEnvelope(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)
	receivedAt := time.Now().UTC().Format(time.RFC3339)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:  "env_replay_1",
		WorkspaceID: "ws_replay",
		Provider:    "notion",
		DeliveryID:  "delivery_replay_1",
		ReceivedAt:  receivedAt,
		Payload: map[string]any{
			"type":     "notion.page.upsert",
			"objectId": "obj_replay_1",
			"path":     "/notion/Replay.md",
			"content":  "# replay",
		},
		CorrelationID: "corr_replay_1",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}
	waitForFileContent(t, store, "ws_replay", "/notion/Replay.md", "# replay")

	initialFeed, err := store.GetEvents("ws_replay", "", "", 1000)
	if err != nil {
		t.Fatalf("events failed: %v", err)
	}
	initialCount := len(initialFeed.Events)

	_, err = store.ReplayEnvelope("env_replay_1", "corr_replay_2")
	if err != nil {
		t.Fatalf("replay envelope failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		feed, err := store.GetEvents("ws_replay", "", "", 1000)
		if err == nil && len(feed.Events) > initialCount {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	feed, err := store.GetEvents("ws_replay", "", "", 1000)
	if err != nil {
		t.Fatalf("events failed after replay: %v", err)
	}
	t.Fatalf("expected replay to increase event count, got %d -> %d", initialCount, len(feed.Events))
}

func TestEnvelopeParseRetriesThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	store := NewStoreWithOptions(StoreOptions{
		MaxEnvelopeAttempts: 5,
		EnvelopeRetryDelay:  5 * time.Millisecond,
		Adapters: []ProviderAdapter{
			testAdapter{
				provider: "custom",
				parseEnvelope: func(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
					n := attempts.Add(1)
					if n < 3 {
						return nil, fmt.Errorf("transient parse failure")
					}
					return []ApplyAction{
						{
							Type:             ActionFileUpsert,
							Path:             "/custom/Retry.md",
							Content:          "# recovered",
							ContentType:      "text/markdown",
							ProviderObjectID: "custom_retry_1",
						},
					}, nil
				},
			},
		},
	})
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_parse_retry_1",
		WorkspaceID:   "ws_parse_retry",
		Provider:      "custom",
		DeliveryID:    "delivery_parse_retry_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339),
		Payload:       map[string]any{"type": "custom.retry"},
		CorrelationID: "corr_parse_retry_1",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}

	waitForFileContent(t, store, "ws_parse_retry", "/custom/Retry.md", "# recovered")
	if attempts.Load() < 3 {
		t.Fatalf("expected at least 3 parse attempts, got %d", attempts.Load())
	}
}

func TestEnvelopeParseDeadLettersAfterMaxAttempts(t *testing.T) {
	var attempts atomic.Int32
	store := NewStoreWithOptions(StoreOptions{
		MaxEnvelopeAttempts: 2,
		EnvelopeRetryDelay:  5 * time.Millisecond,
		Adapters: []ProviderAdapter{
			testAdapter{
				provider: "custom",
				parseEnvelope: func(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
					attempts.Add(1)
					return nil, fmt.Errorf("permanent parse failure")
				},
			},
		},
	})
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_parse_dead_1",
		WorkspaceID:   "ws_parse_dead",
		Provider:      "custom",
		DeliveryID:    "delivery_parse_dead_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339),
		Payload:       map[string]any{"type": "custom.dead"},
		CorrelationID: "corr_parse_dead_1",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := store.GetIngressStatus("ws_parse_dead")
		if err == nil && status.PendingTotal == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, err := store.GetIngressStatus("ws_parse_dead")
	if err != nil {
		t.Fatalf("ingress status failed: %v", err)
	}
	if status.PendingTotal != 0 {
		t.Fatalf("expected no pending envelopes after dead-letter, got %d", status.PendingTotal)
	}
	if status.DeadLetterTotal != 1 {
		t.Fatalf("expected one dead-letter envelope, got %d", status.DeadLetterTotal)
	}
	if status.DeadLetterByProvider["custom"] != 1 {
		t.Fatalf("expected dead-letter breakdown for custom provider, got %+v", status.DeadLetterByProvider)
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected exactly 2 parse attempts, got %d", attempts.Load())
	}
	feed, err := store.GetEvents("ws_parse_dead", "", "", 1000)
	if err != nil {
		t.Fatalf("events failed: %v", err)
	}
	found := false
	for _, event := range feed.Events {
		if event.Type == "sync.error" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected sync.error event after envelope dead-letter")
	}
}

func TestListDeadLettersIncludesFailedEnvelope(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		MaxEnvelopeAttempts: 1,
		Adapters: []ProviderAdapter{
			testAdapter{
				provider: "custom",
				parseEnvelope: func(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
					return nil, fmt.Errorf("parse failed")
				},
			},
		},
	})
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_list_dead_1",
		WorkspaceID:   "ws_list_dead",
		Provider:      "custom",
		DeliveryID:    "delivery_list_dead_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339),
		Payload:       map[string]any{"type": "custom.dead"},
		CorrelationID: "corr_list_dead_1",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		feed, err := store.ListDeadLetters("ws_list_dead", "", "", 10)
		if err == nil && len(feed.Items) == 1 {
			item := feed.Items[0]
			if item.EnvelopeID != "env_list_dead_1" {
				t.Fatalf("unexpected envelope id: %s", item.EnvelopeID)
			}
			if item.AttemptCount != 1 {
				t.Fatalf("expected attempt count 1, got %d", item.AttemptCount)
			}
			if item.LastError == "" {
				t.Fatalf("expected last error to be populated")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected dead-letter item to be listed")
}

func TestGetDeadLetterByID(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		MaxEnvelopeAttempts: 1,
		Adapters: []ProviderAdapter{
			testAdapter{
				provider: "custom",
				parseEnvelope: func(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
					return nil, fmt.Errorf("parse failed")
				},
			},
		},
	})
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_get_dead_1",
		WorkspaceID:   "ws_get_dead",
		Provider:      "custom",
		DeliveryID:    "delivery_get_dead_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339),
		Payload:       map[string]any{"type": "custom.dead"},
		CorrelationID: "corr_get_dead_1",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		item, err := store.GetDeadLetter("ws_get_dead", "env_get_dead_1")
		if err == nil {
			if item.EnvelopeID != "env_get_dead_1" {
				t.Fatalf("unexpected envelope id: %s", item.EnvelopeID)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected dead-letter lookup to succeed")
}

func TestListDeadLettersCanFilterByProvider(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		MaxEnvelopeAttempts: 1,
		Adapters: []ProviderAdapter{
			testAdapter{
				provider: "custom",
				parseEnvelope: func(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
					return nil, fmt.Errorf("custom parse failed")
				},
			},
			testAdapter{
				provider: "custom2",
				parseEnvelope: func(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
					return nil, fmt.Errorf("custom2 parse failed")
				},
			},
		},
	})
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_provider_filter_1",
		WorkspaceID:   "ws_provider_filter",
		Provider:      "custom",
		DeliveryID:    "delivery_provider_filter_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339),
		Payload:       map[string]any{"type": "custom.dead"},
		CorrelationID: "corr_provider_filter_1",
	})
	if err != nil {
		t.Fatalf("ingest custom failed: %v", err)
	}
	_, err = store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_provider_filter_2",
		WorkspaceID:   "ws_provider_filter",
		Provider:      "custom2",
		DeliveryID:    "delivery_provider_filter_2",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339),
		Payload:       map[string]any{"type": "custom2.dead"},
		CorrelationID: "corr_provider_filter_2",
	})
	if err != nil {
		t.Fatalf("ingest custom2 failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		feed, err := store.ListDeadLetters("ws_provider_filter", "custom2", "", 10)
		if err == nil && len(feed.Items) == 1 {
			if feed.Items[0].Provider != "custom2" {
				t.Fatalf("expected provider custom2, got %s", feed.Items[0].Provider)
			}
			status, err := store.GetIngressStatus("ws_provider_filter")
			if err != nil {
				t.Fatalf("ingress status failed: %v", err)
			}
			if status.DeadLetterByProvider["custom"] != 1 || status.DeadLetterByProvider["custom2"] != 1 {
				t.Fatalf("unexpected dead-letter breakdown: %+v", status.DeadLetterByProvider)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected provider-filtered dead-letter list")
}

func TestAcknowledgeDeadLetterRemovesRecord(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		MaxEnvelopeAttempts: 1,
		Adapters: []ProviderAdapter{
			testAdapter{
				provider: "custom",
				parseEnvelope: func(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
					return nil, fmt.Errorf("parse failed")
				},
			},
		},
	})
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_ack_dead_1",
		WorkspaceID:   "ws_ack_dead",
		Provider:      "custom",
		DeliveryID:    "delivery_ack_dead_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339),
		Payload:       map[string]any{"type": "custom.dead"},
		CorrelationID: "corr_ack_dead_1",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		feed, err := store.ListDeadLetters("ws_ack_dead", "", "", 10)
		if err == nil && len(feed.Items) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	ack, err := store.AcknowledgeDeadLetter("ws_ack_dead", "env_ack_dead_1", "corr_ack_dead_2")
	if err != nil {
		t.Fatalf("acknowledge failed: %v", err)
	}
	if ack.Status != "acknowledged" || ack.ID != "env_ack_dead_1" {
		t.Fatalf("unexpected ack response: %+v", ack)
	}

	feed, err := store.ListDeadLetters("ws_ack_dead", "", "", 10)
	if err != nil {
		t.Fatalf("list dead letters failed: %v", err)
	}
	if len(feed.Items) != 0 {
		t.Fatalf("expected no dead letters after ack, got %d", len(feed.Items))
	}

	_, err = store.ReplayEnvelopeForWorkspace("ws_ack_dead", "env_ack_dead_1", "corr_ack_dead_3")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found replay after ack, got %v", err)
	}
}

func TestListDeadLettersRejectsInvalidCursor(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		MaxEnvelopeAttempts: 1,
		Adapters: []ProviderAdapter{
			testAdapter{
				provider: "custom",
				parseEnvelope: func(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
					return nil, fmt.Errorf("parse failed")
				},
			},
		},
	})
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_invalid_dead_cursor_1",
		WorkspaceID:   "ws_invalid_dead_cursor",
		Provider:      "custom",
		DeliveryID:    "delivery_invalid_dead_cursor_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339),
		Payload:       map[string]any{"type": "custom.dead"},
		CorrelationID: "corr_invalid_dead_cursor_1",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, err := store.GetDeadLetter("ws_invalid_dead_cursor", "env_invalid_dead_cursor_1")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	_, err = store.ListDeadLetters("ws_invalid_dead_cursor", "", "env_missing", 10)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected invalid input for unknown dead-letter cursor, got %v", err)
	}
}

func TestReplayEnvelopeClearsDeadLetterRecord(t *testing.T) {
	var allowSuccess atomic.Bool
	store := NewStoreWithOptions(StoreOptions{
		MaxEnvelopeAttempts: 1,
		Adapters: []ProviderAdapter{
			testAdapter{
				provider: "custom",
				parseEnvelope: func(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
					if !allowSuccess.Load() {
						return nil, fmt.Errorf("blocked")
					}
					return []ApplyAction{
						{
							Type:        ActionFileUpsert,
							Path:        "/custom/Replayed.md",
							Content:     "# replayed",
							ContentType: "text/markdown",
						},
					}, nil
				},
			},
		},
	})
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_replay_dead_1",
		WorkspaceID:   "ws_replay_dead",
		Provider:      "custom",
		DeliveryID:    "delivery_replay_dead_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339),
		Payload:       map[string]any{"type": "custom.replay"},
		CorrelationID: "corr_replay_dead_1",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		feed, err := store.ListDeadLetters("ws_replay_dead", "", "", 10)
		if err == nil && len(feed.Items) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	allowSuccess.Store(true)
	_, err = store.ReplayEnvelope("env_replay_dead_1", "corr_replay_dead_2")
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}
	waitForFileContent(t, store, "ws_replay_dead", "/custom/Replayed.md", "# replayed")

	feed, err := store.ListDeadLetters("ws_replay_dead", "", "", 10)
	if err != nil {
		t.Fatalf("list dead letters failed: %v", err)
	}
	if len(feed.Items) != 0 {
		t.Fatalf("expected dead-letter list to be cleared after successful replay, got %d items", len(feed.Items))
	}
	status, err := store.GetIngressStatus("ws_replay_dead")
	if err != nil {
		t.Fatalf("get ingress status failed: %v", err)
	}
	if status.DeadLetterTotal != 0 {
		t.Fatalf("expected zero dead letters after replay, got %d", status.DeadLetterTotal)
	}
	if len(status.DeadLetterByProvider) != 0 {
		t.Fatalf("expected empty dead-letter breakdown after replay, got %+v", status.DeadLetterByProvider)
	}
}

func TestReplayEnvelopeForWorkspaceRejectsWorkspaceMismatch(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_ws_scope_1",
		WorkspaceID:   "ws_scope_a",
		Provider:      "notion",
		DeliveryID:    "delivery_ws_scope_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339),
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_ws_scope_1",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}

	_, err = store.ReplayEnvelopeForWorkspace("ws_scope_b", "env_ws_scope_1", "corr_ws_scope_2")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found for workspace mismatch, got %v", err)
	}
}

func TestReplayEnvelopeForWorkspaceRejectsNonDeadLetterEnvelope(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_ws_scope_2",
		WorkspaceID:   "ws_scope_a",
		Provider:      "notion",
		DeliveryID:    "delivery_ws_scope_2",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339),
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_ws_scope_3",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}

	_, err = store.ReplayEnvelopeForWorkspace("ws_scope_a", "env_ws_scope_2", "corr_ws_scope_4")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found for non-dead-letter envelope, got %v", err)
	}
}

func TestEnvelopePipelineAppliesNotionUpsertMoveDelete(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)
	receivedAt := time.Now().UTC().Format(time.RFC3339)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:  "env_upsert_1",
		WorkspaceID: "ws_pipe",
		Provider:    "notion",
		DeliveryID:  "delivery_pipe_1",
		ReceivedAt:  receivedAt,
		Payload: map[string]any{
			"type":     "notion.page.upsert",
			"objectId": "notion_obj_1",
			"path":     "/notion/Engineering/FromWebhook.md",
			"title":    "FromWebhook",
			"content":  "# from webhook",
		},
		CorrelationID: "corr_pipe_1",
	})
	if err != nil {
		t.Fatalf("ingest upsert failed: %v", err)
	}
	waitForFileContent(t, store, "ws_pipe", "/notion/Engineering/FromWebhook.md", "# from webhook")

	_, err = store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:  "env_upsert_2",
		WorkspaceID: "ws_pipe",
		Provider:    "notion",
		DeliveryID:  "delivery_pipe_2",
		ReceivedAt:  receivedAt,
		Payload: map[string]any{
			"type":     "notion.page.upsert",
			"objectId": "notion_obj_1",
			"path":     "/notion/Engineering/Moved.md",
			"title":    "Moved",
			"content":  "# moved",
		},
		CorrelationID: "corr_pipe_2",
	})
	if err != nil {
		t.Fatalf("ingest move failed: %v", err)
	}
	waitForFileContent(t, store, "ws_pipe", "/notion/Engineering/Moved.md", "# moved")
	waitForNotFound(t, store, "ws_pipe", "/notion/Engineering/FromWebhook.md")

	_, err = store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:  "env_delete_1",
		WorkspaceID: "ws_pipe",
		Provider:    "notion",
		DeliveryID:  "delivery_pipe_3",
		ReceivedAt:  receivedAt,
		Payload: map[string]any{
			"type":     "notion.page.deleted",
			"objectId": "notion_obj_1",
		},
		CorrelationID: "corr_pipe_3",
	})
	if err != nil {
		t.Fatalf("ingest delete failed: %v", err)
	}
	waitForNotFound(t, store, "ws_pipe", "/notion/Engineering/Moved.md")
}

func TestEnvelopeStalenessSkipsOlderUpsertForSameObject(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)

	now := time.Now().UTC()
	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:  "env_stale_upsert_new",
		WorkspaceID: "ws_stale_upsert",
		Provider:    "notion",
		DeliveryID:  "delivery_stale_upsert_new",
		ReceivedAt:  now.Format(time.RFC3339Nano),
		Payload: map[string]any{
			"type":     "notion.page.upsert",
			"objectId": "notion_stale_upsert_1",
			"path":     "/notion/Stale.md",
			"content":  "# newer",
		},
		CorrelationID: "corr_stale_upsert_new",
	})
	if err != nil {
		t.Fatalf("ingest newer envelope failed: %v", err)
	}
	waitForFileContent(t, store, "ws_stale_upsert", "/notion/Stale.md", "# newer")

	_, err = store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:  "env_stale_upsert_old",
		WorkspaceID: "ws_stale_upsert",
		Provider:    "notion",
		DeliveryID:  "delivery_stale_upsert_old",
		ReceivedAt:  now.Add(-1 * time.Minute).Format(time.RFC3339Nano),
		Payload: map[string]any{
			"type":     "notion.page.upsert",
			"objectId": "notion_stale_upsert_1",
			"path":     "/notion/Stale.md",
			"content":  "# older",
		},
		CorrelationID: "corr_stale_upsert_old",
	})
	if err != nil {
		t.Fatalf("ingest older envelope failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		feed, err := store.GetEvents("ws_stale_upsert", "", "", 1000)
		if err != nil {
			t.Fatalf("get events failed: %v", err)
		}
		found := false
		for _, event := range feed.Events {
			if event.Type == "sync.stale" && event.CorrelationID == "corr_stale_upsert_old" {
				found = true
				break
			}
		}
		if found {
			file, err := store.ReadFile("ws_stale_upsert", "/notion/Stale.md")
			if err != nil {
				t.Fatalf("read file failed: %v", err)
			}
			if file.Content != "# newer" {
				t.Fatalf("expected stale upsert to be ignored, got %q", file.Content)
			}
			status, err := store.GetIngressStatus("ws_stale_upsert")
			if err != nil {
				t.Fatalf("get ingress status failed: %v", err)
			}
			if status.StaleTotal < 1 {
				t.Fatalf("expected stale total >= 1, got %d", status.StaleTotal)
			}
			notionIngress, ok := status.IngressByProvider["notion"]
			if !ok || notionIngress.StaleTotal < 1 {
				t.Fatalf("expected notion stale breakdown, got %+v", status.IngressByProvider)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected sync.stale event for older upsert")
}

func TestEnvelopeStalenessSkipsOlderDeleteForSameObject(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)

	now := time.Now().UTC()
	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:  "env_stale_delete_new",
		WorkspaceID: "ws_stale_delete",
		Provider:    "notion",
		DeliveryID:  "delivery_stale_delete_new",
		ReceivedAt:  now.Format(time.RFC3339Nano),
		Payload: map[string]any{
			"type":     "notion.page.upsert",
			"objectId": "notion_stale_delete_1",
			"path":     "/notion/StaleDelete.md",
			"content":  "# alive",
		},
		CorrelationID: "corr_stale_delete_new",
	})
	if err != nil {
		t.Fatalf("ingest newer envelope failed: %v", err)
	}
	waitForFileContent(t, store, "ws_stale_delete", "/notion/StaleDelete.md", "# alive")

	_, err = store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:  "env_stale_delete_old",
		WorkspaceID: "ws_stale_delete",
		Provider:    "notion",
		DeliveryID:  "delivery_stale_delete_old",
		ReceivedAt:  now.Add(-1 * time.Minute).Format(time.RFC3339Nano),
		Payload: map[string]any{
			"type":     "notion.page.deleted",
			"objectId": "notion_stale_delete_1",
		},
		CorrelationID: "corr_stale_delete_old",
	})
	if err != nil {
		t.Fatalf("ingest older delete envelope failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		feed, err := store.GetEvents("ws_stale_delete", "", "", 1000)
		if err != nil {
			t.Fatalf("get events failed: %v", err)
		}
		found := false
		for _, event := range feed.Events {
			if event.Type == "sync.stale" && event.CorrelationID == "corr_stale_delete_old" {
				found = true
				break
			}
		}
		if found {
			file, err := store.ReadFile("ws_stale_delete", "/notion/StaleDelete.md")
			if err != nil {
				t.Fatalf("read file failed: %v", err)
			}
			if file.Content != "# alive" {
				t.Fatalf("expected stale delete to be ignored, got %q", file.Content)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected sync.stale event for older delete")
}

func TestEnvelopeDuplicateDeliveryDoesNotDoubleApply(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)
	receivedAt := time.Now().UTC().Format(time.RFC3339)
	envelope := WebhookEnvelopeRequest{
		EnvelopeID:  "env_dup_1",
		WorkspaceID: "ws_dup",
		Provider:    "notion",
		DeliveryID:  "delivery_dup_1",
		ReceivedAt:  receivedAt,
		Payload: map[string]any{
			"type":     "notion.page.upsert",
			"objectId": "notion_dup",
			"path":     "/notion/Dup.md",
			"content":  "# dup",
		},
		CorrelationID: "corr_dup_1",
	}
	_, err := store.IngestEnvelope(envelope)
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}
	waitForFileContent(t, store, "ws_dup", "/notion/Dup.md", "# dup")
	initialFeed, err := store.GetEvents("ws_dup", "", "", 1000)
	if err != nil {
		t.Fatalf("events failed: %v", err)
	}

	// Same delivery ID should be deduped.
	envelope.EnvelopeID = "env_dup_2"
	envelope.CorrelationID = "corr_dup_2"
	_, err = store.IngestEnvelope(envelope)
	if err != nil {
		t.Fatalf("duplicate ingest failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	nextFeed, err := store.GetEvents("ws_dup", "", "", 1000)
	if err != nil {
		t.Fatalf("events failed: %v", err)
	}
	if len(nextFeed.Events) != len(initialFeed.Events) {
		t.Fatalf("expected dedupe to keep event count stable, got %d -> %d", len(initialFeed.Events), len(nextFeed.Events))
	}
}

func TestEnvelopeCoalescingMergesLatestPayloadWithinWindow(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state-coalesce.json")
	writerStore := NewStoreWithOptions(StoreOptions{
		StateFile:         stateFile,
		DisableWorkers:    true,
		EnvelopeQueueSize: 4,
		CoalesceWindow:    2 * time.Second,
	})
	t.Cleanup(writerStore.Close)

	receivedAt := time.Now().UTC()
	_, err := writerStore.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:  "env_coalesce_1",
		WorkspaceID: "ws_coalesce",
		Provider:    "notion",
		DeliveryID:  "delivery_coalesce_1",
		ReceivedAt:  receivedAt.Format(time.RFC3339Nano),
		Payload: map[string]any{
			"type":     "notion.page.upsert",
			"objectId": "notion_coalesce_1",
			"path":     "/notion/Coalesced.md",
			"content":  "# first",
		},
		CorrelationID: "corr_coalesce_1",
	})
	if err != nil {
		t.Fatalf("first ingest failed: %v", err)
	}

	_, err = writerStore.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:  "env_coalesce_2",
		WorkspaceID: "ws_coalesce",
		Provider:    "notion",
		DeliveryID:  "delivery_coalesce_2",
		ReceivedAt:  receivedAt.Add(500 * time.Millisecond).Format(time.RFC3339Nano),
		Payload: map[string]any{
			"type":     "notion.page.upsert",
			"objectId": "notion_coalesce_1",
			"path":     "/notion/Coalesced.md",
			"content":  "# second",
		},
		CorrelationID: "corr_coalesce_2",
	})
	if err != nil {
		t.Fatalf("second ingest failed: %v", err)
	}

	status, err := writerStore.GetIngressStatus("ws_coalesce")
	if err != nil {
		t.Fatalf("ingress status failed: %v", err)
	}
	if status.AcceptedTotal != 1 || status.CoalescedTotal != 1 || status.PendingTotal != 1 {
		t.Fatalf("unexpected coalescing counters: %+v", status)
	}
	if status.DedupeRate != 0 {
		t.Fatalf("expected dedupe rate 0, got %f", status.DedupeRate)
	}
	if status.CoalesceRate != 0.5 {
		t.Fatalf("expected coalesce rate 0.5, got %f", status.CoalesceRate)
	}
	notionIngress, ok := status.IngressByProvider["notion"]
	if !ok || notionIngress.AcceptedTotal != 1 || notionIngress.CoalescedTotal != 1 || notionIngress.PendingTotal != 1 {
		t.Fatalf("expected notion ingress coalescing breakdown, got %+v", status.IngressByProvider)
	}

	writerStore.Close()

	reloaded := NewStoreWithOptions(StoreOptions{StateFile: stateFile})
	t.Cleanup(reloaded.Close)
	waitForFileContent(t, reloaded, "ws_coalesce", "/notion/Coalesced.md", "# second")
}

func TestEnvelopeCoalescingRespectsWindow(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers:    true,
		EnvelopeQueueSize: 4,
		CoalesceWindow:    20 * time.Millisecond,
	})
	t.Cleanup(store.Close)

	receivedAt := time.Now().UTC()
	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:  "env_coalesce_window_1",
		WorkspaceID: "ws_coalesce_window",
		Provider:    "notion",
		DeliveryID:  "delivery_coalesce_window_1",
		ReceivedAt:  receivedAt.Format(time.RFC3339Nano),
		Payload: map[string]any{
			"type":     "notion.page.upsert",
			"objectId": "notion_coalesce_window_1",
			"path":     "/notion/Window.md",
			"content":  "# first",
		},
		CorrelationID: "corr_coalesce_window_1",
	})
	if err != nil {
		t.Fatalf("first ingest failed: %v", err)
	}

	_, err = store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:  "env_coalesce_window_2",
		WorkspaceID: "ws_coalesce_window",
		Provider:    "notion",
		DeliveryID:  "delivery_coalesce_window_2",
		ReceivedAt:  receivedAt.Add(1 * time.Second).Format(time.RFC3339Nano),
		Payload: map[string]any{
			"type":     "notion.page.upsert",
			"objectId": "notion_coalesce_window_1",
			"path":     "/notion/Window.md",
			"content":  "# second",
		},
		CorrelationID: "corr_coalesce_window_2",
	})
	if err != nil {
		t.Fatalf("second ingest failed: %v", err)
	}

	status, err := store.GetIngressStatus("ws_coalesce_window")
	if err != nil {
		t.Fatalf("ingress status failed: %v", err)
	}
	if status.AcceptedTotal != 2 || status.CoalescedTotal != 0 || status.PendingTotal != 2 {
		t.Fatalf("expected no coalescing outside window, got %+v", status)
	}
}

func TestWebhookBurstCoalescingKeepsSinglePendingEnvelope(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers:    true,
		EnvelopeQueueSize: 16,
		CoalesceWindow:    5 * time.Second,
	})
	t.Cleanup(store.Close)

	receivedAt := time.Now().UTC()
	const burstSize = 200
	for i := 0; i < burstSize; i++ {
		_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
			EnvelopeID:  fmt.Sprintf("env_burst_%d", i),
			WorkspaceID: "ws_burst",
			Provider:    "notion",
			DeliveryID:  fmt.Sprintf("delivery_burst_%d", i),
			ReceivedAt:  receivedAt.Add(time.Duration(i) * 10 * time.Millisecond).Format(time.RFC3339Nano),
			Payload: map[string]any{
				"type":     "notion.page.upsert",
				"objectId": "notion_burst_1",
				"path":     "/notion/Burst.md",
				"content":  fmt.Sprintf("# burst %d", i),
			},
			CorrelationID: fmt.Sprintf("corr_burst_%d", i),
		})
		if err != nil {
			t.Fatalf("burst ingest %d failed: %v", i, err)
		}
	}

	status, err := store.GetIngressStatus("ws_burst")
	if err != nil {
		t.Fatalf("ingress status failed: %v", err)
	}
	if status.AcceptedTotal != 1 || status.CoalescedTotal != burstSize-1 || status.PendingTotal != 1 {
		t.Fatalf("unexpected burst coalescing counters: %+v", status)
	}
	if status.QueueDepth != 1 {
		t.Fatalf("expected queue depth to remain 1 for burst coalescing, got %d", status.QueueDepth)
	}
	if status.CoalesceRate < 0.99 {
		t.Fatalf("expected high coalesce rate for burst coalescing, got %f", status.CoalesceRate)
	}
	notionIngress, ok := status.IngressByProvider["notion"]
	if !ok || notionIngress.AcceptedTotal != 1 || notionIngress.CoalescedTotal != burstSize-1 || notionIngress.PendingTotal != 1 {
		t.Fatalf("unexpected provider burst coalescing breakdown: %+v", status.IngressByProvider)
	}
}

func TestRebuildCoalesceIndexPrefersLatestPendingEnvelope(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers: true,
	})
	t.Cleanup(store.Close)

	oldReq := WebhookEnvelopeRequest{
		EnvelopeID:  "env_rebuild_old",
		WorkspaceID: "ws_rebuild",
		Provider:    "notion",
		DeliveryID:  "delivery_rebuild_old",
		ReceivedAt:  "2026-01-01T10:00:00Z",
		Payload: map[string]any{
			"type":     "notion.page.upsert",
			"objectId": "notion_rebuild_1",
			"path":     "/notion/Rebuild.md",
			"content":  "# old",
		},
	}
	newReq := WebhookEnvelopeRequest{
		EnvelopeID:  "env_rebuild_new",
		WorkspaceID: "ws_rebuild",
		Provider:    "notion",
		DeliveryID:  "delivery_rebuild_new",
		ReceivedAt:  "2026-01-01T10:00:01Z",
		Payload: map[string]any{
			"type":     "notion.page.upsert",
			"objectId": "notion_rebuild_1",
			"path":     "/notion/Rebuild.md",
			"content":  "# new",
		},
	}
	coalesceKey := coalesceObjectKey(oldReq)
	if coalesceKey == "" {
		t.Fatalf("expected coalesce key for test envelope")
	}

	store.mu.Lock()
	store.envelopesByID[oldReq.EnvelopeID] = oldReq
	store.envelopesByID[newReq.EnvelopeID] = newReq
	store.processedEnvs[oldReq.EnvelopeID] = false
	store.processedEnvs[newReq.EnvelopeID] = false
	store.coalesceIndex = map[string]string{
		coalesceKey: oldReq.EnvelopeID,
	}
	store.rebuildCoalesceIndexLocked()
	selected := store.coalesceIndex[coalesceKey]
	store.mu.Unlock()

	if selected != newReq.EnvelopeID {
		t.Fatalf("expected rebuild to select latest pending envelope %q, got %q", newReq.EnvelopeID, selected)
	}
}

func TestProcessedEnvelopeRetentionPrunesOldestProcessed(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		MaxStoredEnvelopes: 2,
	})
	t.Cleanup(store.Close)

	for i := 1; i <= 3; i++ {
		_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
			EnvelopeID:    fmt.Sprintf("env_retention_%d", i),
			WorkspaceID:   "ws_retention",
			Provider:      "notion",
			DeliveryID:    fmt.Sprintf("delivery_retention_%d", i),
			ReceivedAt:    time.Now().UTC().Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			Payload:       map[string]any{"type": "sync"},
			CorrelationID: fmt.Sprintf("corr_retention_%d", i),
		})
		if err != nil {
			t.Fatalf("ingest %d failed: %v", i, err)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := store.GetIngressStatus("ws_retention")
		if err == nil && status.PendingTotal == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	_, err := store.ReplayEnvelope("env_retention_1", "corr_retention_replay_old")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected oldest processed envelope to be pruned, got %v", err)
	}
	_, err = store.ReplayEnvelope("env_retention_3", "corr_retention_replay_new")
	if err != nil {
		t.Fatalf("expected newest envelope to remain replayable, got %v", err)
	}
}

func TestEnvelopeRetentionDoesNotPruneDeadLetters(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		MaxStoredEnvelopes:  1,
		MaxEnvelopeAttempts: 1,
		Adapters: []ProviderAdapter{
			testAdapter{
				provider: "custom",
				parseEnvelope: func(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
					return nil, fmt.Errorf("dead-letter candidate")
				},
			},
		},
	})
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_retention_dead_1",
		WorkspaceID:   "ws_retention_dead",
		Provider:      "custom",
		DeliveryID:    "delivery_retention_dead_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Payload:       map[string]any{"type": "custom.dead"},
		CorrelationID: "corr_retention_dead_1",
	})
	if err != nil {
		t.Fatalf("ingest dead-letter envelope failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := store.GetDeadLetter("ws_retention_dead", "env_retention_dead_1"); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	_, err = store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_retention_ok_1",
		WorkspaceID:   "ws_retention_dead",
		Provider:      "notion",
		DeliveryID:    "delivery_retention_ok_1",
		ReceivedAt:    time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano),
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_retention_ok_1",
	})
	if err != nil {
		t.Fatalf("ingest processed envelope failed: %v", err)
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := store.GetIngressStatus("ws_retention_dead")
		if err == nil && status.PendingTotal == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, err := store.GetDeadLetter("ws_retention_dead", "env_retention_dead_1"); err != nil {
		t.Fatalf("expected dead-letter envelope to be retained, got %v", err)
	}
}

func TestEnvelopeQueueBackpressureAndIngressStatus(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers:    true,
		EnvelopeQueueSize: 1,
	})
	t.Cleanup(store.Close)
	receivedAt := time.Now().UTC().Format(time.RFC3339)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_backpressure_1",
		WorkspaceID:   "ws_backpressure",
		Provider:      "notion",
		DeliveryID:    "delivery_backpressure_1",
		ReceivedAt:    receivedAt,
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_backpressure_1",
	})
	if err != nil {
		t.Fatalf("first ingest should succeed: %v", err)
	}

	_, err = store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_backpressure_2",
		WorkspaceID:   "ws_backpressure",
		Provider:      "notion",
		DeliveryID:    "delivery_backpressure_2",
		ReceivedAt:    receivedAt,
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_backpressure_2",
	})
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected queue full error, got %v", err)
	}

	status, err := store.GetIngressStatus("ws_backpressure")
	if err != nil {
		t.Fatalf("ingress status should be available: %v", err)
	}
	if status.QueueCapacity != 1 || status.QueueDepth != 1 {
		t.Fatalf("expected queue depth/capacity 1/1, got %d/%d", status.QueueDepth, status.QueueCapacity)
	}
	if status.QueueUtilization != 1 {
		t.Fatalf("expected queue utilization 1, got %f", status.QueueUtilization)
	}
	if status.PendingTotal != 1 {
		t.Fatalf("expected one pending envelope, got %d", status.PendingTotal)
	}
	if status.AcceptedTotal != 1 || status.DroppedTotal != 1 || status.DedupedTotal != 0 {
		t.Fatalf("unexpected ingress counters: %+v", status)
	}
	if status.DedupeRate != 0 || status.CoalesceRate != 0 {
		t.Fatalf("expected zero dedupe/coalesce rates, got dedupe=%f coalesce=%f", status.DedupeRate, status.CoalesceRate)
	}
	notionIngress, ok := status.IngressByProvider["notion"]
	if !ok || notionIngress.AcceptedTotal != 1 || notionIngress.DroppedTotal != 1 || notionIngress.PendingTotal != 1 {
		t.Fatalf("expected notion ingress backpressure breakdown, got %+v", status.IngressByProvider)
	}
}

func TestGetIngressStatusForProviderFiltersWorkspaceMetrics(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers:    true,
		EnvelopeQueueSize: 4,
	})
	t.Cleanup(store.Close)
	receivedAt := time.Now().UTC().Format(time.RFC3339Nano)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_ingress_provider_1",
		WorkspaceID:   "ws_ingress_provider",
		Provider:      "notion",
		DeliveryID:    "delivery_ingress_provider_1",
		ReceivedAt:    receivedAt,
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_ingress_provider_1",
	})
	if err != nil {
		t.Fatalf("ingest notion failed: %v", err)
	}
	_, err = store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_ingress_provider_2",
		WorkspaceID:   "ws_ingress_provider",
		Provider:      "custom",
		DeliveryID:    "delivery_ingress_provider_2",
		ReceivedAt:    receivedAt,
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_ingress_provider_2",
	})
	if err != nil {
		t.Fatalf("ingest custom failed: %v", err)
	}

	full, err := store.GetIngressStatus("ws_ingress_provider")
	if err != nil {
		t.Fatalf("workspace ingress status failed: %v", err)
	}
	if full.PendingTotal != 2 {
		t.Fatalf("expected workspace pending=2, got %d", full.PendingTotal)
	}

	filtered, err := store.GetIngressStatusForProvider("ws_ingress_provider", "custom")
	if err != nil {
		t.Fatalf("provider ingress status failed: %v", err)
	}
	if filtered.PendingTotal != 1 || filtered.AcceptedTotal != 1 || filtered.QueueDepth != 1 {
		t.Fatalf("unexpected filtered ingress status: %+v", filtered)
	}
	if len(filtered.IngressByProvider) != 1 {
		t.Fatalf("expected single provider entry in filtered status, got %+v", filtered.IngressByProvider)
	}
	customIngress, ok := filtered.IngressByProvider["custom"]
	if !ok || customIngress.PendingTotal != 1 || customIngress.AcceptedTotal != 1 {
		t.Fatalf("expected custom provider metrics in filtered status, got %+v", filtered.IngressByProvider)
	}
}

func TestListIngressStatusesAggregatesWorkspaces(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers:    true,
		EnvelopeQueueSize: 8,
	})
	t.Cleanup(store.Close)

	receivedAt := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_ingress_list_1",
		WorkspaceID:   "ws_ingress_list_1",
		Provider:      "notion",
		DeliveryID:    "delivery_ingress_list_1",
		ReceivedAt:    receivedAt,
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_ingress_list_1",
	})
	if err != nil {
		t.Fatalf("ingest workspace 1 failed: %v", err)
	}
	_, err = store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_ingress_list_2",
		WorkspaceID:   "ws_ingress_list_2",
		Provider:      "custom",
		DeliveryID:    "delivery_ingress_list_2",
		ReceivedAt:    receivedAt,
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_ingress_list_2",
	})
	if err != nil {
		t.Fatalf("ingest workspace 2 failed: %v", err)
	}

	statuses := store.ListIngressStatuses()
	if len(statuses) < 2 {
		t.Fatalf("expected at least 2 workspace statuses, got %d", len(statuses))
	}
	ws1, ok := statuses["ws_ingress_list_1"]
	if !ok {
		t.Fatalf("expected status for ws_ingress_list_1")
	}
	if ws1.AcceptedTotal != 1 || ws1.PendingTotal != 1 {
		t.Fatalf("unexpected status for ws_ingress_list_1: %+v", ws1)
	}
	ws2, ok := statuses["ws_ingress_list_2"]
	if !ok {
		t.Fatalf("expected status for ws_ingress_list_2")
	}
	if ws2.AcceptedTotal != 1 || ws2.PendingTotal != 1 {
		t.Fatalf("unexpected status for ws_ingress_list_2: %+v", ws2)
	}
}

func TestEnvelopeDedupedIngressStatusCounter(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers:    true,
		EnvelopeQueueSize: 2,
	})
	t.Cleanup(store.Close)
	receivedAt := time.Now().UTC().Format(time.RFC3339)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_dedupe_1",
		WorkspaceID:   "ws_dedupe_status",
		Provider:      "notion",
		DeliveryID:    "delivery_dedupe_1",
		ReceivedAt:    receivedAt,
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_dedupe_1",
	})
	if err != nil {
		t.Fatalf("first ingest failed: %v", err)
	}

	_, err = store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_dedupe_2",
		WorkspaceID:   "ws_dedupe_status",
		Provider:      "notion",
		DeliveryID:    "delivery_dedupe_1",
		ReceivedAt:    receivedAt,
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_dedupe_2",
	})
	if err != nil {
		t.Fatalf("deduped ingest should succeed: %v", err)
	}

	status, err := store.GetIngressStatus("ws_dedupe_status")
	if err != nil {
		t.Fatalf("ingress status should be available: %v", err)
	}
	if status.AcceptedTotal != 1 || status.DedupedTotal != 1 || status.DroppedTotal != 0 {
		t.Fatalf("unexpected ingress counters: %+v", status)
	}
	if status.QueueDepth != 1 || status.PendingTotal != 1 {
		t.Fatalf("expected one queued pending envelope, got queueDepth=%d pending=%d", status.QueueDepth, status.PendingTotal)
	}
	if status.DedupeRate != 0.5 {
		t.Fatalf("expected dedupe rate 0.5, got %f", status.DedupeRate)
	}
	if status.CoalesceRate != 0 {
		t.Fatalf("expected coalesce rate 0, got %f", status.CoalesceRate)
	}
	notionIngress, ok := status.IngressByProvider["notion"]
	if !ok || notionIngress.AcceptedTotal != 1 || notionIngress.DedupedTotal != 1 || notionIngress.PendingTotal != 1 {
		t.Fatalf("expected notion ingress dedupe breakdown, got %+v", status.IngressByProvider)
	}
	if status.QueueUtilization != 0.5 {
		t.Fatalf("expected queue utilization 0.5, got %f", status.QueueUtilization)
	}
}

func TestIngressStatusReportsOldestPendingAge(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		DisableWorkers:    true,
		EnvelopeQueueSize: 4,
	})
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_age_1",
		WorkspaceID:   "ws_age",
		Provider:      "notion",
		DeliveryID:    "delivery_age_1",
		ReceivedAt:    time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339),
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_age_1",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}

	status, err := store.GetIngressStatus("ws_age")
	if err != nil {
		t.Fatalf("ingress status failed: %v", err)
	}
	if status.OldestPendingAgeSeconds < 100 {
		t.Fatalf("expected oldest pending age >=100s, got %d", status.OldestPendingAgeSeconds)
	}
	notionIngress, ok := status.IngressByProvider["notion"]
	if !ok || notionIngress.PendingTotal != 1 {
		t.Fatalf("expected notion pending backlog breakdown, got %+v", status.IngressByProvider)
	}
	if notionIngress.OldestPendingAgeSeconds < 100 {
		t.Fatalf("expected notion oldest pending age >=100s, got %d", notionIngress.OldestPendingAgeSeconds)
	}
}

func TestProviderWriteActionReceivesFileUpsertPayload(t *testing.T) {
	actions := make(chan WritebackAction, 2)
	store := NewStoreWithOptions(StoreOptions{
		ProviderWriteAction: func(action WritebackAction) error {
			actions <- action
			return nil
		},
	})
	t.Cleanup(store.Close)

	_, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_write_action",
		Path:          "/notion/WriteAction.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# write action",
		CorrelationID: "corr_write_action_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}

	select {
	case action := <-actions:
		if action.Type != WritebackActionFileUpsert {
			t.Fatalf("expected upsert action, got %s", action.Type)
		}
		if action.Path != "/notion/WriteAction.md" {
			t.Fatalf("unexpected path: %s", action.Path)
		}
		if action.Content != "# write action" {
			t.Fatalf("unexpected content: %q", action.Content)
		}
		if action.ContentType != "text/markdown" {
			t.Fatalf("unexpected content type: %s", action.ContentType)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected provider write action callback for upsert")
	}
}

func TestProviderWriteActionReceivesFileDeletePayload(t *testing.T) {
	actions := make(chan WritebackAction, 10)
	store := NewStoreWithOptions(StoreOptions{
		ProviderWriteAction: func(action WritebackAction) error {
			actions <- action
			return nil
		},
	})
	t.Cleanup(store.Close)

	_, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_write_delete",
		Path:          "/notion/DeleteAction.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# delete action",
		CorrelationID: "corr_write_delete_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}

	file, err := store.ReadFile("ws_write_delete", "/notion/DeleteAction.md")
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	_, err = store.DeleteFile(DeleteRequest{
		WorkspaceID:   "ws_write_delete",
		Path:          "/notion/DeleteAction.md",
		IfMatch:       file.Revision,
		CorrelationID: "corr_write_delete_2",
	})
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case action := <-actions:
			if action.Type != WritebackActionFileDelete {
				continue
			}
			if action.Path != "/notion/DeleteAction.md" {
				t.Fatalf("unexpected delete path: %s", action.Path)
			}
			return
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Fatalf("expected provider write action callback for delete")
}

func TestWritebackRetriesThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	store := NewStoreWithOptions(StoreOptions{
		MaxWritebackAttempts: 5,
		WritebackDelay:       5 * time.Millisecond,
		ProviderWrite: func(workspaceID, path, revision string) error {
			n := attempts.Add(1)
			if n < 3 {
				return fmt.Errorf("transient provider error")
			}
			return nil
		},
	})
	t.Cleanup(store.Close)

	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_retry",
		Path:          "/notion/Retry.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# retry",
		CorrelationID: "corr_retry_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}

	waitForOpStatus(t, store, "ws_retry", write.OpID, "succeeded")
	op, err := store.GetOperation("ws_retry", write.OpID)
	if err != nil {
		t.Fatalf("get op failed: %v", err)
	}
	if op.AttemptCount < 3 {
		t.Fatalf("expected retries before success, attempts=%d", op.AttemptCount)
	}
}

func TestWritebackDeadLetterAfterMaxAttempts(t *testing.T) {
	var attempts atomic.Int32
	store := NewStoreWithOptions(StoreOptions{
		MaxWritebackAttempts: 2,
		WritebackDelay:       5 * time.Millisecond,
		ProviderWrite: func(workspaceID, path, revision string) error {
			attempts.Add(1)
			return fmt.Errorf("permanent failure")
		},
	})
	t.Cleanup(store.Close)

	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_dead",
		Path:          "/notion/Dead.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# dead",
		CorrelationID: "corr_dead_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	waitForOpStatus(t, store, "ws_dead", write.OpID, "dead_lettered")

	op, err := store.GetOperation("ws_dead", write.OpID)
	if err != nil {
		t.Fatalf("get op failed: %v", err)
	}
	if op.AttemptCount != 2 {
		t.Fatalf("expected exactly 2 attempts, got %d", op.AttemptCount)
	}
	if op.LastError == nil || *op.LastError == "" {
		t.Fatalf("expected last error to be set")
	}
}

func TestPendingEnvelopeIsRecoveredAfterRestart(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state-recovery.json")
	receivedAt := time.Now().UTC().Format(time.RFC3339)

	writerStore := NewStoreWithOptions(StoreOptions{
		StateFile:      stateFile,
		DisableWorkers: true,
	})
	t.Cleanup(writerStore.Close)
	_, err := writerStore.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:  "env_recovery_1",
		WorkspaceID: "ws_recovery",
		Provider:    "notion",
		DeliveryID:  "delivery_recovery_1",
		ReceivedAt:  receivedAt,
		Payload: map[string]any{
			"type":     "notion.page.upsert",
			"objectId": "obj_recovery_1",
			"path":     "/notion/Recovered.md",
			"content":  "# recovered",
		},
		CorrelationID: "corr_recovery_1",
	})
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}
	if _, err := writerStore.ReadFile("ws_recovery", "/notion/Recovered.md"); err != ErrNotFound {
		t.Fatalf("expected no apply before worker starts, got: %v", err)
	}

	// Simulate process restart where workers come up and recover pending envelopes.
	restarted := NewStoreWithOptions(StoreOptions{StateFile: stateFile})
	t.Cleanup(restarted.Close)
	waitForFileContent(t, restarted, "ws_recovery", "/notion/Recovered.md", "# recovered")
}

func TestCustomProviderAdapterIsUsedForEnvelopeProcessing(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		Adapters: []ProviderAdapter{
			testAdapter{
				provider: "custom",
				actions: []ApplyAction{
					{
						Type:             ActionFileUpsert,
						Path:             "/custom/FromAdapter.md",
						Content:          "# adapter",
						ContentType:      "text/markdown",
						ProviderObjectID: "custom_obj_1",
					},
				},
			},
		},
	})
	t.Cleanup(store.Close)

	_, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_custom_1",
		WorkspaceID:   "ws_custom",
		Provider:      "custom",
		DeliveryID:    "delivery_custom_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339),
		Payload:       map[string]any{"type": "ignored_by_adapter"},
		CorrelationID: "corr_custom_1",
	})
	if err != nil {
		t.Fatalf("custom envelope ingest failed: %v", err)
	}
	waitForFileContent(t, store, "ws_custom", "/custom/FromAdapter.md", "# adapter")
}

func TestAdapterWritebackHandlerIsUsedWhenLegacyProviderWriteNotConfigured(t *testing.T) {
	actions := make(chan WritebackAction, 1)
	store := NewStoreWithOptions(StoreOptions{
		Adapters: []ProviderAdapter{
			testAdapter{
				provider: "notion",
				actions:  []ApplyAction{{Type: ActionIgnored}},
				writeback: func(action WritebackAction) error {
					actions <- action
					return nil
				},
			},
		},
	})
	t.Cleanup(store.Close)

	_, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_adapter_writeback",
		Path:          "/notion/AdapterWriteback.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# adapter writeback",
		CorrelationID: "corr_adapter_writeback_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}

	select {
	case action := <-actions:
		if action.Type != WritebackActionFileUpsert {
			t.Fatalf("expected upsert action from adapter writeback, got %s", action.Type)
		}
		if action.Path != "/notion/AdapterWriteback.md" {
			t.Fatalf("unexpected action path: %s", action.Path)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected adapter writeback callback")
	}
}

func TestLoopSuppressionSuppressesProviderEchoWithinWindow(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		SuppressionWindow: time.Minute,
	})
	t.Cleanup(store.Close)

	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_loop",
		Path:          "/notion/Loop.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# local",
		CorrelationID: "corr_loop_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	waitForOpStatus(t, store, "ws_loop", write.OpID, "succeeded")

	_, err = store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:  "env_loop_echo_1",
		WorkspaceID: "ws_loop",
		Provider:    "notion",
		DeliveryID:  "delivery_loop_echo_1",
		ReceivedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		Payload: map[string]any{
			"type":          "notion.page.upsert",
			"objectId":      "notion_loop_1",
			"path":          "/notion/Loop.md",
			"content":       "# echoed",
			"origin":        "relayfile",
			"opId":          write.OpID,
			"correlationId": "corr_loop_1",
		},
		CorrelationID: "corr_loop_echo_1",
	})
	if err != nil {
		t.Fatalf("ingest echo failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		feed, err := store.GetEvents("ws_loop", "", "", 1000)
		if err != nil {
			t.Fatalf("get events failed: %v", err)
		}
		foundSuppressed := false
		for _, event := range feed.Events {
			if event.Type == "sync.suppressed" {
				foundSuppressed = true
				break
			}
		}
		if foundSuppressed {
			file, err := store.ReadFile("ws_loop", "/notion/Loop.md")
			if err != nil {
				t.Fatalf("read file failed: %v", err)
			}
			if file.Content != "# local" {
				t.Fatalf("expected echoed webhook to be suppressed, got content %q", file.Content)
			}
			status, err := store.GetIngressStatus("ws_loop")
			if err != nil {
				t.Fatalf("get ingress status failed: %v", err)
			}
			if status.SuppressedTotal < 1 {
				t.Fatalf("expected suppressed total >= 1, got %d", status.SuppressedTotal)
			}
			notionIngress, ok := status.IngressByProvider["notion"]
			if !ok || notionIngress.SuppressedTotal < 1 {
				t.Fatalf("expected notion suppression breakdown, got %+v", status.IngressByProvider)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected sync.suppressed event for loop echo")
}

func TestLoopSuppressionWindowExpiryAllowsProviderApply(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{
		SuppressionWindow: 20 * time.Millisecond,
	})
	t.Cleanup(store.Close)

	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_loop_expiry",
		Path:          "/notion/LoopExpiry.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# local",
		CorrelationID: "corr_loop_expiry_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	waitForOpStatus(t, store, "ws_loop_expiry", write.OpID, "succeeded")

	time.Sleep(60 * time.Millisecond)

	_, err = store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:  "env_loop_expiry_1",
		WorkspaceID: "ws_loop_expiry",
		Provider:    "notion",
		DeliveryID:  "delivery_loop_expiry_1",
		ReceivedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		Payload: map[string]any{
			"type":          "notion.page.upsert",
			"objectId":      "notion_loop_expiry_1",
			"path":          "/notion/LoopExpiry.md",
			"content":       "# echoed",
			"origin":        "relayfile",
			"opId":          write.OpID,
			"correlationId": "corr_loop_expiry_1",
		},
		CorrelationID: "corr_loop_expiry_echo_1",
	})
	if err != nil {
		t.Fatalf("ingest echo failed: %v", err)
	}

	waitForFileContent(t, store, "ws_loop_expiry", "/notion/LoopExpiry.md", "# echoed")
}

func waitForFileContent(t *testing.T, store *Store, workspaceID, path, expected string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		file, err := store.ReadFile(workspaceID, path)
		if err == nil && file.Content == expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	file, err := store.ReadFile(workspaceID, path)
	if err != nil {
		t.Fatalf("expected file %s to be readable, last err: %v", path, err)
	}
	t.Fatalf("expected content %q at %s, got %q", expected, path, file.Content)
}

func waitForNotFound(t *testing.T, store *Store, workspaceID, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, err := store.ReadFile(workspaceID, path)
		if err == ErrNotFound {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %s to be deleted", path)
}

func waitForOpStatus(t *testing.T, store *Store, workspaceID, opID, status string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		op, err := store.GetOperation(workspaceID, opID)
		if err == nil && op.Status == status {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	op, err := store.GetOperation(workspaceID, opID)
	if err != nil {
		t.Fatalf("expected op %s to exist: %v", opID, err)
	}
	t.Fatalf("expected op %s status %s, got %s", opID, status, op.Status)
}

type testAdapter struct {
	provider      string
	actions       []ApplyAction
	parseEnvelope func(req WebhookEnvelopeRequest) ([]ApplyAction, error)
	writeback     func(action WritebackAction) error
}

func (a testAdapter) Provider() string {
	return a.provider
}

func (a testAdapter) ParseEnvelope(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
	if a.parseEnvelope != nil {
		return a.parseEnvelope(req)
	}
	return append([]ApplyAction(nil), a.actions...), nil
}

func (a testAdapter) ApplyWriteback(action WritebackAction) error {
	if a.writeback == nil {
		return nil
	}
	return a.writeback(action)
}
