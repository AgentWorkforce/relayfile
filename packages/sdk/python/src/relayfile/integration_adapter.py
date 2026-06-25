from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from typing import Any

from .client import RelayFileClient
from .connection import ConnectionProvider
from .types import FileSemantics


@dataclass
class AdapterWebhook:
    provider: str
    event_type: str
    object_type: str
    object_id: str
    payload: dict[str, Any]
    connection_id: str | None = None
    metadata: dict[str, Any] | None = None
    raw: Any = None


@dataclass
class IngestError:
    path: str
    error: str


@dataclass
class IngestResult:
    files_written: int
    files_updated: int
    files_deleted: int
    paths: list[str] = field(default_factory=list)
    errors: list[IngestError] = field(default_factory=list)


@dataclass
class SyncOptions:
    cursor: str | None = None
    limit: int | None = None
    extra: dict[str, Any] = field(default_factory=dict)


@dataclass
class SyncError:
    error: str
    path: str | None = None
    object_type: str | None = None


@dataclass
class SyncResult:
    files_written: int
    files_updated: int
    files_deleted: int
    paths: list[str] | None = None
    cursor: str | None = None
    next_cursor: str | None = None
    synced_object_types: list[str] | None = None
    errors: list[SyncError] = field(default_factory=list)


class IntegrationAdapter(ABC):
    name: str
    version: str

    def __init__(self, client: RelayFileClient, provider: ConnectionProvider) -> None:
        self.client = client
        self.provider = provider

    @abstractmethod
    def ingest_webhook(self, workspace_id: str, event: AdapterWebhook) -> IngestResult:
        ...

    @abstractmethod
    def compute_path(self, object_type: str, object_id: str) -> str:
        ...

    @abstractmethod
    def compute_semantics(
        self,
        object_type: str,
        object_id: str,
        payload: dict[str, Any],
    ) -> FileSemantics:
        ...

    def supported_events(self) -> list[str] | None:
        return None

    def write_back(self, workspace_id: str, path: str, content: str) -> Any:
        raise NotImplementedError

    def sync(self, workspace_id: str, options: SyncOptions | None = None) -> SyncResult:
        raise NotImplementedError
