# Layout

During the mount naming transition, every entity-named segment is `<sanitized-human-readable>__<id>`; IDs are the LAST `__`-separated segment. Consumers MUST continue to accept both the new-style filename and the legacy `<id>`-only basename while producers are being updated.

For example:

```text
inbox/threads/Re_Welcome__01HXYZ.json
inbox/threads/01HXYZ.json
```

The first path is the canonical human-readable form. The second path is the legacy fallback that remains readable during the transition.
