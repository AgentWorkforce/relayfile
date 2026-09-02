package mountstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func readBack(t *testing.T, path string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return document
}

// TestMergeKeepsForeignKeys is the property the whole package exists for: a
// writer publishing its own schema must not delete the other writer's keys.
func TestMergeKeepsForeignKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".relay", "state.json")

	if err := Merge(path, []string{"providers"}, map[string]any{"providers": []string{"github"}}); err != nil {
		t.Fatalf("first merge: %v", err)
	}
	if err := Merge(path, []string{"files", "status"}, map[string]any{
		"files":  map[string]any{"a.md": 1},
		"status": "ready",
	}); err != nil {
		t.Fatalf("second merge: %v", err)
	}

	document := readBack(t, path)
	for _, key := range []string{"providers", "files", "status"} {
		if _, ok := document[key]; !ok {
			t.Errorf("key %q missing after the other writer published", key)
		}
	}
}

// TestMergeClearsOwnedKeys pins the other half: a field the writer has cleared
// must not survive from the previous document.
func TestMergeClearsOwnedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".relay", "state.json")
	owned := []string{"status", "stallReason"}

	if err := Merge(path, owned, map[string]any{"status": "stalled", "stallReason": "no reconcile"}); err != nil {
		t.Fatalf("first merge: %v", err)
	}
	// stallReason is now empty and omitted from the snapshot entirely.
	if err := Merge(path, owned, map[string]any{"status": "ready"}); err != nil {
		t.Fatalf("second merge: %v", err)
	}

	document := readBack(t, path)
	if _, ok := document["stallReason"]; ok {
		t.Errorf("cleared stallReason was resurrected: %v", document["stallReason"])
	}
	if document["status"] != "ready" {
		t.Errorf("status = %v, want ready", document["status"])
	}
}

// TestMergePreservesUnknownKeys covers a writer from a newer build: keys this
// build has never heard of must round-trip untouched.
func TestMergePreservesUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".relay", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"somethingNew":{"nested":[1,2,3]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Merge(path, []string{"status"}, map[string]any{"status": "ready"}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	document := readBack(t, path)
	nested, ok := document["somethingNew"].(map[string]any)
	if !ok {
		t.Fatalf("unknown key was dropped: %v", document)
	}
	if len(nested["nested"].([]any)) != 3 {
		t.Errorf("unknown key was rewritten: %v", nested)
	}
}

// TestIncrementSurvivesConcurrentMerges is the lost-update case from #412: a
// counter increment must not be overwritten by a snapshot that read the
// counter before the increment landed. Both now happen under one lock.
func TestIncrementSurvivesConcurrentMerges(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".relay", "state.json")
	const increments = 200

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < increments; i++ {
			if err := Increment(path, "failedWritebacks", 1); err != nil {
				t.Errorf("increment: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < increments; i++ {
			// A republishing writer that carries the counter forward, exactly
			// as writeMirrorStateFile does.
			err := MergeFunc(path, []string{"status", "failedWritebacks"}, func(previous Document) (any, error) {
				return map[string]any{
					"status":           "ready",
					"failedWritebacks": previous.Uint64("failedWritebacks"),
				}, nil
			})
			if err != nil {
				t.Errorf("merge: %v", err)
				return
			}
		}
	}()
	wg.Wait()

	document := readBack(t, path)
	got, ok := document["failedWritebacks"].(float64)
	if !ok {
		t.Fatalf("failedWritebacks missing or not numeric: %v", document["failedWritebacks"])
	}
	if int(got) != increments {
		t.Errorf("failedWritebacks = %d after %d increments; increments were lost", int(got), increments)
	}
}

func TestDocumentUint64(t *testing.T) {
	document := Document{
		"int":      json.RawMessage(`7`),
		"float":    json.RawMessage(`7.0`),
		"string":   json.RawMessage(`"7"`),
		"null":     json.RawMessage(`null`),
		"object":   json.RawMessage(`{"a":1}`),
		"negative": json.RawMessage(`-3`),
	}
	for _, key := range []string{"int", "float", "string"} {
		if got := document.Uint64(key); got != 7 {
			t.Errorf("Uint64(%q) = %d, want 7", key, got)
		}
	}
	for _, key := range []string{"null", "object", "negative", "absent"} {
		if got := document.Uint64(key); got != 0 {
			t.Errorf("Uint64(%q) = %d, want 0", key, got)
		}
	}
}

// TestMergeRefusesToWriteOverAnUnreadableDocument is the failure mode cubic
// flagged on PR #457: a merge writes back everything it read, so treating an
// unreadable file as empty would delete the other writer's keys on a transient
// I/O or permissions error -- the exact clobber this package exists to prevent.
func TestMergeRefusesToWriteOverAnUnreadableDocument(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this test relies on")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	if err := Merge(path, []string{"providers"}, map[string]any{"providers": []string{"github"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	err := Merge(path, []string{"status"}, map[string]any{"status": "ready"})
	if err == nil {
		t.Fatal("merge over an unreadable document must fail rather than clobber it")
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod back: %v", err)
	}
	document := readBack(t, path)
	if _, ok := document["providers"]; !ok {
		t.Errorf("the refused merge still destroyed the other writer's keys: %v", document)
	}
	if _, ok := document["status"]; ok {
		t.Errorf("the refused merge wrote anyway: %v", document)
	}
}

// TestMergeStartsFreshWhenTheFileIsMissing pins the other side: a missing file
// is not an error, because the first write has to start somewhere.
func TestMergeStartsFreshWhenTheFileIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	if err := Merge(path, []string{"status"}, map[string]any{"status": "ready"}); err != nil {
		t.Fatalf("merge onto a missing file must succeed, got %v", err)
	}
	if readBack(t, path)["status"] != "ready" {
		t.Error("first merge did not publish")
	}
}

// TestMergeSelfHealsAnUnparseableDocument pins the deliberate exception: a file
// that reads fine but does not parse is overwritten rather than failing
// forever, since atomic renames mean a torn document should be unreachable and
// refusing to rewrite one would strand the mount.
func TestMergeSelfHealsAnUnparseableDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Merge(path, []string{"status"}, map[string]any{"status": "ready"}); err != nil {
		t.Fatalf("merge over a corrupt document must self-heal, got %v", err)
	}
	if readBack(t, path)["status"] != "ready" {
		t.Error("merge did not replace the corrupt document")
	}
}
