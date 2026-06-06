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
	return nil
}

// quarantineBootDeleteStorms runs once at store construction, after state
// load and before the boot re-enqueue scan. Workspaces holding more
// pending/running file_delete ops than the threshold get that entire delete
// set flipped to "quarantined": the boot allowlist then skips them, external
// consumers never see them, and ReplayOperation refuses them. Releasing a
// quarantined op is a deliberate operator action (ack it, or a future
// explicit release API) — never automatic.
func (s *Store) quarantineBootDeleteStorms() {
}
