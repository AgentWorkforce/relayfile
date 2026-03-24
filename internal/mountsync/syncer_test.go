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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/httpapi"
	"github.com/agentworkforce/relayfile/internal/relayfile"
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
