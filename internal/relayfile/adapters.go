package relayfile

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type ActionType string

const (
	ActionFileUpsert ActionType = "file_upsert"
	ActionFileDelete ActionType = "file_delete"
	ActionIgnored    ActionType = "ignored"
)

type ApplyAction struct {
	Type             ActionType
	Path             string
	Content          string
	ContentType      string
	ProviderObjectID string
	Semantics        FileSemantics
}

type ProviderAdapter interface {
	Provider() string
	ParseEnvelope(req WebhookEnvelopeRequest) ([]ApplyAction, error)
}

type ProviderWritebackAdapter interface {
	ApplyWriteback(action WritebackAction) error
}

// ParseGenericEnvelope processes a generic webhook envelope for any provider.
// This is used when no provider-specific adapter is registered.
// It expects the envelope to already be in the generic format from the ingest API.
func ParseGenericEnvelope(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
	// Extract event type from the generic envelope
	eventType := toString(req.Payload["event_type"])
	if eventType == "" {
		// Fallback: try to infer from standard field
		if typeField, ok := req.Payload["type"].(string); ok {
			eventType = typeField
		}
	}

	// The generic envelope should have path and data at the top level
	// If coming from the generic ingest API, the entire req.Payload is the data
	path := toString(req.Payload["path"])
	if path == "" {
		// Path might not be in Payload if sent through generic ingest API
		// In that case, it should be in req itself (handled by caller)
		return []ApplyAction{{Type: ActionIgnored}}, nil
	}

	switch {
	case eventType == "file.created" || eventType == "file.updated":
		content := toString(req.Payload["content"])
		contentType := toString(req.Payload["contentType"])
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		return []ApplyAction{
			{
				Type:             ActionFileUpsert,
				Path:             normalizePath(path),
				Content:          content,
				ContentType:      contentType,
				ProviderObjectID: toString(req.Payload["providerObjectId"]),
				Semantics:        extractSemantics(req.Payload),
			},
		}, nil

	case eventType == "file.deleted":
		return []ApplyAction{
			{
				Type:             ActionFileDelete,
				Path:             normalizePath(path),
				ProviderObjectID: toString(req.Payload["providerObjectId"]),
			},
		}, nil

	default:
		// Unknown event type - ignore
		return []ApplyAction{{Type: ActionIgnored}}, nil
	}
}

func extractSemantics(payload map[string]any) FileSemantics {
	if len(payload) == 0 {
		return FileSemantics{}
	}
	var semanticBlock map[string]any
	if rawBlock, ok := payload["semantics"].(map[string]any); ok {
		semanticBlock = rawBlock
	}
	selectField := func(name string) any {
		if semanticBlock != nil {
			if value, ok := semanticBlock[name]; ok {
				return value
			}
		}
		return payload[name]
	}
	semantics := FileSemantics{
		Properties:  asStringMap(selectField("properties")),
		Relations:   asStringSlice(selectField("relations")),
		Permissions: asStringSlice(selectField("permissions")),
		Comments:    asStringSlice(selectField("comments")),
	}
	if len(semantics.Relations) == 0 {
		semantics.Relations = asStringSlice(selectField("relationIds"))
	}
	if len(semantics.Permissions) == 0 {
		semantics.Permissions = asStringSlice(selectField("acl"))
	}
	if len(semantics.Comments) == 0 {
		semantics.Comments = asStringSlice(selectField("commentIds"))
	}
	return normalizeSemantics(semantics)
}

func asStringMap(value any) map[string]string {
	var raw map[string]any
	switch typed := value.(type) {
	case map[string]any:
		raw = typed
	case map[string]string:
		if len(typed) == 0 {
			return nil
		}
		raw = make(map[string]any, len(typed))
		for k, v := range typed {
			raw[k] = v
		}
	default:
		return nil
	}
	if len(raw) == 0 {
		return nil
	}
	out := map[string]string{}
	for key, rawValue := range raw {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			continue
		}
		switch typed := rawValue.(type) {
		case string:
			out[trimmed] = strings.TrimSpace(typed)
		case fmt.Stringer:
			out[trimmed] = strings.TrimSpace(typed.String())
		case bool, int, int32, int64, float32, float64:
			out[trimmed] = strings.TrimSpace(fmt.Sprintf("%v", typed))
		case nil:
			out[trimmed] = ""
		default:
			encoded, err := json.Marshal(typed)
			if err != nil {
				out[trimmed] = strings.TrimSpace(fmt.Sprintf("%v", typed))
			} else {
				out[trimmed] = strings.TrimSpace(string(encoded))
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func asStringSlice(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil
		}
		return []string{trimmed}
	case []any:
		out := make([]string, 0, len(typed))
		for _, raw := range typed {
			switch v := raw.(type) {
			case string:
				trimmed := strings.TrimSpace(v)
				if trimmed != "" {
					out = append(out, trimmed)
				}
			case fmt.Stringer:
				trimmed := strings.TrimSpace(v.String())
				if trimmed != "" {
					out = append(out, trimmed)
				}
			default:
				trimmed := strings.TrimSpace(fmt.Sprintf("%v", raw))
				if trimmed != "" && trimmed != "<nil>" {
					out = append(out, trimmed)
				}
			}
		}
		return dedupeAndSort(out)
	case []string:
		return dedupeAndSort(typed)
	default:
		trimmed := strings.TrimSpace(fmt.Sprintf("%v", value))
		if trimmed == "" || trimmed == "<nil>" {
			return nil
		}
		return []string{trimmed}
	}
}

func dedupeAndSort(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}
