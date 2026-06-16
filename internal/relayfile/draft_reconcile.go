package relayfile

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

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
	// ProviderResult carries additional fields the provider echoed back about
	// the written record (e.g. a Slack message ts/channel). They are surfaced
	// verbatim on the operation's providerResult. The reserved server-owned
	// key providerRevision cannot be overridden via this map.
	ProviderResult map[string]any
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

// draftFile() produces `<prefix> <uuid v4>.json`; the prefix is the resource
// name, so the basename always contains a space before the uuid.
var draftFileBasenamePattern = regexp.MustCompile(
	`^.+ [0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\.json$`,
)

var externalIDFilenameReplacer = strings.NewReplacer("/", "_", "\\", "_", " ", "_")

func basenameOf(filePath string) string {
	if idx := strings.LastIndex(filePath, "/"); idx >= 0 {
		return filePath[idx+1:]
	}
	return filePath
}

func parentDirOf(filePath string) string {
	if idx := strings.LastIndex(filePath, "/"); idx > 0 {
		return filePath[:idx]
	}
	return "/"
}

// isDraftFileBasename reports whether a basename matches the draftFile()
// space-uuid form: `<resource> <uuid>.json`.
func isDraftFileBasename(basename string) bool {
	return draftFileBasenamePattern.MatchString(basename)
}

// IsDraftFilePath reports whether a path matches the relayfile-owned
// draftFile() create-draft basename contract.
func IsDraftFilePath(filePath string) bool {
	return isDraftFileBasename(basenameOf(filePath))
}

// reconcileAckedDraftLocked applies the draftFile() rename contract after a
// successful externally-executed writeback: the agent-authored draft is
// renamed to the canonical id (or removed when the canonical record already
// materialized). Caller must hold s.mu.
//
// Classification exemption is structural: this function mutates ws.Files and
// emits system-origin events directly. It never calls recordWriteLocked or
// enqueueWriteback, so the mutation cannot create operations or writebacks.
func (s *Store) reconcileAckedDraftLocked(workspaceID string, ws *workspaceState, op OperationStatus, ack WritebackAck, correlationID string) DraftDisposition {
	none := DraftDisposition{Action: "none"}
	if ws.ProviderIndex == nil {
		ws.ProviderIndex = map[string]string{}
	}
	externalID := strings.TrimSpace(ack.ExternalID)
	if externalID == "" {
		return none
	}
	// Deletes have no draft to reconcile.
	if op.Action == string(WritebackActionFileDelete) {
		return none
	}
	draftPath := normalizePath(op.Path)
	if draftPath == "/" {
		return none
	}
	file, exists := ws.Files[draftPath]
	if !exists {
		return none
	}

	provider := strings.TrimSpace(file.Provider)
	if provider == "" {
		provider = strings.TrimSpace(op.Provider)
	}
	if provider == "" {
		provider = inferProviderFromPath(draftPath)
	}
	if provider == "" {
		return none
	}
	key := providerObjectKey(provider, externalID)

	// Already linked to this provider object (e.g. an agent edit of a synced
	// canonical record): the path is already canonical, just keep the index
	// fresh.
	if file.ProviderObjectID == externalID {
		if _, indexed := ws.ProviderIndex[key]; !indexed {
			ws.ProviderIndex[key] = draftPath
		}
		return none
	}
	// Linked to a DIFFERENT provider object: not a draft — leave it alone.
	if file.ProviderObjectID != "" {
		return none
	}

	// The provider webhook may already have materialized the canonical
	// record (index lookup first, then the externalId-derived projection).
	// The draft is then redundant: remove it. This removal shares the
	// classification-exempt path below — no adapter-actionable delete can
	// escape it.
	if indexedPath, ok := ws.ProviderIndex[key]; ok && indexedPath != draftPath {
		if _, live := ws.Files[indexedPath]; live {
			s.removeDraftLocked(workspaceID, ws, draftPath, provider, correlationID)
			return DraftDisposition{Action: "removed", From: draftPath}
		}
	}

	targetPath := s.canonicalDraftTargetLocked(draftPath, provider, externalID, ack.CanonicalPath)
	if targetPath == "" || targetPath == draftPath {
		return none
	}
	if _, occupied := ws.Files[targetPath]; occupied {
		// Canonical record already lives at the target: drop the draft.
		s.removeDraftLocked(workspaceID, ws, draftPath, provider, correlationID)
		return DraftDisposition{Action: "removed", From: draftPath}
	}

	// Rename: same content, canonical id, linked to the provider object so a
	// later provider sync upsert for the same object converges onto one file
	// (applyProviderUpsertLocked treats the object id as authoritative and
	// relocates the previous path).
	//
	// Revision convention: the file.deleted/file.created pair below shares
	// ONE revision — the rename is a single atomic state transition of one
	// logical file, and the two events are two views of that one move. This
	// deliberately differs from applyProviderUpsertLocked's relocate, which
	// uses two revisions because it genuinely is two transitions (remove the
	// stale projection, then upsert content that may differ). Event
	// consumers are eventId-keyed, so neither convention affects cursors.
	nowTS := nowRFC3339NanoUTC()
	revision := s.nextRevisionLocked()
	delete(ws.Files, draftPath)
	file.Path = targetPath
	file.Revision = revision
	file.Provider = provider
	file.ProviderObjectID = externalID
	file.LastEditedAt = nowTS
	ws.Files[targetPath] = file
	ws.Revision = revision
	ws.ProviderIndex[key] = targetPath

	s.appendWorkspaceEventLocked(workspaceID, ws, Event{
		EventID:       s.nextEventIDLocked(),
		Type:          "file.deleted",
		Path:          draftPath,
		Revision:      revision,
		Origin:        "system",
		Provider:      provider,
		CorrelationID: correlationID,
		Timestamp:     nowTS,
	})
	s.appendWorkspaceEventLocked(workspaceID, ws, Event{
		EventID:       s.nextEventIDLocked(),
		Type:          "file.created",
		Path:          targetPath,
		Revision:      revision,
		ContentHash:   storedContentHashForFile(file),
		Origin:        "system",
		Provider:      provider,
		CorrelationID: correlationID,
		Timestamp:     nowTS,
	})

	return DraftDisposition{Action: "renamed", From: draftPath, To: targetPath}
}

// canonicalDraftTargetLocked picks the rename target: the consumer-supplied
// canonical path when it stays under the draft's provider root, otherwise the
// externalId-derived name next to the draft.
func (s *Store) canonicalDraftTargetLocked(draftPath, provider, externalID, canonicalOverride string) string {
	if override := strings.TrimSpace(canonicalOverride); override != "" {
		normalized := normalizePath(override)
		if normalized != "/" && inferProviderFromPath(normalized) == provider {
			return normalized
		}
	}
	safeID := externalIDFilenameReplacer.Replace(externalID)
	safeID = strings.Trim(safeID, ".")
	if safeID == "" {
		return ""
	}
	extension := path.Ext(basenameOf(draftPath))
	if extension == "" {
		extension = ".json"
	}
	return joinPath(parentDirOf(draftPath), safeID+extension)
}

// removeDraftLocked drops a draft file with a system-origin delete event.
// Caller must hold s.mu. Never creates operations or writebacks.
func (s *Store) removeDraftLocked(workspaceID string, ws *workspaceState, draftPath, provider, correlationID string) {
	if _, exists := ws.Files[draftPath]; !exists {
		return
	}
	delete(ws.Files, draftPath)
	revision := s.nextRevisionLocked()
	ws.Revision = revision
	s.appendWorkspaceEventLocked(workspaceID, ws, Event{
		EventID:       s.nextEventIDLocked(),
		Type:          "file.deleted",
		Path:          draftPath,
		Revision:      revision,
		Origin:        "system",
		Provider:      provider,
		CorrelationID: correlationID,
		Timestamp:     nowRFC3339NanoUTC(),
	})
}

// SweepWritebackDrafts drains accumulated draft residue (issue #242): drafts
// that were delivered before the rename-at-ack contract existed (or whose
// operations were lost to restarts) and therefore sit forever at canonical
// provider roots.
//
// Matching is strictly name-shape based: the built-in draftFile() space-uuid
// form, plus caller-supplied basename globs for hand-named drafts. Files
// linked to a provider object are never touched, and drafts with a pending or
// running writeback are never touched (they have not been delivered yet).
// Removals are classification-exempt: system-origin events only, no
// operations, no writeback enqueue.
func (s *Store) SweepWritebackDrafts(workspaceID string, req SweepDraftsRequest) (SweepDraftsResult, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return SweepDraftsResult{}, ErrInvalidInput
	}
	for _, pattern := range req.Patterns {
		if _, err := path.Match(pattern, "probe.json"); err != nil {
			return SweepDraftsResult{}, ErrInvalidInput
		}
		if strings.ContainsAny(pattern, `/\`) {
			return SweepDraftsResult{}, ErrInvalidInput
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	ws, ok := s.workspaces[workspaceID]
	if !ok {
		return SweepDraftsResult{}, ErrNotFound
	}

	pathPrefix := strings.TrimSpace(req.PathPrefix)
	if pathPrefix != "" {
		pathPrefix = normalizePath(pathPrefix)
	}

	// Paths with a pending/running writeback are still being delivered; the
	// sweep must never drop them.
	pendingPaths := map[string]struct{}{}
	for _, op := range ws.Ops {
		if op.Status == "pending" || op.Status == "running" {
			pendingPaths[normalizePath(op.Path)] = struct{}{}
		}
	}

	result := SweepDraftsResult{
		DryRun:  !req.Apply,
		Removed: []SweptDraft{},
		Skipped: []SkippedDraft{},
	}

	candidates := make([]string, 0, len(ws.Files))
	for filePath := range ws.Files {
		candidates = append(candidates, filePath)
	}
	sort.Strings(candidates)

	indexedPaths := map[string]struct{}{}
	for _, indexedPath := range ws.ProviderIndex {
		indexedPaths[indexedPath] = struct{}{}
	}

	for _, filePath := range candidates {
		if pathPrefix != "" && !withinBase(pathPrefix, filePath) {
			continue
		}
		result.Scanned++

		basename := basenameOf(filePath)
		reason := ""
		if isDraftFileBasename(basename) {
			reason = "space-uuid-draft"
		} else {
			for _, pattern := range req.Patterns {
				if matched, _ := path.Match(pattern, basename); matched {
					reason = "pattern"
					break
				}
			}
		}
		if reason == "" {
			continue
		}

		file := ws.Files[filePath]
		if file.ProviderObjectID != "" {
			result.Skipped = append(result.Skipped, SkippedDraft{Path: filePath, Reason: "provider-linked"})
			continue
		}
		if _, indexed := indexedPaths[filePath]; indexed {
			result.Skipped = append(result.Skipped, SkippedDraft{Path: filePath, Reason: "provider-linked"})
			continue
		}
		if _, pending := pendingPaths[normalizePath(filePath)]; pending {
			result.Skipped = append(result.Skipped, SkippedDraft{Path: filePath, Reason: "pending-writeback"})
			continue
		}

		result.Removed = append(result.Removed, SweptDraft{Path: filePath, Reason: reason})
		if req.Apply {
			provider := strings.TrimSpace(file.Provider)
			if provider == "" {
				provider = inferProviderFromPath(filePath)
			}
			s.removeDraftLocked(workspaceID, ws, filePath, provider, req.CorrelationID)
		}
	}

	if req.Apply && len(result.Removed) > 0 {
		_ = s.saveLocked()
	}

	return result, nil
}
