package mountsync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/relayfile"
)

// pushPerfStats observes the /fs/bulk traffic a push produces so the large
// corpus benchmark can report how many requests went out, how long the
// client stayed silent before the first one, and how many overlapped.
type pushPerfStats struct {
	started     time.Time
	bulkPosts   atomic.Int64
	requests    atomic.Int64
	inFlight    atomic.Int64
	maxInFlight atomic.Int64
	firstPostMu sync.Mutex
	firstPost   time.Duration
}

func (s *pushPerfStats) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/fs/bulk") {
			s.firstPostMu.Lock()
			if s.firstPost == 0 {
				s.firstPost = time.Since(s.started)
			}
			s.firstPostMu.Unlock()
			s.bulkPosts.Add(1)
			current := s.inFlight.Add(1)
			for {
				observed := s.maxInFlight.Load()
				if current <= observed || s.maxInFlight.CompareAndSwap(observed, current) {
					break
				}
			}
			defer s.inFlight.Add(-1)
		}
		next.ServeHTTP(w, r)
	})
}

// seedPushPerfCorpus writes n deterministic text files of roughly size bytes
// spread across nested directories, approximating a seeded code tree.
func seedPushPerfCorpus(tb testing.TB, root string, n, size int) {
	tb.Helper()
	line := "func example() { return \"relayfile mount push benchmark payload\" }\n"
	body := strings.Repeat(line, size/len(line)+1)[:size]
	for i := 0; i < n; i++ {
		dir := filepath.Join(root, "src", fmt.Sprintf("pkg%03d", i/100), fmt.Sprintf("sub%d", i%7))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			tb.Fatal(err)
		}
		content := fmt.Sprintf("// file %d\n%s", i, body)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%05d.go", i)), []byte(content), 0o644); err != nil {
			tb.Fatal(err)
		}
	}
}

type pushPerfRun struct {
	syncer *Syncer
	stats  *pushPerfStats
	close  func()
}

func newPushPerfRun(tb testing.TB, n, size int) pushPerfRun {
	tb.Helper()
	// The reference store's default 1024-slot writeback queue drops (and
	// leaves pending) receipts when ~2.7k-file chunks land back to back; size
	// it for the corpus so receipts settle and only client cost is measured.
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{WritebackQueue: relayfile.NewInMemoryWritebackQueue(1 << 16)})
	tb.Cleanup(store.Close)
	stats := &pushPerfStats{}
	api := httptest.NewServer(stats.wrap(newMountsyncAPIHandler(tb, store)))
	workspaceID := "ws_push_perf"
	token := mustMountsyncTestJWT(tb, "dev-secret", workspaceID, "push-perf", []string{"fs:read", "fs:write", "sync:trigger", "ops:read"}, time.Now().Add(time.Hour))
	client := NewHTTPClient(api.URL, token, api.Client())
	localRoot := tb.TempDir()
	seedPushPerfCorpus(tb, localRoot, n, size)
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:   workspaceID,
		RemoteRoot:    "/repo",
		LocalRoot:     localRoot,
		StateDir:      tb.TempDir(),
		RootCtx:       context.Background(),
		FullPullEvery: -1,
		WebSocket:     boolPtr(false),
	})
	if err != nil {
		tb.Fatalf("NewSyncer: %v", err)
	}
	return pushPerfRun{syncer: syncer, stats: stats, close: api.Close}
}

// settleLaggingReceipts tolerates exactly one benign outcome of a drain
// against the in-process reference server: every write was accepted, but the
// server's asynchronous writeback dispatch had not yet marked a few operation
// receipts succeeded when the client polled them. Those records keep their
// opId and zero failed attempts; a later receipt poll settles them without
// re-uploading. Anything else fails the benchmark.
func settleLaggingReceipts(b *testing.B, syncer *Syncer, err error) {
	b.Helper()
	if err == nil {
		return
	}
	records, listErr := syncer.listPendingOutboxRecords()
	if listErr != nil || len(records) == 0 {
		b.Fatalf("drain failed: %v", err)
	}
	for _, record := range records {
		if record.OpID == "" || record.AttemptCount != 0 || record.NeedsAttention {
			b.Fatalf("drain failed: %v (record %s attempts=%d opId=%q err=%q)", err, record.RemotePath, record.AttemptCount, record.OpID, record.LastError)
		}
	}
	b.Logf("%d receipt(s) still pending server dispatch; polling again", len(records))
	time.Sleep(2 * time.Second)
	if err := syncer.FlushOutboxOnce(context.Background()); err != nil {
		b.Fatalf("receipt re-poll failed: %v", err)
	}
}

// BenchmarkPushLocalAndFlushOnceLargeTree reproduces the production
// `relayfile-mount --push-local-once` seed of a ~13k file code tree against
// the real HTTP API handler. Run with:
//
//	go test ./internal/mountsync -run '^$' -bench PushLocalAndFlushOnceLargeTree -benchtime 1x -benchmem
func BenchmarkPushLocalAndFlushOnceLargeTree(b *testing.B) {
	const files, size = 13000, 3 * 1024
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		run := newPushPerfRun(b, files, size)
		run.stats.started = time.Now()
		b.StartTimer()
		err := run.syncer.PushLocalAndFlushOnce(context.Background())
		b.StopTimer()
		b.ReportMetric(float64(run.stats.bulkPosts.Load()), "bulk_posts/op")
		b.ReportMetric(float64(run.stats.firstPost.Milliseconds()), "first_post_ms/op")
		b.ReportMetric(float64(run.stats.maxInFlight.Load()), "max_in_flight")
		b.ReportMetric(float64(run.stats.requests.Load()), "http_requests/op")
		settleLaggingReceipts(b, run.syncer, err)
		if outbox := run.syncer.summarizeOutbox(); outbox.Pending != 0 || outbox.NeedsAttention != 0 {
			b.Fatalf("outbox not drained: %+v", outbox)
		}
		run.close()
		b.StartTimer()
	}
}

// seedPushPerfOutbox ingests every corpus file into the durable outbox
// without uploading, reproducing the backlog a later flush drains in
// maxWritebackBatchBytes chunks (the production pattern of a handful of
// strictly serial 8 MiB /fs/bulk POSTs).
func seedPushPerfOutbox(tb testing.TB, syncer *Syncer) int {
	tb.Helper()
	localFiles, err := syncer.scanLocalFiles()
	if err != nil {
		tb.Fatal(err)
	}
	count := 0
	for remotePath, snapshot := range localFiles {
		full, err := syncer.readLocalSnapshot(snapshot.LocalPath, true)
		if err != nil {
			tb.Fatal(err)
		}
		if _, err := syncer.ensureOutboxRecord(pendingBulkWrite{remotePath: remotePath, localPath: snapshot.LocalPath, snapshot: full}); err != nil {
			tb.Fatal(err)
		}
		count++
	}
	return count
}

// BenchmarkFlushOutboxOnceLargeBacklog drains a ~13k record (~41 MB) outbox
// backlog. Run with:
//
//	go test ./internal/mountsync -run '^$' -bench FlushOutboxOnceLargeBacklog -benchtime 1x -benchmem
func BenchmarkFlushOutboxOnceLargeBacklog(b *testing.B) {
	const files, size = 13000, 3 * 1024
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		run := newPushPerfRun(b, files, size)
		if got := seedPushPerfOutbox(b, run.syncer); got != files {
			b.Fatalf("seeded %d records, want %d", got, files)
		}
		// Measure the drain itself. Before linear batch sizing this drain
		// overran the default 60s outbox flush budget on an Apple M3.
		run.syncer.outboxFlushTimeout = time.Hour
		run.stats.started = time.Now()
		b.StartTimer()
		err := run.syncer.FlushOutboxOnce(context.Background())
		b.StopTimer()
		b.ReportMetric(float64(run.stats.bulkPosts.Load()), "bulk_posts/op")
		b.ReportMetric(float64(run.stats.firstPost.Milliseconds()), "first_post_ms/op")
		b.ReportMetric(float64(run.stats.maxInFlight.Load()), "max_in_flight")
		b.ReportMetric(float64(run.stats.requests.Load()), "http_requests/op")
		settleLaggingReceipts(b, run.syncer, err)
		if outbox := run.syncer.summarizeOutbox(); outbox.Pending != 0 || outbox.NeedsAttention != 0 {
			b.Fatalf("outbox not drained: %+v", outbox)
		}
		run.close()
		b.StartTimer()
	}
}
