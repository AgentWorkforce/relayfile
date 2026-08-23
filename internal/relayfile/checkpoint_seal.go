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
	ErrCheckpointDiverged         = errors.New("checkpoint digest does not match durable workspace state")
	ErrCheckpointExpired          = errors.New("checkpoint seal expired")
	ErrCheckpointReplay           = errors.New("checkpoint seal already consumed")
	ErrCheckpointStale            = errors.New("checkpoint seal is stale")
	ErrCheckpointGenerationStale  = errors.New("checkpoint generation is not newer than the last issued generation")
	ErrCheckpointConsumerConflict = errors.New("checkpoint consumer idempotency key is bound to a different seal or identity")
	ErrCheckpointUnconsumed       = errors.New("checkpoint seal has not been consumed")
	ErrCheckpointHandbackRequired = errors.New("checkpoint ownership has not been released by the destination")
	ErrCheckpointHandbackConflict = errors.New("checkpoint handback idempotency key is bound to a different release")
	ErrCheckpointResumeConflict   = errors.New("checkpoint source resume idempotency key is bound to a different claim")
	checkpointSessionPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
	checkpointRevisionPattern     = regexp.MustCompile(`^(?:0|rev_[0-9]+)$`)
	checkpointEventCursorPattern  = regexp.MustCompile(`^(?:0|evt_[0-9]+)$`)
)

type CheckpointDigestEntry struct {
	Path        string `json:"path"`
	Revision    string `json:"revision"`
	ContentHash string `json:"contentHash"`
}

type CheckpointSealRequest struct {
	Root           string `json:"root"`
	SessionID      string `json:"sessionId"`
	Generation     uint64 `json:"generation"`
	ExpectedDigest string `json:"expectedDigest"`
	TTLSeconds     int    `json:"ttlSeconds,omitempty"`
	Issuer         string `json:"-"`
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

// CheckpointSealHandbackRequest is the destination's final ownership-release
// assertion. The original consumer key proves that the same cutover attempt
// which acquired the seal is releasing it; the final digest proves the stopped
// destination drained before the server changes ownership. Revision and cursor
// are captured server-side in the resulting proof because the destination's
// final write can advance them before its local mount observes the emitted event.
// It deliberately contains no seal token.
type CheckpointSealHandbackRequest struct {
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
	ConsumedAt        string `json:"consumedAt"`
	ReleasedAt        string `json:"releasedAt"`
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
	TokenHash            string `json:"tokenHash"`
	Issuer               string `json:"issuer,omitempty"`
	ConsumerKeyHash      string `json:"consumerKeyHash,omitempty"`
	ConsumerPrincipal    string `json:"consumerPrincipal,omitempty"`
	HandbackKeyHash      string `json:"handbackKeyHash,omitempty"`
	HandbackDigest       string `json:"handbackDigest,omitempty"`
	HandbackRevision     string `json:"handbackRevision,omitempty"`
	HandbackEventCursor  string `json:"handbackEventCursor,omitempty"`
	HandbackReleasedAt   string `json:"handbackReleasedAt,omitempty"`
	SourceResumeKeyHash  string `json:"sourceResumeKeyHash,omitempty"`
	SourceResumedAt      string `json:"sourceResumedAt,omitempty"`
	IdempotencyExpiresAt string `json:"idempotencyExpiresAt,omitempty"`
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
	now = now.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeCheckpointSealsLocked(now)
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
		TokenHash: checkpointTokenHash(token),
		Issuer:    strings.TrimSpace(req.Issuer),
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

// HandbackCheckpointSeal releases a consumed destination only after the same
// consumer proves a stable, fully drained local view that matches current
// durable Relayfile state. Exact retries return the original proof even if the
// workspace subsequently changes; changed retry identities fail closed.
func (s *Store) HandbackCheckpointSeal(workspaceID string, req CheckpointSealHandbackRequest, now time.Time) (CheckpointSealOwnership, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	sealID := strings.TrimSpace(req.SealID)
	sessionID := strings.TrimSpace(req.SessionID)
	consumerKey := strings.TrimSpace(req.ConsumerIdempotencyKey)
	consumerPrincipal := strings.TrimSpace(req.ConsumerPrincipal)
	handbackKey := strings.TrimSpace(req.HandbackIdempotencyKey)
	consumedAt := strings.TrimSpace(req.ConsumedAt)
	expectedDigest := strings.TrimSpace(req.ExpectedDigest)
	root, err := NormalizeCheckpointRoot(req.Root)
	if err != nil || workspaceID == "" || sealID == "" || !checkpointSessionPattern.MatchString(sessionID) || req.Generation == 0 ||
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
	if record.WorkspaceID != workspaceID || record.Root != root || record.SessionID != sessionID || record.Generation != req.Generation || record.ConsumedAt != consumedAt {
		return CheckpointSealOwnership{}, ErrCheckpointStale
	}
	if record.ConsumedAt == "" {
		return CheckpointSealOwnership{}, ErrCheckpointUnconsumed
	}
	if record.ConsumerKeyHash != consumerKeyHash || record.ConsumerPrincipal != consumerPrincipal {
		return CheckpointSealOwnership{}, ErrCheckpointConsumerConflict
	}
	if record.HandbackReleasedAt != "" {
		if record.HandbackKeyHash != handbackKeyHash {
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
	if digest != expectedDigest {
		return CheckpointSealOwnership{}, ErrCheckpointDiverged
	}
	previous := record
	record.HandbackKeyHash = handbackKeyHash
	record.HandbackDigest = digest
	record.HandbackRevision = revision
	record.HandbackEventCursor = cursor
	record.HandbackReleasedAt = now.Format(time.RFC3339Nano)
	record.IdempotencyExpiresAt = now.Add(CheckpointConsumeReplayRetention).Format(time.RFC3339Nano)
	s.checkpointSeals[tokenHash] = record
	if err := s.saveLocked(); err != nil {
		s.checkpointSeals[tokenHash] = previous
		return CheckpointSealOwnership{}, err
	}
	return checkpointOwnershipFromRecord(record, "released"), nil
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

func checkpointOwnershipFromRecord(record checkpointSealRecord, status string) CheckpointSealOwnership {
	return CheckpointSealOwnership{
		SealID: record.SealID, WorkspaceID: record.WorkspaceID, Root: record.Root,
		SessionID: record.SessionID, Generation: record.Generation, Status: status,
		Digest: record.HandbackDigest, WorkspaceRevision: record.HandbackRevision,
		EventCursor: record.HandbackEventCursor, ConsumedAt: record.ConsumedAt,
		ReleasedAt: record.HandbackReleasedAt, SourceResumedAt: record.SourceResumedAt,
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
