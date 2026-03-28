from __future__ import annotations

from dataclasses import dataclass, field


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
