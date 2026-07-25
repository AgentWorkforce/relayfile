package relayfile

import (
	"errors"
	"strings"
	"testing"
)

// These tests exercise MergeStrategyThreeWayLines through the real store —
// the language-agnostic counterpart to merge_store_test.go's Go-specific
// coverage. Deliberately uses a .ts file throughout: the whole point of
// this strategy is that it doesn't care what language it's merging.

func TestLinesMergeFileEndToEndSuccess(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	const (
		workspaceID = "ws_lines_merge_e2e"
		path        = "/src/handlers.ts"
	)

	baseContent := "export function createUser(): void {\n  console.log(\"create\");\n}\n\nexport function deleteUser(): void {\n  console.log(\"delete\");\n}\n"
	seed, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: path, IfMatch: "0", Content: baseContent})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Someone else concurrently changes deleteUser.
	theirsContent := "export function createUser(): void {\n  console.log(\"create\");\n}\n\nexport function deleteUser(): void {\n  console.log(\"DELETE!\");\n}\n"
	if _, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: path, IfMatch: seed.TargetRevision, Content: theirsContent}); err != nil {
		t.Fatalf("live write: %v", err)
	}

	// I change createUser, based on the original seed.
	mineContent := "export function createUser(): void {\n  console.log(\"CREATE!\");\n}\n\nexport function deleteUser(): void {\n  console.log(\"delete\");\n}\n"
	resp, err := store.MergeFile(MergeRequest{
		WorkspaceID:  workspaceID,
		Path:         path,
		Strategy:     MergeStrategyThreeWayLines,
		Content:      mineContent,
		BaseRevision: seed.TargetRevision,
		BaseContent:  baseContent,
	})
	if err != nil {
		t.Fatalf("expected merge to succeed, got %v", err)
	}
	if resp.TargetRevision == "" || resp.Strategy != MergeStrategyThreeWayLines {
		t.Fatalf("unexpected response: %+v", resp)
	}

	final, err := store.ReadFile(workspaceID, path)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if !strings.Contains(final.Content, "CREATE!") {
		t.Fatalf("expected my createUser change to be present: %s", final.Content)
	}
	if !strings.Contains(final.Content, "DELETE!") {
		t.Fatalf("expected their deleteUser change to be present: %s", final.Content)
	}
}

func TestLinesMergeFileConflictLeavesLiveFileUnchanged(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	const (
		workspaceID = "ws_lines_merge_conflict"
		path        = "/src/a.ts"
	)

	baseContent := "export const value = 1;\n"
	seed, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: path, IfMatch: "0", Content: baseContent})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	theirsContent := "export const value = 3;\n"
	if _, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: path, IfMatch: seed.TargetRevision, Content: theirsContent}); err != nil {
		t.Fatalf("live write: %v", err)
	}

	mineContent := "export const value = 2;\n"
	_, err = store.MergeFile(MergeRequest{
		WorkspaceID: workspaceID, Path: path, Strategy: MergeStrategyThreeWayLines,
		Content: mineContent, BaseRevision: seed.TargetRevision, BaseContent: baseContent,
	})
	var conflictErr *MergeConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("expected MergeConflictError, got %v", err)
	}
	if len(conflictErr.Conflicts) != 1 {
		t.Fatalf("unexpected conflict details: %+v", conflictErr.Conflicts)
	}

	final, err := store.ReadFile(workspaceID, path)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if final.Content != theirsContent {
		t.Fatalf("live content changed on a conflicting merge! got %q, want unchanged %q", final.Content, theirsContent)
	}
}

func TestLinesMergeFileRejectsUnverifiableBase(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	const (
		workspaceID = "ws_lines_merge_bad_base"
		path        = "/src/a.ts"
	)
	seed, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: path, IfMatch: "0", Content: "export const a = 1;\n"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err = store.MergeFile(MergeRequest{
		WorkspaceID: workspaceID, Path: path, Strategy: MergeStrategyThreeWayLines,
		Content:      "export const a = 2;\n",
		BaseRevision: seed.TargetRevision,
		BaseContent:  "export const a = 999; // this is NOT what the seed actually was\n",
	})
	if !errors.Is(err, ErrMergeBaseUnavailable) {
		t.Fatalf("expected ErrMergeBaseUnavailable for a fabricated base, got %v", err)
	}
}

func TestLinesMergeFileIneligibleCases(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	const workspaceID = "ws_lines_merge_ineligible"

	t.Run("content too large (line count)", func(t *testing.T) {
		seed, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: "/big.ts", IfMatch: "0", Content: "x\n"})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		var b strings.Builder
		for i := 0; i <= maxMergeDiffLines; i++ {
			b.WriteString("line\n")
		}
		_, err = store.MergeFile(MergeRequest{
			WorkspaceID: workspaceID, Path: "/big.ts", Strategy: MergeStrategyThreeWayLines,
			Content: b.String(), BaseRevision: seed.TargetRevision, BaseContent: "x\n",
		})
		var ineligible *ErrMergeIneligible
		if !errors.As(err, &ineligible) {
			t.Fatalf("expected ErrMergeIneligible for oversized line count, got %v", err)
		}
	})

	t.Run("no file-extension restriction, unlike the Go strategy", func(t *testing.T) {
		// Deliberately proves the "any language" claim: this path isn't
		// .go, .ts, or any recognized extension at all, and it's still
		// eligible for three-way-lines-v1.
		seed, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: "/README", IfMatch: "0", Content: "hello\n"})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		_, err = store.MergeFile(MergeRequest{
			WorkspaceID: workspaceID, Path: "/README", Strategy: MergeStrategyThreeWayLines,
			Content: "hello world\n", BaseRevision: seed.TargetRevision, BaseContent: "hello\n",
		})
		if err != nil {
			t.Fatalf("expected an extensionless path to be eligible for three-way-lines-v1, got %v", err)
		}
	})
}

// TestMergeFileDedupedRetryReportsCorrectStrategy exercises
// findExistingMergeOpLocked's ContentIdentity dedup path directly — an
// identical retried request (same ContentIdentity) must return the exact
// same MergeResponse the original call did, including its Strategy, not a
// hardcoded one. Regression coverage for a real bug: prior to Phase 5,
// commitMergeResult and findExistingMergeOpLocked both unconditionally
// reported MergeStrategyGoTopLevelFunctions regardless of which strategy
// was actually used, which would have misreported every deduplicated
// three-way-lines-v1 retry.
func TestMergeFileDedupedRetryReportsCorrectStrategy(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	const (
		workspaceID = "ws_lines_merge_dedup"
		path        = "/src/a.ts"
	)

	baseContent := "export const a = 1;\n"
	seed, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: path, IfMatch: "0", Content: baseContent})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	identity := &ContentIdentity{Kind: "test-dedup", Key: "fixed-key", TTLSeconds: 3600}
	req := MergeRequest{
		WorkspaceID: workspaceID, Path: path, Strategy: MergeStrategyThreeWayLines,
		Content: "export const a = 2;\n", BaseRevision: seed.TargetRevision, BaseContent: baseContent,
		ContentIdentity: identity,
	}

	first, err := store.MergeFile(req)
	if err != nil {
		t.Fatalf("first merge: %v", err)
	}
	if first.Strategy != MergeStrategyThreeWayLines {
		t.Fatalf("first response Strategy = %q, want %q", first.Strategy, MergeStrategyThreeWayLines)
	}

	second, err := store.MergeFile(req)
	if err != nil {
		t.Fatalf("deduplicated retry: %v", err)
	}
	if second != first {
		t.Fatalf("deduplicated retry returned a different response: first=%+v second=%+v", first, second)
	}
}
