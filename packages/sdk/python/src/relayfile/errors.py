from __future__ import annotations

from dataclasses import dataclass
from typing import Any


@dataclass
class RelayFileApiError(Exception):
    status: int
    code: str = "unknown_error"
    message: str = ""
    correlation_id: str | None = None
    details: dict[str, object] | None = None

    def __post_init__(self) -> None:
        super().__init__(self.message or f"RelayFile API error: {self.status}")


@dataclass
class RevisionConflictError(RelayFileApiError):
    expected_revision: str = ""
    current_revision: str = ""
    current_content_preview: str | None = None


@dataclass
class QueueFullError(RelayFileApiError):
    retry_after_seconds: int | None = None


@dataclass
class InvalidStateError(RelayFileApiError):
    pass


@dataclass
class PayloadTooLargeError(RelayFileApiError):
    pass


class RelayfileSetupError(Exception):
    """Base error for Relayfile cloud setup and control-plane helpers."""

    def __init__(self, message: str, code: str = "setup_error") -> None:
        self.code = code
        super().__init__(message)


class CloudApiError(RelayfileSetupError):
    """Raised when the Relayfile Cloud control plane returns a non-2xx response."""

    def __init__(self, status: int, body: Any) -> None:
        self.status = status
        self.body = body
        message = _read_cloud_error_message(body) or f"Cloud API error: HTTP {status}"
        code = body.get("code") if isinstance(body, dict) else None
        super().__init__(message, str(code or "cloud_api_error"))


class MalformedCloudResponseError(RelayfileSetupError):
    pass


class IntegrationConnectionTimeoutError(RelayfileSetupError):
    def __init__(
        self,
        *,
        provider: str,
        connection_id: str,
        elapsed_ms: int,
        timeout_ms: int,
    ) -> None:
        self.provider = provider
        self.connection_id = connection_id
        self.elapsed_ms = elapsed_ms
        self.timeout_ms = timeout_ms
        super().__init__(
            f'Timed out waiting for {provider} connection "{connection_id}" '
            f"after {elapsed_ms}ms.",
            "integration_connection_timeout",
        )


class UnknownProviderError(RelayfileSetupError):
    def __init__(self, provider: str) -> None:
        self.provider = provider
        super().__init__(f'Unknown workspace integration provider "{provider}".', "unknown_provider")


class MissingConnectionIdError(RelayfileSetupError):
    pass


class ProviderNotConnectedError(RelayfileSetupError):
    pass


class ProviderNotReadyError(RelayfileSetupError):
    pass


def _read_cloud_error_message(body: Any) -> str | None:
    if not isinstance(body, dict):
        return None
    for key in ("message", "error"):
        value = body.get(key)
        if isinstance(value, str) and value:
            return value
    return None
