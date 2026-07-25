package relayfile

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// This file implements the "go-top-level-functions-v1" merge strategy: a
// three-way (base/mine/theirs) merge of Go source, scoped to top-level
// function declarations. See docs/multi-agent-collaboration-assessment.md
// Phase 3 for the full design and its safety invariant:
//
//	A merge request either produces one fully validated merged file, or it
//	writes nothing. No parse, classification, or ambiguity may ever produce
//	a partially-merged or silently-wrong result.
//
// Scope, deliberately restrictive for v1 (see goExtractUnits): only files
// where every top-level declaration after the first function is ALSO a
// function are eligible ("prologue" of package/imports/types/vars, then
// functions only). Anything else is ineligible — callers must fall back to
// a plain whole-file conflict, never guess.

// goFuncUnit is one top-level function declaration, matched across
// base/mine/theirs by Key (not by byte position, which shifts across
// versions as other functions change).
type goFuncUnit struct {
	Key    string // "func:Name" or "method:RecvType.Name"
	Name   string // human-readable, for diagnostics only
	Text   string // exact source text, including any attached doc comment
	IsInit bool   // true for func init() — never eligible for auto-merge
}

// goExtractedFile is the result of successfully extracting one source
// file's structure. Only produced when the file is eligible (see above).
type goExtractedFile struct {
	Prologue string       // bytes [0, firstFuncStart) — package/imports/types/vars
	Funcs    []goFuncUnit // in source order
}

func (f goExtractedFile) funcByKey(key string) (goFuncUnit, bool) {
	for _, u := range f.Funcs {
		if u.Key == key {
			return u, true
		}
	}
	return goFuncUnit{}, false
}

// goExtractUnits parses src as a Go source file and extracts its structure,
// or returns an error explaining why it's ineligible. It never panics and
// never guesses — any ambiguity is a returned error, which callers must
// treat as "merge not possible for this file," falling back to the
// existing plain-conflict behavior.
func goExtractUnits(src []byte) (goExtractedFile, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return goExtractedFile{}, fmt.Errorf("go parse error: %w", err)
	}
	tf := fset.File(file.Pos())
	if tf == nil {
		return goExtractedFile{}, fmt.Errorf("go parse error: no token file")
	}
	offset := func(pos token.Pos) int { return tf.Offset(pos) }

	if len(file.Decls) == 0 {
		// A file with no declarations at all (unusual, but valid Go — just
		// package clause and maybe imports). Treat the whole thing as
		// prologue; no functions to merge, so nothing to gain from this
		// strategy, but it's not unsafe — just report zero funcs.
		return goExtractedFile{Prologue: string(src), Funcs: nil}, nil
	}

	firstFuncIdx := -1
	for i, decl := range file.Decls {
		if _, ok := decl.(*ast.FuncDecl); ok {
			firstFuncIdx = i
			break
		}
	}
	if firstFuncIdx == -1 {
		// No functions at all — everything is prologue.
		return goExtractedFile{Prologue: string(src), Funcs: nil}, nil
	}

	// Restrictive v1 rule: every declaration from the first function
	// onward must ALSO be a function. A non-func declaration interleaved
	// after the first function (e.g. a const block between two funcs) is
	// valid Go but structurally more than this v1 extractor handles safely
	// — refuse rather than guess.
	for i := firstFuncIdx; i < len(file.Decls); i++ {
		if _, ok := file.Decls[i].(*ast.FuncDecl); !ok {
			return goExtractedFile{}, fmt.Errorf(
				"non-function top-level declaration after the first function (at decl %d) — file layout too complex for go-top-level-functions-v1", i)
		}
	}

	firstFuncStart := offset(file.Decls[firstFuncIdx].Pos())
	// Extend the prologue boundary backward to include any doc comment
	// directly attached to the first function, so it travels WITH that
	// function's unit instead of being silently dropped into the prologue.
	if fn, ok := file.Decls[firstFuncIdx].(*ast.FuncDecl); ok && fn.Doc != nil {
		docStart := offset(fn.Doc.Pos())
		if docStart < firstFuncStart {
			firstFuncStart = docStart
		}
	}
	prologue := string(src[:firstFuncStart])

	// Units are gapless and contiguous, together spanning exactly
	// [firstFuncStart, len(src)) with no bytes unaccounted for: each unit
	// starts exactly where the previous one (or the prologue, for the
	// first) ended, and the LAST unit is extended to EOF. This is what
	// makes extraction lossless — a standalone comment between two
	// functions, or trailing content after the last one, naturally
	// belongs to a unit instead of falling into an uncaptured gap that
	// goThreeWayMerge's reconstruction would otherwise silently drop even
	// though the result still parses as valid Go.
	units := make([]goFuncUnit, 0, len(file.Decls)-firstFuncIdx)
	seen := map[string]int{}
	prevEnd := firstFuncStart
	for i := firstFuncIdx; i < len(file.Decls); i++ {
		fn := file.Decls[i].(*ast.FuncDecl)
		start := prevEnd
		end := offset(fn.End())
		if i == len(file.Decls)-1 {
			end = len(src)
		}
		if start < 0 || end > len(src) || start > end {
			return goExtractedFile{}, fmt.Errorf("invalid byte range for function %q", funcDisplayName(fn))
		}
		name := funcDisplayName(fn)
		key := funcKey(fn)
		seen[key]++
		if seen[key] > 1 {
			// Two functions resolving to the same key (e.g. genuinely
			// duplicate declarations, which wouldn't compile, or a
			// receiver-type formatting collision) — refuse rather than
			// risk misattributing a change to the wrong one.
			return goExtractedFile{}, fmt.Errorf("duplicate function key %q — cannot uniquely identify functions in this file", key)
		}
		units = append(units, goFuncUnit{
			Key:    key,
			Name:   name,
			Text:   string(src[start:end]),
			IsInit: fn.Name != nil && fn.Name.Name == "init" && fn.Recv == nil,
		})
		prevEnd = offset(fn.End())
	}

	return goExtractedFile{Prologue: prologue, Funcs: units}, nil
}

func funcDisplayName(fn *ast.FuncDecl) string {
	if fn.Name == nil {
		return "<anonymous>"
	}
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	recvType := recvTypeString(fn.Recv.List[0].Type)
	return fmt.Sprintf("(%s).%s", recvType, fn.Name.Name)
}

func funcKey(fn *ast.FuncDecl) string {
	name := "<anonymous>"
	if fn.Name != nil {
		name = fn.Name.Name
	}
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return "func:" + name
	}
	return "method:" + recvTypeString(fn.Recv.List[0].Type) + "." + name
}

func recvTypeString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return "*" + recvTypeString(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		// Generic receiver with one type parameter, e.g. Foo[T] — use the
		// base name only; type parameters aren't part of the identity we
		// need for matching across versions.
		return recvTypeString(t.X)
	case *ast.IndexListExpr:
		// Generic receiver with multiple type parameters, e.g. Foo[T, U].
		return recvTypeString(t.X)
	default:
		return "?"
	}
}

// goMergeConflict describes one unit that could not be auto-merged because
// both "mine" and "theirs" changed it relative to "base".
type goMergeConflict struct {
	Unit   string // function name, or "file scope (package/imports/types/vars)"
	Reason string
	Base   string
	Mine   string
	Theirs string
}

// goMergeResult is returned only when the merge succeeded with zero
// conflicts. If any conflicts exist, callers get []goMergeConflict instead
// and must not use a goMergeResult — see goThreeWayMerge.
type goMergeResult struct {
	Content string
}

// goThreeWayMerge performs the actual base/mine/theirs merge described in
// the Phase 3 design. It never partially applies: it returns exactly one
// of (result, nil) or (nil, conflicts) — the caller commits the former or
// surfaces the latter as a 409, and never writes anything in the conflict
// case.
// textsDiffer compares two function units' Text for change-detection
// purposes, ignoring purely leading/trailing WHITESPACE (not comments —
// actual comment content still counts as a real difference). A function's
// captured span (goExtractUnits) includes the gap since the previous
// declaration (or none, if it's first) and, only for the last declaration
// in the file, everything through EOF. That means the exact same function
// body can gain or lose leading/trailing whitespace purely because it
// stopped or started being the first/last declaration in one version but
// not another (e.g. base has only function B; mine adds a function before
// it, or theirs adds one after) — with no actual edit to B at all.
// Comparing raw Text would treat that as "changed" and cause a false
// conflict. Reconstruction still uses the raw, untrimmed Text (see
// goThreeWayMerge's assembly step) — only the comparison ignores it.
func textsDiffer(a, b string) bool {
	return strings.TrimSpace(a) != strings.TrimSpace(b)
}

func goThreeWayMerge(base, mine, theirs goExtractedFile) (*goMergeResult, []goMergeConflict) {
	var conflicts []goMergeConflict

	// --- prologue (file scope) ---
	prologueChangedByMe := mine.Prologue != base.Prologue
	prologueChangedByOthers := theirs.Prologue != base.Prologue
	resolvedPrologue := base.Prologue
	switch {
	case prologueChangedByMe && prologueChangedByOthers && mine.Prologue != theirs.Prologue:
		conflicts = append(conflicts, goMergeConflict{
			Unit:   "file scope (package/imports/types/vars)",
			Reason: "both sides changed file-scope content",
			Base:   base.Prologue, Mine: mine.Prologue, Theirs: theirs.Prologue,
		})
	case prologueChangedByMe:
		resolvedPrologue = mine.Prologue
	case prologueChangedByOthers:
		resolvedPrologue = theirs.Prologue
	}

	// --- functions ---
	// Walk theirs's functions first (this defines the live ordering the
	// output preserves), resolving each against base+mine. Then append any
	// mine-only new functions. This mirrors goThreeWayMerge's design doc:
	// reconstruction starts from "theirs" as the structural skeleton.
	resolvedByKey := map[string]string{} // key -> resolved text
	order := make([]string, 0, len(theirs.Funcs)+len(mine.Funcs))

	for _, t := range theirs.Funcs {
		order = append(order, t.Key)
		b, inBase := base.funcByKey(t.Key)
		m, inMine := mine.funcByKey(t.Key)

		if !inMine {
			// I deleted a function that still exists live.
			if !inBase {
				// Existed in neither base nor mine, but exists in theirs:
				// theirs added it after base — nothing of mine's to
				// conflict with, keep theirs's version.
				resolvedByKey[t.Key] = t.Text
				continue
			}
			changedByOthers := textsDiffer(t.Text, b.Text)
			if changedByOthers {
				conflicts = append(conflicts, goMergeConflict{
					Unit: t.Name, Reason: "deleted by one side, modified by the other",
					Base: b.Text, Mine: "", Theirs: t.Text,
				})
				continue
			}
			// I deleted it, theirs left it unchanged from base — respect
			// my deletion.
			resolvedByKey[t.Key] = ""
			continue
		}

		if !inBase {
			// Both mine and theirs have it but base doesn't: both sides
			// independently added a function with the same key.
			if !textsDiffer(m.Text, t.Text) {
				resolvedByKey[t.Key] = t.Text
			} else if t.IsInit || m.IsInit {
				conflicts = append(conflicts, goMergeConflict{
					Unit: t.Name, Reason: "init() is never auto-merged",
					Base: "", Mine: m.Text, Theirs: t.Text,
				})
			} else {
				conflicts = append(conflicts, goMergeConflict{
					Unit: t.Name, Reason: "both sides independently added a function with this name",
					Base: "", Mine: m.Text, Theirs: t.Text,
				})
			}
			continue
		}

		changedByMe := textsDiffer(m.Text, b.Text)
		changedByOthers := textsDiffer(t.Text, b.Text)
		switch {
		case t.IsInit && (changedByMe || changedByOthers) && textsDiffer(m.Text, t.Text):
			conflicts = append(conflicts, goMergeConflict{
				Unit: t.Name, Reason: "init() is never auto-merged",
				Base: b.Text, Mine: m.Text, Theirs: t.Text,
			})
		case changedByMe && changedByOthers && textsDiffer(m.Text, t.Text):
			conflicts = append(conflicts, goMergeConflict{
				Unit: t.Name, Reason: "concurrently changed",
				Base: b.Text, Mine: m.Text, Theirs: t.Text,
			})
		case changedByMe:
			resolvedByKey[t.Key] = m.Text
		default:
			resolvedByKey[t.Key] = t.Text
		}
	}

	// Mine-only new functions: in mine, not in theirs. If also not in
	// base, it's a genuine new addition — append it. If it WAS in base but
	// not in theirs, that path was already handled above (theirs deleted
	// it) via the !inMine branch from theirs's perspective... but that
	// only fires for keys theirs HAS. A key present in base+mine but
	// absent from theirs needs handling here too (theirs deleted a
	// function I didn't touch, or did touch).
	newAppendKeys := map[string]bool{}
	for _, m := range mine.Funcs {
		if _, inTheirs := theirs.funcByKey(m.Key); inTheirs {
			continue // already resolved above
		}
		b, inBase := base.funcByKey(m.Key)
		if !inBase {
			// Purely new function only I have. Appended after everything
			// from theirs, in mine's relative order — trimmed and given
			// explicit separation below rather than using the raw
			// gap-inclusive Text, since that span may carry leading/
			// trailing content specific to MINE's own file layout (e.g.
			// mine's own EOF, if this happened to be mine's last
			// declaration) that has no meaningful position in theirs's
			// reconstructed structure.
			order = append(order, m.Key)
			newAppendKeys[m.Key] = true
			resolvedByKey[m.Key] = strings.TrimSpace(m.Text)
			continue
		}
		// In base and mine, not in theirs: theirs deleted it.
		if !textsDiffer(m.Text, b.Text) {
			// I didn't touch it either — respect the deletion.
			continue
		}
		conflicts = append(conflicts, goMergeConflict{
			Unit: m.Name, Reason: "modified by one side, deleted by the other",
			Base: b.Text, Mine: m.Text, Theirs: "",
		})
	}

	if len(conflicts) > 0 {
		return nil, conflicts
	}

	var sb strings.Builder
	sb.WriteString(resolvedPrologue)
	wroteAny := false
	for _, key := range order {
		text, ok := resolvedByKey[key]
		if !ok || text == "" {
			continue // deleted
		}
		if newAppendKeys[key] {
			// A brand-new function with no natural position in theirs's
			// structure: give it explicit, clean separation rather than
			// relying on any gap content, since (being trimmed above) it
			// no longer carries its own.
			if wroteAny {
				sb.WriteString("\n\n")
			}
			sb.WriteString(text)
		} else {
			// Units sourced from theirs's own order (whether keeping
			// theirs's text or substituting mine's changed version)
			// already carry their own correct leading gap — from
			// goExtractUnits's gapless extraction — so no separator is
			// added here. Adding one would double up the blank line
			// already present in that gap.
			sb.WriteString(text)
		}
		wroteAny = true
	}
	if wroteAny && !strings.HasSuffix(sb.String(), "\n") {
		sb.WriteString("\n")
	}
	return &goMergeResult{Content: sb.String()}, nil
}
