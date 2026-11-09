package relayfile

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func startupFixtureState(t *testing.T) *persistedState {
	t.Helper()
	var state persistedState
	if err := json.Unmarshal(serializedEmptyStore(t), &state); err != nil {
		t.Fatal(err)
	}
	state.Workspaces["workspace"] = &workspaceState{Files: map[string]File{}, Ops: map[string]OperationStatus{"operation": {Status: "pending"}}}
	state.EnvelopesByID["envelope"] = WebhookEnvelopeRequest{WorkspaceID: "workspace"}
	return &state
}

func TestCheckedStoreQueueStateCoherenceWithoutConsumption(t *testing.T) {
	for _, test := range []struct {
		name     string
		hasState bool
		envelope string
		task     *WritebackQueueItem
		pass     bool
	}{
		{name: "empty-fresh", pass: true},
		{name: "valid-envelope", hasState: true, envelope: "envelope", pass: true},
		{name: "valid-writeback", hasState: true, task: &WritebackQueueItem{WorkspaceID: "workspace", OpID: "operation"}, pass: true},
		{name: "missing-state-envelope", envelope: "envelope"},
		{name: "missing-state-writeback", task: &WritebackQueueItem{WorkspaceID: "workspace", OpID: "operation"}},
		{name: "orphan-envelope", hasState: true, envelope: "unknown"},
		{name: "missing-workspace", hasState: true, task: &WritebackQueueItem{OpID: "operation"}},
		{name: "foreign-workspace", hasState: true, task: &WritebackQueueItem{WorkspaceID: "other", OpID: "operation"}},
		{name: "orphan-operation", hasState: true, task: &WritebackQueueItem{WorkspaceID: "workspace", OpID: "unknown"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := NewInMemoryStateBackend()
			if test.hasState {
				if err := backend.Save(startupFixtureState(t)); err != nil {
					t.Fatal(err)
				}
			}
			envelopes, queueErr := NewFileEnvelopeQueue(filepath.Join(t.TempDir(), "envelopes.json"), 2)
			if queueErr != nil {
				t.Fatal(queueErr)
			}
			writebacks := NewInMemoryWritebackQueue(2)
			if test.envelope != "" && !envelopes.TryEnqueue(test.envelope) {
				t.Fatal("envelope setup failed")
			}
			if test.task != nil && !writebacks.TryEnqueue(*test.task) {
				t.Fatal("generic queue acceptance changed")
			}
			beforeEnvelope, beforeWriteback := envelopes.Depth(), writebacks.Depth()
			store, err := NewStoreWithOptionsChecked(StoreOptions{StateBackend: backend, EnvelopeQueue: envelopes, WritebackQueue: writebacks, DisableWorkers: true})
			if test.pass {
				if err != nil || store == nil {
					t.Fatalf("consistent startup refused: %v", err)
				}
			} else if err == nil || err.Error() != "persistent_queue_load_failed" || store != nil {
				t.Fatalf("inconsistent startup admitted: %v", err)
			}
			if envelopes.Depth() != beforeEnvelope || writebacks.Depth() != beforeWriteback {
				t.Fatal("startup consumed persisted work")
			}
			if store != nil {
				store.Close()
			} else {
				envelopes.Close()
				writebacks.Close()
			}
		})
	}
}

type opaqueWritebackQueue struct{ WritebackQueue }

func TestCheckedStoreNonemptyOpaqueQueuesRefuseWithoutChangingLegacy(t *testing.T) {
	for _, kind := range []string{"envelope", "writeback"} {
		t.Run(kind, func(t *testing.T) {
			backend := NewInMemoryStateBackend()
			if err := backend.Save(startupFixtureState(t)); err != nil {
				t.Fatal(err)
			}
			envelopes := NewInMemoryEnvelopeQueue(2)
			writebacks := &opaqueWritebackQueue{WritebackQueue: NewInMemoryWritebackQueue(2)}
			if kind == "envelope" {
				envelopes.TryEnqueue("envelope")
			} else {
				writebacks.TryEnqueue(WritebackQueueItem{WorkspaceID: "workspace", OpID: "operation"})
			}
			options := StoreOptions{StateBackend: backend, EnvelopeQueue: envelopes, WritebackQueue: writebacks, DisableWorkers: true}
			store, err := NewStoreWithOptionsChecked(options)
			if store != nil || err == nil {
				if store != nil {
					store.Close()
				}
				t.Fatal("uninspectable persisted work admitted")
			}
			if envelopes.Depth()+writebacks.Depth() != 1 {
				t.Fatal("refusal consumed work")
			}
			legacy := NewStoreWithOptions(options)
			if legacy == nil || envelopes.Depth()+writebacks.Depth() != 1 {
				t.Fatal("legacy queue behavior changed")
			}
			legacy.Close()
		})
	}
}

type closingPoolBackend struct {
	*InMemoryStateBackend
	closed           atomic.Bool
	once             sync.Once
	closeCalled      chan struct{}
	savedAfterCancel atomic.Bool
}

func (b *closingPoolBackend) Close() error {
	b.once.Do(func() { b.closed.Store(true); close(b.closeCalled) })
	return nil
}
func (b *closingPoolBackend) Save(state *persistedState) error {
	if b.closed.Load() {
		return errors.New("pool_closed_before_worker_save")
	}
	if err := b.InMemoryStateBackend.Save(state); err != nil {
		return err
	}
	b.savedAfterCancel.Store(true)
	return nil
}

type sharedPoolEnvelopeQueue struct {
	EnvelopeQueue
	backend *closingPoolBackend
}

func (q *sharedPoolEnvelopeQueue) Close() error { q.EnvelopeQueue.Close(); return q.backend.Close() }

func TestCheckedStoreClosesSharedPoolAfterWorkerPersistence(t *testing.T) {
	backend := &closingPoolBackend{InMemoryStateBackend: NewInMemoryStateBackend(), closeCalled: make(chan struct{})}
	queue := &sharedPoolEnvelopeQueue{EnvelopeQueue: NewInMemoryEnvelopeQueue(1), backend: backend}
	store, err := NewStoreWithOptionsChecked(StoreOptions{StateBackend: backend, EnvelopeQueue: queue, DisableWorkers: true})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	workerResult := make(chan error, 1)
	store.wg.Add(1)
	go func() {
		defer store.wg.Done()
		<-store.queueCtx.Done()
		<-release
		workerResult <- backend.Save(&persistedState{})
	}()
	done := make(chan struct{})
	go func() { store.Close(); close(done) }()
	select {
	case <-store.queueCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("close did not cancel worker")
	}
	select {
	case <-backend.closeCalled:
		releaseOnce.Do(func() { close(release) })
		<-done
		t.Fatal("pool closed while worker still needed to persist")
	case <-time.After(50 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-workerResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not finish")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close did not finish")
	}
	if !backend.savedAfterCancel.Load() || !backend.closed.Load() {
		t.Fatal("worker persistence or final cleanup missing")
	}
}
