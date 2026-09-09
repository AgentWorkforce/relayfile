package mountsync

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Keep the remote files readable while simulating a broken empty listing.
type emptyListingClient struct {
	*fakeExportClient
	empty bool
	total int
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
