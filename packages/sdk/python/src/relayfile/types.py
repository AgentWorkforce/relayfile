from __future__ import annotations

from collections.abc import Awaitable, Callable
from dataclasses import dataclass, field
from typing import Any, Literal, Protocol, TypedDict


class _RelayFileJwtClaimsRequired(TypedDict):
    """Required JWT claims — server returns 401 if missing."""

    workspace_id: str
    agent_name: str
    aud: list[str]  # must include "relayfile"


class RelayFileJwtClaims(_RelayFileJwtClaimsRequired, total=False):
    """JWT claims expected by the Relayfile API for SDK bearer tokens.

    Required: ``workspace_id``, ``agent_name``, ``aud``.
    Optional: ``exp``, ``iat``, ``nbf``, ``iss``, ``sub``.

    Example minting payload::

        {"workspace_id": "ws_123", "agent_name": "review-bot", "aud": ["relayfile"]}
    """

    exp: int
    iat: int
    nbf: int
    iss: str
    sub: str


ContentEncoding = Literal["utf-8", "base64"]
WritebackState = Literal["pending", "succeeded", "failed", "dead_lettered"]
# WritebackListState mirrors the broader set of state filters the `writeback
# list` CLI accepts ("dead" is the CLI alias for "dead_lettered" surfaced in
# `--state dead` and in JSON list rows). Keep the typed surface aligned with
# the TS SDK so consumers can round-trip rows from either client.
WritebackListState = Literal[
    "pending",
    "succeeded",
    "failed",
    "dead_lettered",
    "dead",
]
WritebackActionType = Literal["file_upsert", "file_delete"]
WritebackDeadLetterErrorCode = Literal[
    "schema_violation",
    "provider_4xx",
    "provider_5xx_exhausted",
    "timeout",
]
ExportFormat = Literal["tar", "json", "patch"]


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
class ListTreeOptions:
    path: str | None = None
    depth: int | None = None
    cursor: str | None = None
    correlation_id: str | None = None


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
    encoding: ContentEncoding | None = None
    provider: str | None = None
    provider_object_id: str | None = None
    last_edited_at: str | None = None
    semantics: FileSemantics | None = None


ExportJsonResponse = list[FileReadResponse]


@dataclass
class FileWriteRequest:
    content: str
    content_type: str | None = None
    semantics: FileSemantics | None = None


@dataclass
class BulkWriteFile:
    path: str
    content: str
    content_type: str | None = None
    encoding: ContentEncoding | None = None


@dataclass
class BulkWriteInput:
    workspace_id: str
    files: list[BulkWriteFile]
    correlation_id: str | None = None


@dataclass
class BulkWriteError:
    path: str
    code: str
    message: str


@dataclass
class BulkWriteWritebackResult:
    provider: str | None = None
    state: str | None = None


@dataclass
class BulkWriteResult:
    path: str
    revision: str
    content_type: str | None = None
    op_id: str | None = None
    content_identity: ContentIdentity | None = None
    writeback: BulkWriteWritebackResult | None = None


@dataclass
class BulkWriteResponse:
    written: int
    error_count: int
    errors: list[BulkWriteError]
    correlation_id: str
    results: list[BulkWriteResult] | None = None


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
class QueryFilesOptions:
    path: str | None = None
    provider: str | None = None
    relation: str | None = None
    permission: str | None = None
    comment: str | None = None
    properties: dict[str, str] | None = None
    cursor: str | None = None
    limit: int | None = None
    correlation_id: str | None = None


@dataclass
class ExportOptions:
    workspace_id: str
    format: ExportFormat | None = None
    correlation_id: str | None = None


@dataclass
class WriteFileInput:
    workspace_id: str
    path: str
    base_revision: str
    content: str
    content_type: str | None = None
    encoding: ContentEncoding | None = None
    semantics: FileSemantics | None = None
    correlation_id: str | None = None


@dataclass
class DeleteFileInput:
    workspace_id: str
    path: str
    base_revision: str
    correlation_id: str | None = None


@dataclass
class WritebackInfo:
    provider: str | None = None
    state: WritebackState | None = None


@dataclass
class WriteQueuedResponse:
    op_id: str
    status: str  # "queued" | "pending"
    target_revision: str
    writeback: WritebackInfo | None = None


@dataclass
class ErrorResponse:
    code: str
    message: str
    correlation_id: str
    details: dict[str, Any] | None = None


@dataclass
class ConflictErrorResponse:
    code: str
    message: str
    correlation_id: str
    expected_revision: str
    current_revision: str
    details: dict[str, Any] | None = None
    current_content_preview: str | None = None


# ---------------------------------------------------------------------------
# Events
# ---------------------------------------------------------------------------


@dataclass
class FilesystemEvent:
    """Filesystem event row.

    Digest generation is exposed through ordinary ``file.created`` and
    ``file.updated`` events at paths recognized by ``relayfile.is_digest_path``:
    the anchors in ``relayfile.DIGEST_PATHS`` plus date-stamped closed-day
    files. Consumers can filter returned events after calling
    ``RelayFileClient.get_events``.
    """

    event_id: str
    type: str
    path: str
    revision: str
    origin: str
    correlation_id: str
    timestamp: str
    provider: str | None = None


@dataclass
class WriteEventActor:
    type: str  # "agent" | "user" | "system"
    id: str


@dataclass
class WriteEvent:
    workspace_id: str
    path: str
    operation: str  # "create" | "update" | "delete"
    revision: str
    previous_revision: str | None
    timestamp: str
    source: str  # "webhook" | "agent" | "sync" | "api" | "cli"
    value: Any | None = None
    actor: WriteEventActor | dict[str, Any] | None = None


@dataclass
class EventFeedResponse:
    events: list[FilesystemEvent]
    next_cursor: str | None = None


@dataclass
class DigestWindow:
    """Inclusive/exclusive ISO-8601 window for a digest run."""

    from_: str
    to: str


@dataclass
class DigestBullet:
    text: str
    canonical_path: str


@dataclass
class DigestSection:
    provider: str
    bullets: list[DigestBullet]


class DigestContext(Protocol):
    provider: str
    window: DigestWindow

    def change_events(
        self,
        event_filter: dict[str, list[str]] | None = None,
    ) -> Awaitable[list[dict[str, Any]]]: ...


DigestHandler = Callable[[DigestContext], Awaitable[DigestSection | None]]


@dataclass
class WritebackSchemaRef:
    provider: str
    resource: str
    path: str


@dataclass
class LayoutManifestAlias:
    segment: str
    description: str | None = None


@dataclass
class LayoutManifestResource:
    name: str
    canonical_filename: str
    writeback_actions: list[str] | None = None
    writeback_schemas: list[WritebackSchemaRef] | None = None
    aliases: list[LayoutManifestAlias] | None = None


@dataclass
class LayoutManifest:
    provider: str
    materialization: Literal["eager", "lazy"]
    resources: list[LayoutManifestResource]


@dataclass
class GetEventsOptions:
    provider: str | None = None
    cursor: str | None = None
    limit: int | None = None
    correlation_id: str | None = None


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
    created_at: str | None = None
    updated_at: str | None = None
    completed_at: str | None = None


@dataclass
class GetOperationsOptions:
    status: str | None = None
    action: str | None = None
    provider: str | None = None
    cursor: str | None = None
    limit: int | None = None
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
class SyncRefreshRequest:
    provider: str
    reason: str | None = None


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
    # Productized cloud-mount contract §7.4: cloud SHOULD set this to
    # False when webhook delivery is degraded. Optional for backward
    # compatibility with deployments that do not yet emit it.
    webhook_healthy: bool | None = None


@dataclass
class GetSyncStatusOptions:
    provider: str | None = None
    correlation_id: str | None = None


@dataclass
class SyncStatusResponse:
    workspace_id: str
    providers: list[SyncProviderStatus]


@dataclass
class IngressProviderStats:
    accepted_total: int = 0
    dropped_total: int = 0
    deduped_total: int = 0
    coalesced_total: int = 0
    pending_total: int = 0
    oldest_pending_age_seconds: int = 0
    suppressed_total: int = 0
    stale_total: int = 0
    dedupe_rate: float = 0.0
    coalesce_rate: float = 0.0


@dataclass
class GetSyncIngressStatusOptions:
    provider: str | None = None
    correlation_id: str | None = None


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
    ingress_by_provider: dict[str, IngressProviderStats] = field(default_factory=dict)


@dataclass
class GetSyncDeadLettersOptions:
    provider: str | None = None
    cursor: str | None = None
    limit: int | None = None
    correlation_id: str | None = None


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
class DeadLetterErrorPayload:
    code: WritebackDeadLetterErrorCode
    message: str
    attempts: int
    first_attempt_at: str
    last_attempt_at: str
    op_id: str
    provider_status: int | None = None
    provider_response: dict[str, Any] | None = None

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> DeadLetterErrorPayload:
        return cls(
            code=data["code"],
            message=data["message"],
            attempts=data["attempts"],
            first_attempt_at=_get(data, "firstAttemptAt", "first_attempt_at"),
            last_attempt_at=_get(data, "lastAttemptAt", "last_attempt_at"),
            op_id=_get(data, "opId", "op_id"),
            provider_status=_get_optional(
                data,
                "providerStatus",
                "provider_status",
            ),
            provider_response=_get_optional(
                data,
                "providerResponse",
                "provider_response",
            ),
        )


WritebackDeadLetterError = DeadLetterErrorPayload


@dataclass
class WritebackItem:
    id: str
    workspace_id: str
    path: str
    revision: str
    correlation_id: str | None = None
    state: WritebackListState | None = None
    op_id: str | None = None
    provider: str | None = None
    action: WritebackActionType | None = None
    attempt_count: int | None = None
    last_attempt_at: str | None = None
    first_attempt_at: str | None = None
    created_at: str | None = None
    error: DeadLetterErrorPayload | None = None

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> WritebackItem:
        raw_error = data.get("error")
        error = (
            DeadLetterErrorPayload.from_dict(raw_error)
            if isinstance(raw_error, dict)
            else None
        )
        # `writeback list` rows from the new CLI use `attempts` and
        # `enqueuedAt`; legacy detail rows still use the camel/snake variants.
        # Accept either source so the SDK type surface stays aligned with both
        # the TS SDK and the new server payload contract.
        attempts = data["attempts"] if "attempts" in data else _get_optional(
            data, "attemptCount", "attempt_count",
        )
        created_at = data["enqueuedAt"] if "enqueuedAt" in data else _get_optional(
            data, "createdAt", "created_at",
        )
        return cls(
            id=data["id"],
            workspace_id=_get(data, "workspaceId", "workspace_id"),
            path=data["path"],
            revision=data["revision"],
            correlation_id=_get_optional(data, "correlationId", "correlation_id"),
            state=data.get("state"),
            op_id=_get_optional(data, "opId", "op_id"),
            provider=data.get("provider"),
            action=data.get("action"),
            attempt_count=attempts,
            last_attempt_at=_get_optional(
                data,
                "lastAttemptAt",
                "last_attempt_at",
            ),
            first_attempt_at=_get_optional(
                data,
                "firstAttemptAt",
                "first_attempt_at",
            ),
            created_at=created_at,
            error=error,
        )


@dataclass
class WritebackItemDetail(WritebackItem):
    error: DeadLetterErrorPayload | None = None


def _get(data: dict[str, Any], camel: str, snake: str) -> Any:
    if camel in data:
        return data[camel]
    return data[snake]


def _get_optional(data: dict[str, Any], camel: str, snake: str) -> Any | None:
    if camel in data:
        return data[camel]
    return data.get(snake)


@dataclass
class AckWritebackInput:
    workspace_id: str
    item_id: str
    success: bool
    error: str | None = None
    # Provider-assigned id of the created/updated object (e.g. the Slack
    # message ts). When present on a successful ack, the service reconciles the
    # agent-authored draft file per the draftFile() rename contract.
    external_id: str | None = None
    # Optional canonical projection path for the draft rename. Must stay under
    # the same provider root as the draft.
    canonical_path: str | None = None
    # Optional fields the provider echoed back about the written record (e.g. a
    # Slack message ts/channel). Surfaced verbatim on the operation's
    # providerResult (recoverable via get_operation). The reserved key
    # providerRevision is server-owned and cannot be overridden via this map.
    provider_result: dict[str, Any] | None = None
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
class AdminIngressAlert:
    workspace_id: str
    type: str
    severity: str
    value: float
    threshold: float
    message: str


@dataclass
class AdminIngressAlertThresholds:
    pending: int = 0
    dead_letter: int = 0
    stale: int = 0
    drop_rate: float = 0.0


@dataclass
class AdminIngressAlertTotals:
    total: int = 0
    critical: int = 0
    warning: int = 0
    by_type: dict[str, int] = field(default_factory=dict)


@dataclass
class GetAdminIngressStatusOptions:
    workspace_id: str | None = None
    provider: str | None = None
    alert_profile: str | None = None
    pending_threshold: int | None = None
    dead_letter_threshold: int | None = None
    stale_threshold: int | None = None
    drop_rate_threshold: int | None = None
    non_zero_only: bool | None = None
    max_alerts: int | None = None
    cursor: str | None = None
    limit: int | None = None
    include_workspaces: bool | None = None
    include_alerts: bool | None = None
    correlation_id: str | None = None


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
    thresholds: AdminIngressAlertThresholds = field(
        default_factory=AdminIngressAlertThresholds
    )
    alert_totals: AdminIngressAlertTotals = field(
        default_factory=AdminIngressAlertTotals
    )
    alerts_truncated: bool = False
    alerts: list[AdminIngressAlert] = field(default_factory=list)
    workspaces: dict[str, SyncIngressStatusResponse] = field(default_factory=dict)


@dataclass
class AdminSyncAlert:
    workspace_id: str
    provider: str
    type: str
    severity: str
    value: float
    threshold: float
    message: str


@dataclass
class AdminSyncAlertThresholds:
    status_error: int = 0
    lag_seconds: int = 0
    dead_lettered_envelopes: int = 0
    dead_lettered_ops: int = 0


@dataclass
class AdminSyncAlertTotals:
    total: int = 0
    critical: int = 0
    warning: int = 0
    by_type: dict[str, int] = field(default_factory=dict)


@dataclass
class GetAdminSyncStatusOptions:
    workspace_id: str | None = None
    provider: str | None = None
    non_zero_only: bool | None = None
    cursor: str | None = None
    limit: int | None = None
    include_workspaces: bool | None = None
    status_error_threshold: int | None = None
    lag_seconds_threshold: int | None = None
    dead_lettered_envelopes_threshold: int | None = None
    dead_lettered_ops_threshold: int | None = None
    max_alerts: int | None = None
    include_alerts: bool | None = None
    correlation_id: str | None = None


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
    thresholds: AdminSyncAlertThresholds = field(
        default_factory=AdminSyncAlertThresholds
    )
    alert_totals: AdminSyncAlertTotals = field(default_factory=AdminSyncAlertTotals)
    alerts_truncated: bool = False
    alerts: list[AdminSyncAlert] = field(default_factory=list)
    failure_codes: dict[str, int] = field(default_factory=dict)
    workspaces: dict[str, SyncStatusResponse] = field(default_factory=dict)
