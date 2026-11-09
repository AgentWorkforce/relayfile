package relayfile

import (
	"encoding/json"
	"testing"
)

// Derive the positive fixture from the real Store serializer, rather than
// constructing a JSON shape that merely mirrors the admission implementation.
func serializedEmptyStore(t *testing.T) []byte {
	t.Helper()
	backend := NewInMemoryStateBackend()
	store := NewStoreWithOptions(StoreOptions{StateBackend: backend, DisableWorkers: true})
	defer store.Close()
	store.mu.Lock()
	err := store.saveLocked()
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(backend.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestVerifiedSnapshotRequiresSerializerFields(t *testing.T) {
	payload := serializedEmptyStore(t)
	state, err := decodeVerifiedPostgresSnapshot(payload)
	if err != nil || state == nil || state.Workspaces == nil || len(state.Workspaces) != 0 {
		t.Fatalf("actual serialized empty store refused: %v", err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	for _, optional := range []string{"forks", "checkpointSeals", "checkpointGenerations", "checkpointConsumerKeys"} {
		if _, present := fields[optional]; present {
			t.Fatalf("empty serializer unexpectedly emitted optional %s", optional)
		}
	}
	for name := range fields {
		for _, mutation := range []string{"missing", "null", "array", "string"} {
			t.Run(name+"/"+mutation, func(t *testing.T) {
				var damaged map[string]json.RawMessage
				if err := json.Unmarshal(payload, &damaged); err != nil {
					t.Fatal(err)
				}
				switch mutation {
				case "missing":
					delete(damaged, name)
				case "null":
					damaged[name] = json.RawMessage("null")
				case "array":
					damaged[name] = json.RawMessage("[]")
				case "string":
					damaged[name] = json.RawMessage(`"planted-content"`)
				}
				data, err := json.Marshal(damaged)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = decodeVerifiedPostgresSnapshot(data); err == nil || err.Error() != "postgres_state_shape_refused" {
					t.Fatalf("damaged required field accepted or leaked: %v", err)
				}
			})
		}
	}
	for _, optional := range []string{"forks", "checkpointSeals", "checkpointGenerations", "checkpointConsumerKeys"} {
		fields[optional] = json.RawMessage(`{}`)
	}
	withOptional, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeVerifiedPostgresSnapshot(withOptional); err != nil {
		t.Fatalf("valid optional fields refused: %v", err)
	}
	for _, field := range []string{"workspaces", "forks"} {
		fields[field] = json.RawMessage(`{"owned-fixture":null}`)
		damaged, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = decodeVerifiedPostgresSnapshot(damaged); err == nil {
			t.Fatalf("nil %s entry accepted", field)
		}
		fields[field] = json.RawMessage(`{}`)
	}
}

func TestVerifiedSnapshotExistingNullAndPartialCannotStartOrOverwrite(t *testing.T) {
	for _, payload := range []string{"null", "{}", `{"workspaces":{}}`} {
		t.Run(payload, func(t *testing.T) {
			fixture := &contractFixture{snapshot: &payload}
			backend := &PostgresStateBackend{db: contractFixtureDB(t, fixture), tableName: postgresStateTableName, stateKey: "default", qualified: true}
			backend.initOnce.Do(func() {})
			store, err := NewStoreWithOptionsChecked(StoreOptions{StateBackend: backend})
			if store != nil {
				store.Close()
			}
			if store != nil || err == nil || err.Error() != "persistent_state_load_failed" || fixture.execs != 0 || *fixture.snapshot != payload {
				t.Fatalf("existing damaged row admitted or written: %v", err)
			}
			// The legacy backend keeps its old decoding behavior deliberately.
			backend.qualified = false
			if state, err := backend.Load(); err != nil || state == nil {
				t.Fatalf("legacy decoding changed: %v", err)
			}
		})
	}
}

func TestVerifiedSnapshotAbsentRowIsFreshStore(t *testing.T) {
	fixture := &contractFixture{}
	backend := &PostgresStateBackend{db: contractFixtureDB(t, fixture), tableName: postgresStateTableName, stateKey: "default", qualified: true}
	backend.initOnce.Do(func() {})
	store, err := NewStoreWithOptionsChecked(StoreOptions{StateBackend: backend, DisableWorkers: true})
	if err != nil || store == nil {
		t.Fatalf("fresh database refused: %v", err)
	}
	store.Close()
	if fixture.execs != 0 || fixture.snapshot != nil {
		t.Fatal("startup created or replaced a state row")
	}
}
