package mountsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/relayfile"
)

// Deterministic end-to-end coverage of the outbound writeback path
// (local write -> outbox -> cloud /fs/bulk -> dispatch-receipt poll -> ack),
// plus retry-backoff, dead-letter, and schema-violation. The cloud is an
// httptest server whose per-route behavior each subtest controls; time is an
// injected clock (SyncerOptions.Now) so retry/backoff timing is exact with no
// real sleeps.

// fakeClock is a deterministic, advanceable clock for the outbox path.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{t: start} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// writebackTestCloud is a controllable httptest cloud. Only /fs/bulk and the
// operation-poll route carry test-specific behavior; the pull routes return
// empty so SyncOnce's mirror phase is a no-op and the test isolates writeback.
type writebackTestCloud struct {
	*httptest.Server

	mu sync.Mutex
	// bulk returns the HTTP status + response body for a POST /fs/bulk call.
	// callNum is 1-based.
	bulk func(callNum int, files []BulkWriteFile) (int, BulkWriteResponse)
	// op returns the HTTP status + body for a GET /ops/{opID} poll.
	op func(callNum int, opID string) (int, OperationStatus)

	bulkCalls int
	opCalls   int
	lastFiles []BulkWriteFile
}

func newWritebackTestCloud(t *testing.T) *writebackTestCloud {
	t.Helper()
	c := &writebackTestCloud{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/fs/tree") && r.Method == http.MethodGet:
			writeJSONResponse(t, w, http.StatusOK, TreeResponse{Path: "/", Entries: []TreeEntry{}, NextCursor: nil})
		case strings.HasSuffix(path, "/fs/events") && r.Method == http.MethodGet:
			writeJSONResponse(t, w, http.StatusOK, EventFeed{Events: []FilesystemEvent{}, NextCursor: nil})
		case strings.HasSuffix(path, "/fs/export") && r.Method == http.MethodGet:
			writeJSONResponse(t, w, http.StatusOK, []RemoteFile{})
		case strings.HasSuffix(path, "/fs/file") && r.Method == http.MethodGet:
			writeJSONResponse(t, w, http.StatusNotFound, map[string]any{"code": "not_found", "message": "not found"})
		case strings.Contains(path, "/ops/") && r.Method == http.MethodGet:
			opID := path[strings.LastIndex(path, "/ops/")+len("/ops/"):]
			c.mu.Lock()
			c.opCalls++
			n := c.opCalls
			handler := c.op
			c.mu.Unlock()
			if handler == nil {
				writeJSONResponse(t, w, http.StatusNotFound, map[string]any{"code": "not_found"})
				return
			}
			status, body := handler(n, opID)
			writeJSONResponse(t, w, status, body)
		case strings.HasSuffix(path, "/fs/bulk") && r.Method == http.MethodPost:
			var payload struct {
				Files []BulkWriteFile `json:"files"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				writeJSONResponse(t, w, http.StatusBadRequest, map[string]any{"code": "bad_request"})
				return
			}
			c.mu.Lock()
			c.bulkCalls++
			n := c.bulkCalls
			c.lastFiles = payload.Files
			handler := c.bulk
			c.mu.Unlock()
			if handler == nil {
				writeJSONResponse(t, w, http.StatusServiceUnavailable, map[string]any{"code": "unavailable"})
				return
			}
			status, body := handler(n, payload.Files)
			writeJSONResponse(t, w, status, body)
		default:
			writeJSONResponse(t, w, http.StatusNotFound, map[string]any{"code": "not_found", "path": path})
		}
	}))
	t.Cleanup(c.Server.Close)
	return c
}

func (c *writebackTestCloud) bulkCallCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bulkCalls
}

func (c *writebackTestCloud) opCallCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opCalls
}

// newWritebackSyncer wires a Syncer at NewHTTPClient(cloud) with the injected
// clock and a seeded local draft file that becomes one pending outbox command.
func newWritebackSyncer(t *testing.T, cloud *writebackTestCloud, clock *fakeClock) (*Syncer, string, string) {
	t.Helper()
	const workspaceID = "ws_writeback_e2e"
	localDir := t.TempDir()
	// A slack draft is the canonical writeback shape: a brand-new local file
	// with no remote counterpart becomes exactly one outbox command.
	draftID := "5ab77d67-1111-4111-8111-123456789abc"
	remotePath := "/slack/channels/C123/messages/messages " + draftID + ".json"
	messageDir := filepath.Join(localDir, "slack", "channels", "C123", "messages")
	if err := os.MkdirAll(messageDir, 0o755); err != nil {
		t.Fatalf("mkdir message dir: %v", err)
	}
	content := "{\"channel\":\"C123\",\"text\":\"hello writeback e2e\"}\n"
	if err := os.WriteFile(filepath.Join(messageDir, "messages "+draftID+".json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write draft: %v", err)
	}

	websocketEnabled := false
	client := NewHTTPClient(cloud.Server.URL, "test-token", cloud.Server.Client())
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/",
		LocalRoot:   localDir,
		WebSocket:   &websocketEnabled,
		Now:         clock.now,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	return syncer, localDir, remotePath
}

func ackedOutbox(t *testing.T, localDir string) []outboxRecord {
	return readOutboxRecordsInDirForTest(t, filepath.Join(localDir, ".relay", "outbox", "acked"))
}

func failedOutbox(t *testing.T, localDir string) []outboxRecord {
	return readOutboxRecordsInDirForTest(t, filepath.Join(localDir, ".relay", "outbox", "failed"))
}

// okBulkWithOpID makes /fs/bulk accept every file and hand back an opId +
// initial dispatch state, exercising the dispatch-receipt (poll) path.
func okBulkWithOpID(opID string) func(int, []BulkWriteFile) (int, BulkWriteResponse) {
	return func(_ int, files []BulkWriteFile) (int, BulkWriteResponse) {
		results := make([]BulkWriteResult, 0, len(files))
		for i, f := range files {
			results = append(results, BulkWriteResult{
				Path:      normalizeRemotePath(f.Path),
				Revision:  fmt.Sprintf("rev_%d", i+1),
				OpID:      opID,
				Writeback: &relayfile.BulkWriteWritebackResult{Provider: "slack", State: "queued"},
			})
		}
		return http.StatusAccepted, BulkWriteResponse{
			Written: len(files), Results: results, CorrelationID: "corr_e2e",
		}
	}
}

func TestWritebackE2E_UploadPollAck(t *testing.T) {
	cloud := newWritebackTestCloud(t)
	clock := newFakeClock(time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC))
	cloud.bulk = okBulkWithOpID("op_ack_1")
	cloud.op = func(_ int, opID string) (int, OperationStatus) {
		return http.StatusOK, OperationStatus{OpID: opID, Status: "succeeded", Revision: "rev_1"}
	}

	syncer, localDir, _ := newWritebackSyncer(t, cloud, clock)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	if got := cloud.bulkCallCount(); got != 1 {
		t.Fatalf("bulk calls = %d, want 1", got)
	}
	if got := cloud.opCallCount(); got != 1 {
		t.Fatalf("op poll calls = %d, want 1", got)
	}
	if pending := readPendingOutboxRecordsForTest(t, localDir); len(pending) != 0 {
		t.Fatalf("pending = %d, want 0 (acked)", len(pending))
	}
	acked := ackedOutbox(t, localDir)
	if len(acked) != 1 {
		t.Fatalf("acked = %d, want 1", len(acked))
	}
	if acked[0].DispatchStatus != "succeeded" {
		t.Fatalf("dispatch status = %q, want succeeded", acked[0].DispatchStatus)
	}
	if acked[0].OpID != "op_ack_1" {
		t.Fatalf("opId = %q, want op_ack_1", acked[0].OpID)
	}
	if strings.TrimSpace(acked[0].AckedAt) == "" {
		t.Fatal("ackedAt not set")
	}
}

func TestWritebackE2E_PollPendingThenAck(t *testing.T) {
	cloud := newWritebackTestCloud(t)
	clock := newFakeClock(time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC))
	cloud.bulk = okBulkWithOpID("op_poll_1")
	// First poll: still running. Second poll: succeeded.
	cloud.op = func(callNum int, opID string) (int, OperationStatus) {
		if callNum == 1 {
			return http.StatusOK, OperationStatus{OpID: opID, Status: "running"}
		}
		return http.StatusOK, OperationStatus{OpID: opID, Status: "succeeded", Revision: "rev_1"}
	}

	syncer, localDir, _ := newWritebackSyncer(t, cloud, clock)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	// After the first cycle the op is still running -> record stays pending
	// with the opId recorded, not acked.
	pending := readPendingOutboxRecordsForTest(t, localDir)
	if len(pending) != 1 {
		t.Fatalf("after running poll: pending = %d, want 1", len(pending))
	}
	if pending[0].OpID != "op_poll_1" {
		t.Fatalf("pending opId = %q, want op_poll_1", pending[0].OpID)
	}
	if len(ackedOutbox(t, localDir)) != 0 {
		t.Fatal("must not be acked while op is running")
	}

	// A record that already carries an opId is re-polled directly, regardless of
	// backoff timing. Second flush -> op succeeded -> acked.
	if err := syncer.FlushOutboxOnce(context.Background()); err != nil {
		t.Fatalf("FlushOutboxOnce: %v", err)
	}
	if len(readPendingOutboxRecordsForTest(t, localDir)) != 0 {
		t.Fatal("expected acked after succeeded poll")
	}
	if acked := ackedOutbox(t, localDir); len(acked) != 1 || acked[0].DispatchStatus != "succeeded" {
		t.Fatalf("acked = %+v, want one succeeded", acked)
	}
}

func TestWritebackE2E_OperationNotVisibleThenSucceeded(t *testing.T) {
	cloud := newWritebackTestCloud(t)
	clock := newFakeClock(time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC))
	cloud.bulk = okBulkWithOpID("op_eventually_visible")
	// The operation record may lag the accepted provider dispatch. Treat the
	// first 404 as receipt-pending, then surface the terminal receipt on the
	// next normal poll without re-uploading the Slack write.
	cloud.op = func(callNum int, opID string) (int, OperationStatus) {
		if callNum == 1 {
			return http.StatusNotFound, OperationStatus{OpID: opID}
		}
		return http.StatusOK, OperationStatus{OpID: opID, Status: "succeeded", Revision: "rev_1"}
	}

	syncer, localDir, _ := newWritebackSyncer(t, cloud, clock)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial dispatch with not-yet-visible receipt: %v", err)
	}
	pending := readPendingOutboxRecordsForTest(t, localDir)
	if len(pending) != 1 {
		t.Fatalf("after receipt 404: pending = %d, want 1", len(pending))
	}
	if pending[0].NeedsAttention {
		t.Fatalf("eventually-consistent receipt 404 must remain retryable: %+v", pending[0])
	}
	if pending[0].AttemptCount != 1 || strings.TrimSpace(pending[0].NextAttemptAt) == "" {
		t.Fatalf("receipt 404 was not scheduled for a later poll: %+v", pending[0])
	}
	if len(ackedOutbox(t, localDir)) != 0 {
		t.Fatal("receipt must not ack before terminal success")
	}

	clock.advance(outboxBackoff(1))
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("next poll after receipt became visible: %v", err)
	}
	if got := cloud.bulkCallCount(); got != 1 {
		t.Fatalf("slow receipt caused provider write re-upload: bulk calls = %d, want 1", got)
	}
	if got := cloud.opCallCount(); got != 2 {
		t.Fatalf("operation poll calls = %d, want 2", got)
	}
	if pending := readPendingOutboxRecordsForTest(t, localDir); len(pending) != 0 {
		t.Fatalf("terminal receipt was not surfaced on next poll: %+v", pending)
	}
	acked := ackedOutbox(t, localDir)
	if len(acked) != 1 || acked[0].OpID != "op_eventually_visible" || acked[0].DispatchStatus != "succeeded" {
		t.Fatalf("acked receipt = %+v, want eventual succeeded receipt", acked)
	}
	if acked[0].NeedsAttention || acked[0].LastError != "" || acked[0].NextAttemptAt != "" {
		t.Fatalf("terminal success retained pending retry state: %+v", acked[0])
	}
}

func TestWritebackE2E_TerminalOperationFailureFailsClosed(t *testing.T) {
	cloud := newWritebackTestCloud(t)
	clock := newFakeClock(time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC))
	cloud.bulk = okBulkWithOpID("op_terminal_failure")
	lastError := "Slack rejected the delivered message"
	cloud.op = func(_ int, opID string) (int, OperationStatus) {
		return http.StatusOK, OperationStatus{
			OpID:      opID,
			Status:    "failed",
			LastError: &lastError,
		}
	}

	syncer, localDir, _ := newWritebackSyncer(t, cloud, clock)
	err := syncer.SyncOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), lastError) {
		t.Fatalf("terminal provider receipt must fail closed, got %v", err)
	}
	if pending := readPendingOutboxRecordsForTest(t, localDir); len(pending) != 0 {
		t.Fatalf("terminal failure remained receiptless/pending: %+v", pending)
	}
	failed := failedOutbox(t, localDir)
	if len(failed) != 1 || failed[0].OpID != "op_terminal_failure" || failed[0].DispatchStatus != "failed" {
		t.Fatalf("failed terminal receipt = %+v, want one failed op receipt", failed)
	}
	if summary := syncer.summarizeOutbox(); summary.Failed != 1 || summary.NeedsAttention != 1 {
		t.Fatalf("terminal failure summary = %+v, want failed=1 and needsAttention=1", summary)
	}

	// A later otherwise-clean cycle must not erase the terminal provider
	// failure from mount health merely because the receipt left pending/.
	err = syncer.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("otherwise-clean cycle after terminal failure: %v", err)
	}
	publicBytes, err := os.ReadFile(filepath.Join(localDir, ".relay", "state.json"))
	if err != nil {
		t.Fatalf("read public state after terminal failure: %v", err)
	}
	var public publicState
	if err := json.Unmarshal(publicBytes, &public); err != nil {
		t.Fatalf("decode public state after terminal failure: %v", err)
	}
	if public.Status != "writeback-needs-attention" || !public.States.OutboxNeedsAttention {
		t.Fatalf("public state hid terminal provider failure: status=%q states=%+v", public.Status, public.States)
	}
	if public.Outbox.Failed != 1 || public.Outbox.NeedsAttention != 1 {
		t.Fatalf("public outbox summary = %+v, want failed=1 and needsAttention=1", public.Outbox)
	}
}

func TestWritebackE2E_RetryBackoffIsExponential(t *testing.T) {
	cloud := newWritebackTestCloud(t)
	start := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	clock := newFakeClock(start)
	// Every upload fails with a retryable server error.
	cloud.bulk = func(_ int, _ []BulkWriteFile) (int, BulkWriteResponse) {
		return http.StatusServiceUnavailable, BulkWriteResponse{}
	}

	syncer, localDir, _ := newWritebackSyncer(t, cloud, clock)

	// Attempt 1 (via SyncOnce). The flush error is expected/surfaced.
	_ = syncer.SyncOnce(context.Background())

	// outboxBackoff: 500ms, 1s, 2s, 4s ... computed off the SAME injected `now`
	// for LastAttemptAt and NextAttemptAt, so the delta is exact.
	for attempt := 1; attempt <= 3; attempt++ {
		pending := readPendingOutboxRecordsForTest(t, localDir)
		if len(pending) != 1 {
			t.Fatalf("attempt %d: pending = %d, want 1", attempt, len(pending))
		}
		rec := pending[0]
		if rec.AttemptCount != attempt {
			t.Fatalf("attempt %d: AttemptCount = %d", attempt, rec.AttemptCount)
		}
		last, err := time.Parse(time.RFC3339Nano, rec.LastAttemptAt)
		if err != nil {
			t.Fatalf("parse LastAttemptAt: %v", err)
		}
		next, err := time.Parse(time.RFC3339Nano, rec.NextAttemptAt)
		if err != nil {
			t.Fatalf("attempt %d: parse NextAttemptAt %q: %v", attempt, rec.NextAttemptAt, err)
		}
		if got, want := next.Sub(last), outboxBackoff(attempt); got != want {
			t.Fatalf("attempt %d: backoff = %s, want %s", attempt, got, want)
		}
		// Not due just before NextAttemptAt; due exactly at it.
		clock.advance(outboxBackoff(attempt) - time.Millisecond)
		if syncer.outboxDue(rec, clock.now().UTC()) {
			t.Fatalf("attempt %d: record due before NextAttemptAt", attempt)
		}
		clock.advance(time.Millisecond)
		if !syncer.outboxDue(rec, clock.now().UTC()) {
			t.Fatalf("attempt %d: record not due at NextAttemptAt", attempt)
		}
		// Next retry.
		_ = syncer.FlushOutboxOnce(context.Background())
	}
}

func TestWritebackE2E_DeadLetterAfterMaxAttempts(t *testing.T) {
	cloud := newWritebackTestCloud(t)
	clock := newFakeClock(time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC))
	cloud.bulk = func(_ int, _ []BulkWriteFile) (int, BulkWriteResponse) {
		return http.StatusServiceUnavailable, BulkWriteResponse{}
	}

	syncer, localDir, _ := newWritebackSyncer(t, cloud, clock)
	syncer.maxOutboxAttempts = 3 // shrink to keep the test tight

	_ = syncer.SyncOnce(context.Background())
	// Drive remaining attempts by advancing past each backoff window.
	for i := 0; i < 5; i++ {
		pending := readPendingOutboxRecordsForTest(t, localDir)
		if len(pending) != 1 {
			t.Fatalf("iteration %d: pending = %d, want 1", i, len(pending))
		}
		if pending[0].NeedsAttention {
			break
		}
		clock.advance(outboxBackoffMax)
		_ = syncer.FlushOutboxOnce(context.Background())
	}

	pending := readPendingOutboxRecordsForTest(t, localDir)
	if len(pending) != 1 {
		t.Fatalf("dead-letter: pending = %d, want 1 (held for attention)", len(pending))
	}
	rec := pending[0]
	if !rec.NeedsAttention {
		t.Fatalf("expected NeedsAttention after %d attempts, got attemptCount=%d", syncer.maxOutboxAttempts, rec.AttemptCount)
	}
	if rec.AttemptCount < syncer.maxOutboxAttempts {
		t.Fatalf("AttemptCount = %d, want >= %d", rec.AttemptCount, syncer.maxOutboxAttempts)
	}
	if strings.TrimSpace(rec.NextAttemptAt) != "" {
		t.Fatalf("dead-lettered record must not be rescheduled, NextAttemptAt = %q", rec.NextAttemptAt)
	}
	// A dead-lettered record is not due and is not retried.
	if syncer.outboxDue(rec, clock.now().Add(24*time.Hour).UTC()) {
		t.Fatal("dead-lettered record must never be due")
	}
	bulkBefore := cloud.bulkCallCount()
	_ = syncer.FlushOutboxOnce(context.Background())
	if cloud.bulkCallCount() != bulkBefore {
		t.Fatal("dead-lettered record must not be re-uploaded")
	}
	if s := syncer.summarizeOutbox(); s.NeedsAttention != 1 {
		t.Fatalf("summary NeedsAttention = %d, want 1", s.NeedsAttention)
	}
}

func TestWritebackE2E_SchemaViolationFailsAndQuarantines(t *testing.T) {
	cloud := newWritebackTestCloud(t)
	clock := newFakeClock(time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC))
	cloud.bulk = func(_ int, files []BulkWriteFile) (int, BulkWriteResponse) {
		errs := make([]BulkWriteError, 0, len(files))
		for _, f := range files {
			errs = append(errs, BulkWriteError{
				Path:    normalizeRemotePath(f.Path),
				Code:    "schema_validation_failed",
				Message: "body did not match the adapter schema: text is required",
			})
		}
		return http.StatusAccepted, BulkWriteResponse{ErrorCount: len(errs), Errors: errs, CorrelationID: "corr_schema"}
	}

	syncer, localDir, _ := newWritebackSyncer(t, cloud, clock)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	// A schema violation is terminal for the command: it moves to failed/, not
	// retried, and is NOT held as needs-attention (that's for transient retries).
	if pending := readPendingOutboxRecordsForTest(t, localDir); len(pending) != 0 {
		t.Fatalf("schema violation: pending = %d, want 0", len(pending))
	}
	failed := failedOutbox(t, localDir)
	if len(failed) != 1 {
		t.Fatalf("failed = %d, want 1", len(failed))
	}
	if !strings.Contains(failed[0].LastError, "schema") {
		t.Fatalf("failed record LastError = %q, want a schema message", failed[0].LastError)
	}
	// The offending local body is quarantined as a schema-invalid artifact.
	conflictsDir := filepath.Join(localDir, ".relay", "conflicts")
	var artifacts []string
	_ = filepath.Walk(conflictsDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			artifacts = append(artifacts, path)
		}
		return nil
	})
	if len(artifacts) == 0 {
		t.Fatalf("expected a schema-invalid artifact under %s", conflictsDir)
	}
}
