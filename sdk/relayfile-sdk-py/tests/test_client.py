"""Tests for the RelayFile Python SDK client."""
from __future__ import annotations

import json

import httpx
import pytest
import respx

from relayfile import (
    AsyncRelayFileClient,
    ComposioProvider,
    IntegrationProvider,
    InvalidStateError,
    PayloadTooLargeError,
    QueueFullError,
    RelayFileApiError,
    RelayFileClient,
    RevisionConflictError,
    compute_canonical_path,
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
    def test_bulk_write(self) -> None:
        payload = {"written": 2, "errorCount": 0, "errors": [], "correlationId": "corr_bulk"}
        respx.post(f"{BASE}/v1/workspaces/ws_acme/fs/bulk").mock(
            return_value=httpx.Response(202, json=payload)
        )
        client = self._client()
        res = client.bulk_write(
            "ws_acme",
            [
                {"path": "/a.md", "contentType": "text/markdown", "content": "# A"},
                {"path": "/b.md", "contentType": "text/markdown", "content": "# B"},
            ],
            correlation_id="corr_bulk",
        )
        assert res["written"] == 2
        req = respx.calls.last.request
        assert req.method == "POST"
        assert req.headers["X-Correlation-Id"] == "corr_bulk"
        assert json.loads(req.content)["files"][0]["path"] == "/a.md"

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
    def test_export_workspace_json(self) -> None:
        payload = [
            {"path": "/a.md", "revision": "rev_1", "contentType": "text/markdown", "content": "# A"},
            {"path": "/b.md", "revision": "rev_2", "contentType": "text/markdown", "content": "# B"},
        ]
        respx.get(f"{BASE}/v1/workspaces/ws_acme/fs/export").mock(
            return_value=httpx.Response(200, json=payload)
        )
        client = self._client()
        res = client.export_workspace("ws_acme")
        assert len(res) == 2
        req = respx.calls.last.request
        assert b"format=json" in req.url.raw_path
        assert req.headers["Accept"] == "application/json"

    @respx.mock
    def test_export_workspace_tar(self) -> None:
        payload = b"tar-bytes"
        respx.get(f"{BASE}/v1/workspaces/ws_acme/fs/export").mock(
            return_value=httpx.Response(200, content=payload, headers={"content-type": "application/gzip"})
        )
        client = self._client()
        res = client.export_workspace("ws_acme", format="tar")
        assert res == payload
        req = respx.calls.last.request
        assert b"format=tar" in req.url.raw_path
        assert req.headers["Accept"] == "*/*"

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
# Provider abstractions
# ---------------------------------------------------------------------------


class TestProviderAbstractions:
    def test_compute_canonical_path_known_provider(self) -> None:
        assert compute_canonical_path("github", "issues", "42") == "/github/issues/42.json"

    def test_compute_canonical_path_unknown_provider(self) -> None:
        assert compute_canonical_path("custom", "events", "abc") == "/custom/events/abc.json"

    def test_nango_helpers_extend_integration_provider(self) -> None:
        from relayfile.nango import NangoHelpers

        nango = NangoHelpers(RelayFileClient(BASE, "tok_test"))
        assert isinstance(nango, IntegrationProvider)


# ---------------------------------------------------------------------------
# Composio helpers
# ---------------------------------------------------------------------------


class TestComposioProvider:
    def _provider(self) -> ComposioProvider:
        return ComposioProvider(RelayFileClient(BASE, "tok_test"))

    @respx.mock
    def test_ingest_github_commit_event(self) -> None:
        payload = {"status": "queued", "id": "env_1"}
        respx.post(f"{BASE}/v1/workspaces/ws_1/webhooks/ingest").mock(
            return_value=httpx.Response(200, json=payload)
        )
        provider = self._provider()
        provider.ingest_webhook(
            "ws_1",
            {
                "type": "composio.trigger.message",
                "metadata": {
                    "trigger_slug": "GITHUB_COMMIT_EVENT",
                    "trigger_id": "trig_abc123",
                    "connected_account_id": "ca_xyz789",
                    "user_id": "user_42",
                    "toolkit": "github",
                },
                "data": {
                    "id": "sha-abc123",
                    "author": "khaliq",
                    "message": "fix: update billing logic",
                },
            },
        )

        body = json.loads(respx.calls.last.request.content)
        assert body["provider"] == "github"
        assert body["event_type"] == "event"
        assert body["path"] == "/github/commits/sha-abc123.json"
        assert body["headers"]["X-Composio-Trigger-Id"] == "trig_abc123"
        assert body["headers"]["X-Composio-User-Id"] == "user_42"
        assert body["data"]["semantics"]["properties"]["provider.object_type"] == "commits"
        assert body["data"]["semantics"]["properties"]["provider.object_id"] == "sha-abc123"

    @respx.mock
    def test_ingest_account_expired_event(self) -> None:
        payload = {"status": "queued", "id": "env_2"}
        respx.post(f"{BASE}/v1/workspaces/ws_1/webhooks/ingest").mock(
            return_value=httpx.Response(200, json=payload)
        )
        provider = self._provider()
        provider.ingest_webhook(
            "ws_1",
            {
                "type": "composio.connected_account.expired",
                "metadata": {
                    "trigger_slug": "",
                    "trigger_id": "",
                    "connected_account_id": "ca_expired1",
                    "user_id": "user_10",
                    "toolkit": "github",
                },
                "data": {},
            },
        )

        body = json.loads(respx.calls.last.request.content)
        assert body["event_type"] == "account_expired"
        assert body["path"] == "/.system/composio/expired/ca_expired1.json"
        assert body["data"]["connected_account_id"] == "ca_expired1"

    @respx.mock
    def test_ingest_unknown_trigger_infers_object_type(self) -> None:
        payload = {"status": "queued", "id": "env_3"}
        respx.post(f"{BASE}/v1/workspaces/ws_1/webhooks/ingest").mock(
            return_value=httpx.Response(200, json=payload)
        )
        provider = self._provider()
        provider.ingest_webhook(
            "ws_1",
            {
                "type": "composio.trigger.message",
                "metadata": {
                    "trigger_slug": "NOTION_DATABASE_ROW_CREATED",
                    "trigger_id": "trig_notion1",
                    "connected_account_id": "ca_notion1",
                    "toolkit": "notion",
                },
                "data": {"id": "row-789", "title": "New task"},
            },
        )

        body = json.loads(respx.calls.last.request.content)
        assert body["path"] == "/notion/database_rows/row-789.json"
        assert body["event_type"] == "created"

    @respx.mock
    def test_ingest_nested_object_id(self) -> None:
        payload = {"status": "queued", "id": "env_4"}
        respx.post(f"{BASE}/v1/workspaces/ws_1/webhooks/ingest").mock(
            return_value=httpx.Response(200, json=payload)
        )
        provider = self._provider()
        provider.ingest_webhook(
            "ws_1",
            {
                "type": "composio.trigger.message",
                "metadata": {
                    "trigger_slug": "GITHUB_ISSUE_EVENT",
                    "trigger_id": "trig_issue1",
                    "connected_account_id": "ca_gh1",
                    "toolkit": "github",
                },
                "data": {"issue": {"id": "issue-999", "title": "Bug report"}},
            },
        )

        body = json.loads(respx.calls.last.request.content)
        assert body["path"] == "/github/issues/issue-999.json"

    def test_normalize(self) -> None:
        provider = self._provider()
        normalized = provider.normalize(
            {
                "type": "composio.trigger.message",
                "metadata": {
                    "trigger_slug": "SLACK_NEW_MESSAGE",
                    "trigger_id": "trig_slack1",
                    "connected_account_id": "ca_slack1",
                    "toolkit": "slack",
                },
                "data": {"id": "msg-123", "text": "Hello from Slack!"},
            }
        )

        assert normalized.provider == "slack"
        assert normalized.object_type == "messages"
        assert normalized.object_id == "msg-123"
        assert normalized.event_type == "created"
        assert normalized.metadata is not None
        assert normalized.metadata["composio.trigger_id"] == "trig_slack1"

    @respx.mock
    def test_get_provider_files(self) -> None:
        payload = {
            "items": [
                {
                    "path": "/github/commits/sha-1.json",
                    "revision": "rev_1",
                    "contentType": "application/json",
                    "size": 100,
                }
            ],
            "nextCursor": None,
        }
        respx.get(f"{BASE}/v1/workspaces/ws_1/fs/query").mock(
            return_value=httpx.Response(200, json=payload)
        )
        provider = self._provider()
        files = provider.get_provider_files("ws_1", provider="github", object_type="commits")
        assert len(files) == 1
        req = respx.calls.last.request
        assert b"provider=github" in req.url.raw_path
        assert b"property.provider.object_type=commits" in req.url.raw_path

    def test_name(self) -> None:
        assert self._provider().name == "composio"


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
    async def test_bulk_write(self) -> None:
        payload = {"written": 1, "errorCount": 0, "errors": [], "correlationId": "corr_bulk"}
        respx.post(f"{BASE}/v1/workspaces/ws_acme/fs/bulk").mock(
            return_value=httpx.Response(202, json=payload)
        )
        async with AsyncRelayFileClient(BASE, "tok_test") as client:
            res = await client.bulk_write(
                "ws_acme",
                [{"path": "/a.md", "contentType": "text/markdown", "content": "# A"}],
                correlation_id="corr_bulk",
            )
            assert res["written"] == 1

    @respx.mock
    @pytest.mark.asyncio
    async def test_export_workspace_json(self) -> None:
        payload = [{"path": "/a.md", "revision": "rev_1", "contentType": "text/markdown", "content": "# A"}]
        respx.get(f"{BASE}/v1/workspaces/ws_acme/fs/export").mock(
            return_value=httpx.Response(200, json=payload)
        )
        async with AsyncRelayFileClient(BASE, "tok_test") as client:
            res = await client.export_workspace("ws_acme")
            assert len(res) == 1

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
