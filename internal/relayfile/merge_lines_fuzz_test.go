package relayfile

import (
	"strings"
	"testing"
)

// FuzzLinesThreeWayMerge exercises the line merge with related versions,
// rather than arbitrary unrelated strings. It creates a repeated-line base
// from a deliberately small alphabet, then applies independently tracked
// edits to each side. The edit scripts are in original-base coordinates, so
// fuzzExpectedLineMerge is an oracle independent from the LCS-based merger.
//
// An insertion targets a gap (rather than a base line). Gaps are deliberately
// tracked separately: two different insertions into the same gap do not have
// a deterministic order and must not be called disjoint changes. This also
// lets the test distinguish them from line replacements/deletions.
func FuzzLinesThreeWayMerge(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte("handwritten:ambiguous-duplicate-insertions"),
		[]byte("handwritten:disjoint-line-changes"),
		[]byte("handwritten:same-line-conflict"),
		[]byte("handwritten:different-insertions-same-position"),
		[]byte("handwritten:pure-insertions"),
		[]byte("handwritten:no-trailing-newline"),
		[]byte("handwritten:delete-versus-modify"),
		// A few short byte streams make empty inputs, high repetition, and
		// small boundary cases part of the initial structured corpus too.
		{},
		{0},
		{255},
		{0, 0, 0, 0, 0, 0, 0, 0},
		{255, 255, 255, 255, 255, 255, 255, 255},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		base, mine, theirs, expectation := fuzzLineMergeCase(data)

		result, conflicts, err := linesThreeWayMerge(base, mine, theirs)
		if err != nil {
			t.Fatalf("merge returned unexpected error for base=%q mine=%q theirs=%q: %v", base, mine, theirs, err)
		}
		if len(conflicts) > 0 && result.Content != "" {
			t.Fatalf("conflicting merge returned content %q; base=%q mine=%q theirs=%q conflicts=%+v", result.Content, base, mine, theirs, conflicts)
		}

		switch {
		case expectation.mustConflict:
			if len(conflicts) == 0 {
				t.Fatalf("expected a conflict, got content %q; base=%q mine=%q theirs=%q", result.Content, base, mine, theirs)
			}
		case expectation.expected != nil:
			if len(conflicts) > 0 {
				if expectation.mustSucceed {
					t.Fatalf("expected non-conflicting merge %q, got conflicts %+v; base=%q mine=%q theirs=%q", *expectation.expected, conflicts, base, mine, theirs)
				}
				break
			}
			if expectation.mustSucceed && result.Content != *expectation.expected {
				t.Fatalf("merged content = %q, want oracle result %q; base=%q mine=%q theirs=%q", result.Content, *expectation.expected, base, mine, theirs)
			}
		}

		// These identities must hold regardless of the relationship between
		// mine and theirs. They cover empty inputs, repeated lines, and a
		// missing terminal newline through the structured generator.
		assertFuzzLineMergeIdentity(t, base, mine)
		assertFuzzLineMergeIdentity(t, base, base)
	})
}

type fuzzLineMergeExpectation struct {
	expected     *string
	mustConflict bool
	mustSucceed  bool
}

type fuzzLineEditKind uint8

const (
	fuzzLineInsert fuzzLineEditKind = iota
	fuzzLineDelete
	fuzzLineReplace
)

// fuzzLineEdit positions are in the original base: inserts use a gap from
// 0 through len(base), while deletes and replacements use a base line index.
type fuzzLineEdit struct {
	kind     fuzzLineEditKind
	position int
	line     string
}

type fuzzLineEditScript struct {
	lineEdits map[int]fuzzLineEdit
	inserts   map[int]fuzzLineEdit
}

// fuzzByteSource deterministically turns a native-fuzz byte input into a
// structured test case. Once exhausted, it returns zeroes; that keeps even an
// empty corpus input valid and makes the amount of work strictly bounded.
type fuzzByteSource struct {
	data []byte
	pos  int
}

func (s *fuzzByteSource) next() byte {
	if s.pos >= len(s.data) {
		return 0
	}
	b := s.data[s.pos]
	s.pos++
	return b
}

func fuzzLineMergeCase(data []byte) (string, string, string, fuzzLineMergeExpectation) {
	if base, mine, theirs, expectation, ok := fuzzHandwrittenLineMergeCase(data); ok {
		return base, mine, theirs, expectation
	}

	source := fuzzByteSource{data: data}
	baseLines := fuzzGenerateBaseLines(&source)
	mineScript := fuzzGenerateLineEditScript(&source, baseLines)
	theirsScript := fuzzGenerateLineEditScript(&source, baseLines)
	mineLines := fuzzApplyLineEditScript(baseLines, mineScript)
	theirsLines := fuzzApplyLineEditScript(baseLines, theirsScript)

	base := strings.Join(baseLines, "")
	mine := strings.Join(mineLines, "")
	theirs := strings.Join(theirsLines, "")
	// Likewise, identical final sides have no observable distinction: their
	// common bytes are the only sound result regardless of whether their
	// independently generated histories happened to differ.
	if mine == theirs {
		return base, mine, theirs, fuzzLineMergeExpectation{expected: &mine, mustSucceed: true}
	}
	// A compensating script (for example, delete one "z" then insert an
	// identical "z" immediately before it) can reconstruct base byte for
	// byte. Its hidden operation history is unobservable to a text-only
	// three-way merge, so the only sound oracle is that this input side is
	// unchanged. Without this guard the harness would report impossible
	// failures based on information linesThreeWayMerge never receives.
	if mine == base {
		return base, mine, theirs, fuzzLineMergeExpectation{expected: &theirs, mustSucceed: true}
	}
	if theirs == base {
		return base, mine, theirs, fuzzLineMergeExpectation{expected: &mine, mustSucceed: true}
	}
	if expected, conflicts := fuzzExpectedLineMerge(baseLines, mineScript, theirsScript); conflicts {
		// Different insertions in one gap are a conflict only when that
		// operation is visible in the final text. A more complex script can
		// compensate for one of the insertions, making the same three strings
		// consistent with a non-conflicting history. Divergent outcomes for
		// the same base line are observable and remain a mandatory conflict.
		if fuzzHasDivergentLineEdit(mineScript, theirsScript) && fuzzLineMergeHasUniqueAnchors(baseLines, mineScript, theirsScript) {
			return base, mine, theirs, fuzzLineMergeExpectation{mustConflict: true}
		}
		return base, mine, theirs, fuzzLineMergeExpectation{}
	} else {
		return base, mine, theirs, fuzzLineMergeExpectation{
			expected:    &expected,
			mustSucceed: fuzzLineMergeHasUniqueAnchors(baseLines, mineScript, theirsScript),
		}
	}
}

func fuzzHandwrittenLineMergeCase(data []byte) (string, string, string, fuzzLineMergeExpectation, bool) {
	caseName := string(data)
	match := func(base, mine, theirs, expected string) (string, string, string, fuzzLineMergeExpectation, bool) {
		return base, mine, theirs, fuzzLineMergeExpectation{expected: &expected, mustSucceed: true}, true
	}
	conflict := func(base, mine, theirs string) (string, string, string, fuzzLineMergeExpectation, bool) {
		return base, mine, theirs, fuzzLineMergeExpectation{mustConflict: true}, true
	}

	switch caseName {
	case "handwritten:ambiguous-duplicate-insertions":
		return conflict("x\na\ny\n", "x\na\na\ny\n", "x\nb\na\na\ny\n")
	case "handwritten:disjoint-line-changes":
		return match(
			"line one\nline two\nline three\nline four\nline five\n",
			"line one CHANGED\nline two\nline three\nline four\nline five\n",
			"line one\nline two\nline three\nline four CHANGED\nline five\n",
			"line one CHANGED\nline two\nline three\nline four CHANGED\nline five\n",
		)
	case "handwritten:same-line-conflict":
		return conflict("line one\nline two\nline three\n", "line one\nMINE CHANGE\nline three\n", "line one\nTHEIRS CHANGE\nline three\n")
	case "handwritten:different-insertions-same-position":
		return conflict("alpha\nbeta\n", "alpha\nMINE INSERTED\nbeta\n", "alpha\nTHEIRS INSERTED\nbeta\n")
	case "handwritten:pure-insertions":
		return match("alpha\nbeta\ngamma\n", "alpha\nMINE INSERTED\nbeta\ngamma\n", "alpha\nbeta\ngamma\nTHEIRS INSERTED\n", "alpha\nMINE INSERTED\nbeta\ngamma\nTHEIRS INSERTED\n")
	case "handwritten:no-trailing-newline":
		return match("alpha\nbeta\ngamma", "alpha CHANGED\nbeta\ngamma", "alpha\nbeta\ngamma CHANGED", "alpha CHANGED\nbeta\ngamma CHANGED")
	case "handwritten:delete-versus-modify":
		return conflict("alpha\nbeta\ngamma\n", "alpha\ngamma\n", "alpha\nBETA CHANGED\ngamma\n")
	default:
		return "", "", "", fuzzLineMergeExpectation{}, false
	}
}

var fuzzLineAlphabet = []string{
	"a\n",
	"b\n",
	"aa\n",
	"line1\n",
	"x\n",
	"y\n",
	"z\n",
}

func fuzzGenerateBaseLines(source *fuzzByteSource) []string {
	const maxBaseLines = 30
	count := int(source.next()) % (maxBaseLines + 1)
	if count == 0 {
		return nil
	}
	lines := make([]string, count)
	for i := range lines {
		lines[i] = fuzzLineAlphabet[int(source.next())%len(fuzzLineAlphabet)]
	}
	// Preserve a valid line structure: only the final logical line may lack
	// its newline. Script generation observes this restriction as well.
	if source.next()&1 != 0 {
		lines[len(lines)-1] = strings.TrimSuffix(lines[len(lines)-1], "\n")
	}
	return lines
}

func fuzzGenerateLineEditScript(source *fuzzByteSource, base []string) fuzzLineEditScript {
	const maxEdits = 6
	script := fuzzLineEditScript{
		lineEdits: make(map[int]fuzzLineEdit),
		inserts:   make(map[int]fuzzLineEdit),
	}
	want := int(source.next()) % (maxEdits + 1)
	for attempts := 0; attempts < maxEdits*4 && len(script.lineEdits)+len(script.inserts) < want; attempts++ {
		kind := fuzzLineEditKind(source.next() % 3)
		switch kind {
		case fuzzLineInsert:
			gap := int(source.next()) % (len(base) + 1)
			if !fuzzCanAddInsertion(script, gap, len(base)) {
				continue
			}
			// Inserting after a non-newline-terminated final line would
			// change it into the middle of a physical line. Exclude that
			// representation detail here; the handwritten seeds still
			// exercise terminal-newline behavior directly.
			if len(base) > 0 && gap == len(base) && !strings.HasSuffix(base[len(base)-1], "\n") {
				continue
			}
			line := fuzzLineAlphabet[int(source.next())%len(fuzzLineAlphabet)]
			if len(base) == 0 && source.next()&1 != 0 {
				line = strings.TrimSuffix(line, "\n")
			}
			script.inserts[gap] = fuzzLineEdit{kind: fuzzLineInsert, position: gap, line: line}
		case fuzzLineDelete, fuzzLineReplace:
			if len(base) == 0 {
				continue
			}
			index := int(source.next()) % len(base)
			if !fuzzCanAddLineEdit(script, index, len(base)) {
				continue
			}
			if kind == fuzzLineDelete {
				script.lineEdits[index] = fuzzLineEdit{kind: fuzzLineDelete, position: index}
				continue
			}
			line := fuzzDifferentLine(source, base[index])
			if index == len(base)-1 && !strings.HasSuffix(base[index], "\n") {
				line = strings.TrimSuffix(line, "\n")
			}
			script.lineEdits[index] = fuzzLineEdit{kind: fuzzLineReplace, position: index, line: line}
		}
	}
	return script
}

// Keep every generated side script canonical: a replacement/deletion cannot
// sit beside another line edit or an insertion at either boundary. Adjacent
// operations can compose into a different, shorter text diff (for example,
// replace a line and then delete its neighbor), making the operation history
// unobservable even when all line values are distinct. The fuzzer still
// explores independent adjacent edits across mine and theirs, where merging
// them correctly is the algorithm's job.
func fuzzCanAddInsertion(script fuzzLineEditScript, gap, baseLength int) bool {
	for _, nearbyGap := range []int{gap - 1, gap, gap + 1} {
		if _, exists := script.inserts[nearbyGap]; exists {
			return false
		}
	}
	return !fuzzInsertionBordersLineEdit(gap, baseLength, script)
}

func fuzzCanAddLineEdit(script fuzzLineEditScript, index, baseLength int) bool {
	for _, nearbyIndex := range []int{index - 1, index, index + 1} {
		if _, exists := script.lineEdits[nearbyIndex]; exists {
			return false
		}
	}
	return !fuzzLineEditBordersInsertion(index, baseLength, script)
}

func fuzzLineEditBordersInsertion(index, baseLength int, script fuzzLineEditScript) bool {
	for _, gap := range []int{index, index + 1} {
		if gap >= 0 && gap <= baseLength {
			if _, exists := script.inserts[gap]; exists {
				return true
			}
		}
	}
	return false
}

func fuzzDifferentLine(source *fuzzByteSource, current string) string {
	start := int(source.next())
	for offset := 0; offset < len(fuzzLineAlphabet); offset++ {
		line := fuzzLineAlphabet[(start+offset)%len(fuzzLineAlphabet)]
		if line != current && strings.TrimSuffix(line, "\n") != current {
			return line
		}
	}
	panic("fuzz line alphabet unexpectedly has no distinct entry")
}

func fuzzApplyLineEditScript(base []string, script fuzzLineEditScript) []string {
	output := make([]string, 0, len(base)+len(script.inserts))
	for gap := 0; gap <= len(base); gap++ {
		if insert, ok := script.inserts[gap]; ok {
			output = append(output, insert.line)
		}
		if gap == len(base) {
			break
		}
		if edit, ok := script.lineEdits[gap]; ok {
			if edit.kind == fuzzLineReplace {
				output = append(output, edit.line)
			}
			continue // deletion omits the base line
		}
		output = append(output, base[gap])
	}
	return output
}

// fuzzExpectedLineMerge is intentionally separate from linesThreeWayMerge:
// it combines original-base-coordinate scripts directly, with no LCS or
// mergeWithAlignment calls. Equal changes at the same target are applied
// once; different changes to one line or one insertion gap conflict.
func fuzzExpectedLineMerge(base []string, mine, theirs fuzzLineEditScript) (string, bool) {
	for index, mineEdit := range mine.lineEdits {
		if theirsEdit, overlaps := theirs.lineEdits[index]; overlaps && fuzzLineEditResult(mineEdit) != fuzzLineEditResult(theirsEdit) {
			return "", true
		}
	}
	for gap, mineInsert := range mine.inserts {
		if theirsInsert, overlaps := theirs.inserts[gap]; overlaps && mineInsert.line != theirsInsert.line {
			return "", true
		}
	}

	output := make([]string, 0, len(base)+len(mine.inserts)+len(theirs.inserts))
	for gap := 0; gap <= len(base); gap++ {
		mineInsert, mineInserted := mine.inserts[gap]
		theirsInsert, theirsInserted := theirs.inserts[gap]
		switch {
		case mineInserted:
			output = append(output, mineInsert.line)
		case theirsInserted:
			output = append(output, theirsInsert.line)
		}
		if gap == len(base) {
			break
		}

		mineEdit, mineChanged := mine.lineEdits[gap]
		theirsEdit, theirsChanged := theirs.lineEdits[gap]
		switch {
		case mineChanged:
			if mineEdit.kind == fuzzLineReplace {
				output = append(output, mineEdit.line)
			}
		case theirsChanged:
			if theirsEdit.kind == fuzzLineReplace {
				output = append(output, theirsEdit.line)
			}
		default:
			output = append(output, base[gap])
		}
	}
	return strings.Join(output, ""), false
}

func fuzzHasDivergentLineEdit(mine, theirs fuzzLineEditScript) bool {
	for index, mineEdit := range mine.lineEdits {
		if theirsEdit, overlaps := theirs.lineEdits[index]; overlaps && fuzzLineEditResult(mineEdit) != fuzzLineEditResult(theirsEdit) {
			return true
		}
	}
	return false
}

// fuzzLineMergeHasUniqueAnchors identifies the subset for which the direct
// edit-script oracle is also uniquely recoverable from the three input
// strings: every base line is distinct, no inserted/replacement line can be
// confused with one of those base lines, and an insertion on one side does
// not border a line changed on the other. That last rule matters because an
// insertion gap touches its adjacent base line(s): replacing the only base
// line on mine while theirs inserts before and after it is one overlapping
// hunk, even though the script stores its positions as a line index and two
// gaps. For this subset a conflict is a real failure and the direct oracle's
// content is asserted. Outside it, the hidden edit history can be
// indistinguishable from another history with different combined output (as
// in the documented duplicate-insertion regression), so only universal
// safety and identity invariants are asserted.
func fuzzLineMergeHasUniqueAnchors(base []string, mine, theirs fuzzLineEditScript) bool {
	baseLines := make(map[string]struct{}, len(base))
	for _, line := range base {
		if _, duplicate := baseLines[line]; duplicate {
			return false
		}
		baseLines[line] = struct{}{}
	}
	for _, script := range []fuzzLineEditScript{mine, theirs} {
		for _, insert := range script.inserts {
			if _, matchesBase := baseLines[insert.line]; matchesBase {
				return false
			}
		}
		for _, edit := range script.lineEdits {
			if edit.kind == fuzzLineReplace {
				if _, matchesBase := baseLines[edit.line]; matchesBase {
					return false
				}
			}
		}
	}
	for gap := range mine.inserts {
		if fuzzInsertionBordersLineEdit(gap, len(base), theirs) {
			return false
		}
	}
	for gap := range theirs.inserts {
		if fuzzInsertionBordersLineEdit(gap, len(base), mine) {
			return false
		}
	}
	return true
}

func fuzzInsertionBordersLineEdit(gap, baseLength int, script fuzzLineEditScript) bool {
	if gap > 0 {
		if _, changed := script.lineEdits[gap-1]; changed {
			return true
		}
	}
	if gap < baseLength {
		if _, changed := script.lineEdits[gap]; changed {
			return true
		}
	}
	return false
}

func fuzzLineEditResult(edit fuzzLineEdit) string {
	if edit.kind == fuzzLineDelete {
		return ""
	}
	return edit.line
}

func assertFuzzLineMergeIdentity(t *testing.T, base, changed string) {
	t.Helper()
	result, conflicts, err := linesThreeWayMerge(base, changed, changed)
	if err != nil {
		t.Fatalf("merge(base, changed, changed) returned error: %v; base=%q changed=%q", err, base, changed)
	}
	if len(conflicts) > 0 {
		t.Fatalf("merge(base, changed, changed) returned conflicts %+v; base=%q changed=%q", conflicts, base, changed)
	}
	if result.Content != changed {
		t.Fatalf("merge(base, changed, changed) = %q, want %q; base=%q", result.Content, changed, base)
	}
}
