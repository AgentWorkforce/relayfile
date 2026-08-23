package relayfile

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

const checkpointTestConsumerPrincipal = "cloud-dashboard-observer"

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
		"root":       {SealToken: seal.SealToken, Root: "/other", SessionID: "thread-123", Generation: 7, ConsumerIdempotencyKey: "acquire-mismatch-root", ConsumerPrincipal: checkpointTestConsumerPrincipal},
		"session":    {SealToken: seal.SealToken, Root: "/sessions", SessionID: "thread-456", Generation: 7, ConsumerIdempotencyKey: "acquire-mismatch-session", ConsumerPrincipal: checkpointTestConsumerPrincipal},
		"generation": {SealToken: seal.SealToken, Root: "/sessions", SessionID: "thread-123", Generation: 8, ConsumerIdempotencyKey: "acquire-mismatch-generation", ConsumerPrincipal: checkpointTestConsumerPrincipal},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.ConsumeCheckpointSeal("ws_seal", req, now.Add(time.Second)); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("identity mismatch error = %v, want invalid input", err)
			}
		})
	}
	consume := CheckpointSealConsumeRequest{SealToken: seal.SealToken, Root: "/sessions", SessionID: "thread-123", Generation: 7, ConsumerIdempotencyKey: "acquire-one", ConsumerPrincipal: checkpointTestConsumerPrincipal}
	if _, err := store.ConsumeCheckpointSeal("ws_seal", consume, now.Add(time.Second)); err != nil {
		t.Fatalf("consume seal: %v", err)
	}
	if replay, err := store.ConsumeCheckpointSeal("ws_seal", consume, now.Add(2*time.Second)); err != nil || replay.ConsumedAt == "" {
		t.Fatalf("exact idempotent replay = %+v, err=%v", replay, err)
	}
	differentPrincipal := consume
	differentPrincipal.ConsumerPrincipal = "other-authenticated-agent"
	if _, err := store.ConsumeCheckpointSeal("ws_seal", differentPrincipal, now.Add(2*time.Second)); !errors.Is(err, ErrCheckpointConsumerConflict) {
		t.Fatalf("different authenticated principal error = %v, want consumer conflict", err)
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
		SealToken: expiring.SealToken, Root: "/sessions", SessionID: "thread-expire", Generation: 1, ConsumerIdempotencyKey: "acquire-expire", ConsumerPrincipal: checkpointTestConsumerPrincipal,
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
		SealToken: stale.SealToken, Root: "/sessions", SessionID: "thread-stale", Generation: 2, ConsumerIdempotencyKey: "acquire-stale", ConsumerPrincipal: checkpointTestConsumerPrincipal,
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
		SealToken: seal.SealToken, Root: "/sessions", SessionID: "thread-restart", Generation: 3, ConsumerIdempotencyKey: "acquire-restart", ConsumerPrincipal: checkpointTestConsumerPrincipal,
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
		Generation: 1, ConsumerIdempotencyKey: "cloud-acquire-attempt-123", ConsumerPrincipal: checkpointTestConsumerPrincipal,
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
	recovered, err := second.RecoverConsumedCheckpointSeal("ws_idempotent", CheckpointSealConsumeRecoveryRequest{
		Root: consume.Root, SessionID: consume.SessionID, Generation: consume.Generation,
		ConsumerIdempotencyKey: consume.ConsumerIdempotencyKey, ConsumerPrincipal: checkpointTestConsumerPrincipal,
	}, now.Add(25*time.Hour))
	if err != nil || recovered.SealToken != "" || recovered.SealID != consumed.SealID || recovered.ConsumedAt != consumed.ConsumedAt {
		t.Fatalf("tokenless consume recovery after lease cap = %+v err=%v", recovered, err)
	}
	if _, err := second.RecoverConsumedCheckpointSeal("ws_idempotent", CheckpointSealConsumeRecoveryRequest{
		Root: "/other", SessionID: consume.SessionID, Generation: consume.Generation,
		ConsumerIdempotencyKey: consume.ConsumerIdempotencyKey, ConsumerPrincipal: checkpointTestConsumerPrincipal,
	}, now.Add(25*time.Hour)); !errors.Is(err, ErrCheckpointConsumerConflict) {
		t.Fatalf("changed recovery identity error = %v", err)
	}
	if _, err := second.RecoverConsumedCheckpointSeal("ws_idempotent", CheckpointSealConsumeRecoveryRequest{
		Root: consume.Root, SessionID: consume.SessionID, Generation: consume.Generation,
		ConsumerIdempotencyKey: consume.ConsumerIdempotencyKey, ConsumerPrincipal: "other-authenticated-agent",
	}, now.Add(25*time.Hour)); !errors.Is(err, ErrCheckpointConsumerConflict) {
		t.Fatalf("changed recovery principal error = %v", err)
	}
	if _, err := second.RecoverConsumedCheckpointSeal("ws_idempotent", CheckpointSealConsumeRecoveryRequest{
		Root: consume.Root, SessionID: consume.SessionID, Generation: consume.Generation,
		ConsumerIdempotencyKey: "unknown-consumer", ConsumerPrincipal: checkpointTestConsumerPrincipal,
	}, now.Add(25*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown recovery error = %v", err)
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
		Generation: 1, ConsumerIdempotencyKey: consume.ConsumerIdempotencyKey, ConsumerPrincipal: checkpointTestConsumerPrincipal,
	}, now.Add(13*time.Minute)); !errors.Is(err, ErrCheckpointConsumerConflict) {
		t.Fatalf("consumer key reused for another seal error = %v", err)
	}
}

func TestCheckpointConsumedOwnershipIsRetainedUntilExplicitHandback(t *testing.T) {
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
		Generation: 1, ConsumerIdempotencyKey: "cloud-retention-attempt", ConsumerPrincipal: checkpointTestConsumerPrincipal,
	}
	if _, err := store.ConsumeCheckpointSeal("ws_retention", consume, now.Add(time.Second)); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if replayed, err := store.ConsumeCheckpointSeal("ws_retention", consume, now.Add(time.Second+CheckpointConsumeReplayRetention)); err != nil || replayed.ConsumedAt == "" {
		t.Fatalf("active ownership replay after diagnostic retention = %+v err=%v", replayed, err)
	}
}

func TestCheckpointVerifyReattestsConsumedSealWhenLatestRevisionIsDeletion(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	defer store.Close()
	keep := seedCheckpointFileResult(t, store, "ws_verify_delete", "/keep.txt", "keep\n")
	deleted := seedCheckpointFileResult(t, store, "ws_verify_delete", "/delete.txt", "delete\n")
	if _, err := store.DeleteFile(DeleteRequest{WorkspaceID: "ws_verify_delete", Path: "/delete.txt", IfMatch: deleted.TargetRevision}); err != nil {
		t.Fatalf("delete latest file: %v", err)
	}
	digest := checkpointDigestForStore(t, store, "ws_verify_delete", "/")
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	issued, err := store.IssueCheckpointSeal("ws_verify_delete", CheckpointSealRequest{
		Root: "/", SessionID: "thread-delete", Generation: 1, ExpectedDigest: digest,
	}, now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if issued.WorkspaceRevision == keep.TargetRevision {
		t.Fatalf("fixture did not create delete-only revision: workspace=%q surviving=%q", issued.WorkspaceRevision, keep.TargetRevision)
	}
	unconsumedRequest := checkpointVerifyRequest(issued)
	unconsumedRequest.ConsumedAt = now.Add(time.Second).Format(time.RFC3339Nano)
	if _, err := store.VerifyConsumedCheckpointSeal("ws_verify_delete", unconsumedRequest, now); !errors.Is(err, ErrCheckpointUnconsumed) {
		t.Fatalf("unconsumed verification error = %v", err)
	}
	consumed, err := store.ConsumeCheckpointSeal("ws_verify_delete", CheckpointSealConsumeRequest{
		SealToken: issued.SealToken, Root: "/", SessionID: issued.SessionID, Generation: issued.Generation,
		ConsumerIdempotencyKey: "cloud-delete-proof", ConsumerPrincipal: checkpointTestConsumerPrincipal,
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	verified, err := store.VerifyConsumedCheckpointSeal("ws_verify_delete", checkpointVerifyRequest(consumed), now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("verify consumed delete-latest seal: %v", err)
	}
	if verified.SealToken != "" || verified.WorkspaceRevision != consumed.WorkspaceRevision || verified.ConsumedAt != consumed.ConsumedAt {
		t.Fatalf("verification leaked token or changed identity: %+v", verified)
	}

	tampered := checkpointVerifyRequest(consumed)
	tampered.EventCursor = "evt_999"
	if _, err := store.VerifyConsumedCheckpointSeal("ws_verify_delete", tampered, now.Add(2*time.Second)); !errors.Is(err, ErrCheckpointStale) {
		t.Fatalf("tampered receipt error = %v", err)
	}
	seedCheckpointFile(t, store, "ws_verify_delete", "/after.txt", "after\n")
	if _, err := store.VerifyConsumedCheckpointSeal("ws_verify_delete", checkpointVerifyRequest(consumed), now.Add(3*time.Second)); !errors.Is(err, ErrCheckpointStale) {
		t.Fatalf("post-consume remote mutation error = %v", err)
	}
}

func TestCheckpointSealUsesCanonicalZeroRevisionAndCursorForEmptyWorkspace(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	defer store.Close()
	digest := checkpointDigestForStore(t, store, "ws_empty_checkpoint", "/")
	seal, err := store.IssueCheckpointSeal("ws_empty_checkpoint", CheckpointSealRequest{
		Root: "/", SessionID: "thread-empty", Generation: 1, ExpectedDigest: digest,
	}, time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("issue empty-workspace seal: %v", err)
	}
	if seal.WorkspaceRevision != "0" || seal.EventCursor != "0" {
		t.Fatalf("empty-workspace wire state = revision %q cursor %q, want canonical zeroes", seal.WorkspaceRevision, seal.EventCursor)
	}
}

func TestCheckpointHandbackIsConsumerBoundDurableAndGatesSourceResume(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "relayfile-state.json")
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	first := NewStoreWithOptions(StoreOptions{StateFile: stateFile, DisableWorkers: true})
	seedCheckpointFile(t, first, "ws_handback", "/transcript.jsonl", "source turn\n")
	digest := checkpointDigestForStore(t, first, "ws_handback", "/")
	issued, err := first.IssueCheckpointSeal("ws_handback", CheckpointSealRequest{
		Root: "/", SessionID: "thread-handback", Generation: 4, ExpectedDigest: digest,
	}, now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	consumerKey := "cutover-job-4"
	consumed, err := first.ConsumeCheckpointSeal("ws_handback", CheckpointSealConsumeRequest{
		SealToken: issued.SealToken, Root: "/", SessionID: issued.SessionID, Generation: issued.Generation,
		ConsumerIdempotencyKey: consumerKey, ConsumerPrincipal: checkpointTestConsumerPrincipal,
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	resumeRequest := CheckpointSealResumeRequest{
		SealToken: issued.SealToken, Root: "/", SessionID: issued.SessionID, Generation: issued.Generation,
		ResumeIdempotencyKey: "source-resume-job-4",
	}
	if _, err := first.ResumeCheckpointSeal("ws_handback", resumeRequest, now.Add(2*time.Second)); !errors.Is(err, ErrCheckpointHandbackRequired) {
		t.Fatalf("premature source resume error = %v, want handback required", err)
	}
	seedCheckpointFile(t, first, "ws_handback", "/destination.txt", "destination turn\n")
	finalDigest, _, finalCursor := checkpointStateForTest(t, first, "ws_handback", "/")
	handback := CheckpointSealHandbackRequest{
		Phase:  CheckpointHandbackPhasePrepare,
		SealID: issued.SealID, Root: "/", SessionID: issued.SessionID, Generation: issued.Generation,
		ConsumedAt: consumed.ConsumedAt, ConsumerIdempotencyKey: consumerKey,
		HandbackIdempotencyKey: "handback-job-4", ExpectedDigest: finalDigest, ConsumerPrincipal: checkpointTestConsumerPrincipal,
	}
	wrongConsumer := handback
	wrongConsumer.ConsumerIdempotencyKey = "cutover-job-other"
	if _, err := first.HandbackCheckpointSeal("ws_handback", wrongConsumer, now.Add(3*time.Second)); !errors.Is(err, ErrCheckpointConsumerConflict) {
		t.Fatalf("wrong-consumer handback error = %v", err)
	}
	wrongPrincipal := handback
	wrongPrincipal.ConsumerPrincipal = "other-authenticated-agent"
	if _, err := first.HandbackCheckpointSeal("ws_handback", wrongPrincipal, now.Add(3*time.Second)); !errors.Is(err, ErrCheckpointConsumerConflict) {
		t.Fatalf("wrong-principal handback error = %v", err)
	}
	validButDiverged := handback
	validButDiverged.ExpectedDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := first.HandbackCheckpointSeal("ws_handback", validButDiverged, now.Add(3*time.Second)); !errors.Is(err, ErrCheckpointDiverged) {
		t.Fatalf("diverged handback digest error = %v, want checkpoint diverged", err)
	}
	diverged := handback
	diverged.ExpectedDigest = "sha256:" + string(make([]byte, 64))
	if _, err := first.HandbackCheckpointSeal("ws_handback", diverged, now.Add(3*time.Second)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("malformed handback digest error = %v", err)
	}
	commitWithoutPrepare := handback
	commitWithoutPrepare.Phase = CheckpointHandbackPhaseCommit
	if _, err := first.HandbackCheckpointSeal("ws_handback", commitWithoutPrepare, now.Add(3*time.Second)); !errors.Is(err, ErrCheckpointHandbackUnprepared) {
		t.Fatalf("commit without prepare error = %v", err)
	}
	prepared, err := first.HandbackCheckpointSeal("ws_handback", handback, now.Add(4*time.Second))
	if err != nil {
		t.Fatalf("prepare handback: %v", err)
	}
	if prepared.Status != "prepared" || prepared.Digest != finalDigest || prepared.EventCursor != finalCursor || prepared.PreparedAt == "" || prepared.ReleasedAt != "" || prepared.SourceResumedAt != "" {
		t.Fatalf("handback preparation = %+v", prepared)
	}
	replayed, err := first.HandbackCheckpointSeal("ws_handback", handback, now.Add(5*time.Second))
	if err != nil || replayed != prepared {
		t.Fatalf("prepare replay = %+v err=%v, want %+v", replayed, err, prepared)
	}
	if _, err := first.ResumeCheckpointSeal("ws_handback", resumeRequest, now.Add(5*time.Second)); !errors.Is(err, ErrCheckpointHandbackRequired) {
		t.Fatalf("prepared handback released ownership early: %v", err)
	}
	// Simulate a destination crash after the durable prepare response. The exact
	// commit must remain recoverable after reopening the store.
	first.Close()

	second := NewStoreWithOptions(StoreOptions{StateFile: stateFile, DisableWorkers: true})
	defer second.Close()
	handback.Phase = CheckpointHandbackPhaseCommit
	proof, err := second.HandbackCheckpointSeal("ws_handback", handback, now.Add(6*time.Second))
	if err != nil {
		t.Fatalf("commit handback after reopen: %v", err)
	}
	if proof.Status != "released" || proof.Digest != finalDigest || proof.EventCursor != finalCursor || proof.PreparedAt != prepared.PreparedAt || proof.ReleasedAt == "" || proof.SourceResumedAt != "" {
		t.Fatalf("handback proof = %+v", proof)
	}
	commitReplay, err := second.HandbackCheckpointSeal("ws_handback", handback, now.Add(7*time.Second))
	if err != nil || commitReplay != proof {
		t.Fatalf("commit replay = %+v err=%v, want %+v", commitReplay, err, proof)
	}
	handback.Phase = CheckpointHandbackPhasePrepare
	releasedPrepareReplay, err := second.HandbackCheckpointSeal("ws_handback", handback, now.Add(7*time.Second))
	if err != nil || releasedPrepareReplay != proof {
		t.Fatalf("released prepare replay = %+v err=%v, want %+v", releasedPrepareReplay, err, proof)
	}
	handback.Phase = CheckpointHandbackPhaseCommit
	changedHandback := handback
	changedHandback.HandbackIdempotencyKey = "handback-job-changed"
	if _, err := second.HandbackCheckpointSeal("ws_handback", changedHandback, now.Add(7*time.Second)); !errors.Is(err, ErrCheckpointHandbackConflict) {
		t.Fatalf("changed handback replay error = %v", err)
	}
	resumed, err := second.ResumeCheckpointSeal("ws_handback", resumeRequest, now.Add(8*time.Second))
	if err != nil {
		t.Fatalf("resume after durable handback: %v", err)
	}
	if resumed.Status != "source-resumed" || resumed.Digest != proof.Digest || resumed.SourceResumedAt == "" {
		t.Fatalf("source resume proof = %+v", resumed)
	}
	resumedReplay, err := second.ResumeCheckpointSeal("ws_handback", resumeRequest, now.Add(9*time.Second))
	if err != nil || resumedReplay != resumed {
		t.Fatalf("resume replay = %+v err=%v, want %+v", resumedReplay, err, resumed)
	}
	changedResume := resumeRequest
	changedResume.ResumeIdempotencyKey = "source-resume-changed"
	if _, err := second.ResumeCheckpointSeal("ws_handback", changedResume, now.Add(9*time.Second)); !errors.Is(err, ErrCheckpointResumeConflict) {
		t.Fatalf("changed resume replay error = %v", err)
	}
	if _, err := second.ConsumeCheckpointSeal("ws_handback", CheckpointSealConsumeRequest{
		SealToken: issued.SealToken, Root: "/", SessionID: issued.SessionID, Generation: issued.Generation,
		ConsumerIdempotencyKey: consumerKey, ConsumerPrincipal: checkpointTestConsumerPrincipal,
	}, now.Add(8*time.Second)); !errors.Is(err, ErrCheckpointReplay) {
		t.Fatalf("destination reacquire after handback error = %v", err)
	}
}

func TestCheckpointUnconsumedSealCanBeCancelledBySource(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	defer store.Close()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	digest := checkpointDigestForStore(t, store, "ws_cancel", "/")
	issued, err := store.IssueCheckpointSeal("ws_cancel", CheckpointSealRequest{
		Root: "/", SessionID: "thread-cancel", Generation: 1, ExpectedDigest: digest,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := store.ResumeCheckpointSeal("ws_cancel", CheckpointSealResumeRequest{
		SealToken: issued.SealToken, Root: "/", SessionID: issued.SessionID,
		Generation: issued.Generation, ResumeIdempotencyKey: "source-cancel-one",
	}, now.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("cancel expired unconsumed seal: %v", err)
	}
	if proof.Status != "source-resumed" || proof.ConsumedAt != "" || proof.SourceResumedAt == "" {
		t.Fatalf("cancel proof = %+v", proof)
	}
}

func TestCheckpointHandbackCommitRejectsDurableChangeAfterPrepare(t *testing.T) {
	now := time.Date(2026, 8, 23, 14, 0, 0, 0, time.UTC)
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	defer store.Close()
	seedCheckpointFile(t, store, "ws_handback_prepare_race", "/source.txt", "source turn\n")
	digest := checkpointDigestForStore(t, store, "ws_handback_prepare_race", "/")
	issued, err := store.IssueCheckpointSeal("ws_handback_prepare_race", CheckpointSealRequest{
		Root: "/", SessionID: "thread-handback-prepare-race", Generation: 1, ExpectedDigest: digest,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	consumed, err := store.ConsumeCheckpointSeal("ws_handback_prepare_race", CheckpointSealConsumeRequest{
		SealToken: issued.SealToken, Root: "/", SessionID: issued.SessionID, Generation: issued.Generation,
		ConsumerIdempotencyKey: "consume-handback-prepare-race", ConsumerPrincipal: checkpointTestConsumerPrincipal,
	}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	handback := CheckpointSealHandbackRequest{
		Phase:  CheckpointHandbackPhasePrepare,
		SealID: consumed.SealID, Root: "/", SessionID: consumed.SessionID, Generation: consumed.Generation,
		ConsumedAt: consumed.ConsumedAt, ConsumerIdempotencyKey: "consume-handback-prepare-race",
		HandbackIdempotencyKey: "handback-prepare-race", ExpectedDigest: digest, ConsumerPrincipal: checkpointTestConsumerPrincipal,
	}
	prepared, err := store.HandbackCheckpointSeal("ws_handback_prepare_race", handback, now.Add(2*time.Second))
	if err != nil || prepared.Status != "prepared" {
		t.Fatalf("prepare=%+v err=%v", prepared, err)
	}
	seedCheckpointFile(t, store, "ws_handback_prepare_race", "/late.txt", "late callback bytes\n")
	handback.Phase = CheckpointHandbackPhaseCommit
	if _, err := store.HandbackCheckpointSeal("ws_handback_prepare_race", handback, now.Add(3*time.Second)); !errors.Is(err, ErrCheckpointDiverged) {
		t.Fatalf("changed durable state commit error=%v", err)
	}
	if _, err := store.ResumeCheckpointSeal("ws_handback_prepare_race", CheckpointSealResumeRequest{
		SealToken: issued.SealToken, Root: "/", SessionID: issued.SessionID, Generation: issued.Generation,
		ResumeIdempotencyKey: "resume-handback-prepare-race",
	}, now.Add(4*time.Second)); !errors.Is(err, ErrCheckpointHandbackRequired) {
		t.Fatalf("diverged prepare released ownership: %v", err)
	}
}

func TestCheckpointConsumedOwnershipSurvivesPastGatewayLeaseCap(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "relayfile-state.json")
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	first := NewStoreWithOptions(StoreOptions{StateFile: stateFile, DisableWorkers: true})
	digest := checkpointDigestForStore(t, first, "ws_long_owner", "/")
	issued, err := first.IssueCheckpointSeal("ws_long_owner", CheckpointSealRequest{
		Root: "/", SessionID: "thread-long-owner", Generation: 1, ExpectedDigest: digest,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	consumerKey := "cutover-long-owner"
	consumed, err := first.ConsumeCheckpointSeal("ws_long_owner", CheckpointSealConsumeRequest{
		SealToken: issued.SealToken, Root: "/", SessionID: issued.SessionID,
		Generation: issued.Generation, ConsumerIdempotencyKey: consumerKey, ConsumerPrincipal: checkpointTestConsumerPrincipal,
	}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	first.Close()

	second := NewStoreWithOptions(StoreOptions{StateFile: stateFile, DisableWorkers: true})
	defer second.Close()
	handback := CheckpointSealHandbackRequest{
		Phase:  CheckpointHandbackPhasePrepare,
		SealID: consumed.SealID, Root: "/", SessionID: consumed.SessionID, Generation: consumed.Generation,
		ConsumedAt: consumed.ConsumedAt, ConsumerIdempotencyKey: consumerKey,
		HandbackIdempotencyKey: "handback-after-cap", ExpectedDigest: digest, ConsumerPrincipal: checkpointTestConsumerPrincipal,
	}
	prepared, err := second.HandbackCheckpointSeal("ws_long_owner", handback, now.Add(25*time.Hour))
	if err != nil {
		t.Fatalf("prepare handback after 24h lease cap: %v", err)
	}
	if prepared.Status != "prepared" {
		t.Fatalf("late handback preparation = %+v", prepared)
	}
	handback.Phase = CheckpointHandbackPhaseCommit
	proof, err := second.HandbackCheckpointSeal("ws_long_owner", handback, now.Add(25*time.Hour+time.Second))
	if err != nil || proof.Status != "released" {
		t.Fatalf("late handback proof = %+v", proof)
	}
}

func TestCheckpointStoppedSourceOwnershipSurvivesPastDiagnosticRetention(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	t.Run("destination handed back but source stayed offline", func(t *testing.T) {
		store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
		defer store.Close()
		seedCheckpointFile(t, store, "ws_offline_after_handback", "/turn.txt", "destination turn\n")
		digest := checkpointDigestForStore(t, store, "ws_offline_after_handback", "/")
		issued, err := store.IssueCheckpointSeal("ws_offline_after_handback", CheckpointSealRequest{
			Root: "/", SessionID: "thread-offline-handback", Generation: 1, ExpectedDigest: digest,
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		consumed, err := store.ConsumeCheckpointSeal("ws_offline_after_handback", CheckpointSealConsumeRequest{
			SealToken: issued.SealToken, Root: "/", SessionID: issued.SessionID, Generation: issued.Generation,
			ConsumerIdempotencyKey: "consume-offline-handback", ConsumerPrincipal: checkpointTestConsumerPrincipal,
		}, now.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		handback := CheckpointSealHandbackRequest{
			Phase:  CheckpointHandbackPhasePrepare,
			SealID: issued.SealID, Root: "/", SessionID: issued.SessionID, Generation: issued.Generation,
			ConsumedAt: consumed.ConsumedAt, ConsumerIdempotencyKey: "consume-offline-handback",
			HandbackIdempotencyKey: "handback-offline-source", ExpectedDigest: digest, ConsumerPrincipal: checkpointTestConsumerPrincipal,
		}
		if _, err := store.HandbackCheckpointSeal("ws_offline_after_handback", handback, now.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		handback.Phase = CheckpointHandbackPhaseCommit
		if _, err := store.HandbackCheckpointSeal("ws_offline_after_handback", handback, now.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
		proof, err := store.ResumeCheckpointSeal("ws_offline_after_handback", CheckpointSealResumeRequest{
			SealToken: issued.SealToken, Root: "/", SessionID: issued.SessionID, Generation: issued.Generation,
			ResumeIdempotencyKey: "resume-after-25-hours",
		}, now.Add(25*time.Hour))
		if err != nil || proof.Status != "source-resumed" {
			t.Fatalf("resume after 25h offline proof=%+v err=%v", proof, err)
		}
	})

	t.Run("unconsumed expired seal still identifies stopped source", func(t *testing.T) {
		store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
		defer store.Close()
		digest := checkpointDigestForStore(t, store, "ws_offline_unconsumed", "/")
		issued, err := store.IssueCheckpointSeal("ws_offline_unconsumed", CheckpointSealRequest{
			Root: "/", SessionID: "thread-offline-unconsumed", Generation: 1, ExpectedDigest: digest, TTLSeconds: 1,
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		proof, err := store.ResumeCheckpointSeal("ws_offline_unconsumed", CheckpointSealResumeRequest{
			SealToken: issued.SealToken, Root: "/", SessionID: issued.SessionID, Generation: issued.Generation,
			ResumeIdempotencyKey: "resume-unconsumed-after-25-hours",
		}, now.Add(25*time.Hour))
		if err != nil || proof.Status != "source-resumed" || proof.ConsumedAt != "" {
			t.Fatalf("resume expired unconsumed seal proof=%+v err=%v", proof, err)
		}
	})
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
	_ = seedCheckpointFileResult(t, store, workspaceID, path, content)
}

func seedCheckpointFileResult(t *testing.T, store *Store, workspaceID, path, content string) WriteResult {
	t.Helper()
	result, err := store.WriteFile(WriteRequest{WorkspaceID: workspaceID, Path: path, IfMatch: "*", Content: content})
	if err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
	return result
}

func checkpointVerifyRequest(seal CheckpointSeal) CheckpointSealVerifyRequest {
	return CheckpointSealVerifyRequest{
		SealID: seal.SealID, Root: seal.Root, SessionID: seal.SessionID, Generation: seal.Generation,
		Digest: seal.Digest, WorkspaceRevision: seal.WorkspaceRevision, EventCursor: seal.EventCursor,
		IssuedAt: seal.IssuedAt, ExpiresAt: seal.ExpiresAt, ConsumedAt: seal.ConsumedAt,
		ConsumerPrincipal: checkpointTestConsumerPrincipal,
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

func checkpointStateForTest(t *testing.T, store *Store, workspaceID, root string) (string, string, string) {
	t.Helper()
	store.mu.RLock()
	defer store.mu.RUnlock()
	digest, revision, cursor, err := store.checkpointStateLocked(workspaceID, root)
	if err != nil {
		t.Fatalf("checkpoint state: %v", err)
	}
	return digest, revision, cursor
}
