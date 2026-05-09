package mountfuse

import (
	"context"
	"sort"
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

	deleteCalls []deleteCall

	// Optional error injection — when non-nil the corresponding method
	// returns this error instead of looking up data.
	readFileErr  error
	writeFileErr error
	deleteErr    error
	listTreeErr  error
}

type deleteCall struct {
	path         string
	baseRevision string
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
	f.deleteCalls = append(f.deleteCalls, deleteCall{path: path, baseRevision: baseRevision})
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

func TestDirNodeLookupAndReadAcceptsLegacyAndNameIDForms(t *testing.T) {
	t.Parallel()

	remote := &fakeRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/": {
				Path: "/",
				Entries: []mountsync.TreeEntry{
					{Path: "/legacy-id.json", Type: "file", Revision: "r_legacy"},
					{Path: "/human-name__legacy-id.json", Type: "file", Revision: "r_named"},
					{Path: "/only-human__shared-id.json", Type: "file", Revision: "r_shared"},
				},
			},
		},
		files: map[string]mountsync.RemoteFile{
			"/legacy-id.json": {
				Path:        "/legacy-id.json",
				Revision:    "r_legacy",
				ContentType: "application/json",
				Content:     `{"kind":"legacy"}`,
			},
			"/human-name__legacy-id.json": {
				Path:        "/human-name__legacy-id.json",
				Revision:    "r_named",
				ContentType: "application/json",
				Content:     `{"kind":"named"}`,
			},
			"/only-human__shared-id.json": {
				Path:        "/only-human__shared-id.json",
				Revision:    "r_shared",
				ContentType: "application/json",
				Content:     `{"kind":"shared"}`,
			},
		},
	}

	root, err := New(Config{Client: remote, WorkspaceID: "ws_nameid", RemoteRoot: "/"})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	_ = gofusefs.NewNodeFS(root, &gofusefs.Options{})

	ctx := context.Background()
	stream, errno := root.Readdir(ctx)
	if errno != 0 {
		t.Fatalf("Readdir errno = %d, want 0", errno)
	}
	defer stream.Close()

	var gotNames []string
	for stream.HasNext() {
		entry, nextErrno := stream.Next()
		if nextErrno != 0 {
			t.Fatalf("Readdir.Next errno = %d, want 0", nextErrno)
		}
		gotNames = append(gotNames, entry.Name)
	}
	sort.Strings(gotNames)
	wantNames := []string{
		"LAYOUT.md",
		"human-name__legacy-id.json",
		"legacy-id.json",
		"only-human__shared-id.json",
	}
	if len(gotNames) != len(wantNames) {
		t.Fatalf("Readdir names = %v, want %v", gotNames, wantNames)
	}
	for i := range wantNames {
		if gotNames[i] != wantNames[i] {
			t.Fatalf("Readdir names = %v, want %v", gotNames, wantNames)
		}
	}

	readByName := func(name string) string {
		t.Helper()
		var entryOut fuse.EntryOut
		child, lookupErrno := root.Lookup(ctx, name, &entryOut)
		if lookupErrno != 0 {
			t.Fatalf("Lookup(%q) errno = %d, want 0", name, lookupErrno)
		}
		fileNode, ok := child.Operations().(*FileNode)
		if !ok {
			t.Fatalf("Lookup(%q) returned %T, want *FileNode", name, child.Operations())
		}
		handle, _, openErrno := fileNode.Open(ctx, 0)
		if openErrno != 0 {
			t.Fatalf("Open(%q) errno = %d, want 0", name, openErrno)
		}
		fileHandle, ok := handle.(*FileHandle)
		if !ok {
			t.Fatalf("Open(%q) returned %T, want *FileHandle", name, handle)
		}
		result, readErrno := fileHandle.Read(ctx, make([]byte, 1024), 0)
		if readErrno != 0 {
			t.Fatalf("Read(%q) errno = %d, want 0", name, readErrno)
		}
		data, status := result.Bytes(nil)
		if status != 0 {
			t.Fatalf("Read(%q) status = %d, want 0", name, status)
		}
		result.Done()
		return string(data)
	}

	if got := readByName("legacy-id.json"); got != `{"kind":"named"}` {
		t.Fatalf("read legacy alias with canonical sibling = %q, want %q", got, `{"kind":"named"}`)
	}
	if got := readByName("human-name__legacy-id.json"); got != `{"kind":"named"}` {
		t.Fatalf("read named file = %q, want %q", got, `{"kind":"named"}`)
	}

	// Prime the child cache under the canonical new-style filename, then
	// prove a legacy-style lookup still resolves through the same entity ID.
	if got := readByName(nameWithId("only-human", "shared-id")); got != `{"kind":"shared"}` {
		t.Fatalf("read canonical shared file = %q, want %q", got, `{"kind":"shared"}`)
	}
	if got := readByName("shared-id.json"); got != `{"kind":"shared"}` {
		t.Fatalf("read legacy alias = %q, want %q", got, `{"kind":"shared"}`)
	}

	if legacyID := IDFromBasename("legacy-id.json"); legacyID != "legacy-id" {
		t.Fatalf("IDFromBasename(legacy-id.json) = %q, want %q", legacyID, "legacy-id")
	}
	if namedID := IDFromBasename("human-name__legacy-id.json"); namedID != "legacy-id" {
		t.Fatalf("IDFromBasename(human-name__legacy-id.json) = %q, want %q", namedID, "legacy-id")
	}
}

func TestDirNodeLookupAcceptsNameIDDirectories(t *testing.T) {
	t.Parallel()

	remote := &fakeRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/": {
				Path: "/",
				Entries: []mountsync.TreeEntry{
					{Path: "/thread__01HXYZ", Type: "directory"},
				},
			},
			"/thread__01HXYZ": {
				Path: "/thread__01HXYZ",
				Entries: []mountsync.TreeEntry{
					{Path: "/thread__01HXYZ/note.json", Type: "file", Revision: "r_note"},
				},
			},
		},
		files: map[string]mountsync.RemoteFile{
			"/thread__01HXYZ/note.json": {
				Path:        "/thread__01HXYZ/note.json",
				Revision:    "r_note",
				ContentType: "application/json",
				Content:     `{"note":"ok"}`,
			},
		},
	}

	root, err := New(Config{Client: remote, WorkspaceID: "ws_nameid_dirs", RemoteRoot: "/"})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	_ = gofusefs.NewNodeFS(root, &gofusefs.Options{})

	ctx := context.Background()
	for _, name := range []string{"thread__01HXYZ", "01HXYZ"} {
		t.Run(name, func(t *testing.T) {
			var entryOut fuse.EntryOut
			child, errno := root.Lookup(ctx, name, &entryOut)
			if errno != 0 {
				t.Fatalf("Lookup(%q) errno = %d, want 0", name, errno)
			}
			dirNode, ok := child.Operations().(*DirNode)
			if !ok {
				t.Fatalf("Lookup(%q) returned %T, want *DirNode", name, child.Operations())
			}
			var nestedOut fuse.EntryOut
			nested, nestedErrno := dirNode.Lookup(ctx, "note.json", &nestedOut)
			if nestedErrno != 0 {
				t.Fatalf("Lookup(%q)/note.json errno = %d, want 0", name, nestedErrno)
			}
			if _, ok := nested.Operations().(*FileNode); !ok {
				t.Fatalf("Lookup(%q)/note.json returned %T, want *FileNode", name, nested.Operations())
			}
		})
	}
}

func TestDirNodeUnlinkAcceptsLegacyAndNameIDForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		deleteName  string
		primeLookup string
	}{
		{
			name:        "canonical unlink",
			deleteName:  "only-human__shared-id.json",
			primeLookup: "only-human__shared-id.json",
		},
		{
			name:        "legacy alias unlink",
			deleteName:  "shared-id.json",
			primeLookup: "only-human__shared-id.json",
		},
		{
			name:       "legacy alias unlink without cache prime",
			deleteName: "shared-id.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remote := &fakeRemoteClient{
				trees: map[string]mountsync.TreeResponse{
					"/": {
						Path: "/",
						Entries: []mountsync.TreeEntry{
							{Path: "/only-human__shared-id.json", Type: "file", Revision: "r_shared"},
						},
					},
				},
			}

			root, err := New(Config{Client: remote, WorkspaceID: "ws_nameid_unlink", RemoteRoot: "/"})
			if err != nil {
				t.Fatalf("New() failed: %v", err)
			}
			_ = gofusefs.NewNodeFS(root, &gofusefs.Options{})

			ctx := context.Background()
			if tt.primeLookup != "" {
				var entryOut fuse.EntryOut
				child, errno := root.Lookup(ctx, tt.primeLookup, &entryOut)
				if errno != 0 {
					t.Fatalf("Lookup(%q) errno = %d, want 0", tt.primeLookup, errno)
				}
				root.AddChild("only-human__shared-id.json", child, true)
				if root.GetChild("only-human__shared-id.json") == nil {
					t.Fatalf("expected canonical child cache to be populated before unlink")
				}
			} else if root.GetChild("only-human__shared-id.json") != nil {
				t.Fatalf("expected canonical child cache to start empty without a prime lookup")
			}

			errno := root.Unlink(ctx, tt.deleteName)
			if errno != 0 {
				t.Fatalf("Unlink(%q) errno = %d, want 0", tt.deleteName, errno)
			}
			if len(remote.deleteCalls) != 1 {
				t.Fatalf("DeleteFile calls = %d, want 1", len(remote.deleteCalls))
			}
			if remote.deleteCalls[0].path != "/only-human__shared-id.json" {
				t.Fatalf("DeleteFile path = %q, want %q", remote.deleteCalls[0].path, "/only-human__shared-id.json")
			}
			if remote.deleteCalls[0].baseRevision != "r_shared" {
				t.Fatalf("DeleteFile baseRevision = %q, want %q", remote.deleteCalls[0].baseRevision, "r_shared")
			}
			if root.GetChild("only-human__shared-id.json") != nil {
				t.Fatalf("expected canonical child cache to be cleared after unlink")
			}
		})
	}
}
