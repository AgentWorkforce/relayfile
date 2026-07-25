package relayfile

import (
	"errors"
	"testing"
)

// TestCommitForkLegacyOverlayEntryFallsBackToWorkspaceCheck covers a fork
// overlay entry persisted by a pre-Phase-1 binary, before ForkOverlayEntry
// had a BaseRevision field. Go's JSON unmarshal leaves BaseRevision as the
// zero value "" for such an entry (new code always sets an explicit "0" for
// a path that didn't exist at first-touch, so "" is unambiguous). Commit
// must fall back to the old whole-workspace ParentRevision check for that
// fork rather than treating "" as "0" (which would wrongly assume the path
// never existed).
func TestCommitForkLegacyOverlayEntryFallsBackToWorkspaceCheck(t *testing.T) {
	const (
		workspaceID = "ws_fork_legacy_overlay"
		path        = "/agent-a/legacy.md"
	)

	t.Run("succeeds when workspace unchanged since fork open", func(t *testing.T) {
		store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
		t.Cleanup(store.Close)
		seed, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: path, IfMatch: "0", Content: "# base"})
		if err != nil {
			t.Fatalf("seed file: %v", err)
		}
		fork, err := store.CreateFork(workspaceID, "proposal-legacy-ok", 0)
		if err != nil {
			t.Fatalf("CreateFork: %v", err)
		}
		if _, err := store.WriteForkFile(WriteRequest{
			WorkspaceID: workspaceID, Path: path, IfMatch: seed.TargetRevision, Content: "# fork edit",
		}, fork.ForkID); err != nil {
			t.Fatalf("fork write: %v", err)
		}

		store.mu.Lock()
		entry := store.forks[fork.ForkID].Overlay[path]
		entry.BaseRevision = "" // simulate pre-Phase-1 persisted state
		store.forks[fork.ForkID].Overlay[path] = entry
		store.mu.Unlock()

		if _, err := store.CommitFork(workspaceID, fork.ForkID, "corr-legacy-ok"); err != nil {
			t.Fatalf("expected commit to succeed with unchanged workspace, got %v", err)
		}
	})

	t.Run("conflicts on unrelated write, preserving old conservative behavior", func(t *testing.T) {
		store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
		t.Cleanup(store.Close)
		seed, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: path, IfMatch: "0", Content: "# base"})
		if err != nil {
			t.Fatalf("seed file: %v", err)
		}
		fork, err := store.CreateFork(workspaceID, "proposal-legacy-conflict", 0)
		if err != nil {
			t.Fatalf("CreateFork: %v", err)
		}
		if _, err := store.WriteForkFile(WriteRequest{
			WorkspaceID: workspaceID, Path: path, IfMatch: seed.TargetRevision, Content: "# fork edit",
		}, fork.ForkID); err != nil {
			t.Fatalf("fork write: %v", err)
		}

		store.mu.Lock()
		entry := store.forks[fork.ForkID].Overlay[path]
		entry.BaseRevision = "" // simulate pre-Phase-1 persisted state
		store.forks[fork.ForkID].Overlay[path] = entry
		store.mu.Unlock()

		// Unrelated write elsewhere in the workspace — a per-path-aware
		// check would ignore this, but a legacy entry has no reliable
		// per-path base to compare, so it must still conservatively
		// conflict, matching what a pre-Phase-1 binary would have done.
		if _, err := store.WriteFile(WriteRequest{
			WorkspaceID: workspaceID, Path: "/agent-b/unrelated.md", IfMatch: "0", Content: "# unrelated",
		}); err != nil {
			t.Fatalf("unrelated write: %v", err)
		}

		if _, err := store.CommitFork(workspaceID, fork.ForkID, "corr-legacy-conflict"); !errors.Is(err, ErrParentMoved) {
			t.Fatalf("expected parent_moved for legacy overlay entry after unrelated workspace write, got %v", err)
		}
	})
}

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
