from __future__ import annotations

import time
from dataclasses import dataclass, field
from threading import Event
from typing import Any

from .provider import (
    IntegrationProvider,
    ListProviderFilesOptions,
    WatchProviderEventsOptions,
    WebhookInput,
    compute_canonical_path,
)
from .types import IngestWebhookInput


@dataclass
class ComposioWebhookMetadata:
    trigger_slug: str
    trigger_id: str
    connected_account_id: str
    toolkit: str
    user_id: str | None = None


@dataclass
class ComposioWebhookPayload:
    type: str
    metadata: ComposioWebhookMetadata
    data: dict[str, Any] = field(default_factory=dict)


@dataclass
class ComposioTriggerOptions:
    api_key: str
    webhook_url: str
    base_url: str = "https://backend.composio.dev"


TRIGGER_OBJECT_TYPE: dict[str, str] = {
    "GITHUB_COMMIT_EVENT": "commits",
    "GITHUB_PULL_REQUEST_EVENT": "pull_requests",
    "GITHUB_ISSUE_EVENT": "issues",
    "GITHUB_STAR_EVENT": "stars",
    "GITHUB_PUSH_EVENT": "pushes",
    "SLACK_NEW_MESSAGE": "messages",
    "SLACK_CHANNEL_CREATED": "channels",
    "SLACK_REACTION_ADDED": "reactions",
    "GMAIL_NEW_EMAIL": "emails",
    "ZENDESK_NEW_TICKET": "tickets",
    "ZENDESK_TICKET_UPDATED": "tickets",
    "SHOPIFY_NEW_ORDER": "orders",
    "SHOPIFY_ORDER_UPDATED": "orders",
    "STRIPE_PAYMENT_RECEIVED": "payments",
    "STRIPE_INVOICE_CREATED": "invoices",
    "JIRA_ISSUE_CREATED": "issues",
    "JIRA_ISSUE_UPDATED": "issues",
    "HUBSPOT_CONTACT_CREATED": "contacts",
    "HUBSPOT_DEAL_CREATED": "deals",
    "NOTION_PAGE_UPDATED": "pages",
    "LINEAR_ISSUE_CREATED": "issues",
    "LINEAR_ISSUE_UPDATED": "issues",
    "INTERCOM_NEW_CONVERSATION": "conversations",
    "FRESHDESK_TICKET_CREATED": "tickets",
    "FRESHDESK_TICKET_UPDATED": "tickets",
}


def _coerce_metadata(
    raw_metadata: ComposioWebhookMetadata | dict[str, Any],
) -> ComposioWebhookMetadata:
    if isinstance(raw_metadata, ComposioWebhookMetadata):
        return raw_metadata

    return ComposioWebhookMetadata(
        trigger_slug=str(raw_metadata.get("trigger_slug", "")),
        trigger_id=str(raw_metadata.get("trigger_id", "")),
        connected_account_id=str(raw_metadata["connected_account_id"]),
        user_id=(
            str(raw_metadata["user_id"])
            if raw_metadata.get("user_id") is not None
            else None
        ),
        toolkit=str(raw_metadata.get("toolkit", "")),
    )


def _coerce_payload(
    raw_input: ComposioWebhookPayload | dict[str, Any],
) -> ComposioWebhookPayload:
    if isinstance(raw_input, ComposioWebhookPayload):
        return raw_input

    return ComposioWebhookPayload(
        type=str(raw_input["type"]),
        metadata=_coerce_metadata(raw_input["metadata"]),
        data=dict(raw_input.get("data") or {}),
    )


def _extract_event_type(trigger_slug: str) -> str:
    slug = trigger_slug.upper()
    if "CREATED" in slug or "NEW" in slug:
        return "created"
    if "UPDATED" in slug or "CHANGED" in slug:
        return "updated"
    if "DELETED" in slug or "REMOVED" in slug:
        return "deleted"
    return "event"


def _extract_object_id(data: dict[str, Any]) -> str:
    for field in (
        "id",
        "objectId",
        "object_id",
        "ticketId",
        "issueId",
        "messageId",
        "orderId",
        "commitId",
    ):
        value = data.get(field)
        if isinstance(value, (str, int)):
            return str(value)

    for field in ("issue", "pull_request"):
        nested = data.get(field)
        if isinstance(nested, dict) and "id" in nested:
            return str(nested["id"])

    return f"auto-{int(time.time() * 1000):x}"


def _infer_object_type(trigger_slug: str) -> str:
    parts = trigger_slug.lower().split("_")
    if len(parts) >= 3:
        object_part = "_".join(parts[1:-1])
        return object_part if object_part.endswith("s") else f"{object_part}s"
    return "events"


class ComposioProvider(IntegrationProvider):
    @property
    def name(self) -> str:
        return "composio"

    def ingest_webhook(
        self,
        workspace_id: str,
        raw_input: ComposioWebhookPayload | dict[str, Any],
    ) -> dict[str, Any]:
        input = _coerce_payload(raw_input)

        if input.type == "composio.connected_account.expired":
            return self.client.ingest_webhook(
                IngestWebhookInput(
                    workspace_id=workspace_id,
                    provider=input.metadata.toolkit or "composio",
                    event_type="account_expired",
                    path=(
                        "/.system/composio/expired/"
                        f"{input.metadata.connected_account_id}.json"
                    ),
                    data={
                        "connected_account_id": input.metadata.connected_account_id,
                        "user_id": input.metadata.user_id,
                        "toolkit": input.metadata.toolkit,
                        "expired_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                    },
                    headers={},
                )
            )

        toolkit = input.metadata.toolkit
        trigger_slug = input.metadata.trigger_slug
        object_type = TRIGGER_OBJECT_TYPE.get(trigger_slug, _infer_object_type(trigger_slug))
        object_id = _extract_object_id(input.data)
        event_type = _extract_event_type(trigger_slug)
        path = compute_canonical_path(toolkit, object_type, object_id)
        properties = self._build_semantic_properties(input, object_type, object_id)

        headers = {
            "X-Composio-Trigger-Id": input.metadata.trigger_id,
            "X-Composio-Trigger-Slug": trigger_slug,
            "X-Composio-Connected-Account": input.metadata.connected_account_id,
        }
        if input.metadata.user_id:
            headers["X-Composio-User-Id"] = input.metadata.user_id

        return self.client.ingest_webhook(
            IngestWebhookInput(
                workspace_id=workspace_id,
                provider=toolkit,
                event_type=event_type,
                path=path,
                data={
                    **input.data,
                    "semantics": {
                        "properties": properties,
                        "relations": [],
                    },
                },
                headers=headers,
            )
        )

    def normalize(
        self,
        input: ComposioWebhookPayload | dict[str, Any],
    ) -> WebhookInput:
        payload = _coerce_payload(input)
        object_type = TRIGGER_OBJECT_TYPE.get(
            payload.metadata.trigger_slug,
            _infer_object_type(payload.metadata.trigger_slug),
        )
        metadata = {
            "composio.trigger_id": payload.metadata.trigger_id,
            "composio.trigger_slug": payload.metadata.trigger_slug,
            "composio.connected_account_id": payload.metadata.connected_account_id,
        }
        if payload.metadata.user_id:
            metadata["composio.user_id"] = payload.metadata.user_id

        return WebhookInput(
            provider=payload.metadata.toolkit,
            object_type=object_type,
            object_id=_extract_object_id(payload.data),
            event_type=_extract_event_type(payload.metadata.trigger_slug),
            payload=payload.data,
            metadata=metadata,
        )

    def get_provider_files(
        self,
        workspace_id: str,
        *,
        provider: str,
        object_type: str | None = None,
        status: str | None = None,
        limit: int | None = None,
    ) -> list[dict[str, Any]]:
        return super().get_provider_files(
            workspace_id,
            ListProviderFilesOptions(
                provider=provider,
                object_type=object_type,
                status=status,
                limit=limit,
            ),
        )

    def watch_provider_events(
        self,
        workspace_id: str,
        *,
        provider: str,
        poll_interval_seconds: float = 5.0,
        cursor: str | None = None,
        stop_event: Event | None = None,
    ):
        return super().watch_provider_events(
            workspace_id,
            WatchProviderEventsOptions(
                provider=provider,
                poll_interval_seconds=poll_interval_seconds,
                cursor=cursor,
                stop_event=stop_event,
            ),
        )

    def _build_semantic_properties(
        self,
        input: ComposioWebhookPayload,
        object_type: str,
        object_id: str,
    ) -> dict[str, str]:
        properties: dict[str, str] = {
            "composio.trigger_id": input.metadata.trigger_id,
            "composio.trigger_slug": input.metadata.trigger_slug,
            "composio.connected_account_id": input.metadata.connected_account_id,
            "provider": input.metadata.toolkit,
            "provider.object_type": object_type,
            "provider.object_id": object_id,
        }
        if input.metadata.user_id:
            properties["composio.user_id"] = input.metadata.user_id
        if isinstance(input.data.get("status"), str):
            properties["provider.status"] = input.data["status"]
        if isinstance(input.data.get("updated_at"), str):
            properties["provider.updated_at"] = input.data["updated_at"]
        if isinstance(input.data.get("created_at"), str):
            properties["provider.created_at"] = input.data["created_at"]
        return properties


ComposioHelpers = ComposioProvider
