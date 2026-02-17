package relayfile

import (
	"context"
	"testing"
	"time"
)

type fakeNotionWriteClient struct {
	upserts chan NotionUpsertRequest
	deletes chan NotionDeleteRequest
}

func (f *fakeNotionWriteClient) UpsertPage(ctx context.Context, req NotionUpsertRequest) error {
	if f.upserts != nil {
		f.upserts <- req
	}
	return nil
}

func (f *fakeNotionWriteClient) DeletePage(ctx context.Context, req NotionDeleteRequest) error {
	if f.deletes != nil {
		f.deletes <- req
	}
	return nil
}

func TestNotionAdapterApplyWritebackUpsertMapsPayload(t *testing.T) {
	client := &fakeNotionWriteClient{upserts: make(chan NotionUpsertRequest, 1)}
	adapter := NewNotionAdapter(client)

	err := adapter.ApplyWriteback(WritebackAction{
		WorkspaceID:      "ws_notion_1",
		Path:             "/notion/Doc.md",
		Revision:         "rev_1",
		Type:             WritebackActionFileUpsert,
		ContentType:      "text/markdown",
		Content:          "# hello",
		ProviderObjectID: "notion_obj_1",
		CorrelationID:    "corr_notion_1",
	})
	if err != nil {
		t.Fatalf("apply writeback failed: %v", err)
	}

	select {
	case req := <-client.upserts:
		if req.WorkspaceID != "ws_notion_1" || req.Path != "/notion/Doc.md" || req.Content != "# hello" {
			t.Fatalf("unexpected upsert mapping: %+v", req)
		}
		if req.ProviderObjectID != "notion_obj_1" {
			t.Fatalf("expected provider object id, got %s", req.ProviderObjectID)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected upsert call")
	}
}

func TestNotionAdapterApplyWritebackDeleteMapsPayload(t *testing.T) {
	client := &fakeNotionWriteClient{deletes: make(chan NotionDeleteRequest, 1)}
	adapter := NewNotionAdapter(client)

	err := adapter.ApplyWriteback(WritebackAction{
		WorkspaceID:      "ws_notion_2",
		Path:             "/notion/Delete.md",
		Revision:         "rev_2",
		Type:             WritebackActionFileDelete,
		ProviderObjectID: "notion_obj_2",
		CorrelationID:    "corr_notion_2",
	})
	if err != nil {
		t.Fatalf("apply writeback failed: %v", err)
	}

	select {
	case req := <-client.deletes:
		if req.WorkspaceID != "ws_notion_2" || req.Path != "/notion/Delete.md" {
			t.Fatalf("unexpected delete mapping: %+v", req)
		}
		if req.ProviderObjectID != "notion_obj_2" {
			t.Fatalf("expected provider object id, got %s", req.ProviderObjectID)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected delete call")
	}
}

func TestNotionAdapterApplyWritebackRejectsUnsupportedAction(t *testing.T) {
	adapter := NewNotionAdapter(nil)
	err := adapter.ApplyWriteback(WritebackAction{
		WorkspaceID: "ws_notion_3",
		Path:        "/notion/X.md",
		Type:        WritebackActionType("unknown"),
	})
	if err == nil {
		t.Fatalf("expected error for unsupported action")
	}
}

func TestStoreUsesNotionAdapterWritebackIntegration(t *testing.T) {
	client := &fakeNotionWriteClient{upserts: make(chan NotionUpsertRequest, 2)}
	store := NewStoreWithOptions(StoreOptions{
		Adapters: []ProviderAdapter{
			NewNotionAdapter(client),
		},
	})
	t.Cleanup(store.Close)

	_, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_notion_store",
		Path:          "/notion/FromStore.md",
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       "# from store",
		CorrelationID: "corr_notion_store_1",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}

	select {
	case req := <-client.upserts:
		if req.WorkspaceID != "ws_notion_store" || req.Path != "/notion/FromStore.md" {
			t.Fatalf("unexpected writeback request: %+v", req)
		}
		if req.Content != "# from store" {
			t.Fatalf("unexpected writeback content: %q", req.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected store writeback to reach notion adapter")
	}
}

