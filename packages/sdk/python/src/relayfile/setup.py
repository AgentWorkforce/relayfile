from __future__ import annotations

import time
from collections.abc import Callable
from dataclasses import dataclass, field
from importlib.metadata import PackageNotFoundError, version
from typing import Any
from urllib.parse import quote, urlencode

import httpx

from .client import (
    DEFAULT_RELAYFILE_CLOUD_BASE_URL,
    RelayFileClient,
    _coerce_metadata_response,
    _coerce_resource_entries,
    _resolve_token,
)
from .errors import (
    CloudApiError,
    IntegrationConnectionTimeoutError,
    MalformedCloudResponseError,
    MissingConnectionIdError,
    RelayfileSetupError,
    UnknownProviderError,
)


AccessTokenProvider = str | Callable[[], str]
WorkspaceIntegrationProvider = str
WORKSPACE_INTEGRATION_PROVIDERS: tuple[str, ...] = (
    "github",
    "slack-sage",
    "slack-my-senior-dev",
    "slack-nightcto",
    "notion",
    "linear",
)
DEFAULT_RELAYCAST_BASE_URL = "https://api.relaycast.dev"
DEFAULT_AGENT_NAME = "sdk-agent"
DEFAULT_SCOPES = ("fs:read", "fs:write")
DEFAULT_WAIT_INTERVAL_MS = 2_000
DEFAULT_WAIT_TIMEOUT_MS = 300_000
TOKEN_REFRESH_AGE_SECONDS = 55 * 60


def _sdk_version() -> str:
    try:
        return version("relayfile-sdk")
    except PackageNotFoundError:
        return "0.0.0"


def _enc(segment: str) -> str:
    return quote(segment, safe="")


def _build_query(params: dict[str, Any]) -> str:
    filtered = {key: str(value) for key, value in params.items() if value is not None}
    return f"?{urlencode(filtered)}" if filtered else ""


def _compact(mapping: dict[str, Any]) -> dict[str, Any]:
    return {key: value for key, value in mapping.items() if value is not None}


def _normalize_provider(provider: str, *, require_known: bool) -> str:
    normalized = provider.strip().lower()
    if not normalized:
        raise ValueError("provider is required")
    if require_known and normalized not in WORKSPACE_INTEGRATION_PROVIDERS:
        raise UnknownProviderError(normalized)
    return normalized


def _normalize_connection_id(connection_id: str | None) -> str | None:
    if connection_id is None:
        return None
    trimmed = connection_id.strip()
    return trimmed or None


def _require_connection_id(connection_id: str | None) -> str:
    normalized = _normalize_connection_id(connection_id)
    if normalized is None:
        raise MissingConnectionIdError("connectionId is required", "missing_connection_id")
    return normalized


def _read_json(response: httpx.Response) -> Any:
    content_type = response.headers.get("content-type", "")
    if "application/json" in content_type:
        try:
            return response.json()
        except Exception:
            return {}
    return {"message": response.text}


def _validate_workspace_info(payload: Any) -> dict[str, Any]:
    if not isinstance(payload, dict) or not isinstance(payload.get("workspaceId"), str):
        raise MalformedCloudResponseError(
            "Cloud response did not include workspaceId.",
            "malformed_cloud_response",
        )
    return dict(payload)


def _validate_join_response(payload: Any) -> dict[str, Any]:
    required = ("workspaceId", "token", "relayfileUrl", "wsUrl", "relaycastApiKey")
    if not isinstance(payload, dict) or any(
        not isinstance(payload.get(key), str) or not payload.get(key) for key in required
    ):
        raise MalformedCloudResponseError(
            "Cloud join response did not include workspaceId, token, relayfileUrl, "
            "wsUrl, and relaycastApiKey.",
            "malformed_cloud_response",
        )
    return dict(payload)


def _validate_connect_session(payload: Any) -> dict[str, Any]:
    if not isinstance(payload, dict):
        raise MalformedCloudResponseError(
            "Cloud connect-session response was not an object.",
            "malformed_cloud_response",
        )
    return dict(payload)


@dataclass
class WorkspacePermissions:
    readonly: list[str] | None = None
    ignored: list[str] | None = None

    def to_json(self) -> dict[str, list[str]]:
        return _compact({"readonly": self.readonly, "ignored": self.ignored})


@dataclass
class RelayfileSetupRetryOptions:
    max_retries: int = 3
    base_delay_ms: int = 500


@dataclass
class WorkspaceInfo:
    workspace_id: str
    relayfile_url: str
    relaycast_api_key: str
    created_at: str | None = None
    name: str | None = None
    ws_url: str | None = None
    relaycast_base_url: str | None = None


@dataclass
class ConnectIntegrationResult:
    connect_link: str | None
    session_token: str | None
    expires_at: str | None
    already_connected: bool
    connection_id: str


@dataclass
class AgentWorkspaceInvite:
    workspace_id: str
    cloud_api_url: str
    relayfile_url: str
    relaycast_api_key: str
    relaycast_base_url: str
    agent_name: str
    scopes: list[str]
    relayfile_token: str | None = None
    created_at: str | None = None
    name: str | None = None

    def to_dict(self) -> dict[str, Any]:
        return _compact(
            {
                "workspaceId": self.workspace_id,
                "cloudApiUrl": self.cloud_api_url,
                "relayfileUrl": self.relayfile_url,
                "relaycastApiKey": self.relaycast_api_key,
                "relaycastBaseUrl": self.relaycast_base_url,
                "agentName": self.agent_name,
                "scopes": list(self.scopes),
                "relayfileToken": self.relayfile_token,
                "createdAt": self.created_at,
                "name": self.name,
            }
        )


@dataclass
class _JoinOptions:
    agent_name: str = DEFAULT_AGENT_NAME
    scopes: list[str] = field(default_factory=lambda: list(DEFAULT_SCOPES))
    permissions: WorkspacePermissions | dict[str, Any] | None = None

    def to_json(self) -> dict[str, Any]:
        permissions = self.permissions
        if isinstance(permissions, WorkspacePermissions):
            permissions_body = permissions.to_json()
        else:
            permissions_body = permissions
        return _compact(
            {
                "agentName": self.agent_name,
                "scopes": list(self.scopes),
                "permissions": permissions_body,
            }
        )


class RelayfileSetup:
    """Relayfile Cloud control-plane helper.

    Python exposes the portable setup flow from the TypeScript SDK: create or
    join a workspace, connect/adopt integrations, wait for provider readiness,
    and build peer-agent invites. Interactive browser login and local mount
    launchers remain TypeScript/Node-only.
    """

    def __init__(
        self,
        *,
        cloud_api_url: str = DEFAULT_RELAYFILE_CLOUD_BASE_URL,
        access_token: AccessTokenProvider | None = None,
        timeout: float = 30.0,
        retry: RelayfileSetupRetryOptions | None = None,
        http_client: httpx.Client | None = None,
    ) -> None:
        self._cloud_api_url = cloud_api_url.rstrip("/")
        self._access_token = access_token
        self._timeout = timeout
        self._retry = retry or RelayfileSetupRetryOptions()
        self._client = http_client or httpx.Client(timeout=timeout)
        self._owns_client = http_client is None

    def close(self) -> None:
        if self._owns_client:
            self._client.close()

    def __enter__(self) -> RelayfileSetup:
        return self

    def __exit__(self, *args: object) -> None:
        self.close()

    def get_cloud_api_url(self) -> str:
        return self._cloud_api_url

    def request_json(
        self,
        method: str,
        path: str,
        *,
        body: Any = None,
        token_provider: AccessTokenProvider | None = None,
    ) -> Any:
        token_source = token_provider if token_provider is not None else self._access_token
        token = _resolve_token(token_source) if token_source else None
        headers = {"X-Relayfile-SDK-Version": _sdk_version()}
        if token:
            headers["Authorization"] = f"Bearer {token}"
        if body is not None:
            headers["Content-Type"] = "application/json"

        response = self._client.request(
            method,
            f"{self._cloud_api_url}/{path.lstrip('/')}",
            headers=headers,
            json=body,
        )
        payload = _read_json(response)
        if response.is_success:
            return payload
        raise CloudApiError(response.status_code, payload)

    def create_workspace(
        self,
        *,
        name: str | None = None,
        permissions: WorkspacePermissions | dict[str, Any] | None = None,
        agent_name: str | None = None,
        scopes: list[str] | None = None,
    ) -> WorkspaceHandle:
        create_response = _validate_workspace_info(
            self.request_json(
                "POST",
                "api/v1/workspaces",
                body=_compact(
                    {
                        "name": name,
                        "permissions": permissions.to_json()
                        if isinstance(permissions, WorkspacePermissions)
                        else permissions,
                    }
                ),
            )
        )
        return self.join_workspace(
            create_response["workspaceId"],
            agent_name=agent_name,
            scopes=scopes,
            permissions=permissions,
            created_at=create_response.get("createdAt"),
            name=create_response.get("name"),
        )

    def join_workspace(
        self,
        workspace_id: str,
        *,
        agent_name: str | None = None,
        scopes: list[str] | None = None,
        permissions: WorkspacePermissions | dict[str, Any] | None = None,
        created_at: str | None = None,
        name: str | None = None,
    ) -> WorkspaceHandle:
        join_options = _JoinOptions(
            agent_name=agent_name or DEFAULT_AGENT_NAME,
            scopes=list(scopes or DEFAULT_SCOPES),
            permissions=permissions,
        )
        join_response = self.join_workspace_response(workspace_id, join_options)
        info = WorkspaceInfo(
            workspace_id=join_response["workspaceId"],
            relayfile_url=join_response["relayfileUrl"],
            relaycast_api_key=join_response["relaycastApiKey"],
            relaycast_base_url=join_response.get("relaycastBaseUrl"),
            ws_url=join_response.get("wsUrl"),
            created_at=created_at,
            name=name,
        )
        return WorkspaceHandle(self, info, join_response["token"], join_options)

    def join_workspace_response(
        self,
        workspace_id: str,
        join_options: _JoinOptions,
        *,
        token_provider: AccessTokenProvider | None = None,
    ) -> dict[str, Any]:
        return _validate_join_response(
            self.request_json(
                "POST",
                f"api/v1/workspaces/{_enc(workspace_id)}/join",
                body=join_options.to_json(),
                token_provider=token_provider,
            )
        )


class WorkspaceHandle:
    def __init__(
        self,
        setup: RelayfileSetup,
        info: WorkspaceInfo,
        token: str,
        join_options: _JoinOptions,
    ) -> None:
        self.info = info
        self.workspace_id = info.workspace_id
        self._setup = setup
        self._token = token
        self._token_issued_at = time.monotonic()
        self._join_options = join_options
        self._pending_connections: dict[str, str] = {}
        self._client: RelayFileClient | None = None

    def client(self) -> RelayFileClient:
        if self._client is None:
            self._client = RelayFileClient(
                self.info.relayfile_url,
                lambda: self.get_or_refresh_token(),
                cloud_base_url=self._setup.get_cloud_api_url(),
            )
        return self._client

    def get_token(self) -> str:
        return self._token

    def refresh_token(self) -> None:
        response = self._setup.join_workspace_response(self.workspace_id, self._join_options)
        self._token = response["token"]
        self._token_issued_at = time.monotonic()

    def get_or_refresh_token(self) -> str:
        if time.monotonic() - self._token_issued_at >= TOKEN_REFRESH_AGE_SECONDS:
            self.refresh_token()
        return self._token

    def request_json(self, method: str, path: str, *, body: Any = None) -> Any:
        return self._setup.request_json(
            method,
            path,
            body=body,
            token_provider=lambda: self.get_or_refresh_token(),
        )

    def connect_integration(
        self,
        provider: WorkspaceIntegrationProvider,
        *,
        connection_id: str | None = None,
        allowed_integrations: list[str] | None = None,
    ) -> ConnectIntegrationResult:
        normalized = _normalize_provider(provider, require_known=True)
        requested_connection_id = _normalize_connection_id(connection_id)
        if requested_connection_id and self.is_connected(normalized, requested_connection_id):
            self._pending_connections[normalized] = requested_connection_id
            return ConnectIntegrationResult(
                connect_link=None,
                session_token=None,
                expires_at=None,
                already_connected=True,
                connection_id=requested_connection_id,
            )

        response = _validate_connect_session(
            self.request_json(
                "POST",
                f"api/v1/workspaces/{_enc(self.workspace_id)}/integrations/connect-session",
                body={
                    "allowedIntegrations": allowed_integrations
                    if allowed_integrations
                    else [normalized]
                },
            )
        )
        resolved_connection_id = (
            _normalize_connection_id(response.get("connectionId")) or self.workspace_id
        )
        self._pending_connections[normalized] = resolved_connection_id
        return ConnectIntegrationResult(
            connect_link=response.get("connectLink"),
            session_token=response.get("token"),
            expires_at=response.get("expiresAt"),
            already_connected=False,
            connection_id=resolved_connection_id,
        )

    def connect_notion(self, *, connection_id: str | None = None) -> ConnectIntegrationResult:
        return self.connect_integration(
            "notion",
            connection_id=connection_id,
            allowed_integrations=["notion"],
        )

    def wait_for_connection(
        self,
        provider: WorkspaceIntegrationProvider,
        *,
        connection_id: str | None = None,
        poll_interval_ms: int = DEFAULT_WAIT_INTERVAL_MS,
        timeout_ms: int = DEFAULT_WAIT_TIMEOUT_MS,
        on_poll: Callable[[int], None] | None = None,
    ) -> None:
        normalized = _normalize_provider(provider, require_known=True)
        resolved_connection_id = self._resolve_connection_id(normalized, connection_id)
        started = time.monotonic()
        while True:
            elapsed_ms = int((time.monotonic() - started) * 1000)
            if on_poll:
                on_poll(elapsed_ms)
            if elapsed_ms >= timeout_ms:
                raise IntegrationConnectionTimeoutError(
                    provider=normalized,
                    connection_id=resolved_connection_id,
                    elapsed_ms=elapsed_ms,
                    timeout_ms=timeout_ms,
                )
            status = self.get_connection_status(normalized, resolved_connection_id)
            if bool(status.get("ready")):
                return
            sleep_ms = min(max(0, poll_interval_ms), max(0, timeout_ms - elapsed_ms))
            time.sleep(sleep_ms / 1000)

    def wait_for_notion(self, **kwargs: Any) -> None:
        self.wait_for_connection("notion", **kwargs)

    def is_connected(self, provider: WorkspaceIntegrationProvider, connection_id: str) -> bool:
        normalized = _normalize_provider(provider, require_known=True)
        status = self.get_connection_status(normalized, connection_id)
        return bool(status.get("ready"))

    def get_connection_status(
        self,
        provider: WorkspaceIntegrationProvider,
        connection_id: str,
    ) -> dict[str, Any]:
        normalized = _normalize_provider(provider, require_known=True)
        query = _build_query({"connectionId": _require_connection_id(connection_id)})
        payload = self.request_json(
            "GET",
            f"api/v1/workspaces/{_enc(self.workspace_id)}/integrations/{_enc(normalized)}/status{query}",
        )
        if not isinstance(payload, dict):
            return {"ready": False}
        return dict(payload)

    def disconnect_integration(
        self,
        provider: WorkspaceIntegrationProvider,
        connection_id: str | None = None,
    ) -> None:
        normalized = _normalize_provider(provider, require_known=True)
        self.request_json(
            "DELETE",
            f"api/v1/workspaces/{_enc(self.workspace_id)}/integrations/{_enc(normalized)}/status",
        )
        self._pending_connections.pop(normalized, None)

    def adopt_integration(
        self,
        provider: WorkspaceIntegrationProvider,
        connection_id: str,
        *,
        provider_config_key: str | None = None,
    ) -> dict[str, str]:
        normalized = _normalize_provider(provider, require_known=True)
        trimmed_connection_id = _require_connection_id(connection_id)
        body = _compact(
            {
                "connectionId": trimmed_connection_id,
                "providerConfigKey": provider_config_key.strip()
                if provider_config_key
                else None,
            }
        )
        payload = self.request_json(
            "POST",
            f"api/v1/workspaces/{_enc(self.workspace_id)}/integrations/{_enc(normalized)}/adopt",
            body=body,
        )
        response = payload if isinstance(payload, dict) else {}
        bound_connection_id = response.get("connectionId")
        result = {
            "connectionId": bound_connection_id
            if isinstance(bound_connection_id, str) and bound_connection_id.strip()
            else trimmed_connection_id
        }
        replaced_connection_id = response.get("replacedConnectionId")
        if isinstance(replaced_connection_id, str) and replaced_connection_id.strip():
            result["replacedConnectionId"] = replaced_connection_id.strip()
        self._pending_connections.pop(normalized, None)
        return result

    def list_accessible_resources(self, provider: WorkspaceIntegrationProvider) -> list[dict[str, Any]]:
        normalized = _normalize_provider(provider, require_known=False)
        payload = self.request_json(
            "GET",
            f"api/v1/workspaces/{_enc(self.workspace_id)}/integrations/{_enc(normalized)}/accessible-resources",
        )
        return _coerce_resource_entries(payload)

    def set_integration_metadata(
        self,
        provider: WorkspaceIntegrationProvider,
        metadata: dict[str, Any],
    ) -> dict[str, Any]:
        if not isinstance(metadata, dict):
            raise ValueError("metadata must be a dict")
        normalized = _normalize_provider(provider, require_known=False)
        payload = self.request_json(
            "PUT",
            f"api/v1/workspaces/{_enc(self.workspace_id)}/integrations/{_enc(normalized)}/metadata",
            body={"metadata": metadata},
        )
        return _coerce_metadata_response(payload)

    def mount_env(
        self,
        *,
        local_dir: str | None = None,
        remote_path: str = "/",
        mode: str | None = None,
        relaycast_base_url: str | None = None,
    ) -> dict[str, str]:
        base = relaycast_base_url or self.info.relaycast_base_url or DEFAULT_RELAYCAST_BASE_URL
        return {
            key: value
            for key, value in {
                "RELAYFILE_BASE_URL": self.info.relayfile_url,
                "RELAYFILE_TOKEN": self.get_token(),
                "RELAYFILE_WORKSPACE": self.workspace_id,
                "RELAYFILE_REMOTE_PATH": remote_path,
                "RELAYFILE_LOCAL_DIR": local_dir,
                "RELAYFILE_MOUNT_MODE": mode,
                "RELAYCAST_API_KEY": self.info.relaycast_api_key,
                "RELAY_API_KEY": self.info.relaycast_api_key,
                "RELAYCAST_BASE_URL": base,
                "RELAY_BASE_URL": base,
            }.items()
            if value is not None
        }

    def agent_invite(
        self,
        *,
        agent_name: str | None = None,
        relaycast_base_url: str | None = None,
        include_relayfile_token: bool = True,
    ) -> AgentWorkspaceInvite:
        return AgentWorkspaceInvite(
            workspace_id=self.workspace_id,
            cloud_api_url=self._setup.get_cloud_api_url(),
            relayfile_url=self.info.relayfile_url,
            relaycast_api_key=self.info.relaycast_api_key,
            relaycast_base_url=relaycast_base_url
            or self.info.relaycast_base_url
            or DEFAULT_RELAYCAST_BASE_URL,
            agent_name=agent_name or self._join_options.agent_name,
            scopes=list(self._join_options.scopes),
            relayfile_token=self.get_token() if include_relayfile_token else None,
            created_at=self.info.created_at,
            name=self.info.name,
        )

    def agent_invite_scoped(
        self,
        *,
        scopes: list[str] | None = None,
        agent_name: str | None = None,
        permissions: WorkspacePermissions | dict[str, Any] | None = None,
        relaycast_base_url: str | None = None,
        include_relayfile_token: bool = True,
    ) -> AgentWorkspaceInvite:
        requested_scopes = list(scopes or self._join_options.scopes)
        requested_agent_name = agent_name or self._join_options.agent_name
        join_response = self._setup.join_workspace_response(
            self.workspace_id,
            _JoinOptions(
                agent_name=requested_agent_name,
                scopes=requested_scopes,
                permissions=permissions or self._join_options.permissions,
            ),
            token_provider=lambda: self.get_or_refresh_token(),
        )
        return AgentWorkspaceInvite(
            workspace_id=self.workspace_id,
            cloud_api_url=self._setup.get_cloud_api_url(),
            relayfile_url=join_response.get("relayfileUrl") or self.info.relayfile_url,
            relaycast_api_key=join_response.get("relaycastApiKey")
            or self.info.relaycast_api_key,
            relaycast_base_url=relaycast_base_url
            or join_response.get("relaycastBaseUrl")
            or self.info.relaycast_base_url
            or DEFAULT_RELAYCAST_BASE_URL,
            agent_name=requested_agent_name,
            scopes=requested_scopes,
            relayfile_token=join_response["token"] if include_relayfile_token else None,
            created_at=self.info.created_at,
            name=self.info.name,
        )

    def _resolve_connection_id(self, provider: str, connection_id: str | None) -> str:
        normalized = _normalize_connection_id(connection_id)
        if normalized:
            return normalized
        pending = self._pending_connections.get(provider)
        if pending:
            return pending
        raise MissingConnectionIdError(
            f'connectionId is required for provider "{provider}"',
            "missing_connection_id",
        )


def mount_workspace(*args: Any, **kwargs: Any) -> None:
    raise RelayfileSetupError(
        "mount_workspace starts the local relayfile-mount daemon and is only "
        "available from the TypeScript SDK for now.",
        "ts_only_sdk_feature",
    )


def ensure_mounted_workspace(*args: Any, **kwargs: Any) -> None:
    raise RelayfileSetupError(
        "ensure_mounted_workspace starts the local relayfile-mount daemon and is "
        "only available from the TypeScript SDK for now.",
        "ts_only_sdk_feature",
    )
