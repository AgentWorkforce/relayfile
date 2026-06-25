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

    def ack_writeback(self, ack_input: Any) -> dict[str, Any]:
        self.acks.append(ack_input)
        return {"status": "acknowledged", "id": ack_input.item_id}


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


def item(path: str = "/github/issues/1.json", *, item_id: str = "wb_1") -> WritebackItem:
    return WritebackItem(
        id=item_id,
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


class FlakyAckClient(FakeClient):
    def __init__(self, items: list[WritebackItem], *, fail_times: int) -> None:
        super().__init__(items)
        self.fail_times = fail_times
        self.ack_attempts = 0

    def ack_writeback(self, ack_input: Any) -> dict[str, Any]:
        self.ack_attempts += 1
        if self.ack_attempts <= self.fail_times:
            raise RuntimeError("transient ack failure")
        return super().ack_writeback(ack_input)


def test_writeback_consumer_retries_transient_ack_failures() -> None:
    client = FlakyAckClient([item()], fail_times=1)
    consumer = WritebackConsumer(
        client=client,  # type: ignore[arg-type]
        workspace_id="ws_123",
        handlers=[Handler()],
        provider=FakeProvider(),  # type: ignore[arg-type]
        ack_max_attempts=3,
    )

    consumer.poll_once()

    assert client.ack_attempts == 2
    assert client.acks[0].success is True


def test_writeback_consumer_records_exhausted_ack_and_continues() -> None:
    client = FlakyAckClient([item(item_id="wb_1"), item(item_id="wb_2")], fail_times=2)
    handler = Handler()
    consumer = WritebackConsumer(
        client=client,  # type: ignore[arg-type]
        workspace_id="ws_123",
        handlers=[handler],
        provider=FakeProvider(),  # type: ignore[arg-type]
        ack_max_attempts=2,
    )

    consumer.poll_once()

    assert handler.executed == ["wb_1", "wb_2"]
    assert client.ack_attempts == 3
    assert [ack.item_id for ack in client.acks] == ["wb_2"]
    assert consumer.ack_errors[0][0] == "wb_1"
    assert "ack_writeback failed after 2 attempts" in str(consumer.ack_errors[0][1])


def test_writeback_consumer_does_not_reexecute_completed_redelivery() -> None:
    client = FlakyAckClient([item()], fail_times=2)
    handler = Handler()
    consumer = WritebackConsumer(
        client=client,  # type: ignore[arg-type]
        workspace_id="ws_123",
        handlers=[handler],
        provider=FakeProvider(),  # type: ignore[arg-type]
        ack_max_attempts=2,
    )

    consumer.poll_once()
    consumer.poll_once()

    assert handler.executed == ["wb_1"]
    assert client.ack_attempts == 3
    assert client.acks[0].success is True
    assert client.acks[0].item_id == "wb_1"
