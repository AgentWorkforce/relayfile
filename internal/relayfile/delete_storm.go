package relayfile

import (
	"errors"
	"time"
)

// Delete-storm breaker (issue #249, service-side layer).
//
// The 2026-06-06 incident: stale private mount state over a layout change made
// a client push file_delete for every tracked record — the service admitted
// ~186 canonical-record deletions from one mount correlation family within
// minutes. The binary-side guard (#249 main body) refuses the false diff at
// the client; this breaker is the layer that survives future client bugs: a
// workspace's legitimate delete rate is near zero, so a burst is treated as
// hostile until proven otherwise.
//
// Two protected surfaces, matching the incident's two arming paths:
//   - WRITE TIME: DeleteFile refuses admissions beyond the threshold within
//     the sliding window. The fs mutation itself is refused — the incident's
//     actual harm was the fs damage (every provider call failed and the
//     records were still destroyed).
//   - BOOT TIME: the construction-time scan re-enqueues every pending/running
//     op (a deploy is a restart). A rehydrated burst of pending file_delete
//     ops above the threshold is flipped to "quarantined" instead:
//     outside the boot allowlist, invisible to /writeback/pending, and not
//     replayable (ReplayOperation requires dead_lettered).
//
// Disabled unless DeleteStormThreshold > 0 — enabling it (and sizing the
// threshold) is a deployment decision.

// ErrDeleteStormRejected is returned by DeleteFile when the workspace's
// file_delete admission rate exceeds the configured threshold within the
// configured window.
var ErrDeleteStormRejected = errors.New("delete storm rejected: file_delete burst exceeds configured threshold")

const defaultDeleteStormWindow = time.Minute

// admitDeleteLocked records a file_delete admission attempt and reports
// whether it may proceed. Caller must hold s.mu. Refused attempts are also
// recorded so the breaker stays latched while a storm is ongoing; the window
// must go quiet before deletes are admitted again.
func (s *Store) admitDeleteLocked(workspaceID string, now time.Time) error {
	if s.deleteStormThreshold <= 0 {
		return nil
	}
	window := s.deleteStormWindow
	if window <= 0 {
		window = defaultDeleteStormWindow
	}
	cutoff := now.Add(-window)
	admissions := s.deleteStormAdmissions[workspaceID]
	kept := admissions[:0]
	for _, ts := range admissions {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= s.deleteStormThreshold {
		s.deleteStormAdmissions[workspaceID] = append(kept, now)
		return ErrDeleteStormRejected
	}
	s.deleteStormAdmissions[workspaceID] = append(kept, now)
	return nil
}

// quarantineBootDeleteStorms runs once at store construction, after state
// load and before the boot re-enqueue scan. Workspaces holding more
// pending/running file_delete ops than the threshold get that entire delete
// set flipped to "quarantined": the boot allowlist then skips them, external
// consumers never see them, and ReplayOperation refuses them. Releasing a
// quarantined op is a deliberate operator action (ack it, or a future
// explicit release API) — never automatic.
// Two verb-asymmetric rules, matching the two armed classes the 2026-06-06
// incident produced:
//
//   - DELETE STORMS: a rehydrated pending/running file_delete burst above
//     DeleteStormThreshold is the storm signature at a different entry point.
//   - STALE RUNNING (op_20440 class, verb-agnostic): any op still "running"
//     at boot executed in a previous process life — its executor is dead.
//     Ones older than StaleRunningOpThreshold are abandoned and quarantined;
//     fresher ones keep the existing at-least-once crash semantics and
//     re-arm as before. A stuck-running upsert is an armed provider
//     duplicate exactly like a stuck delete.
func (s *Store) quarantineBootDeleteStorms() {
	if s.deleteStormThreshold <= 0 && s.staleRunningOpThreshold <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	changed := false
	for _, ws := range s.workspaces {
		quarantine := make([]string, 0)

		if s.deleteStormThreshold > 0 {
			stormOps := make([]string, 0)
			for opID, op := range ws.Ops {
				if (op.Status == "pending" || op.Status == "running") && op.Action == string(WritebackActionFileDelete) {
					stormOps = append(stormOps, opID)
				}
			}
			if len(stormOps) > s.deleteStormThreshold {
				quarantine = append(quarantine, stormOps...)
			}
		}

		if s.staleRunningOpThreshold > 0 {
			for opID, op := range ws.Ops {
				if op.Status != "running" {
					continue
				}
				updatedAt, err := time.Parse(time.RFC3339Nano, op.UpdatedAt)
				if err != nil {
					// Unparseable age on an op from a dead process life:
					// treat as stale rather than re-arm blind.
					quarantine = append(quarantine, opID)
					continue
				}
				if now.Sub(updatedAt) > s.staleRunningOpThreshold {
					quarantine = append(quarantine, opID)
				}
			}
		}

		if len(quarantine) == 0 {
			continue
		}
		nowTS := nowRFC3339NanoUTC()
		for _, opID := range quarantine {
			op := ws.Ops[opID]
			if op.Status == "quarantined" {
				continue
			}
			op.Status = "quarantined"
			op.NextAttemptAt = nil
			op.UpdatedAt = nowTS
			ws.Ops[opID] = op
		}
		changed = true
	}
	if changed {
		_ = s.saveLocked()
	}
}
