package relayfile

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"
)

const (
	postgresStateTableName          = "relayfile_state"
	postgresStateKey                = "default"
	postgresEnvelopeQueueTableName  = "relayfile_envelope_queue"
	postgresWritebackQueueTableName = "relayfile_writeback_queue"
	postgresQueueKey                = "default"
	postgresOperationTimeout        = 5 * time.Second
	postgresQueuePollInterval       = 10 * time.Millisecond
)

type sqlOpenFunc func(driverName, dsn string) (*sql.DB, error)

type PostgresStateBackend struct {
	dsn       string
	tableName string
	stateKey  string
	openDB    sqlOpenFunc

	initOnce sync.Once
	initErr  error
	db       *sql.DB
}

func NewPostgresStateBackend(dsn string) (StateBackend, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, ErrInvalidInput
	}
	return &PostgresStateBackend{
		dsn:       dsn,
		tableName: postgresStateTableName,
		stateKey:  postgresStateKey,
		openDB:    sql.Open,
	}, nil
}

func (b *PostgresStateBackend) Load() (*persistedState, error) {
	if b == nil {
		return nil, nil
	}
	if err := b.ensureReady(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), postgresOperationTimeout)
	defer cancel()

	query := fmt.Sprintf("SELECT snapshot FROM %s WHERE state_key = $1", postgresQuoteIdentifier(b.tableName))
	var payload string
	err := b.db.QueryRowContext(ctx, query, b.stateKey).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot persistedState
	if err := json.Unmarshal([]byte(payload), &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (b *PostgresStateBackend) Save(state *persistedState) error {
	if b == nil || state == nil {
		return nil
	}
	if err := b.ensureReady(); err != nil {
		return err
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), postgresOperationTimeout)
	defer cancel()

	query := fmt.Sprintf(`
		INSERT INTO %s (state_key, snapshot, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (state_key)
		DO UPDATE SET snapshot = EXCLUDED.snapshot, updated_at = NOW()`, postgresQuoteIdentifier(b.tableName))
	_, err = b.db.ExecContext(ctx, query, b.stateKey, string(payload))
	return err
}

func (b *PostgresStateBackend) Close() error {
	if b == nil || b.db == nil {
		return nil
	}
	return b.db.Close()
}

func (b *PostgresStateBackend) ensureReady() error {
	if b == nil {
		return ErrInvalidInput
	}
	b.initOnce.Do(func() {
		db, err := b.openDB("postgres", b.dsn)
		if err != nil {
			b.initErr = err
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), postgresOperationTimeout)
		defer cancel()

		query := fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				state_key TEXT PRIMARY KEY,
				snapshot TEXT NOT NULL,
				updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)`, postgresQuoteIdentifier(b.tableName))
		if _, err := db.ExecContext(ctx, query); err != nil {
			_ = db.Close()
			b.initErr = err
			return
		}
		b.db = db
	})
	return b.initErr
}

type postgresQueueCore struct {
	dsn          string
	tableName    string
	queueKey     string
	capacity     int
	pollInterval time.Duration
	openDB       sqlOpenFunc

	initOnce sync.Once
	initErr  error
	db       *sql.DB
}

func newPostgresQueueCore(dsn, tableName, queueKey string, capacity int) (*postgresQueueCore, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, ErrInvalidInput
	}
	if strings.TrimSpace(tableName) == "" {
		return nil, ErrInvalidInput
	}
	if strings.TrimSpace(queueKey) == "" {
		queueKey = postgresQueueKey
	}
	if capacity <= 0 {
		capacity = 1024
	}
	return &postgresQueueCore{
		dsn:          dsn,
		tableName:    tableName,
		queueKey:     queueKey,
		capacity:     capacity,
		pollInterval: postgresQueuePollInterval,
		openDB:       sql.Open,
	}, nil
}

func (q *postgresQueueCore) ensureReady() error {
	if q == nil {
		return ErrInvalidInput
	}
	q.initOnce.Do(func() {
		db, err := q.openDB("postgres", q.dsn)
		if err != nil {
			q.initErr = err
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), postgresOperationTimeout)
		defer cancel()

		createTableQuery := fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				id BIGSERIAL PRIMARY KEY,
				queue_key TEXT NOT NULL,
				payload TEXT NOT NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)`, postgresQuoteIdentifier(q.tableName))
		if _, err := db.ExecContext(ctx, createTableQuery); err != nil {
			_ = db.Close()
			q.initErr = err
			return
		}
		indexName := q.tableName + "_queue_key_id_idx"
		createIndexQuery := fmt.Sprintf(
			"CREATE INDEX IF NOT EXISTS %s ON %s (queue_key, id)",
			postgresQuoteIdentifier(indexName),
			postgresQuoteIdentifier(q.tableName),
		)
		if _, err := db.ExecContext(ctx, createIndexQuery); err != nil {
			_ = db.Close()
			q.initErr = err
			return
		}
		q.db = db
	})
	return q.initErr
}

func (q *postgresQueueCore) tryEnqueuePayload(payload string) bool {
	if strings.TrimSpace(payload) == "" {
		return false
	}
	if err := q.ensureReady(); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), postgresOperationTimeout)
	defer cancel()

	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return false
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE queue_key = $1", postgresQuoteIdentifier(q.tableName))
	lockKey := postgresQueueLockKey(q.tableName, q.queueKey)
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey); err != nil {
		return false
	}
	var depth int
	if err := tx.QueryRowContext(ctx, countQuery, q.queueKey).Scan(&depth); err != nil {
		return false
	}
	if depth >= q.capacity {
		return false
	}
	insertQuery := fmt.Sprintf("INSERT INTO %s (queue_key, payload, created_at) VALUES ($1, $2, NOW())", postgresQuoteIdentifier(q.tableName))
	if _, err := tx.ExecContext(ctx, insertQuery, q.queueKey, payload); err != nil {
		return false
	}
	if err := tx.Commit(); err != nil {
		return false
	}
	committed = true
	return true
}

func (q *postgresQueueCore) enqueuePayload(ctx context.Context, payload string) bool {
	for {
		if q.tryEnqueuePayload(payload) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(q.pollInterval):
		}
	}
}

func (q *postgresQueueCore) dequeuePayload(ctx context.Context) (string, bool) {
	for {
		payload, ok := q.tryDequeuePayload(ctx)
		if ok {
			return payload, true
		}
		select {
		case <-ctx.Done():
			return "", false
		case <-time.After(q.pollInterval):
		}
	}
}

func (q *postgresQueueCore) tryDequeuePayload(ctx context.Context) (string, bool) {
	if err := q.ensureReady(); err != nil {
		return "", false
	}
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	query := fmt.Sprintf(`
		SELECT id, payload
		FROM %s
		WHERE queue_key = $1
		ORDER BY id ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED`, postgresQuoteIdentifier(q.tableName))
	var id int64
	var payload string
	err = tx.QueryRowContext(ctx, query, q.queueKey).Scan(&id, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		return "", false
	}
	deleteQuery := fmt.Sprintf("DELETE FROM %s WHERE id = $1", postgresQuoteIdentifier(q.tableName))
	if _, err := tx.ExecContext(ctx, deleteQuery, id); err != nil {
		return "", false
	}
	if err := tx.Commit(); err != nil {
		return "", false
	}
	committed = true
	return payload, true
}

func (q *postgresQueueCore) depth() int {
	if err := q.ensureReady(); err != nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), postgresOperationTimeout)
	defer cancel()

	query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE queue_key = $1", postgresQuoteIdentifier(q.tableName))
	var depth int
	if err := q.db.QueryRowContext(ctx, query, q.queueKey).Scan(&depth); err != nil {
		return 0
	}
	return depth
}

func (q *postgresQueueCore) snapshotPayloads() []string {
	if err := q.ensureReady(); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), postgresOperationTimeout)
	defer cancel()

	query := fmt.Sprintf("SELECT payload FROM %s WHERE queue_key = $1 ORDER BY id ASC", postgresQuoteIdentifier(q.tableName))
	rows, err := q.db.QueryContext(ctx, query, q.queueKey)
	if err != nil {
		return nil
	}
	defer rows.Close()

	items := make([]string, 0)
	for rows.Next() {
		var payload string
		if scanErr := rows.Scan(&payload); scanErr != nil {
			continue
		}
		items = append(items, payload)
	}
	return items
}

func (q *postgresQueueCore) close() error {
	if q == nil || q.db == nil {
		return nil
	}
	return q.db.Close()
}

type PostgresEnvelopeQueue struct {
	core *postgresQueueCore
}

func NewPostgresEnvelopeQueue(dsn string, capacity int) (EnvelopeQueue, error) {
	core, err := newPostgresQueueCore(dsn, postgresEnvelopeQueueTableName, postgresQueueKey, capacity)
	if err != nil {
		return nil, err
	}
	return &PostgresEnvelopeQueue{core: core}, nil
}

func (q *PostgresEnvelopeQueue) TryEnqueue(envelopeID string) bool {
	if q == nil || q.core == nil || strings.TrimSpace(envelopeID) == "" {
		return false
	}
	return q.core.tryEnqueuePayload(envelopeID)
}

func (q *PostgresEnvelopeQueue) Enqueue(ctx context.Context, envelopeID string) bool {
	if q == nil || q.core == nil || strings.TrimSpace(envelopeID) == "" {
		return false
	}
	return q.core.enqueuePayload(ctx, envelopeID)
}

func (q *PostgresEnvelopeQueue) Dequeue(ctx context.Context) (string, bool) {
	if q == nil || q.core == nil {
		return "", false
	}
	return q.core.dequeuePayload(ctx)
}

func (q *PostgresEnvelopeQueue) Depth() int {
	if q == nil || q.core == nil {
		return 0
	}
	return q.core.depth()
}

func (q *PostgresEnvelopeQueue) Capacity() int {
	if q == nil || q.core == nil {
		return 0
	}
	return q.core.capacity
}

func (q *PostgresEnvelopeQueue) SnapshotEnvelopeIDs() []string {
	if q == nil || q.core == nil {
		return nil
	}
	return q.core.snapshotPayloads()
}

func (q *PostgresEnvelopeQueue) Close() error {
	if q == nil || q.core == nil {
		return nil
	}
	return q.core.close()
}

type PostgresWritebackQueue struct {
	core *postgresQueueCore
}

func NewPostgresWritebackQueue(dsn string, capacity int) (WritebackQueue, error) {
	core, err := newPostgresQueueCore(dsn, postgresWritebackQueueTableName, postgresQueueKey, capacity)
	if err != nil {
		return nil, err
	}
	return &PostgresWritebackQueue{core: core}, nil
}

func (q *PostgresWritebackQueue) TryEnqueue(task WritebackQueueItem) bool {
	if q == nil || q.core == nil || strings.TrimSpace(task.OpID) == "" {
		return false
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return false
	}
	return q.core.tryEnqueuePayload(string(payload))
}

func (q *PostgresWritebackQueue) Enqueue(ctx context.Context, task WritebackQueueItem) bool {
	if q == nil || q.core == nil || strings.TrimSpace(task.OpID) == "" {
		return false
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return false
	}
	return q.core.enqueuePayload(ctx, string(payload))
}

func (q *PostgresWritebackQueue) Dequeue(ctx context.Context) (WritebackQueueItem, bool) {
	if q == nil || q.core == nil {
		return WritebackQueueItem{}, false
	}
	for {
		payload, ok := q.core.dequeuePayload(ctx)
		if !ok {
			return WritebackQueueItem{}, false
		}
		var task WritebackQueueItem
		if err := json.Unmarshal([]byte(payload), &task); err != nil || strings.TrimSpace(task.OpID) == "" {
			continue
		}
		return task, true
	}
}

func (q *PostgresWritebackQueue) Depth() int {
	if q == nil || q.core == nil {
		return 0
	}
	return q.core.depth()
}

func (q *PostgresWritebackQueue) Capacity() int {
	if q == nil || q.core == nil {
		return 0
	}
	return q.core.capacity
}

func (q *PostgresWritebackQueue) SnapshotWritebacks() []WritebackQueueItem {
	if q == nil || q.core == nil {
		return nil
	}
	payloads := q.core.snapshotPayloads()
	items := make([]WritebackQueueItem, 0, len(payloads))
	for _, payload := range payloads {
		var task WritebackQueueItem
		if err := json.Unmarshal([]byte(payload), &task); err != nil || strings.TrimSpace(task.OpID) == "" {
			continue
		}
		items = append(items, task)
	}
	return items
}

func (q *PostgresWritebackQueue) Close() error {
	if q == nil || q.core == nil {
		return nil
	}
	return q.core.close()
}

func postgresQuoteIdentifier(identifier string) string {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return "\"\""
	}
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func postgresQueueLockKey(tableName, queueKey string) int64 {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(strings.TrimSpace(tableName)))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(strings.TrimSpace(queueKey)))
	return int64(hasher.Sum64())
}
