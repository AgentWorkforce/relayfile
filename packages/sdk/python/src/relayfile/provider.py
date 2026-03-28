from __future__ import annotations

from abc import ABC, abstractmethod
from collections.abc import Iterator
from dataclasses import dataclass
from threading import Event
from time import sleep
from typing import Any

from .client import RelayFileClient


DEFAULT_PATH_PREFIXES: dict[str, str] = {
    "zendesk": "/zendesk",
    "shopify": "/shopify",
    "github": "/github",
    "stripe": "/stripe",
    "slack": "/slack",
    "linear": "/linear",
    "jira": "/jira",
    "hubspot": "/hubspot",
    "salesforce": "/salesforce",
    "gmail": "/gmail",
    "notion": "/notion",
    "asana": "/asana",
    "trello": "/trello",
    "intercom": "/intercom",
    "freshdesk": "/freshdesk",
    "discord": "/discord",
    "twilio": "/twilio",
}


@dataclass
class WebhookInput:
    provider: str
    object_type: str
    object_id: str
    event_type: str
    payload: dict[str, Any]
    relations: list[str] | None = None
    metadata: dict[str, str] | None = None


@dataclass
class ListProviderFilesOptions:
    provider: str
    object_type: str | None = None
    status: str | None = None
    limit: int | None = None


@dataclass
class WatchProviderEventsOptions:
    provider: str
    poll_interval_seconds: float = 5.0
    cursor: str | None = None
    stop_event: Event | None = None


def compute_canonical_path(provider: str, object_type: str, object_id: str) -> str:
    prefix = DEFAULT_PATH_PREFIXES.get(provider, f"/{provider}")
    return f"{prefix}/{object_type}/{object_id}.json"


class IntegrationProvider(ABC):
    def __init__(self, client: RelayFileClient) -> None:
        self._client = client

    @property
    def client(self) -> RelayFileClient:
        return self._client

    @property
    @abstractmethod
    def name(self) -> str:
        raise NotImplementedError

    @abstractmethod
    def ingest_webhook(
        self,
        workspace_id: str,
        raw_input: Any,
    ) -> dict[str, Any]:
        raise NotImplementedError

    def get_provider_files(
        self,
        workspace_id: str,
        options: ListProviderFilesOptions,
    ) -> list[dict[str, Any]]:
        prefix = DEFAULT_PATH_PREFIXES.get(options.provider, f"/{options.provider}")
        path_filter = (
            f"{prefix}/{options.object_type}/"
            if options.object_type
            else f"{prefix}/"
        )

        properties: dict[str, str] = {"provider": options.provider}
        if options.object_type:
            properties["provider.object_type"] = options.object_type
        if options.status:
            properties["provider.status"] = options.status

        all_items: list[dict[str, Any]] = []
        cursor = None
        while True:
            result = self.client.query_files(
                workspace_id,
                path=path_filter,
                provider=options.provider,
                properties=properties,
                cursor=cursor,
                limit=options.limit,
            )
            all_items.extend(result.get("items", []))
            cursor = result.get("nextCursor")
            if not cursor or (options.limit and len(all_items) >= options.limit):
                break
        return all_items[: options.limit] if options.limit else all_items

    def watch_provider_events(
        self,
        workspace_id: str,
        options: WatchProviderEventsOptions,
    ) -> Iterator[dict[str, Any]]:
        cursor = options.cursor
        while True:
            if options.stop_event and options.stop_event.is_set():
                return

            response = self.client.get_events(
                workspace_id,
                provider=options.provider,
                cursor=cursor,
            )
            for event in response.get("events", []):
                yield event

            next_cursor = response.get("nextCursor")
            if next_cursor:
                cursor = next_cursor

            if options.stop_event:
                if options.stop_event.wait(options.poll_interval_seconds):
                    return
            else:
                sleep(options.poll_interval_seconds)
