from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any


# ---------------------------------------------------------------------------
# Filesystem
# ---------------------------------------------------------------------------

@dataclass
class TreeEntry:
    path: str
    type: str  # "file" | "dir"
    revision: str
    provider: str | None = None
    provider_object_id: str | None = None
    size: int | None = None
    updated_at: str | None = None
    property_count: int | None = None
    relation_count: int | None = None
    permission_count: int | None = None
    comment_count: int | None = None


@dataclass
class TreeResponse:
    path: str
    entries: list[TreeEntry]
    next_cursor: str | None = None


@dataclass
class FileSemantics:
    properties: dict[str, str] | None = None
    relations: list[str] | None = None
    permissions: list[str] | None = None
    comments: list[str] | None = None


@dataclass
class FileReadResponse:
    path: str
    revision: str
    content_type: str
    content: str
    provider: str | None = None
    provider_object_id: str | None = None
    last_edited_at: str | None = None
    semantics: FileSemantics | None = None


@dataclass
class FileQueryItem:
    path: str
    revision: str
    content_type: str
    size: int
    provider: str | None = None
    provider_object_id: str | None = None
    last_edited_at: str | None = None
    properties: dict[str, str] | None = None
    relations: list[str] | None = None
    permissions: list[str] | None = None
    comments: list[str] | None = None


@dataclass
class FileQueryResponse:
    items: list[FileQueryItem]
    next_cursor: str | None = None


@dataclass
class WriteFileInput:
    workspace_id: str
    path: str
    base_revision: str
    content: str
    content_type: str | None = None
    semantics: FileSemantics | None = None
    correlation_id: str | None = None


@dataclass
class DeleteFileInput:
    workspace_id: str
    path: str
    base_revision: str
    correlation_id: str | None = None


@dataclass
class WriteQueuedResponse:
    op_id: str
    status: str  # "queued" | "pending"
    target_revision: str
    writeback: dict[str, Any] | None = None


# ---------------------------------------------------------------------------
# Events
# ---------------------------------------------------------------------------

@dataclass
class FilesystemEvent:
    event_id: str
    type: str
    path: str
    revision: str
    origin: str
    correlation_id: str
    timestamp: str
    provider: str | None = None


@dataclass
class EventFeedResponse:
    events: list[FilesystemEvent]
    next_cursor: str | None = None


# ---------------------------------------------------------------------------
# Operations
# ---------------------------------------------------------------------------

@dataclass
class OperationStatusResponse:
    op_id: str
    status: str
    attempt_count: int
    path: str | None = None
    revision: str | None = None
    action: str | None = None
    provider: str | None = None
    next_attempt_at: str | None = None
    last_error: str | None = None
    provider_result: dict[str, Any] | None = None
    correlation_id: str | None = None


@dataclass
class OperationFeedResponse:
    items: list[OperationStatusResponse]
    next_cursor: str | None = None


@dataclass
class QueuedResponse:
    status: str  # "queued"
    id: str
    correlation_id: str | None = None


@dataclass
class AckResponse:
    status: str  # "acknowledged"
    id: str
    correlation_id: str | None = None


# ---------------------------------------------------------------------------
# Sync
# ---------------------------------------------------------------------------

@dataclass
class SyncProviderStatus:
    provider: str
    status: str  # "healthy" | "lagging" | "error" | "paused"
    cursor: str | None = None
    watermark_ts: str | None = None
    lag_seconds: int | None = None
    last_error: str | None = None
    failure_codes: dict[str, int] | None = None
    dead_lettered_envelopes: int | None = None
    dead_lettered_ops: int | None = None


@dataclass
class SyncStatusResponse:
    workspace_id: str
    providers: list[SyncProviderStatus]


@dataclass
class SyncIngressStatusResponse:
    workspace_id: str
    queue_depth: int = 0
    queue_capacity: int = 0
    queue_utilization: float = 0.0
    pending_total: int = 0
    oldest_pending_age_seconds: int = 0
    dead_letter_total: int = 0
    dead_letter_by_provider: dict[str, int] = field(default_factory=dict)
    accepted_total: int = 0
    dropped_total: int = 0
    deduped_total: int = 0
    coalesced_total: int = 0
    dedupe_rate: float = 0.0
    coalesce_rate: float = 0.0
    suppressed_total: int = 0
    stale_total: int = 0
    ingress_by_provider: dict[str, Any] = field(default_factory=dict)


@dataclass
class DeadLetterItem:
    envelope_id: str
    workspace_id: str
    provider: str
    delivery_id: str
    failed_at: str
    attempt_count: int
    last_error: str
    correlation_id: str | None = None


@dataclass
class DeadLetterFeedResponse:
    items: list[DeadLetterItem]
    next_cursor: str | None = None


# ---------------------------------------------------------------------------
# Webhook & Writeback
# ---------------------------------------------------------------------------

@dataclass
class IngestWebhookInput:
    workspace_id: str
    provider: str
    event_type: str
    path: str
    data: dict[str, Any] | None = None
    delivery_id: str | None = None
    timestamp: str | None = None
    headers: dict[str, str] | None = None
    correlation_id: str | None = None


@dataclass
class WritebackItem:
    id: str
    workspace_id: str
    path: str
    revision: str
    correlation_id: str | None = None


@dataclass
class AckWritebackInput:
    workspace_id: str
    item_id: str
    success: bool
    error: str | None = None
    correlation_id: str | None = None


@dataclass
class AckWritebackResponse:
    status: str  # "acknowledged"
    id: str
    success: bool
    correlation_id: str | None = None


# ---------------------------------------------------------------------------
# Admin
# ---------------------------------------------------------------------------

@dataclass
class BackendStatusResponse:
    backend_profile: str
    state_backend: str
    envelope_queue: str
    envelope_queue_depth: int
    envelope_queue_capacity: int
    writeback_queue: str
    writeback_queue_depth: int
    writeback_queue_capacity: int


@dataclass
class AdminIngressStatusResponse:
    generated_at: str
    alert_profile: str
    effective_alert_profile: str
    workspace_count: int
    returned_workspace_count: int
    workspace_ids: list[str]
    next_cursor: str | None = None
    pending_total: int = 0
    dead_letter_total: int = 0
    accepted_total: int = 0
    dropped_total: int = 0
    deduped_total: int = 0
    coalesced_total: int = 0
    suppressed_total: int = 0
    stale_total: int = 0
    thresholds: dict[str, Any] = field(default_factory=dict)
    alert_totals: dict[str, Any] = field(default_factory=dict)
    alerts_truncated: bool = False
    alerts: list[dict[str, Any]] = field(default_factory=list)
    workspaces: dict[str, Any] = field(default_factory=dict)


@dataclass
class AdminSyncStatusResponse:
    generated_at: str
    workspace_count: int
    returned_workspace_count: int
    workspace_ids: list[str]
    next_cursor: str | None = None
    provider_status_count: int = 0
    healthy_count: int = 0
    lagging_count: int = 0
    error_count: int = 0
    paused_count: int = 0
    dead_lettered_envelopes_total: int = 0
    dead_lettered_ops_total: int = 0
    thresholds: dict[str, Any] = field(default_factory=dict)
    alert_totals: dict[str, Any] = field(default_factory=dict)
    alerts_truncated: bool = False
    alerts: list[dict[str, Any]] = field(default_factory=list)
    failure_codes: dict[str, int] = field(default_factory=dict)
    workspaces: dict[str, Any] = field(default_factory=dict)
