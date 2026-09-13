package mountsync

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadLocalSnapshotPreservesSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink("../target", link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	snapshot, err := readLocalSnapshot(link, true)
	if err != nil {
		t.Fatalf("read symlink snapshot: %v", err)
	}
	if snapshot.Type != remoteTypeSymlink || snapshot.Target != "../target" {
		t.Fatalf("snapshot metadata = type %q target %q, want symlink ../target", snapshot.Type, snapshot.Target)
	}
	if string(snapshot.RawContent) != "../target" || snapshot.WireContent != "../target" || snapshot.Encoding != "utf-8" || snapshot.Hash != hashBytes([]byte("../target")) {
		t.Fatalf("snapshot payload/hash = %q/%q, want target bytes/hash", snapshot.RawContent, snapshot.Hash)
	}
}

func TestInlineWebSocketMetadataPreservesSymlinkAndExecutableMode(t *testing.T) {
	root := t.TempDir()
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID: "ws_metadata",
		RemoteRoot:  "/project",
		LocalRoot:   root,
		StateFile:   filepath.Join(root, ".relayfile-mount-state.json"),
		WebSocket:   boolPtr(false),
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	linkTarget := "bin/run"
	if err := syncer.applyWebSocketEvent(context.Background(), websocketEvent{
		EventID:       "evt_1",
		Type:          "file.created",
		Path:          "/project/current",
		Revision:      "rev_1",
		ContentHash:   hashBytes([]byte(linkTarget)),
		ContentType:   "application/x-symlink",
		Content:       linkTarget,
		Encoding:      "utf-8",
		InlineContent: true,
		TypeMetadata: &entryTypeMetadata{
			Type: remoteTypeSymlink, Target: linkTarget, Mode: 0o777,
		},
	}); err != nil {
		t.Fatalf("apply inline symlink event: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(root, "current")); err != nil || target != linkTarget {
		t.Fatalf("expected current -> %s, target=%q err=%v", linkTarget, target, err)
	}

	runBody := "#!/bin/sh\n"
	if err := syncer.applyWebSocketEvent(context.Background(), websocketEvent{
		EventID:       "evt_2",
		Type:          "file.created",
		Path:          "/project/bin/run",
		Revision:      "rev_2",
		ContentHash:   hashBytes([]byte(runBody)),
		ContentType:   "text/x-shellscript",
		Content:       runBody,
		Encoding:      "utf-8",
		InlineContent: true,
		TypeMetadata: &entryTypeMetadata{
			Type: remoteTypeFile, Mode: 0o755,
		},
	}); err != nil {
		t.Fatalf("apply inline executable event: %v", err)
	}
	info, err := os.Stat(filepath.Join(root, "bin", "run"))
	if err != nil {
		t.Fatalf("stat executable: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("expected executable mode 0755, got %04o", info.Mode().Perm())
	}
}

func TestReadLocalSnapshotPreservesExecutableMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}

	snapshot, err := readLocalSnapshot(path, true)
	if err != nil {
		t.Fatalf("read executable snapshot: %v", err)
	}
	if snapshot.Type != remoteTypeFile || snapshot.Mode != 0o755 {
		t.Fatalf("snapshot metadata = type %q mode %o, want file 755", snapshot.Type, snapshot.Mode)
	}
}

func TestIncrementalSameHashPreservesExecutableMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tool")
	data := []byte("#!/bin/sh\n")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{WorkspaceID: "ws_incremental_mode", RemoteRoot: "/project", LocalRoot: root, StateFile: filepath.Join(root, "state.json"), WebSocket: boolPtr(false)})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	syncer.state.Files["/project/tool"] = trackedFile{Revision: "old", Type: remoteTypeFile, Mode: 0o644, Hash: hashBytes(data), LocalRelativePath: "tool"}
	skipped, err := syncer.trySkipIncrementalRead("/project/tool", FilesystemEvent{Revision: "new", ContentHash: hashBytes(data), TypeMetadata: &entryTypeMetadata{Type: remoteTypeFile, Mode: 0o755}})
	if err != nil || !skipped {
		t.Fatalf("trySkipIncrementalRead = skipped %v err %v, want true", skipped, err)
	}
	if syncer.state.Files["/project/tool"].Mode != 0o755 || syncer.state.Files["/project/tool"].Type != remoteTypeFile {
		t.Fatalf("tracked metadata = %+v, want executable file metadata", syncer.state.Files["/project/tool"])
	}
}

func TestIncrementalSameHashPreservesSymlinkMetadata(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "current")
	if err := os.Symlink("bin/run", link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{WorkspaceID: "ws_incremental_link", RemoteRoot: "/project", LocalRoot: root, StateFile: filepath.Join(root, "state.json"), WebSocket: boolPtr(false)})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	target := "bin/run"
	syncer.state.Files["/project/current"] = trackedFile{Revision: "old", Type: remoteTypeSymlink, Target: target, Mode: 0o777, Hash: hashBytes([]byte(target)), LocalRelativePath: "current"}
	skipped, err := syncer.trySkipIncrementalRead("/project/current", FilesystemEvent{Revision: "new", ContentHash: hashBytes([]byte(target)), TypeMetadata: &entryTypeMetadata{Type: remoteTypeSymlink, Target: target, Mode: 0o777}})
	if err != nil || !skipped {
		t.Fatalf("trySkipIncrementalRead = skipped %v err %v, want true", skipped, err)
	}
	tracked := syncer.state.Files["/project/current"]
	if tracked.Type != remoteTypeSymlink || tracked.Target != target || tracked.Mode != 0o777 {
		t.Fatalf("tracked metadata = %+v, want symlink metadata", tracked)
	}
}

func TestSameHashDirtyReconciliationUpdatesTypeMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tool")
	data := []byte("same bytes\n")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatalf("write file: %v", err)
	}
	client := &fakeClient{files: map[string]RemoteFile{
		"/project/tool": {
			Path: "/project/tool", Revision: "remote-rev", Type: remoteTypeFile,
			Mode: 0o755, ContentType: "text/plain", Content: string(data),
		},
	}}
	syncer, err := NewSyncer(client, SyncerOptions{WorkspaceID: "ws_same_hash_metadata", RemoteRoot: "/project", LocalRoot: root, StateFile: filepath.Join(root, "state.json"), WebSocket: boolPtr(false)})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	syncer.state.Files["/project/tool"] = trackedFile{Revision: "old-rev", Type: remoteTypeFile, Mode: 0o644, Hash: hashBytes(data), Dirty: true}
	snapshot, err := readLocalSnapshot(path, true)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	pending, err := syncer.preparePendingBulkWrite(context.Background(), "/project/tool", path, snapshot, syncer.state.Files["/project/tool"], true)
	if err != nil || pending != nil {
		t.Fatalf("prepare same-hash reconciliation = pending %v err %v, want no pending write", pending, err)
	}
	tracked := syncer.state.Files["/project/tool"]
	if tracked.Revision != "remote-rev" || tracked.Mode != 0o755 || tracked.Type != remoteTypeFile || tracked.Dirty {
		t.Fatalf("reconciled metadata = %+v", tracked)
	}
}

func TestSecureAtomicReplacementRejectsAncestorSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "redirect")); err != nil {
		t.Fatalf("create redirect: %v", err)
	}
	target := filepath.Join(root, "redirect", "escaped.txt")
	if err := writeFileAtomicSecure(root, target, []byte("must stay rooted"), 0o644); err == nil {
		t.Fatal("secure replacement followed ancestor symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside target was created: %v", err)
	}
}

func TestValidateSymlinkTargetStaysWithinMount(t *testing.T) {
	root := t.TempDir()
	localPath := filepath.Join(root, "nested", "link")
	if err := validateSymlinkTarget(root, localPath, "../target"); err != nil {
		t.Fatalf("valid relative target rejected: %v", err)
	}
	if err := validateSymlinkTarget(root, localPath, "../../outside"); err == nil {
		t.Fatal("escaping target accepted")
	}
	if err := validateSymlinkTarget(root, localPath, "/etc/passwd"); err == nil {
		t.Fatal("absolute target accepted")
	}
	for _, target := range []string{"C:/outside", "C:\\outside", "c:/outside", "C:outside", `\\\\server\\share`} {
		if err := validateSymlinkTarget(root, localPath, target); err == nil {
			t.Errorf("drive/UNC target %q accepted", target)
		}
	}
}

func TestSymlinkTargetRejectsExistingSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "redirect")); err != nil {
		t.Fatalf("create redirect: %v", err)
	}
	localPath := filepath.Join(root, "link")
	if err := validateSymlinkTarget(root, localPath, "redirect/secret"); err == nil {
		t.Fatal("symlink target through existing symlinked parent was accepted")
	}
	if _, err := safeLocalPath(root, filepath.Join("redirect", "new.txt")); err == nil {
		t.Fatal("materialization path through symlinked parent was accepted")
	}
}

func TestReadLocalSnapshotRejectsOverLimitFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large")
	data := bytes.Repeat([]byte{'x'}, int(defaultMaxWritebackBytes)+1)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write large file: %v", err)
	}
	if _, err := readLocalSnapshot(path, true); !errors.Is(err, errLocalSnapshotTooLarge) {
		t.Fatalf("read large snapshot error = %v, want errLocalSnapshotTooLarge", err)
	}
}

func TestLargeEscapedTextUsesBase64WireEncoding(t *testing.T) {
	data := bytes.Repeat([]byte{'<'}, 16<<20)
	snapshot := newLocalSnapshotWithMode("large.json", data, 0o644)
	if snapshot.Encoding != "base64" {
		t.Fatalf("large escaped text encoding = %q, want base64", snapshot.Encoding)
	}
	if bulkWriteRequestSize([]BulkWriteFile{{Path: "/large.json", ContentType: snapshot.ContentType, Content: snapshot.WireContent, Encoding: snapshot.Encoding}}) > maxWritebackBatchBytes() {
		t.Fatal("base64 snapshot still exceeds request wire budget")
	}
}

func TestTarSeedRejectsFileOverWritebackLimitBeforeRead(t *testing.T) {
	root := t.TempDir()
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{WorkspaceID: "ws_tar_limit", RemoteRoot: "/project", LocalRoot: root, StateFile: filepath.Join(root, "state.json"), WebSocket: boolPtr(false)})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "large", Size: defaultMaxWritebackBytes + 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	_, err = syncer.applyGithubWorkingTreeTarSeed(GithubWorkingTreeTar{Body: io.NopCloser(bytes.NewReader(buf.Bytes()))}, map[string]githubTreeFile{"large": {RemotePath: "/project/large", ContentHash: strings.Repeat("0", 64), Type: remoteTypeFile}}, nil, bootstrapProgress{})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized tar error = %v, want bounded rejection", err)
	}
}

func TestCompleteTarSeedRejectsUnsupportedEntryType(t *testing.T) {
	root := t.TempDir()
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{WorkspaceID: "ws_tar_type", RemoteRoot: "/project", LocalRoot: root, StateFile: filepath.Join(root, "state.json"), WebSocket: boolPtr(false)})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "device", Typeflag: tar.TypeChar}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	_, err = syncer.applyGithubWorkingTreeTarSeedStrict(GithubWorkingTreeTar{Body: io.NopCloser(bytes.NewReader(buf.Bytes()))}, nil, nil, bootstrapProgress{}, true)
	if err == nil || !strings.Contains(err.Error(), "unsupported entry type") {
		t.Fatalf("unsupported complete-v1 tar error = %v", err)
	}
}

func TestHTTPClientRequiresSymlinkCapability(t *testing.T) {
	tests := []struct {
		name     string
		response string
		wantErr  bool
	}{
		{name: "legacy", response: `{"status":"ok"}`, wantErr: true},
		{name: "advertised", response: `{"status":"ok","features":["symlink-v1"]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/health" {
					t.Fatalf("health path = %q", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer server.Close()

			err := NewHTTPClient(server.URL, "token", server.Client()).EnsureSymlinkSupport(context.Background())
			if tc.wantErr && !errors.Is(err, ErrSymlinkUnsupported) {
				t.Fatalf("error = %v, want ErrSymlinkUnsupported", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected capability error: %v", err)
			}
		})
	}
}

func TestOutboxBulkWritePreservesSymlinkAndModeMetadata(t *testing.T) {
	files := outboxRecordsAsBulkFiles([]outboxRecord{{
		RemotePath: "/bin/tool",
		Type:       remoteTypeSymlink,
		Target:     "../shared/tool",
		Mode:       0o755,
		Content:    "../shared/tool",
	}})
	if len(files) != 1 {
		t.Fatalf("bulk file count = %d, want 1", len(files))
	}
	if files[0].Type != remoteTypeSymlink || files[0].Target != "../shared/tool" || files[0].Mode != 0o755 {
		t.Fatalf("bulk metadata = %+v, want symlink target and mode", files[0])
	}
}
