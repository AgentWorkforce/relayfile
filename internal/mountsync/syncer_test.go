package mountsync

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/httpapi"
	"github.com/agentworkforce/relayfile/internal/relayfile"
	"github.com/fsnotify/fsnotify"
)

func TestSyncOncePullsRemoteAndPushesLocalEdits(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     "# A",
			},
		},
		revisionCounter: 1,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_1",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}

	localFile := filepath.Join(localDir, "Docs", "A.md")
	data, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatalf("read local mirrored file failed: %v", err)
	}
	if string(data) != "# A" {
		t.Fatalf("expected pulled content '# A', got %q", string(data))
	}

	if err := os.WriteFile(localFile, []byte("# A edited"), 0o644); err != nil {
		t.Fatalf("write local edit failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync after edit failed: %v", err)
	}

	remote := client.files["/notion/Docs/A.md"]
	if remote.Content != "# A edited" {
		t.Fatalf("expected remote content to update, got %q", remote.Content)
	}
	if remote.Revision == "rev_1" {
		t.Fatalf("expected remote revision to advance")
	}
}

func TestSyncOnceCreatesAndDeletesRemoteFiles(t *testing.T) {
	client := &fakeClient{
		files:           map[string]RemoteFile{},
		revisionCounter: 0,
	}
	localDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localDir, "Docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs failed: %v", err)
	}
	localFile := filepath.Join(localDir, "Docs", "New.md")
	if err := os.WriteFile(localFile, []byte("# New"), 0o644); err != nil {
		t.Fatalf("seed local file failed: %v", err)
	}

	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_2",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync create failed: %v", err)
	}
	if _, ok := client.files["/notion/Docs/New.md"]; !ok {
		t.Fatalf("expected remote file to be created")
	}

	if err := os.Remove(localFile); err != nil {
		t.Fatalf("remove local file failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync delete failed: %v", err)
	}
	if _, ok := client.files["/notion/Docs/New.md"]; ok {
		t.Fatalf("expected remote file to be deleted")
	}
}

func TestSyncOncePreservesLocalBufferOnConflict(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     "# A",
			},
		},
		revisionCounter: 1,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_conflict",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}

	localFile := filepath.Join(localDir, "Docs", "A.md")
	client.files["/notion/Docs/A.md"] = RemoteFile{
		Path:        "/notion/Docs/A.md",
		Revision:    "rev_remote",
		ContentType: "text/markdown",
		Content:     "# remote",
	}
	if err := os.WriteFile(localFile, []byte("# local"), 0o644); err != nil {
		t.Fatalf("write local edit failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync conflict cycle failed: %v", err)
	}
	localAfterConflict, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatalf("read local file after conflict failed: %v", err)
	}
	if string(localAfterConflict) != "# local" {
		t.Fatalf("expected local buffer to be preserved after conflict, got %q", string(localAfterConflict))
	}
	if client.files["/notion/Docs/A.md"].Content != "# remote" {
		t.Fatalf("expected remote content to remain remote during conflict cycle")
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync retry cycle failed: %v", err)
	}
	if client.files["/notion/Docs/A.md"].Content != "# local" {
		t.Fatalf("expected remote content to converge to local buffer after retry, got %q", client.files["/notion/Docs/A.md"].Content)
	}
}

func TestSyncOnceClearsDirtyStateWhenRemoteConverges(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     "# A",
			},
		},
		revisionCounter: 1,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_conflict_recovery",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}

	localFile := filepath.Join(localDir, "Docs", "A.md")
	client.files["/notion/Docs/A.md"] = RemoteFile{
		Path:        "/notion/Docs/A.md",
		Revision:    "rev_remote",
		ContentType: "text/markdown",
		Content:     "# remote",
	}
	if err := os.WriteFile(localFile, []byte("# local"), 0o644); err != nil {
		t.Fatalf("write local edit failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync conflict cycle failed: %v", err)
	}

	client.files["/notion/Docs/A.md"] = RemoteFile{
		Path:        "/notion/Docs/A.md",
		Revision:    "rev_remote_2",
		ContentType: "text/markdown",
		Content:     "# local",
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync convergence cycle failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync steady-state cycle failed: %v", err)
	}
	if client.files["/notion/Docs/A.md"].Revision != "rev_remote_2" {
		t.Fatalf("expected no additional writeback after remote convergence, got revision %q", client.files["/notion/Docs/A.md"].Revision)
	}
	if client.files["/notion/Docs/A.md"].Content != "# local" {
		t.Fatalf("expected converged remote content to remain '# local', got %q", client.files["/notion/Docs/A.md"].Content)
	}
}

func TestSyncOnceUsesEventCursorForIncrementalPull(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     "# A",
			},
		},
		events: []FilesystemEvent{
			{
				EventID:  "evt_1",
				Type:     "file.created",
				Path:     "/notion/Docs/A.md",
				Revision: "rev_1",
			},
		},
		revisionCounter: 1,
		eventCounter:    1,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_events",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}
	if client.listTreeCalls != 1 {
		t.Fatalf("expected one full tree pull on initial sync, got %d", client.listTreeCalls)
	}

	client.files["/notion/Docs/A.md"] = RemoteFile{
		Path:        "/notion/Docs/A.md",
		Revision:    "rev_2",
		ContentType: "text/markdown",
		Content:     "# A v2",
	}
	client.appendEvent("file.updated", "/notion/Docs/A.md", "rev_2")
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("incremental sync failed: %v", err)
	}
	if client.listTreeCalls != 1 {
		t.Fatalf("expected second sync to avoid full tree pull, got %d", client.listTreeCalls)
	}
	localFile := filepath.Join(localDir, "Docs", "A.md")
	data, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatalf("read local file failed: %v", err)
	}
	if string(data) != "# A v2" {
		t.Fatalf("expected incremental event update to mirror new content, got %q", string(data))
	}
}

func TestSyncOnceFallsBackToFullPullWhenEventsUnavailable(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     "# A",
			},
		},
		revisionCounter:   1,
		eventsUnsupported: true,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_events_fallback",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}
	if client.listTreeCalls != 1 {
		t.Fatalf("expected one full tree pull on initial sync, got %d", client.listTreeCalls)
	}

	client.files["/notion/Docs/A.md"] = RemoteFile{
		Path:        "/notion/Docs/A.md",
		Revision:    "rev_2",
		ContentType: "text/markdown",
		Content:     "# A fallback",
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("fallback sync failed: %v", err)
	}
	if client.listTreeCalls != 2 {
		t.Fatalf("expected second sync to use full tree fallback, got %d list-tree calls", client.listTreeCalls)
	}
	localFile := filepath.Join(localDir, "Docs", "A.md")
	data, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatalf("read local file failed: %v", err)
	}
	if string(data) != "# A fallback" {
		t.Fatalf("expected fallback pull to refresh local content, got %q", string(data))
	}
}

func TestBulkSeedThenSync(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)

	api := httptest.NewServer(httpapi.NewServer(store))
	defer api.Close()

	workspaceID := "ws_mount_bulk_seed"
	token := mustMountsyncTestJWT(t, "dev-secret", workspaceID, "MountSync", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	body, err := json.Marshal(map[string]any{
		"files": []map[string]any{
			{
				"path":        "/notion/Docs/A.md",
				"contentType": "text/markdown",
				"content":     "# A",
			},
			{
				"path":        "/notion/Docs/B.md",
				"contentType": "text/markdown",
				"content":     "# B",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal bulk seed body failed: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, api.URL+"/v1/workspaces/"+workspaceID+"/fs/bulk", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new bulk request failed: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Correlation-Id", "corr_mount_bulk_seed")
	req.Header.Set("Content-Type", "application/json")

	resp, err := api.Client().Do(req)
	if err != nil {
		t.Fatalf("bulk seed request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 202 from bulk seed, got %d (%s)", resp.StatusCode, string(payload))
	}

	localDir := t.TempDir()
	client := NewHTTPClient(api.URL, token, api.Client())
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync once failed: %v", err)
	}

	assertLocalFileContent(t, filepath.Join(localDir, "Docs", "A.md"), "# A")
	assertLocalFileContent(t, filepath.Join(localDir, "Docs", "B.md"), "# B")
}

func TestSyncOnceUsesWebSocketForRealtimeUpdatesAndSkipsPollingWhileConnected(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)

	workspaceID := "ws_mount_websocket"
	token := mustMountsyncTestJWT(t, "dev-secret", workspaceID, "MountSync", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	var treeCalls atomic.Int32
	var eventCalls atomic.Int32
	handler := httpapi.NewServer(store)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/fs/tree"):
			treeCalls.Add(1)
		case strings.HasSuffix(r.URL.Path, "/fs/events"):
			eventCalls.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer api.Close()

	writeMountsyncRemoteFile(t, api.Client(), api.URL, token, workspaceID, "/notion/Docs/A.md", "0", "# A")

	localDir := t.TempDir()
	client := NewHTTPClient(api.URL, token, api.Client())
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := syncer.SyncOnce(ctx); err != nil {
		t.Fatalf("initial websocket sync failed: %v", err)
	}
	assertLocalFileContent(t, filepath.Join(localDir, "Docs", "A.md"), "# A")

	treeBefore := treeCalls.Load()
	eventsBefore := eventCalls.Load()

	remoteFile, err := client.ReadFile(context.Background(), workspaceID, "/notion/Docs/A.md")
	if err != nil {
		t.Fatalf("read seeded remote file failed: %v", err)
	}
	writeMountsyncRemoteFile(t, api.Client(), api.URL, token, workspaceID, "/notion/Docs/A.md", remoteFile.Revision, "# A websocket")
	waitForLocalContent(t, filepath.Join(localDir, "Docs", "A.md"), "# A websocket")

	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if err := syncer.SyncOnce(ctx2); err != nil {
		t.Fatalf("follow-up sync failed: %v", err)
	}
	if treeCalls.Load() != treeBefore {
		t.Fatalf("expected websocket-connected sync to skip tree polling, got %d -> %d calls", treeBefore, treeCalls.Load())
	}
	if eventCalls.Load() != eventsBefore {
		t.Fatalf("expected websocket-connected sync to skip event polling, got %d -> %d calls", eventsBefore, eventCalls.Load())
	}
}

func TestPullSkipsDeniedFiles(t *testing.T) {
	disableWebSocket := false
	token := mustMountsyncTestJWT(t, "dev-secret", "ws_mount_denied_read", "MountSync", []string{"fs:read"}, time.Now().Add(time.Hour))
	server := newMockMountsyncServer(t, map[string]RemoteFile{
		"/notion/secrets/key.txt": {
			Path:        "/notion/secrets/key.txt",
			Revision:    "rev_1",
			ContentType: "text/markdown",
			Content:     "# secret",
		},
	}, map[string]struct{}{"/notion/secrets/key.txt": {}}, nil)
	defer server.Close()

	localDir := t.TempDir()
	client := NewHTTPClient(server.URL, token, server.Client())
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_denied_read",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		WebSocket:   &disableWebSocket,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync for denied file failed: %v", err)
	}

	localPath := filepath.Join(localDir, "secrets", "key.txt")
	if _, err := os.Stat(localPath); err == nil {
		t.Fatalf("expected denied remote file not to be created locally, got %s", localPath)
	}

	stateFile := filepath.Join(localDir, ".relayfile-mount-state.json")
	assertStateMarksPathDenied(t, stateFile, "/notion/secrets/key.txt")
}

func TestPullDeletesLocalDeniedFile(t *testing.T) {
	disableWebSocket := false
	token := mustMountsyncTestJWT(t, "dev-secret", "ws_mount_denied_delete", "MountSync", []string{"fs:read"}, time.Now().Add(time.Hour))
	localDir := t.TempDir()
	localPath := filepath.Join(localDir, "secrets", "key.txt")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir local dir failed: %v", err)
	}
	if err := os.WriteFile(localPath, []byte("# local secret"), 0o644); err != nil {
		t.Fatalf("write local denied file failed: %v", err)
	}

	initialState := mountState{
		Files: map[string]trackedFile{
			"/notion/secrets/key.txt": {
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Hash:        hashString("# local secret"),
			},
		},
	}
	stateFile := filepath.Join(localDir, ".relayfile-mount-state.json")
	if err := writeMountState(stateFile, initialState); err != nil {
		t.Fatalf("seed state file failed: %v", err)
	}

	server := newMockMountsyncServer(t, map[string]RemoteFile{
		"/notion/secrets/key.txt": {
			Path:        "/notion/secrets/key.txt",
			Revision:    "rev_1",
			ContentType: "text/markdown",
			Content:     "# secret",
		},
	}, map[string]struct{}{"/notion/secrets/key.txt": {}}, nil)
	defer server.Close()

	client := NewHTTPClient(server.URL, token, server.Client())
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_denied_delete",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		WebSocket:   &disableWebSocket,
		StateFile:   stateFile,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync denied file failed: %v", err)
	}
	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Fatalf("expected denied local file to be deleted, got err=%v", err)
	}
	assertStateMarksPathDenied(t, stateFile, "/notion/secrets/key.txt")
}

func TestWriteRejectionRevertsFile(t *testing.T) {
	disableWebSocket := false
	token := mustMountsyncTestJWT(t, "dev-secret", "ws_mount_write_reject", "MountSync", []string{"fs:read"}, time.Now().Add(time.Hour))
	localDir := t.TempDir()
	server := newMockMountsyncServer(t, map[string]RemoteFile{
		"/notion/readonly/notes.md": {
			Path:        "/notion/readonly/notes.md",
			Revision:    "rev_1",
			ContentType: "text/markdown",
			Content:     "# readonly",
		},
	}, nil, map[string]struct{}{"/notion/readonly/notes.md": {}})
	defer server.Close()

	client := NewHTTPClient(server.URL, token, server.Client())
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_write_reject",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		WebSocket:   &disableWebSocket,
		Scopes:      []string{"fs:read"},
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial pull failed: %v", err)
	}

	localPath := filepath.Join(localDir, "readonly", "notes.md")
	// Simulate agent chmod bypass
	if err := os.Chmod(localPath, 0o644); err != nil {
		t.Fatalf("chmod bypass failed: %v", err)
	}
	if err := os.WriteFile(localPath, []byte("# local change"), 0o644); err != nil {
		t.Fatalf("modify local file failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("write-rejected sync failed: %v", err)
	}
	assertLocalFileContent(t, localPath, "# readonly")
	if mode, err := os.Stat(localPath); err != nil {
		t.Fatalf("stat local file failed: %v", err)
	} else if mode.Mode().Perm() != 0o444 {
		t.Fatalf("expected denied-write file mode 0444, got %o", mode.Mode().Perm())
	}
}

func TestReadonlyFilesGetChmod444(t *testing.T) {
	disableWebSocket := false
	token := mustMountsyncTestJWT(t, "dev-secret", "ws_mount_readonly_mode", "MountSync", []string{"fs:read"}, time.Now().Add(time.Hour))
	localDir := t.TempDir()
	server := newMockMountsyncServer(t, map[string]RemoteFile{
		"/notion/docs/notes.md": {
			Path:        "/notion/docs/notes.md",
			Revision:    "rev_1",
			ContentType: "text/markdown",
			Content:     "# notes",
		},
	}, nil, nil)
	defer server.Close()

	client := NewHTTPClient(server.URL, token, server.Client())
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_readonly_mode",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		WebSocket:   &disableWebSocket,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial pull failed: %v", err)
	}

	localPath := filepath.Join(localDir, "docs", "notes.md")
	mode, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("stat local file failed: %v", err)
	}
	if mode.Mode().Perm() != 0o444 {
		t.Fatalf("expected read-only mode 0444, got %o", mode.Mode().Perm())
	}
}

func TestWritableFilesGetChmod644(t *testing.T) {
	disableWebSocket := false
	token := mustMountsyncTestJWT(t, "dev-secret", "ws_mount_rw_mode", "MountSync", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))
	localDir := t.TempDir()
	server := newMockMountsyncServer(t, map[string]RemoteFile{
		"/notion/docs/notes.md": {
			Path:        "/notion/docs/notes.md",
			Revision:    "rev_1",
			ContentType: "text/markdown",
			Content:     "# notes",
		},
	}, nil, nil)
	defer server.Close()

	client := NewHTTPClient(server.URL, token, server.Client())
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_rw_mode",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		WebSocket:   &disableWebSocket,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial pull failed: %v", err)
	}

	localPath := filepath.Join(localDir, "docs", "notes.md")
	mode, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("stat local file failed: %v", err)
	}
	if mode.Mode().Perm() != 0o644 {
		t.Fatalf("expected writable mode 0644, got %o", mode.Mode().Perm())
	}
}

func TestCanWritePathWithShortScopes(t *testing.T) {
	writeToken := mustMountsyncTestJWT(t, "dev-secret", "ws_scope_short", "MountSync", []string{"fs:write"}, time.Now().Add(time.Hour))
	readToken := mustMountsyncTestJWT(t, "dev-secret", "ws_scope_short", "MountSync", []string{"fs:read"}, time.Now().Add(time.Hour))

	writeScopes := parseMountsyncTokenScopes(t, writeToken)
	readScopes := parseMountsyncTokenScopes(t, readToken)

	if !canWritePath(writeScopes, "/notion/docs/readme.md") {
		t.Fatalf("expected fs:write token to permit write for all paths")
	}
	if canWritePath(readScopes, "/notion/docs/readme.md") {
		t.Fatalf("expected fs:read-only token to deny writes")
	}
}

func TestCanWritePathWithRelayauthScopes(t *testing.T) {
	token := mustMountsyncTestJWT(t, "dev-secret", "ws_scope_relayer", "MountSync", []string{"relayfile:fs:write:/src/*"}, time.Now().Add(time.Hour))
	scopes := parseMountsyncTokenScopes(t, token)

	if !canWritePath(scopes, "/src/app.ts") {
		t.Fatalf("expected scoped write token to permit path under /src")
	}
	if canWritePath(scopes, "/docs/readme.md") {
		t.Fatalf("expected scoped write token to deny path outside /src")
	}
}

func TestCanReadPathWithPerFileScopes(t *testing.T) {
	scopes := map[string]struct{}{
		"relayfile:fs:read:/src/app.ts": {},
		"relayfile:fs:read:/README.md": {},
	}

	if !canReadPath(scopes, "/src/app.ts") {
		t.Fatalf("expected /src/app.ts to be readable")
	}
	if canReadPath(scopes, "/.env") {
		t.Fatalf("expected /.env to be unreadable")
	}
	if canReadPath(scopes, "/secrets/key.txt") {
		t.Fatalf("expected /secrets/key.txt to be unreadable")
	}
}

func TestCanReadPathFromTokenPerFileScopes(t *testing.T) {
	token := mustMountsyncTestJWT(t, "dev-secret", "ws_scope_read_from_token", "MountSync", []string{
		"relayfile:fs:read:/src/app.ts",
		"relayfile:fs:read:/notes/*",
	}, time.Now().Add(time.Hour))

	scopes := parseMountsyncTokenScopes(t, token)

	if !canReadPath(scopes, "/src/app.ts") {
		t.Fatalf("expected /src/app.ts to be readable from scoped token")
	}
	if !canReadPath(scopes, "/notes/today.md") {
		t.Fatalf("expected /notes/today.md to be readable from scoped token")
	}
	if canReadPath(scopes, "/docs/readme.md") {
		t.Fatalf("expected /docs/readme.md to be unreadable from scoped token")
	}
}

func TestCanReadPathWithWildcard(t *testing.T) {
	scopes := map[string]struct{}{
		"relayfile:fs:read:/src/*": {},
	}

	if !canReadPath(scopes, "/src/api/handler.ts") {
		t.Fatalf("expected /src/api/handler.ts to be readable")
	}
	if canReadPath(scopes, "/docs/readme.md") {
		t.Fatalf("expected /docs/readme.md to be unreadable")
	}
}

func TestCanReadPathEmpty(t *testing.T) {
	scopes := map[string]struct{}{}

	if !canReadPath(scopes, "/anything") {
		t.Fatalf("expected empty scope set to allow read")
	}
}

func TestPullSkipsUnreadableFiles(t *testing.T) {
	disableWebSocket := false
	token := mustMountsyncTestJWT(t, "dev-secret", "ws_scope_read_filter", "MountSync", []string{"relayfile:fs:read:/src/app.ts"}, time.Now().Add(time.Hour))
	localDir := t.TempDir()
	server := newMockMountsyncServer(t, map[string]RemoteFile{
		"/notion/src/app.ts": {
			Path:        "/notion/src/app.ts",
			Revision:    "rev_1",
			ContentType: "text/markdown",
			Content:     "# app",
		},
		"/notion/.env": {
			Path:        "/notion/.env",
			Revision:    "rev_1",
			ContentType: "text/plain",
			Content:     "SECRET=abc",
		},
	}, map[string]struct{}{
		"/notion/.env": {},
	}, nil)
	defer server.Close()

	client := NewHTTPClient(server.URL, token, server.Client())
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_scope_read_filter",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		WebSocket:   &disableWebSocket,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial pull failed: %v", err)
	}

	stateFile := filepath.Join(localDir, ".relayfile-mount-state.json")

	if _, err := os.Stat(filepath.Join(localDir, "src", "app.ts")); err != nil {
		t.Fatalf("expected readable file to exist, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(localDir, ".env")); !os.IsNotExist(err) {
		t.Fatalf("expected unreadable file to be absent, got err=%v", err)
	}
	assertStateMarksPathDenied(t, stateFile, "/notion/.env")
}

func TestReadonlyRevertAfterChmodBypass(t *testing.T) {
	disableWebSocket := false
	token := mustMountsyncTestJWT(t, "dev-secret", "ws_readonly_chmod_bypass", "MountSync", []string{"relayfile:fs:read:/readonly/notes.md"}, time.Now().Add(time.Hour))
	localDir := t.TempDir()
	server := newMockMountsyncServer(t, map[string]RemoteFile{
		"/notion/readonly/notes.md": {
			Path:        "/notion/readonly/notes.md",
			Revision:    "rev_1",
			ContentType: "text/markdown",
			Content:     "# readonly",
		},
	}, nil, map[string]struct{}{"/notion/readonly/notes.md": {}})
	defer server.Close()

	client := NewHTTPClient(server.URL, token, server.Client())
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_readonly_chmod_bypass",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		WebSocket:   &disableWebSocket,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial pull failed: %v", err)
	}

	localPath := filepath.Join(localDir, "readonly", "notes.md")
	if err := os.Chmod(localPath, 0o644); err != nil {
		t.Fatalf("chmod writable before modify failed: %v", err)
	}
	if err := os.WriteFile(localPath, []byte("# changed"), 0o644); err != nil {
		t.Fatalf("modify local file failed: %v", err)
	}

	if err := syncer.HandleLocalChange(context.Background(), filepath.ToSlash(filepath.Join("readonly", "notes.md")), fsnotify.Write); err != nil {
		t.Fatalf("handle local write failed: %v", err)
	}

	assertLocalFileContent(t, localPath, "# readonly")
	mode, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("stat local file failed: %v", err)
	}
	if mode.Mode().Perm() != 0o444 {
		t.Fatalf("expected reverted readonly file mode 0444, got %o", mode.Mode().Perm())
	}
}

func TestReadonlyRevertOnModify(t *testing.T) {
	disableWebSocket := false
	token := mustMountsyncTestJWT(t, "dev-secret", "ws_readonly_modify", "MountSync", []string{"relayfile:fs:read:/readonly/notes.md"}, time.Now().Add(time.Hour))
	localDir := t.TempDir()
	server := newMockMountsyncServer(t, map[string]RemoteFile{
		"/notion/readonly/notes.md": {
			Path:        "/notion/readonly/notes.md",
			Revision:    "rev_1",
			ContentType: "text/markdown",
			Content:     "# readonly",
		},
	}, nil, nil)
	defer server.Close()

	client := NewHTTPClient(server.URL, token, server.Client())
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_readonly_modify",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		WebSocket:   &disableWebSocket,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial pull failed: %v", err)
	}

	localPath := filepath.Join(localDir, "readonly", "notes.md")
	if err := os.Chmod(localPath, 0o644); err != nil {
		t.Fatalf("chmod writable before modify failed: %v", err)
	}
	if err := os.WriteFile(localPath, []byte("# changed"), 0o644); err != nil {
		t.Fatalf("modify local file failed: %v", err)
	}

	if err := syncer.HandleLocalChange(context.Background(), filepath.ToSlash(filepath.Join("readonly", "notes.md")), fsnotify.Write); err != nil {
		t.Fatalf("handle local modify failed: %v", err)
	}

	assertLocalFileContent(t, localPath, "# readonly")
	mode, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("stat local file failed: %v", err)
	}
	if mode.Mode().Perm() != 0o444 {
		t.Fatalf("expected reverted readonly file mode 0444, got %o", mode.Mode().Perm())
	}

	denialLogPath := filepath.Join(localDir, ".relay", "permissions-denied.log")
	logData, err := os.ReadFile(denialLogPath)
	if err != nil {
		t.Fatalf("read denial log failed: %v", err)
	}
	if !strings.Contains(string(logData), "WRITE_DENIED") {
		t.Fatalf("expected WRITE_DENIED in denial log, got: %q", string(logData))
	}
}

func TestReadonlyRevertOnDelete(t *testing.T) {
	disableWebSocket := false
	token := mustMountsyncTestJWT(t, "dev-secret", "ws_readonly_delete", "MountSync", []string{"relayfile:fs:read:/readonly/notes.md"}, time.Now().Add(time.Hour))
	localDir := t.TempDir()
	server := newMockMountsyncServer(t, map[string]RemoteFile{
		"/notion/readonly/notes.md": {
			Path:        "/notion/readonly/notes.md",
			Revision:    "rev_1",
			ContentType: "text/markdown",
			Content:     "# readonly",
		},
	}, nil, nil)
	defer server.Close()

	client := NewHTTPClient(server.URL, token, server.Client())
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_readonly_delete",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		WebSocket:   &disableWebSocket,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial pull failed: %v", err)
	}

	localPath := filepath.Join(localDir, "readonly", "notes.md")
	if err := os.Remove(localPath); err != nil {
		t.Fatalf("delete local file failed: %v", err)
	}
	if err := syncer.HandleLocalChange(context.Background(), filepath.ToSlash(filepath.Join("readonly", "notes.md")), fsnotify.Remove); err != nil {
		t.Fatalf("handle local delete failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("post-delete sync failed: %v", err)
	}

	assertLocalFileContent(t, localPath, "# readonly")
}

func TestFsnotifyTriggersRevert(t *testing.T) {
	disableWebSocket := false
	token := mustMountsyncTestJWT(t, "dev-secret", "ws_readonly_fsnotify", "MountSync", []string{"relayfile:fs:read:/readonly/notes.md"}, time.Now().Add(time.Hour))
	localDir := t.TempDir()
	server := newMockMountsyncServer(t, map[string]RemoteFile{
		"/notion/readonly/notes.md": {
			Path:        "/notion/readonly/notes.md",
			Revision:    "rev_1",
			ContentType: "text/markdown",
			Content:     "# readonly",
		},
	}, nil, nil)
	defer server.Close()

	client := NewHTTPClient(server.URL, token, server.Client())
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_readonly_fsnotify",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		WebSocket:   &disableWebSocket,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial pull failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	fwErr := make(chan error, 4)
	watcher, err := NewFileWatcher(localDir, func(relativePath string, op fsnotify.Op) {
		if err := syncer.HandleLocalChange(context.Background(), relativePath, op); err != nil {
			select {
			case fwErr <- err:
			default:
			}
		}
	})
	if err != nil {
		t.Fatalf("new watcher failed: %v", err)
	}
	if err := watcher.Start(ctx); err != nil {
		t.Fatalf("start watcher failed: %v", err)
	}
	t.Cleanup(func() {
		_ = watcher.Close()
	})

	localPath := filepath.Join(localDir, "readonly", "notes.md")
	if err := os.Chmod(localPath, 0o644); err != nil {
		t.Fatalf("chmod writable before modify failed: %v", err)
	}
	if err := os.WriteFile(localPath, []byte("# tampered"), 0o644); err != nil {
		t.Fatalf("modify local file failed: %v", err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case <-fwErr:
			t.Fatal("watcher callback returned error during revert")
		default:
		}
		data, readErr := os.ReadFile(localPath)
		if readErr != nil {
			time.Sleep(25 * time.Millisecond)
			continue
		}
		if string(data) == "# readonly" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	assertLocalFileContent(t, localPath, "# readonly")
	mode, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("stat local file failed: %v", err)
	}
	if mode.Mode().Perm() != 0o444 {
		t.Fatalf("expected reverted readonly file mode 0444, got %o", mode.Mode().Perm())
	}
}

func TestRemoteToLocalAndLocalToRemotePath(t *testing.T) {
	localRoot := filepath.Join("tmp", "mirror")
	localPath, err := remoteToLocalPath(localRoot, "/notion", "/notion/Folder/File.md")
	if err != nil {
		t.Fatalf("remoteToLocalPath failed: %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(localPath), "tmp/mirror/Folder/File.md") {
		t.Fatalf("unexpected local path mapping: %s", filepath.ToSlash(localPath))
	}

	remotePath, err := localToRemotePath(localRoot, "/notion", filepath.Join(localRoot, "Folder", "File.md"))
	if err != nil {
		t.Fatalf("localToRemotePath failed: %v", err)
	}
	if remotePath != "/notion/Folder/File.md" {
		t.Fatalf("unexpected remote path mapping: %s", remotePath)
	}
}

func TestInferProviderFromRoot(t *testing.T) {
	if got := inferProviderFromRoot("/notion"); got != "notion" {
		t.Fatalf("expected notion, got %q", got)
	}
	if got := inferProviderFromRoot("/custom/root"); got != "custom" {
		t.Fatalf("expected custom, got %q", got)
	}
	if got := inferProviderFromRoot("/"); got != "" {
		t.Fatalf("expected empty provider for root mount, got %q", got)
	}
}

func TestWriteFileAtomicReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "A.md")
	if err := os.WriteFile(path, []byte("# old"), 0o644); err != nil {
		t.Fatalf("seed file failed: %v", err)
	}
	if err := writeFileAtomic(path, []byte("# new"), 0o644); err != nil {
		t.Fatalf("atomic write failed: %v", err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read updated file failed: %v", err)
	}
	if string(updated) != "# new" {
		t.Fatalf("expected updated content, got %q", string(updated))
	}
}

func TestWriteFileAtomicFailureLeavesOriginalContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "A.md")
	if err := os.WriteFile(path, []byte("# old"), 0o644); err != nil {
		t.Fatalf("seed file failed: %v", err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Skipf("chmod unsupported in this environment: %v", err)
	}
	defer func() {
		_ = os.Chmod(dir, 0o755)
	}()
	err := writeFileAtomic(path, []byte("# new"), 0o644)
	if err == nil {
		t.Skip("atomic write unexpectedly succeeded with read-only directory")
	}
	current, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read file after failure failed: %v", readErr)
	}
	if string(current) != "# old" {
		t.Fatalf("expected original content to remain, got %q", string(current))
	}
}

type fakeClient struct {
	files             map[string]RemoteFile
	events            []FilesystemEvent
	revisionCounter   int
	eventCounter      int
	listTreeCalls     int
	eventsUnsupported bool
}

func (c *fakeClient) ListTree(ctx context.Context, workspaceID, path string, depth int, cursor string) (TreeResponse, error) {
	_ = ctx
	_ = workspaceID
	_ = depth
	_ = cursor
	c.listTreeCalls++
	base := normalizeRemotePath(path)
	entries := make([]TreeEntry, 0, len(c.files))
	for remotePath, file := range c.files {
		if !isUnderRemoteRoot(base, remotePath) {
			continue
		}
		entries = append(entries, TreeEntry{
			Path:     remotePath,
			Type:     "file",
			Revision: file.Revision,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return TreeResponse{
		Path:       base,
		Entries:    entries,
		NextCursor: nil,
	}, nil
}

func (c *fakeClient) ListEvents(ctx context.Context, workspaceID, provider, cursor string, limit int) (EventFeed, error) {
	_ = ctx
	_ = workspaceID
	_ = provider
	if c.eventsUnsupported {
		return EventFeed{}, &HTTPError{StatusCode: 404, Code: "not_found", Message: "not found"}
	}
	if limit <= 0 {
		limit = 200
	}
	start := 0
	if cursor != "" {
		for i := range c.events {
			if c.events[i].EventID == cursor {
				start = i + 1
				break
			}
		}
	}
	if start >= len(c.events) {
		return EventFeed{Events: []FilesystemEvent{}, NextCursor: nil}, nil
	}
	end := start + limit
	if end > len(c.events) {
		end = len(c.events)
	}
	chunk := append([]FilesystemEvent(nil), c.events[start:end]...)
	var nextCursor *string
	if end < len(c.events) {
		next := c.events[end-1].EventID
		nextCursor = &next
	}
	return EventFeed{
		Events:     chunk,
		NextCursor: nextCursor,
	}, nil
}

func (c *fakeClient) ReadFile(ctx context.Context, workspaceID, path string) (RemoteFile, error) {
	_ = ctx
	_ = workspaceID
	path = normalizeRemotePath(path)
	file, ok := c.files[path]
	if !ok {
		return RemoteFile{}, &HTTPError{StatusCode: 404, Code: "not_found", Message: "not found"}
	}
	return file, nil
}

func (c *fakeClient) WriteFile(ctx context.Context, workspaceID, path, baseRevision, contentType, content string) (WriteResult, error) {
	_ = ctx
	_ = workspaceID
	path = normalizeRemotePath(path)
	current, exists := c.files[path]
	if !exists && baseRevision != "0" {
		return WriteResult{}, &HTTPError{StatusCode: 404, Code: "not_found", Message: "not found"}
	}
	if exists && current.Revision != baseRevision {
		return WriteResult{}, &ConflictError{Path: path}
	}
	c.revisionCounter++
	revision := fmt.Sprintf("rev_%d", c.revisionCounter)
	eventType := "file.updated"
	if !exists {
		eventType = "file.created"
	}
	c.files[path] = RemoteFile{
		Path:        path,
		Revision:    revision,
		ContentType: contentType,
		Content:     content,
	}
	c.appendEvent(eventType, path, revision)
	return WriteResult{TargetRevision: revision}, nil
}

func (c *fakeClient) DeleteFile(ctx context.Context, workspaceID, path, baseRevision string) error {
	_ = ctx
	_ = workspaceID
	path = normalizeRemotePath(path)
	current, exists := c.files[path]
	if !exists {
		return &HTTPError{StatusCode: 404, Code: "not_found", Message: "not found"}
	}
	if current.Revision != baseRevision {
		return &ConflictError{Path: path}
	}
	delete(c.files, path)
	c.appendEvent("file.deleted", path, current.Revision)
	return nil
}

func assertLocalFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read local file %s failed: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("expected %s to contain %q, got %q", path, want, string(data))
	}
}

func newMockMountsyncServer(
	t *testing.T,
	files map[string]RemoteFile,
	readDenied map[string]struct{},
	writeDenied map[string]struct{},
) *httptest.Server {
	t.Helper()

	normalizedFiles := map[string]RemoteFile{}
	for path, file := range files {
		normalizedFiles[normalizeRemotePath(path)] = file
	}
	normalizedReadDenied := map[string]struct{}{}
	for path := range readDenied {
		normalizedReadDenied[normalizeRemotePath(path)] = struct{}{}
	}
	normalizedWriteDenied := map[string]struct{}{}
	for path := range writeDenied {
		normalizedWriteDenied[normalizeRemotePath(path)] = struct{}{}
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/fs/tree") && r.Method == http.MethodGet:
			entries := make([]TreeEntry, 0, len(normalizedFiles))
			for path, file := range normalizedFiles {
				entries = append(entries, TreeEntry{
					Path:     path,
					Type:     "file",
					Revision: file.Revision,
				})
			}
			sort.Slice(entries, func(i, j int) bool {
				return entries[i].Path < entries[j].Path
			})
			writeJSONResponse(t, w, http.StatusOK, TreeResponse{
				Path:       "/notion",
				Entries:    entries,
				NextCursor: nil,
			})
		case strings.HasSuffix(r.URL.Path, "/fs/events") && r.Method == http.MethodGet:
			writeJSONResponse(t, w, http.StatusOK, EventFeed{
				Events:     []FilesystemEvent{},
				NextCursor: nil,
			})
		case strings.HasSuffix(r.URL.Path, "/fs/file") && r.Method == http.MethodGet:
			path := normalizeRemotePath(r.URL.Query().Get("path"))
			if _, denied := normalizedReadDenied[path]; denied {
				writeJSONResponse(t, w, http.StatusForbidden, map[string]any{
					"code":    "forbidden",
					"message": "denied",
				})
				return
			}
			file, ok := normalizedFiles[path]
			if !ok {
				writeJSONResponse(t, w, http.StatusNotFound, map[string]any{
					"code":    "not_found",
					"message": "not found",
				})
				return
			}
			writeJSONResponse(t, w, http.StatusOK, file)
		case strings.HasSuffix(r.URL.Path, "/fs/file") && r.Method == http.MethodPut:
			path := normalizeRemotePath(r.URL.Query().Get("path"))
			if _, denied := normalizedWriteDenied[path]; denied {
				writeJSONResponse(t, w, http.StatusForbidden, map[string]any{
					"code":    "forbidden",
					"message": "denied",
				})
				return
			}
			var payload struct {
				Content     string `json:"content"`
				ContentType string `json:"contentType"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				writeJSONResponse(t, w, http.StatusBadRequest, map[string]any{
					"code":    "bad_request",
					"message": "invalid json",
				})
				return
			}
			file := normalizedFiles[path]
			file.Path = path
			file.Content = payload.Content
			if payload.ContentType != "" {
				file.ContentType = payload.ContentType
			}
			if file.Revision == "" {
				file.Revision = "rev_1"
			} else {
				file.Revision = "rev_2"
			}
			normalizedFiles[path] = file
			writeJSONResponse(t, w, http.StatusOK, WriteResult{
				TargetRevision: file.Revision,
			})
		case strings.HasSuffix(r.URL.Path, "/fs/file") && r.Method == http.MethodDelete:
			path := normalizeRemotePath(r.URL.Query().Get("path"))
			if _, denied := normalizedWriteDenied[path]; denied {
				writeJSONResponse(t, w, http.StatusForbidden, map[string]any{
					"code":    "forbidden",
					"message": "denied",
				})
				return
			}
			delete(normalizedFiles, path)
			writeJSONResponse(t, w, http.StatusOK, map[string]any{})
		default:
			writeJSONResponse(t, w, http.StatusNotFound, map[string]any{
				"code":    "not_found",
				"message": "route not found",
			})
		}
	}))
}

func writeJSONResponse(t *testing.T, w http.ResponseWriter, status int, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("write JSON response failed: %v", err)
	}
}

func parseMountsyncTokenScopes(t *testing.T, token string) map[string]struct{} {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		t.Fatalf("invalid token format: %q", token)
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode token payload failed: %v", err)
	}
	var claims struct {
		Scopes []string `json:"scopes"`
	}
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		t.Fatalf("decode token claims failed: %v", err)
	}

	out := map[string]struct{}{}
	for _, scope := range claims.Scopes {
		scope = strings.TrimSpace(scope)
		if scope != "" {
			out[scope] = struct{}{}
		}
	}
	return out
}

func canWritePath(scopes map[string]struct{}, path string) bool {
	target := normalizeRemotePath(path)
	for scope := range scopes {
		if canWritePathForScope(scope, target) {
			return true
		}
	}
	return false
}

func canReadPath(scopes map[string]struct{}, path string) bool {
	if len(scopes) == 0 {
		return true
	}
	target := normalizeRemotePath(path)
	for scope := range scopes {
		if canReadPathForScope(scope, target) {
			return true
		}
	}
	return false
}

func canWritePathForScope(scope, path string) bool {
	scope = strings.TrimSpace(scope)
	if scope == "fs:write" {
		return true
	}
	parts := strings.Split(scope, ":")
	if len(parts) < 3 {
		return false
	}
	plane := strings.ToLower(parts[0])
	resource := strings.ToLower(parts[1])
	action := strings.ToLower(parts[2])
	if plane != "relayfile" && plane != "*" {
		return false
	}
	if resource != "fs" && resource != "*" {
		return false
	}
	if action != "write" && action != "manage" && action != "*" {
		return false
	}

	pathPattern := ""
	if len(parts) >= 4 {
		pathPattern = strings.TrimSpace(parts[3])
	}
	if pathPattern == "" || pathPattern == "*" {
		return true
	}
	if strings.HasSuffix(pathPattern, "/*") {
		base := strings.TrimSuffix(pathPattern, "/*")
		if base == "" {
			return true
		}
		return path == base || strings.HasPrefix(path, base+"/")
	}

	return path == normalizeRemotePath(pathPattern)
}

func canReadPathForScope(scope, path string) bool {
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" {
		return false
	}
	if scope == "fs:read" {
		return true
	}
	parts := strings.Split(scope, ":")
	if len(parts) < 3 {
		return false
	}
	plane := strings.ToLower(strings.TrimSpace(parts[0]))
	resource := strings.ToLower(strings.TrimSpace(parts[1]))
	action := strings.ToLower(strings.TrimSpace(parts[2]))
	if plane != "relayfile" && plane != "*" {
		return false
	}
	if resource != "fs" && resource != "*" {
		return false
	}
	if action != "read" && action != "manage" && action != "*" {
		return false
	}

	pathPattern := ""
	if len(parts) >= 4 {
		pathPattern = strings.TrimSpace(parts[3])
	}
	if pathPattern == "" || pathPattern == "*" {
		return true
	}
	pathPattern = normalizeRemotePath(pathPattern)
	if pathPattern == "/" {
		return true
	}
	if strings.HasSuffix(pathPattern, "/*") {
		prefix := strings.TrimSuffix(pathPattern, "/*")
		if prefix == "" {
			return true
		}
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	}

	return path == pathPattern
}

func assertStateMarksPathDenied(t *testing.T, stateFile, remotePath string) {
	t.Helper()
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("read state file failed: %v", err)
	}
	var raw struct {
		Files map[string]json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal state failed: %v", err)
	}
	blob, ok := raw.Files[remotePath]
	if !ok {
		t.Fatalf("expected state file to track path %q", remotePath)
	}
	var entry struct {
		Denied *bool `json:"denied"`
	}
	if err := json.Unmarshal(blob, &entry); err != nil {
		t.Fatalf("unmarshal state entry failed: %v", err)
	}
	if entry.Denied == nil || !*entry.Denied {
		t.Fatalf("expected state path %q marked denied", remotePath)
	}
}

func writeMountState(stateFile string, state mountState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(stateFile, data, 0o644)
}

func waitForLocalContent(t *testing.T, path, want string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && string(data) == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}

	assertLocalFileContent(t, path, want)
}

func writeMountsyncRemoteFile(t *testing.T, client *http.Client, baseURL, token, workspaceID, path, baseRevision, content string) {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"contentType": "text/markdown",
		"content":     content,
	})
	if err != nil {
		t.Fatalf("marshal remote file body failed: %v", err)
	}

	req, err := http.NewRequest(http.MethodPut, baseURL+"/v1/workspaces/"+workspaceID+"/fs/file?path="+url.QueryEscape(path), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build remote file request failed: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Correlation-Id", correlationID())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", baseRevision)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("write remote file request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200/202 writing remote file, got %d (%s)", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
}

func mustMountsyncTestJWT(t *testing.T, secret, workspaceID, agentName string, scopes []string, exp time.Time) string {
	t.Helper()

	headerBytes, err := json.Marshal(map[string]any{
		"alg": "HS256",
		"typ": "JWT",
	})
	if err != nil {
		t.Fatalf("marshal jwt header: %v", err)
	}
	payloadBytes, err := json.Marshal(map[string]any{
		"workspace_id": workspaceID,
		"agent_name":   agentName,
		"scopes":       scopes,
		"exp":          exp.Unix(),
		"aud":          "relayfile",
	})
	if err != nil {
		t.Fatalf("marshal jwt payload: %v", err)
	}

	h := base64.RawURLEncoding.EncodeToString(headerBytes)
	p := base64.RawURLEncoding.EncodeToString(payloadBytes)
	signingInput := h + "." + p

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(signingInput))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return signingInput + "." + signature
}

func (c *fakeClient) appendEvent(eventType, path, revision string) {
	c.eventCounter++
	c.events = append(c.events, FilesystemEvent{
		EventID:  fmt.Sprintf("evt_%d", c.eventCounter),
		Type:     eventType,
		Path:     path,
		Revision: revision,
	})
}
