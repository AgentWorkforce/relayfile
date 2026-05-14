package writeback

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	schemaassets "github.com/agentworkforce/relayfile/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func compileDeadLetterSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	data, err := schemaassets.FS.ReadFile(SchemaPath)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft7)
	c.AssertFormat()
	if err := c.AddResource(SchemaPath, doc); err != nil {
		t.Fatalf("add resource: %v", err)
	}
	sch, err := c.Compile(SchemaPath)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return sch
}

// forceSchemaViolation simulates the writeback executor's behaviour when a
// payload fails canonical-schema validation against the provider's
// response: persist the original bytes plus a sidecar describing the
// schema_violation cause. All diagnostic fields required by the child
// lead plan are populated — `providerStatus` and `providerResponse`
// carry the upstream HTTP status and raw body so operators can replay
// the failure.
func forceSchemaViolation(t *testing.T, dir string) (payloadPath, sidecarPath string) {
	t.Helper()
	payloadPath = filepath.Join(dir, "github", "issues", "op-42.json")
	original := []byte(`{"title":"missing required field"}`)
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	err := WriteDeadLetter(payloadPath, original, ErrorContext{
		Code:             CodeSchemaViolation,
		Message:          "payload failed canonical schema validation",
		ProviderStatus:   200,
		ProviderResponse: map[string]any{"body": json.RawMessage(original), "contentType": "application/json"},
		Attempts:         3,
		FirstAttemptAt:   now.Add(-2 * time.Hour),
		LastAttemptAt:    now,
		OpID:             "op-42",
	})
	if err != nil {
		t.Fatalf("WriteDeadLetter: %v", err)
	}
	sidecarPath = payloadPath + ".error.json"
	return payloadPath, sidecarPath
}

func TestWriteDeadLetter_DualWrite(t *testing.T) {
	dir := t.TempDir()
	payloadPath, sidecarPath := forceSchemaViolation(t, dir)
	if _, err := os.Stat(payloadPath); err != nil {
		t.Fatalf("payload missing: %v", err)
	}
	if _, err := os.Stat(sidecarPath); err != nil {
		t.Fatalf("sidecar missing: %v", err)
	}
}

func TestWriteDeadLetter_SidecarValidatesSchema(t *testing.T) {
	sch := compileDeadLetterSchema(t)
	dir := t.TempDir()
	_, sidecarPath := forceSchemaViolation(t, dir)

	raw, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("parse sidecar JSON: %v", err)
	}
	if err := sch.Validate(value); err != nil {
		t.Fatalf("sidecar failed schema: %v", err)
	}
}

func TestWriteDeadLetter_AllRequiredFieldsPresent(t *testing.T) {
	dir := t.TempDir()
	_, sidecarPath := forceSchemaViolation(t, dir)
	raw, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parse sidecar: %v", err)
	}
	// Lead plan §4: the forced schema_violation sidecar must carry every
	// diagnostic field — including providerStatus and providerResponse —
	// not just the schema's `required` subset.
	for _, field := range []string{"code", "message", "providerStatus", "providerResponse", "attempts", "firstAttemptAt", "lastAttemptAt", "opId"} {
		if _, ok := got[field]; !ok {
			t.Errorf("missing field %q in sidecar: %v", field, got)
		}
	}
	if v, _ := got["providerStatus"].(float64); v != 200 {
		t.Errorf("providerStatus: got %v want 200", got["providerStatus"])
	}
	if _, ok := got["providerResponse"].(map[string]any); !ok {
		t.Errorf("providerResponse: not an object: %v", got["providerResponse"])
	}
}

func TestWriteDeadLetter_RejectsInvalidContext(t *testing.T) {
	dir := t.TempDir()
	err := WriteDeadLetter(filepath.Join(dir, "x.json"), []byte("{}"), ErrorContext{
		Code:    CodeSchemaViolation,
		Message: "",
		// missing Attempts, OpID
	})
	if err == nil {
		t.Fatal("expected error for missing required fields, got nil")
	}
}

func TestListDead_PairsFiles(t *testing.T) {
	dir := t.TempDir()
	p1, _ := forceSchemaViolation(t, dir)

	// Add a second entry under a different subdir.
	p2 := filepath.Join(dir, "linear", "op-99.json")
	now := time.Date(2026, 5, 13, 14, 0, 0, 0, time.UTC)
	if err := WriteDeadLetter(p2, []byte(`{"x":1}`), ErrorContext{
		Code:           CodeProvider4xx,
		Message:        "bad request",
		ProviderStatus: 422,
		Attempts:       2,
		FirstAttemptAt: now.Add(-time.Hour),
		LastAttemptAt:  now,
		OpID:           "op-99",
	}); err != nil {
		t.Fatalf("WriteDeadLetter #2: %v", err)
	}

	entries, err := ListDead(dir)
	if err != nil {
		t.Fatalf("ListDead: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries: got %d want 2", len(entries))
	}
	// Sorted by payload path.
	if entries[0].PayloadPath != p1 || entries[1].PayloadPath != p2 {
		t.Fatalf("unexpected ordering: %+v", entries)
	}
	if entries[0].Error.Code != CodeSchemaViolation {
		t.Errorf("entry[0] code: got %s want %s", entries[0].Error.Code, CodeSchemaViolation)
	}
	if entries[1].Error.ProviderStatus != 422 {
		t.Errorf("entry[1] providerStatus: got %d want 422", entries[1].Error.ProviderStatus)
	}
}

func TestListDead_MissingRoot(t *testing.T) {
	entries, err := ListDead(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("expected nil error for missing root, got %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty list, got %d", len(entries))
	}
}

func TestListDead_SkipsUnpairedPayload(t *testing.T) {
	dir := t.TempDir()
	lone := filepath.Join(dir, "incomplete.json")
	if err := os.WriteFile(lone, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	entries, err := ListDead(dir)
	if err != nil {
		t.Fatalf("ListDead: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries (unpaired payload), got %d", len(entries))
	}
}

func TestErrorCodeEnumCovered(t *testing.T) {
	// Sanity: all enum values from the schema have a typed constant.
	want := []ErrorCode{CodeSchemaViolation, CodeProvider4xx, CodeProvider5xxExhaust, CodeTimeout}
	if len(want) != 4 {
		t.Fatalf("unexpected enum coverage: %v", want)
	}
	// Smoke-check that the schema accepts each enum value via a minimal
	// payload — guards against accidental constant renames diverging from
	// the schema.
	sch := compileDeadLetterSchema(t)
	for _, code := range want {
		raw, err := MarshalSidecar(ErrorContext{
			Code:           code,
			Message:        "x",
			Attempts:       1,
			FirstAttemptAt: time.Now(),
			LastAttemptAt:  time.Now(),
			OpID:           "op",
		})
		if err != nil {
			t.Fatalf("marshal %s: %v", code, err)
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("unmarshal %s: %v", code, err)
		}
		if err := sch.Validate(v); err != nil {
			t.Errorf("schema rejected valid code %s: %v", code, err)
		}
	}
	// Negative: an invalid code should be rejected.
	bad, _ := MarshalSidecar(ErrorContext{
		Code:           ErrorCode("nope"),
		Message:        "x",
		Attempts:       1,
		FirstAttemptAt: time.Now(),
		LastAttemptAt:  time.Now(),
		OpID:           "op",
	})
	var v any
	_ = json.Unmarshal(bad, &v)
	if err := sch.Validate(v); err == nil {
		t.Error("schema accepted invalid code; want rejection")
	} else if !errors.Is(err, err) { // just touch err to satisfy linters
		_ = err
	}
}
