package mountsync

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Scoped mounts run one Syncer per remote path in the same process. Their
// initial full pulls must share one read budget against the workspace instead
// of each bringing its own worker pool (the :00 bootstrap stampede that pushed
// the workspace Durable Object past its memory limit).
func TestConcurrentSyncerBootstrapsShareProcessWideReadBudget(t *testing.T) {
	t.Setenv("RELAYFILE_BOOTSTRAP_READ_CONCURRENCY", "4")
	withFullPullReadGate(t, defaultFullPullReadConcurrency)
	client := newBootstrapClient(24, 24)
	client.readFileSleep = 20 * time.Millisecond

	const scopes = 3
	syncers := make([]*Syncer, scopes)
	for i := range syncers {
		syncers[i] = newBootstrapSyncer(t, client, t.TempDir(), SyncerOptions{RootCtx: context.Background()})
	}

	var wg sync.WaitGroup
	errs := make(chan error, scopes)
	for _, s := range syncers {
		s := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.Reconcile(context.Background())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("reconcile failed: %v", err)
		}
	}

	if got, limit := client.maxActiveRead.Load(), int64(defaultFullPullReadConcurrency); got > limit {
		t.Fatalf("max concurrent bootstrap reads across %d syncers = %d, want <= %d", scopes, got, limit)
	}
	if got, want := client.readFileCalls.Load(), int64(24*scopes); got != want {
		t.Fatalf("expected every scope to converge with %d reads, got %d", want, got)
	}
}

func TestReadGateWaitHonoursContext(t *testing.T) {
	gate := newReadGate(1)
	release := make(chan struct{})
	holding := make(chan struct{})
	go func() {
		_ = gate.do(context.Background(), func() error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	called := false
	err := gate.do(ctx, func() error { called = true; return nil })
	if err != context.DeadlineExceeded {
		t.Fatalf("gate wait error = %v, want context.DeadlineExceeded", err)
	}
	if called {
		t.Fatal("gated fn ran without a slot")
	}
}

func TestFullPullReadConcurrencyEnv(t *testing.T) {
	for raw, want := range map[string]int{"": 4, "2": 2, "0": 4, "bogus": 4, "99": maxFullPullReadConcurrency} {
		t.Setenv("RELAYFILE_FULL_PULL_READ_CONCURRENCY", raw)
		if got := fullPullReadConcurrency(); got != want {
			t.Fatalf("RELAYFILE_FULL_PULL_READ_CONCURRENCY=%q -> %d, want %d", raw, got, want)
		}
	}
}

// withFullPullReadGate installs a gate of exactly limit slots for one test, so
// assertions about the cap never depend on RELAYFILE_FULL_PULL_READ_CONCURRENCY
// in the environment that initialised the package gate.
func withFullPullReadGate(t *testing.T, limit int) *readGate {
	t.Helper()
	previous := fullPullReadGate
	gate := newReadGate(limit)
	fullPullReadGate = gate
	t.Cleanup(func() { fullPullReadGate = previous })
	return gate
}

// githubSeedGateClient streams its tar export through a body that stays open
// until released, and counts clone-manifest reads, so a test can observe
// whether the GitHub seed holds a full-pull slot for the whole stream.
type githubSeedGateClient struct {
	*fakeExportClient
	manifestPath  string
	manifestReads atomic.Int64
	bodyOpen      chan struct{}
	bodyRelease   chan struct{}
}

func (c *githubSeedGateClient) ReadFile(ctx context.Context, workspaceID, path string) (RemoteFile, error) {
	if normalizeRemotePath(path) == normalizeRemotePath(c.manifestPath) {
		c.manifestReads.Add(1)
	}
	return c.fakeExportClient.fakeClient.ReadFile(ctx, workspaceID, path)
}

func (c *githubSeedGateClient) ExportGithubWorkingTreeTar(ctx context.Context, workspaceID string, seed GithubWorkingTreeSeedRequest) (GithubWorkingTreeTar, error) {
	tarBody, err := c.fakeExportClient.ExportGithubWorkingTreeTar(ctx, workspaceID, seed)
	if err != nil {
		return tarBody, err
	}
	close(c.bodyOpen)
	tarBody.Body = &heldBody{ReadCloser: tarBody.Body, release: c.bodyRelease}
	return tarBody, nil
}

// heldBody blocks its first Read until released: an export whose response
// headers have arrived but whose (large) body is still streaming.
type heldBody struct {
	io.ReadCloser
	release <-chan struct{}
	once    sync.Once
}

func (b *heldBody) Read(p []byte) (int, error) {
	b.once.Do(func() { <-b.release })
	return b.ReadCloser.Read(p)
}

// The GitHub tar seed is the largest full-pull read there is. Its clone
// manifest read and the whole tar stream — request until body close — must
// sit inside the process-wide full-pull budget, or N GitHub scopes seeding at
// once add N unbounded streams on top of the capped tree/bulk reads.
func TestGithubTarSeedHoldsFullPullSlotUntilBodyClosed(t *testing.T) {
	const (
		contentsRoot = "/github/repos/acme/widgets/contents"
		headSHA      = "head123"
		sentinelPath = "/github/repos/acme/widgets/.relayfile/clone.json"
	)
	body := []byte("# readme\n")
	remotePath := contentsRoot + "/README.md@" + headSHA + ".json"
	gate := withFullPullReadGate(t, 1)
	client := &githubSeedGateClient{
		fakeExportClient: &fakeExportClient{
			fakeClient: &fakeClient{files: map[string]RemoteFile{
				sentinelPath: {Path: sentinelPath, Revision: "rev_1", ContentType: "application/json",
					Content: `{"headSha":"` + headSHA + `","eventsCursor":"evt_seed"}`},
				remotePath: {Path: remotePath, Revision: "rev_2", ContentType: "text/markdown",
					Content: string(body), ContentHash: hashBytes(body)},
			}},
			tarFiles: map[string][]byte{"README.md": body},
		},
		manifestPath: sentinelPath,
		bodyOpen:     make(chan struct{}),
		bodyRelease:  make(chan struct{}),
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_github_tar_gate", RemoteRoot: contentsRoot, LocalRoot: t.TempDir(),
		WebSocket: boolPtr(false), FullPullEvery: -1,
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	// Occupy the only slot: the manifest read must wait for it.
	holderRelease := make(chan struct{})
	holding := make(chan struct{})
	go func() {
		_ = gate.do(context.Background(), func() error { close(holding); <-holderRelease; return nil })
	}()
	<-holding
	done := make(chan error, 1)
	go func() { done <- syncer.SyncOnce(context.Background()) }()
	time.Sleep(100 * time.Millisecond)
	if got := client.manifestReads.Load(); got != 0 {
		close(holderRelease)
		close(client.bodyRelease)
		<-done
		t.Fatalf("clone manifest read %d time(s) while every full-pull slot was taken, want 0", got)
	}
	close(holderRelease)

	select {
	case <-client.bodyOpen:
	case <-time.After(2 * time.Second):
		close(client.bodyRelease)
		<-done
		t.Fatal("github tar export never started")
	}
	// Headers are back but the body is still streaming: the seed must still
	// own its slot.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	stolen := gate.do(ctx, func() error { return nil })
	cancel()
	close(client.bodyRelease)
	if err := <-done; err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if stolen == nil {
		t.Fatal("a full-pull slot was free while the github tar body was still streaming, want the seed to hold it until close")
	}
}

// githubSeedTreeHookClient runs onTreeListed after each tree page, while the
// page's own gate slot is still held.
type githubSeedTreeHookClient struct {
	*githubSeedGateClient
	onTreeListed func()
}

func (c *githubSeedTreeHookClient) ListTree(ctx context.Context, workspaceID, path string, depth int, cursor string) (TreeResponse, error) {
	page, err := c.githubSeedGateClient.ListTree(ctx, workspaceID, path, depth, cursor)
	if c.onTreeListed != nil {
		c.onTreeListed()
	}
	return page, err
}

// Waiting for a tar-stream slot must not hold the Syncer mutex: sibling
// scopes can own every slot for a whole stream, and local writeback, outbox
// and watcher handling all need that mutex in the meantime.
func TestGithubTarSeedWaitsForSlotWithoutHoldingSyncerMutex(t *testing.T) {
	const (
		contentsRoot = "/github/repos/acme/widgets/contents"
		headSHA      = "head123"
		sentinelPath = "/github/repos/acme/widgets/.relayfile/clone.json"
	)
	body := []byte("# readme\n")
	remotePath := contentsRoot + "/README.md@" + headSHA + ".json"
	gate := withFullPullReadGate(t, 1)
	base := &githubSeedGateClient{
		fakeExportClient: &fakeExportClient{
			fakeClient: &fakeClient{files: map[string]RemoteFile{
				sentinelPath: {Path: sentinelPath, Revision: "rev_1", ContentType: "application/json",
					Content: `{"headSha":"` + headSHA + `","eventsCursor":"evt_seed"}`},
				remotePath: {Path: remotePath, Revision: "rev_2", ContentType: "text/markdown",
					Content: string(body), ContentHash: hashBytes(body)},
			}},
			tarFiles: map[string][]byte{"README.md": body},
		},
		manifestPath: sentinelPath,
		bodyOpen:     make(chan struct{}),
		bodyRelease:  make(chan struct{}),
	}
	close(base.bodyRelease)
	// Queue a sibling for the only slot while the tree page still holds it,
	// so it wins the slot the moment the page releases and the seed then
	// waits for its tar slot.
	siblingHolding := make(chan struct{})
	siblingRelease := make(chan struct{})
	var once sync.Once
	client := &githubSeedTreeHookClient{githubSeedGateClient: base, onTreeListed: func() {
		once.Do(func() {
			go func() {
				_ = gate.do(context.Background(), func() error { close(siblingHolding); <-siblingRelease; return nil })
			}()
			time.Sleep(20 * time.Millisecond) // let the sibling block on the gate first
		})
	}}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_github_tar_gate_mutex", RemoteRoot: contentsRoot, LocalRoot: t.TempDir(),
		WebSocket: boolPtr(false), FullPullEvery: -1,
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- syncer.SyncOnce(context.Background()) }()
	select {
	case <-siblingHolding:
	case <-time.After(2 * time.Second):
		close(siblingRelease)
		<-done
		t.Fatal("sibling never took the slot after the tree snapshot")
	}
	time.Sleep(50 * time.Millisecond) // the seed is now waiting for its tar slot

	locked := make(chan struct{})
	go func() { syncer.WebSocketConnected(); close(locked) }()
	select {
	case <-locked:
	case <-time.After(500 * time.Millisecond):
		close(siblingRelease)
		<-done
		t.Fatal("Syncer mutex was held while the github tar seed waited for a full-pull slot")
	}
	close(siblingRelease)
	if err := <-done; err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
}
