package mountfuse

import (
	"context"
	"syscall"
	"testing"

	"github.com/agentworkforce/relayfile/internal/mountsync"
)

// fakeRemoteClient implements mountsync.RemoteClient for testing the
// FuseClient wrapper. Each method returns pre-configured data or injected
// errors.
type fakeRemoteClient struct {
	files map[string]mountsync.RemoteFile
	trees map[string]mountsync.TreeResponse

	// Optional error injection — when non-nil the corresponding method
	// returns this error instead of looking up data.
	readFileErr  error
	writeFileErr error
	deleteErr    error
	listTreeErr  error
}

func (f *fakeRemoteClient) ListTree(_ context.Context, _, path string, _ int, _ string) (mountsync.TreeResponse, error) {
	if f.listTreeErr != nil {
		return mountsync.TreeResponse{}, f.listTreeErr
	}
	resp, ok := f.trees[path]
	if !ok {
		return mountsync.TreeResponse{}, &mountsync.HTTPError{StatusCode: 404, Code: "not_found", Message: "tree not found"}
	}
	return resp, nil
}

func (f *fakeRemoteClient) ListEvents(_ context.Context, _, _, _ string, _ int) (mountsync.EventFeed, error) {
	return mountsync.EventFeed{}, nil
}

func (f *fakeRemoteClient) ReadFile(_ context.Context, _, path string) (mountsync.RemoteFile, error) {
	if f.readFileErr != nil {
		return mountsync.RemoteFile{}, f.readFileErr
	}
	file, ok := f.files[path]
	if !ok {
		return mountsync.RemoteFile{}, &mountsync.HTTPError{StatusCode: 404, Code: "not_found", Message: "file not found"}
	}
	return file, nil
}

func (f *fakeRemoteClient) WriteFile(_ context.Context, _, path, _, _, _ string) (mountsync.WriteResult, error) {
	if f.writeFileErr != nil {
		return mountsync.WriteResult{}, f.writeFileErr
	}
	return mountsync.WriteResult{TargetRevision: "r_new"}, nil
}

func (f *fakeRemoteClient) WriteFilesBulk(_ context.Context, _ string, _ []mountsync.BulkWriteFile) (mountsync.BulkWriteResponse, error) {
	// mountfuse tests never exercise the bulk-write path — the daemon uses
	// WriteFile directly. Return an empty response; if a future test ever
	// exercises bulk here, widen this stub with fields + error toggles.
	return mountsync.BulkWriteResponse{}, nil
}

func (f *fakeRemoteClient) DeleteFile(_ context.Context, _, path, _ string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return nil
}

// ---------------------------------------------------------------------------
// FuseClient tests
// ---------------------------------------------------------------------------

func TestFuseClientLookupFile(t *testing.T) {
	remote := &fakeRemoteClient{
		files: map[string]mountsync.RemoteFile{
			"/hello.txt": {Path: "/hello.txt", Revision: "r1", ContentType: "text/plain", Content: "hi"},
		},
	}
	fc := NewFuseClient(remote, "ws1", "/")

	file, errno := fc.LookupFile(context.Background(), "/hello.txt")
	if errno != 0 {
		t.Fatalf("LookupFile errno = %d, want 0", errno)
	}
	if file.Revision != "r1" {
		t.Errorf("Revision = %q, want %q", file.Revision, "r1")
	}
	if file.Content != "hi" {
		t.Errorf("Content = %q, want %q", file.Content, "hi")
	}
}

func TestFuseClientLookupFile404(t *testing.T) {
	remote := &fakeRemoteClient{
		readFileErr: &mountsync.HTTPError{StatusCode: 404, Code: "not_found", Message: "not found"},
	}
	fc := NewFuseClient(remote, "ws1", "/")

	_, errno := fc.LookupFile(context.Background(), "/missing.txt")
	if errno != syscall.ENOENT {
		t.Errorf("errno = %d, want ENOENT (%d)", errno, syscall.ENOENT)
	}
}

func TestFuseClientLookupFile403(t *testing.T) {
	remote := &fakeRemoteClient{
		readFileErr: &mountsync.HTTPError{StatusCode: 403, Code: "forbidden", Message: "forbidden"},
	}
	fc := NewFuseClient(remote, "ws1", "/")

	_, errno := fc.LookupFile(context.Background(), "/secret.txt")
	if errno != syscall.EPERM {
		t.Errorf("errno = %d, want EPERM (%d)", errno, syscall.EPERM)
	}
}

func TestFuseClientListDir(t *testing.T) {
	remote := &fakeRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/docs": {
				Path: "/docs",
				Entries: []mountsync.TreeEntry{
					{Path: "/docs/a.txt", Type: "file", Revision: "r1"},
					{Path: "/docs/b.txt", Type: "file", Revision: "r2"},
				},
			},
		},
	}
	fc := NewFuseClient(remote, "ws1", "/")

	entries, errno := fc.ListDir(context.Background(), "/docs")
	if errno != 0 {
		t.Fatalf("ListDir errno = %d, want 0", errno)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].Path != "/docs/a.txt" {
		t.Errorf("entries[0].Path = %q, want %q", entries[0].Path, "/docs/a.txt")
	}
}

func TestFuseClientPutFile(t *testing.T) {
	remote := &fakeRemoteClient{}
	fc := NewFuseClient(remote, "ws1", "/")

	result, errno := fc.PutFile(context.Background(), "/new.txt", "0", "text/plain", "content")
	if errno != 0 {
		t.Fatalf("PutFile errno = %d, want 0", errno)
	}
	if result.TargetRevision != "r_new" {
		t.Errorf("TargetRevision = %q, want %q", result.TargetRevision, "r_new")
	}
}

func TestFuseClientPutFileConflict(t *testing.T) {
	remote := &fakeRemoteClient{
		writeFileErr: &mountsync.ConflictError{Path: "/conflict.txt"},
	}
	fc := NewFuseClient(remote, "ws1", "/")

	_, errno := fc.PutFile(context.Background(), "/conflict.txt", "r_old", "text/plain", "content")
	if errno != syscall.EAGAIN {
		t.Errorf("errno = %d, want EAGAIN (%d)", errno, syscall.EAGAIN)
	}
}

func TestFuseClientRemoveFile(t *testing.T) {
	remote := &fakeRemoteClient{}
	fc := NewFuseClient(remote, "ws1", "/")

	errno := fc.RemoveFile(context.Background(), "/delete-me.txt", "r1")
	if errno != 0 {
		t.Errorf("RemoveFile errno = %d, want 0", errno)
	}
}

func TestFuseClientRemoveFile403(t *testing.T) {
	remote := &fakeRemoteClient{
		deleteErr: &mountsync.HTTPError{StatusCode: 403, Code: "forbidden", Message: "forbidden"},
	}
	fc := NewFuseClient(remote, "ws1", "/")

	errno := fc.RemoveFile(context.Background(), "/protected.txt", "r1")
	if errno != syscall.EPERM {
		t.Errorf("errno = %d, want EPERM (%d)", errno, syscall.EPERM)
	}
}

func TestMapError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want syscall.Errno
	}{
		{
			name: "nil error",
			err:  nil,
			want: 0,
		},
		{
			name: "HTTP 400",
			err:  &mountsync.HTTPError{StatusCode: 400, Code: "bad_request", Message: "bad"},
			want: syscall.EIO,
		},
		{
			name: "HTTP 403",
			err:  &mountsync.HTTPError{StatusCode: 403, Code: "forbidden", Message: "forbidden"},
			want: syscall.EPERM,
		},
		{
			name: "HTTP 404",
			err:  &mountsync.HTTPError{StatusCode: 404, Code: "not_found", Message: "not found"},
			want: syscall.ENOENT,
		},
		{
			name: "HTTP 409",
			err:  &mountsync.HTTPError{StatusCode: 409, Code: "conflict", Message: "conflict"},
			want: syscall.EAGAIN,
		},
		{
			name: "HTTP 429",
			err:  &mountsync.HTTPError{StatusCode: 429, Code: "rate_limit", Message: "too many"},
			want: syscall.EAGAIN,
		},
		{
			name: "HTTP 500",
			err:  &mountsync.HTTPError{StatusCode: 500, Code: "internal", Message: "internal error"},
			want: syscall.EIO,
		},
		{
			name: "HTTP 503",
			err:  &mountsync.HTTPError{StatusCode: 503, Code: "unavailable", Message: "unavailable"},
			want: syscall.EIO,
		},
		{
			name: "ConflictError",
			err:  &mountsync.ConflictError{Path: "/x.txt"},
			want: syscall.EAGAIN,
		},
		{
			name: "ErrConflict sentinel",
			err:  mountsync.ErrConflict,
			want: syscall.EAGAIN,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapError(tt.err)
			if got != tt.want {
				t.Errorf("mapError(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}
