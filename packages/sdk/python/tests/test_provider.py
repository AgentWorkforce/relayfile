from __future__ import annotations

import pytest

from relayfile import (
    DATE_STAMPED_DIGEST_PATH_PATTERN,
    DIGEST_PATHS,
    is_digest_path,
    provider_layout_path,
    resource_schema_path,
)


def test_digest_paths_are_canonical_workspace_primitive_paths() -> None:
    assert DIGEST_PATHS == (
        "digests/yesterday.md",
        "digests/today.md",
        "digests/this-week.md",
        "digests/last-week.md",
    )


def test_is_digest_path_matches_literal_anchor_paths() -> None:
    assert is_digest_path("digests/today.md") is True
    assert is_digest_path("digests/yesterday.md") is True
    assert is_digest_path("digests/this-week.md") is True
    assert is_digest_path("/digests/this-week.md") is True
    assert is_digest_path("digests/last-week.md") is True
    assert is_digest_path("/digests/last-week.md") is True


def test_is_digest_path_rejects_near_matches_to_weekly_anchors() -> None:
    # Spelling variants and wrong suffixes must NOT be treated as a
    # digest path even though they share the prefix.
    assert is_digest_path("digests/this-weak.md") is False
    assert is_digest_path("digests/this-week.txt") is False
    assert is_digest_path("digests/this_week.md") is False
    assert is_digest_path("digests/last-weak.md") is False
    assert is_digest_path("digests/last-week.txt") is False
    assert is_digest_path("digests/last_week.md") is False


def test_is_digest_path_matches_date_stamped_form() -> None:
    assert is_digest_path("digests/2026-05-12.md") is True
    assert is_digest_path("digests/2026-01-01.md") is True
    assert is_digest_path("digests/2025-12-31.md") is True


def test_is_digest_path_rejects_invalid_or_foreign_paths() -> None:
    # Invalid calendar date.
    assert is_digest_path("digests/2026-02-30.md") is False
    # Wrong suffix.
    assert is_digest_path("digests/2026-05-12.json") is False
    # Wrong prefix.
    assert is_digest_path("notion/pages/2026-05-12.md") is False
    # Bad shape (template placeholder is not a path).
    assert is_digest_path("digests/YYYY-MM-DD.md") is False
    # Mirrors Go normalization — leading/trailing slashes are stripped
    # before matching, so this is recognized.
    assert is_digest_path("/digests/yesterday.md") is True


def test_date_stamped_digest_pattern_is_exposed() -> None:
    assert DATE_STAMPED_DIGEST_PATH_PATTERN.match("digests/2026-05-12.md") is not None
    assert DATE_STAMPED_DIGEST_PATH_PATTERN.match("digests/today.md") is None


def test_provider_layout_path_normalizes_provider_segment() -> None:
    assert provider_layout_path("linear") == "linear/LAYOUT.md"
    assert provider_layout_path("/notion/") == "notion/LAYOUT.md"


def test_resource_schema_path_normalizes_segments() -> None:
    assert (
        resource_schema_path("linear", "/issues/AGE-16__abc/comments/")
        == "linear/issues/AGE-16__abc/comments/.schema.json"
    )


def test_virtual_path_helpers_reject_empty_segments() -> None:
    with pytest.raises(ValueError, match="provider is required"):
        provider_layout_path(" / ")
    with pytest.raises(ValueError, match="resource_path is required"):
        resource_schema_path("linear", "")
