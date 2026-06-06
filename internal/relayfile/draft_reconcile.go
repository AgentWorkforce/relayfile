package relayfile

// Draft reconciliation (issue #242).
//
// vfs-client's draftFile() documents that agent-authored collection writes
// land at `<resource> <uuid>.json` and that "the worker renames the file to
// the canonical id on receipt". The producer half has always existed; this
// file implements the consumer half service-side, where the filesystem can be
// mutated WITHOUT writeback re-classification: every mutation here bypasses
// recordWriteLocked/enqueueWriteback entirely and emits system-origin events,
// so it can never be observed as an agent mutation (which downstream adapters
// would classify as a writeback and could escalate into provider deletes,
// e.g. Slack chat.delete on a real message).

// WritebackAck is the consumer-reported result of an externally executed
// writeback (POST /v1/workspaces/{ws}/writeback/{id}/ack).
type WritebackAck struct {
	Success bool
	Error   string
	// ExternalID is the provider-assigned id of the created/updated object
	// (e.g. the Slack message ts). When present on a successful ack, the
	// service reconciles the agent-authored draft file.
	ExternalID string
	// CanonicalPath optionally pins the exact canonical projection path. It
	// must stay under the same provider root as the draft; otherwise the
	// service falls back to the ExternalID-derived name.
	CanonicalPath string
}

// DraftDisposition reports what the service did to the draft file at ack time.
type DraftDisposition struct {
	Action string `json:"action"` // "renamed" | "removed" | "none"
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

// SweepDraftsRequest scopes the one-time residue sweep. The sweep only ever
// matches draft name shapes: the built-in draftFile() space-uuid form, plus
// explicitly supplied basename globs for hand-named drafts (e.g. "wb-*.json").
type SweepDraftsRequest struct {
	// PathPrefix optionally restricts the sweep to a subtree.
	PathPrefix string
	// Patterns are path.Match basename globs for hand-named drafts. The
	// built-in space-uuid detection always applies.
	Patterns []string
	// Apply executes the removals; when false the sweep is a dry run that
	// only reports candidates.
	Apply         bool
	CorrelationID string
}

// SweptDraft is one removed (or, in dry runs, removable) draft file.
type SweptDraft struct {
	Path   string `json:"path"`
	Reason string `json:"reason"` // "space-uuid-draft" | "pattern"
}

// SkippedDraft is a matched file the sweep refused to touch.
type SkippedDraft struct {
	Path   string `json:"path"`
	Reason string `json:"reason"` // "pending-writeback" | "provider-linked"
}

// SweepDraftsResult reports the sweep outcome.
type SweepDraftsResult struct {
	DryRun  bool           `json:"dryRun"`
	Scanned int            `json:"scanned"`
	Removed []SweptDraft   `json:"removed"`
	Skipped []SkippedDraft `json:"skipped"`
}

// reconcileAckedDraftLocked applies the draftFile() rename contract after a
// successful externally-executed writeback. Caller must hold s.mu.
func (s *Store) reconcileAckedDraftLocked(workspaceID string, ws *workspaceState, op OperationStatus, ack WritebackAck, correlationID string) DraftDisposition {
	return DraftDisposition{Action: "none"}
}

// SweepWritebackDrafts drains accumulated draft residue (issue #242). All
// removals are classification-exempt.
func (s *Store) SweepWritebackDrafts(workspaceID string, req SweepDraftsRequest) (SweepDraftsResult, error) {
	return SweepDraftsResult{}, nil
}

func basenameOf(path string) string {
	return ""
}

// isDraftFileBasename reports whether a basename matches the draftFile()
// space-uuid form: `<resource> <uuid>.json`.
func isDraftFileBasename(basename string) bool {
	return false
}
