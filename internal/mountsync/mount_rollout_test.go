package mountsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsnotify"
)

func newMountRolloutTestSyncer(t *testing.T, client *fakeClient) *Syncer {
	t.Helper()
	websocketDisabled := false
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_rollout",
		RemoteRoot:  "/notion",
		LocalRoot:   t.TempDir(),
		WebSocket:   &websocketDisabled,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	return syncer
}

func writeMountRolloutLocalFile(t *testing.T, syncer *Syncer, relativePath, content string) (string, localSnapshot) {
	t.Helper()
	localPath := filepath.Join(syncer.localRoot, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir local parent: %v", err)
	}
	if err := os.WriteFile(localPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write local file: %v", err)
	}
	snapshot, err := readLocalSnapshot(localPath, true)
	if err != nil {
		t.Fatalf("read local snapshot: %v", err)
	}
	return localPath, snapshot
}

func assertNoMountRolloutConflictArtifact(t *testing.T, syncer *Syncer, remotePath string) {
	t.Helper()
	matches, err := filepath.Glob(conflictArtifactGlob(syncer.conflictsDir, remotePath))
	if err != nil {
		t.Fatalf("glob conflict artifacts: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no conflict artifact for %s, got %v", remotePath, matches)
	}
}

func TestMountRolloutShadowCache(t *testing.T) {
	syncer := newMountRolloutTestSyncer(t, &fakeClient{})
	const remotePath = "/notion/pkg/rollout.go"

	t.Run("round trips eligible Go content", func(t *testing.T) {
		const revision = "rev_base"
		want := []byte("package pkg\n\nfunc Base() {}\n")
		syncer.recordShadowContent(remotePath, revision, want)

		got, ok := syncer.readShadowContent(remotePath, revision)
		if !ok {
			t.Fatal("expected recorded Go shadow content to be readable")
		}
		if string(got) != string(want) {
			t.Fatalf("shadow content = %q, want %q", got, want)
		}
	})

	t.Run("non Go content is never cached", func(t *testing.T) {
		const (
			path     = "/notion/pkg/rollout.md"
			revision = "rev_markdown"
		)
		syncer.recordShadowContent(path, revision, []byte("# ignored"))
		if _, ok := syncer.readShadowContent(path, revision); ok {
			t.Fatal("non-Go shadow content should always miss")
		}
		if _, err := os.Stat(shadowArtifactPath(syncer.mountShadowDir, path, revision)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("non-Go record unexpectedly created a shadow file: %v", err)
		}
	})

	t.Run("missing revision misses", func(t *testing.T) {
		if _, ok := syncer.readShadowContent(remotePath, "rev_never_recorded"); ok {
			t.Fatal("unrecorded revision should miss")
		}
	})

	t.Run("new revision prunes old shadow", func(t *testing.T) {
		const (
			path        = "/notion/pkg/prune.go"
			oldRevision = "rev_old"
			newRevision = "rev_new"
		)
		syncer.recordShadowContent(path, oldRevision, []byte("package pkg\n"))
		syncer.recordShadowContent(path, newRevision, []byte("package pkg\n// newer\n"))

		matches, err := filepath.Glob(shadowArtifactGlob(syncer.mountShadowDir, path))
		if err != nil {
			t.Fatalf("glob shadow files: %v", err)
		}
		if len(matches) != 1 || matches[0] != shadowArtifactPath(syncer.mountShadowDir, path, newRevision) {
			t.Fatalf("shadow files = %v, want only %s", matches, shadowArtifactPath(syncer.mountShadowDir, path, newRevision))
		}
		if _, err := os.Stat(shadowArtifactPath(syncer.mountShadowDir, path, oldRevision)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old shadow file should be pruned, stat error = %v", err)
		}
	})
}

func TestAttemptMountRolloutMergeSkipsNetworkWithoutShadow(t *testing.T) {
	const (
		remotePath = "/notion/pkg/service.go"
		base       = "package pkg\n\nfunc Service() string { return \"base\" }\n"
		local      = "package pkg\n\nfunc Service() string { return \"local\" }\n"
	)
	client := &fakeClient{mergeFileFunc: func(context.Context, string, string, string, string, string, string, string) (MergeResult, error) {
		t.Fatal("MergeFile should not be called without a shadow cache entry")
		return MergeResult{}, nil
	}}
	syncer := newMountRolloutTestSyncer(t, client)
	localPath, snapshot := writeMountRolloutLocalFile(t, syncer, "pkg/service.go", local)
	tracked := trackedFile{Revision: "rev_base", ContentType: "text/x-go", Hash: hashString(base), LocalRelativePath: "pkg/service.go"}
	syncer.state.Files[remotePath] = tracked

	if syncer.attemptMountRolloutMerge(context.Background(), remotePath, localPath, snapshot, tracked) {
		t.Fatal("merge should not apply without a shadow cache entry")
	}
	if client.mergeFileCalls != 0 {
		t.Fatalf("MergeFile calls = %d, want 0 for a shadow-cache miss", client.mergeFileCalls)
	}
}

func TestAttemptMountRolloutMergeAppliesVerifiedResult(t *testing.T) {
	const (
		remotePath = "/notion/pkg/service.go"
		base       = "package pkg\n\nfunc Alpha() string { return \"base\" }\n\nfunc Beta() string { return \"base\" }\n"
		local      = "package pkg\n\nfunc Alpha() string { return \"local\" }\n\nfunc Beta() string { return \"base\" }\n"
		merged     = "package pkg\n\nfunc Alpha() string { return \"local\" }\n\nfunc Beta() string { return \"remote\" }\n"
	)
	client := &fakeClient{files: map[string]RemoteFile{
		remotePath: {Path: remotePath, Revision: "rev_merged", ContentType: "text/x-go", Content: merged},
	}}
	client.mergeFileFunc = func(_ context.Context, workspaceID, path, strategy, baseRevision, baseContent, content, contentType string) (MergeResult, error) {
		if workspaceID != "ws_mount_rollout" || path != remotePath || strategy != MergeStrategyGoTopLevelFunctions {
			t.Fatalf("unexpected merge request workspace=%q path=%q strategy=%q", workspaceID, path, strategy)
		}
		if baseRevision != "rev_base" || baseContent != base || content != local || contentType == "" {
			t.Fatalf("unexpected merge request baseRevision=%q baseContent=%q content=%q contentType=%q", baseRevision, baseContent, content, contentType)
		}
		return MergeResult{TargetRevision: "rev_merged", Strategy: MergeStrategyGoTopLevelFunctions}, nil
	}
	syncer := newMountRolloutTestSyncer(t, client)
	localPath, snapshot := writeMountRolloutLocalFile(t, syncer, "pkg/service.go", local)
	tracked := trackedFile{Revision: "rev_base", ContentType: "text/x-go", Hash: hashString(base), LocalRelativePath: "pkg/service.go"}
	syncer.state.Files[remotePath] = tracked
	syncer.recordShadowContent(remotePath, tracked.Revision, []byte(base))

	if !syncer.attemptMountRolloutMerge(context.Background(), remotePath, localPath, snapshot, tracked) {
		t.Fatal("expected verified merge result to apply")
	}
	assertLocalFileContent(t, localPath, merged)
	updated := syncer.state.Files[remotePath]
	if updated.Revision != "rev_merged" || updated.Hash != hashString(merged) {
		t.Fatalf("tracked state after merge = %+v, want revision rev_merged and merged hash", updated)
	}
	assertNoMountRolloutConflictArtifact(t, syncer, remotePath)
}

func TestAttemptMountRolloutMergeFallsBackOnMergeConflict(t *testing.T) {
	testAttemptMountRolloutMergeHTTPError(t, "merge_conflict")
}

func TestAttemptMountRolloutMergeFallsBackOnBaseUnavailable(t *testing.T) {
	testAttemptMountRolloutMergeHTTPError(t, "merge_base_unavailable")
}

func testAttemptMountRolloutMergeHTTPError(t *testing.T, code string) {
	t.Helper()
	const (
		remotePath = "/notion/pkg/service.go"
		base       = "package pkg\n\nfunc Service() string { return \"base\" }\n"
		local      = "package pkg\n\nfunc Service() string { return \"local\" }\n"
	)
	client := &fakeClient{mergeFileFunc: func(context.Context, string, string, string, string, string, string, string) (MergeResult, error) {
		return MergeResult{}, &HTTPError{StatusCode: 409, Code: code, Message: code}
	}}
	syncer := newMountRolloutTestSyncer(t, client)
	localPath, snapshot := writeMountRolloutLocalFile(t, syncer, "pkg/service.go", local)
	tracked := trackedFile{Revision: "rev_base", ContentType: "text/x-go", Hash: hashString(base), LocalRelativePath: "pkg/service.go"}
	syncer.state.Files[remotePath] = tracked
	syncer.recordShadowContent(remotePath, tracked.Revision, []byte(base))

	if syncer.attemptMountRolloutMerge(context.Background(), remotePath, localPath, snapshot, tracked) {
		t.Fatalf("merge should fall back on %s", code)
	}
	if client.mergeFileCalls != 1 {
		t.Fatalf("MergeFile calls = %d, want 1", client.mergeFileCalls)
	}
}

func TestAttemptMountRolloutMergeReadbackFailureKeepsLocalFile(t *testing.T) {
	const (
		remotePath = "/notion/pkg/service.go"
		base       = "package pkg\n\nfunc Service() string { return \"base\" }\n"
		local      = "package pkg\n\nfunc Service() string { return \"local\" }\n"
	)
	client := &fakeClient{
		readFileErr: errors.New("readback unavailable"),
		mergeFileFunc: func(context.Context, string, string, string, string, string, string, string) (MergeResult, error) {
			return MergeResult{TargetRevision: "rev_merged"}, nil
		},
	}
	syncer := newMountRolloutTestSyncer(t, client)
	localPath, snapshot := writeMountRolloutLocalFile(t, syncer, "pkg/service.go", local)
	tracked := trackedFile{Revision: "rev_base", ContentType: "text/x-go", Hash: hashString(base), LocalRelativePath: "pkg/service.go"}
	syncer.state.Files[remotePath] = tracked
	syncer.recordShadowContent(remotePath, tracked.Revision, []byte(base))

	if syncer.attemptMountRolloutMerge(context.Background(), remotePath, localPath, snapshot, tracked) {
		t.Fatal("merge should fall back when its readback fails")
	}
	assertLocalFileContent(t, localPath, local)
	updated := syncer.state.Files[remotePath]
	if updated.Revision != tracked.Revision || updated.Hash != tracked.Hash {
		t.Fatalf("readback failure should leave tracked state untouched, got %+v", updated)
	}
}

func TestMountRolloutConflictPathAutoMergesGoFile(t *testing.T) {
	const (
		remotePath = "/notion/pkg/service.go"
		base       = "package pkg\n\nfunc Alpha() string { return \"base\" }\n\nfunc Beta() string { return \"base\" }\n"
		remote     = "package pkg\n\nfunc Alpha() string { return \"base\" }\n\nfunc Beta() string { return \"remote\" }\n"
		local      = "package pkg\n\nfunc Alpha() string { return \"local\" }\n\nfunc Beta() string { return \"base\" }\n"
		merged     = "package pkg\n\nfunc Alpha() string { return \"local\" }\n\nfunc Beta() string { return \"remote\" }\n"
	)
	client := &fakeClient{files: map[string]RemoteFile{
		remotePath: {Path: remotePath, Revision: "rev_base", ContentType: "text/x-go", Content: base},
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
	client.mergeFileFunc = func(_ context.Context, _, path, strategy, baseRevision, baseContent, content, contentType string) (MergeResult, error) {
		if path != remotePath || strategy != MergeStrategyGoTopLevelFunctions || baseRevision != "rev_base" || baseContent != base || content != local || contentType == "" {
			t.Fatalf("unexpected merge request path=%q strategy=%q baseRevision=%q baseContent=%q content=%q contentType=%q", path, strategy, baseRevision, baseContent, content, contentType)
		}
		client.files[remotePath] = RemoteFile{Path: remotePath, Revision: "rev_merged", ContentType: "text/x-go", Content: merged}
		return MergeResult{TargetRevision: "rev_merged", Strategy: MergeStrategyGoTopLevelFunctions}, nil
	}
	syncer := newMountRolloutTestSyncer(t, client)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("bootstrap sync: %v", err)
	}

	localPath := filepath.Join(syncer.localRoot, "pkg", "service.go")
	if err := syncer.HandleLocalChange(context.Background(), "pkg/service.go", fsnotify.Write); err != nil {
		t.Fatalf("confirmed-clean local change: %v", err)
	}
	if _, ok := syncer.readShadowContent(remotePath, "rev_base"); !ok {
		t.Fatal("confirmed-clean cycle did not populate the shadow cache")
	}
	client.files[remotePath] = RemoteFile{Path: remotePath, Revision: "rev_remote", ContentType: "text/x-go", Content: remote}
	if err := os.WriteFile(localPath, []byte(local), 0o644); err != nil {
		t.Fatalf("write local Go edit: %v", err)
	}

	if err := syncer.HandleLocalChange(context.Background(), "pkg/service.go", fsnotify.Write); err != nil {
		t.Fatalf("conflicting Go push: %v", err)
	}
	assertLocalFileContent(t, localPath, merged)
	assertNoMountRolloutConflictArtifact(t, syncer, remotePath)
	if client.mergeFileCalls != 1 {
		t.Fatalf("MergeFile calls = %d, want 1", client.mergeFileCalls)
	}
}

func TestMountRolloutConflictPathWithoutShadowFallsBack(t *testing.T) {
	const (
		remotePath = "/notion/pkg/service.go"
		base       = "package pkg\n\nfunc Service() string { return \"base\" }\n"
		remote     = "package pkg\n\nfunc Service() string { return \"remote\" }\n"
		local      = "package pkg\n\nfunc Service() string { return \"local\" }\n"
	)
	client := &fakeClient{files: map[string]RemoteFile{
		remotePath: {Path: remotePath, Revision: "rev_base", ContentType: "text/x-go", Content: base},
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
	client.mergeFileFunc = func(context.Context, string, string, string, string, string, string, string) (MergeResult, error) {
		t.Fatal("MergeFile should not be called when the fresh tracked file has no shadow cache entry")
		return MergeResult{}, nil
	}
	syncer := newMountRolloutTestSyncer(t, client)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("bootstrap sync: %v", err)
	}
	if _, ok := syncer.readShadowContent(remotePath, "rev_base"); ok {
		t.Fatal("fresh tracked file unexpectedly has a shadow cache entry")
	}

	localPath := filepath.Join(syncer.localRoot, "pkg", "service.go")
	client.files[remotePath] = RemoteFile{Path: remotePath, Revision: "rev_remote", ContentType: "text/x-go", Content: remote}
	if err := os.WriteFile(localPath, []byte(local), 0o644); err != nil {
		t.Fatalf("write local Go edit: %v", err)
	}
	if err := syncer.HandleLocalChange(context.Background(), "pkg/service.go", fsnotify.Write); err != nil {
		t.Fatalf("conflicting Go push without shadow: %v", err)
	}

	assertLocalFileContent(t, localPath, remote)
	artifactPath := conflictArtifactPath(syncer.conflictsDir, remotePath, "rev_base")
	assertLocalFileContent(t, artifactPath, local)
	if client.mergeFileCalls != 0 {
		t.Fatalf("MergeFile calls = %d, want 0 without a shadow cache entry", client.mergeFileCalls)
	}
}
