package mountsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsnotify"
)

// A write that already landed must not be reported as a conflict.
//
// The transport retries 429/5xx internally, so a write whose response was lost
// is re-sent carrying the expectedRevision the first attempt used. The server —
// now holding the revision that first attempt created — answers 409. Nothing
// diverged: the remote holds exactly the bytes we were trying to write.
//
// Recording that as a conflict is not cosmetic. It materializes a conflict
// artifact and leaves the outbox entry unresolved, and the warm-start audit then
// refuses to boot the sandbox at all
// (relayfile_mount_intent_warm_audit_unresolved_outbox) — so a delivery that
// SUCCEEDED wedges every later run of the agent, recoverable only by destroying
// the sandbox. Observed 2026-09-04 on repo-intel: two Slack messages
// byte-identical to the remote, filed as failed, blocking every subsequent run.
func TestWriteConflictWithIdenticalRemoteContentIsNotAConflict(t *testing.T) {
	const (
		remotePath = "/notion/messages/message.json"
		base       = "{\n  \"text\": \"base\"\n}\n"
		delivered  = "{\n  \"text\": \"the digest that already landed\"\n}\n"
	)
	client := &fakeClient{files: map[string]RemoteFile{
		remotePath: {Path: remotePath, Revision: "rev_base", ContentType: "application/json", Content: base},
	}}
	client.bulkWriteResponseFunc = func(_ context.Context, _ string, files []BulkWriteFile) (BulkWriteResponse, error) {
		for _, file := range files {
			current := client.files[normalizeRemotePath(file.Path)]
			if file.IfMatch != current.Revision {
				return BulkWriteResponse{ErrorCount: 1, Errors: []BulkWriteError{{
					Path: file.Path, Code: "conflict", Message: "revision conflict",
				}}}, nil
			}
		}
		return BulkWriteResponse{}, nil
	}
	syncer := newMountRolloutTestSyncer(t, client)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("bootstrap sync: %v", err)
	}

	// The lost-ack shape: the remote already holds the bytes we are about to
	// push, under a revision we have not observed, so our IfMatch is stale and
	// the server answers 409 for a write that already succeeded.
	client.files[remotePath] = RemoteFile{
		Path: remotePath, Revision: "rev_delivered", ContentType: "application/json", Content: delivered,
	}
	localPath := filepath.Join(syncer.localRoot, "messages", "message.json")
	if err := os.WriteFile(localPath, []byte(delivered), 0o644); err != nil {
		t.Fatalf("write local copy of the delivered content: %v", err)
	}

	if err := syncer.HandleLocalChange(context.Background(), "messages/message.json", fsnotify.Write); err != nil {
		t.Fatalf("push of already-delivered content: %v", err)
	}

	// No conflict artifact: there is nothing for a human to reconcile.
	artifactPath := conflictArtifactPath(syncer.conflictsDir, remotePath, "rev_base")
	if _, err := os.Stat(artifactPath); !os.IsNotExist(err) {
		t.Fatalf("conflict artifact %s should not exist for a byte-identical remote (stat err = %v)", artifactPath, err)
	}

	// The local file is untouched, and the tracked state adopts the revision
	// the remote actually holds so the next push is not stale all over again.
	assertLocalFileContent(t, localPath, delivered)
	tracked, ok := syncer.state.Files[remotePath]
	if !ok {
		t.Fatal("expected the path to remain tracked after an idempotent write")
	}
	if tracked.Revision != "rev_delivered" {
		t.Fatalf("tracked revision = %q, want rev_delivered (the revision the remote holds)", tracked.Revision)
	}
	if tracked.Dirty {
		t.Fatal("tracked file must not stay dirty once the remote already holds its content")
	}
}

// The guard must only ever turn a FALSE conflict into a success. A genuine
// divergence — the remote holding different bytes — has to keep producing a
// conflict artifact, or the fix above would silently discard a real edit.
func TestWriteConflictWithDifferentRemoteContentStillConflicts(t *testing.T) {
	const (
		remotePath = "/notion/messages/message.json"
		base       = "{\n  \"text\": \"base\"\n}\n"
		remote     = "{\n  \"text\": \"someone else's edit\"\n}\n"
		local      = "{\n  \"text\": \"my edit\"\n}\n"
	)
	client := &fakeClient{files: map[string]RemoteFile{
		remotePath: {Path: remotePath, Revision: "rev_base", ContentType: "application/json", Content: base},
	}}
	client.bulkWriteResponseFunc = func(_ context.Context, _ string, files []BulkWriteFile) (BulkWriteResponse, error) {
		for _, file := range files {
			current := client.files[normalizeRemotePath(file.Path)]
			if file.IfMatch != current.Revision {
				return BulkWriteResponse{ErrorCount: 1, Errors: []BulkWriteError{{
					Path: file.Path, Code: "conflict", Message: "revision conflict",
				}}}, nil
			}
		}
		return BulkWriteResponse{}, nil
	}
	syncer := newMountRolloutTestSyncer(t, client)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("bootstrap sync: %v", err)
	}

	client.files[remotePath] = RemoteFile{
		Path: remotePath, Revision: "rev_remote", ContentType: "application/json", Content: remote,
	}
	localPath := filepath.Join(syncer.localRoot, "messages", "message.json")
	if err := os.WriteFile(localPath, []byte(local), 0o644); err != nil {
		t.Fatalf("write diverging local edit: %v", err)
	}

	if err := syncer.HandleLocalChange(context.Background(), "messages/message.json", fsnotify.Write); err != nil {
		t.Fatalf("push of diverging content: %v", err)
	}

	// The local edit is preserved in an artifact and the remote wins the file:
	// today's behavior, unchanged.
	artifactPath := conflictArtifactPath(syncer.conflictsDir, remotePath, "rev_base")
	assertLocalFileContent(t, artifactPath, local)
	assertLocalFileContent(t, localPath, remote)
}

/**
 * The property that actually matters, and the one the first version of this fix missed.
 *
 * Removing the conflict artifact is not enough. `flushOutboxRecordChunk` treats ANY
 * handled per-file error (handleWriteError returning a nil error) as a failure and calls
 * `failOutboxRecord`, which archives the command under `.relay/outbox/failed`. It is that
 * unresolved outbox entry — not the artifact — that the warm-start audit refuses to boot a
 * sandbox with (`relayfile_mount_intent_warm_audit_unresolved_outbox`).
 *
 * So a fix that only suppressed the artifact would leave the agent exactly as wedged,
 * while every artifact-level assertion passed. This test asserts on the outbox.
 */
func TestIdempotentWriteAcksTheOutboxRecordInsteadOfFailingIt(t *testing.T) {
	localRoot := t.TempDir()
	remotePath := normalizeRemotePath("/messages/message.json")
	localPath := filepath.Join(localRoot, "messages", "message.json")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("create local parent: %v", err)
	}
	delivered := []byte(`{"text":"the digest that already landed"}`)
	if err := os.WriteFile(localPath, delivered, 0o644); err != nil {
		t.Fatalf("write local file: %v", err)
	}

	// The lost-ack shape: the remote already holds our exact bytes under a revision we
	// never observed, so our IfMatch is stale and the server answers 409 for a write
	// that already succeeded.
	client := &fakeClient{files: map[string]RemoteFile{
		remotePath: {
			Path:        remotePath,
			Revision:    "rev_delivered",
			ContentType: "application/json",
			Content:     string(delivered),
		},
	}}
	client.bulkWriteResponseFunc = func(_ context.Context, _ string, files []BulkWriteFile) (BulkWriteResponse, error) {
		return BulkWriteResponse{ErrorCount: len(files), Errors: []BulkWriteError{{
			Path: files[0].Path, Code: "conflict", Message: "revision conflict",
		}}}, nil
	}

	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_idempotent_outbox",
		RemoteRoot:  "/",
		LocalRoot:   localRoot,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	if err := syncer.loadState(); err != nil {
		t.Fatalf("load state: %v", err)
	}
	snapshot, err := readLocalSnapshot(localPath, true)
	if err != nil {
		t.Fatalf("read local snapshot: %v", err)
	}
	pending, err := syncer.preparePendingBulkWrite(
		context.Background(), remotePath, localPath, snapshot, trackedFile{}, false,
	)
	if err != nil || pending == nil {
		t.Fatalf("prepare pending write: pending=%v err=%v", pending, err)
	}
	if _, err := syncer.ensureOutboxRecord(*pending); err != nil {
		t.Fatalf("persist outbox record: %v", err)
	}

	if err := syncer.FlushOutboxOnce(context.Background()); err != nil {
		t.Fatalf("flush outbox: %v", err)
	}

	failed := readOutboxRecordsInDirForTest(t, filepath.Join(localRoot, ".relay", "outbox", "failed"))
	if len(failed) != 0 {
		t.Fatalf("outbox/failed holds %d record(s); a delivery that SUCCEEDED must not be filed as failed — this is what wedges warm start", len(failed))
	}

	acked := readOutboxRecordsInDirForTest(t, filepath.Join(localRoot, ".relay", "outbox", "acked"))
	if len(acked) != 1 {
		t.Fatalf("outbox/acked holds %d record(s), want 1", len(acked))
	}

	// And nothing is left pending, or the next flush would retry a write that landed.
	if pendingLeft := readPendingOutboxRecordsForTest(t, localRoot); len(pendingLeft) != 0 {
		t.Fatalf("outbox/pending holds %d record(s), want 0", len(pendingLeft))
	}
}

/**
 * The negative control for the outbox path: a genuine divergence must STILL be filed as
 * failed. Otherwise the fix above would silently swallow real write failures, which is a
 * far worse bug than the one it set out to fix.
 */
func TestDivergentWriteConflictStillFailsTheOutboxRecord(t *testing.T) {
	localRoot := t.TempDir()
	remotePath := normalizeRemotePath("/messages/message.json")
	localPath := filepath.Join(localRoot, "messages", "message.json")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("create local parent: %v", err)
	}
	if err := os.WriteFile(localPath, []byte(`{"text":"my edit"}`), 0o644); err != nil {
		t.Fatalf("write local file: %v", err)
	}

	client := &fakeClient{files: map[string]RemoteFile{
		remotePath: {
			Path:        remotePath,
			Revision:    "rev_remote",
			ContentType: "application/json",
			Content:     `{"text":"someone else's edit"}`,
		},
	}}
	client.bulkWriteResponseFunc = func(_ context.Context, _ string, files []BulkWriteFile) (BulkWriteResponse, error) {
		return BulkWriteResponse{ErrorCount: len(files), Errors: []BulkWriteError{{
			Path: files[0].Path, Code: "conflict", Message: "revision conflict",
		}}}, nil
	}

	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_divergent_outbox",
		RemoteRoot:  "/",
		LocalRoot:   localRoot,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	if err := syncer.loadState(); err != nil {
		t.Fatalf("load state: %v", err)
	}
	snapshot, err := readLocalSnapshot(localPath, true)
	if err != nil {
		t.Fatalf("read local snapshot: %v", err)
	}
	pending, err := syncer.preparePendingBulkWrite(
		context.Background(), remotePath, localPath, snapshot, trackedFile{}, false,
	)
	if err != nil || pending == nil {
		t.Fatalf("prepare pending write: pending=%v err=%v", pending, err)
	}
	if _, err := syncer.ensureOutboxRecord(*pending); err != nil {
		t.Fatalf("persist outbox record: %v", err)
	}

	if err := syncer.FlushOutboxOnce(context.Background()); err != nil {
		t.Fatalf("flush outbox: %v", err)
	}

	failed := readOutboxRecordsInDirForTest(t, filepath.Join(localRoot, ".relay", "outbox", "failed"))
	if len(failed) != 1 {
		t.Fatalf("outbox/failed holds %d record(s), want 1 — a real conflict must still be reported", len(failed))
	}
	acked := readOutboxRecordsInDirForTest(t, filepath.Join(localRoot, ".relay", "outbox", "acked"))
	if len(acked) != 0 {
		t.Fatalf("outbox/acked holds %d record(s), want 0 for a genuine divergence", len(acked))
	}
}

/**
 * An idempotent write is an ADMITTED write, so it must claim up-path ownership exactly
 * as the accepted-write branch does. Without it, a full pull still in flight is free to
 * replay the path with older bytes or infer it absent — silently undoing a delivery that
 * succeeded.
 */
func TestIdempotentWriteClaimsUpPathOwnership(t *testing.T) {
	localRoot := t.TempDir()
	remotePath := normalizeRemotePath("/messages/message.json")
	localPath := filepath.Join(localRoot, "messages", "message.json")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("create local parent: %v", err)
	}
	delivered := []byte(`{"text":"already landed"}`)
	if err := os.WriteFile(localPath, delivered, 0o644); err != nil {
		t.Fatalf("write local file: %v", err)
	}

	client := &fakeClient{files: map[string]RemoteFile{
		remotePath: {
			Path:        remotePath,
			Revision:    "rev_delivered",
			ContentType: "application/json",
			Content:     string(delivered),
		},
	}}
	client.bulkWriteResponseFunc = func(_ context.Context, _ string, files []BulkWriteFile) (BulkWriteResponse, error) {
		return BulkWriteResponse{ErrorCount: len(files), Errors: []BulkWriteError{{
			Path: files[0].Path, Code: "conflict", Message: "revision conflict",
		}}}, nil
	}

	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_idempotent_uppath",
		RemoteRoot:  "/",
		LocalRoot:   localRoot,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	if err := syncer.loadState(); err != nil {
		t.Fatalf("load state: %v", err)
	}
	snapshot, err := readLocalSnapshot(localPath, true)
	if err != nil {
		t.Fatalf("read local snapshot: %v", err)
	}
	pending, err := syncer.preparePendingBulkWrite(
		context.Background(), remotePath, localPath, snapshot, trackedFile{}, false,
	)
	if err != nil || pending == nil {
		t.Fatalf("prepare pending write: pending=%v err=%v", pending, err)
	}
	if _, err := syncer.ensureOutboxRecord(*pending); err != nil {
		t.Fatalf("persist outbox record: %v", err)
	}

	// The race cubic described: a full pull is in flight while the write is
	// acknowledged. Both the mark and its reader are no-ops otherwise, so without
	// this the assertion below could never hold and the test would prove nothing.
	syncer.fullPullActive = true
	if syncer.fullPullUpPaths == nil {
		syncer.fullPullUpPaths = map[string]struct{}{}
	}

	if err := syncer.FlushOutboxOnce(context.Background()); err != nil {
		t.Fatalf("flush outbox: %v", err)
	}

	if !syncer.fullPullPathTouchedByUpPath(remotePath) {
		t.Fatal("idempotent write did not claim up-path ownership; an in-flight full pull could replay it with older bytes")
	}
}

/**
 * A matching hash is not enough — the revision must be usable.
 *
 * Adopting an empty revision clears the tracked one, and the next local edit then falls
 * back to `ExpectedRevision: "0"`, which `Store.BulkWrite` rejects against an existing
 * file. The path would never sync again. So an empty revision must fall through to the
 * ordinary conflict handling, which keeps the record retryable, rather than be
 * acknowledged as delivered.
 */
func TestIdempotentWriteIsNotAckedWithoutAUsableRevision(t *testing.T) {
	localRoot := t.TempDir()
	remotePath := normalizeRemotePath("/messages/message.json")
	localPath := filepath.Join(localRoot, "messages", "message.json")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("create local parent: %v", err)
	}
	delivered := []byte(`{"text":"already landed"}`)
	if err := os.WriteFile(localPath, delivered, 0o644); err != nil {
		t.Fatalf("write local file: %v", err)
	}

	// Content matches, revision does not come back.
	client := &fakeClient{files: map[string]RemoteFile{
		remotePath: {
			Path:        remotePath,
			Revision:    "",
			ContentType: "application/json",
			Content:     string(delivered),
		},
	}}
	client.bulkWriteResponseFunc = func(_ context.Context, _ string, files []BulkWriteFile) (BulkWriteResponse, error) {
		return BulkWriteResponse{ErrorCount: len(files), Errors: []BulkWriteError{{
			Path: files[0].Path, Code: "conflict", Message: "revision conflict",
		}}}, nil
	}

	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_no_revision",
		RemoteRoot:  "/",
		LocalRoot:   localRoot,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	if err := syncer.loadState(); err != nil {
		t.Fatalf("load state: %v", err)
	}
	snapshot, err := readLocalSnapshot(localPath, true)
	if err != nil {
		t.Fatalf("read local snapshot: %v", err)
	}
	pending, err := syncer.preparePendingBulkWrite(
		context.Background(), remotePath, localPath, snapshot, trackedFile{}, false,
	)
	if err != nil || pending == nil {
		t.Fatalf("prepare pending write: pending=%v err=%v", pending, err)
	}
	if _, err := syncer.ensureOutboxRecord(*pending); err != nil {
		t.Fatalf("persist outbox record: %v", err)
	}

	if err := syncer.FlushOutboxOnce(context.Background()); err != nil {
		t.Fatalf("flush outbox: %v", err)
	}

	acked := readOutboxRecordsInDirForTest(t, filepath.Join(localRoot, ".relay", "outbox", "acked"))
	if len(acked) != 0 {
		t.Fatalf("outbox/acked holds %d record(s); a write must not be acked on an unusable revision — the path would never sync again", len(acked))
	}
	// Deliberately NOT asserting on tracked state here. With an unusable revision this
	// falls through to the ordinary conflict handling, and what that leaves behind is
	// pre-existing behaviour this change neither introduces nor owns. The property that
	// belongs to this change is the one above: the write is not acknowledged as
	// delivered, so it stays retryable instead of being silently accepted on a revision
	// that would make the path unsyncable.
}
