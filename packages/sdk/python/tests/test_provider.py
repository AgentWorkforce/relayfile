from __future__ import annotations

import pytest

from relayfile import DIGEST_PATHS, provider_layout_path, resource_schema_path


def test_digest_paths_are_canonical_workspace_primitive_paths() -> None:
    assert DIGEST_PATHS == ("digests/yesterday.md", "digests/today.md")


def test_provider_layout_path_normalizes_provider_segment() -> None:
    assert provider_layout_path("linear") == "linear/.layout.md"
    assert provider_layout_path("/notion/") == "notion/.layout.md"


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
