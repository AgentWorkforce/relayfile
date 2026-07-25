package relayfile

import (
	"errors"
	"testing"
)

func TestForkOverlayBaseRevisionPinnedAtFirstTouch(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)

	const (
		workspaceID = "ws_fork_base_revision"
		path        = "/agent-a/work.md"
	)
	seed, err := store.WriteFile(WriteRequest{
		WorkspaceID: workspaceID,
		Path:        path,
		IfMatch:     "0",
		Content:     "# base",
	})
	if err != nil {
		t.Fatalf("seed file: %v", err)
	}
	fork, err := store.CreateFork(workspaceID, "proposal-base-revision", 0)
	if err != nil {
		t.Fatalf("CreateFork: %v", err)
	}
	first, err := store.WriteForkFile(WriteRequest{
		WorkspaceID: workspaceID,
		Path:        path,
		IfMatch:     seed.TargetRevision,
		Content:     "# first",
	}, fork.ForkID)
	if err != nil {
		t.Fatalf("first fork write: %v", err)
	}
	if _, err := store.WriteForkFile(WriteRequest{
		WorkspaceID: workspaceID,
		Path:        path,
		IfMatch:     first.TargetRevision,
		Content:     "# second",
	}, fork.ForkID); err != nil {
		t.Fatalf("second fork write: %v", err)
	}

	store.mu.Lock()
	entry := store.forks[fork.ForkID].Overlay[path]
	store.mu.Unlock()
	if entry.BaseRevision != seed.TargetRevision {
		t.Fatalf("expected first-touch base revision %q, got %q", seed.TargetRevision, entry.BaseRevision)
	}
}

func TestCommitForkAllowsDeleteWhenTargetIsUnchanged(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)

	const (
		workspaceID = "ws_fork_delete_base_revision"
		targetPath  = "/agent-a/work.md"
	)
	seed, err := store.WriteFile(WriteRequest{
		WorkspaceID: workspaceID,
		Path:        targetPath,
		IfMatch:     "0",
		Content:     "# base",
	})
	if err != nil {
		t.Fatalf("seed file: %v", err)
	}
	fork, err := store.CreateFork(workspaceID, "proposal-delete-base-revision", 0)
	if err != nil {
		t.Fatalf("CreateFork: %v", err)
	}
	if _, err := store.DeleteForkFile(DeleteRequest{
		WorkspaceID: workspaceID,
		Path:        targetPath,
		IfMatch:     seed.TargetRevision,
	}, fork.ForkID); err != nil {
		t.Fatalf("DeleteForkFile: %v", err)
	}
	if _, err := store.WriteFile(WriteRequest{
		WorkspaceID: workspaceID,
		Path:        "/agent-b/unrelated.md",
		IfMatch:     "0",
		Content:     "# unrelated",
	}); err != nil {
		t.Fatalf("unrelated parent write: %v", err)
	}
	if _, err := store.CommitFork(workspaceID, fork.ForkID, "corr-delete-base-revision"); err != nil {
		t.Fatalf("commit after unrelated parent write: %v", err)
	}
}

func TestCommitForkDetectsLiveDirectoryChildRemoval(t *testing.T) {
	const (
		workspaceID = "ws_fork_directory_change"
		path        = "/agent-a/project/work.md"
	)

	for _, liveChange := range []struct {
		name  string
		apply func(t *testing.T, store *Store, base WriteResult)
	}{
		{
			name: "directory child deleted",
			apply: func(t *testing.T, store *Store, base WriteResult) {
				t.Helper()
				if _, err := store.DeleteFile(DeleteRequest{
					WorkspaceID: workspaceID,
					Path:        path,
					IfMatch:     base.TargetRevision,
				}); err != nil {
					t.Fatalf("live delete: %v", err)
				}
			},
		},
		{
			name: "directory child renamed",
			apply: func(t *testing.T, store *Store, base WriteResult) {
				t.Helper()
				// Store has no directory nodes or rename operation: a directory
				// rename is a per-child delete followed by a write at the new path.
				if _, err := store.DeleteFile(DeleteRequest{
					WorkspaceID: workspaceID,
					Path:        path,
					IfMatch:     base.TargetRevision,
				}); err != nil {
					t.Fatalf("live rename delete: %v", err)
				}
				if _, err := store.WriteFile(WriteRequest{
					WorkspaceID: workspaceID,
					Path:        "/agent-a/renamed-project/work.md",
					IfMatch:     "0",
					Content:     "# moved",
				}); err != nil {
					t.Fatalf("live rename write: %v", err)
				}
			},
		},
	} {
		t.Run(liveChange.name, func(t *testing.T) {
			store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
			t.Cleanup(store.Close)
			base, err := store.WriteFile(WriteRequest{
				WorkspaceID: workspaceID,
				Path:        path,
				IfMatch:     "0",
				Content:     "# base",
			})
			if err != nil {
				t.Fatalf("seed file: %v", err)
			}
			fork, err := store.CreateFork(workspaceID, "proposal-"+liveChange.name, 0)
			if err != nil {
				t.Fatalf("CreateFork: %v", err)
			}
			if _, err := store.WriteForkFile(WriteRequest{
				WorkspaceID: workspaceID,
				Path:        path,
				IfMatch:     base.TargetRevision,
				Content:     "# fork work",
			}, fork.ForkID); err != nil {
				t.Fatalf("fork write: %v", err)
			}
			liveChange.apply(t, store, base)
			if _, err := store.CommitFork(workspaceID, fork.ForkID, "corr-directory-change"); !errors.Is(err, ErrParentMoved) {
				t.Fatalf("expected parent_moved after live directory child removal, got %v", err)
			}
		})
	}
}
