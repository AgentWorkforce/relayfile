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
