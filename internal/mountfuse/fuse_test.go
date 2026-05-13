package mountfuse

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/agentworkforce/relayfile/internal/mountsync"
	gofusefs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// fakeRemoteClient implements mountsync.RemoteClient for testing the
// FuseClient wrapper. Each method returns pre-configured data or injected
// errors.
type fakeRemoteClient struct {
	files        map[string]mountsync.RemoteFile
	trees        map[string]mountsync.TreeResponse
	listTreeFunc func(path string) (mountsync.TreeResponse, error)

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
	if f.listTreeFunc != nil {
		return f.listTreeFunc(normalizeRemotePath(path))
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

func (f *fakeRemoteClient) DeleteFile(_ context.Context, _, path, baseRevision string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	_ = path
	_ = baseRevision
	return nil
}

type fakeLazyRemoteClient struct {
	*fakeRemoteClient

	mu                      sync.Mutex
	lazyMaterializeCalls    int32
	lazyMaterializeRequests []string
	lazyMaterializeFunc     func(ctx context.Context, workspaceID, owner, repo string) error
	materialized            map[string]bool
}

func (f *fakeLazyRemoteClient) LazyMaterialize(ctx context.Context, workspaceID, owner, repo string) error {
	atomic.AddInt32(&f.lazyMaterializeCalls, 1)
	f.mu.Lock()
	f.lazyMaterializeRequests = append(f.lazyMaterializeRequests, owner+"/"+repo)
	f.mu.Unlock()
	if f.lazyMaterializeFunc != nil {
		return f.lazyMaterializeFunc(ctx, workspaceID, owner, repo)
	}
	f.setMaterialized(owner, repo)
	return nil
}

func (f *fakeLazyRemoteClient) setMaterialized(owner, repo string) {
	key := owner + "/" + repo
	f.mu.Lock()
	if f.materialized == nil {
		f.materialized = make(map[string]bool)
	}
	f.materialized[key] = true
	f.mu.Unlock()
}

func (f *fakeLazyRemoteClient) isMaterialized(owner, repo string) bool {
	key := owner + "/" + repo
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.materialized[key]
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

func TestFuseClientLazyMaterialize(t *testing.T) {
	remote := &fakeLazyRemoteClient{fakeRemoteClient: &fakeRemoteClient{}}
	fc := NewFuseClient(remote, "ws1", "/")

	errno := fc.LazyMaterialize(context.Background(), "octocat", "hello-world")
	if errno != 0 {
		t.Fatalf("LazyMaterialize errno = %d, want 0", errno)
	}
	if got := atomic.LoadInt32(&remote.lazyMaterializeCalls); got != 1 {
		t.Fatalf("LazyMaterialize calls = %d, want 1", got)
	}
}

func TestFuseClientLazyMaterializeNoOpWhenUnavailable(t *testing.T) {
	fc := NewFuseClient(&fakeRemoteClient{}, "ws1", "/")
	if errno := fc.LazyMaterialize(context.Background(), "octocat", "hello-world"); errno != 0 {
		t.Fatalf("LazyMaterialize errno = %d, want 0", errno)
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

func TestFuseAliasByTitleResolves(t *testing.T) {
	t.Parallel()

	const (
		workspaceID = "ws_alias_by_title"
		jsonBody    = `{"id":"page-123","title":"Foo"}`
		spaceBody   = `{"id":"page-456","title":"Khaliq's To Dos"}`
		unicodeBody = `{"id":"page-789","title":"unicode alias"}`
		plainBody   = "plain alias body"
	)

	remote := &fakeRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/": {
				Path: "/",
				Entries: []mountsync.TreeEntry{
					{Path: "/notion", Type: "directory"},
				},
			},
			"/notion": {
				Path: "/notion",
				Entries: []mountsync.TreeEntry{
					{Path: "/notion/pages", Type: "directory"},
				},
			},
			"/notion/pages": {
				Path: "/notion/pages",
				Entries: []mountsync.TreeEntry{
					{Path: "/notion/pages/" + aliasByTitleSegment, Type: "directory"},
					{Path: "/notion/pages/page-123.json", Type: "file", Revision: "r-page"},
				},
			},
			"/notion/pages/" + aliasByTitleSegment: {
				Path: "/notion/pages/" + aliasByTitleSegment,
				Entries: []mountsync.TreeEntry{
					{Path: "/notion/pages/" + aliasByTitleSegment + "/foo.json", Type: "file", Revision: "r-foo"},
					{Path: "/notion/pages/" + aliasByTitleSegment + "/Khaliq's To Dos.json", Type: "file", Revision: "r-space"},
					{Path: "/notion/pages/" + aliasByTitleSegment + "/Smørbrød-årsplan.json", Type: "file", Revision: "r-unicode"},
					{Path: "/notion/pages/" + aliasByTitleSegment + "/Foo Bar", Type: "file", Revision: "r-plain"},
				},
			},
		},
		files: map[string]mountsync.RemoteFile{
			"/notion/pages/" + aliasByTitleSegment + "/foo.json": {
				Path:        "/notion/pages/" + aliasByTitleSegment + "/foo.json",
				Revision:    "r-foo",
				ContentType: "application/json",
				Content:     jsonBody,
			},
			"/notion/pages/" + aliasByTitleSegment + "/Khaliq's To Dos.json": {
				Path:        "/notion/pages/" + aliasByTitleSegment + "/Khaliq's To Dos.json",
				Revision:    "r-space",
				ContentType: "application/vnd.alias+json",
				Content:     spaceBody,
			},
			"/notion/pages/" + aliasByTitleSegment + "/Smørbrød-årsplan.json": {
				Path:        "/notion/pages/" + aliasByTitleSegment + "/Smørbrød-årsplan.json",
				Revision:    "r-unicode",
				ContentType: "application/json",
				Content:     unicodeBody,
			},
			"/notion/pages/" + aliasByTitleSegment + "/Foo Bar": {
				Path:     "/notion/pages/" + aliasByTitleSegment + "/Foo Bar",
				Revision: "r-plain",
				Content:  plainBody,
			},
		},
	}

	root := newMountTestRoot(t, remote, workspaceID)
	notion := lookupDir(t, root, "notion")
	pages := lookupDir(t, notion, "pages")
	aliases := lookupDir(t, pages, aliasByTitleSegment)

	tests := []struct {
		name            string
		filename        string
		wantContent     string
		wantContentType string
	}{
		{
			name:            "json alias",
			filename:        "foo.json",
			wantContent:     jsonBody,
			wantContentType: "application/json",
		},
		{
			name:            "space and apostrophe alias",
			filename:        "Khaliq's To Dos.json",
			wantContent:     spaceBody,
			wantContentType: "application/vnd.alias+json",
		},
		{
			name:            "unicode alias",
			filename:        "Smørbrød-årsplan.json",
			wantContent:     unicodeBody,
			wantContentType: "application/json",
		},
		{
			name:            "no extension alias",
			filename:        "Foo Bar",
			wantContent:     plainBody,
			wantContentType: contentTypeForPath("/notion/pages/" + aliasByTitleSegment + "/Foo Bar"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fileNode, entryOut := lookupFile(t, aliases, tt.filename)
			if entryOut.Attr.Mode&syscall.S_IFMT != syscall.S_IFREG {
				t.Fatalf("Lookup(%q) mode = %o, want regular file", tt.filename, entryOut.Attr.Mode)
			}
			gotContent, gotContentType := readFileContent(t, fileNode)
			if gotContent != tt.wantContent {
				t.Fatalf("read %q = %q, want %q", tt.filename, gotContent, tt.wantContent)
			}
			if gotContentType != tt.wantContentType {
				t.Fatalf("content type for %q = %q, want %q", tt.filename, gotContentType, tt.wantContentType)
			}
		})
	}
}

func TestFuseAliasByIDResolves(t *testing.T) {
	t.Parallel()

	const body = `{"identifier":"AGE-8","title":"Alias by ID"}`

	remote := &fakeRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/": {
				Path: "/",
				Entries: []mountsync.TreeEntry{
					{Path: "/linear", Type: "directory"},
				},
			},
			"/linear": {
				Path: "/linear",
				Entries: []mountsync.TreeEntry{
					{Path: "/linear/issues", Type: "directory"},
				},
			},
			"/linear/issues": {
				Path: "/linear/issues",
				Entries: []mountsync.TreeEntry{
					{Path: "/linear/issues/" + aliasByIDSegment, Type: "directory"},
				},
			},
			"/linear/issues/" + aliasByIDSegment: {
				Path: "/linear/issues/" + aliasByIDSegment,
				Entries: []mountsync.TreeEntry{
					{Path: "/linear/issues/" + aliasByIDSegment + "/AGE-8.json", Type: "file", Revision: "r-age-8"},
				},
			},
		},
		files: map[string]mountsync.RemoteFile{
			"/linear/issues/" + aliasByIDSegment + "/AGE-8.json": {
				Path:        "/linear/issues/" + aliasByIDSegment + "/AGE-8.json",
				Revision:    "r-age-8",
				ContentType: "application/json",
				Content:     body,
			},
		},
	}

	root := newMountTestRoot(t, remote, "ws_alias_by_id")
	linear := lookupDir(t, root, "linear")
	issues := lookupDir(t, linear, "issues")
	aliases := lookupDir(t, issues, aliasByIDSegment)
	fileNode, _ := lookupFile(t, aliases, "AGE-8.json")
	gotContent, _ := readFileContent(t, fileNode)
	if gotContent != body {
		t.Fatalf("read AGE-8.json = %q, want %q", gotContent, body)
	}
}

func TestFuseAliasReaddirIncludesByTitle(t *testing.T) {
	t.Parallel()

	remote := &fakeRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/": {
				Path: "/",
				Entries: []mountsync.TreeEntry{
					{Path: "/notion", Type: "directory"},
				},
			},
			"/notion": {
				Path: "/notion",
				Entries: []mountsync.TreeEntry{
					{Path: "/notion/pages", Type: "directory"},
				},
			},
			"/notion/pages": {
				Path: "/notion/pages",
				Entries: []mountsync.TreeEntry{
					{Path: "/notion/pages/" + aliasByTitleSegment, Type: "directory"},
					{Path: "/notion/pages/page-123.json", Type: "file", Revision: "r-page"},
				},
			},
			"/notion/pages/" + aliasByTitleSegment: {
				Path:    "/notion/pages/" + aliasByTitleSegment,
				Entries: nil,
			},
		},
		files: map[string]mountsync.RemoteFile{
			"/notion/pages/page-123.json": {
				Path:        "/notion/pages/page-123.json",
				Revision:    "r-page",
				ContentType: "application/json",
				Content:     `{"id":"page-123"}`,
			},
		},
	}

	root := newMountTestRoot(t, remote, "ws_alias_readdir")
	notion := lookupDir(t, root, "notion")
	pages := lookupDir(t, notion, "pages")

	names := readdirNames(t, pages)
	if !equalSorted(names, []string{aliasByTitleSegment, "page-123.json"}) {
		t.Fatalf("Readdir(/notion/pages) = %v, want %v", names, []string{aliasByTitleSegment, "page-123.json"})
	}

	aliasDir := lookupDir(t, pages, aliasByTitleSegment)
	fileNode, entryOut := lookupFile(t, pages, "page-123.json")
	if entryOut.Attr.Mode&syscall.S_IFMT != syscall.S_IFREG {
		t.Fatalf("Lookup(page-123.json) mode = %o, want regular file", entryOut.Attr.Mode)
	}
	if _, ok := aliasDir.Operations().(*DirNode); !ok {
		t.Fatalf("Lookup(%q) returned non-directory child", aliasByTitleSegment)
	}
	if got := readdirNames(t, aliasDir); len(got) != 0 {
		t.Fatalf("Readdir(%q) = %v, want empty alias directory", aliasByTitleSegment, got)
	}
	if content, _ := readFileContent(t, fileNode); content != `{"id":"page-123"}` {
		t.Fatalf("read page-123.json = %q, want page content", content)
	}
}

func TestFuseAliasCollisionWithRealFile(t *testing.T) {
	t.Parallel()

	remote := &fakeRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/": {
				Path: "/",
				Entries: []mountsync.TreeEntry{
					{Path: "/notion", Type: "directory"},
				},
			},
			"/notion": {
				Path: "/notion",
				Entries: []mountsync.TreeEntry{
					{Path: "/notion/pages", Type: "directory"},
				},
			},
			"/notion/pages": {
				Path: "/notion/pages",
				Entries: []mountsync.TreeEntry{
					{Path: "/notion/pages/" + aliasByTitleSegment, Type: "directory"},
				},
			},
			"/notion/pages/" + aliasByTitleSegment: {
				Path: "/notion/pages/" + aliasByTitleSegment,
				Entries: []mountsync.TreeEntry{
					{Path: "/notion/pages/" + aliasByTitleSegment + "/foo.json", Type: "file", Revision: "r-foo"},
					{Path: "/notion/pages/" + aliasByTitleSegment + "/foo.json.bak", Type: "file", Revision: "r-foo-bak"},
				},
			},
		},
		files: map[string]mountsync.RemoteFile{
			"/notion/pages/" + aliasByTitleSegment + "/foo.json": {
				Path:        "/notion/pages/" + aliasByTitleSegment + "/foo.json",
				Revision:    "r-foo",
				ContentType: "application/json",
				Content:     `{"name":"foo"}`,
			},
			"/notion/pages/" + aliasByTitleSegment + "/foo.json.bak": {
				Path:        "/notion/pages/" + aliasByTitleSegment + "/foo.json.bak",
				Revision:    "r-foo-bak",
				ContentType: "application/octet-stream",
				Content:     "backup",
			},
		},
	}

	root := newMountTestRoot(t, remote, "ws_alias_collision")
	aliases := lookupDir(t, lookupDir(t, lookupDir(t, root, "notion"), "pages"), aliasByTitleSegment)

	names := readdirNames(t, aliases)
	if !equalSorted(names, []string{"foo.json", "foo.json.bak"}) {
		t.Fatalf("Readdir(alias dir) = %v, want both colliding names", names)
	}

	foo, _ := lookupFile(t, aliases, "foo.json")
	fooBak, _ := lookupFile(t, aliases, "foo.json.bak")
	if got, _ := readFileContent(t, foo); got != `{"name":"foo"}` {
		t.Fatalf("read foo.json = %q, want primary content", got)
	}
	if got, _ := readFileContent(t, fooBak); got != "backup" {
		t.Fatalf("read foo.json.bak = %q, want backup content", got)
	}
}

func TestFuseAliasMissingDirectoryReturnsENOENT(t *testing.T) {
	t.Parallel()

	root := newMountTestRoot(t, &fakeRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/": {
				Path: "/",
				Entries: []mountsync.TreeEntry{
					{Path: "/notion", Type: "directory"},
				},
			},
			"/notion": {
				Path: "/notion",
				Entries: []mountsync.TreeEntry{
					{Path: "/notion/pages", Type: "directory"},
				},
			},
			"/notion/pages": {
				Path: "/notion/pages",
				Entries: []mountsync.TreeEntry{
					{Path: "/notion/pages/page-123.json", Type: "file", Revision: "r-page"},
				},
			},
		},
	}, "ws_alias_missing")

	pages := lookupDir(t, lookupDir(t, root, "notion"), "pages")
	if names := readdirNames(t, pages); !equalSorted(names, []string{"page-123.json"}) {
		t.Fatalf("Readdir(/notion/pages) = %v, want no synthesized alias directories", names)
	}

	var missingOut fuse.EntryOut
	if _, errno := pages.Lookup(context.Background(), aliasByTitleSegment, &missingOut); errno != syscall.ENOENT {
		t.Fatalf("Lookup(%q) errno = %d, want ENOENT", aliasByTitleSegment, errno)
	}
	if got := missingOut.EntryTimeout(); got != pages.state.negativeTTL {
		t.Fatalf("negative lookup timeout = %s, want %s", got, pages.state.negativeTTL)
	}

	if _, lookupErrno := root.Lookup(context.Background(), layoutFilename, &fuse.EntryOut{}); lookupErrno != 0 {
		t.Fatalf("Lookup(%q) after missing alias lookup errno = %d, want 0", layoutFilename, lookupErrno)
	}
}

func TestFuseAliasReaddirRefreshesAfterInvalidation(t *testing.T) {
	t.Parallel()

	remote := &fakeRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/": {
				Path: "/",
				Entries: []mountsync.TreeEntry{
					{Path: "/notion", Type: "directory"},
				},
			},
			"/notion": {
				Path: "/notion",
				Entries: []mountsync.TreeEntry{
					{Path: "/notion/pages", Type: "directory"},
				},
			},
			"/notion/pages": {
				Path: "/notion/pages",
				Entries: []mountsync.TreeEntry{
					{Path: "/notion/pages/page-123.json", Type: "file", Revision: "r-page"},
				},
			},
		},
	}

	root := newMountTestRoot(t, remote, "ws_alias_invalidate")
	pages := lookupDir(t, lookupDir(t, root, "notion"), "pages")
	if names := readdirNames(t, pages); !equalSorted(names, []string{"page-123.json"}) {
		t.Fatalf("initial Readdir(/notion/pages) = %v, want page entry only", names)
	}

	remote.trees["/notion/pages"] = mountsync.TreeResponse{
		Path: "/notion/pages",
		Entries: []mountsync.TreeEntry{
			{Path: "/notion/pages/" + aliasByTitleSegment, Type: "directory"},
			{Path: "/notion/pages/page-123.json", Type: "file", Revision: "r-page"},
		},
	}
	remote.trees["/notion/pages/"+aliasByTitleSegment] = mountsync.TreeResponse{
		Path:    "/notion/pages/" + aliasByTitleSegment,
		Entries: nil,
	}
	root.state.invalidate("/notion/pages")

	if names := readdirNames(t, pages); !equalSorted(names, []string{aliasByTitleSegment, "page-123.json"}) {
		t.Fatalf("Readdir(/notion/pages) after invalidate = %v, want refreshed alias dir", names)
	}
}

func TestLazyMaterializeFiresOnceOnRepoStat(t *testing.T) {

	t.Run("repo stat and repeated readdir", func(t *testing.T) {
		remote := newLazyGithubRepoRemote()
		root := newLazyMountTestRoot(t, remote, "ws_lazy_once")
		repo := lookupDir(t, lookupDir(t, lookupDir(t, lookupDir(t, root, "github"), "repos"), "octocat"), "hello-world")

		var out fuse.AttrOut
		if errno := repo.Getattr(context.Background(), nil, &out); errno != 0 {
			t.Fatalf("Getattr(repo) errno = %d, want 0", errno)
		}
		first := readdirNames(t, repo)
		second := readdirNames(t, repo)
		if !equalSorted(first, []string{"_index.json", "issues"}) {
			t.Fatalf("first Readdir(repo) = %v", first)
		}
		if !equalSorted(second, first) {
			t.Fatalf("second Readdir(repo) = %v, want %v", second, first)
		}
		if got := atomic.LoadInt32(&remote.lazyMaterializeCalls); got != 1 {
			t.Fatalf("LazyMaterialize calls = %d, want 1", got)
		}
		if got := strings.Join(remote.lazyMaterializeRequests, ","); got != "octocat/hello-world" {
			t.Fatalf("LazyMaterialize requests = %q, want octocat/hello-world", got)
		}
	})

	t.Run("missing owner or repo segments do not trigger", func(t *testing.T) {
		remote := newLazyGithubRepoRemote()
		root := newLazyMountTestRoot(t, remote, "ws_lazy_missing_segments")
		repos := lookupDir(t, lookupDir(t, root, "github"), "repos")
		owner := lookupDir(t, repos, "octocat")

		var reposAttr, ownerAttr fuse.AttrOut
		if errno := repos.Getattr(context.Background(), nil, &reposAttr); errno != 0 {
			t.Fatalf("Getattr(repos) errno = %d, want 0", errno)
		}
		if errno := owner.Getattr(context.Background(), nil, &ownerAttr); errno != 0 {
			t.Fatalf("Getattr(owner) errno = %d, want 0", errno)
		}
		if got := atomic.LoadInt32(&remote.lazyMaterializeCalls); got != 0 {
			t.Fatalf("LazyMaterialize calls = %d, want 0 for incomplete repo coordinates", got)
		}
	})

	t.Run("multiple repos under same owner are independent", func(t *testing.T) {
		remote := newLazyGithubRepoRemote()
		root := newLazyMountTestRoot(t, remote, "ws_lazy_multi_repo")
		owner := lookupDir(t, lookupDir(t, lookupDir(t, root, "github"), "repos"), "octocat")
		helloWorld := lookupDir(t, owner, "hello-world")
		spoonKnife := lookupDir(t, owner, "spoon-knife")

		if errno := helloWorld.Getattr(context.Background(), nil, &fuse.AttrOut{}); errno != 0 {
			t.Fatalf("Getattr(hello-world) errno = %d, want 0", errno)
		}
		if errno := spoonKnife.Getattr(context.Background(), nil, &fuse.AttrOut{}); errno != 0 {
			t.Fatalf("Getattr(spoon-knife) errno = %d, want 0", errno)
		}
		if got := atomic.LoadInt32(&remote.lazyMaterializeCalls); got != 2 {
			t.Fatalf("LazyMaterialize calls = %d, want 2", got)
		}
		if !equalSorted(remote.lazyMaterializeRequests, []string{"octocat/hello-world", "octocat/spoon-knife"}) {
			t.Fatalf("LazyMaterialize requests = %v", remote.lazyMaterializeRequests)
		}
	})

	t.Run("concurrent stat races collapse to one rpc", func(t *testing.T) {
		remote := newLazyGithubRepoRemote()
		started := make(chan struct{}, 1)
		release := make(chan struct{})
		remote.lazyMaterializeFunc = func(_ context.Context, _ string, owner, repo string) error {
			started <- struct{}{}
			<-release
			remote.setMaterialized(owner, repo)
			return nil
		}
		root := newLazyMountTestRoot(t, remote, "ws_lazy_race")
		repo := lookupDir(t, lookupDir(t, lookupDir(t, lookupDir(t, root, "github"), "repos"), "octocat"), "hello-world")

		errnos := make(chan syscall.Errno, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		for i := 0; i < 2; i++ {
			go func() {
				defer wg.Done()
				errnos <- repo.Getattr(context.Background(), nil, &fuse.AttrOut{})
			}()
		}
		<-started
		close(release)
		wg.Wait()
		close(errnos)
		for errno := range errnos {
			if errno != 0 {
				t.Fatalf("concurrent Getattr errno = %d, want 0", errno)
			}
		}
		if got := atomic.LoadInt32(&remote.lazyMaterializeCalls); got != 1 {
			t.Fatalf("LazyMaterialize calls = %d, want 1", got)
		}
	})
}

func TestLazyReposConfigOverridesEnv(t *testing.T) {
	t.Setenv("RELAYFILE_LAZY_REPOS", "true")

	root := newMountTestRoot(t, newLazyGithubRepoRemote(), "ws_lazy_env_override")
	if root.state.lazyRepos != nil {
		t.Fatal("expected explicit Config.LazyRepos=false to ignore lazy repos env fallback")
	}
}

func TestLazyMaterializeRetriesAfterError(t *testing.T) {

	remote := newLazyGithubRepoRemote()
	remote.lazyMaterializeFunc = func(_ context.Context, _ string, owner, repo string) error {
		if atomic.LoadInt32(&remote.lazyMaterializeCalls) == 1 {
			return &mountsync.HTTPError{StatusCode: 500, Code: "internal_error", Message: "boom"}
		}
		remote.setMaterialized(owner, repo)
		return nil
	}
	root := newLazyMountTestRoot(t, remote, "ws_lazy_retry")
	repo := lookupDir(t, lookupDir(t, lookupDir(t, lookupDir(t, root, "github"), "repos"), "octocat"), "hello-world")

	if _, errno := repo.Readdir(context.Background()); errno != syscall.EIO {
		t.Fatalf("first Readdir errno = %d, want EIO (%d)", errno, syscall.EIO)
	}
	if names := readdirNames(t, repo); !equalSorted(names, []string{"_index.json", "issues"}) {
		t.Fatalf("retry Readdir(repo) = %v", names)
	}
	if got := atomic.LoadInt32(&remote.lazyMaterializeCalls); got != 2 {
		t.Fatalf("LazyMaterialize calls = %d, want 2 after retry", got)
	}
}

func TestLazyMaterializeNoOpWhenRemoteDoesNotImplement(t *testing.T) {

	remote := &fakeRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/":                     {Path: "/", Entries: []mountsync.TreeEntry{{Path: "/github", Type: "directory"}}},
			"/github":               {Path: "/github", Entries: []mountsync.TreeEntry{{Path: "/github/repos", Type: "directory"}}},
			"/github/repos":         {Path: "/github/repos", Entries: []mountsync.TreeEntry{{Path: "/github/repos/octocat", Type: "directory"}}},
			"/github/repos/octocat": {Path: "/github/repos/octocat", Entries: []mountsync.TreeEntry{{Path: "/github/repos/octocat/hello-world", Type: "directory"}}},
			"/github/repos/octocat/hello-world": {
				Path: "/github/repos/octocat/hello-world",
				Entries: []mountsync.TreeEntry{
					{Path: "/github/repos/octocat/hello-world/_index.json", Type: "file", Revision: "r-index"},
					{Path: "/github/repos/octocat/hello-world/issues", Type: "directory"},
				},
			},
		},
		files: map[string]mountsync.RemoteFile{
			"/github/repos/octocat/hello-world/_index.json": {
				Path:        "/github/repos/octocat/hello-world/_index.json",
				Revision:    "r-index",
				ContentType: "application/json",
				Content:     `{"repo":"hello-world"}`,
			},
		},
	}
	root := newLazyMountTestRoot(t, remote, "ws_lazy_noop")
	repo := lookupDir(t, lookupDir(t, lookupDir(t, lookupDir(t, root, "github"), "repos"), "octocat"), "hello-world")

	if errno := repo.Getattr(context.Background(), nil, &fuse.AttrOut{}); errno != 0 {
		t.Fatalf("Getattr(repo) errno = %d, want 0", errno)
	}
	if names := readdirNames(t, repo); !equalSorted(names, []string{"_index.json", "issues"}) {
		t.Fatalf("Readdir(repo) = %v", names)
	}
}

func TestLazyMaterializeAllowsEmptyRepoTree(t *testing.T) {

	remote := &fakeLazyRemoteClient{
		fakeRemoteClient: &fakeRemoteClient{},
		materialized:     make(map[string]bool),
	}
	remote.listTreeFunc = func(path string) (mountsync.TreeResponse, error) {
		switch path {
		case "/":
			return mountsync.TreeResponse{Path: path, Entries: []mountsync.TreeEntry{{Path: "/github", Type: "directory"}}}, nil
		case "/github":
			return mountsync.TreeResponse{Path: path, Entries: []mountsync.TreeEntry{{Path: "/github/repos", Type: "directory"}}}, nil
		case "/github/repos":
			return mountsync.TreeResponse{Path: path, Entries: []mountsync.TreeEntry{{Path: "/github/repos/octocat", Type: "directory"}}}, nil
		case "/github/repos/octocat":
			return mountsync.TreeResponse{
				Path: path,
				Entries: []mountsync.TreeEntry{
					{Path: "/github/repos/octocat/empty-repo", Type: "directory"},
				},
			}, nil
		case "/github/repos/octocat/empty-repo":
			if !remote.isMaterialized("octocat", "empty-repo") {
				return mountsync.TreeResponse{}, &mountsync.HTTPError{StatusCode: 404, Code: "not_found", Message: "not materialized"}
			}
			return mountsync.TreeResponse{Path: path, Entries: nil}, nil
		}
		return mountsync.TreeResponse{}, &mountsync.HTTPError{StatusCode: 404, Code: "not_found", Message: "tree not found"}
	}

	root := newLazyMountTestRoot(t, remote, "ws_lazy_empty_repo")
	repo := lookupDir(t, lookupDir(t, lookupDir(t, lookupDir(t, root, "github"), "repos"), "octocat"), "empty-repo")

	if errno := repo.Getattr(context.Background(), nil, &fuse.AttrOut{}); errno != 0 {
		t.Fatalf("Getattr(repo) errno = %d, want 0", errno)
	}
	if names := readdirNames(t, repo); len(names) != 0 {
		t.Fatalf("Readdir(repo) = %v, want empty directory", names)
	}
	if names := readdirNames(t, repo); len(names) != 0 {
		t.Fatalf("second Readdir(repo) = %v, want empty directory", names)
	}
	if got := atomic.LoadInt32(&remote.lazyMaterializeCalls); got != 1 {
		t.Fatalf("LazyMaterialize calls = %d, want 1", got)
	}
}

func TestFuseAliasByStateResolves(t *testing.T) {
	t.Parallel()

	const body = `{"identifier":"AGE-8","state":"in-progress"}`

	remote := &fakeRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/":              {Path: "/", Entries: []mountsync.TreeEntry{{Path: "/linear", Type: "directory"}}},
			"/linear":        {Path: "/linear", Entries: []mountsync.TreeEntry{{Path: "/linear/issues", Type: "directory"}}},
			"/linear/issues": {Path: "/linear/issues", Entries: []mountsync.TreeEntry{{Path: "/linear/issues/" + aliasByStateSegment, Type: "directory"}}},
			"/linear/issues/" + aliasByStateSegment: {
				Path: "/linear/issues/" + aliasByStateSegment,
				Entries: []mountsync.TreeEntry{
					{Path: "/linear/issues/" + aliasByStateSegment + "/in-progress", Type: "directory"},
				},
			},
			"/linear/issues/" + aliasByStateSegment + "/in-progress": {
				Path: "/linear/issues/" + aliasByStateSegment + "/in-progress",
				Entries: []mountsync.TreeEntry{
					{Path: "/linear/issues/" + aliasByStateSegment + "/in-progress/AGE-8.json", Type: "file", Revision: "r-age-8"},
				},
			},
		},
		files: map[string]mountsync.RemoteFile{
			"/linear/issues/" + aliasByStateSegment + "/in-progress/AGE-8.json": {
				Path:        "/linear/issues/" + aliasByStateSegment + "/in-progress/AGE-8.json",
				Revision:    "r-age-8",
				ContentType: "application/json",
				Content:     body,
			},
		},
	}

	root := newMountTestRoot(t, remote, "ws_alias_by_state")
	linear := lookupDir(t, root, "linear")
	issues := lookupDir(t, linear, "issues")
	byState := lookupDir(t, issues, aliasByStateSegment)
	inProgress := lookupDir(t, byState, "in-progress")
	fileNode, _ := lookupFile(t, inProgress, "AGE-8.json")
	gotContent, gotContentType := readFileContent(t, fileNode)
	if gotContent != body {
		t.Fatalf("read AGE-8.json = %q, want %q", gotContent, body)
	}
	if gotContentType != "application/json" {
		t.Fatalf("content type = %q, want application/json", gotContentType)
	}
}

func TestFuseAliasByStateEmptyDirectory(t *testing.T) {
	t.Parallel()

	remote := &fakeRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/":              {Path: "/", Entries: []mountsync.TreeEntry{{Path: "/linear", Type: "directory"}}},
			"/linear":        {Path: "/linear", Entries: []mountsync.TreeEntry{{Path: "/linear/issues", Type: "directory"}}},
			"/linear/issues": {Path: "/linear/issues", Entries: []mountsync.TreeEntry{{Path: "/linear/issues/" + aliasByStateSegment, Type: "directory"}}},
			"/linear/issues/" + aliasByStateSegment: {
				Path: "/linear/issues/" + aliasByStateSegment,
				Entries: []mountsync.TreeEntry{
					{Path: "/linear/issues/" + aliasByStateSegment + "/in-progress", Type: "directory"},
				},
			},
			"/linear/issues/" + aliasByStateSegment + "/in-progress": {
				Path:    "/linear/issues/" + aliasByStateSegment + "/in-progress",
				Entries: nil,
			},
		},
	}

	root := newMountTestRoot(t, remote, "ws_alias_by_state_empty")
	linear := lookupDir(t, root, "linear")
	issues := lookupDir(t, linear, "issues")
	byState := lookupDir(t, issues, aliasByStateSegment)
	inProgress := lookupDir(t, byState, "in-progress")

	if names := readdirNames(t, inProgress); len(names) != 0 {
		t.Fatalf("Readdir(in-progress) = %v, want empty directory", names)
	}
}

func TestByStateOutsideIssuesPathRoundTrips(t *testing.T) {
	t.Parallel()

	const body = `{"scope":"real-directory"}`

	remote := &fakeRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/":       {Path: "/", Entries: []mountsync.TreeEntry{{Path: "/notion", Type: "directory"}}},
			"/notion": {Path: "/notion", Entries: []mountsync.TreeEntry{{Path: "/notion/by-state", Type: "directory"}}},
			"/notion/by-state": {
				Path: "/notion/by-state",
				Entries: []mountsync.TreeEntry{
					{Path: "/notion/by-state/current.json", Type: "file", Revision: "r-current"},
				},
			},
		},
		files: map[string]mountsync.RemoteFile{
			"/notion/by-state/current.json": {
				Path:        "/notion/by-state/current.json",
				Revision:    "r-current",
				ContentType: "application/json",
				Content:     body,
			},
		},
	}

	root := newMountTestRoot(t, remote, "ws_real_by_state")
	notion := lookupDir(t, root, "notion")
	byState := lookupDir(t, notion, "by-state")
	fileNode, _ := lookupFile(t, byState, "current.json")
	gotContent, gotContentType := readFileContent(t, fileNode)
	if gotContent != body {
		t.Fatalf("read current.json = %q, want %q", gotContent, body)
	}
	if gotContentType != "application/json" {
		t.Fatalf("content type = %q, want application/json", gotContentType)
	}
}

func TestProviderLayoutVisibleAndReadable(t *testing.T) {
	t.Parallel()

	remote := &fakeRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/": {
				Path: "/",
				Entries: []mountsync.TreeEntry{
					{Path: "/linear", Type: "directory"},
				},
			},
			"/linear": {
				Path: "/linear",
				Entries: []mountsync.TreeEntry{
					{Path: "/linear/issues", Type: "directory"},
				},
			},
		},
	}
	root, err := New(Config{
		Client:      remote,
		WorkspaceID: "ws_provider_layout",
		RemoteRoot:  "/",
		LayoutManifests: map[string]LayoutManifest{
			"linear": {
				Provider:            "linear",
				ResourceDirectories: []string{"issues"},
				AliasSegments: []string{
					aliasByIDSegment,
					aliasByNameSegment,
					aliasByStateSegment,
					aliasByTitleSegment,
				},
				WritebackResources: []string{"comments"},
			},
		},
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	_ = gofusefs.NewNodeFS(root, &gofusefs.Options{})

	linear := lookupDir(t, root, "linear")
	if names := readdirNames(t, linear); !equalSorted(names, []string{providerLayoutFilename, "issues"}) {
		t.Fatalf("Readdir(/linear) = %v, want layout and issues", names)
	}
	layoutNode, entryOut := lookupFile(t, linear, providerLayoutFilename)
	if perm := entryOut.Attr.Mode & 0o777; perm != 0o444 {
		t.Fatalf("provider layout permissions = %o, want 0444", perm)
	}
	body, contentType := readFileContent(t, layoutNode)
	if contentType != layoutContentType {
		t.Fatalf("provider layout content type = %q, want %q", contentType, layoutContentType)
	}
	for _, needle := range []string{
		"linear layout",
		"issues/",
		aliasByIDSegment,
		aliasByNameSegment,
		aliasByStateSegment,
		aliasByTitleSegment,
		"comments/.schema.json",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("provider layout missing %q:\n%s", needle, body)
		}
	}
}

func TestResourceSchemaReadableAndReadOnly(t *testing.T) {
	t.Parallel()

	remote := &fakeRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/": {
				Path: "/",
				Entries: []mountsync.TreeEntry{
					{Path: "/github", Type: "directory"},
				},
			},
			"/github": {
				Path: "/github",
				Entries: []mountsync.TreeEntry{
					{Path: "/github/repos", Type: "directory"},
				},
			},
			"/github/repos": {
				Path: "/github/repos",
				Entries: []mountsync.TreeEntry{
					{Path: "/github/repos/octocat", Type: "directory"},
				},
			},
			"/github/repos/octocat": {
				Path: "/github/repos/octocat",
				Entries: []mountsync.TreeEntry{
					{Path: "/github/repos/octocat/hello-world", Type: "directory"},
				},
			},
			"/github/repos/octocat/hello-world": {
				Path: "/github/repos/octocat/hello-world",
				Entries: []mountsync.TreeEntry{
					{Path: "/github/repos/octocat/hello-world/issues", Type: "directory"},
				},
			},
			"/github/repos/octocat/hello-world/issues": {
				Path:    "/github/repos/octocat/hello-world/issues",
				Entries: nil,
			},
		},
	}

	root := newMountTestRoot(t, remote, "ws_resource_schema")
	issues := lookupDir(t, lookupDir(t, lookupDir(t, lookupDir(t, lookupDir(t, root, "github"), "repos"), "octocat"), "hello-world"), "issues")
	if names := readdirNames(t, issues); !equalSorted(names, []string{schemaFilename}) {
		t.Fatalf("Readdir(issues) = %v, want schema file", names)
	}
	schemaNode, entryOut := lookupFile(t, issues, schemaFilename)
	if perm := entryOut.Attr.Mode & 0o777; perm != 0o444 {
		t.Fatalf("schema permissions = %o, want 0444", perm)
	}
	body, contentType := readFileContent(t, schemaNode)
	if contentType != schemaContentType {
		t.Fatalf("schema content type = %q, want %q", contentType, schemaContentType)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("schema is not valid JSON: %v\n%s", err, body)
	}
	if decoded["$schema"] == "" {
		t.Fatalf("schema missing $schema: %v", decoded)
	}
	if _, _, errno := schemaNode.Open(context.Background(), syscall.O_WRONLY); errno != syscall.EACCES {
		t.Fatalf("Open(schema, O_WRONLY) errno = %d, want EACCES", errno)
	}
}

func TestDeadLetterErrorSidecarReadable(t *testing.T) {
	t.Parallel()

	const errorBody = `{"code":"schema_violation","message":"bad payload","attempts":4}`
	remote := &fakeRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/": {
				Path: "/",
				Entries: []mountsync.TreeEntry{
					{Path: "/.relay", Type: "directory"},
				},
			},
			"/.relay": {
				Path: "/.relay",
				Entries: []mountsync.TreeEntry{
					{Path: "/.relay/dead-letter", Type: "directory"},
				},
			},
			"/.relay/dead-letter": {
				Path: "/.relay/dead-letter",
				Entries: []mountsync.TreeEntry{
					{Path: "/.relay/dead-letter/wb-1715600000.json", Type: "file", Revision: "r-payload"},
					{Path: "/.relay/dead-letter/wb-1715600000.error.json", Type: "file", Revision: "r-error"},
				},
			},
		},
		files: map[string]mountsync.RemoteFile{
			"/.relay/dead-letter/wb-1715600000.json": {
				Path:        "/.relay/dead-letter/wb-1715600000.json",
				Revision:    "r-payload",
				ContentType: "application/json",
				Content:     `{"opId":"op_1"}`,
			},
			"/.relay/dead-letter/wb-1715600000.error.json": {
				Path:        "/.relay/dead-letter/wb-1715600000.error.json",
				Revision:    "r-error",
				ContentType: "application/json",
				Content:     errorBody,
			},
		},
	}

	root := newMountTestRoot(t, remote, "ws_dead_letter_error")
	deadLetter := lookupDir(t, lookupDir(t, root, ".relay"), "dead-letter")
	names := readdirNames(t, deadLetter)
	if !equalSorted(names, []string{"wb-1715600000.json", "wb-1715600000.error.json"}) {
		t.Fatalf("Readdir(dead-letter) = %v, want payload and error sidecar", names)
	}
	errorNode, errorEntry := lookupFile(t, deadLetter, "wb-1715600000.error.json")
	body, contentType := readFileContent(t, errorNode)
	if body != errorBody {
		t.Fatalf("read error sidecar = %q, want %q", body, errorBody)
	}
	if contentType != "application/json" {
		t.Fatalf("error sidecar content type = %q, want application/json", contentType)
	}
	// Lead plan §4 gate 6: dead-letter `.error.json` sidecars are read-only;
	// writes/truncations must surface as EACCES so agents cannot mutate the
	// daemon's deterministic failure record.
	if perm := errorEntry.Attr.Mode & 0o777; perm != 0o444 {
		t.Fatalf("error sidecar permissions = %o, want 0444", perm)
	}
	if _, _, errno := errorNode.Open(context.Background(), syscall.O_WRONLY); errno != syscall.EACCES {
		t.Fatalf("Open(error sidecar, O_WRONLY) errno = %d, want EACCES", errno)
	}
	if _, _, errno := errorNode.Open(context.Background(), syscall.O_RDWR); errno != syscall.EACCES {
		t.Fatalf("Open(error sidecar, O_RDWR) errno = %d, want EACCES", errno)
	}
	if _, _, errno := errorNode.Open(context.Background(), syscall.O_WRONLY|syscall.O_TRUNC); errno != syscall.EACCES {
		t.Fatalf("Open(error sidecar, O_WRONLY|O_TRUNC) errno = %d, want EACCES", errno)
	}
}

func newMountTestRoot(t *testing.T, remote mountsync.RemoteClient, workspaceID string) *DirNode {
	t.Helper()

	root, err := New(Config{Client: remote, WorkspaceID: workspaceID, RemoteRoot: "/"})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	_ = gofusefs.NewNodeFS(root, &gofusefs.Options{})
	return root
}

func newLazyMountTestRoot(t *testing.T, remote mountsync.RemoteClient, workspaceID string) *DirNode {
	t.Helper()

	root, err := New(Config{Client: remote, WorkspaceID: workspaceID, RemoteRoot: "/", LazyRepos: true})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	_ = gofusefs.NewNodeFS(root, &gofusefs.Options{})
	return root
}

func newLazyGithubRepoRemote() *fakeLazyRemoteClient {
	remote := &fakeLazyRemoteClient{
		fakeRemoteClient: &fakeRemoteClient{
			files: map[string]mountsync.RemoteFile{
				"/github/repos/octocat/hello-world/_index.json": {
					Path:        "/github/repos/octocat/hello-world/_index.json",
					Revision:    "r-hello-index",
					ContentType: "application/json",
					Content:     `{"repo":"hello-world"}`,
				},
				"/github/repos/octocat/spoon-knife/_index.json": {
					Path:        "/github/repos/octocat/spoon-knife/_index.json",
					Revision:    "r-spoon-index",
					ContentType: "application/json",
					Content:     `{"repo":"spoon-knife"}`,
				},
			},
		},
		materialized: make(map[string]bool),
	}
	remote.listTreeFunc = func(path string) (mountsync.TreeResponse, error) {
		switch path {
		case "/":
			return mountsync.TreeResponse{Path: path, Entries: []mountsync.TreeEntry{{Path: "/github", Type: "directory"}}}, nil
		case "/github":
			return mountsync.TreeResponse{Path: path, Entries: []mountsync.TreeEntry{{Path: "/github/repos", Type: "directory"}}}, nil
		case "/github/repos":
			return mountsync.TreeResponse{Path: path, Entries: []mountsync.TreeEntry{{Path: "/github/repos/octocat", Type: "directory"}}}, nil
		case "/github/repos/octocat":
			return mountsync.TreeResponse{
				Path: path,
				Entries: []mountsync.TreeEntry{
					{Path: "/github/repos/octocat/hello-world", Type: "directory"},
					{Path: "/github/repos/octocat/spoon-knife", Type: "directory"},
				},
			}, nil
		case "/github/repos/octocat/hello-world":
			if !remote.isMaterialized("octocat", "hello-world") {
				return mountsync.TreeResponse{}, &mountsync.HTTPError{StatusCode: 404, Code: "not_found", Message: "not materialized"}
			}
			return mountsync.TreeResponse{
				Path: path,
				Entries: []mountsync.TreeEntry{
					{Path: "/github/repos/octocat/hello-world/_index.json", Type: "file", Revision: "r-hello-index"},
					{Path: "/github/repos/octocat/hello-world/issues", Type: "directory"},
				},
			}, nil
		case "/github/repos/octocat/spoon-knife":
			if !remote.isMaterialized("octocat", "spoon-knife") {
				return mountsync.TreeResponse{}, &mountsync.HTTPError{StatusCode: 404, Code: "not_found", Message: "not materialized"}
			}
			return mountsync.TreeResponse{
				Path: path,
				Entries: []mountsync.TreeEntry{
					{Path: "/github/repos/octocat/spoon-knife/_index.json", Type: "file", Revision: "r-spoon-index"},
					{Path: "/github/repos/octocat/spoon-knife/issues", Type: "directory"},
				},
			}, nil
		}
		return mountsync.TreeResponse{}, &mountsync.HTTPError{StatusCode: 404, Code: "not_found", Message: "tree not found"}
	}
	return remote
}

func lookupDir(t *testing.T, parent *DirNode, name string) *DirNode {
	t.Helper()

	child, errno, out := lookupChild(t, parent, name)
	if errno != 0 {
		t.Fatalf("Lookup(%q) errno = %d, want 0", name, errno)
	}
	if out.Attr.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		t.Fatalf("Lookup(%q) mode = %o, want directory", name, out.Attr.Mode)
	}
	dir, ok := child.Operations().(*DirNode)
	if !ok {
		t.Fatalf("Lookup(%q) returned %T, want *DirNode", name, child.Operations())
	}
	return dir
}

func lookupFile(t *testing.T, parent *DirNode, name string) (*FileNode, fuse.EntryOut) {
	t.Helper()

	child, errno, out := lookupChild(t, parent, name)
	if errno != 0 {
		t.Fatalf("Lookup(%q) errno = %d, want 0", name, errno)
	}
	fileNode, ok := child.Operations().(*FileNode)
	if !ok {
		t.Fatalf("Lookup(%q) returned %T, want *FileNode", name, child.Operations())
	}
	return fileNode, out
}

func lookupChild(t *testing.T, parent *DirNode, name string) (*gofusefs.Inode, syscall.Errno, fuse.EntryOut) {
	t.Helper()

	var out fuse.EntryOut
	child, errno := parent.Lookup(context.Background(), name, &out)
	return child, errno, out
}

func readdirNames(t *testing.T, dir *DirNode) []string {
	t.Helper()

	stream, errno := dir.Readdir(context.Background())
	if errno != 0 {
		t.Fatalf("Readdir(%q) errno = %d, want 0", dir.path, errno)
	}
	defer stream.Close()

	var names []string
	for stream.HasNext() {
		entry, nextErrno := stream.Next()
		if nextErrno != 0 {
			t.Fatalf("Readdir(%q).Next errno = %d, want 0", dir.path, nextErrno)
		}
		names = append(names, entry.Name)
	}
	sort.Strings(names)
	return names
}

func readFileContent(t *testing.T, fileNode *FileNode) (string, string) {
	t.Helper()

	handle, _, errno := fileNode.Open(context.Background(), 0)
	if errno != 0 {
		t.Fatalf("Open(%q) errno = %d, want 0", fileNode.path, errno)
	}
	fileHandle, ok := handle.(*FileHandle)
	if !ok {
		t.Fatalf("Open(%q) returned %T, want *FileHandle", fileNode.path, handle)
	}
	result, readErrno := fileHandle.Read(context.Background(), make([]byte, len(fileHandle.buf)+16), 0)
	if readErrno != 0 {
		t.Fatalf("Read(%q) errno = %d, want 0", fileNode.path, readErrno)
	}
	data, status := result.Bytes(nil)
	if status != 0 {
		t.Fatalf("Read(%q) status = %d, want 0", fileNode.path, status)
	}
	result.Done()
	return string(data), fileHandle.contentType
}

func equalSorted(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	gotCopy := append([]string(nil), got...)
	wantCopy := append([]string(nil), want...)
	sort.Strings(gotCopy)
	sort.Strings(wantCopy)
	return strings.Join(gotCopy, "\x00") == strings.Join(wantCopy, "\x00")
}
