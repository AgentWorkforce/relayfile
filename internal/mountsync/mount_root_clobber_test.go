package mountsync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureLogger collects log lines so tests can assert that a guard fired.
type captureLogger struct {
	lines []string
}

func (l *captureLogger) Printf(format string, args ...any) {
	l.lines = append(l.lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

// TestRemoteToLocalPathRejectsRootResolution verifies that
// remoteToLocalPath never returns a path equal to the mount root. The
// pre-fix final check explicitly allowed `joined == localRoot`, leaving
// the mount root reachable as a write target via any combination of
// roots + paths whose cleaned join lands on the root directory.
func TestRemoteToLocalPathRejectsRootResolution(t *testing.T) {
	localRoot := filepath.Join(t.TempDir(), "relayfile-mount")
	if err := os.MkdirAll(localRoot, 0o755); err != nil {
		t.Fatalf("mkdir local root: %v", err)
	}

	// Empty / root-equivalent remote paths must error (existing behavior,
	// re-asserted as part of the data-loss contract).
	for _, rp := range []string{"/", ""} {
		if _, err := remoteToLocalPath(localRoot, "/", rp); err == nil {
			t.Fatalf("expected error for remote path %q resolving onto root", rp)
		}
	}

	// remoteRoot equal to remotePath (rel becomes empty) must error.
	if _, err := remoteToLocalPath(localRoot, "/relayfile-mount", "/relayfile-mount"); err == nil {
		t.Fatalf("expected error when remotePath equals remoteRoot")
	}

	// A legitimate child still resolves under (not onto) the root.
	got, err := remoteToLocalPath(localRoot, "/", "/notes.md")
	if err != nil {
		t.Fatalf("plain child rejected: %v", err)
	}
	if filepath.Clean(got) == localRoot {
		t.Fatalf("legitimate child resolved onto mount root: %s", got)
	}
	if !strings.HasPrefix(filepath.Clean(got), localRoot+string(filepath.Separator)) {
		t.Fatalf("legitimate child resolved outside root: %s", got)
	}
}

// TestLocalToRemotePathRejectsMountRoot verifies that localToRemotePath
// refuses to generate a remote path for the mount root itself. This is
// the round-trip safety net: any path whose remote form would resolve
// back onto the mount root must be rejected at generation time.
func TestLocalToRemotePathRejectsMountRoot(t *testing.T) {
	localRoot := filepath.Join(t.TempDir(), "relayfile-mount")
	if err := os.MkdirAll(localRoot, 0o755); err != nil {
		t.Fatalf("mkdir local root: %v", err)
	}

	// The mount root itself must always be rejected.
	if _, err := localToRemotePath(localRoot, "/", localRoot); err == nil {
		t.Fatalf("expected localToRemotePath to reject the mount root itself")
	}
	if _, err := localToRemotePath(localRoot, "/relayfile-mount", localRoot); err == nil {
		t.Fatalf("expected localToRemotePath to reject the mount root under any remoteRoot")
	}

	// A normal nested child round-trips cleanly.
	ok := filepath.Join(localRoot, "sub", "doc.md")
	rp, err := localToRemotePath(localRoot, "/", ok)
	if err != nil {
		t.Fatalf("normal child rejected: %v", err)
	}
	if rp != "/sub/doc.md" {
		t.Fatalf("unexpected remote path %q", rp)
	}

	// Round-trip the normal child and confirm it lands on the child, not
	// the mount root.
	back, err := remoteToLocalPath(localRoot, "/", rp)
	if err != nil {
		t.Fatalf("round-trip rejected: %v", err)
	}
	if filepath.Clean(back) == localRoot {
		t.Fatalf("round-trip landed on mount root: %s", back)
	}
}

// TestWriteFileAtomicRefusesToReplaceDirectory verifies the rename guard:
// writeFileAtomic must never replace an existing directory (the mechanism
// by which an 11MB file clobbered the mount root).
func TestWriteFileAtomicRefusesToReplaceDirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "mountdir")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	// Drop a file inside so we can prove the directory survived intact.
	inner := filepath.Join(target, "keep.txt")
	if err := os.WriteFile(inner, []byte("important"), 0o644); err != nil {
		t.Fatalf("write inner file: %v", err)
	}

	err := writeFileAtomic(target, []byte(strings.Repeat("x", 1024)), 0o644)
	if err == nil {
		t.Fatalf("expected writeFileAtomic to refuse replacing a directory")
	}

	info, statErr := os.Stat(target)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("directory was clobbered: statErr=%v info=%v", statErr, info)
	}
	if b, rErr := os.ReadFile(inner); rErr != nil || string(b) != "important" {
		t.Fatalf("inner file lost: err=%v contents=%q", rErr, string(b))
	}

	// Sanity: writing over a regular file still works.
	fileTarget := filepath.Join(root, "ok.txt")
	if err := os.WriteFile(fileTarget, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := writeFileAtomic(fileTarget, []byte("new"), 0o644); err != nil {
		t.Fatalf("writeFileAtomic over regular file failed: %v", err)
	}
	if b, _ := os.ReadFile(fileTarget); string(b) != "new" {
		t.Fatalf("expected file overwrite to succeed, got %q", string(b))
	}
}

// TestAssertNotMountRoot verifies the shared defense-in-depth guard.
func TestAssertNotMountRoot(t *testing.T) {
	root := t.TempDir()
	s := &Syncer{localRoot: filepath.Clean(root)}

	if err := s.assertNotMountRoot(root); err == nil {
		t.Fatalf("expected assertNotMountRoot to reject the mount root")
	}
	if err := s.assertNotMountRoot(root + string(filepath.Separator) + "."); err == nil {
		t.Fatalf("expected assertNotMountRoot to reject a non-clean root path")
	}
	if err := s.assertNotMountRoot(filepath.Join(root, "child.txt")); err != nil {
		t.Fatalf("legitimate child rejected by assertNotMountRoot: %v", err)
	}
}

// TestSnapshotDeleteUnsafeCircuitBreaker verifies that an empty or
// drastically truncated fresh remote listing does not authorize a delete
// pass when meaningful local state is tracked.
func TestSnapshotDeleteUnsafeCircuitBreaker(t *testing.T) {
	mk := func(n int) *Syncer {
		files := make(map[string]trackedFile, n)
		for i := 0; i < n; i++ {
			files[fmt.Sprintf("/f%d.txt", i)] = trackedFile{Hash: "h"}
		}
		return &Syncer{state: mountState{Files: files}}
	}

	if !mk(20).snapshotDeleteUnsafe(0) {
		t.Fatalf("empty fresh listing with tracked files must be unsafe")
	}
	if !mk(20).snapshotDeleteUnsafe(3) {
		t.Fatalf("drastic shrink (3 of 20) must be unsafe")
	}
	if mk(20).snapshotDeleteUnsafe(18) {
		t.Fatalf("near-complete listing must be safe")
	}
	if mk(0).snapshotDeleteUnsafe(0) {
		t.Fatalf("no tracked state means nothing to protect; must be safe")
	}
	// Tiny working sets are exempt from the ratio check (only the
	// empty-listing rule applies).
	if mk(3).snapshotDeleteUnsafe(1) {
		t.Fatalf("small tracked set below ratio-check floor must be safe")
	}
}

// TestPullDoesNotDeleteLocalFilesWhenCloudTreeEmpty exercises the
// end-to-end circuit breaker: a cloud listing that comes back empty while
// local state tracks files must NOT delete the local mirror.
func TestPullDoesNotDeleteLocalFilesWhenCloudTreeEmpty(t *testing.T) {
	disableWS := false
	localDir := t.TempDir()

	// Seed a tracked, mirrored file locally.
	mirrored := filepath.Join(localDir, "keep.md")
	if err := os.WriteFile(mirrored, []byte("# keep me"), 0o644); err != nil {
		t.Fatalf("seed mirrored file: %v", err)
	}
	stateFile := filepath.Join(localDir, ".relayfile-mount-state.json")
	if err := writeMountState(stateFile, mountState{
		Files: map[string]trackedFile{
			"/notion/keep.md": {Revision: "rev_1", ContentType: "text/markdown", Hash: hashString("# keep me")},
		},
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	// fakeClient with NO files -> ListTree returns an empty tree.
	fc := &fakeClient{files: map[string]RemoteFile{}, eventsUnsupported: true}
	syncer, err := NewSyncer(fc, SyncerOptions{
		WorkspaceID: "ws_empty_tree",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		WebSocket:   &disableWS,
		StateFile:   stateFile,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync against empty cloud tree failed: %v", err)
	}

	if b, rErr := os.ReadFile(mirrored); rErr != nil || string(b) != "# keep me" {
		t.Fatalf("local mirrored file was deleted/corrupted by empty cloud tree: err=%v contents=%q", rErr, string(b))
	}
}

// TestPullDoesNotDeleteLocalFilesWhenCloudExportEmpty mirrors
// TestPullDoesNotDeleteLocalFilesWhenCloudTreeEmpty for the export path:
// a cloud export response that comes back empty while local state tracks
// files must NOT delete the local mirror.
func TestPullDoesNotDeleteLocalFilesWhenCloudExportEmpty(t *testing.T) {
	disableWS := false
	localDir := t.TempDir()

	// Seed a tracked, mirrored file locally.
	mirrored := filepath.Join(localDir, "keep.md")
	if err := os.WriteFile(mirrored, []byte("# keep me"), 0o644); err != nil {
		t.Fatalf("seed mirrored file: %v", err)
	}
	stateFile := filepath.Join(localDir, ".relayfile-mount-state.json")
	if err := writeMountState(stateFile, mountState{
		Files: map[string]trackedFile{
			"/notion/keep.md": {Revision: "rev_1", ContentType: "text/markdown", Hash: hashString("# keep me")},
		},
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	// fakeExportClient with NO files -> ExportFiles returns an empty
	// slice. The syncer should detect the unsafe response and preserve
	// the local mirror instead of running applyRemoteSnapshotDeletes.
	fc := &fakeExportClient{
		fakeClient: &fakeClient{files: map[string]RemoteFile{}, eventsUnsupported: true},
	}
	syncer, err := NewSyncer(fc, SyncerOptions{
		WorkspaceID: "ws_empty_export",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		WebSocket:   &disableWS,
		StateFile:   stateFile,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync against empty cloud export failed: %v", err)
	}

	// Confirm the export path was actually exercised so the test fails
	// loudly if the syncer silently falls back to ListTree.
	if fc.exportCalls == 0 {
		t.Fatalf("expected at least one ExportFiles call; got 0 (export path not exercised)")
	}

	if b, rErr := os.ReadFile(mirrored); rErr != nil || string(b) != "# keep me" {
		t.Fatalf("local mirrored file was deleted/corrupted by empty cloud export: err=%v contents=%q", rErr, string(b))
	}
}

// TestMountRootBasenameCollisionNeverClobbered is the top-level
// regression: a mount whose basename collides with a child file name must
// never sync that child onto the root nor destroy the mount directory.
func TestMountRootBasenameCollisionNeverClobbered(t *testing.T) {
	disableWS := false
	parent := t.TempDir()
	localDir := filepath.Join(parent, "relayfile-mount")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatalf("mkdir mount dir: %v", err)
	}

	// Cloud serves a file at "/relayfile-mount" (basename collision).
	remoteFiles := map[string]RemoteFile{
		"/relayfile-mount": {
			Path:        "/relayfile-mount",
			Revision:    "rev_1",
			ContentType: "text/plain",
			Content:     strings.Repeat("A", 4096),
		},
		"/safe.txt": {
			Path:        "/safe.txt",
			Revision:    "rev_2",
			ContentType: "text/plain",
			Content:     "safe",
		},
	}
	fc := &fakeClient{files: remoteFiles, eventsUnsupported: true}

	logger := &captureLogger{}
	syncer, err := NewSyncer(fc, SyncerOptions{
		WorkspaceID: "ws_collision",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
		WebSocket:   &disableWS,
		Logger:      logger,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	// Mount directory must still be a directory.
	info, statErr := os.Stat(localDir)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("mount root was clobbered: statErr=%v info=%v", statErr, info)
	}

	// The safe file should have synced normally.
	if b, rErr := os.ReadFile(filepath.Join(localDir, "safe.txt")); rErr != nil || string(b) != "safe" {
		t.Fatalf("expected safe.txt to sync: err=%v contents=%q", rErr, string(b))
	}
}

// TestOversizedFileSkippedByWritebackCap verifies that an oversized local
// file is not enqueued for writeback and is surfaced in the log.
func TestOversizedFileSkippedByWritebackCap(t *testing.T) {
	t.Setenv("RELAYFILE_MAX_WRITEBACK_BYTES", "1024")

	disableWS := false
	localDir := t.TempDir()

	big := filepath.Join(localDir, "huge.bin")
	if err := os.WriteFile(big, make([]byte, 4096), 0o644); err != nil {
		t.Fatalf("write big file: %v", err)
	}
	small := filepath.Join(localDir, "small.txt")
	if err := os.WriteFile(small, []byte("tiny"), 0o644); err != nil {
		t.Fatalf("write small file: %v", err)
	}

	fc := &fakeClient{files: map[string]RemoteFile{}, eventsUnsupported: true}
	logger := &captureLogger{}
	syncer, err := NewSyncer(fc, SyncerOptions{
		WorkspaceID: "ws_size_cap",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
		WebSocket:   &disableWS,
		Logger:      logger,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	// The small file should have been pushed; the huge one must not be.
	for _, batch := range fc.bulkWriteBatches {
		for _, f := range batch {
			if strings.Contains(f.Path, "huge.bin") {
				t.Fatalf("oversized file was enqueued for writeback: %s", f.Path)
			}
		}
	}

	sawSkipLog := false
	for _, line := range logger.lines {
		if strings.Contains(line, "huge.bin") && strings.Contains(line, "writeback cap") {
			sawSkipLog = true
		}
	}
	if !sawSkipLog {
		t.Fatalf("expected oversized-file skip to be surfaced in logs; got %v", logger.lines)
	}
}

