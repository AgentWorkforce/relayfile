from __future__ import annotations

from collections.abc import Callable
from typing import Any, Literal, Protocol, TypeVar, runtime_checkable


ProxyMethod = Literal["DELETE", "GET", "PATCH", "POST", "PUT"]
ProxyHeaders = dict[str, str]
ProxyQuery = dict[str, str]
ProviderConfigKeyMap = dict[str, str]
T = TypeVar("T")


class ProxyRequest(dict[str, Any]):
    pass


class ProxyResponse(dict[str, Any]):
    pass


class NormalizedWebhook(dict[str, Any]):
    pass


class CreateConnectSessionInput(dict[str, Any]):
    pass


class ConnectSession(dict[str, Any]):
    pass


class GetConnectConnectionStatusInput(dict[str, Any]):
    pass


class ConnectConnectionStatus(dict[str, Any]):
    pass


@runtime_checkable
class ConnectionProvider(Protocol):
    name: str

    def proxy(self, request: ProxyRequest) -> ProxyResponse:
        ...

    def health_check(self, connection_id: str) -> bool:
        ...

    def handle_webhook(self, raw_payload: Any) -> NormalizedWebhook:
        ...

    def get_connection(self, connection_id: str) -> dict[str, Any]:
        ...

    def list_connections(self) -> list[dict[str, Any]]:
        ...


@runtime_checkable
class ConnectCapableProvider(ConnectionProvider, Protocol):
    create_connect_session: Callable[[CreateConnectSessionInput], ConnectSession]
    get_connection_status: Callable[
        [GetConnectConnectionStatusInput], ConnectConnectionStatus
    ]


def supports_connect(provider: object) -> bool:
    return callable(getattr(provider, "create_connect_session", None)) and callable(
        getattr(provider, "get_connection_status", None)
    )
