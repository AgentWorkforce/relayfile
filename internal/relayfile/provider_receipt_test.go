package relayfile

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
)

const (
	productionSlackChannel = "C0B9Z4CLG1J"
	productionSlackTS      = "1786747030.233189"
)

func loadSlackReplyReceiptFixture(t *testing.T) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("testdata/slack-reply-receipt.json")
	if err != nil {
		t.Fatalf("read Slack reply receipt fixture: %v", err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(payload, &receipt); err != nil {
		t.Fatalf("decode Slack reply receipt fixture: %v", err)
	}
	return receipt
}

// Issue #3033: a provider-confirmed reply receipt is terminal output from the
// requested write. The built provider-write worker may reconcile the draft
// locally, but neither that reconciliation nor replaying the original queued
// task may execute a second provider write.
func TestProviderReplyReceiptIsTerminalAndReplaySafe(t *testing.T) {
	receipt := loadSlackReplyReceiptFixture(t)
	var providerCalls atomic.Int32
	var outboundMu sync.Mutex
	var outboundContent string
	store := NewStoreWithOptions(StoreOptions{
		ProviderWriteAction: func(action WritebackAction) (map[string]any, error) {
			providerCalls.Add(1)
			outboundMu.Lock()
			outboundContent = action.Content
			outboundMu.Unlock()
			return receipt, nil
		},
	})
	t.Cleanup(store.Close)

	draftPath := "/slack/channels/" + productionSlackChannel + "/messages/1786747000_000001/replies/replies " + draftUUIDA + ".json"
	write, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_slack_receipt",
		Path:          draftPath,
		IfMatch:       "0",
		ContentType:   "application/json",
		Content:       `{"text":"shipping the fix"}`,
		CorrelationID: "corr_slack_reply",
	})
	if err != nil {
		t.Fatalf("write Slack reply draft: %v", err)
	}

	waitForOpStatus(t, store, "ws_slack_receipt", write.OpID, "succeeded")
	if got := providerCalls.Load(); got != 1 {
		t.Fatalf("provider writes = %d, want 1", got)
	}
	outboundMu.Lock()
	gotOutboundContent := outboundContent
	outboundMu.Unlock()
	if gotOutboundContent != `{"text":"shipping the fix"}` {
		t.Fatalf("outbound requested payload = %q", gotOutboundContent)
	}

	op, err := store.GetOperation("ws_slack_receipt", write.OpID)
	if err != nil {
		t.Fatalf("get succeeded operation: %v", err)
	}
	if op.ProviderResult["ts"] != productionSlackTS {
		t.Fatalf("provider receipt ts = %v, want %s", op.ProviderResult["ts"], productionSlackTS)
	}
	if op.LastError != nil {
		t.Fatalf("delivery succeeded but lastError is set: %v", *op.LastError)
	}

	canonicalPath := "/slack/channels/" + productionSlackChannel + "/messages/1786747000_000001/replies/" + productionSlackTS + ".json"
	canonical, err := store.ReadFile("ws_slack_receipt", canonicalPath)
	if err != nil {
		t.Fatalf("provider receipt did not reconcile reply draft: %v", err)
	}
	if canonical.Content != `{"text":"shipping the fix"}` {
		t.Fatalf("receipt leaked into requested content: %q", canonical.Content)
	}
	if _, err := store.ReadFile("ws_slack_receipt", draftPath); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reply draft remained after receipt reconciliation: %v", err)
	}
	createdEvents := eventsForPath(t, store, "ws_slack_receipt", canonicalPath)
	if len(createdEvents) != 1 || createdEvents[0].Type != "file.created" || createdEvents[0].Origin != "system" {
		t.Fatalf("receipt reconciliation event = %+v, want one system file.created", createdEvents)
	}

	// The mounted provider projection may subsequently enrich the canonical
	// file with the same receipt. Ingesting (and redelivering) that provider
	// materialization is local state reconciliation only, never a new write.
	materializedReceipt, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("encode mounted receipt: %v", err)
	}
	receiptEnvelope := WebhookEnvelopeRequest{
		EnvelopeID:  "env_slack_receipt_3033",
		WorkspaceID: "ws_slack_receipt",
		Provider:    "slack",
		DeliveryID:  "delivery_slack_receipt_3033",
		Payload: map[string]any{
			"event_type":       "file.updated",
			"providerObjectId": productionSlackTS,
			"path":             canonicalPath,
			"contentType":      "application/json",
			"content":          string(materializedReceipt),
		},
		CorrelationID: "corr_slack_receipt_materialized",
	}
	if _, err := store.IngestEnvelope(receiptEnvelope); err != nil {
		t.Fatalf("ingest mounted receipt: %v", err)
	}
	if _, err := store.IngestEnvelope(receiptEnvelope); err != nil {
		t.Fatalf("redeliver mounted receipt: %v", err)
	}
	waitForFileContent(t, store, "ws_slack_receipt", canonicalPath, string(materializedReceipt))

	// Replaying the exact queue task exercises the restart/redelivery guard on
	// the built worker path. A succeeded op must short-circuit before dispatch.
	store.processWriteback(writebackTask{
		WorkspaceID: "ws_slack_receipt",
		OpID:        write.OpID,
		Path:        draftPath,
		Revision:    write.TargetRevision,
	})
	if got := providerCalls.Load(); got != 1 {
		t.Fatalf("replayed receipt duplicated Slack delivery: provider writes = %d", got)
	}
	if got := opCount(t, store, "ws_slack_receipt"); got != 1 {
		t.Fatalf("receipt reconciliation created %d operations, want 1", got)
	}
	if pending := store.GetPendingWritebacks("ws_slack_receipt"); len(pending) != 0 {
		t.Fatalf("receipt reconciliation generated follow-on writeback: %+v", pending)
	}
}

// A queued write can be recovered after the mounted provider projection has
// already replaced the draft body with receipt-owned fields. Dispatch must use
// the persisted request snapshot, not the current materialized file.
func TestRequestedSlackWriteSurvivesMaterializedReceiptRaceAndRestart(t *testing.T) {
	backend := &memoryStateBackend{}
	original := NewStoreWithOptions(StoreOptions{
		ExternalWritebackMode: true,
		StateBackend:          backend,
	})
	draftPath := "/slack/channels/" + productionSlackChannel + "/messages/1786747000_000001/replies/replies " + draftUUIDA + ".json"
	requestedContent := `{"text":"immutable requested reply"}`
	write, err := original.WriteFile(WriteRequest{
		WorkspaceID:   "ws_receipt_restart",
		Path:          draftPath,
		IfMatch:       "0",
		ContentType:   "application/json",
		Content:       requestedContent,
		CorrelationID: "corr_receipt_restart",
	})
	if err != nil {
		original.Close()
		t.Fatalf("write queued reply: %v", err)
	}

	receipt := loadSlackReplyReceiptFixture(t)
	materializedReceipt, err := json.Marshal(receipt)
	if err != nil {
		original.Close()
		t.Fatalf("encode materialized receipt: %v", err)
	}
	original.mu.Lock()
	ws := original.ensureWorkspaceLocked("ws_receipt_restart")
	original.applyProviderUpsertLocked(ws, "slack", ApplyAction{
		Type:             ActionFileUpsert,
		Path:             draftPath,
		Content:          string(materializedReceipt),
		ContentType:      "application/json",
		ProviderObjectID: productionSlackTS,
	}, "corr_receipt_materialized_before_dispatch")
	if err := original.saveLocked(); err != nil {
		original.mu.Unlock()
		original.Close()
		t.Fatalf("persist materialized receipt race: %v", err)
	}
	original.mu.Unlock()
	original.Close()

	var providerCalls atomic.Int32
	var outboundMu sync.Mutex
	var outboundContent string
	recovered := NewStoreWithOptions(StoreOptions{
		StateBackend: backend,
		ProviderWriteAction: func(action WritebackAction) (map[string]any, error) {
			providerCalls.Add(1)
			outboundMu.Lock()
			outboundContent = action.Content
			outboundMu.Unlock()
			return receipt, nil
		},
	})
	t.Cleanup(recovered.Close)
	waitForOpStatus(t, recovered, "ws_receipt_restart", write.OpID, "succeeded")

	outboundMu.Lock()
	gotOutbound := outboundContent
	outboundMu.Unlock()
	if gotOutbound != requestedContent {
		t.Fatalf("dispatch re-ingested materialized receipt: got %q, want %q", gotOutbound, requestedContent)
	}
	if got := providerCalls.Load(); got != 1 {
		t.Fatalf("provider writes after restart = %d, want 1", got)
	}
	recovered.mu.RLock()
	_, retained := recovered.workspaces["ws_receipt_restart"].RequestedWrites[write.OpID]
	recovered.mu.RUnlock()
	if retained {
		t.Fatal("terminal receipt retained a replayable requested-write snapshot")
	}
}

// Contract coverage for the three Slack message mutation shapes. Canonical
// records can contain provider-owned fields, but the provider write action must
// receive only writable input fields; local materialized state stays enriched.
func TestSlackProviderOwnedFieldsNeverEnterWritePayload(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		seedUpdate bool
		receiptTS  string
	}{
		{
			name:      "create",
			path:      "/slack/channels/" + productionSlackChannel + "/messages/messages " + draftUUIDA + ".json",
			receiptTS: productionSlackTS,
		},
		{
			name:      "reply",
			path:      "/slack/channels/" + productionSlackChannel + "/messages/1786747000_000001/replies/replies " + draftUUIDB + ".json",
			receiptTS: "1786747031.000002",
		},
		{
			name:       "update",
			path:       "/slack/channels/" + productionSlackChannel + "/messages/1786747032_000003.json",
			seedUpdate: true,
			receiptTS:  "1786747032.000003",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var outbound map[string]any
			store := NewStoreWithOptions(StoreOptions{
				ProviderWriteAction: func(action WritebackAction) (map[string]any, error) {
					var decoded map[string]any
					if err := json.Unmarshal([]byte(action.Content), &decoded); err != nil {
						t.Fatalf("decode outbound Slack payload: %v", err)
					}
					mu.Lock()
					outbound = decoded
					mu.Unlock()
					return map[string]any{
						"provider": "slack",
						"action":   tt.name,
						"endpoint": "/chat.postMessage",
						"status":   200,
						"channel":  productionSlackChannel,
						"ts":       tt.receiptTS,
					}, nil
				},
			})
			t.Cleanup(store.Close)

			baseRevision := "0"
			if tt.seedUpdate {
				store.mu.Lock()
				ws := store.ensureWorkspaceLocked("ws_slack_contract")
				store.applyProviderUpsertLocked(ws, "slack", ApplyAction{
					Type:             ActionFileUpsert,
					Path:             tt.path,
					Content:          `{"text":"before","ts":"1786747032.000003","id":"provider-message"}`,
					ContentType:      "application/json",
					ProviderObjectID: tt.receiptTS,
				}, "corr_seed")
				baseRevision = ws.Files[tt.path].Revision
				store.mu.Unlock()
			}

			write, err := store.WriteFile(WriteRequest{
				WorkspaceID:   "ws_slack_contract",
				Path:          tt.path,
				IfMatch:       baseRevision,
				ContentType:   "application/json",
				Content:       `{"text":"after","ts":"provider-owned","id":"provider-message","createdAt":"provider-owned"}`,
				CorrelationID: "corr_" + tt.name,
			})
			if err != nil {
				t.Fatalf("write %s payload: %v", tt.name, err)
			}
			waitForOpStatus(t, store, "ws_slack_contract", write.OpID, "succeeded")

			mu.Lock()
			got := outbound
			mu.Unlock()
			if got["text"] != "after" {
				t.Fatalf("writable text missing from %s payload: %+v", tt.name, got)
			}
			for _, field := range []string{"ts", "id", "createdAt"} {
				if _, exists := got[field]; exists {
					t.Fatalf("provider-owned field %q leaked into %s payload: %+v", field, tt.name, got)
				}
			}

			if tt.seedUpdate {
				materialized, err := store.ReadFile("ws_slack_contract", tt.path)
				if err != nil {
					t.Fatalf("read materialized update: %v", err)
				}
				if !jsonContainsField(materialized.Content, "ts") {
					t.Fatalf("sanitizing outbound payload erased provider-owned local state: %s", materialized.Content)
				}
			}
		})
	}
}

func jsonContainsField(content, field string) bool {
	var decoded map[string]any
	return json.Unmarshal([]byte(content), &decoded) == nil && decoded[field] != nil
}

type failNthSaveBackend struct {
	mu       sync.Mutex
	calls    int
	failAt   int
	loaded   bool
	snapshot persistedState
}

func (b *failNthSaveBackend) Load() (*persistedState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loaded {
		return nil, nil
	}
	payload, err := json.Marshal(b.snapshot)
	if err != nil {
		return nil, err
	}
	var snapshot persistedState
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (b *failNthSaveBackend) Save(state *persistedState) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.calls == b.failAt {
		return errors.New("receipt state persistence failed")
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, &b.snapshot); err != nil {
		return err
	}
	b.loaded = true
	return nil
}

func TestSuccessfulReceiptKeepsDeliverySucceededWhenBookkeepingFails(t *testing.T) {
	backend := &failNthSaveBackend{failAt: 2}
	store := NewStoreWithOptions(StoreOptions{
		ExternalWritebackMode: true,
		StateBackend:          backend,
	})
	t.Cleanup(store.Close)

	draftPath := "/slack/channels/" + productionSlackChannel + "/messages/messages " + draftUUIDA + ".json"
	write := writeDraft(t, store, "ws_bookkeeping", draftPath, `{"text":"delivered"}`)
	resp, err := store.AcknowledgeWriteback("ws_bookkeeping", write.OpID, WritebackAck{
		Success:        true,
		ProviderResult: loadSlackReplyReceiptFixture(t),
	}, "corr_receipt")
	if err != nil {
		t.Fatalf("provider-confirmed receipt must not become a delivery error: %v", err)
	}
	if resp["success"] != true {
		t.Fatalf("delivery outcome changed after bookkeeping failure: %+v", resp)
	}
	if resp["bookkeepingError"] != "receipt state persistence failed" {
		t.Fatalf("bookkeeping failure not reported separately: %+v", resp)
	}

	op, err := store.GetOperation("ws_bookkeeping", write.OpID)
	if err != nil {
		t.Fatalf("get operation after bookkeeping failure: %v", err)
	}
	if op.Status != "succeeded" || op.LastError != nil {
		t.Fatalf("bookkeeping failure contradicted delivery outcome: %+v", op)
	}
	if op.BookkeepingError == nil || *op.BookkeepingError != "receipt state persistence failed" {
		t.Fatalf("operation did not expose bookkeeping failure separately: %+v", op)
	}
	persisted := NewStoreWithOptions(StoreOptions{
		ExternalWritebackMode: true,
		StateBackend:          backend,
	})
	t.Cleanup(persisted.Close)
	persistedOp, err := persisted.GetOperation("ws_bookkeeping", write.OpID)
	if err != nil {
		t.Fatalf("reload terminal operation after bookkeeping repair: %v", err)
	}
	if persistedOp.Status != "succeeded" || persistedOp.BookkeepingError == nil {
		t.Fatalf("bookkeeping repair did not persist terminal delivery: %+v", persistedOp)
	}
	persisted.mu.RLock()
	_, replayable := persisted.workspaces["ws_bookkeeping"].RequestedWrites[write.OpID]
	persisted.mu.RUnlock()
	if replayable {
		t.Fatal("persisted terminal receipt retained provider write input")
	}
}

func TestSucceededReceiptCannotBeDowngradedByFailureReplay(t *testing.T) {
	store := newExternalStore(t)
	draftPath := "/slack/channels/" + productionSlackChannel + "/messages/messages " + draftUUIDA + ".json"
	write := writeDraft(t, store, "ws_terminal_replay", draftPath, `{"text":"delivered"}`)
	receipt := loadSlackReplyReceiptFixture(t)

	first, err := store.AcknowledgeWriteback("ws_terminal_replay", write.OpID, WritebackAck{
		Success:        true,
		ProviderResult: receipt,
	}, "corr_success")
	if err != nil {
		t.Fatalf("ack success: %v", err)
	}
	if first["success"] != true {
		t.Fatalf("unexpected success receipt: %+v", first)
	}

	replayed, err := store.AcknowledgeWriteback("ws_terminal_replay", write.OpID, WritebackAck{
		Success: false,
		Error:   "local receipt bookkeeping failed",
	}, "corr_stale_failure")
	if err != nil {
		t.Fatalf("stale failure replay: %v", err)
	}
	if replayed["success"] != true || replayed["replayed"] != true {
		t.Fatalf("terminal success was not treated idempotently: %+v", replayed)
	}

	op, err := store.GetOperation("ws_terminal_replay", write.OpID)
	if err != nil {
		t.Fatalf("get replayed operation: %v", err)
	}
	if op.Status != "succeeded" || op.LastError != nil {
		t.Fatalf("failure replay downgraded successful delivery: %+v", op)
	}
	if got := opCount(t, store, "ws_terminal_replay"); got != 1 {
		t.Fatalf("receipt replay synthesized operations: %d", got)
	}
}
