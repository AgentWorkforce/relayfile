from __future__ import annotations

import asyncio
from collections.abc import AsyncIterator, Iterator
from dataclasses import dataclass
from threading import Event
from typing import Any

from .client import AsyncRelayFileClient, RelayFileClient
from .provider import (
    DEFAULT_PATH_PREFIXES,
    IntegrationProvider,
    ListProviderFilesOptions,
    WatchProviderEventsOptions,
    WebhookInput,
    compute_canonical_path,
)
from .types import IngestWebhookInput


@dataclass
class NangoWebhookInput:
    connection_id: str
    integration_id: str
    model: str
    object_id: str
    event_type: str
    payload: dict[str, Any]
    provider_config_key: str | None = None
    relations: list[str] | None = None


def _coerce_webhook_input(raw_input: NangoWebhookInput | dict[str, Any]) -> NangoWebhookInput:
    if isinstance(raw_input, NangoWebhookInput):
        return raw_input

    return NangoWebhookInput(
        connection_id=str(raw_input["connection_id"]),
        integration_id=str(raw_input["integration_id"]),
        provider_config_key=(
            str(raw_input["provider_config_key"])
            if raw_input.get("provider_config_key") is not None
            else None
        ),
        model=str(raw_input["model"]),
        object_id=str(raw_input["object_id"]),
        event_type=str(raw_input["event_type"]),
        payload=dict(raw_input.get("payload") or {}),
        relations=list(raw_input["relations"]) if raw_input.get("relations") else None,
    )


def _provider_for_integration(integration_id: str) -> str:
    return integration_id.split("-")[0] or integration_id


def _build_semantic_properties(
    provider: str,
    input: NangoWebhookInput,
) -> dict[str, str]:
    props: dict[str, str] = {
        "nango.connection_id": input.connection_id,
        "nango.integration_id": input.integration_id,
        "provider": provider,
        "provider.object_type": input.model,
        "provider.object_id": input.object_id,
    }
    if input.provider_config_key:
        props["nango.provider_config_key"] = input.provider_config_key
    if isinstance(input.payload.get("status"), str):
        props["provider.status"] = input.payload["status"]
    if isinstance(input.payload.get("updated_at"), str):
        props["provider.updated_at"] = input.payload["updated_at"]
    if isinstance(input.payload.get("created_at"), str):
        props["provider.created_at"] = input.payload["created_at"]
    return props


def _build_ingest_input(
    workspace_id: str,
    input: NangoWebhookInput,
) -> tuple[str, IngestWebhookInput]:
    provider = _provider_for_integration(input.integration_id)
    path = compute_canonical_path(provider, input.model, input.object_id)
    properties = _build_semantic_properties(provider, input)
    headers: dict[str, str] = {
        "X-Nango-Connection-Id": input.connection_id,
        "X-Nango-Integration-Id": input.integration_id,
    }
    if input.provider_config_key:
        headers["X-Nango-Provider-Config-Key"] = input.provider_config_key

    data: dict[str, Any] = {
        **input.payload,
        "semantics": {
            "properties": properties,
            "relations": input.relations or [],
        },
    }
    return provider, IngestWebhookInput(
        workspace_id=workspace_id,
        provider=provider,
        event_type=input.event_type,
        path=path,
        data=data,
        headers=headers,
    )


class NangoHelpers(IntegrationProvider):
    """Synchronous Nango bridge helpers."""

    @property
    def name(self) -> str:
        return "nango"

    def __init__(self, client: RelayFileClient) -> None:
        super().__init__(client)

    def ingest_webhook(
        self,
        workspace_id: str,
        raw_input: NangoWebhookInput | dict[str, Any],
    ) -> dict[str, Any]:
        input = _coerce_webhook_input(raw_input)
        _, ingest_input = _build_ingest_input(workspace_id, input)
        return self.client.ingest_webhook(ingest_input)

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
        return self.ingest_webhook(
            workspace_id,
            NangoWebhookInput(
                connection_id=connection_id,
                integration_id=integration_id,
                provider_config_key=provider_config_key,
                model=model,
                object_id=object_id,
                event_type=event_type,
                payload=payload,
                relations=relations,
            ),
        )

    def normalize(
        self,
        input: NangoWebhookInput | dict[str, Any],
    ) -> WebhookInput:
        webhook_input = _coerce_webhook_input(input)
        provider = _provider_for_integration(webhook_input.integration_id)
        metadata = {
            "nango.connection_id": webhook_input.connection_id,
            "nango.integration_id": webhook_input.integration_id,
        }
        if webhook_input.provider_config_key:
            metadata["nango.provider_config_key"] = webhook_input.provider_config_key
        return WebhookInput(
            provider=provider,
            object_type=webhook_input.model,
            object_id=webhook_input.object_id,
            event_type=webhook_input.event_type,
            payload=webhook_input.payload,
            relations=webhook_input.relations,
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
    ) -> Iterator[dict[str, Any]]:
        return super().watch_provider_events(
            workspace_id,
            WatchProviderEventsOptions(
                provider=provider,
                poll_interval_seconds=poll_interval_seconds,
                cursor=cursor,
                stop_event=stop_event,
            ),
        )


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
        input = NangoWebhookInput(
            connection_id=connection_id,
            integration_id=integration_id,
            provider_config_key=provider_config_key,
            model=model,
            object_id=object_id,
            event_type=event_type,
            payload=payload,
            relations=relations,
        )
        _, ingest_input = _build_ingest_input(workspace_id, input)
        return await self._client.ingest_webhook(ingest_input)

    async def get_provider_files(
        self,
        workspace_id: str,
        *,
        provider: str,
        object_type: str | None = None,
        status: str | None = None,
        limit: int | None = None,
    ) -> list[dict[str, Any]]:
        prefix = DEFAULT_PATH_PREFIXES.get(provider, f"/{provider}")
        path_filter = f"{prefix}/{object_type}/" if object_type else f"{prefix}/"
        properties: dict[str, str] = {"provider": provider}
        if object_type:
            properties["provider.object_type"] = object_type
        if status:
            properties["provider.status"] = status

        all_items: list[dict[str, Any]] = []
        cursor = None
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
