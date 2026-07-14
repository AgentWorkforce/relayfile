package main

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountsync"
	"github.com/agentworkforce/relayfile/internal/relayfile"
)

// blockingBootstrapClient embeds the full RemoteClient contract and overrides
// only the calls this regression reaches. The nil promoted methods are a
// deliberate tripwire: if the bootstrap flow unexpectedly leaves the export
// path, the test panics instead of silently accepting a different scenario.
type blockingBootstrapClient struct {
	mountsync.RemoteClient

	exportStarted chan struct{}
	bulkCalled    chan struct{}
	opCalled      chan struct{}
	exportOnce    sync.Once
	bulkOnce      sync.Once
	opOnce        sync.Once
}

type concurrentCredentialFailureClient struct {
	mountsync.RemoteClient

	exportStarted chan struct{}
	bulkStarted   chan struct{}
	release       <-chan struct{}
	exportOnce    sync.Once
	bulkOnce      sync.Once
}

func (c *concurrentCredentialFailureClient) ExportFiles(ctx context.Context, _, _ string) ([]mountsync.RemoteFile, error) {
	c.exportOnce.Do(func() { close(c.exportStarted) })
	select {
	case <-c.release:
		return nil, ErrDelegatedRelayfileCredentialsExpired
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *concurrentCredentialFailureClient) WriteFilesBulk(ctx context.Context, _ string, _ []mountsync.BulkWriteFile) (mountsync.BulkWriteResponse, error) {
	c.bulkOnce.Do(func() { close(c.bulkStarted) })
	select {
	case <-c.release:
		return mountsync.BulkWriteResponse{}, ErrDelegatedRelayfileCredentialsExpired
	case <-ctx.Done():
		return mountsync.BulkWriteResponse{}, ctx.Err()
	}
}

func (c *blockingBootstrapClient) ExportFiles(ctx context.Context, _, _ string) ([]mountsync.RemoteFile, error) {
	c.exportOnce.Do(func() { close(c.exportStarted) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *blockingBootstrapClient) WriteFilesBulk(_ context.Context, _ string, files []mountsync.BulkWriteFile) (mountsync.BulkWriteResponse, error) {
	c.bulkOnce.Do(func() { close(c.bulkCalled) })
	return mountsync.BulkWriteResponse{
		Written: 1,
		Results: []mountsync.BulkWriteResult{{
			Path:        files[0].Path,
			Revision:    "rev_local_1",
			ContentType: files[0].ContentType,
			OpID:        "op_watcher_before_bootstrap",
			Writeback:   &relayfile.BulkWriteWritebackResult{Provider: "slack", State: "running"},
		}},
		CorrelationID: "corr_watcher_before_bootstrap",
	}, nil
}

func (c *blockingBootstrapClient) GetOperation(_ context.Context, _ string, opID string) (mountsync.OperationStatus, error) {
	c.opOnce.Do(func() { close(c.opCalled) })
	return mountsync.OperationStatus{
		OpID:     opID,
		Path:     "/slack/channels/C123/messages/messages 5ab77d67-1111-4111-8111-123456789abc.json",
		Revision: "rev_local_1",
		Provider: "slack",
		Status:   "succeeded",
	}, nil
}

func TestMountLoopStartsWatcherBeforeBlockedInitialBootstrap(t *testing.T) {
	localDir := t.TempDir()
	draftRel := filepath.FromSlash("slack/channels/C123/messages/messages 5ab77d67-1111-4111-8111-123456789abc.json")
	draftPath := filepath.Join(localDir, draftRel)
	if err := os.MkdirAll(filepath.Dir(draftPath), 0o755); err != nil {
		t.Fatalf("mkdir draft parent: %v", err)
	}

	client := &blockingBootstrapClient{
		exportStarted: make(chan struct{}),
		bulkCalled:    make(chan struct{}),
		opCalled:      make(chan struct{}),
	}
	rootCtx, cancel := context.WithCancel(context.Background())
	syncer, err := mountsync.NewSyncer(client, mountsync.SyncerOptions{
		WorkspaceID: "ws_watcher_before_bootstrap",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
		RootCtx:     rootCtx,
		WebSocket:   boolPtr(false),
		Logger:      log.New(io.Discard, "", 0),
	})
	if err != nil {
		cancel()
		t.Fatalf("NewSyncer: %v", err)
	}

	previousLogWriter := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(previousLogWriter)

	loopDone := make(chan error, 1)
	go func() {
		loopDone <- runMountLoop(
			rootCtx,
			syncer,
			localDir,
			"ws_watcher_before_bootstrap",
			"http://unused.test",
			"",
			time.Second,
			time.Hour,
			0,
			false,
			false,
			false,
			mountPIDFile(localDir),
			mountLogFile(localDir),
		)
	}()

	select {
	case <-client.exportStarted:
	case <-time.After(time.Second):
		cancel()
		<-loopDone
		t.Fatal("initial export did not start")
	}
	if err := os.WriteFile(draftPath, []byte(`{"channel":"C123","text":"watcher ready"}`), 0o644); err != nil {
		cancel()
		<-loopDone
		t.Fatalf("write draft: %v", err)
	}

	select {
	case <-client.bulkCalled:
	case <-time.After(2 * time.Second):
		cancel()
		<-loopDone
		t.Fatal("watcher did not admit local draft while initial export was blocked")
	}
	select {
	case <-client.opCalled:
	case <-time.After(time.Second):
		cancel()
		<-loopDone
		t.Fatal("watcher did not settle operation receipt while initial export was blocked")
	}

	var acked []string
	var globErr error
	ackDeadline := time.Now().Add(time.Second)
	for time.Now().Before(ackDeadline) {
		acked, globErr = filepath.Glob(filepath.Join(localDir, ".relay", "outbox", "acked", "*.json"))
		if globErr != nil || len(acked) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if globErr != nil || len(acked) != 1 {
		cancel()
		<-loopDone
		t.Fatalf("acked receipts = %v (glob err=%v), want exactly one", acked, globErr)
	}

	cancel()
	select {
	case err := <-loopDone:
		if err != nil {
			t.Fatalf("mount loop shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mount loop did not stop after cancel")
	}
}

func TestMountLoopSerializesStatusWhenWatcherAndInitialBootstrapFailTogether(t *testing.T) {
	localDir := t.TempDir()
	draftRel := filepath.FromSlash("slack/channels/C123/messages/messages 6ab77d67-1111-4111-8111-123456789abc.json")
	draftPath := filepath.Join(localDir, draftRel)
	if err := os.MkdirAll(filepath.Dir(draftPath), 0o755); err != nil {
		t.Fatalf("mkdir draft parent: %v", err)
	}

	release := make(chan struct{})
	client := &concurrentCredentialFailureClient{
		exportStarted: make(chan struct{}),
		bulkStarted:   make(chan struct{}),
		release:       release,
	}
	rootCtx, cancel := context.WithCancel(context.Background())
	syncer, err := mountsync.NewSyncer(client, mountsync.SyncerOptions{
		WorkspaceID: "ws_mount_loop_status_race",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
		RootCtx:     rootCtx,
		WebSocket:   boolPtr(false),
		Logger:      log.New(io.Discard, "", 0),
	})
	if err != nil {
		cancel()
		t.Fatalf("NewSyncer: %v", err)
	}

	previousLogWriter := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(previousLogWriter)

	loopDone := make(chan error, 1)
	go func() {
		loopDone <- runMountLoop(
			rootCtx,
			syncer,
			localDir,
			"ws_mount_loop_status_race",
			"http://unused.test",
			"",
			time.Second,
			time.Hour,
			0,
			false,
			false,
			false,
			mountPIDFile(localDir),
			mountLogFile(localDir),
		)
	}()

	select {
	case <-client.exportStarted:
	case <-time.After(time.Second):
		cancel()
		<-loopDone
		t.Fatal("initial export did not start")
	}
	if err := os.WriteFile(draftPath, []byte(`{"channel":"C123","text":"race status"}`), 0o644); err != nil {
		cancel()
		<-loopDone
		t.Fatalf("write draft: %v", err)
	}
	select {
	case <-client.bulkStarted:
	case <-time.After(2 * time.Second):
		cancel()
		<-loopDone
		t.Fatal("watcher did not reach concurrent credential failure")
	}

	// Both the blocked initial runCycle and the watcher callback now return
	// the same terminal credential error. They independently transition the
	// mount-loop status to degraded; -race must prove those transitions and
	// snapshot reads are serialized.
	close(release)
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-loopDone:
		if err != nil {
			t.Fatalf("mount loop shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mount loop did not stop after cancel")
	}
}
