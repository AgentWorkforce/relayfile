package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountlease"
	"github.com/agentworkforce/relayfile/internal/mountscope"
	"github.com/agentworkforce/relayfile/internal/mountsync"
)

type fakeCheckpointLease struct{ released bool }

func (l *fakeCheckpointLease) Release() error { l.released = true; return nil }

func installCheckpointLifecycleSeams(t *testing.T, active activeCheckpointMount, receipt mountsync.CheckpointSeal) (*fakeCheckpointLease, *int, *int) {
	t.Helper()
	originalResolve := checkpointResolveActive
	originalStop := checkpointStopActive
	originalIssue := checkpointIssueStopped
	originalVerify := checkpointVerifyStopped
	originalHandback := checkpointHandbackStopped
	originalEnsure := checkpointEnsureSource
	originalBurn := checkpointBurnReceipt
	originalWait := checkpointWaitMountReady
	originalWaitSourceProof := checkpointWaitSourceProof
	lease := &fakeCheckpointLease{}
	ensureCalls := 0
	burnCalls := 0
	checkpointResolveActive = func(string) (activeCheckpointMount, error) { return active, nil }
	checkpointStopActive = func(context.Context, activeCheckpointMount) (checkpointLease, error) { return lease, nil }
	checkpointIssueStopped = func(context.Context, checkpointMountConfig, string, uint64, int) (mountsync.CheckpointSeal, error) {
		return receipt, nil
	}
	checkpointVerifyStopped = func(context.Context, checkpointMountConfig, mountsync.CheckpointSeal) (mountsync.CheckpointVerification, error) {
		return mountsync.CheckpointVerification{
			Observed: mountsync.CheckpointObservedState{Digest: receipt.Digest, WorkspaceRevision: receipt.WorkspaceRevision, EventCursor: receipt.EventCursor},
		}, nil
	}
	checkpointHandbackStopped = func(_ context.Context, _ checkpointMountConfig, consumed mountsync.CheckpointSeal, _, _ string) (mountsync.CheckpointSealOwnership, mountsync.CheckpointVerificationHealth, error) {
		return mountsync.CheckpointSealOwnership{
			SealID: consumed.SealID, WorkspaceID: consumed.WorkspaceID, Root: consumed.Root,
			SessionID: consumed.SessionID, Generation: consumed.Generation, Status: "released",
			Digest: consumed.Digest, WorkspaceRevision: consumed.WorkspaceRevision, EventCursor: consumed.EventCursor,
			ConsumedAt: consumed.ConsumedAt, PreparedAt: "2026-08-23T12:00:09Z", ReleasedAt: "2026-08-23T12:00:10Z",
		}, mountsync.CheckpointVerificationHealth{}, nil
	}
	checkpointEnsureSource = func(checkpointMountConfig, time.Duration) error { ensureCalls++; return nil }
	checkpointBurnReceipt = func(checkpointLifecycleState, time.Duration) (mountsync.CheckpointSealOwnership, error) {
		burnCalls++
		return mountsync.CheckpointSealOwnership{
			SealID: receipt.SealID, WorkspaceID: receipt.WorkspaceID, Root: receipt.Root,
			SessionID: receipt.SessionID, Generation: receipt.Generation, Status: "source-resumed",
			Digest: receipt.Digest, WorkspaceRevision: receipt.WorkspaceRevision, EventCursor: receipt.EventCursor,
			ConsumedAt: receipt.ConsumedAt, ReleasedAt: "2026-08-23T12:00:10Z", SourceResumedAt: "2026-08-23T12:00:11Z",
		}, nil
	}
	checkpointWaitMountReady = func(checkpointMountConfig, time.Duration) error { return nil }
	checkpointWaitSourceProof = func(checkpointMountConfig, mountsync.CheckpointSealOwnership, time.Duration) error { return nil }
	t.Cleanup(func() {
		checkpointResolveActive = originalResolve
		checkpointStopActive = originalStop
		checkpointIssueStopped = originalIssue
		checkpointVerifyStopped = originalVerify
		checkpointHandbackStopped = originalHandback
		checkpointEnsureSource = originalEnsure
		checkpointBurnReceipt = originalBurn
		checkpointWaitMountReady = originalWait
		checkpointWaitSourceProof = originalWaitSourceProof
	})
	return lease, &ensureCalls, &burnCalls
}

func consumedCheckpointTestReceipt(receipt mountsync.CheckpointSeal) mountsync.CheckpointSeal {
	receipt.SealToken = ""
	receipt.ConsumedAt = "2026-08-23T12:00:05Z"
	return receipt
}

func checkpointInputAtLimit(t *testing.T, base []byte) []byte {
	t.Helper()
	if len(base) > checkpointCLIInputMaxBytes {
		t.Fatalf("base input is already %d bytes", len(base))
	}
	payload := append([]byte(nil), base...)
	payload = append(payload, bytes.Repeat([]byte(" "), checkpointCLIInputMaxBytes-len(payload))...)
	return payload
}

func checkpointTestActive(t *testing.T) (activeCheckpointMount, mountsync.CheckpointSeal) {
	t.Helper()
	root := t.TempDir()
	credentials := filepath.Join(t.TempDir(), "delegated.json")
	if err := os.WriteFile(credentials, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := checkpointMountConfig{
		Version: checkpointLifecycleVersion, Server: "https://relayfile.test", CredentialsFile: credentials,
		WorkspaceID: "ws_checkpoint", LocalRoot: root, RemotePaths: []string{"/"},
		LocalLayout: mountscope.LayoutExact, Mode: defaultMountMode, Interval: "30s", Timeout: "15s",
		BootstrapTimeout: "0s", BootstrapMaxFilesPerCycle: 2000, FullPullMinInterval: "24h0m0s",
		CursorTimeout: "1m0s", WebsocketEnabled: true, MemlogInterval: "0s",
	}
	receipt := mountsync.CheckpointSeal{
		SealID: "cps_123", SealToken: "one-use-token", WorkspaceID: config.WorkspaceID,
		Root: "/", SessionID: "session-123", Generation: 2,
		Digest: "sha256:" + strings.Repeat("a", 64), WorkspaceRevision: "rev_1", EventCursor: "evt_1",
		IssuedAt: "2026-08-23T12:00:00Z", ExpiresAt: "2026-08-23T12:01:00Z",
	}
	return activeCheckpointMount{record: workspaceRecord{ID: config.WorkspaceID, LocalDir: root, RemotePaths: []string{"/"}, LocalLayout: mountscope.LayoutExact}, config: config}, receipt
}

func TestMountCheckpointSealEmitsDistinctLocalAndRemoteRootsAndIsCrashIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	active, receipt := checkpointTestActive(t)
	lease, _, _ := installCheckpointLifecycleSeams(t, active, receipt)
	args := []string{"--root", active.config.LocalRoot, "--lifecycle-id", "rsm_controller_123", "--session", "session-123", "--generation", "2", "--json"}
	var first bytes.Buffer
	if err := runMountCheckpointSeal(args, &first); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if !lease.released {
		t.Fatal("checkpoint did not release the acquired mount lease")
	}
	var envelope checkpointSealEnvelope
	if err := json.Unmarshal(first.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Status != "sealed" || !sameCheckpointLocalRoot(envelope.LocalRoot, active.config.LocalRoot) || envelope.Receipt.Root != "/" || envelope.ResumeID == "" {
		t.Fatalf("ambiguous or incomplete roots: %+v", envelope)
	}
	if envelope.Health != (mountsync.CheckpointVerificationHealth{}) {
		t.Fatalf("successful final drain reported non-zero health: %+v", envelope.Health)
	}
	if !strings.Contains(first.String(), `"outboxNeedsAttention": false`) {
		t.Fatalf("checkpoint health wire type is not an explicit boolean: %s", first.String())
	}
	var replay bytes.Buffer
	if err := runMountCheckpointSeal(args, &replay); err != nil {
		t.Fatalf("response-loss retry: %v", err)
	}
	var replayEnvelope checkpointSealEnvelope
	_ = json.Unmarshal(replay.Bytes(), &replayEnvelope)
	if replayEnvelope.ResumeID != envelope.ResumeID || replayEnvelope.Receipt.SealToken != envelope.Receipt.SealToken {
		t.Fatalf("checkpoint retry changed durable handoff: first=%+v retry=%+v", envelope, replayEnvelope)
	}
	conflicting := []string{"--root", active.config.LocalRoot, "--lifecycle-id", "rsm_controller_123", "--session", "session-other", "--generation", "2", "--json"}
	if err := runMountCheckpointSeal(conflicting, &bytes.Buffer{}); checkpointExitCode(err) != 3 {
		t.Fatalf("lifecycle identity conflict = %v", err)
	}
	staleArgs := []string{"--root", active.config.LocalRoot, "--lifecycle-id", "rsm_controller_stale", "--session", "session-123", "--generation", "1", "--json"}
	if err := runMountCheckpointSeal(staleArgs, &bytes.Buffer{}); checkpointExitCode(err) != 3 {
		t.Fatalf("stale generation error = %v", err)
	}
}

func TestMountCheckpointRequiresControllerLifecycleIntentBeforeStop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	active, receipt := checkpointTestActive(t)
	_, _, _ = installCheckpointLifecycleSeams(t, active, receipt)
	stopCalls := 0
	originalStop := checkpointStopActive
	checkpointStopActive = func(context.Context, activeCheckpointMount) (checkpointLease, error) {
		stopCalls++
		return &fakeCheckpointLease{}, nil
	}
	t.Cleanup(func() { checkpointStopActive = originalStop })
	err := runMountCheckpointSeal([]string{"--root", active.config.LocalRoot, "--session", receipt.SessionID, "--generation", "2", "--json"}, &bytes.Buffer{})
	if checkpointExitCode(err) != 2 || stopCalls != 0 {
		t.Fatalf("missing lifecycle intent err=%v stopCalls=%d", err, stopCalls)
	}
}

func TestMountCheckpointPresealFailureRestartsBeforeReturning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	active, receipt := checkpointTestActive(t)
	_, ensureCalls, _ := installCheckpointLifecycleSeams(t, active, receipt)
	checkpointIssueStopped = func(context.Context, checkpointMountConfig, string, uint64, int) (mountsync.CheckpointSeal, error) {
		return mountsync.CheckpointSeal{}, errors.New("server divergence")
	}
	err := runMountCheckpointSeal([]string{"--root", active.config.LocalRoot, "--lifecycle-id", "rsm_controller_fail", "--session", "session-fail", "--generation", "3", "--json"}, &bytes.Buffer{})
	if checkpointExitCode(err) != 5 || *ensureCalls != 1 {
		t.Fatalf("preseal failure err=%v ensureCalls=%d", err, *ensureCalls)
	}
}

func TestMountResumeSealIsCrashRecoverableIdempotentAndRootBound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	active, receipt := checkpointTestActive(t)
	_, ensureCalls, burnCalls := installCheckpointLifecycleSeams(t, active, receipt)
	state := checkpointLifecycleState{
		Version: checkpointLifecycleVersion, Kind: "relayfile-checkpoint-lifecycle", ResumeID: "rsm_test_123",
		WorkspaceID: active.config.WorkspaceID, LocalRoot: active.config.LocalRoot, RemoteRoot: receipt.Root,
		SessionID: receipt.SessionID, Generation: receipt.Generation, Status: "sealed", Config: active.config,
		Receipt: &receipt, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), SealedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := saveCheckpointLifecycle(state); err != nil {
		t.Fatal(err)
	}
	stdin := func() *strings.Reader { return strings.NewReader(`{"resumeId":"rsm_test_123"}`) }
	var output bytes.Buffer
	if err := runMountResumeSeal([]string{"--root", active.config.LocalRoot, "--json"}, stdin(), &output); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if *burnCalls != 1 || *ensureCalls != 1 {
		t.Fatalf("resume calls burn=%d ensure=%d", *burnCalls, *ensureCalls)
	}
	if err := runMountResumeSeal([]string{"--root", active.config.LocalRoot, "--json"}, stdin(), &bytes.Buffer{}); err != nil {
		t.Fatalf("idempotent resume: %v", err)
	}
	wrongRoot := t.TempDir()
	if err := runMountResumeSeal([]string{"--root", wrongRoot, "--json"}, stdin(), &bytes.Buffer{}); checkpointExitCode(err) != 2 {
		t.Fatalf("wrong root error = %v", err)
	}
}

func TestMountResumeSealRejectsNonExactJSONDocument(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	for name, input := range map[string]string{
		"malformed":              `{"resumeId":`,
		"trailing second object": `{"resumeId":"rsm_exact"}{"resumeId":"rsm_other"}`,
		"trailing garbage":       `{"resumeId":"rsm_exact"}not-json`,
		"unknown field":          `{"resumeId":"rsm_exact","extra":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			err := runMountResumeSeal([]string{"--root", root, "--json"}, strings.NewReader(input), &output)
			if checkpointExitCode(err) != 2 || !strings.Contains(err.Error(), "exactly one JSON object") || output.Len() != 0 {
				t.Fatalf("non-exact JSON err=%v output=%q", err, output.String())
			}
		})
	}
}

func TestCheckpointCommandInputCutoffRejectsHiddenTrailingBytes(t *testing.T) {
	t.Run("exact boundary remains a complete decodable document", func(t *testing.T) {
		payload := checkpointInputAtLimit(t, []byte(`{"resumeId":"rsm_boundary"}`))
		var input checkpointResumeInput
		if err := decodeStrictCheckpointInput(bytes.NewReader(payload), &input); err != nil || input.ResumeID != "rsm_boundary" {
			t.Fatalf("exact-boundary decode input=%+v err=%v", input, err)
		}
	})

	t.Run("resume second object just beyond cutoff has no lifecycle effect", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		root := t.TempDir()
		payload := append(checkpointInputAtLimit(t, []byte(`{"resumeId":"rsm_cutoff"}`)), []byte(`{}`)...)
		err := runMountResumeSeal([]string{"--root", root, "--json"}, bytes.NewReader(payload), &bytes.Buffer{})
		if checkpointExitCode(err) != 2 {
			t.Fatalf("resume cutoff error=%v", err)
		}
		if _, statErr := os.Stat(checkpointLifecyclePath("rsm_cutoff")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("resume cutoff created lifecycle: %v", statErr)
		}
	})

	t.Run("verify garbage just beyond cutoff has no mount effect", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		active, issued := checkpointTestActive(t)
		receipt := consumedCheckpointTestReceipt(issued)
		installCheckpointLifecycleSeams(t, active, receipt)
		resolveCalls := 0
		checkpointResolveActive = func(string) (activeCheckpointMount, error) {
			resolveCalls++
			return active, nil
		}
		input := checkpointVerificationInput{VerificationID: "vrf_cutoff", Receipt: receipt}
		base, _ := json.Marshal(input)
		payload := append(checkpointInputAtLimit(t, base), 'x')
		err := runMountVerifySeal([]string{"--root", active.config.LocalRoot, "--json"}, bytes.NewReader(payload), &bytes.Buffer{})
		if checkpointExitCode(err) != 2 || resolveCalls != 0 {
			t.Fatalf("verify cutoff error=%v resolveCalls=%d", err, resolveCalls)
		}
		if _, statErr := os.Stat(checkpointVerificationPath(input.VerificationID)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("verify cutoff created lifecycle: %v", statErr)
		}
	})

	t.Run("handback second object just beyond cutoff has no mount effect", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		active, issued := checkpointTestActive(t)
		receipt := consumedCheckpointTestReceipt(issued)
		installCheckpointLifecycleSeams(t, active, receipt)
		resolveCalls := 0
		checkpointResolveActive = func(string) (activeCheckpointMount, error) {
			resolveCalls++
			return active, nil
		}
		input := checkpointHandbackInput{HandbackID: "handback-cutoff", ConsumerIdempotencyKey: "cutover-cutoff", Receipt: receipt}
		base, _ := json.Marshal(input)
		payload := append(checkpointInputAtLimit(t, base), []byte(`{}`)...)
		err := runMountHandbackSeal([]string{"--root", active.config.LocalRoot, "--json"}, bytes.NewReader(payload), &bytes.Buffer{})
		if checkpointExitCode(err) != 2 || resolveCalls != 0 {
			t.Fatalf("handback cutoff error=%v resolveCalls=%d", err, resolveCalls)
		}
		if _, statErr := os.Stat(checkpointHandbackPath(input.HandbackID)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("handback cutoff created lifecycle: %v", statErr)
		}
	})
}

func TestMountResumeSealCannotReportReadyBeforeResumeProofMaterializes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	active, receipt := checkpointTestActive(t)
	_, _, burnCalls := installCheckpointLifecycleSeams(t, active, receipt)
	state := checkpointLifecycleState{
		Version: checkpointLifecycleVersion, Kind: "relayfile-checkpoint-lifecycle", ResumeID: "rsm_delayed_pull",
		WorkspaceID: active.config.WorkspaceID, LocalRoot: active.config.LocalRoot, RemoteRoot: receipt.Root,
		SessionID: receipt.SessionID, Generation: receipt.Generation, Status: "sealed", Config: active.config,
		Receipt: &receipt, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), SealedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := saveCheckpointLifecycle(state); err != nil {
		t.Fatal(err)
	}
	destinationTurn := filepath.Join(active.config.LocalRoot, "destination-turn.txt")
	checkpointWaitSourceProof = func(_ checkpointMountConfig, _ mountsync.CheckpointSealOwnership, _ time.Duration) error {
		if _, err := os.Stat(destinationTurn); err != nil {
			return fmt.Errorf("%w: destination turn not materialized", mountsync.ErrCheckpointNonConverged)
		}
		return nil
	}
	input := func() *strings.Reader { return strings.NewReader(`{"resumeId":"rsm_delayed_pull"}`) }
	var premature bytes.Buffer
	err := runMountResumeSeal([]string{"--root", active.config.LocalRoot, "--json"}, input(), &premature)
	if checkpointExitCode(err) != 5 || premature.Len() != 0 {
		t.Fatalf("premature source admission err=%v output=%q", err, premature.String())
	}
	loaded, loadErr := loadCheckpointLifecycle("rsm_delayed_pull")
	if loadErr != nil || loaded.Status != "resuming" || loaded.ResumeProof == nil || loaded.Receipt == nil || loaded.Receipt.SealToken == "" {
		t.Fatalf("nonconverged lifecycle=%+v err=%v", loaded, loadErr)
	}
	if err := os.WriteFile(destinationTurn, []byte("destination turn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var ready bytes.Buffer
	if err := runMountResumeSeal([]string{"--root", active.config.LocalRoot, "--json"}, input(), &ready); err != nil {
		t.Fatalf("resume after destination turn materialized: %v", err)
	}
	loaded, loadErr = loadCheckpointLifecycle("rsm_delayed_pull")
	if loadErr != nil || loaded.Status != "ready" || loaded.ResumeProof == nil || loaded.Receipt == nil || loaded.Receipt.SealToken != "" || *burnCalls != 1 {
		t.Fatalf("ready lifecycle=%+v burnCalls=%d err=%v", loaded, *burnCalls, loadErr)
	}
}

func TestMountResumeSealFailsClosedUntilDestinationHandback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	active, receipt := checkpointTestActive(t)
	_, ensureCalls, _ := installCheckpointLifecycleSeams(t, active, receipt)
	state := checkpointLifecycleState{
		Version: checkpointLifecycleVersion, Kind: "relayfile-checkpoint-lifecycle", ResumeID: "rsm_destination_owned",
		WorkspaceID: active.config.WorkspaceID, LocalRoot: active.config.LocalRoot, RemoteRoot: receipt.Root,
		SessionID: receipt.SessionID, Generation: receipt.Generation, Status: "sealed", Config: active.config,
		Receipt: &receipt, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), SealedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := saveCheckpointLifecycle(state); err != nil {
		t.Fatal(err)
	}
	checkpointBurnReceipt = func(checkpointLifecycleState, time.Duration) (mountsync.CheckpointSealOwnership, error) {
		return mountsync.CheckpointSealOwnership{}, &mountsync.HTTPError{StatusCode: 409, Code: "checkpoint_handback_required", Message: "destination still owns seal"}
	}
	err := runMountResumeSeal([]string{"--root", active.config.LocalRoot, "--json"}, strings.NewReader(`{"resumeId":"rsm_destination_owned"}`), &bytes.Buffer{})
	if checkpointExitCode(err) != 3 || !strings.Contains(err.Error(), "checkpoint_handback_required") || *ensureCalls != 0 {
		t.Fatalf("destination-owned resume err=%v ensureCalls=%d", err, *ensureCalls)
	}
	loaded, loadErr := loadCheckpointLifecycle("rsm_destination_owned")
	if loadErr != nil || loaded.Status != "sealed" {
		t.Fatalf("premature resume lifecycle=%+v err=%v", loaded, loadErr)
	}
}

func TestMountVerifySealOwnsVerdictRestartsAndIsResponseLossIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	active, issued := checkpointTestActive(t)
	receipt := consumedCheckpointTestReceipt(issued)
	lease, ensureCalls, _ := installCheckpointLifecycleSeams(t, active, receipt)
	input := checkpointVerificationInput{VerificationID: "vrf_controller_123", Receipt: receipt}
	payload, _ := json.Marshal(input)
	args := []string{"--root", active.config.LocalRoot, "--json"}
	var first bytes.Buffer
	if err := runMountVerifySeal(args, bytes.NewReader(payload), &first); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !lease.released || *ensureCalls != 1 {
		t.Fatalf("verification did not release/restart: lease=%v ensure=%d", lease.released, *ensureCalls)
	}
	var envelope checkpointDestinationVerificationEnvelope
	if err := json.Unmarshal(first.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Version != 1 || envelope.Kind != "relayfile-destination-verification" || envelope.VerificationID != input.VerificationID || envelope.WorkspaceID != receipt.WorkspaceID || envelope.RemoteRoot != "/" || envelope.SessionID != receipt.SessionID || envelope.Generation != receipt.Generation || envelope.Status != "converged" || envelope.Observed.Digest != receipt.Digest || envelope.Observed.WorkspaceRevision != receipt.WorkspaceRevision || envelope.Observed.EventCursor != receipt.EventCursor || envelope.VerifiedAt == "" {
		t.Fatalf("invalid verification envelope: %+v", envelope)
	}
	if !strings.Contains(first.String(), `"outboxNeedsAttention": false`) {
		t.Fatalf("verification health wire type is not an explicit boolean: %s", first.String())
	}
	var replay bytes.Buffer
	if err := runMountVerifySeal(args, bytes.NewReader(payload), &replay); err != nil {
		t.Fatalf("response-loss replay: %v", err)
	}
	var replayEnvelope checkpointDestinationVerificationEnvelope
	_ = json.Unmarshal(replay.Bytes(), &replayEnvelope)
	if replayEnvelope.VerifiedAt != envelope.VerifiedAt || replayEnvelope.Observed != envelope.Observed || replayEnvelope.Health != envelope.Health {
		t.Fatalf("verification replay changed verdict: first=%+v replay=%+v", envelope, replayEnvelope)
	}
	changed := input
	changed.Receipt.EventCursor = "evt_999"
	changedPayload, _ := json.Marshal(changed)
	if err := runMountVerifySeal(args, bytes.NewReader(changedPayload), &bytes.Buffer{}); checkpointExitCode(err) != 3 {
		t.Fatalf("changed receipt replay = %v", err)
	}
}

func TestMountVerifySealRejectsNonNativeRevisionAndCursorBeforeStop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	active, issued := checkpointTestActive(t)
	receipt := consumedCheckpointTestReceipt(issued)
	stopCalls := 0
	originalStop := checkpointStopActive
	checkpointStopActive = func(context.Context, activeCheckpointMount) (checkpointLease, error) {
		stopCalls++
		return &fakeCheckpointLease{}, nil
	}
	t.Cleanup(func() { checkpointStopActive = originalStop })
	for name, mutate := range map[string]func(*mountsync.CheckpointSeal){
		"bare revision": func(value *mountsync.CheckpointSeal) { value.WorkspaceRevision = "12" },
		"bare cursor":   func(value *mountsync.CheckpointSeal) { value.EventCursor = "12" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := receipt
			mutate(&candidate)
			payload, _ := json.Marshal(checkpointVerificationInput{VerificationID: "vrf_invalid_" + strings.ReplaceAll(name, " ", "_"), Receipt: candidate})
			err := runMountVerifySeal([]string{"--root", active.config.LocalRoot, "--json"}, bytes.NewReader(payload), &bytes.Buffer{})
			if checkpointExitCode(err) != 2 {
				t.Fatalf("invalid wire receipt error = %v", err)
			}
		})
	}
	if stopCalls != 0 {
		t.Fatalf("invalid receipt stopped destination mount %d times", stopCalls)
	}
}

func TestMountVerifySealServerFailureFailsClosedAfterDestinationRecovery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	active, issued := checkpointTestActive(t)
	receipt := consumedCheckpointTestReceipt(issued)
	lease, ensureCalls, _ := installCheckpointLifecycleSeams(t, active, receipt)
	checkpointVerifyStopped = func(context.Context, checkpointMountConfig, mountsync.CheckpointSeal) (mountsync.CheckpointVerification, error) {
		return mountsync.CheckpointVerification{}, errors.New("server unavailable")
	}
	payload, _ := json.Marshal(checkpointVerificationInput{VerificationID: "vrf_server_down", Receipt: receipt})
	err := runMountVerifySeal([]string{"--root", active.config.LocalRoot, "--json"}, bytes.NewReader(payload), &bytes.Buffer{})
	if checkpointExitCode(err) != 5 || !strings.Contains(err.Error(), "server unavailable") || !lease.released || *ensureCalls != 1 {
		t.Fatalf("server failure err=%v lease=%v ensure=%d", err, lease.released, *ensureCalls)
	}
}

func TestMountHandbackSealStopsDrainsReleasesAndIsResponseLossIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	active, issued := checkpointTestActive(t)
	receipt := consumedCheckpointTestReceipt(issued)
	lease, ensureCalls, _ := installCheckpointLifecycleSeams(t, active, receipt)
	handbackCalls := 0
	checkpointHandbackStopped = func(_ context.Context, _ checkpointMountConfig, consumed mountsync.CheckpointSeal, consumerKey, handbackKey string) (mountsync.CheckpointSealOwnership, mountsync.CheckpointVerificationHealth, error) {
		handbackCalls++
		if consumerKey != "cutover-job-handback" || handbackKey != "handback-job-one" {
			t.Fatalf("handback identities consumer=%q handback=%q", consumerKey, handbackKey)
		}
		return mountsync.CheckpointSealOwnership{
			SealID: consumed.SealID, WorkspaceID: consumed.WorkspaceID, Root: consumed.Root,
			SessionID: consumed.SessionID, Generation: consumed.Generation, Status: "released",
			Digest: consumed.Digest, WorkspaceRevision: "rev_3", EventCursor: consumed.EventCursor,
			ConsumedAt: consumed.ConsumedAt, PreparedAt: "2026-08-23T12:00:09Z", ReleasedAt: "2026-08-23T12:00:10Z",
		}, mountsync.CheckpointVerificationHealth{}, nil
	}
	input := checkpointHandbackInput{HandbackID: "handback-job-one", ConsumerIdempotencyKey: "cutover-job-handback", Receipt: receipt}
	payload, _ := json.Marshal(input)
	args := []string{"--root", active.config.LocalRoot, "--json"}
	var first bytes.Buffer
	if err := runMountHandbackSeal(args, bytes.NewReader(payload), &first); err != nil {
		t.Fatalf("handback: %v", err)
	}
	if !lease.released || *ensureCalls != 0 || handbackCalls != 1 {
		t.Fatalf("handback lease=%v restart=%d calls=%d", lease.released, *ensureCalls, handbackCalls)
	}
	var envelope checkpointHandbackEnvelope
	if err := json.Unmarshal(first.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Kind != "relayfile-checkpoint-handback" || envelope.Status != "released" || envelope.HandbackID != input.HandbackID || envelope.Proof.Status != "released" || envelope.Proof.SourceResumedAt != "" {
		t.Fatalf("handback envelope=%+v", envelope)
	}
	if !strings.Contains(first.String(), `"outboxNeedsAttention": false`) || strings.Contains(first.String(), "sealToken") {
		t.Fatalf("handback output leaked bearer or changed health wire type: %s", first.String())
	}
	var replay bytes.Buffer
	if err := runMountHandbackSeal(args, bytes.NewReader(payload), &replay); err != nil {
		t.Fatalf("handback response-loss retry: %v", err)
	}
	if handbackCalls != 1 || replay.String() != first.String() {
		t.Fatalf("handback retry calls=%d first=%s replay=%s", handbackCalls, first.String(), replay.String())
	}
	saved, ok, err := loadCheckpointHandbackIfExists(input.HandbackID)
	if err != nil || !ok || saved.Result == nil {
		t.Fatalf("load released handback lifecycle=%+v ok=%v err=%v", saved, ok, err)
	}
	for name, preparedAt := range map[string]string{
		"missing preparedAt":   "",
		"malformed preparedAt": "not-a-time",
	} {
		t.Run(name, func(t *testing.T) {
			candidate := saved
			result := *saved.Result
			result.Proof.PreparedAt = preparedAt
			candidate.Result = &result
			if err := saveCheckpointHandback(candidate); err == nil {
				t.Fatal("released handback lifecycle accepted invalid preparedAt")
			}
		})
	}
}

func TestMountHandbackFailureRecoveryDistinguishesDefinitiveFromAmbiguous(t *testing.T) {
	t.Run("definitive server rejection restarts destination", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		active, issued := checkpointTestActive(t)
		receipt := consumedCheckpointTestReceipt(issued)
		_, ensureCalls, _ := installCheckpointLifecycleSeams(t, active, receipt)
		checkpointHandbackStopped = func(context.Context, checkpointMountConfig, mountsync.CheckpointSeal, string, string) (mountsync.CheckpointSealOwnership, mountsync.CheckpointVerificationHealth, error) {
			return mountsync.CheckpointSealOwnership{}, mountsync.CheckpointVerificationHealth{}, &mountsync.HTTPError{StatusCode: 409, Code: "checkpoint_diverged", Message: "destination not drained"}
		}
		payload, _ := json.Marshal(checkpointHandbackInput{HandbackID: "handback-definitive", ConsumerIdempotencyKey: "cutover-definitive", Receipt: receipt})
		err := runMountHandbackSeal([]string{"--root", active.config.LocalRoot, "--json"}, bytes.NewReader(payload), &bytes.Buffer{})
		if checkpointExitCode(err) != 5 || *ensureCalls != 1 {
			t.Fatalf("definitive handback err=%v restart=%d", err, *ensureCalls)
		}
	})

	t.Run("transport ambiguity leaves destination stopped for exact retry", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		active, issued := checkpointTestActive(t)
		receipt := consumedCheckpointTestReceipt(issued)
		_, ensureCalls, _ := installCheckpointLifecycleSeams(t, active, receipt)
		checkpointHandbackStopped = func(context.Context, checkpointMountConfig, mountsync.CheckpointSeal, string, string) (mountsync.CheckpointSealOwnership, mountsync.CheckpointVerificationHealth, error) {
			return mountsync.CheckpointSealOwnership{}, mountsync.CheckpointVerificationHealth{}, errors.New("connection reset after POST")
		}
		payload, _ := json.Marshal(checkpointHandbackInput{HandbackID: "handback-ambiguous", ConsumerIdempotencyKey: "cutover-ambiguous", Receipt: receipt})
		err := runMountHandbackSeal([]string{"--root", active.config.LocalRoot, "--json"}, bytes.NewReader(payload), &bytes.Buffer{})
		if checkpointExitCode(err) != 4 || !strings.Contains(err.Error(), "handback_result_unknown") || *ensureCalls != 0 {
			t.Fatalf("ambiguous handback err=%v restart=%d", err, *ensureCalls)
		}
		state, ok, loadErr := loadCheckpointHandbackIfExists("handback-ambiguous")
		if loadErr != nil || !ok || state.Status != "handback-unknown" {
			t.Fatalf("ambiguous lifecycle=%+v ok=%v err=%v", state, ok, loadErr)
		}
	})

	for _, tc := range []struct {
		name   string
		status int
		code   string
	}{
		{name: "unknown-409", status: 409, code: "conflict"},
		{name: "released-under-another-handback-key", status: 409, code: "checkpoint_handback_conflict"},
		{name: "not-found-after-release-or-gc", status: 404, code: "checkpoint_seal_not_found"},
		{name: "source-already-resumed", status: 409, code: "checkpoint_replayed"},
		{name: "resume-ownership-conflict", status: 409, code: "checkpoint_resume_conflict"},
	} {
		tc := tc
		t.Run(tc.name+" stays stopped and ambiguous", func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			active, issued := checkpointTestActive(t)
			receipt := consumedCheckpointTestReceipt(issued)
			_, ensureCalls, _ := installCheckpointLifecycleSeams(t, active, receipt)
			checkpointHandbackStopped = func(context.Context, checkpointMountConfig, mountsync.CheckpointSeal, string, string) (mountsync.CheckpointSealOwnership, mountsync.CheckpointVerificationHealth, error) {
				return mountsync.CheckpointSealOwnership{}, mountsync.CheckpointVerificationHealth{}, &mountsync.HTTPError{StatusCode: tc.status, Code: tc.code, Message: "ownership outcome is not safe to invert"}
			}
			handbackID := "handback-" + tc.name
			payload, _ := json.Marshal(checkpointHandbackInput{HandbackID: handbackID, ConsumerIdempotencyKey: "cutover-" + tc.name, Receipt: receipt})
			err := runMountHandbackSeal([]string{"--root", active.config.LocalRoot, "--json"}, bytes.NewReader(payload), &bytes.Buffer{})
			if checkpointExitCode(err) != 4 || !strings.Contains(err.Error(), "handback_result_unknown") || *ensureCalls != 0 {
				t.Fatalf("ambiguous semantic HTTP error=%v restart=%d", err, *ensureCalls)
			}
			state, ok, loadErr := loadCheckpointHandbackIfExists(handbackID)
			if loadErr != nil || !ok || state.Status != "handback-unknown" {
				t.Fatalf("ambiguous semantic lifecycle=%+v ok=%v err=%v", state, ok, loadErr)
			}
		})
	}

	for _, tc := range []struct {
		name       string
		status     int
		code       string
		retryCount int
	}{
		{name: "bad-request", status: 400, code: "bad_request", retryCount: 1},
		{name: "unauthorized", status: 401, code: "unauthorized", retryCount: 2},
		{name: "forbidden", status: 403, code: "forbidden", retryCount: 2},
		{name: "payload-too-large", status: 413, code: "payload_too_large", retryCount: 1},
	} {
		tc := tc
		t.Run(tc.name+" after committed response loss stays stopped", func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			active, issued := checkpointTestActive(t)
			receipt := consumedCheckpointTestReceipt(issued)
			lease, ensureCalls, _ := installCheckpointLifecycleSeams(t, active, receipt)
			committed := false
			handbackCalls := 0
			checkpointHandbackStopped = func(context.Context, checkpointMountConfig, mountsync.CheckpointSeal, string, string) (mountsync.CheckpointSealOwnership, mountsync.CheckpointVerificationHealth, error) {
				handbackCalls++
				committed = true
				return mountsync.CheckpointSealOwnership{}, mountsync.CheckpointVerificationHealth{}, &mountsync.HTTPError{
					StatusCode: tc.status,
					Code:       tc.code,
					Message:    "retry response received after the original POST committed",
				}
			}
			handbackID := "handback-postcommit-" + tc.name
			payload, _ := json.Marshal(checkpointHandbackInput{HandbackID: handbackID, ConsumerIdempotencyKey: "cutover-postcommit-" + tc.name, Receipt: receipt})
			args := []string{"--root", active.config.LocalRoot, "--json"}
			for attempt := 0; attempt < tc.retryCount; attempt++ {
				var output bytes.Buffer
				err := runMountHandbackSeal(args, bytes.NewReader(payload), &output)
				if checkpointExitCode(err) != 4 || !strings.Contains(err.Error(), "handback_result_unknown") || output.Len() != 0 || *ensureCalls != 0 || !committed {
					t.Fatalf("attempt %d err=%v output=%q restart=%d committed=%v", attempt+1, err, output.String(), *ensureCalls, committed)
				}
				state, ok, loadErr := loadCheckpointHandbackIfExists(handbackID)
				if loadErr != nil || !ok || state.Status != "handback-unknown" || state.Result != nil {
					t.Fatalf("attempt %d lifecycle=%+v ok=%v err=%v", attempt+1, state, ok, loadErr)
				}
			}
			if !lease.released || handbackCalls != tc.retryCount {
				t.Fatalf("lease=%v handbackCalls=%d want=%d", lease.released, handbackCalls, tc.retryCount)
			}
		})
	}

	t.Run("503 after application commit stays stopped and exact retry recovers proof", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		active, issued := checkpointTestActive(t)
		receipt := consumedCheckpointTestReceipt(issued)
		lease, ensureCalls, _ := installCheckpointLifecycleSeams(t, active, receipt)
		committed := false
		handbackCalls := 0
		checkpointHandbackStopped = func(_ context.Context, _ checkpointMountConfig, consumed mountsync.CheckpointSeal, consumerKey, handbackKey string) (mountsync.CheckpointSealOwnership, mountsync.CheckpointVerificationHealth, error) {
			handbackCalls++
			if consumerKey != "cutover-503-after-commit" || handbackKey != "handback-503-after-commit" {
				t.Fatalf("changed idempotency identity consumer=%q handback=%q", consumerKey, handbackKey)
			}
			proof := mountsync.CheckpointSealOwnership{
				SealID: consumed.SealID, WorkspaceID: consumed.WorkspaceID, Root: consumed.Root,
				SessionID: consumed.SessionID, Generation: consumed.Generation, Status: "released",
				Digest: consumed.Digest, WorkspaceRevision: "rev_3", EventCursor: consumed.EventCursor,
				ConsumedAt: consumed.ConsumedAt, PreparedAt: "2026-08-23T12:00:09Z", ReleasedAt: "2026-08-23T12:00:10Z",
			}
			if !committed {
				committed = true
				return mountsync.CheckpointSealOwnership{}, mountsync.CheckpointVerificationHealth{}, &mountsync.HTTPError{StatusCode: 503, Code: "upstream_unavailable", Message: "gateway lost committed response"}
			}
			return proof, mountsync.CheckpointVerificationHealth{}, nil
		}
		input := checkpointHandbackInput{HandbackID: "handback-503-after-commit", ConsumerIdempotencyKey: "cutover-503-after-commit", Receipt: receipt}
		payload, _ := json.Marshal(input)
		args := []string{"--root", active.config.LocalRoot, "--json"}
		var first bytes.Buffer
		err := runMountHandbackSeal(args, bytes.NewReader(payload), &first)
		if checkpointExitCode(err) != 4 || !strings.Contains(err.Error(), "handback_result_unknown") || first.Len() != 0 || *ensureCalls != 0 || !lease.released || !committed {
			t.Fatalf("503-after-commit err=%v output=%q restart=%d lease=%v committed=%v", err, first.String(), *ensureCalls, lease.released, committed)
		}
		state, ok, loadErr := loadCheckpointHandbackIfExists(input.HandbackID)
		if loadErr != nil || !ok || state.Status != "handback-unknown" || state.Result != nil {
			t.Fatalf("ambiguous 503 lifecycle=%+v ok=%v err=%v", state, ok, loadErr)
		}
		var retry bytes.Buffer
		if err := runMountHandbackSeal(args, bytes.NewReader(payload), &retry); err != nil {
			t.Fatalf("idempotent handback recovery: %v", err)
		}
		if *ensureCalls != 0 || handbackCalls != 2 || !strings.Contains(retry.String(), `"status": "released"`) {
			t.Fatalf("recovered handback restart=%d calls=%d output=%s", *ensureCalls, handbackCalls, retry.String())
		}
		state, ok, loadErr = loadCheckpointHandbackIfExists(input.HandbackID)
		if loadErr != nil || !ok || state.Status != "released" || state.Result == nil {
			t.Fatalf("recovered lifecycle=%+v ok=%v err=%v", state, ok, loadErr)
		}
	})
}

func TestCheckpointHandbackHTTPFailureClassification(t *testing.T) {
	for _, httpErr := range []*mountsync.HTTPError{
		{StatusCode: 409, Code: "checkpoint_diverged"},
	} {
		if !definitiveCheckpointHandbackHTTPFailure(httpErr) {
			t.Errorf("HTTP %d/%s should be definitive", httpErr.StatusCode, httpErr.Code)
		}
	}
	for _, httpErr := range []*mountsync.HTTPError{
		{StatusCode: 0},
		{StatusCode: 400, Code: "bad_request"},
		{StatusCode: 400, Code: "gateway_bad_request"},
		{StatusCode: 401, Code: "unauthorized"},
		{StatusCode: 403, Code: "forbidden"},
		{StatusCode: 404, Code: "checkpoint_seal_not_found"},
		{StatusCode: 404, Code: "not_found"},
		{StatusCode: 409, Code: "conflict"},
		{StatusCode: 409, Code: "checkpoint_handback_conflict"},
		{StatusCode: 409, Code: "checkpoint_replayed"},
		{StatusCode: 409, Code: "checkpoint_resume_conflict"},
		{StatusCode: 408, Code: "request_timeout"},
		{StatusCode: 425, Code: "too_early"},
		{StatusCode: 429, Code: "rate_limited"},
		{StatusCode: 413, Code: "payload_too_large"},
		{StatusCode: 500, Code: "internal_error"},
		{StatusCode: 502, Code: "bad_gateway"},
		{StatusCode: 503, Code: "upstream_unavailable"},
		{StatusCode: 504, Code: "gateway_timeout"},
	} {
		if definitiveCheckpointHandbackHTTPFailure(httpErr) {
			t.Errorf("HTTP %d/%s should remain ambiguous", httpErr.StatusCode, httpErr.Code)
		}
	}
	if definitiveCheckpointHandbackHTTPFailure(errors.New("connection reset after POST")) {
		t.Error("transport failure should remain ambiguous")
	}
}

func TestCheckpointAbortAndImmediateResumeSerializeAndRecover(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	active, receipt := checkpointTestActive(t)
	_, ensureCalls, burnCalls := installCheckpointLifecycleSeams(t, active, receipt)
	issueStarted := make(chan struct{})
	releaseIssue := make(chan struct{})
	checkpointIssueStopped = func(context.Context, checkpointMountConfig, string, uint64, int) (mountsync.CheckpointSeal, error) {
		close(issueStarted)
		<-releaseIssue
		return mountsync.CheckpointSeal{}, context.Canceled
	}
	checkpointArgs := []string{"--root", active.config.LocalRoot, "--lifecycle-id", "rsm_abort_resume", "--session", "session-abort", "--generation", "4", "--json"}
	checkpointDone := make(chan error, 1)
	go func() { checkpointDone <- runMountCheckpointSeal(checkpointArgs, &bytes.Buffer{}) }()
	<-issueStarted
	resumeDone := make(chan error, 1)
	go func() {
		resumeDone <- runMountResumeSeal([]string{"--root", active.config.LocalRoot, "--timeout", "5s", "--json"}, strings.NewReader(`{"resumeId":"rsm_abort_resume"}`), &bytes.Buffer{})
	}()
	select {
	case err := <-resumeDone:
		t.Fatalf("resume overtook in-flight checkpoint: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseIssue)
	if err := <-checkpointDone; checkpointExitCode(err) != 5 {
		t.Fatalf("aborted checkpoint error = %v", err)
	}
	if err := <-resumeDone; err != nil {
		t.Fatalf("immediate resume: %v", err)
	}
	if *ensureCalls < 2 || *burnCalls != 0 {
		t.Fatalf("abort recovery calls ensure=%d burn=%d", *ensureCalls, *burnCalls)
	}
}

func TestCheckpointRestartContractRejectsFUSEAndNeverPersistsTokens(t *testing.T) {
	active, _ := checkpointTestActive(t)
	active.config.Mode = "fuse"
	err := validateCheckpointMountConfig(active.config, active.record, active.config.LocalRoot)
	if checkpointExitCode(err) != 2 || !strings.Contains(err.Error(), "checkpoint_fuse_unsupported") {
		t.Fatalf("fuse validation error = %v", err)
	}
	active.config.Mode = defaultMountMode
	args := checkpointMountArgs(active.config)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--token") || strings.Contains(joined, "one-use-token") {
		t.Fatalf("restart argv contains bearer material: %s", joined)
	}
	env := checkpointSubprocessEnv([]string{"PATH=/bin", "RELAYFILE_TOKEN=secret", "RELAYFILE_MOUNT_MODE=fuse"})
	if strings.Join(env, " ") != "PATH=/bin" {
		t.Fatalf("restart environment retained unsafe overrides: %v", env)
	}
}

func TestCheckpointRestartContractRejectsScopedRootBeforeStop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	active, receipt := checkpointTestActive(t)
	active.config.RemotePaths = []string{"/sessions"}
	active.record.RemotePaths = []string{"/sessions"}
	receipt.Root = "/sessions"
	stopCalls := 0
	originalResolve := checkpointResolveActive
	originalStop := checkpointStopActive
	checkpointResolveActive = func(string) (activeCheckpointMount, error) {
		if err := validateCheckpointMountConfig(active.config, active.record, active.config.LocalRoot); err != nil {
			return activeCheckpointMount{}, err
		}
		return active, nil
	}
	checkpointStopActive = func(context.Context, activeCheckpointMount) (checkpointLease, error) {
		stopCalls++
		return &fakeCheckpointLease{}, nil
	}
	t.Cleanup(func() {
		checkpointResolveActive = originalResolve
		checkpointStopActive = originalStop
	})
	err := runMountCheckpointSeal([]string{"--root", active.config.LocalRoot, "--lifecycle-id", "rsm_controller_scoped", "--session", receipt.SessionID, "--generation", "2", "--json"}, &bytes.Buffer{})
	if checkpointExitCode(err) != 2 || !strings.Contains(err.Error(), "checkpoint_topology_unsupported") || stopCalls != 0 {
		t.Fatalf("scoped root err=%v stopCalls=%d", err, stopCalls)
	}
}

func TestStopCheckpointMountSignalsRealProcessAndWaitsForLease(t *testing.T) {
	if os.Getenv("RELAYFILE_CHECKPOINT_HELPER") == "1" {
		t.Skip("helper branch is handled by TestCheckpointLifecycleHelperProcess")
	}
	root := t.TempDir()
	server := "https://relayfile.stop.test"
	workspace := "ws_stop_real"
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readyReader.Close()
	cmd := exec.Command(os.Args[0], "-test.run=TestCheckpointLifecycleHelperProcess")
	cmd.ExtraFiles = []*os.File{readyWriter}
	cmd.Env = append(os.Environ(),
		"RELAYFILE_CHECKPOINT_HELPER=1",
		"RELAYFILE_CHECKPOINT_HELPER_SERVER="+server,
		"RELAYFILE_CHECKPOINT_HELPER_WORKSPACE="+workspace,
		"RELAYFILE_CHECKPOINT_HELPER_ROOT="+root,
	)
	if err := cmd.Start(); err != nil {
		_ = readyWriter.Close()
		t.Fatal(err)
	}
	_ = readyWriter.Close()
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
		}
	}()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	if err := readyReader.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	ready := make([]byte, 1)
	if n, err := readyReader.Read(ready); err != nil || n != 1 || ready[0] != 1 {
		t.Fatalf("helper readiness n=%d byte=%v err=%v", n, ready, err)
	}
	if lease, err := mountlease.Acquire(server, workspace, root); !errors.Is(err, mountlease.ErrHeld) {
		if err == nil {
			_ = lease.Release()
		}
		t.Fatalf("helper signaled ready without holding mount lease: %v", err)
	}
	active := activeCheckpointMount{pid: daemonPIDState{PID: cmd.Process.Pid}, config: checkpointMountConfig{Server: server, WorkspaceID: workspace, LocalRoot: root}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	lease, err := stopCheckpointMount(ctx, active)
	cancel()
	if err != nil {
		t.Fatalf("stop real daemon: %v", err)
	}
	_ = lease.Release()
	if err := <-waited; err != nil {
		t.Fatalf("helper exit: %v", err)
	}
}

func TestCheckpointLifecycleHelperProcess(t *testing.T) {
	if os.Getenv("RELAYFILE_CHECKPOINT_HELPER") != "1" {
		return
	}
	signals := make(chan os.Signal, 1)
	signalNotify(signals)
	defer signal.Stop(signals)
	lease, err := mountlease.Acquire(
		os.Getenv("RELAYFILE_CHECKPOINT_HELPER_SERVER"),
		os.Getenv("RELAYFILE_CHECKPOINT_HELPER_WORKSPACE"),
		os.Getenv("RELAYFILE_CHECKPOINT_HELPER_ROOT"),
	)
	if err != nil {
		os.Exit(41)
	}
	readyWriter := os.NewFile(uintptr(3), "checkpoint-helper-ready")
	if readyWriter == nil {
		_ = lease.Release()
		os.Exit(42)
	}
	if _, err := readyWriter.Write([]byte{1}); err != nil {
		_ = readyWriter.Close()
		_ = lease.Release()
		os.Exit(43)
	}
	_ = readyWriter.Close()
	<-signals
	_ = lease.Release()
}

func signalNotify(ch chan<- os.Signal) {
	// Kept behind a helper so the real-process test remains small and the
	// production signal path is still the one exercised by stopCheckpointMount.
	signal.Notify(ch, syscall.SIGTERM)
}

func checkpointExitCode(err error) int {
	if err == nil {
		return 0
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return 1
}
