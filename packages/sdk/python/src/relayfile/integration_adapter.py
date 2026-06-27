from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from typing import Any, Protocol, runtime_checkable

from .client import RelayFileClient
from .connection import ConnectionProvider
from .types import FileSemantics, WriteEvent, WriteEventActor


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


@dataclass
class RelayBindingView:
    text: str
    author: str | None = None
    skip: bool = False


@runtime_checkable
class RelayBinding(Protocol):
    def present(self, event: WriteEvent) -> RelayBindingView | None: ...
    def reply_path_for(self, source_path: str) -> str | None: ...


_DEFAULT_PRESENT_TEXT_LIMIT = 500


def generic_present(event: WriteEvent, adapter: IntegrationAdapter) -> RelayBindingView:
    provider = _provider_from_path(event.path) or adapter.name
    payload: dict[str, Any] = event.value if isinstance(event.value, dict) else {}
    try:
        object_type, object_id = _object_identity_from_path(event.path)
        semantics = adapter.compute_semantics(object_type, object_id, payload)
    except Exception:
        semantics = FileSemantics()

    lines: list[str] = [f"{event.operation} {event.path}"]
    text = "\n".join(line for line in lines if line.strip())
    actor = event.actor
    author: str | None = None
    if isinstance(actor, WriteEventActor):
        author = actor.id
    elif isinstance(actor, dict):
        author = str(actor.get("id", "")) or None
    return RelayBindingView(
        text=text[:_DEFAULT_PRESENT_TEXT_LIMIT],
        author=author or provider,
    )


def generic_reply_path_for(source_path: str) -> str | None:
    normalized = source_path.strip().rstrip("/")
    if not normalized or not normalized.startswith("/"):
        return None
    return f"{normalized}/replies/draft.json"


def _provider_from_path(path: str) -> str:
    segments = [s for s in path.split("/") if s]
    return segments[0] if segments else ""


def _object_identity_from_path(path: str) -> tuple[str, str]:
    segments = [s for s in path.split("/") if s]
    object_type = segments[1] if len(segments) > 1 else "unknown"
    filename = segments[-1] if segments else ""
    object_id = filename.rsplit(".", 1)[0] if "." in filename else filename
    return object_type, object_id


class IntegrationAdapter(ABC):
    name: str
    version: str
    relay_binding: RelayBinding | None = None

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
