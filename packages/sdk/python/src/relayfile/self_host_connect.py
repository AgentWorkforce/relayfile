from __future__ import annotations

import time
from collections.abc import Callable
from dataclasses import dataclass
from typing import Any

from .connection import (
    ConnectConnectionStatus,
    ConnectionProvider,
    CreateConnectSessionInput,
    GetConnectConnectionStatusInput,
    ProviderConfigKeyMap,
    supports_connect,
)


DEFAULT_WAIT_INTERVAL_MS = 2_000
DEFAULT_WAIT_TIMEOUT_MS = 5 * 60_000
AUTH_READY_STATE = "oauth_connected"


@dataclass
class SelfHostConnectResult:
    relayfile_provider: str
    provider_config_key: str
    connect_link: str | None
    session_token: str | None
    expires_at: str | None
    connection_id: str


class SelfHostConnect:
    def __init__(
        self,
        *,
        provider: ConnectionProvider,
        provider_config_keys: ProviderConfigKeyMap,
        default_poll_interval_ms: int = DEFAULT_WAIT_INTERVAL_MS,
        default_timeout_ms: int = DEFAULT_WAIT_TIMEOUT_MS,
    ) -> None:
        self._provider = provider
        self._provider_config_keys = dict(provider_config_keys)
        self._default_poll_interval_ms = max(1, int(default_poll_interval_ms))
        self._default_timeout_ms = max(1, int(default_timeout_ms))

    def start_connect(
        self,
        relayfile_provider: str,
        *,
        end_user_id: str,
        connection_id: str | None = None,
        metadata: dict[str, Any] | None = None,
    ) -> SelfHostConnectResult:
        normalized = _normalize_relayfile_provider(relayfile_provider)
        provider_config_key = self._resolve_provider_config_key(normalized)
        connect_provider = self._require_connect_provider()
        trimmed_end_user_id = end_user_id.strip()
        if not trimmed_end_user_id:
            raise ValueError("end_user_id is required to start a self-host connect session")

        input: CreateConnectSessionInput = {
            "relayfileProvider": normalized,
            "providerConfigKey": provider_config_key,
            "endUserId": trimmed_end_user_id,
        }
        if connection_id and connection_id.strip():
            input["connectionId"] = connection_id.strip()
        if metadata:
            input["metadata"] = dict(metadata)

        session = connect_provider.create_connect_session(input)  # type: ignore[attr-defined]
        return SelfHostConnectResult(
            relayfile_provider=normalized,
            provider_config_key=provider_config_key,
            connect_link=session.get("connectLink"),
            session_token=session.get("sessionToken"),
            expires_at=session.get("expiresAt"),
            connection_id=session["connectionId"],
        )

    def wait_for_connection(
        self,
        relayfile_provider: str,
        *,
        connection_id: str,
        poll_interval_ms: int | None = None,
        timeout_ms: int | None = None,
        on_poll: Callable[[int, ConnectConnectionStatus | None], None] | None = None,
    ) -> ConnectConnectionStatus:
        normalized = _normalize_relayfile_provider(relayfile_provider)
        provider_config_key = self._resolve_provider_config_key(normalized)
        connect_provider = self._require_connect_provider()
        trimmed_connection_id = connection_id.strip()
        if not trimmed_connection_id:
            raise ValueError("connection_id is required to wait for a self-host connection")

        poll_ms = max(0, int(poll_interval_ms if poll_interval_ms is not None else self._default_poll_interval_ms))
        timeout = max(1, int(timeout_ms if timeout_ms is not None else self._default_timeout_ms))
        started = time.monotonic()
        while True:
            elapsed_ms = int((time.monotonic() - started) * 1000)
            if elapsed_ms >= timeout:
                raise TimeoutError(
                    f'Timed out waiting for {normalized} connection "{trimmed_connection_id}" '
                    f"after {elapsed_ms}ms."
                )
            status = connect_provider.get_connection_status(  # type: ignore[attr-defined]
                GetConnectConnectionStatusInput(
                    relayfileProvider=normalized,
                    providerConfigKey=provider_config_key,
                    connectionId=trimmed_connection_id,
                )
            )
            if on_poll:
                on_poll(int((time.monotonic() - started) * 1000), status)
            if _is_auth_ready(status):
                return status
            time.sleep(min(poll_ms, max(0, timeout - elapsed_ms)) / 1000)

    def _resolve_provider_config_key(self, relayfile_provider: str) -> str:
        provider_config_key = self._provider_config_keys.get(relayfile_provider, "").strip()
        if not provider_config_key:
            raise ValueError(
                f'No provider_config_key mapping configured for relayfile provider "{relayfile_provider}".'
            )
        return provider_config_key

    def _require_connect_provider(self) -> Any:
        if not supports_connect(self._provider):
            provider_name = getattr(self._provider, "name", "unknown")
            raise ValueError(f'Provider "{provider_name}" does not support self-host Connect.')
        return self._provider


def _is_auth_ready(status: ConnectConnectionStatus) -> bool:
    return status.get("state") == AUTH_READY_STATE or status.get("ready") is True


def _normalize_relayfile_provider(provider: str) -> str:
    normalized = provider.strip()
    if not normalized:
        raise ValueError("relayfile_provider is required")
    return normalized
