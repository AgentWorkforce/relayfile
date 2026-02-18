package relayfile

import (
	"context"
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

type NotionUpsertRequest struct {
	WorkspaceID      string `json:"workspaceId"`
	Path             string `json:"path"`
	Revision         string `json:"revision"`
	ContentType      string `json:"contentType"`
	Content          string `json:"content"`
	ProviderObjectID string `json:"providerObjectId"`
	CorrelationID    string `json:"correlationId"`
}

type NotionDeleteRequest struct {
	WorkspaceID      string `json:"workspaceId"`
	Path             string `json:"path"`
	Revision         string `json:"revision"`
	ProviderObjectID string `json:"providerObjectId"`
	CorrelationID    string `json:"correlationId"`
}

type NotionWriteClient interface {
	UpsertPage(ctx context.Context, req NotionUpsertRequest) error
	DeletePage(ctx context.Context, req NotionDeleteRequest) error
}

type noopNotionWriteClient struct{}

func (noopNotionWriteClient) UpsertPage(ctx context.Context, req NotionUpsertRequest) error {
	return nil
}

func (noopNotionWriteClient) DeletePage(ctx context.Context, req NotionDeleteRequest) error {
	return nil
}

type NotionAdapter struct {
	writeClient NotionWriteClient
}

func NewNotionAdapter(writeClient NotionWriteClient) NotionAdapter {
	if writeClient == nil {
		writeClient = noopNotionWriteClient{}
	}
	return NotionAdapter{writeClient: writeClient}
}

func (NotionAdapter) Provider() string {
	return "notion"
}

func (NotionAdapter) ParseEnvelope(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
	eventType := toString(req.Payload["type"])
	switch eventType {
	case "notion.page.upsert":
		content := toString(req.Payload["content"])
		if content == "" {
			title := strings.TrimSpace(toString(req.Payload["title"]))
			if title != "" {
				content = "# " + title
			}
		}
		return []ApplyAction{
			{
				Type:             ActionFileUpsert,
				Path:             normalizePath(toString(req.Payload["path"])),
				Content:          content,
				ContentType:      toString(req.Payload["contentType"]),
				ProviderObjectID: toString(req.Payload["objectId"]),
				Semantics:        extractSemantics(req.Payload),
			},
		}, nil
	case "notion.page.deleted":
		return []ApplyAction{
			{
				Type:             ActionFileDelete,
				Path:             normalizePath(toString(req.Payload["path"])),
				ProviderObjectID: toString(req.Payload["objectId"]),
			},
		}, nil
	default:
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

func (a NotionAdapter) ApplyWriteback(action WritebackAction) error {
	writer := a.writeClient
	if writer == nil {
		writer = noopNotionWriteClient{}
	}
	switch action.Type {
	case WritebackActionFileUpsert:
		return writer.UpsertPage(context.Background(), NotionUpsertRequest{
			WorkspaceID:      action.WorkspaceID,
			Path:             action.Path,
			Revision:         action.Revision,
			ContentType:      action.ContentType,
			Content:          action.Content,
			ProviderObjectID: action.ProviderObjectID,
			CorrelationID:    action.CorrelationID,
		})
	case WritebackActionFileDelete:
		return writer.DeletePage(context.Background(), NotionDeleteRequest{
			WorkspaceID:      action.WorkspaceID,
			Path:             action.Path,
			Revision:         action.Revision,
			ProviderObjectID: action.ProviderObjectID,
			CorrelationID:    action.CorrelationID,
		})
	default:
		return fmt.Errorf("unsupported notion writeback action: %s", action.Type)
	}
}
