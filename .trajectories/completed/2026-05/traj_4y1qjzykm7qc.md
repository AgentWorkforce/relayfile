# Trajectory: Migrate legacy agent-relay on/off mount UX into relayfile Go CLI

> **Status:** ✅ Completed
> **Confidence:** 85%
> **Started:** May 31, 2026 at 11:36 PM
> **Completed:** May 31, 2026 at 11:41 PM

---

## Summary

Added on/off aliases to relayfile CLI mapping legacy agent-relay relay on/off mount UX to mount/stop; help text, usage, docs, and dispatch tests added; full suite green

**Approach:** Standard approach

---

## Key Decisions

### Map legacy on/off to relayfile mount/stop via on=mount and off=stop aliases
- **Chose:** Map legacy on/off to relayfile mount/stop via on=mount and off=stop aliases
- **Reasoning:** relayfile is the mount substrate only (not an agent launcher). Legacy 'on' mounted provider files into the workspace; relayfile 'mount'/'start' already does that. Legacy 'off' stopped the mount; relayfile 'stop' does that. Adding on/off as aliases preserves the migrated mount UX with minimal surface and matches existing start/mount and stop verbs.

---

## Chapters

### 1. Work
*Agent: default*

- Map legacy on/off to relayfile mount/stop via on=mount and off=stop aliases: Map legacy on/off to relayfile mount/stop via on=mount and off=stop aliases

---

## Artifacts

**Commits:** 1386546
**Files changed:** 1
