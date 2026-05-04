package mountsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fsnotify/fsnotify"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

var ErrConflict = errors.New("revision conflict")

// ErrSchemaValidation is returned when the cloud rejects a writeback because
// the body fails the adapter's declared schema (contract §8.2). The CLI
// quarantines the offending local file and restores the prior remote
// version instead of treating the failure as a fatal sync error.
var ErrSchemaValidation = errors.New("schema validation failed")

// SchemaValidationError carries the per-file schema-violation message the
// cloud emits alongside `400 schema_validation_failed`.
type SchemaValidationError struct {
	Path    string
	Message string
}

func (e *SchemaValidationError) Error() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return fmt.Sprintf("schema validation failed for %s", e.Path)
	}
	return fmt.Sprintf("schema validation failed for %s: %s", e.Path, e.Message)
}

func (e *SchemaValidationError) Is(target error) bool {
	return target == ErrSchemaValidation
}

const defaultBulkFlushThreshold = 256

type ConflictError struct {
	Path string
}

func (e *ConflictError) Error() string {
	if e.Path == "" {
		return "revision conflict"
	}
	return fmt.Sprintf("revision conflict for %s", e.Path)
}

func (e *ConflictError) Is(target error) bool {
	return target == ErrConflict
}

type HTTPError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *HTTPError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("http %d %s: %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("http %d: %s", e.StatusCode, e.Message)
}

type TreeEntry struct {
	Path     string `json:"path"`
	Type     string `json:"type"`
	Revision string `json:"revision"`
}

type TreeResponse struct {
	Path       string      `json:"path"`
	Entries    []TreeEntry `json:"entries"`
	NextCursor *string     `json:"nextCursor"`
}

type FilesystemEvent struct {
	EventID   string `json:"eventId"`
	Type      string `json:"type"`
	Path      string `json:"path"`
	Revision  string `json:"revision"`
	Provider  string `json:"provider,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

type EventFeed struct {
	Events     []FilesystemEvent `json:"events"`
	NextCursor *string           `json:"nextCursor"`
}

type RemoteFile struct {
	Path        string `json:"path"`
	Revision    string `json:"revision"`
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
	Encoding    string `json:"encoding,omitempty"`
}

type WriteResult struct {
	TargetRevision string `json:"targetRevision"`
}

type RemoteClient interface {
	ListTree(ctx context.Context, workspaceID, path string, depth int, cursor string) (TreeResponse, error)
	ListEvents(ctx context.Context, workspaceID, provider, cursor string, limit int) (EventFeed, error)
	ReadFile(ctx context.Context, workspaceID, path string) (RemoteFile, error)
	WriteFile(ctx context.Context, workspaceID, path, baseRevision, contentType, content string) (WriteResult, error)
	WriteFilesBulk(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error)
	DeleteFile(ctx context.Context, workspaceID, path, baseRevision string) error
}

type exportSnapshotClient interface {
	ExportFiles(ctx context.Context, workspaceID, path string) ([]RemoteFile, error)
}

type HTTPClient struct {
	baseURL    string
	token      string
	tokenMu    sync.RWMutex
	httpClient *http.Client
	maxRetries int
	baseDelay  time.Duration
	maxDelay   time.Duration
}

func NewHTTPClient(baseURL, token string, httpClient *http.Client) *HTTPClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &HTTPClient{
		baseURL:    baseURL,
		token:      strings.TrimSpace(token),
		httpClient: httpClient,
		maxRetries: 3,
		baseDelay:  100 * time.Millisecond,
		maxDelay:   2 * time.Second,
	}
}

func (c *HTTPClient) ListTree(ctx context.Context, workspaceID, path string, depth int, cursor string) (TreeResponse, error) {
	q := url.Values{}
	q.Set("path", normalizeRemotePath(path))
	if depth > 0 {
		q.Set("depth", fmt.Sprintf("%d", depth))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var out TreeResponse
	err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/v1/workspaces/%s/fs/tree?%s", url.PathEscape(workspaceID), q.Encode()), nil, nil, &out)
	return out, err
}

func (c *HTTPClient) ListEvents(ctx context.Context, workspaceID, provider, cursor string, limit int) (EventFeed, error) {
	q := url.Values{}
	if strings.TrimSpace(provider) != "" {
		q.Set("provider", strings.TrimSpace(provider))
	}
	if strings.TrimSpace(cursor) != "" {
		q.Set("cursor", strings.TrimSpace(cursor))
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	var out EventFeed
	err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/v1/workspaces/%s/fs/events?%s", url.PathEscape(workspaceID), q.Encode()), nil, nil, &out)
	return out, err
}

func (c *HTTPClient) ReadFile(ctx context.Context, workspaceID, path string) (RemoteFile, error) {
	q := url.Values{}
	q.Set("path", normalizeRemotePath(path))
	var out RemoteFile
	err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/v1/workspaces/%s/fs/file?%s", url.PathEscape(workspaceID), q.Encode()), nil, nil, &out)
	return out, err
}

func (c *HTTPClient) WriteFile(ctx context.Context, workspaceID, path, baseRevision, contentType, content string) (WriteResult, error) {
	return c.writeFile(ctx, workspaceID, path, baseRevision, contentType, content, "")
}

func (c *HTTPClient) writeFile(ctx context.Context, workspaceID, path, baseRevision, contentType, content, encoding string) (WriteResult, error) {
	if contentType == "" {
		contentType = "text/markdown"
	}
	body := map[string]any{
		"contentType": contentType,
		"content":     content,
	}
	if strings.TrimSpace(encoding) != "" {
		body["encoding"] = strings.TrimSpace(encoding)
	}
	q := url.Values{}
	q.Set("path", normalizeRemotePath(path))
	headers := map[string]string{
		"If-Match": baseRevision,
	}
	var out WriteResult
	err := c.doJSON(ctx, http.MethodPut, fmt.Sprintf("/v1/workspaces/%s/fs/file?%s", url.PathEscape(workspaceID), q.Encode()), headers, body, &out)
	return out, err
}

func (c *HTTPClient) WriteFilesBulk(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
	if len(files) == 0 {
		return BulkWriteResponse{}, ErrEmptyBulkWrite
	}
	body := struct {
		Files []BulkWriteFile `json:"files"`
	}{
		Files: files,
	}
	var out BulkWriteResponse
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/v1/workspaces/%s/fs/bulk", url.PathEscape(workspaceID)), nil, body, &out)
	return out, err
}

func (c *HTTPClient) DeleteFile(ctx context.Context, workspaceID, path, baseRevision string) error {
	q := url.Values{}
	q.Set("path", normalizeRemotePath(path))
	headers := map[string]string{
		"If-Match": baseRevision,
	}
	return c.doJSON(ctx, http.MethodDelete, fmt.Sprintf("/v1/workspaces/%s/fs/file?%s", url.PathEscape(workspaceID), q.Encode()), headers, nil, nil)
}

func (c *HTTPClient) ExportFiles(ctx context.Context, workspaceID, path string) ([]RemoteFile, error) {
	q := url.Values{}
	q.Set("format", "json")
	q.Set("path", normalizeRemotePath(path))
	var out []struct {
		Path        string `json:"path"`
		Revision    string `json:"revision"`
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
		Encoding    string `json:"encoding"`
	}
	err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/v1/workspaces/%s/fs/export?%s", url.PathEscape(workspaceID), q.Encode()), nil, nil, &out)
	if err != nil {
		return nil, err
	}
	files := make([]RemoteFile, 0, len(out))
	for _, file := range out {
		remotePath := normalizeRemotePath(file.Path)
		if remotePath == "/" {
			continue
		}
		files = append(files, RemoteFile{
			Path:        remotePath,
			Revision:    file.Revision,
			ContentType: file.ContentType,
			Content:     file.Content,
			Encoding:    strings.TrimSpace(file.Encoding),
		})
	}
	return files, nil
}

func (c *HTTPClient) doJSON(
	ctx context.Context,
	method, requestPath string,
	headers map[string]string,
	body any,
	out any,
) error {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		var bodyReader io.Reader
		if bodyBytes != nil {
			bodyReader = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+requestPath, bodyReader)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token())
		req.Header.Set("X-Correlation-Id", correlationID())
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if attempt < c.maxRetries {
				if waitErr := waitWithContext(ctx, c.retryDelay(attempt+1, "")); waitErr != nil {
					return waitErr
				}
				continue
			}
			return err
		}
		// Limit response body to 64MB to prevent unbounded memory usage.
		const maxResponseSize = 64 << 20
		payloadBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
		_ = resp.Body.Close()
		if readErr != nil {
			return readErr
		}

		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			if out == nil || len(payloadBytes) == 0 {
				return nil
			}
			return json.Unmarshal(payloadBytes, out)
		}

		if (resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode >= 500 && resp.StatusCode <= 599)) && attempt < c.maxRetries {
			if waitErr := waitWithContext(ctx, c.retryDelay(attempt+1, resp.Header.Get("Retry-After"))); waitErr != nil {
				return waitErr
			}
			continue
		}

		var errPayload struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(payloadBytes, &errPayload)
		if resp.StatusCode == http.StatusConflict {
			return &ConflictError{Path: requestPath}
		}
		return &HTTPError{
			StatusCode: resp.StatusCode,
			Code:       errPayload.Code,
			Message:    errPayload.Message,
		}
	}
}

func (c *HTTPClient) Token() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.token
}

func (c *HTTPClient) SetToken(token string) {
	c.tokenMu.Lock()
	c.token = strings.TrimSpace(token)
	c.tokenMu.Unlock()
}

type SyncerOptions struct {
	WorkspaceID   string
	RemoteRoot    string
	LocalRoot     string
	StateFile     string
	EventProvider string
	Scopes        []string
	WebSocket     *bool
	RootCtx       context.Context
	Logger        Logger
	Mode          string
	Interval      time.Duration
}

type Logger interface {
	Printf(format string, args ...any)
}

type Syncer struct {
	client               RemoteClient
	workspace            string
	remoteRoot           string
	localRoot            string
	localDir             string
	stateFile            string
	publicStatePath      string
	conflictsDir         string
	resolvedConflictsDir string
	deadLetterDir        string
	eventProvider        string
	scopes               []string
	logger               Logger
	denialLogPath        string // path to .relay/permissions-denied.log
	state                mountState
	loaded               bool
	bootstrapped         bool
	websocket            bool
	rootCtx              context.Context
	wsConn               *websocket.Conn
	wsCancel             context.CancelFunc
	bulkFlushThreshold   int
	mode                 string
	interval             time.Duration
	mu                   sync.Mutex
}

type mountState struct {
	Files                     map[string]trackedFile `json:"files"`
	EventsCursor              string                 `json:"eventsCursor,omitempty"`
	LastReconcileAt           string                 `json:"lastReconcileAt,omitempty"`
	LastSuccessfulReconcileAt string                 `json:"lastSuccessfulReconcileAt,omitempty"`
	LastEventAt               string                 `json:"lastEventAt,omitempty"`
	LastError                 *statusError           `json:"lastError,omitempty"`
}

type trackedFile struct {
	Revision    string `json:"revision"`
	ContentType string `json:"contentType"`
	Encoding    string `json:"encoding,omitempty"`
	Hash        string `json:"hash"`
	Dirty       bool   `json:"dirty,omitempty"`
	// Denied — the server denied reading this path. The local copy (if any)
	// has been removed and future syncs ignore it.
	Denied bool `json:"denied,omitempty"`
	// WriteDenied — the server denied creating this path from local. Unlike
	// Denied, the local copy is preserved. Future sync cycles skip the push
	// attempt as long as the file's hash still matches DeniedHash; if the
	// user modifies the file we retry the write on the next cycle.
	WriteDenied bool   `json:"writeDenied,omitempty"`
	DeniedHash  string `json:"deniedHash,omitempty"`
	ReadOnly    bool   `json:"readonly,omitempty"`
}

type localSnapshot struct {
	RawContent  []byte
	WireContent string
	ContentType string
	Encoding    string
	Hash        string
}

type pendingBulkWrite struct {
	remotePath string
	localPath  string
	snapshot   localSnapshot
	tracked    trackedFile
	exists     bool
}

type websocketEvent struct {
	Type      string `json:"type"`
	Path      string `json:"path,omitempty"`
	Revision  string `json:"revision,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

type statusError struct {
	Kind       string `json:"kind"`
	StatusCode int    `json:"statusCode,omitempty"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message"`
	At         string `json:"at"`
}

type publicState struct {
	WorkspaceID               string                     `json:"workspaceId"`
	RemoteRoot                string                     `json:"remoteRoot"`
	LocalRoot                 string                     `json:"localRoot"`
	Mode                      string                     `json:"mode"`
	IntervalMs                int64                      `json:"intervalMs"`
	LastReconcileAt           string                     `json:"lastReconcileAt,omitempty"`
	LastSuccessfulReconcileAt string                     `json:"lastSuccessfulReconcileAt,omitempty"`
	LastEventAt               string                     `json:"lastEventAt,omitempty"`
	StaleAfter                string                     `json:"staleAfter,omitempty"`
	Status                    string                     `json:"status"`
	States                    publicStateFlags           `json:"states"`
	PendingWriteback          int                        `json:"pendingWriteback"`
	PendingConflicts          int                        `json:"pendingConflicts"`
	DeniedPaths               int                        `json:"deniedPaths"`
	LastError                 *statusError               `json:"lastError,omitempty"`
	Files                     map[string]publicFileState `json:"files,omitempty"`
}

type publicStateFlags struct {
	Stale               bool `json:"stale"`
	Offline             bool `json:"offline"`
	HasConflicts        bool `json:"hasConflicts"`
	HasPendingWriteback bool `json:"hasPendingWriteback"`
}

type publicFileState struct {
	Revision    string `json:"revision,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Encoding    string `json:"encoding,omitempty"`
	Dirty       bool   `json:"dirty,omitempty"`
	Denied      bool   `json:"denied,omitempty"`
	WriteDenied bool   `json:"writeDenied,omitempty"`
	ReadOnly    bool   `json:"readonly,omitempty"`
	Status      string `json:"status"`
}

func NewSyncer(client RemoteClient, opts SyncerOptions) (*Syncer, error) {
	if client == nil {
		return nil, fmt.Errorf("client is required")
	}
	workspace := strings.TrimSpace(opts.WorkspaceID)
	if workspace == "" {
		return nil, fmt.Errorf("workspace id is required")
	}
	localRootRaw := strings.TrimSpace(opts.LocalRoot)
	if localRootRaw == "" {
		return nil, fmt.Errorf("local root is required")
	}
	localRoot := filepath.Clean(localRootRaw)
	remoteRoot := normalizeRemotePath(opts.RemoteRoot)
	if remoteRoot == "" {
		remoteRoot = "/"
	}
	eventProvider := strings.TrimSpace(opts.EventProvider)
	if eventProvider == "" {
		eventProvider = inferProviderFromRoot(remoteRoot)
	}
	stateFile := strings.TrimSpace(opts.StateFile)
	if stateFile == "" {
		stateFile = filepath.Join(localRoot, ".relayfile-mount-state.json")
	}
	publicStatePath := filepath.Join(localRoot, ".relay", "state.json")
	conflictsDir := filepath.Join(localRoot, ".relay", "conflicts")
	resolvedConflictsDir := filepath.Join(conflictsDir, "resolved")
	deadLetterDir := filepath.Join(localRoot, ".relay", "dead-letter")
	scopes := normalizeScopes(opts.Scopes)
	if len(scopes) == 0 {
		if httpClient, ok := client.(*HTTPClient); ok {
			scopes = parseScopesFromJWT(httpClient.token)
		}
	}
	if err := os.MkdirAll(localRoot, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(localRoot, ".relay"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(conflictsDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(resolvedConflictsDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(deadLetterDir, 0o755); err != nil {
		return nil, err
	}
	websocketEnabled := true
	if opts.WebSocket != nil {
		websocketEnabled = *opts.WebSocket
	}
	rootCtx := opts.RootCtx
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	return &Syncer{
		client:               client,
		workspace:            workspace,
		remoteRoot:           remoteRoot,
		localRoot:            localRoot,
		localDir:             localRoot,
		stateFile:            stateFile,
		publicStatePath:      publicStatePath,
		conflictsDir:         conflictsDir,
		resolvedConflictsDir: resolvedConflictsDir,
		deadLetterDir:        deadLetterDir,
		eventProvider:        eventProvider,
		scopes:               scopes,
		websocket:            websocketEnabled,
		rootCtx:              rootCtx,
		logger:               opts.Logger,
		denialLogPath:        filepath.Join(localRoot, ".relay", "permissions-denied.log"),
		bulkFlushThreshold:   defaultBulkFlushThreshold,
		mode:                 strings.TrimSpace(opts.Mode),
		interval:             opts.Interval,
		state: mountState{
			Files: map[string]trackedFile{},
		},
	}, nil
}

func parseScopesFromJWT(token string) []string {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return nil
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims struct {
		Scopes []string `json:"scopes"`
	}
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil
	}
	return claims.Scopes
}

func (s *Syncer) SyncOnce(ctx context.Context) error {
	return s.sync(ctx, false)
}

func (s *Syncer) Reconcile(ctx context.Context) error {
	return s.sync(ctx, true)
}

func (s *Syncer) HandleLocalChange(ctx context.Context, relativePath string, op fsnotify.Op) error {
	relativePath = filepath.ToSlash(strings.TrimSpace(filepath.Clean(relativePath)))
	if relativePath == "" || relativePath == "." {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadState(); err != nil {
		return err
	}

	remotePath := normalizeRemotePath(filepath.Join(s.remoteRoot, filepath.FromSlash(relativePath)))
	if remotePath == "/" || !isUnderRemoteRoot(s.remoteRoot, remotePath) {
		return nil
	}
	localPath := filepath.Join(s.localDir, filepath.FromSlash(relativePath))

	if info, statErr := os.Stat(localPath); statErr == nil && info.IsDir() {
		return nil
	}

	saveWithStatus := func(run func() error) error {
		if err := run(); err != nil {
			s.markSyncError(err)
			_ = s.saveState()
			return err
		}
		s.markSyncSuccess()
		return s.saveState()
	}

	switch {
	case op&(fsnotify.Write|fsnotify.Create) != 0:
		return saveWithStatus(func() error {
			return s.handleLocalWriteOrCreate(ctx, remotePath, localPath)
		})
	case op&(fsnotify.Remove|fsnotify.Rename) != 0:
		tracked, exists := s.state.Files[remotePath]
		if exists && tracked.ReadOnly {
			return saveWithStatus(func() error {
				return s.revertReadonlyFile(ctx, remotePath, localPath, tracked, "")
			})
		}
		return saveWithStatus(func() error {
			return s.pushSingleDelete(ctx, remotePath, localPath)
		})
	case op&fsnotify.Chmod != 0:
		tracked, exists := s.state.Files[remotePath]
		if exists && tracked.ReadOnly {
			if err := s.applyLocalPermissions(localPath, false); err != nil && !errors.Is(err, os.ErrNotExist) {
				s.markSyncError(err)
				_ = s.saveState()
				return err
			}
		}
	}
	return nil
}

func (s *Syncer) handleLocalWriteOrCreate(ctx context.Context, remotePath, localPath string) error {
	snapshotContent, err := os.ReadFile(localPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s.pushSingleDelete(ctx, remotePath, localPath)
		}
		return err
	}
	snapshot := newLocalSnapshot(localPath, snapshotContent)
	tracked, exists := s.state.Files[remotePath]
	if exists && tracked.ReadOnly {
		return s.revertReadonlyFile(ctx, remotePath, localPath, tracked, snapshot.ContentType)
	}
	return s.pushSingleFile(ctx, remotePath, localPath, snapshot, tracked, exists, nil)
}

func (s *Syncer) pushSingleFile(
	ctx context.Context,
	remotePath, localPath string,
	snapshot localSnapshot,
	tracked trackedFile,
	exists bool,
	conflicted map[string]struct{},
) error {
	pendingWrite, err := s.preparePendingBulkWrite(ctx, remotePath, localPath, snapshot, tracked, exists)
	if err != nil || pendingWrite == nil {
		return err
	}
	return s.flushPendingBulkWrites(ctx, []pendingBulkWrite{*pendingWrite}, conflicted)
}

func (s *Syncer) preparePendingBulkWrite(
	ctx context.Context,
	remotePath, localPath string,
	snapshot localSnapshot,
	tracked trackedFile,
	exists bool,
) (*pendingBulkWrite, error) {
	canWrite := s.canWritePath(remotePath)
	tracked.ReadOnly = !canWrite
	if !canWrite {
		// If content was modified (chmod bypass), revert to server version
		if snapshot.Hash != tracked.Hash && tracked.Hash != "" {
			return nil, s.revertReadonlyFile(ctx, remotePath, localPath, tracked, snapshot.ContentType)
		}
		if err := s.applyLocalPermissions(localPath, false); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		tracked.Dirty = false
		s.state.Files[remotePath] = tracked
		return nil, nil
	}

	if exists && !tracked.Dirty && tracked.Hash == snapshot.Hash {
		if tracked.ContentType == "" {
			tracked.ContentType = snapshot.ContentType
		}
		tracked.ReadOnly = false
		s.state.Files[remotePath] = tracked
		return nil, nil
	}

	if exists && tracked.Dirty {
		remoteFile, readErr := s.client.ReadFile(ctx, s.workspace, remotePath)
		if readErr == nil {
			remoteBytes, decodeErr := decodeRemoteFileContent(remoteFile)
			if decodeErr == nil && hashBytes(remoteBytes) == snapshot.Hash {
				contentType := strings.TrimSpace(remoteFile.ContentType)
				if contentType == "" {
					contentType = snapshot.ContentType
				}
				tracked.Revision = remoteFile.Revision
				tracked.ContentType = contentType
				tracked.Encoding = normalizeEncoding(remoteFile.Encoding)
				tracked.Hash = snapshot.Hash
				tracked.Dirty = false
				s.state.Files[remotePath] = tracked
				return nil, nil
			}
		}
	}

	return &pendingBulkWrite{
		remotePath: remotePath,
		localPath:  localPath,
		snapshot:   snapshot,
		tracked:    tracked,
		exists:     exists,
	}, nil
}

func (s *Syncer) flushPendingBulkWrites(ctx context.Context, pending []pendingBulkWrite, conflicted map[string]struct{}) error {
	if len(pending) == 0 {
		return nil
	}
	files := make([]BulkWriteFile, 0, len(pending))
	for _, pendingWrite := range pending {
		files = append(files, BulkWriteFile{
			Path:        pendingWrite.remotePath,
			ContentType: pendingWrite.snapshot.ContentType,
			Content:     pendingWrite.snapshot.WireContent,
			Encoding:    pendingWrite.snapshot.Encoding,
		})
	}

	response, err := s.client.WriteFilesBulk(ctx, s.workspace, files)
	if err != nil {
		return err
	}

	errorsByPath := make(map[string]BulkWriteError, len(response.Errors))
	for _, writeErr := range response.Errors {
		errorsByPath[normalizeRemotePath(writeErr.Path)] = writeErr
	}
	resultsByPath := response.resultsByPath()

	var firstErr error
	for _, pendingWrite := range pending {
		if writeErr, ok := errorsByPath[pendingWrite.remotePath]; ok {
			err := s.handleWriteError(
				ctx,
				pendingWrite.remotePath,
				pendingWrite.localPath,
				pendingWrite.snapshot,
				pendingWrite.tracked,
				pendingWrite.exists,
				conflicted,
				bulkWriteErrorAsError(writeErr),
			)
			if err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		if result, ok := resultsByPath[pendingWrite.remotePath]; ok && strings.TrimSpace(result.ContentType) != "" {
			pendingWrite.snapshot.ContentType = result.ContentType
		}
		if err := s.reconcileBulkWrite(ctx, pendingWrite, resultsByPath[pendingWrite.remotePath].Revision); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Syncer) reconcileBulkWrite(ctx context.Context, pendingWrite pendingBulkWrite, revision string) error {
	tracked := pendingWrite.tracked
	contentType := strings.TrimSpace(pendingWrite.snapshot.ContentType)
	if contentType == "" {
		contentType = pendingWrite.snapshot.ContentType
	}
	revision = strings.TrimSpace(revision)
	if revision == "" {
		remoteFile, err := s.client.ReadFile(ctx, s.workspace, pendingWrite.remotePath)
		if err != nil {
			return err
		}
		revision = remoteFile.Revision
		if contentType == "" {
			contentType = strings.TrimSpace(remoteFile.ContentType)
		}
	}
	tracked.ContentType = contentType
	s.state.Files[pendingWrite.remotePath] = trackedFile{
		Revision:    revision,
		ContentType: tracked.ContentType,
		Encoding:    normalizeEncoding(pendingWrite.snapshot.Encoding),
		Hash:        pendingWrite.snapshot.Hash,
		Dirty:       false,
		ReadOnly:    false,
	}
	s.resolveConflictArtifacts(pendingWrite.remotePath)
	return nil
}

func (s *Syncer) handleWriteError(
	ctx context.Context,
	remotePath, localPath string,
	snapshot localSnapshot,
	tracked trackedFile,
	exists bool,
	conflicted map[string]struct{},
	err error,
) error {
	if errors.Is(err, ErrConflict) {
		if conflicted != nil {
			conflicted[remotePath] = struct{}{}
		}
		return s.materializeConflict(ctx, remotePath, localPath, snapshot, tracked)
	}

	var schemaErr *SchemaValidationError
	if errors.As(err, &schemaErr) {
		// Per contract §8.2, a schema-validation failure is a graceful
		// per-file degradation: quarantine the local body, restore the
		// last known remote, and let the user fix the offending file
		// without aborting the cycle.
		return s.materializeSchemaInvalid(ctx, remotePath, localPath, snapshot, tracked, schemaErr.Message)
	}

	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusForbidden {
		if !exists {
			// The server rejected the create. Do NOT delete the local file —
			// destroying user data on permission failure is worse than the
			// workspace drifting out of sync. Record the denial + hash so the
			// next cycle skips re-pushing (no log spam) unless the file
			// changes, in which case we retry.
			s.logDenial(
				"WRITE_DENIED",
				remotePath,
				"agent does not have write permission; local copy preserved",
			)
			s.state.Files[remotePath] = trackedFile{
				ContentType: snapshot.ContentType,
				Hash:        snapshot.Hash,
				WriteDenied: true,
				DeniedHash:  snapshot.Hash,
			}
			return nil
		}
		return s.revertReadonlyFile(ctx, remotePath, localPath, tracked, snapshot.ContentType)
	}

	return err
}

func (s *Syncer) materializeConflict(ctx context.Context, remotePath, localPath string, snapshot localSnapshot, tracked trackedFile) error {
	artifactPath, artifactErr := s.writeConflictArtifact(remotePath, tracked.Revision, snapshot.RawContent)
	if artifactErr != nil {
		return artifactErr
	}

	remoteFile, readErr := s.client.ReadFile(ctx, s.workspace, remotePath)
	if readErr != nil {
		tracked.ContentType = snapshot.ContentType
		tracked.Encoding = normalizeEncoding(snapshot.Encoding)
		tracked.Hash = snapshot.Hash
		tracked.Dirty = true
		s.state.Files[remotePath] = tracked
		return readErr
	}

	remoteBytes, decodeErr := decodeRemoteFileContent(remoteFile)
	if decodeErr != nil {
		return decodeErr
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(localPath, remoteBytes, 0o644); err != nil {
		return err
	}
	if err := s.applyLocalPermissions(localPath, s.canWritePath(remotePath)); err != nil {
		return err
	}

	contentType := strings.TrimSpace(remoteFile.ContentType)
	if contentType == "" {
		contentType = snapshot.ContentType
	}
	s.state.Files[remotePath] = trackedFile{
		Revision:    remoteFile.Revision,
		ContentType: contentType,
		Encoding:    normalizeEncoding(remoteFile.Encoding),
		Hash:        hashBytes(remoteBytes),
		ReadOnly:    !s.canWritePath(remotePath),
	}
	s.logf("conflict at %s; local saved at %s", remotePath, artifactPath)
	return nil
}

// materializeSchemaInvalid implements contract §8.2: park the local body in
// .relay/conflicts/<path>.invalid.<ts>, restore the prior remote into the
// original path, and clear the dirty/pending flags so reconcile does not
// keep retrying the same invalid body. If the remote does not yet exist
// (the offending write was a CREATE), the local file is removed so the
// invalid body does not stay in the mirror.
func (s *Syncer) materializeSchemaInvalid(
	ctx context.Context,
	remotePath, localPath string,
	snapshot localSnapshot,
	tracked trackedFile,
	violation string,
) error {
	artifactPath, artifactErr := s.writeSchemaInvalidArtifact(remotePath, time.Now().UTC(), snapshot.RawContent)
	if artifactErr != nil {
		return artifactErr
	}

	violationDescription := strings.TrimSpace(violation)
	if violationDescription == "" {
		violationDescription = "body did not match the adapter schema"
	}

	remoteFile, readErr := s.client.ReadFile(ctx, s.workspace, remotePath)
	if readErr != nil {
		// No prior remote version to restore — typical for a CREATE that
		// violated the schema. Remove the local file and stop tracking
		// it so the next reconcile does not re-push.
		_ = os.Remove(localPath)
		delete(s.state.Files, remotePath)
		s.logf("schema validation failed at %s (%s); local saved at %s; no prior remote version to restore",
			remotePath, violationDescription, artifactPath)
		return nil
	}

	remoteBytes, decodeErr := decodeRemoteFileContent(remoteFile)
	if decodeErr != nil {
		return decodeErr
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(localPath, remoteBytes, 0o644); err != nil {
		return err
	}
	if err := s.applyLocalPermissions(localPath, s.canWritePath(remotePath)); err != nil {
		return err
	}

	contentType := strings.TrimSpace(remoteFile.ContentType)
	if contentType == "" {
		contentType = snapshot.ContentType
	}
	s.state.Files[remotePath] = trackedFile{
		Revision:    remoteFile.Revision,
		ContentType: contentType,
		Encoding:    normalizeEncoding(remoteFile.Encoding),
		Hash:        hashBytes(remoteBytes),
		ReadOnly:    !s.canWritePath(remotePath),
	}
	_ = tracked
	s.logf("schema validation failed at %s (%s); local saved at %s, remote restored",
		remotePath, violationDescription, artifactPath)
	return nil
}

func (s *Syncer) writeSchemaInvalidArtifact(remotePath string, ts time.Time, content []byte) (string, error) {
	if err := os.MkdirAll(s.conflictsDir, 0o755); err != nil {
		return "", err
	}
	artifactPath := schemaInvalidArtifactPath(s.conflictsDir, remotePath, ts)
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		return "", err
	}
	return artifactPath, writeFileAtomic(artifactPath, content, 0o644)
}

func schemaInvalidArtifactPath(baseDir, remotePath string, ts time.Time) string {
	rel := strings.TrimPrefix(normalizeRemotePath(remotePath), "/")
	stamp := ts.UTC().Format("20060102T150405Z")
	return filepath.Join(baseDir, filepath.FromSlash(rel)+".invalid."+stamp)
}

func bulkWriteErrorAsError(writeErr BulkWriteError) error {
	statusCode := 0
	switch strings.TrimSpace(writeErr.Code) {
	case "forbidden":
		statusCode = http.StatusForbidden
	case "not_found":
		statusCode = http.StatusNotFound
	case "precondition_failed":
		statusCode = http.StatusPreconditionFailed
	case "conflict":
		return &ConflictError{Path: normalizeRemotePath(writeErr.Path)}
	case "schema_validation_failed", "validation_error":
		return &SchemaValidationError{
			Path:    normalizeRemotePath(writeErr.Path),
			Message: writeErr.Message,
		}
	}
	if statusCode != 0 || writeErr.Code != "" || writeErr.Message != "" {
		return &HTTPError{
			StatusCode: statusCode,
			Code:       writeErr.Code,
			Message:    writeErr.Message,
		}
	}
	return fmt.Errorf("bulk write failed for %s", normalizeRemotePath(writeErr.Path))
}

func (s *Syncer) bulkFlushThresholdValue() int {
	if s.bulkFlushThreshold > 0 {
		return s.bulkFlushThreshold
	}
	return defaultBulkFlushThreshold
}

func (s *Syncer) revertReadonlyFile(ctx context.Context, remotePath, localPath string, tracked trackedFile, fallbackContentType string) error {
	s.logDenial("WRITE_DENIED", remotePath, "agent does not have write permission")
	remoteFile, readErr := s.client.ReadFile(ctx, s.workspace, remotePath)
	if readErr == nil {
		remoteBytes, decodeErr := decodeRemoteFileContent(remoteFile)
		if decodeErr != nil {
			return decodeErr
		}
		contentType := strings.TrimSpace(remoteFile.ContentType)
		if contentType == "" {
			contentType = strings.TrimSpace(fallbackContentType)
			if contentType == "" {
				contentType = detectContentType(localPath)
			}
		}
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			return err
		}
		if err := writeFileAtomic(localPath, remoteBytes, 0o444); err != nil {
			return err
		}
		if err := os.Chmod(localPath, 0o444); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		tracked.Revision = remoteFile.Revision
		tracked.ContentType = contentType
		tracked.Encoding = normalizeEncoding(remoteFile.Encoding)
		tracked.Hash = hashBytes(remoteBytes)
		s.logDenial("WRITE_REVERTED", remotePath, "file is read-only; content reverted to server version")
	} else {
		if err := s.applyLocalPermissions(localPath, false); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if tracked.ContentType == "" {
			tracked.ContentType = strings.TrimSpace(fallbackContentType)
			if tracked.ContentType == "" {
				tracked.ContentType = detectContentType(localPath)
			}
		}
	}
	tracked.Dirty = false
	tracked.Denied = false
	tracked.ReadOnly = true
	s.state.Files[remotePath] = tracked
	s.logf("write denied, reverted: %s", remotePath)
	return nil
}

func (s *Syncer) pushSingleDelete(ctx context.Context, remotePath, localPath string) error {
	tracked, exists := s.state.Files[remotePath]
	if !exists {
		delete(s.state.Files, remotePath)
		return nil
	}

	if !s.canWritePath(remotePath) || tracked.ReadOnly {
		return s.revertReadonlyFile(ctx, remotePath, localPath, tracked, "")
	}

	err := s.client.DeleteFile(ctx, s.workspace, remotePath, tracked.Revision)
	if err != nil {
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			delete(s.state.Files, remotePath)
			return nil
		}
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusForbidden {
			return s.revertReadonlyFile(ctx, remotePath, localPath, tracked, "")
		}
		if errors.Is(err, ErrConflict) {
			s.logf("conflict deleting %s; remote changed", remotePath)
			remoteFile, readErr := s.client.ReadFile(ctx, s.workspace, remotePath)
			if readErr == nil {
				return s.applyRemoteFile(remotePath, remoteFile, nil)
			}
			return nil
		}
		return err
	}
	delete(s.state.Files, remotePath)
	return nil
}

func (s *Syncer) sync(ctx context.Context, forcePoll bool) error {
	s.mu.Lock()
	if err := s.loadState(); err != nil {
		s.mu.Unlock()
		return err
	}

	// Check if websocket connection is needed while holding the lock,
	// then release before connecting to avoid deadlock with readWebSocketLoop.
	needsWS := s.websocket && s.wsConn == nil
	s.mu.Unlock()

	if needsWS {
		if err := s.connectWebSocket(ctx); err != nil {
			s.logf("websocket unavailable; using polling sync: %v", err)
		}
	}

	// Re-acquire lock for the remainder of the sync operation.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markReconcileStarted()

	conflicted, err := s.pushLocal(ctx)
	if err != nil {
		s.markSyncError(err)
		_ = s.saveState()
		return err
	}

	shouldPoll := forcePoll || !s.bootstrapped || s.wsConn == nil
	if shouldPoll {
		if err := s.pullRemote(ctx, conflicted); err != nil {
			s.markSyncError(err)
			_ = s.saveState()
			return err
		}
		s.bootstrapped = true
	}
	s.markSyncSuccess()
	return s.saveState()
}

func (s *Syncer) connectWebSocket(ctx context.Context) error {
	s.mu.Lock()
	if !s.websocket || s.wsConn != nil {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	httpClient, ok := s.client.(*HTTPClient)
	if !ok {
		return nil
	}

	wsURL, err := httpClient.websocketURL(s.workspace)
	if err != nil {
		return err
	}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + httpClient.Token()},
		},
	})
	if err != nil {
		return err
	}

	readCtx, cancel := context.WithCancel(s.rootCtx)

	s.mu.Lock()
	s.wsConn = conn
	s.wsCancel = cancel
	s.mu.Unlock()

	go s.readWebSocketLoop(readCtx, conn)
	return nil
}

func (s *Syncer) readWebSocketLoop(ctx context.Context, conn *websocket.Conn) {
	defer s.handleWebSocketDisconnect(conn)

	for {
		var event websocketEvent
		if err := wsjson.Read(ctx, conn, &event); err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
				websocket.CloseStatus(err) == websocket.StatusGoingAway ||
				errors.Is(err, context.Canceled) {
				return
			}
			s.logf("websocket read failed; falling back to polling: %v", err)
			return
		}
		if err := s.applyWebSocketEvent(ctx, event); err != nil {
			s.logf("websocket event apply failed for %s: %v", strings.TrimSpace(event.Path), err)
		}
	}
}

func (s *Syncer) applyWebSocketEvent(ctx context.Context, event websocketEvent) error {
	eventAt := strings.TrimSpace(event.Timestamp)
	if eventAt == "" {
		eventAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	switch eventType := strings.TrimSpace(event.Type); eventType {
	case "", "pong":
		return nil
	case "file.created", "file.updated":
		remotePath := normalizeRemotePath(event.Path)
		if remotePath == "/" || !isUnderRemoteRoot(s.remoteRoot, remotePath) {
			return nil
		}
		file, err := s.client.ReadFile(ctx, s.workspace, remotePath)
		if err != nil {
			var httpErr *HTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
				return nil
			}
			if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusForbidden {
				s.logf("skipping denied file: %s", remotePath)
				if markErr := s.markReadDenied(remotePath); markErr != nil {
					return markErr
				}
				return nil
			}
			return err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.state.LastEventAt = eventAt
		if err := s.applyRemoteFile(remotePath, file, nil); err != nil {
			return err
		}
		return s.saveState()
	case "file.deleted":
		remotePath := normalizeRemotePath(event.Path)
		if remotePath == "/" || !isUnderRemoteRoot(s.remoteRoot, remotePath) {
			return nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.state.LastEventAt = eventAt
		if err := s.applyRemoteDelete(remotePath, nil); err != nil {
			return err
		}
		return s.saveState()
	default:
		return nil
	}
}

func (s *Syncer) handleWebSocketDisconnect(conn *websocket.Conn) {
	_ = conn.Close(websocket.StatusNormalClosure, "")

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.wsConn != conn {
		return
	}
	if s.wsCancel != nil {
		s.wsCancel()
		s.wsCancel = nil
	}
	s.wsConn = nil
}

func (s *Syncer) ResetWebSocket() {
	s.mu.Lock()
	conn := s.wsConn
	cancel := s.wsCancel
	s.wsConn = nil
	s.wsCancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
}

func (s *Syncer) HTTPClient() (*HTTPClient, bool) {
	client, ok := s.client.(*HTTPClient)
	return client, ok
}

func (s *Syncer) pullRemote(ctx context.Context, conflicted map[string]struct{}) error {
	if s.state.EventsCursor != "" {
		nextCursor, err := s.pullRemoteIncremental(ctx, conflicted, s.state.EventsCursor)
		if err == nil {
			s.state.EventsCursor = nextCursor
			return nil
		}
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound {
			return err
		}
		s.logf("events feed unavailable; falling back to full pull")
		s.state.EventsCursor = ""
	}

	if err := s.pullRemoteFull(ctx, conflicted); err != nil {
		return err
	}
	if s.wsConn != nil {
		return nil
	}
	cursor, err := s.resolveLatestEventCursor(ctx)
	if err != nil {
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			return nil
		}
		return err
	}
	s.state.EventsCursor = cursor
	return nil
}

func (s *Syncer) pullRemoteFull(ctx context.Context, conflicted map[string]struct{}) error {
	if client, ok := s.client.(exportSnapshotClient); ok {
		used, err := s.pullRemoteFullExport(ctx, client, conflicted)
		if used {
			return err
		}
	}
	return s.pullRemoteFullTree(ctx, conflicted)
}

func (s *Syncer) pullRemoteFullExport(ctx context.Context, client exportSnapshotClient, conflicted map[string]struct{}) (bool, error) {
	files, err := client.ExportFiles(ctx, s.workspace, s.remoteRoot)
	if err != nil {
		if exportSnapshotUnsupported(err) {
			return false, nil
		}
		return true, err
	}
	remoteFiles := map[string]RemoteFile{}
	for _, file := range files {
		remotePath := normalizeRemotePath(file.Path)
		if remotePath == "/" || !isUnderRemoteRoot(s.remoteRoot, remotePath) {
			continue
		}
		if tracked, ok := s.state.Files[remotePath]; ok && tracked.Denied {
			continue
		}
		file.Path = remotePath
		remoteFiles[remotePath] = file
	}
	return true, s.applyRemoteSnapshot(remoteFiles, conflicted)
}

func exportSnapshotUnsupported(err error) bool {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	if httpErr.StatusCode == http.StatusNotFound {
		return true
	}
	return httpErr.StatusCode == http.StatusBadRequest && strings.EqualFold(httpErr.Code, "bad_request")
}

func (s *Syncer) pullRemoteFullTree(ctx context.Context, conflicted map[string]struct{}) error {
	remoteFiles := map[string]RemoteFile{}
	cursor := ""
	for {
		page, err := s.client.ListTree(ctx, s.workspace, s.remoteRoot, 10, cursor)
		if err != nil {
			return err
		}
		for _, entry := range page.Entries {
			if entry.Type != "file" {
				continue
			}
			remotePath := normalizeRemotePath(entry.Path)
			if !isUnderRemoteRoot(s.remoteRoot, remotePath) {
				continue
			}
			if tracked, ok := s.state.Files[remotePath]; ok && tracked.Denied {
				continue
			}
			file, err := s.client.ReadFile(ctx, s.workspace, remotePath)
			if err != nil {
				var httpErr *HTTPError
				if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusForbidden {
					s.logf("skipping denied file: %s", remotePath)
					if markErr := s.markReadDenied(remotePath); markErr != nil {
						return markErr
					}
					continue
				}
				return err
			}
			remoteFiles[remotePath] = file
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = *page.NextCursor
	}

	return s.applyRemoteSnapshot(remoteFiles, conflicted)
}

func (s *Syncer) applyRemoteSnapshot(remoteFiles map[string]RemoteFile, conflicted map[string]struct{}) error {
	remotePaths := make([]string, 0, len(remoteFiles))
	for remotePath := range remoteFiles {
		remotePaths = append(remotePaths, remotePath)
	}
	sort.Strings(remotePaths)

	for _, remotePath := range remotePaths {
		if err := s.applyRemoteFile(remotePath, remoteFiles[remotePath], conflicted); err != nil {
			return err
		}
	}

	statePaths := make([]string, 0, len(s.state.Files))
	for remotePath := range s.state.Files {
		statePaths = append(statePaths, remotePath)
	}
	sort.Strings(statePaths)
	for _, remotePath := range statePaths {
		if _, ok := remoteFiles[remotePath]; ok {
			continue
		}
		if err := s.applyRemoteDelete(remotePath, conflicted); err != nil {
			return err
		}
	}
	return nil
}

func (s *Syncer) pullRemoteIncremental(ctx context.Context, conflicted map[string]struct{}, cursor string) (string, error) {
	changed := map[string]struct{}{}
	deleted := map[string]struct{}{}
	currentCursor := strings.TrimSpace(cursor)

	for {
		feed, err := s.client.ListEvents(ctx, s.workspace, s.eventProvider, currentCursor, 500)
		if err != nil {
			return cursor, err
		}
		for _, event := range feed.Events {
			eventID := strings.TrimSpace(event.EventID)
			if eventID != "" {
				currentCursor = eventID
			}
			if ts := strings.TrimSpace(event.Timestamp); ts != "" {
				s.state.LastEventAt = ts
			}
			remotePath := normalizeRemotePath(event.Path)
			if remotePath == "/" || !isUnderRemoteRoot(s.remoteRoot, remotePath) {
				continue
			}
			if tracked, ok := s.state.Files[remotePath]; ok && tracked.Denied {
				continue
			}
			switch event.Type {
			case "file.created", "file.updated":
				changed[remotePath] = struct{}{}
				delete(deleted, remotePath)
			case "file.deleted":
				deleted[remotePath] = struct{}{}
				delete(changed, remotePath)
			}
		}
		if feed.NextCursor == nil || *feed.NextCursor == "" {
			break
		}
		currentCursor = *feed.NextCursor
	}

	changedPaths := make([]string, 0, len(changed))
	for remotePath := range changed {
		changedPaths = append(changedPaths, remotePath)
	}
	sort.Strings(changedPaths)
	for _, remotePath := range changedPaths {
		file, err := s.client.ReadFile(ctx, s.workspace, remotePath)
		if err != nil {
			var httpErr *HTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
				deleted[remotePath] = struct{}{}
				continue
			}
			if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusForbidden {
				s.logf("skipping denied file: %s", remotePath)
				if markErr := s.markReadDenied(remotePath); markErr != nil {
					return cursor, markErr
				}
				continue
			}
			return cursor, err
		}
		if err := s.applyRemoteFile(remotePath, file, conflicted); err != nil {
			return cursor, err
		}
	}

	deletedPaths := make([]string, 0, len(deleted))
	for remotePath := range deleted {
		deletedPaths = append(deletedPaths, remotePath)
	}
	sort.Strings(deletedPaths)
	for _, remotePath := range deletedPaths {
		if err := s.applyRemoteDelete(remotePath, conflicted); err != nil {
			return cursor, err
		}
	}

	if currentCursor == "" {
		currentCursor = cursor
	}
	return currentCursor, nil
}

func (s *Syncer) resolveLatestEventCursor(ctx context.Context) (string, error) {
	cursor := ""
	latest := ""
	for {
		feed, err := s.client.ListEvents(ctx, s.workspace, s.eventProvider, cursor, 1000)
		if err != nil {
			return "", err
		}
		if len(feed.Events) > 0 {
			eventID := strings.TrimSpace(feed.Events[len(feed.Events)-1].EventID)
			if eventID != "" {
				latest = eventID
			}
		}
		if feed.NextCursor == nil || *feed.NextCursor == "" {
			break
		}
		cursor = *feed.NextCursor
	}
	return latest, nil
}

func (s *Syncer) applyRemoteFile(remotePath string, file RemoteFile, conflicted map[string]struct{}) error {
	if conflicted != nil {
		if _, skip := conflicted[remotePath]; skip {
			return nil
		}
	}
	remoteBytes, err := decodeRemoteFileContent(file)
	if err != nil {
		return err
	}
	tracked := s.state.Files[remotePath]
	canWrite := s.canWritePath(remotePath)
	tracked.ReadOnly = !canWrite
	tracked.Denied = false
	if tracked.Dirty {
		localPath, err := remoteToLocalPath(s.localRoot, s.remoteRoot, remotePath)
		if err != nil {
			return nil
		}
		if err := s.applyLocalPermissions(localPath, canWrite); err != nil {
			return err
		}
		s.state.Files[remotePath] = tracked
		return nil
	}
	localPath, err := remoteToLocalPath(s.localRoot, s.remoteRoot, remotePath)
	if err != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}
	remoteHash := hashBytes(remoteBytes)
	shouldWrite := true
	if current, err := os.ReadFile(localPath); err == nil {
		localHash := hashBytes(current)
		if localHash == remoteHash {
			shouldWrite = false
		} else if tracked, ok := s.state.Files[remotePath]; ok && localHash != tracked.Hash {
			// Local file was modified since last sync — mark dirty and preserve the local edit
			tracked.Revision = file.Revision
			tracked.ContentType = file.ContentType
			tracked.Hash = localHash
			tracked.Dirty = true
			if err := s.applyLocalPermissions(localPath, canWrite); err != nil {
				return err
			}
			s.state.Files[remotePath] = tracked
			return nil
		}
	}
	if shouldWrite {
		if err := writeFileAtomic(localPath, remoteBytes, 0o644); err != nil {
			return err
		}
	}
	if err := s.applyLocalPermissions(localPath, canWrite); err != nil {
		return err
	}
	contentType := strings.TrimSpace(file.ContentType)
	if contentType == "" {
		contentType = detectContentType(localPath)
	}
	tracked.ReadOnly = !canWrite
	s.state.Files[remotePath] = trackedFile{
		Revision:    file.Revision,
		ContentType: contentType,
		Encoding:    normalizeEncoding(file.Encoding),
		Hash:        remoteHash,
		Dirty:       false,
		Denied:      false,
		ReadOnly:    !canWrite,
	}
	return nil
}

func (s *Syncer) applyLocalPermissions(localPath string, canWrite bool) error {
	if canWrite {
		return os.Chmod(localPath, 0o644)
	}
	return os.Chmod(localPath, 0o444)
}

func (s *Syncer) canWritePath(filePath string) bool {
	if len(s.scopes) == 0 {
		return true
	}
	normalizedPath := normalizeRemotePath(filePath)
	for _, scope := range s.scopes {
		if scopeGrantsWrite(scope, normalizedPath) {
			return true
		}
	}
	return false
}

func scopeGrantsWrite(scope, filePath string) bool {
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" {
		return false
	}
	// Short-form scope without plane prefix.
	if scope == "fs:write" || scope == "fs:manage" {
		return true
	}

	segments := strings.SplitN(scope, ":", 4)
	if len(segments) < 3 {
		return false
	}

	plane := segments[0]
	res := segments[1]
	act := segments[2]

	// Plane must be "relayfile" or wildcard.
	if plane != "relayfile" && plane != "*" {
		return false
	}
	// Resource must be "fs" or wildcard.
	if res != "fs" && res != "*" {
		return false
	}
	// Action must grant write: "write", "manage", or wildcard.
	if act != "write" && act != "manage" && act != "*" {
		return false
	}

	// If there is a path restriction (4th segment), check it.
	if len(segments) == 4 {
		allowedPrefix := strings.TrimSpace(segments[3])
		if allowedPrefix == "" {
			return false
		}
		if allowedPrefix == "*" {
			return true
		}
		allowedPrefix = normalizeRemotePath(allowedPrefix)
		if strings.HasSuffix(allowedPrefix, "/*") {
			allowedPrefix = strings.TrimSuffix(allowedPrefix, "/*")
			allowedPrefix = normalizeRemotePath(allowedPrefix)
		}
		if allowedPrefix == "/" {
			return true
		}
		if allowedPrefix == "" {
			return false
		}
		return filePath == allowedPrefix || strings.HasPrefix(filePath, allowedPrefix+"/")
	}

	return true
}

func (s *Syncer) applyRemoteDelete(remotePath string, conflicted map[string]struct{}) error {
	if conflicted != nil {
		if _, skip := conflicted[remotePath]; skip {
			return nil
		}
	}
	tracked, ok := s.state.Files[remotePath]
	if !ok || tracked.Dirty {
		return nil
	}
	if tracked.Denied {
		return nil
	}
	// Write-denied files were never on the remote — absence from the remote
	// snapshot is expected and must not trigger a local delete.
	if tracked.WriteDenied {
		return nil
	}
	localPath, err := remoteToLocalPath(s.localRoot, s.remoteRoot, remotePath)
	if err != nil {
		delete(s.state.Files, remotePath)
		return nil
	}
	currentBytes, readErr := os.ReadFile(localPath)
	if readErr == nil && hashBytes(currentBytes) == tracked.Hash {
		_ = os.Remove(localPath)
	}
	delete(s.state.Files, remotePath)
	return nil
}

func (s *Syncer) pushLocal(ctx context.Context) (map[string]struct{}, error) {
	conflicted := map[string]struct{}{}
	localFiles, err := s.scanLocalFiles()
	if err != nil {
		return nil, err
	}

	localRemotePaths := make([]string, 0, len(localFiles))
	for remotePath := range localFiles {
		localRemotePaths = append(localRemotePaths, remotePath)
	}
	sort.Strings(localRemotePaths)

	pendingWrites := make([]pendingBulkWrite, 0, len(localRemotePaths))

	for _, remotePath := range localRemotePaths {
		snapshot := localFiles[remotePath]
		tracked, exists := s.state.Files[remotePath]
		localPath, err := remoteToLocalPath(s.localRoot, s.remoteRoot, remotePath)
		if err != nil {
			return nil, err
		}
		canWrite := s.canWritePath(remotePath)
		tracked.ReadOnly = !canWrite
		if exists && tracked.Denied {
			if err := os.Remove(localPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			tracked.Dirty = false
			s.state.Files[remotePath] = tracked
			continue
		}
		if exists && tracked.ReadOnly {
			// Check if agent modified the readonly file (e.g. via chmod bypass)
			if snapshot.Hash != tracked.Hash {
				// Revert to server content
				remoteFile, readErr := s.client.ReadFile(ctx, s.workspace, remotePath)
				if readErr == nil {
					remoteBytes, decodeErr := decodeRemoteFileContent(remoteFile)
					if decodeErr == nil && os.WriteFile(localPath, remoteBytes, 0o444) == nil {
						tracked.Hash = hashBytes(remoteBytes)
						tracked.Revision = remoteFile.Revision
						tracked.Encoding = normalizeEncoding(remoteFile.Encoding)
						s.logDenial("WRITE_REVERTED", remotePath, "file is read-only; content reverted to server version")
						s.logf("write denied, reverted: %s", remotePath)
					}
				}
			}
			if err := s.applyLocalPermissions(localPath, false); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			s.state.Files[remotePath] = tracked
			continue
		}
		if exists && !canWrite {
			if err := s.applyLocalPermissions(localPath, false); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			tracked.Dirty = false
			s.state.Files[remotePath] = tracked
			continue
		}
		if exists && tracked.Hash == snapshot.Hash && !tracked.Dirty {
			continue
		}
		// Skip re-pushing a previously write-denied file whose content hasn't
		// changed — no point spamming the denial log. If the user edits it,
		// Hash differs from DeniedHash and we fall through to pushSingleFile
		// which will retry (and may succeed or log a fresh denial).
		if exists && tracked.WriteDenied && tracked.DeniedHash == snapshot.Hash {
			continue
		}
		pendingWrite, err := s.preparePendingBulkWrite(ctx, remotePath, localPath, snapshot, tracked, exists)
		if err != nil {
			return nil, err
		}
		if pendingWrite == nil {
			continue
		}
		pendingWrites = append(pendingWrites, *pendingWrite)
		if len(pendingWrites) < s.bulkFlushThresholdValue() {
			continue
		}
		if err := s.flushPendingBulkWrites(ctx, pendingWrites, conflicted); err != nil {
			return nil, err
		}
		pendingWrites = pendingWrites[:0]
	}

	if err := s.flushPendingBulkWrites(ctx, pendingWrites, conflicted); err != nil {
		return nil, err
	}

	statePaths := make([]string, 0, len(s.state.Files))
	for remotePath := range s.state.Files {
		statePaths = append(statePaths, remotePath)
	}
	sort.Strings(statePaths)

	for _, remotePath := range statePaths {
		tracked := s.state.Files[remotePath]
		tracked.ReadOnly = !s.canWritePath(remotePath)
		// Write-denied files never made it to the remote — there's nothing to
		// delete remotely and we must not delete the local copy.
		if tracked.Denied || tracked.ReadOnly || tracked.WriteDenied {
			s.state.Files[remotePath] = tracked
			continue
		}
		if _, ok := localFiles[remotePath]; ok {
			continue
		}
		err := s.client.DeleteFile(ctx, s.workspace, remotePath, tracked.Revision)
		if err != nil {
			var httpErr *HTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
				delete(s.state.Files, remotePath)
				continue
			}
			if errors.Is(err, ErrConflict) {
				s.logf("conflict deleting %s; remote changed", remotePath)
				remoteFile, readErr := s.client.ReadFile(ctx, s.workspace, remotePath)
				if readErr == nil {
					if applyErr := s.applyRemoteFile(remotePath, remoteFile, nil); applyErr != nil {
						return nil, applyErr
					}
				}
				continue
			}
			return nil, err
		}
		delete(s.state.Files, remotePath)
	}
	return conflicted, nil
}

func (s *Syncer) markReadDenied(remotePath string) error {
	tracked := s.state.Files[remotePath]
	tracked.Denied = true
	tracked.Dirty = false
	tracked.ReadOnly = false
	s.state.Files[remotePath] = tracked

	localPath, err := remoteToLocalPath(s.localRoot, s.remoteRoot, remotePath)
	if err != nil {
		return nil
	}
	s.logDenial("READ_DENIED", remotePath, "agent does not have read permission; file removed")
	if err := os.Remove(localPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Syncer) logDenial(action, filePath, reason string) {
	entry := fmt.Sprintf("[%s] %s %s: %s\n",
		time.Now().Format(time.RFC3339), action, filePath, reason)
	f, err := os.OpenFile(s.denialLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(entry)
}

func (s *Syncer) applyWriteDenied(ctx context.Context, remotePath, localPath string, snapshot localSnapshot, tracked trackedFile) error {
	return s.revertReadonlyFile(ctx, remotePath, localPath, tracked, snapshot.ContentType)
}

func (s *Syncer) scanLocalFiles() (map[string]localSnapshot, error) {
	results := map[string]localSnapshot{}
	statePathAbs, err := filepath.Abs(s.stateFile)
	if err != nil {
		return nil, err
	}
	err = filepath.WalkDir(s.localRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == ".relay" {
				return filepath.SkipDir
			}
			return nil
		}
		absPath, err := filepath.Abs(path)
		if err == nil && absPath == statePathAbs {
			return nil
		}
		remotePath, err := localToRemotePath(s.localRoot, s.remoteRoot, path)
		if err != nil {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		results[remotePath] = newLocalSnapshot(path, data)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func normalizeScopes(scopes []string) []string {
	normalized := make([]string, 0, len(scopes))
	seen := map[string]struct{}{}
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		normalized = append(normalized, scope)
	}
	return normalized
}

func (s *Syncer) loadState() error {
	if s.loaded {
		return nil
	}
	s.loaded = true
	data, err := os.ReadFile(s.stateFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.state.Files = map[string]trackedFile{}
			return nil
		}
		return err
	}
	var state mountState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if state.Files == nil {
		state.Files = map[string]trackedFile{}
	}
	s.state = state
	return nil
}

func (s *Syncer) saveState() error {
	data, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.stateFile), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(s.stateFile, data, 0o644); err != nil {
		return err
	}
	return s.savePublicState()
}

func (s *Syncer) savePublicState() error {
	currentFiles, err := s.scanLocalFiles()
	if err != nil {
		return err
	}
	conflictsByPath, pendingConflicts, err := s.listConflictArtifacts()
	if err != nil {
		return err
	}
	deniedPaths := 0
	pendingWriteback := 0
	files := make(map[string]publicFileState, len(s.state.Files))

	for remotePath, tracked := range s.state.Files {
		fileStatus := "ready"
		switch {
		case conflictsByPath[remotePath] > 0:
			fileStatus = "conflict"
		case tracked.WriteDenied:
			fileStatus = "write-denied"
		case tracked.Denied:
			fileStatus = "read-denied"
		case tracked.Dirty:
			fileStatus = "writeback-pending"
		}
		if snapshot, ok := currentFiles[remotePath]; ok {
			if tracked.Hash != snapshot.Hash && fileStatus == "ready" {
				fileStatus = "writeback-pending"
			}
		} else if !tracked.Denied && !tracked.WriteDenied && tracked.Hash != "" && fileStatus == "ready" {
			fileStatus = "writeback-pending"
		}
		if fileStatus == "writeback-pending" {
			pendingWriteback++
		}
		if tracked.Denied || tracked.WriteDenied {
			deniedPaths++
		}
		files[remotePath] = publicFileState{
			Revision:    tracked.Revision,
			ContentType: tracked.ContentType,
			Encoding:    tracked.Encoding,
			Dirty:       tracked.Dirty,
			Denied:      tracked.Denied,
			WriteDenied: tracked.WriteDenied,
			ReadOnly:    tracked.ReadOnly,
			Status:      fileStatus,
		}
	}
	for remotePath, snapshot := range currentFiles {
		if _, ok := files[remotePath]; ok {
			continue
		}
		pendingWriteback++
		files[remotePath] = publicFileState{
			ContentType: snapshot.ContentType,
			Encoding:    snapshot.Encoding,
			Status:      "writeback-pending",
		}
	}

	states := publicStateFlags{
		Offline:             s.state.LastError != nil && s.state.LastError.Kind == "offline",
		HasConflicts:        pendingConflicts > 0,
		HasPendingWriteback: pendingWriteback > 0,
	}
	staleAfter := ""
	if lastOK, err := parseStateTime(s.state.LastSuccessfulReconcileAt); err == nil && !lastOK.IsZero() && s.interval > 0 {
		staleAfter = lastOK.Add(2 * s.interval).UTC().Format(time.RFC3339Nano)
		states.Stale = time.Now().UTC().After(lastOK.Add(2 * s.interval))
	}
	status := "ready"
	switch {
	case states.Offline:
		status = "offline"
	case states.HasConflicts:
		status = "conflict"
	case states.HasPendingWriteback:
		status = "writeback-pending"
	case states.Stale:
		status = "stale"
	}

	mode := s.mode
	if mode == "" {
		mode = "poll"
	}
	public := publicState{
		WorkspaceID:               s.workspace,
		RemoteRoot:                s.remoteRoot,
		LocalRoot:                 s.localRoot,
		Mode:                      mode,
		IntervalMs:                s.interval.Milliseconds(),
		LastReconcileAt:           s.state.LastReconcileAt,
		LastSuccessfulReconcileAt: s.state.LastSuccessfulReconcileAt,
		LastEventAt:               s.state.LastEventAt,
		StaleAfter:                staleAfter,
		Status:                    status,
		States:                    states,
		PendingWriteback:          pendingWriteback,
		PendingConflicts:          pendingConflicts,
		DeniedPaths:               deniedPaths,
		LastError:                 s.state.LastError,
		Files:                     files,
	}
	if err := os.MkdirAll(filepath.Dir(s.publicStatePath), 0o755); err != nil {
		return err
	}
	publicBytes, err := json.Marshal(public)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.publicStatePath, publicBytes, 0o644)
}

func (s *Syncer) listConflictArtifacts() (map[string]int, int, error) {
	counts := map[string]int{}
	total := 0
	err := filepath.WalkDir(s.conflictsDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path == s.resolvedConflictsDir {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(s.conflictsDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "" {
			return nil
		}
		remotePath := conflictArtifactToRemotePath(rel)
		if remotePath == "" {
			return nil
		}
		counts[remotePath]++
		total++
		return nil
	})
	return counts, total, err
}

func (s *Syncer) writeConflictArtifact(remotePath, baseRevision string, content []byte) (string, error) {
	if err := os.MkdirAll(s.conflictsDir, 0o755); err != nil {
		return "", err
	}
	artifactPath := conflictArtifactPath(s.conflictsDir, remotePath, baseRevision)
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		return "", err
	}
	return artifactPath, writeFileAtomic(artifactPath, content, 0o644)
}

func (s *Syncer) resolveConflictArtifacts(remotePath string) {
	pattern := conflictArtifactGlob(s.conflictsDir, remotePath)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return
	}
	for _, match := range matches {
		rel, err := filepath.Rel(s.conflictsDir, match)
		if err != nil {
			continue
		}
		target := filepath.Join(s.resolvedConflictsDir, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			continue
		}
		if err := os.Rename(match, target); err != nil {
			continue
		}
	}
}

func (s *Syncer) markReconcileStarted() {
	s.state.LastReconcileAt = time.Now().UTC().Format(time.RFC3339Nano)
}

func (s *Syncer) markSyncSuccess() {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.state.LastReconcileAt = now
	s.state.LastSuccessfulReconcileAt = now
	s.state.LastError = nil
}

func (s *Syncer) markSyncError(err error) {
	s.state.LastReconcileAt = time.Now().UTC().Format(time.RFC3339Nano)
	s.state.LastError = classifyStatusError(err)
}

func parseStateTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, value)
}

func conflictArtifactPath(baseDir, remotePath, baseRevision string) string {
	rel := strings.TrimPrefix(normalizeRemotePath(remotePath), "/")
	revision := sanitizeRevision(baseRevision)
	return filepath.Join(baseDir, filepath.FromSlash(rel)+"."+revision+".local")
}

func conflictArtifactGlob(baseDir, remotePath string) string {
	rel := strings.TrimPrefix(normalizeRemotePath(remotePath), "/")
	return filepath.Join(baseDir, filepath.FromSlash(rel)+".*.local")
}

func conflictArtifactToRemotePath(rel string) string {
	trimmed := strings.TrimSuffix(rel, ".local")
	if trimmed == rel {
		return ""
	}
	lastDot := strings.LastIndex(trimmed, ".")
	if lastDot <= 0 {
		return ""
	}
	return normalizeRemotePath(trimmed[:lastDot])
}

func sanitizeRevision(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	value = strings.ReplaceAll(value, "/", "_")
	value = strings.ReplaceAll(value, string(filepath.Separator), "_")
	return value
}

func newLocalSnapshot(path string, data []byte) localSnapshot {
	contentType := detectContentType(path)
	encoding := ""
	wireContent := string(data)
	if shouldEncodeLocalContentAsBase64(data, contentType) {
		encoding = "base64"
		wireContent = base64.StdEncoding.EncodeToString(data)
	}
	return localSnapshot{
		RawContent:  append([]byte(nil), data...),
		WireContent: wireContent,
		ContentType: contentType,
		Encoding:    encoding,
		Hash:        hashBytes(data),
	}
}

func shouldEncodeLocalContentAsBase64(data []byte, contentType string) bool {
	if !utf8.Valid(data) || !isTextLikeContentType(contentType) {
		return true
	}
	return containsNonTextControlBytes(data)
}

func containsNonTextControlBytes(data []byte) bool {
	for _, b := range data {
		switch b {
		case '\t', '\n', '\r':
			continue
		}
		if b < 0x20 || b == 0x7f {
			return true
		}
	}
	return false
}

func isTextLikeContentType(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.HasPrefix(contentType, "text/"):
		return true
	case contentType == "application/json",
		contentType == "application/xml",
		contentType == "application/javascript",
		contentType == "application/x-javascript",
		contentType == "image/svg+xml":
		return true
	default:
		return false
	}
}

func normalizeEncoding(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "utf-8", "utf8":
		return ""
	case "base64":
		return value
	default:
		return value
	}
}

func decodeRemoteFileContent(file RemoteFile) ([]byte, error) {
	if normalizeEncoding(file.Encoding) != "base64" {
		return []byte(file.Content), nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(file.Content); err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(file.Content)
}

func classifyStatusError(err error) *statusError {
	if err == nil {
		return nil
	}
	status := &statusError{
		Kind:    "error",
		Message: err.Error(),
		At:      time.Now().UTC().Format(time.RFC3339Nano),
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		status.StatusCode = httpErr.StatusCode
		status.Code = httpErr.Code
		if httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode >= 500 {
			status.Kind = "offline"
		} else {
			status.Kind = "http"
		}
		return status
	}
	var netErr net.Error
	if errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded) {
		status.Kind = "offline"
		return status
	}
	if errors.Is(err, ErrConflict) {
		status.Kind = "conflict"
	}
	return status
}

func (s *Syncer) logf(format string, args ...any) {
	if s.logger == nil {
		return
	}
	s.logger.Printf(format, args...)
}

func normalizeRemotePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	// Clean the path to resolve any ".." or "." components, preventing traversal.
	path = filepath.ToSlash(filepath.Clean(path))
	if path == "." || path == "" {
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

func inferProviderFromRoot(remoteRoot string) string {
	normalized := normalizeRemotePath(remoteRoot)
	if normalized == "/" {
		return ""
	}
	trimmed := strings.TrimPrefix(normalized, "/")
	if trimmed == "" {
		return ""
	}
	if idx := strings.Index(trimmed, "/"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	return strings.TrimSpace(trimmed)
}

func isUnderRemoteRoot(remoteRoot, remotePath string) bool {
	remoteRoot = normalizeRemotePath(remoteRoot)
	remotePath = normalizeRemotePath(remotePath)
	if remoteRoot == "/" {
		return true
	}
	return remotePath == remoteRoot || strings.HasPrefix(remotePath, remoteRoot+"/")
}

func remoteToLocalPath(localRoot, remoteRoot, remotePath string) (string, error) {
	localRoot = filepath.Clean(localRoot)
	remoteRoot = normalizeRemotePath(remoteRoot)
	remotePath = normalizeRemotePath(remotePath)
	if !isUnderRemoteRoot(remoteRoot, remotePath) {
		return "", fmt.Errorf("remote path %s is outside root %s", remotePath, remoteRoot)
	}
	rel := ""
	if remoteRoot == "/" {
		rel = strings.TrimPrefix(remotePath, "/")
	} else {
		rel = strings.TrimPrefix(remotePath, remoteRoot)
		rel = strings.TrimPrefix(rel, "/")
	}
	if rel == "" {
		return "", fmt.Errorf("remote path %s cannot map to local root", remotePath)
	}
	// Reject path traversal components in the relative path.
	relSlash := filepath.ToSlash(rel)
	if strings.HasPrefix(relSlash, "../") || relSlash == ".." || strings.Contains(relSlash, "/../") {
		return "", fmt.Errorf("path %s escapes local root", remotePath)
	}
	joined := filepath.Join(localRoot, filepath.FromSlash(rel))
	// Final safety check: resolved path must be under localRoot.
	if !strings.HasPrefix(filepath.Clean(joined), localRoot+string(filepath.Separator)) && filepath.Clean(joined) != localRoot {
		return "", fmt.Errorf("resolved path %s escapes local root %s", joined, localRoot)
	}
	return joined, nil
}

func localToRemotePath(localRoot, remoteRoot, localPath string) (string, error) {
	rel, err := filepath.Rel(localRoot, localPath)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "", fmt.Errorf("local root is not a file")
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "../") || rel == ".." {
		return "", fmt.Errorf("path %s escapes local root", localPath)
	}
	remoteRoot = normalizeRemotePath(remoteRoot)
	if remoteRoot == "/" {
		return normalizeRemotePath("/" + rel), nil
	}
	return normalizeRemotePath(remoteRoot + "/" + rel), nil
}

func detectContentType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".md" || ext == ".markdown" {
		return "text/markdown"
	}
	m := mime.TypeByExtension(ext)
	if m == "" {
		return "text/plain"
	}
	if idx := strings.Index(m, ";"); idx >= 0 {
		m = m[:idx]
	}
	return m
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hashString(s string) string {
	return hashBytes([]byte(s))
}

func correlationID() string {
	return fmt.Sprintf("mount_%d", time.Now().UnixNano())
}

func (c *HTTPClient) websocketURL(workspaceID string) (string, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return "", err
	}
	switch base.Scheme {
	case "http":
		base.Scheme = "ws"
	case "https":
		base.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported base url scheme %q", base.Scheme)
	}
	base.Path = fmt.Sprintf("/v1/workspaces/%s/fs/ws", url.PathEscape(workspaceID))
	// TODO: Remove query-param token once server supports Authorization header on WS upgrade.
	q := url.Values{}
	q.Set("token", c.Token())
	base.RawQuery = q.Encode()
	return base.String(), nil
}

func (c *HTTPClient) retryDelay(attempt int, retryAfterHeader string) time.Duration {
	maxDelay := c.maxDelay
	if maxDelay <= 0 {
		maxDelay = 2 * time.Second
	}
	if retryAfter := parseRetryAfter(retryAfterHeader); retryAfter > 0 {
		if retryAfter > maxDelay {
			return maxDelay
		}
		return retryAfter
	}
	delay := c.baseDelay
	if delay <= 0 {
		delay = 100 * time.Millisecond
	}
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= maxDelay {
			return maxDelay
		}
	}
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

func parseRetryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(header); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if ts, err := time.Parse(time.RFC1123, header); err == nil {
		delta := time.Until(ts)
		if delta > 0 {
			return delta
		}
	}
	return 0
}

func waitWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Chmod(mode); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	committed = true
	return nil
}
