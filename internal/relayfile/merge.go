package relayfile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// MergeStrategyGoTopLevelFunctions is the only supported merge strategy in
// v1. See merge_go.go for the actual algorithm and
// docs/multi-agent-collaboration-assessment.md (Phase 3) for the design.
const MergeStrategyGoTopLevelFunctions = "go-top-level-functions-v1"

// maxMergeContentBytes bounds request size for the merge path — parsing
// three copies of a file plus diffing is more expensive than a plain
// write, and this is explicitly NOT meant to replace ordinary writes for
// large generated files.
const maxMergeContentBytes = 512 * 1024

// maxMergeCommitAttempts bounds how many times MergeFile will recompute
// the merge if the live revision moves between snapshot and commit. This
// is a livelock guard, not a correctness mechanism — correctness comes
// from always re-verifying against the live revision actually committed
// against, never the one used to start the computation.
const maxMergeCommitAttempts = 5

type MergeRequest struct {
	WorkspaceID     string
	Path            string
	Strategy        string
	Content         string // "mine" — the caller's proposed new content
	BaseRevision    string // the revision the caller's BaseContent came from
	BaseContent     string // the caller's own last-synced content
	ContentType     string
	CorrelationID   string
	ContentIdentity *ContentIdentity
}

type MergeConflictDetail struct {
	Unit   string `json:"unit"`
	Reason string `json:"reason"`
	Base   string `json:"base,omitempty"`
	Mine   string `json:"mine,omitempty"`
	Theirs string `json:"theirs,omitempty"`
}

type MergeResponse struct {
	TargetRevision        string `json:"targetRevision"`
	Strategy              string `json:"strategy"`
	BaseRevision          string `json:"baseRevision"`
	MergedAgainstRevision string `json:"mergedAgainstRevision"`
}

// MergeConflictError is returned when the merge could not be auto-applied
// — at least one unit was concurrently changed by both sides. Nothing was
// written; the live file is exactly as it was before this call.
type MergeConflictError struct {
	CurrentRevision string
	Conflicts       []MergeConflictDetail
}

func (e *MergeConflictError) Error() string {
	return fmt.Sprintf("merge conflict: %d unit(s) concurrently changed", len(e.Conflicts))
}

// ErrMergeBaseUnavailable is returned when the server can't verify the
// caller's claimed base — either it never recorded a hash for that
// revision (evicted, pre-Phase-3, or a restart since — the provenance
// ledger is in-memory only) or the caller's supplied content doesn't hash
// to what was actually recorded. Both cases are treated identically and
// fail closed: the caller must fall back to a plain conditional write
// (which will itself correctly conflict, since MergeFile is only called
// when a plain write was already stale).
var ErrMergeBaseUnavailable = fmt.Errorf("merge base unavailable")

// ErrMergeIneligible is returned when the strategy can't be applied to
// this content at all (wrong strategy name, not valid Go, file layout too
// complex for v1's extractor — see goExtractUnits). Fail closed, same as
// ErrMergeBaseUnavailable: caller falls back to a plain write.
type ErrMergeIneligible struct {
	Reason string
}

func (e *ErrMergeIneligible) Error() string {
	return "merge ineligible: " + e.Reason
}

func hashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// MergeFile implements the go-top-level-functions-v1 merge strategy
// described in docs/multi-agent-collaboration-assessment.md (Phase 3).
//
// Safety invariant, non-negotiable: this either writes exactly one fully
// validated merged file (revision bumped, event emitted, exactly like a
// normal write), or it writes nothing at all. Every failure path —
// provenance miss, parse failure, extraction ineligibility, unresolved
// conflicts, a commit-time race — returns an error or MergeConflictError
// and leaves the live file byte-for-byte unchanged.
func (s *Store) MergeFile(req MergeRequest) (MergeResponse, error) {
	workspaceID := strings.TrimSpace(req.WorkspaceID)
	path := normalizePath(req.Path)
	if workspaceID == "" || path == "" || strings.TrimSpace(req.BaseRevision) == "" {
		return MergeResponse{}, ErrInvalidInput
	}
	if req.Strategy != MergeStrategyGoTopLevelFunctions {
		return MergeResponse{}, &ErrMergeIneligible{Reason: fmt.Sprintf("unsupported strategy %q", req.Strategy)}
	}
	if len(req.Content) > maxMergeContentBytes || len(req.BaseContent) > maxMergeContentBytes {
		return MergeResponse{}, &ErrMergeIneligible{Reason: "content exceeds merge size limit"}
	}
	if !strings.HasSuffix(path, ".go") {
		return MergeResponse{}, &ErrMergeIneligible{Reason: "only .go files are eligible for go-top-level-functions-v1"}
	}

	// Extraction and merge computation are pure functions of (base, mine,
	// theirs) and don't need the store lock at all — only reading "theirs"
	// (the live content) and committing the result do. Parsing three files
	// and diffing them while holding the store's single global RWMutex
	// would block every other workspace's unrelated writes for no reason.
	baseExtracted, err := goExtractUnits([]byte(req.BaseContent))
	if err != nil {
		return MergeResponse{}, &ErrMergeIneligible{Reason: "base content: " + err.Error()}
	}
	mineExtracted, err := goExtractUnits([]byte(req.Content))
	if err != nil {
		return MergeResponse{}, &ErrMergeIneligible{Reason: "proposed content: " + err.Error()}
	}

	var lastCurrentRevision string
	for attempt := 0; attempt < maxMergeCommitAttempts; attempt++ {
		s.mu.RLock()
		if existingOp, ok := s.findExistingMergeOpLocked(workspaceID, req.ContentIdentity); ok {
			s.mu.RUnlock()
			return existingOp, nil
		}
		ws := s.workspaces[workspaceID]
		var liveContent string
		var liveExists bool
		var currentRevision string
		if ws != nil {
			if f, exists := ws.Files[path]; exists {
				liveContent = f.Content
				liveExists = true
			}
			currentRevision = ws.Revision
		}
		baseEntry, baseKnown := s.lookupRevisionProvenanceLocked(workspaceID, path, req.BaseRevision)
		s.mu.RUnlock()

		if !baseKnown {
			return MergeResponse{}, ErrMergeBaseUnavailable
		}
		if baseEntry.Deleted {
			// The base revision was a deletion — nothing sensible to
			// three-way-merge against. Fall back.
			return MergeResponse{}, ErrMergeBaseUnavailable
		}
		if hashContent(req.BaseContent) != baseEntry.Hash {
			// The caller's claimed base doesn't match what was actually
			// recorded at that revision — refuse to trust it.
			return MergeResponse{}, ErrMergeBaseUnavailable
		}
		if !liveExists {
			return MergeResponse{}, &ErrMergeIneligible{Reason: "live file no longer exists"}
		}

		theirsExtracted, err := goExtractUnits([]byte(liveContent))
		if err != nil {
			return MergeResponse{}, &ErrMergeIneligible{Reason: "live content: " + err.Error()}
		}

		result, conflicts := goThreeWayMerge(baseExtracted, mineExtracted, theirsExtracted)
		if len(conflicts) > 0 {
			details := make([]MergeConflictDetail, 0, len(conflicts))
			for _, c := range conflicts {
				details = append(details, MergeConflictDetail{
					Unit: c.Unit, Reason: c.Reason, Base: c.Base, Mine: c.Mine, Theirs: c.Theirs,
				})
			}
			return MergeResponse{}, &MergeConflictError{CurrentRevision: currentRevision, Conflicts: details}
		}

		// Final self-check before ever committing: the merge result must
		// itself be valid, parseable Go. This is the same invariant
		// TestMergeResultsAreAlwaysValidGo enforces in isolation — belt
		// and suspenders, since a bug in goThreeWayMerge must never reach
		// disk even if the unit tests somehow missed it.
		if _, err := goExtractUnits([]byte(result.Content)); err != nil {
			return MergeResponse{}, fmt.Errorf("internal error: merge produced invalid Go, refusing to write: %w", err)
		}

		lastCurrentRevision = currentRevision
		resp, committed, err := s.commitMergeResultLocked(workspaceID, path, req, result.Content, currentRevision)
		if err != nil {
			return MergeResponse{}, err
		}
		if committed {
			return resp, nil
		}
		// currentRevision moved between snapshot and commit — recompute
		// against the new live state and retry, bounded by
		// maxMergeCommitAttempts.
	}
	return MergeResponse{}, fmt.Errorf("merge did not converge after %d attempts (currentRevision kept moving, last seen %q)", maxMergeCommitAttempts, lastCurrentRevision)
}

// findExistingMergeOpLocked mirrors BulkWrite's content-identity dedupe
// (findOperationByContentIdentityLocked) so a retried merge request (e.g.
// after a dropped response) doesn't attempt a second merge against
// whatever the live content has become by the time the retry arrives.
func (s *Store) findExistingMergeOpLocked(workspaceID string, identity *ContentIdentity) (MergeResponse, bool) {
	if identity == nil {
		return MergeResponse{}, false
	}
	ws := s.workspaces[workspaceID]
	if ws == nil {
		return MergeResponse{}, false
	}
	now := time.Now().UTC()
	op, ok := findOperationByContentIdentityLocked(ws, identity, now)
	if !ok {
		return MergeResponse{}, false
	}
	return MergeResponse{
		TargetRevision: op.Revision,
		Strategy:       MergeStrategyGoTopLevelFunctions,
	}, true
}

// commitMergeResultLocked takes the store's write lock, re-verifies the
// live revision is still what the merge was computed against, and commits
// if so. Returns committed=false (not an error) if the revision moved —
// the caller recomputes against fresh state and retries.
func (s *Store) commitMergeResultLocked(workspaceID, path string, req MergeRequest, mergedContent, expectedRevision string) (MergeResponse, bool, error) {
	s.mu.Lock()
	ws := s.ensureWorkspaceLocked(workspaceID)
	if ws.Revision != expectedRevision {
		s.mu.Unlock()
		return MergeResponse{}, false, nil
	}

	contentType := strings.TrimSpace(req.ContentType)
	if contentType == "" {
		contentType = "text/plain"
	}
	revision := s.nextRevisionLocked()
	existing, existed := ws.Files[path]
	file := existing
	file.Path = path
	file.Revision = revision
	file.Content = mergedContent
	file.Encoding = ""
	file.ContentHash = contentHashForEncodedContent(mergedContent, "")
	file.ContentType = contentType
	if file.Provider == "" {
		file.Provider = inferProviderFromPath(path)
	}
	file.LastEditedAt = time.Now().UTC().Format(time.RFC3339Nano)
	ws.Files[path] = file
	ws.Revision = revision

	eventType := "file.updated"
	if !existed {
		eventType = "file.created"
	}
	result, task := s.recordWriteWithContentIdentityLocked(ws, path, revision, eventType, file.Provider, req.CorrelationID, req.ContentIdentity)
	_ = s.saveLocked()
	s.mu.Unlock()

	s.enqueueWriteback(task)

	return MergeResponse{
		TargetRevision:        result.TargetRevision,
		Strategy:              MergeStrategyGoTopLevelFunctions,
		BaseRevision:          req.BaseRevision,
		MergedAgainstRevision: expectedRevision,
	}, true, nil
}
