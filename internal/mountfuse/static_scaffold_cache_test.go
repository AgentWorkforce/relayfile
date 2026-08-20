package mountfuse

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountsync"
)

func TestStaticScaffoldCachedForMountGeneration(t *testing.T) {
	const scaffoldPath = "/discovery/google-mail/filters/.schema.json"
	client := &fakeRemoteClient{files: map[string]mountsync.RemoteFile{
		scaffoldPath: {
			Path:        scaffoldPath,
			Revision:    "rev_1",
			ContentType: "application/json",
			Content:     `{"type":"object"}`,
		},
	}}
	state := newFSState(Config{
		Client:      client,
		WorkspaceID: "rw_target",
		RemoteRoot:  "/",
		ContentTTL:  time.Nanosecond,
	})

	for i := 0; i < 9; i++ {
		file, err := state.readFile(context.Background(), scaffoldPath)
		if err != nil {
			t.Fatalf("read %d: %v", i+1, err)
		}
		if file.Revision != "rev_1" {
			t.Fatalf("read %d revision = %q, want rev_1", i+1, file.Revision)
		}
		time.Sleep(time.Microsecond)
	}

	if got := atomic.LoadInt32(&client.readFileCalls); got != 1 {
		t.Fatalf("static scaffold remote reads = %d, want 1", got)
	}

	// Must-fire control: the websocket generation invalidator evicts the
	// generation-scoped entry, so the next read reaches the remote once.
	client.files[scaffoldPath] = mountsync.RemoteFile{
		Path:        scaffoldPath,
		Revision:    "rev_2",
		ContentType: "application/json",
		Content:     `{"type":"object","generation":2}`,
	}
	state.invalidate(scaffoldPath)
	file, err := state.readFile(context.Background(), scaffoldPath)
	if err != nil {
		t.Fatalf("read after generation invalidation: %v", err)
	}
	if file.Revision != "rev_2" {
		t.Fatalf("revision after generation invalidation = %q, want rev_2", file.Revision)
	}
	if got := atomic.LoadInt32(&client.readFileCalls); got != 2 {
		t.Fatalf("remote reads after generation invalidation = %d, want 2", got)
	}
}

func TestStaticScaffoldConcurrentReadsCoalesce(t *testing.T) {
	const scaffoldPath = "/discovery/google-mail/LAYOUT.md"
	started := make(chan struct{})
	release := make(chan struct{})
	client := &fakeRemoteClient{
		readFileFunc: func(ctx context.Context, path string) (mountsync.RemoteFile, error) {
			if path != scaffoldPath {
				t.Fatalf("remote path = %q, want %q", path, scaffoldPath)
			}
			select {
			case <-started:
			default:
				close(started)
			}
			select {
			case <-ctx.Done():
				return mountsync.RemoteFile{}, ctx.Err()
			case <-release:
				return mountsync.RemoteFile{
					Path:        scaffoldPath,
					Revision:    "rev_1",
					ContentType: "text/markdown",
					Content:     "# layout",
				}, nil
			}
		},
	}
	state := newFSState(Config{
		Client:      client,
		WorkspaceID: "rw_target",
		RemoteRoot:  "/",
	})

	const readers = 24
	var wg sync.WaitGroup
	errs := make(chan error, readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := state.readFile(context.Background(), scaffoldPath)
			errs <- err
		}()
	}
	<-started
	time.Sleep(10 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent read: %v", err)
		}
	}
	if got := atomic.LoadInt32(&client.readFileCalls); got != 1 {
		t.Fatalf("coalesced remote reads = %d, want 1", got)
	}
}

func TestMutableContentStillUsesTTL(t *testing.T) {
	const mutablePath = "/records/live.json"
	client := &fakeRemoteClient{files: map[string]mountsync.RemoteFile{
		mutablePath: {
			Path:        mutablePath,
			Revision:    "rev_1",
			ContentType: "application/json",
			Content:     `{"live":true}`,
		},
	}}
	state := newFSState(Config{
		Client:      client,
		WorkspaceID: "rw_target",
		RemoteRoot:  "/",
		ContentTTL:  time.Nanosecond,
	})

	if _, err := state.readFile(context.Background(), mutablePath); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Microsecond)
	if _, err := state.readFile(context.Background(), mutablePath); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&client.readFileCalls); got != 2 {
		t.Fatalf("mutable remote reads = %d, want 2", got)
	}
}
