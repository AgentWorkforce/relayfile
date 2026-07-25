package relayfile

import (
	"strings"
	"testing"
)

func mustExtract(t *testing.T, src string) goExtractedFile {
	t.Helper()
	f, err := goExtractUnits([]byte(src))
	if err != nil {
		t.Fatalf("goExtractUnits: %v\nsrc:\n%s", err, src)
	}
	return f
}

const preamble = "package demo\n\nimport \"fmt\"\n\n"

func TestGoExtractUnitsBasic(t *testing.T) {
	src := preamble + `func CreateUser() {
	fmt.Println("create")
}

func DeleteUser() {
	fmt.Println("delete")
}
`
	f := mustExtract(t, src)
	if f.Prologue != preamble {
		t.Fatalf("prologue = %q, want %q", f.Prologue, preamble)
	}
	if len(f.Funcs) != 2 {
		t.Fatalf("expected 2 funcs, got %d: %+v", len(f.Funcs), f.Funcs)
	}
	if f.Funcs[0].Key != "func:CreateUser" || f.Funcs[1].Key != "func:DeleteUser" {
		t.Fatalf("unexpected keys: %+v", f.Funcs)
	}
}

func TestGoExtractUnitsMethodReceiver(t *testing.T) {
	src := preamble + `type UserHandler struct{}

func (h *UserHandler) CreateUser() {}
`
	// Non-func decl (the struct type) appears BEFORE the first func, so
	// it's part of the prologue — this must succeed.
	f := mustExtract(t, src)
	if len(f.Funcs) != 1 {
		t.Fatalf("expected 1 func, got %d", len(f.Funcs))
	}
	if f.Funcs[0].Key != "method:*UserHandler.CreateUser" {
		t.Fatalf("unexpected key: %q", f.Funcs[0].Key)
	}
}

func TestGoExtractUnitsDocCommentTravelsWithFunc(t *testing.T) {
	src := preamble + `// CreateUser creates a user.
func CreateUser() {}

func DeleteUser() {}
`
	f := mustExtract(t, src)
	if !strings.Contains(f.Funcs[0].Text, "// CreateUser creates a user.") {
		t.Fatalf("doc comment did not travel with function: %q", f.Funcs[0].Text)
	}
	if strings.Contains(f.Prologue, "CreateUser creates a user") {
		t.Fatalf("doc comment leaked into prologue: %q", f.Prologue)
	}
}

func TestGoExtractUnitsInterleavedDeclIsIneligible(t *testing.T) {
	src := preamble + `func CreateUser() {}

const Foo = 1

func DeleteUser() {}
`
	if _, err := goExtractUnits([]byte(src)); err == nil {
		t.Fatalf("expected extraction to refuse a non-func decl interleaved after the first func")
	}
}

func TestGoExtractUnitsParseErrorIsRefused(t *testing.T) {
	src := preamble + `func CreateUser( {{{ this is not valid go`
	if _, err := goExtractUnits([]byte(src)); err == nil {
		t.Fatalf("expected extraction to refuse malformed source")
	}
}

func TestGoExtractUnitsInitIsFlagged(t *testing.T) {
	src := preamble + `func init() {}

func CreateUser() {}
`
	f := mustExtract(t, src)
	if !f.Funcs[0].IsInit {
		t.Fatalf("expected init() to be flagged IsInit")
	}
	if f.Funcs[1].IsInit {
		t.Fatalf("expected CreateUser to NOT be flagged IsInit")
	}
}

// --- three-way merge ---

func TestMergeDistinctFunctionsAutoMerge(t *testing.T) {
	base := mustExtract(t, preamble+"func A() { x := 1; _ = x }\n\nfunc B() { y := 1; _ = y }\n")
	mine := mustExtract(t, preamble+"func A() { x := 2; _ = x }\n\nfunc B() { y := 1; _ = y }\n")
	theirs := mustExtract(t, preamble+"func A() { x := 1; _ = x }\n\nfunc B() { y := 2; _ = y }\n")

	result, conflicts := goThreeWayMerge(base, mine, theirs)
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %+v", conflicts)
	}
	if !strings.Contains(result.Content, "x := 2") {
		t.Fatalf("expected my change to A to be present: %s", result.Content)
	}
	if !strings.Contains(result.Content, "y := 2") {
		t.Fatalf("expected their change to B to be present: %s", result.Content)
	}
}

func TestMergeSameFunctionConflictsEvenOnDifferentLines(t *testing.T) {
	base := mustExtract(t, preamble+"func A() {\n\tx := 1\n\ty := 1\n\t_ = x\n\t_ = y\n}\n")
	// Mine changes the FIRST line, theirs changes the SECOND line — no
	// literal line overlap, but it's the same function, so per the
	// function-granularity contract this must still conflict.
	mine := mustExtract(t, preamble+"func A() {\n\tx := 2\n\ty := 1\n\t_ = x\n\t_ = y\n}\n")
	theirs := mustExtract(t, preamble+"func A() {\n\tx := 1\n\ty := 2\n\t_ = x\n\t_ = y\n}\n")

	result, conflicts := goThreeWayMerge(base, mine, theirs)
	if result != nil {
		t.Fatalf("expected nil result on conflict, got %+v", result)
	}
	if len(conflicts) != 1 || conflicts[0].Unit != "A" {
		t.Fatalf("expected exactly one conflict on A, got %+v", conflicts)
	}
}

func TestMergeNewFunctionPlusUnrelatedChangeAutoMerges(t *testing.T) {
	base := mustExtract(t, preamble+"func A() { x := 1; _ = x }\n")
	mine := mustExtract(t, preamble+"func A() { x := 1; _ = x }\n\nfunc NewOne() {}\n")
	theirs := mustExtract(t, preamble+"func A() { x := 2; _ = x }\n")

	result, conflicts := goThreeWayMerge(base, mine, theirs)
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %+v", conflicts)
	}
	if !strings.Contains(result.Content, "func NewOne()") {
		t.Fatalf("expected new function to be present: %s", result.Content)
	}
	if !strings.Contains(result.Content, "x := 2") {
		t.Fatalf("expected their unrelated change to A to be present: %s", result.Content)
	}
}

func TestMergeDeleteVsModifyConflicts(t *testing.T) {
	base := mustExtract(t, preamble+"func A() { x := 1; _ = x }\n\nfunc B() {}\n")
	mine := mustExtract(t, preamble+"func A() { x := 2; _ = x }\n\nfunc B() {}\n") // I modified A
	theirs := mustExtract(t, preamble+"func B() {}\n")                             // they deleted A

	_, conflicts := goThreeWayMerge(base, mine, theirs)
	if len(conflicts) != 1 || conflicts[0].Unit != "A" {
		t.Fatalf("expected exactly one conflict on A (modify-vs-delete), got %+v", conflicts)
	}
}

func TestMergeDeleteVsUnchangedAppliesTheDeletion(t *testing.T) {
	base := mustExtract(t, preamble+"func A() {}\n\nfunc B() {}\n")
	mine := mustExtract(t, preamble+"func B() {}\n")                                 // I deleted A, didn't touch B
	theirs := mustExtract(t, preamble+"func A() {}\n\nfunc B() { x := 1; _ = x }\n") // they touched only B

	result, conflicts := goThreeWayMerge(base, mine, theirs)
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %+v", conflicts)
	}
	if strings.Contains(result.Content, "func A()") {
		t.Fatalf("expected A to remain deleted: %s", result.Content)
	}
	if !strings.Contains(result.Content, "x := 1") {
		t.Fatalf("expected their change to B to survive: %s", result.Content)
	}
}

func TestMergeBothBothDeleteSameUnchangedFunctionAutoMerges(t *testing.T) {
	base := mustExtract(t, preamble+"func A() {}\n\nfunc B() {}\n")
	mine := mustExtract(t, preamble+"func B() {}\n")
	theirs := mustExtract(t, preamble+"func B() {}\n")

	result, conflicts := goThreeWayMerge(base, mine, theirs)
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %+v", conflicts)
	}
	if strings.Contains(result.Content, "func A()") {
		t.Fatalf("expected A to remain deleted: %s", result.Content)
	}
}

func TestMergeInitNeverAutoMerges(t *testing.T) {
	base := mustExtract(t, preamble+"func init() { x := 1; _ = x }\n\nfunc A() {}\n")
	mine := mustExtract(t, preamble+"func init() { x := 2; _ = x }\n\nfunc A() {}\n")
	theirs := mustExtract(t, preamble+"func init() { x := 1; _ = x }\n\nfunc A() { y := 1; _ = y }\n")

	// Only mine touched init(); theirs left it identical to base. Under
	// the general rule this would auto-merge (only one side changed it),
	// but init() must NEVER auto-merge in v1 regardless.
	_, conflicts := goThreeWayMerge(base, mine, theirs)
	found := false
	for _, c := range conflicts {
		if strings.Contains(c.Reason, "init()") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected init() to always be flagged as a conflict, got %+v", conflicts)
	}
}

func TestMergeFileScopeBothChangedConflicts(t *testing.T) {
	base := mustExtract(t, preamble+"func A() {}\n")
	mine := mustExtract(t, "package demo\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc A() {}\n")
	theirs := mustExtract(t, "package demo\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\nfunc A() {}\n")

	_, conflicts := goThreeWayMerge(base, mine, theirs)
	found := false
	for _, c := range conflicts {
		if strings.Contains(c.Unit, "file scope") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a file-scope conflict, got %+v", conflicts)
	}
}

func TestMergeFileScopeOnlyOneSideChangedAutoMerges(t *testing.T) {
	base := mustExtract(t, preamble+"func A() {}\n")
	mine := mustExtract(t, "package demo\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc A() {}\n")
	theirs := mustExtract(t, preamble+"func A() { x := 1; _ = x }\n")

	result, conflicts := goThreeWayMerge(base, mine, theirs)
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %+v", conflicts)
	}
	if !strings.Contains(result.Content, "\"os\"") {
		t.Fatalf("expected my prologue change to survive: %s", result.Content)
	}
	if !strings.Contains(result.Content, "x := 1") {
		t.Fatalf("expected their function change to survive: %s", result.Content)
	}
}

func TestMergeBothAddSameNewFunctionIdenticallyAutoMerges(t *testing.T) {
	base := mustExtract(t, preamble+"func A() {}\n")
	mine := mustExtract(t, preamble+"func A() {}\n\nfunc Shared() { x := 1; _ = x }\n")
	theirs := mustExtract(t, preamble+"func A() {}\n\nfunc Shared() { x := 1; _ = x }\n")

	result, conflicts := goThreeWayMerge(base, mine, theirs)
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts for identical independent additions, got %+v", conflicts)
	}
	if !strings.Contains(result.Content, "func Shared()") {
		t.Fatalf("expected Shared to be present: %s", result.Content)
	}
}

// TestMergeResultsAreAlwaysValidGo is the concrete expression of the
// safety invariant: every successful (non-conflicting) merge in this file
// must itself parse cleanly. A merge that produced syntactically broken Go
// would be exactly the kind of silent corruption the whole design exists
// to prevent.
func TestMergeResultsAreAlwaysValidGo(t *testing.T) {
	cases := []struct {
		name               string
		base, mine, theirs string
	}{
		{
			"distinct functions",
			preamble + "func A() { x := 1; _ = x }\n\nfunc B() { y := 1; _ = y }\n",
			preamble + "func A() { x := 2; _ = x }\n\nfunc B() { y := 1; _ = y }\n",
			preamble + "func A() { x := 1; _ = x }\n\nfunc B() { y := 2; _ = y }\n",
		},
		{
			"new function plus unrelated change",
			preamble + "func A() { x := 1; _ = x }\n",
			preamble + "func A() { x := 1; _ = x }\n\nfunc NewOne() {}\n",
			preamble + "func A() { x := 2; _ = x }\n",
		},
		{
			"delete vs unchanged",
			preamble + "func A() {}\n\nfunc B() {}\n",
			preamble + "func B() {}\n",
			preamble + "func A() {}\n\nfunc B() { x := 1; _ = x }\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, conflicts := goThreeWayMerge(mustExtract(t, tc.base), mustExtract(t, tc.mine), mustExtract(t, tc.theirs))
			if len(conflicts) != 0 {
				t.Fatalf("expected no conflicts, got %+v", conflicts)
			}
			if _, err := goExtractUnits([]byte(result.Content)); err != nil {
				t.Fatalf("merged output does not parse as valid Go: %v\n---\n%s", err, result.Content)
			}
		})
	}
}

func TestMergeBothAddDifferentSameNamedFunctionConflicts(t *testing.T) {
	base := mustExtract(t, preamble+"func A() {}\n")
	mine := mustExtract(t, preamble+"func A() {}\n\nfunc New() { x := 1; _ = x }\n")
	theirs := mustExtract(t, preamble+"func A() {}\n\nfunc New() { x := 2; _ = x }\n")

	_, conflicts := goThreeWayMerge(base, mine, theirs)
	if len(conflicts) != 1 || conflicts[0].Unit != "New" {
		t.Fatalf("expected a conflict on New, got %+v", conflicts)
	}
}
