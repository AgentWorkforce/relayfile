from __future__ import annotations

import json
import queue
import threading
import time
from typing import Any

import pytest

from relayfile import RelayFileClient, on_write, path_matches


BASE = "https://relay.test"


class FakeSocket:
    def __init__(self) -> None:
        self.messages: queue.Queue[str | BaseException] = queue.Queue()
        self.closed = False

    def recv(self) -> str:
        item = self.messages.get(timeout=1)
        if isinstance(item, BaseException):
            raise item
        return item

    def close(self) -> None:
        self.closed = True
        self.messages.put(ConnectionError("closed"))

    def emit(self, payload: dict[str, Any]) -> None:
        self.messages.put(json.dumps(payload))

    def fail(self) -> None:
        self.messages.put(ConnectionError("dropped"))


def event(path: str, event_type: str = "file.updated") -> dict[str, Any]:
    return {
        "eventId": f"evt:{path}",
        "type": event_type,
        "path": path,
        "revision": "rev_1",
        "timestamp": "2026-05-06T10:00:00Z",
        "origin": "provider_sync",
    }


def wait_for(condition: Any, timeout: float = 1.0) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if condition():
            return
        time.sleep(0.01)
    raise AssertionError("condition was not met before timeout")


class RecordingClient(RelayFileClient):
    def __init__(self) -> None:
        super().__init__(BASE, "tok_test")
        self.handler_errors: list[dict[str, Any]] = []

    def record_handler_error(self, payload: dict[str, Any]) -> None:
        self.handler_errors.append(payload)


def test_path_matches_requested_patterns() -> None:
    assert path_matches("/notion/pages/calls/*/transcript", "/notion/pages/calls/2026-05-08/transcript")
    assert not path_matches("/notion/pages/calls/*/transcript", "/notion/pages/calls/2026-05-08/notes/transcript")
    assert path_matches("/linear/issues/**", "/linear/issues/PROJ-441/comments/c-1")
    assert path_matches("/linear/issues/**", "/linear/issues")
    assert path_matches("/github/repos/acme/api/pulls/*", "/github/repos/acme/api/pulls/42")
    assert not path_matches("/github/repos/acme/api/pulls/*", "/github/repos/acme/api/pulls/42/files")


def test_invalid_pattern_throws_synchronously() -> None:
    with pytest.raises(ValueError, match="start with"):
        path_matches("linear/issues/**", "/linear/issues/PROJ-1")
    with pytest.raises(ValueError, match="trailing"):
        path_matches("/linear/**/comments", "/linear/PROJ-1/comments")
    with pytest.raises(ValueError, match="empty"):
        on_write("/linear//issues/*", lambda event: None, client=RecordingClient(), workspace_id="ws_acme")


def test_on_write_dispatches_matching_events_and_shares_socket() -> None:
    client = RecordingClient()
    sockets: list[FakeSocket] = []
    calls: list[str] = []
    socket_ready = threading.Event()

    def factory(url: str) -> FakeSocket:
        assert url == "wss://relay.test/v1/workspaces/ws_acme/fs/ws?token=tok_test"
        socket = FakeSocket()
        sockets.append(socket)
        socket_ready.set()
        return socket

    unsub_transcript = on_write(
        "/notion/pages/calls/*/transcript",
        lambda evt: calls.append(f"transcript:{evt.path}:{evt.operation}:{evt.source}"),
        client=client,
        workspace_id="ws_acme",
        base_url=BASE,
        token="tok_test",
        websocket_factory=factory,
    )
    unsub_linear = on_write(
        "/linear/issues/**",
        lambda evt: calls.append(f"linear:{evt.path}"),
        client=client,
        workspace_id="ws_acme",
        base_url=BASE,
        token="tok_test",
    )
    unsub_pull = on_write(
        "/github/repos/acme/api/pulls/*",
        lambda evt: calls.append(f"pull:{evt.path}"),
        client=client,
        workspace_id="ws_acme",
        base_url=BASE,
        token="tok_test",
    )

    assert socket_ready.wait(1)
    assert len(sockets) == 1
    sockets[0].emit(event("/notion/pages/calls/call-1/transcript"))
    sockets[0].emit(event("/notion/pages/calls/call-1/notes/transcript"))
    sockets[0].emit(event("/linear/issues/PROJ-441/comments/c-1"))
    sockets[0].emit(event("/github/repos/acme/api/pulls/42", "file.created"))

    wait_for(lambda: len(calls) == 3)
    assert calls == [
        "transcript:/notion/pages/calls/call-1/transcript:update:sync",
        "linear:/linear/issues/PROJ-441/comments/c-1",
        "pull:/github/repos/acme/api/pulls/42",
    ]

    unsub_transcript()
    unsub_linear()
    unsub_pull()


def test_handler_error_isolated_and_recorded() -> None:
    client = RecordingClient()
    sockets: list[FakeSocket] = []
    calls: list[str] = []
    socket_ready = threading.Event()

    def factory(url: str) -> FakeSocket:
        socket = FakeSocket()
        sockets.append(socket)
        socket_ready.set()
        return socket

    def boom(_: Any) -> None:
        raise RuntimeError("boom")

    unsub_1 = on_write(
        "/linear/issues/**",
        boom,
        client=client,
        workspace_id="ws_acme",
        base_url=BASE,
        token="tok_test",
        websocket_factory=factory,
    )
    unsub_2 = on_write(
        "/linear/issues/**",
        lambda evt: calls.append(evt.path),
        client=client,
        workspace_id="ws_acme",
        base_url=BASE,
        token="tok_test",
    )

    assert socket_ready.wait(1)
    sockets[0].emit(event("/linear/issues/PROJ-1"))

    wait_for(lambda: len(calls) == 1 and len(client.handler_errors) == 1)
    assert calls == ["/linear/issues/PROJ-1"]
    assert client.handler_errors[0]["pattern"] == "/linear/issues/**"
    assert client.handler_errors[0]["path"] == "/linear/issues/PROJ-1"
    assert client.handler_errors[0]["retryable"] is False

    unsub_1()
    unsub_2()


def test_reconnect_backoff_uses_one_then_two_seconds() -> None:
    client = RecordingClient()
    sockets: list[FakeSocket] = []
    delays: list[float] = []
    observed = threading.Event()

    def factory(_: str) -> FakeSocket:
        socket = FakeSocket()
        sockets.append(socket)
        socket.fail()
        return socket

    def sleep(delay: float) -> None:
        delays.append(delay)
        if len(delays) >= 2:
            observed.set()
        time.sleep(0.01)

    unsubscribe = on_write(
        "/github/repos/acme/api/pulls/*",
        lambda event: None,
        client=client,
        workspace_id="ws_acme",
        base_url=BASE,
        token="tok_test",
        websocket_factory=factory,
        _sleep=sleep,
    )

    assert observed.wait(1)
    unsubscribe()
    assert delays[:2] == [1.0, 2.0]
    assert len(sockets) >= 2


def test_rejects_mixed_workspace_on_same_client() -> None:
    """A single RelayFileClient is bound to one workspace. The second on_write
    must not silently attach to the first workspace's socket — reject it.
    """
    client = RecordingClient()
    sockets: list[FakeSocket] = []

    def factory(_: str) -> FakeSocket:
        socket = FakeSocket()
        sockets.append(socket)
        return socket

    unsub = on_write(
        "/notion/pages/calls/*/transcript",
        lambda evt: None,
        client=client,
        workspace_id="ws_acme",
        base_url=BASE,
        token="tok_test",
        websocket_factory=factory,
    )

    with pytest.raises(ValueError, match="same workspace_id"):
        on_write(
            "/linear/issues/**",
            lambda evt: None,
            client=client,
            workspace_id="ws_other",
            base_url=BASE,
            token="tok_test",
        )

    assert len(sockets) == 1, "second registration must not have started a socket"
    unsub()


def test_isolates_dispatch_when_recorder_raises() -> None:
    """recordHandlerError implementations that raise must not break sequential
    dispatch for the pattern.
    """

    class ExplodingClient(RelayFileClient):
        def __init__(self) -> None:
            super().__init__(BASE, "tok_test")

        def record_handler_error(self, payload: dict[str, Any]) -> None:
            raise RuntimeError("telemetry exploded")

    client = ExplodingClient()
    sockets: list[FakeSocket] = []
    survived: list[str] = []
    socket_ready = threading.Event()

    def factory(_: str) -> FakeSocket:
        socket = FakeSocket()
        sockets.append(socket)
        socket_ready.set()
        return socket

    unsub_one = on_write(
        "/linear/issues/**",
        lambda _: (_ for _ in ()).throw(RuntimeError("user handler boom")),
        client=client,
        workspace_id="ws_acme",
        base_url=BASE,
        token="tok_test",
        websocket_factory=factory,
    )
    unsub_two = on_write(
        "/linear/issues/**",
        lambda evt: survived.append(evt.path),
        client=client,
        workspace_id="ws_acme",
        base_url=BASE,
        token="tok_test",
    )

    assert socket_ready.wait(1)
    sockets[0].emit(event("/linear/issues/PROJ-1"))

    # The reporter raised, but the second handler still ran for this path.
    wait_for(lambda: survived == ["/linear/issues/PROJ-1"])

    unsub_one()
    unsub_two()


def test_dispatcher_restarts_after_full_drain() -> None:
    """Regression: re-subscribing on the same client after the last unsubscribe
    must spin up a fresh worker thread and deliver events. Previously the
    dispatcher's stop event stayed permanently set and `_thread` stayed
    non-None, so subsequent registrations silently never received anything.
    """
    client = RecordingClient()
    sockets: list[FakeSocket] = []
    factory_signal = threading.Event()

    def factory(_: str) -> FakeSocket:
        socket = FakeSocket()
        sockets.append(socket)
        factory_signal.set()
        return socket

    calls_one: list[str] = []
    unsub_one = on_write(
        "/linear/issues/**",
        lambda evt: calls_one.append(evt.path),
        client=client,
        workspace_id="ws_acme",
        base_url=BASE,
        token="tok_test",
        websocket_factory=factory,
    )
    assert factory_signal.wait(1)
    sockets[0].emit(event("/linear/issues/PROJ-1"))
    wait_for(lambda: calls_one == ["/linear/issues/PROJ-1"])

    unsub_one()  # drain — dispatcher should reset internal state

    # Re-subscribe on the same client.
    factory_signal.clear()
    calls_two: list[str] = []
    unsub_two = on_write(
        "/linear/issues/**",
        lambda evt: calls_two.append(evt.path),
        client=client,
        workspace_id="ws_acme",
        base_url=BASE,
        token="tok_test",
        websocket_factory=factory,
    )
    assert factory_signal.wait(1), "second registration did not start a worker"
    assert len(sockets) == 2

    sockets[1].emit(event("/linear/issues/PROJ-2"))
    wait_for(lambda: calls_two == ["/linear/issues/PROJ-2"])

    unsub_two()
