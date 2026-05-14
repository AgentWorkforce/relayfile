# Python SDK

Keep the Python SDK idiomatic and in parity with the TypeScript surface.

- Prefer snake_case names for Python-specific APIs; only keep camelCase when mirroring HTTP JSON keys from the wire format.
- Re-export public SDK symbols from `src/relayfile/__init__.py` whenever you add or remove client types, errors, or provider helpers.
- Maintain both sync and async client behavior when adding endpoints or retry/error handling.
- Test HTTP behavior with `pytest`, `pytest-asyncio`, `respx`, and `httpx.Response` mocks under `tests/`.
- Verify with `pytest packages/sdk/python/tests`.
- Workspace primitive SDK changes should remain additive: mirror the TypeScript
  type surface, expose path helpers for digest/layout/schema virtual files, and
  avoid adding client methods until the HTTP OpenAPI contract exists.
