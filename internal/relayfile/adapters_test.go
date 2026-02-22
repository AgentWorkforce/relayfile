package relayfile

import "testing"

func TestParseGenericEnvelopeFileUpdated(t *testing.T) {
	actions, err := ParseGenericEnvelope(WebhookEnvelopeRequest{
		Provider: "custom-system",
		Payload: map[string]any{
			"path":               "/data/record_123.json",
			"event_type":         "file.updated",
			"content":            `{"id": 123}`,
			"contentType":        "application/json",
			"providerObjectId":   "obj_123",
			"properties": map[string]any{
				"system": "crm",
			},
		},
	})
	if err != nil {
		t.Fatalf("parse envelope failed: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected one action, got %d", len(actions))
	}
	action := actions[0]
	if action.Type != ActionFileUpsert {
		t.Fatalf("expected ActionFileUpsert, got %v", action.Type)
	}
	if action.Path != "/data/record_123.json" {
		t.Fatalf("expected path /data/record_123.json, got %s", action.Path)
	}
	if action.Content != `{"id": 123}` {
		t.Fatalf("expected content preserved, got %s", action.Content)
	}
	if action.ContentType != "application/json" {
		t.Fatalf("expected application/json, got %s", action.ContentType)
	}
	if action.ProviderObjectID != "obj_123" {
		t.Fatalf("expected provider object id obj_123, got %s", action.ProviderObjectID)
	}
	if action.Semantics.Properties["system"] != "crm" {
		t.Fatalf("expected system property in semantics, got %+v", action.Semantics.Properties)
	}
}

func TestParseGenericEnvelopeFileDeleted(t *testing.T) {
	actions, err := ParseGenericEnvelope(WebhookEnvelopeRequest{
		Provider: "salesforce",
		Payload: map[string]any{
			"path":             "/salesforce/Account_123",
			"event_type":       "file.deleted",
			"providerObjectId": "sf_acc_123",
		},
	})
	if err != nil {
		t.Fatalf("parse envelope failed: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected one action, got %d", len(actions))
	}
	action := actions[0]
	if action.Type != ActionFileDelete {
		t.Fatalf("expected ActionFileDelete, got %v", action.Type)
	}
	if action.Path != "/salesforce/Account_123" {
		t.Fatalf("expected path /salesforce/Account_123, got %s", action.Path)
	}
	if action.ProviderObjectID != "sf_acc_123" {
		t.Fatalf("expected provider object id sf_acc_123, got %s", action.ProviderObjectID)
	}
}

func TestParseGenericEnvelopeFileCreated(t *testing.T) {
	actions, err := ParseGenericEnvelope(WebhookEnvelopeRequest{
		Provider: "custom",
		Payload: map[string]any{
			"path":        "/files/new_document.md",
			"event_type":  "file.created",
			"content":     "# New Document",
			"contentType": "text/markdown",
		},
	})
	if err != nil {
		t.Fatalf("parse envelope failed: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected one action, got %d", len(actions))
	}
	action := actions[0]
	if action.Type != ActionFileUpsert {
		t.Fatalf("expected ActionFileUpsert for created, got %v", action.Type)
	}
	if action.Content != "# New Document" {
		t.Fatalf("expected content preserved, got %s", action.Content)
	}
}

func TestParseGenericEnvelopeUnknownEventType(t *testing.T) {
	actions, err := ParseGenericEnvelope(WebhookEnvelopeRequest{
		Provider: "unknown",
		Payload: map[string]any{
			"path":       "/unknown/event",
			"event_type": "custom.unknown",
		},
	})
	if err != nil {
		t.Fatalf("parse envelope failed: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected one action, got %d", len(actions))
	}
	action := actions[0]
	if action.Type != ActionIgnored {
		t.Fatalf("expected ActionIgnored for unknown event type, got %v", action.Type)
	}
}
