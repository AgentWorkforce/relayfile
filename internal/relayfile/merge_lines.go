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
// will diff. lcsMatch is a full O(n*m) DP table sized int32 — at this
// bound that's roughly maxMergeDiffLines^2*4 bytes (~36MB) per call, twice
// per merge attempt (base-vs-mine, base-vs-theirs). A single real source
// file this long is already unusual; like every other bound in this file,
// exceeding it fails closed (ineligible), never degrades to a slower or
// more memory-hungry path.
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

// lcsMatch returns, for each index i in base, the index in other that
// base[i] is matched to as part of a longest common subsequence of lines,
// or -1 if base[i] has no match (i.e. it was changed/deleted relative to
// other). Lines are compared by exact string equality, including their
// trailing newline. This is a textbook full DP LCS, bounded to
// maxMergeDiffLines by the caller so the O(n*m) table stays memory-safe.
func lcsMatch(base, other []string) []int {
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
		} else if dp[i+1][j] >= dp[i][j+1] {
			i++
		} else {
			j++
		}
	}
	return match
}

// linesThreeWayMerge implements the three-way-lines-v1 strategy: a
// diff3-style merge over base/mine/theirs, resolved at "sync points" —
// base lines that are unchanged (present verbatim) in both mine and
// theirs. Between consecutive sync points, whichever side actually
// changed something relative to base wins; if both changed it identically,
// that's not a conflict either; only a genuine divergence — both sides
// changed the same span to different content — is reported as a conflict.
//
// Safety invariant, matching goThreeWayMerge: on ANY conflict, no content
// is returned at all — the caller must treat this as an all-or-nothing
// merge, never a partial one.
func linesThreeWayMerge(base, mine, theirs string) (lineMergeResult, []lineMergeConflict, error) {
	baseLines := splitLinesKeepEnds(base)
	mineLines := splitLinesKeepEnds(mine)
	theirsLines := splitLinesKeepEnds(theirs)

	if len(baseLines) > maxMergeDiffLines || len(mineLines) > maxMergeDiffLines || len(theirsLines) > maxMergeDiffLines {
		return lineMergeResult{}, nil, fmt.Errorf("content exceeds %d lines, too large for %s", maxMergeDiffLines, MergeStrategyThreeWayLines)
	}

	matchMine := lcsMatch(baseLines, mineLines)
	matchTheirs := lcsMatch(baseLines, theirsLines)

	type syncPoint struct {
		base, mine, theirs int
	}
	syncPoints := make([]syncPoint, 0, len(baseLines)+2)
	syncPoints = append(syncPoints, syncPoint{base: -1, mine: -1, theirs: -1})
	for i := 0; i < len(baseLines); i++ {
		if matchMine[i] >= 0 && matchTheirs[i] >= 0 {
			syncPoints = append(syncPoints, syncPoint{base: i, mine: matchMine[i], theirs: matchTheirs[i]})
		}
	}
	syncPoints = append(syncPoints, syncPoint{base: len(baseLines), mine: len(mineLines), theirs: len(theirsLines)})

	var output []string
	var conflicts []lineMergeConflict

	for k := 0; k < len(syncPoints)-1; k++ {
		prev, next := syncPoints[k], syncPoints[k+1]

		baseRegion := baseLines[prev.base+1 : next.base]
		mineRegion := mineLines[prev.mine+1 : next.mine]
		theirsRegion := theirsLines[prev.theirs+1 : next.theirs]

		baseText := strings.Join(baseRegion, "")
		mineText := strings.Join(mineRegion, "")
		theirsText := strings.Join(theirsRegion, "")

		switch {
		case mineText == theirsText:
			// Includes the common "neither side changed this span" case,
			// and "both sides made the identical change" — neither is a
			// real conflict.
			output = append(output, mineRegion...)
		case mineText == baseText:
			// Mine left this span untouched; theirs' change wins.
			output = append(output, theirsRegion...)
		case theirsText == baseText:
			// Theirs left this span untouched; mine's change wins.
			output = append(output, mineRegion...)
		default:
			conflicts = append(conflicts, lineMergeConflict{
				Unit:   fmt.Sprintf("base lines %d-%d", prev.base+2, next.base+1),
				Reason: "concurrently changed",
				Base:   baseText,
				Mine:   mineText,
				Theirs: theirsText,
			})
			// Deliberately keep scanning rather than return immediately:
			// collecting every conflict in one pass gives the caller a
			// complete picture instead of a single one at a time across
			// repeated retries. output from here on is never used (any
			// conflict discards the whole result), so no further
			// bookkeeping is needed for this region.
		}

		if next.base < len(baseLines) {
			// The sync point's own line: by construction it matches both
			// mine[next.mine] and theirs[next.theirs] exactly, so any of
			// the three copies is identical.
			output = append(output, baseLines[next.base])
		}
	}

	if len(conflicts) > 0 {
		return lineMergeResult{}, conflicts, nil
	}
	return lineMergeResult{Content: strings.Join(output, "")}, nil, nil
}
