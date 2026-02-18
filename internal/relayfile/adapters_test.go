package relayfile

import "testing"

func TestNotionAdapterParseEnvelopeExtractsSemantics(t *testing.T) {
	adapter := NewNotionAdapter(nil)
	actions, err := adapter.ParseEnvelope(WebhookEnvelopeRequest{
		Provider: "notion",
		Payload: map[string]any{
			"type":     "notion.page.upsert",
			"path":     "/notion/Investments/Seed.md",
			"objectId": "obj_1",
			"content":  "# seed",
			"properties": map[string]any{
				"topic": "investments",
				"stage": "seed",
			},
			"relations":   []any{"db_investments"},
			"permissions": []any{"role:finance"},
			"comments":    []any{"comment_a"},
		},
	})
	if err != nil {
		t.Fatalf("parse envelope failed: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected one action, got %d", len(actions))
	}
	action := actions[0]
	if action.Semantics.Properties["topic"] != "investments" {
		t.Fatalf("expected topic property, got %+v", action.Semantics.Properties)
	}
	if len(action.Semantics.Relations) != 1 || action.Semantics.Relations[0] != "db_investments" {
		t.Fatalf("expected relations to be extracted, got %+v", action.Semantics.Relations)
	}
	if len(action.Semantics.Permissions) != 1 || action.Semantics.Permissions[0] != "role:finance" {
		t.Fatalf("expected permissions to be extracted, got %+v", action.Semantics.Permissions)
	}
	if len(action.Semantics.Comments) != 1 || action.Semantics.Comments[0] != "comment_a" {
		t.Fatalf("expected comments to be extracted, got %+v", action.Semantics.Comments)
	}
}

func TestNotionAdapterParseEnvelopeSupportsNestedSemanticsBlock(t *testing.T) {
	adapter := NewNotionAdapter(nil)
	actions, err := adapter.ParseEnvelope(WebhookEnvelopeRequest{
		Provider: "notion",
		Payload: map[string]any{
			"type":     "notion.page.upsert",
			"path":     "/notion/Investments/Seed.md",
			"objectId": "obj_2",
			"content":  "# seed",
			"semantics": map[string]any{
				"properties": map[string]any{
					"topic": "investments",
				},
				"relationIds": []any{"db_investments"},
				"acl":         []any{"role:finance"},
				"commentIds":  []any{"comment_b"},
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
	if action.Semantics.Properties["topic"] != "investments" {
		t.Fatalf("expected nested topic property, got %+v", action.Semantics.Properties)
	}
	if len(action.Semantics.Relations) != 1 || action.Semantics.Relations[0] != "db_investments" {
		t.Fatalf("expected nested relationIds to map to relations, got %+v", action.Semantics.Relations)
	}
	if len(action.Semantics.Permissions) != 1 || action.Semantics.Permissions[0] != "role:finance" {
		t.Fatalf("expected nested acl to map to permissions, got %+v", action.Semantics.Permissions)
	}
	if len(action.Semantics.Comments) != 1 || action.Semantics.Comments[0] != "comment_b" {
		t.Fatalf("expected nested commentIds to map to comments, got %+v", action.Semantics.Comments)
	}
}
