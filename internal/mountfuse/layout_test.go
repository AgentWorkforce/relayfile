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

type layoutRemoteClient struct {
	trees         map[string]mountsync.TreeResponse
	readFilePaths []string
}

func (c *layoutRemoteClient) ListTree(_ context.Context, _, path string, _ int, _ string) (mountsync.TreeResponse, error) {
	if resp, ok := c.trees[path]; ok {
		return resp, nil
	}
	return mountsync.TreeResponse{Path: path}, nil
}

func (c *layoutRemoteClient) ListEvents(_ context.Context, _, _, _ string, _ int) (mountsync.EventFeed, error) {
	return mountsync.EventFeed{}, nil
}

func (c *layoutRemoteClient) ReadFile(_ context.Context, _, path string) (mountsync.RemoteFile, error) {
	c.readFilePaths = append(c.readFilePaths, path)
	return mountsync.RemoteFile{}, &mountsync.HTTPError{StatusCode: 404, Code: "not_found", Message: "file not found"}
}

func (c *layoutRemoteClient) WriteFile(_ context.Context, _, _, _, _, _ string) (mountsync.WriteResult, error) {
	return mountsync.WriteResult{}, nil
}

func (c *layoutRemoteClient) WriteFilesBulk(_ context.Context, _ string, _ []mountsync.BulkWriteFile) (mountsync.BulkWriteResponse, error) {
	return mountsync.BulkWriteResponse{}, nil
}

func (c *layoutRemoteClient) DeleteFile(_ context.Context, _, _, _ string) error {
	return nil
}

func TestLayoutMarkdownContainsRequiredAnchors(t *testing.T) {
	t.Parallel()

	required := []string{
		"LAYOUT",
		"_index.json",
		"notion/pages/_index.json",
		"linear/issues/_index.json",
		"github/repos/_index.json",
		"find by title",
		"by-title",
		"by-id",
		"by-name",
		"by-state",
		"LazyMaterialize",
		"github/repos/<owner>/<repo>",
		"notion/pages/by-title/",
		"linear/issues/by-id/",
		"linear/users/by-name/",
		"github/repos/by-name/",
		"__",
		"<integration>/.layout.md",
	}
	for _, needle := range required {
		if !strings.Contains(LayoutMarkdown, needle) {
			t.Fatalf("LayoutMarkdown missing %q", needle)
		}
	}
}

func TestRootDirectorySynthesizesLayoutMarkdown(t *testing.T) {
	t.Parallel()

	remote := &layoutRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/": {Path: "/", Entries: nil},
		},
	}
	root, err := New(Config{Client: remote, WorkspaceID: "ws_layout", RemoteRoot: "/"})
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
	if len(gotNames) != 1 || gotNames[0] != layoutFilename {
		t.Fatalf("Readdir names = %v, want [%s]", gotNames, layoutFilename)
	}

	var entryOut fuse.EntryOut
	child, lookupErrno := root.Lookup(ctx, layoutFilename, &entryOut)
	if lookupErrno != 0 {
		t.Fatalf("Lookup(%q) errno = %d, want 0", layoutFilename, lookupErrno)
	}
	if entryOut.Attr.Mode&syscall.S_IFMT != syscall.S_IFREG {
		t.Fatalf("Lookup(%q) mode = %o, want regular file", layoutFilename, entryOut.Attr.Mode)
	}
	if perm := entryOut.Attr.Mode & 0o777; perm != 0o444 {
		t.Fatalf("Lookup(%q) perm = %o, want 0444 (read-only)", layoutFilename, perm)
	}
	if entryOut.Attr.Size != uint64(len(LayoutMarkdown)) {
		t.Fatalf("Lookup(%q) size = %d, want %d", layoutFilename, entryOut.Attr.Size, len(LayoutMarkdown))
	}

	fileNode, ok := child.Operations().(*FileNode)
	if !ok {
		t.Fatalf("Lookup(%q) returned %T, want *FileNode", layoutFilename, child.Operations())
	}
	handle, _, openErrno := fileNode.Open(ctx, 0)
	if openErrno != 0 {
		t.Fatalf("Open(%q) errno = %d, want 0", layoutFilename, openErrno)
	}
	fileHandle, ok := handle.(*FileHandle)
	if !ok {
		t.Fatalf("Open(%q) returned %T, want *FileHandle", layoutFilename, handle)
	}
	result, readErrno := fileHandle.Read(ctx, make([]byte, len(LayoutMarkdown)+16), 0)
	if readErrno != 0 {
		t.Fatalf("Read(%q) errno = %d, want 0", layoutFilename, readErrno)
	}
	data, status := result.Bytes(nil)
	if status != 0 {
		t.Fatalf("Read(%q) status = %d, want 0", layoutFilename, status)
	}
	result.Done()
	if string(data) != LayoutMarkdown {
		t.Fatalf("Read(%q) content mismatch", layoutFilename)
	}

	virtualFile, err := root.state.readFile(ctx, layoutRemotePath(root.state.remoteRoot))
	if err != nil {
		t.Fatalf("state.readFile(%q) failed: %v", layoutFilename, err)
	}
	if virtualFile.ContentType != layoutContentType {
		t.Fatalf("virtual layout content type = %q, want %q", virtualFile.ContentType, layoutContentType)
	}
	if len(remote.readFilePaths) != 0 {
		t.Fatalf("expected virtual layout reads to avoid RemoteClient.ReadFile, got %v", remote.readFilePaths)
	}
}

func TestVirtualLayoutWinsOverRemoteCollision(t *testing.T) {
	t.Parallel()

	remote := &layoutRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/": {
				Path: "/",
				Entries: []mountsync.TreeEntry{
					{Path: "/LAYOUT.md", Type: "file", Revision: "remote-layout"},
				},
			},
		},
	}
	root, err := New(Config{Client: remote, WorkspaceID: "ws_layout_collision", RemoteRoot: "/"})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	_ = gofusefs.NewNodeFS(root, &gofusefs.Options{})

	ctx := context.Background()
	var entryOut fuse.EntryOut
	child, lookupErrno := root.Lookup(ctx, layoutFilename, &entryOut)
	if lookupErrno != 0 {
		t.Fatalf("Lookup(%q) errno = %d, want 0", layoutFilename, lookupErrno)
	}
	fileNode, ok := child.Operations().(*FileNode)
	if !ok {
		t.Fatalf("Lookup(%q) returned %T, want *FileNode", layoutFilename, child.Operations())
	}
	handle, _, openErrno := fileNode.Open(ctx, 0)
	if openErrno != 0 {
		t.Fatalf("Open(%q) errno = %d, want 0", layoutFilename, openErrno)
	}
	fileHandle, ok := handle.(*FileHandle)
	if !ok {
		t.Fatalf("Open(%q) returned %T, want *FileHandle", layoutFilename, handle)
	}
	result, readErrno := fileHandle.Read(ctx, make([]byte, len(LayoutMarkdown)+16), 0)
	if readErrno != 0 {
		t.Fatalf("Read(%q) errno = %d, want 0", layoutFilename, readErrno)
	}
	data, status := result.Bytes(nil)
	if status != 0 {
		t.Fatalf("Read(%q) status = %d, want 0", layoutFilename, status)
	}
	result.Done()
	if string(data) != LayoutMarkdown {
		t.Fatalf("collision read returned remote content instead of virtual layout")
	}
	if len(remote.readFilePaths) != 0 {
		t.Fatalf("expected collision reads to avoid RemoteClient.ReadFile, got %v", remote.readFilePaths)
	}
}
