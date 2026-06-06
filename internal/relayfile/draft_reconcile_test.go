package relayfile

import (
	"strings"
	"testing"
)

// Issue #242: the draft-rename contract documented by vfs-client draftFile()
// ("worker renames the file to the canonical id on receipt") was implemented
// by no consumer, so agent-authored writeback drafts accumulated forever at
// canonical provider roots. These tests pin the service-side contract:
//
//  1. Acking a writeback successfully with an externalId renames the draft
//     to the canonical id (or removes it when the canonical record already
//     materialized).
//  2. Every draft mutation performed by the service is classification-exempt:
//     it must NEVER create a new operation, enqueue a new writeback, or emit
//     an agent_write event (which downstream consumers classify as writebacks
//     and could turn into provider deletes, e.g. Slack chat.delete).
//  3. A one-time residue sweep drains already-accumulated drafts under the
//     same exemption, scoped strictly to draft name shapes.

const (
	draftUUIDA = "0e89a031-65f0-480e-a823-ab1d94b324ea"
	draftUUIDB = "15250fcf-de54-44b0-a808-c8e514480647"
)

func newExternalStore(t *testing.T) *Store {
	t.Helper()
	store := NewStoreWithOptions(StoreOptions{ExternalWritebackMode: true})
	t.Cleanup(store.Close)
	return store
}

func writeDraft(t *testing.T, store *Store, workspaceID, path, content string) WriteResult {
	t.Helper()
	result, err := store.WriteFile(WriteRequest{
		WorkspaceID:   workspaceID,
		Path:          path,
		IfMatch:       "0",
		ContentType:   "application/json",
		Content:       content,
		CorrelationID: "corr_draft_write",
	})
	if err != nil {
		t.Fatalf("draft write failed: %v", err)
	}
	return result
}

func opCount(t *testing.T, store *Store, workspaceID string) int {
	t.Helper()
	feed, err := store.ListOperations(workspaceID, "", "", "", "", 1000)
	if err != nil {
		t.Fatalf("list operations failed: %v", err)
	}
	return len(feed.Items)
}

func eventsForPath(t *testing.T, store *Store, workspaceID, path string) []Event {
	t.Helper()
	feed, err := store.GetEvents(workspaceID, "", "", 1000)
	if err != nil {
		t.Fatalf("get events failed: %v", err)
	}
	var matched []Event
	for _, event := range feed.Events {
		if event.Path == path {
			matched = append(matched, event)
		}
	}
	return matched
}

// assertNoAgentWriteMutation pins the classification-exemption property: a
// service-side draft rename/removal must never look like an agent mutation.
func assertNoAgentWriteMutation(t *testing.T, store *Store, workspaceID, path string) {
	t.Helper()
	for _, event := range eventsForPath(t, store, workspaceID, path) {
		if event.Origin == "agent_write" && (event.Type == "file.deleted" || event.Type == "file.created") {
			t.Fatalf("draft mutation at %s emitted adapter-actionable agent_write event %s — this re-classifies as a writeback", path, event.Type)
		}
	}
}

func TestAckWithExternalIDRenamesDraftToCanonicalID(t *testing.T) {
	store := newExternalStore(t)
	draftPath := "/slack/channels/C0ALQ06AAUT/messages/messages " + draftUUIDA + ".json"
	result := writeDraft(t, store, "ws_1", draftPath, `{"text":"hi"}`)

	resp, err := store.AcknowledgeWriteback("ws_1", result.OpID, WritebackAck{
		Success:    true,
		ExternalID: "1780018871.351819",
	}, "corr_ack_1")
	if err != nil {
		t.Fatalf("ack failed: %v", err)
	}

	if _, err := store.ReadFile("ws_1", draftPath); err != ErrNotFound {
		t.Fatalf("expected draft to be renamed away, got err=%v", err)
	}
	canonicalPath := "/slack/channels/C0ALQ06AAUT/messages/1780018871.351819.json"
	file, err := store.ReadFile("ws_1", canonicalPath)
	if err != nil {
		t.Fatalf("expected canonical-id file at %s: %v", canonicalPath, err)
	}
	if file.Content != `{"text":"hi"}` {
		t.Fatalf("expected draft content preserved, got %q", file.Content)
	}
	if file.ProviderObjectID != "1780018871.351819" {
		t.Fatalf("expected ProviderObjectID linked to externalId, got %q", file.ProviderObjectID)
	}

	draft, ok := resp["draft"].(map[string]any)
	if !ok {
		t.Fatalf("expected draft disposition in ack response, got %v", resp)
	}
	if draft["action"] != "renamed" || draft["from"] != draftPath || draft["to"] != canonicalPath {
		t.Fatalf("unexpected draft disposition: %v", draft)
	}
}

func TestAckRenameIsClassificationExempt(t *testing.T) {
	store := newExternalStore(t)
	draftPath := "/slack/channels/C0ALQ06AAUT/messages/messages " + draftUUIDA + ".json"
	result := writeDraft(t, store, "ws_1", draftPath, `{"text":"hi"}`)

	opsBefore := opCount(t, store, "ws_1")
	if _, err := store.AcknowledgeWriteback("ws_1", result.OpID, WritebackAck{
		Success:    true,
		ExternalID: "1780018871.351819",
	}, "corr_ack_1"); err != nil {
		t.Fatalf("ack failed: %v", err)
	}

	if got := opCount(t, store, "ws_1"); got != opsBefore {
		t.Fatalf("draft rename created new operations: before=%d after=%d", opsBefore, got)
	}
	if pending := store.GetPendingWritebacks("ws_1"); len(pending) != 0 {
		t.Fatalf("draft rename enqueued new writebacks: %v", pending)
	}
	assertNoAgentWriteMutation(t, store, "ws_1", draftPath)
	assertNoAgentWriteMutation(t, store, "ws_1", "/slack/channels/C0ALQ06AAUT/messages/1780018871.351819.json")

	// The rename must be observable to subscribers as system-origin events.
	deleted := eventsForPath(t, store, "ws_1", draftPath)
	var sawSystemDelete bool
	for _, event := range deleted {
		if event.Type == "file.deleted" && event.Origin == "system" {
			sawSystemDelete = true
		}
	}
	if !sawSystemDelete {
		t.Fatalf("expected system-origin file.deleted for draft path, got %v", deleted)
	}
}

func TestAckWhenCanonicalAlreadyMaterializedRemovesDraft(t *testing.T) {
	store := newExternalStore(t)
	draftPath := "/slack/channels/C0ALQ06AAUT/messages/messages " + draftUUIDA + ".json"
	result := writeDraft(t, store, "ws_1", draftPath, `{"text":"hi"}`)

	// Simulate the provider webhook materializing the canonical record first.
	canonicalPath := "/slack/channels/C0ALQ06AAUT/messages/1780018871_351819.json"
	store.mu.Lock()
	ws := store.ensureWorkspaceLocked("ws_1")
	store.applyProviderUpsertLocked(ws, "slack", ApplyAction{
		Type:             ActionFileUpsert,
		Path:             canonicalPath,
		Content:          `{"text":"hi","ts":"1780018871.351819"}`,
		ContentType:      "application/json",
		ProviderObjectID: "1780018871.351819",
	}, "corr_sync_1")
	store.mu.Unlock()

	opsBefore := opCount(t, store, "ws_1")
	resp, err := store.AcknowledgeWriteback("ws_1", result.OpID, WritebackAck{
		Success:    true,
		ExternalID: "1780018871.351819",
	}, "corr_ack_1")
	if err != nil {
		t.Fatalf("ack failed: %v", err)
	}

	if _, err := store.ReadFile("ws_1", draftPath); err != ErrNotFound {
		t.Fatalf("expected draft removed when canonical already exists, got err=%v", err)
	}
	if _, err := store.ReadFile("ws_1", canonicalPath); err != nil {
		t.Fatalf("canonical record must remain readable: %v", err)
	}
	draft, _ := resp["draft"].(map[string]any)
	if draft == nil || draft["action"] != "removed" {
		t.Fatalf("expected removed disposition, got %v", resp)
	}

	// Reviewer steer (claude-mount-cleanup): the remove branch must be
	// provider-invisible too — no adapter-actionable delete may escape.
	if got := opCount(t, store, "ws_1"); got != opsBefore {
		t.Fatalf("draft removal created new operations: before=%d after=%d", opsBefore, got)
	}
	if pending := store.GetPendingWritebacks("ws_1"); len(pending) != 0 {
		t.Fatalf("draft removal enqueued new writebacks: %v", pending)
	}
	assertNoAgentWriteMutation(t, store, "ws_1", draftPath)
}

func TestAckWithCanonicalPathOverride(t *testing.T) {
	store := newExternalStore(t)
	draftPath := "/slack/channels/C0ALQ06AAUT/messages/wb-claude1-ack2.json"
	result := writeDraft(t, store, "ws_1", draftPath, `{"text":"ack"}`)

	canonicalPath := "/slack/channels/C0ALQ06AAUT/messages/1780018871_351819.json"
	resp, err := store.AcknowledgeWriteback("ws_1", result.OpID, WritebackAck{
		Success:       true,
		ExternalID:    "1780018871.351819",
		CanonicalPath: canonicalPath,
	}, "corr_ack_1")
	if err != nil {
		t.Fatalf("ack failed: %v", err)
	}
	if _, err := store.ReadFile("ws_1", draftPath); err != ErrNotFound {
		t.Fatalf("expected hand-named draft renamed away, got err=%v", err)
	}
	if _, err := store.ReadFile("ws_1", canonicalPath); err != nil {
		t.Fatalf("expected file at canonicalPath override: %v", err)
	}
	draft, _ := resp["draft"].(map[string]any)
	if draft == nil || draft["action"] != "renamed" || draft["to"] != canonicalPath {
		t.Fatalf("unexpected disposition: %v", resp)
	}
}

func TestAckCanonicalPathOutsideProviderRootFallsBackToExternalID(t *testing.T) {
	store := newExternalStore(t)
	draftPath := "/slack/channels/C0ALQ06AAUT/messages/messages " + draftUUIDA + ".json"
	result := writeDraft(t, store, "ws_1", draftPath, `{"text":"hi"}`)

	if _, err := store.AcknowledgeWriteback("ws_1", result.OpID, WritebackAck{
		Success:       true,
		ExternalID:    "1780018871.351819",
		CanonicalPath: "/github/issues/42.json",
	}, "corr_ack_1"); err != nil {
		t.Fatalf("ack failed: %v", err)
	}
	if _, err := store.ReadFile("ws_1", "/github/issues/42.json"); err != ErrNotFound {
		t.Fatalf("cross-provider canonicalPath must not be honored")
	}
	if _, err := store.ReadFile("ws_1", "/slack/channels/C0ALQ06AAUT/messages/1780018871.351819.json"); err != nil {
		t.Fatalf("expected externalId-derived rename fallback: %v", err)
	}
}

func TestAckWithoutExternalIDLeavesDraftUntouched(t *testing.T) {
	store := newExternalStore(t)
	draftPath := "/slack/channels/C0ALQ06AAUT/messages/messages " + draftUUIDA + ".json"
	result := writeDraft(t, store, "ws_1", draftPath, `{"text":"hi"}`)

	if _, err := store.AcknowledgeWriteback("ws_1", result.OpID, WritebackAck{Success: true}, "corr_ack_1"); err != nil {
		t.Fatalf("ack failed: %v", err)
	}
	if _, err := store.ReadFile("ws_1", draftPath); err != nil {
		t.Fatalf("legacy ack without externalId must not mutate files: %v", err)
	}
}

func TestAckFailureLeavesDraftUntouched(t *testing.T) {
	store := newExternalStore(t)
	draftPath := "/slack/channels/C0ALQ06AAUT/messages/messages " + draftUUIDA + ".json"
	result := writeDraft(t, store, "ws_1", draftPath, `{"text":"hi"}`)

	if _, err := store.AcknowledgeWriteback("ws_1", result.OpID, WritebackAck{
		Success:    false,
		Error:      "provider 500",
		ExternalID: "1780018871.351819",
	}, "corr_ack_1"); err != nil {
		t.Fatalf("ack failed: %v", err)
	}
	if _, err := store.ReadFile("ws_1", draftPath); err != nil {
		t.Fatalf("failed writeback must not reconcile the draft: %v", err)
	}
}

func TestAckUpdateOfCanonicalRecordDoesNotRename(t *testing.T) {
	store := newExternalStore(t)
	canonicalPath := "/slack/channels/C0ALQ06AAUT/messages/1780018871_351819.json"
	store.mu.Lock()
	ws := store.ensureWorkspaceLocked("ws_1")
	store.applyProviderUpsertLocked(ws, "slack", ApplyAction{
		Type:             ActionFileUpsert,
		Path:             canonicalPath,
		Content:          `{"text":"original"}`,
		ContentType:      "application/json",
		ProviderObjectID: "1780018871.351819",
	}, "corr_sync_1")
	store.mu.Unlock()

	file, err := store.ReadFile("ws_1", canonicalPath)
	if err != nil {
		t.Fatalf("setup read failed: %v", err)
	}
	result, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_1",
		Path:          canonicalPath,
		IfMatch:       file.Revision,
		ContentType:   "application/json",
		Content:       `{"text":"edited"}`,
		CorrelationID: "corr_edit_1",
	})
	if err != nil {
		t.Fatalf("edit failed: %v", err)
	}

	if _, err := store.AcknowledgeWriteback("ws_1", result.OpID, WritebackAck{
		Success:    true,
		ExternalID: "1780018871.351819",
	}, "corr_ack_1"); err != nil {
		t.Fatalf("ack failed: %v", err)
	}
	got, err := store.ReadFile("ws_1", canonicalPath)
	if err != nil {
		t.Fatalf("canonical record must remain at its path after update ack: %v", err)
	}
	if got.Content != `{"text":"edited"}` {
		t.Fatalf("expected edited content preserved, got %q", got.Content)
	}
}

func TestRenamedDraftConvergesWithLaterProviderSync(t *testing.T) {
	store := newExternalStore(t)
	draftPath := "/slack/channels/C0ALQ06AAUT/messages/messages " + draftUUIDA + ".json"
	result := writeDraft(t, store, "ws_1", draftPath, `{"text":"hi"}`)

	if _, err := store.AcknowledgeWriteback("ws_1", result.OpID, WritebackAck{
		Success:    true,
		ExternalID: "1780018871.351819",
	}, "corr_ack_1"); err != nil {
		t.Fatalf("ack failed: %v", err)
	}

	// The provider webhook later materializes the canonical projection at a
	// provider-chosen path. The object-id index must relocate our renamed
	// file instead of leaving a duplicate.
	canonicalPath := "/slack/channels/C0ALQ06AAUT/messages/1780018871_351819.json"
	store.mu.Lock()
	ws := store.ensureWorkspaceLocked("ws_1")
	store.applyProviderUpsertLocked(ws, "slack", ApplyAction{
		Type:             ActionFileUpsert,
		Path:             canonicalPath,
		Content:          `{"text":"hi","ts":"1780018871.351819"}`,
		ContentType:      "application/json",
		ProviderObjectID: "1780018871.351819",
	}, "corr_sync_1")
	store.mu.Unlock()

	if _, err := store.ReadFile("ws_1", "/slack/channels/C0ALQ06AAUT/messages/1780018871.351819.json"); err != ErrNotFound {
		t.Fatalf("expected interim renamed file to be relocated by provider sync")
	}
	if _, err := store.ReadFile("ws_1", canonicalPath); err != nil {
		t.Fatalf("expected canonical record after sync: %v", err)
	}
	if _, err := store.ReadFile("ws_1", draftPath); err != ErrNotFound {
		t.Fatalf("draft must not reappear")
	}
}

func seedResidueFile(t *testing.T, store *Store, workspaceID, path, content string) {
	t.Helper()
	store.mu.Lock()
	ws := store.ensureWorkspaceLocked(workspaceID)
	revision := store.nextRevisionLocked()
	ws.Files[path] = File{
		Path:        path,
		Revision:    revision,
		ContentType: "application/json",
		Content:     content,
		Provider:    inferProviderFromPath(path),
	}
	ws.Revision = revision
	store.mu.Unlock()
}

func TestSweepDraftsRemovesSpaceUUIDResidue(t *testing.T) {
	store := newExternalStore(t)
	// Class E residue: draftFile() space-form, delivered long ago — the ops
	// are gone (service restarts), only the files remain.
	residueA := "/slack/channels/C0ALQ06AAUT/messages/messages " + draftUUIDA + ".json"
	residueB := "/slack/channels/C0AJ0MWH83F/messages/messages " + draftUUIDB + ".json"
	seedResidueFile(t, store, "ws_1", residueA, `{"text":"old"}`)
	seedResidueFile(t, store, "ws_1", residueB, `{"text":"old"}`)

	// Canonical synced record must survive any sweep.
	canonicalPath := "/slack/channels/C0ALQ06AAUT/messages/1780018871_351819.json"
	store.mu.Lock()
	ws := store.ensureWorkspaceLocked("ws_1")
	store.applyProviderUpsertLocked(ws, "slack", ApplyAction{
		Type:             ActionFileUpsert,
		Path:             canonicalPath,
		Content:          `{"text":"real"}`,
		ContentType:      "application/json",
		ProviderObjectID: "1780018871.351819",
	}, "corr_sync_1")
	store.mu.Unlock()

	result, err := store.SweepWritebackDrafts("ws_1", SweepDraftsRequest{
		Apply:         true,
		CorrelationID: "corr_sweep_1",
	})
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if result.DryRun {
		t.Fatalf("expected applied sweep, got dry run")
	}
	if len(result.Removed) != 2 {
		t.Fatalf("expected 2 removed drafts, got %v", result.Removed)
	}
	for _, residue := range []string{residueA, residueB} {
		if _, err := store.ReadFile("ws_1", residue); err != ErrNotFound {
			t.Fatalf("expected residue %s removed", residue)
		}
	}
	if _, err := store.ReadFile("ws_1", canonicalPath); err != nil {
		t.Fatalf("canonical record must survive sweep: %v", err)
	}
}

func TestSweepDraftsPatternMatchesHandNamedResidue(t *testing.T) {
	store := newExternalStore(t)
	classD := "/slack/channels/C0AD7UU0J1G__proj-cloud/messages/wb-claude1-ack2.json"
	unrelated := "/slack/channels/C0AD7UU0J1G__proj-cloud/messages/claude-1-welcome-back-reply.json"
	seedResidueFile(t, store, "ws_1", classD, `{"text":"ack2"}`)
	seedResidueFile(t, store, "ws_1", unrelated, `{"text":"reply"}`)

	result, err := store.SweepWritebackDrafts("ws_1", SweepDraftsRequest{
		Patterns:      []string{"wb-*.json"},
		Apply:         true,
		CorrelationID: "corr_sweep_1",
	})
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0].Path != classD {
		t.Fatalf("expected exactly the wb-* draft removed, got %v", result.Removed)
	}
	if _, err := store.ReadFile("ws_1", classD); err != ErrNotFound {
		t.Fatalf("expected wb-* draft removed")
	}
	// Hand-named files NOT matching a supplied pattern stay: the sweep never
	// guesses beyond the explicit name shapes it was given.
	if _, err := store.ReadFile("ws_1", unrelated); err != nil {
		t.Fatalf("non-matching file must survive: %v", err)
	}
}

func TestSweepDryRunByDefaultRemovesNothing(t *testing.T) {
	store := newExternalStore(t)
	residue := "/slack/channels/C0ALQ06AAUT/messages/messages " + draftUUIDA + ".json"
	seedResidueFile(t, store, "ws_1", residue, `{"text":"old"}`)

	result, err := store.SweepWritebackDrafts("ws_1", SweepDraftsRequest{CorrelationID: "corr_sweep_1"})
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if !result.DryRun {
		t.Fatalf("expected dry run when Apply not set")
	}
	if len(result.Removed) != 1 {
		t.Fatalf("dry run must report candidates, got %v", result.Removed)
	}
	if _, err := store.ReadFile("ws_1", residue); err != nil {
		t.Fatalf("dry run must not remove files: %v", err)
	}
}

func TestSweepSkipsDraftsWithPendingWritebacks(t *testing.T) {
	store := newExternalStore(t)
	// A live draft whose writeback has not been delivered yet must never be
	// swept — that would drop the message before it reaches the provider.
	pendingDraft := "/slack/channels/C0ALQ06AAUT/messages/messages " + draftUUIDA + ".json"
	writeDraft(t, store, "ws_1", pendingDraft, `{"text":"in flight"}`)

	result, err := store.SweepWritebackDrafts("ws_1", SweepDraftsRequest{
		Apply:         true,
		CorrelationID: "corr_sweep_1",
	})
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if len(result.Removed) != 0 {
		t.Fatalf("pending draft must not be removed, got %v", result.Removed)
	}
	var sawSkip bool
	for _, skipped := range result.Skipped {
		if skipped.Path == pendingDraft && skipped.Reason == "pending-writeback" {
			sawSkip = true
		}
	}
	if !sawSkip {
		t.Fatalf("expected pending draft reported as skipped, got %v", result.Skipped)
	}
	if _, err := store.ReadFile("ws_1", pendingDraft); err != nil {
		t.Fatalf("pending draft must survive sweep: %v", err)
	}
}

func TestSweepSkipsProviderLinkedFilesEvenWhenPatternMatches(t *testing.T) {
	store := newExternalStore(t)
	// A provider-reconciled record whose name happens to match a sweep
	// pattern must survive: ProviderObjectID marks it as canonical.
	linked := "/slack/channels/C0ALQ06AAUT/messages/wb-looks-like-draft.json"
	store.mu.Lock()
	ws := store.ensureWorkspaceLocked("ws_1")
	store.applyProviderUpsertLocked(ws, "slack", ApplyAction{
		Type:             ActionFileUpsert,
		Path:             linked,
		Content:          `{"text":"canonical"}`,
		ContentType:      "application/json",
		ProviderObjectID: "1780000000.000001",
	}, "corr_sync_1")
	store.mu.Unlock()

	result, err := store.SweepWritebackDrafts("ws_1", SweepDraftsRequest{
		Patterns:      []string{"wb-*.json"},
		Apply:         true,
		CorrelationID: "corr_sweep_1",
	})
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if len(result.Removed) != 0 {
		t.Fatalf("provider-linked file must not be removed, got %v", result.Removed)
	}
	if _, err := store.ReadFile("ws_1", linked); err != nil {
		t.Fatalf("provider-linked file must survive: %v", err)
	}
}

func TestSweepHonorsPathPrefix(t *testing.T) {
	store := newExternalStore(t)
	inScope := "/slack/channels/C0ALQ06AAUT/messages/messages " + draftUUIDA + ".json"
	outOfScope := "/slack/channels/C0AJ0MWH83F/messages/messages " + draftUUIDB + ".json"
	seedResidueFile(t, store, "ws_1", inScope, `{"text":"old"}`)
	seedResidueFile(t, store, "ws_1", outOfScope, `{"text":"old"}`)

	result, err := store.SweepWritebackDrafts("ws_1", SweepDraftsRequest{
		PathPrefix:    "/slack/channels/C0ALQ06AAUT",
		Apply:         true,
		CorrelationID: "corr_sweep_1",
	})
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0].Path != inScope {
		t.Fatalf("expected only in-scope draft removed, got %v", result.Removed)
	}
	if _, err := store.ReadFile("ws_1", outOfScope); err != nil {
		t.Fatalf("out-of-scope draft must survive: %v", err)
	}
}

func TestSweepIsClassificationExempt(t *testing.T) {
	store := newExternalStore(t)
	residue := "/slack/channels/C0ALQ06AAUT/messages/messages " + draftUUIDA + ".json"
	classD := "/slack/channels/C0AD7UU0J1G__proj-cloud/messages/wb-claude1-ack2.json"
	seedResidueFile(t, store, "ws_1", residue, `{"text":"old"}`)
	seedResidueFile(t, store, "ws_1", classD, `{"text":"ack2"}`)

	opsBefore := opCount(t, store, "ws_1")
	if _, err := store.SweepWritebackDrafts("ws_1", SweepDraftsRequest{
		Patterns:      []string{"wb-*.json"},
		Apply:         true,
		CorrelationID: "corr_sweep_1",
	}); err != nil {
		t.Fatalf("sweep failed: %v", err)
	}

	if got := opCount(t, store, "ws_1"); got != opsBefore {
		t.Fatalf("sweep created operations: before=%d after=%d — a swept draft must NEVER enqueue a writeback (chat.delete risk)", opsBefore, got)
	}
	if pending := store.GetPendingWritebacks("ws_1"); len(pending) != 0 {
		t.Fatalf("sweep enqueued writebacks: %v", pending)
	}
	for _, path := range []string{residue, classD} {
		assertNoAgentWriteMutation(t, store, "ws_1", path)
		events := eventsForPath(t, store, "ws_1", path)
		var sawSystemDelete bool
		for _, event := range events {
			if event.Type == "file.deleted" && event.Origin == "system" {
				sawSystemDelete = true
			}
		}
		if !sawSystemDelete {
			t.Fatalf("sweep removal of %s must emit a system-origin file.deleted for subscribers, got %v", path, events)
		}
	}
}

func TestSweepRejectsBlankWorkspace(t *testing.T) {
	store := newExternalStore(t)
	if _, err := store.SweepWritebackDrafts("", SweepDraftsRequest{}); err != ErrInvalidInput {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if _, err := store.SweepWritebackDrafts("ws_unknown", SweepDraftsRequest{}); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for unknown workspace, got %v", err)
	}
}

func TestDraftBasenameDetection(t *testing.T) {
	cases := []struct {
		path  string
		draft bool
	}{
		{"/slack/channels/C0ALQ06AAUT/messages/messages " + draftUUIDA + ".json", true},
		{"/linear/issues/issues " + draftUUIDB + ".json", true},
		{"/slack/channels/C0ALQ06AAUT/messages/1780018871_351819.json", false},
		{"/slack/channels/C0ALQ06AAUT/messages/wb-claude1-ack2.json", false}, // class D needs explicit pattern
		{"/slack/channels/C0ALQ06AAUT/messages/messages not-a-uuid.json", false},
		{"/slack/channels/C0ALQ06AAUT/messages " + draftUUIDA, false}, // no .json
	}
	for _, tc := range cases {
		if got := isDraftFileBasename(basenameOf(tc.path)); got != tc.draft {
			t.Fatalf("isDraftFileBasename(%q) = %v, want %v", tc.path, got, tc.draft)
		}
	}
	if !strings.Contains(basenameOf(cases[0].path), " ") {
		t.Fatalf("sanity: draftFile space-form fixture lost its space")
	}
}
