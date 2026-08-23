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
	DefaultCheckpointSealTTL = 60 * time.Second
	MaxCheckpointSealTTL     = 5 * time.Minute
)

var (
	ErrCheckpointDiverged        = errors.New("checkpoint digest does not match durable workspace state")
	ErrCheckpointExpired         = errors.New("checkpoint seal expired")
	ErrCheckpointReplay          = errors.New("checkpoint seal already consumed")
	ErrCheckpointStale           = errors.New("checkpoint seal is stale")
	ErrCheckpointGenerationStale = errors.New("checkpoint generation is not newer than the last issued generation")
	checkpointSessionPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
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
	SealToken  string `json:"sealToken"`
	Root       string `json:"root"`
	SessionID  string `json:"sessionId"`
	Generation uint64 `json:"generation"`
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
	TokenHash string `json:"tokenHash"`
	Issuer    string `json:"issuer,omitempty"`
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
	root, err := NormalizeCheckpointRoot(req.Root)
	if err != nil || workspaceID == "" || !checkpointSessionPattern.MatchString(strings.TrimSpace(req.SessionID)) || req.Generation == 0 || strings.TrimSpace(req.SealToken) == "" {
		return CheckpointSeal{}, ErrInvalidInput
	}
	now = now.UTC()
	tokenHash := checkpointTokenHash(strings.TrimSpace(req.SealToken))
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.checkpointSeals[tokenHash]
	if !ok {
		return CheckpointSeal{}, ErrNotFound
	}
	if record.WorkspaceID != workspaceID || record.Root != root || record.SessionID != strings.TrimSpace(req.SessionID) || record.Generation != req.Generation {
		return CheckpointSeal{}, ErrInvalidInput
	}
	if record.ConsumedAt != "" {
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
	s.checkpointSeals[tokenHash] = record
	if err := s.saveLocked(); err != nil {
		record.ConsumedAt = ""
		s.checkpointSeals[tokenHash] = record
		return CheckpointSeal{}, err
	}
	return record.CheckpointSeal, nil
}

func (s *Store) checkpointStateLocked(workspaceID, root string) (digest, workspaceRevision, cursor string, err error) {
	entries := []CheckpointDigestEntry{}
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
		workspaceRevision = strings.TrimSpace(ws.Revision)
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
