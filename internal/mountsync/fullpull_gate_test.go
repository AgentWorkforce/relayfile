package mountsync

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Scoped mounts run one Syncer per remote path in the same process. Their
// initial full pulls must share one read budget against the workspace instead
// of each bringing its own worker pool (the :00 bootstrap stampede that pushed
// the workspace Durable Object past its memory limit).
func TestConcurrentSyncerBootstrapsShareProcessWideReadBudget(t *testing.T) {
	t.Setenv("RELAYFILE_BOOTSTRAP_READ_CONCURRENCY", "4")
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
