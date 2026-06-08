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
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	defaultExportTimeout             = 45 * time.Second
	defaultCursorResolutionAttempts  = 3
	defaultCursorRetryBaseDelay      = 250 * time.Millisecond
	defaultBootstrapReadWorkers      = 16
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
	TargetRevision string `json:"targetRevision"`
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
	// ForceFullReconcile, when non-nil and true, forces one full reconcile
	// regardless of BootstrapComplete (escape hatch / clobber-remnant
	// recovery). nil falls back to env RELAYFILE_FORCE_FULL_RECONCILE.
	ForceFullReconcile *bool
	// LazyRepos controls lazy GitHub repo subtree hydration. nil falls back to env.
	LazyRepos *bool
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
	bootstrapTimeout     time.Duration
	bootstrapIdleTimeout time.Duration
	forceFullReconcile   bool
	incrementalCycles    int
	oversizedLogged      map[string]struct{}
	lazyRepos            bool
	lowMemory            bool
	writeOnly            bool
	layoutRegistrar      ProviderLayoutRegistrar
	githubWorkingTree    *githubWorkingTreeMount
	closeScheduler       *CloseScheduler
	rollingCoalescer     *RollingDigestCoalescer
	circuit              *CloudErrorCircuit
	mu                   sync.Mutex
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
	BootstrapComplete        bool   `json:"bootstrapComplete,omitempty"`
	BootstrapCursor          string `json:"bootstrapCursor,omitempty"`
	BootstrapFilesSynced     int    `json:"bootstrapFilesSynced,omitempty"`
	BootstrapFilesTotal      int    `json:"bootstrapFilesTotal,omitempty"`
	BootstrapStartedAt       string `json:"bootstrapStartedAt,omitempty"`
	GithubWorkingTreeHeadSHA string `json:"githubWorkingTreeHeadSha,omitempty"`
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
	SnapshotDeleteBlocked    uint64 `json:"snapshotDeleteBlocked,omitempty"`
	CircuitOpenEvents        uint64 `json:"circuitOpenEvents,omitempty"`
	TombstonesPending        uint64 `json:"tombstonesPending,omitempty"`
	TombstonesConfirmed      uint64 `json:"tombstonesConfirmed,omitempty"`
	TombstonesAgedOut        uint64 `json:"tombstonesAgedOut,omitempty"`
}

type trackedFile struct {
	Revision    string `json:"revision"`
	ContentType string `json:"contentType"`
	Encoding    string `json:"encoding,omitempty"`
	Hash        string `json:"hash"`
	Dirty       bool   `json:"dirty,omitempty"`
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
	// LastAppliedRevision is the highest cloud revision the daemon has
	// reconciled. Useful for operator status display.
	LastAppliedRevision string `json:"lastAppliedRevision,omitempty"`
	// Bootstrap surfaces in-progress full-tree bootstrap so operators see
	// "bootstrapping N/M files" instead of a misleading stall. The resume
	// cursor is intentionally NOT exposed (internal-only).
	Bootstrap *bootstrapStatus `json:"bootstrap,omitempty"`
}

// bootstrapStatus is the public, cursor-free view of bootstrap progress.
type bootstrapStatus struct {
	Phase       string `json:"phase"`
	FilesSynced int    `json:"filesSynced"`
	FilesTotal  int    `json:"filesTotal,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
}

type publicStateFlags struct {
	Stale               bool `json:"stale"`
	Offline             bool `json:"offline"`
	Syncing             bool `json:"syncing,omitempty"`
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
	exportTimeout := resolveDurationEnv(opts.ExportTimeout, "RELAYFILE_EXPORT_TIMEOUT", defaultExportTimeout, opts.Logger)
	if exportTimeout <= 0 {
		exportTimeout = defaultExportTimeout
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
		bulkFlushThreshold:   bulkFlushThreshold,
		mode:                 strings.TrimSpace(opts.Mode),
		writeOnly:            normalizeSyncMode(opts.SyncMode) == "write-only",
		interval:             opts.Interval,
		fullPullEvery:        fullPullEvery,
		cursorTimeout:        cursorTimeout,
		exportTimeout:        exportTimeout,
		bootstrapTimeout:     bootstrapTimeout,
		bootstrapIdleTimeout: bootstrapIdleTimeout,
		forceFullReconcile:   forceFullReconcile,
		oversizedLogged:      map[string]struct{}{},
		lazyRepos:            lazyRepos,
		lowMemory:            lowMemory,
		layoutRegistrar:      opts.ProviderLayoutRegistrar,
		githubWorkingTree:    githubWorkingTree,
		closeScheduler:       closeScheduler,
		rollingCoalescer:     rollingCoalescer,
		circuit:              NewCloudErrorCircuit(),
		state: mountState{
			Files: map[string]trackedFile{},
		},
	}, nil
}

// Circuit returns the cloud-error breaker for tests and status reporters.
func (s *Syncer) Circuit() *CloudErrorCircuit { return s.circuit }

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
	for _, chunk := range chunkPendingBulkWrites(s.workspace, pending, maxWritebackBatchBytes()) {
		if err := s.flushPendingBulkWriteChunk(ctx, chunk, conflicted); err != nil {
			return err
		}
	}
	return nil
}

func (s *Syncer) flushPendingBulkWriteChunk(ctx context.Context, pending []pendingBulkWrite, conflicted map[string]struct{}) error {
	files := bulkWriteFilesForPending(s.workspace, pending)
	response, err := s.client.WriteFilesBulk(ctx, s.workspace, files)
	if err != nil {
		s.recordCloudFailure(err)
		return err
	}
	s.recordCloudSuccess()

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
	if !relayfile.IsDraftFilePath(normalizedRemotePath) {
		return nil
	}
	return newMountWritebackCreateDraftContentIdentity(workspaceID, normalizedRemotePath, contentHash)
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
	delete(s.state.Files, remotePath)
	return nil
}

func (s *Syncer) sync(ctx context.Context, forcePoll bool) error {
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
	didPoll := false
	if s.writeOnly {
		if !s.state.BootstrapComplete {
			s.markBootstrapComplete()
		}
	} else if !s.state.BootstrapComplete || s.forceFullReconcile {
		if err := s.pullRemote(ctx, conflicted); err != nil {
			s.markSyncError(err)
			_ = s.saveState()
			return err
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

func (s *Syncer) pullRemote(ctx context.Context, conflicted map[string]struct{}) error {
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
		s.logf("detected non-empty state without completed bootstrap; forcing full reconcile (%d tracked files)", len(s.state.Files))
	}
	if s.state.BootstrapComplete && !s.forceFullReconcile && len(s.state.Files) > 0 {
		cursor, err := s.resolveLatestEventCursor(ctx)
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
	if s.wsConn != nil {
		return nil
	}
	if strings.TrimSpace(s.state.EventsCursor) != "" {
		return nil
	}
	cursor, err := s.resolveLatestEventCursor(bctx)
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
	manifest, err := s.readGithubCloneManifest(ctx)
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
		cursor, err = s.resolveGithubCloneManifestCursor(ctx, manifest)
		if err != nil {
			s.logf("github tar seed unavailable: resolve clone manifest cursor failed: %v", err)
			return false, nil
		}
	}
	if cursor == "" {
		s.logf("github tar seed unavailable: clone manifest has no event cursor")
		return false, nil
	}

	tree, maxObservedRevision, err := s.githubWorkingTreeSnapshot(ctx, prog)
	if err != nil {
		s.logf("github tar seed unavailable: tree verification snapshot failed: %v", err)
		return false, nil
	}
	if len(tree) == 0 {
		return false, nil
	}

	tarBody, err := client.ExportGithubWorkingTreeTar(ctx, s.workspace, GithubWorkingTreeSeedRequest{
		Owner:      s.githubWorkingTree.Owner,
		Repo:       s.githubWorkingTree.Repo,
		PathPrefix: s.githubWorkingTree.ContentsRoot,
		HeadSHA:    headSHA,
		Gzip:       false,
	})
	if err != nil {
		if exportSnapshotUnsupported(err) {
			return false, nil
		}
		s.recordCloudFailure(err)
		return true, err
	}
	defer tarBody.Body.Close()
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
	files, err := client.ExportFiles(exportCtx, s.workspace, s.remoteRoot)
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
		gz, err := gzip.NewReader(buffered)
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
		header, err := tr.Next()
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
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		hash := hashBytes(data)
		if hash != meta.ContentHash {
			return nil, fmt.Errorf("github tar seed contentHash mismatch for %s: tar=%s tree=%s", rel, hash, meta.ContentHash)
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

func (s *Syncer) pullRemoteFullTree(ctx context.Context, conflicted map[string]struct{}, prog bootstrapProgress) error {
	remotePaths := map[string]struct{}{}
	// Resumable bootstrap: if a prior bootstrap was interrupted mid-tree,
	// pick traversal back up from the persisted cursor rather than
	// re-reading everything. startedFromEmpty tracks whether this process
	// traversed the WHOLE tree (empty start -> NextCursor==nil): only then
	// is the snapshot delete pass authoritative. Resuming from a persisted
	// cursor means we did not observe the full remote set this cycle, so
	// the delete pass is skipped (next full cycle does the authoritative
	// delete) — preserving the #164/#165 mount-root-clobber invariants.
	cursor := ""
	if !s.state.BootstrapComplete && strings.TrimSpace(s.state.BootstrapCursor) != "" {
		cursor = s.state.BootstrapCursor
		s.logf("resuming bootstrap full-tree pull from persisted cursor (%d files already synced)", s.state.BootstrapFilesSynced)
	}
	startedFromEmpty := cursor == ""
	if s.state.BootstrapStartedAt == "" {
		s.state.BootstrapStartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	maxObservedRevision := ""
	for {
		page, err := s.client.ListTree(ctx, s.workspace, s.remoteRoot, 200, cursor)
		if err != nil {
			s.recordCloudFailure(err)
			return err
		}
		s.recordCloudSuccess()
		prog.touch()
		filesThisPage := 0
		readJobs := make([]bootstrapReadJob, 0, len(page.Entries))
		for _, entry := range page.Entries {
			if entry.Type != "file" {
				continue
			}
			if revisionAdvances(maxObservedRevision, entry.Revision) {
				maxObservedRevision = entry.Revision
			}
			remotePath := normalizeRemotePath(entry.Path)
			if !isUnderRemoteRoot(s.remoteRoot, remotePath) {
				continue
			}
			// Contract: lazy GitHub repos do not eagerly hydrate per-repo content at startup.
			if s.lazyRepos && isUnderLazyGithubRepoSubtree(s.remoteRoot, remotePath) {
				continue
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
			readJobs = append(readJobs, bootstrapReadJob{
				Index:      len(readJobs),
				RemotePath: remotePath,
			})
		}
		for _, result := range s.readBootstrapFiles(ctx, readJobs, prog) {
			if result.Err != nil {
				var httpErr *HTTPError
				if errors.As(result.Err, &httpErr) && httpErr.StatusCode == http.StatusForbidden {
					s.logf("skipping denied file: %s", result.RemotePath)
					if markErr := s.markReadDenied(result.RemotePath); markErr != nil {
						return markErr
					}
					continue
				}
				return result.Err
			}
			if err := s.applyRemoteFile(result.RemotePath, result.File, conflicted); err != nil {
				return err
			}
			prog.touch()
			remotePaths[result.RemotePath] = struct{}{}
			filesThisPage++
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = *page.NextCursor
		// Persist the resume point + progress so an interrupted bootstrap
		// (timeout, crash, watchdog cancel) picks up here next cycle
		// instead of restarting the whole tree.
		if !s.state.BootstrapComplete {
			s.state.BootstrapCursor = cursor
			s.state.BootstrapFilesSynced += filesThisPage
			prog.touch()
			if err := s.saveState(); err != nil {
				return err
			}
			prog.touch()
		}
	}

	// Resumed/partial traversal safety: only run the authoritative
	// snapshot delete pass when this process traversed the ENTIRE tree
	// (started from an empty cursor and reached NextCursor==nil). If the
	// traversal began from a persisted resume cursor, remotePaths only
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
	if tracked, ok := s.state.Files[remotePath]; ok {
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
		return false, err
	}
	s.state.Files[remotePath] = trackedFile{
		Revision:    entry.Revision,
		ContentType: snapshot.ContentType,
		Hash:        snapshot.Hash,
		Dirty:       false,
		Denied:      false,
		ReadOnly:    !canWrite,
	}
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
	s.state.BootstrapCursor = ""
	s.state.BootstrapStartedAt = ""
	s.state.BootstrapFilesSynced = 0
	s.state.BootstrapFilesTotal = 0
	// One-shot escape hatch / clobber-remnant recovery: after a single
	// successful full reconcile, clear the in-memory force flag so
	// subsequent cycles can use the fast-path again.
	s.forceFullReconcile = false
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
//  2. Refuse when the observedRevision does not strictly advance past
//     state.LastAppliedRevision — an older or equal listing must not
//     authorize deletes.
//  3. For every tracked path missing from the fresh listing, write or
//     confirm a tombstone under .relay/pending-deletes. Only the second
//     consecutive confirmation actually deletes; the first observation
//     is recorded and skipped.
//  4. After the pass, prune tombstones for paths that have reappeared.
//  5. On a clean pass, advance state.LastAppliedRevision.
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

	// Revision gate: refuse to act on a listing that does not strictly
	// advance the highest-applied revision. revisionAdvances treats an
	// empty observedRevision as "unknown" — which is also refused. An
	// empty stored LastAppliedRevision allows the first advancement.
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
			continue
		}
		stillMissing[remotePath] = struct{}{}
		allow, tErr := s.observePendingDelete(remotePath, observedRevision)
		if tErr != nil {
			s.logf("tombstone update failed for %s: %v", remotePath, tErr)
			continue
		}
		if !allow {
			// First observation (or aged-out reset) — record and skip.
			continue
		}
		if err := s.applyRemoteDelete(remotePath, conflicted); err != nil {
			return err
		}
		// Confirmed delete fired; clear the marker.
		s.removeTombstone(remotePath)
		delete(stillMissing, remotePath)
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
// observed is never an advancement.
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
			s.markIncrementalCheckpoint(pageStartCursor, pageCursor, "changed", remotePath)
			continue
		}
		file, err := s.client.ReadFile(ctx, s.workspace, remotePath)
		if err != nil {
			var httpErr *HTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
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
		Revision:    revision,
		ContentType: snapshot.ContentType,
		Hash:        snapshot.Hash,
		Dirty:       false,
		Denied:      false,
		ReadOnly:    !canWrite,
	}
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
		localPath, err := s.remoteToLocalPath(remotePath)
		if err != nil {
			return nil
		}
		if err := s.assertNotMountRoot(localPath); err != nil {
			s.logf("skipping remote file %s: %v", remotePath, err)
			return nil
		}
		if err := s.applyLocalPermissions(localPath, canWrite); err != nil {
			return err
		}
		s.state.Files[remotePath] = tracked
		return nil
	}
	localPath, err := s.remoteToLocalPath(remotePath)
	if err != nil {
		return nil
	}
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
		localPath, err := s.remoteToLocalPath(remotePath)
		if err != nil {
			return nil, err
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
			return nil
		}
		// Data-loss guard: skip any top-level entry whose name collides
		// with the mount directory's own basename (round-trip-onto-root).
		if rel, relErr := filepath.Rel(s.localRoot, path); relErr == nil {
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
		results[remotePath] = snapshot
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
	if s.githubWorkingTree != nil && strings.TrimSpace(s.state.GithubWorkingTreeHeadSHA) != "" {
		s.githubWorkingTree.HeadSHA = strings.TrimSpace(s.state.GithubWorkingTreeHeadSHA)
	}
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
	if s.githubWorkingTree != nil {
		if rel, ok := s.githubWorkingTree.remotePathToWorkingTreeRel(remotePath); ok {
			return safeLocalPath(s.localRoot, rel)
		}
	}
	return remoteToLocalPath(s.localRoot, s.remoteRoot, remotePath)
}

func (s *Syncer) localPathToRemotePath(localPath string, githubPathIndex map[string]string) (string, error) {
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
	if s.githubWorkingTree != nil {
		if remotePath := s.githubRemotePathForWorkingTreeRel(relativePath, nil); remotePath != "" {
			return remotePath
		}
		return s.githubWorkingTree.workingTreeRelToRemotePath(relativePath)
	}
	return normalizeRemotePath(filepath.Join(s.remoteRoot, filepath.FromSlash(relativePath)))
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
	base := filepath.Base(path)
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
		return fmt.Errorf("refusing to replace directory %s with a file", path)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	committed = true
	return nil
}
