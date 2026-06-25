from __future__ import annotations

import json
from typing import Any

import httpx
import pytest
import respx

from relayfile import (
    CloudApiError,
    IntegrationConnectionTimeoutError,
    MalformedCloudResponseError,
    RelayfileSetup,
    RelayfileSetupRetryOptions,
    SelfHostConnect,
    WorkspacePermissions,
)


CLOUD = "https://cloud.test/base"


def join_payload(token: str = "rf_jwt_1") -> dict[str, Any]:
    return {
        "workspaceId": "ws_123",
        "token": token,
        "relayfileUrl": "https://relayfile.test",
        "wsUrl": "wss://relayfile.test/ws",
        "relaycastApiKey": "rc_test",
    }


@respx.mock
def test_create_workspace_then_join_preserves_cloud_base_path() -> None:
    respx.post(f"{CLOUD}/api/v1/workspaces").mock(
        return_value=httpx.Response(
            200,
            json={
                "workspaceId": "ws_123",
                "createdAt": "2026-04-30T00:00:00.000Z",
                "name": "sdk-workspace",
            },
        )
    )
    respx.post(f"{CLOUD}/api/v1/workspaces/ws_123/join").mock(
        return_value=httpx.Response(200, json=join_payload())
    )

    setup = RelayfileSetup(cloud_api_url=CLOUD, access_token="cld_token")
    handle = setup.create_workspace(
        name="sdk-workspace",
        permissions=WorkspacePermissions(readonly=["/docs/**"]),
    )

    assert handle.workspace_id == "ws_123"
    assert handle.info.name == "sdk-workspace"
    create_req = respx.calls[0].request
    join_req = respx.calls[1].request
    assert create_req.headers["Authorization"] == "Bearer cld_token"
    assert create_req.headers["X-Relayfile-SDK-Version"]
    assert json.loads(create_req.content)["permissions"] == {"readonly": ["/docs/**"]}
    assert json.loads(join_req.content)["agentName"] == "sdk-agent"


@respx.mock
def test_workspace_connect_adopt_metadata_and_invite_flow() -> None:
    respx.post(f"{CLOUD}/api/v1/workspaces/ws_123/join").mock(
        return_value=httpx.Response(200, json=join_payload())
    )
    respx.post(f"{CLOUD}/api/v1/workspaces/ws_123/integrations/connect-session").mock(
        return_value=httpx.Response(
            200,
            json={
                "token": "session_token",
                "expiresAt": "2026-04-30T01:00:00.000Z",
                "connectLink": "https://nango.test/connect",
                "connectionId": "conn_123",
            },
        )
    )
    respx.get(
        f"{CLOUD}/api/v1/workspaces/ws_123/integrations/github/status",
        params={"connectionId": "conn_123"},
    ).mock(return_value=httpx.Response(200, json={"ready": True}))
    respx.post(f"{CLOUD}/api/v1/workspaces/ws_123/integrations/github/adopt").mock(
        return_value=httpx.Response(
            200,
            json={
                "ok": True,
                "connectionId": "conn_new",
                "replacedConnectionId": "conn_old",
            },
        )
    )
    respx.get(
        f"{CLOUD}/api/v1/workspaces/ws_123/integrations/jira/accessible-resources"
    ).mock(
        return_value=httpx.Response(
            200,
            json={
                "resources": [
                    {"id": "cloud-1", "url": "https://foo.atlassian.net"},
                    {"id": "cloud-2"},
                ]
            },
        )
    )
    respx.put(f"{CLOUD}/api/v1/workspaces/ws_123/integrations/jira/metadata").mock(
        return_value=httpx.Response(200, json={"metadata": {"cloudId": "cloud-1"}})
    )

    setup = RelayfileSetup(cloud_api_url=CLOUD)
    handle = setup.join_workspace("ws_123")
    connect = handle.connect_integration("github")
    assert connect.connection_id == "conn_123"
    assert connect.session_token == "session_token"
    assert handle.is_connected("github", "conn_123") is True
    assert handle.adopt_integration("github", "conn_new") == {
        "connectionId": "conn_new",
        "replacedConnectionId": "conn_old",
    }
    assert handle.list_accessible_resources("jira") == [
        {"id": "cloud-1", "url": "https://foo.atlassian.net"}
    ]
    assert handle.set_integration_metadata("jira", {"cloudId": "cloud-1"}) == {
        "cloudId": "cloud-1"
    }
    assert handle.agent_invite(agent_name="peer").to_dict()["relayfileToken"] == "rf_jwt_1"

    connect_req = respx.calls[1].request
    assert connect_req.headers["Authorization"] == "Bearer rf_jwt_1"
    assert json.loads(connect_req.content)["allowedIntegrations"] == ["github"]


@respx.mock
def test_cloud_errors_surface_as_cloud_api_error() -> None:
    respx.post(f"{CLOUD}/api/v1/workspaces/ws_123/join").mock(
        return_value=httpx.Response(
            409,
            json={
                "ok": False,
                "code": "workspace_mismatch",
                "error": "connection belongs to another workspace",
            },
        )
    )

    setup = RelayfileSetup(cloud_api_url=CLOUD)
    with pytest.raises(CloudApiError) as exc_info:
        setup.join_workspace("ws_123")
    assert exc_info.value.status == 409
    assert exc_info.value.code == "workspace_mismatch"


@respx.mock
def test_wait_for_connection_times_out() -> None:
    respx.post(f"{CLOUD}/api/v1/workspaces/ws_123/join").mock(
        return_value=httpx.Response(200, json=join_payload())
    )
    respx.post(f"{CLOUD}/api/v1/workspaces/ws_123/integrations/connect-session").mock(
        return_value=httpx.Response(200, json={"connectionId": "conn_pending"})
    )
    respx.get(
        f"{CLOUD}/api/v1/workspaces/ws_123/integrations/github/status",
        params={"connectionId": "conn_pending"},
    ).mock(return_value=httpx.Response(200, json={"ready": False}))

    handle = RelayfileSetup(cloud_api_url=CLOUD).join_workspace("ws_123")
    handle.connect_integration("github")
    with pytest.raises(IntegrationConnectionTimeoutError):
        handle.wait_for_connection("github", poll_interval_ms=0, timeout_ms=1)


class ConnectProvider:
    name = "test-provider"

    def __init__(self) -> None:
        self.status_calls = 0

    def proxy(self, request: dict[str, Any]) -> dict[str, Any]:
        return {"status": 200, "headers": {}, "data": {}}

    def health_check(self, connection_id: str) -> bool:
        return True

    def create_connect_session(self, payload: dict[str, Any]) -> dict[str, Any]:
        return {
            "connectLink": "https://connect.test",
            "sessionToken": "session_token",
            "expiresAt": None,
            "connectionId": payload["connectionId"],
        }

    def get_connection_status(self, payload: dict[str, Any]) -> dict[str, Any]:
        self.status_calls += 1
        return {"connectionId": payload["connectionId"], "state": "oauth_connected"}


def test_self_host_connect_uses_provider_mapping() -> None:
    provider = ConnectProvider()
    connect = SelfHostConnect(
        provider=provider,
        provider_config_keys={"github": "github-prod"},
        default_poll_interval_ms=0,
    )

    result = connect.start_connect(
        "github",
        end_user_id="user_123",
        connection_id="conn_123",
    )
    status = connect.wait_for_connection("github", connection_id=result.connection_id)

    assert result.provider_config_key == "github-prod"
    assert result.connection_id == "conn_123"
    assert status["state"] == "oauth_connected"


@respx.mock
def test_request_json_retries_transient_errors() -> None:
    route = respx.post(f"{CLOUD}/api/v1/workspaces/ws_123/join").mock(
        side_effect=[
            httpx.Response(503, json={"error": "try later"}),
            httpx.Response(200, json=join_payload()),
        ]
    )

    handle = RelayfileSetup(
        cloud_api_url=CLOUD,
        retry=RelayfileSetupRetryOptions(max_retries=3, base_delay_ms=0),
    ).join_workspace("ws_123")

    assert handle.workspace_id == "ws_123"
    assert route.call_count == 2


@respx.mock
def test_request_json_does_not_retry_client_errors() -> None:
    route = respx.post(f"{CLOUD}/api/v1/workspaces/ws_123/join").mock(
        return_value=httpx.Response(409, json={"code": "conflict"})
    )

    with pytest.raises(CloudApiError):
        RelayfileSetup(
            cloud_api_url=CLOUD,
            retry=RelayfileSetupRetryOptions(max_retries=3, base_delay_ms=0),
        ).join_workspace("ws_123")
    assert route.call_count == 1


@respx.mock
def test_connect_session_without_connection_id_raises() -> None:
    respx.post(f"{CLOUD}/api/v1/workspaces/ws_123/join").mock(
        return_value=httpx.Response(200, json=join_payload())
    )
    respx.post(f"{CLOUD}/api/v1/workspaces/ws_123/integrations/connect-session").mock(
        return_value=httpx.Response(200, json={"token": "session_token"})
    )

    handle = RelayfileSetup(cloud_api_url=CLOUD).join_workspace("ws_123")
    with pytest.raises(MalformedCloudResponseError):
        handle.connect_integration("github")


@respx.mock
def test_disconnect_integration_sends_connection_id() -> None:
    respx.post(f"{CLOUD}/api/v1/workspaces/ws_123/join").mock(
        return_value=httpx.Response(200, json=join_payload())
    )
    route = respx.delete(
        f"{CLOUD}/api/v1/workspaces/ws_123/integrations/github/status",
        params={"connectionId": "conn_123"},
    ).mock(return_value=httpx.Response(200, json={"ok": True}))

    handle = RelayfileSetup(cloud_api_url=CLOUD).join_workspace("ws_123")
    handle.disconnect_integration("github", "conn_123")

    assert route.called
