package mountsync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// concurrentBulkClient is a goroutine-safe WriteFilesBulk fake. Every other
// RemoteClient method falls through to fakeClient, which the Syncer only calls
// from its lock-holding goroutine.
type concurrentBulkClient struct {
	*fakeClient

	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	uploads     map[string]int // remote path -> POSTs that carried it
	calls       int
	// overlap, when > 1, holds each POST until that many are in flight (or a
	// short timeout passes) so the test observes real overlap.
	overlap int
	arrived *sync.Cond
	// failFirstPath fails, once, the POST whose first file has this path.
	failFirstPath string
	failErr       error
	failed        bool
	// holdOthers delays every non-failing POST, so a failing one completes
	// first and its result is buffered while an earlier head is in flight.
	holdOthers time.Duration
	// failureSeen is set once a failing POST has returned; every POST of
	// the initial window has started by then (the failure is delayed by
	// failDelay), so startsAfterFailure counts only refills sent after the
	// failure was observable.
	failDelay          time.Duration
	failureSeen        bool
	startsAfterFailure int
}

func newConcurrentBulkClient(overlap int) *concurrentBulkClient {
	c := &concurrentBulkClient{
		fakeClient: &fakeClient{files: map[string]RemoteFile{}},
		uploads:    map[string]int{},
		overlap:    overlap,
	}
	c.arrived = sync.NewCond(&c.mu)
	return c
}

func (c *concurrentBulkClient) WriteFilesBulk(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
	c.mu.Lock()
	if c.failureSeen {
		c.startsAfterFailure++
	}
	c.calls++
	c.inFlight++
	if c.inFlight > c.maxInFlight {
		c.maxInFlight = c.inFlight
	}
	c.arrived.Broadcast()
	deadline := time.Now().Add(200 * time.Millisecond)
	for c.overlap > 1 && c.inFlight < c.overlap && time.Now().Before(deadline) {
		// sync.Cond has no timed wait; poll briefly while releasing the lock.
		c.mu.Unlock()
		time.Sleep(time.Millisecond)
		c.mu.Lock()
	}
	fail := !c.failed && c.failFirstPath != "" && files[0].Path == c.failFirstPath
	if fail {
		c.failed = true
	} else {
		for _, file := range files {
			c.uploads[file.Path]++
		}
	}
	c.mu.Unlock()

	// Hold briefly so concurrently dispatched chunks really overlap.
	switch {
	case fail:
		time.Sleep(c.failDelay)
	case c.holdOthers > 0:
		time.Sleep(c.holdOthers)
	default:
		time.Sleep(5 * time.Millisecond)
	}

	c.mu.Lock()
	c.inFlight--
	if fail {
		c.failureSeen = true
	}
	c.mu.Unlock()
	if fail {
		return BulkWriteResponse{}, c.failErr
	}
	response := BulkWriteResponse{Written: len(files), CorrelationID: "corr"}
	for _, file := range files {
		response.Results = append(response.Results, BulkWriteResult{Path: file.Path, Revision: "rev_" + strings.TrimPrefix(file.Path, "/repo/")})
	}
	return response, nil
}

// seedConcurrencyOutbox persists n pending records of ~1 KiB in FirstSeenAt
// order and returns their remote paths in that order.
func seedConcurrencyOutbox(t *testing.T, syncer *Syncer, n int) []string {
	t.Helper()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	paths := make([]string, 0, n)
	for i := 0; i < n; i++ {
		remotePath := fmt.Sprintf("/repo/f%03d.txt", i)
		content := fmt.Sprintf("%03d:%s", i, strings.Repeat("x", 1000))
		record := outboxRecord{
			CommandID:        fmt.Sprintf("mountcmd_conc_%03d", i),
			WorkspaceID:      syncer.workspace,
			RemotePath:       remotePath,
			ContentType:      "text/plain",
			Content:          content,
			Hash:             hashString(content),
			Status:           outboxStatusPending,
			FirstSeenAt:      base.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			ExpectedRevision: "0",
		}
		record.CorrelationID = record.CommandID
		if err := syncer.saveOutboxRecord(record); err != nil {
			t.Fatalf("seed outbox record: %v", err)
		}
		paths = append(paths, remotePath)
	}
	return paths
}

func newConcurrencySyncer(t *testing.T, client RemoteClient, concurrency int) (*Syncer, string) {
	t.Helper()
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_bulk_concurrency",
		RemoteRoot:  "/repo",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	syncer.bulkUploadConcurrency = concurrency
	return syncer, localDir
}

func outboxRecordsByPathForTest(t *testing.T, localDir, status string) map[string]outboxRecord {
	t.Helper()
	out := map[string]outboxRecord{}
	for _, record := range readOutboxRecordsInDirForTest(t, filepath.Join(localDir, ".relay", "outbox", status)) {
		if _, dup := out[record.RemotePath]; dup {
			t.Fatalf("duplicate %s outbox record for %s", status, record.RemotePath)
		}
		out[record.RemotePath] = record
	}
	return out
}

func TestFlushOutboxChunksBoundsInFlightAndAcksEachRecordOnce(t *testing.T) {
	// ~1 KiB records under a 4 KiB request cap: three records per chunk, so
	// 30 records drain as 10 chunks.
	t.Setenv("RELAYFILE_MAX_WRITEBACK_BATCH_BYTES", "4096")
	client := newConcurrentBulkClient(3)
	syncer, localDir := newConcurrencySyncer(t, client, 3)
	paths := seedConcurrencyOutbox(t, syncer, 30)

	if err := syncer.FlushOutboxOnce(context.Background()); err != nil {
		t.Fatalf("FlushOutboxOnce: %v", err)
	}

	if client.calls != 10 {
		t.Fatalf("bulk POSTs = %d, want 10 (chunk boundaries changed)", client.calls)
	}
	if client.maxInFlight != 3 {
		t.Fatalf("max in-flight POSTs = %d, want exactly the configured 3", client.maxInFlight)
	}
	acked := outboxRecordsByPathForTest(t, localDir, "acked")
	pending := outboxRecordsByPathForTest(t, localDir, "pending")
	if len(pending) != 0 {
		t.Fatalf("pending after flush = %d, want 0", len(pending))
	}
	for _, path := range paths {
		if client.uploads[path] != 1 {
			t.Fatalf("%s uploaded %d times, want 1", path, client.uploads[path])
		}
		if _, ok := acked[path]; !ok {
			t.Fatalf("%s not acked", path)
		}
		if got := syncer.state.Files[path].Revision; got != "rev_"+strings.TrimPrefix(path, "/repo/") {
			t.Fatalf("%s tracked revision = %q", path, got)
		}
	}
	if len(acked) != len(paths) {
		t.Fatalf("acked = %d, want %d", len(acked), len(paths))
	}
}

func TestFlushOutboxChunksSerialWhenConcurrencyIsOne(t *testing.T) {
	t.Setenv("RELAYFILE_MAX_WRITEBACK_BATCH_BYTES", "4096")
	client := newConcurrentBulkClient(1)
	syncer, _ := newConcurrencySyncer(t, client, 1)
	seedConcurrencyOutbox(t, syncer, 12)
	if err := syncer.FlushOutboxOnce(context.Background()); err != nil {
		t.Fatalf("FlushOutboxOnce: %v", err)
	}
	if client.maxInFlight != 1 || client.calls != 4 {
		t.Fatalf("serial flush: maxInFlight=%d calls=%d, want 1 and 4", client.maxInFlight, client.calls)
	}
}

// A 429 workspace_busy that outlives the client's own Retry-After retries
// fails only its chunk: those records stay pending with backoff, the chunk
// in flight beside it is still acked (never re-sent), no later chunk is
// dispatched, and the next flush delivers the rest exactly once.
func TestFlushOutboxChunksWorkspaceBusyKeepsAccountingExact(t *testing.T) {
	t.Setenv("RELAYFILE_MAX_WRITEBACK_BATCH_BYTES", "4096")
	client := newConcurrentBulkClient(2)
	syncer, localDir := newConcurrencySyncer(t, client, 2)
	paths := seedConcurrencyOutbox(t, syncer, 30)
	// Chunks are [0-2] [3-5] [6-8] ...; fail the second chunk.
	client.failFirstPath = paths[3]
	client.failErr = &HTTPError{StatusCode: http.StatusTooManyRequests, Code: "workspace_busy", Message: "workspace busy"}

	err := syncer.FlushOutboxOnce(context.Background())
	if err == nil {
		t.Fatalf("FlushOutboxOnce succeeded despite a workspace_busy chunk (failed=%v calls=%d)", client.failed, client.calls)
	}

	acked := outboxRecordsByPathForTest(t, localDir, "acked")
	pending := outboxRecordsByPathForTest(t, localDir, "pending")
	for path := range acked {
		if _, both := pending[path]; both {
			t.Fatalf("%s is both acked and pending", path)
		}
	}
	if len(acked)+len(pending) != len(paths) {
		t.Fatalf("acked %d + pending %d != %d records", len(acked), len(pending), len(paths))
	}
	for _, path := range paths[:3] {
		if _, ok := acked[path]; !ok {
			t.Fatalf("%s from the chunk before the failure was not acked", path)
		}
	}
	for _, path := range paths[3:6] {
		record, ok := pending[path]
		if !ok {
			t.Fatalf("%s from the failed chunk is not pending", path)
		}
		if record.AttemptCount != 1 || record.NextAttemptAt == "" || !strings.Contains(record.LastError, "busy") {
			t.Fatalf("%s failed-chunk record = attempts %d next %q err %q, want one backed-off attempt", path, record.AttemptCount, record.NextAttemptAt, record.LastError)
		}
	}
	// The failure was observed before the window refilled, so the third
	// chunk was never sent: it stays pending with no attempt recorded.
	for _, path := range paths[6:9] {
		record, ok := pending[path]
		if !ok || record.AttemptCount != 0 || client.uploads[path] != 0 {
			t.Fatalf("%s after the failed chunk: pending=%v attempts=%d uploads=%d, want never sent", path, ok, record.AttemptCount, client.uploads[path])
		}
	}
	for path := range pending {
		if client.uploads[path] != 0 {
			t.Fatalf("pending %s was already accepted by the server %d time(s)", path, client.uploads[path])
		}
	}
	if client.calls != 2 {
		t.Fatalf("chunks after the failure were dispatched: %d POSTs, want 2", client.calls)
	}

	// The next drain (FlushOutboxOnce forces due) delivers everything left.
	if err := syncer.FlushOutboxOnce(context.Background()); err != nil {
		t.Fatalf("second FlushOutboxOnce: %v", err)
	}
	acked = outboxRecordsByPathForTest(t, localDir, "acked")
	if pending := outboxRecordsByPathForTest(t, localDir, "pending"); len(pending) != 0 {
		t.Fatalf("pending after recovery = %d", len(pending))
	}
	for _, path := range paths {
		if client.uploads[path] != 1 {
			t.Fatalf("%s accepted %d times across both flushes, want exactly 1", path, client.uploads[path])
		}
		if _, ok := acked[path]; !ok {
			t.Fatalf("%s not acked after recovery", path)
		}
	}
	if client.maxInFlight > 2 {
		t.Fatalf("max in-flight = %d exceeds the configured 2", client.maxInFlight)
	}
}

// Two pending commands for one path must never be in flight together.
func TestFlushOutboxChunksSerializesSamePathAcrossChunks(t *testing.T) {
	client := newConcurrentBulkClient(1)
	var order []string
	var orderMu sync.Mutex
	syncer, _ := newConcurrencySyncer(t, &pathOrderClient{concurrentBulkClient: client, order: &order, mu: &orderMu}, 3)
	var chunks [][]outboxRecord
	for i, remotePath := range []string{"/repo/a.txt", "/repo/b.txt", "/repo/a.txt"} {
		record := outboxRecord{
			CommandID:   fmt.Sprintf("mountcmd_samepath_%d", i),
			WorkspaceID: syncer.workspace,
			RemotePath:  remotePath,
			ContentType: "text/plain",
			Content:     fmt.Sprintf("v%d", i),
			Hash:        hashString(fmt.Sprintf("v%d", i)),
			Status:      outboxStatusPending,
			FirstSeenAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		if err := syncer.saveOutboxRecord(record); err != nil {
			t.Fatalf("seed: %v", err)
		}
		chunks = append(chunks, []outboxRecord{record})
	}
	syncer.mu.Lock()
	err := syncer.flushOutboxChunks(context.Background(), chunks, map[string]struct{}{}, true, false)
	syncer.mu.Unlock()
	if err != nil {
		t.Fatalf("flushOutboxChunks: %v", err)
	}
	if client.maxInFlight > 2 {
		t.Fatalf("max in flight = %d, want <= 2 (a.txt twice must not overlap)", client.maxInFlight)
	}
	orderMu.Lock()
	defer orderMu.Unlock()
	firstA, secondA := -1, -1
	for i, event := range order {
		switch event {
		case "end:/repo/a.txt":
			if firstA < 0 {
				firstA = i
			}
		case "start:/repo/a.txt":
			if firstA >= 0 {
				secondA = i
			}
		}
	}
	if firstA < 0 || secondA < 0 || secondA < firstA {
		t.Fatalf("second a.txt upload started before the first finished: %v", order)
	}
}

type pathOrderClient struct {
	*concurrentBulkClient
	order *[]string
	mu    *sync.Mutex
}

func (c *pathOrderClient) WriteFilesBulk(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
	c.mu.Lock()
	*c.order = append(*c.order, "start:"+files[0].Path)
	c.mu.Unlock()
	response, err := c.concurrentBulkClient.WriteFilesBulk(ctx, workspaceID, files)
	c.mu.Lock()
	*c.order = append(*c.order, "end:"+files[0].Path)
	c.mu.Unlock()
	return response, err
}

// With concurrency above 1, once any chunk has failed — a workspace_busy or
// transport error that completed while an earlier chunk was still in flight,
// or a failed receipt in a chunk being prepared — no further chunk may be
// sent, matching the serial loop's stop-on-first-error.
func TestFlushOutboxChunksSendsNothingAfterFirstFailure(t *testing.T) {
	failures := map[string]func(c *concurrentBulkClient, s *Syncer, paths []string){
		"workspace_busy": func(c *concurrentBulkClient, _ *Syncer, paths []string) {
			c.failFirstPath = paths[3]
			c.failErr = &HTTPError{StatusCode: http.StatusTooManyRequests, Code: "workspace_busy", Message: "workspace busy"}
		},
		"transport": func(c *concurrentBulkClient, _ *Syncer, paths []string) {
			c.failFirstPath = paths[3]
			c.failErr = errors.New("connection reset by peer")
		},
		"receipt": func(c *concurrentBulkClient, s *Syncer, _ []string) {
			// The second chunk holds a record whose provider dispatch already
			// failed plus two new records; only those two may still upload.
			record, err := s.readOutboxRecord(s.pendingOutboxPath("mountcmd_conc_003"))
			if err != nil {
				t.Fatalf("read seeded record: %v", err)
			}
			record.OpID = "op_failed"
			if err := s.saveOutboxRecord(record); err != nil {
				t.Fatalf("save receipt record: %v", err)
			}
			c.fakeClient.operations = map[string]OperationStatus{"op_failed": {OpID: "op_failed", Status: "failed"}}
		},
	}
	for name, inject := range failures {
		for _, limit := range []int{2, 8} {
			t.Run(fmt.Sprintf("%s/concurrency=%d", name, limit), func(t *testing.T) {
				t.Setenv("RELAYFILE_MAX_WRITEBACK_BATCH_BYTES", "4096")
				client := newConcurrentBulkClient(1)
				client.holdOthers = 80 * time.Millisecond
				client.failDelay = 10 * time.Millisecond
				syncer, localDir := newConcurrencySyncer(t, client, limit)
				paths := seedConcurrencyOutbox(t, syncer, 30)
				inject(client, syncer, paths)

				if err := syncer.FlushOutboxOnce(context.Background()); err == nil {
					t.Fatal("FlushOutboxOnce succeeded despite a failed chunk")
				}
				if client.startsAfterFailure != 0 {
					t.Fatalf("%d chunk(s) were sent after the failure was observable (calls=%d)", client.startsAfterFailure, client.calls)
				}
				if name == "receipt" && client.calls != 2 {
					// The receipt fails while preparing the second chunk: the
					// first chunk and the second chunk's own new records are
					// the only POSTs, whatever the concurrency.
					t.Fatalf("receipt failure: %d POSTs, want 2", client.calls)
				}
				if limit == 2 && client.calls != 2 {
					t.Fatalf("concurrency 2: %d POSTs, want only the two dispatched before the failure", client.calls)
				}
				if client.calls > limit {
					t.Fatalf("%d POSTs exceed the initial window of %d", client.calls, limit)
				}
				acked := outboxRecordsByPathForTest(t, localDir, "acked")
				pending := outboxRecordsByPathForTest(t, localDir, "pending")
				for path := range pending {
					if _, both := acked[path]; both {
						t.Fatalf("%s both acked and pending", path)
					}
					if client.uploads[path] != 0 {
						t.Fatalf("pending %s was accepted by the server", path)
					}
				}
				for path := range acked {
					if client.uploads[path] != 1 {
						t.Fatalf("acked %s accepted %d times", path, client.uploads[path])
					}
				}
			})
		}
	}
}
