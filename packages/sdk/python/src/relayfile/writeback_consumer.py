from __future__ import annotations

import time
from typing import Protocol

from .client import RelayFileClient
from .connection import ConnectionProvider
from .types import AckWritebackInput, WritebackItem


class WritebackHandler(Protocol):
    def can_handle(self, path: str) -> bool:
        ...

    def execute(self, item: WritebackItem, provider: ConnectionProvider) -> None:
        ...


class WritebackConsumer:
    def __init__(
        self,
        *,
        client: RelayFileClient,
        workspace_id: str,
        handlers: list[WritebackHandler],
        provider: ConnectionProvider,
        poll_interval_ms: int = 1_000,
        ack_max_attempts: int = 3,
    ) -> None:
        if poll_interval_ms < 0:
            raise ValueError("poll_interval_ms must be greater than or equal to 0")
        if ack_max_attempts < 1:
            raise ValueError("ack_max_attempts must be greater than or equal to 1")
        self._client = client
        self._workspace_id = workspace_id
        self._handlers = handlers
        self._provider = provider
        self._poll_interval_ms = poll_interval_ms
        self._ack_max_attempts = ack_max_attempts
        self._stopped = False

    def start(self) -> None:
        self._stopped = False
        while not self._stopped:
            self.poll_once()
            if self._stopped:
                return
            time.sleep(self._poll_interval_ms / 1000)

    def stop(self) -> None:
        self._stopped = True

    def poll_once(self) -> None:
        items = self._client.list_pending_writebacks(self._workspace_id)
        for item in items:
            if self._stopped:
                return
            handler = next(
                (candidate for candidate in self._handlers if candidate.can_handle(item.path)),
                None,
            )
            if handler is None:
                self._ack_failure(item, Exception(f"No writeback handler found for path: {item.path}"))
                continue
            try:
                handler.execute(item, self._provider)
            except Exception as exc:
                self._ack_failure(item, exc)
                continue
            self._ack_with_retry(
                AckWritebackInput(
                    workspace_id=self._workspace_id,
                    item_id=item.id,
                    success=True,
                    correlation_id=item.correlation_id,
                )
            )

    def _ack_failure(self, item: WritebackItem, error: Exception) -> None:
        self._ack_with_retry(
            AckWritebackInput(
                workspace_id=self._workspace_id,
                item_id=item.id,
                success=False,
                error=str(error),
                correlation_id=item.correlation_id,
            )
        )

    def _ack_with_retry(self, ack: AckWritebackInput) -> None:
        # A handler side effect may already be committed before the ACK, so a
        # transient ACK failure must not redeliver the item. Retry with bounded
        # backoff; handlers should still be idempotent keyed by ``item.id``.
        last_error: Exception | None = None
        for attempt in range(self._ack_max_attempts):
            try:
                self._client.ack_writeback(ack)
                return
            except Exception as exc:  # noqa: BLE001 - surfaced after retries exhaust
                last_error = exc
                if attempt < self._ack_max_attempts - 1:
                    time.sleep(min(0.25 * (2**attempt), 2.0))
        raise RuntimeError(
            f"ack_writeback failed after {self._ack_max_attempts} attempts"
        ) from last_error
