package relayfile

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
)

// encoding/json accepts duplicate keys and case-insensitive struct aliases.
// Neither may override a separately validated authoritative snapshot field.
func verifiedSnapshotObject(payload []byte, known []string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, postgresRefuse("state_shape_refused")
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return nil, postgresRefuse("state_shape_refused")
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, postgresRefuse("state_shape_refused")
		}
		for _, canonical := range known {
			if name != canonical && strings.EqualFold(name, canonical) {
				return nil, postgresRefuse("state_shape_refused")
			}
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, postgresRefuse("state_shape_refused")
		}
		fields[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, postgresRefuse("state_shape_refused")
	}
	var extra json.RawMessage
	if decoder.Decode(&extra) != io.EOF {
		return nil, postgresRefuse("state_shape_refused")
	}
	return fields, nil
}

func verifiedOperationCounter(id string, counter uint64) bool {
	if !strings.HasPrefix(id, "op_") {
		return true
	}
	digits := strings.TrimPrefix(id, "op_")
	if digits == "" || strings.IndexFunc(digits, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return true // Preserve non-generated legacy operation identifiers.
	}
	sequence, err := strconv.ParseUint(digits, 10, 64)
	return err == nil && sequence <= counter
}

// The verified path admits only complete snapshots emitted by saveLocked.
// An absent database row is a fresh store; an existing null/partial row is not.
// Optional forks/checkpoint fields remain optional for older snapshots.
func decodeVerifiedPostgresSnapshot(payload []byte) (*persistedState, error) {
	fields, err := verifiedSnapshotObject(payload, []string{"revCounter", "opCounter", "eventCounter", "workspaces", "forks", "envelopesById", "deliveryIndex", "processedEnvs", "ingressByWorkspace", "envelopeAttempts", "envelopeNextAttempt", "deadLetters", "suppressions", "checkpointSeals", "checkpointGenerations", "checkpointConsumerKeys"})
	if err != nil {
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
	workspaces, err := verifiedSnapshotObject(fields["workspaces"], nil)
	if err != nil {
		return nil, postgresRefuse("state_shape_refused")
	}
	for id, workspace := range state.Workspaces {
		if workspace == nil || workspace.Files == nil || workspace.Ops == nil {
			return nil, postgresRefuse("state_shape_refused")
		}
		if _, err := verifiedSnapshotObject(workspaces[id], []string{"revision", "files", "events", "aclPermissionsByEvent", "ops", "providerIndex", "providerWatermarks"}); err != nil {
			return nil, err
		}
		for id, operation := range workspace.Ops {
			if !verifiedOperationCounter(id, state.OpCounter) || !verifiedOperationCounter(operation.OpID, state.OpCounter) {
				return nil, postgresRefuse("state_shape_refused")
			}
		}
	}
	for _, fork := range state.Forks {
		if fork == nil {
			return nil, postgresRefuse("state_shape_refused")
		}
	}
	return &state, nil
}
