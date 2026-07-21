package mountsync

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
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
	"net/http/pprof"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/agentworkforce/relayfile/internal/digest"
	"github.com/agentworkforce/relayfile/internal/relayfile"
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

const (
	mountWritebackCreateDraftContentIdentityKind       = "mount-writeback-create-draft"
	mountWritebackCreateDraftContentIdentityTTLSeconds = 2592000
)

// defaultFullPullEvery is the default cadence for the "trust but verify"
// periodic full tree pull that runs from the incremental path. At 30s sync
// intervals, 20 cycles is roughly every 10 minutes. This is the safety net
// for cloud-side revision reuse: even when the events feed says "nothing
// changed at rev_X," every Nth cycle we re-export the tree and let
// applyRemoteFile re-hash and overwrite any drift between the daemon's
// tracked.Hash and the actual remote content.
const defaultFullPullEvery = 20

// Bootstrap / cursor timeout defaults. The bootstrap default is the
// "unbounded while making progress" sentinel (<=0): the heavy full-tree
// pull is allowed to run as long as it keeps applying files within
// defaultBootstrapIdleTimeout. The cursor resolution gets its own short
// independent deadline so it can never hang an otherwise healthy cycle.
const (
	defaultBootstrapTimeout     = 0 * time.Second
	defaultBootstrapIdleTimeout = 90 * time.Second
	// defaultBootstrapStallCycles is the maximum number of consecutive
	// bootstrap cycles allowed to leave the persisted traversal checkpoint
	// unchanged. It turns a permanently failing resume point into an explicit
	// operator-visible failure instead of silently retrying forever.
	defaultBootstrapStallCycles = 20
	defaultCursorTimeout        = 60 * time.Second
	// defaultExportTimeout bounds the single atomic full-tree export so a slow
	// or 429-contended export yields to the resumable pullRemoteFullTree BEFORE
	// the no-progress bootstrap watchdog (defaultBootstrapIdleTimeout) cancels
	// the whole bootstrap. ExportFiles reports NO incremental progress until
	// its whole body returns, so without this bound a doomed export can burn
	// the entire watchdog window and be cancelled with zero files applied —
	// and, having no resume cursor, restart from scratch every cycle (the
	// #1499/#1516 non-convergence loop). Must stay strictly under
	// defaultBootstrapIdleTimeout.
	defaultExportTimeout = 45 * time.Second
	// defaultOutboxFlushTimeout bounds a durable writeback/outbox flush with
	// its OWN deadline derived from rootCtx (see outboxContext), independent of
	// the tiny per-cycle RELAYFILE_MOUNT_TIMEOUT. A small outbound write (e.g. a
	// Slack reply draft) must not share — and be starved by — the same 15s
	// budget as a full-tree mirror pull on a large workspace; when it does, the
	// write sits in the outbox retrying across cycles for minutes (the
	// churn-digest "context deadline exceeded" / late-reply failure mode). This
	// mirrors bootstrapContext's "derive from rootCtx, not the per-cycle ctx".
	defaultOutboxFlushTimeout         = 60 * time.Second
	defaultIncrementalReadNotReadyTTL = 5 * time.Minute
	defaultCursorResolutionAttempts   = 3
	defaultCursorRetryBaseDelay       = 250 * time.Millisecond
	defaultBootstrapReadWorkers       = 16
	// fullTreeTraversalDepth bounds each tree request so the client can see and
	// prune a reserved .relay directory before the server reaches deep
	// outbox/acked/mountcmd_* leaves. Depth 3 also covers the common Slack
	// channels/<id>/messages frontier in one request, avoiding one network
	// round trip per message directory.
	fullTreeTraversalDepth           = 3
	defaultIncrementalEventPageLimit = 50
	defaultRetryAfterMaxDelay        = 60 * time.Second
	defaultWebSocketReconnectBase    = 1 * time.Second
	defaultWebSocketReconnectMax     = 60 * time.Second
	defaultWebSocketReconnectJitter  = 200 * time.Millisecond
	DefaultWebSocketMaintenanceEvery = 1 * time.Second
)

// resolveDurationEnv returns the option value if non-zero, else parses the
// named env var, else falls back to def. A negative option/env keeps its
// (possibly sentinel) value.
func resolveDurationEnv(opt time.Duration, env string, def time.Duration, logger Logger) time.Duration {
	if opt != 0 {
		return opt
	}
	if raw := strings.TrimSpace(os.Getenv(env)); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			return parsed
		} else if logger != nil {
			logger.Printf("ignoring invalid %s=%q: %v", env, raw, err)
		}
	}
	return def
}

// resolveBootstrapStallCycles returns the explicit option when set, otherwise
// RELAYFILE_BOOTSTRAP_STALL_CYCLES, otherwise the default. A non-positive
// value is invalid because the guard must remain a bounded hard stop.
func resolveBootstrapStallCycles(opt int, logger Logger) int {
	if opt > 0 {
		return opt
	}
	if opt < 0 && logger != nil {
		logger.Printf("ignoring invalid BootstrapStallCycles=%d; using env/default", opt)
	}
	const env = "RELAYFILE_BOOTSTRAP_STALL_CYCLES"
	if raw := strings.TrimSpace(os.Getenv(env)); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			return parsed
		} else if logger != nil {
			logger.Printf("ignoring invalid %s=%q; expected a positive integer", env, raw)
		}
	}
	return defaultBootstrapStallCycles
}

var providerLayoutAliasSegments = []string{
	"by-title",
	"by-id",
	"by-edited",
	"by-name",
	"by-state",
}

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

type IncrementalReadNotReadyError struct {
	Path       string
	StatusCode int
	Code       string
	Message    string
}

func (e *IncrementalReadNotReadyError) Error() string {
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = "not readable yet"
	}
	return fmt.Sprintf("changed event for %s is not readable yet: %s", normalizeRemotePath(e.Path), message)
}

// BootstrapStalledError reports that a resumable bootstrap has retried the
// same persisted checkpoint too many times. It is deliberately a typed error
// so callers can distinguish an operator-actionable hard stop from a normal
// transient cloud failure.
type BootstrapStalledError struct {
	Cycles int
	Limit  int
	Cursor string
	Cause  error
}

func (e *BootstrapStalledError) Error() string {
	message := fmt.Sprintf("bootstrap stalled for %d consecutive checkpoint-stable cycles (limit %d, cursor %q)", e.Cycles, e.Limit, e.Cursor)
	if e.Cause != nil {
		return message + ": " + e.Cause.Error()
	}
	return message
}

func (e *BootstrapStalledError) Unwrap() error { return e.Cause }

type TreeEntry struct {
	Path        string `json:"path"`
	Type        string `json:"type"`
	Revision    string `json:"revision"`
	ContentHash string `json:"contentHash,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Encoding    string `json:"encoding,omitempty"`
}

type TreeResponse struct {
	Path       string      `json:"path"`
	Entries    []TreeEntry `json:"entries"`
	NextCursor *string     `json:"nextCursor"`
}

type FilesystemEvent struct {
	EventID     string `json:"eventId"`
	Type        string `json:"type"`
	Path        string `json:"path"`
	Revision    string `json:"revision"`
	ContentHash string `json:"contentHash,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Timestamp   string `json:"timestamp,omitempty"`
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
	ContentHash string `json:"contentHash,omitempty"`
}

type WriteResult struct {
	OpID           string `json:"opId,omitempty"`
	Status         string `json:"status,omitempty"`
	TargetRevision string `json:"targetRevision"`
	Writeback      struct {
		Provider string `json:"provider,omitempty"`
		State    string `json:"state,omitempty"`
	} `json:"writeback,omitempty"`
}

type RemoteClient interface {
	ListTree(ctx context.Context, workspaceID, path string, depth int, cursor string) (TreeResponse, error)
	ListEvents(ctx context.Context, workspaceID, provider, cursor string, limit int) (EventFeed, error)
	// LatestEventID returns the most recent event id for the workspace
	// (optionally filtered by provider) in a single round trip, using the
	// `direction=desc&limit=1` shape on /fs/events. Returns "" when the
	// workspace has no events. Implementations may return an HTTPError with
	// status 400 or 404 to signal that the server does not support the
	// descending-direction query; callers should treat that as a fallthrough
	// to the legacy page-walk.
	LatestEventID(ctx context.Context, workspaceID, provider string) (string, error)
	ReadFile(ctx context.Context, workspaceID, path string) (RemoteFile, error)
	WriteFile(ctx context.Context, workspaceID, path, baseRevision, contentType, content string) (WriteResult, error)
	WriteFilesBulk(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error)
	GetOperation(ctx context.Context, workspaceID, opID string) (OperationStatus, error)
	DeleteFile(ctx context.Context, workspaceID, path, baseRevision string) error
}

type exportSnapshotClient interface {
	ExportFiles(ctx context.Context, workspaceID, path string) ([]RemoteFile, error)
}

type githubWorkingTreeTarClient interface {
	ExportGithubWorkingTreeTar(ctx context.Context, workspaceID string, seed GithubWorkingTreeSeedRequest) (GithubWorkingTreeTar, error)
}

type GithubWorkingTreeSeedRequest struct {
	Owner      string
	Repo       string
	PathPrefix string
	HeadSHA    string
	Gzip       bool
}

type GithubWorkingTreeTar struct {
	Body        io.ReadCloser
	ContentType string
}

type LazyMaterializeClient interface {
	LazyMaterialize(ctx context.Context, workspaceID, owner, repo string) error
}

type HTTPClient struct {
	baseURL          string
	token            string
	tokenMu          sync.RWMutex
	tokenRefreshMu   sync.RWMutex
	tokenRefreshFunc AuthTokenRefreshFunc
	httpClient       *http.Client
	maxRetries       int
	baseDelay        time.Duration
	maxDelay         time.Duration
	httpStatusLogMu  sync.RWMutex
	httpStatusLogger Logger
}

// AuthTokenRefreshFunc returns a replacement bearer token after the current
// token has been rejected by the server. changed must be true only when the
// replacement token should be installed and the failed request retried.
type AuthTokenRefreshFunc func(currentToken string) (newToken string, changed bool, err error)

// NewSyncTransport builds the *http.Transport used by every mount-daemon
// HTTP client. It deliberately sets GRANULAR timeouts that bound the parts
// of a request that can wedge against an unresponsive server (connect, TLS
// handshake, time-to-first-byte) but imposes NO total-request deadline.
//
// Why no total-request cap: the bootstrap full-tree pull on a large
// workspace legitimately streams a multi-MB body for far longer than the
// per-cycle RELAYFILE_MOUNT_TIMEOUT (default 15s). An http.Client.Timeout
// is a whole-request wall-clock that net/http enforces INDEPENDENT of
// context — it kills the body read mid-stream regardless of how the
// caller scoped its context. That is precisely the gap that left the
// 581-file workspace stuck ("request canceled ... while reading body").
// Cancellation of a genuinely stuck transfer is instead the job of the
// caller's context: the per-cycle ctx for incremental sync, the
// progress-extending bootstrap ctx (+ idle watchdog) for the full pull,
// and the cursor ctx for event-cursor resolution. ResponseHeaderTimeout
// still bounds a server that accepts the connection but never starts
// responding, without ever capping a body that is actively progressing.
func NewSyncTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Intentionally NO total-request timeout here. Bootstrap/poll
		// callers should pair this transport with http.Client.Timeout == 0.
	}
}

// NewSyncHTTPClient returns an *http.Client wired with NewSyncTransport and
// — critically — Timeout: 0. base, if non-nil, is chained beneath the sync
// transport (used to layer the writeback-failure RoundTripper) and is
// responsible for delegating to a NewSyncTransport()-style transport.
func NewSyncHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   0, // no whole-request cap; context is the cancel mechanism
		Transport: NewSyncTransport(),
	}
}

func NewHTTPClient(baseURL, token string, httpClient *http.Client) *HTTPClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	if httpClient == nil {
		// Default to the no-whole-request-timeout sync client so callers
		// that pass nil (tests, embedders) inherit the bootstrap-safe
		// behaviour rather than the old blunt 15s cap.
		httpClient = NewSyncHTTPClient()
	} else {
		// Do not mutate the caller's client. Poll/bootstrap code that needs
		// no whole-request cap passes NewSyncHTTPClient explicitly; other
		// callers (notably FUSE) may intentionally rely on Timeout.
		cloned := *httpClient
		httpClient = &cloned
	}
	return &HTTPClient{
		baseURL:    baseURL,
		token:      strings.TrimSpace(token),
		httpClient: httpClient,
		maxRetries: 3,
		baseDelay:  100 * time.Millisecond,
		maxDelay:   defaultRetryAfterMaxDelay,
	}
}

func (c *HTTPClient) SetHTTPStatusLogger(logger Logger) {
	c.httpStatusLogMu.Lock()
	defer c.httpStatusLogMu.Unlock()
	c.httpStatusLogger = logger
}

func (c *HTTPClient) SetTokenRefreshFunc(refresh AuthTokenRefreshFunc) {
	c.tokenRefreshMu.Lock()
	defer c.tokenRefreshMu.Unlock()
	c.tokenRefreshFunc = refresh
}

func (c *HTTPClient) logHTTPStatus(method, requestPath string, statusCode int, retryAfter string, attempt int) {
	c.httpStatusLogMu.RLock()
	logger := c.httpStatusLogger
	c.httpStatusLogMu.RUnlock()
	if logger == nil {
		return
	}
	retryAfter = strings.TrimSpace(retryAfter)
	if retryAfter == "" {
		logger.Printf(
			"relayfile http %d method=%s path=%s attempt=%d",
			statusCode,
			method,
			requestPath,
			attempt+1,
		)
		return
	}
	logger.Printf(
		"relayfile http %d method=%s path=%s retry-after=%q attempt=%d",
		statusCode,
		method,
		requestPath,
		retryAfter,
		attempt+1,
	)
}

func (c *HTTPClient) ListTree(ctx context.Context, workspaceID, path string, depth int, cursor string) (TreeResponse, error) {
	q := url.Values{}
	q.Set("path", normalizeRemotePath(path))
	// Relayfile mount owns .relay and its atomic state files as private runtime
	// data. Ask the server to remove those rows before raw SQL pagination so a
	// large acked outbox cannot consume every page before real content appears.
	q.Set("excludeMountRuntime", "true")
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

func (c *HTTPClient) LatestEventID(ctx context.Context, workspaceID, provider string) (string, error) {
	q := url.Values{}
	if strings.TrimSpace(provider) != "" {
		q.Set("provider", strings.TrimSpace(provider))
	}
	q.Set("direction", "desc")
	q.Set("limit", "1")
	var out EventFeed
	if err := c.doJSON(ctx, http.MethodGet,
		fmt.Sprintf("/v1/workspaces/%s/fs/events?%s", url.PathEscape(workspaceID), q.Encode()),
		nil, nil, &out); err != nil {
		return "", err
	}
	if len(out.Events) == 0 {
		return "", nil
	}
	return strings.TrimSpace(out.Events[0].EventID), nil
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

func (c *HTTPClient) GetOperation(ctx context.Context, workspaceID, opID string) (OperationStatus, error) {
	var out OperationStatus
	err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/v1/workspaces/%s/ops/%s", url.PathEscape(workspaceID), url.PathEscape(opID)), nil, nil, &out)
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

func (c *HTTPClient) ExportGithubWorkingTreeTar(ctx context.Context, workspaceID string, seed GithubWorkingTreeSeedRequest) (GithubWorkingTreeTar, error) {
	q := url.Values{}
	q.Set("format", "tar")
	q.Set("decode", "github-working-tree")
	q.Set("pathPrefix", normalizeRemotePath(seed.PathPrefix))
	q.Set("headSha", strings.TrimSpace(seed.HeadSHA))
	if !seed.Gzip {
		q.Set("gzip", "0")
	}
	requestPath := fmt.Sprintf("/v1/workspaces/%s/fs/export?%s", url.PathEscape(workspaceID), q.Encode())

	authRefreshTried := false
	for attempt := 0; ; attempt++ {
		requestToken := c.Token()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+requestPath, nil)
		if err != nil {
			return GithubWorkingTreeTar{}, err
		}
		req.Header.Set("Authorization", "Bearer "+requestToken)
		req.Header.Set("X-Correlation-Id", correlationID())
		resp, err := c.httpClient.Do(req)
		if err != nil {
			if attempt < c.maxRetries {
				if waitErr := waitWithContext(ctx, c.retryDelay(attempt+1, "")); waitErr != nil {
					return GithubWorkingTreeTar{}, waitErr
				}
				continue
			}
			return GithubWorkingTreeTar{}, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			c.logHTTPStatus(http.MethodGet, requestPath, resp.StatusCode, resp.Header.Get("Retry-After"), attempt)
			return GithubWorkingTreeTar{
				Body:        resp.Body,
				ContentType: resp.Header.Get("Content-Type"),
			}, nil
		}
		payloadBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return GithubWorkingTreeTar{}, readErr
		}
		c.logHTTPStatus(http.MethodGet, requestPath, resp.StatusCode, resp.Header.Get("Retry-After"), attempt)
		if resp.StatusCode == http.StatusUnauthorized && !authRefreshTried {
			authRefreshTried = true
			if c.refreshTokenAfterUnauthorized(requestToken) {
				continue
			}
		}
		if (resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode >= 500 && resp.StatusCode <= 599)) && attempt < c.maxRetries {
			if waitErr := waitWithContext(ctx, c.retryDelay(attempt+1, resp.Header.Get("Retry-After"))); waitErr != nil {
				return GithubWorkingTreeTar{}, waitErr
			}
			continue
		}
		var errPayload struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(payloadBytes, &errPayload)
		return GithubWorkingTreeTar{}, &HTTPError{
			StatusCode: resp.StatusCode,
			Code:       errPayload.Code,
			Message:    errPayload.Message,
		}
	}
}

func (c *HTTPClient) LazyMaterialize(ctx context.Context, workspaceID, owner, repo string) error {
	return c.doJSON(
		ctx,
		http.MethodPost,
		fmt.Sprintf(
			"/v1/workspaces/%s/integrations/github/repos/%s/%s/materialize",
			url.PathEscape(workspaceID),
			url.PathEscape(strings.TrimSpace(owner)),
			url.PathEscape(strings.TrimSpace(repo)),
		),
		nil,
		nil,
		nil,
	)
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
	authRefreshTried := false
	for attempt := 0; ; attempt++ {
		var bodyReader io.Reader
		if bodyBytes != nil {
			bodyReader = bytes.NewReader(bodyBytes)
		}
		requestToken := c.Token()
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+requestPath, bodyReader)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+requestToken)
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
		c.logHTTPStatus(method, requestPath, resp.StatusCode, resp.Header.Get("Retry-After"), attempt)

		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			if out == nil || len(payloadBytes) == 0 {
				return nil
			}
			return json.Unmarshal(payloadBytes, out)
		}

		if resp.StatusCode == http.StatusUnauthorized && !authRefreshTried {
			authRefreshTried = true
			if c.refreshTokenAfterUnauthorized(requestToken) {
				continue
			}
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

func (c *HTTPClient) refreshTokenAfterUnauthorized(attemptedToken string) bool {
	attemptedToken = strings.TrimSpace(attemptedToken)
	current := c.Token()
	if attemptedToken != "" && current != attemptedToken {
		return true
	}
	c.tokenRefreshMu.RLock()
	refresh := c.tokenRefreshFunc
	c.tokenRefreshMu.RUnlock()
	if refresh == nil {
		return false
	}
	next, changed, err := refresh(current)
	if err != nil || !changed {
		return false
	}
	next = strings.TrimSpace(next)
	if next == "" || next == current {
		return false
	}
	c.SetToken(next)
	return true
}

type SyncerOptions struct {
	WorkspaceID   string
	RemoteRoot    string
	LocalRoot     string
	StateFile     string
	StateDir      string
	MountKind     string
	ValidateState bool
	EventProvider string
	Scopes        []string
	WebSocket     *bool
	RootCtx       context.Context
	Logger        Logger
	Mode          string
	SyncMode      string
	Interval      time.Duration
	// FullPullEvery controls how often the incremental pull path forces a
	// full tree pull as a "trust but verify" safety net against cloud-side
	// revision reuse (see fix/cloud-side-rev-reuse-defense). 0 means use
	// the default (defaultFullPullEvery, ~10 min at 30s intervals). A
	// negative value disables the periodic full pull entirely.
	FullPullEvery int
	// BootstrapTimeout caps the one-time full-tree bootstrap / periodic
	// full pull. It is derived from the Syncer's RootCtx (NOT the inbound
	// per-cycle ctx) so a tiny per-cycle deadline cannot starve a large
	// initial mirror. 0 falls back to env RELAYFILE_BOOTSTRAP_TIMEOUT;
	// the resolved default is the "unbounded while making progress"
	// sentinel (<=0): the bootstrap runs to completion as long as it keeps
	// applying files within the idle window.
	BootstrapTimeout time.Duration
	// BootstrapStallCycles is the maximum number of consecutive bootstrap
	// cycles that may leave the persisted traversal checkpoint unchanged. 0
	// falls back to RELAYFILE_BOOTSTRAP_STALL_CYCLES, then the default (20).
	BootstrapStallCycles int
	// CursorTimeout bounds each resolveLatestEventCursor attempt with its OWN
	// deadline derived from RootCtx. Timeout-class failures are retried with
	// backoff before the caller decides whether a full pull is safe.
	//
	// 0 falls back to env RELAYFILE_CURSOR_TIMEOUT, default 60s.
	CursorTimeout time.Duration
	// ExportTimeout bounds each atomic full-tree export (ExportFiles) with its
	// OWN deadline derived from the bootstrap ctx, kept strictly under
	// bootstrapIdleTimeout. When the export does not finish in time the syncer
	// falls through to the resumable, per-page pullRemoteFullTree instead of
	// retrying a doomed one-shot export. 0 falls back to env
	// RELAYFILE_EXPORT_TIMEOUT, default 45s; values >= bootstrapIdleTimeout are
	// clamped below it so the fall-through always fires before the watchdog.
	ExportTimeout time.Duration
	// OutboxFlushTimeout bounds a durable writeback/outbox flush with its OWN
	// deadline derived from RootCtx (see outboxContext), so the tiny per-cycle
	// RELAYFILE_MOUNT_TIMEOUT that bounds a mirror cycle cannot starve an
	// outbound write. 0 falls back to env RELAYFILE_OUTBOX_TIMEOUT, default 60s.
	OutboxFlushTimeout time.Duration
	// IncrementalReadNotReadyTTL bounds how long an incremental create/update
	// event may keep returning ReadFile 404 before the syncer treats it as a
	// delete and advances past the event. 0 falls back to env
	// RELAYFILE_INCREMENTAL_READ_NOT_READY_TTL, default 5m.
	IncrementalReadNotReadyTTL time.Duration
	// ForceFullReconcile, when non-nil and true, forces one full reconcile
	// regardless of BootstrapComplete (escape hatch / clobber-remnant
	// recovery). nil falls back to env RELAYFILE_FORCE_FULL_RECONCILE.
	ForceFullReconcile *bool
	// LazyRepos controls lazy GitHub repo subtree hydration. nil falls back to env.
	LazyRepos *bool
	// LazySkipUntrackedPush controls whether pushLocal excludes local files
	// under a lazy GitHub repo subtree that this daemon never tracked in
	// s.state.Files (see SkippedLazyUntrackedPush). Only takes effect when
	// LazyRepos is true. nil falls back to env RELAYFILE_LAZY_SKIP_UNTRACKED_PUSH,
	// default true.
	LazySkipUntrackedPush *bool
	// LowMemory avoids expensive diagnostic/public-state scans and large
	// in-memory snapshots. nil falls back to RELAYFILE_MOUNT_LOW_MEMORY.
	LowMemory *bool
	// ProviderLayoutRegistrar receives deterministic per-provider layout
	// manifests derived from remote snapshots. Mount layers can implement
	// this to expose virtual <provider>/LAYOUT.md files; legacy
	// <provider>/.layout.md remains a compatibility alias.
	ProviderLayoutRegistrar ProviderLayoutRegistrar
	DigestSource            digest.ChangeEventSource
	DigestProviders         []string
	DigestTimezone          string
	DigestNow               func() time.Time
	// Now overrides the wall clock used by the outbox writeback path
	// (first-seen stamping, retry-backoff scheduling, due-ness, ack time).
	// Defaults to time.Now when nil. Tests inject a controllable clock to make
	// retry-backoff and dead-letter timing deterministic without real sleeps.
	Now func() time.Time
}

type Logger interface {
	Printf(format string, args ...any)
}

func StartDiagnostics(ctx context.Context, addr string, memlogInterval time.Duration, logger Logger) (*http.Server, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" && memlogInterval <= 0 {
		return nil, nil
	}
	if logger == nil {
		logger = noopLogger{}
	}
	if memlogInterval > 0 {
		go logMemoryStats(ctx, memlogInterval, logger)
	}
	if addr == "" {
		return nil, nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		logger.Printf("relayfile diagnostics listening on http://%s/debug/pprof/", addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("relayfile diagnostics server failed: %v", err)
		}
	}()
	return server, nil
}

type noopLogger struct{}

func (noopLogger) Printf(string, ...any) {}

func normalizeSyncMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "write-only":
		return "write-only"
	default:
		return "mirror"
	}
}

func logMemoryStats(ctx context.Context, interval time.Duration, logger Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		logMemoryStatSample(logger)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func logMemoryStatSample(logger Logger) {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	logger.Printf(
		"relayfile memory: alloc=%s total_alloc=%s sys=%s heap_alloc=%s heap_sys=%s heap_objects=%d goroutines=%d next_gc=%s num_gc=%d",
		formatBytes(stats.Alloc),
		formatBytes(stats.TotalAlloc),
		formatBytes(stats.Sys),
		formatBytes(stats.HeapAlloc),
		formatBytes(stats.HeapSys),
		stats.HeapObjects,
		runtime.NumGoroutine(),
		formatBytes(stats.NextGC),
		stats.NumGC,
	)
}

func formatBytes(value uint64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%dB", value)
	}
	div, exp := uint64(unit), 0
	for n := value / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(value)/float64(div), "KMGTPE"[exp])
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
	outboxDir            string
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
	wsNextAttempt        time.Time
	wsReconnectFailures  int
	wsConnecting         bool
	wsGeneration         int64
	bulkFlushThreshold   int
	mode                 string
	interval             time.Duration
	fullPullEvery        int
	cursorTimeout        time.Duration
	exportTimeout        time.Duration
	outboxFlushTimeout   time.Duration
	bootstrapTimeout     time.Duration
	bootstrapIdleTimeout time.Duration
	bootstrapStallCycles int
	readNotReadyTTL      time.Duration
	forceFullReconcile   bool
	incrementalCycles    int
	// staleAliasSkips counts stale provider-layout-alias events drained in the
	// current reconcile cycle. Reset at the start of each cycle and surfaced to
	// the CLI (e.g. `relayfile pull` summary, `workspace status`) so operators
	// can see the stuck-event drain making progress.
	staleAliasSkips int
	// skip-stuck escape hatch (`relayfile writeback skip-stuck`). When
	// skipStuckMode is set, the incremental drain drops every read-404 event
	// immediately — not just provider-layout-alias paths — without waiting the
	// read-not-ready TTL. skipStuckMax bounds the number of events dropped
	// (0 = unbounded); skipStuckCount tracks how many were dropped.
	skipStuckMode  bool
	skipStuckMax   int
	skipStuckCount int
	syncActive     bool
	// fullPullActive is true only while a normal SyncOnce/Reconcile cycle is
	// applying a point-in-time full snapshot. The cycle still owns mu for all
	// state mutation, but releases it around remote/downstream I/O so watcher
	// writeback and durable receipt settlement can take priority. Paths touched
	// by that up-path are remembered for the lifetime of the snapshot so stale
	// export/tree data cannot overwrite or tombstone a concurrent local write.
	fullPullActive        bool
	fullPullUpPaths       map[string]struct{}
	oversizedLogged       map[string]struct{}
	controlSkipLogged     map[string]struct{}
	lazyUntrackedLogged   map[string]struct{}
	quarantinedPaths      map[string]struct{}
	lazyRepos             bool
	lazySkipUntrackedPush bool
	lowMemory             bool
	writeOnly             bool
	layoutRegistrar       ProviderLayoutRegistrar
	githubWorkingTree     *githubWorkingTreeMount
	closeScheduler        *CloseScheduler
	rollingCoalescer      *RollingDigestCoalescer
	circuit               *CloudErrorCircuit
	maxOutboxAttempts     int
	nowFn                 func() time.Time
	// credExpiresAt is the RFC3339 expiry of the delegated access token,
	// set by the CLI layer via SetCredentialExpiry and included in the
	// public state as credExpiresInSecs so operators get advance warning.
	credExpiresAt string
	mu            sync.Mutex
}

// runFullPullIO temporarily releases the Syncer's state mutex around a remote
// read that belongs to a full snapshot. Callers enter with mu held and must not
// access mutable Syncer state from fn. Direct low-level tests that invoke full
// pull helpers without a reserved sync cycle leave fullPullActive false, so
// they retain their historical lock-free calling convention.
func (s *Syncer) runFullPullIO(fn func()) {
	if !s.fullPullActive {
		fn()
		return
	}
	s.mu.Unlock()
	defer s.mu.Lock()
	fn()
}

// runReservedSyncIO is the same lock split for bounded down-path I/O that
// brackets a full pull (for example, resolving the event cursor immediately
// before/after bootstrap). These calls are not inside pullRemoteFull itself,
// but leaving the mutex held for their independent 60s deadline would recreate
// the same up-path starvation at the edge of the heavy mirror operation.
func (s *Syncer) runReservedSyncIO(fn func()) {
	if !s.syncActive {
		fn()
		return
	}
	s.mu.Unlock()
	defer s.mu.Lock()
	fn()
}

// yieldFullPullStateLock gives an already-waiting watcher/outbox operation a
// scheduling point between local snapshot applications. Remote export can
// return thousands of files at once, so releasing only around the HTTP body
// would still let the subsequent disk apply monopolize mu for a long time.
func (s *Syncer) yieldFullPullStateLock() {
	if !s.fullPullActive {
		return
	}
	s.mu.Unlock()
	runtime.Gosched()
	s.mu.Lock()
}

func (s *Syncer) fullPullPathTouchedByUpPath(remotePath string) bool {
	if !s.fullPullActive {
		return false
	}
	_, ok := s.fullPullUpPaths[normalizeRemotePath(remotePath)]
	return ok
}

func (s *Syncer) markFullPullUpPath(remotePath string) {
	if !s.fullPullActive {
		return
	}
	s.fullPullUpPaths[normalizeRemotePath(remotePath)] = struct{}{}
}

// now returns the current time using the injected clock when set (tests),
// otherwise the wall clock. Used by the outbox writeback path so retry-backoff
// and dead-letter timing are deterministic under test without real sleeps.
func (s *Syncer) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn()
	}
	return time.Now()
}

type mountState struct {
	Files                      map[string]trackedFile `json:"files"`
	EventsCursor               string                 `json:"eventsCursor,omitempty"`
	IncrementalCheckpoint      *incrementalCheckpoint `json:"incrementalCheckpoint,omitempty"`
	IncrementalBacklogDraining bool                   `json:"incrementalBacklogDraining,omitempty"`
	LastReconcileAt            string                 `json:"lastReconcileAt,omitempty"`
	LastSuccessfulReconcileAt  string                 `json:"lastSuccessfulReconcileAt,omitempty"`
	LastEventAt                string                 `json:"lastEventAt,omitempty"`
	LastError                  *statusError           `json:"lastError,omitempty"`
	// LastAppliedRevision is the highest cloud revision the daemon has
	// successfully reconciled. Snapshot deletes refuse to fire unless the
	// freshly-observed revision strictly advances past this value, which
	// prevents an older replayed listing from authorizing destructive ops.
	LastAppliedRevision string `json:"lastAppliedRevision,omitempty"`
	// Counters carries telemetry that the public status JSON surfaces.
	Counters telemetryCounters `json:"counters,omitempty"`
	// Bootstrap* track the one-time full-tree bootstrap so it can resume
	// after interruption and so the fast-path can refuse to short-circuit
	// until the workspace has been fully mirrored at least once. All fields
	// are additive/omitempty: legacy state files load with zero values
	// (BootstrapComplete=false), which self-heals by forcing a full
	// reconcile on the next cycle.
	BootstrapComplete    bool     `json:"bootstrapComplete,omitempty"`
	BootstrapDirectories []string `json:"bootstrapDirectories,omitempty"`
	BootstrapCursor      string   `json:"bootstrapCursor,omitempty"`
	BootstrapFilesSynced int      `json:"bootstrapFilesSynced,omitempty"`
	BootstrapFilesTotal  int      `json:"bootstrapFilesTotal,omitempty"`
	BootstrapStartedAt   string   `json:"bootstrapStartedAt,omitempty"`
	// BootstrapStallCycles counts consecutive bootstrap cycles that did not
	// advance the resumable directory/cursor checkpoint. It is persisted so a
	// process restart cannot evade the hard-stall guard.
	BootstrapStallCycles int `json:"bootstrapStallCycles,omitempty"`
	// QuarantinedPaths holds remote paths that cannot be materialized locally
	// due to file/directory name collisions. Persisted across cycle restarts
	// so the daemon does not re-fetch these paths from the cloud until the
	// adapter emitting the collision is fixed. Cleared on bootstrap completion
	// so a fixed adapter gets a clean slate on the next full cycle.
	QuarantinedPaths map[string]string `json:"quarantinedPaths,omitempty"`
	// SkippedMaterializations durably retains per-path local apply failures
	// after the traversal cursor advances. Later cycles retry these paths
	// directly, so skip-and-continue cannot turn into silent mirror data loss.
	SkippedMaterializations  map[string]skippedMaterialization `json:"skippedMaterializations,omitempty"`
	SyncMode                 string                            `json:"syncMode,omitempty"`
	GithubWorkingTreeHeadSHA string                            `json:"githubWorkingTreeHeadSha,omitempty"`
	// IncrementalReadNotReadySince records first-seen timestamps for
	// incremental create/update events whose remote content was not readable
	// yet. The daemon retries these without advancing EventsCursor until the
	// TTL expires, then treats a still-unreadable path as deleted so
	// create-then-delete events cannot wedge the mount forever.
	IncrementalReadNotReadySince map[string]string `json:"incrementalReadNotReadySince,omitempty"`
}

type incrementalCheckpoint struct {
	Cursor     string `json:"cursor,omitempty"`
	PageCursor string `json:"pageCursor,omitempty"`
	Phase      string `json:"phase,omitempty"`
	Path       string `json:"path,omitempty"`
}

// telemetryCounters tracks defensive-guard activity so operators can see at
// a glance whether the breaker has fired, oversized writebacks have been
// dropped, root-target denials have hit, etc.
type telemetryCounters struct {
	SkippedOversizeWriteback uint64 `json:"skippedOversizeWriteback,omitempty"`
	DeniedRootTarget         uint64 `json:"deniedRootTarget,omitempty"`
	// PathMaterializationSkipped counts remote files that could not be
	// represented at one local path (for example ENAMETOOLONG/ELOOP). These
	// failures are isolated to the file so one pathological provider record
	// cannot prevent bootstrap checkpoint progress for the rest of the mount.
	PathMaterializationSkipped uint64 `json:"pathMaterializationSkipped,omitempty"`
	SnapshotDeleteBlocked      uint64 `json:"snapshotDeleteBlocked,omitempty"`
	CircuitOpenEvents          uint64 `json:"circuitOpenEvents,omitempty"`
	TombstonesPending          uint64 `json:"tombstonesPending,omitempty"`
	TombstonesConfirmed        uint64 `json:"tombstonesConfirmed,omitempty"`
	TombstonesAgedOut          uint64 `json:"tombstonesAgedOut,omitempty"`
	// PathCollisionQuarantined counts apply attempts skipped because a remote
	// path could not be represented on the local filesystem — an ancestor
	// component exists as a regular file (or the target is a directory). The
	// fix belongs in the emitting adapter (do not emit one name as both a file
	// and a directory); the daemon quarantines the path so a single collision
	// can't wedge the whole mount. See isRemotePathCollision.
	PathCollisionQuarantined uint64 `json:"pathCollisionQuarantined,omitempty"`
	// SkippedLazyUntrackedPush counts real push-skip decisions for local
	// files excluded because they sit under a lazy GitHub repo subtree
	// (isLazyGithubRepoSubtreePath) this daemon never tracked in
	// s.state.Files — e.g. a canonical record materialized by an isolated
	// non-lazy pre-pull with its own state file. Without this guard, an
	// untracked-file push path would treat the pre-pulled record as a
	// brand-new local write and push it upstream as agent_write. Writeback
	// drafts/commands (isMountWritebackCreateDraftPath,
	// isGithubAdapterCreateCommandPath) are exempt and still push. Increments
	// only at the two actual skip decision points — pushLocal's
	// SkipWriteback check and preparePendingBulkWrite (the watcher-path
	// choke point) — never from scanLocalFiles/savePublicState's
	// status-display scans, so a state save cannot inflate this counter.
	SkippedLazyUntrackedPush uint64 `json:"skippedLazyUntrackedPush,omitempty"`
}

// quarantineRemotePath records a remote path that can't be materialized
// locally because of a file/directory name collision. It counts every
// occurrence (consistent with the other defensive counters), but logs each
// distinct path only once per process so a persistently-colliding path does not
// spam the log on every sync cycle. Mirrors the oversizedLogged dedup pattern.
func (s *Syncer) quarantineRemotePath(remotePath, detail string, cause error) {
	s.state.Counters.PathCollisionQuarantined++
	if s.quarantinedPaths == nil {
		s.quarantinedPaths = map[string]struct{}{}
	}
	if _, seen := s.quarantinedPaths[remotePath]; seen {
		return
	}
	s.quarantinedPaths[remotePath] = struct{}{}
	if s.state.QuarantinedPaths == nil {
		s.state.QuarantinedPaths = map[string]string{}
	}
	s.state.QuarantinedPaths[remotePath] = detail
	s.logf("quarantining remote path %s: %s (%v); skipping so the sync cycle can complete — fix is adapter-side (a name emitted as both a file and a directory)", remotePath, detail, cause)
}

// isRemotePathCollision reports whether err is a POSIX path-shape collision:
// an ancestor path component is a regular file (ENOTDIR), or the target name is
// already a directory / already exists with the wrong type (EISDIR/os.ErrExist).
// This happens when an adapter emits the same name as both a file and a
// directory — e.g. the Slack adapter writing a thread reply leaf
// `replies/<ts>.json` while also nesting that reply's children under a
// directory at the same stem. Such a path can never be materialized here, so
// failing the sync cycle on it would wedge the mount forever (bootstrap never
// completes, the teardown writeback flush hangs and is killed at its timeout,
// and the run is marked FAILED). The daemon logs + counts it and moves on.
func isRemotePathCollision(err error) bool {
	return errors.Is(err, syscall.ENOTDIR) ||
		errors.Is(err, syscall.EISDIR) ||
		errors.Is(err, os.ErrExist)
}

// isPathLocalMaterializationError reports failures caused by one local path's
// representation or permissions. They differ from process/system failures
// such as EMFILE or ENOSPC: retrying the same provider path cannot make them
// succeed, while aborting the whole page prevents unrelated files from being
// mirrored and wedges the bootstrap checkpoint forever.
func isPathLocalMaterializationError(err error) bool {
	return errors.Is(err, syscall.ENAMETOOLONG) ||
		errors.Is(err, syscall.ELOOP) ||
		errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.EROFS)
}

func (s *Syncer) skipPathLocalMaterializationError(remotePath, operation string, err error) bool {
	if !isPathLocalMaterializationError(err) {
		return false
	}
	s.recordSkippedMaterialization(remotePath, operation, err)
	s.state.Counters.PathMaterializationSkipped++
	s.logf("warning: skipping remote file %s after local %s failed: %v; continuing sync cycle", normalizeRemotePath(remotePath), operation, err)
	return true
}

func (s *Syncer) recordSkippedMaterialization(remotePath, operation string, err error) {
	remotePath = normalizeRemotePath(remotePath)
	now := s.now().UTC().Format(time.RFC3339Nano)
	if s.state.SkippedMaterializations == nil {
		s.state.SkippedMaterializations = map[string]skippedMaterialization{}
	}
	record := s.state.SkippedMaterializations[remotePath]
	if record.FirstSeenAt == "" {
		record.FirstSeenAt = now
	}
	record.LastAttemptAt = now
	record.AttemptCount++
	record.Operation = strings.TrimSpace(operation)
	record.LastError = sanitizeOutboxError(err)
	s.state.SkippedMaterializations[remotePath] = record
}

func (s *Syncer) clearSkippedMaterialization(remotePath string) {
	if s.state.SkippedMaterializations == nil {
		return
	}
	delete(s.state.SkippedMaterializations, normalizeRemotePath(remotePath))
	if len(s.state.SkippedMaterializations) == 0 {
		s.state.SkippedMaterializations = nil
	}
}

type skippedMaterialization struct {
	Operation     string `json:"operation,omitempty"`
	FirstSeenAt   string `json:"firstSeenAt"`
	LastAttemptAt string `json:"lastAttemptAt"`
	AttemptCount  int    `json:"attemptCount"`
	LastError     string `json:"lastError"`
}

type trackedFile struct {
	Revision    string `json:"revision"`
	ContentType string `json:"contentType"`
	Encoding    string `json:"encoding,omitempty"`
	Hash        string `json:"hash"`
	Dirty       bool   `json:"dirty,omitempty"`
	// LocalRelativePath preserves the identity of a valid user-created local
	// name when the mirror's conservative atomic-write limit would otherwise
	// shorten it. Remote-origin names still use deterministic shortening.
	LocalRelativePath string `json:"localRelativePath,omitempty"`
	// DeletePending is set only by an observed local delete. A missing file
	// during a scan is not enough evidence to delete cloud state.
	DeletePending bool `json:"deletePending,omitempty"`
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

type githubWorkingTreeMount struct {
	Owner        string
	Repo         string
	RepoRoot     string
	ContentsRoot string
	HeadSHA      string
}

type localSnapshot struct {
	RawContent    []byte
	WireContent   string
	ContentType   string
	Encoding      string
	Hash          string
	LocalPath     string
	SkipWriteback bool
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
	SyncMode                  string                     `json:"syncMode,omitempty"`
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
	FailedWritebacks          uint64                     `json:"failedWritebacks,omitempty"`
	LastError                 *statusError               `json:"lastError,omitempty"`
	Files                     map[string]publicFileState `json:"files,omitempty"`
	LowMemory                 bool                       `json:"lowMemory,omitempty"`
	// Counters mirrors the in-state telemetry counters so consumers of
	// .relay/state.json can see breaker activity, oversize-writeback
	// drops, and tombstone progress without parsing the private state
	// file. Existing consumers can ignore unknown fields.
	Counters telemetryCounters `json:"counters,omitempty"`
	// Circuit summarises the cloud-error breaker state.
	Circuit *CircuitState `json:"circuit,omitempty"`
	// Outbox summarizes durable upload commands that are waiting for a
	// server ACK, have exhausted retry budget, or have completed.
	Outbox outboxSummary `json:"outbox,omitempty"`
	// LastAppliedRevision is the highest cloud revision the daemon has
	// reconciled. Useful for operator status display.
	LastAppliedRevision string `json:"lastAppliedRevision,omitempty"`
	// Bootstrap surfaces in-progress full-tree bootstrap so operators see
	// "bootstrapping N/M files" instead of a misleading stall. The resume
	// cursor is intentionally NOT exposed (internal-only).
	Bootstrap *bootstrapStatus `json:"bootstrap,omitempty"`
	// ReconcileAgeSecs is seconds elapsed since the last successful reconcile.
	// 0 when no successful reconcile has been recorded yet.
	ReconcileAgeSecs int64 `json:"reconcileAgeSecs,omitempty"`
	// CredExpiresInSecs is seconds until the delegated access token expires.
	// Negative means already expired. Omitted (0) when unknown.
	CredExpiresInSecs int64 `json:"credExpiresInSecs,omitempty"`
}

// bootstrapStatus is the public, cursor-free view of bootstrap progress.
type bootstrapStatus struct {
	Phase       string `json:"phase"`
	FilesSynced int    `json:"filesSynced"`
	FilesTotal  int    `json:"filesTotal,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
}

type publicStateFlags struct {
	Stale                bool `json:"stale"`
	Offline              bool `json:"offline"`
	Syncing              bool `json:"syncing,omitempty"`
	HasConflicts         bool `json:"hasConflicts"`
	HasPendingWriteback  bool `json:"hasPendingWriteback"`
	OutboxNeedsAttention bool `json:"outboxNeedsAttention,omitempty"`
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
	stateFileOpt := opts.StateFile
	if strings.TrimSpace(stateFileOpt) == "" && strings.TrimSpace(opts.StateDir) == "" && !opts.ValidateState {
		stateFileOpt = filepath.Join(localRoot, LegacyMountStateFileName)
	}
	statePath, err := ResolveMountStatePath(MountStatePathOptions{
		WorkspaceID:     workspace,
		RemoteRoot:      remoteRoot,
		LocalRoot:       localRoot,
		StateFile:       stateFileOpt,
		StateDir:        opts.StateDir,
		MountKind:       opts.MountKind,
		ValidateOutside: opts.ValidateState,
	})
	if err != nil {
		return nil, err
	}
	stateFile := statePath.StateFile
	publicStatePath := filepath.Join(localRoot, ".relay", "state.json")
	conflictsDir := filepath.Join(localRoot, ".relay", "conflicts")
	resolvedConflictsDir := filepath.Join(conflictsDir, "resolved")
	deadLetterDir := filepath.Join(localRoot, ".relay", "dead-letter")
	outboxDir := filepath.Join(localRoot, ".relay", "outbox")
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
	if opts.ValidateState && !statePath.Override {
		if moved, err := QuarantineLegacyMountState(localRoot, statePath.StateDir); err != nil {
			if opts.Logger != nil {
				opts.Logger.Printf("warning: failed to quarantine legacy private mount state: %v", err)
			}
		} else if len(moved) > 0 && opts.Logger != nil {
			opts.Logger.Printf("quarantined %d legacy private mount state file(s) outside mounted tree", len(moved))
		}
	}
	for _, dir := range []string{outboxDir, filepath.Join(outboxDir, "pending"), filepath.Join(outboxDir, "acked"), filepath.Join(outboxDir, "failed")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	websocketEnabled := true
	if opts.WebSocket != nil {
		websocketEnabled = *opts.WebSocket
	}
	rootCtx := opts.RootCtx
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	fullPullEvery := opts.FullPullEvery
	if fullPullEvery == 0 {
		if raw := strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_FULL_PULL_EVERY")); raw != "" {
			if parsed, perr := strconv.Atoi(raw); perr == nil {
				fullPullEvery = parsed
			} else if opts.Logger != nil {
				opts.Logger.Printf("ignoring invalid RELAYFILE_MOUNT_FULL_PULL_EVERY=%q: %v", raw, perr)
			}
		}
		if fullPullEvery == 0 {
			fullPullEvery = defaultFullPullEvery
		}
	}
	cursorTimeout := resolveDurationEnv(opts.CursorTimeout, "RELAYFILE_CURSOR_TIMEOUT", defaultCursorTimeout, opts.Logger)
	if cursorTimeout <= 0 {
		cursorTimeout = defaultCursorTimeout
	}
	bootstrapTimeout := resolveDurationEnv(opts.BootstrapTimeout, "RELAYFILE_BOOTSTRAP_TIMEOUT", defaultBootstrapTimeout, opts.Logger)
	bootstrapIdleTimeout := resolveDurationEnv(0, "RELAYFILE_BOOTSTRAP_IDLE_TIMEOUT", defaultBootstrapIdleTimeout, opts.Logger)
	if bootstrapIdleTimeout <= 0 {
		bootstrapIdleTimeout = defaultBootstrapIdleTimeout
	}
	bootstrapStallCycles := resolveBootstrapStallCycles(opts.BootstrapStallCycles, opts.Logger)
	exportTimeout := resolveDurationEnv(opts.ExportTimeout, "RELAYFILE_EXPORT_TIMEOUT", defaultExportTimeout, opts.Logger)
	if exportTimeout <= 0 {
		exportTimeout = defaultExportTimeout
	}
	outboxFlushTimeout := resolveDurationEnv(opts.OutboxFlushTimeout, "RELAYFILE_OUTBOX_TIMEOUT", defaultOutboxFlushTimeout, opts.Logger)
	if outboxFlushTimeout <= 0 {
		outboxFlushTimeout = defaultOutboxFlushTimeout
	}
	// The export's own deadline MUST elapse before the bootstrap context can
	// be canceled so a slow export yields to the resumable tree pull while the
	// bootstrap ctx is still alive. Clamp to a fraction of the active bootstrap
	// window so misconfiguration can't let the export outlive either the
	// no-progress watchdog or a hard bootstrap cap (a positive
	// RELAYFILE_BOOTSTRAP_TIMEOUT would otherwise cancel the parent ctx before
	// the export sub-deadline, defeating the same-cycle tree fall-through and
	// re-creating the #1499/#1516 stall loop).
	maxExportTimeout := bootstrapIdleTimeout * 3 / 4
	if bootstrapTimeout > 0 {
		hardCapMax := bootstrapTimeout * 3 / 4
		if maxExportTimeout <= 0 || hardCapMax < maxExportTimeout {
			maxExportTimeout = hardCapMax
		}
	}
	if maxExportTimeout > 0 && exportTimeout > maxExportTimeout {
		if opts.Logger != nil {
			opts.Logger.Printf("clamping exportTimeout from %s to %s (must stay strictly under the active bootstrap window — no-progress watchdog %s, hard cap %s — so the export yields to the resumable tree pull before the bootstrap ctx is canceled)", exportTimeout, maxExportTimeout, bootstrapIdleTimeout, bootstrapTimeout)
		}
		exportTimeout = maxExportTimeout
	}
	readNotReadyTTL := resolveDurationEnv(opts.IncrementalReadNotReadyTTL, "RELAYFILE_INCREMENTAL_READ_NOT_READY_TTL", defaultIncrementalReadNotReadyTTL, opts.Logger)
	if readNotReadyTTL <= 0 {
		readNotReadyTTL = defaultIncrementalReadNotReadyTTL
	}
	forceFullReconcile := false
	if opts.ForceFullReconcile != nil {
		forceFullReconcile = *opts.ForceFullReconcile
	} else if raw := strings.TrimSpace(os.Getenv("RELAYFILE_FORCE_FULL_RECONCILE")); raw != "" {
		if parsed, perr := strconv.ParseBool(raw); perr == nil {
			forceFullReconcile = parsed
		} else if opts.Logger != nil {
			opts.Logger.Printf("ignoring invalid RELAYFILE_FORCE_FULL_RECONCILE=%q: %v", raw, perr)
		}
	}
	lazyRepos := false
	if opts.LazyRepos != nil {
		lazyRepos = *opts.LazyRepos
	} else if raw := strings.TrimSpace(os.Getenv("RELAYFILE_LAZY_REPOS")); raw != "" {
		if parsed, perr := strconv.ParseBool(raw); perr == nil {
			lazyRepos = parsed
		} else if opts.Logger != nil {
			opts.Logger.Printf("ignoring invalid RELAYFILE_LAZY_REPOS=%q: %v", raw, perr)
		}
	} else if raw := strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_LAZY_GITHUB_REPOS")); raw != "" {
		if parsed, perr := strconv.ParseBool(raw); perr == nil {
			lazyRepos = parsed
		} else if opts.Logger != nil {
			opts.Logger.Printf("ignoring invalid RELAYFILE_MOUNT_LAZY_GITHUB_REPOS=%q: %v", raw, perr)
		}
	}
	lazySkipUntrackedPush := true
	if opts.LazySkipUntrackedPush != nil {
		lazySkipUntrackedPush = *opts.LazySkipUntrackedPush
	} else if raw := strings.TrimSpace(os.Getenv("RELAYFILE_LAZY_SKIP_UNTRACKED_PUSH")); raw != "" {
		if parsed, perr := strconv.ParseBool(raw); perr == nil {
			lazySkipUntrackedPush = parsed
		} else if opts.Logger != nil {
			opts.Logger.Printf("ignoring invalid RELAYFILE_LAZY_SKIP_UNTRACKED_PUSH=%q: %v", raw, perr)
		}
	}
	lowMemory := false
	if opts.LowMemory != nil {
		lowMemory = *opts.LowMemory
	} else if raw := strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_LOW_MEMORY")); raw != "" {
		if parsed, perr := strconv.ParseBool(raw); perr == nil {
			lowMemory = parsed
		} else if opts.Logger != nil {
			opts.Logger.Printf("ignoring invalid RELAYFILE_MOUNT_LOW_MEMORY=%q: %v", raw, perr)
		}
	}
	bulkFlushThreshold := defaultBulkFlushThreshold
	if lowMemory {
		bulkFlushThreshold = 16
	}
	var closeScheduler *CloseScheduler
	var rollingCoalescer *RollingDigestCoalescer
	if opts.DigestSource != nil {
		tz, err := digest.ResolveTZ(digest.WorkspaceTZConfig{Timezone: opts.DigestTimezone})
		if err != nil {
			return nil, err
		}
		closeScheduler = &CloseScheduler{
			MountRoot: localRoot,
			TZ:        tz,
			Providers: append([]string(nil), opts.DigestProviders...),
			Source:    opts.DigestSource,
			Now:       opts.DigestNow,
		}
		rollingCoalescer = &RollingDigestCoalescer{
			Interval: 30 * time.Second,
			Now:      opts.DigestNow,
		}
	}
	githubWorkingTree := detectGithubWorkingTreeMount(remoteRoot)
	return &Syncer{
		client:                client,
		workspace:             workspace,
		remoteRoot:            remoteRoot,
		localRoot:             localRoot,
		localDir:              localRoot,
		stateFile:             stateFile,
		publicStatePath:       publicStatePath,
		conflictsDir:          conflictsDir,
		resolvedConflictsDir:  resolvedConflictsDir,
		deadLetterDir:         deadLetterDir,
		outboxDir:             outboxDir,
		eventProvider:         eventProvider,
		scopes:                scopes,
		websocket:             websocketEnabled,
		rootCtx:               rootCtx,
		logger:                opts.Logger,
		denialLogPath:         filepath.Join(localRoot, ".relay", "permissions-denied.log"),
		bulkFlushThreshold:    bulkFlushThreshold,
		mode:                  strings.TrimSpace(opts.Mode),
		writeOnly:             normalizeSyncMode(opts.SyncMode) == "write-only",
		interval:              opts.Interval,
		fullPullEvery:         fullPullEvery,
		cursorTimeout:         cursorTimeout,
		exportTimeout:         exportTimeout,
		outboxFlushTimeout:    outboxFlushTimeout,
		bootstrapTimeout:      bootstrapTimeout,
		bootstrapIdleTimeout:  bootstrapIdleTimeout,
		bootstrapStallCycles:  bootstrapStallCycles,
		readNotReadyTTL:       readNotReadyTTL,
		forceFullReconcile:    forceFullReconcile,
		oversizedLogged:       map[string]struct{}{},
		quarantinedPaths:      map[string]struct{}{},
		lazyRepos:             lazyRepos,
		lazySkipUntrackedPush: lazySkipUntrackedPush,
		lowMemory:             lowMemory,
		layoutRegistrar:       opts.ProviderLayoutRegistrar,
		githubWorkingTree:     githubWorkingTree,
		closeScheduler:        closeScheduler,
		rollingCoalescer:      rollingCoalescer,
		circuit:               NewCloudErrorCircuit(),
		maxOutboxAttempts:     defaultOutboxMaxAttempts,
		nowFn:                 opts.Now,
		state: mountState{
			Files: map[string]trackedFile{},
		},
	}, nil
}

// Circuit returns the cloud-error breaker for tests and status reporters.
func (s *Syncer) Circuit() *CloudErrorCircuit { return s.circuit }

// SetCredentialExpiry records the RFC3339 expiry of the delegated access token
// so it can be surfaced in the public state as credExpiresInSecs. Call this
// from the CLI layer whenever credentials are loaded or refreshed.
func (s *Syncer) SetCredentialExpiry(expiresAt string) {
	s.mu.Lock()
	s.credExpiresAt = strings.TrimSpace(expiresAt)
	s.mu.Unlock()
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

// StaleAliasSkips returns the number of stale provider-layout-alias events the
// most recent reconcile cycle drained without waiting the read-not-ready TTL.
// Operators use this (via `relayfile pull` / `workspace status`) to confirm the
// stuck-event drain is making progress.
func (s *Syncer) StaleAliasSkips() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.staleAliasSkips
}

// BacklogDraining reports whether the events feed still has unprocessed pages
// after the most recent cycle (e.g. the cycle was canceled mid-drain). The CLI
// uses this to tell the operator to re-run or run `writeback skip-stuck`.
func (s *Syncer) BacklogDraining() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.IncrementalBacklogDraining
}

// SkipStuck is the operator escape hatch for a wedged events cursor. It runs a
// reconcile cycle that drops every read-404 ("not readable yet") event
// immediately — without waiting the read-not-ready TTL — so the cursor walks
// forward past consecutive stuck events in one invocation. max bounds the
// number of events dropped (0 = unbounded). It returns the number of stuck
// events skipped along with any reconcile error.
func (s *Syncer) SkipStuck(ctx context.Context, max int) (int, error) {
	s.mu.Lock()
	if s.syncActive {
		s.mu.Unlock()
		return 0, errors.New("sync already in progress")
	}
	s.syncActive = true
	s.skipStuckMode = true
	s.skipStuckMax = max
	s.skipStuckCount = 0
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.skipStuckMode = false
		s.skipStuckMax = 0
		s.syncActive = false
		s.mu.Unlock()
	}()
	err := s.syncReserved(ctx, true)
	s.mu.Lock()
	count := s.skipStuckCount
	s.mu.Unlock()
	return count, err
}

// FlushOutboxOnce uploads only persisted durable outbox records and exits
// without reconciling the local mirror. It is intentionally O(outbox): no
// local tree scan, pushLocal, pullRemote, websocket, or digest work.
func (s *Syncer) FlushOutboxOnce(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.loadState(); err != nil {
		return err
	}
	if err := s.flushOutboxRecords(ctx, nil, true); err != nil {
		s.markSyncError(err)
		_ = s.saveStateWithoutLocalScan()
		return err
	}
	outbox := s.summarizeOutbox()
	if outbox.NeedsAttention > 0 {
		err := fmt.Errorf("outbox needs attention: %d command(s)", outbox.NeedsAttention)
		s.markSyncError(err)
		_ = s.saveStateWithoutLocalScan()
		return err
	}
	if outbox.Pending > 0 {
		err := fmt.Errorf("outbox pending remains: %d command(s)", outbox.Pending)
		s.markSyncError(err)
		_ = s.saveStateWithoutLocalScan()
		return err
	}
	s.markSyncSuccess()
	return s.saveStateWithoutLocalScan()
}

// PushLocalAndFlushOnce ingests pending local writeback drafts with a single
// pushLocal pass, then flushes the durable outbox, and exits — without
// pullRemote, digest, websocket, or a full reconcile cycle.
//
// It is the teardown drain. Local writeback drafts are normally ingested into
// the outbox by the running daemon's sync cycle (watcher + pushLocal). A draft
// written after that daemon's last cycle and just before shutdown — e.g. a
// final fire-and-forget reply right before a one-shot sandbox is torn down — is
// still on disk but not yet in the outbox, so FlushOutboxOnce (outbox-only, no
// local scan) silently drops it. Running pushLocal here, in the fresh cleanup
// process that scans the on-disk mirror, ingests those drafts before flushing.
//
// The local scan is the cost (the same O(tree) work FlushOutboxOnce exists to
// avoid), so callers should invoke this only when pending local writes are
// detected and keep FlushOutboxOnce for the no-pending-writes fast path. Unlike
// a full reconcile it still skips pullRemote/digest/websocket, so it cannot
// reintroduce the pull-side flush-124 stalls.
func (s *Syncer) PushLocalAndFlushOnce(ctx context.Context) error {
	// Same top-of-cycle invariant as syncReserved: pushLocal scans and mutates
	// the local mirror, so refuse to run if the mount root was wiped/clobbered
	// (recovery is gated behind --reset-after-clobber). FlushOutboxOnce skips
	// this because it is outbox-only and never touches the mirror.
	if err := s.assertMountRootInvariant(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.loadState(); err != nil {
		return err
	}
	conflicted, err := s.pushLocal(ctx)
	if err != nil {
		s.markSyncError(err)
		_ = s.saveStateWithoutLocalScan()
		return err
	}
	if err := s.flushOutboxRecords(ctx, conflicted, true); err != nil {
		s.markSyncError(err)
		_ = s.saveStateWithoutLocalScan()
		return err
	}
	outbox := s.summarizeOutbox()
	if outbox.NeedsAttention > 0 {
		err := fmt.Errorf("outbox needs attention: %d command(s)", outbox.NeedsAttention)
		s.markSyncError(err)
		_ = s.saveStateWithoutLocalScan()
		return err
	}
	if outbox.Pending > 0 {
		err := fmt.Errorf("outbox pending remains: %d command(s)", outbox.Pending)
		s.markSyncError(err)
		_ = s.saveStateWithoutLocalScan()
		return err
	}
	s.markSyncSuccess()
	return s.saveState()
}

// HandleLocalChange routes a local filesystem event to the appropriate
// writeback action.
//
// Routing is state-driven (does the file exist on disk?) rather than
// op-driven (what flag did fsnotify deliver?). This is load-bearing on
// macOS and behind several editors that perform atomic save-via-rename
// or end a save with a Chmod:
//
//   - Vim, VSCode, JetBrains, and similar editors typically emit
//     Write → Rename → Chmod or just Rename → Chmod. With per-path
//     debouncing in queueChange, only the *last* op survives the 100ms
//     window. If we routed by op alone, a save ending in Chmod would
//     hit the no-op Chmod branch and silently never queue a writeback.
//
//   - An atomic rename-replace leaves the file present on disk. Routing
//     a Rename op to `pushSingleDelete` would wrongly delete the cloud
//     file even though the user just *saved* it.
//
// Instead: stat the local path, and dispatch by whether the file still
// exists. The downstream handlers (`handleLocalWriteOrCreate`,
// `pushSingleDelete`) hash-check internally and short-circuit if there
// is no actual content change, so spurious events (Chmod-only on an
// unmodified file) do not generate noise on the wire.
func (s *Syncer) HandleLocalChange(ctx context.Context, relativePath string, op fsnotify.Op) error {
	relativePath = filepath.ToSlash(strings.TrimSpace(filepath.Clean(relativePath)))
	if relativePath == "" || relativePath == "." {
		return nil
	}
	if isMountRuntimeRelativePath(relativePath) {
		s.logMountControlPathSkipped(relativePath)
		return nil
	}
	if first := strings.SplitN(relativePath, "/", 2)[0]; reservedTopLevel(first) {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// ObserveChange mutates coalescer state that Due()/MarkFlushed() read
	// under s.mu (runRollingDigestJobsLocked). It must run under the same
	// lock to avoid races while the watcher is processing local churn.
	if s.rollingCoalescer != nil {
		s.rollingCoalescer.ObserveChange(relativePath)
	}
	if err := s.loadState(); err != nil {
		return err
	}

	remotePath := s.localRelativeToRemotePath(relativePath)
	if remotePath == "/" || !isUnderRemoteRoot(s.remoteRoot, remotePath) {
		return nil
	}
	localPath := filepath.Join(s.localDir, filepath.FromSlash(relativePath))

	saveWithStatus := func(run func() error) error {
		if err := run(); err != nil {
			s.markSyncError(err)
			_ = s.saveState()
			return err
		}
		s.markSyncSuccess()
		return s.saveState()
	}

	info, statErr := os.Stat(localPath)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	fileExists := statErr == nil && !info.IsDir()
	isDir := statErr == nil && info.IsDir()

	if isDir {
		return nil
	}

	if !fileExists {
		// File is gone. If we tracked it (i.e. it once lived in the cloud),
		// push the delete. If it never existed cloud-side, ignore — the
		// event is from a transient file (e.g. an editor backup) we do not
		// own. ReadOnly tracked files are reverted, not deleted.
		tracked, exists := s.state.Files[remotePath]
		if !exists {
			return nil
		}
		if tracked.ReadOnly {
			return saveWithStatus(func() error {
				return s.revertReadonlyFile(ctx, remotePath, localPath, tracked, "")
			})
		}
		return saveWithStatus(func() error {
			return s.pushSingleDelete(ctx, remotePath, localPath)
		})
	}

	// File exists. Treat any non-directory event as a potential update —
	// `handleLocalWriteOrCreate` reads the file, hash-checks against the
	// tracked snapshot, and short-circuits if nothing actually changed.
	// This handles Write/Create/Rename(atomic-replace)/Chmod uniformly:
	// the source of truth is the file's content, not the event flag.
	return saveWithStatus(func() error {
		return s.handleLocalWriteOrCreate(ctx, remotePath, localPath)
	})
}

func (s *Syncer) handleLocalWriteOrCreate(ctx context.Context, remotePath, localPath string) error {
	snapshot, err := readLocalSnapshot(localPath, true)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s.pushSingleDelete(ctx, remotePath, localPath)
		}
		return err
	}
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
	tracked = s.trackLocalPathIdentity(remotePath, localPath, tracked)
	// Shared choke point for both push paths: pushLocal (via scanLocalFiles)
	// and the filesystem-watcher path (HandleLocalChange ->
	// handleLocalWriteOrCreate -> pushSingleFile). The watcher path never
	// calls scanLocalFiles, so it cannot rely on snapshot.SkipWriteback —
	// without this check here, a watcher-delivered write/create event for an
	// untracked file under a lazy GitHub repo subtree would push regardless
	// of shouldSkipLazyUntrackedPush. pushLocal already filters these out
	// earlier via scanLocalFiles's SkipWriteback flag (to avoid a full
	// content re-read for large pre-pulled files), so for that path this
	// check is a defense-in-depth no-op and never double-counts the skip.
	if s.shouldSkipLazyUntrackedPush(remotePath) {
		s.logLazyUntrackedPushSkipped(remotePath)
		s.state.Counters.SkippedLazyUntrackedPush++
		return nil, nil
	}
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

	tracked.ContentType = snapshot.ContentType
	tracked.Encoding = normalizeEncoding(snapshot.Encoding)
	tracked.Hash = snapshot.Hash
	tracked.Dirty = true
	tracked.DeletePending = false
	tracked.Denied = false
	tracked.WriteDenied = false
	tracked.DeniedHash = ""
	tracked.ReadOnly = false
	s.state.Files[remotePath] = tracked

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
	// Circuit breaker: while open, refuse writebacks. The local dirty
	// state is preserved (pendingWrite.tracked is unchanged) so the next
	// healthy cycle picks the pending bytes back up.
	if s.circuit != nil && s.circuit.IsOpen() {
		s.logf("writeback flush refused: cloud-error circuit breaker is open; %d file(s) remain pending", len(pending))
		return nil
	}
	for _, pendingWrite := range pending {
		if _, err := s.ensureOutboxRecord(pendingWrite); err != nil {
			return err
		}
	}
	return s.flushDueOutboxRecords(ctx, conflicted)
}

func (s *Syncer) flushDueOutboxRecords(ctx context.Context, conflicted map[string]struct{}) error {
	return s.flushOutboxRecords(ctx, conflicted, false)
}

func (s *Syncer) flushOutboxRecords(ctx context.Context, conflicted map[string]struct{}, forceDue bool) error {
	if s.circuit != nil && s.circuit.IsOpen() {
		return nil
	}
	records, err := s.listPendingOutboxRecords()
	if err != nil {
		return err
	}
	now := s.now().UTC()
	due := make([]outboxRecord, 0, len(records))
	for _, record := range records {
		if record.AttemptCount >= s.maxOutboxAttemptsValue() && !record.NeedsAttention {
			record.NeedsAttention = true
			if err := s.saveOutboxRecord(record); err != nil {
				return err
			}
		}
		if forceDue && !record.NeedsAttention && record.AttemptCount < s.maxOutboxAttemptsValue() {
			due = append(due, record)
			continue
		}
		if s.outboxDue(record, now) {
			due = append(due, record)
		}
	}
	if len(due) == 0 {
		return nil
	}
	// The actual cloud upload runs under its OWN rootCtx-derived deadline, not
	// the inbound per-cycle ctx, so a 15s mirror budget cannot cancel a
	// writeback mid-flight and leave it retrying for minutes (see outboxContext).
	flushCtx, cancel := s.outboxContext(ctx)
	defer cancel()
	for _, chunk := range chunkOutboxRecords(due, maxWritebackBatchBytes()) {
		if err := s.flushOutboxRecordChunk(flushCtx, chunk, conflicted); err != nil {
			return err
		}
	}
	return nil
}

func (s *Syncer) flushOutboxRecordChunk(ctx context.Context, records []outboxRecord, conflicted map[string]struct{}) error {
	var firstErr error
	uploadRecords := make([]outboxRecord, 0, len(records))
	for _, record := range records {
		if isMountRuntimeRemotePath(record.RemotePath) {
			s.logMountControlPathSkipped(strings.TrimPrefix(normalizeRemotePath(record.RemotePath), "/"))
			if err := s.skipOutboxRecord(record, "skipped_control_path"); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		if strings.TrimSpace(record.OpID) != "" {
			if err := s.settleOutboxRecord(ctx, record); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		uploadRecords = append(uploadRecords, record)
	}
	if len(uploadRecords) == 0 {
		return firstErr
	}

	files := outboxRecordsAsBulkFiles(uploadRecords)
	response, err := s.client.WriteFilesBulk(ctx, s.workspace, files)
	if err != nil {
		s.recordCloudFailure(err)
		for _, record := range uploadRecords {
			if incErr := s.incrementOutboxAttempt(record, err); incErr != nil {
				return incErr
			}
		}
		return err
	}
	s.recordCloudSuccess()

	errorsByPath := make(map[string]BulkWriteError, len(response.Errors))
	for _, writeErr := range response.Errors {
		errorsByPath[normalizeRemotePath(writeErr.Path)] = writeErr
	}
	resultsByPath := response.resultsByPath()

	for _, record := range uploadRecords {
		tracked, exists := s.state.Files[record.RemotePath]
		pendingWrite, pendingErr := s.outboxRecordAsPending(record, tracked, exists)
		if pendingErr != nil {
			if firstErr == nil {
				firstErr = pendingErr
			}
			continue
		}
		if writeErr, ok := errorsByPath[record.RemotePath]; ok {
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
			if err == nil {
				if failErr := s.failOutboxRecord(record, writeErr.Message); failErr != nil && firstErr == nil {
					firstErr = failErr
				}
			} else if incErr := s.incrementOutboxAttempt(record, err); incErr != nil && firstErr == nil {
				firstErr = incErr
			}
			continue
		}
		// The cloud accepted this record. Only now claim up-path ownership:
		// watcher noise and per-path bulk errors must leave the point-in-time
		// snapshot authoritative, while an admitted write must not be replayed
		// with older bytes (or inferred absent) when the full pull resumes.
		s.markFullPullUpPath(record.RemotePath)
		if result, ok := resultsByPath[pendingWrite.remotePath]; ok && strings.TrimSpace(result.ContentType) != "" {
			pendingWrite.snapshot.ContentType = result.ContentType
		}
		result := resultsByPath[pendingWrite.remotePath]
		if err := s.reconcileBulkWrite(ctx, pendingWrite, result.Revision); err != nil {
			// The cloud accepted the batch, but old prod may omit per-file
			// results and leave us doing a follow-up ReadFile for revision
			// discovery. Keep the outbox record pending so restart/reconnect
			// can retry with the same contentIdentity instead of silently
			// losing the command.
			if incErr := s.incrementOutboxAttempt(record, err); incErr != nil && firstErr == nil {
				firstErr = incErr
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		revision := result.Revision
		if strings.TrimSpace(revision) == "" {
			revision = s.state.Files[pendingWrite.remotePath].Revision
		}
		record.Revision = revision
		if strings.TrimSpace(response.CorrelationID) != "" {
			record.CorrelationID = strings.TrimSpace(response.CorrelationID)
		}
		record.OpID = strings.TrimSpace(result.OpID)
		if result.Writeback != nil {
			record.DispatchStatus = strings.TrimSpace(result.Writeback.State)
		}
		if record.OpID == "" {
			// Legacy servers only prove upload acceptance. Keep compatibility
			// with v0.8.20-era clouds; v0.8.21+ receipts use opId below.
			if err := s.ackOutboxRecord(record, revision, response.CorrelationID); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := s.saveOutboxRecord(record); err != nil && firstErr == nil {
			firstErr = err
			continue
		}
		if err := s.settleOutboxRecord(ctx, record); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Syncer) settleOutboxRecord(ctx context.Context, record outboxRecord) error {
	op, err := s.client.GetOperation(ctx, s.workspace, record.OpID)
	if err != nil {
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			// The provider dispatch was accepted and returned an opID, but the
			// operation record may be eventually consistent. Keep the durable
			// receipt retryable and poll it on a later cycle instead of turning
			// the first 404 into a permanent receiptless NeedsAttention state.
			return s.incrementOutboxAttempt(record, err)
		}
		s.recordCloudFailure(err)
		return s.incrementOutboxAttempt(record, err)
	}
	s.recordCloudSuccess()

	status := strings.TrimSpace(op.Status)
	if status == "" {
		status = "pending"
	}
	record.DispatchStatus = status
	if strings.TrimSpace(op.OpID) != "" {
		record.OpID = strings.TrimSpace(op.OpID)
	}
	switch status {
	case "succeeded":
		revision := strings.TrimSpace(record.Revision)
		if revision == "" {
			revision = strings.TrimSpace(op.Revision)
		}
		return s.ackOutboxRecord(record, revision, record.CorrelationID)
	case "failed", "dead_lettered", "canceled":
		reason := ""
		if op.LastError != nil && strings.TrimSpace(*op.LastError) != "" {
			reason = strings.TrimSpace(*op.LastError)
		} else {
			reason = fmt.Sprintf("writeback op %s status %s", record.OpID, status)
		}
		if err := s.failOutboxRecord(record, reason); err != nil {
			return err
		}
		return errors.New(reason)
	case "pending", "running", "queued":
		record.LastError = ""
		return s.saveOutboxRecord(record)
	default:
		record.LastError = fmt.Sprintf("writeback op %s status %s", record.OpID, status)
		return s.saveOutboxRecord(record)
	}
}

func chunkOutboxRecords(records []outboxRecord, maxBytes int64) [][]outboxRecord {
	if len(records) == 0 {
		return nil
	}
	if maxBytes <= 0 {
		return [][]outboxRecord{records}
	}
	chunks := make([][]outboxRecord, 0, 1)
	current := make([]outboxRecord, 0, len(records))
	for _, record := range records {
		candidate := append(append([]outboxRecord(nil), current...), record)
		if len(current) > 0 && bulkWriteRequestSize(outboxRecordsAsBulkFiles(candidate)) > maxBytes {
			chunks = append(chunks, append([]outboxRecord(nil), current...))
			current = current[:0]
		}
		current = append(current, record)
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

func outboxRecordsAsBulkFiles(records []outboxRecord) []BulkWriteFile {
	files := make([]BulkWriteFile, 0, len(records))
	for _, record := range records {
		files = append(files, BulkWriteFile{
			Path:            record.RemotePath,
			ContentType:     record.ContentType,
			Content:         record.Content,
			Encoding:        record.Encoding,
			ContentIdentity: outboxRecordContentIdentity(record),
		})
	}
	return files
}

// outboxRecordContentIdentity derives the server-side dedupe key for a durable
// outbox record. Writeback "create draft" paths must dedupe on
// (workspace, path, content hash) — the same identity used by the in-flight
// `bulkWriteFilesForPending` path and by the CLI's direct `relayfile writeback
// push`. Keying those on the per-record commandId instead would mint a second
// idempotency key for identical content, so a direct push racing a mount-daemon
// flush of the same pending receipt could create duplicate provider
// drafts/tickets. Non-draft mount commands keep the commandId identity, which is
// stable across reconnect/restart.
func outboxRecordContentIdentity(record outboxRecord) *ContentIdentity {
	if identity := mountWritebackCreateDraftContentIdentity(record.WorkspaceID, record.RemotePath, record.Hash); identity != nil {
		return identity
	}
	return &ContentIdentity{
		Kind:       "mount-command",
		Key:        record.CommandID,
		TTLSeconds: 7 * 24 * 60 * 60,
	}
}

func bulkWriteFilesForPending(workspaceID string, pending []pendingBulkWrite) []BulkWriteFile {
	files := make([]BulkWriteFile, 0, len(pending))
	for _, pendingWrite := range pending {
		files = append(files, BulkWriteFile{
			Path:            pendingWrite.remotePath,
			ContentType:     pendingWrite.snapshot.ContentType,
			Content:         pendingWrite.snapshot.WireContent,
			Encoding:        pendingWrite.snapshot.Encoding,
			ContentIdentity: mountWritebackCreateDraftContentIdentity(workspaceID, pendingWrite.remotePath, pendingWrite.snapshot.Hash),
		})
	}
	return files
}

func mountWritebackCreateDraftContentIdentity(workspaceID, normalizedRemotePath, contentHash string) *ContentIdentity {
	if !isMountWritebackCreateDraftPath(normalizedRemotePath) {
		return nil
	}
	return newMountWritebackCreateDraftContentIdentity(workspaceID, normalizedRemotePath, contentHash)
}

func isMountWritebackCreateDraftPath(remotePath string) bool {
	if relayfile.IsDraftFilePath(remotePath) {
		return true
	}
	base := path.Base(normalizeRemotePath(remotePath))
	return strings.HasPrefix(base, "factory-create-") && strings.HasSuffix(base, ".json")
}

func newMountWritebackCreateDraftContentIdentity(workspaceID, normalizedRemotePath, contentHash string) *ContentIdentity {
	return &ContentIdentity{
		Kind:       mountWritebackCreateDraftContentIdentityKind,
		Key:        fmt.Sprintf("%s:%s:%s", workspaceID, normalizedRemotePath, contentHash),
		TTLSeconds: mountWritebackCreateDraftContentIdentityTTLSeconds,
	}
}

func chunkPendingBulkWrites(workspaceID string, pending []pendingBulkWrite, maxBytes int64) [][]pendingBulkWrite {
	if len(pending) == 0 {
		return nil
	}
	if maxBytes <= 0 {
		return [][]pendingBulkWrite{pending}
	}
	chunks := make([][]pendingBulkWrite, 0, 1)
	current := make([]pendingBulkWrite, 0, len(pending))
	for _, item := range pending {
		candidate := append(append([]pendingBulkWrite(nil), current...), item)
		if len(current) > 0 && bulkWriteRequestSize(bulkWriteFilesForPending(workspaceID, candidate)) > maxBytes {
			chunks = append(chunks, append([]pendingBulkWrite(nil), current...))
			current = current[:0]
		}
		current = append(current, item)
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

func bulkWriteRequestSize(files []BulkWriteFile) int64 {
	body := struct {
		Files []BulkWriteFile `json:"files"`
	}{Files: files}
	data, err := json.Marshal(body)
	if err != nil {
		return 0
	}
	return int64(len(data))
}

func (s *Syncer) reconcileBulkWrite(ctx context.Context, pendingWrite pendingBulkWrite, revision string) error {
	tracked := pendingWrite.tracked
	contentType := strings.TrimSpace(pendingWrite.snapshot.ContentType)
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
		Revision:          revision,
		ContentType:       tracked.ContentType,
		Encoding:          normalizeEncoding(pendingWrite.snapshot.Encoding),
		Hash:              pendingWrite.snapshot.Hash,
		Dirty:             false,
		LocalRelativePath: tracked.LocalRelativePath,
		ReadOnly:          false,
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
				ContentType:       snapshot.ContentType,
				Hash:              snapshot.Hash,
				LocalRelativePath: tracked.LocalRelativePath,
				WriteDenied:       true,
				DeniedHash:        snapshot.Hash,
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
	if err := s.assertNotMountRoot(localPath); err != nil {
		s.logf("skipping conflict materialization for %s: %v", remotePath, err)
		return nil
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
		Revision:          remoteFile.Revision,
		ContentType:       contentType,
		Encoding:          normalizeEncoding(remoteFile.Encoding),
		Hash:              hashBytes(remoteBytes),
		LocalRelativePath: tracked.LocalRelativePath,
		ReadOnly:          !s.canWritePath(remotePath),
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
	if err := s.assertNotMountRoot(localPath); err != nil {
		s.logf("skipping schema-invalid materialization for %s: %v", remotePath, err)
		return nil
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
		Revision:          remoteFile.Revision,
		ContentType:       contentType,
		Encoding:          normalizeEncoding(remoteFile.Encoding),
		Hash:              hashBytes(remoteBytes),
		LocalRelativePath: tracked.LocalRelativePath,
		ReadOnly:          !s.canWritePath(remotePath),
	}
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
	if err := s.assertNotMountRoot(localPath); err != nil {
		s.logf("skipping readonly revert for %s: %v", remotePath, err)
		return nil
	}
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

	tracked.DeletePending = true
	tracked.Dirty = false
	s.state.Files[remotePath] = tracked

	err := s.client.DeleteFile(ctx, s.workspace, remotePath, tracked.Revision)
	if err != nil {
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			// Idempotent delete success: the remote is already absent, so a
			// snapshot captured before this observation must not restore it.
			s.markFullPullUpPath(remotePath)
			delete(s.state.Files, remotePath)
			return nil
		}
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusForbidden {
			tracked.DeletePending = false
			s.state.Files[remotePath] = tracked
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
	s.markFullPullUpPath(remotePath)
	delete(s.state.Files, remotePath)
	return nil
}

func (s *Syncer) sync(ctx context.Context, forcePoll bool) error {
	s.mu.Lock()
	if s.syncActive {
		s.mu.Unlock()
		return errors.New("sync already in progress")
	}
	s.syncActive = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.syncActive = false
		s.mu.Unlock()
	}()
	return s.syncReserved(ctx, forcePoll)
}

func (s *Syncer) syncReserved(ctx context.Context, forcePoll bool) error {
	// Top-of-cycle invariant: the mount root must exist and be a
	// directory. If a previous cycle, an external process, or a cloud
	// clobber wiped it out, refuse to continue rather than recreating
	// it under the daemon's feet. The recovery path is gated behind an
	// explicit operator acknowledgment (--reset-after-clobber).
	if err := s.assertMountRootInvariant(); err != nil {
		return err
	}
	s.mu.Lock()
	if err := s.loadState(); err != nil {
		s.mu.Unlock()
		return err
	}

	s.mu.Unlock()

	if !s.writeOnly {
		if err := s.MaintainWebSocket(ctx); err != nil {
			s.logf("websocket unavailable; using polling sync: %v", err)
		}
	}

	// Re-acquire lock for the remainder of the sync operation.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markReconcileStarted()
	s.staleAliasSkips = 0
	if err := s.runClosingDigestJobsLocked(ctx); err != nil {
		s.markSyncError(err)
		_ = s.saveState()
		return err
	}
	if err := s.runRollingDigestJobsLocked(ctx); err != nil {
		s.markSyncError(err)
		_ = s.saveState()
		return err
	}

	conflicted := map[string]struct{}{}
	if err := s.flushDueOutboxRecords(ctx, conflicted); err != nil {
		s.markSyncError(err)
		_ = s.saveState()
		return err
	}
	didPoll := false
	if s.writeOnly {
		if !s.state.BootstrapComplete {
			s.markBootstrapComplete()
		}
	} else if !s.state.BootstrapComplete || s.forceFullReconcile {
		previousBootstrapCursor := s.state.BootstrapCursor
		previousBootstrapDirectories := append([]string(nil), s.state.BootstrapDirectories...)
		pullErr := s.pullRemote(ctx, conflicted)
		if stallErr := s.recordBootstrapCycle(previousBootstrapCursor, previousBootstrapDirectories, pullErr); stallErr != nil {
			s.markSyncError(stallErr)
			_ = s.saveState()
			return stallErr
		}
		if pullErr != nil {
			s.markSyncError(pullErr)
			_ = s.saveState()
			return pullErr
		}
		s.bootstrapped = true
		didPoll = true
	}

	conflicted, err := s.pushLocal(ctx)
	if err != nil {
		s.markSyncError(err)
		_ = s.saveState()
		return err
	}

	shouldPoll := !didPoll && (forcePoll || !s.bootstrapped || s.wsConn == nil)
	if shouldPoll && !s.writeOnly {
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

func (s *Syncer) runClosingDigestJobsLocked(ctx context.Context) error {
	if s.closeScheduler == nil {
		return nil
	}
	_, err := s.closeScheduler.Tick(ctx)
	return err
}

func (s *Syncer) runRollingDigestJobsLocked(ctx context.Context) error {
	if s.rollingCoalescer == nil || s.closeScheduler == nil || !s.rollingCoalescer.Due() {
		return nil
	}
	tz := s.closeScheduler.TZ
	if tz == nil {
		tz = time.UTC
	}
	clock := s.closeScheduler.Now
	if clock == nil {
		clock = time.Now
	}
	now := clock()
	if _, err := digest.WriteToday(ctx, s.closeScheduler.MountRoot, s.closeScheduler.Source, s.closeScheduler.Providers, now, tz, digest.BuildOptions{}); err != nil {
		return err
	}
	if _, err := digest.WriteThisWeek(ctx, s.closeScheduler.MountRoot, s.closeScheduler.Source, digest.ThisWeekWindow(now, now, s.closeScheduler.Providers, tz)); err != nil {
		return err
	}
	s.rollingCoalescer.MarkFlushed()
	return nil
}

func (s *Syncer) connectWebSocket(ctx context.Context) error {
	s.mu.Lock()
	if !s.websocketConnectDueLocked(time.Now()) {
		s.mu.Unlock()
		return nil
	}
	s.wsConnecting = true
	generation := s.wsGeneration
	s.mu.Unlock()

	httpClient, ok := s.client.(*HTTPClient)
	if !ok {
		s.finishWebSocketConnectFailure(generation, 0)
		return nil
	}

	wsURL, err := httpClient.websocketURL(s.workspace)
	if err != nil {
		s.finishWebSocketConnectFailure(generation, 0)
		return err
	}
	conn, response, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + httpClient.Token()},
		},
	})
	if err != nil {
		retryAfter := retryAfterFromResponse(response)
		s.finishWebSocketConnectFailure(generation, retryAfter)
		return webSocketDialError{err: err, retryAfter: retryAfter}
	}

	readCtx, cancel := context.WithCancel(s.rootCtx)

	s.mu.Lock()
	if s.wsGeneration != generation || s.wsConn != nil {
		if s.wsGeneration == generation {
			s.wsConnecting = false
		}
		s.mu.Unlock()
		cancel()
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return nil
	}
	s.wsConnecting = false
	s.wsConn = conn
	s.wsCancel = cancel
	s.wsNextAttempt = time.Time{}
	s.wsReconnectFailures = 0
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
		s.markSyncSuccess()
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
		s.markSyncSuccess()
		return s.saveState()
	case "directory.created":
		remotePath := normalizeRemotePath(event.Path)
		if remotePath == "/" || !isUnderRemoteRoot(s.remoteRoot, remotePath) {
			return nil
		}
		provider, _, ok := providerLayoutParts(s.remoteRoot, remotePath)
		if !ok {
			return nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.state.LastEventAt = eventAt
		if err := s.ensureProviderLayout(provider); err != nil {
			return err
		}
		s.markSyncSuccess()
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
	s.scheduleWebSocketReconnectLocked(0)
}

func (s *Syncer) ResetWebSocket() {
	s.mu.Lock()
	conn := s.wsConn
	cancel := s.wsCancel
	s.wsConn = nil
	s.wsCancel = nil
	s.wsNextAttempt = time.Time{}
	s.wsReconnectFailures = 0
	s.wsConnecting = false
	s.wsGeneration++
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
}

func (s *Syncer) MaintainWebSocket(ctx context.Context) error {
	s.mu.Lock()
	needsWS := s.websocketConnectDueLocked(time.Now())
	s.mu.Unlock()
	if !needsWS {
		return nil
	}
	if err := s.connectWebSocket(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Syncer) websocketConnectDueLocked(now time.Time) bool {
	if !s.websocket || s.wsConn != nil || s.wsConnecting {
		return false
	}
	return s.wsNextAttempt.IsZero() || !now.Before(s.wsNextAttempt)
}

func (s *Syncer) scheduleWebSocketReconnectLocked(retryAfter time.Duration) {
	if s.wsConn != nil {
		return
	}
	s.wsReconnectFailures++
	delay := retryAfter
	if delay <= 0 {
		delay = websocketReconnectDelay(s.wsReconnectFailures)
	}
	if delay > defaultWebSocketReconnectMax {
		delay = defaultWebSocketReconnectMax
	}
	s.wsNextAttempt = time.Now().Add(delay)
}

func (s *Syncer) finishWebSocketConnectFailure(
	generation int64,
	retryAfter time.Duration,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wsGeneration != generation {
		return
	}
	s.wsConnecting = false
	s.scheduleWebSocketReconnectLocked(retryAfter)
}

func websocketReconnectDelay(failures int) time.Duration {
	if failures <= 0 {
		failures = 1
	}
	delay := defaultWebSocketReconnectBase
	for i := 1; i < failures; i++ {
		delay *= 2
		if delay >= defaultWebSocketReconnectMax {
			delay = defaultWebSocketReconnectMax
			break
		}
	}
	jitter := time.Duration(time.Now().UnixNano()%int64(defaultWebSocketReconnectJitter*2)) - defaultWebSocketReconnectJitter
	delay += jitter
	if delay < defaultWebSocketReconnectBase {
		return defaultWebSocketReconnectBase
	}
	if delay > defaultWebSocketReconnectMax {
		return defaultWebSocketReconnectMax
	}
	return delay
}

func (s *Syncer) HTTPClient() (*HTTPClient, bool) {
	client, ok := s.client.(*HTTPClient)
	return client, ok
}

// bootstrapProgress carries the watchdog "touch" mechanism back to the
// heavy full-pull loops so they can signal liveness. touch() is a no-op
// in hard-cap mode.
type bootstrapProgress struct {
	last *atomic.Int64
}

func (p bootstrapProgress) touch() {
	if p.last != nil {
		p.last.Store(time.Now().UnixNano())
	}
}

// bootstrapContext returns a context for the heavy one-time bootstrap /
// periodic full-tree pull. It is derived from s.rootCtx (NOT the inbound
// per-cycle ctx) so a tiny RELAYFILE_MOUNT_TIMEOUT cannot starve a large
// initial mirror. Two modes:
//
//   - hard-cap (bootstrapTimeout > 0): WithTimeout(rootCtx, bootstrapTimeout).
//   - progress-extension (default, bootstrapTimeout <= 0): WithCancel +
//     a watchdog goroutine that cancels only if no progress touch() has
//     landed within bootstrapIdleTimeout.
//
// Callers MUST defer the returned CancelFunc on every exit path; doing so
// also tears the watchdog goroutine down (no leak).
func (s *Syncer) bootstrapContext(parent context.Context) (context.Context, context.CancelFunc, bootstrapProgress) {
	_ = parent // intentionally derive from rootCtx, not the per-cycle ctx
	if s.bootstrapTimeout > 0 {
		ctx, cancel := context.WithTimeout(s.rootCtx, s.bootstrapTimeout)
		return ctx, cancel, bootstrapProgress{}
	}
	ctx, cancel := context.WithCancel(s.rootCtx)
	prog := bootstrapProgress{last: &atomic.Int64{}}
	prog.touch()
	idle := s.bootstrapIdleTimeout
	if idle <= 0 {
		idle = defaultBootstrapIdleTimeout
	}
	// Poll at most every 10s, but for short idle windows poll
	// proportionally faster so cancellation lands promptly (and tests
	// stay fast). Never below 10ms.
	pollEvery := 10 * time.Second
	if third := idle / 3; third < pollEvery {
		pollEvery = third
	}
	if pollEvery < 10*time.Millisecond {
		pollEvery = 10 * time.Millisecond
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(pollEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				last := prog.last.Load()
				if last == 0 {
					continue
				}
				if time.Since(time.Unix(0, last)) > idle {
					s.logf("bootstrap watchdog: no progress for %s; cancelling full pull (will resume next cycle)", idle)
					cancel()
					return
				}
			}
		}
	}()
	wrapped := func() {
		close(done)
		cancel()
	}
	return ctx, wrapped, prog
}

// outboxContext returns a deadline for a durable writeback/outbox flush. Like
// bootstrapContext, it is derived from s.rootCtx (NOT the inbound per-cycle
// ctx) so the tiny per-cycle RELAYFILE_MOUNT_TIMEOUT that bounds a mirror cycle
// cannot starve an outbound write — a small Slack/Notion writeback must not
// share, and lose, the same 15s budget as a full-tree pull on a large
// workspace. rootCtx cancellation (process shutdown) still propagates.
//
// Callers MUST defer the returned CancelFunc.
func (s *Syncer) outboxContext(parent context.Context) (context.Context, context.CancelFunc) {
	root := s.rootCtx
	if root == nil {
		// Defensive: NewSyncer always sets rootCtx, but never derive from a nil
		// parent — fall back to the inbound ctx so behaviour degrades to the
		// pre-fix per-cycle deadline rather than panicking.
		root = parent
	}
	timeout := s.outboxFlushTimeout
	if timeout <= 0 {
		timeout = defaultOutboxFlushTimeout
	}
	return context.WithTimeout(root, timeout)
}

// retrySkippedMaterializations revisits remote paths that a prior traversal
// deliberately skipped after a path-local filesystem failure. The retry runs
// before the normal events fast-path, so a quiet workspace still repairs the
// mirror once the local obstruction disappears. Records survive cursor/page
// advancement and process restart until materialization succeeds or the
// remote file is authoritatively gone.
func (s *Syncer) retrySkippedMaterializations(ctx context.Context, conflicted map[string]struct{}) error {
	if len(s.state.SkippedMaterializations) == 0 {
		return nil
	}
	paths := make([]string, 0, len(s.state.SkippedMaterializations))
	for remotePath := range s.state.SkippedMaterializations {
		remotePath = normalizeRemotePath(remotePath)
		if remotePath == "/" || !isUnderRemoteRoot(s.remoteRoot, remotePath) {
			s.clearSkippedMaterialization(remotePath)
			continue
		}
		paths = append(paths, remotePath)
	}
	sort.Strings(paths)
	for _, remotePath := range paths {
		if conflicted != nil {
			if _, skip := conflicted[remotePath]; skip {
				continue
			}
		}
		var file RemoteFile
		var err error
		s.runReservedSyncIO(func() {
			file, err = s.client.ReadFile(ctx, s.workspace, remotePath)
		})
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			var httpErr *HTTPError
			if errors.As(err, &httpErr) {
				switch httpErr.StatusCode {
				case http.StatusNotFound:
					s.logf("clearing skipped local materialization for deleted remote file %s", remotePath)
					s.clearSkippedMaterialization(remotePath)
					continue
				case http.StatusForbidden:
					if markErr := s.markReadDenied(remotePath); markErr != nil {
						return markErr
					}
					s.clearSkippedMaterialization(remotePath)
					continue
				}
			}
			s.recordSkippedMaterialization(remotePath, "retry read", err)
			s.logf("warning: deferred retry read for skipped remote file %s failed: %v; keeping it queued", remotePath, err)
			continue
		}
		if err := s.applyRemoteFile(remotePath, file, conflicted); err != nil {
			return err
		}
		if _, pending := s.state.SkippedMaterializations[remotePath]; !pending {
			s.logf("recovered skipped local materialization for remote file %s", remotePath)
		}
	}
	return nil
}

func (s *Syncer) pullRemote(ctx context.Context, conflicted map[string]struct{}) error {
	if err := s.retrySkippedMaterializations(ctx, conflicted); err != nil {
		return err
	}
	if s.state.EventsCursor != "" && !s.forceFullReconcile {
		// Skip-if-no-events short-circuit. Most reconcile cycles on a
		// quiet workspace have nothing to pull; turning that into a
		// single cheap ListEvents probe avoids the worst-case full-tree
		// fetch (sequential ReadFile per entry) that times out on
		// workspaces with hundreds of files. If the events feed reports
		// no new events since our last cursor, we can usually return
		// immediately. The low-frequency periodic full-pull cadence still
		// bypasses the short-circuit so records written without fs events
		// eventually self-heal.
		//
		// If the events feed itself is unavailable (404) we fall through
		// to the existing incremental/full-pull path, which will hit the
		// 404 again and degrade to the full-tree fetch. That preserves
		// pre-fix behaviour for backends without an events feed.
		// "Trust but verify": every Nth incremental cycle, force a full
		// tree pull regardless of cursor health. This self-heals any stale
		// state caused by cloud-side revision reuse — applyRemoteFile
		// re-hashes content and overwrites when the on-disk hash diverges
		// from what cloud now serves under the same revision identifier.
		// Tracks production failure mode where rev counter rolled back
		// mid-envelope, leaving the daemon and cloud both calling distinct
		// content "rev_96".
		forceFullPull := func(reason string) error {
			s.incrementalCycles = 0
			s.logf("%s", reason)
			// Periodic full pull is the same heavy op as bootstrap — give
			// it the rootCtx-derived bootstrap deadline, not the tiny
			// per-cycle one. The surrounding ListEvents probe above stays
			// on the inbound per-cycle ctx (no latency regression).
			// Bound the bootstrap context cancel to this one operation
			// with a closure so the watchdog is always torn down — even
			// if pullRemoteFull panics — without the defer accumulating
			// across loop iterations. Matches the deferred-cancel pattern
			// used on the post-fast-path full-pull sibling below.
			if err := func() error {
				bctx, bcancel, bprog := s.bootstrapContext(ctx)
				defer bcancel()
				return s.pullRemoteFull(bctx, conflicted, bprog)
			}(); err != nil {
				return err
			}
			// Intentionally leave s.state.EventsCursor unchanged. A naive
			// resolveLatestEventCursor here introduces a race: any remote
			// change committed after pullRemoteFull listed/read the tree
			// but before the cursor resolution would be skipped forever
			// (advanced past). Replaying from the prior cursor is safe —
			// applyRemoteFile is idempotent and will no-op when on-disk
			// content already matches.
			s.state.IncrementalCheckpoint = nil
			s.state.IncrementalBacklogDraining = false
			s.clearAllIncrementalReadNotReady()
			return nil
		}
		feed, err := s.client.ListEvents(ctx, s.workspace, s.eventProvider, s.state.EventsCursor, 1)
		if err == nil && len(feed.Events) == 0 {
			s.state.IncrementalBacklogDraining = false
			if s.fullPullEvery > 0 {
				s.incrementalCycles++
				if s.incrementalCycles >= s.fullPullEvery {
					return forceFullPull(fmt.Sprintf("forcing periodic full tree pull (every %d quiet/incremental cycles) as defense against cloud-side revision reuse and missing events", s.fullPullEvery))
				}
			}
			return nil
		}
		s.incrementalCycles++
		if s.fullPullEvery > 0 && s.incrementalCycles >= s.fullPullEvery {
			return forceFullPull(fmt.Sprintf("forcing periodic full tree pull (every %d quiet/incremental cycles) as defense against cloud-side revision reuse and missing events", s.fullPullEvery))
		}

		nextCursor, err := s.pullRemoteIncremental(ctx, conflicted, s.state.EventsCursor)
		if err == nil {
			s.state.EventsCursor = nextCursor
			return nil
		}
		if strings.TrimSpace(nextCursor) != "" && nextCursor != s.state.EventsCursor {
			s.state.EventsCursor = nextCursor
		}
		var notReadyErr *IncrementalReadNotReadyError
		if errors.As(err, &notReadyErr) {
			return err
		}
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound {
			return err
		}
		s.logf("events feed unavailable; falling back to full pull")
		s.state.EventsCursor = ""
		s.state.IncrementalCheckpoint = nil
		s.state.IncrementalBacklogDraining = false
		s.clearAllIncrementalReadNotReady()
	}

	// Restart fast-path. When EventsCursor is empty but the state file
	// already records tracked files AND a prior LastEventAt — meaning a
	// previous daemon successfully observed events from this workspace
	// — this is a daemon restart against a workspace we have synced
	// before. The full-tree fetch (export or per-file ReadFile loop) on
	// workspaces with hundreds of files routinely exceeds the per-cycle
	// deadline (RELAYFILE_MOUNT_TIMEOUT, default 15s), trapping the
	// daemon in a permanent stall:
	//
	//   mount sync cycle failed: context deadline exceeded
	//   mount stalled: no successful reconcile for 10m
	//
	// Skip the bootstrap full pull: seed the events cursor against the
	// current tip and trust the existing on-disk state. Any drift between
	// local and remote will be caught either by the next incremental
	// cycle (if events fired during downtime) or by the periodic full
	// pull cadence (every fullPullEvery cycles). If resolving the cursor
	// fails — including on backends without an events feed — fall
	// through to the full pull as before so this is purely additive on
	// supported backends.
	//
	// Correctness gate: the restart fast-path may ONLY skip the bootstrap
	// full pull when the workspace has been *completely* mirrored at least
	// once (BootstrapComplete). The previous LastEventAt heuristic let a
	// partially-populated state file (e.g. a clobber remnant, or a state
	// written by an interrupted prior bootstrap) short-circuit the full
	// pull forever, leaving the mirror permanently incomplete
	// (rw_517d60b6). BootstrapComplete is set only by a full-tree/export
	// pull that mirrored the whole remote, so it is the authoritative
	// signal. The escape hatch / clobber-remnant auto-recovery falls out
	// for free: a non-empty Files map with BootstrapComplete=false (or an
	// explicit --full-reconcile) forces the full pull below.
	if len(s.state.Files) > 0 && !s.state.BootstrapComplete {
		if strings.TrimSpace(s.state.BootstrapCursor) != "" {
			s.logf("detected non-empty state without completed bootstrap; will resume from persisted cursor (%d files already synced)", s.state.BootstrapFilesSynced)
		} else {
			s.logf("detected non-empty state without completed bootstrap; forcing full reconcile (%d tracked files)", len(s.state.Files))
		}
	}
	if s.state.BootstrapComplete && !s.forceFullReconcile && len(s.state.Files) > 0 {
		var cursor string
		var err error
		s.runReservedSyncIO(func() {
			cursor, err = s.resolveLatestEventCursor(ctx)
		})
		if err == nil && strings.TrimSpace(cursor) != "" {
			// Only short-circuit when the events feed yielded a real
			// tip. An empty cursor means the feed has no usable
			// watermark (no events, or an always-empty/unusable feed):
			// seeding "" and returning would skip the full pull AND
			// never re-arm the periodic full-pull cadence (which keys
			// off a non-empty EventsCursor), so new remote files would
			// never land. Fall through to the full pull instead — it is
			// idempotent and self-heals. This restores the safety the
			// old LastEventAt gate provided without reintroducing the
			// rw_517d60b6 partial-mirror hazard (still gated on
			// BootstrapComplete).
			s.state.EventsCursor = cursor
			s.state.IncrementalCheckpoint = nil
			s.state.IncrementalBacklogDraining = false
			s.logf("restart fast-path: seeded events cursor %q from %d tracked files; skipping bootstrap full pull", cursor, len(s.state.Files))
			return nil
		}
		if err == nil {
			s.logf("restart fast-path: events feed returned no usable cursor; falling through to full pull")
		}
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			// No events feed on this backend — fall through to the
			// full-pull bootstrap path. (Pre-fix behaviour.)
		} else if isCursorResolutionRetryable(err) {
			// A completed prior bootstrap with tracked files is reusable
			// state. Under load, falling through from a transient cursor
			// timeout to a full export recreates the production stall loop:
			// full export exceeds the bootstrap watchdog, the cursor stays
			// empty, and the next reuse repeats the same expensive path.
			// Surface the cycle failure instead; the daemon will retry the
			// cheap cursor path next reconcile without destroying progress.
			return err
		} else {
			s.logf("restart fast-path: cursor resolution failed (%v); falling through to full pull", err)
		}
	}

	// Bootstrap / full-pull path. Derive the deadline from rootCtx (NOT
	// the inbound per-cycle ctx) so a large initial mirror is not starved
	// by a tiny RELAYFILE_MOUNT_TIMEOUT. resolveLatestEventCursor already
	// owns its own short rootCtx-derived deadline (Step 3) so it is also
	// safe under a tiny inbound ctx.
	bctx, bcancel, bprog := s.bootstrapContext(ctx)
	defer bcancel()
	if err := s.pullRemoteFull(bctx, conflicted, bprog); err != nil {
		return err
	}
	s.state.IncrementalCheckpoint = nil
	s.state.IncrementalBacklogDraining = false
	s.clearAllIncrementalReadNotReady()
	if s.wsConn != nil {
		return nil
	}
	if strings.TrimSpace(s.state.EventsCursor) != "" {
		return nil
	}
	var cursor string
	var err error
	s.runReservedSyncIO(func() {
		cursor, err = s.resolveLatestEventCursor(bctx)
	})
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

func (s *Syncer) pullRemoteFull(ctx context.Context, conflicted map[string]struct{}, prog bootstrapProgress) error {
	// syncActive means this full pull was entered through SyncOnce/Reconcile,
	// where the caller holds mu. Low-level tests also call pullRemoteFull
	// directly without that reservation; keep those calls on the historical
	// non-yielding path so we never unlock a mutex they do not own.
	prioritizeUpPath := s.syncActive
	if prioritizeUpPath {
		s.fullPullActive = true
		s.fullPullUpPaths = make(map[string]struct{})
		defer func() {
			s.fullPullActive = false
			s.fullPullUpPaths = nil
		}()
	}
	if client, ok := s.client.(githubWorkingTreeTarClient); ok {
		used, err := s.pullRemoteFullGithubTarSeed(ctx, client, conflicted, prog)
		if used {
			return err
		}
	}
	if client, ok := s.client.(exportSnapshotClient); ok {
		used, err := s.pullRemoteFullExport(ctx, client, conflicted, prog)
		if used {
			return err
		}
	}
	return s.pullRemoteFullTree(ctx, conflicted, prog)
}

type githubCloneManifest struct {
	HeadSHA       string
	DefaultBranch string
	EventsCursor  string
	EventID       string
	Path          string
}

func (s *Syncer) pullRemoteFullGithubTarSeed(ctx context.Context, client githubWorkingTreeTarClient, conflicted map[string]struct{}, prog bootstrapProgress) (bool, error) {
	if s.githubWorkingTree == nil || s.lazyRepos {
		return false, nil
	}
	var manifest githubCloneManifest
	var err error
	s.runFullPullIO(func() {
		manifest, err = s.readGithubCloneManifest(ctx)
	})
	if err != nil {
		if exportSnapshotUnsupported(err) {
			return false, nil
		}
		s.logf("github tar seed unavailable: read clone manifest failed: %v", err)
		return false, nil
	}
	headSHA := strings.TrimSpace(manifest.HeadSHA)
	if headSHA == "" {
		return false, nil
	}
	s.githubWorkingTree.HeadSHA = headSHA
	s.state.GithubWorkingTreeHeadSHA = headSHA

	cursor := strings.TrimSpace(manifest.EventsCursor)
	if cursor == "" {
		cursor = strings.TrimSpace(manifest.EventID)
	}
	if cursor == "" {
		s.runFullPullIO(func() {
			cursor, err = s.resolveGithubCloneManifestCursor(ctx, manifest)
		})
		if err != nil {
			s.logf("github tar seed unavailable: resolve clone manifest cursor failed: %v", err)
			return false, nil
		}
	}
	if cursor == "" {
		s.logf("github tar seed unavailable: clone manifest has no event cursor")
		return false, nil
	}

	var tree map[string]githubTreeFile
	var maxObservedRevision string
	s.runFullPullIO(func() {
		tree, maxObservedRevision, err = s.githubWorkingTreeSnapshot(ctx, prog)
	})
	if err != nil {
		s.logf("github tar seed unavailable: tree verification snapshot failed: %v", err)
		return false, nil
	}
	if len(tree) == 0 {
		return false, nil
	}

	var tarBody GithubWorkingTreeTar
	s.runFullPullIO(func() {
		tarBody, err = client.ExportGithubWorkingTreeTar(ctx, s.workspace, GithubWorkingTreeSeedRequest{
			Owner:      s.githubWorkingTree.Owner,
			Repo:       s.githubWorkingTree.Repo,
			PathPrefix: s.githubWorkingTree.ContentsRoot,
			HeadSHA:    headSHA,
			Gzip:       false,
		})
	})
	if err != nil {
		if exportSnapshotUnsupported(err) {
			return false, nil
		}
		s.recordCloudFailure(err)
		return true, err
	}
	defer func() {
		s.runFullPullIO(func() { _ = tarBody.Body.Close() })
	}()
	s.recordCloudSuccess()

	remotePaths, err := s.applyGithubWorkingTreeTarSeed(tarBody, tree, conflicted, prog)
	if err != nil {
		return true, err
	}
	if len(remotePaths) != len(tree) {
		return true, fmt.Errorf("github tar seed verification failed: tar contained %d verified files, tree expected %d", len(remotePaths), len(tree))
	}
	if s.snapshotDeleteUnsafe(len(remotePaths)) {
		s.logf("skipping snapshot delete pass (github tar seed): fresh remote tree has %d files but %d are tracked locally (suspected partial/empty cloud listing); preserving local state", len(remotePaths), len(s.state.Files))
		s.markBootstrapComplete()
		s.state.EventsCursor = cursor
		return true, nil
	}
	if err := s.applyRemoteSnapshotDeletesRev(remotePaths, conflicted, maxObservedRevision); err != nil {
		return true, err
	}
	s.markBootstrapComplete()
	s.state.EventsCursor = cursor
	s.state.IncrementalCheckpoint = nil
	s.state.IncrementalBacklogDraining = false
	s.logf("github tar seed complete for %s/%s at %s: %d files, events cursor %q", s.githubWorkingTree.Owner, s.githubWorkingTree.Repo, headSHA, len(remotePaths), cursor)
	return true, nil
}

func (s *Syncer) pullRemoteFullExport(ctx context.Context, client exportSnapshotClient, conflicted map[string]struct{}, prog bootstrapProgress) (bool, error) {
	// Bound the atomic export with its OWN deadline, strictly under the
	// no-progress bootstrap watchdog. ExportFiles is a single HTTP call that
	// reports NO incremental progress until the whole body returns, so on a
	// large/slow/429-throttled workspace it can consume the entire watchdog
	// window and be cancelled with zero files applied — and, being a one-shot
	// mirror with no resume cursor, every retry restarts from scratch and is
	// cancelled again (the #1499/#1516 "non-empty without completed bootstrap
	// -> forcing full reconcile" loop). The sub-deadline makes a doomed export
	// fail EARLY, while the parent bootstrap ctx is still alive, so we can fall
	// through to the resumable, per-page pullRemoteFullTree.
	exportCtx := ctx
	if s.exportTimeout > 0 {
		var cancel context.CancelFunc
		exportCtx, cancel = context.WithTimeout(ctx, s.exportTimeout)
		defer cancel()
	}
	var files []RemoteFile
	var err error
	s.runFullPullIO(func() {
		files, err = client.ExportFiles(exportCtx, s.workspace, s.remoteRoot)
	})
	if err != nil {
		if exportSnapshotUnsupported(err) {
			return false, nil
		}
		// The export's own sub-deadline elapsed (or it was otherwise
		// cancelled) while the PARENT bootstrap ctx is still alive: the atomic
		// export is too slow/contended for this workspace. Yield to the
		// resumable tree pull rather than retrying a doomed full export. If the
		// parent ctx itself is done (the outer watchdog fired), propagate — the
		// next cycle's shorter export sub-deadline fires before the watchdog
		// and converges via the tree path.
		if ctx.Err() == nil && exportCtx.Err() != nil {
			s.logf("export snapshot did not complete within %s; falling back to resumable tree pull", s.exportTimeout)
			return false, nil
		}
		s.recordCloudFailure(err)
		return true, err
	}
	s.recordCloudSuccess()
	sort.Slice(files, func(i, j int) bool {
		return normalizeRemotePath(files[i].Path) < normalizeRemotePath(files[j].Path)
	})
	remotePaths := map[string]struct{}{}
	maxObservedRevision := ""
	for i := range files {
		remotePath := normalizeRemotePath(files[i].Path)
		if remotePath == "/" || !isUnderRemoteRoot(s.remoteRoot, remotePath) {
			continue
		}
		if revisionAdvances(maxObservedRevision, files[i].Revision) {
			maxObservedRevision = files[i].Revision
		}
		// Contract: lazy GitHub repos do not eagerly hydrate per-repo content at startup.
		if s.lazyRepos && isUnderLazyGithubRepoSubtree(s.remoteRoot, remotePath) {
			continue
		}
		if tracked, ok := s.state.Files[remotePath]; ok && tracked.Denied {
			continue
		}
		files[i].Path = remotePath
		if err := s.applyRemoteFile(remotePath, files[i], conflicted); err != nil {
			return true, err
		}
		s.yieldFullPullStateLock()
		prog.touch()
		remotePaths[remotePath] = struct{}{}
		files[i].Content = ""
	}

	// Circuit breaker: an empty or drastically truncated export response
	// (degraded cloud / partial provider listing) would otherwise authorize
	// applyRemoteSnapshotDeletesRev to wipe every locally-mirrored file.
	// Skip the delete pass when the fresh listing is unsafe; the next
	// healthy cycle will reconcile correctly. Mirrors the safeguard in
	// pullRemoteFullTree.
	if s.snapshotDeleteUnsafe(len(remotePaths)) {
		// A 0/drastically-shrunk export for a workspace we KNOW has tracked
		// files is NOT an authoritative mirror (the production empty-200
		// export, #1499/#1516). Do NOT markBootstrapComplete here: that would
		// lock in the stale/empty snapshot and let the restart fast-path skip
		// recovery forever. Fall through to pullRemoteFullTree, whose paginated
		// ListTree re-reads the real content via a DIFFERENT cloud code path
		// and only marks bootstrap complete on a full traversal. If the tree
		// listing is ALSO empty, its own circuit breaker preserves local state
		// without marking complete, so the next cycle retries instead of
		// converging on an empty mirror.
		s.logf("export returned %d files but %d are tracked locally (suspected partial/empty cloud export); falling back to tree pull for an authoritative listing", len(remotePaths), len(s.state.Files))
		return false, nil
	}

	if err := s.applyRemoteSnapshotDeletesRev(remotePaths, conflicted, maxObservedRevision); err != nil {
		return true, err
	}
	// Export is atomic: a successful ExportFiles + apply is a complete
	// one-shot mirror with no resume cursor.
	s.markBootstrapComplete()
	return true, nil
}

func (s *Syncer) readGithubCloneManifest(ctx context.Context) (githubCloneManifest, error) {
	if s.githubWorkingTree == nil {
		return githubCloneManifest{}, fmt.Errorf("not a github working-tree mount")
	}
	paths := []string{s.githubWorkingTree.cloneSentinelPath(), s.githubWorkingTree.legacyMetaPath()}
	var lastErr error
	for _, manifestPath := range paths {
		file, err := s.client.ReadFile(ctx, s.workspace, manifestPath)
		if err != nil {
			var httpErr *HTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
				lastErr = err
				continue
			}
			return githubCloneManifest{}, err
		}
		payload, err := decodeRemoteFileContent(file)
		if err != nil {
			return githubCloneManifest{}, err
		}
		manifest, ok := parseGithubCloneManifest(payload)
		if !ok {
			lastErr = fmt.Errorf("clone manifest %s missing headSha", manifestPath)
			continue
		}
		manifest.Path = manifestPath
		return manifest, nil
	}
	if lastErr != nil {
		return githubCloneManifest{}, lastErr
	}
	return githubCloneManifest{}, &HTTPError{StatusCode: http.StatusNotFound, Code: "not_found", Message: "github clone manifest not found"}
}

func parseGithubCloneManifest(payload []byte) (githubCloneManifest, bool) {
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return githubCloneManifest{}, false
	}
	read := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := raw[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
		return ""
	}
	manifest := githubCloneManifest{
		HeadSHA:       read("headSha", "headSHA", "head_sha"),
		DefaultBranch: read("defaultBranch", "default_branch"),
		EventsCursor:  read("eventsCursor", "events_cursor", "eventCursor", "event_cursor", "fsEventsCursor", "fs_events_cursor", "cursor"),
		EventID:       read("eventId", "eventID", "event_id"),
	}
	return manifest, manifest.HeadSHA != ""
}

func (s *Syncer) resolveGithubCloneManifestCursor(ctx context.Context, manifest githubCloneManifest) (string, error) {
	manifestPath := normalizeRemotePath(manifest.Path)
	if manifestPath == "/" {
		return "", nil
	}
	cursor := ""
	latest := ""
	for {
		feed, err := s.client.ListEvents(ctx, s.workspace, s.eventProvider, cursor, 200)
		if err != nil {
			return "", err
		}
		for _, event := range feed.Events {
			if normalizeRemotePath(event.Path) == manifestPath && strings.TrimSpace(event.EventID) != "" {
				latest = strings.TrimSpace(event.EventID)
			}
		}
		if feed.NextCursor == nil || strings.TrimSpace(*feed.NextCursor) == "" {
			break
		}
		cursor = strings.TrimSpace(*feed.NextCursor)
	}
	return latest, nil
}

type githubTreeFile struct {
	RemotePath  string
	Revision    string
	ContentHash string
	Encoding    string
}

func (s *Syncer) githubWorkingTreeSnapshot(ctx context.Context, prog bootstrapProgress) (map[string]githubTreeFile, string, error) {
	files := map[string]githubTreeFile{}
	cursor := ""
	maxObservedRevision := ""
	for {
		page, err := s.client.ListTree(ctx, s.workspace, s.githubWorkingTree.ContentsRoot, 200, cursor)
		if err != nil {
			return nil, "", err
		}
		s.recordCloudSuccess()
		prog.touch()
		for _, entry := range page.Entries {
			if entry.Type != "file" {
				continue
			}
			if revisionAdvances(maxObservedRevision, entry.Revision) {
				maxObservedRevision = entry.Revision
			}
			if headSHA := strings.TrimSpace(s.githubWorkingTree.HeadSHA); headSHA != "" && !strings.HasSuffix(normalizeRemotePath(entry.Path), "@"+headSHA+".json") {
				continue
			}
			rel, ok := s.githubWorkingTree.remotePathToWorkingTreeRel(entry.Path)
			if !ok {
				continue
			}
			contentHash := strings.TrimSpace(entry.ContentHash)
			if contentHash == "" {
				return nil, "", fmt.Errorf("tree entry %s missing contentHash", entry.Path)
			}
			files[rel] = githubTreeFile{
				RemotePath:  normalizeRemotePath(entry.Path),
				Revision:    entry.Revision,
				ContentHash: contentHash,
				Encoding:    normalizeEncoding(entry.Encoding),
			}
		}
		if page.NextCursor == nil || strings.TrimSpace(*page.NextCursor) == "" {
			break
		}
		cursor = strings.TrimSpace(*page.NextCursor)
	}
	return files, maxObservedRevision, nil
}

func (s *Syncer) applyGithubWorkingTreeTarSeed(tarBody GithubWorkingTreeTar, tree map[string]githubTreeFile, conflicted map[string]struct{}, prog bootstrapProgress) (map[string]struct{}, error) {
	reader := io.Reader(tarBody.Body)
	buffered := bufio.NewReader(reader)
	if strings.Contains(strings.ToLower(tarBody.ContentType), "gzip") {
		var gz *gzip.Reader
		var err error
		s.runFullPullIO(func() {
			gz, err = gzip.NewReader(buffered)
		})
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		reader = gz
	} else {
		reader = buffered
	}
	tr := tar.NewReader(reader)
	remotePaths := map[string]struct{}{}
	seen := map[string]struct{}{}
	for {
		var header *tar.Header
		var err error
		s.runFullPullIO(func() {
			header, err = tr.Next()
		})
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if header == nil || header.FileInfo().IsDir() {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			continue
		}
		rel := filepath.ToSlash(filepath.Clean(strings.TrimSpace(header.Name)))
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" || rel == "." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
			return nil, fmt.Errorf("github tar seed contains unsafe path %q", header.Name)
		}
		if _, ok := seen[rel]; ok {
			return nil, fmt.Errorf("github tar seed contains duplicate file %s", rel)
		}
		seen[rel] = struct{}{}
		meta, ok := tree[rel]
		if !ok {
			return nil, fmt.Errorf("github tar seed contains unexpected file %s", rel)
		}
		if conflicted != nil {
			if _, skip := conflicted[meta.RemotePath]; skip {
				remotePaths[meta.RemotePath] = struct{}{}
				continue
			}
		}
		var data []byte
		s.runFullPullIO(func() {
			data, err = io.ReadAll(tr)
		})
		if err != nil {
			return nil, err
		}
		hash := hashBytes(data)
		if hash != meta.ContentHash {
			return nil, fmt.Errorf("github tar seed contentHash mismatch for %s: tar=%s tree=%s", rel, hash, meta.ContentHash)
		}
		if s.fullPullPathTouchedByUpPath(meta.RemotePath) {
			// The tar/tree pair is a point-in-time snapshot too. A watcher
			// writeback that completed while this body streamed owns the newer
			// bytes; count the remote path as present but never replay the stale
			// archive entry over it.
			remotePaths[meta.RemotePath] = struct{}{}
			s.yieldFullPullStateLock()
			continue
		}
		localPath, err := safeLocalPath(s.localRoot, rel)
		if err != nil {
			return nil, err
		}
		if err := s.assertNotMountRoot(localPath); err != nil {
			return nil, err
		}
		tracked := s.state.Files[meta.RemotePath]
		canWrite := s.canWritePath(meta.RemotePath)
		if tracked.Dirty {
			if err := s.applyLocalPermissions(localPath, canWrite); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			tracked.ReadOnly = !canWrite
			s.state.Files[meta.RemotePath] = tracked
			remotePaths[meta.RemotePath] = struct{}{}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			return nil, err
		}
		shouldWrite := true
		if current, err := os.ReadFile(localPath); err == nil && hashBytes(current) == hash {
			shouldWrite = false
		}
		if shouldWrite {
			if err := writeFileAtomic(localPath, data, 0o644); err != nil {
				return nil, err
			}
		}
		if err := s.applyLocalPermissions(localPath, canWrite); err != nil {
			return nil, err
		}
		contentType := detectContentType(localPath)
		s.state.Files[meta.RemotePath] = trackedFile{
			Revision:    meta.Revision,
			ContentType: contentType,
			Encoding:    meta.Encoding,
			Hash:        hash,
			Dirty:       false,
			Denied:      false,
			ReadOnly:    !canWrite,
		}
		remotePaths[meta.RemotePath] = struct{}{}
		s.yieldFullPullStateLock()
		prog.touch()
	}
	for rel, meta := range tree {
		if _, ok := remotePaths[meta.RemotePath]; !ok {
			return nil, fmt.Errorf("github tar seed missing tree file %s", rel)
		}
	}
	return remotePaths, nil
}

func exportSnapshotUnsupported(err error) bool {
	if exportSnapshotTruncated(err) || exportSnapshotOverloaded(err) {
		return true
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	if httpErr.StatusCode == http.StatusNotFound {
		return true
	}
	// The full-tree export serializes the entire workspace into one body.
	// Large workspaces exceed the cloud export cap, which responds 413 and
	// explicitly directs clients to the paginated tree/read APIs. Fall
	// through to pullRemoteFullTree rather than retrying an export that can
	// never fit.
	if httpErr.StatusCode == http.StatusRequestEntityTooLarge {
		return true
	}
	// HTTP 429 workspace_busy: the WorkspaceDO is persistently busy and doJSON
	// has already exhausted its Retry-After backoff. Retrying the same atomic
	// export keeps contending for the one overloaded invocation; fall through
	// to pullRemoteFullTree, whose paginated ListTree + per-file reads are
	// individually bounded, individually retried, and resume from the persisted
	// cursor instead of restarting the whole export. Other 429 classes (for
	// example global rate limits or queue pressure) are not export-specific and
	// should remain visible to the caller after retries are exhausted.
	if httpErr.StatusCode == http.StatusTooManyRequests && strings.EqualFold(httpErr.Code, "workspace_busy") {
		return true
	}
	return httpErr.StatusCode == http.StatusBadRequest && strings.EqualFold(httpErr.Code, "bad_request")
}

// exportSnapshotOverloaded reports whether err is a cloud Durable Object
// overload (HTTP 5xx with an "overloaded" signal). The single full-tree
// export forces the DO to serialize the entire workspace in one invocation;
// on large workspaces that reliably trips the DO's request-queue/memory
// limits and the cycle spins on the export forever. Treating it as
// "unsupported" lets pullRemoteFull fall through to pullRemoteFullTree, whose
// paginated ListTree + per-file reads are individually bounded and resume
// from the persisted cursor. A bare 5xx without the overload signal is left
// alone so genuinely transient server errors still retry the export.
func exportSnapshotOverloaded(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.StatusCode >= 500 && strings.Contains(strings.ToLower(httpErr.Message), "overloaded") {
			return true
		}
	}
	return strings.Contains(strings.ToLower(err.Error()), "durable object is overloaded")
}

func exportSnapshotTruncated(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return true
	}
	return strings.Contains(err.Error(), "unexpected end of JSON input")
}

func isUnderLazyGithubRepoSubtree(remoteRoot, remotePath string) bool {
	remoteRoot = normalizeRemotePath(remoteRoot)
	remotePath = normalizeRemotePath(remotePath)
	if !isUnderRemoteRoot(remoteRoot, remotePath) {
		return false
	}
	absolute := strings.TrimPrefix(remotePath, "/")
	return isLazyGithubRepoSubtreePath(absolute)
}

func isLazyGithubRepoSubtreePath(path string) bool {
	if path == "" {
		return false
	}
	segments := strings.Split(path, "/")
	for i := 0; i+1 < len(segments); i++ {
		if segments[i] == "github" && segments[i+1] == "repos" {
			return len(segments[i:]) >= 5
		}
	}
	return false
}

func (s *Syncer) pullRemoteFullTree(ctx context.Context, conflicted map[string]struct{}, prog bootstrapProgress) (returnErr error) {
	metrics := fullTreeTraversalMetrics{startedAt: time.Now()}
	defer func() {
		s.logf(
			"mount full-tree traversal summary remote_root=%q list_calls=%d entries_seen=%d files_seen=%d directories_seen=%d bytes_seen=%d runtime_entries_seen=%d runtime_subtrees_pruned=%d traversal_complete=%t traversal_failed=%t duration_ms=%d",
			s.remoteRoot,
			metrics.listCalls,
			metrics.entriesSeen,
			metrics.filesSeen,
			metrics.directoriesSeen,
			metrics.bytesSeen,
			metrics.runtimeEntriesSeen,
			metrics.runtimeSubtreesPruned,
			metrics.traversalComplete,
			returnErr != nil,
			time.Since(metrics.startedAt).Milliseconds(),
		)
	}()
	remotePaths := map[string]struct{}{}
	// Traverse in bounded-depth chunks. A depth=200 ListTree request makes the
	// server enumerate every descendant before the client can reject reserved
	// runtime paths. A small fixed depth sees a nested .relay directory before
	// it reaches deep outbox/acked/mountcmd_* leaves, while advancing through a
	// representative Slack tree in a few requests rather than one per directory.
	directories := []string{s.remoteRoot}
	cursor := ""
	startedFromEmpty := true
	if !s.state.BootstrapComplete && len(s.state.BootstrapDirectories) > 0 {
		resumedDirectories := make([]string, 0, len(s.state.BootstrapDirectories))
		for _, directory := range s.state.BootstrapDirectories {
			directory = normalizeRemotePath(directory)
			if !isUnderRemoteRoot(s.remoteRoot, directory) || isMountRuntimeRemotePath(directory) {
				continue
			}
			resumedDirectories = append(resumedDirectories, directory)
		}
		if len(resumedDirectories) > 0 {
			directories = resumedDirectories
			cursor = strings.TrimSpace(s.state.BootstrapCursor)
			startedFromEmpty = false
			s.logf("resuming bootstrap bounded-tree pull at %s from persisted cursor (%d directories pending, %d files already synced)", directories[0], len(directories), s.state.BootstrapFilesSynced)
		}
	} else if !s.state.BootstrapComplete && strings.TrimSpace(s.state.BootstrapCursor) != "" {
		// v0.10.28 and older persisted a cursor into one depth=200 listing.
		// That cursor is invalid for a bounded-depth directory page, so restart from
		// the root. The local-hash fast path avoids re-reading unchanged files.
		s.logf("discarding legacy deep-tree bootstrap cursor and restarting bounded traversal from %s", s.remoteRoot)
		s.state.BootstrapCursor = ""
	}
	queuedDirectories := make(map[string]struct{}, len(directories))
	for _, directory := range directories {
		queuedDirectories[directory] = struct{}{}
	}
	if s.state.BootstrapStartedAt == "" {
		s.state.BootstrapStartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	persistTraversal := func(filesThisPage int) error {
		if s.state.BootstrapComplete {
			return nil
		}
		s.state.BootstrapDirectories = append([]string(nil), directories...)
		s.state.BootstrapCursor = cursor
		s.state.BootstrapFilesSynced += filesThisPage
		prog.touch()
		if err := s.saveState(); err != nil {
			return err
		}
		prog.touch()
		return nil
	}
	maxObservedRevision := ""
	var transientBootstrapAbort bool
	prunedRuntimeRoots := map[string]struct{}{}
	for len(directories) > 0 {
		currentDirectory := directories[0]
		var page TreeResponse
		var err error
		s.runFullPullIO(func() {
			page, err = s.client.ListTree(ctx, s.workspace, currentDirectory, fullTreeTraversalDepth, cursor)
		})
		metrics.listCalls++
		if err != nil {
			s.recordCloudFailure(err)
			return err
		}
		s.recordCloudSuccess()
		prog.touch()
		filesThisPage := 0
		readJobs := make([]bootstrapReadJob, 0, len(page.Entries))
		for _, entry := range page.Entries {
			remotePath := normalizeRemotePath(entry.Path)
			metrics.entriesSeen++
			switch entry.Type {
			case "file":
				metrics.filesSeen++
				if entry.Size > 0 {
					metrics.bytesSeen += entry.Size
				}
			case "dir":
				metrics.directoriesSeen++
			}
			runtimeRoot := mountRuntimeRemoteRoot(remotePath)
			if runtimeRoot != "" {
				metrics.runtimeEntriesSeen++
			}
			if !isUnderRemoteRoot(s.remoteRoot, remotePath) {
				continue
			}
			if runtimeRoot != "" {
				if _, pruned := prunedRuntimeRoots[runtimeRoot]; !pruned {
					prunedRuntimeRoots[runtimeRoot] = struct{}{}
					metrics.runtimeSubtreesPruned++
					s.skipMountRuntimeRemotePath(runtimeRoot, conflicted)
				}
				continue
			}
			// Contract: lazy GitHub repos do not eagerly hydrate per-repo content at startup.
			if s.lazyRepos && isUnderLazyGithubRepoSubtree(s.remoteRoot, remotePath) {
				continue
			}
			if entry.Type == "dir" {
				if remoteDescendantDepth(currentDirectory, remotePath) >= fullTreeTraversalDepth {
					if _, exists := queuedDirectories[remotePath]; exists {
						continue
					}
					queuedDirectories[remotePath] = struct{}{}
					directories = append(directories, remotePath)
				}
				continue
			}
			if entry.Type != "file" {
				continue
			}
			if revisionAdvances(maxObservedRevision, entry.Revision) {
				maxObservedRevision = entry.Revision
			}
			if tracked, ok := s.state.Files[remotePath]; ok && tracked.Denied {
				continue
			}
			// Defensive logging for cloud-side revision reuse: if the tree
			// entry surfaces a content hash that diverges from what we
			// tracked under the same revision, that's the production bug
			// signature. applyRemoteFile already re-hashes content and
			// overwrites local state when hashes diverge, so this is
			// purely diagnostic — treat as changed (i.e. re-fetch, which
			// the loop already does unconditionally).
			if entry.ContentHash != "" {
				if tracked, ok := s.state.Files[remotePath]; ok &&
					tracked.Revision == entry.Revision &&
					tracked.Hash != "" &&
					tracked.Hash != entry.ContentHash {
					s.logf("tree revision %s reused for %s with divergent content hash (tracked=%s remote=%s); refetching", entry.Revision, remotePath, tracked.Hash, entry.ContentHash)
				}
			}
			skipped, err := s.trySkipBootstrapRead(remotePath, entry)
			if err != nil {
				return err
			}
			if skipped {
				prog.touch()
				remotePaths[remotePath] = struct{}{}
				filesThisPage++
				continue
			}
			// Skip paths quarantined in a prior cycle due to a file/directory
			// name collision. Treat as present so the snapshot delete pass does
			// not remove any locally-mirrored copy. The quarantine is cleared by
			// markBootstrapComplete so a fixed adapter gets a clean slate.
			if detail, quarantined := s.state.QuarantinedPaths[remotePath]; quarantined {
				s.logf("skipping quarantined path %s (%s); fix is adapter-side", remotePath, detail)
				remotePaths[remotePath] = struct{}{}
				filesThisPage++
				continue
			}
			readJobs = append(readJobs, bootstrapReadJob{
				Index:      len(readJobs),
				RemotePath: remotePath,
			})
		}
		var readResults []bootstrapReadResult
		s.runFullPullIO(func() {
			readResults = s.readBootstrapFiles(ctx, readJobs, prog)
		})
		for _, result := range readResults {
			if result.Err != nil {
				// Context canceled/deadline exceeded — propagate; the cycle is
				// being torn down and the next one will resume from the cursor.
				if ctx.Err() != nil {
					return result.Err
				}
				var httpErr *HTTPError
				if errors.As(result.Err, &httpErr) {
					if httpErr.StatusCode == http.StatusForbidden {
						s.logf("skipping denied file: %s", result.RemotePath)
						if markErr := s.markReadDenied(result.RemotePath); markErr != nil {
							return markErr
						}
						continue
					}
					// Transient HTTP error (503, 429, etc.): stop the current
					// page immediately without advancing the cursor. On a full
					// bootstrap (startedFromEmpty) this guarantees the failing
					// path is retried next cycle rather than permanently skipped.
					// On a resumed traversal (startedFromEmpty=false) the cursor
					// already points past this page, so stopping here is still
					// safe — the delete pass is skipped on resumed cycles anyway.
					s.logf("transient error reading %s (status %d); aborting page without advancing cursor so it is retried next cycle: %v", result.RemotePath, httpErr.StatusCode, result.Err)
					transientBootstrapAbort = true
					// Preserve any previously-synced files processed on this page
					// before the abort so the delete pass (if it runs) does not
					// remove local copies that are still on the server.
					if _, prevSynced := s.state.Files[result.RemotePath]; prevSynced {
						remotePaths[result.RemotePath] = struct{}{}
					}
					break
				}
				return result.Err
			}
			if err := s.applyRemoteFile(result.RemotePath, result.File, conflicted); err != nil {
				return err
			}
			s.yieldFullPullStateLock()
			prog.touch()
			remotePaths[result.RemotePath] = struct{}{}
			filesThisPage++
		}
		// A transient read error stopped the page early. Do not advance the
		// cursor so the same page (including the failing path) is retried next
		// cycle. Save state up to the last successfully-committed cursor.
		if transientBootstrapAbort {
			if err := persistTraversal(filesThisPage); err != nil {
				return err
			}
			break
		}
		if page.NextCursor != nil && *page.NextCursor != "" {
			cursor = *page.NextCursor
			if err := persistTraversal(filesThisPage); err != nil {
				return err
			}
			continue
		}
		// This directory is exhausted. Persist the remaining queue before
		// requesting its next member so cancellation resumes at a directory
		// boundary instead of restarting the root.
		directories = directories[1:]
		cursor = ""
		if len(directories) > 0 {
			if err := persistTraversal(filesThisPage); err != nil {
				return err
			}
			continue
		}
		metrics.traversalComplete = true
	}

	// A transient read error stopped the page scan early. Bootstrap is not
	// complete; the cursor is left at the page boundary so the next cycle
	// retries the failing paths. Skip the delete pass entirely.
	if transientBootstrapAbort {
		s.logf("bootstrap paused due to transient read error(s); will resume from last cursor next cycle")
		return nil
	}

	// Resumed/partial traversal safety: only run the authoritative
	// snapshot delete pass when this process traversed the ENTIRE tree
	// (started from the root with an empty directory queue). If the
	// traversal began from a persisted directory/cursor boundary, remotePaths only
	// covers the tail of the tree, so deleting "everything not in
	// remotePaths" would wipe the already-mirrored prefix — exactly the
	// #164/#165 clobber failure mode. Skip the delete pass this cycle;
	// the next full cycle (fresh empty-cursor traversal) does the
	// authoritative delete.
	if !startedFromEmpty {
		s.logf("skipping snapshot delete pass: bootstrap resumed from a persisted cursor so the fresh listing is partial; deferring deletes to the next full cycle")
		// Bootstrap is now complete (we reached the end of the tree),
		// just not authoritative for deletes this cycle.
		s.markBootstrapComplete()
		return nil
	}

	// Circuit breaker: a cloud OOM / 5xx storm can return a successful but
	// empty or drastically truncated tree. Treating that as authoritative
	// would delete every locally-mirrored file. If we have a meaningful
	// amount of tracked state but the fresh listing came back empty (or
	// shrank past the safety ratio), skip the delete pass and leave local
	// state intact — the next healthy cycle will reconcile correctly.
	if s.snapshotDeleteUnsafe(len(remotePaths)) {
		s.state.Counters.SnapshotDeleteBlocked++
		s.logf("skipping snapshot delete pass: fresh remote tree has %d files but %d are tracked locally (suspected partial/empty cloud listing); preserving local state", len(remotePaths), len(s.state.Files))
		return nil
	}

	if err := s.applyRemoteSnapshotDeletesRev(remotePaths, conflicted, maxObservedRevision); err != nil {
		return err
	}
	s.markBootstrapComplete()
	return nil
}

type fullTreeTraversalMetrics struct {
	startedAt             time.Time
	listCalls             int
	entriesSeen           int
	filesSeen             int
	directoriesSeen       int
	bytesSeen             int64
	runtimeEntriesSeen    int
	runtimeSubtreesPruned int
	traversalComplete     bool
}

type bootstrapReadJob struct {
	Index      int
	RemotePath string
}

type bootstrapReadResult struct {
	Index      int
	RemotePath string
	File       RemoteFile
	Err        error
}

func (s *Syncer) trySkipBootstrapRead(remotePath string, entry TreeEntry) (bool, error) {
	if strings.TrimSpace(entry.ContentHash) == "" {
		return false, nil
	}
	tracked, ok := s.state.Files[remotePath]
	if ok {
		if tracked.Dirty || tracked.Denied {
			return false, nil
		}
	}
	localPath, err := s.remoteToLocalPath(remotePath)
	if err != nil {
		return false, nil
	}
	if err := s.assertNotMountRoot(localPath); err != nil {
		s.logf("skipping local hash probe for %s: %v", remotePath, err)
		return false, nil
	}
	snapshot, err := readLocalSnapshot(localPath, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		s.logf("local hash probe failed for %s (%s): %v", remotePath, localPath, err)
		return false, nil
	}
	if snapshot.Hash != entry.ContentHash {
		return false, nil
	}
	canWrite := s.canWritePath(remotePath)
	if err := s.applyLocalPermissions(localPath, canWrite); err != nil {
		if s.skipPathLocalMaterializationError(remotePath, "permission update", err) {
			return true, nil
		}
		return false, err
	}
	s.state.Files[remotePath] = trackedFile{
		Revision:          entry.Revision,
		ContentType:       snapshot.ContentType,
		Hash:              snapshot.Hash,
		Dirty:             false,
		LocalRelativePath: tracked.LocalRelativePath,
		Denied:            false,
		ReadOnly:          !canWrite,
	}
	s.clearSkippedMaterialization(remotePath)
	return true, nil
}

func (s *Syncer) readBootstrapFiles(ctx context.Context, jobs []bootstrapReadJob, prog bootstrapProgress) []bootstrapReadResult {
	if len(jobs) == 0 {
		return nil
	}
	workers := bootstrapReadWorkers()
	if workers > len(jobs) {
		workers = len(jobs)
	}
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobCh := make(chan bootstrapReadJob, len(jobs))
	for _, job := range jobs {
		jobCh <- job
	}
	close(jobCh)

	resultCh := make(chan bootstrapReadResult, len(jobs))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobCh {
				file, err := s.client.ReadFile(readCtx, s.workspace, job.RemotePath)
				if err == nil {
					prog.touch()
				}
				resultCh <- bootstrapReadResult{
					Index:      job.Index,
					RemotePath: job.RemotePath,
					File:       file,
					Err:        err,
				}
			}
		}()
	}
	wg.Wait()
	close(resultCh)

	results := make([]bootstrapReadResult, 0, len(jobs))
	for result := range resultCh {
		results = append(results, result)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Index < results[j].Index })
	return results
}

func bootstrapReadWorkers() int {
	raw := strings.TrimSpace(os.Getenv("RELAYFILE_BOOTSTRAP_READ_CONCURRENCY"))
	if raw == "" {
		return defaultBootstrapReadWorkers
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return defaultBootstrapReadWorkers
	}
	if v > 64 {
		return 64
	}
	return v
}

// markBootstrapComplete records that a full-tree (or export) pull has
// fully mirrored the remote at least once. Completion is set ONLY here
// (and the export path) — never in markSyncSuccess — so the fast-path
// gate (Step 5) cannot be satisfied by a partial pull.
func (s *Syncer) markBootstrapComplete() {
	s.state.BootstrapComplete = true
	s.state.BootstrapDirectories = nil
	s.state.BootstrapCursor = ""
	s.state.BootstrapStartedAt = ""
	s.state.BootstrapFilesSynced = 0
	s.state.BootstrapFilesTotal = 0
	s.state.BootstrapStallCycles = 0
	// Clear persisted quarantine so a fixed adapter gets a clean slate.
	s.state.QuarantinedPaths = nil
	s.clearAllIncrementalReadNotReady()
	// One-shot escape hatch / clobber-remnant recovery: after a single
	// successful full reconcile, clear the in-memory force flag so
	// subsequent cycles can use the fast-path again.
	s.forceFullReconcile = false
}

// bootstrapCheckpointAdvanced reports whether this cycle moved the persisted
// resumable traversal checkpoint. Directory queue changes matter just as much
// as a page cursor change: bounded-depth traversals can finish a directory and
// begin the next one with an empty page cursor.
func (s *Syncer) bootstrapCheckpointAdvanced(previousCursor string, previousDirectories []string) bool {
	if strings.TrimSpace(s.state.BootstrapCursor) != strings.TrimSpace(previousCursor) {
		return true
	}
	if len(s.state.BootstrapDirectories) != len(previousDirectories) {
		return true
	}
	for i, directory := range s.state.BootstrapDirectories {
		if directory != previousDirectories[i] {
			return true
		}
	}
	return false
}

// recordBootstrapCycle updates the persisted bootstrap stall guard after a
// bootstrap attempt. It resets only when the traversal checkpoint advances or
// completes, so retries across process restarts cannot spin forever at the
// same cursor. At the configured limit it returns a typed hard failure and
// intentionally leaves BootstrapComplete false.
func (s *Syncer) recordBootstrapCycle(previousCursor string, previousDirectories []string, cause error) error {
	if s.state.BootstrapComplete || s.bootstrapCheckpointAdvanced(previousCursor, previousDirectories) {
		s.state.BootstrapStallCycles = 0
		return nil
	}
	// Root-context cancellation means this runner is intentionally stopping,
	// not that the remote checkpoint has made a retryable failed attempt. Keep
	// the persisted count intact so shutdown/restart does not consume a stall
	// budget. A deadline remains a real failed attempt and continues below.
	if errors.Is(cause, context.Canceled) {
		return nil
	}
	s.state.BootstrapStallCycles++
	limit := s.bootstrapStallCycles
	if limit <= 0 {
		limit = defaultBootstrapStallCycles
	}
	if s.state.BootstrapStallCycles < limit {
		return nil
	}
	return &BootstrapStalledError{
		Cycles: s.state.BootstrapStallCycles,
		Limit:  limit,
		Cursor: strings.TrimSpace(s.state.BootstrapCursor),
		Cause:  cause,
	}
}

// snapshotDeleteUnsafe reports whether running snapshot-driven deletes is
// unsafe given how many files the fresh remote listing returned versus how
// many we currently track. It guards against a degraded cloud response
// (empty or drastically truncated tree) wiping the local mirror.
func (s *Syncer) snapshotDeleteUnsafe(remoteCount int) bool {
	tracked := s.reconcilableTrackedFileCount()
	if tracked == 0 {
		return false
	}
	// Empty fresh listing while we track files is the classic OOM/500
	// signature — never delete on that basis.
	if remoteCount == 0 {
		return true
	}
	// Configurable floor for "drastic shrink". By default, refuse the
	// delete pass if the fresh listing dropped to less than 50% of tracked
	// files while tracking a non-trivial number of files.
	const minTrackedForRatioCheck = 10
	ratio := snapshotDeleteMinRatio()
	if tracked >= minTrackedForRatioCheck && float64(remoteCount) < float64(tracked)*ratio {
		return true
	}
	return false
}

func (s *Syncer) reconcilableTrackedFileCount() int {
	tracked := 0
	for _, file := range s.state.Files {
		// Keep this baseline aligned with applyRemoteDelete: these states
		// do not result in snapshot-driven local deletes, so they should
		// not make a filtered remotePaths listing look unsafe.
		if file.Denied || file.WriteDenied || file.Dirty {
			continue
		}
		tracked++
	}
	return tracked
}

// snapshotDeleteMinRatio is the minimum fraction of tracked files the fresh
// remote listing must contain before snapshot deletes are allowed. Tunable
// via RELAYFILE_SNAPSHOT_DELETE_MIN_RATIO (clamped to (0,1]); defaults to 0.5.
func snapshotDeleteMinRatio() float64 {
	const def = 0.5
	raw := strings.TrimSpace(os.Getenv("RELAYFILE_SNAPSHOT_DELETE_MIN_RATIO"))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 || v > 1 {
		return def
	}
	return v
}

// recordCloudFailure feeds the cloud-error circuit when a remote call
// returns a 5xx, gateway timeout, or transport-level reset. Non-failure
// errors (e.g. 404 not found, 401 unauthorized, 4xx contract violations)
// do NOT count — they indicate logical state, not a cloud outage.
func (s *Syncer) recordCloudFailure(err error) {
	if s.circuit == nil || err == nil {
		return
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		if IsCloudFailureStatus(httpErr.StatusCode) {
			if s.circuit.RecordFailure() {
				s.logf("cloud-error circuit breaker opened: %d failures within window", s.circuit.Snapshot().Failures)
			}
		}
		return
	}
	if IsCloudFailureError(err) {
		if s.circuit.RecordFailure() {
			s.logf("cloud-error circuit breaker opened: transport failure %v", err)
		}
	}
}

// recordCloudSuccess feeds the breaker on a successful remote call.
func (s *Syncer) recordCloudSuccess() {
	if s.circuit == nil {
		return
	}
	s.circuit.RecordSuccess()
}

// defaultMaxWritebackBytes caps the size of a local file eligible for
// writeback. The clobber incident involved an ~11MB file renamed over the
// mount root; 8MB is a generous text/document ceiling that keeps such
// pathological payloads out of the sync pipeline by default.
const defaultMaxWritebackBytes int64 = 8 << 20

// defaultMaxWritebackBatchBytes caps the serialized /fs/bulk request body.
// The cloud rejects requests above roughly 10 MiB; 8 MiB leaves room for
// transport and schema overhead while still batching normal writebacks.
const defaultMaxWritebackBatchBytes int64 = 8 << 20

// maxWritebackBytes returns the writeback body size cap in bytes.
// Configurable via RELAYFILE_MAX_WRITEBACK_BYTES (positive integer).
// A value of 0 or a negative override disables the cap.
func maxWritebackBytes() int64 {
	raw := strings.TrimSpace(os.Getenv("RELAYFILE_MAX_WRITEBACK_BYTES"))
	if raw == "" {
		return defaultMaxWritebackBytes
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return defaultMaxWritebackBytes
	}
	if v <= 0 {
		return 0
	}
	return v
}

func maxWritebackBatchBytes() int64 {
	raw := strings.TrimSpace(os.Getenv("RELAYFILE_MAX_WRITEBACK_BATCH_BYTES"))
	if raw == "" {
		return defaultMaxWritebackBatchBytes
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return defaultMaxWritebackBatchBytes
	}
	if v <= 0 {
		return 0
	}
	return v
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
	if err := s.materializeProviderLayouts(remoteFiles); err != nil {
		return err
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

func (s *Syncer) applyRemoteSnapshotDeletes(remotePaths map[string]struct{}, conflicted map[string]struct{}) error {
	return s.applyRemoteSnapshotDeletesRev(remotePaths, conflicted, "")
}

// applyRemoteSnapshotDeletesRev is the revision-gated, tombstone-protected
// snapshot delete pass. observedRevision is the maximum revision seen
// across the fresh listing (empty string means "unknown / no advancement
// signal" — destructive deletes are then refused even after tombstone
// confirmation).
//
// Protocol:
//  1. Refuse the destructive pass entirely when the cloud-error circuit
//     is open (read-only mirror remains; the next healthy cycle catches
//     up). Layout materialization is still safe to run.
//  2. Treat the first complete bootstrap observation with no revision as a
//     non-destructive no-op. This lets the caller persist bootstrap completion
//     without creating or confirming tombstones.
//  3. Refuse when the observedRevision does not strictly advance past
//     state.LastAppliedRevision — an older or equal listing must not
//     authorize deletes.
//  4. For every tracked path missing from the fresh listing, write or
//     confirm a tombstone under .relay/pending-deletes. Only the second
//     consecutive confirmation actually deletes; the first observation
//     is recorded and skipped.
//  5. After the pass, prune tombstones for paths that have reappeared.
//  6. On a clean pass, advance state.LastAppliedRevision.
func (s *Syncer) applyRemoteSnapshotDeletesRev(remotePaths map[string]struct{}, conflicted map[string]struct{}, observedRevision string) error {
	if err := s.materializeProviderLayoutsFromPaths(remotePaths); err != nil {
		return err
	}

	// Circuit breaker: while open, refuse destructive ops entirely.
	if s.circuit != nil && s.circuit.IsOpen() {
		s.state.Counters.SnapshotDeleteBlocked++
		s.logf("snapshot delete pass refused: cloud-error circuit breaker is open; %d tracked files preserved", len(s.state.Files))
		return nil
	}

	// The first completed bootstrap traversal may legitimately have no
	// revision. Let the caller record that completion, but do not allow this
	// unversioned observation to enter tombstone creation or confirmation.
	// BootstrapComplete is persisted immediately after the successful
	// traversal, so later unversioned observations fall through to the normal
	// revision gate and are counted as blocked.
	if strings.TrimSpace(observedRevision) == "" &&
		strings.TrimSpace(s.state.LastAppliedRevision) == "" &&
		!s.state.BootstrapComplete {
		s.logf("snapshot delete pass skipped: first bootstrap observation has no revision; destructive reconciliation deferred")
		return nil
	}

	// Revision gate: refuse to act on a listing that does not strictly
	// advance the highest-applied revision.
	if !revisionAdvances(s.state.LastAppliedRevision, observedRevision) {
		s.state.Counters.SnapshotDeleteBlocked++
		s.logf("snapshot delete pass refused: observed revision %q does not advance past last applied %q",
			observedRevision, s.state.LastAppliedRevision)
		return nil
	}

	statePaths := make([]string, 0, len(s.state.Files))
	for remotePath := range s.state.Files {
		statePaths = append(statePaths, remotePath)
	}
	sort.Strings(statePaths)

	stillMissing := map[string]struct{}{}
	for _, remotePath := range statePaths {
		if _, ok := remotePaths[remotePath]; ok {
			s.yieldFullPullStateLock()
			continue
		}
		if s.fullPullPathTouchedByUpPath(remotePath) {
			// The snapshot predates this local write. Treat the up-path as the
			// authoritative observation for this cycle and do not even create a
			// first-pass tombstone from the stale absence.
			s.yieldFullPullStateLock()
			continue
		}
		stillMissing[remotePath] = struct{}{}
		allow, tErr := s.observePendingDelete(remotePath, observedRevision)
		if tErr != nil {
			s.logf("tombstone update failed for %s: %v", remotePath, tErr)
			s.yieldFullPullStateLock()
			continue
		}
		if !allow {
			// First observation (or aged-out reset) — record and skip.
			s.yieldFullPullStateLock()
			continue
		}
		if err := s.applyRemoteDelete(remotePath, conflicted); err != nil {
			return err
		}
		// Confirmed delete fired; clear the marker.
		s.removeTombstone(remotePath)
		delete(stillMissing, remotePath)
		s.yieldFullPullStateLock()
	}

	// Drop markers whose paths have reappeared.
	s.pruneStaleTombstones(stillMissing)

	// Advance the gate on a clean, non-empty observation.
	if observedRevision != "" {
		s.state.LastAppliedRevision = observedRevision
	}
	return nil
}

// revisionAdvances reports whether observed is strictly newer than last.
// Revisions in this codebase look like "rev_<int>" (see fakeClient) but
// real cloud revisions may be opaque; we compare numerically when both
// match the rev_<int> shape, and lexicographically otherwise. An empty
// observed is never an advancement. Bootstrap callers that need to accept a
// first unversioned observation must keep that exception local to their
// persisted bootstrap-completion gate; this generic predicate is also used by
// strict candidate ordering.
func revisionAdvances(last, observed string) bool {
	observed = strings.TrimSpace(observed)
	last = strings.TrimSpace(last)
	if observed == "" {
		return false
	}
	if last == "" {
		return true
	}
	if a, aOk := parseRevSeq(last); aOk {
		if b, bOk := parseRevSeq(observed); bOk {
			return b > a
		}
	}
	return observed > last
}

// parseRevSeq extracts the integer portion of a "rev_<int>" identifier.
func parseRevSeq(rev string) (int64, bool) {
	rev = strings.TrimSpace(rev)
	if !strings.HasPrefix(rev, "rev_") {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(rev, "rev_"), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func (s *Syncer) materializeProviderLayouts(remoteFiles map[string]RemoteFile) error {
	if s.layoutRegistrar == nil {
		return nil
	}

	remotePaths := make(map[string]struct{}, len(remoteFiles))
	for remotePath := range remoteFiles {
		remotePaths[remotePath] = struct{}{}
	}
	return s.materializeProviderLayoutsFromPaths(remotePaths)
}

func (s *Syncer) materializeProviderLayoutsFromPaths(remotePaths map[string]struct{}) error {
	if s.layoutRegistrar == nil {
		return nil
	}

	providerManifests := map[string]struct {
		resources map[string]struct{}
		aliases   map[string]struct{}
	}{}
	for remotePath := range remotePaths {
		provider, resource, alias, ok := providerLayoutPartsWithAlias(s.remoteRoot, remotePath)
		if !ok {
			continue
		}
		manifest := providerManifests[provider]
		if manifest.resources == nil {
			manifest.resources = map[string]struct{}{}
		}
		if resource != "" {
			manifest.resources[resource] = struct{}{}
		}
		if alias != "" {
			if manifest.aliases == nil {
				manifest.aliases = map[string]struct{}{}
			}
			manifest.aliases[alias] = struct{}{}
		}
		providerManifests[provider] = manifest
	}

	providers := make([]string, 0, len(providerManifests))
	for provider := range providerManifests {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	for _, provider := range providers {
		parts := providerManifests[provider]
		manifest := providerLayoutManifest(provider, parts.resources, parts.aliases)
		if err := s.layoutRegistrar.RegisterProviderLayout(provider, manifest); err != nil {
			return fmt.Errorf("register provider layout for %s: %w", provider, err)
		}
	}
	return nil
}

func (s *Syncer) ensureProviderLayout(provider string) error {
	if s.layoutRegistrar == nil {
		return nil
	}
	provider = strings.TrimSpace(provider)
	if provider == "" || isReservedProviderLayoutSegment(provider) {
		return nil
	}
	return s.layoutRegistrar.RegisterProviderLayout(provider, providerLayoutManifest(provider, nil, nil))
}

func providerLayoutManifest(provider string, resources map[string]struct{}, observedAliases map[string]struct{}) ProviderLayoutManifest {
	resourceNames := make([]string, 0, len(resources))
	for resource := range resources {
		resourceNames = append(resourceNames, resource)
	}
	sort.Strings(resourceNames)

	aliases := make(map[string]struct{}, len(observedAliases))
	for alias := range observedAliases {
		if isProviderLayoutAliasSegment(alias) {
			aliases[alias] = struct{}{}
		}
	}
	aliasNames := make([]string, 0, len(aliases))
	for alias := range aliases {
		aliasNames = append(aliasNames, alias)
	}
	sort.Strings(aliasNames)

	return ProviderLayoutManifest{
		Provider:      provider,
		Resources:     resourceNames,
		AliasSegments: aliasNames,
	}
}

func providerLayoutParts(remoteRoot, remotePath string) (provider, resource string, ok bool) {
	provider, resource, _, ok = providerLayoutPartsWithAlias(remoteRoot, remotePath)
	return provider, resource, ok
}

func providerLayoutPartsWithAlias(remoteRoot, remotePath string) (provider, resource, alias string, ok bool) {
	remoteRoot = normalizeRemotePath(remoteRoot)
	remotePath = normalizeRemotePath(remotePath)
	if !isUnderRemoteRoot(remoteRoot, remotePath) {
		return "", "", "", false
	}

	rel := strings.TrimPrefix(remotePath, remoteRoot)
	if remoteRoot == "/" {
		rel = strings.TrimPrefix(remotePath, "/")
	} else {
		rel = strings.TrimPrefix(rel, "/")
	}
	rel = strings.Trim(rel, "/")
	rootSegments := providerLayoutPathSegments(remoteRoot)
	relSegments := providerLayoutPathSegments(rel)
	if remoteRoot != "/" && len(rootSegments) > 0 {
		provider = strings.TrimSpace(rootSegments[0])
		if provider == "" || isReservedProviderLayoutSegment(provider) {
			return "", "", "", false
		}
		if len(rootSegments) > 1 {
			resource = providerLayoutRootResourceSegment(rootSegments[1:])
		}
		if resource == "" {
			resource = providerLayoutResourceSegment(relSegments)
		}
		alias = providerLayoutAliasSegment(append(rootSegments[1:], relSegments...))
		return provider, resource, alias, true
	}
	if len(relSegments) == 0 {
		return "", "", "", false
	}

	provider = strings.TrimSpace(relSegments[0])
	if provider == "" || isReservedProviderLayoutSegment(provider) {
		return "", "", "", false
	}
	resource = providerLayoutResourceSegment(relSegments[1:])
	alias = providerLayoutAliasSegment(relSegments[1:])
	return provider, resource, alias, true
}

func providerLayoutPathSegments(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

func providerLayoutRootResourceSegment(segments []string) string {
	if len(segments) == 0 {
		return ""
	}
	return providerLayoutCleanResourceSegment(segments[0])
}

func providerLayoutResourceSegment(segments []string) string {
	if len(segments) < 2 {
		return ""
	}
	return providerLayoutCleanResourceSegment(segments[0])
}

func providerLayoutCleanResourceSegment(segment string) string {
	candidate := strings.TrimSpace(segment)
	if candidate == "" || isReservedProviderLayoutSegment(candidate) || isProviderLayoutAliasSegment(candidate) {
		return ""
	}
	return candidate
}

func providerLayoutAliasSegment(segments []string) string {
	for _, segment := range segments {
		candidate := strings.TrimSpace(segment)
		if isProviderLayoutAliasSegment(candidate) {
			return candidate
		}
	}
	return ""
}

func isReservedProviderLayoutSegment(segment string) bool {
	switch strings.TrimSpace(segment) {
	case "", ".relay", ".skills", "digests", "_index.json", "LAYOUT.md", ".relayfile-mount-state.json":
		return true
	default:
		return false
	}
}

func isProviderLayoutAliasSegment(segment string) bool {
	for _, alias := range providerLayoutAliasSegments {
		if segment == alias {
			return true
		}
	}
	return false
}

// isProviderLayoutAliasRemotePath reports whether remotePath lives under a
// provider layout alias index (by-state, by-id, by-title, ...). Those paths are
// derived index views: the emitter removes the alias entry when the underlying
// record changes state, but the events feed may still carry the now-dangling
// path. A read 404 on such a path means the event is stale, not slow — the
// alias mirror is reconstructable from the canonical record and the periodic
// full pull — so the cycle can skip it immediately instead of holding the
// events cursor for the full read-not-ready TTL.
func isProviderLayoutAliasRemotePath(remotePath string) bool {
	for _, segment := range strings.Split(strings.Trim(normalizeRemotePath(remotePath), "/"), "/") {
		if isProviderLayoutAliasSegment(segment) {
			return true
		}
	}
	return false
}

func (s *Syncer) pullRemoteIncremental(ctx context.Context, conflicted map[string]struct{}, cursor string) (string, error) {
	currentCursor := strings.TrimSpace(cursor)
	safeCursor := currentCursor
	madeProgress := false

	for {
		pageStartCursor := currentCursor
		feed, err := s.client.ListEvents(ctx, s.workspace, s.eventProvider, currentCursor, defaultIncrementalEventPageLimit)
		if err != nil {
			if madeProgress && errors.Is(err, context.DeadlineExceeded) {
				s.state.IncrementalBacklogDraining = true
				return safeCursor, nil
			}
			return safeCursor, err
		}
		changed := map[string]FilesystemEvent{}
		deleted := map[string]struct{}{}
		pageCursor := currentCursor
		pageLastEventAt := ""
		for _, event := range feed.Events {
			eventID := strings.TrimSpace(event.EventID)
			if eventID != "" {
				pageCursor = eventID
			}
			if ts := strings.TrimSpace(event.Timestamp); ts != "" {
				pageLastEventAt = ts
			}
			remotePath := normalizeRemotePath(event.Path)
			if remotePath == "/" || !isUnderRemoteRoot(s.remoteRoot, remotePath) {
				continue
			}
			if s.skipMountRuntimeRemotePath(remotePath, conflicted) {
				continue
			}
			if tracked, ok := s.state.Files[remotePath]; ok && tracked.Denied {
				continue
			}
			switch event.Type {
			case "file.created", "file.updated":
				changed[remotePath] = event
				delete(deleted, remotePath)
				// Defensive cross-check against cloud-side revision reuse:
				// if cloud surfaces a content hash and it diverges from
				// what we have tracked under the same revision, force a
				// re-fetch even though the revision matches. Without this
				// check, a buggy cloud that reuses a revision identifier
				// for new content would leave the local file stale forever
				// because the standard rev-equality short-circuit would
				// hide the drift. The field is omitted on cloud versions
				// that have not yet been updated, in which case this
				// branch is a no-op.
				if event.ContentHash != "" {
					if tracked, ok := s.state.Files[remotePath]; ok &&
						tracked.Revision == event.Revision &&
						tracked.Hash != "" &&
						tracked.Hash != event.ContentHash {
						// Path is already in `changed` from the unconditional
						// add above for file.created/file.updated; this branch
						// exists purely to surface the rev-reuse anomaly in
						// logs so operators can spot the cloud-side bug.
						s.logf("revision %s reused for %s with divergent content hash (tracked=%s remote=%s); forcing re-fetch", event.Revision, remotePath, tracked.Hash, event.ContentHash)
					}
				}
			case "file.deleted":
				deleted[remotePath] = struct{}{}
				delete(changed, remotePath)
			}
		}
		checkpoint := s.incrementalCheckpointForPage(pageStartCursor, pageCursor)
		if err := s.applyIncrementalChanges(ctx, changed, deleted, conflicted, pageStartCursor, pageCursor, checkpoint); err != nil {
			return safeCursor, err
		}
		hasMore := feed.NextCursor != nil && strings.TrimSpace(*feed.NextCursor) != ""
		resumeCursor := strings.TrimSpace(pageCursor)
		if hasMore {
			resumeCursor = strings.TrimSpace(*feed.NextCursor)
		}
		if strings.TrimSpace(pageCursor) != "" {
			currentCursor = strings.TrimSpace(pageCursor)
			safeCursor = currentCursor
		}
		if resumeCursor != "" {
			s.state.EventsCursor = resumeCursor
			safeCursor = resumeCursor
			madeProgress = resumeCursor != strings.TrimSpace(cursor)
		}
		s.state.IncrementalBacklogDraining = hasMore
		s.state.IncrementalCheckpoint = nil
		if pageLastEventAt != "" {
			s.state.LastEventAt = pageLastEventAt
		}
		if resumeCursor != "" {
			if err := s.saveState(); err != nil {
				return safeCursor, err
			}
		}
		if !hasMore {
			break
		}
		currentCursor = strings.TrimSpace(*feed.NextCursor)
		if currentCursor != "" {
			safeCursor = currentCursor
		}
	}

	if safeCursor == "" {
		safeCursor = cursor
	}
	return safeCursor, nil
}

func (s *Syncer) incrementalCheckpointForPage(cursor, pageCursor string) incrementalCheckpoint {
	if s.state.IncrementalCheckpoint == nil {
		return incrementalCheckpoint{}
	}
	checkpoint := *s.state.IncrementalCheckpoint
	checkpoint.Cursor = strings.TrimSpace(checkpoint.Cursor)
	checkpoint.PageCursor = strings.TrimSpace(checkpoint.PageCursor)
	checkpoint.Phase = strings.TrimSpace(checkpoint.Phase)
	checkpoint.Path = normalizeRemotePath(checkpoint.Path)
	if checkpoint.Cursor != strings.TrimSpace(cursor) || checkpoint.PageCursor != strings.TrimSpace(pageCursor) {
		return incrementalCheckpoint{}
	}
	if checkpoint.Phase != "changed" && checkpoint.Phase != "deleted" {
		return incrementalCheckpoint{}
	}
	if checkpoint.Path == "/" || strings.TrimSpace(checkpoint.Path) == "" {
		return incrementalCheckpoint{}
	}
	return checkpoint
}

func (s *Syncer) applyIncrementalChanges(
	ctx context.Context,
	changed map[string]FilesystemEvent,
	deleted map[string]struct{},
	conflicted map[string]struct{},
	pageStartCursor, pageCursor string,
	checkpoint incrementalCheckpoint,
) error {
	changedPaths := make([]string, 0, len(changed))
	for remotePath := range changed {
		changedPaths = append(changedPaths, remotePath)
	}
	sort.Strings(changedPaths)
	for _, remotePath := range changedPaths {
		event := changed[remotePath]
		if checkpoint.Phase == "changed" && remotePath <= checkpoint.Path {
			continue
		}
		if checkpoint.Phase == "deleted" {
			continue
		}
		if conflicted != nil {
			if _, skip := conflicted[remotePath]; skip {
				s.markIncrementalCheckpoint(pageStartCursor, pageCursor, "changed", remotePath)
				continue
			}
		}
		skipped, err := s.trySkipIncrementalRead(remotePath, event)
		if err != nil {
			return err
		}
		if skipped {
			s.clearIncrementalReadNotReady(remotePath)
			s.markIncrementalCheckpoint(pageStartCursor, pageCursor, "changed", remotePath)
			continue
		}
		file, err := s.client.ReadFile(ctx, s.workspace, remotePath)
		if err != nil {
			var httpErr *HTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
				// Operator escape hatch (`relayfile writeback skip-stuck`): drop
				// every unreadable event immediately, regardless of path shape
				// or TTL, until the optional max budget is exhausted. Once the
				// budget is hit, preserve the cursor so a follow-up run resumes
				// from here.
				if s.skipStuckMode {
					if s.skipStuckMax > 0 && s.skipStuckCount >= s.skipStuckMax {
						s.logf("skip-stuck: reached max=%d skipped events; preserving events cursor for next run", s.skipStuckMax)
						return &IncrementalReadNotReadyError{
							Path:       remotePath,
							StatusCode: httpErr.StatusCode,
							Code:       httpErr.Code,
							Message:    httpErr.Message,
						}
					}
					s.skipStuckCount++
					s.logf("skip-stuck: dropping unreadable event for %s (404); advancing events cursor", remotePath)
					if err := s.applyRemoteDelete(remotePath, conflicted); err != nil {
						return err
					}
					s.markIncrementalCheckpoint(pageStartCursor, pageCursor, "changed", remotePath)
					continue
				}
				// In-cycle drain of the stuck-event class. A read 404 on a
				// provider-layout-alias path (by-state, by-id, ...) is a stale
				// index event: the emitter dropped the alias entry when the
				// record changed state, but the events feed still carries the
				// dangling path. Skip it immediately and keep draining the rest
				// of the page in this same cycle rather than holding the cursor
				// for the full read-not-ready TTL and exiting after one event.
				// Canonical records are reconstructable independently, so this
				// cannot lose committed data; the periodic full pull self-heals
				// any alias mirror that should still exist.
				if isProviderLayoutAliasRemotePath(remotePath) {
					s.staleAliasSkips++
					s.logf("changed event for %s is a stale index-alias path (read 404); skipping immediately and continuing drain", remotePath)
					if err := s.applyRemoteDelete(remotePath, conflicted); err != nil {
						return err
					}
					s.markIncrementalCheckpoint(pageStartCursor, pageCursor, "changed", remotePath)
					continue
				}
				if s.incrementalReadNotReadyExpired(remotePath, time.Now().UTC()) {
					s.logf("changed event for %s remained unreadable for at least %s; treating as deleted and advancing events cursor", remotePath, s.readNotReadyTTL)
					if err := s.applyRemoteDelete(remotePath, conflicted); err != nil {
						return err
					}
					s.markIncrementalCheckpoint(pageStartCursor, pageCursor, "changed", remotePath)
					continue
				}
				s.logf("changed event for %s is not readable yet; preserving events cursor for retry", remotePath)
				return &IncrementalReadNotReadyError{
					Path:       remotePath,
					StatusCode: httpErr.StatusCode,
					Code:       httpErr.Code,
					Message:    httpErr.Message,
				}
			}
			if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusForbidden {
				s.logf("skipping denied file: %s", remotePath)
				if markErr := s.markReadDenied(remotePath); markErr != nil {
					return markErr
				}
				s.clearIncrementalReadNotReady(remotePath)
				s.markIncrementalCheckpoint(pageStartCursor, pageCursor, "changed", remotePath)
				continue
			}
			return err
		}
		if err := s.applyRemoteFile(remotePath, file, conflicted); err != nil {
			return err
		}
		s.markIncrementalCheckpoint(pageStartCursor, pageCursor, "changed", remotePath)
	}

	deletedPaths := make([]string, 0, len(deleted))
	for remotePath := range deleted {
		deletedPaths = append(deletedPaths, remotePath)
	}
	sort.Strings(deletedPaths)
	for _, remotePath := range deletedPaths {
		if checkpoint.Phase == "deleted" && remotePath <= checkpoint.Path {
			continue
		}
		if err := s.applyRemoteDelete(remotePath, conflicted); err != nil {
			return err
		}
		s.markIncrementalCheckpoint(pageStartCursor, pageCursor, "deleted", remotePath)
	}
	return nil
}

func (s *Syncer) trySkipIncrementalRead(remotePath string, event FilesystemEvent) (bool, error) {
	contentHash := strings.TrimSpace(event.ContentHash)
	if contentHash == "" {
		return false, nil
	}
	tracked, ok := s.state.Files[remotePath]
	if ok && (tracked.Dirty || tracked.Denied) {
		return false, nil
	}
	localPath, err := s.remoteToLocalPath(remotePath)
	if err != nil {
		return false, nil
	}
	if err := s.assertNotMountRoot(localPath); err != nil {
		s.logf("skipping local hash probe for %s: %v", remotePath, err)
		return false, nil
	}
	snapshot, err := readLocalSnapshot(localPath, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		s.logf("local hash probe failed for %s (%s): %v", remotePath, localPath, err)
		return false, nil
	}
	if snapshot.Hash != contentHash {
		return false, nil
	}
	canWrite := s.canWritePath(remotePath)
	if err := s.applyLocalPermissions(localPath, canWrite); err != nil {
		return false, err
	}
	revision := strings.TrimSpace(event.Revision)
	if revision == "" {
		revision = tracked.Revision
	}
	s.state.Files[remotePath] = trackedFile{
		Revision:          revision,
		ContentType:       snapshot.ContentType,
		Hash:              snapshot.Hash,
		Dirty:             false,
		LocalRelativePath: tracked.LocalRelativePath,
		Denied:            false,
		ReadOnly:          !canWrite,
	}
	s.clearSkippedMaterialization(remotePath)
	return true, nil
}

func (s *Syncer) markIncrementalCheckpoint(pageStartCursor, pageCursor, phase, remotePath string) {
	s.state.IncrementalCheckpoint = &incrementalCheckpoint{
		Cursor:     strings.TrimSpace(pageStartCursor),
		PageCursor: strings.TrimSpace(pageCursor),
		Phase:      strings.TrimSpace(phase),
		Path:       normalizeRemotePath(remotePath),
	}
}

func (s *Syncer) incrementalReadNotReadyExpired(remotePath string, now time.Time) bool {
	remotePath = normalizeRemotePath(remotePath)
	if s.state.IncrementalReadNotReadySince == nil {
		s.state.IncrementalReadNotReadySince = map[string]string{}
	}
	raw := strings.TrimSpace(s.state.IncrementalReadNotReadySince[remotePath])
	if raw == "" {
		s.state.IncrementalReadNotReadySince[remotePath] = now.Format(time.RFC3339Nano)
		return false
	}
	firstSeen, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		s.state.IncrementalReadNotReadySince[remotePath] = now.Format(time.RFC3339Nano)
		return false
	}
	return !now.Before(firstSeen.Add(s.readNotReadyTTL))
}

func (s *Syncer) clearIncrementalReadNotReady(remotePath string) {
	if s.state.IncrementalReadNotReadySince == nil {
		return
	}
	delete(s.state.IncrementalReadNotReadySince, normalizeRemotePath(remotePath))
	if len(s.state.IncrementalReadNotReadySince) == 0 {
		s.state.IncrementalReadNotReadySince = nil
	}
}

func (s *Syncer) clearAllIncrementalReadNotReady() {
	s.state.IncrementalReadNotReadySince = nil
}

func (s *Syncer) resolveLatestEventCursor(ctx context.Context) (string, error) {
	var lastErr error
	attempts := defaultCursorResolutionAttempts
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		cursor, err := s.resolveLatestEventCursorOnce(ctx, s.cursorTimeout)
		if err == nil {
			return cursor, nil
		}
		lastErr = err
		if !isCursorResolutionRetryable(err) || attempt == attempts-1 {
			return "", err
		}
		delay := cursorResolutionRetryDelay(s.cursorTimeout, attempt)
		s.logf("cursor resolution attempt %d/%d failed (%v); retrying in %s", attempt+1, attempts, err, delay)
		if waitErr := waitWithContext(s.rootCtx, delay); waitErr != nil {
			return "", waitErr
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", nil
}

func (s *Syncer) resolveLatestEventCursorOnce(ctx context.Context, timeout time.Duration) (string, error) {
	// Derive an OWN deadline from rootCtx so a slow/hanging events feed can
	// never wedge an otherwise healthy cycle (and is independent of whatever
	// inbound per-cycle/bootstrap ctx the caller passed). The signature/return
	// contract is unchanged; the inbound ctx is observed only for cancellation
	// before starting an attempt.
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(s.rootCtx, timeout)
	defer cancel()

	// Preferred path (post cloud#927): /fs/events?direction=desc&limit=1
	// returns the latest event id in one round trip. Walking the whole feed
	// to the tail is O(N) pages and reliably exceeded cursorTimeout on
	// workspaces with >~50k events.
	if latest, err := s.client.LatestEventID(cctx, s.workspace, s.eventProvider); err == nil {
		return latest, nil
	} else {
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || (httpErr.StatusCode != http.StatusBadRequest && httpErr.StatusCode != http.StatusNotFound) {
			return "", err
		}
		// Fall through: older self-host cloud may not yet support
		// direction=desc; degrade to the legacy page-walk.
	}

	cursor := ""
	latest := ""
	for {
		feed, err := s.client.ListEvents(cctx, s.workspace, s.eventProvider, cursor, 1000)
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

func isCursorResolutionRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func cursorResolutionRetryDelay(timeout time.Duration, attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := defaultCursorRetryBaseDelay
	for i := 0; i < attempt; i++ {
		delay *= 2
	}
	maxDelay := 2 * time.Second
	if timeout > 0 && timeout/10 < maxDelay {
		maxDelay = timeout / 10
	}
	if maxDelay < time.Millisecond {
		maxDelay = time.Millisecond
	}
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

func (s *Syncer) applyRemoteFile(remotePath string, file RemoteFile, conflicted map[string]struct{}) (err error) {
	materialized := false
	defer func() {
		if err == nil {
			s.clearIncrementalReadNotReady(remotePath)
			if materialized {
				s.clearSkippedMaterialization(remotePath)
			}
		}
	}()
	if s.skipMountRuntimeRemotePath(remotePath, conflicted) {
		return nil
	}
	if conflicted != nil {
		if _, skip := conflicted[remotePath]; skip {
			return nil
		}
	}
	if s.fullPullPathTouchedByUpPath(remotePath) {
		return nil
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
		localPath, err := s.remoteToLocalPath(remotePath)
		if err != nil {
			return nil
		}
		tracked.LocalRelativePath = s.state.Files[remotePath].LocalRelativePath
		if err := s.assertNotMountRoot(localPath); err != nil {
			s.logf("skipping remote file %s: %v", remotePath, err)
			return nil
		}
		if err := s.applyLocalPermissions(localPath, canWrite); err != nil {
			if s.skipPathLocalMaterializationError(remotePath, "permission update", err) {
				return nil
			}
			return err
		}
		s.state.Files[remotePath] = tracked
		materialized = true
		return nil
	}
	localPath, err := s.remoteToLocalPath(remotePath)
	if err != nil {
		return nil
	}
	tracked.LocalRelativePath = s.state.Files[remotePath].LocalRelativePath
	if err := s.assertNotMountRoot(localPath); err != nil {
		s.logf("skipping remote file %s: %v", remotePath, err)
		return nil
	}
	// _index.json files and nested <integration>/.layout.md dotfiles are
	// first-class remote payloads. Do not filter or normalize them here; only
	// the internal .relayfile-mount-state.json family is reserved elsewhere.
	// Virtual <provider>/.layout.md manifests are registered from snapshots by
	// materializeProviderLayouts; remote-supplied .layout.md payloads still
	// pass through to disk unchanged.
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		if isRemotePathCollision(err) {
			s.quarantineRemotePath(remotePath, "cannot create parent directory", err)
			return nil
		}
		if s.skipPathLocalMaterializationError(remotePath, "parent directory creation", err) {
			return nil
		}
		return err
	}
	remoteHash := hashBytes(remoteBytes)
	shouldWrite := true
	if current, err := os.ReadFile(localPath); err == nil {
		localHash := hashBytes(current)
		if localHash == remoteHash {
			shouldWrite = false
		}
	}
	if shouldWrite {
		if err := writeFileAtomic(localPath, remoteBytes, 0o644); err != nil {
			if isRemotePathCollision(err) {
				s.quarantineRemotePath(remotePath, "cannot write file (target is a directory)", err)
				return nil
			}
			if s.skipPathLocalMaterializationError(remotePath, "atomic write", err) {
				return nil
			}
			return err
		}
	}
	if err := s.applyLocalPermissions(localPath, canWrite); err != nil {
		if s.skipPathLocalMaterializationError(remotePath, "permission update", err) {
			return nil
		}
		return err
	}
	contentType := strings.TrimSpace(file.ContentType)
	if contentType == "" {
		contentType = detectContentType(localPath)
	}
	tracked.ReadOnly = !canWrite
	s.state.Files[remotePath] = trackedFile{
		Revision:          file.Revision,
		ContentType:       contentType,
		Encoding:          normalizeEncoding(file.Encoding),
		Hash:              remoteHash,
		Dirty:             false,
		LocalRelativePath: tracked.LocalRelativePath,
		Denied:            false,
		ReadOnly:          !canWrite,
	}
	materialized = true
	return nil
}

func (s *Syncer) skipMountRuntimeRemotePath(remotePath string, conflicted map[string]struct{}) bool {
	remotePath = mountRuntimeRemoteRoot(remotePath)
	if remotePath == "" {
		return false
	}
	s.logf("skipping mount runtime path surfaced as workspace content: %s", remotePath)
	prefix := strings.TrimSuffix(remotePath, "/") + "/"
	for trackedPath := range s.state.Files {
		if trackedPath != remotePath && !strings.HasPrefix(trackedPath, prefix) {
			continue
		}
		delete(s.state.Files, trackedPath)
		s.clearIncrementalReadNotReady(trackedPath)
		if conflicted != nil {
			delete(conflicted, trackedPath)
		}
	}
	for notReadyPath := range s.state.IncrementalReadNotReadySince {
		if notReadyPath == remotePath || strings.HasPrefix(notReadyPath, prefix) {
			s.clearIncrementalReadNotReady(notReadyPath)
		}
	}
	if localPath, err := s.remoteToLocalPath(remotePath); err == nil {
		if s.isActiveMountRuntimeLocalPath(localPath) {
			// A leaked remote /.relay entry maps onto this daemon's own public
			// runtime directory. Reject the remote entry, but never delete the
			// live local state/outbox while doing so.
		} else if err := s.assertNotMountRoot(localPath); err != nil {
			s.logf("failed to clean local mount runtime path %s: %v", remotePath, err)
		} else if removeErr := os.RemoveAll(localPath); removeErr != nil {
			s.logf("failed to remove local mount runtime subtree %s (%s): %v", remotePath, localPath, removeErr)
		}
	}
	s.clearIncrementalReadNotReady(remotePath)
	return true
}

func (s *Syncer) isActiveMountRuntimeLocalPath(localPath string) bool {
	localPath = filepath.Clean(localPath)
	publicRuntimeRoot := filepath.Join(filepath.Clean(s.localRoot), ".relay")
	if relative, err := filepath.Rel(publicRuntimeRoot, localPath); err == nil &&
		(relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))) {
		return true
	}
	stateFile := filepath.Clean(s.stateFile)
	if localPath == stateFile {
		return true
	}
	stateTempPrefix := strings.TrimSuffix(atomicTempPattern(stateFile), "*")
	return filepath.Dir(localPath) == filepath.Dir(stateFile) &&
		strings.HasPrefix(filepath.Base(localPath), stateTempPrefix)
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
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return false
	}
	// Short-form scope without plane prefix.
	if strings.EqualFold(scope, "fs:write") || strings.EqualFold(scope, "fs:manage") {
		return true
	}

	segments := strings.SplitN(scope, ":", 4)
	if len(segments) < 3 {
		return false
	}

	plane := strings.ToLower(strings.TrimSpace(segments[0]))
	res := strings.ToLower(strings.TrimSpace(segments[1]))
	act := strings.ToLower(strings.TrimSpace(segments[2]))

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

func (s *Syncer) applyRemoteDelete(remotePath string, conflicted map[string]struct{}) (err error) {
	defer func() {
		if err == nil {
			s.clearIncrementalReadNotReady(remotePath)
		}
	}()
	if conflicted != nil {
		if _, skip := conflicted[remotePath]; skip {
			return nil
		}
	}
	// An authoritative remote delete resolves any earlier local
	// materialization failure even when the file was never tracked locally.
	s.clearSkippedMaterialization(remotePath)
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
	localPath, err := s.remoteToLocalPath(remotePath)
	if err != nil {
		delete(s.state.Files, remotePath)
		return nil
	}
	if err := s.assertNotMountRoot(localPath); err != nil {
		s.logf("skipping remote delete for %s: %v", remotePath, err)
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
		localPath := snapshot.LocalPath
		if strings.TrimSpace(localPath) == "" {
			localPath, err = s.remoteToLocalPath(remotePath)
			if err != nil {
				return nil, err
			}
		}
		canWrite := s.canWritePath(remotePath)
		tracked.ReadOnly = !canWrite
		if exists && tracked.Denied {
			if err := s.assertNotMountRoot(localPath); err != nil {
				s.logf("skipping denied-file removal for %s: %v", remotePath, err)
				continue
			}
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
		if snapshot.SkipWriteback {
			// snapshot.SkipWriteback is a general "don't push this" flag set
			// by scanLocalFiles for two distinct reasons (oversize body,
			// lazy-untracked). Recomputing shouldSkipLazyUntrackedPush here
			// (cheap: no I/O, same inputs as the scan) attributes the
			// increment to the correct counter only when this is actually
			// the lazy-untracked reason, and only at the real pushLocal skip
			// decision — not on every scanLocalFiles call (see scanLocalFiles).
			if s.shouldSkipLazyUntrackedPush(remotePath) {
				s.state.Counters.SkippedLazyUntrackedPush++
			}
			continue
		}
		if exists && !tracked.Dirty {
			tracked.ContentType = snapshot.ContentType
			tracked.Encoding = normalizeEncoding(snapshot.Encoding)
			tracked.ReadOnly = false
			s.state.Files[remotePath] = tracked
			continue
		}
		fullSnapshot, err := readLocalSnapshot(localPath, true)
		if err != nil {
			return nil, err
		}
		snapshot = fullSnapshot
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
		if !tracked.DeletePending {
			s.state.Files[remotePath] = tracked
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

	localPath, err := s.remoteToLocalPath(remotePath)
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
	githubPathIndex := s.githubWorkingTreePathIndex()
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
			if rel, relErr := filepath.Rel(s.localRoot, path); relErr == nil &&
				rel != "." &&
				isMountRuntimeRelativePath(rel) {
				s.logMountControlPathSkipped(rel)
				return filepath.SkipDir
			}
			return nil
		}
		// Data-loss guard: skip any top-level entry whose name collides
		// with the mount directory's own basename (round-trip-onto-root).
		if rel, relErr := filepath.Rel(s.localRoot, path); relErr == nil {
			if isMountRuntimeRelativePath(rel) {
				s.logMountControlPathSkipped(rel)
				return nil
			}
			first := strings.SplitN(rel, string(os.PathSeparator), 2)[0]
			if reservedTopLevel(first) || first == filepath.Base(s.localRoot) {
				return nil
			}
		}
		absPath, err := filepath.Abs(path)
		if err == nil && absPath == statePathAbs {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		// Writeback body size cap: an oversized local file must not be
		// enqueued for writeback (it both stresses the cloud and was part
		// of the clobber pathology). Surface it and skip.
		if max := maxWritebackBytes(); max > 0 && info.Size() > max {
			remotePath, err := s.localPathToRemotePath(path, githubPathIndex)
			if err != nil {
				return nil
			}
			logKey := fmt.Sprintf("%s:%d:%d", remotePath, info.Size(), max)
			if s.oversizedLogged == nil {
				s.oversizedLogged = map[string]struct{}{}
			}
			if _, seen := s.oversizedLogged[logKey]; !seen {
				s.logf("skipping oversized local file %s (%d bytes > %d byte writeback cap); not enqueued", path, info.Size(), max)
				s.oversizedLogged[logKey] = struct{}{}
			}
			snapshot, err := readLocalSnapshot(path, false)
			if err != nil {
				return err
			}
			snapshot.SkipWriteback = true
			results[remotePath] = snapshot
			s.state.Counters.SkippedOversizeWriteback++
			return nil
		}
		remotePath, err := s.localPathToRemotePath(path, githubPathIndex)
		if err != nil {
			return nil
		}
		snapshot, err := readLocalSnapshot(path, false)
		if err != nil {
			return err
		}
		if s.shouldSkipLazyUntrackedPush(remotePath) {
			// Only flag the snapshot here; do NOT increment
			// Counters.SkippedLazyUntrackedPush in this function.
			// scanLocalFiles also runs from savePublicState (status-display
			// only, not a push decision), so counting here would inflate the
			// counter on every state save instead of only on real pushLocal
			// skip decisions. The counter increments where the skip actually
			// happens: pushLocal's SkipWriteback check, and
			// preparePendingBulkWrite for the watcher path.
			snapshot.SkipWriteback = true
			s.logLazyUntrackedPushSkipped(remotePath)
		}
		results[remotePath] = snapshot
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// githubAdapterCreateCommandPathPatterns enumerate the GitHub Relayfile
// adapter's writeback command roots that accept an arbitrary (noncanonical)
// leaf filename to create a new provider record. Each entry's pattern
// capture group is the leaf id; a leaf that ALSO matches the adapter's
// canonical id pattern is a materialized record (the adapter's PATCH-by-
// editing-record surface), not a create command, and must stay classified
// as non-create so it remains subject to shouldSkipLazyUntrackedPush.
//
// Semantics cited to ../relayfile-adapters:
//   - adapter-core classifyWrite: a path matches a resource's pathPattern,
//     the leaf id is extracted, and canonical-id-match => "patch" while
//     non-canonical => "create" (packages/core/src/runtime/file-native-router.ts:148-186).
//   - issues:                    packages/github/src/writeback.ts:35-36 (ISSUE_WRITEBACK_PATH)
//     packages/github/src/resources.ts:12-18 (idPattern ^[1-9]\d*$)
//   - issue comments:            packages/github/src/writeback.ts:41-42 (ISSUE_COMMENT_WRITEBACK_PATH)
//     packages/github/src/resources.ts:19-26 (idPattern ^(?:meta|\d+)$ — the
//     literal "meta" leaf, e.g. comments/42/meta.json, is canonical too)
//   - PR reviews:                packages/github/src/writeback.ts:31-32 (REVIEW_WRITEBACK_PATH)
//     packages/github/src/resources.ts:27-34 (idPattern ^\d+$)
//   - PR review-comment replies: packages/github/src/writeback.ts:43-45 (PR_COMMENT_REPLY_WRITEBACK_PATH)
//     packages/github/src/resources.ts:43-50 (idPattern ^\d+$)
//
// merge.json (packages/github/src/writeback.ts:33-34, resources.ts:35-42) has
// no arbitrary-leaf variant — the leaf is always the literal "merge.json" —
// so it is matched separately by githubMergeCommandPathPattern below rather
// than through this table.
var githubAdapterCreateCommandPathPatterns = []struct {
	pattern   *regexp.Regexp
	canonical *regexp.Regexp
}{
	{
		pattern:   regexp.MustCompile(`^github/repos/[^/]+/[^/]+/issues/([^/]+?)(?:\.json)?$`),
		canonical: regexp.MustCompile(`^[1-9]\d*$`),
	},
	{
		pattern:   regexp.MustCompile(`^github/repos/[^/]+/[^/]+/issues/[1-9]\d*(?:__[^/]+)?/comments/([^/]+?)(?:\.json|/meta\.json)?$`),
		canonical: regexp.MustCompile(`^(?:meta|\d+)$`),
	},
	{
		pattern:   regexp.MustCompile(`^github/repos/[^/]+/[^/]+/pulls/[1-9]\d*(?:__[^/]+)?/reviews/([^/]+?)(?:\.json)?$`),
		canonical: regexp.MustCompile(`^\d+$`),
	},
	{
		pattern:   regexp.MustCompile(`^github/repos/[^/]+/[^/]+/pulls/[1-9]\d*(?:__[^/]+)?/review-comments/[1-9]\d*/replies/([^/]+?)(?:\.json)?$`),
		canonical: regexp.MustCompile(`^\d+$`),
	},
}

// githubMergeCommandPathPattern matches the exact merge.json command file
// (packages/github/src/writeback.ts:33-34). It is always pushable as a
// create-only command leaf: unlike the other roots it has no numeric-leaf
// canonical variant of its own (the enclosing PR-number segment is what
// classifyWrite treats as the id, and a PR number is always numeric —
// merge.json itself is the fixed command name).
var githubMergeCommandPathPattern = regexp.MustCompile(`^github/repos/[^/]+/[^/]+/pulls/[1-9]\d*(?:__[^/]+)?/merge\.json$`)

// isGithubAdapterReservedAuxiliaryLeaf reports whether basename is a
// provider-emitted auxiliary/index payload rather than an agent-authored
// writeback command. Cloud's pre-pull of a GitHub repo's /issues (and
// /pulls) root always materializes a resource-level `_index.json`
// alongside the canonical records (relayfile-adapters
// packages/github/src/layout-prompt.ts:8-10,24 and
// emit-auxiliary-files.ts:13-21). That index is a first-class remote
// payload, not a command: applyRemoteFile passes `_index.json` files and
// nested `<integration>/.layout.md` dotfiles through unchanged
// (syncer.go:5242-5244, the comment this function centralizes on). Without
// this exclusion, `issues/_index.json` has a nonnumeric leaf (`_index`)
// that would otherwise match the issues create-root pattern below and get
// pushed as a bogus issue-create — reopening the exact pre-pull hazard this
// guard exists to close. The exclusion matches EXACT reserved names only —
// the existing isReservedProviderLayoutSegment literal set (reused rather
// than duplicated; covers `_index.json` and `LAYOUT.md`). It deliberately
// does NOT ban `_`/`.` name prefixes: the adapter's create contract has no
// prefix reservation (adapter-core file-native-router.ts:148-186,637-710
// reserves only exact `.schema`/`.create.example`/`.adapter`/`.tmp`/
// `.partial` stem variants), so `issues/_draft.json` or `issues/.draft.json`
// are VALID create commands that a prefix rule would silently suppress —
// the over-skip loss class this review rejected twice.
func isGithubAdapterReservedAuxiliaryLeaf(basename string) bool {
	if isReservedProviderLayoutSegment(basename) {
		return true
	}
	return isGithubAdapterReservedWritebackStem(basename)
}

// isGithubAdapterReservedWritebackStem mirrors the adapter runtime's
// authoritative isReservedWritebackFilename (adapter-core
// packages/core/src/runtime/file-native-router.ts:695-706): the router
// explicitly ignores these exact stems and .tmp/.partial suffixes instead of
// treating them as create commands, so pushing them from a lazy mount would
// only manufacture the spurious revision/event this guard exists to prevent
// (cubic P1 on 20c374db). The stem is the basename minus a trailing ".json",
// exactly like the router computes it. Everything else — including
// `_draft.json` and `.draft.json` — remains a valid create leaf.
func isGithubAdapterReservedWritebackStem(basename string) bool {
	stem := strings.TrimSuffix(basename, ".json")
	switch stem {
	case ".schema", ".create.example", ".adapter", ".tmp", ".partial", "partial":
		return true
	}
	return strings.HasSuffix(stem, ".tmp") || strings.HasSuffix(stem, ".partial")
}

// isGithubAdapterCreateCommandPath reports whether remotePath is a
// noncanonical (arbitrary-name) leaf under one of the GitHub adapter's
// create command roots, or the exact merge.json command. See
// githubAdapterCreateCommandPathPatterns for the adapter contract citations.
// Numeric/meta canonical leaves — the adapter's PATCH-by-editing-record
// surface, e.g. pulls/7/reviews/991.json or comments/42/meta.json —
// deliberately return false: they are materialized records, not commands.
// Reserved/auxiliary provider payloads (isGithubAdapterReservedAuxiliaryLeaf,
// e.g. `_index.json`) are excluded before any create-root pattern is
// consulted, regardless of which root's directory they happen to sit under.
func isGithubAdapterCreateCommandPath(remotePath string) bool {
	normalized := strings.TrimPrefix(normalizeRemotePath(remotePath), "/")
	if isGithubAdapterReservedAuxiliaryLeaf(path.Base(normalized)) {
		return false
	}
	if githubMergeCommandPathPattern.MatchString(normalized) {
		return true
	}
	for _, root := range githubAdapterCreateCommandPathPatterns {
		match := root.pattern.FindStringSubmatch(normalized)
		if match == nil {
			continue
		}
		leaf := match[1]
		return !root.canonical.MatchString(leaf)
	}
	return false
}

// shouldSkipLazyUntrackedPush reports whether a locally-scanned file must be
// excluded from pushLocal's writeback path because it sits under a lazy
// GitHub repo subtree (isLazyGithubRepoSubtreePath) that this daemon never
// tracked in s.state.Files. This happens when a separate, isolated
// non-lazy pre-pull (its own state file) has already materialized a
// canonical record under a broad-root daemon's localDir before the lazy
// daemon's first cycle. Without this guard, scanLocalFiles's untracked-file
// path treats the pre-pulled record as a brand-new local write and pushLocal
// pushes it upstream as agent_write — minting a spurious revision,
// file.updated event, and provider writeback for content the agent never
// touched.
//
// Two independent OR'd exemptions cover real agent-authored writes that must
// still push even when untracked: (1) isMountWritebackCreateDraftPath, the
// pre-existing Relayfile-owned `<resource> <uuid>.json` / `factory-create-*`
// draft contract, and (2) isGithubAdapterCreateCommandPath, the GitHub
// adapter's actual create-command surface (arbitrary-name leaves under
// issues/comments/reviews/replies, plus merge.json) — the narrower
// isMountWritebackCreateDraftPath check alone misses these real command
// paths (e.g. review-comments/<id>/replies/new-reply.json), which is a
// blocking gap the first version of this guard had.
//
// Accepted trade-off: a numeric/meta canonical leaf under a lazy subtree
// (e.g. pulls/7/reviews/991.json) is the adapter's PATCH-by-editing-record
// surface too — an agent editing a pre-pulled canonical record's local copy
// won't have that edit pushed under a lazy mount. This does not regress
// anything that works today: that flow is already nonfunctional under lazy
// mounts (the record never materializes locally in the first place without
// an external pre-pull). The skip is still logged (logLazyUntrackedPushSkipped)
// so a future persona author debugging a silent patch gets a breadcrumb.
// Patch-under-lazy is a follow-up (daemon consulting pre-pull state hashes),
// not this guard.
func (s *Syncer) shouldSkipLazyUntrackedPush(remotePath string) bool {
	if !s.lazyRepos || !s.lazySkipUntrackedPush {
		return false
	}
	if !isUnderLazyGithubRepoSubtree(s.remoteRoot, remotePath) {
		return false
	}
	if _, tracked := s.state.Files[normalizeRemotePath(remotePath)]; tracked {
		return false
	}
	if isMountWritebackCreateDraftPath(remotePath) {
		return false
	}
	return !isGithubAdapterCreateCommandPath(remotePath)
}

// logLazyUntrackedPushSkipped logs each distinct skipped path only once per
// process, mirroring the oversizedLogged/controlSkipLogged dedup pattern so a
// large pre-pulled subtree does not spam the log every sync cycle.
func (s *Syncer) logLazyUntrackedPushSkipped(remotePath string) {
	normalized := normalizeRemotePath(remotePath)
	if s.lazyUntrackedLogged == nil {
		s.lazyUntrackedLogged = map[string]struct{}{}
	}
	if _, seen := s.lazyUntrackedLogged[normalized]; seen {
		return
	}
	s.lazyUntrackedLogged[normalized] = struct{}{}
	s.logf("skipping untracked local file under lazy GitHub repo subtree: %s (not enqueued for writeback)", normalized)
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

func isMountRuntimeRemotePath(path string) bool {
	return mountRuntimeRemoteRoot(path) != ""
}

func isMountRuntimeRelativePath(path string) bool {
	return mountRuntimeRemoteRoot("/"+strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(path)), "/")) != ""
}

// mountRuntimeRemoteRoot returns the reserved runtime subtree containing path.
// Canonicalizing descendants to their first reserved segment lets a bounded
// tree page clean/log one .relay root without treating its state/outbox
// children as separate subtrees.
func mountRuntimeRemoteRoot(path string) string {
	normalized := strings.TrimPrefix(filepath.ToSlash(normalizeRemotePath(path)), "/")
	if normalized == "" || normalized == "." {
		return ""
	}
	segments := strings.Split(normalized, "/")
	for index, segment := range segments {
		if segment == "" || segment == "." {
			continue
		}
		if segment == ".relay" ||
			segment == ".relayfile-mount-state.json" ||
			strings.HasPrefix(segment, ".relayfile-mount-state.json.tmp-") {
			return normalizeRemotePath("/" + strings.Join(segments[:index+1], "/"))
		}
	}
	return ""
}

func (s *Syncer) logMountControlPathSkipped(relativePath string) {
	normalized := filepath.ToSlash(strings.TrimSpace(relativePath))
	if normalized == "" || normalized == "." {
		return
	}
	if s.controlSkipLogged == nil {
		s.controlSkipLogged = map[string]struct{}{}
	}
	if _, seen := s.controlSkipLogged[normalized]; seen {
		return
	}
	s.controlSkipLogged[normalized] = struct{}{}
	s.logf("skipping mount control path before upload: %s", normalized)
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
	if s.state.BootstrapComplete && s.state.SyncMode == "write-only" && !s.writeOnly {
		s.logf("syncMode transition write-only->mirror detected; resetting BootstrapComplete to force a full bootstrap pull (backfills records missed while write-only)")
		s.state.BootstrapComplete = false
		s.state.BootstrapDirectories = nil
		s.state.BootstrapCursor = ""
		s.state.BootstrapStartedAt = ""
		s.state.BootstrapFilesSynced = 0
		s.state.BootstrapFilesTotal = 0
		s.state.BootstrapStallCycles = 0
	}
	if s.githubWorkingTree != nil && strings.TrimSpace(s.state.GithubWorkingTreeHeadSHA) != "" {
		s.githubWorkingTree.HeadSHA = strings.TrimSpace(s.state.GithubWorkingTreeHeadSHA)
	}
	return nil
}

func (s *Syncer) currentSyncMode() string {
	if s.writeOnly {
		return "write-only"
	}
	return "mirror"
}

func (s *Syncer) saveState() error {
	s.state.SyncMode = s.currentSyncMode()
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

func (s *Syncer) saveStateWithoutLocalScan() error {
	s.state.SyncMode = s.currentSyncMode()
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
	wasLowMemory := s.lowMemory
	s.lowMemory = true
	err = s.savePublicState()
	s.lowMemory = wasLowMemory
	return err
}

func (s *Syncer) savePublicState() error {
	currentFiles := map[string]localSnapshot{}
	if !s.lowMemory {
		var err error
		currentFiles, err = s.scanLocalFiles()
		if err != nil {
			return err
		}
	}
	conflictsByPath, pendingConflicts, err := s.listConflictArtifacts()
	if err != nil {
		return err
	}
	failedWritebacks := s.readPublicFailedWritebacks()
	deniedPaths := 0
	pendingWriteback := 0
	var files map[string]publicFileState
	if !s.lowMemory {
		files = make(map[string]publicFileState, len(s.state.Files))
	}

	for remotePath, tracked := range s.state.Files {
		fileStatus := "ready"
		switch {
		case conflictsByPath[remotePath] > 0:
			fileStatus = "conflict"
		case tracked.WriteDenied:
			fileStatus = "write-denied"
		case tracked.Denied:
			fileStatus = "read-denied"
		case tracked.Dirty || tracked.DeletePending:
			fileStatus = "writeback-pending"
		}
		if !s.lowMemory {
			if snapshot, ok := currentFiles[remotePath]; ok {
				if snapshot.SkipWriteback && !tracked.Denied && !tracked.WriteDenied {
					fileStatus = "writeback-skipped"
				}
			} else if tracked.DeletePending && !tracked.Denied && !tracked.WriteDenied && fileStatus == "ready" {
				fileStatus = "writeback-pending"
			}
		}
		if fileStatus == "writeback-pending" {
			pendingWriteback++
		}
		if tracked.Denied || tracked.WriteDenied {
			deniedPaths++
		}
		if !s.lowMemory {
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
	}
	if !s.lowMemory {
		for remotePath, snapshot := range currentFiles {
			if _, ok := files[remotePath]; ok {
				continue
			}
			if snapshot.SkipWriteback {
				files[remotePath] = publicFileState{
					ContentType: snapshot.ContentType,
					Encoding:    snapshot.Encoding,
					Status:      "writeback-skipped",
				}
				continue
			}
			pendingWriteback++
			files[remotePath] = publicFileState{
				ContentType: snapshot.ContentType,
				Encoding:    snapshot.Encoding,
				Status:      "writeback-pending",
			}
		}
	}

	states := publicStateFlags{
		Offline:             s.state.LastError != nil && s.state.LastError.Kind == "offline",
		Syncing:             s.state.IncrementalBacklogDraining,
		HasConflicts:        pendingConflicts > 0,
		HasPendingWriteback: pendingWriteback > 0,
	}
	outbox := s.summarizeOutbox()
	if outbox.Pending > 0 {
		states.HasPendingWriteback = true
	}
	if outbox.NeedsAttention > 0 {
		states.OutboxNeedsAttention = true
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
	case states.OutboxNeedsAttention:
		status = "writeback-needs-attention"
	case states.HasConflicts:
		status = "conflict"
	case states.HasPendingWriteback:
		status = "writeback-pending"
	case states.Syncing:
		status = "syncing"
	case states.Stale:
		status = "stale"
	}

	// Bootstrap-in-progress overrides "stale"/"ready": surface explicit
	// progress so operators (and the CLI status surface) see
	// "bootstrapping N/M" instead of a misleading stall while a large
	// initial mirror is still running.
	var bootstrap *bootstrapStatus
	if !s.state.BootstrapComplete && strings.TrimSpace(s.state.BootstrapStartedAt) != "" {
		status = "bootstrapping"
		bootstrap = &bootstrapStatus{
			Phase:       "bootstrapping",
			FilesSynced: s.state.BootstrapFilesSynced,
			FilesTotal:  s.state.BootstrapFilesTotal,
			StartedAt:   s.state.BootstrapStartedAt,
		}
	}

	mode := s.mode
	if mode == "" {
		mode = "poll"
	}
	syncMode := "mirror"
	if s.writeOnly {
		syncMode = "write-only"
	}
	public := publicState{
		WorkspaceID:               s.workspace,
		RemoteRoot:                s.remoteRoot,
		LocalRoot:                 s.localRoot,
		Mode:                      mode,
		SyncMode:                  syncMode,
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
		FailedWritebacks:          failedWritebacks,
		LastError:                 s.state.LastError,
		Files:                     files,
		LowMemory:                 s.lowMemory,
		Counters:                  s.state.Counters,
		Outbox:                    outbox,
		LastAppliedRevision:       s.state.LastAppliedRevision,
		Bootstrap:                 bootstrap,
	}
	if s.circuit != nil {
		snap := s.circuit.Snapshot()
		// Reconcile the in-state CircuitOpenEvents counter with the
		// breaker's own counter so the .relay/state.json view is the
		// canonical surface for operators.
		if snap.OpenEvents > s.state.Counters.CircuitOpenEvents {
			s.state.Counters.CircuitOpenEvents = snap.OpenEvents
			public.Counters.CircuitOpenEvents = snap.OpenEvents
		}
		public.Circuit = &snap
	}
	// ReconcileAgeSecs: seconds since last successful reconcile.
	if lastOK, err := parseStateTime(s.state.LastSuccessfulReconcileAt); err == nil && !lastOK.IsZero() {
		public.ReconcileAgeSecs = int64(time.Since(lastOK).Seconds())
	}
	// CredExpiresInSecs: seconds until access token expires (negative = expired).
	if s.credExpiresAt != "" {
		if exp, err := time.Parse(time.RFC3339, s.credExpiresAt); err == nil {
			public.CredExpiresInSecs = int64(time.Until(exp).Seconds())
		}
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

func (s *Syncer) readPublicFailedWritebacks() uint64 {
	payload, err := os.ReadFile(s.publicStatePath)
	if err != nil {
		return 0
	}
	var current struct {
		FailedWritebacks uint64 `json:"failedWritebacks"`
	}
	if err := json.Unmarshal(payload, &current); err != nil {
		return 0
	}
	return current.FailedWritebacks
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
	if !s.state.IncrementalBacklogDraining {
		s.state.LastSuccessfulReconcileAt = now
	}
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
		RawContent:  data,
		WireContent: wireContent,
		ContentType: contentType,
		Encoding:    encoding,
		Hash:        hashBytes(data),
		LocalPath:   path,
	}
}

func readLocalSnapshot(path string, includeContent bool) (localSnapshot, error) {
	if includeContent {
		data, err := os.ReadFile(path)
		if err != nil {
			return localSnapshot{}, err
		}
		return newLocalSnapshot(path, data), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return localSnapshot{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return localSnapshot{}, err
	}
	return localSnapshot{
		ContentType: detectContentType(path),
		Hash:        hex.EncodeToString(h.Sum(nil)),
		LocalPath:   path,
	}, nil
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
	var bootstrapStalled *BootstrapStalledError
	if errors.As(err, &bootstrapStalled) {
		status.Kind = "bootstrap_stalled"
		status.Code = "bootstrap_stall_cycle_limit"
		return status
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

func remoteDescendantDepth(remoteRoot, remotePath string) int {
	remoteRoot = normalizeRemotePath(remoteRoot)
	remotePath = normalizeRemotePath(remotePath)
	if remotePath == remoteRoot || !isUnderRemoteRoot(remoteRoot, remotePath) {
		return 0
	}
	relative := strings.TrimPrefix(remotePath, remoteRoot)
	relative = strings.TrimPrefix(relative, "/")
	if relative == "" {
		return 0
	}
	return len(strings.Split(relative, "/"))
}

func remoteToLocalPath(localRoot, remoteRoot, remotePath string) (string, error) {
	return remoteToLocalPathWithShortening(localRoot, remoteRoot, remotePath, true)
}

func remoteToLocalPathWithShortening(localRoot, remoteRoot, remotePath string, shorten bool) (string, error) {
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
	// A provider basename can exceed the local filesystem's NAME_MAX even when
	// the full remote path is valid. Shorten each overlong component
	// deterministically for the mirror. The Syncer resolves the inverse from
	// its persisted remote-path state before writeback, preserving the exact
	// provider name rather than sending the shortened local representation.
	if shorten {
		rel = shortenLocalRelativePath(rel)
	}
	joined := filepath.Join(localRoot, filepath.FromSlash(rel))
	cleanJoined := filepath.Clean(joined)
	// Data-loss guard: a remote path whose only relative component is the
	// basename of the mount directory (e.g. remoteRoot="/" and
	// remotePath="/relayfile-mount" when localRoot=".../relayfile-mount")
	// resolves directly onto the mount root itself. Writing a file there
	// would clobber the entire mount directory. Reject it — only genuine
	// children may map onto the local tree.
	if cleanJoined == localRoot {
		return "", fmt.Errorf("remote path %s resolves onto the mount root %s", remotePath, localRoot)
	}
	// Final safety check: resolved path must be strictly under localRoot.
	if !strings.HasPrefix(cleanJoined, localRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("resolved path %s escapes local root %s", joined, localRoot)
	}
	return joined, nil
}

func (s *Syncer) remoteToLocalPath(remotePath string) (string, error) {
	remotePath = normalizeRemotePath(remotePath)
	if tracked, ok := s.state.Files[remotePath]; ok {
		if strings.TrimSpace(tracked.LocalRelativePath) != "" {
			localPath, err := safeLocalPath(s.localRoot, tracked.LocalRelativePath)
			if err != nil {
				return "", fmt.Errorf("invalid preserved local path for %s: %w", remotePath, err)
			}
			return localPath, nil
		}
		if localPath, rel, found := s.existingPreShorteningLocalPath(remotePath); found {
			tracked.LocalRelativePath = rel
			s.state.Files[remotePath] = tracked
			return localPath, nil
		}
	}
	return s.defaultRemoteToLocalPath(remotePath)
}

func (s *Syncer) existingPreShorteningLocalPath(remotePath string) (string, string, bool) {
	if s.githubWorkingTree != nil ||
		!pathHasComponentLongerThan(remotePath, maxLocalMirrorBasenameBytes) ||
		pathHasComponentLongerThan(remotePath, maxLocalFilesystemBasenameBytes) {
		return "", "", false
	}
	// Migration is only unambiguous when the deterministic post-fix path is
	// absent. If both names exist, keep the shortened mirror authoritative so
	// it cannot reappear in a scan as a new literal remote path.
	defaultPath, err := s.defaultRemoteToLocalPath(remotePath)
	if err != nil {
		return "", "", false
	}
	if _, err := os.Lstat(defaultPath); err == nil || !errors.Is(err, os.ErrNotExist) {
		return "", "", false
	}
	localPath, err := remoteToLocalPathWithShortening(s.localRoot, s.remoteRoot, remotePath, false)
	if err != nil {
		return "", "", false
	}
	info, err := os.Lstat(localPath)
	if err != nil || info.IsDir() {
		return "", "", false
	}
	mappedRemote, err := localToRemotePath(s.localRoot, s.remoteRoot, localPath)
	if err != nil || normalizeRemotePath(mappedRemote) != normalizeRemotePath(remotePath) {
		return "", "", false
	}
	rel, err := filepath.Rel(s.localRoot, localPath)
	if err != nil {
		return "", "", false
	}
	rel = filepath.ToSlash(rel)
	resolved, err := safeLocalPath(s.localRoot, rel)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(localPath) {
		return "", "", false
	}
	return localPath, rel, true
}

func (s *Syncer) defaultRemoteToLocalPath(remotePath string) (string, error) {
	if s.githubWorkingTree != nil {
		if rel, ok := s.githubWorkingTree.remotePathToWorkingTreeRel(remotePath); ok {
			return safeLocalPath(s.localRoot, rel)
		}
	}
	return remoteToLocalPath(s.localRoot, s.remoteRoot, remotePath)
}

func (s *Syncer) trackLocalPathIdentity(remotePath, localPath string, tracked trackedFile) trackedFile {
	if strings.TrimSpace(tracked.LocalRelativePath) != "" || s.githubWorkingTree != nil {
		return tracked
	}
	defaultPath, err := s.defaultRemoteToLocalPath(remotePath)
	if err != nil || filepath.Clean(defaultPath) == filepath.Clean(localPath) {
		return tracked
	}
	mappedRemote, err := localToRemotePath(s.localRoot, s.remoteRoot, localPath)
	if err != nil || normalizeRemotePath(mappedRemote) != normalizeRemotePath(remotePath) {
		return tracked
	}
	rel, err := filepath.Rel(s.localRoot, localPath)
	if err != nil {
		return tracked
	}
	rel = filepath.ToSlash(rel)
	resolved, err := safeLocalPath(s.localRoot, rel)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(localPath) {
		return tracked
	}
	tracked.LocalRelativePath = rel
	return tracked
}

func (s *Syncer) localPathToRemotePath(localPath string, githubPathIndex map[string]string) (string, error) {
	if remotePath, ok := s.trackedRemotePathForLocalPath(localPath); ok {
		return remotePath, nil
	}
	if s.githubWorkingTree != nil {
		rel, err := RelativeRemotePathFromLocal(s.localRoot, localPath)
		if err != nil {
			return "", err
		}
		if remotePath := s.githubRemotePathForWorkingTreeRel(rel.Slash(), githubPathIndex); remotePath != "" {
			return remotePath, nil
		}
		return s.githubWorkingTree.workingTreeRelToRemotePath(rel.Slash()), nil
	}
	return localToRemotePath(s.localRoot, s.remoteRoot, localPath)
}

func (s *Syncer) localRelativeToRemotePath(relativePath string) string {
	relativePath = filepath.ToSlash(strings.TrimSpace(relativePath))
	if localPath, err := safeLocalPath(s.localRoot, relativePath); err == nil {
		if remotePath, ok := s.trackedRemotePathForLocalPath(localPath); ok {
			return remotePath
		}
	}
	if s.githubWorkingTree != nil {
		if remotePath := s.githubRemotePathForWorkingTreeRel(relativePath, nil); remotePath != "" {
			return remotePath
		}
		return s.githubWorkingTree.workingTreeRelToRemotePath(relativePath)
	}
	return normalizeRemotePath(filepath.Join(s.remoteRoot, filepath.FromSlash(relativePath)))
}

// trackedRemotePathForLocalPath reverses deterministic local-name shortening
// through the persisted remote-path keys in mount state. There is no lossless
// stateless encoding from an arbitrary >NAME_MAX basename into <=NAME_MAX
// bytes; state is therefore the Path Contract's authoritative inverse. Sorting
// keeps the result deterministic even in the astronomically unlikely event of
// a truncated SHA-256 collision.
func (s *Syncer) trackedRemotePathForLocalPath(localPath string) (string, bool) {
	want := filepath.Clean(localPath)
	if !localPathMayContainShortenedName(want) {
		return "", false
	}
	var remotePaths []string
	for remotePath := range s.state.Files {
		// Only a remote path with an overlong component can map to a local
		// path containing the shortening marker. Avoid hashing every ordinary
		// tracked file when one shortened path is written back.
		if pathHasComponentLongerThan(remotePath, maxLocalMirrorBasenameBytes) {
			remotePaths = append(remotePaths, remotePath)
		}
	}
	sort.Strings(remotePaths)
	for _, remotePath := range remotePaths {
		mapped, err := s.remoteToLocalPath(remotePath)
		if err == nil && filepath.Clean(mapped) == want {
			return remotePath, true
		}
	}
	return "", false
}

func localPathMayContainShortenedName(localPath string) bool {
	return strings.Contains(localPath, "~rf-")
}

const (
	// POSIX NAME_MAX is commonly 255 bytes. Atomic mirror writes prepend a dot
	// and append a randomized ".tmp-*" suffix, so leave ample headroom for the
	// temporary basename as well as the final target.
	maxLocalMirrorBasenameBytes     = 220
	maxLocalFilesystemBasenameBytes = 255
	localNameHashBytes              = 16
	maxPreservedExtensionBytes      = 32
)

func shortenLocalRelativePath(rel string) string {
	rel = filepath.ToSlash(rel)
	if !pathHasComponentLongerThan(rel, maxLocalMirrorBasenameBytes) {
		return rel
	}
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		parts[i] = shortenLocalBasename(part)
	}
	return strings.Join(parts, "/")
}

func pathHasComponentLongerThan(path string, maxBytes int) bool {
	componentBytes := 0
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			if componentBytes > maxBytes {
				return true
			}
			componentBytes = 0
			continue
		}
		componentBytes++
	}
	return componentBytes > maxBytes
}

func shortenLocalBasename(base string) string {
	if len(base) <= maxLocalMirrorBasenameBytes {
		return base
	}
	sum := sha256.Sum256([]byte(base))
	hashSuffix := "~rf-" + hex.EncodeToString(sum[:localNameHashBytes])
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" || len(ext) > maxPreservedExtensionBytes {
		ext = ""
		stem = base
	}
	prefixBytes := maxLocalMirrorBasenameBytes - len(hashSuffix) - len(ext)
	if prefixBytes < 0 {
		prefixBytes = 0
	}
	prefix := truncateValidUTF8(stem, prefixBytes)
	return prefix + hashSuffix + ext
}

func truncateValidUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for value != "" && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func (s *Syncer) githubRemotePathForWorkingTreeRel(rel string, githubPathIndex map[string]string) string {
	if s.githubWorkingTree == nil || len(s.state.Files) == 0 {
		return ""
	}
	rel = filepath.ToSlash(filepath.Clean(filepath.ToSlash(strings.TrimSpace(rel))))
	rel = strings.TrimPrefix(rel, "/")
	if githubPathIndex != nil {
		return githubPathIndex[rel]
	}
	return s.githubWorkingTreePathIndex()[rel]
}

func (s *Syncer) githubWorkingTreePathIndex() map[string]string {
	if s.githubWorkingTree == nil || len(s.state.Files) == 0 {
		return nil
	}
	index := map[string]string{}
	revisions := map[string]string{}
	paths := make([]string, 0, len(s.state.Files))
	for remotePath := range s.state.Files {
		paths = append(paths, remotePath)
	}
	sort.Strings(paths)
	for _, remotePath := range paths {
		rel, ok := s.githubWorkingTree.remotePathToWorkingTreeRel(remotePath)
		if !ok {
			continue
		}
		current := index[rel]
		revision := s.state.Files[remotePath].Revision
		if current == "" || s.githubWorkingTreePathCandidatePreferred(remotePath, revision, current, revisions[rel]) {
			index[rel] = remotePath
			revisions[rel] = revision
		}
	}
	return index
}

func (s *Syncer) githubWorkingTreePathCandidatePreferred(candidatePath, candidateRevision, currentPath, currentRevision string) bool {
	candidateMatchesHead := s.githubWorkingTreeRemotePathMatchesHead(candidatePath)
	currentMatchesHead := s.githubWorkingTreeRemotePathMatchesHead(currentPath)
	if candidateMatchesHead != currentMatchesHead {
		return candidateMatchesHead
	}
	return revisionAdvances(currentRevision, candidateRevision)
}

func (s *Syncer) githubWorkingTreeRemotePathMatchesHead(remotePath string) bool {
	if s.githubWorkingTree == nil {
		return false
	}
	headSHA := strings.TrimSpace(s.githubWorkingTree.HeadSHA)
	if headSHA == "" {
		return false
	}
	return strings.HasSuffix(normalizeRemotePath(remotePath), "@"+headSHA+".json")
}

func safeLocalPath(localRoot, rel string) (string, error) {
	localRoot = filepath.Clean(localRoot)
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" || rel == "." || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("invalid working tree path %q", rel)
	}
	cleanRel := filepath.ToSlash(filepath.Clean(rel))
	if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, "../") || strings.Contains(cleanRel, "/../") {
		return "", fmt.Errorf("path %s escapes local root", rel)
	}
	joined := filepath.Join(localRoot, filepath.FromSlash(cleanRel))
	cleanJoined := filepath.Clean(joined)
	if cleanJoined == localRoot {
		return "", fmt.Errorf("path %s resolves onto the mount root %s", rel, localRoot)
	}
	if !strings.HasPrefix(cleanJoined, localRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("resolved path %s escapes local root %s", joined, localRoot)
	}
	return cleanJoined, nil
}

func detectGithubWorkingTreeMount(remoteRoot string) *githubWorkingTreeMount {
	remoteRoot = normalizeRemotePath(remoteRoot)
	segments := strings.Split(strings.Trim(remoteRoot, "/"), "/")
	if len(segments) != 5 || segments[0] != "github" || segments[1] != "repos" || segments[4] != "contents" {
		return nil
	}
	owner, err := url.PathUnescape(segments[2])
	if err != nil || strings.TrimSpace(owner) == "" {
		return nil
	}
	repo, err := url.PathUnescape(segments[3])
	if err != nil || strings.TrimSpace(repo) == "" {
		return nil
	}
	repoRoot := normalizeRemotePath(strings.Join(segments[:4], "/"))
	return &githubWorkingTreeMount{
		Owner:        owner,
		Repo:         repo,
		RepoRoot:     repoRoot,
		ContentsRoot: remoteRoot,
	}
}

func (g *githubWorkingTreeMount) cloneSentinelPath() string {
	return normalizeRemotePath(g.RepoRoot + "/.relayfile/clone.json")
}

func (g *githubWorkingTreeMount) legacyMetaPath() string {
	return normalizeRemotePath(g.RepoRoot + "/meta.json")
}

func (g *githubWorkingTreeMount) remotePathToWorkingTreeRel(remotePath string) (string, bool) {
	remotePath = normalizeRemotePath(remotePath)
	if !isUnderRemoteRoot(g.ContentsRoot, remotePath) {
		return "", false
	}
	rel := strings.TrimPrefix(remotePath, g.ContentsRoot)
	rel = strings.TrimPrefix(rel, "/")
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", false
	}
	suffix := "@" + strings.TrimSpace(g.HeadSHA) + ".json"
	if strings.TrimSpace(g.HeadSHA) != "" && strings.HasSuffix(rel, suffix) {
		rel = strings.TrimSuffix(rel, suffix)
	} else if idx := strings.LastIndex(rel, "@"); idx >= 0 && strings.HasSuffix(rel, ".json") {
		rel = rel[:idx]
	} else {
		return "", false
	}
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil {
			return "", false
		}
		parts[i] = decoded
	}
	return filepath.ToSlash(filepath.Clean(strings.Join(parts, "/"))), true
}

func (g *githubWorkingTreeMount) workingTreeRelToRemotePath(rel string) string {
	rel = filepath.ToSlash(strings.TrimSpace(filepath.Clean(filepath.ToSlash(rel))))
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" || rel == "." {
		return g.ContentsRoot
	}
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	suffix := ""
	if strings.TrimSpace(g.HeadSHA) != "" {
		suffix = "@" + strings.TrimSpace(g.HeadSHA) + ".json"
	}
	parts[len(parts)-1] += suffix
	return normalizeRemotePath(g.ContentsRoot + "/" + strings.Join(parts, "/"))
}

func localToRemotePath(localRoot, remoteRoot, localPath string) (string, error) {
	// Boundary gate: route through the typed RelativeRemotePath constructor
	// so the round-trip-onto-root collision (a child whose name equals the
	// mount-dir basename) is rejected by construction. Existing callers
	// still receive the legacy string return value; the newtype is purely
	// defensive here. Empty / "." / leading-".." paths are also rejected.
	typed, err := RelativeRemotePathFromLocal(localRoot, localPath)
	if err != nil {
		return "", err
	}
	rel := typed.Slash()
	remoteRoot = normalizeRemotePath(remoteRoot)
	var remotePath string
	if remoteRoot == "/" {
		remotePath = normalizeRemotePath("/" + rel)
	} else {
		remotePath = normalizeRemotePath(remoteRoot + "/" + rel)
	}
	// Data-loss guard: refuse to generate a remote path that would
	// round-trip back onto the mount root via remoteToLocalPath. This
	// happens when a child file's name equals the mount-dir basename
	// (e.g. localRoot=".../relayfile-mount" with a child
	// "relayfile-mount" mapping to remote "/relayfile-mount", which
	// then resolves back onto the root directory).
	if back, err := remoteToLocalPath(localRoot, remoteRoot, remotePath); err != nil {
		return "", fmt.Errorf("local path %s maps to a remote path that escapes the mount root: %w", localPath, err)
	} else if filepath.Clean(back) == filepath.Clean(localRoot) {
		return "", fmt.Errorf("local path %s maps to remote %s which round-trips onto the mount root", localPath, remotePath)
	}
	return remotePath, nil
}

// assertNotMountRoot is a defense-in-depth guard: it returns an error if
// localPath, once cleaned, equals the mount root. Callers that mutate the
// filesystem (writes, deletes) invoke this to ensure a malformed or
// adversarial remote path can never operate on the mount directory itself.
func (s *Syncer) assertNotMountRoot(localPath string) error {
	if filepath.Clean(localPath) == filepath.Clean(s.localRoot) {
		s.state.Counters.DeniedRootTarget++
		return fmt.Errorf("refusing filesystem operation on mount root %s", s.localRoot)
	}
	return nil
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

type webSocketDialError struct {
	err        error
	retryAfter time.Duration
}

func (e webSocketDialError) Error() string {
	if e.err == nil {
		return "websocket dial failed"
	}
	return e.err.Error()
}

func (e webSocketDialError) Unwrap() error {
	return e.err
}

func retryAfterFromResponse(response *http.Response) time.Duration {
	if response == nil {
		return 0
	}
	return parseRetryAfter(response.Header.Get("Retry-After"))
}

func (c *HTTPClient) retryDelay(attempt int, retryAfterHeader string) time.Duration {
	maxDelay := c.maxDelay
	if maxDelay <= 0 {
		maxDelay = defaultRetryAfterMaxDelay
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

// atomicTempPattern returns the pattern writeFileAtomic passes to
// os.CreateTemp for a given target path. Extracted so the temp-naming
// contract can be unit-tested directly — by the time writeFileAtomic
// returns, the temp file has already been renamed or removed, so an
// after-the-fact ReadDir cannot observe it.
//
// The pattern always produces a hidden temp (single-dot prefix), even
// when the target itself is hidden. Pre-fix, this function double-
// prefixed dot-leading targets and produced
// "..relayfile-mount-state.json.tmp-*", which the file watcher's
// shouldSkip didn't recognize as internal.
func atomicTempPattern(path string) string {
	// A user-created basename may be valid at 221-255 bytes even though
	// prefixing it and appending the randomized temp suffix would exceed the
	// filesystem's NAME_MAX. The temp name does not participate in the Path
	// Contract, so shorten only the disposable temp basename.
	base := shortenLocalBasename(filepath.Base(path))
	if !strings.HasPrefix(base, ".") {
		base = "." + base
	}
	return base + ".tmp-*"
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, atomicTempPattern(path))
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
	// Data-loss guard: never rename a file over an existing directory.
	// os.Rename onto a directory would (on some platforms) or would
	// otherwise be the mechanism by which the mount root was clobbered
	// by an 11MB file. If the target exists and is a directory, refuse.
	if info, err := os.Lstat(path); err == nil && info.IsDir() {
		return fmt.Errorf("refusing to replace directory %s with a file: %w", path, os.ErrExist)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	committed = true
	return nil
}
