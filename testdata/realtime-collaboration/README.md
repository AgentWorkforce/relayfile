# Relayfile real-time collaboration fixture

This deliberately small service has three independently testable seams:

- `internal/backend`: JSON API ownership
- `internal/frontend`: HTML rendering ownership
- `internal/model`: a shared contract both sides must extend

The two-machine exercise seeds this directory into relayfile, initializes a
separate local Git repository in each mount, and asks concurrent agents to
implement interdependent backend and frontend changes while collision,
restart, and network-partition scenarios are injected.
