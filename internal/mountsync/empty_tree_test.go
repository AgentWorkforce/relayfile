package mountsync

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// Keep the remote files readable while simulating a broken empty listing.
type emptyListingClient struct {
	*fakeExportClient
	empty bool
	total int
}

// The root advertises files but only returns a frontier. Traversing its empty
// tail persists that frontier before the empty-tree error is raised.
type advertisedEmptyFrontierClient struct {
	*fakeClient
	paginated bool
	rootCalls int
}

func (c *advertisedEmptyFrontierClient) ListTree(_ context.Context, _ string, path string, _ int, cursor string) (TreeResponse, error) {
	if path != "/documents" || cursor != "" {
		return TreeResponse{Path: path}, nil
	}
	c.rootCalls++
	page := TreeResponse{Path: path, TotalFiles: 286}
	if c.paginated {
		next := "empty-tail"
		page.NextCursor = &next
	} else {
		page.Entries = []TreeEntry{{Path: path + "/a/b/c", Type: "dir"}}
	}
	return page, nil
}

func TestAdvertisedEmptyTreeRemainsRejectedAfterResume(t *testing.T) {
	for _, paginated := range []bool{false, true} {
		name := "directory frontier"
		if paginated {
			name = "page frontier"
		}
		t.Run(name, func(t *testing.T) {
			client := &advertisedEmptyFrontierClient{fakeClient: &fakeClient{files: map[string]RemoteFile{}}, paginated: paginated}
			localDir := t.TempDir()
			logger := &captureLogger{}
			opts := SyncerOptions{WorkspaceID: "ws_empty_frontier", RemoteRoot: "/documents", LocalRoot: localDir, StateFile: filepath.Join(localDir, "private-state.json"), Logger: logger, WebSocket: boolPtr(false)}
			s, err := NewSyncer(client, opts)
			if err != nil {
				t.Fatal(err)
			}
			for cycle := 0; cycle < 3; cycle++ {
				if cycle == 2 {
					// Reload the checkpoint from disk, as a restarted daemon does.
					s, err = NewSyncer(client, opts)
					if err != nil {
						t.Fatal(err)
					}
				}
				err := s.Reconcile(context.Background())
				var emptyTree *EmptyRemoteTreeError
				if !errors.As(err, &emptyTree) || emptyTree.ExpectedFiles != 286 {
					t.Fatalf("cycle %d lost advertised empty-tree rejection: %v", cycle, err)
				}
				if s.state.BootstrapComplete || s.fullPullAuthoritative || s.state.LastSuccessfulReconcileAt != "" || s.state.EventsCursor != "" {
					t.Fatalf("cycle %d marked the empty tree ready", cycle)
				}
				if s.state.BootstrapFilesTotal != 286 || len(s.state.BootstrapDirectories) == 0 {
					t.Fatalf("cycle %d did not preserve root total and resume frontier", cycle)
				}
			}
			if client.rootCalls != 1 {
				t.Fatalf("expected retries to use the saved frontier, got %d root requests", client.rootCalls)
			}
			logs := strings.Join(logger.lines, "\n")
			if strings.Count(logs, "traversal_complete=false traversal_failed=true") != 3 {
				t.Fatalf("resumed failure summaries missing: %s", logs)
			}
		})
	}
}

func TestEmptyTreeDoesNotReuseUnrelatedOrUnavailableTotals(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		resumed, unavailable bool
		filesSynced          int
	}{
		{name: "fresh traversal ignores historical total"},
		{name: "resumed pruned total is unavailable", resumed: true, unavailable: true},
		{name: "resumed tail follows populated prefix", resumed: true, filesSynced: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &emptyListingClient{fakeExportClient: &fakeExportClient{fakeClient: &fakeClient{files: map[string]RemoteFile{}}}, empty: true}
			s, err := NewSyncer(client, SyncerOptions{WorkspaceID: "ws_empty_total", RemoteRoot: "/documents", LocalRoot: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			s.state.BootstrapFilesTotal = 286
			s.state.BootstrapFilesTotalUnavailable = tc.unavailable
			s.state.BootstrapFilesSynced = tc.filesSynced
			if tc.resumed {
				s.state.BootstrapDirectories = []string{"/documents/a/b/c"}
			}
			if err := s.pullRemoteFullTree(context.Background(), nil, bootstrapProgress{}); err != nil {
				t.Fatalf("valid empty traversal: %v", err)
			}
			if !s.state.BootstrapComplete {
				t.Fatal("valid empty traversal should complete")
			}
		})
	}
}

func (c *emptyListingClient) ListTree(ctx context.Context, workspace, path string, depth int, cursor string) (TreeResponse, error) {
	if c.empty {
		c.listTreeCalls++
		return TreeResponse{Path: path, TotalFiles: c.total}, nil
	}
	return c.fakeClient.ListTree(ctx, workspace, path, depth, cursor)
}

func TestEmptyTreeCannotCompleteUnverifiedSourceOrAdvertisedFiles(t *testing.T) {
	for _, tc := range []struct {
		name      string
		root      string
		total     int
		wantError bool
	}{
		{"source without materialization evidence", "/github/repos/acme/project/contents", 0, true},
		{"nonempty remote advertised by server", "/documents", 286, true},
		{"ordinary empty mount", "/documents", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filePath := tc.root + "/README.md"
			client := &emptyListingClient{
				fakeExportClient: &fakeExportClient{fakeClient: &fakeClient{files: map[string]RemoteFile{
					filePath:                               {Path: filePath, Content: "# Project", Revision: "rev_1"},
					"/github/repos/acme/project/meta.json": {Path: "/github/repos/acme/project/meta.json", Content: `{"default_branch":"main"}`},
				}}},
				empty: true,
				total: tc.total,
			}
			logger := &captureLogger{}
			if !tc.wantError {
				client.files = map[string]RemoteFile{}
			}
			s, err := NewSyncer(client, SyncerOptions{WorkspaceID: "ws_empty_tree", RemoteRoot: tc.root, LocalRoot: t.TempDir(), Logger: logger, WebSocket: boolPtr(false)})
			if err != nil {
				t.Fatal(err)
			}
			err = s.Reconcile(context.Background())
			if !tc.wantError {
				if err != nil {
					t.Fatalf("ordinary empty mount: %v", err)
				}
				if !s.state.BootstrapComplete {
					t.Fatal("ordinary empty mount should complete")
				}
				return
			}
			if err == nil {
				t.Fatal("empty listing against a non-empty remote reported success")
			}
			var emptyTree *EmptyRemoteTreeError
			if !errors.As(err, &emptyTree) || emptyTree.ExpectedFiles != tc.total {
				t.Fatalf("missing typed empty-tree evidence: %v", err)
			}
			if s.state.LastError == nil || s.state.LastError.Code != "empty_remote_tree" || s.state.LastSuccessfulReconcileAt != "" {
				t.Fatalf("failure not exposed in status: %+v", s.state.LastError)
			}
			if !strings.Contains(err.Error(), "empty remote tree") {
				t.Fatalf("missing actionable error: %v", err)
			}
			if tc.total == 0 && !strings.Contains(err.Error(), "missing headSha") {
				t.Fatalf("lost seed failure: %v", err)
			}
			if s.state.BootstrapComplete || s.fullPullAuthoritative || s.state.EventsCursor != "" || s.state.LastFullPullAt != "" {
				t.Fatalf("failed empty traversal committed completion: %+v", s.state)
			}
			logs := strings.Join(logger.lines, "\n")
			if !strings.Contains(logs, "traversal_complete=false traversal_failed=true") {
				t.Fatalf("summary reported success: %s", logs)
			}
			// A later healthy listing must be able to recover without stale failure state.
			client.empty = false
			if err := s.Reconcile(context.Background()); err != nil {
				t.Fatalf("populated fallback should recover despite missing manifest: %v", err)
			}
			if !s.state.BootstrapComplete {
				t.Fatal("healthy fallback did not complete")
			}
		})
	}
}

func TestEmptySourceExportFallsBackWithoutDeletingTrackedFiles(t *testing.T) {
	root := "/github/repos/acme/project/contents"
	client := &emptyListingClient{fakeExportClient: &fakeExportClient{fakeClient: &fakeClient{files: map[string]RemoteFile{}}, exportReturnsEmpty: true}, empty: true}
	s, err := NewSyncer(client, SyncerOptions{WorkspaceID: "ws_empty_export", RemoteRoot: root, LocalRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s.state.BootstrapComplete = true
	s.state.Files[root+"/README.md"] = trackedFile{Revision: "rev_1"}
	err = s.pullRemoteFull(context.Background(), nil, bootstrapProgress{})
	var emptyTree *EmptyRemoteTreeError
	if !errors.As(err, &emptyTree) {
		t.Fatalf("empty export/tree must fail: %v", err)
	}
	if client.exportCalls != 1 || client.listTreeCalls != 1 {
		t.Fatalf("expected export then tree, got %d/%d", client.exportCalls, client.listTreeCalls)
	}
	if len(s.state.Files) != 1 || s.fullPullAuthoritative {
		t.Fatal("empty source snapshot became authoritative")
	}
}

func TestEmptyResumedSourceTailCanComplete(t *testing.T) {
	root := "/github/repos/acme/project/contents"
	client := &emptyListingClient{fakeExportClient: &fakeExportClient{fakeClient: &fakeClient{files: map[string]RemoteFile{}}}, empty: true}
	s, err := NewSyncer(client, SyncerOptions{WorkspaceID: "ws_source_tail", RemoteRoot: root, LocalRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s.state.BootstrapDirectories = []string{root + "/empty/deep/directory"}
	s.state.BootstrapFilesSynced = 1
	s.state.Files[root+"/README.md"] = trackedFile{Revision: "rev_1"}
	if err := s.pullRemoteFullTree(context.Background(), nil, bootstrapProgress{}); err != nil {
		t.Fatalf("empty tail after populated prefix: %v", err)
	}
	if !s.state.BootstrapComplete {
		t.Fatal("resumed traversal should complete")
	}
}
