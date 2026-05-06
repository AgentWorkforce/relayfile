from __future__ import annotations

import asyncio
import json
import logging
import os
import threading
import time
import weakref
from collections.abc import Callable, Iterable
from typing import Any, Protocol, TypeVar
from urllib.parse import quote

from .client import DEFAULT_RELAYFILE_BASE_URL, RelayFileClient
from .types import WriteEvent

OnWriteHandler = Callable[[WriteEvent], Any]
Unsubscribe = Callable[[], None]
THandler = TypeVar("THandler", bound=OnWriteHandler)

RECONNECT_DELAYS_SECONDS = (1.0, 2.0, 4.0, 8.0, 16.0, 30.0)
DEFAULT_OPERATIONS = ("create", "update")


class OnWriteSocket(Protocol):
    def recv(self) -> str: ...
    def close(self) -> None: ...


WebSocketFactory = Callable[[str], OnWriteSocket]
SleepFn = Callable[[float], None]

_dispatchers: weakref.WeakKeyDictionary[RelayFileClient, "_OnWriteDispatcher"] = weakref.WeakKeyDictionary()
_default_client: RelayFileClient | None = None


def path_matches(pattern: str, path: str) -> bool:
    pattern_segments = _normalize_pattern(pattern)
    path_segments = _normalize_path(path)
    return _match_segments(pattern_segments, path_segments)


def on_write(
    pattern: str,
    handler: OnWriteHandler | None = None,
    *,
    client: RelayFileClient | None = None,
    workspace_id: str | None = None,
    operations: Iterable[str] = DEFAULT_OPERATIONS,
    base_url: str | None = None,
    token: str | None = None,
    websocket_factory: WebSocketFactory | None = None,
    _sleep: SleepFn = time.sleep,
) -> Unsubscribe | Callable[[THandler], THandler]:
    """Subscribe to write events on a path pattern.

    Usable as a decorator or as a direct function call.
    """

    normalized_pattern = "/" + "/".join(_normalize_pattern(pattern))
    operation_set = set(operations)
    invalid_operations = operation_set - {"create", "update", "delete"}
    if invalid_operations:
        raise ValueError(f"Invalid on_write operation: {sorted(invalid_operations)[0]}")

    def register(actual_handler: THandler) -> THandler:
        _register(
            normalized_pattern,
            actual_handler,
            client=client,
            workspace_id=workspace_id,
            operations=operation_set,
            base_url=base_url,
            token=token,
            websocket_factory=websocket_factory,
            sleep=_sleep,
        )
        return actual_handler

    if handler is None:
        return register

    return _register(
        normalized_pattern,
        handler,
        client=client,
        workspace_id=workspace_id,
        operations=operation_set,
        base_url=base_url,
        token=token,
        websocket_factory=websocket_factory,
        sleep=_sleep,
    )


def _register(
    pattern: str,
    handler: OnWriteHandler,
    *,
    client: RelayFileClient | None,
    workspace_id: str | None,
    operations: set[str],
    base_url: str | None,
    token: str | None,
    websocket_factory: WebSocketFactory | None,
    sleep: SleepFn,
) -> Unsubscribe:
    if not callable(handler):
        raise TypeError("on_write handler must be callable")

    resolved_client = client or _get_default_client()
    resolved_workspace_id = workspace_id or os.getenv("RELAYFILE_WORKSPACE_ID")
    if not resolved_workspace_id:
        raise ValueError("on_write requires workspace_id or RELAYFILE_WORKSPACE_ID")

    dispatcher = _dispatchers.get(resolved_client)
    if dispatcher is None:
        dispatcher = _OnWriteDispatcher(resolved_client)
        _dispatchers[resolved_client] = dispatcher

    return dispatcher.register(
        pattern,
        handler,
        workspace_id=resolved_workspace_id,
        operations=operations,
        base_url=base_url,
        token=token,
        websocket_factory=websocket_factory,
        sleep=sleep,
    )


class _Registration:
    def __init__(self, registration_id: int, pattern: str, operations: set[str], handler: OnWriteHandler) -> None:
        self.id = registration_id
        self.pattern = pattern
        self.operations = operations
        self.handler = handler


class _OnWriteDispatcher:
    def __init__(self, client: RelayFileClient) -> None:
        self._client = client
        self._registrations: list[_Registration] = []
        self._lock = threading.RLock()
        self._pattern_locks: dict[str, threading.Lock] = {}
        self._next_id = 1
        self._socket: OnWriteSocket | None = None
        self._thread: threading.Thread | None = None
        self._stop = threading.Event()

    def register(
        self,
        pattern: str,
        handler: OnWriteHandler,
        *,
        workspace_id: str,
        operations: set[str],
        base_url: str | None,
        token: str | None,
        websocket_factory: WebSocketFactory | None,
        sleep: SleepFn,
    ) -> Unsubscribe:
        with self._lock:
            registration = _Registration(self._next_id, pattern, operations, handler)
            self._next_id += 1
            self._registrations.append(registration)
            if self._thread is None:
                self._start(
                    workspace_id=workspace_id,
                    base_url=base_url,
                    token=token,
                    websocket_factory=websocket_factory,
                    sleep=sleep,
                )

        def unsubscribe() -> None:
            self.unregister(registration.id)

        return unsubscribe

    def unregister(self, registration_id: int) -> None:
        with self._lock:
            self._registrations = [registration for registration in self._registrations if registration.id != registration_id]
            if not self._registrations:
                self._stop.set()
                if self._socket is not None:
                    self._socket.close()

    def _start(
        self,
        *,
        workspace_id: str,
        base_url: str | None,
        token: str | None,
        websocket_factory: WebSocketFactory | None,
        sleep: SleepFn,
    ) -> None:
        factory = websocket_factory or _default_websocket_factory
        resolved_token = token or os.getenv("RELAYFILE_TOKEN")
        if not resolved_token:
            raise ValueError("on_write requires token or RELAYFILE_TOKEN")

        url = _build_websocket_url(base_url or os.getenv("RELAYFILE_BASE_URL") or DEFAULT_RELAYFILE_BASE_URL, workspace_id, resolved_token)
        self._thread = threading.Thread(
            target=self._run,
            kwargs={"url": url, "factory": factory, "sleep": sleep},
            name="relayfile-on-write",
            daemon=True,
        )
        self._thread.start()

    def _run(self, *, url: str, factory: WebSocketFactory, sleep: SleepFn) -> None:
        reconnect_attempt = 0
        while not self._stop.is_set():
            try:
                self._socket = factory(url)
                while not self._stop.is_set():
                    raw = self._socket.recv()
                    if raw:
                        self._dispatch_raw(raw)
                        reconnect_attempt = 0
            except Exception:
                if self._stop.is_set():
                    return
                delay = RECONNECT_DELAYS_SECONDS[min(reconnect_attempt, len(RECONNECT_DELAYS_SECONDS) - 1)]
                reconnect_attempt += 1
                sleep(delay)
            finally:
                self._socket = None

    def _dispatch_raw(self, raw: str) -> None:
        try:
            payload = json.loads(raw)
        except json.JSONDecodeError:
            return
        if not isinstance(payload, dict):
            return

        event = _to_write_event(payload)
        if event is None:
            return

        with self._lock:
            registrations = list(self._registrations)

        for registration in registrations:
            if event.operation not in registration.operations or not path_matches(registration.pattern, event.path):
                continue
            pattern_lock = self._pattern_locks.setdefault(registration.pattern, threading.Lock())
            with pattern_lock:
                self._run_handler(registration, event)

    def _run_handler(self, registration: _Registration, event: WriteEvent) -> None:
        try:
            result = registration.handler(event)
            if hasattr(result, "__await__"):
                asyncio.run(result)
        except Exception as exc:
            self._record_handler_error(registration.pattern, event.path, exc)

    def _record_handler_error(self, pattern: str, path: str, error: Exception) -> None:
        payload = {"pattern": pattern, "path": path, "error": error, "retryable": False}
        recorder = getattr(self._client, "record_handler_error", None) or getattr(self._client, "recordHandlerError", None)
        if callable(recorder):
            result = recorder(payload)
            if hasattr(result, "__await__"):
                asyncio.run(result)
            return
        logging.getLogger(__name__).exception("Relayfile on_write handler error", exc_info=error)


def _normalize_pattern(pattern: str) -> list[str]:
    if not isinstance(pattern, str) or not pattern:
        raise ValueError("on_write pattern must be a non-empty string")
    if not pattern.startswith("/"):
        raise ValueError("on_write pattern must start with '/'")
    if "//" in pattern:
        raise ValueError("on_write pattern cannot contain empty path segments")
    segments = _normalize_path(pattern)
    if "**" in segments and segments.index("**") != len(segments) - 1:
        raise ValueError("on_write pattern only supports '**' as the trailing segment")
    return segments


def _normalize_path(path: str) -> list[str]:
    normalized = path if path.startswith("/") else f"/{path}"
    trimmed = normalized.rstrip("/")
    if not trimmed:
        return []
    return [segment for segment in trimmed.split("/") if segment]


def _match_segments(pattern: list[str], path: list[str]) -> bool:
    if pattern and pattern[-1] == "**":
        prefix = pattern[:-1]
        return len(path) >= len(prefix) and all(segment == "*" or segment == path[index] for index, segment in enumerate(prefix))
    return len(pattern) == len(path) and all(segment == "*" or segment == path[index] for index, segment in enumerate(pattern))


def _to_write_event(payload: dict[str, Any]) -> WriteEvent | None:
    event_type = payload.get("type")
    operation = _operation_from_type(event_type)
    path = payload.get("path")
    if operation is None or not isinstance(path, str):
        return None
    return WriteEvent(
        workspace_id=str(payload.get("workspaceId") or payload.get("workspace_id") or ""),
        path=path,
        operation=operation,
        revision=str(payload.get("revision") or ""),
        previous_revision=payload.get("previousRevision") or payload.get("previous_revision"),
        timestamp=str(payload.get("timestamp") or payload.get("ts") or ""),
        source=str(payload.get("source") or _source_from_origin(payload.get("origin"))),
        value=payload.get("value"),
        actor=payload.get("actor"),
    )


def _operation_from_type(event_type: Any) -> str | None:
    if event_type == "file.created":
        return "create"
    if event_type == "file.updated":
        return "update"
    if event_type == "file.deleted":
        return "delete"
    return None


def _source_from_origin(origin: Any) -> str:
    if origin == "agent_write":
        return "agent"
    if origin == "provider_sync":
        return "sync"
    return "api"


def _build_websocket_url(base_url: str, workspace_id: str, token: str) -> str:
    clean_base = base_url.rstrip("/")
    if clean_base.startswith("https://"):
        clean_base = "wss://" + clean_base[len("https://") :]
    elif clean_base.startswith("http://"):
        clean_base = "ws://" + clean_base[len("http://") :]
    return f"{clean_base}/v1/workspaces/{quote(workspace_id, safe='')}/fs/ws?token={quote(token, safe='')}"


def _default_websocket_factory(url: str) -> OnWriteSocket:
    try:
        import websocket  # type: ignore[import-not-found]
    except ImportError as exc:
        raise RuntimeError("Install websocket-client or pass websocket_factory to use on_write.") from exc
    return websocket.create_connection(url)


def _get_default_client() -> RelayFileClient:
    global _default_client
    if _default_client is None:
        token = os.getenv("RELAYFILE_TOKEN")
        if not token:
            raise ValueError("on_write requires client or RELAYFILE_TOKEN")
        _default_client = RelayFileClient(os.getenv("RELAYFILE_BASE_URL") or DEFAULT_RELAYFILE_BASE_URL, token)
    return _default_client
