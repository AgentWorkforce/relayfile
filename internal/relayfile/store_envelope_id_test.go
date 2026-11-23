package relayfile

import (
	"fmt"
	"sync"
	"testing"
)

// Regression for conformance RF-ING-003 (CI run 42336690516): two concurrent
// deliveries minted the same env_<nanos> ID, the second overwrote the first in
// envelopesByID, and the first delivery was lost behind a 202.
func TestIngestEnvelopeSameNanosecondMintsDistinctEnvelopeIDs(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	store.generatedIDNanos = func() int64 { return 1794030548828838053 }

	ingest := func(index int) QueuedResponse {
		t.Helper()
		resp, err := store.IngestEnvelope(WebhookEnvelopeRequest{
			WorkspaceID: "ws_collision",
			Provider:    "conformance",
			DeliveryID:  fmt.Sprintf("delivery-%02d", index),
			Payload: map[string]any{
				"event_type":       "file.updated",
				"path":             fmt.Sprintf("/ingest/concurrent-%02d.json", index),
				"content":          fmt.Sprintf("value-%d", index),
				"contentType":      "text/plain",
				"providerObjectId": fmt.Sprintf("concurrent-%d", index),
			},
			CorrelationID: fmt.Sprintf("corr-%02d", index),
		})
		if err != nil {
			t.Fatalf("ingest %d failed: %v", index, err)
		}
		return resp
	}

	first := ingest(6)
	second := ingest(7)
	if first.ID != "env_1794030548828838053" {
		t.Fatalf("unexpected first envelope ID %q", first.ID)
	}
	if second.ID == first.ID {
		t.Fatalf("second ingest reused envelope ID %q", first.ID)
	}
	if second.ID != "env_1794030548828838053_1" {
		t.Fatalf("unexpected second envelope ID %q", second.ID)
	}

	store.mu.RLock()
	firstReq, firstOK := store.envelopesByID[first.ID]
	secondReq, secondOK := store.envelopesByID[second.ID]
	store.mu.RUnlock()
	if !firstOK || !secondOK {
		t.Fatalf("expected both envelopes stored, got first=%v second=%v", firstOK, secondOK)
	}
	if firstReq.DeliveryID != "delivery-06" || secondReq.DeliveryID != "delivery-07" {
		t.Fatalf("envelopes stored under the wrong deliveries: %q %q", firstReq.DeliveryID, secondReq.DeliveryID)
	}

	store.processEnvelope(first.ID)
	store.processEnvelope(second.ID)

	for index := 6; index <= 7; index++ {
		path := fmt.Sprintf("/ingest/concurrent-%02d.json", index)
		file, err := store.ReadFile("ws_collision", path)
		if err != nil {
			t.Fatalf("delivery %d was not projected: %v", index, err)
		}
		if file.Content != fmt.Sprintf("value-%d", index) {
			t.Fatalf("delivery %d projected wrong content %q", index, file.Content)
		}
	}
	feed, err := store.GetEvents("ws_collision", "", "", 100)
	if err != nil {
		t.Fatalf("events failed: %v", err)
	}
	projected := map[string]bool{}
	for _, event := range feed.Events {
		if event.Type == "file.created" || event.Type == "file.updated" {
			projected[event.Path] = true
		}
	}
	if len(projected) != 2 {
		t.Fatalf("expected file events for both deliveries, got %v", projected)
	}
}

func TestIngestEnvelopeSameNanosecondGeneratedDeliveryIDsDoNotCollapse(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	store.generatedIDNanos = func() int64 { return 42 }

	var ids []string
	for index := 0; index < 2; index++ {
		resp, err := store.IngestEnvelope(WebhookEnvelopeRequest{
			WorkspaceID: "ws_generated_delivery",
			Provider:    "conformance",
			Payload: map[string]any{
				"event_type": "file.updated",
				"path":       fmt.Sprintf("/ingest/no-delivery-%d.json", index),
				"content":    "x",
			},
		})
		if err != nil {
			t.Fatalf("ingest %d failed: %v", index, err)
		}
		ids = append(ids, resp.ID)
	}
	if ids[0] == ids[1] {
		t.Fatalf("deliveries without a provider delivery ID collapsed into %q", ids[0])
	}
	store.mu.RLock()
	stored := len(store.envelopesByID)
	firstDelivery := store.envelopesByID[ids[0]].DeliveryID
	secondDelivery := store.envelopesByID[ids[1]].DeliveryID
	store.mu.RUnlock()
	if stored != 2 {
		t.Fatalf("expected 2 stored envelopes, got %d", stored)
	}
	if firstDelivery != "dlv_42" || secondDelivery != "dlv_42_1" {
		t.Fatalf("unexpected generated delivery IDs %q %q", firstDelivery, secondDelivery)
	}
}

func TestIngestEnvelopeConcurrentGeneratedIDsAreDistinct(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)

	const count = 400
	ids := make([]string, count)
	var wg sync.WaitGroup
	for index := 0; index < count; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			resp, err := store.IngestEnvelope(WebhookEnvelopeRequest{
				WorkspaceID: "ws_concurrent",
				Provider:    "conformance",
				DeliveryID:  fmt.Sprintf("delivery-%d", index),
				Payload: map[string]any{
					"event_type": "file.updated",
					"path":       fmt.Sprintf("/ingest/concurrent-%d.json", index),
					"content":    "x",
				},
			})
			if err != nil {
				t.Errorf("ingest %d failed: %v", index, err)
				return
			}
			ids[index] = resp.ID
		}(index)
	}
	wg.Wait()

	seen := map[string]int{}
	for _, id := range ids {
		seen[id]++
	}
	if len(seen) != count {
		t.Fatalf("expected %d distinct envelope IDs, got %d", count, len(seen))
	}
	store.mu.RLock()
	stored := len(store.envelopesByID)
	store.mu.RUnlock()
	if stored != count {
		t.Fatalf("expected %d stored envelopes, got %d", count, stored)
	}
}
