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
    ) -> None:
        if poll_interval_ms < 0:
            raise ValueError("poll_interval_ms must be greater than or equal to 0")
        self._client = client
        self._workspace_id = workspace_id
        self._handlers = handlers
        self._provider = provider
        self._poll_interval_ms = poll_interval_ms
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
            self._client.ack_writeback(
                AckWritebackInput(
                    workspace_id=self._workspace_id,
                    item_id=item.id,
                    success=True,
                    correlation_id=item.correlation_id,
                )
            )

    def _ack_failure(self, item: WritebackItem, error: Exception) -> None:
        self._client.ack_writeback(
            AckWritebackInput(
                workspace_id=self._workspace_id,
                item_id=item.id,
                success=False,
                error=str(error),
                correlation_id=item.correlation_id,
            )
        )
