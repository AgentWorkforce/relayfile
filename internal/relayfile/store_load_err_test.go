package relayfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
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
