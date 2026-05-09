# Trajectory: relayfile workspace current + active marker in 'workspace list'

> **Status:** ✅ Completed
> **Confidence:** 85%
> **Started:** May 9, 2026 at 08:44 PM
> **Completed:** May 9, 2026 at 08:48 PM

---

## Summary

Added 'relayfile workspace current' subcommand and active marker (* ) in 'workspace list' (with --names-only escape hatch). Reuses existing resolveWorkspaceRecord precedence (env > token > default). 4 new tests, 2 existing list tests updated for marker contract.

**Approach:** Standard approach
