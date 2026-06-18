from __future__ import annotations

import json
import random
import time
from dataclasses import dataclass
from typing import Any, Callable, Union
from urllib.parse import quote, urlencode

import httpx

from .errors import (
    InvalidStateError,
    PayloadTooLargeError,
    QueueFullError,
    RelayFileApiError,
    RevisionConflictError,
)
from .types import (
    AckWritebackInput,
    DeleteFileInput,
    IngestWebhookInput,
    WritebackItem,
    WritebackState,
    WriteFileInput,
)

AccessTokenProvider = Union[str, Callable[[], str]]
AsyncAccessTokenProvider = Union[str, Callable[[], Any]]  # sync or async callable


@dataclass
class RetryOptions:
    """Retry configuration for SDK clients."""

    max_retries: int = 3
    base_delay_ms: int = 100
    max_delay_ms: int = 2000
    jitter_ratio: float = 0.2


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _normalize_retry(opts: RetryOptions | None) -> RetryOptions:
    if opts is None:
        return RetryOptions()
    return RetryOptions(
        max_retries=max(0, opts.max_retries),
        base_delay_ms=max(1, opts.base_delay_ms),
        max_delay_ms=max(1, opts.max_delay_ms),
        jitter_ratio=max(0.0, min(1.0, opts.jitter_ratio)),
    )


def _generate_correlation_id() -> str:
    return f"rf_{int(time.time() * 1000)}_{random.getrandbits(32):08x}"


def _merge_headers(
    headers: dict[str, str] | None,
    *,
    correlation_id: str | None,
    user_agent: str | None,
    has_json_body: bool,
) -> dict[str, str]:
    base_headers: dict[str, str] = {}
    if has_json_body:
        base_headers["Content-Type"] = "application/json"
    if user_agent:
        base_headers["User-Agent"] = user_agent
    if headers:
        base_headers.update(headers)
    if "X-Correlation-Id" not in base_headers:
        base_headers["X-Correlation-Id"] = correlation_id or _generate_correlation_id()
    return base_headers


def _enc(segment: str) -> str:
    return quote(segment, safe="")


def _build_query(params: dict[str, Any]) -> str:
    filtered = {
        k: (str(v).lower() if isinstance(v, bool) else str(v))
        for k, v in params.items()
        if v is not None
    }
    return f"?{urlencode(filtered)}" if filtered else ""


def _resolve_token(provider: AccessTokenProvider) -> str:
    if callable(provider):
        return provider()
    return provider


async def _resolve_token_async(provider: AsyncAccessTokenProvider) -> str:
    if callable(provider):
        result = provider()
        if hasattr(result, "__await__"):
            return await result
        return result  # type: ignore[return-value]
    return provider


def _read_payload(response: httpx.Response) -> Any:
    ct = response.headers.get("content-type", "")
    if "application/json" in ct:
        try:
            return response.json()
        except Exception:
            return {}
    return {"message": response.text}


def _parse_retry_after_ms(header: str | None) -> float | None:
    if not header:
        return None
    try:
        seconds = int(header)
        if seconds >= 0:
            return seconds * 1000.0
    except ValueError:
        pass
    try:
        from email.utils import parsedate_to_datetime

        dt = parsedate_to_datetime(header)
        return max(0.0, (dt.timestamp() - time.time()) * 1000)
    except Exception:
        pass
    return None


def _compute_delay(retry: RetryOptions, attempt: int, retry_after: str | None) -> float:
    parsed = _parse_retry_after_ms(retry_after)
    if parsed is not None:
        return min(retry.max_delay_ms, parsed)
    backoff = retry.base_delay_ms * (2 ** max(0, attempt - 1))
    capped = min(retry.max_delay_ms, backoff)
    factor = 1 + (random.random() * 2 - 1) * retry.jitter_ratio
    return max(0, round(capped * factor))


def _should_retry(status: int, retries: int, max_retries: int) -> bool:
    if retries >= max_retries:
        return False
    return status == 429 or 500 <= status <= 599


def _throw_for_error(status: int, payload: Any, headers: httpx.Headers) -> None:
    data: dict[str, Any] = payload if isinstance(payload, dict) else {}

    code = data.get("code", "unknown_error")
    message = data.get("message", f"HTTP {status}")
    cid = data.get("correlationId")
    details = data.get("details")

    if status == 409 and "expectedRevision" in data and "currentRevision" in data:
        raise RevisionConflictError(
            status=status,
            code=code,
            message=message,
            correlation_id=cid,
            details=details,
            expected_revision=data["expectedRevision"],
            current_revision=data["currentRevision"],
            current_content_preview=data.get("currentContentPreview"),
        )
    if status == 409 and code == "invalid_state":
        raise InvalidStateError(
            status=status,
            code=code,
            message=message,
            correlation_id=cid,
            details=details,
        )
    if status == 429 and code == "queue_full":
        retry_after: int | None = None
        raw = headers.get("retry-after")
        if raw:
            try:
                val = int(raw)
                if val >= 0:
                    retry_after = val
            except ValueError:
                pass
        raise QueueFullError(
            status=status,
            code=code,
            message=message,
            correlation_id=cid,
            details=details,
            retry_after_seconds=retry_after,
        )
    if status == 413:
        raise PayloadTooLargeError(
            status=status,
            code=data.get("code", "payload_too_large"),
            message=data.get("message", "Request payload exceeds configured limit"),
            correlation_id=cid,
            details=details,
        )
    raise RelayFileApiError(
        status=status,
        code=code,
        message=message,
        correlation_id=cid,
        details=details,
    )


# ======================================================================
# Synchronous client
# ======================================================================


DEFAULT_RELAYFILE_BASE_URL = "https://api.relayfile.dev"
DEFAULT_RELAYFILE_CLOUD_BASE_URL = "https://agentrelay.com/cloud"


def _normalize_provider_id(provider: str) -> str:
    # Mirrors the TS SDK: the metadata + accessible-resources verbs accept
    # any string so operators can target dynamically-discovered providers.
    # We only trim + lowercase here; cloud rejects unknown ids with a typed
    # 404 (`unknown_provider`).
    if not isinstance(provider, str):
        raise ValueError("provider must be a string")
    trimmed = provider.strip()
    if not trimmed:
        raise ValueError("provider is required")
    return trimmed.lower()


def _coerce_resource_entries(payload: Any) -> list[dict[str, Any]]:
    if not isinstance(payload, dict):
        return []
    resources = payload.get("resources")
    if not isinstance(resources, list):
        return []
    out: list[dict[str, Any]] = []
    for entry in resources:
        if not isinstance(entry, dict):
            continue
        rid = entry.get("id")
        url = entry.get("url")
        if not isinstance(rid, str) or not rid or not isinstance(url, str) or not url:
            continue
        out.append(dict(entry))
    return out


def _coerce_metadata_response(payload: Any) -> dict[str, Any]:
    if isinstance(payload, dict):
        metadata = payload.get("metadata")
        if isinstance(metadata, dict):
            return metadata
    raise ValueError(
        f"invalid cloud response: expected object field `metadata`, got {payload!r}"
    )


def _coerce_writeback_items(payload: Any) -> list[WritebackItem]:
    rows = payload.get("items") if isinstance(payload, dict) else payload
    if not isinstance(rows, list):
        return []
    return [WritebackItem.from_dict(row) for row in rows if isinstance(row, dict)]


class RelayFileClient:
    """Synchronous RelayFile SDK client with retry support."""

    def __init__(
        self,
        base_url: str = DEFAULT_RELAYFILE_BASE_URL,
        token: AccessTokenProvider = "",
        *,
        timeout: float = 30.0,
        user_agent: str | None = None,
        retry: RetryOptions | None = None,
        http_client: httpx.Client | None = None,
        cloud_base_url: str | None = None,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        # Integration setup verbs (list_accessible_resources +
        # set_integration_metadata) target the cloud control plane, which
        # is a different service from the relayfile data plane. Default to
        # the public agentrelay.com/cloud host when the caller hasn't
        # supplied an override.
        self._cloud_base_url = (
            cloud_base_url or DEFAULT_RELAYFILE_CLOUD_BASE_URL
        ).rstrip("/")
        self._token_provider = token
        self._user_agent = user_agent
        self._retry = _normalize_retry(retry)
        self._client = http_client or httpx.Client(timeout=timeout)
        self._owns_client = http_client is None

    def close(self) -> None:
        if self._owns_client:
            self._client.close()

    def __enter__(self) -> RelayFileClient:
        return self

    def __exit__(self, *args: object) -> None:
        self.close()

    # ------------------------------------------------------------------
    # Internal HTTP with retry
    # ------------------------------------------------------------------

    def _request(
        self,
        method: str,
        path: str,
        *,
        headers: dict[str, str] | None = None,
        json_body: Any = None,
        correlation_id: str | None = None,
        base_url: str | None = None,
    ) -> Any:
        resp = self._request_response(
            method,
            path,
            headers=headers,
            json_body=json_body,
            correlation_id=correlation_id,
            base_url=base_url,
        )
        return _read_payload(resp)

    def _request_response(
        self,
        method: str,
        path: str,
        *,
        headers: dict[str, str] | None = None,
        json_body: Any = None,
        correlation_id: str | None = None,
        base_url: str | None = None,
    ) -> httpx.Response:
        base_headers = _merge_headers(
            headers,
            correlation_id=correlation_id,
            user_agent=self._user_agent,
            has_json_body=json_body is not None,
        )

        url = f"{(base_url or self._base_url).rstrip('/')}{path}"
        retries = 0

        while True:
            token = _resolve_token(self._token_provider)
            req_headers = {"Authorization": f"Bearer {token}", **base_headers}

            try:
                resp = self._client.request(
                    method,
                    url,
                    headers=req_headers,
                    content=json.dumps(json_body).encode()
                    if json_body is not None
                    else None,
                )
            except httpx.TransportError:
                if retries < self._retry.max_retries:
                    retries += 1
                    time.sleep(_compute_delay(self._retry, retries, None) / 1000)
                    continue
                raise

            if resp.is_success:
                return resp

            payload = _read_payload(resp)

            if _should_retry(resp.status_code, retries, self._retry.max_retries):
                retries += 1
                time.sleep(
                    _compute_delay(
                        self._retry, retries, resp.headers.get("retry-after")
                    )
                    / 1000
                )
                continue

            _throw_for_error(resp.status_code, payload, resp.headers)

    # ------------------------------------------------------------------
    # Filesystem
    # ------------------------------------------------------------------

    def list_tree(
        self,
        workspace_id: str,
        *,
        path: str = "/",
        depth: int | None = None,
        cursor: str | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        query = _build_query({"path": path, "depth": depth, "cursor": cursor})
        return self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/fs/tree{query}",
            correlation_id=correlation_id,
        )

    def read_file(
        self,
        workspace_id: str,
        path: str,
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        query = _build_query({"path": path})
        return self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/fs/file{query}",
            correlation_id=correlation_id,
        )

    def query_files(
        self,
        workspace_id: str,
        *,
        path: str | None = None,
        provider: str | None = None,
        relation: str | None = None,
        permission: str | None = None,
        comment: str | None = None,
        cursor: str | None = None,
        limit: int | None = None,
        properties: dict[str, str] | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        params: dict[str, Any] = {
            "path": path,
            "provider": provider,
            "relation": relation,
            "permission": permission,
            "comment": comment,
            "cursor": cursor,
            "limit": limit,
        }
        if properties:
            for k, v in properties.items():
                if k and v is not None:
                    params[f"property.{k}"] = v
        query = _build_query(params)
        return self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/fs/query{query}",
            correlation_id=correlation_id,
        )

    def write_file(self, input: WriteFileInput) -> dict[str, Any]:
        query = _build_query({"path": input.path})
        body: dict[str, Any] = {
            "contentType": input.content_type or "text/markdown",
            "content": input.content,
        }
        if input.semantics is not None:
            body["semantics"] = {
                "properties": input.semantics.properties,
                "relations": input.semantics.relations,
                "permissions": input.semantics.permissions,
                "comments": input.semantics.comments,
            }
        # Single-file PUT expects the target path in the query string, not the JSON body.
        return self._request(
            "PUT",
            f"/v1/workspaces/{_enc(input.workspace_id)}/fs/file{query}",
            headers={"If-Match": input.base_revision},
            json_body=body,
            correlation_id=input.correlation_id,
        )

    def bulk_write(
        self,
        workspace_id: str,
        files: list[dict[str, Any]],
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        return self._request(
            "POST",
            f"/v1/workspaces/{_enc(workspace_id)}/fs/bulk",
            json_body={"files": files},
            correlation_id=correlation_id,
        )

    def delete_file(self, input: DeleteFileInput) -> dict[str, Any]:
        query = _build_query({"path": input.path})
        return self._request(
            "DELETE",
            f"/v1/workspaces/{_enc(input.workspace_id)}/fs/file{query}",
            headers={"If-Match": input.base_revision},
            correlation_id=input.correlation_id,
        )

    def export_workspace(
        self,
        workspace_id: str,
        *,
        format: str = "json",
        correlation_id: str | None = None,
    ) -> list[dict[str, Any]] | bytes:
        query = _build_query({"format": format})
        response = self._request_response(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/fs/export{query}",
            headers={"Accept": "application/json" if format == "json" else "*/*"},
            correlation_id=correlation_id,
        )
        if format == "json":
            payload = _read_payload(response)
            return payload if isinstance(payload, list) else []
        return response.content

    # ------------------------------------------------------------------
    # Events
    # ------------------------------------------------------------------

    def get_events(
        self,
        workspace_id: str,
        *,
        provider: str | None = None,
        cursor: str | None = None,
        limit: int | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        """List filesystem events.

        Digest artifacts are reported as ordinary ``file.created`` and
        ``file.updated`` events. ``DIGEST_PATHS`` holds the literal rolling
        anchor paths, including ``digests/today.md``,
        ``digests/yesterday.md``, ``digests/this-week.md``, and
        ``digests/last-week.md``; closed-window daily artifacts use the
        date-stamped form
        ``digests/YYYY-MM-DD.md``. Filter returned events through
        ``relayfile.is_digest_path`` to subscribe to the full taxonomy.
        """

        query = _build_query({"provider": provider, "cursor": cursor, "limit": limit})
        return self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/fs/events{query}",
            correlation_id=correlation_id,
        )

    # ------------------------------------------------------------------
    # Operations
    # ------------------------------------------------------------------

    def get_op(
        self,
        workspace_id: str,
        op_id: str,
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        return self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/ops/{_enc(op_id)}",
            correlation_id=correlation_id,
        )

    def list_ops(
        self,
        workspace_id: str,
        *,
        status: str | None = None,
        action: str | None = None,
        provider: str | None = None,
        cursor: str | None = None,
        limit: int | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        query = _build_query(
            {
                "status": status,
                "action": action,
                "provider": provider,
                "cursor": cursor,
                "limit": limit,
            }
        )
        return self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/ops{query}",
            correlation_id=correlation_id,
        )

    def replay_op(
        self,
        workspace_id: str,
        op_id: str,
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        return self._request(
            "POST",
            f"/v1/workspaces/{_enc(workspace_id)}/ops/{_enc(op_id)}/replay",
            correlation_id=correlation_id,
        )

    # ------------------------------------------------------------------
    # Sync
    # ------------------------------------------------------------------

    def get_sync_status(
        self,
        workspace_id: str,
        *,
        provider: str | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        query = _build_query({"provider": provider})
        return self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/sync/status{query}",
            correlation_id=correlation_id,
        )

    def get_sync_ingress_status(
        self,
        workspace_id: str,
        *,
        provider: str | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        query = _build_query({"provider": provider})
        return self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/sync/ingress{query}",
            correlation_id=correlation_id,
        )

    def get_sync_dead_letters(
        self,
        workspace_id: str,
        *,
        provider: str | None = None,
        cursor: str | None = None,
        limit: int | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        query = _build_query({"provider": provider, "cursor": cursor, "limit": limit})
        return self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/sync/dead-letter{query}",
            correlation_id=correlation_id,
        )

    def get_sync_dead_letter(
        self,
        workspace_id: str,
        envelope_id: str,
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        return self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/sync/dead-letter/{_enc(envelope_id)}",
            correlation_id=correlation_id,
        )

    def replay_sync_dead_letter(
        self,
        workspace_id: str,
        envelope_id: str,
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        return self._request(
            "POST",
            f"/v1/workspaces/{_enc(workspace_id)}/sync/dead-letter/{_enc(envelope_id)}/replay",
            correlation_id=correlation_id,
        )

    def ack_sync_dead_letter(
        self,
        workspace_id: str,
        envelope_id: str,
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        return self._request(
            "POST",
            f"/v1/workspaces/{_enc(workspace_id)}/sync/dead-letter/{_enc(envelope_id)}/ack",
            correlation_id=correlation_id,
        )

    def trigger_sync_refresh(
        self,
        workspace_id: str,
        provider: str,
        *,
        reason: str | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        return self._request(
            "POST",
            f"/v1/workspaces/{_enc(workspace_id)}/sync/refresh",
            json_body={"provider": provider, "reason": reason},
            correlation_id=correlation_id,
        )

    # ------------------------------------------------------------------
    # Webhooks & Writeback
    # ------------------------------------------------------------------

    def ingest_webhook(self, input: IngestWebhookInput) -> dict[str, Any]:
        body: dict[str, Any] = {
            "provider": input.provider,
            "event_type": input.event_type,
            "path": input.path,
        }
        if input.data is not None:
            body["data"] = input.data
        if input.delivery_id is not None:
            body["delivery_id"] = input.delivery_id
        if input.timestamp is not None:
            body["timestamp"] = input.timestamp
        if input.headers is not None:
            body["headers"] = input.headers
        return self._request(
            "POST",
            f"/v1/workspaces/{_enc(input.workspace_id)}/webhooks/ingest",
            json_body=body,
            correlation_id=input.correlation_id,
        )

    def list_writebacks(
        self,
        workspace_id: str,
        *,
        state: WritebackState,
        correlation_id: str | None = None,
    ) -> list[WritebackItem]:
        """List writeback items for the given state.

        Currently only ``state="pending"`` is wired against the authoritative
        HTTP contract (``GET /v1/workspaces/{workspaceId}/writeback/pending``).
        Other states will raise :class:`NotImplementedError` until the
        state-filtered endpoint is added to ``openapi/relayfile-v1.openapi.yaml``
        and ``internal/httpapi/server.go`` by the ``update-relayfile-cli`` slice
        (workspace-primitives work item 5).
        """
        if state != "pending":
            raise NotImplementedError(
                "list_writebacks(state=%r) requires the state-filtered writeback "
                "endpoint, which is not yet present in the authoritative OpenAPI "
                "contract. Track the update-relayfile-cli slice "
                "(workspace-primitives work item 5)." % (state,)
            )
        payload = self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/writeback/pending",
            correlation_id=correlation_id,
        )
        return _coerce_writeback_items(payload)

    def list_pending_writebacks(
        self,
        workspace_id: str,
        *,
        correlation_id: str | None = None,
    ) -> list[WritebackItem]:
        return self.list_writebacks(
            workspace_id,
            state="pending",
            correlation_id=correlation_id,
        )

    def ack_writeback(self, input: AckWritebackInput) -> dict[str, Any]:
        return self._request(
            "POST",
            f"/v1/workspaces/{_enc(input.workspace_id)}/writeback/{_enc(input.item_id)}/ack",
            json_body={
                "success": input.success,
                "error": input.error,
                "externalId": input.external_id,
                "canonicalPath": input.canonical_path,
                "providerResult": input.provider_result,
            },
            correlation_id=input.correlation_id,
        )

    # ------------------------------------------------------------------
    # Admin
    # ------------------------------------------------------------------

    def get_backend_status(
        self, *, correlation_id: str | None = None
    ) -> dict[str, Any]:
        return self._request("GET", "/v1/admin/backends", correlation_id=correlation_id)

    def get_admin_ingress_status(
        self,
        *,
        workspace_id: str | None = None,
        provider: str | None = None,
        alert_profile: str | None = None,
        pending_threshold: int | None = None,
        dead_letter_threshold: int | None = None,
        stale_threshold: int | None = None,
        drop_rate_threshold: float | None = None,
        non_zero_only: bool | None = None,
        max_alerts: int | None = None,
        cursor: str | None = None,
        limit: int | None = None,
        include_workspaces: bool | None = None,
        include_alerts: bool | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        query = _build_query(
            {
                "workspaceId": workspace_id,
                "provider": provider,
                "alertProfile": alert_profile,
                "pendingThreshold": pending_threshold,
                "deadLetterThreshold": dead_letter_threshold,
                "staleThreshold": stale_threshold,
                "dropRateThreshold": drop_rate_threshold,
                "nonZeroOnly": non_zero_only,
                "maxAlerts": max_alerts,
                "cursor": cursor,
                "limit": limit,
                "includeWorkspaces": include_workspaces,
                "includeAlerts": include_alerts,
            }
        )
        return self._request(
            "GET", f"/v1/admin/ingress{query}", correlation_id=correlation_id
        )

    def get_admin_sync_status(
        self,
        *,
        workspace_id: str | None = None,
        provider: str | None = None,
        non_zero_only: bool | None = None,
        cursor: str | None = None,
        limit: int | None = None,
        include_workspaces: bool | None = None,
        status_error_threshold: int | None = None,
        lag_seconds_threshold: int | None = None,
        dead_lettered_envelopes_threshold: int | None = None,
        dead_lettered_ops_threshold: int | None = None,
        max_alerts: int | None = None,
        include_alerts: bool | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        query = _build_query(
            {
                "workspaceId": workspace_id,
                "provider": provider,
                "nonZeroOnly": non_zero_only,
                "cursor": cursor,
                "limit": limit,
                "includeWorkspaces": include_workspaces,
                "statusErrorThreshold": status_error_threshold,
                "lagSecondsThreshold": lag_seconds_threshold,
                "deadLetteredEnvelopesThreshold": dead_lettered_envelopes_threshold,
                "deadLetteredOpsThreshold": dead_lettered_ops_threshold,
                "maxAlerts": max_alerts,
                "includeAlerts": include_alerts,
            }
        )
        return self._request(
            "GET", f"/v1/admin/sync{query}", correlation_id=correlation_id
        )

    def replay_admin_envelope(
        self,
        envelope_id: str,
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        return self._request(
            "POST",
            f"/v1/admin/replay/envelope/{_enc(envelope_id)}",
            correlation_id=correlation_id,
        )

    def replay_admin_op(
        self,
        op_id: str,
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        return self._request(
            "POST",
            f"/v1/admin/replay/op/{_enc(op_id)}",
            correlation_id=correlation_id,
        )

    # ------------------------------------------------------------------
    # Integration setup (cloud control plane)
    # ------------------------------------------------------------------

    def list_accessible_resources(
        self,
        workspace_id: str,
        provider: str,
        *,
        correlation_id: str | None = None,
    ) -> list[dict[str, Any]]:
        """List the upstream resources the connection's OAuth grant covers.

        Today this is only meaningful for Atlassian-family providers
        (``jira``, ``confluence``); for everything else cloud returns a
        400 with ``code="provider_has_no_accessible_resources"`` which
        surfaces here as a ``RelayFileApiError`` so callers can branch on
        the code rather than treating an empty list as ambiguous.

        Use the returned ``id`` / ``url`` pair to call
        :meth:`set_integration_metadata` with ``{"cloudId": id,
        "baseUrl": url}``.
        """
        normalized = _normalize_provider_id(provider)
        payload = self._request(
            "GET",
            f"/api/v1/workspaces/{_enc(workspace_id)}/integrations/{_enc(normalized)}/accessible-resources",
            correlation_id=correlation_id,
            base_url=self._cloud_base_url,
        )
        return _coerce_resource_entries(payload)

    def set_integration_metadata(
        self,
        workspace_id: str,
        provider: str,
        metadata: dict[str, Any],
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        """Replace the connection metadata namespace for ``provider``.

        Wraps cloud's PUT .../metadata, which forwards to
        ``nango.setMetadata`` (full replacement, not merge). The cloud
        side rejects top-level keys that look like Nango plumbing
        (``_*``, ``connection_*``, ``auth_*``,
        ``provider_config_key``, ``connection_id``) with
        ``code="invalid_metadata"``.
        """
        if not isinstance(metadata, dict):
            raise ValueError("metadata must be a dict")
        normalized = _normalize_provider_id(provider)
        payload = self._request(
            "PUT",
            f"/api/v1/workspaces/{_enc(workspace_id)}/integrations/{_enc(normalized)}/metadata",
            json_body={"metadata": metadata},
            correlation_id=correlation_id,
            base_url=self._cloud_base_url,
        )
        return _coerce_metadata_response(payload)


# ======================================================================
# Async client
# ======================================================================


class AsyncRelayFileClient:
    """Async RelayFile SDK client with retry support."""

    def __init__(
        self,
        base_url: str = DEFAULT_RELAYFILE_BASE_URL,
        token: AsyncAccessTokenProvider = "",
        *,
        timeout: float = 30.0,
        user_agent: str | None = None,
        retry: RetryOptions | None = None,
        http_client: httpx.AsyncClient | None = None,
        cloud_base_url: str | None = None,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        # See the sync client for why integration setup verbs target a
        # different base URL.
        self._cloud_base_url = (
            cloud_base_url or DEFAULT_RELAYFILE_CLOUD_BASE_URL
        ).rstrip("/")
        self._token_provider = token
        self._user_agent = user_agent
        self._retry = _normalize_retry(retry)
        self._client = http_client or httpx.AsyncClient(timeout=timeout)
        self._owns_client = http_client is None

    async def aclose(self) -> None:
        if self._owns_client:
            await self._client.aclose()

    async def __aenter__(self) -> AsyncRelayFileClient:
        return self

    async def __aexit__(self, *args: object) -> None:
        await self.aclose()

    # ------------------------------------------------------------------
    # Internal HTTP with retry
    # ------------------------------------------------------------------

    async def _request(
        self,
        method: str,
        path: str,
        *,
        headers: dict[str, str] | None = None,
        json_body: Any = None,
        correlation_id: str | None = None,
        base_url: str | None = None,
    ) -> Any:
        resp = await self._request_response(
            method,
            path,
            headers=headers,
            json_body=json_body,
            correlation_id=correlation_id,
            base_url=base_url,
        )
        return _read_payload(resp)

    async def _request_response(
        self,
        method: str,
        path: str,
        *,
        headers: dict[str, str] | None = None,
        json_body: Any = None,
        correlation_id: str | None = None,
        base_url: str | None = None,
    ) -> httpx.Response:
        import asyncio

        base_headers = _merge_headers(
            headers,
            correlation_id=correlation_id,
            user_agent=self._user_agent,
            has_json_body=json_body is not None,
        )

        url = f"{(base_url or self._base_url).rstrip('/')}{path}"
        retries = 0

        while True:
            token = await _resolve_token_async(self._token_provider)
            req_headers = {"Authorization": f"Bearer {token}", **base_headers}

            try:
                resp = await self._client.request(
                    method,
                    url,
                    headers=req_headers,
                    content=json.dumps(json_body).encode()
                    if json_body is not None
                    else None,
                )
            except httpx.TransportError:
                if retries < self._retry.max_retries:
                    retries += 1
                    await asyncio.sleep(
                        _compute_delay(self._retry, retries, None) / 1000
                    )
                    continue
                raise

            if resp.is_success:
                return resp

            payload = _read_payload(resp)

            if _should_retry(resp.status_code, retries, self._retry.max_retries):
                retries += 1
                await asyncio.sleep(
                    _compute_delay(
                        self._retry, retries, resp.headers.get("retry-after")
                    )
                    / 1000
                )
                continue

            _throw_for_error(resp.status_code, payload, resp.headers)

    # ------------------------------------------------------------------
    # Filesystem
    # ------------------------------------------------------------------

    async def list_tree(
        self,
        workspace_id: str,
        *,
        path: str = "/",
        depth: int | None = None,
        cursor: str | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        query = _build_query({"path": path, "depth": depth, "cursor": cursor})
        return await self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/fs/tree{query}",
            correlation_id=correlation_id,
        )

    async def read_file(
        self,
        workspace_id: str,
        path: str,
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        query = _build_query({"path": path})
        return await self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/fs/file{query}",
            correlation_id=correlation_id,
        )

    async def query_files(
        self,
        workspace_id: str,
        *,
        path: str | None = None,
        provider: str | None = None,
        relation: str | None = None,
        permission: str | None = None,
        comment: str | None = None,
        cursor: str | None = None,
        limit: int | None = None,
        properties: dict[str, str] | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        params: dict[str, Any] = {
            "path": path,
            "provider": provider,
            "relation": relation,
            "permission": permission,
            "comment": comment,
            "cursor": cursor,
            "limit": limit,
        }
        if properties:
            for k, v in properties.items():
                if k and v is not None:
                    params[f"property.{k}"] = v
        query = _build_query(params)
        return await self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/fs/query{query}",
            correlation_id=correlation_id,
        )

    async def write_file(self, input: WriteFileInput) -> dict[str, Any]:
        query = _build_query({"path": input.path})
        body: dict[str, Any] = {
            "contentType": input.content_type or "text/markdown",
            "content": input.content,
        }
        if input.semantics is not None:
            body["semantics"] = {
                "properties": input.semantics.properties,
                "relations": input.semantics.relations,
                "permissions": input.semantics.permissions,
                "comments": input.semantics.comments,
            }
        # Single-file PUT expects the target path in the query string, not the JSON body.
        return await self._request(
            "PUT",
            f"/v1/workspaces/{_enc(input.workspace_id)}/fs/file{query}",
            headers={"If-Match": input.base_revision},
            json_body=body,
            correlation_id=input.correlation_id,
        )

    async def bulk_write(
        self,
        workspace_id: str,
        files: list[dict[str, Any]],
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        return await self._request(
            "POST",
            f"/v1/workspaces/{_enc(workspace_id)}/fs/bulk",
            json_body={"files": files},
            correlation_id=correlation_id,
        )

    async def delete_file(self, input: DeleteFileInput) -> dict[str, Any]:
        query = _build_query({"path": input.path})
        return await self._request(
            "DELETE",
            f"/v1/workspaces/{_enc(input.workspace_id)}/fs/file{query}",
            headers={"If-Match": input.base_revision},
            correlation_id=input.correlation_id,
        )

    async def export_workspace(
        self,
        workspace_id: str,
        *,
        format: str = "json",
        correlation_id: str | None = None,
    ) -> list[dict[str, Any]] | bytes:
        query = _build_query({"format": format})
        response = await self._request_response(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/fs/export{query}",
            headers={"Accept": "application/json" if format == "json" else "*/*"},
            correlation_id=correlation_id,
        )
        if format == "json":
            payload = _read_payload(response)
            return payload if isinstance(payload, list) else []
        return response.content

    # ------------------------------------------------------------------
    # Events
    # ------------------------------------------------------------------

    async def get_events(
        self,
        workspace_id: str,
        *,
        provider: str | None = None,
        cursor: str | None = None,
        limit: int | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        """List filesystem events.

        Digest artifacts are reported as ordinary ``file.created`` and
        ``file.updated`` events. ``DIGEST_PATHS`` holds the literal rolling
        anchor paths, including ``digests/today.md``,
        ``digests/yesterday.md``, ``digests/this-week.md``, and
        ``digests/last-week.md``; closed-window daily artifacts use the
        date-stamped form
        ``digests/YYYY-MM-DD.md``. Filter returned events through
        ``relayfile.is_digest_path`` to subscribe to the full taxonomy.
        """

        query = _build_query({"provider": provider, "cursor": cursor, "limit": limit})
        return await self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/fs/events{query}",
            correlation_id=correlation_id,
        )

    # ------------------------------------------------------------------
    # Operations
    # ------------------------------------------------------------------

    async def get_op(
        self,
        workspace_id: str,
        op_id: str,
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        return await self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/ops/{_enc(op_id)}",
            correlation_id=correlation_id,
        )

    async def list_ops(
        self,
        workspace_id: str,
        *,
        status: str | None = None,
        action: str | None = None,
        provider: str | None = None,
        cursor: str | None = None,
        limit: int | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        query = _build_query(
            {
                "status": status,
                "action": action,
                "provider": provider,
                "cursor": cursor,
                "limit": limit,
            }
        )
        return await self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/ops{query}",
            correlation_id=correlation_id,
        )

    async def replay_op(
        self,
        workspace_id: str,
        op_id: str,
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        return await self._request(
            "POST",
            f"/v1/workspaces/{_enc(workspace_id)}/ops/{_enc(op_id)}/replay",
            correlation_id=correlation_id,
        )

    # ------------------------------------------------------------------
    # Sync
    # ------------------------------------------------------------------

    async def get_sync_status(
        self,
        workspace_id: str,
        *,
        provider: str | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        query = _build_query({"provider": provider})
        return await self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/sync/status{query}",
            correlation_id=correlation_id,
        )

    async def get_sync_ingress_status(
        self,
        workspace_id: str,
        *,
        provider: str | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        query = _build_query({"provider": provider})
        return await self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/sync/ingress{query}",
            correlation_id=correlation_id,
        )

    async def get_sync_dead_letters(
        self,
        workspace_id: str,
        *,
        provider: str | None = None,
        cursor: str | None = None,
        limit: int | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        query = _build_query({"provider": provider, "cursor": cursor, "limit": limit})
        return await self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/sync/dead-letter{query}",
            correlation_id=correlation_id,
        )

    async def get_sync_dead_letter(
        self,
        workspace_id: str,
        envelope_id: str,
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        return await self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/sync/dead-letter/{_enc(envelope_id)}",
            correlation_id=correlation_id,
        )

    async def replay_sync_dead_letter(
        self,
        workspace_id: str,
        envelope_id: str,
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        return await self._request(
            "POST",
            f"/v1/workspaces/{_enc(workspace_id)}/sync/dead-letter/{_enc(envelope_id)}/replay",
            correlation_id=correlation_id,
        )

    async def ack_sync_dead_letter(
        self,
        workspace_id: str,
        envelope_id: str,
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        return await self._request(
            "POST",
            f"/v1/workspaces/{_enc(workspace_id)}/sync/dead-letter/{_enc(envelope_id)}/ack",
            correlation_id=correlation_id,
        )

    async def trigger_sync_refresh(
        self,
        workspace_id: str,
        provider: str,
        *,
        reason: str | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        return await self._request(
            "POST",
            f"/v1/workspaces/{_enc(workspace_id)}/sync/refresh",
            json_body={"provider": provider, "reason": reason},
            correlation_id=correlation_id,
        )

    # ------------------------------------------------------------------
    # Webhooks & Writeback
    # ------------------------------------------------------------------

    async def ingest_webhook(self, input: IngestWebhookInput) -> dict[str, Any]:
        body: dict[str, Any] = {
            "provider": input.provider,
            "event_type": input.event_type,
            "path": input.path,
        }
        if input.data is not None:
            body["data"] = input.data
        if input.delivery_id is not None:
            body["delivery_id"] = input.delivery_id
        if input.timestamp is not None:
            body["timestamp"] = input.timestamp
        if input.headers is not None:
            body["headers"] = input.headers
        return await self._request(
            "POST",
            f"/v1/workspaces/{_enc(input.workspace_id)}/webhooks/ingest",
            json_body=body,
            correlation_id=input.correlation_id,
        )

    async def list_writebacks(
        self,
        workspace_id: str,
        *,
        state: WritebackState,
        correlation_id: str | None = None,
    ) -> list[WritebackItem]:
        """Async parity of :meth:`RelayFileClient.list_writebacks`.

        Currently only ``state="pending"`` is wired against the authoritative
        HTTP contract; other states raise :class:`NotImplementedError` until
        the state-filtered endpoint lands (see sync docstring).
        """
        if state != "pending":
            raise NotImplementedError(
                "list_writebacks(state=%r) requires the state-filtered writeback "
                "endpoint, which is not yet present in the authoritative OpenAPI "
                "contract. Track the update-relayfile-cli slice "
                "(workspace-primitives work item 5)." % (state,)
            )
        payload = await self._request(
            "GET",
            f"/v1/workspaces/{_enc(workspace_id)}/writeback/pending",
            correlation_id=correlation_id,
        )
        return _coerce_writeback_items(payload)

    async def list_pending_writebacks(
        self,
        workspace_id: str,
        *,
        correlation_id: str | None = None,
    ) -> list[WritebackItem]:
        return await self.list_writebacks(
            workspace_id,
            state="pending",
            correlation_id=correlation_id,
        )

    async def ack_writeback(self, input: AckWritebackInput) -> dict[str, Any]:
        return await self._request(
            "POST",
            f"/v1/workspaces/{_enc(input.workspace_id)}/writeback/{_enc(input.item_id)}/ack",
            json_body={
                "success": input.success,
                "error": input.error,
                "externalId": input.external_id,
                "canonicalPath": input.canonical_path,
                "providerResult": input.provider_result,
            },
            correlation_id=input.correlation_id,
        )

    # ------------------------------------------------------------------
    # Admin
    # ------------------------------------------------------------------

    async def get_backend_status(
        self, *, correlation_id: str | None = None
    ) -> dict[str, Any]:
        return await self._request(
            "GET", "/v1/admin/backends", correlation_id=correlation_id
        )

    async def get_admin_ingress_status(
        self,
        *,
        workspace_id: str | None = None,
        provider: str | None = None,
        alert_profile: str | None = None,
        pending_threshold: int | None = None,
        dead_letter_threshold: int | None = None,
        stale_threshold: int | None = None,
        drop_rate_threshold: float | None = None,
        non_zero_only: bool | None = None,
        max_alerts: int | None = None,
        cursor: str | None = None,
        limit: int | None = None,
        include_workspaces: bool | None = None,
        include_alerts: bool | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        query = _build_query(
            {
                "workspaceId": workspace_id,
                "provider": provider,
                "alertProfile": alert_profile,
                "pendingThreshold": pending_threshold,
                "deadLetterThreshold": dead_letter_threshold,
                "staleThreshold": stale_threshold,
                "dropRateThreshold": drop_rate_threshold,
                "nonZeroOnly": non_zero_only,
                "maxAlerts": max_alerts,
                "cursor": cursor,
                "limit": limit,
                "includeWorkspaces": include_workspaces,
                "includeAlerts": include_alerts,
            }
        )
        return await self._request(
            "GET", f"/v1/admin/ingress{query}", correlation_id=correlation_id
        )

    async def get_admin_sync_status(
        self,
        *,
        workspace_id: str | None = None,
        provider: str | None = None,
        non_zero_only: bool | None = None,
        cursor: str | None = None,
        limit: int | None = None,
        include_workspaces: bool | None = None,
        status_error_threshold: int | None = None,
        lag_seconds_threshold: int | None = None,
        dead_lettered_envelopes_threshold: int | None = None,
        dead_lettered_ops_threshold: int | None = None,
        max_alerts: int | None = None,
        include_alerts: bool | None = None,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        query = _build_query(
            {
                "workspaceId": workspace_id,
                "provider": provider,
                "nonZeroOnly": non_zero_only,
                "cursor": cursor,
                "limit": limit,
                "includeWorkspaces": include_workspaces,
                "statusErrorThreshold": status_error_threshold,
                "lagSecondsThreshold": lag_seconds_threshold,
                "deadLetteredEnvelopesThreshold": dead_lettered_envelopes_threshold,
                "deadLetteredOpsThreshold": dead_lettered_ops_threshold,
                "maxAlerts": max_alerts,
                "includeAlerts": include_alerts,
            }
        )
        return await self._request(
            "GET", f"/v1/admin/sync{query}", correlation_id=correlation_id
        )

    async def replay_admin_envelope(
        self,
        envelope_id: str,
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        return await self._request(
            "POST",
            f"/v1/admin/replay/envelope/{_enc(envelope_id)}",
            correlation_id=correlation_id,
        )

    async def replay_admin_op(
        self,
        op_id: str,
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        return await self._request(
            "POST",
            f"/v1/admin/replay/op/{_enc(op_id)}",
            correlation_id=correlation_id,
        )

    # ------------------------------------------------------------------
    # Integration setup (cloud control plane)
    # ------------------------------------------------------------------

    async def list_accessible_resources(
        self,
        workspace_id: str,
        provider: str,
        *,
        correlation_id: str | None = None,
    ) -> list[dict[str, Any]]:
        """Async version of :meth:`RelayFileClient.list_accessible_resources`."""
        normalized = _normalize_provider_id(provider)
        payload = await self._request(
            "GET",
            f"/api/v1/workspaces/{_enc(workspace_id)}/integrations/{_enc(normalized)}/accessible-resources",
            correlation_id=correlation_id,
            base_url=self._cloud_base_url,
        )
        return _coerce_resource_entries(payload)

    async def set_integration_metadata(
        self,
        workspace_id: str,
        provider: str,
        metadata: dict[str, Any],
        *,
        correlation_id: str | None = None,
    ) -> dict[str, Any]:
        """Async version of :meth:`RelayFileClient.set_integration_metadata`."""
        if not isinstance(metadata, dict):
            raise ValueError("metadata must be a dict")
        normalized = _normalize_provider_id(provider)
        payload = await self._request(
            "PUT",
            f"/api/v1/workspaces/{_enc(workspace_id)}/integrations/{_enc(normalized)}/metadata",
            json_body={"metadata": metadata},
            correlation_id=correlation_id,
            base_url=self._cloud_base_url,
        )
        return _coerce_metadata_response(payload)
