package relayfile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotFound            = errors.New("not found")
	ErrRevisionConflict    = errors.New("revision conflict")
	ErrMissingPrecondition = errors.New("missing precondition")
	ErrInvalidInput        = errors.New("invalid input")
	ErrInvalidState        = errors.New("invalid state")
	ErrQueueFull           = errors.New("queue full")
	ErrNotImplemented      = errors.New("not implemented")
)

const DirectoryPermissionMarkerFile = ".relayfile.acl"

type ConflictError struct {
	ExpectedRevision      string
	CurrentRevision       string
	CurrentContentPreview string
}

func (e *ConflictError) Error() string {
	return "revision conflict"
}

func (e *ConflictError) Is(target error) bool {
	return target == ErrRevisionConflict
}

type TreeEntry struct {
	Path             string `json:"path"`
	Type             string `json:"type"`
	Revision         string `json:"revision"`
	Provider         string `json:"provider,omitempty"`
	ProviderObjectID string `json:"providerObjectId,omitempty"`
	Size             int64  `json:"size,omitempty"`
	UpdatedAt        string `json:"updatedAt,omitempty"`
	PropertyCount    int    `json:"propertyCount,omitempty"`
	RelationCount    int    `json:"relationCount,omitempty"`
	PermissionCount  int    `json:"permissionCount,omitempty"`
	CommentCount     int    `json:"commentCount,omitempty"`
}

type TreeResponse struct {
	Path       string      `json:"path"`
	Entries    []TreeEntry `json:"entries"`
	NextCursor *string     `json:"nextCursor"`
}

type FileSemantics struct {
	Properties  map[string]string `json:"properties,omitempty"`
	Relations   []string          `json:"relations,omitempty"`
	Permissions []string          `json:"permissions,omitempty"`
	Comments    []string          `json:"comments,omitempty"`
}

type File struct {
	Path             string        `json:"path"`
	Revision         string        `json:"revision"`
	ContentType      string        `json:"contentType"`
	Content          string        `json:"content"`
	Provider         string        `json:"provider,omitempty"`
	ProviderObjectID string        `json:"providerObjectId,omitempty"`
	LastEditedAt     string        `json:"lastEditedAt,omitempty"`
	Semantics        FileSemantics `json:"semantics,omitempty"`
}

type Event struct {
	EventID       string `json:"eventId"`
	Type          string `json:"type"`
	Path          string `json:"path"`
	Revision      string `json:"revision"`
	Origin        string `json:"origin"`
	Provider      string `json:"provider,omitempty"`
	CorrelationID string `json:"correlationId"`
	Timestamp     string `json:"timestamp"`
}

type EventFeed struct {
	Events     []Event `json:"events"`
	NextCursor *string `json:"nextCursor"`
}

type WriteRequest struct {
	WorkspaceID   string
	Path          string
	IfMatch       string
	ContentType   string
	Content       string
	Semantics     FileSemantics
	CorrelationID string
}

type DeleteRequest struct {
	WorkspaceID   string
	Path          string
	IfMatch       string
	CorrelationID string
}

type WriteResult struct {
	OpID           string `json:"opId"`
	Status         string `json:"status"`
	TargetRevision string `json:"targetRevision"`
	Writeback      struct {
		Provider string `json:"provider"`
		State    string `json:"state"`
	} `json:"writeback"`
}

type OperationStatus struct {
	OpID           string         `json:"opId"`
	Path           string         `json:"path,omitempty"`
	Revision       string         `json:"revision,omitempty"`
	Action         string         `json:"action,omitempty"`
	Provider       string         `json:"provider,omitempty"`
	Status         string         `json:"status"`
	AttemptCount   int            `json:"attemptCount"`
	NextAttemptAt  *string        `json:"nextAttemptAt,omitempty"`
	LastError      *string        `json:"lastError,omitempty"`
	ProviderResult map[string]any `json:"providerResult,omitempty"`
	CorrelationID  string         `json:"correlationId,omitempty"`
}

type OperationFeed struct {
	Items      []OperationStatus `json:"items"`
	NextCursor *string           `json:"nextCursor"`
}

type FileQueryRequest struct {
	PathPrefix string
	Provider   string
	Relation   string
	Permission string
	Comment    string
	Properties map[string]string
	Cursor     string
	Limit      int
}

type FileQueryItem struct {
	Path             string            `json:"path"`
	Revision         string            `json:"revision"`
	ContentType      string            `json:"contentType"`
	Provider         string            `json:"provider,omitempty"`
	ProviderObjectID string            `json:"providerObjectId,omitempty"`
	LastEditedAt     string            `json:"lastEditedAt,omitempty"`
	Size             int64             `json:"size"`
	Properties       map[string]string `json:"properties,omitempty"`
	Relations        []string          `json:"relations,omitempty"`
	Permissions      []string          `json:"permissions,omitempty"`
	Comments         []string          `json:"comments,omitempty"`
}

type FileQueryResponse struct {
	Items      []FileQueryItem `json:"items"`
	NextCursor *string         `json:"nextCursor"`
}

type SyncProviderStatus struct {
	Provider              string         `json:"provider"`
	Status                string         `json:"status"`
	Cursor                *string        `json:"cursor,omitempty"`
	WatermarkTs           *string        `json:"watermarkTs,omitempty"`
	LagSeconds            int            `json:"lagSeconds,omitempty"`
	LastError             *string        `json:"lastError,omitempty"`
	FailureCodes          map[string]int `json:"failureCodes,omitempty"`
	DeadLetteredEnvelopes int            `json:"deadLetteredEnvelopes,omitempty"`
	DeadLetteredOps       int            `json:"deadLetteredOps,omitempty"`
}

type SyncStatus struct {
	WorkspaceID string               `json:"workspaceId"`
	Providers   []SyncProviderStatus `json:"providers"`
}

type IngressStatus struct {
	WorkspaceID             string                           `json:"workspaceId"`
	QueueDepth              int                              `json:"queueDepth"`
	QueueCapacity           int                              `json:"queueCapacity"`
	QueueUtilization        float64                          `json:"queueUtilization"`
	PendingTotal            int                              `json:"pendingTotal"`
	OldestPendingAgeSeconds int                              `json:"oldestPendingAgeSeconds"`
	DeadLetterTotal         int                              `json:"deadLetterTotal"`
	DeadLetterByProvider    map[string]int                   `json:"deadLetterByProvider"`
	AcceptedTotal           uint64                           `json:"acceptedTotal"`
	DroppedTotal            uint64                           `json:"droppedTotal"`
	DedupedTotal            uint64                           `json:"dedupedTotal"`
	CoalescedTotal          uint64                           `json:"coalescedTotal"`
	DedupeRate              float64                          `json:"dedupeRate"`
	CoalesceRate            float64                          `json:"coalesceRate"`
	SuppressedTotal         uint64                           `json:"suppressedTotal"`
	StaleTotal              uint64                           `json:"staleTotal"`
	IngressByProvider       map[string]IngressProviderStatus `json:"ingressByProvider"`
}

type IngressProviderStatus struct {
	AcceptedTotal           uint64  `json:"acceptedTotal"`
	DroppedTotal            uint64  `json:"droppedTotal"`
	DedupedTotal            uint64  `json:"dedupedTotal"`
	CoalescedTotal          uint64  `json:"coalescedTotal"`
	PendingTotal            int     `json:"pendingTotal"`
	OldestPendingAgeSeconds int     `json:"oldestPendingAgeSeconds"`
	SuppressedTotal         uint64  `json:"suppressedTotal"`
	StaleTotal              uint64  `json:"staleTotal"`
	DedupeRate              float64 `json:"dedupeRate"`
	CoalesceRate            float64 `json:"coalesceRate"`
}

type EnvelopeDeadLetter struct {
	EnvelopeID    string `json:"envelopeId"`
	WorkspaceID   string `json:"workspaceId"`
	Provider      string `json:"provider"`
	DeliveryID    string `json:"deliveryId"`
	CorrelationID string `json:"correlationId,omitempty"`
	FailedAt      string `json:"failedAt"`
	AttemptCount  int    `json:"attemptCount"`
	LastError     string `json:"lastError"`
}

type DeadLetterFeed struct {
	Items      []EnvelopeDeadLetter `json:"items"`
	NextCursor *string              `json:"nextCursor"`
}

type BackendStatus struct {
	BackendProfile      string `json:"backendProfile,omitempty"`
	StateBackend        string `json:"stateBackend"`
	EnvelopeQueue       string `json:"envelopeQueue"`
	EnvelopeQueueDepth  int    `json:"envelopeQueueDepth"`
	EnvelopeQueueCap    int    `json:"envelopeQueueCapacity"`
	WritebackQueue      string `json:"writebackQueue"`
	WritebackQueueDepth int    `json:"writebackQueueDepth"`
	WritebackQueueCap   int    `json:"writebackQueueCapacity"`
}

type StoreOptions struct {
	StateFile              string
	StateBackend           StateBackend
	MaxWritebackAttempts   int
	WritebackDelay         time.Duration
	MaxEnvelopeAttempts    int
	EnvelopeRetryDelay     time.Duration
	SuppressionWindow      time.Duration
	CoalesceWindow         time.Duration
	MaxStoredEnvelopes     int
	ProviderWrite          ProviderWriteFunc
	ProviderWriteAction    ProviderWriteActionFunc
	DisableWorkers         bool
	EnvelopeQueueSize      int
	EnvelopeQueue          EnvelopeQueue
	WritebackQueue         WritebackQueue
	BackendProfile         string
	EnvelopeWorkers        int
	WritebackWorkers       int
	ProviderMaxConcurrency int
	Adapters               []ProviderAdapter
}

type ProviderWriteFunc func(workspaceID, path, revision string) error
type ProviderWriteActionFunc func(action WritebackAction) error

type WritebackActionType string

const (
	WritebackActionFileUpsert WritebackActionType = "file_upsert"
	WritebackActionFileDelete WritebackActionType = "file_delete"
)

type WritebackAction struct {
	WorkspaceID      string              `json:"workspaceId"`
	Path             string              `json:"path"`
	Revision         string              `json:"revision"`
	Type             WritebackActionType `json:"type"`
	ContentType      string              `json:"contentType,omitempty"`
	Content          string              `json:"content,omitempty"`
	Provider         string              `json:"provider,omitempty"`
	ProviderObjectID string              `json:"providerObjectId,omitempty"`
	CorrelationID    string              `json:"correlationId,omitempty"`
}

type QueuedResponse struct {
	Status        string `json:"status"`
	ID            string `json:"id"`
	CorrelationID string `json:"correlationId,omitempty"`
}

type EnvelopeQueue interface {
	TryEnqueue(envelopeID string) bool
	Enqueue(ctx context.Context, envelopeID string) bool
	Dequeue(ctx context.Context) (string, bool)
	Depth() int
	Capacity() int
	Close() error
}

type WritebackQueue interface {
	TryEnqueue(task WritebackQueueItem) bool
	Enqueue(ctx context.Context, task WritebackQueueItem) bool
	Dequeue(ctx context.Context) (WritebackQueueItem, bool)
	Depth() int
	Capacity() int
	Close() error
}

type envelopeQueueSnapshotter interface {
	SnapshotEnvelopeIDs() []string
}

type writebackQueueSnapshotter interface {
	SnapshotWritebacks() []WritebackQueueItem
}

type AckResponse struct {
	Status        string `json:"status"`
	ID            string `json:"id"`
	CorrelationID string `json:"correlationId,omitempty"`
}

type WebhookEnvelopeRequest struct {
	EnvelopeID    string            `json:"envelopeId"`
	WorkspaceID   string            `json:"workspaceId"`
	Provider      string            `json:"provider"`
	DeliveryID    string            `json:"deliveryId"`
	ReceivedAt    string            `json:"receivedAt"`
	Headers       map[string]string `json:"headers,omitempty"`
	Payload       map[string]any    `json:"payload"`
	CorrelationID string            `json:"correlationId"`
}

type Store struct {
	mu                      sync.RWMutex
	queueMu                 sync.Mutex
	workspaces              map[string]*workspaceState
	revCounter              uint64
	opCounter               uint64
	eventCounter            uint64
	envelopesByID           map[string]WebhookEnvelopeRequest
	deliveryIndex           map[string]string
	processedEnvs           map[string]bool
	processingEnvs          map[string]bool
	stateBackend            StateBackend
	writebackQueue          WritebackQueue
	envelopeQueue           EnvelopeQueue
	queuedWritebacks        map[string]struct{}
	queuedEnvelopes         map[string]struct{}
	ingressByWS             map[string]ingressCounter
	coalesceIndex           map[string]string
	envelopeAttempts        map[string]int
	envelopeNextAttempt     map[string]time.Time
	deadLetters             map[string]EnvelopeDeadLetter
	providerWrite           ProviderWriteFunc
	providerWriteConfigured bool
	providerWriteAction     ProviderWriteActionFunc
	adapters                map[string]ProviderAdapter
	backendProfile          string
	maxAttempts             int
	retryDelay              time.Duration
	maxEnvelopeAttempts     int
	envelopeRetryDelay      time.Duration
	suppressionWindow       time.Duration
	coalesceWindow          time.Duration
	suppressions            map[string]time.Time
	maxStoredEnvelopes      int
	providerMaxConcurrency  int
	providerSemMu           sync.Mutex
	providerSemaphores      map[string]chan struct{}
	closed                  chan struct{}
	queueCtx                context.Context
	queueCancel             context.CancelFunc
	closeOnce               sync.Once
	wg                      sync.WaitGroup
}

type workspaceState struct {
	Files              map[string]File            `json:"files"`
	Events             []Event                    `json:"events"`
	Ops                map[string]OperationStatus `json:"ops"`
	ProviderIndex      map[string]string          `json:"providerIndex,omitempty"`
	ProviderWatermarks map[string]string          `json:"providerWatermarks,omitempty"`
}

type WritebackQueueItem struct {
	WorkspaceID   string `json:"workspaceId"`
	OpID          string `json:"opId"`
	Path          string `json:"path"`
	Revision      string `json:"revision"`
	CorrelationID string `json:"correlationId"`
}

type writebackTask = WritebackQueueItem

type ingressCounter struct {
	AcceptedTotal   uint64                            `json:"acceptedTotal"`
	DroppedTotal    uint64                            `json:"droppedTotal"`
	DedupedTotal    uint64                            `json:"dedupedTotal"`
	CoalescedTotal  uint64                            `json:"coalescedTotal"`
	SuppressedTotal uint64                            `json:"suppressedTotal"`
	StaleTotal      uint64                            `json:"staleTotal"`
	ByProvider      map[string]providerIngressCounter `json:"byProvider,omitempty"`
}

type providerIngressCounter struct {
	AcceptedTotal   uint64 `json:"acceptedTotal"`
	DroppedTotal    uint64 `json:"droppedTotal"`
	DedupedTotal    uint64 `json:"dedupedTotal"`
	CoalescedTotal  uint64 `json:"coalescedTotal"`
	SuppressedTotal uint64 `json:"suppressedTotal"`
	StaleTotal      uint64 `json:"staleTotal"`
}

type persistedState struct {
	RevCounter          uint64                            `json:"revCounter"`
	OpCounter           uint64                            `json:"opCounter"`
	EventCounter        uint64                            `json:"eventCounter"`
	Workspaces          map[string]*workspaceState        `json:"workspaces"`
	EnvelopesByID       map[string]WebhookEnvelopeRequest `json:"envelopesById"`
	DeliveryIndex       map[string]string                 `json:"deliveryIndex"`
	ProcessedEnvs       map[string]bool                   `json:"processedEnvs"`
	IngressByWorkspace  map[string]ingressCounter         `json:"ingressByWorkspace"`
	EnvelopeAttempts    map[string]int                    `json:"envelopeAttempts"`
	EnvelopeNextAttempt map[string]time.Time              `json:"envelopeNextAttempt"`
	DeadLetters         map[string]EnvelopeDeadLetter     `json:"deadLetters"`
	Suppressions        map[string]time.Time              `json:"suppressions"`
}

type StateBackend interface {
	Load() (*persistedState, error)
	Save(state *persistedState) error
}

type stateBackendCloser interface {
	Close() error
}

type JSONFileStateBackend struct {
	Path string
}

type inMemoryEnvelopeQueue struct {
	ch chan string
}

func NewInMemoryEnvelopeQueue(capacity int) EnvelopeQueue {
	if capacity <= 0 {
		capacity = 1024
	}
	return &inMemoryEnvelopeQueue{
		ch: make(chan string, capacity),
	}
}

func (q *inMemoryEnvelopeQueue) TryEnqueue(envelopeID string) bool {
	if q == nil || envelopeID == "" {
		return false
	}
	select {
	case q.ch <- envelopeID:
		return true
	default:
		return false
	}
}

func (q *inMemoryEnvelopeQueue) Enqueue(ctx context.Context, envelopeID string) bool {
	if q == nil || envelopeID == "" {
		return false
	}
	select {
	case q.ch <- envelopeID:
		return true
	case <-ctx.Done():
		return false
	}
}

func (q *inMemoryEnvelopeQueue) Dequeue(ctx context.Context) (string, bool) {
	if q == nil {
		return "", false
	}
	select {
	case envelopeID := <-q.ch:
		return envelopeID, true
	case <-ctx.Done():
		return "", false
	}
}

func (q *inMemoryEnvelopeQueue) Depth() int {
	if q == nil {
		return 0
	}
	return len(q.ch)
}

func (q *inMemoryEnvelopeQueue) Capacity() int {
	if q == nil {
		return 0
	}
	return cap(q.ch)
}

func (q *inMemoryEnvelopeQueue) Close() error {
	return nil
}

type inMemoryWritebackQueue struct {
	ch    chan WritebackQueueItem
	items map[string]WritebackQueueItem
	mu    sync.Mutex
}

func NewInMemoryWritebackQueue(capacity int) WritebackQueue {
	if capacity <= 0 {
		capacity = 1024
	}
	return &inMemoryWritebackQueue{
		ch:    make(chan WritebackQueueItem, capacity),
		items: make(map[string]WritebackQueueItem),
	}
}

func (q *inMemoryWritebackQueue) TryEnqueue(task WritebackQueueItem) bool {
	if q == nil || task.OpID == "" {
		return false
	}
	select {
	case q.ch <- task:
		q.mu.Lock()
		q.items[task.OpID] = task
		q.mu.Unlock()
		return true
	default:
		return false
	}
}

func (q *inMemoryWritebackQueue) Enqueue(ctx context.Context, task WritebackQueueItem) bool {
	if q == nil || task.OpID == "" {
		return false
	}
	select {
	case q.ch <- task:
		q.mu.Lock()
		q.items[task.OpID] = task
		q.mu.Unlock()
		return true
	case <-ctx.Done():
		return false
	}
}

func (q *inMemoryWritebackQueue) Dequeue(ctx context.Context) (WritebackQueueItem, bool) {
	if q == nil {
		return WritebackQueueItem{}, false
	}
	select {
	case task := <-q.ch:
		q.mu.Lock()
		delete(q.items, task.OpID)
		q.mu.Unlock()
		return task, true
	case <-ctx.Done():
		return WritebackQueueItem{}, false
	}
}

func (q *inMemoryWritebackQueue) SnapshotWritebacks() []WritebackQueueItem {
	if q == nil {
		return []WritebackQueueItem{}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	result := make([]WritebackQueueItem, 0, len(q.items))
	for _, item := range q.items {
		result = append(result, item)
	}
	return result
}

func (q *inMemoryWritebackQueue) Depth() int {
	if q == nil {
		return 0
	}
	return len(q.ch)
}

func (q *inMemoryWritebackQueue) Capacity() int {
	if q == nil {
		return 0
	}
	return cap(q.ch)
}

func (q *inMemoryWritebackQueue) Close() error {
	return nil
}

func NewJSONFileStateBackend(path string) *JSONFileStateBackend {
	return &JSONFileStateBackend{Path: strings.TrimSpace(path)}
}

func (b *JSONFileStateBackend) Load() (*persistedState, error) {
	if b == nil || strings.TrimSpace(b.Path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(b.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var snapshot persistedState
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (b *JSONFileStateBackend) Save(state *persistedState) error {
	if b == nil || strings.TrimSpace(b.Path) == "" || state == nil {
		return nil
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	dir := filepath.Dir(b.Path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := b.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, b.Path)
}

func NewStore() *Store {
	return NewStoreWithOptions(StoreOptions{})
}

func NewStoreWithOptions(opts StoreOptions) *Store {
	maxAttempts := opts.MaxWritebackAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	retryDelay := opts.WritebackDelay
	if retryDelay <= 0 {
		retryDelay = 50 * time.Millisecond
	}
	maxEnvelopeAttempts := opts.MaxEnvelopeAttempts
	if maxEnvelopeAttempts <= 0 {
		maxEnvelopeAttempts = 3
	}
	envelopeRetryDelay := opts.EnvelopeRetryDelay
	if envelopeRetryDelay <= 0 {
		envelopeRetryDelay = 50 * time.Millisecond
	}
	suppressionWindow := opts.SuppressionWindow
	if suppressionWindow <= 0 {
		suppressionWindow = 2 * time.Minute
	}
	coalesceWindow := opts.CoalesceWindow
	if coalesceWindow <= 0 {
		coalesceWindow = 3 * time.Second
	}
	maxStoredEnvelopes := opts.MaxStoredEnvelopes
	if maxStoredEnvelopes <= 0 {
		maxStoredEnvelopes = 10000
	}
	queueSize := opts.EnvelopeQueueSize
	if queueSize <= 0 {
		queueSize = 1024
	}
	envelopeQueue := opts.EnvelopeQueue
	if envelopeQueue == nil {
		envelopeQueue = NewInMemoryEnvelopeQueue(queueSize)
	}
	writebackQueue := opts.WritebackQueue
	if writebackQueue == nil {
		writebackQueue = NewInMemoryWritebackQueue(1024)
	}
	envelopeWorkers := opts.EnvelopeWorkers
	if envelopeWorkers <= 0 {
		envelopeWorkers = 1
	}
	writebackWorkers := opts.WritebackWorkers
	if writebackWorkers <= 0 {
		writebackWorkers = 1
	}
	providerMaxConcurrency := opts.ProviderMaxConcurrency
	if providerMaxConcurrency <= 0 {
		providerMaxConcurrency = 1
	}
	writer := opts.ProviderWrite
	legacyWriterConfigured := writer != nil
	if writer == nil {
		writer = func(workspaceID, path, revision string) error {
			return nil
		}
	}
	actionWriter := opts.ProviderWriteAction
	stateBackend := opts.StateBackend
	if stateBackend == nil && strings.TrimSpace(opts.StateFile) != "" {
		stateBackend = NewJSONFileStateBackend(opts.StateFile)
	}
	queueCtx, queueCancel := context.WithCancel(context.Background())

	adapters := map[string]ProviderAdapter{}
	for _, adapter := range opts.Adapters {
		if adapter == nil || adapter.Provider() == "" {
			continue
		}
		adapters[adapter.Provider()] = adapter
	}
	backendProfile := normalizeProvider(opts.BackendProfile)
	if backendProfile == "" {
		backendProfile = "custom"
	}

	s := &Store{
		workspaces:              map[string]*workspaceState{},
		envelopesByID:           map[string]WebhookEnvelopeRequest{},
		deliveryIndex:           map[string]string{},
		processedEnvs:           map[string]bool{},
		processingEnvs:          map[string]bool{},
		stateBackend:            stateBackend,
		writebackQueue:          writebackQueue,
		envelopeQueue:           envelopeQueue,
		queuedWritebacks:        map[string]struct{}{},
		queuedEnvelopes:         map[string]struct{}{},
		ingressByWS:             map[string]ingressCounter{},
		coalesceIndex:           map[string]string{},
		envelopeAttempts:        map[string]int{},
		envelopeNextAttempt:     map[string]time.Time{},
		deadLetters:             map[string]EnvelopeDeadLetter{},
		providerWrite:           writer,
		providerWriteConfigured: legacyWriterConfigured,
		providerWriteAction:     actionWriter,
		adapters:                adapters,
		backendProfile:          backendProfile,
		maxAttempts:             maxAttempts,
		retryDelay:              retryDelay,
		maxEnvelopeAttempts:     maxEnvelopeAttempts,
		envelopeRetryDelay:      envelopeRetryDelay,
		suppressionWindow:       suppressionWindow,
		coalesceWindow:          coalesceWindow,
		suppressions:            map[string]time.Time{},
		maxStoredEnvelopes:      maxStoredEnvelopes,
		providerMaxConcurrency:  providerMaxConcurrency,
		providerSemaphores:      map[string]chan struct{}{},
		closed:                  make(chan struct{}),
		queueCtx:                queueCtx,
		queueCancel:             queueCancel,
	}
	s.seedQueuedIndexesFromQueues()
	_ = s.loadFromDisk()
	s.rebuildCoalesceIndexLocked()
	if !opts.DisableWorkers {
		s.wg.Add(writebackWorkers + envelopeWorkers)
		for i := 0; i < writebackWorkers; i++ {
			go func() {
				defer s.wg.Done()
				s.writebackWorker()
			}()
		}
		for i := 0; i < envelopeWorkers; i++ {
			go func() {
				defer s.wg.Done()
				s.envelopeWorker()
			}()
		}
		s.mu.RLock()
		type delayedEnvelopeTask struct {
			id    string
			delay time.Duration
		}
		pendingEnvelopes := make([]string, 0, len(s.envelopesByID))
		delayedEnvelopes := make([]delayedEnvelopeTask, 0)
		for envelopeID := range s.envelopesByID {
			if !s.processedEnvs[envelopeID] {
				if nextAttempt, ok := s.envelopeNextAttempt[envelopeID]; ok {
					if until := time.Until(nextAttempt); until > 0 {
						delayedEnvelopes = append(delayedEnvelopes, delayedEnvelopeTask{id: envelopeID, delay: until})
						continue
					}
				}
				pendingEnvelopes = append(pendingEnvelopes, envelopeID)
			}
		}
		type delayedWritebackTask struct {
			task  writebackTask
			delay time.Duration
		}
		pendingWritebacks := make([]writebackTask, 0)
		delayedWritebacks := make([]delayedWritebackTask, 0)
		for workspaceID, ws := range s.workspaces {
			for opID, op := range ws.Ops {
				if op.Status != "pending" && op.Status != "running" {
					continue
				}
				task := writebackTask{
					WorkspaceID:   workspaceID,
					OpID:          opID,
					Path:          op.Path,
					Revision:      op.Revision,
					CorrelationID: op.CorrelationID,
				}
				delay := time.Duration(0)
				if op.NextAttemptAt != nil {
					if nextAt, err := time.Parse(time.RFC3339Nano, *op.NextAttemptAt); err == nil {
						if until := time.Until(nextAt); until > 0 {
							delay = until
						}
					}
				}
				if delay > 0 {
					delayedWritebacks = append(delayedWritebacks, delayedWritebackTask{task: task, delay: delay})
					continue
				}
				pendingWritebacks = append(pendingWritebacks, task)
			}
		}
		s.mu.RUnlock()
		for _, envelopeID := range pendingEnvelopes {
			s.enqueueEnvelope(envelopeID)
		}
		for _, delayed := range delayedEnvelopes {
			s.scheduleEnvelopeRetry(delayed.id, delayed.delay)
		}
		for _, task := range pendingWritebacks {
			s.enqueueWriteback(task)
		}
		for _, scheduled := range delayedWritebacks {
			scheduledTask := scheduled.task
			scheduledDelay := scheduled.delay
			time.AfterFunc(scheduledDelay, func() {
				select {
				case <-s.closed:
					return
				default:
					s.enqueueWriteback(scheduledTask)
				}
			})
		}
	}
	return s
}

func (s *Store) seedQueuedIndexesFromQueues() {
	if s.envelopeQueue != nil {
		if snapshotter, ok := s.envelopeQueue.(envelopeQueueSnapshotter); ok {
			for _, envelopeID := range snapshotter.SnapshotEnvelopeIDs() {
				if strings.TrimSpace(envelopeID) == "" {
					continue
				}
				s.queuedEnvelopes[envelopeID] = struct{}{}
			}
		}
	}
	if s.writebackQueue != nil {
		if snapshotter, ok := s.writebackQueue.(writebackQueueSnapshotter); ok {
			for _, task := range snapshotter.SnapshotWritebacks() {
				if strings.TrimSpace(task.OpID) == "" {
					continue
				}
				s.queuedWritebacks[task.OpID] = struct{}{}
			}
		}
	}
}

func (s *Store) Close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		if s.queueCancel != nil {
			s.queueCancel()
		}
		if s.envelopeQueue != nil {
			_ = s.envelopeQueue.Close()
		}
		if s.writebackQueue != nil {
			_ = s.writebackQueue.Close()
		}
		if closer, ok := s.stateBackend.(stateBackendCloser); ok && closer != nil {
			_ = closer.Close()
		}
		s.wg.Wait()
	})
}

func (s *Store) ListTree(workspaceID, path string, depth int, cursor string) (TreeResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ws, ok := s.workspaces[workspaceID]
	if !ok {
		return TreeResponse{Path: normalizePath(path), Entries: []TreeEntry{}, NextCursor: nil}, nil
	}

	base := normalizePath(path)
	if depth <= 0 {
		depth = 1
	}

	entryMap := map[string]TreeEntry{}
	for filePath, file := range ws.Files {
		if !withinBase(base, filePath) {
			continue
		}
		rest := strings.TrimPrefix(filePath, base)
		rest = strings.TrimPrefix(rest, "/")
		if rest == "" {
			continue
		}
		parts := strings.Split(rest, "/")
		if len(parts) == 0 {
			continue
		}
		maxLevel := depth
		if len(parts) < maxLevel {
			maxLevel = len(parts)
		}
		for level := 1; level <= maxLevel; level++ {
			child := joinPath(base, strings.Join(parts[:level], "/"))
			if level == len(parts) {
				entryMap[child] = TreeEntry{
					Path:             child,
					Type:             "file",
					Revision:         file.Revision,
					Provider:         file.Provider,
					ProviderObjectID: file.ProviderObjectID,
					Size:             int64(len(file.Content)),
					UpdatedAt:        file.LastEditedAt,
					PropertyCount:    len(file.Semantics.Properties),
					RelationCount:    len(file.Semantics.Relations),
					PermissionCount:  len(file.Semantics.Permissions),
					CommentCount:     len(file.Semantics.Comments),
				}
				continue
			}
			if _, exists := entryMap[child]; !exists {
				entryMap[child] = TreeEntry{Path: child, Type: "dir", Revision: "dir"}
			}
		}
	}

	entries := make([]TreeEntry, 0, len(entryMap))
	for _, entry := range entryMap {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

	return TreeResponse{Path: base, Entries: entries, NextCursor: nil}, nil
}

func (s *Store) ReadFile(workspaceID, path string) (File, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ws, ok := s.workspaces[workspaceID]
	if !ok {
		return File{}, ErrNotFound
	}
	file, ok := ws.Files[normalizePath(path)]
	if !ok {
		return File{}, ErrNotFound
	}
	return file, nil
}

func (s *Store) ResolveFilePermissions(workspaceID, path string, includeTarget bool) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ws, ok := s.workspaces[workspaceID]
	if !ok {
		return nil
	}
	target := normalizePath(path)
	permissions := make([]string, 0, 8)
	for _, dir := range ancestorDirectories(target) {
		markerPath := joinPath(dir, DirectoryPermissionMarkerFile)
		if markerPath == target {
			continue
		}
		marker, exists := ws.Files[markerPath]
		if !exists || len(marker.Semantics.Permissions) == 0 {
			continue
		}
		permissions = append(permissions, marker.Semantics.Permissions...)
	}
	if includeTarget {
		if file, exists := ws.Files[target]; exists && len(file.Semantics.Permissions) > 0 {
			permissions = append(permissions, file.Semantics.Permissions...)
		}
	}
	if len(permissions) == 0 {
		return nil
	}
	out := make([]string, len(permissions))
	copy(out, permissions)
	return out
}

func (s *Store) QueryFiles(workspaceID string, req FileQueryRequest) (FileQueryResponse, error) {
	if workspaceID == "" {
		return FileQueryResponse{}, ErrInvalidInput
	}
	base := normalizePath(req.PathPrefix)
	if req.PathPrefix == "" {
		base = "/"
	}
	provider := normalizeProvider(req.Provider)
	relation := strings.TrimSpace(req.Relation)
	permission := strings.TrimSpace(req.Permission)
	comment := strings.TrimSpace(req.Comment)
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	properties := normalizeProperties(req.Properties)

	s.mu.RLock()
	defer s.mu.RUnlock()

	ws, ok := s.workspaces[workspaceID]
	if !ok {
		if strings.TrimSpace(req.Cursor) != "" {
			return FileQueryResponse{}, ErrInvalidInput
		}
		return FileQueryResponse{Items: []FileQueryItem{}, NextCursor: nil}, nil
	}
	paths := make([]string, 0, len(ws.Files))
	for path := range ws.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	start := 0
	cursor := normalizePath(req.Cursor)
	if strings.TrimSpace(req.Cursor) != "" {
		found := false
		for i := range paths {
			if paths[i] == cursor {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return FileQueryResponse{}, ErrInvalidInput
		}
	}

	items := make([]FileQueryItem, 0, limit)
	var nextCursor *string

	for i := start; i < len(paths); i++ {
		path := paths[i]
		if !withinBase(base, path) {
			continue
		}
		file := ws.Files[path]
		if provider != "" && normalizeProvider(file.Provider) != provider {
			continue
		}
		semantics := normalizeSemantics(file.Semantics)
		if relation != "" && !stringSliceContains(semantics.Relations, relation) {
			continue
		}
		if permission != "" && !stringSliceContains(semantics.Permissions, permission) {
			continue
		}
		if comment != "" && !stringSliceContains(semantics.Comments, comment) {
			continue
		}
		if !propertiesMatch(semantics.Properties, properties) {
			continue
		}
		if len(items) >= limit {
			cursorValue := items[len(items)-1].Path
			nextCursor = &cursorValue
			break
		}
		items = append(items, FileQueryItem{
			Path:             path,
			Revision:         file.Revision,
			ContentType:      file.ContentType,
			Provider:         file.Provider,
			ProviderObjectID: file.ProviderObjectID,
			LastEditedAt:     file.LastEditedAt,
			Size:             int64(len(file.Content)),
			Properties:       copyStringMap(semantics.Properties),
			Relations:        append([]string(nil), semantics.Relations...),
			Permissions:      append([]string(nil), semantics.Permissions...),
			Comments:         append([]string(nil), semantics.Comments...),
		})
	}

	return FileQueryResponse{
		Items:      items,
		NextCursor: nextCursor,
	}, nil
}

func (s *Store) WriteFile(req WriteRequest) (WriteResult, error) {
	if req.IfMatch == "" {
		return WriteResult{}, ErrMissingPrecondition
	}
	if req.WorkspaceID == "" || req.Path == "" {
		return WriteResult{}, ErrInvalidInput
	}

	s.mu.Lock()
	ws := s.ensureWorkspaceLocked(req.WorkspaceID)
	path := normalizePath(req.Path)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	contentType := req.ContentType
	semantics := normalizeSemantics(req.Semantics)
	if contentType == "" {
		contentType = "text/markdown"
	}

	existing, exists := ws.Files[path]
	if !exists {
		if req.IfMatch != "0" {
			s.mu.Unlock()
			return WriteResult{}, ErrNotFound
		}
		revision := s.nextRevisionLocked()
		provider := inferProviderFromPath(path)
		ws.Files[path] = File{
			Path:         path,
			Revision:     revision,
			ContentType:  contentType,
			Content:      req.Content,
			Provider:     provider,
			LastEditedAt: now,
			Semantics:    semantics,
		}
		result, task := s.recordWriteLocked(ws, path, revision, "file.created", ws.Files[path].Provider, req.CorrelationID)
		_ = s.saveLocked()
		s.mu.Unlock()
		s.enqueueWriteback(task)
		return result, nil
	}

	if req.IfMatch != existing.Revision {
		s.mu.Unlock()
		return WriteResult{}, &ConflictError{
			ExpectedRevision:      req.IfMatch,
			CurrentRevision:       existing.Revision,
			CurrentContentPreview: truncatePreview(existing.Content),
		}
	}

	revision := s.nextRevisionLocked()
	existing.Revision = revision
	existing.ContentType = contentType
	existing.Content = req.Content
	existing.LastEditedAt = now
	if !isZeroSemantics(semantics) {
		existing.Semantics = semantics
	}
	ws.Files[path] = existing

	result, task := s.recordWriteLocked(ws, path, revision, "file.updated", existing.Provider, req.CorrelationID)
	_ = s.saveLocked()
	s.mu.Unlock()
	s.enqueueWriteback(task)
	return result, nil
}

func (s *Store) DeleteFile(req DeleteRequest) (WriteResult, error) {
	if req.IfMatch == "" {
		return WriteResult{}, ErrMissingPrecondition
	}
	if req.WorkspaceID == "" || req.Path == "" {
		return WriteResult{}, ErrInvalidInput
	}

	s.mu.Lock()
	ws := s.ensureWorkspaceLocked(req.WorkspaceID)
	path := normalizePath(req.Path)
	existing, exists := ws.Files[path]
	if !exists {
		s.mu.Unlock()
		return WriteResult{}, ErrNotFound
	}
	if req.IfMatch != existing.Revision {
		s.mu.Unlock()
		return WriteResult{}, &ConflictError{
			ExpectedRevision:      req.IfMatch,
			CurrentRevision:       existing.Revision,
			CurrentContentPreview: truncatePreview(existing.Content),
		}
	}
	delete(ws.Files, path)

	revision := s.nextRevisionLocked()
	result, task := s.recordWriteLocked(ws, path, revision, "file.deleted", existing.Provider, req.CorrelationID)
	_ = s.saveLocked()
	s.mu.Unlock()
	s.enqueueWriteback(task)
	return result, nil
}

func (s *Store) GetEvents(workspaceID, provider, cursor string, limit int) (EventFeed, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ws, ok := s.workspaces[workspaceID]
	if !ok || len(ws.Events) == 0 {
		return EventFeed{Events: []Event{}, NextCursor: nil}, nil
	}
	if limit <= 0 {
		limit = 200
	}
	events := ws.Events
	normalizedProvider := normalizeProvider(provider)
	if normalizedProvider != "" {
		filtered := make([]Event, 0, len(ws.Events))
		for _, event := range ws.Events {
			if eventProvider(event) == normalizedProvider {
				filtered = append(filtered, event)
			}
		}
		events = filtered
	}
	if len(events) == 0 {
		return EventFeed{Events: []Event{}, NextCursor: nil}, nil
	}

	start := 0
	if cursor != "" {
		for i := range events {
			if events[i].EventID == cursor {
				start = i + 1
				break
			}
		}
	}
	if start >= len(events) {
		return EventFeed{Events: []Event{}, NextCursor: nil}, nil
	}
	end := start + limit
	if end > len(events) {
		end = len(events)
	}
	chunk := append([]Event(nil), events[start:end]...)

	var nextCursor *string
	if end < len(events) {
		next := events[end-1].EventID
		nextCursor = &next
	}

	return EventFeed{Events: chunk, NextCursor: nextCursor}, nil
}

func (s *Store) GetOperation(workspaceID, opID string) (OperationStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ws, ok := s.workspaces[workspaceID]
	if !ok {
		return OperationStatus{}, ErrNotFound
	}
	op, ok := ws.Ops[opID]
	if !ok {
		return OperationStatus{}, ErrNotFound
	}
	return op, nil
}

func (s *Store) ListOperations(workspaceID, status, action, provider, cursor string, limit int) (OperationFeed, error) {
	if workspaceID == "" {
		return OperationFeed{}, ErrInvalidInput
	}
	provider = normalizeProvider(provider)
	if limit <= 0 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	ws, ok := s.workspaces[workspaceID]
	if !ok {
		if cursor != "" {
			return OperationFeed{}, ErrInvalidInput
		}
		return OperationFeed{Items: []OperationStatus{}, NextCursor: nil}, nil
	}
	opIDs := make([]string, 0, len(ws.Ops))
	for opID, op := range ws.Ops {
		if status != "" && op.Status != status {
			continue
		}
		if action != "" && op.Action != action {
			continue
		}
		if provider != "" && op.Provider != provider {
			continue
		}
		opIDs = append(opIDs, opID)
	}
	sort.Slice(opIDs, func(i, j int) bool {
		left := operationIDSeq(opIDs[i])
		right := operationIDSeq(opIDs[j])
		if left == right {
			return opIDs[i] > opIDs[j]
		}
		return left > right
	})
	start := 0
	if cursor != "" {
		found := false
		for i := range opIDs {
			if opIDs[i] == cursor {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return OperationFeed{}, ErrInvalidInput
		}
	}
	if start >= len(opIDs) {
		return OperationFeed{Items: []OperationStatus{}, NextCursor: nil}, nil
	}
	end := start + limit
	if end > len(opIDs) {
		end = len(opIDs)
	}
	items := make([]OperationStatus, 0, end-start)
	for _, opID := range opIDs[start:end] {
		items = append(items, ws.Ops[opID])
	}
	var next *string
	if end < len(opIDs) {
		cursorValue := opIDs[end-1]
		next = &cursorValue
	}
	return OperationFeed{Items: items, NextCursor: next}, nil
}

func (s *Store) GetSyncStatus(workspaceID string, provider string) (SyncStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.buildSyncStatusLocked(workspaceID, provider), nil
}

func (s *Store) ListSyncStatuses(provider string) map[string]SyncStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	normalizedProvider := normalizeProvider(provider)

	type syncProviderAggregate struct {
		latestProcessedAt         time.Time
		latestProcessedEnvelopeID string
		oldestPendingAt           time.Time
		latestDeadLetterAt        time.Time
		lastDeadLetterError       string
		deadLetteredEnvelopes     int
		latestDeadLetteredOpSeq   int64
		lastDeadLetteredOpError   string
		deadLetteredOps           int
		failureCodes              map[string]int
	}
	type syncWorkspaceAggregate struct {
		providers map[string]*syncProviderAggregate
	}

	workspaceSet := make(map[string]struct{}, len(s.workspaces)+len(s.ingressByWS))
	workspaceAggregates := map[string]*syncWorkspaceAggregate{}
	ensureWorkspace := func(workspaceID string) *syncWorkspaceAggregate {
		if workspaceID == "" {
			return nil
		}
		workspaceSet[workspaceID] = struct{}{}
		aggregate := workspaceAggregates[workspaceID]
		if aggregate == nil {
			aggregate = &syncWorkspaceAggregate{
				providers: map[string]*syncProviderAggregate{},
			}
			workspaceAggregates[workspaceID] = aggregate
		}
		return aggregate
	}
	ensureProvider := func(workspaceAggregate *syncWorkspaceAggregate, providerName string) *syncProviderAggregate {
		if workspaceAggregate == nil {
			return nil
		}
		providerName = normalizeProvider(providerName)
		if providerName == "" {
			return nil
		}
		aggregate := workspaceAggregate.providers[providerName]
		if aggregate == nil {
			aggregate = &syncProviderAggregate{
				failureCodes: map[string]int{},
			}
			workspaceAggregate.providers[providerName] = aggregate
		}
		return aggregate
	}

	for workspaceID := range s.workspaces {
		ensureWorkspace(workspaceID)
	}
	for workspaceID := range s.ingressByWS {
		ensureWorkspace(workspaceID)
	}
	for envelopeID, envelope := range s.envelopesByID {
		workspaceAggregate := ensureWorkspace(envelope.WorkspaceID)
		providerAggregate := ensureProvider(workspaceAggregate, envelope.Provider)
		if providerAggregate == nil {
			continue
		}
		receivedAt, err := time.Parse(time.RFC3339Nano, envelope.ReceivedAt)
		if err != nil {
			continue
		}
		if s.processedEnvs[envelopeID] {
			if providerAggregate.latestProcessedAt.IsZero() ||
				receivedAt.After(providerAggregate.latestProcessedAt) ||
				(receivedAt.Equal(providerAggregate.latestProcessedAt) && envelopeID > providerAggregate.latestProcessedEnvelopeID) {
				providerAggregate.latestProcessedAt = receivedAt
				providerAggregate.latestProcessedEnvelopeID = envelopeID
			}
			continue
		}
		if providerAggregate.oldestPendingAt.IsZero() || receivedAt.Before(providerAggregate.oldestPendingAt) {
			providerAggregate.oldestPendingAt = receivedAt
		}
	}
	for _, dead := range s.deadLetters {
		workspaceAggregate := ensureWorkspace(dead.WorkspaceID)
		providerAggregate := ensureProvider(workspaceAggregate, dead.Provider)
		if providerAggregate == nil {
			continue
		}
		providerAggregate.deadLetteredEnvelopes++
		providerAggregate.failureCodes[normalizeFailureCode(dead.LastError)]++
		failedAt, err := time.Parse(time.RFC3339Nano, dead.FailedAt)
		if err != nil {
			continue
		}
		if providerAggregate.latestDeadLetterAt.IsZero() || failedAt.After(providerAggregate.latestDeadLetterAt) {
			providerAggregate.latestDeadLetterAt = failedAt
			providerAggregate.lastDeadLetterError = dead.LastError
		}
	}
	for workspaceID, workspace := range s.workspaces {
		workspaceAggregate := ensureWorkspace(workspaceID)
		for _, file := range workspace.Files {
			_ = ensureProvider(workspaceAggregate, file.Provider)
		}
		for opID, op := range workspace.Ops {
			providerAggregate := ensureProvider(workspaceAggregate, op.Provider)
			if providerAggregate == nil || op.Status != "dead_lettered" || op.LastError == nil || *op.LastError == "" {
				continue
			}
			providerAggregate.deadLetteredOps++
			providerAggregate.failureCodes[normalizeFailureCode(*op.LastError)]++
			seq := operationIDSeq(opID)
			if seq > providerAggregate.latestDeadLetteredOpSeq {
				providerAggregate.latestDeadLetteredOpSeq = seq
				providerAggregate.lastDeadLetteredOpError = *op.LastError
			}
		}
	}

	now := time.Now().UTC()
	statuses := make(map[string]SyncStatus, len(workspaceSet))
	for workspaceID := range workspaceSet {
		workspaceAggregate := workspaceAggregates[workspaceID]
		providerNames := []string{}
		if normalizedProvider != "" {
			providerNames = append(providerNames, normalizedProvider)
		} else {
			providerSet := map[string]struct{}{
			}
			if workspaceAggregate != nil {
				for providerName := range workspaceAggregate.providers {
					if providerName == "" {
						continue
					}
					providerSet[providerName] = struct{}{}
				}
			}
			providerNames = make([]string, 0, len(providerSet))
			for providerName := range providerSet {
				providerNames = append(providerNames, providerName)
			}
			sort.Strings(providerNames)
		}

		providerStatuses := make([]SyncProviderStatus, 0, len(providerNames))
		for _, providerName := range providerNames {
			providerStatus := SyncProviderStatus{
				Provider: providerName,
				Status:   "healthy",
			}
			var providerAggregate *syncProviderAggregate
			if workspaceAggregate != nil {
				providerAggregate = workspaceAggregate.providers[providerName]
			}
			if providerAggregate != nil {
				if providerAggregate.latestProcessedEnvelopeID != "" {
					cursor := providerAggregate.latestProcessedEnvelopeID
					providerStatus.Cursor = &cursor
				}
				if !providerAggregate.latestProcessedAt.IsZero() {
					watermark := providerAggregate.latestProcessedAt.Format(time.RFC3339Nano)
					providerStatus.WatermarkTs = &watermark
				}
				if !providerAggregate.oldestPendingAt.IsZero() {
					lagSeconds := int(now.Sub(providerAggregate.oldestPendingAt).Seconds())
					if lagSeconds < 0 {
						lagSeconds = 0
					}
					providerStatus.LagSeconds = lagSeconds
					if lagSeconds > 30 {
						providerStatus.Status = "lagging"
					}
				}
				if providerAggregate.lastDeadLetterError != "" {
					providerStatus.Status = "error"
					lastError := providerAggregate.lastDeadLetterError
					providerStatus.LastError = &lastError
				} else if providerAggregate.lastDeadLetteredOpError != "" {
					providerStatus.Status = "error"
					lastError := providerAggregate.lastDeadLetteredOpError
					providerStatus.LastError = &lastError
				}
				providerStatus.DeadLetteredEnvelopes = providerAggregate.deadLetteredEnvelopes
				providerStatus.DeadLetteredOps = providerAggregate.deadLetteredOps
				if len(providerAggregate.failureCodes) > 0 {
					providerStatus.FailureCodes = providerAggregate.failureCodes
				}
			}
			providerStatuses = append(providerStatuses, providerStatus)
		}
		statuses[workspaceID] = SyncStatus{
			WorkspaceID: workspaceID,
			Providers:   providerStatuses,
		}
	}
	return statuses
}

func (s *Store) buildSyncStatusLocked(workspaceID string, provider string) SyncStatus {
	provider = normalizeProvider(provider)

	providers := map[string]struct{}{}
	if provider != "" {
		providers[provider] = struct{}{}
	} else {
		for envelopeID, envelope := range s.envelopesByID {
			if envelopeID == "" {
				continue
			}
			if envelope.WorkspaceID != workspaceID || envelope.Provider == "" {
				continue
			}
			providers[envelope.Provider] = struct{}{}
		}
		for _, dead := range s.deadLetters {
			if dead.WorkspaceID != workspaceID || dead.Provider == "" {
				continue
			}
			providers[dead.Provider] = struct{}{}
		}
		if ws, ok := s.workspaces[workspaceID]; ok {
			for _, file := range ws.Files {
				if file.Provider != "" {
					providers[file.Provider] = struct{}{}
				}
			}
			for _, op := range ws.Ops {
				if op.Provider != "" {
					providers[op.Provider] = struct{}{}
				}
			}
		}
	}

	providerNames := make([]string, 0, len(providers))
	for name := range providers {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)

	statuses := make([]SyncProviderStatus, 0, len(providerNames))
	for _, providerName := range providerNames {
		statuses = append(statuses, s.buildProviderSyncStatusLocked(workspaceID, providerName))
	}
	return SyncStatus{WorkspaceID: workspaceID, Providers: statuses}
}

func (s *Store) buildProviderSyncStatusLocked(workspaceID, provider string) SyncProviderStatus {
	status := SyncProviderStatus{
		Provider: provider,
		Status:   "healthy",
	}
	failureCodes := map[string]int{}
	now := time.Now().UTC()
	var latestProcessedAt time.Time
	var latestProcessedEnvelopeID string
	var oldestPendingAt time.Time

	for envelopeID, envelope := range s.envelopesByID {
		if envelope.WorkspaceID != workspaceID || envelope.Provider != provider {
			continue
		}
		receivedAt, err := time.Parse(time.RFC3339Nano, envelope.ReceivedAt)
		if err != nil {
			continue
		}
		if s.processedEnvs[envelopeID] {
			if latestProcessedAt.IsZero() || receivedAt.After(latestProcessedAt) {
				latestProcessedAt = receivedAt
				latestProcessedEnvelopeID = envelopeID
			}
			continue
		}
		if oldestPendingAt.IsZero() || receivedAt.Before(oldestPendingAt) {
			oldestPendingAt = receivedAt
		}
	}

	if latestProcessedEnvelopeID != "" {
		cursor := latestProcessedEnvelopeID
		status.Cursor = &cursor
	}
	if !latestProcessedAt.IsZero() {
		watermark := latestProcessedAt.Format(time.RFC3339Nano)
		status.WatermarkTs = &watermark
	}
	if !oldestPendingAt.IsZero() {
		lagSeconds := int(now.Sub(oldestPendingAt).Seconds())
		if lagSeconds < 0 {
			lagSeconds = 0
		}
		status.LagSeconds = lagSeconds
		if lagSeconds > 30 {
			status.Status = "lagging"
		}
	}

	var latestFailedAt time.Time
	var lastError string
	deadLetteredEnvelopes := 0
	for _, dead := range s.deadLetters {
		if dead.WorkspaceID != workspaceID || dead.Provider != provider {
			continue
		}
		deadLetteredEnvelopes++
		failedAt, err := time.Parse(time.RFC3339Nano, dead.FailedAt)
		if err != nil {
			continue
		}
		if latestFailedAt.IsZero() || failedAt.After(latestFailedAt) {
			latestFailedAt = failedAt
			lastError = dead.LastError
		}
		failureCodes[normalizeFailureCode(dead.LastError)]++
	}
	if lastError != "" {
		status.Status = "error"
		status.LastError = &lastError
	}
	status.DeadLetteredEnvelopes = deadLetteredEnvelopes
	if ws, ok := s.workspaces[workspaceID]; ok {
		latestDeadLetteredOpSeq := int64(-1)
		lastOpError := ""
		deadLetteredOps := 0
		for opID, op := range ws.Ops {
			if op.Provider != provider || op.Status != "dead_lettered" || op.LastError == nil || *op.LastError == "" {
				continue
			}
			deadLetteredOps++
			seq := operationIDSeq(opID)
			if seq > latestDeadLetteredOpSeq {
				latestDeadLetteredOpSeq = seq
				lastOpError = *op.LastError
			}
			failureCodes[normalizeFailureCode(*op.LastError)]++
		}
		if lastOpError != "" && status.LastError == nil {
			status.Status = "error"
			status.LastError = &lastOpError
		}
		status.DeadLetteredOps = deadLetteredOps
	}
	if len(failureCodes) > 0 {
		status.FailureCodes = failureCodes
	}

	return status
}

func (s *Store) GetIngressStatus(workspaceID string) (IngressStatus, error) {
	if workspaceID == "" {
		return IngressStatus{}, ErrInvalidInput
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	counter := s.ingressByWS[workspaceID]
	pending := 0
	pendingByProvider := map[string]int{}
	deadLetters := 0
	deadLettersByProvider := map[string]int{}
	var oldestPending time.Time
	oldestPendingByProvider := map[string]time.Time{}
	now := time.Now().UTC()
	for envelopeID, envelope := range s.envelopesByID {
		if envelope.WorkspaceID != workspaceID {
			continue
		}
		if !s.processedEnvs[envelopeID] {
			pending++
			pendingByProvider[envelope.Provider]++
			if ts, err := time.Parse(time.RFC3339Nano, envelope.ReceivedAt); err == nil {
				if oldestPending.IsZero() || ts.Before(oldestPending) {
					oldestPending = ts
				}
				if providerOldest, ok := oldestPendingByProvider[envelope.Provider]; !ok || ts.Before(providerOldest) {
					oldestPendingByProvider[envelope.Provider] = ts
				}
			}
		}
	}
	for _, dead := range s.deadLetters {
		if dead.WorkspaceID == workspaceID {
			deadLetters++
			deadLettersByProvider[dead.Provider]++
		}
	}
	utilization := 0.0
	queueDepth := 0
	queueCapacity := 0
	if s.envelopeQueue != nil {
		queueDepth = s.envelopeQueue.Depth()
		queueCapacity = s.envelopeQueue.Capacity()
	}
	if queueCapacity > 0 {
		utilization = float64(queueDepth) / float64(queueCapacity)
	}
	totalIngressAttempts := counter.AcceptedTotal + counter.DroppedTotal + counter.DedupedTotal + counter.CoalescedTotal
	dedupeRate := 0.0
	coalesceRate := 0.0
	if totalIngressAttempts > 0 {
		dedupeRate = float64(counter.DedupedTotal) / float64(totalIngressAttempts)
		coalesceRate = float64(counter.CoalescedTotal) / float64(totalIngressAttempts)
	}
	ingressByProvider := map[string]IngressProviderStatus{}
	providers := map[string]struct{}{}
	for provider := range counter.ByProvider {
		providers[provider] = struct{}{}
	}
	for provider := range pendingByProvider {
		providers[provider] = struct{}{}
	}
	for provider := range deadLettersByProvider {
		providers[provider] = struct{}{}
	}
	for provider := range providers {
		providerCounter := counter.ByProvider[provider]
		providerAttempts := providerCounter.AcceptedTotal + providerCounter.DroppedTotal + providerCounter.DedupedTotal + providerCounter.CoalescedTotal
		providerDedupeRate := 0.0
		providerCoalesceRate := 0.0
		if providerAttempts > 0 {
			providerDedupeRate = float64(providerCounter.DedupedTotal) / float64(providerAttempts)
			providerCoalesceRate = float64(providerCounter.CoalescedTotal) / float64(providerAttempts)
		}
		providerOldestPendingAgeSeconds := 0
		if providerOldestPending, ok := oldestPendingByProvider[provider]; ok {
			age := int(now.Sub(providerOldestPending).Seconds())
			if age > 0 {
				providerOldestPendingAgeSeconds = age
			}
		}
		ingressByProvider[provider] = IngressProviderStatus{
			AcceptedTotal:           providerCounter.AcceptedTotal,
			DroppedTotal:            providerCounter.DroppedTotal,
			DedupedTotal:            providerCounter.DedupedTotal,
			CoalescedTotal:          providerCounter.CoalescedTotal,
			PendingTotal:            pendingByProvider[provider],
			OldestPendingAgeSeconds: providerOldestPendingAgeSeconds,
			SuppressedTotal:         providerCounter.SuppressedTotal,
			StaleTotal:              providerCounter.StaleTotal,
			DedupeRate:              providerDedupeRate,
			CoalesceRate:            providerCoalesceRate,
		}
	}
	oldestPendingAgeSeconds := 0
	if !oldestPending.IsZero() {
		age := int(now.Sub(oldestPending).Seconds())
		if age > 0 {
			oldestPendingAgeSeconds = age
		}
	}
	return IngressStatus{
		WorkspaceID:             workspaceID,
		QueueDepth:              queueDepth,
		QueueCapacity:           queueCapacity,
		QueueUtilization:        utilization,
		PendingTotal:            pending,
		OldestPendingAgeSeconds: oldestPendingAgeSeconds,
		DeadLetterTotal:         deadLetters,
		DeadLetterByProvider:    deadLettersByProvider,
		AcceptedTotal:           counter.AcceptedTotal,
		DroppedTotal:            counter.DroppedTotal,
		DedupedTotal:            counter.DedupedTotal,
		CoalescedTotal:          counter.CoalescedTotal,
		DedupeRate:              dedupeRate,
		CoalesceRate:            coalesceRate,
		SuppressedTotal:         counter.SuppressedTotal,
		StaleTotal:              counter.StaleTotal,
		IngressByProvider:       ingressByProvider,
	}, nil
}

func (s *Store) GetIngressStatusForProvider(workspaceID, provider string) (IngressStatus, error) {
	provider = normalizeProvider(provider)
	if provider == "" {
		return s.GetIngressStatus(workspaceID)
	}
	status, err := s.GetIngressStatus(workspaceID)
	if err != nil {
		return IngressStatus{}, err
	}
	providerStatus, ok := status.IngressByProvider[provider]
	if !ok {
		providerStatus = IngressProviderStatus{}
	}
	filteredDeadLetters := map[string]int{}
	if deadCount, ok := status.DeadLetterByProvider[provider]; ok && deadCount > 0 {
		filteredDeadLetters[provider] = deadCount
	}
	status.QueueDepth = providerStatus.PendingTotal
	if status.QueueCapacity > 0 {
		status.QueueUtilization = float64(providerStatus.PendingTotal) / float64(status.QueueCapacity)
	} else {
		status.QueueUtilization = 0
	}
	status.PendingTotal = providerStatus.PendingTotal
	status.OldestPendingAgeSeconds = providerStatus.OldestPendingAgeSeconds
	status.DeadLetterTotal = filteredDeadLetters[provider]
	status.DeadLetterByProvider = filteredDeadLetters
	status.AcceptedTotal = providerStatus.AcceptedTotal
	status.DroppedTotal = providerStatus.DroppedTotal
	status.DedupedTotal = providerStatus.DedupedTotal
	status.CoalescedTotal = providerStatus.CoalescedTotal
	status.DedupeRate = providerStatus.DedupeRate
	status.CoalesceRate = providerStatus.CoalesceRate
	status.SuppressedTotal = providerStatus.SuppressedTotal
	status.StaleTotal = providerStatus.StaleTotal
	status.IngressByProvider = map[string]IngressProviderStatus{
		provider: providerStatus,
	}
	return status, nil
}

func (s *Store) ListIngressStatuses() map[string]IngressStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type pendingWorkspace struct {
		total            int
		byProvider       map[string]int
		oldest           time.Time
		oldestByProvider map[string]time.Time
	}
	type deadWorkspace struct {
		total      int
		byProvider map[string]int
	}

	workspaceSet := make(map[string]struct{}, len(s.workspaces)+len(s.ingressByWS))
	for workspaceID := range s.workspaces {
		workspaceSet[workspaceID] = struct{}{}
	}
	for workspaceID := range s.ingressByWS {
		workspaceSet[workspaceID] = struct{}{}
	}

	pendingByWorkspace := map[string]*pendingWorkspace{}
	for envelopeID, envelope := range s.envelopesByID {
		workspaceID := envelope.WorkspaceID
		if workspaceID == "" {
			continue
		}
		workspaceSet[workspaceID] = struct{}{}
		if s.processedEnvs[envelopeID] {
			continue
		}
		pending := pendingByWorkspace[workspaceID]
		if pending == nil {
			pending = &pendingWorkspace{
				byProvider:       map[string]int{},
				oldestByProvider: map[string]time.Time{},
			}
			pendingByWorkspace[workspaceID] = pending
		}
		pending.total++
		pending.byProvider[envelope.Provider]++
		if ts, err := time.Parse(time.RFC3339Nano, envelope.ReceivedAt); err == nil {
			if pending.oldest.IsZero() || ts.Before(pending.oldest) {
				pending.oldest = ts
			}
			if providerOldest, ok := pending.oldestByProvider[envelope.Provider]; !ok || ts.Before(providerOldest) {
				pending.oldestByProvider[envelope.Provider] = ts
			}
		}
	}

	deadByWorkspace := map[string]*deadWorkspace{}
	for _, dead := range s.deadLetters {
		workspaceID := dead.WorkspaceID
		if workspaceID == "" {
			continue
		}
		workspaceSet[workspaceID] = struct{}{}
		entry := deadByWorkspace[workspaceID]
		if entry == nil {
			entry = &deadWorkspace{byProvider: map[string]int{}}
			deadByWorkspace[workspaceID] = entry
		}
		entry.total++
		entry.byProvider[dead.Provider]++
	}

	queueDepth := 0
	queueCapacity := 0
	if s.envelopeQueue != nil {
		queueDepth = s.envelopeQueue.Depth()
		queueCapacity = s.envelopeQueue.Capacity()
	}
	utilization := 0.0
	if queueCapacity > 0 {
		utilization = float64(queueDepth) / float64(queueCapacity)
	}

	now := time.Now().UTC()
	statuses := make(map[string]IngressStatus, len(workspaceSet))
	for workspaceID := range workspaceSet {
		counter := s.ingressByWS[workspaceID]
		pending := pendingByWorkspace[workspaceID]
		dead := deadByWorkspace[workspaceID]

		pendingTotal := 0
		oldestPendingAgeSeconds := 0
		pendingByProvider := map[string]int{}
		oldestPendingByProvider := map[string]time.Time{}
		if pending != nil {
			pendingTotal = pending.total
			pendingByProvider = pending.byProvider
			oldestPendingByProvider = pending.oldestByProvider
			if !pending.oldest.IsZero() {
				if age := int(now.Sub(pending.oldest).Seconds()); age > 0 {
					oldestPendingAgeSeconds = age
				}
			}
		}

		deadLetterTotal := 0
		deadLetterByProvider := map[string]int{}
		if dead != nil {
			deadLetterTotal = dead.total
			deadLetterByProvider = dead.byProvider
		}

		totalIngressAttempts := counter.AcceptedTotal + counter.DroppedTotal + counter.DedupedTotal + counter.CoalescedTotal
		dedupeRate := 0.0
		coalesceRate := 0.0
		if totalIngressAttempts > 0 {
			dedupeRate = float64(counter.DedupedTotal) / float64(totalIngressAttempts)
			coalesceRate = float64(counter.CoalescedTotal) / float64(totalIngressAttempts)
		}

		providers := map[string]struct{}{}
		for provider := range counter.ByProvider {
			providers[provider] = struct{}{}
		}
		for provider := range pendingByProvider {
			providers[provider] = struct{}{}
		}
		for provider := range deadLetterByProvider {
			providers[provider] = struct{}{}
		}

		ingressByProvider := map[string]IngressProviderStatus{}
		for provider := range providers {
			providerCounter := counter.ByProvider[provider]
			providerAttempts := providerCounter.AcceptedTotal + providerCounter.DroppedTotal + providerCounter.DedupedTotal + providerCounter.CoalescedTotal
			providerDedupeRate := 0.0
			providerCoalesceRate := 0.0
			if providerAttempts > 0 {
				providerDedupeRate = float64(providerCounter.DedupedTotal) / float64(providerAttempts)
				providerCoalesceRate = float64(providerCounter.CoalescedTotal) / float64(providerAttempts)
			}
			providerOldestPendingAgeSeconds := 0
			if providerOldestPending, ok := oldestPendingByProvider[provider]; ok {
				if age := int(now.Sub(providerOldestPending).Seconds()); age > 0 {
					providerOldestPendingAgeSeconds = age
				}
			}
			ingressByProvider[provider] = IngressProviderStatus{
				AcceptedTotal:           providerCounter.AcceptedTotal,
				DroppedTotal:            providerCounter.DroppedTotal,
				DedupedTotal:            providerCounter.DedupedTotal,
				CoalescedTotal:          providerCounter.CoalescedTotal,
				PendingTotal:            pendingByProvider[provider],
				OldestPendingAgeSeconds: providerOldestPendingAgeSeconds,
				SuppressedTotal:         providerCounter.SuppressedTotal,
				StaleTotal:              providerCounter.StaleTotal,
				DedupeRate:              providerDedupeRate,
				CoalesceRate:            providerCoalesceRate,
			}
		}

		statuses[workspaceID] = IngressStatus{
			WorkspaceID:             workspaceID,
			QueueDepth:              queueDepth,
			QueueCapacity:           queueCapacity,
			QueueUtilization:        utilization,
			PendingTotal:            pendingTotal,
			OldestPendingAgeSeconds: oldestPendingAgeSeconds,
			DeadLetterTotal:         deadLetterTotal,
			DeadLetterByProvider:    deadLetterByProvider,
			AcceptedTotal:           counter.AcceptedTotal,
			DroppedTotal:            counter.DroppedTotal,
			DedupedTotal:            counter.DedupedTotal,
			CoalescedTotal:          counter.CoalescedTotal,
			DedupeRate:              dedupeRate,
			CoalesceRate:            coalesceRate,
			SuppressedTotal:         counter.SuppressedTotal,
			StaleTotal:              counter.StaleTotal,
			IngressByProvider:       ingressByProvider,
		}
	}
	return statuses
}

func (s *Store) ListDeadLetters(workspaceID, provider, cursor string, limit int) (DeadLetterFeed, error) {
	if workspaceID == "" {
		return DeadLetterFeed{}, ErrInvalidInput
	}
	provider = normalizeProvider(provider)
	if limit <= 0 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]EnvelopeDeadLetter, 0, len(s.deadLetters))
	for _, dead := range s.deadLetters {
		if dead.WorkspaceID != workspaceID {
			continue
		}
		if provider != "" && dead.Provider != provider {
			continue
		}
		items = append(items, dead)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].FailedAt == items[j].FailedAt {
			return items[i].EnvelopeID < items[j].EnvelopeID
		}
		return items[i].FailedAt > items[j].FailedAt
	})

	start := 0
	if cursor != "" {
		found := false
		for i := range items {
			if items[i].EnvelopeID == cursor {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return DeadLetterFeed{}, ErrInvalidInput
		}
	}
	if start >= len(items) {
		return DeadLetterFeed{Items: []EnvelopeDeadLetter{}, NextCursor: nil}, nil
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	slice := append([]EnvelopeDeadLetter(nil), items[start:end]...)
	var next *string
	if end < len(items) {
		cursorValue := items[end-1].EnvelopeID
		next = &cursorValue
	}
	return DeadLetterFeed{Items: slice, NextCursor: next}, nil
}

func (s *Store) GetDeadLetter(workspaceID, envelopeID string) (EnvelopeDeadLetter, error) {
	if workspaceID == "" || envelopeID == "" {
		return EnvelopeDeadLetter{}, ErrInvalidInput
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	item, ok := s.deadLetters[envelopeID]
	if !ok || item.WorkspaceID != workspaceID {
		return EnvelopeDeadLetter{}, ErrNotFound
	}
	return item, nil
}

func (s *Store) AcknowledgeDeadLetter(workspaceID, envelopeID, correlationID string) (AckResponse, error) {
	if workspaceID == "" || envelopeID == "" {
		return AckResponse{}, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.deadLetters[envelopeID]
	if !ok || item.WorkspaceID != workspaceID {
		return AckResponse{}, ErrNotFound
	}
	delete(s.deadLetters, envelopeID)
	_ = s.saveLocked()
	return AckResponse{
		Status:        "acknowledged",
		ID:            envelopeID,
		CorrelationID: correlationID,
	}, nil
}

// GetPendingWritebacks returns all pending writeback items for a workspace
func (s *Store) GetPendingWritebacks(workspaceID string) []map[string]any {
	if s.writebackQueue == nil {
		return []map[string]any{}
	}

	// Try to snapshot writebacks if the queue supports it
	snapshotter, ok := s.writebackQueue.(writebackQueueSnapshotter)
	if !ok {
		return []map[string]any{}
	}

	items := snapshotter.SnapshotWritebacks()
	result := make([]map[string]any, 0)
	for _, item := range items {
		if item.WorkspaceID == workspaceID {
			result = append(result, map[string]any{
				"id":              item.OpID,
				"workspaceId":     item.WorkspaceID,
				"path":            item.Path,
				"revision":        item.Revision,
				"correlationId":   item.CorrelationID,
			})
		}
	}
	return result
}

// AcknowledgeWriteback acknowledges a writeback item as processed
func (s *Store) AcknowledgeWriteback(workspaceID, itemID string, success bool, errMsg, correlationID string) (map[string]any, error) {
	if workspaceID == "" || itemID == "" {
		return nil, ErrInvalidInput
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Find the operation with the matching ID
	ws, ok := s.workspaces[workspaceID]
	if !ok {
		return nil, ErrNotFound
	}

	op, ok := ws.Ops[itemID]
	if !ok {
		return nil, ErrNotFound
	}

	// Update operation status based on acknowledgment
	if success {
		op.Status = "succeeded"
		op.LastError = nil
	} else {
		op.Status = "dead_lettered"
		if errMsg != "" {
			op.LastError = &errMsg
		}
	}
	op.NextAttemptAt = nil

	ws.Ops[itemID] = op
	_ = s.saveLocked()

	return map[string]any{
		"status":        "acknowledged",
		"id":            itemID,
		"correlationId": correlationID,
		"success":       success,
	}, nil
}

func (s *Store) TriggerSyncRefresh(workspaceID, provider, reason, correlationID string) (QueuedResponse, error) {
	if workspaceID == "" || provider == "" {
		return QueuedResponse{}, ErrInvalidInput
	}
	provider = normalizeProvider(provider)
	// Allow any provider to trigger refresh; no adapter requirement (provider-agnostic)
	jobID := fmt.Sprintf("sync_%d", time.Now().UnixNano())
	return QueuedResponse{
		Status:        "queued",
		ID:            jobID,
		CorrelationID: correlationID,
	}, nil
}

func (s *Store) GetBackendStatus() BackendStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stateBackendType := "none"
	if s.stateBackend != nil {
		stateBackendType = fmt.Sprintf("%T", s.stateBackend)
	}
	envelopeQueueType := "none"
	envelopeQueueDepth := 0
	envelopeQueueCap := 0
	if s.envelopeQueue != nil {
		envelopeQueueType = fmt.Sprintf("%T", s.envelopeQueue)
		envelopeQueueDepth = s.envelopeQueue.Depth()
		envelopeQueueCap = s.envelopeQueue.Capacity()
	}
	writebackQueueType := "none"
	writebackQueueDepth := 0
	writebackQueueCap := 0
	if s.writebackQueue != nil {
		writebackQueueType = fmt.Sprintf("%T", s.writebackQueue)
		writebackQueueDepth = s.writebackQueue.Depth()
		writebackQueueCap = s.writebackQueue.Capacity()
	}
	return BackendStatus{
		BackendProfile:      s.backendProfile,
		StateBackend:        stateBackendType,
		EnvelopeQueue:       envelopeQueueType,
		EnvelopeQueueDepth:  envelopeQueueDepth,
		EnvelopeQueueCap:    envelopeQueueCap,
		WritebackQueue:      writebackQueueType,
		WritebackQueueDepth: writebackQueueDepth,
		WritebackQueueCap:   writebackQueueCap,
	}
}

func (s *Store) IngestEnvelope(req WebhookEnvelopeRequest) (QueuedResponse, error) {
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	req.Provider = normalizeProvider(req.Provider)
	req.DeliveryID = strings.TrimSpace(req.DeliveryID)
	if req.WorkspaceID == "" || req.Provider == "" {
		return QueuedResponse{}, ErrInvalidInput
	}
	// DeliveryID is optional; generate one if not provided (for deduplication)
	if req.DeliveryID == "" {
		req.DeliveryID = fmt.Sprintf("dlv_%d", time.Now().UnixNano())
	}
	if req.ReceivedAt == "" {
		req.ReceivedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	envelopeID := req.EnvelopeID
	if envelopeID == "" {
		envelopeID = fmt.Sprintf("env_%d", time.Now().UnixNano())
		req.EnvelopeID = envelopeID
	}

	s.mu.Lock()
	key := deliveryKey(req.WorkspaceID, req.Provider, req.DeliveryID)
	if existingID, exists := s.deliveryIndex[key]; exists {
		s.adjustIngressCountsLocked(req.WorkspaceID, req.Provider, 0, 0, 1, 0, 0, 0)
		_ = s.saveLocked()
		s.mu.Unlock()
		return QueuedResponse{Status: "queued", ID: existingID, CorrelationID: req.CorrelationID}, nil
	}
	coalesceKey := coalesceObjectKey(req)
	if coalesceKey != "" {
		if existingID, exists := s.coalesceIndex[coalesceKey]; exists {
			if !s.processedEnvs[existingID] && !s.processingEnvs[existingID] {
				if existingReq, ok := s.envelopesByID[existingID]; ok && withinCoalesceWindow(existingReq.ReceivedAt, req.ReceivedAt, s.coalesceWindow) {
					existingReq.Headers = req.Headers
					existingReq.Payload = req.Payload
					existingReq.ReceivedAt = req.ReceivedAt
					if req.CorrelationID != "" {
						existingReq.CorrelationID = req.CorrelationID
					}
					s.envelopesByID[existingID] = existingReq
					s.deliveryIndex[key] = existingID
					s.adjustIngressCountsLocked(req.WorkspaceID, req.Provider, 0, 0, 0, 1, 0, 0)
					_ = s.saveLocked()
					s.mu.Unlock()
					return QueuedResponse{Status: "queued", ID: existingID, CorrelationID: req.CorrelationID}, nil
				}
			} else {
				delete(s.coalesceIndex, coalesceKey)
			}
		}
	}
	s.envelopesByID[envelopeID] = req
	s.deliveryIndex[key] = envelopeID
	if coalesceKey != "" {
		s.coalesceIndex[coalesceKey] = envelopeID
	}
	s.processedEnvs[envelopeID] = false
	s.adjustIngressCountsLocked(req.WorkspaceID, req.Provider, 1, 0, 0, 0, 0, 0)
	_ = s.saveLocked()
	s.mu.Unlock()

	if !s.tryEnqueueEnvelope(envelopeID) {
		s.mu.Lock()
		delete(s.envelopesByID, envelopeID)
		delete(s.deliveryIndex, key)
		if coalesceKey != "" {
			if currentID, ok := s.coalesceIndex[coalesceKey]; ok && currentID == envelopeID {
				delete(s.coalesceIndex, coalesceKey)
			}
		}
		delete(s.processedEnvs, envelopeID)
		delete(s.envelopeAttempts, envelopeID)
		delete(s.envelopeNextAttempt, envelopeID)
		s.adjustIngressCountsLocked(req.WorkspaceID, req.Provider, -1, 1, 0, 0, 0, 0)
		_ = s.saveLocked()
		s.mu.Unlock()
		return QueuedResponse{}, ErrQueueFull
	}

	return QueuedResponse{Status: "queued", ID: envelopeID, CorrelationID: req.CorrelationID}, nil
}

func (s *Store) ReplayEnvelope(envelopeID, correlationID string) (QueuedResponse, error) {
	s.mu.Lock()
	req, ok := s.envelopesByID[envelopeID]
	if !ok {
		s.mu.Unlock()
		return QueuedResponse{}, ErrNotFound
	}
	s.processedEnvs[envelopeID] = false
	delete(s.envelopeAttempts, envelopeID)
	delete(s.envelopeNextAttempt, envelopeID)
	delete(s.deadLetters, envelopeID)
	if req.CorrelationID == "" {
		req.CorrelationID = correlationID
		s.envelopesByID[envelopeID] = req
	}
	if key := coalesceObjectKey(req); key != "" {
		s.coalesceIndex[key] = envelopeID
	}
	_ = s.saveLocked()
	s.mu.Unlock()
	s.enqueueEnvelope(envelopeID)
	return QueuedResponse{Status: "queued", ID: envelopeID, CorrelationID: correlationID}, nil
}

func (s *Store) ReplayEnvelopeForWorkspace(workspaceID, envelopeID, correlationID string) (QueuedResponse, error) {
	s.mu.RLock()
	req, ok := s.envelopesByID[envelopeID]
	_, deadLettered := s.deadLetters[envelopeID]
	s.mu.RUnlock()
	if !ok {
		return QueuedResponse{}, ErrNotFound
	}
	if !deadLettered {
		return QueuedResponse{}, ErrNotFound
	}
	if req.WorkspaceID != workspaceID {
		return QueuedResponse{}, ErrNotFound
	}
	return s.ReplayEnvelope(envelopeID, correlationID)
}

func (s *Store) ReplayOperation(workspaceID, opID, correlationID string) (QueuedResponse, error) {
	s.mu.Lock()
	ws, ok := s.workspaces[workspaceID]
	if !ok {
		s.mu.Unlock()
		return QueuedResponse{}, ErrNotFound
	}
	op, ok := ws.Ops[opID]
	if !ok {
		s.mu.Unlock()
		return QueuedResponse{}, ErrNotFound
	}
	if op.Status != "dead_lettered" {
		s.mu.Unlock()
		return QueuedResponse{}, ErrInvalidState
	}
	op.Status = "pending"
	op.AttemptCount = 0
	op.LastError = nil
	op.CorrelationID = correlationID
	op.NextAttemptAt = nil
	ws.Ops[opID] = op
	_ = s.saveLocked()
	s.mu.Unlock()

	s.enqueueWriteback(writebackTask{
		WorkspaceID:   workspaceID,
		OpID:          opID,
		Path:          op.Path,
		Revision:      op.Revision,
		CorrelationID: correlationID,
	})
	return QueuedResponse{Status: "queued", ID: opID, CorrelationID: correlationID}, nil
}

func (s *Store) ReplayOperationAny(opID, correlationID string) (QueuedResponse, error) {
	s.mu.RLock()
	var workspaceID string
	for wsID, ws := range s.workspaces {
		if _, ok := ws.Ops[opID]; ok {
			workspaceID = wsID
			break
		}
	}
	s.mu.RUnlock()
	if workspaceID == "" {
		return QueuedResponse{}, ErrNotFound
	}
	return s.ReplayOperation(workspaceID, opID, correlationID)
}

func (s *Store) ensureWorkspaceLocked(workspaceID string) *workspaceState {
	ws, ok := s.workspaces[workspaceID]
	if ok {
		if ws.ProviderIndex == nil {
			ws.ProviderIndex = map[string]string{}
		}
		if ws.ProviderWatermarks == nil {
			ws.ProviderWatermarks = map[string]string{}
		}
		return ws
	}
	ws = &workspaceState{
		Files:              map[string]File{},
		Events:             []Event{},
		Ops:                map[string]OperationStatus{},
		ProviderIndex:      map[string]string{},
		ProviderWatermarks: map[string]string{},
	}
	s.workspaces[workspaceID] = ws
	return ws
}

func (s *Store) nextRevisionLocked() string {
	s.revCounter++
	return fmt.Sprintf("rev_%d", s.revCounter)
}

func (s *Store) nextOperationIDLocked() string {
	s.opCounter++
	return fmt.Sprintf("op_%d", s.opCounter)
}

func (s *Store) nextEventIDLocked() string {
	s.eventCounter++
	return fmt.Sprintf("evt_%d", s.eventCounter)
}

func (s *Store) recordWriteLocked(ws *workspaceState, path, revision, eventType, provider, correlationID string) (WriteResult, writebackTask) {
	if provider == "" {
	}
	opID := s.nextOperationIDLocked()
	op := OperationStatus{
		OpID:          opID,
		Path:          path,
		Revision:      revision,
		Action:        string(writebackActionFromEventType(eventType)),
		Provider:      provider,
		Status:        "pending",
		AttemptCount:  0,
		CorrelationID: correlationID,
	}
	ws.Ops[opID] = op

	event := Event{
		EventID:       s.nextEventIDLocked(),
		Type:          eventType,
		Path:          path,
		Revision:      revision,
		Origin:        "agent_write",
		Provider:      provider,
		CorrelationID: correlationID,
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
	}
	ws.Events = append(ws.Events, event)

	result := WriteResult{OpID: opID, Status: "queued", TargetRevision: revision}
	result.Writeback.Provider = provider
	result.Writeback.State = "pending"

	task := writebackTask{WorkspaceID: "", OpID: opID, Path: path, Revision: revision, CorrelationID: correlationID}
	for wsID, candidate := range s.workspaces {
		if candidate == ws {
			task.WorkspaceID = wsID
			break
		}
	}
	return result, task
}

func (s *Store) enqueueWriteback(task writebackTask) {
	if task.OpID == "" || s.writebackQueue == nil {
		return
	}
	select {
	case <-s.closed:
		return
	default:
	}
	s.queueMu.Lock()
	if _, exists := s.queuedWritebacks[task.OpID]; exists {
		s.queueMu.Unlock()
		return
	}
	s.queuedWritebacks[task.OpID] = struct{}{}
	s.queueMu.Unlock()
	if s.writebackQueue.TryEnqueue(task) {
		return
	}
	go func() {
		if !s.writebackQueue.Enqueue(s.queueCtx, task) {
			s.queueMu.Lock()
			delete(s.queuedWritebacks, task.OpID)
			s.queueMu.Unlock()
		}
	}()
}

func (s *Store) writebackWorker() {
	for {
		task, ok := s.writebackQueue.Dequeue(s.queueCtx)
		if !ok {
			return
		}
		s.queueMu.Lock()
		delete(s.queuedWritebacks, task.OpID)
		s.queueMu.Unlock()
		s.processWriteback(task)
	}
}

func (s *Store) tryEnqueueEnvelope(envelopeID string) bool {
	if envelopeID == "" || s.envelopeQueue == nil {
		return false
	}
	select {
	case <-s.closed:
		return false
	default:
	}
	s.queueMu.Lock()
	if _, exists := s.queuedEnvelopes[envelopeID]; exists {
		s.queueMu.Unlock()
		return true
	}
	s.queuedEnvelopes[envelopeID] = struct{}{}
	s.queueMu.Unlock()
	if s.envelopeQueue.TryEnqueue(envelopeID) {
		return true
	}
	s.queueMu.Lock()
	delete(s.queuedEnvelopes, envelopeID)
	s.queueMu.Unlock()
	return false
}

func (s *Store) enqueueEnvelope(envelopeID string) {
	if envelopeID == "" || s.envelopeQueue == nil {
		return
	}
	select {
	case <-s.closed:
		return
	default:
	}
	s.queueMu.Lock()
	if _, exists := s.queuedEnvelopes[envelopeID]; exists {
		s.queueMu.Unlock()
		return
	}
	s.queuedEnvelopes[envelopeID] = struct{}{}
	s.queueMu.Unlock()
	if s.envelopeQueue.TryEnqueue(envelopeID) {
		return
	}
	go func() {
		if !s.envelopeQueue.Enqueue(s.queueCtx, envelopeID) {
			s.queueMu.Lock()
			delete(s.queuedEnvelopes, envelopeID)
			s.queueMu.Unlock()
		}
	}()
}

func (s *Store) envelopeWorker() {
	for {
		envelopeID, ok := s.envelopeQueue.Dequeue(s.queueCtx)
		if !ok {
			return
		}
		s.queueMu.Lock()
		delete(s.queuedEnvelopes, envelopeID)
		s.queueMu.Unlock()
		release := s.acquireProviderSlot(envelopeID)
		s.processEnvelope(envelopeID)
		release()
	}
}

func (s *Store) acquireProviderSlot(envelopeID string) func() {
	if s.providerMaxConcurrency <= 0 {
		return func() {}
	}
	s.mu.RLock()
	req, ok := s.envelopesByID[envelopeID]
	s.mu.RUnlock()
	if !ok {
		return func() {}
	}
	provider := normalizeProvider(req.Provider)
	if provider == "" {
		return func() {}
	}
	workspace := strings.TrimSpace(req.WorkspaceID)
	if workspace == "" {
		return func() {}
	}
	key := workspace + "|" + provider

	s.providerSemMu.Lock()
	sem, exists := s.providerSemaphores[key]
	if !exists {
		sem = make(chan struct{}, s.providerMaxConcurrency)
		s.providerSemaphores[key] = sem
	}
	s.providerSemMu.Unlock()

	select {
	case sem <- struct{}{}:
		return func() {
			select {
			case <-sem:
			default:
			}
		}
	case <-s.closed:
		return func() {}
	}
}

func (s *Store) scheduleEnvelopeRetry(envelopeID string, delay time.Duration) {
	if envelopeID == "" {
		return
	}
	if delay <= 0 {
		delay = s.envelopeRetryDelay
	}
	time.AfterFunc(delay, func() {
		select {
		case <-s.closed:
			return
		default:
			s.enqueueEnvelope(envelopeID)
		}
	})
}

func (s *Store) processEnvelope(envelopeID string) {
	s.mu.Lock()
	req, ok := s.envelopesByID[envelopeID]
	if !ok {
		s.mu.Unlock()
		return
	}
	if s.processedEnvs[envelopeID] || s.processingEnvs[envelopeID] {
		s.mu.Unlock()
		return
	}
	s.processingEnvs[envelopeID] = true
	ws := s.ensureWorkspaceLocked(req.WorkspaceID)

	adapter, ok := s.adapters[req.Provider]
	// If no provider-specific adapter found, we'll use generic pass-through later
	// This allows any provider to submit webhooks
	if s.shouldSuppressEnvelopeLocked(req, time.Now().UTC()) {
		ws.Events = append(ws.Events, Event{
			EventID:       s.nextEventIDLocked(),
			Type:          "sync.suppressed",
			Path:          "/",
			Revision:      "",
			Origin:        "provider_sync",
			Provider:      req.Provider,
			CorrelationID: req.CorrelationID,
			Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		})
		s.adjustIngressCountsLocked(req.WorkspaceID, req.Provider, 0, 0, 0, 0, 1, 0)
		s.processedEnvs[envelopeID] = true
		s.clearCoalesceIndexLocked(req, envelopeID)
		delete(s.envelopeAttempts, envelopeID)
		delete(s.envelopeNextAttempt, envelopeID)
		delete(s.deadLetters, envelopeID)
		delete(s.processingEnvs, envelopeID)
		s.pruneProcessedEnvelopesLocked()
		_ = s.saveLocked()
		s.mu.Unlock()
		return
	}

	attempt := s.envelopeAttempts[envelopeID] + 1
	s.envelopeAttempts[envelopeID] = attempt
	s.mu.Unlock()

	// Parse envelope using provider-specific adapter, or generic pass-through
	var actions []ApplyAction
	var parseErr error
	if ok {
		// Provider-specific adapter available
		actions, parseErr = adapter.ParseEnvelope(req)
	} else {
		// Use generic pass-through parser for unknown providers
		actions, parseErr = ParseGenericEnvelope(req)
	}

	s.mu.Lock()
	req, ok = s.envelopesByID[envelopeID]
	if !ok || s.processedEnvs[envelopeID] {
		delete(s.processingEnvs, envelopeID)
		s.mu.Unlock()
		return
	}
	ws = s.ensureWorkspaceLocked(req.WorkspaceID)
	if parseErr != nil {
		errText := parseErr.Error()
		retryDelay := time.Duration(0)
		if attempt >= s.maxEnvelopeAttempts {
			ws.Events = append(ws.Events, Event{
				EventID:       s.nextEventIDLocked(),
				Type:          "sync.error",
				Path:          "/",
				Revision:      "",
				Origin:        "provider_sync",
				Provider:      req.Provider,
				CorrelationID: req.CorrelationID,
				Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
			})
			s.processedEnvs[envelopeID] = true
			s.clearCoalesceIndexLocked(req, envelopeID)
			s.deadLetters[envelopeID] = EnvelopeDeadLetter{
				EnvelopeID:    envelopeID,
				WorkspaceID:   req.WorkspaceID,
				Provider:      req.Provider,
				DeliveryID:    req.DeliveryID,
				CorrelationID: req.CorrelationID,
				FailedAt:      time.Now().UTC().Format(time.RFC3339Nano),
				AttemptCount:  attempt,
				LastError:     errText,
			}
			delete(s.envelopeAttempts, envelopeID)
			delete(s.envelopeNextAttempt, envelopeID)
			s.pruneProcessedEnvelopesLocked()
		} else {
			nextAttempt := time.Now().UTC().Add(s.envelopeRetryDelay)
			s.envelopeNextAttempt[envelopeID] = nextAttempt
			retryDelay = time.Until(nextAttempt)
		}
		delete(s.processingEnvs, envelopeID)
		_ = s.saveLocked()
		s.mu.Unlock()
		if attempt < s.maxEnvelopeAttempts {
			s.scheduleEnvelopeRetry(envelopeID, retryDelay)
		}
		return
	}
	for _, action := range actions {
		if s.isStaleProviderActionLocked(ws, req.Provider, action, req.ReceivedAt) {
			ws.Events = append(ws.Events, Event{
				EventID:       s.nextEventIDLocked(),
				Type:          "sync.stale",
				Path:          normalizePath(action.Path),
				Revision:      "",
				Origin:        "provider_sync",
				Provider:      req.Provider,
				CorrelationID: req.CorrelationID,
				Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
			})
			s.adjustIngressCountsLocked(req.WorkspaceID, req.Provider, 0, 0, 0, 0, 0, 1)
			continue
		}
		switch action.Type {
		case ActionFileUpsert:
			s.applyProviderUpsertLocked(ws, req.Provider, action, req.CorrelationID)
		case ActionFileDelete:
			s.applyProviderDeleteLocked(ws, req.Provider, action, req.CorrelationID)
		default:
			ws.Events = append(ws.Events, Event{
				EventID:       s.nextEventIDLocked(),
				Type:          "sync.ignored",
				Path:          "/",
				Revision:      "",
				Origin:        "provider_sync",
				Provider:      req.Provider,
				CorrelationID: req.CorrelationID,
				Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
			})
		}
	}
	s.processedEnvs[envelopeID] = true
	s.clearCoalesceIndexLocked(req, envelopeID)
	delete(s.envelopeAttempts, envelopeID)
	delete(s.envelopeNextAttempt, envelopeID)
	delete(s.deadLetters, envelopeID)
	delete(s.processingEnvs, envelopeID)
	s.pruneProcessedEnvelopesLocked()
	_ = s.saveLocked()
	s.mu.Unlock()
}

func (s *Store) processWriteback(task writebackTask) {
	// Step 1: capture current op state under lock.
	s.mu.Lock()
	ws, ok := s.workspaces[task.WorkspaceID]
	if !ok {
		s.mu.Unlock()
		return
	}
	op, ok := ws.Ops[task.OpID]
	if !ok {
		s.mu.Unlock()
		return
	}
	if op.Status != "pending" && op.Status != "running" {
		s.mu.Unlock()
		return
	}
	// Backfill path/revision for replay tasks if omitted.
	if task.Path == "" {
		task.Path = op.Path
	}
	if task.Revision == "" {
		task.Revision = op.Revision
	}
	writeAction := WritebackAction{
		WorkspaceID:   task.WorkspaceID,
		Path:          task.Path,
		Revision:      task.Revision,
		CorrelationID: task.CorrelationID,
	}
	if op.Provider != "" {
		writeAction.Provider = op.Provider
	}
	if op.Action != "" {
		writeAction.Type = WritebackActionType(op.Action)
	}
	if writeAction.Type == "" {
		if _, exists := ws.Files[task.Path]; exists {
			writeAction.Type = WritebackActionFileUpsert
		} else {
			writeAction.Type = WritebackActionFileDelete
		}
	}
	if file, exists := ws.Files[task.Path]; exists {
		writeAction.Provider = file.Provider
		writeAction.ProviderObjectID = file.ProviderObjectID
		if writeAction.Type == WritebackActionFileUpsert {
			writeAction.ContentType = file.ContentType
			writeAction.Content = file.Content
		}
	}
	if writeAction.Provider == "" {
	}
	attempt := op.AttemptCount + 1
	s.mu.Unlock()

	// Step 2: execute provider write outside the lock.
	var err error
	if s.providerWriteAction != nil {
		err = s.providerWriteAction(writeAction)
	} else if s.providerWriteConfigured {
		err = s.providerWrite(task.WorkspaceID, task.Path, task.Revision)
	} else if adapter, ok := s.adapters[writeAction.Provider]; ok {
		if outbound, ok := adapter.(ProviderWritebackAdapter); ok {
			err = outbound.ApplyWriteback(writeAction)
		} else {
			err = s.providerWrite(task.WorkspaceID, task.Path, task.Revision)
		}
	} else {
		err = s.providerWrite(task.WorkspaceID, task.Path, task.Revision)
	}

	// Step 3: persist state update and emit event.
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, ok = s.workspaces[task.WorkspaceID]
	if !ok {
		return
	}
	op, ok = ws.Ops[task.OpID]
	if !ok {
		return
	}
	op.AttemptCount = attempt
	op.Path = task.Path
	op.Revision = task.Revision
	op.CorrelationID = task.CorrelationID
	if op.Provider == "" {
		op.Provider = writeAction.Provider
	}

	now := time.Now().UTC()
	nowTS := now.Format(time.RFC3339Nano)
	if err == nil {
		op.Status = "succeeded"
		op.LastError = nil
		op.NextAttemptAt = nil
		op.ProviderResult = map[string]any{"providerRevision": task.Revision}
		ws.Ops[task.OpID] = op
		ws.Events = append(ws.Events, Event{
			EventID:       s.nextEventIDLocked(),
			Type:          "writeback.succeeded",
			Path:          task.Path,
			Revision:      task.Revision,
			Origin:        "system",
			Provider:      writeAction.Provider,
			CorrelationID: task.CorrelationID,
			Timestamp:     nowTS,
		})
		s.recordSuppressionLocked(task.WorkspaceID, writeAction.Provider, task.OpID, task.CorrelationID, now)
		_ = s.saveLocked()
		return
	}

	errText := err.Error()
	op.LastError = &errText
	if attempt >= s.maxAttempts {
		op.Status = "dead_lettered"
		op.NextAttemptAt = nil
		ws.Ops[task.OpID] = op
		ws.Events = append(ws.Events, Event{
			EventID:       s.nextEventIDLocked(),
			Type:          "writeback.failed",
			Path:          task.Path,
			Revision:      task.Revision,
			Origin:        "system",
			Provider:      writeAction.Provider,
			CorrelationID: task.CorrelationID,
			Timestamp:     nowTS,
		})
		_ = s.saveLocked()
		return
	}

	op.Status = "pending"
	nextAt := now.Add(s.retryDelay).Format(time.RFC3339Nano)
	op.NextAttemptAt = &nextAt
	ws.Ops[task.OpID] = op
	_ = s.saveLocked()

	retryTask := task
	time.AfterFunc(s.retryDelay, func() {
		select {
		case <-s.closed:
			return
		default:
			s.enqueueWriteback(retryTask)
		}
	})
}

func (s *Store) applyProviderUpsertLocked(ws *workspaceState, provider string, action ApplyAction, correlationID string) {
	path := normalizePath(action.Path)
	objectID := strings.TrimSpace(action.ProviderObjectID)
	if path == "/" && objectID != "" {
		if indexedPath, ok := ws.ProviderIndex[providerObjectKey(provider, objectID)]; ok {
			path = indexedPath
		} else {
			path = fallbackProviderPath(provider, objectID)
		}
	}
	if path == "/" {
		return
	}
	content := action.Content
	contentType := action.ContentType
	if contentType == "" {
		contentType = "text/markdown"
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Canonical identity: object ID is authoritative, path is projection.
	if objectID != "" {
		key := providerObjectKey(provider, objectID)
		if previousPath, ok := ws.ProviderIndex[key]; ok && previousPath != path {
			delete(ws.Files, previousPath)
			ws.Events = append(ws.Events, Event{
				EventID:       s.nextEventIDLocked(),
				Type:          "file.deleted",
				Path:          previousPath,
				Revision:      s.nextRevisionLocked(),
				Origin:        "provider_sync",
				Provider:      provider,
				CorrelationID: correlationID,
				Timestamp:     now,
			})
		}
	}

	existing, exists := ws.Files[path]
	revision := s.nextRevisionLocked()
	semantics := normalizeSemantics(action.Semantics)
	if exists && isZeroSemantics(semantics) {
		semantics = normalizeSemantics(existing.Semantics)
	}
	file := File{
		Path:             path,
		Revision:         revision,
		ContentType:      contentType,
		Content:          content,
		Provider:         provider,
		ProviderObjectID: objectID,
		LastEditedAt:     now,
		Semantics:        semantics,
	}
	ws.Files[path] = file
	if objectID != "" {
		ws.ProviderIndex[providerObjectKey(provider, objectID)] = path
	}

	fsEvent := "file.created"
	if exists {
		fsEvent = "file.updated"
		if existing.Content == file.Content && existing.Path == file.Path {
			// Keep update event for sync observability, but still revision-incremented.
		}
	}
	ws.Events = append(ws.Events, Event{
		EventID:       s.nextEventIDLocked(),
		Type:          fsEvent,
		Path:          path,
		Revision:      revision,
		Origin:        "provider_sync",
		Provider:      provider,
		CorrelationID: correlationID,
		Timestamp:     now,
	})
}

func (s *Store) applyProviderDeleteLocked(ws *workspaceState, provider string, action ApplyAction, correlationID string) {
	objectID := action.ProviderObjectID
	path := normalizePath(action.Path)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	if objectID != "" && path == "/" {
		if indexed, ok := ws.ProviderIndex[providerObjectKey(provider, objectID)]; ok {
			path = indexed
		}
	}
	if path == "/" {
		return
	}
	if _, ok := ws.Files[path]; !ok {
		return
	}
	delete(ws.Files, path)
	if objectID != "" {
		delete(ws.ProviderIndex, providerObjectKey(provider, objectID))
	}
	ws.Events = append(ws.Events, Event{
		EventID:       s.nextEventIDLocked(),
		Type:          "file.deleted",
		Path:          path,
		Revision:      s.nextRevisionLocked(),
		Origin:        "provider_sync",
		Provider:      provider,
		CorrelationID: correlationID,
		Timestamp:     now,
	})
}

func (s *Store) loadFromDisk() error {
	if s.stateBackend == nil {
		return nil
	}
	snapshot, err := s.stateBackend.Load()
	if err != nil {
		return err
	}
	if snapshot == nil {
		return nil
	}
	if snapshot.Workspaces != nil {
		s.workspaces = snapshot.Workspaces
		for _, ws := range s.workspaces {
			if ws.Files == nil {
				ws.Files = map[string]File{}
			}
			if ws.Ops == nil {
				ws.Ops = map[string]OperationStatus{}
			}
			if ws.ProviderIndex == nil {
				ws.ProviderIndex = map[string]string{}
			}
			if ws.ProviderWatermarks == nil {
				ws.ProviderWatermarks = map[string]string{}
			}
		}
	}
	if snapshot.EnvelopesByID != nil {
		s.envelopesByID = snapshot.EnvelopesByID
	}
	if snapshot.DeliveryIndex != nil {
		s.deliveryIndex = snapshot.DeliveryIndex
	}
	if snapshot.ProcessedEnvs != nil {
		s.processedEnvs = snapshot.ProcessedEnvs
	}
	if snapshot.IngressByWorkspace != nil {
		s.ingressByWS = snapshot.IngressByWorkspace
	}
	if snapshot.EnvelopeAttempts != nil {
		s.envelopeAttempts = snapshot.EnvelopeAttempts
	}
	if snapshot.EnvelopeNextAttempt != nil {
		s.envelopeNextAttempt = snapshot.EnvelopeNextAttempt
	}
	if snapshot.DeadLetters != nil {
		s.deadLetters = snapshot.DeadLetters
	}
	if snapshot.Suppressions != nil {
		s.suppressions = snapshot.Suppressions
	}
	s.revCounter = snapshot.RevCounter
	s.opCounter = snapshot.OpCounter
	s.eventCounter = snapshot.EventCounter
	return nil
}

func (s *Store) saveLocked() error {
	if s.stateBackend == nil {
		return nil
	}
	snapshot := persistedState{
		RevCounter:          s.revCounter,
		OpCounter:           s.opCounter,
		EventCounter:        s.eventCounter,
		Workspaces:          s.workspaces,
		EnvelopesByID:       s.envelopesByID,
		DeliveryIndex:       s.deliveryIndex,
		ProcessedEnvs:       s.processedEnvs,
		IngressByWorkspace:  s.ingressByWS,
		EnvelopeAttempts:    s.envelopeAttempts,
		EnvelopeNextAttempt: s.envelopeNextAttempt,
		DeadLetters:         s.deadLetters,
		Suppressions:        s.suppressions,
	}
	return s.stateBackend.Save(&snapshot)
}

func normalizePath(path string) string {
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	return path
}

func withinBase(base, candidate string) bool {
	base = normalizePath(base)
	candidate = normalizePath(candidate)
	if base == "/" {
		return true
	}
	return candidate == base || strings.HasPrefix(candidate, base+"/")
}

func joinPath(base, child string) string {
	base = normalizePath(base)
	if base == "/" {
		return "/" + child
	}
	return base + "/" + child
}

func ancestorDirectories(path string) []string {
	path = normalizePath(path)
	dirs := []string{"/"}

	parent := path
	if lastSlash := strings.LastIndex(path, "/"); lastSlash >= 0 {
		parent = path[:lastSlash]
		if parent == "" {
			parent = "/"
		}
	}
	if parent == "/" {
		return dirs
	}
	parts := strings.Split(strings.TrimPrefix(parent, "/"), "/")
	current := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = current + "/" + part
		dirs = append(dirs, current)
	}
	return dirs
}

func inferProviderFromPath(path string) string {
	normalized := normalizePath(path)
	trimmed := strings.TrimPrefix(normalized, "/")
	if trimmed == "" {
		return ""
	}
	parts := strings.SplitN(trimmed, "/", 2)
	provider := strings.TrimSpace(parts[0])
	if provider == "" {
		return ""
	}
	return strings.ToLower(provider)
}

func fallbackProviderPath(provider, objectID string) string {
	objectID = strings.TrimSpace(objectID)
	if objectID == "" {
		return "/"
	}
	normalizedProvider := normalizeProvider(provider)
	if normalizedProvider == "" {
		normalizedProvider = provider
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", " ", "_")
	safeObjectID := replacer.Replace(objectID)
	if safeObjectID == "" {
		safeObjectID = "object"
	}
	return normalizePath("/" + normalizedProvider + "/" + safeObjectID + ".md")
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func eventProvider(event Event) string {
	if normalized := normalizeProvider(event.Provider); normalized != "" {
		return normalized
	}
	path := normalizePath(event.Path)
	if path == "/" {
		return ""
	}
	return inferProviderFromPath(path)
}

func deliveryKey(workspaceID, provider, deliveryID string) string {
	return workspaceID + "|" + provider + "|" + deliveryID
}

func coalesceObjectKey(req WebhookEnvelopeRequest) string {
	if req.WorkspaceID == "" || req.Provider == "" {
		return ""
	}
	objectID := strings.TrimSpace(toString(req.Payload["objectId"]))
	if objectID != "" {
		return req.WorkspaceID + "|" + req.Provider + "|object:" + objectID
	}
	path := normalizePath(strings.TrimSpace(toString(req.Payload["path"])))
	if path != "/" {
		return req.WorkspaceID + "|" + req.Provider + "|path:" + path
	}
	return ""
}

func withinCoalesceWindow(existingReceivedAt, incomingReceivedAt string, window time.Duration) bool {
	if window <= 0 {
		return true
	}
	existingTS, err := time.Parse(time.RFC3339Nano, existingReceivedAt)
	if err != nil {
		return false
	}
	incomingTS, err := time.Parse(time.RFC3339Nano, incomingReceivedAt)
	if err != nil {
		return false
	}
	if incomingTS.Before(existingTS) {
		return false
	}
	return incomingTS.Sub(existingTS) <= window
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func normalizeProperties(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range in {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

func normalizeSemantics(in FileSemantics) FileSemantics {
	out := FileSemantics{
		Properties:  normalizeProperties(in.Properties),
		Relations:   normalizeStringSlice(in.Relations),
		Permissions: normalizeStringSlice(in.Permissions),
		Comments:    normalizeStringSlice(in.Comments),
	}
	return out
}

func isZeroSemantics(in FileSemantics) bool {
	return len(in.Properties) == 0 &&
		len(in.Relations) == 0 &&
		len(in.Permissions) == 0 &&
		len(in.Comments) == 0
}

func stringSliceContains(values []string, needle string) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return false
	}
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func propertiesMatch(actual map[string]string, expected map[string]string) bool {
	if len(expected) == 0 {
		return true
	}
	for key, value := range expected {
		if actual == nil {
			return false
		}
		if actual[key] != value {
			return false
		}
	}
	return true
}

func truncatePreview(content string) string {
	if len(content) <= 4000 {
		return content
	}
	return content[:4000]
}

func providerObjectKey(provider, objectID string) string {
	if provider == "" {
		return objectID
	}
	return provider + "|" + objectID
}

func normalizeFailureCode(errText string) string {
	normalized := strings.ToLower(strings.TrimSpace(errText))
	if normalized == "" {
		return "unknown"
	}
	switch {
	case strings.Contains(normalized, "429"), strings.Contains(normalized, "rate limit"), strings.Contains(normalized, "rate_limited"):
		return "rate_limited"
	case strings.Contains(normalized, "timeout"), strings.Contains(normalized, "timed out"), strings.Contains(normalized, "deadline exceeded"):
		return "timeout"
	case strings.Contains(normalized, "401"), strings.Contains(normalized, "unauthorized"):
		return "unauthorized"
	case strings.Contains(normalized, "403"), strings.Contains(normalized, "forbidden"):
		return "forbidden"
	case strings.Contains(normalized, "404"), strings.Contains(normalized, "not found"):
		return "not_found"
	case strings.Contains(normalized, "409"), strings.Contains(normalized, "conflict"):
		return "conflict"
	case strings.Contains(normalized, "500"), strings.Contains(normalized, "502"), strings.Contains(normalized, "503"), strings.Contains(normalized, "504"), strings.Contains(normalized, "internal server"):
		return "provider_unavailable"
	default:
		return "unknown"
	}
}

func providerWatermarkKey(provider string, action ApplyAction) string {
	if action.ProviderObjectID != "" {
		return "object:" + providerObjectKey(provider, action.ProviderObjectID)
	}
	path := normalizePath(action.Path)
	if path != "/" {
		return "path:" + provider + "|" + path
	}
	return ""
}

func (s *Store) isStaleProviderActionLocked(ws *workspaceState, provider string, action ApplyAction, receivedAtRaw string) bool {
	if ws == nil {
		return false
	}
	key := providerWatermarkKey(provider, action)
	if key == "" {
		return false
	}
	receivedAt, err := time.Parse(time.RFC3339Nano, receivedAtRaw)
	if err != nil {
		return false
	}
	if ws.ProviderWatermarks == nil {
		ws.ProviderWatermarks = map[string]string{}
	}
	if watermarkRaw, ok := ws.ProviderWatermarks[key]; ok {
		watermark, err := time.Parse(time.RFC3339Nano, watermarkRaw)
		if err == nil && receivedAt.Before(watermark) {
			return true
		}
	}
	ws.ProviderWatermarks[key] = receivedAt.Format(time.RFC3339Nano)
	return false
}

func operationIDSeq(opID string) int64 {
	if !strings.HasPrefix(opID, "op_") {
		return -1
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(opID, "op_"), 10, 64)
	if err != nil {
		return -1
	}
	return n
}

func writebackActionFromEventType(eventType string) WritebackActionType {
	if eventType == "file.deleted" {
		return WritebackActionFileDelete
	}
	return WritebackActionFileUpsert
}

func (s *Store) recordSuppressionLocked(workspaceID, provider, opID, correlationID string, now time.Time) {
	if s.suppressionWindow <= 0 || workspaceID == "" || provider == "" {
		return
	}
	s.pruneSuppressionsLocked(now)
	expiresAt := now.Add(s.suppressionWindow)
	if opID != "" {
		s.suppressions[suppressionKey(workspaceID, provider, "op:"+opID)] = expiresAt
	}
	if correlationID != "" {
		s.suppressions[suppressionKey(workspaceID, provider, "corr:"+correlationID)] = expiresAt
	}
}

func (s *Store) shouldSuppressEnvelopeLocked(req WebhookEnvelopeRequest, now time.Time) bool {
	if s.suppressionWindow <= 0 {
		return false
	}
	origin := strings.ToLower(strings.TrimSpace(toString(req.Payload["origin"])))
	if origin != "relayfile" {
		return false
	}
	s.pruneSuppressionsLocked(now)
	markers := make([]string, 0, 3)
	if opID := strings.TrimSpace(toString(req.Payload["opId"])); opID != "" {
		markers = append(markers, "op:"+opID)
	}
	if payloadCorr := strings.TrimSpace(toString(req.Payload["correlationId"])); payloadCorr != "" {
		markers = append(markers, "corr:"+payloadCorr)
	}
	if req.CorrelationID != "" {
		markers = append(markers, "corr:"+strings.TrimSpace(req.CorrelationID))
	}
	for _, marker := range markers {
		key := suppressionKey(req.WorkspaceID, req.Provider, marker)
		if expiresAt, ok := s.suppressions[key]; ok {
			if now.Before(expiresAt) {
				return true
			}
			delete(s.suppressions, key)
		}
	}
	return false
}

func (s *Store) pruneSuppressionsLocked(now time.Time) {
	for key, expiresAt := range s.suppressions {
		if !now.Before(expiresAt) {
			delete(s.suppressions, key)
		}
	}
}

func suppressionKey(workspaceID, provider, marker string) string {
	return workspaceID + "|" + provider + "|" + marker
}

func applyIngressDelta(current uint64, delta int64) uint64 {
	if delta >= 0 {
		return current + uint64(delta)
	}
	dec := uint64(-delta)
	if current < dec {
		return 0
	}
	return current - dec
}

func (s *Store) adjustIngressCountsLocked(workspaceID, provider string, acceptedDelta, droppedDelta, dedupedDelta, coalescedDelta, suppressedDelta, staleDelta int64) {
	counter := s.ingressByWS[workspaceID]
	counter.AcceptedTotal = applyIngressDelta(counter.AcceptedTotal, acceptedDelta)
	counter.DroppedTotal = applyIngressDelta(counter.DroppedTotal, droppedDelta)
	counter.DedupedTotal = applyIngressDelta(counter.DedupedTotal, dedupedDelta)
	counter.CoalescedTotal = applyIngressDelta(counter.CoalescedTotal, coalescedDelta)
	counter.SuppressedTotal = applyIngressDelta(counter.SuppressedTotal, suppressedDelta)
	counter.StaleTotal = applyIngressDelta(counter.StaleTotal, staleDelta)
	if provider != "" {
		if counter.ByProvider == nil {
			counter.ByProvider = map[string]providerIngressCounter{}
		}
		providerCounter := counter.ByProvider[provider]
		providerCounter.AcceptedTotal = applyIngressDelta(providerCounter.AcceptedTotal, acceptedDelta)
		providerCounter.DroppedTotal = applyIngressDelta(providerCounter.DroppedTotal, droppedDelta)
		providerCounter.DedupedTotal = applyIngressDelta(providerCounter.DedupedTotal, dedupedDelta)
		providerCounter.CoalescedTotal = applyIngressDelta(providerCounter.CoalescedTotal, coalescedDelta)
		providerCounter.SuppressedTotal = applyIngressDelta(providerCounter.SuppressedTotal, suppressedDelta)
		providerCounter.StaleTotal = applyIngressDelta(providerCounter.StaleTotal, staleDelta)
		counter.ByProvider[provider] = providerCounter
	}
	s.ingressByWS[workspaceID] = counter
}

func (s *Store) clearCoalesceIndexLocked(req WebhookEnvelopeRequest, envelopeID string) {
	key := coalesceObjectKey(req)
	if key == "" {
		return
	}
	if currentID, ok := s.coalesceIndex[key]; ok && currentID == envelopeID {
		delete(s.coalesceIndex, key)
	}
}

func (s *Store) rebuildCoalesceIndexLocked() {
	s.coalesceIndex = map[string]string{}
	latestByKey := map[string]struct {
		id         string
		receivedAt time.Time
		hasTime    bool
	}{}
	for envelopeID, req := range s.envelopesByID {
		if s.processedEnvs[envelopeID] {
			continue
		}
		key := coalesceObjectKey(req)
		if key == "" {
			continue
		}
		receivedAt, parseErr := time.Parse(time.RFC3339Nano, req.ReceivedAt)
		candidate := struct {
			id         string
			receivedAt time.Time
			hasTime    bool
		}{
			id:         envelopeID,
			receivedAt: receivedAt,
			hasTime:    parseErr == nil,
		}
		current, exists := latestByKey[key]
		if !exists {
			latestByKey[key] = candidate
			continue
		}
		switch {
		case candidate.hasTime && !current.hasTime:
			latestByKey[key] = candidate
		case candidate.hasTime && current.hasTime && candidate.receivedAt.After(current.receivedAt):
			latestByKey[key] = candidate
		case candidate.hasTime == current.hasTime && candidate.receivedAt.Equal(current.receivedAt) && candidate.id > current.id:
			latestByKey[key] = candidate
		}
	}
	for key, candidate := range latestByKey {
		s.coalesceIndex[key] = candidate.id
	}
}

func (s *Store) pruneProcessedEnvelopesLocked() {
	if s.maxStoredEnvelopes <= 0 {
		return
	}
	if len(s.envelopesByID) <= s.maxStoredEnvelopes {
		return
	}

	type candidate struct {
		id         string
		receivedAt time.Time
	}
	candidates := make([]candidate, 0, len(s.envelopesByID))
	for envelopeID, req := range s.envelopesByID {
		if !s.processedEnvs[envelopeID] {
			continue
		}
		if _, isDead := s.deadLetters[envelopeID]; isDead {
			continue
		}
		ts, _ := time.Parse(time.RFC3339Nano, req.ReceivedAt)
		candidates = append(candidates, candidate{id: envelopeID, receivedAt: ts})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].receivedAt.Equal(candidates[j].receivedAt) {
			return candidates[i].id < candidates[j].id
		}
		if candidates[i].receivedAt.IsZero() {
			return true
		}
		if candidates[j].receivedAt.IsZero() {
			return false
		}
		return candidates[i].receivedAt.Before(candidates[j].receivedAt)
	})

	for _, item := range candidates {
		if len(s.envelopesByID) <= s.maxStoredEnvelopes {
			return
		}
		req, ok := s.envelopesByID[item.id]
		if !ok {
			continue
		}
		s.clearCoalesceIndexLocked(req, item.id)
		delete(s.envelopesByID, item.id)
		delete(s.processedEnvs, item.id)
		delete(s.envelopeAttempts, item.id)
		delete(s.envelopeNextAttempt, item.id)
		key := deliveryKey(req.WorkspaceID, req.Provider, req.DeliveryID)
		if existingID, ok := s.deliveryIndex[key]; ok && existingID == item.id {
			delete(s.deliveryIndex, key)
		}
	}
}
