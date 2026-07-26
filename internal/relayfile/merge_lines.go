package relayfile

import (
	"fmt"
	"strings"
)

// MergeStrategyThreeWayLines is a language-agnostic three-way merge — the
// same class of algorithm as `diff3`/`git merge-file`. It operates on raw
// lines, not any language's AST, so it works identically for TypeScript,
// Python, JSON, Markdown, or any other text file, unlike
// MergeStrategyGoTopLevelFunctions (internal/relayfile/merge_go.go), which
// requires a Go-specific parser and only understands .go files.
//
// The tradeoff versus AST-based merge: a merged region here is not
// guaranteed to be a complete top-level language construct the way a
// merged Go function is — this strategy has no notion of "function" or
// "syntax" at all. It fails closed the same way everywhere else in this
// file does (ambiguity -> conflict -> nothing written), just with a
// different definition of ambiguity: two changes touching the same base
// line range, not "I don't understand this language."
const MergeStrategyThreeWayLines = "three-way-lines-v1"

// maxMergeDiffLines bounds the per-side line count the line-based merge
// will diff. Each LCS alignment is a full O(n*m) DP table sized int32 — at
// this bound that's roughly maxMergeDiffLines^2*4 bytes (~36MB) per table,
// and linesThreeWayMerge computes four of them (forward+backward alignment,
// each for base-vs-mine and base-vs-theirs) per merge attempt. A single
// real source file this long is already unusual; like every other bound in
// this file, exceeding it fails closed (ineligible), never degrades to a
// slower or more memory-hungry path.
const maxMergeDiffLines = 3000

// lineMergeConflict reports one contiguous base line range that both sides
// changed differently. Base/Mine/Theirs are that range's full text (with
// line endings) on each side — empty on the side that deleted it.
type lineMergeConflict struct {
	Unit   string
	Reason string
	Base   string
	Mine   string
	Theirs string
}

type lineMergeResult struct {
	Content string
}

// splitLinesKeepEnds splits s into lines, each retaining its own trailing
// "\n" so that concatenating the returned slice reconstructs s exactly,
// byte for byte. Only the last element may lack a trailing newline (true
// iff s itself doesn't end in one).
func splitLinesKeepEnds(s string) []string {
	if s == "" {
		return nil
	}
	lines := make([]string, 0, strings.Count(s, "\n")+1)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// lcsMatchPreferFirst returns, for each index i in base, the index in other
// that base[i] is matched to as part of a longest common subsequence of
// lines, or -1 if base[i] has no match (i.e. it was changed/deleted
// relative to other). Lines are compared by exact string equality,
// including their trailing newline. This is a textbook full DP LCS,
// bounded to maxMergeDiffLines by the caller so the O(n*m) table stays
// memory-safe.
//
// When the LCS is ambiguous — repeated/duplicated lines admit more than one
// optimal alignment — this backtrack greedily matches the EARLIEST valid
// occurrence. lcsMatchPreferLast computes the same LCS but greedily matches
// the LATEST valid occurrence instead. The two are combined in
// linesThreeWayMerge specifically to catch cases a single alignment choice
// would get silently wrong (see its doc comment).
func lcsMatchPreferFirst(base, other []string) []int {
	n, m := len(base), len(other)
	match := make([]int, n)
	for i := range match {
		match[i] = -1
	}
	if n == 0 || m == 0 {
		return match
	}
	// dp[i][j] = LCS length of base[i:], other[j:].
	dp := make([][]int32, n+1)
	for i := range dp {
		dp[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		row, nextRow := dp[i], dp[i+1]
		for j := m - 1; j >= 0; j-- {
			if base[i] == other[j] {
				row[j] = nextRow[j+1] + 1
			} else if nextRow[j] >= row[j+1] {
				row[j] = nextRow[j]
			} else {
				row[j] = row[j+1]
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		if base[i] == other[j] {
			match[i] = j
			i++
			j++
		} else if dp[i+1][j] > dp[i][j+1] {
			i++
		} else {
			j++
		}
	}
	return match
}

// lcsMatchPreferLast is lcsMatchPreferFirst's mirror image: it computes a
// PREFIX LCS table (dp[i][j] = LCS length of base[:i], other[:j]) and
// backtracks from the end of both sequences toward the start. On a tie it
// skips other (rather than base), preserving the latest possible base index;
// that is the exact opposite of lcsMatchPreferFirst's tie handling.
func lcsMatchPreferLast(base, other []string) []int {
	n, m := len(base), len(other)
	match := make([]int, n)
	for i := range match {
		match[i] = -1
	}
	if n == 0 || m == 0 {
		return match
	}
	dp := make([][]int32, n+1)
	for i := range dp {
		dp[i] = make([]int32, m+1)
	}
	for i := 1; i <= n; i++ {
		row, previousRow := dp[i], dp[i-1]
		for j := 1; j <= m; j++ {
			if base[i-1] == other[j-1] {
				row[j] = previousRow[j-1] + 1
			} else if previousRow[j] >= row[j-1] {
				row[j] = previousRow[j]
			} else {
				row[j] = row[j-1]
			}
		}
	}
	for i, j := n, m; i > 0 && j > 0; {
		if base[i-1] == other[j-1] {
			match[i-1] = j - 1
			i--
			j--
		} else if dp[i-1][j] > dp[i][j-1] {
			i--
		} else {
			j--
		}
	}
	return match
}

// mergeWithAlignment implements the core diff3-style resolution given a pair
// of already-computed base-vs-mine / base-vs-theirs alignments. It first
// converts each alignment to replacement hunks in base coordinates, then
// combines only overlapping hunk components. This is more precise than
// treating the complete span between two shared sync points as one region:
// adjacent base-line edits can have no shared unchanged line between them
// while still being independent and safely mergeable.
func mergeWithAlignment(baseLines, mineLines, theirsLines []string, matchMine, matchTheirs []int) (lineMergeResult, []lineMergeConflict) {
	mineChanges := lineChangesFromAlignment(baseLines, mineLines, matchMine)
	theirsChanges := lineChangesFromAlignment(baseLines, theirsLines, matchTheirs)
	output := make([]string, 0, len(baseLines)+len(mineChanges)+len(theirsChanges))
	var conflicts []lineMergeConflict
	mineIndex, theirsIndex, baseIndex := 0, 0, 0
	for mineIndex < len(mineChanges) || theirsIndex < len(theirsChanges) {
		mineGroup, theirsGroup, nextMine, nextTheirs := overlappingChangeComponent(mineChanges, theirsChanges, mineIndex, theirsIndex)
		start, end := changeComponentBounds(mineGroup, theirsGroup)
		output = append(output, baseLines[baseIndex:start]...)

		baseRegion := baseLines[start:end]
		mineRegion := renderChangeComponent(baseLines, start, end, mineGroup)
		theirsRegion := renderChangeComponent(baseLines, start, end, theirsGroup)
		baseText := strings.Join(baseRegion, "")
		mineText := strings.Join(mineRegion, "")
		theirsText := strings.Join(theirsRegion, "")

		switch {
		case mineText == theirsText:
			// Includes identical changes on both sides.
			output = append(output, mineRegion...)
		case mineText == baseText:
			// Mine left this component untouched; theirs' change wins.
			output = append(output, theirsRegion...)
		case theirsText == baseText:
			// Theirs left this component untouched; mine's change wins.
			output = append(output, mineRegion...)
		default:
			unit := fmt.Sprintf("base lines %d-%d", start+1, end)
			if start == end {
				// A pure insertion has no base-line span of its own; describe
				// it by the insertion point instead of an inverted range.
				unit = fmt.Sprintf("insertion before base line %d", start+1)
			}
			conflicts = append(conflicts, lineMergeConflict{
				Unit:   unit,
				Reason: "concurrently changed",
				Base:   baseText,
				Mine:   mineText,
				Theirs: theirsText,
			})
			// Keep scanning to report every independent conflict. Output is
			// discarded below whenever this slice is non-empty.
		}

		baseIndex = end
		mineIndex, theirsIndex = nextMine, nextTheirs
	}
	output = append(output, baseLines[baseIndex:]...)

	if len(conflicts) > 0 {
		return lineMergeResult{}, conflicts
	}
	return lineMergeResult{Content: strings.Join(output, "")}, nil
}

// lineChange replaces base[start:end] with lines. An insertion has
// start == end. Changes from one side never overlap because they come from a
// single ordered LCS alignment.
type lineChange struct {
	start, end int
	lines      []string
}

func lineChangesFromAlignment(base, other []string, match []int) []lineChange {
	changes := make([]lineChange, 0)
	previousBase, previousOther := -1, -1
	for baseIndex := 0; baseIndex <= len(base); baseIndex++ {
		otherIndex := len(other)
		if baseIndex < len(base) {
			if match[baseIndex] < 0 {
				continue
			}
			otherIndex = match[baseIndex]
		}
		start, end := previousBase+1, baseIndex
		if start != end || previousOther+1 != otherIndex {
			changes = append(changes, lineChange{
				start: start,
				end:   end,
				lines: other[previousOther+1 : otherIndex],
			})
		}
		previousBase, previousOther = baseIndex, otherIndex
	}
	return changes
}

// overlappingChangeComponent returns the next connected component of the
// bipartite overlap graph between mine's and theirs' hunks. Adjacent hunks
// from the same side alone do not join a component, but a hunk on one side
// may bridge several hunks on the other.
func overlappingChangeComponent(mine, theirs []lineChange, mineIndex, theirsIndex int) ([]lineChange, []lineChange, int, int) {
	mineGroup, theirsGroup := make([]lineChange, 0), make([]lineChange, 0)
	if theirsIndex == len(theirs) || (mineIndex < len(mine) && mine[mineIndex].start <= theirs[theirsIndex].start) {
		mineGroup = append(mineGroup, mine[mineIndex])
		mineIndex++
	} else {
		theirsGroup = append(theirsGroup, theirs[theirsIndex])
		theirsIndex++
	}

	for expanded := true; expanded; {
		expanded = false
		for mineIndex < len(mine) && changeOverlapsAny(mine[mineIndex], theirsGroup) {
			mineGroup = append(mineGroup, mine[mineIndex])
			mineIndex++
			expanded = true
		}
		for theirsIndex < len(theirs) && changeOverlapsAny(theirs[theirsIndex], mineGroup) {
			theirsGroup = append(theirsGroup, theirs[theirsIndex])
			theirsIndex++
			expanded = true
		}
	}
	return mineGroup, theirsGroup, mineIndex, theirsIndex
}

func changeOverlapsAny(change lineChange, others []lineChange) bool {
	for _, other := range others {
		if lineChangesOverlap(change, other) {
			return true
		}
	}
	return false
}

func lineChangesOverlap(first, second lineChange) bool {
	firstInsertion := first.start == first.end
	secondInsertion := second.start == second.end
	switch {
	case firstInsertion && secondInsertion:
		return first.start == second.start
	case firstInsertion:
		return first.start >= second.start && first.start <= second.end
	case secondInsertion:
		return second.start >= first.start && second.start <= first.end
	default:
		return first.start < second.end && second.start < first.end
	}
}

func changeComponentBounds(mine, theirs []lineChange) (int, int) {
	start, end := -1, -1
	for _, changes := range [][]lineChange{mine, theirs} {
		for _, change := range changes {
			if start < 0 || change.start < start {
				start = change.start
			}
			if change.end > end {
				end = change.end
			}
		}
	}
	return start, end
}

func renderChangeComponent(base []string, start, end int, changes []lineChange) []string {
	output := make([]string, 0, end-start)
	baseIndex := start
	for _, change := range changes {
		output = append(output, base[baseIndex:change.start]...)
		output = append(output, change.lines...)
		baseIndex = change.end
	}
	return append(output, base[baseIndex:end]...)
}

// linesThreeWayMerge implements the three-way-lines-v1 strategy.
//
// Safety invariant, matching goThreeWayMerge: on ANY conflict, no content
// is returned at all — the caller must treat this as an all-or-nothing
// merge, never a partial one.
//
// Repeated/duplicated lines near an edit make the LCS alignment between
// base and a changed side genuinely ambiguous — more than one alignment
// achieves the same optimal length, and different choices can attribute an
// insertion to a different position. Trusting a single (e.g. greedy-
// leftmost) alignment can silently DROP one side's independent insertion
// when both sides happen to insert lines that are textually identical to
// something nearby (see TestLinesMergeAmbiguousDuplicateInsertionsConflict
// for a concrete example matching what `git merge-file` resolves
// correctly by choosing a different, non-colliding alignment). Rather than
// try to replicate git's exact resolution heuristic, this computes earliest
// and latest LCS alignments for each side and only proceeds when the complete
// base-to-side match maps are identical. Comparing only the resulting merge
// content is not strong enough: correlated choices can make multiple
// alignments appear to agree while silently collapsing an independent edit.
// A differing match map means the correspondence itself is ambiguous, so the
// safe response is a conflict before attributing any change to a base line.
func linesThreeWayMerge(base, mine, theirs string) (lineMergeResult, []lineMergeConflict, error) {
	baseLines := splitLinesKeepEnds(base)
	mineLines := splitLinesKeepEnds(mine)
	theirsLines := splitLinesKeepEnds(theirs)

	if len(baseLines) > maxMergeDiffLines || len(mineLines) > maxMergeDiffLines || len(theirsLines) > maxMergeDiffLines {
		return lineMergeResult{}, nil, fmt.Errorf("content exceeds %d lines, too large for %s", maxMergeDiffLines, MergeStrategyThreeWayLines)
	}
	// If both sides already have identical bytes, there is no merge decision
	// to make. In particular, repeated lines can still give the two LCS
	// backtracks different valid alignments of that same content; the
	// ambiguity check below must not turn this identity into a conflict.
	if mine == theirs {
		return lineMergeResult{Content: mine}, nil, nil
	}
	if mine == base {
		return lineMergeResult{Content: theirs}, nil, nil
	}
	if theirs == base {
		return lineMergeResult{Content: mine}, nil, nil
	}

	firstMineMatch := lcsMatchPreferFirst(baseLines, mineLines)
	lastMineMatch := lcsMatchPreferLast(baseLines, mineLines)
	firstTheirsMatch := lcsMatchPreferFirst(baseLines, theirsLines)
	lastTheirsMatch := lcsMatchPreferLast(baseLines, theirsLines)

	// Any difference proves that there are at least two equally optimal ways
	// to associate a base line with a side. Do not infer an edit location from
	// that ambiguity: doing so can turn a delete-vs-replace conflict into a
	// clean-looking, lossy merge.
	if !lcsMatchesEqual(firstMineMatch, lastMineMatch) || !lcsMatchesEqual(firstTheirsMatch, lastTheirsMatch) {
		return lineMergeResult{}, []lineMergeConflict{{
			Unit:   "whole file",
			Reason: "ambiguous merge alignment: repeated or duplicated lines admit more than one valid resolution",
			Base:   base,
			Mine:   mine,
			Theirs: theirs,
		}}, nil
	}

	result, conflicts := mergeWithAlignment(baseLines, mineLines, theirsLines, firstMineMatch, firstTheirsMatch)
	return result, conflicts, nil
}

func lcsMatchesEqual(first, last []int) bool {
	if len(first) != len(last) {
		return false
	}
	for i := range first {
		if first[i] != last[i] {
			return false
		}
	}
	return true
}
