package relayfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type failingLoadStateBackend struct {
	err error
}

func (b failingLoadStateBackend) Load() (*persistedState, error) { return nil, b.err }

func (b failingLoadStateBackend) Save(*persistedState) error { return nil }

// TestStoreLoadErrRetainsStateBackendLoadFailure covers #569: the constructor
// must keep returning a Store, but the state backend Load error must no longer
// be dropped.
func TestStoreLoadErrRetainsStateBackendLoadFailure(t *testing.T) {
	t.Parallel()

	loadFailure := errors.New("pq: SSL is not enabled on the server")
	corruptStateFile := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(corruptStateFile, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt state file: %v", err)
	}

	tests := []struct {
		name    string
		opts    StoreOptions
		wantErr func(error) bool
	}{
		{
			name:    "no state backend",
			opts:    StoreOptions{},
			wantErr: func(err error) bool { return err == nil },
		},
		{
			name:    "missing state file is not a load error",
			opts:    StoreOptions{StateFile: filepath.Join(t.TempDir(), "absent.json")},
			wantErr: func(err error) bool { return err == nil },
		},
		{
			name:    "state backend load failure is retained",
			opts:    StoreOptions{StateBackend: failingLoadStateBackend{err: loadFailure}},
			wantErr: func(err error) bool { return errors.Is(err, loadFailure) },
		},
		{
			name:    "corrupt state file is retained",
			opts:    StoreOptions{StateFile: corruptStateFile},
			wantErr: func(err error) bool { return err != nil },
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := NewStoreWithOptions(tt.opts)
			if store == nil {
				t.Fatal("NewStoreWithOptions returned nil")
			}
			t.Cleanup(store.Close)
			if got := store.LoadErr(); !tt.wantErr(got) {
				t.Fatalf("LoadErr() = %v", got)
			}
		})
	}
}

func TestStoreLoadErrNilReceiver(t *testing.T) {
	t.Parallel()
	var store *Store
	if err := store.LoadErr(); err != nil {
		t.Fatalf("nil Store LoadErr() = %v, want nil", err)
	}
}

// TestStoreLoadErrDoesNotConsumeDurableQueues covers the review finding on
// #569: a store whose state backend failed to load must not start queue
// workers, because Dequeue persists removal and the empty in-memory state
// cannot resolve the popped entries.
func TestStoreLoadErrDoesNotConsumeDurableQueues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		loadErr     error
		wantDepth   int
		wantLoadErr bool
	}{
		{name: "healthy load drains the queue", loadErr: nil, wantDepth: 0, wantLoadErr: false},
		{name: "failed load leaves the queue untouched", loadErr: errors.New("pq: SSL is not enabled on the server"), wantDepth: 1, wantLoadErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			queuePath := filepath.Join(dir, "envelopes.json")
			seed, err := NewFileEnvelopeQueue(queuePath, 8)
			if err != nil {
				t.Fatalf("new file envelope queue: %v", err)
			}
			if !seed.TryEnqueue("env-42") {
				t.Fatal("seed TryEnqueue returned false")
			}
			if err := seed.Close(); err != nil {
				t.Fatalf("close seed queue: %v", err)
			}
			queue, err := NewFileEnvelopeQueue(queuePath, 8)
			if err != nil {
				t.Fatalf("reopen file envelope queue: %v", err)
			}
			if got := queue.Depth(); got != 1 {
				t.Fatalf("reopened queue depth = %d, want 1", got)
			}

			var backend StateBackend
			if tt.loadErr != nil {
				backend = failingLoadStateBackend{err: tt.loadErr}
			}
			store := NewStoreWithOptions(StoreOptions{StateBackend: backend, EnvelopeQueue: queue})
			if (store.LoadErr() != nil) != tt.wantLoadErr {
				t.Fatalf("LoadErr() = %v, wantErr=%v", store.LoadErr(), tt.wantLoadErr)
			}
			if tt.wantDepth == 0 {
				deadline := time.Now().Add(5 * time.Second)
				for queue.Depth() != 0 && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
			} else {
				// Give any wrongly started worker time to pop the entry.
				time.Sleep(200 * time.Millisecond)
			}
			store.Close()
			if got := queue.Depth(); got != tt.wantDepth {
				t.Fatalf("queue depth after construction = %d, want %d", got, tt.wantDepth)
			}
			reopened, err := NewFileEnvelopeQueue(queuePath, 8)
			if err != nil {
				t.Fatalf("reopen queue after close: %v", err)
			}
			defer reopened.Close()
			if got := reopened.Depth(); got != tt.wantDepth {
				t.Fatalf("persisted queue depth = %d, want %d", got, tt.wantDepth)
			}
		})
	}
}
