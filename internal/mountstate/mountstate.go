// Package mountstate owns the merge discipline for a mount's public
// `.relay/state.json`.
//
// Two writers in the mount process publish to that one path: the syncer's
// public state and (in the CLI) the provider/daemon mirror snapshot. Their
// schemas are disjoint, so a writer that serializes its own struct over the
// whole file deletes the other's keys — and a consumer's guard keyed on a
// deleted key silently fails open rather than reporting ill health.
//
// Every writer therefore goes through this package, declaring the keys it
// owns. The merge replaces exactly those keys and preserves the rest, under
// one process-wide lock so the two writers cannot interleave a
// read-modify-write.
//
// See relayfile#412.
package mountstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// stateMu serializes every read-modify-write of a mount state file in this
// process. It replaces the arrangement relayfile#412 documented, where one
// writer's mutex appeared to synchronize against a writer that could not take
// it — a lock that looks synchronized and is not.
var stateMu sync.Mutex

// unreadableQuarantineAfter bounds how many consecutive publishes may be
// refused because the state file could not be read.
//
// Refusing to merge over a document we could not read is what stops a
// transient EIO from deleting the other writer's keys. But the refusal must
// not be able to last forever: a state file left unreadable (a stray chmod, an
// ownership change across a container restart) would otherwise stall every
// publish for the life of the mount, and the sandbox readiness guard treats an
// unreadable state file exactly like a missing one — so a permanent refusal
// and a permanent exit 75 are the same observable. That is the failure
// relayfile#455 is about, which is not a thing the fix for relayfile#412 gets
// to reintroduce.
//
// After this many consecutive failures the unreadable file is moved aside and
// publishing resumes. Its keys are already unreachable to every consumer by
// then, so a live document beats a preserved unreadable one, and the
// quarantined copy is left on disk for diagnosis.
const unreadableQuarantineAfter = 3

// consecutiveReadFailures counts failed reads per state path. Guarded by
// stateMu; entries are removed as soon as a read succeeds, so this holds at
// most one entry per mount in the process.
var consecutiveReadFailures = map[string]int{}

// Document is a decoded state file. Values stay as raw JSON: the public state
// carries a per-file map that runs to megabytes on a large workspace, and both
// writers publish on every reconcile and every local-change batch, so a merge
// must not pay to materialize a structure it only intends to copy or drop.
type Document map[string]json.RawMessage

// Merge writes snapshot into statePath, replacing exactly ownedKeys and
// leaving every other key untouched — including keys this build does not know
// about.
//
// ownedKeys is cleared before the overlay so a field the caller has cleared
// (a drained stall reason, a resolved error) is not resurrected from the
// previous document.
func Merge(statePath string, ownedKeys []string, snapshot any) error {
	return MergeFunc(statePath, ownedKeys, func(Document) (any, error) { return snapshot, nil })
}

// MergeFunc is Merge for a writer whose snapshot depends on what is already
// published — a counter it must not regress, say. build runs under the state
// lock with the previous document, so the value it reads cannot be overwritten
// between the read and this write.
func MergeFunc(statePath string, ownedKeys []string, build func(previous Document) (any, error)) error {
	stateMu.Lock()
	defer stateMu.Unlock()

	document, err := readForWriteLocked(statePath)
	if err != nil {
		return err
	}
	snapshot, err := build(document)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	var owned Document
	if err := json.Unmarshal(encoded, &owned); err != nil {
		return err
	}
	for _, key := range ownedKeys {
		delete(document, key)
	}
	for key, value := range owned {
		document[key] = value
	}
	return writeDocumentLocked(statePath, document)
}

// Increment adds delta to a numeric key, creating it when absent. The read and
// the write happen under one lock hold, so an increment cannot be lost to a
// snapshot that read the counter before it landed.
func Increment(statePath, key string, delta uint64) error {
	stateMu.Lock()
	defer stateMu.Unlock()

	document, err := readForWriteLocked(statePath)
	if err != nil {
		return err
	}
	next := document.Uint64(key) + delta
	encoded, err := json.Marshal(next)
	if err != nil {
		return err
	}
	document[key] = encoded
	return writeDocumentLocked(statePath, document)
}

// Read returns the published document, empty when the file is missing,
// unreadable or unparseable. It takes the same lock, so a reader never
// observes a half-applied merge.
//
// Unlike the writers, a reader has nothing to destroy by treating an
// unreadable file as empty, so this deliberately does not surface the error.
func Read(statePath string) Document {
	stateMu.Lock()
	defer stateMu.Unlock()
	document, err := readDocumentLocked(statePath)
	if err != nil {
		return Document{}
	}
	return document
}

// Uint64 decodes key as an unsigned integer, returning 0 when it is absent or
// is not a number. JSON numbers arrive as floats often enough that a plain
// decode into uint64 is not sufficient on its own.
func (d Document) Uint64(key string) uint64 {
	raw, ok := d[key]
	if !ok || len(raw) == 0 {
		return 0
	}
	var asUint uint64
	if err := json.Unmarshal(raw, &asUint); err == nil {
		return asUint
	}
	var asFloat float64
	if err := json.Unmarshal(raw, &asFloat); err == nil && asFloat > 0 {
		return uint64(asFloat)
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		if parsed, err := strconv.ParseUint(asString, 10, 64); err == nil {
			return parsed
		}
	}
	return 0
}

// readForWriteLocked is the read a writer performs before merging: it refuses
// on an unreadable document so the merge cannot delete the other writer's
// keys, but only until unreadableQuarantineAfter consecutive failures, at
// which point it moves the file aside so publishing can resume.
func readForWriteLocked(statePath string) (Document, error) {
	document, err := readDocumentLocked(statePath)
	if err == nil {
		delete(consecutiveReadFailures, statePath)
		return document, nil
	}

	consecutiveReadFailures[statePath]++
	if consecutiveReadFailures[statePath] < unreadableQuarantineAfter {
		return nil, err
	}

	quarantine := fmt.Sprintf("%s.unreadable-%d", statePath, time.Now().UnixNano())
	if renameErr := os.Rename(statePath, quarantine); renameErr != nil {
		// Nothing safe left to do: keep refusing rather than write over a
		// document we can neither read nor move.
		return nil, fmt.Errorf("%w (also could not quarantine it: %v)", err, renameErr)
	}
	delete(consecutiveReadFailures, statePath)
	return Document{}, nil
}

// readDocumentLocked returns the current document, distinguishing "there is
// nothing here yet" from "I could not read what is here".
//
// The distinction is the whole point of the package. A merge writes back
// everything it read, so treating an unreadable file as empty would delete the
// other writer's keys — the exact clobber this package exists to prevent — on
// a transient EIO or a permissions change. A missing file is different: there
// is nothing to lose, and the first write has to start somewhere.
//
// A file that reads fine but does not parse is treated as empty on purpose.
// Writes go through an atomic rename, so a torn document should not be
// reachable, and refusing to rewrite a corrupt one would strand the mount with
// it forever with no way back.
func readDocumentLocked(statePath string) (Document, error) {
	payload, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return Document{}, nil
		}
		return nil, fmt.Errorf("read mount state %s: %w", statePath, err)
	}
	document := Document{}
	if err := json.Unmarshal(payload, &document); err != nil || document == nil {
		return Document{}, nil
	}
	return document, nil
}

func writeDocumentLocked(statePath string, document Document) error {
	payload, err := json.Marshal(document)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(statePath, payload, 0o644)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, ".state.json.tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Chmod(mode); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	// Data-loss guard, matching the mountsync writer this replaces: never
	// rename a file over an existing directory.
	if info, err := os.Lstat(path); err == nil && info.IsDir() {
		return fmt.Errorf("refusing to replace directory %s with a file: %w", path, os.ErrExist)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	committed = true
	return nil
}
