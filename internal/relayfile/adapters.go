package relayfile

import (
	"context"
	"fmt"
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
