package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountlease"
	"github.com/agentworkforce/relayfile/internal/mountscope"
	"github.com/agentworkforce/relayfile/internal/mountsync"
)

const (
	checkpointLifecycleVersion = 1
	checkpointStopTimeout      = 10 * time.Second
	checkpointResumeTimeout    = 60 * time.Second
)

var (
	checkpointLifecycleIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
	checkpointDigestPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	checkpointRevisionPattern    = regexp.MustCompile(`^(?:0|rev_[0-9]+)$`)
	checkpointCursorPattern      = regexp.MustCompile(`^(?:0|evt_[0-9]+)$`)
)

type checkpointCLIError struct {
	code     string
	exitCode int
	err      error
}

func (e *checkpointCLIError) Error() string { return e.code + ": " + e.err.Error() }
func (e *checkpointCLIError) Unwrap() error { return e.err }
func (e *checkpointCLIError) ExitCode() int { return e.exitCode }

func checkpointError(code string, exitCode int, err error) error {
	if err == nil {
		err = errors.New(code)
	}
	return &checkpointCLIError{code: code, exitCode: exitCode, err: err}
}

type checkpointMountConfig struct {
	Version                   int      `json:"version"`
	Server                    string   `json:"server"`
	CredentialsFile           string   `json:"credentialsFile"`
	WorkspaceID               string   `json:"workspaceId"`
	LocalRoot                 string   `json:"localRoot"`
	RemotePaths               []string `json:"remotePaths"`
	LocalLayout               string   `json:"localLayout"`
	EventProvider             string   `json:"eventProvider,omitempty"`
	StateFile                 string   `json:"stateFile,omitempty"`
	StateDir                  string   `json:"stateDir"`
	MountKind                 string   `json:"mountKind"`
	Mode                      string   `json:"mode"`
	Interval                  string   `json:"interval"`
	IntervalJitter            float64  `json:"intervalJitter"`
	Timeout                   string   `json:"timeout"`
	BootstrapTimeout          string   `json:"bootstrapTimeout"`
	BootstrapMaxFilesPerCycle int      `json:"bootstrapMaxFilesPerCycle"`
	FullPullMinInterval       string   `json:"fullPullMinInterval"`
	CursorTimeout             string   `json:"cursorTimeout"`
	ForceFullReconcile        bool     `json:"forceFullReconcile"`
	WebsocketEnabled          bool     `json:"websocketEnabled"`
	LowMemory                 bool     `json:"lowMemory"`
	PprofAddr                 string   `json:"pprofAddr,omitempty"`
	MemlogInterval            string   `json:"memlogInterval"`
}

type checkpointLifecycleState struct {
	Version     int                                `json:"version"`
	Kind        string                             `json:"kind"`
	ResumeID    string                             `json:"resumeId"`
	WorkspaceID string                             `json:"workspaceId"`
	LocalRoot   string                             `json:"localRoot"`
	RemoteRoot  string                             `json:"remoteRoot"`
	SessionID   string                             `json:"sessionId"`
	Generation  uint64                             `json:"generation"`
	Status      string                             `json:"status"`
	Config      checkpointMountConfig              `json:"config"`
	Receipt     *mountsync.CheckpointSeal          `json:"receipt,omitempty"`
	ResumeProof *mountsync.CheckpointSealOwnership `json:"resumeProof,omitempty"`
	CreatedAt   string                             `json:"createdAt"`
	UpdatedAt   string                             `json:"updatedAt"`
	SealedAt    string                             `json:"sealedAt,omitempty"`
	ResumedAt   string                             `json:"resumedAt,omitempty"`
	LastError   string                             `json:"lastError,omitempty"`
}

type checkpointSealEnvelope struct {
	Version     int                                    `json:"version"`
	Kind        string                                 `json:"kind"`
	Status      string                                 `json:"status"`
	WorkspaceID string                                 `json:"workspaceId"`
	LocalRoot   string                                 `json:"localRoot"`
	SessionID   string                                 `json:"sessionId"`
	Generation  uint64                                 `json:"generation"`
	Receipt     mountsync.CheckpointSeal               `json:"receipt"`
	Health      mountsync.CheckpointVerificationHealth `json:"health"`
	ResumeID    string                                 `json:"resumeId"`
	SealedAt    string                                 `json:"sealedAt"`
}

type checkpointResumeEnvelope struct {
	Version     int    `json:"version"`
	Kind        string `json:"kind"`
	WorkspaceID string `json:"workspaceId"`
	LocalRoot   string `json:"localRoot"`
	ResumeID    string `json:"resumeId"`
	Status      string `json:"status"`
	ResumedAt   string `json:"resumedAt"`
}

type checkpointResumeInput struct {
	ResumeID string `json:"resumeId"`
}

type checkpointVerificationInput struct {
	VerificationID string                   `json:"verificationId"`
	Receipt        mountsync.CheckpointSeal `json:"receipt"`
}

type checkpointDestinationVerificationEnvelope struct {
	Version        int                                    `json:"version"`
	Kind           string                                 `json:"kind"`
	VerificationID string                                 `json:"verificationId"`
	WorkspaceID    string                                 `json:"workspaceId"`
	LocalRoot      string                                 `json:"localRoot"`
	RemoteRoot     string                                 `json:"remoteRoot"`
	SessionID      string                                 `json:"sessionId"`
	Generation     uint64                                 `json:"generation"`
	Status         string                                 `json:"status"`
	Observed       mountsync.CheckpointObservedState      `json:"observed"`
	Health         mountsync.CheckpointVerificationHealth `json:"health"`
	VerifiedAt     string                                 `json:"verifiedAt"`
}

type checkpointHandbackInput struct {
	HandbackID             string                   `json:"handbackId"`
	ConsumerIdempotencyKey string                   `json:"consumerIdempotencyKey"`
	Receipt                mountsync.CheckpointSeal `json:"receipt"`
}

type checkpointHandbackEnvelope struct {
	Version     int                                    `json:"version"`
	Kind        string                                 `json:"kind"`
	HandbackID  string                                 `json:"handbackId"`
	WorkspaceID string                                 `json:"workspaceId"`
	LocalRoot   string                                 `json:"localRoot"`
	RemoteRoot  string                                 `json:"remoteRoot"`
	SessionID   string                                 `json:"sessionId"`
	Generation  uint64                                 `json:"generation"`
	Status      string                                 `json:"status"`
	Proof       mountsync.CheckpointSealOwnership      `json:"proof"`
	Health      mountsync.CheckpointVerificationHealth `json:"health"`
	ReleasedAt  string                                 `json:"releasedAt"`
}

type checkpointHandbackLifecycle struct {
	Version                int                         `json:"version"`
	Kind                   string                      `json:"kind"`
	HandbackID             string                      `json:"handbackId"`
	ConsumerIdempotencyKey string                      `json:"consumerIdempotencyKey"`
	WorkspaceID            string                      `json:"workspaceId"`
	LocalRoot              string                      `json:"localRoot"`
	RemoteRoot             string                      `json:"remoteRoot"`
	SessionID              string                      `json:"sessionId"`
	Generation             uint64                      `json:"generation"`
	Status                 string                      `json:"status"`
	Config                 checkpointMountConfig       `json:"config"`
	Receipt                mountsync.CheckpointSeal    `json:"receipt"`
	Result                 *checkpointHandbackEnvelope `json:"result,omitempty"`
	CreatedAt              string                      `json:"createdAt"`
	UpdatedAt              string                      `json:"updatedAt"`
	LastError              string                      `json:"lastError,omitempty"`
}

type checkpointVerificationLifecycle struct {
	Version        int                                        `json:"version"`
	Kind           string                                     `json:"kind"`
	VerificationID string                                     `json:"verificationId"`
	WorkspaceID    string                                     `json:"workspaceId"`
	LocalRoot      string                                     `json:"localRoot"`
	RemoteRoot     string                                     `json:"remoteRoot"`
	SessionID      string                                     `json:"sessionId"`
	Generation     uint64                                     `json:"generation"`
	Status         string                                     `json:"status"`
	Config         checkpointMountConfig                      `json:"config"`
	Receipt        mountsync.CheckpointSeal                   `json:"receipt"`
	Result         *checkpointDestinationVerificationEnvelope `json:"result,omitempty"`
	CreatedAt      string                                     `json:"createdAt"`
	UpdatedAt      string                                     `json:"updatedAt"`
	LastError      string                                     `json:"lastError,omitempty"`
}

type activeCheckpointMount struct {
	record workspaceRecord
	pid    daemonPIDState
	config checkpointMountConfig
}

type checkpointLease interface {
	Release() error
}

var (
	checkpointStartMount      = startCheckpointMountProcess
	checkpointResolveActive   = resolveActiveCheckpointMount
	checkpointStopActive      = stopCheckpointMount
	checkpointIssueStopped    = issueCheckpointForStoppedMount
	checkpointVerifyStopped   = verifyCheckpointForStoppedMount
	checkpointHandbackStopped = handbackCheckpointForStoppedMount
	checkpointEnsureSource    = ensureCheckpointSourceReady
	checkpointBurnReceipt     = burnCheckpointReceiptForResume
	checkpointWaitMountReady  = waitCheckpointMountReady
	checkpointWaitSourceProof = waitCheckpointSourceProof
)

func runMountCheckpointSeal(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("mount checkpoint-seal", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	localRoot := fs.String("root", "", "absolute local mount root")
	lifecycleID := fs.String("lifecycle-id", "", "stable controller-persisted cutover lifecycle id")
	sessionID := fs.String("session", "", "live session identifier")
	generation := fs.Uint64("generation", 0, "strictly increasing migration generation")
	timeout := fs.Duration("timeout", 30*time.Second, "checkpoint deadline")
	ttl := fs.Duration("ttl", mountsync.DefaultCheckpointSealTTL, "server receipt TTL")
	jsonOutput := fs.Bool("json", false, "emit the machine contract")
	if err := fs.Parse(args); err != nil {
		return checkpointError("checkpoint_invalid_input", 2, err)
	}
	root, err := normalizeAbsoluteLocalRoot(*localRoot)
	if err != nil || !checkpointLifecycleIDPattern.MatchString(strings.TrimSpace(*lifecycleID)) || !checkpointLifecycleIDPattern.MatchString(strings.TrimSpace(*sessionID)) || *generation == 0 || *timeout <= 0 || *ttl < time.Second || *ttl > mountsync.MaxCheckpointSealTTL || !*jsonOutput || fs.NArg() != 0 {
		return checkpointError("checkpoint_invalid_input", 2, errors.New("--root, --lifecycle-id, --session, --generation, and --json are required; timeout/ttl must be positive and ttl <= 5m"))
	}
	release, err := acquireCheckpointLifecycleLock(root)
	if err != nil {
		return checkpointError("checkpoint_lifecycle_conflict", 3, err)
	}
	defer release()
	lifecycleCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	controllerLifecycleID := strings.TrimSpace(*lifecycleID)
	if existing, ok, err := loadCheckpointLifecycleIfExists(controllerLifecycleID); err != nil {
		return checkpointError("checkpoint_lifecycle_state_invalid", 3, err)
	} else if ok {
		if !sameCheckpointLocalRoot(existing.LocalRoot, root) || existing.SessionID != strings.TrimSpace(*sessionID) || existing.Generation != *generation {
			return checkpointError("checkpoint_lifecycle_identity_conflict", 3, errors.New("lifecycle-id is already bound to a different root/session/generation"))
		}
		if existing.Status == "sealed" && existing.Receipt != nil {
			return writeJSON(stdout, checkpointEnvelopeFromState(existing))
		}
		if existing.Status == "preparing" || existing.Status == "stopped" {
			recoveryErr := checkpointEnsureSource(existing.Config, checkpointResumeTimeout)
			existing.LastError = "recovered interrupted pre-seal lifecycle"
			if recoveryErr != nil {
				existing.Status = "recovery-failed"
				existing.LastError += ": " + recoveryErr.Error()
				_ = saveCheckpointLifecycle(existing)
				return checkpointError("checkpoint_source_recovery_failed", 4, errors.New(existing.LastError))
			}
			existing.Status = "preseal-failed-source-ready"
			existing.ResumedAt = time.Now().UTC().Format(time.RFC3339Nano)
			_ = saveCheckpointLifecycle(existing)
			return checkpointError("checkpoint_lifecycle_interrupted", 3, errors.New("interrupted pre-seal attempt was recovered; use a newer generation and lifecycle-id"))
		}
		return checkpointError("checkpoint_lifecycle_terminal", 3, fmt.Errorf("lifecycle-id already has terminal state %q", existing.Status))
	}
	if existing, ok, err := findCheckpointLifecycle(root, strings.TrimSpace(*sessionID), *generation); err != nil {
		return checkpointError("checkpoint_lifecycle_state_invalid", 3, err)
	} else if ok {
		return checkpointError("checkpoint_lifecycle_identity_conflict", 3, fmt.Errorf("root/session/generation is already bound to lifecycle-id %q", existing.ResumeID))
	}
	if err := rejectStaleCheckpointGeneration(root, strings.TrimSpace(*sessionID), *generation); err != nil {
		return checkpointError("checkpoint_generation_stale", 3, err)
	}

	active, err := checkpointResolveActive(root)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	state := checkpointLifecycleState{
		Version: checkpointLifecycleVersion, Kind: "relayfile-checkpoint-lifecycle",
		ResumeID: controllerLifecycleID, WorkspaceID: active.config.WorkspaceID,
		LocalRoot: root, RemoteRoot: active.config.RemotePaths[0],
		SessionID: strings.TrimSpace(*sessionID), Generation: *generation,
		Status: "preparing", Config: active.config,
		CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano),
	}
	if err := saveCheckpointLifecycle(state); err != nil {
		return checkpointError("checkpoint_lifecycle_state_failed", 4, err)
	}

	stopCtx, stopCancel := context.WithTimeout(lifecycleCtx, checkpointStopTimeout)
	lease, stopErr := checkpointStopActive(stopCtx, active)
	stopCancel()
	if stopErr != nil {
		state.LastError = stopErr.Error()
		if recoveryErr := checkpointEnsureSource(state.Config, checkpointResumeTimeout); recoveryErr != nil {
			state.Status = "recovery-failed"
			state.LastError += "; source recovery: " + recoveryErr.Error()
			_ = saveCheckpointLifecycle(state)
			return checkpointError("checkpoint_source_recovery_failed", 4, errors.New(state.LastError))
		}
		state.Status = "preseal-failed-source-ready"
		state.ResumedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_ = saveCheckpointLifecycle(state)
		return checkpointError("checkpoint_daemon_stop_failed", 4, stopErr)
	}
	state.Status = "stopped"
	_ = saveCheckpointLifecycle(state)

	checkpointCtx, checkpointCancel := context.WithTimeout(lifecycleCtx, *timeout)
	receipt, checkpointErr := checkpointIssueStopped(checkpointCtx, state.Config, state.SessionID, state.Generation, int(ttl.Seconds()))
	checkpointCancel()
	_ = lease.Release()
	if checkpointErr != nil {
		recoveryErr := checkpointEnsureSource(state.Config, checkpointResumeTimeout)
		state.LastError = checkpointErr.Error()
		if recoveryErr != nil {
			state.Status = "recovery-failed"
			state.LastError += "; source recovery: " + recoveryErr.Error()
			_ = saveCheckpointLifecycle(state)
			return checkpointError("checkpoint_source_recovery_failed", 4, errors.New(state.LastError))
		}
		state.Status = "preseal-failed-source-ready"
		state.ResumedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_ = saveCheckpointLifecycle(state)
		return checkpointError("checkpoint_nonconverged", 5, checkpointErr)
	}
	state.Status = "sealed"
	state.Receipt = &receipt
	state.SealedAt = time.Now().UTC().Format(time.RFC3339Nano)
	state.UpdatedAt = state.SealedAt
	if err := saveCheckpointLifecycle(state); err != nil {
		// A server seal exists but was not durably handed off. Restarting the
		// source makes that undisclosed receipt stale before returning failure.
		recoveryErr := checkpointEnsureSource(state.Config, checkpointResumeTimeout)
		if recoveryErr != nil {
			return checkpointError("checkpoint_source_recovery_failed", 4, fmt.Errorf("persist sealed lifecycle: %v; source recovery: %w", err, recoveryErr))
		}
		return checkpointError("checkpoint_lifecycle_state_failed", 4, err)
	}
	return writeJSON(stdout, checkpointEnvelopeFromState(state))
}

func runMountResumeSeal(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("mount resume-seal", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	localRoot := fs.String("root", "", "absolute local mount root")
	timeout := fs.Duration("timeout", checkpointResumeTimeout, "resume readiness deadline")
	jsonOutput := fs.Bool("json", false, "emit the machine contract")
	if err := fs.Parse(args); err != nil {
		return checkpointError("resume_invalid_input", 2, err)
	}
	root, err := normalizeAbsoluteLocalRoot(*localRoot)
	if err != nil || *timeout <= 0 || !*jsonOutput || fs.NArg() != 0 {
		return checkpointError("resume_invalid_input", 2, errors.New("--root and --json are required and timeout must be positive"))
	}
	var input checkpointResumeInput
	decoder := json.NewDecoder(io.LimitReader(stdin, 16*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(&struct{}{}) != io.EOF || !checkpointLifecycleIDPattern.MatchString(strings.TrimSpace(input.ResumeID)) {
		return checkpointError("resume_invalid_input", 2, errors.New("stdin must contain exactly one JSON object with a valid resumeId"))
	}
	lockCtx, lockCancel := context.WithTimeout(context.Background(), *timeout)
	release, err := acquireCheckpointLifecycleLockWait(lockCtx, root)
	lockCancel()
	if err != nil {
		return checkpointError("checkpoint_lifecycle_conflict", 3, err)
	}
	defer release()
	state, err := loadCheckpointLifecycle(strings.TrimSpace(input.ResumeID))
	if err != nil {
		return checkpointError("resume_not_found", 3, err)
	}
	if !sameCheckpointLocalRoot(state.LocalRoot, root) {
		return checkpointError("resume_root_mismatch", 2, errors.New("resumeId is not bound to the requested local root"))
	}
	if state.Status == "ready" {
		if err := checkpointWaitMountReady(state.Config, *timeout); err != nil {
			return checkpointError("resume_readiness_failed", 4, err)
		}
		return writeJSON(stdout, resumeEnvelopeFromState(state))
	}
	if state.Status == "preparing" || state.Status == "stopped" || state.Status == "preseal-failed-source-ready" {
		if err := checkpointEnsureSource(state.Config, *timeout); err != nil {
			state.Status = "recovery-failed"
			state.LastError = err.Error()
			_ = saveCheckpointLifecycle(state)
			return checkpointError("resume_readiness_failed", 4, err)
		}
		state.Status = "ready"
		state.ResumedAt = time.Now().UTC().Format(time.RFC3339Nano)
		state.LastError = ""
		if err := saveCheckpointLifecycle(state); err != nil {
			return checkpointError("resume_lifecycle_state_failed", 4, err)
		}
		return writeJSON(stdout, resumeEnvelopeFromState(state))
	}
	if state.Status != "sealed" && state.Status != "resuming" {
		return checkpointError("resume_lifecycle_terminal", 3, fmt.Errorf("lifecycle state %q cannot be resumed", state.Status))
	}
	if state.Receipt == nil || state.Receipt.SealToken == "" || state.Receipt.Root != state.RemoteRoot {
		return checkpointError("resume_lifecycle_state_invalid", 3, errors.New("sealed lifecycle is missing its remote receipt"))
	}
	state.Status = "resuming"
	if err := saveCheckpointLifecycle(state); err != nil {
		return checkpointError("resume_lifecycle_state_failed", 4, err)
	}
	var proof mountsync.CheckpointSealOwnership
	if state.ResumeProof != nil {
		proof = *state.ResumeProof
	} else {
		proof, err = checkpointBurnReceipt(state, *timeout)
		if err != nil {
			var httpErr *mountsync.HTTPError
			if errors.As(err, &httpErr) && httpErr.Code == "checkpoint_handback_required" {
				state.Status = "sealed"
				state.LastError = err.Error()
				_ = saveCheckpointLifecycle(state)
				return checkpointError("resume_handback_required", 3, err)
			}
			if errors.As(err, &httpErr) && (httpErr.Code == "checkpoint_resume_conflict" || httpErr.Code == "checkpoint_replayed") {
				state.Status = "ownership-conflict"
				state.LastError = err.Error()
				_ = saveCheckpointLifecycle(state)
				return checkpointError("resume_ownership_conflict", 3, err)
			}
			state.LastError = err.Error()
			_ = saveCheckpointLifecycle(state)
			return checkpointError("resume_receipt_burn_failed", 4, err)
		}
		if err := validateSourceResumeProof(state, proof); err != nil {
			state.Status = "ownership-conflict"
			state.LastError = err.Error()
			_ = saveCheckpointLifecycle(state)
			return checkpointError("resume_ownership_conflict", 3, err)
		}
		state.ResumeProof = &proof
		state.LastError = ""
		if err := saveCheckpointLifecycle(state); err != nil {
			return checkpointError("resume_lifecycle_state_failed", 4, fmt.Errorf("persist source-resume proof before restart: %w", err))
		}
	}
	if err := checkpointEnsureSource(state.Config, *timeout); err != nil {
		state.LastError = err.Error()
		_ = saveCheckpointLifecycle(state)
		return checkpointError("resume_readiness_failed", 4, err)
	}
	if err := checkpointWaitSourceProof(state.Config, proof, *timeout); err != nil {
		state.LastError = err.Error()
		_ = saveCheckpointLifecycle(state)
		return checkpointError("resume_nonconverged", 5, err)
	}
	state.Status = "ready"
	state.ResumedAt = time.Now().UTC().Format(time.RFC3339Nano)
	state.UpdatedAt = state.ResumedAt
	state.LastError = ""
	state.Receipt.SealToken = ""
	if err := saveCheckpointLifecycle(state); err != nil {
		return checkpointError("resume_lifecycle_state_failed", 4, err)
	}
	return writeJSON(stdout, resumeEnvelopeFromState(state))
}

func runMountVerifySeal(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("mount verify-seal", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	localRoot := fs.String("root", "", "absolute local destination mount root")
	timeout := fs.Duration("timeout", checkpointResumeTimeout, "verification and recovery deadline")
	jsonOutput := fs.Bool("json", false, "emit the machine contract")
	if err := fs.Parse(args); err != nil {
		return checkpointError("verification_invalid_input", 2, err)
	}
	root, err := normalizeAbsoluteLocalRoot(*localRoot)
	if err != nil || *timeout <= 0 || !*jsonOutput || fs.NArg() != 0 {
		return checkpointError("verification_invalid_input", 2, errors.New("--root and --json are required and timeout must be positive"))
	}
	var input checkpointVerificationInput
	decoder := json.NewDecoder(io.LimitReader(stdin, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(&struct{}{}) != io.EOF || !checkpointLifecycleIDPattern.MatchString(strings.TrimSpace(input.VerificationID)) {
		return checkpointError("verification_invalid_input", 2, errors.New("stdin must contain only a valid verificationId and consumed receipt"))
	}
	input.VerificationID = strings.TrimSpace(input.VerificationID)
	if err := validateDestinationReceipt(input.Receipt); err != nil {
		return checkpointError("verification_invalid_input", 2, err)
	}

	lockCtx, lockCancel := context.WithTimeout(context.Background(), *timeout)
	release, err := acquireCheckpointLifecycleLockWait(lockCtx, root)
	lockCancel()
	if err != nil {
		return checkpointError("verification_lifecycle_conflict", 3, err)
	}
	defer release()

	if existing, ok, err := loadCheckpointVerificationIfExists(input.VerificationID); err != nil {
		return checkpointError("verification_lifecycle_state_invalid", 3, err)
	} else if ok {
		if !sameCheckpointLocalRoot(existing.LocalRoot, root) || !sameDestinationReceipt(existing.Receipt, input.Receipt) {
			return checkpointError("verification_identity_conflict", 3, errors.New("verificationId is already bound to another root or receipt"))
		}
		switch existing.Status {
		case "converged":
			if existing.Result == nil {
				return checkpointError("verification_lifecycle_state_invalid", 3, errors.New("converged verification has no result"))
			}
			if err := checkpointWaitMountReady(existing.Config, *timeout); err != nil {
				return checkpointError("verification_recovery_failed", 4, err)
			}
			return writeJSON(stdout, *existing.Result)
		case "verified":
			if existing.Result == nil {
				return checkpointError("verification_lifecycle_state_invalid", 3, errors.New("verified lifecycle has no result"))
			}
			if err := checkpointEnsureSource(existing.Config, *timeout); err != nil {
				existing.Status = "recovery-failed"
				existing.LastError = err.Error()
				_ = saveCheckpointVerification(existing)
				return checkpointError("verification_recovery_failed", 4, err)
			}
			existing.Status = "converged"
			existing.LastError = ""
			if err := saveCheckpointVerification(existing); err != nil {
				return checkpointError("verification_lifecycle_state_failed", 4, err)
			}
			return writeJSON(stdout, *existing.Result)
		case "preparing":
			if err := checkpointEnsureSource(existing.Config, *timeout); err != nil {
				return checkpointError("verification_recovery_failed", 4, err)
			}
		case "stopped", "verifying":
			return continueStoppedDestinationVerification(existing, *timeout, stdout)
		case "diverged-source-ready":
			return checkpointError("verification_nonconverged", 5, errors.New(existing.LastError))
		default:
			return checkpointError("verification_lifecycle_terminal", 3, fmt.Errorf("verificationId has state %q", existing.Status))
		}
	}

	active, err := checkpointResolveActive(root)
	if err != nil {
		return err
	}
	if active.config.WorkspaceID != input.Receipt.WorkspaceID || active.config.RemotePaths[0] != input.Receipt.Root {
		return checkpointError("verification_identity_conflict", 3, errors.New("active destination mount does not match consumed receipt"))
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	state := checkpointVerificationLifecycle{
		Version: checkpointLifecycleVersion, Kind: "relayfile-destination-verification-lifecycle",
		VerificationID: input.VerificationID, WorkspaceID: input.Receipt.WorkspaceID,
		LocalRoot: root, RemoteRoot: input.Receipt.Root, SessionID: input.Receipt.SessionID, Generation: input.Receipt.Generation,
		Status: "preparing", Config: active.config, Receipt: input.Receipt, CreatedAt: now, UpdatedAt: now,
	}
	if err := saveCheckpointVerification(state); err != nil {
		return checkpointError("verification_lifecycle_state_failed", 4, err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), checkpointStopTimeout)
	lease, stopErr := checkpointStopActive(stopCtx, active)
	stopCancel()
	if stopErr != nil {
		return recoverDestinationAfterVerificationFailure(state, "verification_daemon_stop_failed", 4, stopErr, *timeout)
	}
	state.Status = "stopped"
	if err := saveCheckpointVerification(state); err != nil {
		_ = lease.Release()
		return recoverDestinationAfterVerificationFailure(state, "verification_lifecycle_state_failed", 4, err, *timeout)
	}
	return verifyAndRecoverDestination(state, lease, *timeout, stdout)
}

func runMountHandbackSeal(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("mount handback-seal", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	localRoot := fs.String("root", "", "absolute local destination mount root")
	timeout := fs.Duration("timeout", checkpointResumeTimeout, "final drain and handback deadline")
	jsonOutput := fs.Bool("json", false, "emit the machine contract")
	if err := fs.Parse(args); err != nil {
		return checkpointError("handback_invalid_input", 2, err)
	}
	root, err := normalizeAbsoluteLocalRoot(*localRoot)
	if err != nil || *timeout <= 0 || !*jsonOutput || fs.NArg() != 0 {
		return checkpointError("handback_invalid_input", 2, errors.New("--root and --json are required and timeout must be positive"))
	}
	var input checkpointHandbackInput
	decoder := json.NewDecoder(io.LimitReader(stdin, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		!checkpointLifecycleIDPattern.MatchString(strings.TrimSpace(input.HandbackID)) || !checkpointLifecycleIDPattern.MatchString(strings.TrimSpace(input.ConsumerIdempotencyKey)) || validateDestinationReceipt(input.Receipt) != nil {
		return checkpointError("handback_invalid_input", 2, errors.New("stdin must contain only handbackId, original consumerIdempotencyKey, and a consumed full-root receipt"))
	}
	input.HandbackID = strings.TrimSpace(input.HandbackID)
	input.ConsumerIdempotencyKey = strings.TrimSpace(input.ConsumerIdempotencyKey)
	lockCtx, lockCancel := context.WithTimeout(context.Background(), *timeout)
	release, err := acquireCheckpointLifecycleLockWait(lockCtx, root)
	lockCancel()
	if err != nil {
		return checkpointError("handback_lifecycle_conflict", 3, err)
	}
	defer release()

	if existing, ok, err := loadCheckpointHandbackIfExists(input.HandbackID); err != nil {
		return checkpointError("handback_lifecycle_state_invalid", 3, err)
	} else if ok {
		if !sameCheckpointLocalRoot(existing.LocalRoot, root) || existing.ConsumerIdempotencyKey != input.ConsumerIdempotencyKey || !sameDestinationReceipt(existing.Receipt, input.Receipt) {
			return checkpointError("handback_identity_conflict", 3, errors.New("handbackId is already bound to another root, consumer, or receipt"))
		}
		if existing.Status == "released" {
			if existing.Result == nil {
				return checkpointError("handback_lifecycle_state_invalid", 3, errors.New("released handback has no result"))
			}
			return writeJSON(stdout, *existing.Result)
		}
		if existing.Status == "failed-destination-ready" {
			return checkpointError("handback_nonconverged", 5, errors.New(existing.LastError))
		}
		if existing.Status != "preparing" && existing.Status != "stopped" && existing.Status != "handing-back" && existing.Status != "handback-unknown" {
			return checkpointError("handback_lifecycle_terminal", 3, fmt.Errorf("handbackId has state %q", existing.Status))
		}
		lease, err := acquireStoppedHandbackLease(existing)
		if err != nil {
			return checkpointError("handback_destination_stop_failed", 4, err)
		}
		existing.Status = "stopped"
		if err := saveCheckpointHandback(existing); err != nil {
			_ = lease.Release()
			return checkpointError("handback_lifecycle_state_failed", 4, err)
		}
		return finishCheckpointHandback(existing, lease, *timeout, stdout)
	}

	active, err := checkpointResolveActive(root)
	if err != nil {
		return err
	}
	if active.config.WorkspaceID != input.Receipt.WorkspaceID || active.config.RemotePaths[0] != input.Receipt.Root {
		return checkpointError("handback_identity_conflict", 3, errors.New("active destination mount does not match consumed receipt"))
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	state := checkpointHandbackLifecycle{
		Version: checkpointLifecycleVersion, Kind: "relayfile-checkpoint-handback-lifecycle",
		HandbackID: input.HandbackID, ConsumerIdempotencyKey: input.ConsumerIdempotencyKey,
		WorkspaceID: input.Receipt.WorkspaceID, LocalRoot: root, RemoteRoot: input.Receipt.Root,
		SessionID: input.Receipt.SessionID, Generation: input.Receipt.Generation,
		Status: "preparing", Config: active.config, Receipt: input.Receipt, CreatedAt: now, UpdatedAt: now,
	}
	if err := saveCheckpointHandback(state); err != nil {
		return checkpointError("handback_lifecycle_state_failed", 4, err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), checkpointStopTimeout)
	lease, stopErr := checkpointStopActive(stopCtx, active)
	stopCancel()
	if stopErr != nil {
		state.LastError = stopErr.Error()
		if recoveryErr := checkpointEnsureSource(state.Config, *timeout); recoveryErr != nil {
			state.Status = "recovery-failed"
			state.LastError += "; destination recovery: " + recoveryErr.Error()
			_ = saveCheckpointHandback(state)
			return checkpointError("handback_destination_recovery_failed", 4, errors.New(state.LastError))
		}
		state.Status = "failed-destination-ready"
		_ = saveCheckpointHandback(state)
		return checkpointError("handback_destination_stop_failed", 4, stopErr)
	}
	state.Status = "stopped"
	if err := saveCheckpointHandback(state); err != nil {
		_ = lease.Release()
		_ = checkpointEnsureSource(state.Config, *timeout)
		return checkpointError("handback_lifecycle_state_failed", 4, err)
	}
	return finishCheckpointHandback(state, lease, *timeout, stdout)
}

func acquireStoppedHandbackLease(state checkpointHandbackLifecycle) (checkpointLease, error) {
	lease, err := mountlease.Acquire(state.Config.Server, state.Config.WorkspaceID, state.Config.LocalRoot)
	if err == nil {
		return lease, nil
	}
	if state.Status != "preparing" {
		return nil, fmt.Errorf("destination mount lease is not available while handback is %s: %w", state.Status, err)
	}
	active, resolveErr := checkpointResolveActive(state.LocalRoot)
	if resolveErr != nil {
		return nil, fmt.Errorf("resolve interrupted destination: %w", resolveErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkpointStopTimeout)
	defer cancel()
	return checkpointStopActive(ctx, active)
}

func finishCheckpointHandback(state checkpointHandbackLifecycle, lease checkpointLease, timeout time.Duration, stdout io.Writer) error {
	state.Status = "handing-back"
	if err := saveCheckpointHandback(state); err != nil {
		_ = lease.Release()
		return checkpointError("handback_lifecycle_state_failed", 4, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	proof, health, handbackErr := checkpointHandbackStopped(ctx, state.Config, state.Receipt, state.ConsumerIdempotencyKey, state.HandbackID)
	cancel()
	if handbackErr != nil {
		_ = lease.Release()
		state.LastError = handbackErr.Error()
		if definitiveCheckpointHandbackHTTPFailure(handbackErr) {
			if recoveryErr := checkpointEnsureSource(state.Config, timeout); recoveryErr != nil {
				state.Status = "recovery-failed"
				state.LastError += "; destination recovery: " + recoveryErr.Error()
				_ = saveCheckpointHandback(state)
				return checkpointError("handback_destination_recovery_failed", 4, errors.New(state.LastError))
			}
			state.Status = "failed-destination-ready"
			_ = saveCheckpointHandback(state)
			return checkpointError("handback_nonconverged", 5, handbackErr)
		}
		// A transport failure or exhausted retryable HTTP response after POST is
		// ambiguous: the application may have committed before a proxy/gateway
		// emitted 5xx. Leave the destination stopped and require an exact retry
		// with the same handbackId; never restart into split-brain.
		state.Status = "handback-unknown"
		if err := saveCheckpointHandback(state); err != nil {
			return checkpointError("handback_lifecycle_state_failed", 4, fmt.Errorf("persist ambiguous handback while destination remains stopped: %w", err))
		}
		return checkpointError("handback_result_unknown", 4, handbackErr)
	}
	result := checkpointHandbackEnvelope{
		Version: checkpointLifecycleVersion, Kind: "relayfile-checkpoint-handback", HandbackID: state.HandbackID,
		WorkspaceID: state.WorkspaceID, LocalRoot: state.LocalRoot, RemoteRoot: state.RemoteRoot,
		SessionID: state.SessionID, Generation: state.Generation, Status: "released",
		Proof: proof, Health: health, ReleasedAt: proof.ReleasedAt,
	}
	state.Status = "released"
	state.Result = &result
	state.LastError = ""
	if err := saveCheckpointHandback(state); err != nil {
		_ = lease.Release()
		return checkpointError("handback_lifecycle_state_failed", 4, err)
	}
	if err := lease.Release(); err != nil {
		return checkpointError("handback_lease_release_failed", 4, err)
	}
	return writeJSON(stdout, result)
}

func definitiveCheckpointHandbackHTTPFailure(err error) bool {
	var httpErr *mountsync.HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	code := strings.TrimSpace(httpErr.Code)
	switch {
	case httpErr.StatusCode == 400 && code == "bad_request":
		return true
	case httpErr.StatusCode == 401 && code == "unauthorized":
		return true
	case httpErr.StatusCode == 403 && code == "forbidden":
		return true
	case httpErr.StatusCode == 404 && code == "checkpoint_seal_not_found":
		return true
	case httpErr.StatusCode == 409 && strings.HasPrefix(code, "checkpoint_"):
		return true
	case httpErr.StatusCode == 413 && code == "payload_too_large":
		return true
	default:
		// Unknown/gateway 4xx, retry-class 408/425/429, transport failures,
		// and every 5xx remain ambiguous after an exhausted POST.
		return false
	}
}

func continueStoppedDestinationVerification(state checkpointVerificationLifecycle, timeout time.Duration, stdout io.Writer) error {
	acquiredLease, err := mountlease.Acquire(state.Config.Server, state.Config.WorkspaceID, state.Config.LocalRoot)
	var lease checkpointLease
	if err == nil {
		lease = acquiredLease
	}
	if err != nil {
		if readyErr := checkpointWaitMountReady(state.Config, 500*time.Millisecond); readyErr == nil {
			active, resolveErr := checkpointResolveActive(state.LocalRoot)
			if resolveErr != nil {
				return checkpointError("verification_recovery_failed", 4, resolveErr)
			}
			ctx, cancel := context.WithTimeout(context.Background(), checkpointStopTimeout)
			lease, err = checkpointStopActive(ctx, active)
			cancel()
		}
	}
	if err != nil {
		return recoverDestinationAfterVerificationFailure(state, "verification_recovery_failed", 4, err, timeout)
	}
	return verifyAndRecoverDestination(state, lease, timeout, stdout)
}

func verifyAndRecoverDestination(state checkpointVerificationLifecycle, lease checkpointLease, timeout time.Duration, stdout io.Writer) error {
	state.Status = "verifying"
	_ = saveCheckpointVerification(state)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	verification, verifyErr := checkpointVerifyStopped(ctx, state.Config, state.Receipt)
	cancel()
	if verifyErr != nil {
		_ = lease.Release()
		return recoverDestinationAfterVerificationFailure(state, "verification_nonconverged", 5, verifyErr, timeout)
	}
	verifiedAt := time.Now().UTC().Format(time.RFC3339Nano)
	result := checkpointDestinationVerificationEnvelope{
		Version: checkpointLifecycleVersion, Kind: "relayfile-destination-verification", VerificationID: state.VerificationID,
		WorkspaceID: state.WorkspaceID, LocalRoot: state.LocalRoot, RemoteRoot: state.RemoteRoot,
		SessionID: state.SessionID, Generation: state.Generation, Status: "converged",
		Observed: verification.Observed, Health: verification.Health, VerifiedAt: verifiedAt,
	}
	state.Status = "verified"
	state.Result = &result
	state.LastError = ""
	if err := saveCheckpointVerification(state); err != nil {
		_ = lease.Release()
		return recoverDestinationAfterVerificationFailure(state, "verification_lifecycle_state_failed", 4, err, timeout)
	}
	releaseErr := lease.Release()
	if err := checkpointEnsureSource(state.Config, timeout); err != nil {
		if releaseErr != nil {
			err = fmt.Errorf("release verification lease: %v; destination recovery: %w", releaseErr, err)
		}
		state.Status = "recovery-failed"
		state.LastError = err.Error()
		_ = saveCheckpointVerification(state)
		return checkpointError("verification_recovery_failed", 4, err)
	}
	if releaseErr != nil {
		return checkpointError("verification_recovery_failed", 4, releaseErr)
	}
	state.Status = "converged"
	if err := saveCheckpointVerification(state); err != nil {
		return checkpointError("verification_lifecycle_state_failed", 4, err)
	}
	return writeJSON(stdout, result)
}

func recoverDestinationAfterVerificationFailure(state checkpointVerificationLifecycle, code string, exitCode int, cause error, timeout time.Duration) error {
	state.LastError = cause.Error()
	if recoveryErr := checkpointEnsureSource(state.Config, timeout); recoveryErr != nil {
		state.Status = "recovery-failed"
		state.LastError += "; destination recovery: " + recoveryErr.Error()
		_ = saveCheckpointVerification(state)
		return checkpointError("verification_recovery_failed", 4, errors.New(state.LastError))
	}
	if exitCode == 5 {
		state.Status = "diverged-source-ready"
	} else {
		state.Status = "failed-source-ready"
	}
	_ = saveCheckpointVerification(state)
	return checkpointError(code, exitCode, cause)
}

func normalizeAbsoluteLocalRoot(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !filepath.IsAbs(trimmed) {
		return "", errors.New("local root must be absolute")
	}
	cleaned := filepath.Clean(trimmed)
	info, err := os.Stat(cleaned)
	if err != nil {
		return "", fmt.Errorf("local root must be an existing directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("local root must be an existing directory")
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve local root: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func resolveActiveCheckpointMount(localRoot string) (activeCheckpointMount, error) {
	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return activeCheckpointMount{}, checkpointError("checkpoint_catalog_invalid", 2, err)
	}
	var matches []workspaceRecord
	for _, record := range catalog.Workspaces {
		if sameCheckpointLocalRoot(record.LocalDir, localRoot) {
			matches = append(matches, record)
		}
	}
	if len(matches) != 1 {
		return activeCheckpointMount{}, checkpointError("checkpoint_catalog_mismatch", 2, fmt.Errorf("expected one catalog workspace for local root, found %d", len(matches)))
	}
	record := matches[0]
	pidState, structured := readDaemonPIDState(localRoot)
	if !structured || !pidState.Registered || pidState.CheckpointConfig == nil {
		return activeCheckpointMount{}, checkpointError("checkpoint_restart_contract_missing", 2, errors.New("active daemon lacks a registered non-secret checkpoint restart contract"))
	}
	pid, verified := verifyDaemonProcess(localRoot, record.ID)
	if !verified || pid != pidState.PID || !processAlive(pid) {
		return activeCheckpointMount{}, checkpointError("checkpoint_daemon_identity_invalid", 2, errors.New("registered PID does not identify the active workspace daemon"))
	}
	running, stalePID, err := runningMountDaemons(localRoot, record.ID, record.Name)
	if err != nil || stalePID != 0 || len(running) != 1 || running[0].PID != pid {
		return activeCheckpointMount{}, checkpointError("checkpoint_daemon_identity_invalid", 2, fmt.Errorf("active daemon discovery was not unique: running=%d stalePid=%d err=%v", len(running), stalePID, err))
	}
	config := *pidState.CheckpointConfig
	if err := validateCheckpointMountConfig(config, record, localRoot); err != nil {
		return activeCheckpointMount{}, err
	}
	if err := waitCheckpointMountReady(config, 2*time.Second); err != nil {
		return activeCheckpointMount{}, checkpointError("checkpoint_source_not_ready", 4, err)
	}
	return activeCheckpointMount{record: record, pid: pidState, config: config}, nil
}

func validateCheckpointMountConfig(config checkpointMountConfig, record workspaceRecord, localRoot string) error {
	if config.Version != checkpointLifecycleVersion || config.Mode != defaultMountMode {
		if strings.EqualFold(config.Mode, "fuse") {
			return checkpointError("checkpoint_fuse_unsupported", 2, errors.New("FUSE checkpoint requires daemon IPC and is not available"))
		}
		return checkpointError("checkpoint_restart_contract_invalid", 2, errors.New("unsupported restart contract version or mode"))
	}
	if config.LocalLayout != mountscope.LayoutExact || len(mountscope.NormalizePaths(config.RemotePaths, "/")) != 1 {
		return checkpointError("checkpoint_topology_unsupported", 2, errors.New("live checkpoint v1 requires one exact full-root poll mount"))
	}
	remoteRoot := mountscope.FirstPath(config.RemotePaths, "/")
	if remoteRoot != "/" {
		return checkpointError("checkpoint_topology_unsupported", 2, errors.New("live checkpoint v1 supports only the full remote root /"))
	}
	if config.WorkspaceID != record.ID || !sameCheckpointLocalRoot(config.LocalRoot, localRoot) || remoteRoot != mountscope.FirstPath(record.RemotePaths, "/") || config.Server == "" {
		return checkpointError("checkpoint_restart_contract_invalid", 2, errors.New("PID restart identity does not match the workspace catalog"))
	}
	if config.CredentialsFile == "" || !filepath.IsAbs(config.CredentialsFile) {
		return checkpointError("checkpoint_credentials_unrecoverable", 2, errors.New("daemon was not started from a delegated credential file; secret argv cannot be reconstructed safely"))
	}
	info, err := os.Lstat(config.CredentialsFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return checkpointError("checkpoint_credentials_unrecoverable", 2, errors.New("delegated credential file is missing, non-regular, or not private"))
	}
	for name, raw := range map[string]string{
		"interval": config.Interval, "timeout": config.Timeout, "bootstrapTimeout": config.BootstrapTimeout,
		"fullPullMinInterval": config.FullPullMinInterval, "cursorTimeout": config.CursorTimeout, "memlogInterval": config.MemlogInterval,
	} {
		if _, err := parseCheckpointDuration(raw, name == "fullPullMinInterval"); err != nil {
			return checkpointError("checkpoint_restart_contract_invalid", 2, fmt.Errorf("%s: %w", name, err))
		}
	}
	return nil
}

func parseCheckpointDuration(raw string, allowNegativeOne bool) (time.Duration, error) {
	if allowNegativeOne && strings.TrimSpace(raw) == "-1ns" {
		return -1, nil
	}
	value, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, err
	}
	if value < 0 && !(allowNegativeOne && value == -1) {
		return 0, errors.New("negative duration")
	}
	return value, nil
}

func stopCheckpointMount(ctx context.Context, active activeCheckpointMount) (checkpointLease, error) {
	process, err := os.FindProcess(active.pid.PID)
	if err != nil {
		return nil, err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !isProcessAlreadyGone(err) {
		return nil, err
	}
	for {
		lease, leaseErr := mountlease.Acquire(active.config.Server, active.config.WorkspaceID, active.config.LocalRoot)
		if leaseErr == nil && !processAlive(active.pid.PID) {
			_ = os.Remove(mountPIDFile(active.config.LocalRoot))
			return lease, nil
		}
		if leaseErr == nil {
			_ = lease.Release()
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("daemon did not exit and release its mount lease: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func issueCheckpointForStoppedMount(ctx context.Context, config checkpointMountConfig, sessionID string, generation uint64, ttlSeconds int) (mountsync.CheckpointSeal, error) {
	bundle, loadedPath, err := loadDelegatedCredentials(config.CredentialsFile)
	if err != nil {
		return mountsync.CheckpointSeal{}, err
	}
	bundle, err = refreshDelegatedCredentials(loadedPath, bundle, false)
	if err != nil {
		return mountsync.CheckpointSeal{}, err
	}
	if !workspaceRequestMatchesDelegatedCredentials(config.WorkspaceID, bundle.Workspace()) {
		return mountsync.CheckpointSeal{}, errors.New("delegated credential workspace does not match restart contract")
	}
	interval, _ := parseCheckpointDuration(config.Interval, false)
	bootstrapTimeout, _ := parseCheckpointDuration(config.BootstrapTimeout, false)
	fullPullMinInterval, _ := parseCheckpointDuration(config.FullPullMinInterval, true)
	cursorTimeout, _ := parseCheckpointDuration(config.CursorTimeout, false)
	client := mountsync.NewHTTPClient(config.Server, bundle.BearerToken(), mountsync.NewSyncHTTPClient())
	syncer, err := mountsync.NewSyncer(client, mountsync.SyncerOptions{
		WorkspaceID: config.WorkspaceID, RemoteRoot: config.RemotePaths[0], EventProvider: config.EventProvider,
		LocalRoot: config.LocalRoot, StateFile: config.StateFile, StateDir: config.StateDir, MountKind: config.MountKind,
		ValidateState: true, Scopes: delegatedBundleAvailableScopes(bundle), RootCtx: ctx, Mode: config.Mode,
		Interval: interval, LowMemory: boolPtr(config.LowMemory), BootstrapTimeout: bootstrapTimeout,
		BootstrapMaxFilesPerCycle: config.BootstrapMaxFilesPerCycle, FullPullMinInterval: fullPullMinInterval,
		CursorTimeout: cursorTimeout, ForceFullReconcile: boolPtr(config.ForceFullReconcile), SyncMode: "mirror",
	})
	if err != nil {
		return mountsync.CheckpointSeal{}, err
	}
	return syncer.CheckpointAndSeal(ctx, mountsync.CheckpointAndSealOptions{SessionID: sessionID, Generation: generation, TTLSeconds: ttlSeconds})
}

func verifyCheckpointForStoppedMount(ctx context.Context, config checkpointMountConfig, receipt mountsync.CheckpointSeal) (mountsync.CheckpointVerification, error) {
	bundle, loadedPath, err := loadDelegatedCredentials(config.CredentialsFile)
	if err != nil {
		return mountsync.CheckpointVerification{}, err
	}
	bundle, err = refreshDelegatedCredentials(loadedPath, bundle, false)
	if err != nil {
		return mountsync.CheckpointVerification{}, err
	}
	if !workspaceRequestMatchesDelegatedCredentials(config.WorkspaceID, bundle.Workspace()) {
		return mountsync.CheckpointVerification{}, errors.New("delegated credential workspace does not match destination restart contract")
	}
	interval, _ := parseCheckpointDuration(config.Interval, false)
	bootstrapTimeout, _ := parseCheckpointDuration(config.BootstrapTimeout, false)
	fullPullMinInterval, _ := parseCheckpointDuration(config.FullPullMinInterval, true)
	cursorTimeout, _ := parseCheckpointDuration(config.CursorTimeout, false)
	client := mountsync.NewHTTPClient(config.Server, bundle.BearerToken(), mountsync.NewSyncHTTPClient())
	syncer, err := mountsync.NewSyncer(client, mountsync.SyncerOptions{
		WorkspaceID: config.WorkspaceID, RemoteRoot: config.RemotePaths[0], EventProvider: config.EventProvider,
		LocalRoot: config.LocalRoot, StateFile: config.StateFile, StateDir: config.StateDir, MountKind: config.MountKind,
		ValidateState: true, Scopes: delegatedBundleAvailableScopes(bundle), RootCtx: ctx, Mode: config.Mode,
		Interval: interval, LowMemory: boolPtr(config.LowMemory), BootstrapTimeout: bootstrapTimeout,
		BootstrapMaxFilesPerCycle: config.BootstrapMaxFilesPerCycle, FullPullMinInterval: fullPullMinInterval,
		CursorTimeout: cursorTimeout, ForceFullReconcile: boolPtr(config.ForceFullReconcile), SyncMode: "mirror",
	})
	if err != nil {
		return mountsync.CheckpointVerification{}, err
	}
	return syncer.VerifyCheckpoint(ctx, receipt)
}

func handbackCheckpointForStoppedMount(ctx context.Context, config checkpointMountConfig, receipt mountsync.CheckpointSeal, consumerKey, handbackKey string) (mountsync.CheckpointSealOwnership, mountsync.CheckpointVerificationHealth, error) {
	bundle, loadedPath, err := loadDelegatedCredentials(config.CredentialsFile)
	if err != nil {
		return mountsync.CheckpointSealOwnership{}, mountsync.CheckpointVerificationHealth{}, err
	}
	bundle, err = refreshDelegatedCredentials(loadedPath, bundle, false)
	if err != nil {
		return mountsync.CheckpointSealOwnership{}, mountsync.CheckpointVerificationHealth{}, err
	}
	if !workspaceRequestMatchesDelegatedCredentials(config.WorkspaceID, bundle.Workspace()) {
		return mountsync.CheckpointSealOwnership{}, mountsync.CheckpointVerificationHealth{}, errors.New("delegated credential workspace does not match destination handback contract")
	}
	interval, _ := parseCheckpointDuration(config.Interval, false)
	bootstrapTimeout, _ := parseCheckpointDuration(config.BootstrapTimeout, false)
	fullPullMinInterval, _ := parseCheckpointDuration(config.FullPullMinInterval, true)
	cursorTimeout, _ := parseCheckpointDuration(config.CursorTimeout, false)
	client := mountsync.NewHTTPClient(config.Server, bundle.BearerToken(), mountsync.NewSyncHTTPClient())
	syncer, err := mountsync.NewSyncer(client, mountsync.SyncerOptions{
		WorkspaceID: config.WorkspaceID, RemoteRoot: config.RemotePaths[0], EventProvider: config.EventProvider,
		LocalRoot: config.LocalRoot, StateFile: config.StateFile, StateDir: config.StateDir, MountKind: config.MountKind,
		ValidateState: true, Scopes: delegatedBundleAvailableScopes(bundle), RootCtx: ctx, Mode: config.Mode,
		Interval: interval, LowMemory: boolPtr(config.LowMemory), BootstrapTimeout: bootstrapTimeout,
		BootstrapMaxFilesPerCycle: config.BootstrapMaxFilesPerCycle, FullPullMinInterval: fullPullMinInterval,
		CursorTimeout: cursorTimeout, ForceFullReconcile: boolPtr(config.ForceFullReconcile), SyncMode: "mirror",
	})
	if err != nil {
		return mountsync.CheckpointSealOwnership{}, mountsync.CheckpointVerificationHealth{}, err
	}
	return syncer.HandbackCheckpoint(ctx, receipt, consumerKey, handbackKey)
}

func ensureCheckpointSourceReady(config checkpointMountConfig, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	started := false
	for time.Now().Before(deadline) {
		if err := waitCheckpointMountReady(config, 100*time.Millisecond); err == nil {
			return nil
		}
		state, ok := readDaemonPIDState(config.LocalRoot)
		if (!ok || !state.Registered || !processAlive(state.PID)) && !started {
			remaining := time.Until(deadline)
			ctx, cancel := context.WithTimeout(context.Background(), remaining)
			err := checkpointStartMount(ctx, config)
			cancel()
			if err != nil {
				return err
			}
			started = true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("source mount did not become ready within %s", timeout)
}

func startCheckpointMountProcess(ctx context.Context, config checkpointMountConfig) error {
	executable := resolvedSelfExecutable()
	if executable == "" {
		return errors.New("resolve relayfile executable")
	}
	args := checkpointMountArgs(config)
	cmd := exec.CommandContext(ctx, executable, append([]string{"mount"}, args...)...)
	cmd.Env = checkpointSubprocessEnv(os.Environ())
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("start source mount: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func checkpointMountArgs(config checkpointMountConfig) []string {
	args := []string{
		config.WorkspaceID, config.LocalRoot, "--background",
		"--server", config.Server, "--creds-file", config.CredentialsFile,
		"--local-layout", config.LocalLayout, "--mode", config.Mode,
		"--interval", config.Interval, "--interval-jitter", strconv.FormatFloat(config.IntervalJitter, 'g', -1, 64),
		"--timeout", config.Timeout, "--bootstrap-timeout", config.BootstrapTimeout,
		"--bootstrap-max-files-per-cycle", strconv.Itoa(config.BootstrapMaxFilesPerCycle),
		"--full-pull-min-interval", config.FullPullMinInterval, "--cursor-timeout", config.CursorTimeout,
		"--websocket=" + strconv.FormatBool(config.WebsocketEnabled), "--low-memory=" + strconv.FormatBool(config.LowMemory),
		"--memlog-interval", config.MemlogInterval,
	}
	for _, remotePath := range config.RemotePaths {
		args = append(args, "--remote-path", remotePath)
	}
	for _, pair := range [][2]string{{"--provider", config.EventProvider}, {"--state-file", config.StateFile}, {"--state-dir", config.StateDir}, {"--mount-kind", config.MountKind}, {"--pprof-addr", config.PprofAddr}} {
		if strings.TrimSpace(pair[1]) != "" {
			args = append(args, pair[0], pair[1])
		}
	}
	if config.ForceFullReconcile {
		args = append(args, "--full-reconcile")
	}
	return args
}

func checkpointSubprocessEnv(env []string) []string {
	blocked := map[string]struct{}{
		"RELAYFILE_TOKEN": {}, "RELAYFILE_SERVER": {}, "RELAYFILE_BASE_URL": {}, "RELAYFILE_REMOTE_PATH": {},
		"RELAYFILE_MOUNT_CREDS_FILE": {}, "RELAYFILE_DELEGATED_CREDENTIALS_FILE": {}, "RELAYFILE_MOUNT_PATHS_FILE": {},
	}
	out := make([]string, 0, len(env))
	for _, item := range env {
		key := strings.SplitN(item, "=", 2)[0]
		if _, skip := blocked[key]; skip || strings.HasPrefix(key, "RELAYFILE_MOUNT_") {
			continue
		}
		out = append(out, item)
	}
	return out
}

func waitCheckpointMountReady(config checkpointMountConfig, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		pidState, ok := readDaemonPIDState(config.LocalRoot)
		if ok && pidState.Registered && pidState.WorkspaceID == config.WorkspaceID && processAlive(pidState.PID) && pidState.CheckpointConfig != nil {
			payload, err := os.ReadFile(filepath.Join(config.LocalRoot, ".relay", "state.json"))
			var state syncStateFile
			if err == nil && json.Unmarshal(payload, &state) == nil && state.WorkspaceID == config.WorkspaceID && state.Mode == defaultMountMode && state.RemoteRoot == config.RemotePaths[0] && state.Daemon != nil && state.Daemon.PID == pidState.PID && state.LastSuccessfulReconcileAt != "" && state.LastError == nil && state.PendingConflicts == 0 {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("mount did not become ready within %s", timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func burnCheckpointReceiptForResume(state checkpointLifecycleState, timeout time.Duration) (mountsync.CheckpointSealOwnership, error) {
	bundle, loadedPath, err := loadDelegatedCredentials(state.Config.CredentialsFile)
	if err != nil {
		return mountsync.CheckpointSealOwnership{}, err
	}
	bundle, err = refreshDelegatedCredentials(loadedPath, bundle, false)
	if err != nil {
		return mountsync.CheckpointSealOwnership{}, err
	}
	client := mountsync.NewHTTPClient(state.Config.Server, bundle.BearerToken(), mountsync.NewSyncHTTPClient())
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	proof, err := client.ResumeCheckpointSeal(ctx, state.WorkspaceID, mountsync.CheckpointSealResumeRequest{
		SealToken: state.Receipt.SealToken, Root: state.Receipt.Root, SessionID: state.SessionID,
		Generation: state.Generation, ResumeIdempotencyKey: "source-resume:" + checkpointHash(state.ResumeID)[:24],
	})
	return proof, err
}

func waitCheckpointSourceProof(config checkpointMountConfig, proof mountsync.CheckpointSealOwnership, timeout time.Duration) error {
	syncer, err := newCheckpointSourceProofVerifier(config)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if lastErr == nil {
				lastErr = context.DeadlineExceeded
			}
			return fmt.Errorf("source did not converge to resume proof within %s: %w", timeout, lastErr)
		}
		ctx, cancel := context.WithTimeout(context.Background(), remaining)
		_, lastErr = syncer.VerifyCheckpointOwnership(ctx, proof)
		cancel()
		if lastErr == nil {
			return nil
		}
		pause := 50 * time.Millisecond
		if remaining < pause {
			pause = remaining
		}
		time.Sleep(pause)
	}
}

func newCheckpointSourceProofVerifier(config checkpointMountConfig) (*mountsync.Syncer, error) {
	bundle, loadedPath, err := loadDelegatedCredentials(config.CredentialsFile)
	if err != nil {
		return nil, err
	}
	bundle, err = refreshDelegatedCredentials(loadedPath, bundle, false)
	if err != nil {
		return nil, err
	}
	if !workspaceRequestMatchesDelegatedCredentials(config.WorkspaceID, bundle.Workspace()) {
		return nil, errors.New("delegated credential workspace does not match source resume contract")
	}
	interval, _ := parseCheckpointDuration(config.Interval, false)
	bootstrapTimeout, _ := parseCheckpointDuration(config.BootstrapTimeout, false)
	fullPullMinInterval, _ := parseCheckpointDuration(config.FullPullMinInterval, true)
	cursorTimeout, _ := parseCheckpointDuration(config.CursorTimeout, false)
	client := mountsync.NewHTTPClient(config.Server, bundle.BearerToken(), mountsync.NewSyncHTTPClient())
	syncer, err := mountsync.NewSyncer(client, mountsync.SyncerOptions{
		WorkspaceID: config.WorkspaceID, RemoteRoot: config.RemotePaths[0], EventProvider: config.EventProvider,
		LocalRoot: config.LocalRoot, StateFile: config.StateFile, StateDir: config.StateDir, MountKind: config.MountKind,
		ValidateState: true, Scopes: delegatedBundleAvailableScopes(bundle), RootCtx: context.Background(), Mode: config.Mode,
		Interval: interval, LowMemory: boolPtr(config.LowMemory), BootstrapTimeout: bootstrapTimeout,
		BootstrapMaxFilesPerCycle: config.BootstrapMaxFilesPerCycle, FullPullMinInterval: fullPullMinInterval,
		CursorTimeout: cursorTimeout, ForceFullReconcile: boolPtr(config.ForceFullReconcile), SyncMode: "mirror",
	})
	if err != nil {
		return nil, err
	}
	return syncer, nil
}

func checkpointEnvelopeFromState(state checkpointLifecycleState) checkpointSealEnvelope {
	return checkpointSealEnvelope{
		Version: checkpointLifecycleVersion, Kind: "relayfile-checkpoint-seal", Status: "sealed", WorkspaceID: state.WorkspaceID,
		LocalRoot: state.LocalRoot, SessionID: state.SessionID, Generation: state.Generation,
		Receipt: *state.Receipt, Health: mountsync.CheckpointVerificationHealth{}, ResumeID: state.ResumeID, SealedAt: state.SealedAt,
	}
}

func resumeEnvelopeFromState(state checkpointLifecycleState) checkpointResumeEnvelope {
	return checkpointResumeEnvelope{
		Version: checkpointLifecycleVersion, Kind: "relayfile-resume-seal", WorkspaceID: state.WorkspaceID,
		LocalRoot: state.LocalRoot, ResumeID: state.ResumeID, Status: "ready", ResumedAt: state.ResumedAt,
	}
}

func newResumeID() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "rsm_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func checkpointLifecycleDir() string { return filepath.Join(configDir(), "checkpoint-resumes") }

func checkpointVerificationDir() string {
	return filepath.Join(configDir(), "checkpoint-verifications")
}

func checkpointHandbackDir() string {
	return filepath.Join(configDir(), "checkpoint-handbacks")
}

func checkpointLifecyclePath(resumeID string) string {
	return filepath.Join(checkpointLifecycleDir(), checkpointHash(resumeID)+".json")
}

func checkpointVerificationPath(verificationID string) string {
	return filepath.Join(checkpointVerificationDir(), checkpointHash(verificationID)+".json")
}

func checkpointHandbackPath(handbackID string) string {
	return filepath.Join(checkpointHandbackDir(), checkpointHash(handbackID)+".json")
}

func checkpointHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func ensureCheckpointLifecycleDir() error {
	dir := checkpointLifecycleDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("checkpoint lifecycle directory is not a private real directory")
	}
	return nil
}

func ensureCheckpointVerificationDir() error {
	dir := checkpointVerificationDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("checkpoint verification directory is not a private real directory")
	}
	return nil
}

func ensureCheckpointHandbackDir() error {
	dir := checkpointHandbackDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("checkpoint handback directory is not a private real directory")
	}
	return nil
}

func saveCheckpointHandback(state checkpointHandbackLifecycle) error {
	remotePaths := mountscope.NormalizePaths(state.Config.RemotePaths, "/")
	if state.Version != checkpointLifecycleVersion || !checkpointLifecycleIDPattern.MatchString(state.HandbackID) || !checkpointLifecycleIDPattern.MatchString(state.ConsumerIdempotencyKey) ||
		!sameCheckpointLocalRoot(state.LocalRoot, state.Config.LocalRoot) || state.Config.WorkspaceID != state.WorkspaceID || len(remotePaths) != 1 || remotePaths[0] != state.RemoteRoot ||
		state.Receipt.WorkspaceID != state.WorkspaceID || state.Receipt.Root != state.RemoteRoot || state.Receipt.SessionID != state.SessionID || state.Receipt.Generation != state.Generation || validateDestinationReceipt(state.Receipt) != nil {
		return errors.New("invalid checkpoint handback lifecycle identity")
	}
	if state.Result != nil {
		proof := state.Result.Proof
		if state.Result.HandbackID != state.HandbackID || state.Result.WorkspaceID != state.WorkspaceID || !sameCheckpointLocalRoot(state.Result.LocalRoot, state.LocalRoot) || state.Result.RemoteRoot != state.RemoteRoot ||
			state.Result.SessionID != state.SessionID || state.Result.Generation != state.Generation || state.Result.Status != "released" || proof.Status != "released" || proof.SealID != state.Receipt.SealID ||
			proof.WorkspaceID != state.WorkspaceID || proof.Root != state.RemoteRoot || proof.SessionID != state.SessionID || proof.Generation != state.Generation || proof.ConsumedAt != state.Receipt.ConsumedAt ||
			!checkpointDigestPattern.MatchString(proof.Digest) || !checkpointRevisionPattern.MatchString(proof.WorkspaceRevision) || !checkpointCursorPattern.MatchString(proof.EventCursor) || proof.ReleasedAt == "" || proof.SourceResumedAt != "" {
			return errors.New("checkpoint handback result identity mismatch")
		}
	}
	if err := ensureCheckpointHandbackDir(); err != nil {
		return err
	}
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomically(checkpointHandbackPath(state.HandbackID), payload, 0o600)
}

func loadCheckpointHandbackIfExists(handbackID string) (checkpointHandbackLifecycle, bool, error) {
	var state checkpointHandbackLifecycle
	if !checkpointLifecycleIDPattern.MatchString(handbackID) {
		return state, false, errors.New("invalid handbackId")
	}
	path := checkpointHandbackPath(handbackID)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return state, false, errors.New("handback lifecycle is missing, non-regular, or not private")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return state, false, err
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		return state, false, err
	}
	if state.Version != checkpointLifecycleVersion || subtle.ConstantTimeCompare([]byte(state.HandbackID), []byte(handbackID)) != 1 {
		return state, false, errors.New("handback lifecycle identity mismatch")
	}
	return state, true, nil
}

func saveCheckpointVerification(state checkpointVerificationLifecycle) error {
	remotePaths := mountscope.NormalizePaths(state.Config.RemotePaths, "/")
	if state.Version != checkpointLifecycleVersion || !checkpointLifecycleIDPattern.MatchString(state.VerificationID) || !sameCheckpointLocalRoot(state.LocalRoot, state.Config.LocalRoot) ||
		state.Config.WorkspaceID != state.WorkspaceID || len(remotePaths) != 1 || remotePaths[0] != state.RemoteRoot ||
		state.Receipt.WorkspaceID != state.WorkspaceID || state.Receipt.Root != state.RemoteRoot || state.Receipt.SessionID != state.SessionID || state.Receipt.Generation != state.Generation || validateDestinationReceipt(state.Receipt) != nil {
		return errors.New("invalid checkpoint verification lifecycle identity")
	}
	if state.Result != nil && (state.Result.VerificationID != state.VerificationID || state.Result.WorkspaceID != state.WorkspaceID || !sameCheckpointLocalRoot(state.Result.LocalRoot, state.LocalRoot) || state.Result.RemoteRoot != state.RemoteRoot || state.Result.SessionID != state.SessionID || state.Result.Generation != state.Generation) {
		return errors.New("checkpoint verification result identity mismatch")
	}
	if err := ensureCheckpointVerificationDir(); err != nil {
		return err
	}
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomically(checkpointVerificationPath(state.VerificationID), payload, 0o600)
}

func loadCheckpointVerificationIfExists(verificationID string) (checkpointVerificationLifecycle, bool, error) {
	var state checkpointVerificationLifecycle
	if !checkpointLifecycleIDPattern.MatchString(verificationID) {
		return state, false, errors.New("invalid verificationId")
	}
	path := checkpointVerificationPath(verificationID)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return state, false, errors.New("verification lifecycle is missing, non-regular, or not private")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return state, false, err
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		return state, false, err
	}
	if state.Version != checkpointLifecycleVersion || subtle.ConstantTimeCompare([]byte(state.VerificationID), []byte(verificationID)) != 1 {
		return state, false, errors.New("verification lifecycle identity mismatch")
	}
	return state, true, nil
}

func validateDestinationReceipt(receipt mountsync.CheckpointSeal) error {
	if receipt.SealID == "" || receipt.SealToken != "" || receipt.WorkspaceID == "" || receipt.Root != "/" || !checkpointLifecycleIDPattern.MatchString(receipt.SessionID) || receipt.Generation == 0 ||
		!checkpointDigestPattern.MatchString(receipt.Digest) || !checkpointRevisionPattern.MatchString(receipt.WorkspaceRevision) || !checkpointCursorPattern.MatchString(receipt.EventCursor) || receipt.ConsumedAt == "" {
		return errors.New("receipt must be a consumed full-root checkpoint seal without sealToken")
	}
	for _, raw := range []string{receipt.IssuedAt, receipt.ExpiresAt, receipt.ConsumedAt} {
		if _, err := time.Parse(time.RFC3339Nano, raw); err != nil {
			return errors.New("receipt timestamps must be RFC3339")
		}
	}
	return nil
}

func sameDestinationReceipt(left, right mountsync.CheckpointSeal) bool {
	return left.SealToken == "" && right.SealToken == "" && left.SealID == right.SealID && left.WorkspaceID == right.WorkspaceID && left.Root == right.Root && left.SessionID == right.SessionID && left.Generation == right.Generation &&
		left.Digest == right.Digest && left.WorkspaceRevision == right.WorkspaceRevision && left.EventCursor == right.EventCursor && left.IssuedAt == right.IssuedAt && left.ExpiresAt == right.ExpiresAt && left.ConsumedAt == right.ConsumedAt
}

func validateSourceResumeProof(state checkpointLifecycleState, proof mountsync.CheckpointSealOwnership) error {
	if state.Receipt == nil || proof.Status != "source-resumed" || proof.SealID != state.Receipt.SealID || proof.WorkspaceID != state.WorkspaceID || proof.Root != state.RemoteRoot ||
		proof.SessionID != state.SessionID || proof.Generation != state.Generation || !checkpointDigestPattern.MatchString(proof.Digest) ||
		!checkpointRevisionPattern.MatchString(proof.WorkspaceRevision) || !checkpointCursorPattern.MatchString(proof.EventCursor) || proof.ReleasedAt == "" || proof.SourceResumedAt == "" {
		return errors.New("source-resume proof does not match the sealed lifecycle")
	}
	for _, raw := range []string{proof.ReleasedAt, proof.SourceResumedAt} {
		if _, err := time.Parse(time.RFC3339Nano, raw); err != nil {
			return errors.New("source-resume proof timestamps must be RFC3339")
		}
	}
	if proof.ConsumedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, proof.ConsumedAt); err != nil {
			return errors.New("source-resume consumedAt must be RFC3339")
		}
	}
	return nil
}

func saveCheckpointLifecycle(state checkpointLifecycleState) error {
	if state.Version != checkpointLifecycleVersion || !checkpointLifecycleIDPattern.MatchString(state.ResumeID) {
		return errors.New("invalid checkpoint lifecycle identity")
	}
	if state.ResumeProof != nil {
		if err := validateSourceResumeProof(state, *state.ResumeProof); err != nil {
			return err
		}
	}
	if err := ensureCheckpointLifecycleDir(); err != nil {
		return err
	}
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomically(checkpointLifecyclePath(state.ResumeID), payload, 0o600)
}

func loadCheckpointLifecycle(resumeID string) (checkpointLifecycleState, error) {
	var state checkpointLifecycleState
	if !checkpointLifecycleIDPattern.MatchString(resumeID) {
		return state, errors.New("invalid resumeId")
	}
	path := checkpointLifecyclePath(resumeID)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return state, errors.New("resume lifecycle not found or not private")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		return state, err
	}
	if state.Version != checkpointLifecycleVersion || subtle.ConstantTimeCompare([]byte(state.ResumeID), []byte(resumeID)) != 1 {
		return state, errors.New("resume lifecycle identity mismatch")
	}
	return state, nil
}

func loadCheckpointLifecycleIfExists(resumeID string) (checkpointLifecycleState, bool, error) {
	var state checkpointLifecycleState
	if !checkpointLifecycleIDPattern.MatchString(resumeID) {
		return state, false, errors.New("invalid lifecycle-id")
	}
	path := checkpointLifecyclePath(resumeID)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return state, false, nil
	} else if err != nil {
		return state, false, err
	}
	state, err := loadCheckpointLifecycle(resumeID)
	return state, err == nil, err
}

func findCheckpointLifecycle(localRoot, sessionID string, generation uint64) (checkpointLifecycleState, bool, error) {
	states, err := listCheckpointLifecycles()
	if err != nil {
		return checkpointLifecycleState{}, false, err
	}
	for _, state := range states {
		if sameCheckpointLocalRoot(state.LocalRoot, localRoot) && state.SessionID == sessionID && state.Generation == generation {
			return state, true, nil
		}
	}
	return checkpointLifecycleState{}, false, nil
}

func rejectStaleCheckpointGeneration(localRoot, sessionID string, generation uint64) error {
	states, err := listCheckpointLifecycles()
	if err != nil {
		return err
	}
	for _, state := range states {
		if sameCheckpointLocalRoot(state.LocalRoot, localRoot) && state.SessionID == sessionID && state.Generation >= generation {
			return fmt.Errorf("generation %d is not newer than lifecycle generation %d", generation, state.Generation)
		}
	}
	return nil
}

func listCheckpointLifecycles() ([]checkpointLifecycleState, error) {
	if err := ensureCheckpointLifecycleDir(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(checkpointLifecycleDir())
	if err != nil {
		return nil, err
	}
	states := make([]checkpointLifecycleState, 0)
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), "lock-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(checkpointLifecycleDir(), entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("lifecycle %s is not a private regular file", entry.Name())
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var state checkpointLifecycleState
		if err := json.Unmarshal(payload, &state); err != nil {
			return nil, fmt.Errorf("parse lifecycle %s: %w", entry.Name(), err)
		}
		states = append(states, state)
	}
	return states, nil
}

func acquireCheckpointLifecycleLock(localRoot string) (func(), error) {
	return acquireCheckpointLifecycleLockContext(nil, localRoot)
}

func acquireCheckpointLifecycleLockWait(ctx context.Context, localRoot string) (func(), error) {
	if ctx == nil {
		return nil, errors.New("checkpoint lifecycle wait requires a context")
	}
	return acquireCheckpointLifecycleLockContext(ctx, localRoot)
}

func acquireCheckpointLifecycleLockContext(waitCtx context.Context, localRoot string) (func(), error) {
	if err := ensureCheckpointLifecycleDir(); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(checkpointLifecycleDir(), "lock-"+checkpointHash(localRoot)+".json")
	nonce, err := newResumeID()
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "nonce": nonce})
	for {
		file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, err := file.Write(payload); err != nil {
				_ = file.Close()
				_ = os.Remove(lockPath)
				return nil, err
			}
			if err := file.Close(); err != nil {
				_ = os.Remove(lockPath)
				return nil, err
			}
			return func() {
				current, _ := os.ReadFile(lockPath)
				if subtle.ConstantTimeCompare(current, payload) == 1 {
					_ = os.Remove(lockPath)
				}
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		var owner struct {
			PID int `json:"pid"`
		}
		current, _ := os.ReadFile(lockPath)
		if json.Unmarshal(current, &owner) == nil && processAlive(owner.PID) {
			if waitCtx == nil {
				return nil, fmt.Errorf("checkpoint lifecycle is active in pid %d", owner.PID)
			}
			select {
			case <-waitCtx.Done():
				return nil, fmt.Errorf("checkpoint lifecycle remained active in pid %d: %w", owner.PID, waitCtx.Err())
			case <-time.After(50 * time.Millisecond):
				continue
			}
		}
		_ = os.Remove(lockPath)
	}
}

func sameCheckpointLocalRoot(a, b string) bool {
	left, err := filepath.Abs(strings.TrimSpace(a))
	if err != nil {
		return false
	}
	right, err := filepath.Abs(strings.TrimSpace(b))
	if err != nil {
		return false
	}
	if resolved, resolveErr := filepath.EvalSymlinks(left); resolveErr == nil {
		left = resolved
	}
	if resolved, resolveErr := filepath.EvalSymlinks(right); resolveErr == nil {
		right = resolved
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
