package relayfile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const segmentedStateVersion = 2

// SegmentedFileStateBackend stores immutable payloads and per-save metadata
// deltas outside a small commit snapshot. Persistence work is proportional to
// the files, events, and operations changed by a save rather than to the total
// workspace size or event history.
//
// A generation's payloads and deltas are written before metadata atomically
// advances the committed generation. An interrupted save can therefore leave
// only unreachable data. Older generations and payload revisions are retained
// for crash safety and can be removed by an offline compactor.
type SegmentedFileStateBackend struct {
	Root string
	mu   sync.Mutex

	initialized      bool
	generation       uint64
	needsFullSegment bool
	fileRevisions    map[string]map[string]string
	eventIDs         map[string][]string
	opDigests        map[string]map[string]string
	suppressions     map[string]time.Time
}

type segmentedMetadataEnvelope struct {
	Version    int            `json:"version"`
	Generation uint64         `json:"generation"`
	State      persistedState `json:"state"`
}

type segmentedFileDelta struct {
	WorkspaceID string          `json:"workspaceId"`
	Upserts     map[string]File `json:"upserts,omitempty"`
	Deletes     []string        `json:"deletes,omitempty"`
}

type segmentedEventDelta struct {
	WorkspaceID string  `json:"workspaceId"`
	Reset       bool    `json:"reset,omitempty"`
	Events      []Event `json:"events,omitempty"`
}

type segmentedOperationDelta struct {
	WorkspaceID string                     `json:"workspaceId"`
	Upserts     map[string]OperationStatus `json:"upserts,omitempty"`
	Deletes     []string                   `json:"deletes,omitempty"`
}

type segmentedSuppressionDelta struct {
	Upserts map[string]time.Time `json:"upserts,omitempty"`
	Deletes []string             `json:"deletes,omitempty"`
}

func NewSegmentedFileStateBackend(root string) *SegmentedFileStateBackend {
	return &SegmentedFileStateBackend{Root: strings.TrimSpace(root)}
}

func (b *SegmentedFileStateBackend) Load() (*persistedState, error) {
	if b == nil || strings.TrimSpace(b.Root) == "" {
		return nil, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.loadLocked()
}

func (b *SegmentedFileStateBackend) loadLocked() (*persistedState, error) {
	data, err := os.ReadFile(b.metadataPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			b.initializeEmptyLocked()
			return nil, nil
		}
		return nil, err
	}

	var header struct {
		Version    int             `json:"version"`
		Generation uint64          `json:"generation"`
		State      json.RawMessage `json:"state"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, err
	}

	var snapshot persistedState
	if header.Version == segmentedStateVersion && len(header.State) > 0 {
		if err := json.Unmarshal(header.State, &snapshot); err != nil {
			return nil, err
		}
		if err := b.applyCommittedGenerations(&snapshot, header.Generation); err != nil {
			return nil, err
		}
		b.generation = header.Generation
		b.needsFullSegment = false
	} else {
		// Version 1 stored file metadata, event history, and operations directly
		// in metadata.json. Load it exactly once, then migrate it transactionally
		// into generation deltas on the next Save.
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return nil, err
		}
		b.generation = 0
		b.needsFullSegment = true
	}

	if err := b.hydratePayloads(&snapshot); err != nil {
		return nil, err
	}
	b.initialized = true
	if b.needsFullSegment {
		b.initializeCachesLocked()
	} else if err := b.captureCachesLocked(&snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (b *SegmentedFileStateBackend) Save(state *persistedState) error {
	if b == nil || strings.TrimSpace(b.Root) == "" || state == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.initialized {
		if _, err := os.Stat(b.metadataPath()); err == nil {
			if _, err := b.loadLocked(); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		} else {
			b.initializeEmptyLocked()
		}
	}

	generation := b.generation + 1
	generationRoot := b.generationPath(generation)
	// This generation is not committed yet, so clearing remnants from an
	// interrupted attempt cannot remove reachable state.
	if err := os.RemoveAll(generationRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(generationRoot, 0o755); err != nil {
		return err
	}

	metadata := *state
	suppressionDelta := b.buildSuppressionDelta(state.Suppressions)
	if len(suppressionDelta.Upserts) > 0 || len(suppressionDelta.Deletes) > 0 {
		if err := b.writeGenerationRecord(generation, "suppressions", "global", suppressionDelta); err != nil {
			return err
		}
	}
	metadata.Suppressions = emptySuppressionMapLike(state.Suppressions)
	metadata.Workspaces = make(map[string]*workspaceState, len(state.Workspaces))
	for workspaceID, workspace := range state.Workspaces {
		if workspace == nil {
			metadata.Workspaces[workspaceID] = nil
			continue
		}
		fileDelta, err := b.buildFileDelta(workspaceID, workspace)
		if err != nil {
			return err
		}
		if len(fileDelta.Upserts) > 0 || len(fileDelta.Deletes) > 0 {
			if err := b.writeGenerationRecord(generation, "files", workspaceID, fileDelta); err != nil {
				return err
			}
		}

		eventDelta := b.buildEventDelta(workspaceID, workspace)
		if eventDelta.Reset || len(eventDelta.Events) > 0 {
			if err := b.writeGenerationRecord(generation, "events", workspaceID, eventDelta); err != nil {
				return err
			}
		}

		opDelta, err := b.buildOperationDelta(workspaceID, workspace)
		if err != nil {
			return err
		}
		if len(opDelta.Upserts) > 0 || len(opDelta.Deletes) > 0 {
			if err := b.writeGenerationRecord(generation, "operations", workspaceID, opDelta); err != nil {
				return err
			}
		}

		workspaceMetadata := *workspace
		workspaceMetadata.Files = emptyFileMapLike(workspace.Files)
		workspaceMetadata.Events = emptyEventSliceLike(workspace.Events)
		workspaceMetadata.Ops = emptyOperationMapLike(workspace.Ops)
		metadata.Workspaces[workspaceID] = &workspaceMetadata
	}

	envelope := segmentedMetadataEnvelope{
		Version:    segmentedStateVersion,
		Generation: generation,
		State:      metadata,
	}
	data, err := json.Marshal(&envelope)
	if err != nil {
		return err
	}
	if err := writeSegmentedFileAtomic(b.metadataPath(), data, 0o600); err != nil {
		return fmt.Errorf("persist segmented metadata: %w", err)
	}
	if err := b.captureCachesLocked(state); err != nil {
		return err
	}
	b.generation = generation
	b.needsFullSegment = false
	return nil
}

func (b *SegmentedFileStateBackend) buildFileDelta(workspaceID string, workspace *workspaceState) (segmentedFileDelta, error) {
	delta := segmentedFileDelta{WorkspaceID: workspaceID, Upserts: map[string]File{}}
	previous := b.fileRevisions[workspaceID]
	for path, file := range workspace.Files {
		if !b.needsFullSegment && previous[path] == file.Revision {
			continue
		}
		blobPath := b.blobPath(workspaceID, path, file.Revision)
		if _, err := os.Stat(blobPath); errors.Is(err, os.ErrNotExist) {
			if err := writeSegmentedFileAtomic(blobPath, []byte(file.Content), 0o600); err != nil {
				return delta, fmt.Errorf("persist segmented payload for workspace %q path %q revision %q: %w", workspaceID, path, file.Revision, err)
			}
		} else if err != nil {
			return delta, err
		}
		file.Content = ""
		delta.Upserts[path] = file
	}
	for path := range previous {
		if _, exists := workspace.Files[path]; !exists {
			delta.Deletes = append(delta.Deletes, path)
		}
	}
	sort.Strings(delta.Deletes)
	return delta, nil
}

func (b *SegmentedFileStateBackend) buildEventDelta(workspaceID string, workspace *workspaceState) segmentedEventDelta {
	delta := segmentedEventDelta{WorkspaceID: workspaceID}
	previous := b.eventIDs[workspaceID]
	prefix := !b.needsFullSegment && len(previous) <= len(workspace.Events)
	if prefix {
		for index, eventID := range previous {
			if workspace.Events[index].EventID != eventID {
				prefix = false
				break
			}
		}
	}
	if !prefix {
		delta.Reset = true
		delta.Events = append([]Event(nil), workspace.Events...)
		return delta
	}
	delta.Events = append([]Event(nil), workspace.Events[len(previous):]...)
	return delta
}

func (b *SegmentedFileStateBackend) buildOperationDelta(workspaceID string, workspace *workspaceState) (segmentedOperationDelta, error) {
	delta := segmentedOperationDelta{WorkspaceID: workspaceID, Upserts: map[string]OperationStatus{}}
	previous := b.opDigests[workspaceID]
	for opID, operation := range workspace.Ops {
		digest, err := jsonDigest(operation)
		if err != nil {
			return delta, err
		}
		if b.needsFullSegment || previous[opID] != digest {
			delta.Upserts[opID] = operation
		}
	}
	for opID := range previous {
		if _, exists := workspace.Ops[opID]; !exists {
			delta.Deletes = append(delta.Deletes, opID)
		}
	}
	sort.Strings(delta.Deletes)
	return delta, nil
}

func (b *SegmentedFileStateBackend) buildSuppressionDelta(current map[string]time.Time) segmentedSuppressionDelta {
	delta := segmentedSuppressionDelta{Upserts: map[string]time.Time{}}
	for key, expiresAt := range current {
		if previous, exists := b.suppressions[key]; b.needsFullSegment || !exists || !previous.Equal(expiresAt) {
			delta.Upserts[key] = expiresAt
		}
	}
	for key := range b.suppressions {
		if _, exists := current[key]; !exists {
			delta.Deletes = append(delta.Deletes, key)
		}
	}
	sort.Strings(delta.Deletes)
	return delta
}

func (b *SegmentedFileStateBackend) applyCommittedGenerations(snapshot *persistedState, committed uint64) error {
	entries, err := os.ReadDir(b.generationsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && committed == 0 {
			return nil
		}
		return err
	}
	type generationEntry struct {
		ordinal uint64
		name    string
	}
	generations := make([]generationEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		ordinal, parseErr := strconv.ParseUint(entry.Name(), 10, 64)
		if parseErr == nil && ordinal <= committed {
			generations = append(generations, generationEntry{ordinal: ordinal, name: entry.Name()})
		}
	}
	sort.Slice(generations, func(i, j int) bool { return generations[i].ordinal < generations[j].ordinal })
	for _, generation := range generations {
		root := filepath.Join(b.generationsPath(), generation.name)
		if err := applySegmentedRecords(filepath.Join(root, "files"), func(data []byte) error {
			var delta segmentedFileDelta
			if err := json.Unmarshal(data, &delta); err != nil {
				return err
			}
			workspace := snapshot.Workspaces[delta.WorkspaceID]
			if workspace == nil {
				return nil
			}
			if workspace.Files == nil {
				workspace.Files = map[string]File{}
			}
			for _, path := range delta.Deletes {
				delete(workspace.Files, path)
			}
			for path, file := range delta.Upserts {
				workspace.Files[path] = file
			}
			return nil
		}); err != nil {
			return err
		}
		if err := applySegmentedRecords(filepath.Join(root, "events"), func(data []byte) error {
			var delta segmentedEventDelta
			if err := json.Unmarshal(data, &delta); err != nil {
				return err
			}
			workspace := snapshot.Workspaces[delta.WorkspaceID]
			if workspace == nil {
				return nil
			}
			if delta.Reset {
				workspace.Events = nil
			}
			workspace.Events = append(workspace.Events, delta.Events...)
			return nil
		}); err != nil {
			return err
		}
		if err := applySegmentedRecords(filepath.Join(root, "operations"), func(data []byte) error {
			var delta segmentedOperationDelta
			if err := json.Unmarshal(data, &delta); err != nil {
				return err
			}
			workspace := snapshot.Workspaces[delta.WorkspaceID]
			if workspace == nil {
				return nil
			}
			if workspace.Ops == nil {
				workspace.Ops = map[string]OperationStatus{}
			}
			for _, opID := range delta.Deletes {
				delete(workspace.Ops, opID)
			}
			for opID, operation := range delta.Upserts {
				workspace.Ops[opID] = operation
			}
			return nil
		}); err != nil {
			return err
		}
		if err := applySegmentedRecords(filepath.Join(root, "suppressions"), func(data []byte) error {
			var delta segmentedSuppressionDelta
			if err := json.Unmarshal(data, &delta); err != nil {
				return err
			}
			if snapshot.Suppressions == nil {
				snapshot.Suppressions = map[string]time.Time{}
			}
			for _, key := range delta.Deletes {
				delete(snapshot.Suppressions, key)
			}
			for key, expiresAt := range delta.Upserts {
				snapshot.Suppressions[key] = expiresAt
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func applySegmentedRecords(root string, apply func([]byte) error) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			return err
		}
		if err := apply(data); err != nil {
			return err
		}
	}
	return nil
}

func (b *SegmentedFileStateBackend) hydratePayloads(snapshot *persistedState) error {
	for workspaceID, workspace := range snapshot.Workspaces {
		if workspace == nil {
			continue
		}
		for path, file := range workspace.Files {
			payload, err := os.ReadFile(b.blobPath(workspaceID, path, file.Revision))
			if err != nil {
				return fmt.Errorf("load segmented payload for workspace %q path %q revision %q: %w", workspaceID, path, file.Revision, err)
			}
			file.Content = string(payload)
			workspace.Files[path] = file
		}
	}
	return nil
}

func (b *SegmentedFileStateBackend) captureCachesLocked(state *persistedState) error {
	b.initializeCachesLocked()
	for key, expiresAt := range state.Suppressions {
		b.suppressions[key] = expiresAt
	}
	for workspaceID, workspace := range state.Workspaces {
		if workspace == nil {
			continue
		}
		files := make(map[string]string, len(workspace.Files))
		for path, file := range workspace.Files {
			files[path] = file.Revision
		}
		b.fileRevisions[workspaceID] = files

		events := make([]string, len(workspace.Events))
		for index, event := range workspace.Events {
			events[index] = event.EventID
		}
		b.eventIDs[workspaceID] = events

		operations := make(map[string]string, len(workspace.Ops))
		for opID, operation := range workspace.Ops {
			digest, err := jsonDigest(operation)
			if err != nil {
				return err
			}
			operations[opID] = digest
		}
		b.opDigests[workspaceID] = operations
	}
	return nil
}

func (b *SegmentedFileStateBackend) initializeEmptyLocked() {
	b.generation = 0
	b.needsFullSegment = false
	b.initialized = true
	b.initializeCachesLocked()
}

func (b *SegmentedFileStateBackend) initializeCachesLocked() {
	b.fileRevisions = map[string]map[string]string{}
	b.eventIDs = map[string][]string{}
	b.opDigests = map[string]map[string]string{}
	b.suppressions = map[string]time.Time{}
}

func (b *SegmentedFileStateBackend) writeGenerationRecord(generation uint64, kind, workspaceID string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(workspaceID))
	path := filepath.Join(b.generationPath(generation), kind, hex.EncodeToString(digest[:])+".json")
	return writeSegmentedFileAtomic(path, data, 0o600)
}

func jsonDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func emptyFileMapLike(source map[string]File) map[string]File {
	if source == nil {
		return nil
	}
	return map[string]File{}
}

func emptyEventSliceLike(source []Event) []Event {
	if source == nil {
		return nil
	}
	return []Event{}
}

func emptyOperationMapLike(source map[string]OperationStatus) map[string]OperationStatus {
	if source == nil {
		return nil
	}
	return map[string]OperationStatus{}
}

func emptySuppressionMapLike(source map[string]time.Time) map[string]time.Time {
	if source == nil {
		return nil
	}
	return map[string]time.Time{}
}

func (b *SegmentedFileStateBackend) metadataPath() string {
	return filepath.Join(b.Root, "metadata.json")
}

func (b *SegmentedFileStateBackend) generationsPath() string {
	return filepath.Join(b.Root, "generations")
}

func (b *SegmentedFileStateBackend) generationPath(generation uint64) string {
	return filepath.Join(b.generationsPath(), fmt.Sprintf("%020d", generation))
}

func (b *SegmentedFileStateBackend) blobPath(workspaceID, path, revision string) string {
	digest := sha256.Sum256([]byte(workspaceID + "\x00" + path + "\x00" + revision))
	name := hex.EncodeToString(digest[:])
	return filepath.Join(b.Root, "blobs", name[:2], name+".content")
}

func writeSegmentedFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".segment-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}
