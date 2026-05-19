from __future__ import annotations

import re
from abc import ABC, abstractmethod
from collections.abc import Iterator
from datetime import date
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

DIGEST_PATHS: tuple[str, ...] = (
    "digests/yesterday.md",
    "digests/today.md",
    "digests/this-week.md",
    "digests/last-week.md",
)

# Pattern for the canonical date-stamped digest path. Mirrors
# IsDateStampedPath in internal/digest/date_stamped.go: literal "digests/"
# prefix, ISO-8601 YYYY-MM-DD date, ".md" suffix.
DATE_STAMPED_DIGEST_PATH_PATTERN = re.compile(r"^digests/(\d{4})-(\d{2})-(\d{2})\.md$")


def is_digest_path(path: str) -> bool:
    """Return True if path is a recognized v1 digest artifact.

    Matches the literal anchor paths in ``DIGEST_PATHS`` as well as the
    date-stamped closed-window form ``digests/YYYY-MM-DD.md`` with a valid
    calendar date. Mirrors ``IsDigestPath`` in
    ``internal/digest/date_stamped.go``: leading and trailing slashes are
    stripped before matching.
    """

    normalized = path.strip().lstrip("/").rstrip("/")
    if normalized in DIGEST_PATHS:
        return True
    match = DATE_STAMPED_DIGEST_PATH_PATTERN.match(normalized)
    if match is None:
        return False
    year, month, day = (int(g) for g in match.groups())
    try:
        date(year, month, day)
    except ValueError:
        return False
    return True


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


def _norm_segment(value: str, *, name: str) -> str:
    trimmed = value.strip().strip("/")
    if not trimmed:
        raise ValueError(f"{name} is required")
    return trimmed


def provider_layout_path(provider: str) -> str:
    return f"{_norm_segment(provider, name='provider')}/LAYOUT.md"


def resource_schema_path(provider: str, resource_path: str) -> str:
    return (
        f"{_norm_segment(provider, name='provider')}/"
        f"{_norm_segment(resource_path, name='resource_path')}/.schema.json"
    )


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
            f"{prefix}/{options.object_type}/" if options.object_type else f"{prefix}/"
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
