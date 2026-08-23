package relayfile

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	pathpkg "path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultCheckpointSealTTL         = 60 * time.Second
	MaxCheckpointSealTTL             = 5 * time.Minute
	CheckpointConsumeReplayRetention = 24 * time.Hour
)

var (
	ErrCheckpointDiverged           = errors.New("checkpoint digest does not match durable workspace state")
	ErrCheckpointExpired            = errors.New("checkpoint seal expired")
	ErrCheckpointReplay             = errors.New("checkpoint seal already consumed")
	ErrCheckpointStale              = errors.New("checkpoint seal is stale")
	ErrCheckpointGenerationStale    = errors.New("checkpoint generation is not newer than the last issued generation")
	ErrCheckpointIssuanceConflict   = errors.New("checkpoint issuance idempotency key is bound to a different request or issuer")
	ErrCheckpointConsumerConflict   = errors.New("checkpoint consumer idempotency key is bound to a different seal or identity")
	ErrCheckpointUnconsumed         = errors.New("checkpoint seal has not been consumed")
	ErrCheckpointHandbackRequired   = errors.New("checkpoint ownership has not been released by the destination")
	ErrCheckpointHandbackUnprepared = errors.New("checkpoint handback has not been prepared")
	ErrCheckpointHandbackConflict   = errors.New("checkpoint handback idempotency key is bound to a different release")
	ErrCheckpointResumeConflict     = errors.New("checkpoint source resume idempotency key is bound to a different claim")
	ErrCheckpointAdminConflict      = errors.New("checkpoint administrative reconciliation identity conflicts with durable state")
	checkpointSessionPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
	checkpointRevisionPattern       = regexp.MustCompile(`^(?:0|rev_[0-9]+)$`)
	checkpointEventCursorPattern    = regexp.MustCompile(`^(?:0|evt_[0-9]+)$`)
)

const (
	CheckpointHandbackPhasePrepare = "prepare"
	CheckpointHandbackPhaseCommit  = "commit"
)

type CheckpointDigestEntry struct {
	Path        string `json:"path"`
	Revision    string `json:"revision"`
	ContentHash string `json:"contentHash"`
}

type CheckpointSealRequest struct {
	Root                   string `json:"root"`
	SessionID              string `json:"sessionId"`
	Generation             uint64 `json:"generation"`
	ExpectedDigest         string `json:"expectedDigest"`
	TTLSeconds             int    `json:"ttlSeconds,omitempty"`
	IssuanceIdempotencyKey string `json:"issuanceIdempotencyKey"`
	Issuer                 string `json:"-"`
}

type CheckpointSealConsumeRequest struct {
	SealToken              string `json:"sealToken"`
	Root                   string `json:"root"`
	SessionID              string `json:"sessionId"`
	Generation             uint64 `json:"generation"`
	ConsumerIdempotencyKey string `json:"consumerIdempotencyKey"`
	ConsumerPrincipal      string `json:"-"`
}

// CheckpointSealConsumeRecoveryRequest recovers the safe, tokenless consume
// receipt when the consume response was lost. Cloud persists this stable
// identity before the first consume but intentionally never persists the
// bearer token.
type CheckpointSealConsumeRecoveryRequest struct {
	Root                   string `json:"root"`
	SessionID              string `json:"sessionId"`
	Generation             uint64 `json:"generation"`
	ConsumerIdempotencyKey string `json:"consumerIdempotencyKey"`
	ConsumerPrincipal      string `json:"-"`
}

// CheckpointSealVerifyRequest deliberately excludes SealToken. Destination
// convergence is proven only after Cloud has consumed the one-use bearer, and
// neither the request nor the response may reintroduce it.
type CheckpointSealVerifyRequest struct {
	SealID            string `json:"sealId"`
	Root              string `json:"root"`
	SessionID         string `json:"sessionId"`
	Generation        uint64 `json:"generation"`
	Digest            string `json:"digest"`
	WorkspaceRevision string `json:"workspaceRevision"`
	EventCursor       string `json:"eventCursor"`
	IssuedAt          string `json:"issuedAt"`
	ExpiresAt         string `json:"expiresAt"`
	ConsumedAt        string `json:"consumedAt"`
	ConsumerPrincipal string `json:"-"`
}

// CheckpointSealHandbackRequest is one phase of the destination's final
// ownership-release assertion. Prepare durably binds the stopped destination's
// drained digest without releasing ownership. Commit releases only that exact
// prepared state after the destination performs its closing local scan. The
// original consumer key proves that the same cutover attempt which acquired the
// seal is releasing it. It deliberately contains no seal token.
type CheckpointSealHandbackRequest struct {
	Phase                  string `json:"phase"`
	SealID                 string `json:"sealId"`
	Root                   string `json:"root"`
	SessionID              string `json:"sessionId"`
	Generation             uint64 `json:"generation"`
	ConsumedAt             string `json:"consumedAt"`
	ConsumerIdempotencyKey string `json:"consumerIdempotencyKey"`
	HandbackIdempotencyKey string `json:"handbackIdempotencyKey"`
	ExpectedDigest         string `json:"expectedDigest"`
	ConsumerPrincipal      string `json:"-"`
}

// CheckpointSealResumeRequest is the stopped source's ownership claim. The
// original one-use token remains the source proof; it can only be used here
// after the destination has released ownership.
type CheckpointSealResumeRequest struct {
	SealToken            string `json:"sealToken"`
	Root                 string `json:"root"`
	SessionID            string `json:"sessionId"`
	Generation           uint64 `json:"generation"`
	ResumeIdempotencyKey string `json:"resumeIdempotencyKey"`
}

// CheckpointSealOwnership is a tokenless authoritative handback/resume proof.
type CheckpointSealOwnership struct {
	SealID            string `json:"sealId"`
	WorkspaceID       string `json:"workspaceId"`
	Root              string `json:"root"`
	SessionID         string `json:"sessionId"`
	Generation        uint64 `json:"generation"`
	Status            string `json:"status"`
	Digest            string `json:"digest"`
	WorkspaceRevision string `json:"workspaceRevision"`
	EventCursor       string `json:"eventCursor"`
	ConsumedAt        string `json:"consumedAt,omitempty"`
	PreparedAt        string `json:"preparedAt,omitempty"`
	ReleasedAt        string `json:"releasedAt,omitempty"`
	SourceResumedAt   string `json:"sourceResumedAt,omitempty"`
}

type CheckpointSeal struct {
	SealID            string `json:"sealId"`
	SealToken         string `json:"sealToken,omitempty"`
	WorkspaceID       string `json:"workspaceId"`
	Root              string `json:"root"`
	SessionID         string `json:"sessionId"`
	Generation        uint64 `json:"generation"`
	Digest            string `json:"digest"`
	WorkspaceRevision string `json:"workspaceRevision"`
	EventCursor       string `json:"eventCursor"`
	IssuedAt          string `json:"issuedAt"`
	ExpiresAt         string `json:"expiresAt"`
	ConsumedAt        string `json:"consumedAt,omitempty"`
}

type checkpointSealRecord struct {
	CheckpointSeal
	IssuanceKeyHash       string `json:"issuanceKeyHash,omitempty"`
	IssuanceRequestHash   string `json:"issuanceRequestHash,omitempty"`
	TokenHash             string `json:"tokenHash"`
	Issuer                string `json:"issuer,omitempty"`
	ConsumerKeyHash       string `json:"consumerKeyHash,omitempty"`
	ConsumerPrincipal     string `json:"consumerPrincipal,omitempty"`
	HandbackKeyHash       string `json:"handbackKeyHash,omitempty"`
	HandbackDigest        string `json:"handbackDigest,omitempty"`
	HandbackRevision      string `json:"handbackRevision,omitempty"`
	HandbackEventCursor   string `json:"handbackEventCursor,omitempty"`
	HandbackPreparedAt    string `json:"handbackPreparedAt,omitempty"`
	HandbackReleasedAt    string `json:"handbackReleasedAt,omitempty"`
	SourceResumeKeyHash   string `json:"sourceResumeKeyHash,omitempty"`
	SourceResumedAt       string `json:"sourceResumedAt,omitempty"`
	AdminReconcileKeyHash string `json:"adminReconcileKeyHash,omitempty"`
	AdminReconciledAt     string `json:"adminReconciledAt,omitempty"`
	IdempotencyExpiresAt  string `json:"idempotencyExpiresAt,omitempty"`
}

type checkpointConsumerBinding struct {
	TokenHash   string `json:"tokenHash"`
	WorkspaceID string `json:"workspaceId"`
	Root        string `json:"root"`
	SessionID   string `json:"sessionId"`
	Generation  uint64 `json:"generation"`
	Principal   string `json:"principal"`
	ReplayUntil string `json:"replayUntil"`
}

type CheckpointSealRetentionRecord struct {
	SealID             string `json:"sealId"`
	WorkspaceID        string `json:"workspaceId"`
	Root               string `json:"root"`
	SessionID          string `json:"sessionId"`
	Generation         uint64 `json:"generation"`
	OwnershipStatus    string `json:"ownershipStatus"`
	IssuedAt           string `json:"issuedAt"`
	ExpiresAt          string `json:"expiresAt"`
	ConsumedAt         string `json:"consumedAt,omitempty"`
	HandbackReleasedAt string `json:"handbackReleasedAt,omitempty"`
	SourceResumedAt    string `json:"sourceResumedAt,omitempty"`
	AdminReconciledAt  string `json:"adminReconciledAt,omitempty"`
}

type CheckpointSealRetentionSummary struct {
	GeneratedAt          string                          `json:"generatedAt"`
	UnresumedTotal       int                             `json:"unresumedTotal"`
	UnresumedByWorkspace map[string]int                  `json:"unresumedByWorkspace"`
	Records              []CheckpointSealRetentionRecord `json:"records"`
}

type CheckpointSealAdminReconcileRequest struct {
	WorkspaceID                  string `json:"workspaceId"`
	Root                         string `json:"root"`
	SessionID                    string `json:"sessionId"`
	Generation                   uint64 `json:"generation"`
	ExpectedOwnershipStatus      string `json:"expectedOwnershipStatus"`
	ReconciliationIdempotencyKey string `json:"reconciliationIdempotencyKey"`
	ConfirmSourceReady           bool   `json:"confirmSourceReady"`
}

func NormalizeCheckpointRoot(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.IndexByte(trimmed, 0) >= 0 || !strings.HasPrefix(trimmed, "/") {
		return "", ErrInvalidInput
	}
	cleaned := pathpkg.Clean(trimmed)
	if cleaned != trimmed && !(trimmed != "/" && strings.TrimSuffix(trimmed, "/") == cleaned) {
		return "", ErrInvalidInput
	}
	if cleaned == "." || !strings.HasPrefix(cleaned, "/") {
		return "", ErrInvalidInput
	}
	return cleaned, nil
}

func ComputeCheckpointDigest(root string, entries []CheckpointDigestEntry) (string, error) {
	normalizedRoot, err := NormalizeCheckpointRoot(root)
	if err != nil {
		return "", err
	}
	canonical := append([]CheckpointDigestEntry(nil), entries...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].Path < canonical[j].Path })
	h := sha256.New()
	writeDigestField(h, normalizedRoot)
	lastPath := ""
	for _, entry := range canonical {
		entryPath, err := NormalizeCheckpointRoot(strings.TrimSpace(entry.Path))
		if err != nil || !withinBase(normalizedRoot, entryPath) || entryPath == normalizedRoot {
			return "", ErrInvalidInput
		}
		revision := strings.TrimSpace(entry.Revision)
		contentHash := strings.TrimSpace(entry.ContentHash)
		if revision == "" || contentHash == "" || entryPath == lastPath {
			return "", ErrInvalidInput
		}
		lastPath = entryPath
		writeDigestField(h, entryPath)
		writeDigestField(h, revision)
		writeDigestField(h, contentHash)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

type digestWriter interface {
	Write([]byte) (int, error)
}

func writeDigestField(w digestWriter, value string) {
	_, _ = w.Write([]byte(strconv.Itoa(len(value))))
	_, _ = w.Write([]byte{':'})
	_, _ = w.Write([]byte(value))
	_, _ = w.Write([]byte{0})
}

func (s *Store) IssueCheckpointSeal(workspaceID string, req CheckpointSealRequest, now time.Time) (CheckpointSeal, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	sessionID := strings.TrimSpace(req.SessionID)
	issuanceKey := strings.TrimSpace(req.IssuanceIdempotencyKey)
	issuer := strings.TrimSpace(req.Issuer)
	root, err := NormalizeCheckpointRoot(req.Root)
	if err != nil || workspaceID == "" || !checkpointSessionPattern.MatchString(sessionID) || req.Generation == 0 {
		return CheckpointSeal{}, ErrInvalidInput
	}
	expectedDigest := strings.TrimSpace(req.ExpectedDigest)
	if !strings.HasPrefix(expectedDigest, "sha256:") || len(expectedDigest) != len("sha256:")+sha256.Size*2 {
		return CheckpointSeal{}, ErrInvalidInput
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(expectedDigest, "sha256:")); err != nil {
		return CheckpointSeal{}, ErrInvalidInput
	}
	if req.TTLSeconds < 0 || req.TTLSeconds > int(MaxCheckpointSealTTL/time.Second) {
		return CheckpointSeal{}, ErrInvalidInput
	}
	ttl := DefaultCheckpointSealTTL
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	issuanceRequestHash := checkpointIssuanceRequestHash(workspaceID, root, sessionID, req.Generation, expectedDigest, int(ttl/time.Second))
	issuanceKeyHash := ""
	if issuanceKey != "" && !checkpointSessionPattern.MatchString(issuanceKey) {
		return CheckpointSeal{}, ErrInvalidInput
	}
	if issuanceKey != "" {
		issuanceKeyHash = checkpointTokenHash(issuanceKey)
	}
	persistedIssuanceRequestHash := issuanceRequestHash
	if issuanceKeyHash == "" {
		persistedIssuanceRequestHash = ""
	}
	now = now.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeCheckpointSealsLocked(now)
	if oldTokenHash, replay, ok := s.checkpointSealByIssuanceKeyLocked(issuanceKeyHash); issuanceKeyHash != "" && ok {
		if replay.IssuanceRequestHash != issuanceRequestHash || replay.Issuer != issuer {
			return CheckpointSeal{}, ErrCheckpointIssuanceConflict
		}
		if replay.ConsumedAt != "" || replay.HandbackReleasedAt != "" || replay.SourceResumedAt != "" {
			return CheckpointSeal{}, ErrCheckpointReplay
		}
		expiresAt, parseErr := time.Parse(time.RFC3339Nano, replay.ExpiresAt)
		if parseErr != nil || !now.Before(expiresAt) {
			return CheckpointSeal{}, ErrCheckpointExpired
		}
		digest, workspaceRevision, cursor, stateErr := s.checkpointStateLocked(workspaceID, root)
		if stateErr != nil {
			return CheckpointSeal{}, stateErr
		}
		if digest != replay.Digest || workspaceRevision != replay.WorkspaceRevision || cursor != replay.EventCursor {
			return CheckpointSeal{}, ErrCheckpointStale
		}
		// A response-loss retry cannot recover a plaintext bearer from its
		// stored hash. Rotate it atomically while preserving the seal identity
		// and immutable attestation; the lost token becomes unusable.
		token, tokenErr := newCheckpointToken()
		if tokenErr != nil {
			return CheckpointSeal{}, tokenErr
		}
		rotated := replay
		rotated.TokenHash = checkpointTokenHash(token)
		delete(s.checkpointSeals, oldTokenHash)
		s.checkpointSeals[rotated.TokenHash] = rotated
		if saveErr := s.saveLocked(); saveErr != nil {
			delete(s.checkpointSeals, rotated.TokenHash)
			s.checkpointSeals[oldTokenHash] = replay
			return CheckpointSeal{}, saveErr
		}
		response := rotated.CheckpointSeal
		response.SealToken = token
		return response, nil
	}
	key := checkpointGenerationKey(workspaceID, root, sessionID)
	previousGeneration := s.checkpointGenerations[key]
	if req.Generation <= previousGeneration {
		return CheckpointSeal{}, ErrCheckpointGenerationStale
	}
	digest, workspaceRevision, cursor, err := s.checkpointStateLocked(workspaceID, root)
	if err != nil {
		return CheckpointSeal{}, err
	}
	if digest != expectedDigest {
		return CheckpointSeal{}, ErrCheckpointDiverged
	}
	token, err := newCheckpointToken()
	if err != nil {
		return CheckpointSeal{}, err
	}
	sealID := "cps_" + token[:16]
	record := checkpointSealRecord{
		CheckpointSeal: CheckpointSeal{
			SealID:            sealID,
			WorkspaceID:       workspaceID,
			Root:              root,
			SessionID:         sessionID,
			Generation:        req.Generation,
			Digest:            digest,
			WorkspaceRevision: workspaceRevision,
			EventCursor:       cursor,
			IssuedAt:          now.Format(time.RFC3339Nano),
			ExpiresAt:         now.Add(ttl).Format(time.RFC3339Nano),
		},
		IssuanceKeyHash:     issuanceKeyHash,
		IssuanceRequestHash: persistedIssuanceRequestHash,
		TokenHash:           checkpointTokenHash(token),
		Issuer:              issuer,
	}
	if s.checkpointSeals == nil {
		s.checkpointSeals = map[string]checkpointSealRecord{}
	}
	if s.checkpointGenerations == nil {
		s.checkpointGenerations = map[string]uint64{}
	}
	s.checkpointSeals[record.TokenHash] = record
	s.checkpointGenerations[key] = req.Generation
	if err := s.saveLocked(); err != nil {
		delete(s.checkpointSeals, record.TokenHash)
		if previousGeneration == 0 {
			delete(s.checkpointGenerations, key)
		} else {
			s.checkpointGenerations[key] = previousGeneration
		}
		return CheckpointSeal{}, err
	}
	response := record.CheckpointSeal
	response.SealToken = token
	return response, nil
}

func (s *Store) ConsumeCheckpointSeal(workspaceID string, req CheckpointSealConsumeRequest, now time.Time) (CheckpointSeal, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	sessionID := strings.TrimSpace(req.SessionID)
	consumerKey := strings.TrimSpace(req.ConsumerIdempotencyKey)
	consumerPrincipal := strings.TrimSpace(req.ConsumerPrincipal)
	root, err := NormalizeCheckpointRoot(req.Root)
	if err != nil || workspaceID == "" || !checkpointSessionPattern.MatchString(sessionID) || req.Generation == 0 || strings.TrimSpace(req.SealToken) == "" || !checkpointSessionPattern.MatchString(consumerKey) || consumerPrincipal == "" {
		return CheckpointSeal{}, ErrInvalidInput
	}
	now = now.UTC()
	tokenHash := checkpointTokenHash(strings.TrimSpace(req.SealToken))
	consumerKeyHash := checkpointTokenHash(consumerKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeCheckpointSealsLocked(now)
	if binding, exists := s.checkpointConsumerKeys[consumerKeyHash]; exists && !checkpointConsumerBindingMatches(binding, tokenHash, workspaceID, root, sessionID, req.Generation, consumerPrincipal) {
		return CheckpointSeal{}, ErrCheckpointConsumerConflict
	}
	record, ok := s.checkpointSeals[tokenHash]
	if !ok {
		return CheckpointSeal{}, ErrNotFound
	}
	if record.WorkspaceID != workspaceID || record.Root != root || record.SessionID != sessionID || record.Generation != req.Generation {
		return CheckpointSeal{}, ErrInvalidInput
	}
	if record.HandbackReleasedAt != "" || record.SourceResumedAt != "" {
		return CheckpointSeal{}, ErrCheckpointReplay
	}
	if record.ConsumedAt != "" {
		if record.ConsumerKeyHash == consumerKeyHash && record.ConsumerPrincipal == consumerPrincipal {
			return record.CheckpointSeal, nil
		}
		return CheckpointSeal{}, ErrCheckpointReplay
	}
	expiresAt, parseErr := time.Parse(time.RFC3339Nano, record.ExpiresAt)
	if parseErr != nil || !now.Before(expiresAt) {
		return CheckpointSeal{}, ErrCheckpointExpired
	}
	digest, workspaceRevision, cursor, err := s.checkpointStateLocked(workspaceID, root)
	if err != nil {
		return CheckpointSeal{}, err
	}
	if digest != record.Digest || workspaceRevision != record.WorkspaceRevision || cursor != record.EventCursor {
		return CheckpointSeal{}, ErrCheckpointStale
	}
	record.ConsumedAt = now.Format(time.RFC3339Nano)
	record.ConsumerKeyHash = consumerKeyHash
	record.ConsumerPrincipal = consumerPrincipal
	record.IdempotencyExpiresAt = now.Add(CheckpointConsumeReplayRetention).Format(time.RFC3339Nano)
	s.checkpointSeals[tokenHash] = record
	if s.checkpointConsumerKeys == nil {
		s.checkpointConsumerKeys = map[string]checkpointConsumerBinding{}
	}
	s.checkpointConsumerKeys[consumerKeyHash] = checkpointConsumerBinding{
		TokenHash: tokenHash, WorkspaceID: workspaceID, Root: root,
		SessionID: sessionID, Generation: req.Generation,
		Principal:   consumerPrincipal,
		ReplayUntil: record.IdempotencyExpiresAt,
	}
	if err := s.saveLocked(); err != nil {
		record.ConsumedAt = ""
		record.ConsumerKeyHash = ""
		record.ConsumerPrincipal = ""
		record.IdempotencyExpiresAt = ""
		s.checkpointSeals[tokenHash] = record
		delete(s.checkpointConsumerKeys, consumerKeyHash)
		return CheckpointSeal{}, err
	}
	return record.CheckpointSeal, nil
}

// RecoverConsumedCheckpointSeal returns only the durable tokenless consume
// receipt bound to a controller-persisted consumer identity. NotFound is an
// authoritative statement that no matching consume committed.
func (s *Store) RecoverConsumedCheckpointSeal(workspaceID string, req CheckpointSealConsumeRecoveryRequest, now time.Time) (CheckpointSeal, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	sessionID := strings.TrimSpace(req.SessionID)
	consumerKey := strings.TrimSpace(req.ConsumerIdempotencyKey)
	consumerPrincipal := strings.TrimSpace(req.ConsumerPrincipal)
	root, err := NormalizeCheckpointRoot(req.Root)
	if err != nil || workspaceID == "" || !checkpointSessionPattern.MatchString(sessionID) || req.Generation == 0 || !checkpointSessionPattern.MatchString(consumerKey) || consumerPrincipal == "" {
		return CheckpointSeal{}, ErrInvalidInput
	}
	consumerKeyHash := checkpointTokenHash(consumerKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeCheckpointSealsLocked(now.UTC())
	binding, ok := s.checkpointConsumerKeys[consumerKeyHash]
	if !ok {
		return CheckpointSeal{}, ErrNotFound
	}
	if binding.WorkspaceID != workspaceID || binding.Root != root || binding.SessionID != sessionID || binding.Generation != req.Generation || binding.Principal != consumerPrincipal {
		return CheckpointSeal{}, ErrCheckpointConsumerConflict
	}
	record, ok := s.checkpointSeals[binding.TokenHash]
	if !ok || record.ConsumerKeyHash != consumerKeyHash || record.ConsumerPrincipal != consumerPrincipal || record.ConsumedAt == "" {
		return CheckpointSeal{}, ErrNotFound
	}
	response := record.CheckpointSeal
	response.SealToken = ""
	return response, nil
}

// VerifyConsumedCheckpointSeal re-attests that an exact consumed seal still
// describes current durable workspace state. It is intentionally read-only:
// the destination's local verifier supplies the independent filesystem proof,
// while this method prevents a caller from inventing or mutating a receipt and
// handles workspace revisions (including delete-only revisions) authoritatively.
func (s *Store) VerifyConsumedCheckpointSeal(workspaceID string, req CheckpointSealVerifyRequest, now time.Time) (CheckpointSeal, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	consumerPrincipal := strings.TrimSpace(req.ConsumerPrincipal)
	root, err := NormalizeCheckpointRoot(req.Root)
	if err != nil || workspaceID == "" || strings.TrimSpace(req.SealID) == "" || !checkpointSessionPattern.MatchString(strings.TrimSpace(req.SessionID)) || req.Generation == 0 || consumerPrincipal == "" {
		return CheckpointSeal{}, ErrInvalidInput
	}
	if !validCheckpointDigest(req.Digest) || !checkpointRevisionPattern.MatchString(strings.TrimSpace(req.WorkspaceRevision)) || !checkpointEventCursorPattern.MatchString(strings.TrimSpace(req.EventCursor)) || strings.TrimSpace(req.ConsumedAt) == "" {
		return CheckpointSeal{}, ErrInvalidInput
	}
	for _, raw := range []string{req.IssuedAt, req.ExpiresAt, req.ConsumedAt} {
		if _, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw)); err != nil {
			return CheckpointSeal{}, ErrInvalidInput
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeCheckpointSealsLocked(now.UTC())
	var record checkpointSealRecord
	found := false
	for _, candidate := range s.checkpointSeals {
		if candidate.SealID == strings.TrimSpace(req.SealID) {
			record = candidate
			found = true
			break
		}
	}
	if !found {
		return CheckpointSeal{}, ErrNotFound
	}
	if strings.TrimSpace(record.ConsumedAt) == "" {
		return CheckpointSeal{}, ErrCheckpointUnconsumed
	}
	if strings.TrimSpace(record.HandbackReleasedAt) != "" || strings.TrimSpace(record.SourceResumedAt) != "" {
		return CheckpointSeal{}, ErrCheckpointReplay
	}
	if record.ConsumerPrincipal != consumerPrincipal {
		return CheckpointSeal{}, ErrCheckpointConsumerConflict
	}
	if record.WorkspaceID != workspaceID || record.Root != root || record.SessionID != strings.TrimSpace(req.SessionID) || record.Generation != req.Generation ||
		record.Digest != strings.TrimSpace(req.Digest) || record.WorkspaceRevision != strings.TrimSpace(req.WorkspaceRevision) || record.EventCursor != strings.TrimSpace(req.EventCursor) ||
		record.IssuedAt != strings.TrimSpace(req.IssuedAt) || record.ExpiresAt != strings.TrimSpace(req.ExpiresAt) || record.ConsumedAt != strings.TrimSpace(req.ConsumedAt) {
		return CheckpointSeal{}, ErrCheckpointStale
	}
	digest, workspaceRevision, cursor, err := s.checkpointStateLocked(workspaceID, root)
	if err != nil {
		return CheckpointSeal{}, err
	}
	if digest != record.Digest || workspaceRevision != record.WorkspaceRevision || cursor != record.EventCursor {
		return CheckpointSeal{}, ErrCheckpointStale
	}
	response := record.CheckpointSeal
	response.SealToken = ""
	return response, nil
}

// HandbackCheckpointSeal prepares or commits a consumed destination handback.
// Prepare persists the exact durable state while ownership remains with the
// destination. Commit releases ownership only if that prepared state is still
// current. Exact retries return the durable phase result; changed retry
// identities fail closed.
func (s *Store) HandbackCheckpointSeal(workspaceID string, req CheckpointSealHandbackRequest, now time.Time) (CheckpointSealOwnership, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	phase := strings.TrimSpace(req.Phase)
	sealID := strings.TrimSpace(req.SealID)
	sessionID := strings.TrimSpace(req.SessionID)
	consumerKey := strings.TrimSpace(req.ConsumerIdempotencyKey)
	consumerPrincipal := strings.TrimSpace(req.ConsumerPrincipal)
	handbackKey := strings.TrimSpace(req.HandbackIdempotencyKey)
	consumedAt := strings.TrimSpace(req.ConsumedAt)
	expectedDigest := strings.TrimSpace(req.ExpectedDigest)
	root, err := NormalizeCheckpointRoot(req.Root)
	if err != nil || (phase != CheckpointHandbackPhasePrepare && phase != CheckpointHandbackPhaseCommit) || workspaceID == "" || sealID == "" || !checkpointSessionPattern.MatchString(sessionID) || req.Generation == 0 ||
		!checkpointSessionPattern.MatchString(consumerKey) || !checkpointSessionPattern.MatchString(handbackKey) || consumerPrincipal == "" || !validCheckpointDigest(expectedDigest) || consumedAt == "" {
		return CheckpointSealOwnership{}, ErrInvalidInput
	}
	if _, err := time.Parse(time.RFC3339Nano, consumedAt); err != nil {
		return CheckpointSealOwnership{}, ErrInvalidInput
	}
	now = now.UTC()
	consumerKeyHash := checkpointTokenHash(consumerKey)
	handbackKeyHash := checkpointTokenHash(handbackKey)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeCheckpointSealsLocked(now)
	tokenHash, record, ok := s.checkpointSealByIDLocked(sealID)
	if !ok {
		return CheckpointSealOwnership{}, ErrNotFound
	}
	if record.ConsumedAt == "" {
		return CheckpointSealOwnership{}, ErrCheckpointUnconsumed
	}
	if record.WorkspaceID != workspaceID || record.Root != root || record.SessionID != sessionID || record.Generation != req.Generation || record.ConsumedAt != consumedAt {
		return CheckpointSealOwnership{}, ErrCheckpointStale
	}
	if record.ConsumerKeyHash != consumerKeyHash || record.ConsumerPrincipal != consumerPrincipal {
		return CheckpointSealOwnership{}, ErrCheckpointConsumerConflict
	}
	if record.HandbackReleasedAt != "" {
		if record.HandbackKeyHash != handbackKeyHash || record.HandbackDigest != expectedDigest {
			return CheckpointSealOwnership{}, ErrCheckpointHandbackConflict
		}
		return checkpointOwnershipFromRecord(record, "released"), nil
	}
	if record.SourceResumedAt != "" {
		return CheckpointSealOwnership{}, ErrCheckpointResumeConflict
	}
	digest, revision, cursor, err := s.checkpointStateLocked(workspaceID, root)
	if err != nil {
		return CheckpointSealOwnership{}, err
	}

	if record.HandbackPreparedAt != "" {
		if record.HandbackKeyHash != handbackKeyHash || record.HandbackDigest != expectedDigest {
			return CheckpointSealOwnership{}, ErrCheckpointHandbackConflict
		}
		if digest != record.HandbackDigest || revision != record.HandbackRevision || cursor != record.HandbackEventCursor {
			return CheckpointSealOwnership{}, ErrCheckpointDiverged
		}
		if phase == CheckpointHandbackPhasePrepare {
			return checkpointOwnershipFromRecord(record, "prepared"), nil
		}
		previous := record
		record.HandbackReleasedAt = now.Format(time.RFC3339Nano)
		record.IdempotencyExpiresAt = now.Add(CheckpointConsumeReplayRetention).Format(time.RFC3339Nano)
		s.checkpointSeals[tokenHash] = record
		if err := s.saveLocked(); err != nil {
			s.checkpointSeals[tokenHash] = previous
			return CheckpointSealOwnership{}, err
		}
		return checkpointOwnershipFromRecord(record, "released"), nil
	}

	if phase == CheckpointHandbackPhaseCommit {
		return CheckpointSealOwnership{}, ErrCheckpointHandbackUnprepared
	}
	if digest != expectedDigest {
		return CheckpointSealOwnership{}, ErrCheckpointDiverged
	}
	previous := record
	record.HandbackKeyHash = handbackKeyHash
	record.HandbackDigest = digest
	record.HandbackRevision = revision
	record.HandbackEventCursor = cursor
	record.HandbackPreparedAt = now.Format(time.RFC3339Nano)
	record.IdempotencyExpiresAt = now.Add(CheckpointConsumeReplayRetention).Format(time.RFC3339Nano)
	s.checkpointSeals[tokenHash] = record
	if err := s.saveLocked(); err != nil {
		s.checkpointSeals[tokenHash] = previous
		return CheckpointSealOwnership{}, err
	}
	return checkpointOwnershipFromRecord(record, "prepared"), nil
}

// ResumeCheckpointSeal returns ownership to the stopped source. An
// unconsumed seal may be cancelled directly; once a destination consumed it,
// the destination's durable handback is mandatory. The original token is the
// source proof and never appears in the response.
func (s *Store) ResumeCheckpointSeal(workspaceID string, req CheckpointSealResumeRequest, now time.Time) (CheckpointSealOwnership, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	sessionID := strings.TrimSpace(req.SessionID)
	resumeKey := strings.TrimSpace(req.ResumeIdempotencyKey)
	root, err := NormalizeCheckpointRoot(req.Root)
	if err != nil || workspaceID == "" || !checkpointSessionPattern.MatchString(sessionID) || req.Generation == 0 ||
		strings.TrimSpace(req.SealToken) == "" || !checkpointSessionPattern.MatchString(resumeKey) {
		return CheckpointSealOwnership{}, ErrInvalidInput
	}
	now = now.UTC()
	tokenHash := checkpointTokenHash(strings.TrimSpace(req.SealToken))
	resumeKeyHash := checkpointTokenHash(resumeKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeCheckpointSealsLocked(now)
	record, ok := s.checkpointSeals[tokenHash]
	if !ok {
		return CheckpointSealOwnership{}, ErrNotFound
	}
	if record.WorkspaceID != workspaceID || record.Root != root || record.SessionID != sessionID || record.Generation != req.Generation {
		return CheckpointSealOwnership{}, ErrInvalidInput
	}
	if record.SourceResumedAt != "" {
		if record.SourceResumeKeyHash != resumeKeyHash {
			return CheckpointSealOwnership{}, ErrCheckpointResumeConflict
		}
		return checkpointOwnershipFromRecord(record, "source-resumed"), nil
	}
	if record.ConsumedAt != "" && record.HandbackReleasedAt == "" {
		return CheckpointSealOwnership{}, ErrCheckpointHandbackRequired
	}
	previous := record
	if record.HandbackReleasedAt == "" {
		digest, revision, cursor, stateErr := s.checkpointStateLocked(workspaceID, root)
		if stateErr != nil {
			return CheckpointSealOwnership{}, stateErr
		}
		record.HandbackDigest = digest
		record.HandbackRevision = revision
		record.HandbackEventCursor = cursor
		record.HandbackReleasedAt = now.Format(time.RFC3339Nano)
	}
	record.SourceResumeKeyHash = resumeKeyHash
	record.SourceResumedAt = now.Format(time.RFC3339Nano)
	record.IdempotencyExpiresAt = now.Add(CheckpointConsumeReplayRetention).Format(time.RFC3339Nano)
	s.checkpointSeals[tokenHash] = record
	if err := s.saveLocked(); err != nil {
		s.checkpointSeals[tokenHash] = previous
		return CheckpointSealOwnership{}, err
	}
	return checkpointOwnershipFromRecord(record, "source-resumed"), nil
}

func (s *Store) checkpointSealByIDLocked(sealID string) (string, checkpointSealRecord, bool) {
	for tokenHash, record := range s.checkpointSeals {
		if record.SealID == sealID {
			return tokenHash, record, true
		}
	}
	return "", checkpointSealRecord{}, false
}

func (s *Store) checkpointSealByIssuanceKeyLocked(keyHash string) (string, checkpointSealRecord, bool) {
	if keyHash == "" {
		return "", checkpointSealRecord{}, false
	}
	for tokenHash, record := range s.checkpointSeals {
		if record.IssuanceKeyHash == keyHash {
			return tokenHash, record, true
		}
	}
	return "", checkpointSealRecord{}, false
}

func checkpointOwnershipFromRecord(record checkpointSealRecord, status string) CheckpointSealOwnership {
	return CheckpointSealOwnership{
		SealID: record.SealID, WorkspaceID: record.WorkspaceID, Root: record.Root,
		SessionID: record.SessionID, Generation: record.Generation, Status: status,
		Digest: record.HandbackDigest, WorkspaceRevision: record.HandbackRevision,
		EventCursor: record.HandbackEventCursor, ConsumedAt: record.ConsumedAt,
		PreparedAt: record.HandbackPreparedAt, ReleasedAt: record.HandbackReleasedAt,
		SourceResumedAt: record.SourceResumedAt,
	}
}

func validCheckpointDigest(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func checkpointConsumerBindingMatches(binding checkpointConsumerBinding, tokenHash, workspaceID, root, sessionID string, generation uint64, principal string) bool {
	return binding.TokenHash == tokenHash && binding.WorkspaceID == workspaceID && binding.Root == root && binding.SessionID == sessionID && binding.Generation == generation && binding.Principal == principal
}

// GetCheckpointSealRetentionSummary exposes bounded operational visibility
// without returning bearer material or internal hashes. Unresumed records are
// intentionally retained fail-closed until an ordinary source resume or an
// explicit break-glass administrative reconciliation proves source readiness.
func (s *Store) GetCheckpointSealRetentionSummary(workspaceID string, now time.Time) CheckpointSealRetentionSummary {
	workspaceID = strings.TrimSpace(workspaceID)
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeCheckpointSealsLocked(now)
	summary := CheckpointSealRetentionSummary{
		GeneratedAt:          now.Format(time.RFC3339Nano),
		UnresumedByWorkspace: map[string]int{},
		Records:              []CheckpointSealRetentionRecord{},
	}
	for _, record := range s.checkpointSeals {
		if record.SourceResumedAt != "" || (workspaceID != "" && record.WorkspaceID != workspaceID) {
			continue
		}
		summary.UnresumedTotal++
		summary.UnresumedByWorkspace[record.WorkspaceID]++
		summary.Records = append(summary.Records, checkpointRetentionRecord(record))
	}
	sort.Slice(summary.Records, func(i, j int) bool {
		if summary.Records[i].WorkspaceID != summary.Records[j].WorkspaceID {
			return summary.Records[i].WorkspaceID < summary.Records[j].WorkspaceID
		}
		if summary.Records[i].IssuedAt != summary.Records[j].IssuedAt {
			return summary.Records[i].IssuedAt < summary.Records[j].IssuedAt
		}
		return summary.Records[i].SealID < summary.Records[j].SealID
	})
	return summary
}

// ReconcileCheckpointSealSource is a break-glass administrative path. It can
// resolve only states where destination ownership is absent (never a consumed,
// unreleased seal), requires an exact durable identity/status fence and an
// explicit assertion that the source is ready, and is idempotent across lost
// responses. The record becomes a bounded tombstone; any consumer-key binding
// is removed immediately and the tombstone expires after replay retention.
func (s *Store) ReconcileCheckpointSealSource(sealID string, req CheckpointSealAdminReconcileRequest, now time.Time) (CheckpointSealRetentionRecord, error) {
	sealID = strings.TrimSpace(sealID)
	workspaceID := strings.TrimSpace(req.WorkspaceID)
	sessionID := strings.TrimSpace(req.SessionID)
	expectedStatus := strings.TrimSpace(req.ExpectedOwnershipStatus)
	reconciliationKey := strings.TrimSpace(req.ReconciliationIdempotencyKey)
	root, err := NormalizeCheckpointRoot(req.Root)
	if err != nil || sealID == "" || workspaceID == "" || !checkpointSessionPattern.MatchString(sessionID) || req.Generation == 0 ||
		(expectedStatus != "unconsumed" && expectedStatus != "released") || !checkpointSessionPattern.MatchString(reconciliationKey) || !req.ConfirmSourceReady {
		return CheckpointSealRetentionRecord{}, ErrInvalidInput
	}
	now = now.UTC()
	reconciliationKeyHash := checkpointTokenHash(reconciliationKey)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeCheckpointSealsLocked(now)
	tokenHash, record, ok := s.checkpointSealByIDLocked(sealID)
	if !ok {
		return CheckpointSealRetentionRecord{}, ErrNotFound
	}
	if record.WorkspaceID != workspaceID || record.Root != root || record.SessionID != sessionID || record.Generation != req.Generation {
		return CheckpointSealRetentionRecord{}, ErrCheckpointAdminConflict
	}
	if record.SourceResumedAt != "" {
		if record.AdminReconcileKeyHash == reconciliationKeyHash {
			return checkpointRetentionRecord(record), nil
		}
		return CheckpointSealRetentionRecord{}, ErrCheckpointAdminConflict
	}
	status := checkpointOwnershipStatus(record)
	if status == "consumed" {
		return CheckpointSealRetentionRecord{}, ErrCheckpointHandbackRequired
	}
	if status != expectedStatus {
		return CheckpointSealRetentionRecord{}, ErrCheckpointAdminConflict
	}
	previous := record
	var previousBinding checkpointConsumerBinding
	hadBinding := false
	if record.ConsumerKeyHash != "" {
		previousBinding, hadBinding = s.checkpointConsumerKeys[record.ConsumerKeyHash]
		delete(s.checkpointConsumerKeys, record.ConsumerKeyHash)
	}
	reconciledAt := now.Format(time.RFC3339Nano)
	record.SourceResumedAt = reconciledAt
	record.AdminReconciledAt = reconciledAt
	record.AdminReconcileKeyHash = reconciliationKeyHash
	record.IdempotencyExpiresAt = now.Add(CheckpointConsumeReplayRetention).Format(time.RFC3339Nano)
	s.checkpointSeals[tokenHash] = record
	if err := s.saveLocked(); err != nil {
		s.checkpointSeals[tokenHash] = previous
		if hadBinding {
			s.checkpointConsumerKeys[record.ConsumerKeyHash] = previousBinding
		}
		return CheckpointSealRetentionRecord{}, err
	}
	return checkpointRetentionRecord(record), nil
}

func checkpointRetentionRecord(record checkpointSealRecord) CheckpointSealRetentionRecord {
	return CheckpointSealRetentionRecord{
		SealID: record.SealID, WorkspaceID: record.WorkspaceID, Root: record.Root,
		SessionID: record.SessionID, Generation: record.Generation,
		OwnershipStatus: checkpointOwnershipStatus(record), IssuedAt: record.IssuedAt,
		ExpiresAt: record.ExpiresAt, ConsumedAt: record.ConsumedAt,
		HandbackReleasedAt: record.HandbackReleasedAt, SourceResumedAt: record.SourceResumedAt,
		AdminReconciledAt: record.AdminReconciledAt,
	}
}

func checkpointOwnershipStatus(record checkpointSealRecord) string {
	if record.SourceResumedAt != "" {
		return "source-resumed"
	}
	if record.HandbackReleasedAt != "" {
		return "released"
	}
	if record.ConsumedAt != "" {
		return "consumed"
	}
	return "unconsumed"
}

func (s *Store) purgeCheckpointSealsLocked(now time.Time) {
	for tokenHash, record := range s.checkpointSeals {
		// Every seal represents a stopped source until that source explicitly
		// resumes. Expiry only closes destination consume; it is never evidence
		// that source ownership was restored. Likewise, destination handback does
		// not prove the source came back. Retain both unconsumed and released
		// records indefinitely until SourceResumedAt is durable.
		if record.SourceResumedAt == "" {
			continue
		}
		replayUntil, err := time.Parse(time.RFC3339Nano, record.IdempotencyExpiresAt)
		if err == nil && now.Before(replayUntil) {
			continue
		}
		delete(s.checkpointSeals, tokenHash)
		if record.ConsumerKeyHash != "" {
			delete(s.checkpointConsumerKeys, record.ConsumerKeyHash)
		}
	}
}

func (s *Store) checkpointStateLocked(workspaceID, root string) (digest, workspaceRevision, cursor string, err error) {
	entries := []CheckpointDigestEntry{}
	workspaceRevision = s.currentWorkspaceRevisionLocked(workspaceID)
	cursor = "0"
	ws, ok := s.workspaces[workspaceID]
	if ok {
		paths := make([]string, 0, len(ws.Files))
		for filePath := range ws.Files {
			if withinBase(root, filePath) && normalizePath(filePath) != root {
				paths = append(paths, normalizePath(filePath))
			}
		}
		sort.Strings(paths)
		for _, filePath := range paths {
			file := ws.Files[filePath]
			entries = append(entries, CheckpointDigestEntry{Path: filePath, Revision: file.Revision, ContentHash: storedContentHashForFile(file)})
		}
		if len(ws.Events) > 0 {
			cursor = strings.TrimSpace(ws.Events[len(ws.Events)-1].EventID)
		}
	}
	digest, err = ComputeCheckpointDigest(root, entries)
	return digest, workspaceRevision, cursor, err
}

func checkpointGenerationKey(workspaceID, root, sessionID string) string {
	return strings.Join([]string{strings.TrimSpace(workspaceID), root, strings.TrimSpace(sessionID)}, "\x00")
}

func checkpointIssuanceRequestHash(workspaceID, root, sessionID string, generation uint64, expectedDigest string, ttlSeconds int) string {
	h := sha256.New()
	for _, value := range []string{
		strings.TrimSpace(workspaceID), root, strings.TrimSpace(sessionID),
		strconv.FormatUint(generation, 10), strings.TrimSpace(expectedDigest), strconv.Itoa(ttlSeconds),
	} {
		writeDigestField(h, value)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func newCheckpointToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate checkpoint seal token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func checkpointTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
