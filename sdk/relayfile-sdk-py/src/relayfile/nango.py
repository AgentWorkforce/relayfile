from __future__ import annotations

import asyncio
from typing import Any, AsyncIterator

from .client import AsyncRelayFileClient, RelayFileClient
from .types import IngestWebhookInput


PROVIDER_PATH_PREFIX: dict[str, str] = {
    "zendesk": "/zendesk",
    "shopify": "/shopify",
    "github": "/github",
    "stripe": "/stripe",
    "slack": "/slack",
    "linear": "/linear",
    "jira": "/jira",
    "hubspot": "/hubspot",
    "salesforce": "/salesforce",
}


def _compute_canonical_path(provider: str, model: str, object_id: str) -> str:
    prefix = PROVIDER_PATH_PREFIX.get(provider, f"/{provider}")
    return f"{prefix}/{model}/{object_id}.json"


def _build_semantic_properties(
    provider: str,
    connection_id: str,
    integration_id: str,
    model: str,
    object_id: str,
    provider_config_key: str | None,
    payload: dict[str, Any],
) -> dict[str, str]:
    props: dict[str, str] = {
        "nango.connection_id": connection_id,
        "nango.integration_id": integration_id,
        "provider": provider,
        "provider.object_type": model,
        "provider.object_id": object_id,
    }
    if provider_config_key:
        props["nango.provider_config_key"] = provider_config_key
    if isinstance(payload.get("status"), str):
        props["provider.status"] = payload["status"]
    if isinstance(payload.get("updated_at"), str):
        props["provider.updated_at"] = payload["updated_at"]
    if isinstance(payload.get("created_at"), str):
        props["provider.created_at"] = payload["created_at"]
    return props


def _build_ingest_input(
    workspace_id: str,
    *,
    connection_id: str,
    integration_id: str,
    provider_config_key: str | None,
    model: str,
    object_id: str,
    event_type: str,
    payload: dict[str, Any],
    relations: list[str] | None,
) -> tuple[str, IngestWebhookInput]:
    provider = integration_id.split("-")[0] or integration_id
    path = _compute_canonical_path(provider, model, object_id)
    properties = _build_semantic_properties(
        provider, connection_id, integration_id, model, object_id,
        provider_config_key, payload,
    )
    headers: dict[str, str] = {
        "X-Nango-Connection-Id": connection_id,
        "X-Nango-Integration-Id": integration_id,
    }
    if provider_config_key:
        headers["X-Nango-Provider-Config-Key"] = provider_config_key

    data: dict[str, Any] = {
        **payload,
        "semantics": {
            "properties": properties,
            "relations": relations or [],
        },
    }
    return provider, IngestWebhookInput(
        workspace_id=workspace_id,
        provider=provider,
        event_type=event_type,
        path=path,
        data=data,
        headers=headers,
    )


class NangoHelpers:
    """Synchronous Nango bridge helpers."""

    def __init__(self, client: RelayFileClient) -> None:
        self._client = client

    def ingest_nango_webhook(
        self,
        workspace_id: str,
        *,
        connection_id: str,
        integration_id: str,
        provider_config_key: str | None = None,
        model: str,
        object_id: str,
        event_type: str,
        payload: dict[str, Any],
        relations: list[str] | None = None,
    ) -> dict[str, Any]:
        _, inp = _build_ingest_input(
            workspace_id,
            connection_id=connection_id,
            integration_id=integration_id,
            provider_config_key=provider_config_key,
            model=model,
            object_id=object_id,
            event_type=event_type,
            payload=payload,
            relations=relations,
        )
        return self._client.ingest_webhook(inp)

    def get_provider_files(
        self,
        workspace_id: str,
        *,
        provider: str,
        object_type: str | None = None,
        status: str | None = None,
        limit: int | None = None,
    ) -> list[dict[str, Any]]:
        prefix = PROVIDER_PATH_PREFIX.get(provider, f"/{provider}")
        path_filter = f"{prefix}/{object_type}/" if object_type else f"{prefix}/"
        properties: dict[str, str] = {"provider": provider}
        if object_type:
            properties["provider.object_type"] = object_type
        if status:
            properties["provider.status"] = status

        all_items: list[dict[str, Any]] = []
        cursor: str | None = None
        while True:
            result = self._client.query_files(
                workspace_id,
                path=path_filter,
                provider=provider,
                properties=properties,
                cursor=cursor,
                limit=limit,
            )
            all_items.extend(result.get("items", []))
            cursor = result.get("nextCursor")
            if not cursor or (limit and len(all_items) >= limit):
                break
        return all_items[:limit] if limit else all_items


class AsyncNangoHelpers:
    """Async Nango bridge helpers."""

    def __init__(self, client: AsyncRelayFileClient) -> None:
        self._client = client

    async def ingest_nango_webhook(
        self,
        workspace_id: str,
        *,
        connection_id: str,
        integration_id: str,
        provider_config_key: str | None = None,
        model: str,
        object_id: str,
        event_type: str,
        payload: dict[str, Any],
        relations: list[str] | None = None,
    ) -> dict[str, Any]:
        _, inp = _build_ingest_input(
            workspace_id,
            connection_id=connection_id,
            integration_id=integration_id,
            provider_config_key=provider_config_key,
            model=model,
            object_id=object_id,
            event_type=event_type,
            payload=payload,
            relations=relations,
        )
        return await self._client.ingest_webhook(inp)

    async def get_provider_files(
        self,
        workspace_id: str,
        *,
        provider: str,
        object_type: str | None = None,
        status: str | None = None,
        limit: int | None = None,
    ) -> list[dict[str, Any]]:
        prefix = PROVIDER_PATH_PREFIX.get(provider, f"/{provider}")
        path_filter = f"{prefix}/{object_type}/" if object_type else f"{prefix}/"
        properties: dict[str, str] = {"provider": provider}
        if object_type:
            properties["provider.object_type"] = object_type
        if status:
            properties["provider.status"] = status

        all_items: list[dict[str, Any]] = []
        cursor: str | None = None
        while True:
            result = await self._client.query_files(
                workspace_id,
                path=path_filter,
                provider=provider,
                properties=properties,
                cursor=cursor,
                limit=limit,
            )
            all_items.extend(result.get("items", []))
            cursor = result.get("nextCursor")
            if not cursor or (limit and len(all_items) >= limit):
                break
        return all_items[:limit] if limit else all_items

    async def watch_provider_events(
        self,
        workspace_id: str,
        *,
        provider: str,
        poll_interval_seconds: float = 5.0,
        cursor: str | None = None,
    ) -> AsyncIterator[dict[str, Any]]:
        """Poll for events from a specific provider. Yields event dicts.

        Cancel by breaking out of the async for loop or cancelling the task.
        """
        while True:
            response = await self._client.get_events(
                workspace_id,
                provider=provider,
                cursor=cursor,
            )
            for event in response.get("events", []):
                yield event
            next_cursor = response.get("nextCursor")
            if next_cursor:
                cursor = next_cursor
            await asyncio.sleep(poll_interval_seconds)
