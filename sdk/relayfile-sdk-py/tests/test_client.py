"""Tests for the RelayFile Python SDK client."""
from __future__ import annotations

import json

import httpx
import pytest
import respx

from relayfile import (
    RelayFileClient,
    AsyncRelayFileClient,
    RelayFileApiError,
    RevisionConflictError,
    QueueFullError,
    InvalidStateError,
    PayloadTooLargeError,
)
from relayfile.types import (
    IngestWebhookInput,
    WriteFileInput,
    AckWritebackInput,
)

BASE = "https://relay.test"


# ---------------------------------------------------------------------------
# Sync client tests
# ---------------------------------------------------------------------------


class TestRelayFileClient:
    def _client(self) -> RelayFileClient:
        return RelayFileClient(BASE, "tok_test")

    @respx.mock
    def test_list_tree(self) -> None:
        payload = {"path": "/", "entries": [{"path": "/zendesk", "type": "dir", "revision": "rev_1"}], "nextCursor": None}
        respx.get(f"{BASE}/v1/workspaces/ws_acme/fs/tree").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = self._client()
        res = client.list_tree("ws_acme")
        assert len(res["entries"]) == 1
        assert res["entries"][0]["path"] == "/zendesk"

    @respx.mock
    def test_list_tree_params(self) -> None:
        respx.get(f"{BASE}/v1/workspaces/ws_acme/fs/tree").mock(
            return_value=httpx.Response(200, json={"path": "/", "entries": [], "nextCursor": None})
        )
        client = self._client()
        client.list_tree("ws_acme", path="/zendesk", depth=2, cursor="abc")
        req = respx.calls.last.request
        assert b"depth=2" in req.url.raw_path
        assert b"cursor=abc" in req.url.raw_path

    @respx.mock
    def test_read_file(self) -> None:
        payload = {"path": "/f.json", "revision": "rev_3", "contentType": "application/json", "content": '{"id":1}'}
        respx.get(f"{BASE}/v1/workspaces/ws_acme/fs/file").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = self._client()
        res = client.read_file("ws_acme", "/f.json")
        assert res["content"] == '{"id":1}'

    @respx.mock
    def test_write_file(self) -> None:
        payload = {"opId": "op_1", "status": "queued", "targetRevision": "rev_4"}
        respx.put(f"{BASE}/v1/workspaces/ws_acme/fs/file").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = self._client()
        inp = WriteFileInput(workspace_id="ws_acme", path="/f.json", base_revision="rev_3", content="{}")
        res = client.write_file(inp)
        assert res["opId"] == "op_1"
        req = respx.calls.last.request
        assert req.method == "PUT"
        assert req.headers["If-Match"] == "rev_3"

    @respx.mock
    def test_query_files(self) -> None:
        payload = {"items": [], "nextCursor": None}
        respx.get(f"{BASE}/v1/workspaces/ws_acme/fs/query").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = self._client()
        res = client.query_files("ws_acme", provider="zendesk", properties={"provider.status": "open"})
        assert res["items"] == []
        req = respx.calls.last.request
        assert b"provider=zendesk" in req.url.raw_path
        assert b"property.provider.status=open" in req.url.raw_path

    @respx.mock
    def test_get_events(self) -> None:
        payload = {"events": [], "nextCursor": None}
        respx.get(f"{BASE}/v1/workspaces/ws_acme/fs/events").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = self._client()
        res = client.get_events("ws_acme", provider="github", limit=10)
        assert res["events"] == []

    @respx.mock
    def test_get_op(self) -> None:
        payload = {"opId": "op_1", "status": "succeeded", "attemptCount": 1}
        respx.get(f"{BASE}/v1/workspaces/ws_acme/ops/op_1").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = self._client()
        res = client.get_op("ws_acme", "op_1")
        assert res["status"] == "succeeded"

    @respx.mock
    def test_list_ops(self) -> None:
        payload = {"items": [], "nextCursor": None}
        respx.get(f"{BASE}/v1/workspaces/ws_acme/ops").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = self._client()
        client.list_ops("ws_acme", status="failed")
        req = respx.calls.last.request
        assert b"status=failed" in req.url.raw_path

    @respx.mock
    def test_replay_op(self) -> None:
        payload = {"status": "queued", "id": "op_1"}
        respx.post(f"{BASE}/v1/workspaces/ws_acme/ops/op_1/replay").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = self._client()
        res = client.replay_op("ws_acme", "op_1")
        assert res["status"] == "queued"

    @respx.mock
    def test_get_backend_status(self) -> None:
        payload = {
            "backendProfile": "memory", "stateBackend": "memory://",
            "envelopeQueue": "memory://", "envelopeQueueDepth": 0,
            "envelopeQueueCapacity": 1000, "writebackQueue": "memory://",
            "writebackQueueDepth": 0, "writebackQueueCapacity": 100,
        }
        respx.get(f"{BASE}/v1/admin/backends").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = self._client()
        res = client.get_backend_status()
        assert res["backendProfile"] == "memory"

    @respx.mock
    def test_replay_admin_envelope(self) -> None:
        payload = {"status": "queued", "id": "env_1"}
        respx.post(f"{BASE}/v1/admin/replay/envelope/env_1").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = self._client()
        res = client.replay_admin_envelope("env_1")
        assert res["status"] == "queued"

    @respx.mock
    def test_get_sync_status(self) -> None:
        payload = {"workspaceId": "ws_acme", "providers": [{"provider": "zendesk", "status": "healthy"}]}
        respx.get(f"{BASE}/v1/workspaces/ws_acme/sync/status").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = self._client()
        res = client.get_sync_status("ws_acme")
        assert len(res["providers"]) == 1

    @respx.mock
    def test_trigger_sync_refresh(self) -> None:
        payload = {"status": "queued", "id": "ref_1"}
        respx.post(f"{BASE}/v1/workspaces/ws_acme/sync/refresh").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = self._client()
        client.trigger_sync_refresh("ws_acme", "zendesk", reason="manual")
        body = json.loads(respx.calls.last.request.content)
        assert body["provider"] == "zendesk"
        assert body["reason"] == "manual"


# ---------------------------------------------------------------------------
# Webhook / writeback methods
# ---------------------------------------------------------------------------


class TestWebhookWriteback:
    def _client(self) -> RelayFileClient:
        return RelayFileClient(BASE, "tok_test")

    @respx.mock
    def test_ingest_webhook(self) -> None:
        payload = {"status": "queued", "id": "env_abc"}
        respx.post(f"{BASE}/v1/workspaces/ws_acme/webhooks/ingest").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = self._client()
        inp = IngestWebhookInput(
            workspace_id="ws_acme",
            provider="zendesk",
            event_type="file.updated",
            path="/zendesk/tickets/48291.json",
            data={"content": '{"id":48291}'},
            delivery_id="nango_evt_abc123",
        )
        res = client.ingest_webhook(inp)
        assert res["status"] == "queued"
        body = json.loads(respx.calls.last.request.content)
        assert body["provider"] == "zendesk"
        assert body["event_type"] == "file.updated"
        assert body["path"] == "/zendesk/tickets/48291.json"

    @respx.mock
    def test_ingest_webhook_optional_fields(self) -> None:
        payload = {"status": "queued", "id": "env_def"}
        respx.post(f"{BASE}/v1/workspaces/ws_acme/webhooks/ingest").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = self._client()
        inp = IngestWebhookInput(
            workspace_id="ws_acme",
            provider="github",
            event_type="file.created",
            path="/github/issues/42.json",
            data={"number": 42},
            timestamp="2026-03-14T12:00:00Z",
            headers={"X-GitHub-Event": "issues"},
        )
        client.ingest_webhook(inp)
        body = json.loads(respx.calls.last.request.content)
        assert body["timestamp"] == "2026-03-14T12:00:00Z"
        assert body["headers"]["X-GitHub-Event"] == "issues"

    @respx.mock
    def test_list_pending_writebacks(self) -> None:
        payload = [{"id": "wb_1", "workspaceId": "ws_acme", "path": "/zendesk/tickets/48291.json", "revision": "rev_5"}]
        respx.get(f"{BASE}/v1/workspaces/ws_acme/writeback/pending").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = self._client()
        res = client.list_pending_writebacks("ws_acme")
        assert len(res) == 1
        assert res[0]["path"] == "/zendesk/tickets/48291.json"

    @respx.mock
    def test_ack_writeback_success(self) -> None:
        payload = {"status": "acknowledged", "id": "wb_1", "success": True}
        respx.post(f"{BASE}/v1/workspaces/ws_acme/writeback/wb_1/ack").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = self._client()
        inp = AckWritebackInput(workspace_id="ws_acme", item_id="wb_1", success=True)
        res = client.ack_writeback(inp)
        assert res["status"] == "acknowledged"

    @respx.mock
    def test_ack_writeback_failure(self) -> None:
        payload = {"status": "acknowledged", "id": "wb_2", "success": False}
        respx.post(f"{BASE}/v1/workspaces/ws_acme/writeback/wb_2/ack").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = self._client()
        inp = AckWritebackInput(workspace_id="ws_acme", item_id="wb_2", success=False, error="Provider returned 403")
        client.ack_writeback(inp)
        body = json.loads(respx.calls.last.request.content)
        assert body["success"] is False
        assert body["error"] == "Provider returned 403"


# ---------------------------------------------------------------------------
# Error handling
# ---------------------------------------------------------------------------


class TestErrorHandling:
    def _client(self) -> RelayFileClient:
        return RelayFileClient(BASE, "tok_test")

    @respx.mock
    def test_revision_conflict_error(self) -> None:
        body = {
            "code": "revision_conflict", "message": "Conflict",
            "expectedRevision": "rev_old", "currentRevision": "rev_new",
        }
        respx.put(f"{BASE}/v1/workspaces/ws_1/fs/file").mock(
            return_value=httpx.Response(409, json=body)
        )
        client = self._client()
        with pytest.raises(RevisionConflictError) as exc_info:
            client.write_file(WriteFileInput(workspace_id="ws_1", path="/f.json", base_revision="rev_old", content="{}"))
        assert exc_info.value.expected_revision == "rev_old"
        assert exc_info.value.current_revision == "rev_new"

    @respx.mock
    def test_invalid_state_error(self) -> None:
        body = {"code": "invalid_state", "message": "Bad state"}
        respx.post(f"{BASE}/v1/workspaces/ws_1/ops/op_1/replay").mock(
            return_value=httpx.Response(409, json=body)
        )
        client = self._client()
        with pytest.raises(InvalidStateError):
            client.replay_op("ws_1", "op_1")

    @respx.mock
    def test_queue_full_error(self) -> None:
        body = {"code": "queue_full", "message": "Full"}
        respx.put(f"{BASE}/v1/workspaces/ws_1/fs/file").mock(
            return_value=httpx.Response(429, json=body, headers={"retry-after": "5"})
        )
        client = self._client()
        with pytest.raises(QueueFullError) as exc_info:
            client.write_file(WriteFileInput(workspace_id="ws_1", path="/f.json", base_revision="rev_1", content="{}"))
        assert exc_info.value.retry_after_seconds == 5

    @respx.mock
    def test_payload_too_large_error(self) -> None:
        body = {"code": "payload_too_large", "message": "Too big"}
        respx.put(f"{BASE}/v1/workspaces/ws_1/fs/file").mock(
            return_value=httpx.Response(413, json=body)
        )
        client = self._client()
        with pytest.raises(PayloadTooLargeError):
            client.write_file(WriteFileInput(workspace_id="ws_1", path="/f.json", base_revision="rev_1", content="x" * 10000))

    @respx.mock
    def test_generic_api_error(self) -> None:
        body = {"code": "not_found", "message": "Not found"}
        respx.get(f"{BASE}/v1/workspaces/ws_1/fs/file").mock(
            return_value=httpx.Response(404, json=body)
        )
        client = self._client()
        with pytest.raises(RelayFileApiError):
            client.read_file("ws_1", "/nope.json")


# ---------------------------------------------------------------------------
# Auth & headers
# ---------------------------------------------------------------------------


class TestAuthHeaders:
    @respx.mock
    def test_bearer_token(self) -> None:
        respx.get(f"{BASE}/v1/workspaces/ws_1/fs/tree").mock(
            return_value=httpx.Response(200, json={"path": "/", "entries": [], "nextCursor": None})
        )
        client = RelayFileClient(BASE, "tok_test")
        client.list_tree("ws_1")
        req = respx.calls.last.request
        assert req.headers["Authorization"] == "Bearer tok_test"

    @respx.mock
    def test_correlation_id(self) -> None:
        respx.get(f"{BASE}/v1/workspaces/ws_1/fs/tree").mock(
            return_value=httpx.Response(200, json={"path": "/", "entries": [], "nextCursor": None})
        )
        client = RelayFileClient(BASE, "tok_test")
        client.list_tree("ws_1", correlation_id="corr_custom")
        req = respx.calls.last.request
        assert req.headers["X-Correlation-Id"] == "corr_custom"

    @respx.mock
    def test_auto_correlation_id(self) -> None:
        respx.get(f"{BASE}/v1/workspaces/ws_1/fs/tree").mock(
            return_value=httpx.Response(200, json={"path": "/", "entries": [], "nextCursor": None})
        )
        client = RelayFileClient(BASE, "tok_test")
        client.list_tree("ws_1")
        req = respx.calls.last.request
        assert req.headers["X-Correlation-Id"].startswith("rf_")

    @respx.mock
    def test_custom_user_agent(self) -> None:
        respx.get(f"{BASE}/v1/workspaces/ws_1/fs/tree").mock(
            return_value=httpx.Response(200, json={"path": "/", "entries": [], "nextCursor": None})
        )
        client = RelayFileClient(BASE, "tok_test", user_agent="my-agent/1.0")
        client.list_tree("ws_1")
        req = respx.calls.last.request
        assert req.headers["User-Agent"] == "my-agent/1.0"

    @respx.mock
    def test_trailing_slash_stripped(self) -> None:
        respx.get(f"{BASE}/v1/workspaces/ws_1/fs/tree").mock(
            return_value=httpx.Response(200, json={"path": "/", "entries": [], "nextCursor": None})
        )
        client = RelayFileClient(BASE + "///", "tok_test")
        client.list_tree("ws_1")
        req = respx.calls.last.request
        assert str(req.url).startswith(f"{BASE}/v1/")


# ---------------------------------------------------------------------------
# Nango helpers
# ---------------------------------------------------------------------------


class TestNangoHelpers:
    @respx.mock
    def test_ingest_nango_webhook(self) -> None:
        from relayfile.nango import NangoHelpers

        payload = {"status": "queued", "id": "env_1"}
        respx.post(f"{BASE}/v1/workspaces/ws_acme/webhooks/ingest").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = RelayFileClient(BASE, "tok_test")
        nango = NangoHelpers(client)
        nango.ingest_nango_webhook(
            "ws_acme",
            connection_id="conn_zendesk_acme",
            integration_id="zendesk-support",
            provider_config_key="zendesk",
            model="tickets",
            object_id="48291",
            event_type="updated",
            payload={"id": 48291, "status": "open"},
        )
        body = json.loads(respx.calls.last.request.content)
        assert body["path"] == "/zendesk/tickets/48291.json"
        assert body["provider"] == "zendesk"
        assert body["event_type"] == "updated"
        assert body["headers"]["X-Nango-Connection-Id"] == "conn_zendesk_acme"
        assert body["data"]["semantics"]["properties"]["nango.connection_id"] == "conn_zendesk_acme"
        assert body["data"]["semantics"]["properties"]["provider.object_type"] == "tickets"
        assert body["data"]["semantics"]["properties"]["provider.status"] == "open"

    @respx.mock
    def test_ingest_nango_webhook_unknown_provider(self) -> None:
        from relayfile.nango import NangoHelpers

        payload = {"status": "queued", "id": "env_2"}
        respx.post(f"{BASE}/v1/workspaces/ws_acme/webhooks/ingest").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = RelayFileClient(BASE, "tok_test")
        nango = NangoHelpers(client)
        nango.ingest_nango_webhook(
            "ws_acme",
            connection_id="conn_1",
            integration_id="notion-sync",
            model="pages",
            object_id="page_1",
            event_type="created",
            payload={"title": "My Page"},
        )
        body = json.loads(respx.calls.last.request.content)
        assert body["path"] == "/notion/pages/page_1.json"
        assert body["provider"] == "notion"
        assert "X-Nango-Provider-Config-Key" not in body["headers"]

    @respx.mock
    def test_ingest_nango_webhook_relations(self) -> None:
        from relayfile.nango import NangoHelpers

        payload = {"status": "queued", "id": "env_3"}
        respx.post(f"{BASE}/v1/workspaces/ws_acme/webhooks/ingest").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = RelayFileClient(BASE, "tok_test")
        nango = NangoHelpers(client)
        nango.ingest_nango_webhook(
            "ws_acme",
            connection_id="conn_1",
            integration_id="zendesk-support",
            model="tickets",
            object_id="100",
            event_type="updated",
            payload={"id": 100},
            relations=["/zendesk/users/42.json"],
        )
        body = json.loads(respx.calls.last.request.content)
        assert body["data"]["semantics"]["relations"] == ["/zendesk/users/42.json"]

    @respx.mock
    def test_get_provider_files(self) -> None:
        from relayfile.nango import NangoHelpers

        payload = {
            "items": [{"path": "/zendesk/tickets/1.json", "revision": "rev_1", "contentType": "application/json", "size": 100}],
            "nextCursor": None,
        }
        respx.get(f"{BASE}/v1/workspaces/ws_acme/fs/query").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = RelayFileClient(BASE, "tok_test")
        nango = NangoHelpers(client)
        files = nango.get_provider_files("ws_acme", provider="zendesk", object_type="tickets", status="open")
        assert len(files) == 1
        assert files[0]["path"] == "/zendesk/tickets/1.json"
        req = respx.calls.last.request
        assert b"provider=zendesk" in req.url.raw_path
        assert b"property.provider.object_type=tickets" in req.url.raw_path
        assert b"property.provider.status=open" in req.url.raw_path


# ---------------------------------------------------------------------------
# Async client (basic smoke test)
# ---------------------------------------------------------------------------


class TestAsyncClient:
    @respx.mock
    @pytest.mark.asyncio
    async def test_list_tree(self) -> None:
        payload = {"path": "/", "entries": [], "nextCursor": None}
        respx.get(f"{BASE}/v1/workspaces/ws_1/fs/tree").mock(
            return_value=httpx.Response(200, json=payload)
        )
        async with AsyncRelayFileClient(BASE, "tok_test") as client:
            res = await client.list_tree("ws_1")
            assert res["entries"] == []

    @respx.mock
    @pytest.mark.asyncio
    async def test_ingest_webhook(self) -> None:
        payload = {"status": "queued", "id": "env_1"}
        respx.post(f"{BASE}/v1/workspaces/ws_acme/webhooks/ingest").mock(
            return_value=httpx.Response(200, json=payload)
        )
        async with AsyncRelayFileClient(BASE, "tok_test") as client:
            inp = IngestWebhookInput(
                workspace_id="ws_acme", provider="zendesk",
                event_type="file.updated", path="/zendesk/tickets/1.json",
                data={"id": 1},
            )
            res = await client.ingest_webhook(inp)
            assert res["status"] == "queued"
