package relayfile

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

// TestWriteFilePopulatesContentHash verifies that the public WriteFile API
// stores the content hash on the File struct for both create and update paths.
func TestWriteFilePopulatesContentHash(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)

	const path = "/external/Engineering/Hash.md"
	const content = "# create"

	createRes, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_write_hash",
		Path:          path,
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       content,
		CorrelationID: "corr_write_hash_create",
	})
	if err != nil {
		t.Fatalf("create write failed: %v", err)
	}

	wantCreate := contentHashForEncodedContent(content, "")
	store.mu.Lock()
	file := store.workspaces["ws_write_hash"].Files[path]
	store.mu.Unlock()
	if file.ContentHash != wantCreate {
		t.Fatalf("create: expected ContentHash %q, got %q", wantCreate, file.ContentHash)
	}

	const updateContent = "# updated"
	if _, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_write_hash",
		Path:          path,
		IfMatch:       createRes.TargetRevision,
		ContentType:   "text/markdown",
		Content:       updateContent,
		CorrelationID: "corr_write_hash_update",
	}); err != nil {
		t.Fatalf("update write failed: %v", err)
	}

	wantUpdate := contentHashForEncodedContent(updateContent, "")
	store.mu.Lock()
	file = store.workspaces["ws_write_hash"].Files[path]
	store.mu.Unlock()
	if file.ContentHash != wantUpdate {
		t.Fatalf("update: expected ContentHash %q, got %q", wantUpdate, file.ContentHash)
	}
}

// TestWriteFileBase64HashesDecodedBytes verifies that for base64-encoded
// content the stored ContentHash is the hash of the *decoded* bytes, not the
// base64 string.
func TestWriteFileBase64HashesDecodedBytes(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)

	const path = "/external/blob.bin"
	rawBytes := []byte{0x00, 0x01, 0x02, 0x03, 0xff, 0xfe, 0xfd}
	encoded := base64.StdEncoding.EncodeToString(rawBytes)

	if _, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_b64_hash",
		Path:          path,
		IfMatch:       "0",
		ContentType:   "application/octet-stream",
		Content:       encoded,
		Encoding:      "base64",
		CorrelationID: "corr_b64",
	}); err != nil {
		t.Fatalf("base64 write failed: %v", err)
	}

	expectedSum := sha256.Sum256(rawBytes)
	expected := hex.EncodeToString(expectedSum[:])

	store.mu.Lock()
	file := store.workspaces["ws_b64_hash"].Files[path]
	store.mu.Unlock()
	if file.ContentHash != expected {
		t.Fatalf("expected ContentHash of decoded bytes %q, got %q", expected, file.ContentHash)
	}

	// Sanity: the naive base64-string hash must differ.
	naiveSum := sha256.Sum256([]byte(encoded))
	naive := hex.EncodeToString(naiveSum[:])
	if file.ContentHash == naive {
		t.Fatalf("ContentHash should not equal hash of raw base64 string")
	}
}

// TestBulkWritePopulatesContentHash verifies that BulkWrite populates
// ContentHash for each written file.
func TestBulkWritePopulatesContentHash(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)

	files := []BulkWriteFile{
		{Path: "/external/A.md", ContentType: "text/markdown", Content: "# a"},
		{Path: "/external/B.md", ContentType: "text/markdown", Content: "# b"},
	}

	count, results, errs := store.BulkWrite("ws_bulk_hash", files)
	if len(errs) != 0 {
		t.Fatalf("unexpected bulk write errors: %+v", errs)
	}
	if count != len(files) {
		t.Fatalf("expected %d writes, got %d", len(files), count)
	}
	if len(results) != len(files) {
		t.Fatalf("expected %d results, got %d", len(files), len(results))
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	for _, in := range files {
		file := store.workspaces["ws_bulk_hash"].Files[in.Path]
		want := contentHashForEncodedContent(in.Content, "")
		if file.ContentHash != want {
			t.Fatalf("bulk: %s expected ContentHash %q, got %q", in.Path, want, file.ContentHash)
		}
	}
}

// TestWriteForkFilePopulatesContentHash verifies that fork overlay writes
// (writeForkOverlayLocked via WriteForkFile) populate ContentHash on the
// overlay entry.
func TestWriteForkFilePopulatesContentHash(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)

	const workspaceID = "ws_fork_hash"
	const proposalID = "prop_fork_hash"
	const path = "/external/Forked.md"
	const content = "# fork content"

	handle, err := store.CreateFork(workspaceID, proposalID, 0)
	if err != nil {
		t.Fatalf("CreateFork failed: %v", err)
	}

	if _, err := store.WriteForkFile(WriteRequest{
		WorkspaceID:   workspaceID,
		Path:          path,
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       content,
		CorrelationID: "corr_fork_hash",
	}, handle.ForkID); err != nil {
		t.Fatalf("WriteForkFile failed: %v", err)
	}

	want := contentHashForEncodedContent(content, "")
	store.mu.Lock()
	overlay := store.forks[handle.ForkID].Overlay[path]
	store.mu.Unlock()
	if overlay.File == nil {
		t.Fatalf("expected overlay file to be populated")
	}
	if overlay.File.ContentHash != want {
		t.Fatalf("fork overlay: expected ContentHash %q, got %q", want, overlay.File.ContentHash)
	}

	got, err := store.ReadForkFile(workspaceID, handle.ForkID, path)
	if err != nil {
		t.Fatalf("ReadForkFile failed: %v", err)
	}
	if got.ContentHash != want {
		t.Fatalf("ReadForkFile: expected ContentHash %q, got %q", want, got.ContentHash)
	}
}

// TestProviderUpsertPopulatesContentHash drives the applyProviderUpsertLocked
// path via IngestEnvelope and asserts ContentHash is populated on the
// projected file.
func TestProviderUpsertPopulatesContentHash(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)

	const path = "/external/Provider/Hash.md"
	const content = "# from provider"

	receivedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    "env_provider_hash_1",
		WorkspaceID:   "ws_provider_hash",
		Provider:      "external",
		DeliveryID:    "delivery_provider_hash_1",
		ReceivedAt:    receivedAt,
		CorrelationID: "corr_provider_hash",
		Payload: map[string]any{
			"event_type":       "file.created",
			"providerObjectId": "obj_provider_hash",
			"path":             path,
			"content":          content,
		},
	}); err != nil {
		t.Fatalf("ingest failed: %v", err)
	}

	want := contentHashForEncodedContent(content, "")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		file, err := store.ReadFile("ws_provider_hash", path)
		if err == nil && file.Content == content {
			if file.ContentHash != want {
				t.Fatalf("provider upsert: expected ContentHash %q, got %q", want, file.ContentHash)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("provider upsert did not materialize file with expected hash")
}

// TestContentHashSurvivesSaveLoadCycle writes a file via WriteFile, marshals
// the in-memory persistedState through Save/Load (round-trip via JSON), and
// asserts the ContentHash is preserved on reload.
func TestContentHashSurvivesSaveLoadCycle(t *testing.T) {
	backend := &memoryStateBackend{}
	store := NewStoreWithOptions(StoreOptions{StateBackend: backend, DisableWorkers: true})

	const path = "/external/Persisted.md"
	const content = "# persisted"
	if _, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_persisted",
		Path:          path,
		IfMatch:       "0",
		ContentType:   "text/markdown",
		Content:       content,
		CorrelationID: "corr_persisted",
	}); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	store.Close()

	want := contentHashForEncodedContent(content, "")

	// Sanity: the saved snapshot's JSON contains the contentHash field.
	raw, err := json.Marshal(backend.snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if !containsString(raw, `"contentHash":"`+want+`"`) {
		t.Fatalf("snapshot JSON missing expected contentHash; got: %s", string(raw))
	}

	// Re-open with the same backend to exercise loadFromDisk.
	reloaded := NewStoreWithOptions(StoreOptions{StateBackend: backend, DisableWorkers: true})
	t.Cleanup(reloaded.Close)

	file, err := reloaded.ReadFile("ws_persisted", path)
	if err != nil {
		t.Fatalf("reload read failed: %v", err)
	}
	if file.ContentHash != want {
		t.Fatalf("after save/load: expected ContentHash %q, got %q", want, file.ContentHash)
	}
}

// TestLoadFromDiskBackfillsForkOverlayContentHash simulates a persisted
// snapshot whose fork overlay entry lacks ContentHash and verifies the load
// path backfills it via ensureStoredContentHash.
func TestLoadFromDiskBackfillsForkOverlayContentHash(t *testing.T) {
	const workspaceID = "ws_fork_backfill"
	const forkID = "fork_backfill"
	const proposalID = "prop_fork_backfill"
	const path = "/external/Fork/Backfill.md"
	const content = "# fork backfill"

	overlayFile := File{
		Path:        path,
		Revision:    "rev_fork_backfill",
		ContentType: "text/markdown",
		Content:     content,
		// Intentionally no ContentHash.
	}
	backend := &memoryStateBackend{
		loaded: true,
		snapshot: persistedState{
			Workspaces: map[string]*workspaceState{
				workspaceID: {Files: map[string]File{}},
			},
			Forks: map[string]*forkState{
				forkID: {
					ForkID:      forkID,
					WorkspaceID: workspaceID,
					ProposalID:  proposalID,
					ExpiresAt:   time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339Nano),
					Overlay: map[string]ForkOverlayEntry{
						path: {Type: "file", File: &overlayFile, Revision: "rev_fork_backfill"},
					},
				},
			},
		},
	}

	store := NewStoreWithOptions(StoreOptions{StateBackend: backend, DisableWorkers: true})
	t.Cleanup(store.Close)

	want := contentHashForEncodedContent(content, "")
	store.mu.Lock()
	got := store.forks[forkID].Overlay[path]
	store.mu.Unlock()
	if got.File == nil {
		t.Fatalf("expected overlay file to be populated after load")
	}
	if got.File.ContentHash != want {
		t.Fatalf("fork overlay backfill: expected ContentHash %q, got %q", want, got.File.ContentHash)
	}
}

// TestStoredContentHashForFilePrefersCached confirms that
// storedContentHashForFile returns the cached value when present, and falls
// back to recomputing when empty.
func TestStoredContentHashForFilePrefersCached(t *testing.T) {
	// Cached path: return the stored hash even if it doesn't match the
	// content. This proves the function trusts the cache and does not
	// recompute.
	cached := File{
		Path:        "/whatever",
		Content:     "real content",
		ContentHash: "sentinel-cached-hash",
	}
	if got := storedContentHashForFile(cached); got != "sentinel-cached-hash" {
		t.Fatalf("expected cached hash sentinel, got %q", got)
	}

	// Missing/blank ContentHash: fall back to recomputing from the content.
	empty := File{Path: "/whatever", Content: "real content"}
	want := contentHashForEncodedContent("real content", "")
	if got := storedContentHashForFile(empty); got != want {
		t.Fatalf("expected recomputed hash %q, got %q", want, got)
	}

	// Whitespace-only ContentHash should also trigger fallback.
	blanky := File{Path: "/whatever", Content: "real content", ContentHash: "   "}
	if got := storedContentHashForFile(blanky); got != want {
		t.Fatalf("expected recomputed hash for blank ContentHash %q, got %q", want, got)
	}
}

func containsString(haystack []byte, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	target := []byte(needle)
	if len(target) > len(haystack) {
		return false
	}
	for i := 0; i+len(target) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(target); j++ {
			if haystack[i+j] != target[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
