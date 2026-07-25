package relayfile

import (
	"errors"
	"testing"
)

func TestRebaseForkNoConflicts(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	const workspaceID = "ws_rebase_no_conflicts"

	seedA, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: "/agent-a/a.md", IfMatch: "0", Content: "# a base"})
	if err != nil {
		t.Fatalf("seed a: %v", err)
	}
	fork, err := store.CreateFork(workspaceID, "proposal-rebase-clean", 0)
	if err != nil {
		t.Fatalf("CreateFork: %v", err)
	}
	if _, err := store.WriteForkFile(WriteRequest{
		WorkspaceID: workspaceID, Path: "/agent-a/a.md", IfMatch: seedA.TargetRevision, Content: "# fork edit a",
	}, fork.ForkID); err != nil {
		t.Fatalf("fork write: %v", err)
	}

	// Unrelated live write elsewhere — should not conflict.
	if _, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: "/agent-b/b.md", IfMatch: "0", Content: "# unrelated"}); err != nil {
		t.Fatalf("unrelated write: %v", err)
	}

	resp, err := store.RebaseFork(workspaceID, fork.ForkID)
	if err != nil {
		t.Fatalf("RebaseFork: %v", err)
	}
	if resp.CleanCount != 1 {
		t.Fatalf("expected 1 clean path, got %d (conflicts=%+v)", resp.CleanCount, resp.Conflicts)
	}
	if len(resp.Conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %+v", resp.Conflicts)
	}

	if _, err := store.CommitFork(workspaceID, fork.ForkID, "corr-rebase-clean-commit"); err != nil {
		t.Fatalf("expected commit to succeed after clean rebase, got %v", err)
	}
}

func TestRebaseForkReportsGenuineConflictWithoutMutating(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	const (
		workspaceID = "ws_rebase_conflict"
		path        = "/shared/target.md"
	)

	seed, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: path, IfMatch: "0", Content: "# base"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	fork, err := store.CreateFork(workspaceID, "proposal-rebase-conflict", 0)
	if err != nil {
		t.Fatalf("CreateFork: %v", err)
	}
	if _, err := store.WriteForkFile(WriteRequest{
		WorkspaceID: workspaceID, Path: path, IfMatch: seed.TargetRevision, Content: "# fork edit",
	}, fork.ForkID); err != nil {
		t.Fatalf("fork write: %v", err)
	}

	// Genuine collision: live parent changes the SAME path the fork touched.
	liveWrite, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: path, IfMatch: seed.TargetRevision, Content: "# live edit"})
	if err != nil {
		t.Fatalf("live write: %v", err)
	}

	resp, err := store.RebaseFork(workspaceID, fork.ForkID)
	if err != nil {
		t.Fatalf("RebaseFork: %v", err)
	}
	if resp.CleanCount != 0 {
		t.Fatalf("expected 0 clean paths, got %d", resp.CleanCount)
	}
	if len(resp.Conflicts) != 1 {
		t.Fatalf("expected exactly 1 conflict, got %+v", resp.Conflicts)
	}
	c := resp.Conflicts[0]
	if c.Path != path || c.ForkType != "write" || !c.LiveExists || c.LiveRevision != liveWrite.TargetRevision {
		t.Fatalf("unexpected conflict details: %+v (want live revision %s)", c, liveWrite.TargetRevision)
	}
	if c.LiveContentPreview != "# live edit" {
		t.Fatalf("expected live content preview %q, got %q", "# live edit", c.LiveContentPreview)
	}

	// Rebase must not silently mutate the overlay for a real conflict —
	// commit should still correctly fail.
	if _, err := store.CommitFork(workspaceID, fork.ForkID, "corr-rebase-still-conflicts"); !errors.Is(err, ErrParentMoved) {
		t.Fatalf("expected commit to still fail with parent_moved after unresolved rebase conflict, got %v", err)
	}
}

func TestRebaseForkResolveConflictByRewritingWithLiveRevision(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	const (
		workspaceID = "ws_rebase_resolve_write"
		path        = "/shared/target.md"
	)

	seed, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: path, IfMatch: "0", Content: "# base"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	fork, err := store.CreateFork(workspaceID, "proposal-rebase-resolve", 0)
	if err != nil {
		t.Fatalf("CreateFork: %v", err)
	}
	if _, err := store.WriteForkFile(WriteRequest{
		WorkspaceID: workspaceID, Path: path, IfMatch: seed.TargetRevision, Content: "# fork edit",
	}, fork.ForkID); err != nil {
		t.Fatalf("fork write: %v", err)
	}
	if _, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: path, IfMatch: seed.TargetRevision, Content: "# live edit"}); err != nil {
		t.Fatalf("live write: %v", err)
	}

	resp, err := store.RebaseFork(workspaceID, fork.ForkID)
	if err != nil {
		t.Fatalf("RebaseFork: %v", err)
	}
	if len(resp.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %+v", resp.Conflicts)
	}
	liveRevision := resp.Conflicts[0].LiveRevision

	// Resolve by re-writing, citing the live revision RebaseFork reported —
	// this is the documented resolution mechanism.
	if _, err := store.WriteForkFile(WriteRequest{
		WorkspaceID: workspaceID, Path: path, IfMatch: liveRevision, Content: "# reconciled edit",
	}, fork.ForkID); err != nil {
		t.Fatalf("expected resolution write citing live revision to succeed, got %v", err)
	}

	commit, err := store.CommitFork(workspaceID, fork.ForkID, "corr-rebase-resolved-commit")
	if err != nil {
		t.Fatalf("expected commit to succeed after resolving via rewrite, got %v", err)
	}
	if commit.WrittenCount != 1 {
		t.Fatalf("expected 1 file written, got %+v", commit)
	}
	final, err := store.ReadFile(workspaceID, path)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if final.Content != "# reconciled edit" {
		t.Fatalf("expected reconciled content to land, got %q", final.Content)
	}
}

func TestRebaseForkResolveConflictByDeletingWithLiveRevision(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	const (
		workspaceID = "ws_rebase_resolve_delete"
		path        = "/shared/target.md"
	)

	seed, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: path, IfMatch: "0", Content: "# base"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	fork, err := store.CreateFork(workspaceID, "proposal-rebase-resolve-delete", 0)
	if err != nil {
		t.Fatalf("CreateFork: %v", err)
	}
	if _, err := store.WriteForkFile(WriteRequest{
		WorkspaceID: workspaceID, Path: path, IfMatch: seed.TargetRevision, Content: "# fork edit",
	}, fork.ForkID); err != nil {
		t.Fatalf("fork write: %v", err)
	}
	if _, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: path, IfMatch: seed.TargetRevision, Content: "# live edit"}); err != nil {
		t.Fatalf("live write: %v", err)
	}

	resp, err := store.RebaseFork(workspaceID, fork.ForkID)
	if err != nil {
		t.Fatalf("RebaseFork: %v", err)
	}
	if len(resp.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %+v", resp.Conflicts)
	}
	liveRevision := resp.Conflicts[0].LiveRevision

	if _, err := store.DeleteForkFile(DeleteRequest{
		WorkspaceID: workspaceID, Path: path, IfMatch: liveRevision,
	}, fork.ForkID); err != nil {
		t.Fatalf("expected resolution delete citing live revision to succeed, got %v", err)
	}

	commit, err := store.CommitFork(workspaceID, fork.ForkID, "corr-rebase-resolved-delete-commit")
	if err != nil {
		t.Fatalf("expected commit to succeed after resolving via delete, got %v", err)
	}
	if commit.DeletedCount != 1 {
		t.Fatalf("expected 1 file deleted, got %+v", commit)
	}
	if _, err := store.ReadFile(workspaceID, path); err != ErrNotFound {
		t.Fatalf("expected path to be gone after commit, got err=%v", err)
	}
}

func TestRebaseForkLegacyEntryAlwaysConflicts(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	const (
		workspaceID = "ws_rebase_legacy"
		path        = "/agent-a/legacy.md"
	)

	seed, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: path, IfMatch: "0", Content: "# base"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	fork, err := store.CreateFork(workspaceID, "proposal-rebase-legacy", 0)
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
	entry.BaseRevision = ""
	store.forks[fork.ForkID].Overlay[path] = entry
	store.mu.Unlock()

	resp, err := store.RebaseFork(workspaceID, fork.ForkID)
	if err != nil {
		t.Fatalf("RebaseFork: %v", err)
	}
	if len(resp.Conflicts) != 1 || resp.Conflicts[0].Path != path {
		t.Fatalf("expected the legacy entry to always be reported as a conflict, got %+v", resp)
	}
}
