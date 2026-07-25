package relayfile

import (
	"strings"
	"testing"
)

func TestLinesMergeDisjointLineChangesAutoMerge(t *testing.T) {
	base := "line one\nline two\nline three\nline four\nline five\n"
	mine := "line one CHANGED\nline two\nline three\nline four\nline five\n"
	theirs := "line one\nline two\nline three\nline four CHANGED\nline five\n"
	want := "line one CHANGED\nline two\nline three\nline four CHANGED\nline five\n"

	result, conflicts, err := linesThreeWayMerge(base, mine, theirs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	if result.Content != want {
		t.Fatalf("merged content = %q, want %q", result.Content, want)
	}
}

func TestLinesMergeSameLineConflict(t *testing.T) {
	base := "line one\nline two\nline three\n"
	mine := "line one\nMINE CHANGE\nline three\n"
	theirs := "line one\nTHEIRS CHANGE\nline three\n"

	result, conflicts, err := linesThreeWayMerge(base, mine, theirs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "" {
		t.Fatalf("expected no content on conflict, got %q", result.Content)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d: %+v", len(conflicts), conflicts)
	}
	c := conflicts[0]
	if c.Base != "line two\n" {
		t.Fatalf("conflict.Base = %q, want %q", c.Base, "line two\n")
	}
	if c.Mine != "MINE CHANGE\n" {
		t.Fatalf("conflict.Mine = %q, want %q", c.Mine, "MINE CHANGE\n")
	}
	if c.Theirs != "THEIRS CHANGE\n" {
		t.Fatalf("conflict.Theirs = %q, want %q", c.Theirs, "THEIRS CHANGE\n")
	}
	if c.Reason != "concurrently changed" {
		t.Fatalf("conflict.Reason = %q", c.Reason)
	}
}

func TestLinesMergeIdenticalChangeOnBothSidesAutoMerges(t *testing.T) {
	base := "line one\nline two\nline three\n"
	mine := "line one\nSAME CHANGE\nline three\n"
	theirs := "line one\nSAME CHANGE\nline three\n"

	result, conflicts, err := linesThreeWayMerge(base, mine, theirs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	if result.Content != mine {
		t.Fatalf("merged content = %q, want %q", result.Content, mine)
	}
}

func TestLinesMergeOneSideUnchangedOtherChangesAutoMerges(t *testing.T) {
	base := "line one\nline two\nline three\n"
	mine := "line one\nline two\nline three\n" // unchanged
	theirs := "line one\nCHANGED\nline three\n"

	result, conflicts, err := linesThreeWayMerge(base, mine, theirs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	if result.Content != theirs {
		t.Fatalf("merged content = %q, want %q", result.Content, theirs)
	}
}

func TestLinesMergePureInsertionBothSidesAutoMerges(t *testing.T) {
	base := "alpha\nbeta\ngamma\n"
	mine := "alpha\nMINE INSERTED\nbeta\ngamma\n"
	theirs := "alpha\nbeta\ngamma\nTHEIRS INSERTED\n"
	want := "alpha\nMINE INSERTED\nbeta\ngamma\nTHEIRS INSERTED\n"

	result, conflicts, err := linesThreeWayMerge(base, mine, theirs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	if result.Content != want {
		t.Fatalf("merged content = %q, want %q", result.Content, want)
	}
}

func TestLinesMergePureDeletionAutoMerges(t *testing.T) {
	base := "alpha\nbeta\ngamma\ndelta\n"
	mine := "alpha\ngamma\ndelta\n" // deleted beta
	theirs := "alpha\nbeta\ngamma\ndelta CHANGED\n"
	want := "alpha\ngamma\ndelta CHANGED\n"

	result, conflicts, err := linesThreeWayMerge(base, mine, theirs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	if result.Content != want {
		t.Fatalf("merged content = %q, want %q", result.Content, want)
	}
}

func TestLinesMergeBothDeleteSameLinesAutoMerges(t *testing.T) {
	base := "alpha\nbeta\ngamma\n"
	mine := "alpha\ngamma\n"
	theirs := "alpha\ngamma\n"
	want := "alpha\ngamma\n"

	result, conflicts, err := linesThreeWayMerge(base, mine, theirs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	if result.Content != want {
		t.Fatalf("merged content = %q, want %q", result.Content, want)
	}
}

func TestLinesMergeDeleteVsModifyConflicts(t *testing.T) {
	base := "alpha\nbeta\ngamma\n"
	mine := "alpha\ngamma\n"                 // deleted beta
	theirs := "alpha\nBETA CHANGED\ngamma\n" // modified beta

	result, conflicts, err := linesThreeWayMerge(base, mine, theirs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "" {
		t.Fatalf("expected no content on conflict, got %q", result.Content)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d: %+v", len(conflicts), conflicts)
	}
	if conflicts[0].Mine != "" {
		t.Fatalf("conflict.Mine = %q, want empty (deleted)", conflicts[0].Mine)
	}
	if conflicts[0].Theirs != "BETA CHANGED\n" {
		t.Fatalf("conflict.Theirs = %q", conflicts[0].Theirs)
	}
}

func TestLinesMergeDeleteVsUnchangedAppliesTheDeletion(t *testing.T) {
	base := "alpha\nbeta\ngamma\n"
	mine := "alpha\ngamma\n"         // deleted beta
	theirs := "alpha\nbeta\ngamma\n" // unchanged
	want := "alpha\ngamma\n"

	result, conflicts, err := linesThreeWayMerge(base, mine, theirs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	if result.Content != want {
		t.Fatalf("merged content = %q, want %q", result.Content, want)
	}
}

func TestLinesMergeNoChangesReturnsBaseUnchanged(t *testing.T) {
	base := "alpha\nbeta\ngamma\n"

	result, conflicts, err := linesThreeWayMerge(base, base, base)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	if result.Content != base {
		t.Fatalf("merged content = %q, want %q", result.Content, base)
	}
}

func TestLinesMergeDifferentInsertionsAtSamePositionConflict(t *testing.T) {
	base := "alpha\nbeta\n"
	mine := "alpha\nMINE INSERTED\nbeta\n"
	theirs := "alpha\nTHEIRS INSERTED\nbeta\n"

	result, conflicts, err := linesThreeWayMerge(base, mine, theirs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "" {
		t.Fatalf("expected no content on conflict, got %q", result.Content)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict (ambiguous insertion order), got %d: %+v", len(conflicts), conflicts)
	}
	if conflicts[0].Base != "" {
		t.Fatalf("conflict.Base = %q, want empty (pure insertion, nothing in base)", conflicts[0].Base)
	}
}

// TestLinesMergeAmbiguousDuplicateInsertionsConflict is a regression test
// for a real P1 finding (external review on PR #375): a repeated line near
// an independent insertion on each side makes the LCS alignment ambiguous.
// base has one "a"; mine independently inserts a second "a" after it;
// theirs independently inserts "b" before the "a" AND a second "a" after
// it. A single greedy-leftmost alignment can align mine's inserted "a" and
// theirs' inserted "a" to the SAME logical gap purely because they're
// textually identical, silently collapsing what are actually two
// independent insertions into one and losing content — `git merge-file`
// resolves this correctly (by choosing a different, non-colliding
// alignment) to "x\nb\na\na\na\ny\n" (three a's: theirs' insertion, the
// original, and mine's insertion). This strategy doesn't try to replicate
// that exact resolution; it only has to never silently guess wrong, so the
// bar here is "conflict" not "the git answer" — see linesThreeWayMerge's
// doc comment for why cross-checking two alignments is how that's caught.
func TestLinesMergeAmbiguousDuplicateInsertionsConflict(t *testing.T) {
	base := "x\na\ny\n"
	mine := "x\na\na\ny\n"
	theirs := "x\nb\na\na\ny\n"

	result, conflicts, err := linesThreeWayMerge(base, mine, theirs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "" {
		t.Fatalf("expected no content on ambiguous alignment, got %q — this would be silent data loss if it doesn't contain all of: theirs' \"b\", the original \"a\", mine's inserted \"a\", and theirs' inserted \"a\"", result.Content)
	}
	if len(conflicts) == 0 {
		t.Fatal("expected at least one conflict for an ambiguous alignment, got none — this means one side's independent insertion was silently dropped")
	}
}

func TestLinesMergeNoTrailingNewlinePreservedExactly(t *testing.T) {
	base := "alpha\nbeta\ngamma" // no trailing newline
	mine := "alpha CHANGED\nbeta\ngamma"
	theirs := "alpha\nbeta\ngamma CHANGED"
	want := "alpha CHANGED\nbeta\ngamma CHANGED"

	result, conflicts, err := linesThreeWayMerge(base, mine, theirs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	if result.Content != want {
		t.Fatalf("merged content = %q, want %q", result.Content, want)
	}
}

func TestLinesMergeMultipleConflictsAllReported(t *testing.T) {
	base := "one\ntwo\nthree\nfour\nfive\n"
	mine := "MINE-ONE\ntwo\nthree\nfour\nMINE-FIVE\n"
	theirs := "THEIRS-ONE\ntwo\nthree\nfour\nTHEIRS-FIVE\n"

	result, conflicts, err := linesThreeWayMerge(base, mine, theirs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "" {
		t.Fatalf("expected no content on conflict, got %q", result.Content)
	}
	if len(conflicts) != 2 {
		t.Fatalf("expected 2 conflicts (one at each end), got %d: %+v", len(conflicts), conflicts)
	}
}

func TestLinesMergeExceedsLineCountIsIneligible(t *testing.T) {
	var b strings.Builder
	for i := 0; i <= maxMergeDiffLines; i++ {
		b.WriteString("line\n")
	}
	tooLong := b.String()

	_, _, err := linesThreeWayMerge(tooLong, tooLong, tooLong)
	if err == nil {
		t.Fatal("expected an error for content exceeding maxMergeDiffLines")
	}
}

// TestLinesMergeResultsReconstructInputsExactlyWhenUnchanged is a narrow
// safety check: when one side makes no changes at all, the merge result
// must be byte-for-byte identical to the other side's content — no
// silent truncation, no accidental extra/missing newline at any boundary.
func TestLinesMergeResultsReconstructInputsExactlyWhenUnchanged(t *testing.T) {
	cases := []struct {
		name          string
		base, changed string
	}{
		{"single line no newline", "x", "y"},
		{"single line with newline", "x\n", "y\n"},
		{"many lines", "a\nb\nc\nd\ne\nf\ng\n", "a\nb\nCHANGED\nd\ne\nf\ng\n"},
		{"empty base", "", "new content\n"},
		{"change at very start", "first\nsecond\n", "CHANGED\nsecond\n"},
		{"change at very end", "first\nsecond\n", "first\nCHANGED\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, conflicts, err := linesThreeWayMerge(tc.base, tc.changed, tc.base)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(conflicts) != 0 {
				t.Fatalf("unexpected conflicts: %+v", conflicts)
			}
			if result.Content != tc.changed {
				t.Fatalf("merged content = %q, want %q", result.Content, tc.changed)
			}
		})
	}
}

func TestSplitLinesKeepEndsRoundTrips(t *testing.T) {
	cases := []string{
		"",
		"a",
		"a\n",
		"a\nb",
		"a\nb\n",
		"\n",
		"\n\n",
		"a\n\nb\n",
	}
	for _, s := range cases {
		lines := splitLinesKeepEnds(s)
		if got := strings.Join(lines, ""); got != s {
			t.Fatalf("splitLinesKeepEnds(%q) round-trip = %q, want %q", s, got, s)
		}
	}
}
