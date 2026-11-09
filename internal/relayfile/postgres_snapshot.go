package relayfile

import "encoding/json"

// The verified path admits only complete snapshots emitted by saveLocked.
// An absent database row is a fresh store; an existing null/partial row is not.
// Optional forks/checkpoint fields remain optional for older snapshots.
func decodeVerifiedPostgresSnapshot(payload []byte) (*persistedState, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil || fields == nil {
		return nil, postgresRefuse("state_shape_refused")
	}
	for _, name := range []string{"revCounter", "opCounter", "eventCounter"} {
		value, present := fields[name]
		var number *uint64
		if !present || json.Unmarshal(value, &number) != nil || number == nil {
			return nil, postgresRefuse("state_shape_refused")
		}
	}
	for _, name := range []string{"workspaces", "envelopesById", "deliveryIndex", "processedEnvs", "ingressByWorkspace", "envelopeAttempts", "envelopeNextAttempt", "deadLetters", "suppressions"} {
		value, present := fields[name]
		var object map[string]json.RawMessage
		if !present || json.Unmarshal(value, &object) != nil || object == nil {
			return nil, postgresRefuse("state_shape_refused")
		}
	}
	var state persistedState
	if json.Unmarshal(payload, &state) != nil {
		return nil, postgresRefuse("state_shape_refused")
	}
	for _, workspace := range state.Workspaces {
		if workspace == nil {
			return nil, postgresRefuse("state_shape_refused")
		}
	}
	for _, fork := range state.Forks {
		if fork == nil {
			return nil, postgresRefuse("state_shape_refused")
		}
	}
	return &state, nil
}
