package mountfuse

import (
	"context"
	"sort"
	"strings"
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

func (f *fakeRemoteClient) DeleteFile(_ context.Context, _, path, baseRevision string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	_ = path
	_ = baseRevision
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

func newMountTestRoot(t *testing.T, remote *fakeRemoteClient, workspaceID string) *DirNode {
	t.Helper()

	root, err := New(Config{Client: remote, WorkspaceID: workspaceID, RemoteRoot: "/"})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	_ = gofusefs.NewNodeFS(root, &gofusefs.Options{})
	return root
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
