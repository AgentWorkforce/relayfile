package relayfile

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckpointSealIsOneUseAndIdentityBound(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	defer store.Close()
	seedCheckpointFile(t, store, "ws_seal", "/sessions/transcript.jsonl", "first\n")
	digest := checkpointDigestForStore(t, store, "ws_seal", "/sessions")
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	seal, err := store.IssueCheckpointSeal("ws_seal", CheckpointSealRequest{
		Root:           "/sessions",
		SessionID:      "thread-123",
		Generation:     7,
		ExpectedDigest: digest,
		TTLSeconds:     30,
	}, now)
	if err != nil {
		t.Fatalf("issue seal: %v", err)
	}
	if seal.SealToken == "" || seal.Digest != digest || seal.EventCursor == "" {
		t.Fatalf("incomplete server seal: %+v", seal)
	}
	for name, req := range map[string]CheckpointSealConsumeRequest{
		"root":       {SealToken: seal.SealToken, Root: "/other", SessionID: "thread-123", Generation: 7, ConsumerIdempotencyKey: "acquire-mismatch-root"},
		"session":    {SealToken: seal.SealToken, Root: "/sessions", SessionID: "thread-456", Generation: 7, ConsumerIdempotencyKey: "acquire-mismatch-session"},
		"generation": {SealToken: seal.SealToken, Root: "/sessions", SessionID: "thread-123", Generation: 8, ConsumerIdempotencyKey: "acquire-mismatch-generation"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.ConsumeCheckpointSeal("ws_seal", req, now.Add(time.Second)); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("identity mismatch error = %v, want invalid input", err)
			}
		})
	}
	consume := CheckpointSealConsumeRequest{SealToken: seal.SealToken, Root: "/sessions", SessionID: "thread-123", Generation: 7, ConsumerIdempotencyKey: "acquire-one"}
	if _, err := store.ConsumeCheckpointSeal("ws_seal", consume, now.Add(time.Second)); err != nil {
		t.Fatalf("consume seal: %v", err)
	}
	if replay, err := store.ConsumeCheckpointSeal("ws_seal", consume, now.Add(2*time.Second)); err != nil || replay.ConsumedAt == "" {
		t.Fatalf("exact idempotent replay = %+v, err=%v", replay, err)
	}
	differentConsumer := consume
	differentConsumer.ConsumerIdempotencyKey = "acquire-two"
	if _, err := store.ConsumeCheckpointSeal("ws_seal", differentConsumer, now.Add(2*time.Second)); !errors.Is(err, ErrCheckpointReplay) {
		t.Fatalf("different consumer replay error = %v, want checkpoint replay", err)
	}
	if _, err := store.IssueCheckpointSeal("ws_seal", CheckpointSealRequest{
		Root: "/sessions", SessionID: "thread-123", Generation: 7, ExpectedDigest: digest,
	}, now.Add(3*time.Second)); !errors.Is(err, ErrCheckpointGenerationStale) {
		t.Fatalf("stale generation error = %v", err)
	}
}

func TestCheckpointSealRejectsDivergenceExpiryAndRemoteMutation(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	defer store.Close()
	seedCheckpointFile(t, store, "ws_stale", "/sessions/transcript.jsonl", "first\n")
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if _, err := store.IssueCheckpointSeal("ws_stale", CheckpointSealRequest{
		Root: "/sessions", SessionID: "thread-stale", Generation: 1,
		ExpectedDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, now); !errors.Is(err, ErrCheckpointDiverged) {
		t.Fatalf("mismatched caller digest error = %v", err)
	}
	digest := checkpointDigestForStore(t, store, "ws_stale", "/sessions")
	expiring, err := store.IssueCheckpointSeal("ws_stale", CheckpointSealRequest{
		Root: "/sessions", SessionID: "thread-expire", Generation: 1, ExpectedDigest: digest, TTLSeconds: 1,
	}, now)
	if err != nil {
		t.Fatalf("issue expiring: %v", err)
	}
	if _, err := store.ConsumeCheckpointSeal("ws_stale", CheckpointSealConsumeRequest{
		SealToken: expiring.SealToken, Root: "/sessions", SessionID: "thread-expire", Generation: 1, ConsumerIdempotencyKey: "acquire-expire",
	}, now.Add(time.Second)); !errors.Is(err, ErrCheckpointExpired) {
		t.Fatalf("expiry error = %v", err)
	}

	stale, err := store.IssueCheckpointSeal("ws_stale", CheckpointSealRequest{
		Root: "/sessions", SessionID: "thread-stale", Generation: 2, ExpectedDigest: digest,
	}, now)
	if err != nil {
		t.Fatalf("issue stale candidate: %v", err)
	}
	seedCheckpointFile(t, store, "ws_stale", "/sessions/after.json", "changed")
	if _, err := store.ConsumeCheckpointSeal("ws_stale", CheckpointSealConsumeRequest{
		SealToken: stale.SealToken, Root: "/sessions", SessionID: "thread-stale", Generation: 2, ConsumerIdempotencyKey: "acquire-stale",
	}, now.Add(time.Second)); !errors.Is(err, ErrCheckpointStale) {
		t.Fatalf("remote mutation error = %v, want stale", err)
	}
}

func TestCheckpointSealSurvivesDaemonRestart(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "relayfile-state.json")
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	first := NewStoreWithOptions(StoreOptions{StateFile: stateFile, DisableWorkers: true})
	seedCheckpointFile(t, first, "ws_restart", "/sessions/transcript.jsonl", "durable\n")
	digest := checkpointDigestForStore(t, first, "ws_restart", "/sessions")
	seal, err := first.IssueCheckpointSeal("ws_restart", CheckpointSealRequest{
		Root: "/sessions", SessionID: "thread-restart", Generation: 3, ExpectedDigest: digest,
	}, now)
	if err != nil {
		t.Fatalf("issue before restart: %v", err)
	}
	first.Close()

	second := NewStoreWithOptions(StoreOptions{StateFile: stateFile, DisableWorkers: true})
	defer second.Close()
	if _, err := second.ConsumeCheckpointSeal("ws_restart", CheckpointSealConsumeRequest{
		SealToken: seal.SealToken, Root: "/sessions", SessionID: "thread-restart", Generation: 3, ConsumerIdempotencyKey: "acquire-restart",
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("consume after restart: %v", err)
	}
	if _, err := second.IssueCheckpointSeal("ws_restart", CheckpointSealRequest{
		Root: "/sessions", SessionID: "thread-restart", Generation: 3, ExpectedDigest: digest,
	}, now.Add(2*time.Second)); !errors.Is(err, ErrCheckpointGenerationStale) {
		t.Fatalf("generation replay after restart error = %v", err)
	}
}

func TestCheckpointConsumeResponseLossIsIdempotentAcrossExpiryAndRestart(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "relayfile-state.json")
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	first := NewStoreWithOptions(StoreOptions{StateFile: stateFile, DisableWorkers: true})
	seedCheckpointFile(t, first, "ws_idempotent", "/sessions/transcript.jsonl", "durable\n")
	digest := checkpointDigestForStore(t, first, "ws_idempotent", "/sessions")
	seal, err := first.IssueCheckpointSeal("ws_idempotent", CheckpointSealRequest{
		Root: "/sessions", SessionID: "thread-idempotent", Generation: 1,
		ExpectedDigest: digest, TTLSeconds: 1,
	}, now)
	if err != nil {
		t.Fatalf("issue seal: %v", err)
	}
	consume := CheckpointSealConsumeRequest{
		SealToken: seal.SealToken, Root: "/sessions", SessionID: "thread-idempotent",
		Generation: 1, ConsumerIdempotencyKey: "cloud-acquire-attempt-123",
	}
	consumed, err := first.ConsumeCheckpointSeal("ws_idempotent", consume, now.Add(500*time.Millisecond))
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	first.Close()

	second := NewStoreWithOptions(StoreOptions{StateFile: stateFile, DisableWorkers: true})
	defer second.Close()
	replayed, err := second.ConsumeCheckpointSeal("ws_idempotent", consume, now.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("response-loss replay after seal expiry/restart: %v", err)
	}
	if replayed.ConsumedAt != consumed.ConsumedAt || replayed.SealID != consumed.SealID {
		t.Fatalf("replayed result changed: first=%+v replay=%+v", consumed, replayed)
	}

	changedIdentity := consume
	changedIdentity.Root = "/other"
	if _, err := second.ConsumeCheckpointSeal("ws_idempotent", changedIdentity, now.Add(11*time.Minute)); !errors.Is(err, ErrCheckpointConsumerConflict) {
		t.Fatalf("same consumer key with changed identity error = %v", err)
	}

	otherSeal, err := second.IssueCheckpointSeal("ws_idempotent", CheckpointSealRequest{
		Root: "/sessions", SessionID: "thread-other", Generation: 1, ExpectedDigest: digest,
	}, now.Add(12*time.Minute))
	if err != nil {
		t.Fatalf("issue other seal: %v", err)
	}
	if _, err := second.ConsumeCheckpointSeal("ws_idempotent", CheckpointSealConsumeRequest{
		SealToken: otherSeal.SealToken, Root: "/sessions", SessionID: "thread-other",
		Generation: 1, ConsumerIdempotencyKey: consume.ConsumerIdempotencyKey,
	}, now.Add(13*time.Minute)); !errors.Is(err, ErrCheckpointConsumerConflict) {
		t.Fatalf("consumer key reused for another seal error = %v", err)
	}
}

func TestCheckpointConsumeReplayRetentionIsBounded(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	defer store.Close()
	seedCheckpointFile(t, store, "ws_retention", "/sessions/transcript.jsonl", "durable\n")
	digest := checkpointDigestForStore(t, store, "ws_retention", "/sessions")
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	seal, err := store.IssueCheckpointSeal("ws_retention", CheckpointSealRequest{
		Root: "/sessions", SessionID: "thread-retention", Generation: 1, ExpectedDigest: digest,
	}, now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	consume := CheckpointSealConsumeRequest{
		SealToken: seal.SealToken, Root: "/sessions", SessionID: "thread-retention",
		Generation: 1, ConsumerIdempotencyKey: "cloud-retention-attempt",
	}
	if _, err := store.ConsumeCheckpointSeal("ws_retention", consume, now.Add(time.Second)); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if _, err := store.ConsumeCheckpointSeal("ws_retention", consume, now.Add(time.Second+CheckpointConsumeReplayRetention)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("post-retention replay error = %v, want not found after GC", err)
	}
}

func TestCheckpointDigestRejectsMalformedRootAndDuplicatePath(t *testing.T) {
	for _, root := range []string{"", "sessions", "/sessions/../other", "/sessions//nested"} {
		if _, err := ComputeCheckpointDigest(root, nil); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("root %q error = %v, want invalid input", root, err)
		}
	}
	entries := []CheckpointDigestEntry{
		{Path: "/sessions/a", Revision: "rev_1", ContentHash: "a"},
		{Path: "/sessions/a", Revision: "rev_2", ContentHash: "b"},
	}
	if _, err := ComputeCheckpointDigest("/sessions", entries); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate path error = %v", err)
	}
}

func TestCheckpointSealCanonicalizesSessionBeforeGenerationCheck(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	defer store.Close()
	seedCheckpointFile(t, store, "ws_canonical", "/sessions/transcript.jsonl", "first\n")
	digest := checkpointDigestForStore(t, store, "ws_canonical", "/sessions")
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if _, err := store.IssueCheckpointSeal("ws_canonical", CheckpointSealRequest{
		Root: "/sessions", SessionID: " thread-123 ", Generation: 1, ExpectedDigest: digest,
	}, now); err != nil {
		t.Fatalf("issue canonical session: %v", err)
	}
	if _, err := store.IssueCheckpointSeal("ws_canonical", CheckpointSealRequest{
		Root: "/sessions", SessionID: "thread-123", Generation: 1, ExpectedDigest: digest,
	}, now.Add(time.Second)); !errors.Is(err, ErrCheckpointGenerationStale) {
		t.Fatalf("whitespace variant replay error = %v, want stale generation", err)
	}
}

func TestCheckpointSealRejectsMalformedDigestAndTTL(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	defer store.Close()
	for name, req := range map[string]CheckpointSealRequest{
		"non-hex digest": {
			Root: "/sessions", SessionID: "thread-123", Generation: 1,
			ExpectedDigest: "sha256:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		},
		"negative ttl": {
			Root: "/sessions", SessionID: "thread-123", Generation: 1,
			ExpectedDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", TTLSeconds: -1,
		},
		"oversize ttl": {
			Root: "/sessions", SessionID: "thread-123", Generation: 1,
			ExpectedDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", TTLSeconds: 301,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.IssueCheckpointSeal("ws_input", req, time.Now()); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want invalid input", err)
			}
		})
	}
}

func seedCheckpointFile(t *testing.T, store *Store, workspaceID, path, content string) {
	t.Helper()
	if _, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: path, IfMatch: "*", Content: content}); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
}

func checkpointDigestForStore(t *testing.T, store *Store, workspaceID, root string) string {
	t.Helper()
	store.mu.RLock()
	defer store.mu.RUnlock()
	digest, _, _, err := store.checkpointStateLocked(workspaceID, root)
	if err != nil {
		t.Fatalf("checkpoint state: %v", err)
	}
	return digest
}
