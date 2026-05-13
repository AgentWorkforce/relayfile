from __future__ import annotations

import inspect

from relayfile import (
    DeadLetterErrorPayload,
    DigestBullet,
    DigestSection,
    DigestWindow,
    LayoutManifest,
    LayoutManifestAlias,
    LayoutManifestResource,
    WritebackDeadLetterError,
    WritebackItem,
    WritebackItemDetail,
    WritebackSchemaRef,
)


def test_writeback_item_accepts_dead_letter_detail_fields() -> None:
    error = DeadLetterErrorPayload(
        code="provider_4xx",
        message="Provider rejected writeback",
        attempts=4,
        first_attempt_at="2026-05-13T14:30:01Z",
        last_attempt_at="2026-05-13T14:32:07Z",
        op_id="op_123",
        provider_status=422,
        provider_response={"error": "invalid state"},
    )
    item = WritebackItem(
        id="wb_1",
        workspace_id="ws_acme",
        path="/linear/issues/AGE-16__abc/comments/wb-1778700000.json",
        revision="rev_7",
        correlation_id="corr_1",
        state="dead_lettered",
        op_id="op_123",
        provider="linear",
        action="file_upsert",
        attempt_count=4,
        created_at="2026-05-13T14:29:59Z",
        first_attempt_at="2026-05-13T14:30:01Z",
        last_attempt_at="2026-05-13T14:32:07Z",
        error=error,
    )

    assert item.error is error
    assert item.state == "dead_lettered"
    assert item.attempt_count == 4
    assert item.op_id == "op_123"
    assert item.error.provider_response == {"error": "invalid state"}


def test_writeback_dead_letter_error_back_compat_alias() -> None:
    # The plan exposes DeadLetterErrorPayload as the canonical public name.
    # WritebackDeadLetterError is preserved as a transition alias so older
    # consumers do not break during rollout.
    assert WritebackDeadLetterError is DeadLetterErrorPayload


def test_dead_letter_error_payload_from_dict_accepts_snake_or_camel() -> None:
    snake = DeadLetterErrorPayload.from_dict(
        {
            "code": "timeout",
            "message": "Provider call exceeded deadline",
            "attempts": 3,
            "first_attempt_at": "2026-05-13T14:30:01Z",
            "last_attempt_at": "2026-05-13T14:31:01Z",
            "op_id": "op_snake",
        }
    )
    camel = DeadLetterErrorPayload.from_dict(
        {
            "code": "timeout",
            "message": "Provider call exceeded deadline",
            "attempts": 3,
            "firstAttemptAt": "2026-05-13T14:30:01Z",
            "lastAttemptAt": "2026-05-13T14:31:01Z",
            "opId": "op_camel",
        }
    )
    assert snake.first_attempt_at == "2026-05-13T14:30:01Z"
    assert camel.op_id == "op_camel"
    assert snake.provider_status is None
    assert camel.provider_response is None


def test_writeback_item_from_dict_coerces_dead_letter_payload() -> None:
    item = WritebackItem.from_dict(
        {
            "id": "wb_dead",
            "workspaceId": "ws_acme",
            "path": "/linear/issues/AGE-16__abc/comments/wb-1778700000.json",
            "revision": "rev_7",
            "correlationId": "corr_1",
            "state": "dead_lettered",
            "opId": "op_123",
            "provider": "linear",
            "action": "file_upsert",
            "attemptCount": 4,
            "firstAttemptAt": "2026-05-13T14:30:01Z",
            "lastAttemptAt": "2026-05-13T14:32:07Z",
            "createdAt": "2026-05-13T14:29:59Z",
            "error": {
                "code": "schema_violation",
                "message": "Comment body is required",
                "providerStatus": 422,
                "providerResponse": {"field": "body"},
                "attempts": 4,
                "firstAttemptAt": "2026-05-13T14:30:01Z",
                "lastAttemptAt": "2026-05-13T14:32:07Z",
                "opId": "op_123",
            },
        }
    )

    assert item.op_id == "op_123"
    assert item.attempt_count == 4
    assert isinstance(item.error, DeadLetterErrorPayload)
    assert item.error.code == "schema_violation"


def test_writeback_item_detail_keeps_error_optional_for_rollout_parity() -> None:
    item = WritebackItemDetail(
        id="wb_2",
        workspace_id="ws_acme",
        path="/github/issues/42.json",
        revision="rev_8",
    )

    assert item.error is None


def test_digest_and_layout_manifest_shapes() -> None:
    window = DigestWindow(
        from_="2026-05-12T00:00:00Z",
        to="2026-05-13T00:00:00Z",
    )
    section = DigestSection(
        provider="linear",
        bullets=[
            DigestBullet(
                text="AGE-16 moved to in-review",
                canonical_path="/linear/issues/AGE-16__abc.json",
            )
        ],
    )
    schema_ref = WritebackSchemaRef(
        provider="linear",
        resource="comments",
        path="linear/issues/AGE-16__abc/comments/.schema.json",
    )
    manifest = LayoutManifest(
        provider="linear",
        materialization="lazy",
        resources=[
            LayoutManifestResource(
                name="issues",
                canonical_filename="<identifier>__<uuid>.json",
                writeback_actions=["comment"],
                writeback_schemas=[schema_ref],
                aliases=[LayoutManifestAlias(segment="by-id")],
            )
        ],
    )

    assert window.from_ == "2026-05-12T00:00:00Z"
    assert section.bullets[0].canonical_path == "/linear/issues/AGE-16__abc.json"
    assert manifest.resources[0].writeback_schemas == [schema_ref]


def test_digest_context_protocol_is_runtime_importable() -> None:
    import relayfile

    assert inspect.isclass(relayfile.DigestContext)
