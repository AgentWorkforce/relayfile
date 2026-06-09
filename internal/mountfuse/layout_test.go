package mountfuse

import (
	"context"
	"os"
	"path/filepath"
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

func (c *layoutRemoteClient) LatestEventID(_ context.Context, _, _ string) (string, error) {
	return "", nil
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

func (c *layoutRemoteClient) GetOperation(_ context.Context, _, _ string) (mountsync.OperationStatus, error) {
	return mountsync.OperationStatus{}, &mountsync.HTTPError{StatusCode: 404, Code: "not_found", Message: "not found"}
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
		"by-edited",
		"by-name",
		"by-state",
		"LazyMaterialize",
		"github/repos/<owner>/<repo>",
		"notion/pages/by-title/",
		"linear/issues/by-id/",
		"notion/pages/by-edited/YYYY-MM-DD/",
		"linear/users/by-name/",
		"github/repos/by-name/",
		"__",
		"<integration>/LAYOUT.md",
	}
	for _, needle := range required {
		if !strings.Contains(LayoutMarkdown, needle) {
			t.Fatalf("LayoutMarkdown missing %q", needle)
		}
	}
}

func TestProviderLayoutMarkdownDeterministic(t *testing.T) {
	t.Parallel()

	manifest := LayoutManifest{
		Provider:            "linear",
		ResourceDirectories: []string{"projects", "issues", "issues"},
		AliasSegments:       []string{aliasByStateSegment, aliasByEditedSegment, aliasByIDSegment, aliasByIDSegment},
		WritebackResources:  []string{"state-transitions", "comments"},
	}

	first := providerLayoutMarkdown(manifest)
	second := providerLayoutMarkdown(manifest)
	if first != second {
		t.Fatal("providerLayoutMarkdown is not deterministic")
	}
	for _, needle := range []string{
		"linear layout",
		"issues/",
		"projects/",
		aliasByEditedSegment,
		aliasByIDSegment,
		aliasByStateSegment,
		"`comments/` (schema: `comments/.schema.json`)",
		"`state-transitions/` (schema: `state-transitions/.schema.json`)",
		"Recover the provider id from the last `__`-separated segment.",
		"wb-<timestamp>.json",
	} {
		if !strings.Contains(first, needle) {
			t.Fatalf("provider layout missing %q:\n%s", needle, first)
		}
	}
	assertOrdered(t, first, "`issues/`", "`projects/`")
	assertOrdered(t, first, "`by-id/`", "`by-state/`")
	assertOrdered(t, first, "`comments/` (schema: `comments/.schema.json`)", "`state-transitions/` (schema: `state-transitions/.schema.json`)")
}

func TestProviderLayoutMarkdownMaterializationMode(t *testing.T) {
	t.Parallel()

	eager := providerLayoutMarkdown(LayoutManifest{Provider: "notion"})
	if !strings.Contains(eager, "expected to expose its advertised tree eagerly") {
		t.Fatalf("eager provider layout missing materialization copy:\n%s", eager)
	}

	lazy := providerLayoutMarkdown(LayoutManifest{Provider: "github", LazyMaterialization: true})
	if !strings.Contains(lazy, "may materialize large subtrees lazily on first access") {
		t.Fatalf("lazy provider layout missing materialization copy:\n%s", lazy)
	}
}

func TestDefaultProviderLayoutManifestDoesNotAdvertiseByEdited(t *testing.T) {
	t.Parallel()

	state := &fsState{}
	markdown := providerLayoutMarkdown(state.layoutManifest("linear"))
	if strings.Contains(markdown, "`"+aliasByEditedSegment+"/`") {
		t.Fatalf("fallback provider layout advertised by-edited:\n%s", markdown)
	}
	if !strings.Contains(markdown, "_No alias indexes have been advertised yet._") {
		t.Fatalf("fallback provider layout should not advertise unknown aliases:\n%s", markdown)
	}
}

func TestProviderLayoutMetaUsesV2Revision(t *testing.T) {
	t.Parallel()

	meta := virtualProviderLayoutMeta("/", LayoutManifest{Provider: "linear"})
	if meta.revision != providerLayoutRevision {
		t.Fatalf("provider layout revision = %q, want %q", meta.revision, providerLayoutRevision)
	}
	if providerLayoutRevision != "virtual-provider-layout-v2" {
		t.Fatalf("provider layout revision constant = %q, want virtual-provider-layout-v2", providerLayoutRevision)
	}
}

func TestProviderLayoutCanonicalAndLegacyPathsHaveSameVirtualContent(t *testing.T) {
	t.Parallel()

	manifest := LayoutManifest{
		Provider:            "linear",
		ResourceDirectories: []string{"issues"},
		AliasSegments:       []string{aliasByIDSegment},
	}

	canonicalMeta := virtualProviderLayoutMeta("/", manifest)
	legacyMeta := legacyVirtualProviderLayoutMeta("/", manifest)
	if canonicalMeta.path != "/linear/LAYOUT.md" {
		t.Fatalf("canonical provider layout path = %q, want /linear/LAYOUT.md", canonicalMeta.path)
	}
	if legacyMeta.path != "/linear/.layout.md" {
		t.Fatalf("legacy provider layout path = %q, want /linear/.layout.md", legacyMeta.path)
	}
	for name, meta := range map[string]nodeMeta{
		providerLayoutFilename:       canonicalMeta,
		legacyProviderLayoutFilename: legacyMeta,
	} {
		if perm := meta.mode & 0o777; perm != 0o444 {
			t.Fatalf("%s permissions = %o, want 0444", name, perm)
		}
		if meta.contentType != layoutContentType {
			t.Fatalf("%s content type = %q, want %q", name, meta.contentType, layoutContentType)
		}
	}

	canonicalFile := readVirtualProviderLayout("/", manifest)
	legacyFile := readLegacyVirtualProviderLayout("/", manifest)
	if canonicalFile.Content != legacyFile.Content {
		t.Fatalf("legacy provider layout content differed from canonical content")
	}
	if canonicalFile.ContentType != layoutContentType || legacyFile.ContentType != layoutContentType {
		t.Fatalf("provider layout content types = %q/%q, want %q", canonicalFile.ContentType, legacyFile.ContentType, layoutContentType)
	}
}

func assertOrdered(t *testing.T, haystack string, first string, second string) {
	t.Helper()

	firstIndex := strings.Index(haystack, first)
	if firstIndex < 0 {
		t.Fatalf("missing ordered token %q:\n%s", first, haystack)
	}
	secondIndex := strings.Index(haystack, second)
	if secondIndex < 0 {
		t.Fatalf("missing ordered token %q:\n%s", second, haystack)
	}
	if firstIndex >= secondIndex {
		t.Fatalf("expected %q before %q:\n%s", first, second, haystack)
	}
}

func TestIsVirtualProviderLayoutPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		remoteRoot string
		remotePath string
		want       string
		wantOK     bool
	}{
		{name: "root provider", remoteRoot: "/", remotePath: "/linear/LAYOUT.md", want: "linear", wantOK: true},
		{name: "root provider legacy", remoteRoot: "/", remotePath: "/linear/.layout.md", want: "linear", wantOK: true},
		{name: "prefixed root", remoteRoot: "/external", remotePath: "/external/github/LAYOUT.md", want: "github", wantOK: true},
		{name: "prefixed root legacy", remoteRoot: "/external", remotePath: "/external/github/.layout.md", want: "github", wantOK: true},
		{name: "root layout", remoteRoot: "/", remotePath: "/LAYOUT.md", wantOK: false},
		{name: "nested layout", remoteRoot: "/", remotePath: "/github/repos/LAYOUT.md", wantOK: false},
		{name: "nested legacy layout", remoteRoot: "/", remotePath: "/github/repos/.layout.md", wantOK: false},
		{name: "outside root", remoteRoot: "/external", remotePath: "/github/LAYOUT.md", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := isVirtualProviderLayoutPath(tt.remoteRoot, tt.remotePath)
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("isVirtualProviderLayoutPath(%q, %q) = (%q, %v), want (%q, %v)", tt.remoteRoot, tt.remotePath, got, ok, tt.want, tt.wantOK)
			}
		})
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
	if len(gotNames) != 2 || gotNames[0] != skillsDirname || gotNames[1] != layoutFilename {
		t.Fatalf("Readdir names = %v, want [%s %s]", gotNames, skillsDirname, layoutFilename)
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

func TestSkillsDirectorySynthesizesActivitySummary(t *testing.T) {
	t.Parallel()

	remote := &layoutRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/": {Path: "/", Entries: nil},
		},
	}
	root, err := New(Config{Client: remote, WorkspaceID: "ws_activity_summary", RemoteRoot: "/"})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	_ = gofusefs.NewNodeFS(root, &gofusefs.Options{})

	ctx := context.Background()
	var skillsOut fuse.EntryOut
	skillsNode, lookupErrno := root.Lookup(ctx, skillsDirname, &skillsOut)
	if lookupErrno != 0 {
		t.Fatalf("Lookup(%q) errno = %d, want 0", skillsDirname, lookupErrno)
	}
	if skillsOut.Attr.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		t.Fatalf("Lookup(%q) mode = %o, want directory", skillsDirname, skillsOut.Attr.Mode)
	}
	skillsDir, ok := skillsNode.Operations().(*DirNode)
	if !ok {
		t.Fatalf("Lookup(%q) returned %T, want *DirNode", skillsDirname, skillsNode.Operations())
	}
	var activityOut fuse.EntryOut
	activityNode, activityErrno := skillsDir.Lookup(ctx, activitySummaryFilename, &activityOut)
	if activityErrno != 0 {
		t.Fatalf("Lookup(%q) errno = %d, want 0", activitySummaryFilename, activityErrno)
	}
	if perm := activityOut.Attr.Mode & 0o777; perm != 0o444 {
		t.Fatalf("activity summary permissions = %o, want 0444", perm)
	}
	fileNode, ok := activityNode.Operations().(*FileNode)
	if !ok {
		t.Fatalf("Lookup(%q) returned %T, want *FileNode", activitySummaryFilename, activityNode.Operations())
	}
	handle, _, openErrno := fileNode.Open(ctx, 0)
	if openErrno != 0 {
		t.Fatalf("Open(%q) errno = %d, want 0", activitySummaryFilename, openErrno)
	}
	fileHandle, ok := handle.(*FileHandle)
	if !ok {
		t.Fatalf("Open(%q) returned %T, want *FileHandle", activitySummaryFilename, handle)
	}
	result, readErrno := fileHandle.Read(ctx, make([]byte, len(ActivitySummarySkillMarkdown)+16), 0)
	if readErrno != 0 {
		t.Fatalf("Read(%q) errno = %d, want 0", activitySummaryFilename, readErrno)
	}
	data, status := result.Bytes(nil)
	if status != 0 {
		t.Fatalf("Read(%q) status = %d, want 0", activitySummaryFilename, status)
	}
	result.Done()
	body := string(data)
	for _, needle := range []string{"activity-summary", "digests/yesterday.md", "_index.json", "LAYOUT.md"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("activity summary missing %q:\n%s", needle, body)
		}
	}
	if len(remote.readFilePaths) != 0 {
		t.Fatalf("expected virtual activity-summary reads to avoid RemoteClient.ReadFile, got %v", remote.readFilePaths)
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

func TestVirtualProviderLayoutsWinOverRemoteCollisions(t *testing.T) {
	t.Parallel()

	remote := &layoutRemoteClient{
		trees: map[string]mountsync.TreeResponse{
			"/linear": {
				Path: "/linear",
				Entries: []mountsync.TreeEntry{
					{Path: "/linear/LAYOUT.md", Type: "file", Revision: "remote-canonical"},
					{Path: "/linear/.layout.md", Type: "file", Revision: "remote-legacy"},
				},
			},
		},
	}
	root, err := New(Config{
		Client:      remote,
		WorkspaceID: "ws_provider_layout_collision",
		RemoteRoot:  "/",
		LayoutManifests: map[string]LayoutManifest{
			"linear": {
				Provider:            "linear",
				ResourceDirectories: []string{"issues"},
				AliasSegments:       []string{aliasByIDSegment},
			},
		},
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	ctx := context.Background()
	entries, err := root.state.listDirectory(ctx, "/linear")
	if err != nil {
		t.Fatalf("listDirectory(/linear) failed: %v", err)
	}
	wantContent := providerLayoutMarkdown(root.state.layoutManifest("linear"))
	for _, filename := range []string{providerLayoutFilename, legacyProviderLayoutFilename} {
		meta, ok := entries[filename]
		if !ok {
			t.Fatalf("provider layout entry %q missing from /linear entries: %#v", filename, entries)
		}
		if perm := meta.mode & 0o777; perm != 0o444 {
			t.Fatalf("%s permissions = %o, want 0444", filename, perm)
		}
		if meta.contentType != layoutContentType {
			t.Fatalf("%s content type = %q, want %q", filename, meta.contentType, layoutContentType)
		}

		file, err := root.state.readFile(ctx, joinRemotePath("/linear", filename))
		if err != nil {
			t.Fatalf("readFile(%s) failed: %v", filename, err)
		}
		if file.Content != wantContent {
			t.Fatalf("%s collision read returned remote content instead of virtual layout", filename)
		}
	}
	if len(remote.readFilePaths) != 0 {
		t.Fatalf("expected provider layout collision reads to avoid RemoteClient.ReadFile, got %v", remote.readFilePaths)
	}
}

// TestProviderLayoutFilenames pins the canonical provider layout filename and
// the legacy virtual compatibility path required by the update-layout slice.
func TestProviderLayoutFilenames(t *testing.T) {
	t.Parallel()

	if providerLayoutFilename != "LAYOUT.md" {
		t.Fatalf("providerLayoutFilename = %q, want %q", providerLayoutFilename, "LAYOUT.md")
	}
	if legacyProviderLayoutFilename != ".layout.md" {
		t.Fatalf("legacyProviderLayoutFilename = %q, want %q", legacyProviderLayoutFilename, ".layout.md")
	}
	if layoutFilename != providerLayoutFilename {
		t.Fatalf("layoutFilename = %q, providerLayoutFilename = %q; canonical name must be shared", layoutFilename, providerLayoutFilename)
	}
	if strings.Contains(LayoutMarkdown, ".layout.md") {
		t.Fatalf("root LayoutMarkdown still references legacy .layout.md:\n%s", LayoutMarkdown)
	}
}

// TestProviderFixtureParity asserts the canonical mount-verify/<provider>/
// LAYOUT.md fixtures (the cross-provider reference text shipped in the repo)
// stay in sync with the layout-runtime constants and the workspace-layout
// skill contract: canonical filename, canonical alias names, filename
// convention, and writeback-discovery surface. Byte-for-byte parity with the
// generated provider layout is not asserted because the fixtures carry
// hand-written prose richer than the manifest-driven render.
func TestProviderFixtureParity(t *testing.T) {
	t.Parallel()

	// Lead plan §"Code Changes": Work Item C in-scope providers must mention
	// a concrete by-edited alias path. Slack and Confluence are not in this
	// scope for the GitHub-style placeholder rule (Slack: no edited-date
	// concept; Confluence: spelled out separately if the fixture exists).
	workItemCProviders := map[string]bool{
		"notion":     true,
		"linear":     true,
		"github":     true,
		"jira":       true,
		"confluence": true,
	}

	providers := []string{"notion", "linear", "github", "jira", "confluence", "slack"}
	for _, provider := range providers {
		provider := provider
		t.Run(provider, func(t *testing.T) {
			t.Parallel()

			// Resolve mount-verify/<provider>/LAYOUT.md relative to the
			// package directory. internal/mountfuse → repo root is ../..
			fixturePath := filepath.Join("..", "..", "mount-verify", provider, "LAYOUT.md")
			body, err := os.ReadFile(fixturePath)
			if err != nil {
				if os.IsNotExist(err) {
					t.Skipf("fixture %s not present in this checkout", fixturePath)
				}
				t.Fatalf("read fixture %s: %v", fixturePath, err)
			}
			text := string(body)

			required := []string{
				"LAYOUT.md",      // canonical filename advertised in lede
				"read-only",      // virtual/read-only declaration
				"__",             // __<id> filename convention
				"`.schema.json`", // writeback discovery surface
			}
			for _, needle := range required {
				if !strings.Contains(text, needle) {
					t.Fatalf("%s missing required anchor %q", fixturePath, needle)
				}
			}

			// Regression guard for F3 (Linear by-updated → by-edited):
			// no provider fixture may advertise `by-updated/` as the
			// canonical edited-date alias. Linear and others must use
			// `by-edited/`.
			if strings.Contains(text, "by-updated/") {
				t.Fatalf("%s advertises non-canonical `by-updated/`; canonical alias is `by-edited/`", fixturePath)
			}

			// Regression guard for F5: the fixture must NOT point agents at
			// the retired `.layout.md` dotfile.
			if strings.Contains(text, ".layout.md") {
				t.Fatalf("%s references legacy `.layout.md`; canonical is `LAYOUT.md`", fixturePath)
			}

			// F4: in-scope providers must materialize a concrete
			// by-edited placeholder path (Linear, Notion already do;
			// GitHub adds it as a future-Work-Item-C placeholder).
			if workItemCProviders[provider] {
				if !strings.Contains(text, "by-edited") {
					t.Fatalf("%s missing required `by-edited` alias mention (Work Item C scope)", fixturePath)
				}
			}
		})
	}
}
