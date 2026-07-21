package mountsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteLongBasenameIsShortenedAndRoundTripsForWriteback(t *testing.T) {
	t.Parallel()

	localRoot := t.TempDir()
	longBase := strings.Repeat("reddit-title-", 22) + "1uvwn9q.json"
	if len(longBase) <= 255 {
		t.Fatalf("test basename is only %d bytes; want > 255", len(longBase))
	}
	remotePath := "/reddit/subreddits/localllama/posts/" + longBase
	client := &fakeClient{}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_long_reddit_name",
		RemoteRoot:  "/",
		LocalRoot:   localRoot,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	localPath, err := syncer.remoteToLocalPath(remotePath)
	if err != nil {
		t.Fatalf("remoteToLocalPath: %v", err)
	}
	if got := len([]byte(filepath.Base(localPath))); got > 255 {
		t.Fatalf("local basename is %d bytes, want <= NAME_MAX (255): %q", got, filepath.Base(localPath))
	}
	localPathAgain, err := syncer.remoteToLocalPath(remotePath)
	if err != nil {
		t.Fatalf("second remoteToLocalPath: %v", err)
	}
	if localPathAgain != localPath {
		t.Fatalf("long-name mapping is not deterministic: %q != %q", localPathAgain, localPath)
	}

	file := RemoteFile{
		Path:        remotePath,
		Revision:    "rev_reddit_long_name",
		ContentType: "application/json",
		Content:     `{"id":"1uvwn9q"}`,
	}
	if err := syncer.applyRemoteFile(remotePath, file, nil); err != nil {
		t.Fatalf("applyRemoteFile(long basename): %v", err)
	}
	if got, err := os.ReadFile(localPath); err != nil {
		t.Fatalf("read shortened local mirror: %v", err)
	} else if string(got) != file.Content {
		t.Fatalf("shortened local mirror content = %q, want %q", got, file.Content)
	}

	gotRemote, err := syncer.localPathToRemotePath(localPath, nil)
	if err != nil {
		t.Fatalf("localPathToRemotePath(shortened path): %v", err)
	}
	if gotRemote != remotePath {
		t.Fatalf("writeback remote path = %q, want exact original %q", gotRemote, remotePath)
	}
	rel, err := filepath.Rel(localRoot, localPath)
	if err != nil {
		t.Fatalf("relative shortened path: %v", err)
	}
	if got := syncer.localRelativeToRemotePath(rel); got != remotePath {
		t.Fatalf("watcher writeback remote path = %q, want exact original %q", got, remotePath)
	}

	// Exercise the real watcher/outbox writeback boundary, not only the path
	// helpers: editing the shortened local file must update the original remote
	// provider name and must never create a hash-shortened cloud path.
	if err := syncer.saveState(); err != nil {
		t.Fatalf("persist tracked long-name state: %v", err)
	}
	updated := `{"id":"1uvwn9q","edited":true}`
	if err := os.WriteFile(localPath, []byte(updated), 0o644); err != nil {
		t.Fatalf("edit shortened local mirror: %v", err)
	}
	if err := syncer.HandleLocalChange(context.Background(), filepath.ToSlash(rel), 0); err != nil {
		t.Fatalf("write back shortened local mirror: %v", err)
	}
	if got := client.files[remotePath].Content; got != updated {
		t.Fatalf("original remote path content = %q, want %q", got, updated)
	}
	shortRemote := normalizeRemotePath("/" + filepath.ToSlash(rel))
	if shortRemote != remotePath {
		if _, exists := client.files[shortRemote]; exists {
			t.Fatalf("writeback created shortened remote path %q", shortRemote)
		}
	}
}

func TestRemoteLongBasenameHashSuffixAvoidsPrefixCollisions(t *testing.T) {
	t.Parallel()

	localRoot := t.TempDir()
	prefix := strings.Repeat("same-reddit-title-", 18)
	remoteA := "/reddit/posts/" + prefix + "a.json"
	remoteB := "/reddit/posts/" + prefix + "b.json"
	localA, err := remoteToLocalPath(localRoot, "/", remoteA)
	if err != nil {
		t.Fatalf("map A: %v", err)
	}
	localB, err := remoteToLocalPath(localRoot, "/", remoteB)
	if err != nil {
		t.Fatalf("map B: %v", err)
	}
	if filepath.Base(localA) == filepath.Base(localB) {
		t.Fatalf("distinct long remote basenames collided at %q", filepath.Base(localA))
	}
	for label, localPath := range map[string]string{"A": localA, "B": localB} {
		if got := len([]byte(filepath.Base(localPath))); got > 255 {
			t.Fatalf("local basename %s is %d bytes, want <= NAME_MAX (255)", label, got)
		}
	}
}

func TestLocallyCreatedLongValidBasenameKeepsIdentityAcrossReconcile(t *testing.T) {
	t.Parallel()

	localRoot := t.TempDir()
	longBase := strings.Repeat("local-title-", 19) + "draft.json"
	if got := len(longBase); got <= maxLocalMirrorBasenameBytes || got > 255 {
		t.Fatalf("test basename is %d bytes, want %d < length <= 255", got, maxLocalMirrorBasenameBytes)
	}
	remotePath := normalizeRemotePath("/reddit/posts/" + longBase)
	localPath := filepath.Join(localRoot, "reddit", "posts", longBase)
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("create local parent: %v", err)
	}

	client := &fakeClient{}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_local_long_valid_name",
		RemoteRoot:  "/",
		LocalRoot:   localRoot,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	if err := os.WriteFile(localPath, []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("create long valid local file: %v", err)
	}
	// The scan path derives the correct remote name from the local path, but
	// must keep reading the original local identity instead of remapping the
	// remote path through the mirror-shortening function.
	if _, err := syncer.pushLocal(context.Background()); err != nil {
		t.Fatalf("initial scan writeback remapped the user-created name: %v", err)
	}
	if got, ok := client.files[remotePath]; !ok {
		t.Fatalf("initial writeback did not create exact remote path %q", remotePath)
	} else if got.Content != `{"version":1}` {
		t.Fatalf("initial scan writeback content = %q, want version 1", got.Content)
	}
	pendingRetry, err := syncer.outboxRecordAsPending(outboxRecord{
		RemotePath:  remotePath,
		ContentType: "application/json",
		Content:     `{"version":1}`,
		Hash:        hashBytes([]byte(`{"version":1}`)),
	}, syncer.state.Files[remotePath], true)
	if err != nil {
		t.Fatalf("rebuild pending retry: %v", err)
	}
	if pendingRetry.localPath != localPath {
		t.Fatalf("pending retry mapped to %q, want preserved %q", pendingRetry.localPath, localPath)
	}

	// Subsequent remote reconciliation must update the same user-created path
	// rather than materializing a second hash-shortened sibling.
	remoteUpdate := RemoteFile{
		Path:        remotePath,
		Revision:    "rev_remote_update",
		ContentType: "application/json",
		Content:     `{"version":3}`,
	}
	if err := syncer.applyRemoteFile(remotePath, remoteUpdate, nil); err != nil {
		t.Fatalf("apply remote update to preserved local identity: %v", err)
	}
	if got, err := os.ReadFile(localPath); err != nil {
		t.Fatalf("read preserved local path: %v", err)
	} else if string(got) != remoteUpdate.Content {
		t.Fatalf("preserved local content = %q, want %q", got, remoteUpdate.Content)
	}
	shortenedPath, err := remoteToLocalPath(localRoot, "/", remotePath)
	if err != nil {
		t.Fatalf("compute default shortened path: %v", err)
	}
	if shortenedPath == localPath {
		t.Fatalf("test did not exercise a shortened default path")
	}
	if _, err := os.Stat(shortenedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reconcile created shortened sibling %q: %v", shortenedPath, err)
	}

	if err := syncer.saveState(); err != nil {
		t.Fatalf("persist local identity mapping: %v", err)
	}
	restarted, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_local_long_valid_name",
		RemoteRoot:  "/",
		LocalRoot:   localRoot,
	})
	if err != nil {
		t.Fatalf("restart syncer: %v", err)
	}
	if err := restarted.loadState(); err != nil {
		t.Fatalf("load persisted local identity mapping: %v", err)
	}
	if got, err := restarted.remoteToLocalPath(remotePath); err != nil {
		t.Fatalf("map remote path after restart: %v", err)
	} else if got != localPath {
		t.Fatalf("remote path mapped to %q after restart, want preserved %q", got, localPath)
	}
}

func TestLongValidBasenameOutboxIdentitySurvivesCrashBeforeStateSave(t *testing.T) {
	t.Parallel()

	localRoot := t.TempDir()
	longBase := strings.Repeat("local-title-", 19) + "crash.json"
	if got := len(longBase); got <= maxLocalMirrorBasenameBytes || got > 255 {
		t.Fatalf("test basename is %d bytes, want %d < length <= 255", got, maxLocalMirrorBasenameBytes)
	}
	remotePath := normalizeRemotePath("/reddit/posts/" + longBase)
	localPath := filepath.Join(localRoot, "reddit", "posts", longBase)
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("create local parent: %v", err)
	}
	content := []byte(`{"version":"crash"}`)
	if err := os.WriteFile(localPath, content, 0o644); err != nil {
		t.Fatalf("create long valid local file: %v", err)
	}

	client := &fakeClient{}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_local_long_crash",
		RemoteRoot:  "/",
		LocalRoot:   localRoot,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	if err := syncer.loadState(); err != nil {
		t.Fatalf("load initial state: %v", err)
	}
	snapshot, err := readLocalSnapshot(localPath, true)
	if err != nil {
		t.Fatalf("read local snapshot: %v", err)
	}
	pending, err := syncer.preparePendingBulkWrite(context.Background(), remotePath, localPath, snapshot, trackedFile{}, false)
	if err != nil || pending == nil {
		t.Fatalf("prepare pending write: pending=%v err=%v", pending, err)
	}
	record, err := syncer.ensureOutboxRecord(*pending)
	if err != nil {
		t.Fatalf("persist durable outbox record: %v", err)
	}
	wantRel := filepath.ToSlash(filepath.Join("reddit", "posts", longBase))
	if record.LocalRelativePath != wantRel {
		t.Fatalf("durable outbox local identity = %q, want %q", record.LocalRelativePath, wantRel)
	}
	persisted, err := syncer.readOutboxRecord(syncer.pendingOutboxPath(record.CommandID))
	if err != nil {
		t.Fatalf("read durable outbox record: %v", err)
	}
	if persisted.LocalRelativePath != wantRel {
		t.Fatalf("persisted outbox local identity = %q, want %q", persisted.LocalRelativePath, wantRel)
	}
	// Simulate a crash here: the outbox is durable, but the in-memory state
	// containing LocalRelativePath has deliberately not been saved.
	restarted, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_local_long_crash",
		RemoteRoot:  "/",
		LocalRoot:   localRoot,
	})
	if err != nil {
		t.Fatalf("restart syncer: %v", err)
	}
	if err := restarted.FlushOutboxOnce(context.Background()); err != nil {
		t.Fatalf("flush durable outbox after crash: %v", err)
	}
	if got, err := restarted.remoteToLocalPath(remotePath); err != nil {
		t.Fatalf("map remote path after durable retry: %v", err)
	} else if got != localPath {
		t.Fatalf("durable outbox retry lost local identity: mapped=%q want=%q", got, localPath)
	}
}

func TestExistingLongValidBasenameMigratesFromPrefixState(t *testing.T) {
	t.Parallel()

	localRoot := t.TempDir()
	longBase := strings.Repeat("legacy-title-", 18) + "upgrade.json"
	if got := len(longBase); got <= maxLocalMirrorBasenameBytes || got > 255 {
		t.Fatalf("test basename is %d bytes, want %d < length <= 255", got, maxLocalMirrorBasenameBytes)
	}
	remotePath := normalizeRemotePath("/reddit/posts/" + longBase)
	localPath := filepath.Join(localRoot, "reddit", "posts", longBase)
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("create legacy local parent: %v", err)
	}
	content := []byte(`{"version":"legacy"}`)
	if err := os.WriteFile(localPath, content, 0o644); err != nil {
		t.Fatalf("create legacy long local file: %v", err)
	}

	client := &fakeClient{}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_local_long_upgrade",
		RemoteRoot:  "/",
		LocalRoot:   localRoot,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	if err := syncer.loadState(); err != nil {
		t.Fatalf("load initial state: %v", err)
	}
	syncer.state.Files[remotePath] = trackedFile{
		Revision:    "rev_before_long_name_fix",
		ContentType: "application/json",
		Hash:        hashBytes(content),
	}
	if err := syncer.saveState(); err != nil {
		t.Fatalf("persist pre-fix state: %v", err)
	}

	restarted, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_local_long_upgrade",
		RemoteRoot:  "/",
		LocalRoot:   localRoot,
	})
	if err != nil {
		t.Fatalf("restart upgraded syncer: %v", err)
	}
	if err := restarted.loadState(); err != nil {
		t.Fatalf("load pre-fix state: %v", err)
	}
	remoteUpdate := RemoteFile{
		Path:        remotePath,
		Revision:    "rev_after_long_name_fix",
		ContentType: "application/json",
		Content:     `{"version":"upgraded"}`,
	}
	if err := restarted.applyRemoteFile(remotePath, remoteUpdate, nil); err != nil {
		t.Fatalf("apply remote update while migrating pre-fix state: %v", err)
	}
	wantRel := filepath.ToSlash(filepath.Join("reddit", "posts", longBase))
	if got := restarted.state.Files[remotePath].LocalRelativePath; got != wantRel {
		t.Fatalf("remote apply overwrote migrated local identity = %q, want %q", got, wantRel)
	}
	if got, err := restarted.remoteToLocalPath(remotePath); err != nil {
		t.Fatalf("map pre-fix tracked path: %v", err)
	} else if got != localPath {
		t.Fatalf("upgrade remapped existing local identity: mapped=%q want=%q", got, localPath)
	}
	if got, err := os.ReadFile(localPath); err != nil {
		t.Fatalf("read migrated local path after remote apply: %v", err)
	} else if string(got) != remoteUpdate.Content {
		t.Fatalf("migrated local content = %q, want %q", got, remoteUpdate.Content)
	}
}

func TestLegacyLongValidBasenameOutboxMigratesWithoutState(t *testing.T) {
	t.Parallel()

	localRoot := t.TempDir()
	longBase := strings.Repeat("legacy-outbox-", 16) + "upgrade.json"
	if got := len(longBase); got <= maxLocalMirrorBasenameBytes || got > 255 {
		t.Fatalf("test basename is %d bytes, want %d < length <= 255", got, maxLocalMirrorBasenameBytes)
	}
	remotePath := normalizeRemotePath("/reddit/posts/" + longBase)
	localPath := filepath.Join(localRoot, "reddit", "posts", longBase)
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("create legacy local parent: %v", err)
	}
	content := `{"version":"legacy-outbox"}`
	if err := os.WriteFile(localPath, []byte(content), 0o644); err != nil {
		t.Fatalf("create legacy long local file: %v", err)
	}

	client := &fakeClient{}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_local_long_legacy_outbox",
		RemoteRoot:  "/",
		LocalRoot:   localRoot,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	record := outboxRecord{
		CommandID:     "mountcmd_legacy_long_name",
		WorkspaceID:   "ws_local_long_legacy_outbox",
		RemotePath:    remotePath,
		ContentType:   "application/json",
		Content:       content,
		Hash:          hashBytes([]byte(content)),
		Status:        outboxStatusPending,
		FirstSeenAt:   "2026-06-11T12:00:00Z",
		CorrelationID: "mountcmd_legacy_long_name",
	}
	if err := syncer.saveOutboxRecord(record); err != nil {
		t.Fatalf("persist legacy outbox record: %v", err)
	}

	restarted, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_local_long_legacy_outbox",
		RemoteRoot:  "/",
		LocalRoot:   localRoot,
	})
	if err != nil {
		t.Fatalf("restart upgraded syncer: %v", err)
	}
	if err := restarted.FlushOutboxOnce(context.Background()); err != nil {
		t.Fatalf("flush legacy outbox after upgrade: %v", err)
	}
	if got, err := restarted.remoteToLocalPath(remotePath); err != nil {
		t.Fatalf("map legacy outbox path: %v", err)
	} else if got != localPath {
		t.Fatalf("legacy outbox remapped existing identity: mapped=%q want=%q", got, localPath)
	}
}

func TestExistingExactSiblingDoesNotStealShortenedTrackedIdentity(t *testing.T) {
	t.Parallel()

	localRoot := t.TempDir()
	longBase := strings.Repeat("remote-title-", 18) + "tracked.json"
	if got := len(longBase); got <= maxLocalMirrorBasenameBytes || got > 255 {
		t.Fatalf("test basename is %d bytes, want %d < length <= 255", got, maxLocalMirrorBasenameBytes)
	}
	remotePath := normalizeRemotePath("/reddit/posts/" + longBase)
	shortenedPath, err := remoteToLocalPath(localRoot, "/", remotePath)
	if err != nil {
		t.Fatalf("map shortened mirror path: %v", err)
	}
	exactPath, err := remoteToLocalPathWithShortening(localRoot, "/", remotePath, false)
	if err != nil {
		t.Fatalf("map exact sibling path: %v", err)
	}
	for _, path := range []string{shortenedPath, exactPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create parent for %s: %v", path, err)
		}
	}
	shortenedContent := []byte(`{"source":"remote"}`)
	if err := os.WriteFile(shortenedPath, shortenedContent, 0o644); err != nil {
		t.Fatalf("create shortened tracked mirror: %v", err)
	}
	if err := os.WriteFile(exactPath, []byte(`{"source":"local-sibling"}`), 0o644); err != nil {
		t.Fatalf("create exact sibling: %v", err)
	}

	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID: "ws_long_exact_sibling",
		RemoteRoot:  "/",
		LocalRoot:   localRoot,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	if err := syncer.loadState(); err != nil {
		t.Fatalf("load state: %v", err)
	}
	syncer.state.Files[remotePath] = trackedFile{
		Revision:    "rev_shortened_mirror",
		ContentType: "application/json",
		Hash:        hashBytes(shortenedContent),
	}
	if got, err := syncer.remoteToLocalPath(remotePath); err != nil {
		t.Fatalf("map tracked remote path: %v", err)
	} else if got != shortenedPath {
		t.Fatalf("exact sibling stole shortened tracked identity: mapped=%q want=%q", got, shortenedPath)
	}
	localFiles, err := syncer.scanLocalFiles()
	if err != nil {
		t.Fatalf("scan local files: %v", err)
	}
	shortRel, err := filepath.Rel(localRoot, shortenedPath)
	if err != nil {
		t.Fatalf("shortened relative path: %v", err)
	}
	shortenedLiteralRemote := normalizeRemotePath("/" + filepath.ToSlash(shortRel))
	if _, exists := localFiles[shortenedLiteralRemote]; exists && shortenedLiteralRemote != remotePath {
		t.Fatalf("shortened mirror leaked as new cloud path %q", shortenedLiteralRemote)
	}
}

func TestBootstrapSkipsOneUnmaterializablePathAndContinues(t *testing.T) {
	t.Parallel()

	localRoot := t.TempDir()
	// Keep every component legal while making the total remote path just under
	// MaxRemotePathLen. Adding the absolute temp-root prefix pushes the local
	// mirror path over PATH_MAX on POSIX filesystems, deterministically yielding
	// ENAMETOOLONG without relying on chmod behavior (which differs as root).
	badPath := "/reddit"
	for len(badPath)+len("/segment0123456789")+len("/pathological.json") < MaxRemotePathLen-8 {
		badPath += "/segment0123456789"
	}
	badPath += "/pathological.json"
	healthyPath := "/slack/channels/C1/messages/healthy.json"
	client := &fakeClient{files: map[string]RemoteFile{
		badPath: {
			Path:        badPath,
			Revision:    "rev_bad",
			ContentType: "application/json",
			Content:     `{"bad":true}`,
		},
		healthyPath: {
			Path:        healthyPath,
			Revision:    "rev_healthy",
			ContentType: "application/json",
			Content:     `{"ok":true}`,
		},
	}}
	logger := &captureLogger{}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_skip_bad_local_path",
		RemoteRoot:  "/",
		LocalRoot:   localRoot,
		Logger:      logger,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	if err := syncer.pullRemoteFullTree(context.Background(), nil, bootstrapProgress{}); err != nil {
		t.Fatalf("bootstrap should skip one path-local failure and continue: %v", err)
	}
	if !syncer.state.BootstrapComplete {
		t.Fatal("bootstrap did not complete after skipping one unmaterializable path")
	}
	if _, ok := syncer.state.SkippedMaterializations[badPath]; !ok {
		t.Fatalf("skipped path %s was not durably queued for retry", badPath)
	}
	if err := syncer.saveStateWithoutLocalScan(); err != nil {
		t.Fatalf("persist skipped-materialization queue: %v", err)
	}
	restarted, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_skip_bad_local_path",
		RemoteRoot:  "/",
		LocalRoot:   localRoot,
		Logger:      logger,
	})
	if err != nil {
		t.Fatalf("new restarted syncer: %v", err)
	}
	if err := restarted.loadState(); err != nil {
		t.Fatalf("load persisted skipped-materialization queue: %v", err)
	}
	if _, ok := restarted.state.SkippedMaterializations[badPath]; !ok {
		t.Fatalf("skipped path %s was lost across process restart", badPath)
	}
	healthyLocal, err := syncer.remoteToLocalPath(healthyPath)
	if err != nil {
		t.Fatalf("map healthy path: %v", err)
	}
	if got, err := os.ReadFile(healthyLocal); err != nil {
		t.Fatalf("healthy sibling was not mirrored: %v", err)
	} else if string(got) != `{"ok":true}` {
		t.Fatalf("healthy sibling content = %q", got)
	}
	if got := strings.Join(logger.lines, "\n"); !strings.Contains(got, "warning") ||
		!strings.Contains(got, badPath) || !strings.Contains(got, "skipping") {
		t.Fatalf("missing skip-and-continue warning for %s; logs:\n%s", badPath, got)
	}
}

func TestSkippedMaterializationIsPersistedRetriedAndCleared(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure is not enforceable as root")
	}

	localRoot := t.TempDir()
	blockedDir := filepath.Join(localRoot, "reddit", "posts")
	if err := os.MkdirAll(blockedDir, 0o755); err != nil {
		t.Fatalf("create blocked directory: %v", err)
	}
	if err := os.Chmod(blockedDir, 0o555); err != nil {
		t.Fatalf("make directory read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blockedDir, 0o755) })

	badPath := "/reddit/posts/temporarily-blocked.json"
	healthyPath := "/slack/healthy.json"
	client := &fakeClient{files: map[string]RemoteFile{
		badPath: {
			Path:        badPath,
			Revision:    "rev_bad",
			ContentType: "application/json",
			Content:     `{"retry":true}`,
		},
		healthyPath: {
			Path:        healthyPath,
			Revision:    "rev_healthy",
			ContentType: "application/json",
			Content:     `{"ok":true}`,
		},
	}}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_retry_skipped_materialization",
		RemoteRoot:  "/",
		LocalRoot:   localRoot,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	if err := syncer.pullRemoteFullTree(context.Background(), nil, bootstrapProgress{}); err != nil {
		t.Fatalf("bootstrap with one blocked file: %v", err)
	}
	if _, ok := syncer.state.SkippedMaterializations[badPath]; !ok {
		t.Fatalf("blocked path %s was not persisted for retry", badPath)
	}
	if got := client.readFileCallsByPath[badPath]; got != 1 {
		t.Fatalf("initial read count for blocked path = %d, want 1", got)
	}

	if err := os.Chmod(blockedDir, 0o755); err != nil {
		t.Fatalf("unblock directory: %v", err)
	}
	// Make the normal event path a quiet fast-path. The only reason the remote
	// file is read again must be the durable skipped-materialization retry.
	syncer.state.EventsCursor = "evt_tip"
	if err := syncer.pullRemote(context.Background(), nil); err != nil {
		t.Fatalf("retry skipped materialization on quiet cycle: %v", err)
	}
	if _, ok := syncer.state.SkippedMaterializations[badPath]; ok {
		t.Fatalf("successfully materialized path %s remained queued", badPath)
	}
	if got := client.readFileCallsByPath[badPath]; got != 2 {
		t.Fatalf("read count after targeted retry = %d, want 2", got)
	}
	localPath, err := syncer.remoteToLocalPath(badPath)
	if err != nil {
		t.Fatalf("map retried path: %v", err)
	}
	if got, err := os.ReadFile(localPath); err != nil {
		t.Fatalf("retried file was not materialized: %v", err)
	} else if string(got) != `{"retry":true}` {
		t.Fatalf("retried file content = %q", got)
	}
}
