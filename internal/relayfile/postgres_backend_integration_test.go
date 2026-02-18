package relayfile

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var postgresIntegrationCounter uint64

func TestPostgresIntegrationStateBackendRoundTrip(t *testing.T) {
	dsn := postgresIntegrationDSN(t)

	backend, err := NewPostgresStateBackend(dsn)
	if err != nil {
		t.Fatalf("new postgres state backend: %v", err)
	}
	pg, ok := backend.(*PostgresStateBackend)
	if !ok {
		t.Fatalf("expected *PostgresStateBackend, got %T", backend)
	}
	pg.tableName = postgresIntegrationTableName("relayfile_state_it")
	pg.stateKey = "it"
	t.Cleanup(func() {
		_ = backend.(interface{ Close() error }).Close()
		postgresIntegrationDropTable(t, dsn, pg.tableName)
	})

	snapshot, err := backend.Load()
	if err != nil {
		t.Fatalf("initial load failed: %v", err)
	}
	if snapshot != nil {
		t.Fatalf("expected nil initial snapshot, got %+v", snapshot)
	}

	saved := &persistedState{
		RevCounter:         7,
		OpCounter:          3,
		EventCounter:       11,
		Workspaces:         map[string]*workspaceState{},
		EnvelopesByID:      map[string]WebhookEnvelopeRequest{},
		DeliveryIndex:      map[string]string{},
		ProcessedEnvs:      map[string]bool{},
		IngressByWorkspace: map[string]ingressCounter{},
	}
	if err := backend.Save(saved); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := backend.Load()
	if err != nil {
		t.Fatalf("load after save failed: %v", err)
	}
	if loaded == nil {
		t.Fatalf("expected non-nil snapshot after save")
	}
	if loaded.RevCounter != 7 || loaded.OpCounter != 3 || loaded.EventCounter != 11 {
		t.Fatalf("unexpected loaded counters: %+v", loaded)
	}

	loaded.RevCounter = 12
	if err := backend.Save(loaded); err != nil {
		t.Fatalf("second save failed: %v", err)
	}
	reloaded, err := backend.Load()
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if reloaded == nil || reloaded.RevCounter != 12 {
		t.Fatalf("expected revCounter 12 after update, got %+v", reloaded)
	}
}

func TestPostgresIntegrationEnvelopeQueueFIFOAndCapacity(t *testing.T) {
	dsn := postgresIntegrationDSN(t)

	queue, err := NewPostgresEnvelopeQueue(dsn, 2)
	if err != nil {
		t.Fatalf("new postgres envelope queue: %v", err)
	}
	pg, ok := queue.(*PostgresEnvelopeQueue)
	if !ok {
		t.Fatalf("expected *PostgresEnvelopeQueue, got %T", queue)
	}
	pg.core.tableName = postgresIntegrationTableName("relayfile_envq_it")
	pg.core.queueKey = postgresIntegrationTableName("qk")
	t.Cleanup(func() {
		_ = queue.Close()
		postgresIntegrationDropTable(t, dsn, pg.core.tableName)
	})

	if !queue.TryEnqueue("env_a") {
		t.Fatalf("expected enqueue env_a to succeed")
	}
	if !queue.TryEnqueue("env_b") {
		t.Fatalf("expected enqueue env_b to succeed")
	}
	if queue.TryEnqueue("env_c") {
		t.Fatalf("expected enqueue env_c to fail at capacity")
	}
	if got := queue.Depth(); got != 2 {
		t.Fatalf("expected depth 2, got %d", got)
	}

	snapshotter, ok := queue.(envelopeQueueSnapshotter)
	if !ok {
		t.Fatalf("expected envelope queue snapshotter")
	}
	snapshot := snapshotter.SnapshotEnvelopeIDs()
	if len(snapshot) != 2 || snapshot[0] != "env_a" || snapshot[1] != "env_b" {
		t.Fatalf("unexpected snapshot order/content: %+v", snapshot)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	first, ok := queue.Dequeue(ctx)
	if !ok || first != "env_a" {
		t.Fatalf("expected first dequeue env_a, got ok=%v value=%q", ok, first)
	}
	second, ok := queue.Dequeue(ctx)
	if !ok || second != "env_b" {
		t.Fatalf("expected second dequeue env_b, got ok=%v value=%q", ok, second)
	}

	emptyCtx, emptyCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer emptyCancel()
	if _, ok := queue.Dequeue(emptyCtx); ok {
		t.Fatalf("expected empty dequeue to return false")
	}
}

func TestPostgresIntegrationWritebackQueueRestartPersistence(t *testing.T) {
	dsn := postgresIntegrationDSN(t)
	tableName := postgresIntegrationTableName("relayfile_wbq_it")
	queueKey := postgresIntegrationTableName("qk")

	queue, err := NewPostgresWritebackQueue(dsn, 2)
	if err != nil {
		t.Fatalf("new postgres writeback queue: %v", err)
	}
	firstQueue, ok := queue.(*PostgresWritebackQueue)
	if !ok {
		t.Fatalf("expected *PostgresWritebackQueue, got %T", queue)
	}
	firstQueue.core.tableName = tableName
	firstQueue.core.queueKey = queueKey
	t.Cleanup(func() {
		_ = queue.Close()
		postgresIntegrationDropTable(t, dsn, tableName)
	})

	taskA := WritebackQueueItem{WorkspaceID: "ws_1", OpID: "op_a", Path: "/a", Revision: "rev_1", CorrelationID: "corr_1"}
	taskB := WritebackQueueItem{WorkspaceID: "ws_1", OpID: "op_b", Path: "/b", Revision: "rev_2", CorrelationID: "corr_2"}
	taskC := WritebackQueueItem{WorkspaceID: "ws_1", OpID: "op_c", Path: "/c", Revision: "rev_3", CorrelationID: "corr_3"}

	if !queue.TryEnqueue(taskA) {
		t.Fatalf("expected enqueue taskA to succeed")
	}
	if !queue.TryEnqueue(taskB) {
		t.Fatalf("expected enqueue taskB to succeed")
	}
	if queue.TryEnqueue(taskC) {
		t.Fatalf("expected enqueue taskC to fail at capacity")
	}

	snapshotter, ok := queue.(writebackQueueSnapshotter)
	if !ok {
		t.Fatalf("expected writeback queue snapshotter")
	}
	snapshot := snapshotter.SnapshotWritebacks()
	if len(snapshot) != 2 || snapshot[0].OpID != "op_a" || snapshot[1].OpID != "op_b" {
		t.Fatalf("unexpected writeback snapshot order/content: %+v", snapshot)
	}

	if err := queue.Close(); err != nil {
		t.Fatalf("close first queue failed: %v", err)
	}

	reopenedRaw, err := NewPostgresWritebackQueue(dsn, 2)
	if err != nil {
		t.Fatalf("reopen postgres writeback queue: %v", err)
	}
	reopened, ok := reopenedRaw.(*PostgresWritebackQueue)
	if !ok {
		t.Fatalf("expected *PostgresWritebackQueue on reopen, got %T", reopenedRaw)
	}
	reopened.core.tableName = tableName
	reopened.core.queueKey = queueKey
	queue = reopenedRaw

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	first, ok := reopenedRaw.Dequeue(ctx)
	if !ok || first.OpID != "op_a" {
		t.Fatalf("expected first dequeued op_a, got ok=%v item=%+v", ok, first)
	}
	second, ok := reopenedRaw.Dequeue(ctx)
	if !ok || second.OpID != "op_b" {
		t.Fatalf("expected second dequeued op_b, got ok=%v item=%+v", ok, second)
	}
}

func TestPostgresIntegrationEnvelopeQueueCapacityUnderConcurrentEnqueue(t *testing.T) {
	dsn := postgresIntegrationDSN(t)

	queue, err := NewPostgresEnvelopeQueue(dsn, 1)
	if err != nil {
		t.Fatalf("new postgres envelope queue: %v", err)
	}
	pg, ok := queue.(*PostgresEnvelopeQueue)
	if !ok {
		t.Fatalf("expected *PostgresEnvelopeQueue, got %T", queue)
	}
	pg.core.tableName = postgresIntegrationTableName("relayfile_envq_race_it")
	pg.core.queueKey = postgresIntegrationTableName("qk")
	t.Cleanup(func() {
		_ = queue.Close()
		postgresIntegrationDropTable(t, dsn, pg.core.tableName)
	})

	const producers = 16
	var successCount atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < producers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if queue.TryEnqueue(fmt.Sprintf("env_%d", n)) {
				successCount.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if got := successCount.Load(); got != 1 {
		t.Fatalf("expected exactly 1 successful enqueue at capacity=1, got %d", got)
	}
	if depth := queue.Depth(); depth != 1 {
		t.Fatalf("expected queue depth 1 after concurrent enqueue, got %d", depth)
	}
}

func postgresIntegrationDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("RELAYFILE_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set RELAYFILE_TEST_POSTGRES_DSN to run Postgres integration tests")
	}
	return dsn
}

func postgresIntegrationTableName(prefix string) string {
	n := atomic.AddUint64(&postgresIntegrationCounter, 1)
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano(), n)
}

func postgresIntegrationDropTable(t *testing.T, dsn, tableName string) {
	t.Helper()
	if strings.TrimSpace(dsn) == "" || strings.TrimSpace(tableName) == "" {
		return
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres for cleanup failed: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	query := fmt.Sprintf("DROP TABLE IF EXISTS %s", postgresQuoteIdentifier(tableName))
	if _, err := db.ExecContext(ctx, query); err != nil {
		t.Fatalf("drop cleanup table %q failed: %v", tableName, err)
	}
}
