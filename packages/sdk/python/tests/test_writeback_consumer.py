from __future__ import annotations

from typing import Any

from relayfile import WritebackConsumer, WritebackItem


class FakeClient:
    def __init__(self, items: list[WritebackItem]) -> None:
        self.items = items
        self.acks: list[Any] = []

    def list_pending_writebacks(self, workspace_id: str) -> list[WritebackItem]:
        assert workspace_id == "ws_123"
        return self.items

    def ack_writeback(self, input: Any) -> dict[str, Any]:
        self.acks.append(input)
        return {"status": "acknowledged", "id": input.item_id}


class FakeProvider:
    name = "provider"


class Handler:
    def __init__(self, *, fail: bool = False) -> None:
        self.fail = fail
        self.executed: list[str] = []

    def can_handle(self, path: str) -> bool:
        return path.endswith(".json")

    def execute(self, item: WritebackItem, provider: FakeProvider) -> None:
        self.executed.append(item.id)
        if self.fail:
            raise RuntimeError("provider rejected write")


def item(path: str = "/github/issues/1.json") -> WritebackItem:
    return WritebackItem(
        id="wb_1",
        workspace_id="ws_123",
        path=path,
        revision="rev_1",
        correlation_id="corr_1",
    )


def test_writeback_consumer_acks_success() -> None:
    client = FakeClient([item()])
    handler = Handler()
    consumer = WritebackConsumer(
        client=client,  # type: ignore[arg-type]
        workspace_id="ws_123",
        handlers=[handler],
        provider=FakeProvider(),  # type: ignore[arg-type]
        poll_interval_ms=0,
    )

    consumer.poll_once()

    assert handler.executed == ["wb_1"]
    assert client.acks[0].success is True
    assert client.acks[0].correlation_id == "corr_1"


def test_writeback_consumer_acks_failure_when_handler_fails() -> None:
    client = FakeClient([item()])
    consumer = WritebackConsumer(
        client=client,  # type: ignore[arg-type]
        workspace_id="ws_123",
        handlers=[Handler(fail=True)],
        provider=FakeProvider(),  # type: ignore[arg-type]
    )

    consumer.poll_once()

    assert client.acks[0].success is False
    assert "provider rejected write" in client.acks[0].error


def test_writeback_consumer_dead_letters_unhandled_paths() -> None:
    client = FakeClient([item("/github/issues/1.md")])
    consumer = WritebackConsumer(
        client=client,  # type: ignore[arg-type]
        workspace_id="ws_123",
        handlers=[Handler()],
        provider=FakeProvider(),  # type: ignore[arg-type]
    )

    consumer.poll_once()

    assert client.acks[0].success is False
    assert "No writeback handler found" in client.acks[0].error
