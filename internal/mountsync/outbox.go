package mountsync

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultOutboxMaxAttempts = 5
	outboxBackoffBase        = 500 * time.Millisecond
	outboxBackoffMax         = 30 * time.Second

	outboxCapabilitiesSchemaVersion = 2
)

type outboxStatus string

const (
	outboxStatusPending outboxStatus = "pending"
	outboxStatusAcked   outboxStatus = "acked"
	outboxStatusFailed  outboxStatus = "failed"
)

type outboxRecord struct {
	CommandID         string       `json:"commandId"`
	WorkspaceID       string       `json:"workspaceId"`
	RemotePath        string       `json:"remotePath"`
	ContentType       string       `json:"contentType"`
	Content           string       `json:"content"`
	Encoding          string       `json:"encoding,omitempty"`
	Hash              string       `json:"hash"`
	Exists            bool         `json:"exists"`
	Status            outboxStatus `json:"status"`
	FirstSeenAt       string       `json:"firstSeenAt"`
	LastAttemptAt     string       `json:"lastAttemptAt,omitempty"`
	NextAttemptAt     string       `json:"nextAttemptAt,omitempty"`
	AttemptCount      int          `json:"attemptCount"`
	LastError         string       `json:"lastError,omitempty"`
	NeedsAttention    bool         `json:"needsAttention,omitempty"`
	OpID              string       `json:"opId,omitempty"`
	DispatchStatus    string       `json:"dispatchStatus,omitempty"`
	AckedAt           string       `json:"ackedAt,omitempty"`
	Revision          string       `json:"revision,omitempty"`
	CorrelationID     string       `json:"correlationId,omitempty"`
	LocalRelativePath string       `json:"localRelativePath,omitempty"`
}

type outboxSummary struct {
	Pending        int `json:"pending"`
	NeedsAttention int `json:"needsAttention"`
	Failed         int `json:"failed"`
	Acked          int `json:"acked"`
}

type outboxCapabilities struct {
	SchemaVersion    int  `json:"schemaVersion"`
	DispatchReceipts bool `json:"dispatchReceipts"`
}

func (s *Syncer) outboxPendingDir() string { return filepath.Join(s.outboxDir, "pending") }
func (s *Syncer) outboxAckedDir() string   { return filepath.Join(s.outboxDir, "acked") }
func (s *Syncer) outboxFailedDir() string  { return filepath.Join(s.outboxDir, "failed") }
func (s *Syncer) outboxCapabilitiesPath() string {
	return filepath.Join(s.outboxDir, "capabilities.json")
}

func (s *Syncer) ensureOutboxDirs() error {
	for _, dir := range []string{s.outboxDir, s.outboxPendingDir(), s.outboxAckedDir(), s.outboxFailedDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := s.ensureOutboxCapabilities(); err != nil {
		return err
	}
	return nil
}

func (s *Syncer) ensureOutboxCapabilities() error {
	capabilities := outboxCapabilities{
		SchemaVersion:    outboxCapabilitiesSchemaVersion,
		DispatchReceipts: true,
	}
	data, err := json.Marshal(capabilities)
	if err != nil {
		return err
	}
	path := s.outboxCapabilitiesPath()
	existing, err := os.ReadFile(path)
	if err == nil {
		// Compare decoded values rather than raw bytes so a field reorder or
		// whitespace difference does not cause a spurious rewrite.
		var current outboxCapabilities
		if json.Unmarshal(existing, &current) == nil && current == capabilities {
			return nil
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeFileAtomic(path, data, 0o644)
}

func (s *Syncer) pendingOutboxPath(commandID string) string {
	return filepath.Join(s.outboxPendingDir(), commandID+".json")
}

func (s *Syncer) ackedOutboxPath(commandID string) string {
	return filepath.Join(s.outboxAckedDir(), commandID+".json")
}

func (s *Syncer) failedOutboxPath(commandID string) string {
	return filepath.Join(s.outboxFailedDir(), commandID+".json")
}

func (s *Syncer) saveOutboxRecord(record outboxRecord) error {
	if err := s.ensureOutboxDirs(); err != nil {
		return err
	}
	if strings.TrimSpace(record.CommandID) == "" {
		return errors.New("mountsync: outbox command id is required")
	}
	record.Status = outboxStatusPending
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.pendingOutboxPath(record.CommandID), data, 0o644)
}

func (s *Syncer) readOutboxRecord(path string) (outboxRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return outboxRecord{}, err
	}
	var record outboxRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return outboxRecord{}, err
	}
	record.RemotePath = normalizeRemotePath(record.RemotePath)
	record.CommandID = strings.TrimSpace(record.CommandID)
	record.WorkspaceID = strings.TrimSpace(record.WorkspaceID)
	record.ContentType = strings.TrimSpace(record.ContentType)
	record.Encoding = normalizeEncoding(record.Encoding)
	record.Hash = strings.TrimSpace(record.Hash)
	record.OpID = strings.TrimSpace(record.OpID)
	record.DispatchStatus = strings.TrimSpace(record.DispatchStatus)
	record.CorrelationID = strings.TrimSpace(record.CorrelationID)
	if record.CorrelationID == "" {
		record.CorrelationID = record.CommandID
	}
	if record.Status == "" {
		record.Status = outboxStatusPending
	}
	return record, nil
}

func (s *Syncer) listPendingOutboxRecords() ([]outboxRecord, error) {
	if err := s.ensureOutboxDirs(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.outboxPendingDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	records := make([]outboxRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := s.readOutboxRecord(filepath.Join(s.outboxPendingDir(), entry.Name()))
		if err != nil {
			return nil, err
		}
		if record.Status == outboxStatusPending {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].FirstSeenAt == records[j].FirstSeenAt {
			return records[i].CommandID < records[j].CommandID
		}
		return records[i].FirstSeenAt < records[j].FirstSeenAt
	})
	return records, nil
}

func (s *Syncer) findPendingOutboxByPath(remotePath string) ([]outboxRecord, error) {
	records, err := s.listPendingOutboxRecords()
	if err != nil {
		return nil, err
	}
	remotePath = normalizeRemotePath(remotePath)
	matched := make([]outboxRecord, 0, 1)
	for _, record := range records {
		if record.RemotePath == remotePath {
			matched = append(matched, record)
		}
	}
	return matched, nil
}

func (s *Syncer) ensureOutboxRecord(pending pendingBulkWrite) (outboxRecord, error) {
	matches, err := s.findPendingOutboxByPath(pending.remotePath)
	if err != nil {
		return outboxRecord{}, err
	}
	for _, record := range matches {
		if record.Hash == pending.snapshot.Hash {
			// Stability rule: once a command exists, the persisted record is
			// the sole source of truth for commandId across reconnect/restart.
			return record, nil
		}
		if err := s.archiveFailedOutboxRecord(record, "superseded by newer local content", false); err != nil {
			return outboxRecord{}, err
		}
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	record := outboxRecord{
		CommandID:         newOutboxCommandID(s.workspace, pending.remotePath, pending.snapshot.Hash, now),
		WorkspaceID:       s.workspace,
		RemotePath:        pending.remotePath,
		ContentType:       pending.snapshot.ContentType,
		Content:           pending.snapshot.WireContent,
		Encoding:          normalizeEncoding(pending.snapshot.Encoding),
		Hash:              pending.snapshot.Hash,
		Exists:            pending.exists,
		Status:            outboxStatusPending,
		FirstSeenAt:       now,
		CorrelationID:     "",
		LocalRelativePath: pending.tracked.LocalRelativePath,
	}
	record.CorrelationID = record.CommandID
	if err := s.saveOutboxRecord(record); err != nil {
		return outboxRecord{}, err
	}
	return record, nil
}

func newOutboxCommandID(workspaceID, remotePath, hash, firstSeenAt string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(workspaceID),
		normalizeRemotePath(remotePath),
		strings.TrimSpace(hash),
		strings.TrimSpace(firstSeenAt),
	}, "\x00")))
	return "mountcmd_" + hex.EncodeToString(sum[:])[:32]
}

func (s *Syncer) incrementOutboxAttempt(record outboxRecord, err error) error {
	record.AttemptCount++
	now := s.now().UTC()
	record.LastAttemptAt = now.Format(time.RFC3339Nano)
	record.LastError = sanitizeOutboxError(err)
	if record.AttemptCount >= s.maxOutboxAttemptsValue() {
		record.NeedsAttention = true
		record.NextAttemptAt = ""
	} else {
		record.NextAttemptAt = now.Add(outboxBackoff(record.AttemptCount)).Format(time.RFC3339Nano)
	}
	return s.saveOutboxRecord(record)
}

func outboxBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return outboxBackoffBase
	}
	delay := outboxBackoffBase
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= outboxBackoffMax {
			return outboxBackoffMax
		}
	}
	return delay
}

func sanitizeOutboxError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return msg
}

func (s *Syncer) outboxDue(record outboxRecord, now time.Time) bool {
	if record.NeedsAttention || record.AttemptCount >= s.maxOutboxAttemptsValue() {
		return false
	}
	if strings.TrimSpace(record.NextAttemptAt) == "" {
		return true
	}
	next, err := time.Parse(time.RFC3339Nano, record.NextAttemptAt)
	if err != nil {
		return true
	}
	return !now.Before(next)
}

func (s *Syncer) ackOutboxRecord(record outboxRecord, revision, correlationID string) error {
	if err := s.ensureOutboxDirs(); err != nil {
		return err
	}
	record.Status = outboxStatusAcked
	record.AckedAt = s.now().UTC().Format(time.RFC3339Nano)
	record.DispatchStatus = "succeeded"
	record.Revision = strings.TrimSpace(revision)
	if strings.TrimSpace(correlationID) != "" {
		record.CorrelationID = strings.TrimSpace(correlationID)
	}
	record.NeedsAttention = false
	record.LastError = ""
	record.NextAttemptAt = ""
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.ackedOutboxPath(record.CommandID), data, 0o644); err != nil {
		return err
	}
	if err := os.Remove(s.pendingOutboxPath(record.CommandID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Syncer) skipOutboxRecord(record outboxRecord, reason string) error {
	if err := s.ensureOutboxDirs(); err != nil {
		return err
	}
	record.Status = outboxStatusAcked
	record.AckedAt = s.now().UTC().Format(time.RFC3339Nano)
	record.DispatchStatus = strings.TrimSpace(reason)
	record.Revision = ""
	record.CorrelationID = ""
	record.NeedsAttention = false
	record.LastError = ""
	record.NextAttemptAt = ""
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.ackedOutboxPath(record.CommandID), data, 0o644); err != nil {
		return err
	}
	if err := os.Remove(s.pendingOutboxPath(record.CommandID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Syncer) failOutboxRecord(record outboxRecord, reason string) error {
	return s.archiveFailedOutboxRecord(record, reason, false)
}

func (s *Syncer) failOutboxRecordNeedsAttention(record outboxRecord, reason string) error {
	return s.archiveFailedOutboxRecord(record, reason, true)
}

func (s *Syncer) archiveFailedOutboxRecord(record outboxRecord, reason string, needsAttention bool) error {
	if err := s.ensureOutboxDirs(); err != nil {
		return err
	}
	record.Status = outboxStatusFailed
	record.LastError = strings.TrimSpace(reason)
	record.NextAttemptAt = ""
	record.NeedsAttention = needsAttention
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.failedOutboxPath(record.CommandID), data, 0o644); err != nil {
		return err
	}
	if err := os.Remove(s.pendingOutboxPath(record.CommandID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Syncer) summarizeOutbox() outboxSummary {
	var summary outboxSummary
	pending, err := s.listPendingOutboxRecords()
	if err == nil {
		for _, record := range pending {
			summary.Pending++
			if record.NeedsAttention || record.AttemptCount >= s.maxOutboxAttemptsValue() {
				summary.NeedsAttention++
			}
		}
	}
	if entries, err := os.ReadDir(s.outboxFailedDir()); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
				summary.Failed++
				record, readErr := s.readOutboxRecord(filepath.Join(s.outboxFailedDir(), entry.Name()))
				if readErr != nil || record.NeedsAttention || terminalOutboxDispatchStatus(record.DispatchStatus) {
					summary.NeedsAttention++
				}
			}
		}
	}
	if entries, err := os.ReadDir(s.outboxAckedDir()); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
				summary.Acked++
			}
		}
	}
	return summary
}

func terminalOutboxDispatchStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "failed", "dead_lettered", "canceled":
		return true
	default:
		return false
	}
}

func (s *Syncer) maxOutboxAttemptsValue() int {
	if s.maxOutboxAttempts > 0 {
		return s.maxOutboxAttempts
	}
	return defaultOutboxMaxAttempts
}

func (s *Syncer) outboxRecordAsPending(record outboxRecord, tracked trackedFile, exists bool) (pendingBulkWrite, error) {
	var localPath string
	var err error
	if strings.TrimSpace(record.LocalRelativePath) != "" {
		localPath, err = safeLocalPath(s.localRoot, record.LocalRelativePath)
		if err == nil {
			var mappedRemote string
			mappedRemote, err = localToRemotePath(s.localRoot, s.remoteRoot, localPath)
			if err == nil && normalizeRemotePath(mappedRemote) != normalizeRemotePath(record.RemotePath) {
				err = fmt.Errorf("outbox local path %s maps to %s, want %s", record.LocalRelativePath, mappedRemote, record.RemotePath)
			}
		}
		if err == nil && strings.TrimSpace(tracked.LocalRelativePath) == "" {
			tracked.LocalRelativePath = filepath.ToSlash(record.LocalRelativePath)
		}
	} else {
		if migratedPath, rel, found := s.existingPreShorteningLocalPath(record.RemotePath); found {
			localPath = migratedPath
			if strings.TrimSpace(tracked.LocalRelativePath) == "" {
				tracked.LocalRelativePath = rel
			}
		} else {
			localPath, err = s.remoteToLocalPath(record.RemotePath)
		}
	}
	if err != nil {
		return pendingBulkWrite{}, err
	}
	contentType := strings.TrimSpace(record.ContentType)
	if contentType == "" {
		contentType = "text/markdown"
	}
	rawContent := []byte(record.Content)
	if normalizeEncoding(record.Encoding) == "base64" {
		if decoded, err := base64.StdEncoding.DecodeString(record.Content); err == nil {
			rawContent = decoded
		}
	}
	exists = record.Exists
	return pendingBulkWrite{
		remotePath: record.RemotePath,
		localPath:  localPath,
		snapshot: localSnapshot{
			RawContent:  rawContent,
			WireContent: record.Content,
			ContentType: contentType,
			Encoding:    normalizeEncoding(record.Encoding),
			Hash:        strings.TrimSpace(record.Hash),
		},
		tracked: tracked,
		exists:  exists,
	}, nil
}

func outboxHTTPError(record outboxRecord) error {
	if strings.TrimSpace(record.LastError) == "" {
		return fmt.Errorf("outbox command %s needs attention", record.CommandID)
	}
	return fmt.Errorf("outbox command %s needs attention: %s", record.CommandID, record.LastError)
}
