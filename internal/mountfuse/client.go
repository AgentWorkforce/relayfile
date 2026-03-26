package mountfuse

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountsync"
)

// FuseClient wraps a mountsync.RemoteClient and maps HTTP errors to
// syscall.Errno values suitable for FUSE responses.
type FuseClient struct {
	remote      mountsync.RemoteClient
	workspaceID string
	remotePath  string // prefix to prepend to all paths
}

type HTTPClient struct {
	*FuseClient
	baseURL string
	token   string
}

// NewFuseClient creates a FuseClient that delegates to remote, scoping all
// operations to workspaceID and prepending remotePath to every path argument.
func NewFuseClient(remote mountsync.RemoteClient, workspaceID, remotePath string) *FuseClient {
	return &FuseClient{
		remote:      remote,
		workspaceID: workspaceID,
		remotePath:  remotePath,
	}
}

func NewHTTPClient(baseURL, token string, remote mountsync.RemoteClient, workspaceID, remotePath string) *HTTPClient {
	return &HTTPClient{
		FuseClient: NewFuseClient(remote, workspaceID, remotePath),
		baseURL:    strings.TrimSpace(baseURL),
		token:      strings.TrimSpace(token),
	}
}

// LookupFile fetches a single file's metadata and content.
func (c *FuseClient) LookupFile(ctx context.Context, path string) (mountsync.RemoteFile, syscall.Errno) {
	file, err := c.remote.ReadFile(ctx, c.workspaceID, c.fullPath(path))
	if err != nil {
		return mountsync.RemoteFile{}, mapError(err)
	}
	return file, 0
}

// ListDir lists directory contents at depth=1.
func (c *FuseClient) ListDir(ctx context.Context, path string) ([]mountsync.TreeEntry, syscall.Errno) {
	resp, err := c.remote.ListTree(ctx, c.workspaceID, c.fullPath(path), 1, "")
	if err != nil {
		return nil, mapError(err)
	}
	return resp.Entries, 0
}

// PutFile writes or creates a file with optimistic concurrency via revision.
func (c *FuseClient) PutFile(ctx context.Context, path, revision, contentType, content string) (mountsync.WriteResult, syscall.Errno) {
	result, err := c.remote.WriteFile(ctx, c.workspaceID, c.fullPath(path), revision, contentType, content)
	if err != nil {
		return mountsync.WriteResult{}, mapError(err)
	}
	return result, 0
}

// RemoveFile deletes a file at the given path using revision for concurrency.
func (c *FuseClient) RemoveFile(ctx context.Context, path, revision string) syscall.Errno {
	err := c.remote.DeleteFile(ctx, c.workspaceID, c.fullPath(path), revision)
	if err != nil {
		return mapError(err)
	}
	return 0
}

// fullPath joins the remotePath prefix with the FUSE path, avoiding double slashes.
func (c *FuseClient) fullPath(fusePath string) string {
	prefix := strings.TrimRight(c.remotePath, "/")
	suffix := fusePath
	if !strings.HasPrefix(suffix, "/") {
		suffix = "/" + suffix
	}
	if prefix == "" || prefix == "/" {
		return suffix
	}
	return prefix + suffix
}

// mapError converts an error from the remote client into a syscall.Errno.
func mapError(err error) syscall.Errno {
	if err == nil {
		return 0
	}

	var httpErr *mountsync.HTTPError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.StatusCode == 403:
			return syscall.EPERM
		case httpErr.StatusCode == 404:
			return syscall.ENOENT
		case httpErr.StatusCode == 409:
			return syscall.EAGAIN
		case httpErr.StatusCode == 429 || (httpErr.StatusCode >= 500 && httpErr.StatusCode <= 599):
			return syscall.EIO
		default:
			return syscall.EIO
		}
	}

	if errors.Is(err, mountsync.ErrConflict) {
		return syscall.EAGAIN
	}

	return syscall.EIO
}

func (c *HTTPClient) websocketURL(workspaceID string) (string, error) {
	if c == nil {
		return "", errors.New("mountfuse: nil HTTP client")
	}
	if strings.TrimSpace(workspaceID) == "" {
		workspaceID = c.workspaceID
	}
	parsed, err := url.Parse(strings.TrimSpace(c.baseURL))
	if err != nil {
		return "", err
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", errors.New("mountfuse: unsupported websocket scheme")
	}
	parsed.Path = "/v1/workspaces/" + url.PathEscape(workspaceID) + "/fs/ws"
	query := parsed.Query()
	query.Set("token", c.token)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func waitWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
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
