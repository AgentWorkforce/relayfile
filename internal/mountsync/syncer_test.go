package mountsync

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/httpapi"
	"github.com/agentworkforce/relayfile/internal/relayfile"
	"github.com/fsnotify/fsnotify"
)

func boolPtr(value bool) *bool {
	return &value
}

func markLocalDirtyForTest(t *testing.T, syncer *Syncer, remotePath, localPath string) {
	t.Helper()
	snapshot, err := readLocalSnapshot(localPath, true)
	if err != nil {
		t.Fatalf("read dirty snapshot for %s: %v", remotePath, err)
	}
	tracked := syncer.state.Files[normalizeRemotePath(remotePath)]
	tracked.ContentType = snapshot.ContentType
	tracked.Encoding = normalizeEncoding(snapshot.Encoding)
	tracked.Hash = snapshot.Hash
	tracked.Dirty = true
	tracked.DeletePending = false
	syncer.state.Files[normalizeRemotePath(remotePath)] = tracked
}

func TestHTTPClientRetryDelayHonorsRetryAfter(t *testing.T) {
	client := NewHTTPClient("https://example.test", "token", nil)
	if got := client.retryDelay(1, "30"); got != 30*time.Second {
		t.Fatalf("expected Retry-After 30s, got %s", got)
	}
	if got := client.retryDelay(1, "999"); got != defaultRetryAfterMaxDelay {
		t.Fatalf("expected Retry-After cap %s, got %s", defaultRetryAfterMaxDelay, got)
	}
}

func TestWebSocketReconnectDelayBounds(t *testing.T) {
	if got := websocketReconnectDelay(1); got < defaultWebSocketReconnectBase || got > defaultWebSocketReconnectBase+defaultWebSocketReconnectJitter {
		t.Fatalf("first reconnect delay out of bounds: %s", got)
	}
	if got := websocketReconnectDelay(20); got < defaultWebSocketReconnectMax-defaultWebSocketReconnectJitter || got > defaultWebSocketReconnectMax {
		t.Fatalf("capped reconnect delay out of bounds: %s", got)
	}
}

func TestWebSocketConnectDueRespectsScheduledBackoff(t *testing.T) {
	syncer := &Syncer{websocket: true}
	now := time.Now()
	if !syncer.websocketConnectDueLocked(now) {
		t.Fatal("expected websocket connect to be due before a backoff is scheduled")
	}
	syncer.wsNextAttempt = now.Add(time.Minute)
	if syncer.websocketConnectDueLocked(now) {
		t.Fatal("expected websocket connect to wait for scheduled backoff")
	}
	syncer.wsNextAttempt = time.Time{}
	syncer.wsConnecting = true
	if syncer.websocketConnectDueLocked(now) {
		t.Fatal("expected websocket connect to wait while another dial is in progress")
	}
}

func TestMaintainWebSocketHonorsRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "17")
		http.Error(w, "busy", http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "token", server.Client())
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_retry_after",
		RemoteRoot:  "/",
		LocalRoot:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewSyncer failed: %v", err)
	}

	before := time.Now()
	if err := syncer.MaintainWebSocket(context.Background()); err == nil {
		t.Fatal("expected websocket dial to fail")
	}

	syncer.mu.Lock()
	nextAttempt := syncer.wsNextAttempt
	connecting := syncer.wsConnecting
	syncer.mu.Unlock()

	if connecting {
		t.Fatal("expected websocket connecting flag to be cleared after failed dial")
	}
	if nextAttempt.Before(before.Add(16*time.Second)) || nextAttempt.After(before.Add(18*time.Second)) {
		t.Fatalf("expected Retry-After based reconnect around 17s, got %s from now", time.Until(nextAttempt))
	}
}

func TestWebSocketConnectFailureDoesNotClearNewGeneration(t *testing.T) {
	syncer := &Syncer{
		websocket:    true,
		wsConnecting: true,
		wsGeneration: 2,
	}

	syncer.finishWebSocketConnectFailure(1, 17*time.Second)

	syncer.mu.Lock()
	defer syncer.mu.Unlock()
	if !syncer.wsConnecting {
		t.Fatal("stale dial failure cleared the current generation's connecting flag")
	}
	if !syncer.wsNextAttempt.IsZero() {
		t.Fatalf("stale dial failure scheduled backoff for current generation: %s", syncer.wsNextAttempt)
	}
}

func TestResetWebSocketInvalidatesInFlightDialGeneration(t *testing.T) {
	syncer := &Syncer{
		websocket:    true,
		wsConnecting: true,
	}

	syncer.ResetWebSocket()
	syncer.finishWebSocketConnectFailure(0, 17*time.Second)

	syncer.mu.Lock()
	defer syncer.mu.Unlock()
	if syncer.wsGeneration != 1 {
		t.Fatalf("expected reset to advance websocket generation, got %d", syncer.wsGeneration)
	}
	if !syncer.wsNextAttempt.IsZero() {
		t.Fatalf("stale dial scheduled backoff after reset: %s", syncer.wsNextAttempt)
	}
}

type fakeProviderLayoutRegistrar struct {
	calls    []string
	manifest map[string]ProviderLayoutManifest
}

func (r *fakeProviderLayoutRegistrar) RegisterProviderLayout(provider string, manifest ProviderLayoutManifest) error {
	if r.manifest == nil {
		r.manifest = map[string]ProviderLayoutManifest{}
	}
	r.calls = append(r.calls, provider)
	r.manifest[provider] = manifest
	return nil
}

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
	if err := syncer.HandleLocalChange(context.Background(), "Docs/A.md", fsnotify.Write); err != nil {
		t.Fatalf("handle local edit failed: %v", err)
	}

	remote := client.files["/notion/Docs/A.md"]
	if remote.Content != "# A edited" {
		t.Fatalf("expected remote content to update, got %q", remote.Content)
	}
	if remote.Revision == "rev_1" {
		t.Fatalf("expected remote revision to advance")
	}
}

func TestSyncOnceWriteOnlySkipsRemotePullButPushesLocalFiles(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{
			"/slack/channels/C123/messages/history.json": {
				Path:        "/slack/channels/C123/messages/history.json",
				Revision:    "rev_1",
				ContentType: "application/json",
				Content:     `{"text":"history"}`,
			},
		},
		revisionCounter: 1,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_write_only",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
		SyncMode:    "write-only",
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	localDraft := filepath.Join(localDir, "wb-pear-ack.json")
	if err := os.WriteFile(localDraft, []byte(`{"text":"ack"}`), 0o644); err != nil {
		t.Fatalf("write local draft failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("write-only sync failed: %v", err)
	}

	if client.listTreeCalls != 0 || client.listEventsCalls != 0 || client.readFileCalls != 0 {
		t.Fatalf("write-only sync should not pull remote records; tree=%d events=%d read=%d", client.listTreeCalls, client.listEventsCalls, client.readFileCalls)
	}
	if got := len(client.bulkWriteBatches); got != 1 {
		t.Fatalf("expected one bulk write batch, got %d", got)
	}
	if got, want := client.bulkWriteBatches[0][0].Path, "/slack/channels/C123/messages/wb-pear-ack.json"; got != want {
		t.Fatalf("expected canonical write path %q, got %q", want, got)
	}
	if _, err := os.Stat(filepath.Join(localDir, ".relay", "dead-letter")); err != nil {
		t.Fatalf("expected write-only mount to keep dead-letter feedback dir: %v", err)
	}
	var public struct {
		Mode     string `json:"mode"`
		SyncMode string `json:"syncMode"`
	}
	data, err := os.ReadFile(filepath.Join(localDir, ".relay", "state.json"))
	if err != nil {
		t.Fatalf("read public state: %v", err)
	}
	if err := json.Unmarshal(data, &public); err != nil {
		t.Fatalf("decode public state: %v", err)
	}
	if public.Mode != "poll" || public.SyncMode != "write-only" {
		t.Fatalf("expected public state mode poll/write-only, got %+v", public)
	}
	if _, err := os.Stat(filepath.Join(localDir, "history.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write-only sync mirrored provider history, stat err=%v", err)
	}
}

func TestHandleLocalChangeIgnoresAlreadyTrackedContent(t *testing.T) {
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
		WorkspaceID: "ws_mount_watcher_echo",
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
	assertLocalFileContent(t, localFile, "# A")

	if err := syncer.HandleLocalChange(context.Background(), "Docs/A.md", fsnotify.Write); err != nil {
		t.Fatalf("handle unchanged local write failed: %v", err)
	}

	if client.writeFileCalls != 0 || client.bulkWriteCalls != 0 {
		t.Fatalf("expected unchanged watcher event not to push, got %d write calls and %d bulk calls", client.writeFileCalls, client.bulkWriteCalls)
	}
	remote := client.files["/notion/Docs/A.md"]
	if remote.Revision != "rev_1" {
		t.Fatalf("expected remote revision to remain rev_1, got %q", remote.Revision)
	}
}

func TestLazyReposSkipsEagerFetchOfIssuesOnStartup(t *testing.T) {
	t.Setenv("RELAYFILE_LAZY_REPOS", "")
	t.Setenv("RELAYFILE_MOUNT_LAZY_GITHUB_REPOS", "")

	client := &fakeClient{
		files: map[string]RemoteFile{
			"/github/repos/octocat/hello-world/_index.json": {
				Path:        "/github/repos/octocat/hello-world/_index.json",
				Revision:    "rev_index",
				ContentType: "application/json",
				Content:     `{"repo":"hello-world"}`,
			},
			"/github/repos/octocat/hello-world/issues/issue-1.json": {
				Path:        "/github/repos/octocat/hello-world/issues/issue-1.json",
				Revision:    "rev_issue_1",
				ContentType: "application/json",
				Content:     `{"id":1}`,
			},
		},
		revisionCounter: 2,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_lazy_repos_on",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
		LazyRepos:   boolPtr(true),
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("lazy-repos sync failed: %v", err)
	}

	if got := client.readFileCallsByPath["/github/repos/octocat/hello-world/issues/issue-1.json"]; got != 0 {
		t.Fatalf("expected zero eager issue reads in lazy mode, got %d", got)
	}
	if client.listTreeCalls != 1 {
		t.Fatalf("expected one bootstrap ListTree call, got %d", client.listTreeCalls)
	}
}

func TestLazyReposDefaultsToEagerFetchOfIssues(t *testing.T) {
	t.Setenv("RELAYFILE_LAZY_REPOS", "")
	t.Setenv("RELAYFILE_MOUNT_LAZY_GITHUB_REPOS", "")

	client := &fakeClient{
		files: map[string]RemoteFile{
			"/github/repos/octocat/hello-world/_index.json": {
				Path:        "/github/repos/octocat/hello-world/_index.json",
				Revision:    "rev_index",
				ContentType: "application/json",
				Content:     `{"repo":"hello-world"}`,
			},
			"/github/repos/octocat/hello-world/issues/issue-1.json": {
				Path:        "/github/repos/octocat/hello-world/issues/issue-1.json",
				Revision:    "rev_issue_1",
				ContentType: "application/json",
				Content:     `{"id":1}`,
			},
		},
		revisionCounter: 2,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_lazy_repos_default",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("default sync failed: %v", err)
	}

	if got := client.readFileCallsByPath["/github/repos/octocat/hello-world/issues/issue-1.json"]; got < 1 {
		t.Fatalf("expected eager issue reads by default, got %d", got)
	}
}

func TestLazyReposEnvFallbackStillSkipsEagerFetchOfIssues(t *testing.T) {
	t.Setenv("RELAYFILE_LAZY_REPOS", "true")
	t.Setenv("RELAYFILE_MOUNT_LAZY_GITHUB_REPOS", "")

	client := &fakeClient{
		files: map[string]RemoteFile{
			"/github/repos/octocat/hello-world/_index.json": {
				Path:        "/github/repos/octocat/hello-world/_index.json",
				Revision:    "rev_index",
				ContentType: "application/json",
				Content:     `{"repo":"hello-world"}`,
			},
			"/github/repos/octocat/hello-world/issues/issue-1.json": {
				Path:        "/github/repos/octocat/hello-world/issues/issue-1.json",
				Revision:    "rev_issue_1",
				ContentType: "application/json",
				Content:     `{"id":1}`,
			},
		},
		revisionCounter: 2,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_lazy_repos_env",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("env lazy sync failed: %v", err)
	}

	if got := client.readFileCallsByPath["/github/repos/octocat/hello-world/issues/issue-1.json"]; got != 0 {
		t.Fatalf("expected zero eager issue reads when env opts into lazy mode, got %d", got)
	}
}

func TestLazyReposOffStillFetchesIssues(t *testing.T) {
	t.Setenv("RELAYFILE_LAZY_REPOS", "")
	t.Setenv("RELAYFILE_MOUNT_LAZY_GITHUB_REPOS", "")

	client := &fakeClient{
		files: map[string]RemoteFile{
			"/github/repos/octocat/hello-world/_index.json": {
				Path:        "/github/repos/octocat/hello-world/_index.json",
				Revision:    "rev_index",
				ContentType: "application/json",
				Content:     `{"repo":"hello-world"}`,
			},
			"/github/repos/octocat/hello-world/issues/issue-1.json": {
				Path:        "/github/repos/octocat/hello-world/issues/issue-1.json",
				Revision:    "rev_issue_1",
				ContentType: "application/json",
				Content:     `{"id":1}`,
			},
		},
		revisionCounter: 2,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_lazy_repos_off",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
		LazyRepos:   boolPtr(false),
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("non-lazy sync failed: %v", err)
	}

	if got := client.readFileCallsByPath["/github/repos/octocat/hello-world/issues/issue-1.json"]; got < 1 {
		t.Fatalf("expected eager issue reads when lazy mode is off, got %d", got)
	}
}

func TestLazyReposExplicitFalseOverridesEnv(t *testing.T) {
	t.Setenv("RELAYFILE_LAZY_REPOS", "true")
	t.Setenv("RELAYFILE_MOUNT_LAZY_GITHUB_REPOS", "")

	client := &fakeClient{
		files: map[string]RemoteFile{
			"/github/repos/octocat/hello-world/_index.json": {
				Path:        "/github/repos/octocat/hello-world/_index.json",
				Revision:    "rev_index",
				ContentType: "application/json",
				Content:     `{"repo":"hello-world"}`,
			},
			"/github/repos/octocat/hello-world/issues/issue-1.json": {
				Path:        "/github/repos/octocat/hello-world/issues/issue-1.json",
				Revision:    "rev_issue_1",
				ContentType: "application/json",
				Content:     `{"id":1}`,
			},
		},
		revisionCounter: 2,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_lazy_repos_explicit_false",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
		LazyRepos:   boolPtr(false),
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("explicit non-lazy sync failed: %v", err)
	}

	if got := client.readFileCallsByPath["/github/repos/octocat/hello-world/issues/issue-1.json"]; got < 1 {
		t.Fatalf("expected eager issue reads when explicit lazy mode is off, got %d", got)
	}
}

func TestIsUnderLazyGithubRepoSubtree(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		remoteRoot string
		remotePath string
		want       bool
	}{
		{
			name:       "repos root",
			remoteRoot: "/",
			remotePath: "/github/repos",
			want:       false,
		},
		{
			name:       "repo root",
			remoteRoot: "/",
			remotePath: "/github/repos/octocat/hello-world",
			want:       false,
		},
		{
			name:       "repo subtree file",
			remoteRoot: "/",
			remotePath: "/github/repos/octocat/hello-world/issues/issue-1.json",
			want:       true,
		},
		{
			name:       "repo subtree trailing slash",
			remoteRoot: "/",
			remotePath: "/github/repos/octocat/hello-world/issues/",
			want:       true,
		},
		{
			name:       "other integration",
			remoteRoot: "/",
			remotePath: "/notion/pages/x.json",
			want:       false,
		},
		{
			name:       "slack integration",
			remoteRoot: "/",
			remotePath: "/slack/channels/C123/messages/1711111000_000100.json",
			want:       false,
		},
		{
			name:       "memory integration",
			remoteRoot: "/",
			remotePath: "/memory/workspace/daily-ship.md",
			want:       false,
		},
		{
			name:       "root github dir",
			remoteRoot: "/",
			remotePath: "/github",
			want:       false,
		},
		{
			name:       "nested remote root",
			remoteRoot: "/relay",
			remotePath: "/relay/github/repos/octocat/hello-world/issues/issue-1.json",
			want:       true,
		},
		{
			name:       "scoped org remote root repo subtree file",
			remoteRoot: "/github/repos/AgentWorkforce",
			remotePath: "/github/repos/AgentWorkforce/cloud/pulls/123.json",
			want:       true,
		},
		{
			name:       "scoped org and repo remote root repo subtree file",
			remoteRoot: "/github/repos/AgentWorkforce/cloud/pulls",
			remotePath: "/github/repos/AgentWorkforce/cloud/pulls/123.json",
			want:       true,
		},
		{
			name:       "scoped org remote root repo root",
			remoteRoot: "/github/repos/AgentWorkforce",
			remotePath: "/github/repos/AgentWorkforce/cloud",
			want:       false,
		},
		{
			name:       "outside scoped org remote root",
			remoteRoot: "/github/repos/AgentWorkforce",
			remotePath: "/github/repos/OtherOrg/cloud/pulls/123.json",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUnderLazyGithubRepoSubtree(tt.remoteRoot, tt.remotePath); got != tt.want {
				t.Fatalf("isUnderLazyGithubRepoSubtree(%q, %q) = %v, want %v", tt.remoteRoot, tt.remotePath, got, tt.want)
			}
		})
	}
}

// TestHandleLocalChangePushesOnChmodOnlyEvent pins the regression that
// motivated the state-driven dispatch: editors (Vim, VSCode, JetBrains)
// often end a save sequence with a Chmod event, and the per-path
// debounce in the watcher only retains the *last* op within its 100ms
// window. Pre-fix, an op of `fsnotify.Chmod` alone hit a no-op branch
// and silently failed to queue a writeback for the new content. Now we
// dispatch by file state — the file exists with new content, so we
// hash-check and push the update.
func TestHandleLocalChangePushesOnChmodOnlyEvent(t *testing.T) {
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
		WorkspaceID: "ws_chmod_only",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}

	// Modify the file content as an editor would, then deliver only
	// fsnotify.Chmod (the surviving op after debounce-collapse on macOS).
	localFile := filepath.Join(localDir, "Docs", "A.md")
	if err := os.WriteFile(localFile, []byte("# A — edited"), 0o644); err != nil {
		t.Fatalf("edit local file: %v", err)
	}

	if err := syncer.HandleLocalChange(context.Background(), "Docs/A.md", fsnotify.Chmod); err != nil {
		t.Fatalf("handle chmod-only event failed: %v", err)
	}

	if client.bulkWriteCalls == 0 && client.writeFileCalls == 0 {
		t.Fatalf("expected chmod-only event with new content to push update; bulk=%d write=%d",
			client.bulkWriteCalls, client.writeFileCalls)
	}
	remote := client.files["/notion/Docs/A.md"]
	if remote.Content != "# A — edited" {
		t.Fatalf("expected remote content to reflect local edit, got %q", remote.Content)
	}
}

// TestHandleLocalChangeTreatsAtomicRenameAsUpdate pins the second
// regression: editors that save-via-rename (Vim's default, many IDEs)
// can deliver a Rename event for the target path even though the file
// is still present afterward. Pre-fix, the Remove|Rename branch
// blindly called pushSingleDelete and removed the cloud file. Now we
// stat the path; if it exists, we route as an update.
func TestHandleLocalChangeTreatsAtomicRenameAsUpdate(t *testing.T) {
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
		WorkspaceID: "ws_atomic_rename",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}

	// Simulate atomic save-via-rename: write the new content (as if a
	// .swp file had just been renamed over the target), then deliver
	// only fsnotify.Rename for the target path.
	localFile := filepath.Join(localDir, "Docs", "A.md")
	if err := os.WriteFile(localFile, []byte("# A — atomically saved"), 0o644); err != nil {
		t.Fatalf("write replacement content: %v", err)
	}

	if err := syncer.HandleLocalChange(context.Background(), "Docs/A.md", fsnotify.Rename); err != nil {
		t.Fatalf("handle atomic rename failed: %v", err)
	}

	// Must have pushed the update, not deleted the file.
	if _, exists := client.files["/notion/Docs/A.md"]; !exists {
		t.Fatalf("atomic rename should not have deleted the cloud file")
	}
	remote := client.files["/notion/Docs/A.md"]
	if remote.Content != "# A — atomically saved" {
		t.Fatalf("expected remote content to reflect the atomically-renamed content, got %q",
			remote.Content)
	}
}

// TestHandleLocalChangeDeletesWhenFileGone confirms that an actual
// removal — file no longer present on disk — still routes to a delete
// post-fix. The Remove|Rename branch is gone, but the state-driven
// dispatch must still treat a missing local file as a delete signal.
func TestHandleLocalChangeDeletesWhenFileGone(t *testing.T) {
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
		WorkspaceID: "ws_actual_delete",
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
	if err := os.Remove(localFile); err != nil {
		t.Fatalf("remove local file: %v", err)
	}
	if err := syncer.HandleLocalChange(context.Background(), "Docs/A.md", fsnotify.Remove); err != nil {
		t.Fatalf("handle remove failed: %v", err)
	}
	if _, exists := client.files["/notion/Docs/A.md"]; exists {
		t.Fatalf("expected cloud file to be deleted after local removal")
	}
}

func TestHandleLocalChangeSkipsNestedMountRuntimeState(t *testing.T) {
	client := &fakeClient{
		files:           map[string]RemoteFile{},
		revisionCounter: 1,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:   "ws_nested_relay_change",
		RemoteRoot:    "/slack",
		LocalRoot:     localDir,
		FullPullEvery: -1,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	runtimeRel := filepath.ToSlash(filepath.Join("channels", "C123", "messages", ".relay", "state.json"))
	runtimeLocal := filepath.Join(localDir, filepath.FromSlash(runtimeRel))
	if err := os.MkdirAll(filepath.Dir(runtimeLocal), 0o755); err != nil {
		t.Fatalf("mkdir nested .relay: %v", err)
	}
	if err := os.WriteFile(runtimeLocal, []byte(`{"status":"writeback-pending"}`), 0o644); err != nil {
		t.Fatalf("write nested .relay state: %v", err)
	}

	if err := syncer.HandleLocalChange(context.Background(), runtimeRel, fsnotify.Write); err != nil {
		t.Fatalf("handle nested runtime change failed: %v", err)
	}

	if client.bulkWriteCalls != 0 || client.writeFileCalls != 0 {
		t.Fatalf("nested mount runtime file must not be uploaded, bulk=%d write=%d", client.bulkWriteCalls, client.writeFileCalls)
	}
	if len(syncer.state.Files) != 0 {
		t.Fatalf("nested mount runtime file must not be tracked, got %#v", syncer.state.Files)
	}
}

func TestReconcileUsesExportSnapshotForInitialPull(t *testing.T) {
	base := &fakeClient{
		files: map[string]RemoteFile{
			"/github/repos/demo/README.md": {
				Path:        "/github/repos/demo/README.md",
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     "# Demo",
			},
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_2",
				ContentType: "text/markdown",
				Content:     "# A",
			},
		},
	}
	client := &fakeExportClient{fakeClient: base}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_export",
		RemoteRoot:  "/github",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if client.exportCalls != 1 {
		t.Fatalf("expected one export snapshot call, got %d", client.exportCalls)
	}
	if base.listTreeCalls != 0 {
		t.Fatalf("expected export bootstrap to avoid list tree, got %d calls", base.listTreeCalls)
	}
	if client.readFileCalls != 0 {
		t.Fatalf("expected export bootstrap to avoid per-file reads, got %d calls", client.readFileCalls)
	}
	assertLocalFileContent(t, filepath.Join(localDir, "repos", "demo", "README.md"), "# Demo")
	if _, err := os.Stat(filepath.Join(localDir, "notion", "Docs", "A.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected remote root filter to exclude notion file, stat err=%v", err)
	}
}

func TestReconcileFallsBackToTreeWhenExportJSONTruncated(t *testing.T) {
	base := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     "# A",
			},
		},
	}
	client := &fakeExportClient{
		fakeClient: base,
		exportErr:  errors.New("unexpected end of JSON input"),
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_truncated_export",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile should fall back to tree after truncated export: %v", err)
	}

	if client.exportCalls != 1 {
		t.Fatalf("expected one export snapshot attempt, got %d", client.exportCalls)
	}
	if base.listTreeCalls != 1 {
		t.Fatalf("expected truncated export to fall back to list tree once, got %d calls", base.listTreeCalls)
	}
	if client.readFileCalls != 1 {
		t.Fatalf("expected tree fallback to read one file, got %d calls", client.readFileCalls)
	}
	assertLocalFileContent(t, filepath.Join(localDir, "Docs", "A.md"), "# A")
}

func TestReconcileFallsBackToTreeWhenExportDurableObjectOverloaded(t *testing.T) {
	base := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     "# A",
			},
		},
	}
	client := &fakeExportClient{
		fakeClient: base,
		exportErr: &HTTPError{
			StatusCode: 500,
			Code:       "internal_error",
			Message:    "Durable Object is overloaded. Requests queued for too long.",
		},
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_overloaded_export",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile should fall back to tree after DO overload: %v", err)
	}

	if client.exportCalls != 1 {
		t.Fatalf("expected one export snapshot attempt, got %d", client.exportCalls)
	}
	if base.listTreeCalls != 1 {
		t.Fatalf("expected DO overload to fall back to list tree once, got %d calls", base.listTreeCalls)
	}
	if client.readFileCalls != 1 {
		t.Fatalf("expected tree fallback to read one file, got %d calls", client.readFileCalls)
	}
	assertLocalFileContent(t, filepath.Join(localDir, "Docs", "A.md"), "# A")
}

func TestExportSnapshotOverloadedClassification(t *testing.T) {
	unsupported := []error{
		&HTTPError{StatusCode: 500, Code: "internal_error", Message: "Durable Object is overloaded. Requests queued for too long."},
		&HTTPError{StatusCode: 503, Message: "Worker overloaded"},
		errors.New("http 500 internal_error: Durable Object is overloaded. Requests queued for too long."),
		&HTTPError{StatusCode: 413, Code: "payload_too_large", Message: "workspace export body is 3524788058 bytes, which exceeds the export body limit of 134217728; use paginated tree/read APIs instead"},
		&HTTPError{StatusCode: 429, Code: "workspace_busy", Message: "workspace durable object is busy; retry after the advertised delay"},
	}
	for _, err := range unsupported {
		if !exportSnapshotUnsupported(err) {
			t.Fatalf("expected error to be classified as unsupported: %v", err)
		}
	}

	supported := []error{
		&HTTPError{StatusCode: 500, Code: "internal_error", Message: "boom"},
		&HTTPError{StatusCode: 502, Message: "bad gateway"},
		&HTTPError{StatusCode: 429, Code: "rate_limited", Message: "rate limit exceeded"},
		&HTTPError{StatusCode: 429, Code: "queue_full", Message: "cloud write queue is full"},
		errors.New("http2: server sent GOAWAY and closed the connection"),
	}
	for _, err := range supported {
		if exportSnapshotUnsupported(err) {
			t.Fatalf("expected transient error to retry export, not fall back: %v", err)
		}
	}
}

func TestReconcileFallsBackToTreeWhenExportPayloadTooLarge(t *testing.T) {
	base := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     "# A",
			},
		},
	}
	client := &fakeExportClient{
		fakeClient: base,
		exportErr: &HTTPError{
			StatusCode: 413,
			Code:       "payload_too_large",
			Message:    "workspace export body is 3524788058 bytes, which exceeds the export body limit of 134217728; use paginated tree/read APIs instead",
		},
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_oversized_export",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile should fall back to tree after 413: %v", err)
	}

	if client.exportCalls != 1 {
		t.Fatalf("expected one export snapshot attempt, got %d", client.exportCalls)
	}
	if base.listTreeCalls != 1 {
		t.Fatalf("expected 413 to fall back to list tree once, got %d calls", base.listTreeCalls)
	}
	if client.readFileCalls != 1 {
		t.Fatalf("expected tree fallback to read one file, got %d calls", client.readFileCalls)
	}
	assertLocalFileContent(t, filepath.Join(localDir, "Docs", "A.md"), "# A")
}

// TestReconcileFallsBackToTreeWhenExportExceedsSubDeadline is the core #1499/
// #1516 convergence regression: a slow atomic export that would otherwise run
// until the no-progress bootstrap watchdog cancels it (with zero files applied
// and no resume cursor -> permanent "non-empty without completed bootstrap"
// loop) must instead hit its OWN short sub-deadline while the bootstrap ctx is
// still alive, and fall through to the resumable, per-page pullRemoteFullTree.
func TestReconcileFallsBackToTreeWhenExportExceedsSubDeadline(t *testing.T) {
	base := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     "# A",
			},
		},
	}
	client := &fakeExportClient{fakeClient: base, exportBlockUntilCancel: true}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:   "ws_slow_export",
		RemoteRoot:    "/notion",
		LocalRoot:     localDir,
		ExportTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile should fall back to tree after export sub-deadline: %v", err)
	}

	if client.exportCalls != 1 {
		t.Fatalf("expected one export snapshot attempt, got %d", client.exportCalls)
	}
	if base.listTreeCalls != 1 {
		t.Fatalf("expected slow export to fall back to list tree once, got %d calls", base.listTreeCalls)
	}
	if client.readFileCalls != 1 {
		t.Fatalf("expected tree fallback to read one file, got %d calls", client.readFileCalls)
	}
	assertLocalFileContent(t, filepath.Join(localDir, "Docs", "A.md"), "# A")
	if !syncer.state.BootstrapComplete {
		t.Fatalf("tree fallback should complete the bootstrap; loop would otherwise persist")
	}
}

// TestReconcileFallsBackToTreeWhenExportExceedsHardBootstrapCap covers the
// companion clamp: when a positive RELAYFILE_BOOTSTRAP_TIMEOUT (hard cap) is
// shorter than the configured export sub-deadline, exportTimeout is clamped
// below the hard cap so the export still yields to the resumable tree pull
// while the parent bootstrap ctx is alive (rather than the hard cap cancelling
// the parent first and propagating instead of falling through).
func TestReconcileFallsBackToTreeWhenExportExceedsHardBootstrapCap(t *testing.T) {
	base := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     "# A",
			},
		},
	}
	client := &fakeExportClient{fakeClient: base, exportBlockUntilCancel: true}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:      "ws_slow_export_hard_cap",
		RemoteRoot:       "/notion",
		LocalRoot:        localDir,
		BootstrapTimeout: 200 * time.Millisecond,
		ExportTimeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile should fall back to tree before hard bootstrap cap: %v", err)
	}
	if client.exportCalls != 1 {
		t.Fatalf("expected one export snapshot attempt, got %d", client.exportCalls)
	}
	if base.listTreeCalls != 1 {
		t.Fatalf("expected hard-cap-clamped export to fall back to list tree once, got %d calls", base.listTreeCalls)
	}
	assertLocalFileContent(t, filepath.Join(localDir, "Docs", "A.md"), "# A")
	if !syncer.state.BootstrapComplete {
		t.Fatalf("tree fallback should complete the bootstrap before the hard cap")
	}
}

// TestReconcileFallsBackToTreeWhenExportWorkspaceBusy covers the HTTP 429
// workspace_busy signal (ProbeV085's prod evidence): after doJSON exhausts its
// Retry-After backoff, the busy DO surfaces a 429 that must fall through to the
// per-file-bounded tree path instead of retrying the contended atomic export.
func TestReconcileFallsBackToTreeWhenExportWorkspaceBusy(t *testing.T) {
	base := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     "# A",
			},
		},
	}
	client := &fakeExportClient{
		fakeClient: base,
		exportErr: &HTTPError{
			StatusCode: 429,
			Code:       "workspace_busy",
			Message:    "workspace durable object is busy; retry after the advertised delay",
		},
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_busy_export",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile should fall back to tree after 429 workspace_busy: %v", err)
	}
	if client.exportCalls != 1 {
		t.Fatalf("expected one export snapshot attempt, got %d", client.exportCalls)
	}
	if base.listTreeCalls != 1 {
		t.Fatalf("expected 429 workspace_busy to fall back to list tree once, got %d calls", base.listTreeCalls)
	}
	assertLocalFileContent(t, filepath.Join(localDir, "Docs", "A.md"), "# A")
}

// TestExportEmptyButPopulatedTreeRecoversViaTreePull covers the empty-200
// export (ProbeV085's "fresh remote export has 0 files but N tracked locally"):
// a successful-but-empty export for a workspace we KNOW has tracked files must
// NOT markBootstrapComplete (locking in the stale/empty mirror); it falls
// through to the tree pull, which re-reads the real content via a different
// cloud code path and recovers.
func TestExportEmptyButPopulatedTreeRecoversViaTreePull(t *testing.T) {
	disableWS := false
	base := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     "# A",
			},
		},
	}
	client := &fakeExportClient{fakeClient: base, exportReturnsEmpty: true}
	localDir := t.TempDir()
	stateFile := filepath.Join(localDir, ".relayfile-mount-state.json")
	if err := writeMountState(stateFile, mountState{
		Files: map[string]trackedFile{
			"/notion/Docs/A.md": {Revision: "rev_1", ContentType: "text/markdown", Hash: hashString("# A")},
		},
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_empty200_export",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		StateFile:   stateFile,
		WebSocket:   &disableWS,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile should recover via tree pull after empty-200 export: %v", err)
	}
	if client.exportCalls != 1 {
		t.Fatalf("expected one export snapshot attempt, got %d", client.exportCalls)
	}
	if base.listTreeCalls != 1 {
		t.Fatalf("expected empty-200 export to fall back to list tree once, got %d calls", base.listTreeCalls)
	}
	assertLocalFileContent(t, filepath.Join(localDir, "Docs", "A.md"), "# A")
	if !syncer.state.BootstrapComplete {
		t.Fatalf("tree recovery should complete bootstrap; empty export must not have short-circuited it")
	}
}

// TestFailedExportDoesNotAdvanceCursorOrCompleteBootstrap pins the pivotal
// cursor-safety property: a propagated export failure (a transient 5xx that is
// retried as an export, not a fall-through) must not advance EventsCursor nor
// mark the bootstrap complete, so the next cycle re-attempts cleanly instead of
// short-circuiting past unsynced content.
func TestFailedExportDoesNotAdvanceCursorOrCompleteBootstrap(t *testing.T) {
	base := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     "# A",
			},
		},
	}
	client := &fakeExportClient{
		fakeClient: base,
		exportErr:  &HTTPError{StatusCode: 502, Message: "bad gateway"},
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_failed_export",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.Reconcile(context.Background()); err == nil {
		t.Fatalf("expected a propagated 502 export error, got nil")
	}
	if client.exportCalls != 1 {
		t.Fatalf("expected one export snapshot attempt, got %d", client.exportCalls)
	}
	if base.listTreeCalls != 0 {
		t.Fatalf("transient 5xx should retry export, not fall to tree; got %d list tree calls", base.listTreeCalls)
	}
	if strings.TrimSpace(syncer.state.EventsCursor) != "" {
		t.Fatalf("failed export must not advance the events cursor, got %q", syncer.state.EventsCursor)
	}
	if syncer.state.BootstrapComplete {
		t.Fatalf("failed export must not mark bootstrap complete")
	}
}

// TestResolveLatestEventCursorPrefersLatestEventID guards against regressing
// from the one-shot /fs/events?direction=desc&limit=1 lookup back to the
// O(N) page-walk. resolveLatestEventCursor must call LatestEventID first and
// MUST NOT issue any ListEvents calls when LatestEventID succeeds, because
// the page-walk reliably exceeds cursorTimeout on workspaces with large
// event histories (cloud#926).
func TestResolveLatestEventCursorPrefersLatestEventID(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{},
		events: []FilesystemEvent{
			{EventID: "evt_1"}, {EventID: "evt_2"}, {EventID: "evt_3"},
		},
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_latest",
		LocalRoot:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	cursor, err := syncer.resolveLatestEventCursor(context.Background())
	if err != nil {
		t.Fatalf("resolveLatestEventCursor failed: %v", err)
	}
	if cursor != "evt_3" {
		t.Fatalf("expected latest cursor evt_3, got %q", cursor)
	}
	if client.latestEventIDCalls != 1 {
		t.Fatalf("expected exactly 1 LatestEventID call, got %d", client.latestEventIDCalls)
	}
	if client.listEventsCalls != 0 {
		t.Fatalf("expected zero ListEvents calls (one-shot path), got %d", client.listEventsCalls)
	}
}

// TestResolveLatestEventCursorFallsBackOnUnsupported covers older self-host
// cloud deployments that don't implement direction=desc. A 400 bad_request
// from LatestEventID must transparently fall through to the legacy
// page-walk, not surface as a cycle failure.
func TestResolveLatestEventCursorFallsBackOnUnsupported(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{},
		events: []FilesystemEvent{
			{EventID: "evt_1"}, {EventID: "evt_2"}, {EventID: "evt_3"},
		},
		latestEventIDUnsupported: true,
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_legacy",
		LocalRoot:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	cursor, err := syncer.resolveLatestEventCursor(context.Background())
	if err != nil {
		t.Fatalf("resolveLatestEventCursor failed: %v", err)
	}
	if cursor != "evt_3" {
		t.Fatalf("expected legacy fallback to return latest evt_3, got %q", cursor)
	}
	if client.latestEventIDCalls != 1 {
		t.Fatalf("expected one LatestEventID attempt before fallback, got %d", client.latestEventIDCalls)
	}
	if client.listEventsCalls == 0 {
		t.Fatalf("expected fallback to use ListEvents page-walk, got 0 calls")
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
	if err := syncer.HandleLocalChange(context.Background(), "Docs/New.md", fsnotify.Remove); err != nil {
		t.Fatalf("handle delete failed: %v", err)
	}
	if _, ok := client.files["/notion/Docs/New.md"]; ok {
		t.Fatalf("expected remote file to be deleted")
	}
}

func TestPullRemoteFullTreeSkipsReadFileWhenLocalHashMatchesContentHash(t *testing.T) {
	content := "# A"
	remotePath := "/notion/Docs/A.md"
	client := &fakeClient{
		files: map[string]RemoteFile{
			remotePath: {
				Path:        remotePath,
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     content,
				ContentHash: hashString(content),
			},
		},
		revisionCounter: 1,
	}
	localDir := t.TempDir()
	localPath := filepath.Join(localDir, "Docs", "A.md")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir local dir failed: %v", err)
	}
	if err := os.WriteFile(localPath, []byte(content), 0o644); err != nil {
		t.Fatalf("seed local file failed: %v", err)
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_skip_hash",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.pullRemoteFullTree(context.Background(), nil, bootstrapProgress{}); err != nil {
		t.Fatalf("pull full tree failed: %v", err)
	}

	if got := client.requestedReadCalls(); got != 0 {
		t.Fatalf("expected matching contentHash to skip ReadFile, got %d read(s)", got)
	}
	tracked := syncer.state.Files[remotePath]
	if tracked.Revision != "rev_1" || tracked.Hash != hashString(content) || tracked.Dirty {
		t.Fatalf("unexpected tracked state after skip: %+v", tracked)
	}
	assertLocalFileContent(t, localPath, content)
}

func TestPullRemoteFullGithubWorkingTreeTarSeedsAndStoresCursor(t *testing.T) {
	localDir := t.TempDir()
	contentsRoot := "/github/repos/AgentWorkforce/cloud/contents"
	headSHA := "head123"
	readme := []byte("# Cloud\n")
	app := []byte("export const ok = true;\n")
	readmeRemote := contentsRoot + "/README.md@" + headSHA + ".json"
	appRemote := contentsRoot + "/src/app.ts@" + headSHA + ".json"
	sentinelPath := "/github/repos/AgentWorkforce/cloud/.relayfile/clone.json"
	client := &fakeExportClient{
		fakeClient: &fakeClient{
			files: map[string]RemoteFile{
				sentinelPath: {
					Path:        sentinelPath,
					Revision:    "rev_1",
					ContentType: "application/json",
					Content:     `{"headSha":"` + headSHA + `","defaultBranch":"main"}`,
				},
				readmeRemote: {
					Path:        readmeRemote,
					Revision:    "rev_2",
					ContentType: "application/json",
					Content:     string(readme),
					ContentHash: hashBytes(readme),
				},
				appRemote: {
					Path:        appRemote,
					Revision:    "rev_3",
					ContentType: "application/json",
					Content:     string(app),
					ContentHash: hashBytes(app),
				},
			},
			events: []FilesystemEvent{
				{EventID: "evt_1", Type: "file.created", Path: readmeRemote, Revision: "rev_2", ContentHash: hashBytes(readme)},
				{EventID: "evt_2", Type: "file.updated", Path: sentinelPath, Revision: "rev_1"},
			},
		},
		tarFiles: map[string][]byte{
			"README.md":  readme,
			"src/app.ts": app,
		},
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:   "ws_tar_seed",
		RemoteRoot:    contentsRoot,
		LocalRoot:     localDir,
		StateFile:     filepath.Join(localDir, ".relayfile-mount-state.json"),
		WebSocket:     boolPtr(false),
		FullPullEvery: -1,
	})
	if err != nil {
		t.Fatalf("NewSyncer failed: %v", err)
	}
	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if client.tarCalls != 1 {
		t.Fatalf("expected github tar export to be used once, got %d", client.tarCalls)
	}
	gotReadme, err := os.ReadFile(filepath.Join(localDir, "README.md"))
	if err != nil {
		t.Fatalf("read seeded README: %v", err)
	}
	if !bytes.Equal(gotReadme, readme) {
		t.Fatalf("unexpected README content: %q", string(gotReadme))
	}
	if _, err := os.Stat(filepath.Join(localDir, "README.md@"+headSHA+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected decoded working-tree path without relayfile suffix, stat err=%v", err)
	}
	if syncer.state.EventsCursor != "evt_2" {
		t.Fatalf("expected events cursor at clone sentinel evt_2, got %q", syncer.state.EventsCursor)
	}
	if syncer.state.GithubWorkingTreeHeadSHA != headSHA {
		t.Fatalf("expected head sha persisted, got %q", syncer.state.GithubWorkingTreeHeadSHA)
	}
	if tracked := syncer.state.Files[readmeRemote]; tracked.Hash != hashBytes(readme) || tracked.Revision != "rev_2" {
		t.Fatalf("unexpected tracked README state: %+v", tracked)
	}
	if got := syncer.localRelativeToRemotePath("src/app.ts"); got != appRemote {
		t.Fatalf("local write path should map back to content object with head sha: got %q want %q", got, appRemote)
	}
}

func TestParseGithubCloneManifestAcceptsSnakeCaseCursor(t *testing.T) {
	manifest, ok := parseGithubCloneManifest([]byte(`{
		"head_sha": "head123",
		"default_branch": "main",
		"events_cursor": "evt_cursor",
		"event_id": "evt_id"
	}`))
	if !ok {
		t.Fatalf("expected snake_case clone manifest to parse")
	}
	if manifest.HeadSHA != "head123" {
		t.Fatalf("unexpected head sha %q", manifest.HeadSHA)
	}
	if manifest.EventsCursor != "evt_cursor" {
		t.Fatalf("unexpected events cursor %q", manifest.EventsCursor)
	}
	if manifest.EventID != "evt_id" {
		t.Fatalf("unexpected event id %q", manifest.EventID)
	}
}

func TestGithubWorkingTreeLocalMappingPrefersCurrentHeadSHA(t *testing.T) {
	localDir := t.TempDir()
	contentsRoot := "/github/repos/AgentWorkforce/cloud/contents"
	oldRemote := contentsRoot + "/src/app.ts@oldsha.json"
	newRemote := contentsRoot + "/src/app.ts@newsha.json"
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID:   "ws_head_change",
		RemoteRoot:    contentsRoot,
		LocalRoot:     localDir,
		StateFile:     filepath.Join(localDir, ".relayfile-mount-state.json"),
		WebSocket:     boolPtr(false),
		FullPullEvery: -1,
	})
	if err != nil {
		t.Fatalf("NewSyncer failed: %v", err)
	}
	syncer.githubWorkingTree.HeadSHA = "newsha"
	syncer.state.Files = map[string]trackedFile{
		oldRemote: {Revision: "rev_999", Hash: "old"},
		newRemote: {Revision: "rev_010", Hash: "new"},
	}

	if got := syncer.localRelativeToRemotePath("src/app.ts"); got != newRemote {
		t.Fatalf("local write path should prefer current head object: got %q want %q", got, newRemote)
	}
}

func TestGithubWorkingTreeTarSeedRejectsDuplicateEntries(t *testing.T) {
	localDir := t.TempDir()
	contentsRoot := "/github/repos/AgentWorkforce/cloud/contents"
	headSHA := "head123"
	readme := []byte("# Cloud\n")
	readmeRemote := contentsRoot + "/README.md@" + headSHA + ".json"
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID:   "ws_dup_tar",
		RemoteRoot:    contentsRoot,
		LocalRoot:     localDir,
		StateFile:     filepath.Join(localDir, ".relayfile-mount-state.json"),
		WebSocket:     boolPtr(false),
		FullPullEvery: -1,
	})
	if err != nil {
		t.Fatalf("NewSyncer failed: %v", err)
	}
	syncer.githubWorkingTree.HeadSHA = headSHA
	syncer.state.Files = map[string]trackedFile{}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := 0; i < 2; i++ {
		if err := tw.WriteHeader(&tar.Header{Name: "README.md", Mode: 0o644, Size: int64(len(readme))}); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write(readme); err != nil {
			t.Fatalf("write tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	_, err = syncer.applyGithubWorkingTreeTarSeed(GithubWorkingTreeTar{
		Body:        io.NopCloser(bytes.NewReader(buf.Bytes())),
		ContentType: "application/x-tar",
	}, map[string]githubTreeFile{
		"README.md": {
			RemotePath:  readmeRemote,
			Revision:    "rev_1",
			ContentHash: hashBytes(readme),
		},
	}, nil, bootstrapProgress{})
	if err == nil || !strings.Contains(err.Error(), "duplicate file README.md") {
		t.Fatalf("expected duplicate tar entry error, got %v", err)
	}
}

func TestReconcileBootstrapSkipsMatchingKeptMirrorBeforePushLocal(t *testing.T) {
	content := "# A"
	remotePath := "/notion/Docs/A.md"
	client := &fakeClient{
		files: map[string]RemoteFile{
			remotePath: {
				Path:        remotePath,
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     content,
				ContentHash: hashString(content),
			},
		},
		revisionCounter: 1,
	}
	localDir := t.TempDir()
	localPath := filepath.Join(localDir, "Docs", "A.md")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir local dir failed: %v", err)
	}
	if err := os.WriteFile(localPath, []byte(content), 0o644); err != nil {
		t.Fatalf("seed kept mirror failed: %v", err)
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_kept_mirror",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		WebSocket:   boolPtr(false),
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if got := client.requestedReadCalls(); got != 0 {
		t.Fatalf("expected matching kept mirror to skip ReadFile, got %d read(s)", got)
	}
	if client.bulkWriteCalls != 0 {
		t.Fatalf("expected bootstrap to track kept mirror before pushLocal, got %d bulk write(s)", client.bulkWriteCalls)
	}
	tracked := syncer.state.Files[remotePath]
	if tracked.Revision != "rev_1" || tracked.Hash != hashString(content) || tracked.Dirty {
		t.Fatalf("unexpected tracked state after reconcile: %+v", tracked)
	}
	assertLocalFileContent(t, localPath, content)
}

func TestSyncOnceWritesPublicStateFile(t *testing.T) {
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
	interval := 30 * time.Second
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_public_state",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		Mode:        "poll",
		Interval:    interval,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	state := readPublicState(t, localDir)
	if state.WorkspaceID != "ws_mount_public_state" {
		t.Fatalf("expected workspace id in public state, got %+v", state)
	}
	if state.Status != "ready" {
		t.Fatalf("expected ready status, got %+v", state)
	}
	if state.Mode != "poll" || state.IntervalMs != interval.Milliseconds() {
		t.Fatalf("expected mode/interval in public state, got %+v", state)
	}
	if state.PendingWriteback != 0 || state.PendingConflicts != 0 || state.DeniedPaths != 0 {
		t.Fatalf("expected clean public state, got %+v", state)
	}
	if fileState := state.Files["/notion/Docs/A.md"]; fileState.Status != "ready" {
		t.Fatalf("expected mirrored file state ready, got %+v", fileState)
	}
}

func TestBinaryFileRoundTripsThroughMirror(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{
			"/external/blob.bin": {
				Path:        "/external/blob.bin",
				Revision:    "rev_1",
				ContentType: "application/octet-stream",
				Content:     base64.StdEncoding.EncodeToString([]byte{0x00, 0x7f, 0xff, 0x10}),
				Encoding:    "base64",
			},
		},
		revisionCounter: 1,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_binary_roundtrip",
		RemoteRoot:  "/external",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial binary sync failed: %v", err)
	}

	localPath := filepath.Join(localDir, "blob.bin")
	assertLocalFileBytes(t, localPath, []byte{0x00, 0x7f, 0xff, 0x10})

	updated := []byte{0x01, 0x02, 0x03, 0x04}
	if err := os.WriteFile(localPath, updated, 0o644); err != nil {
		t.Fatalf("write binary local edit failed: %v", err)
	}
	if err := syncer.HandleLocalChange(context.Background(), "blob.bin", fsnotify.Write); err != nil {
		t.Fatalf("binary local change failed: %v", err)
	}

	remote := client.files["/external/blob.bin"]
	if remote.Encoding != "base64" {
		t.Fatalf("expected remote encoding=base64, got %+v", remote)
	}
	decoded, err := base64.StdEncoding.DecodeString(remote.Content)
	if err != nil {
		t.Fatalf("decode remote base64 failed: %v", err)
	}
	if !bytes.Equal(decoded, updated) {
		t.Fatalf("expected remote bytes %v, got %v", updated, decoded)
	}
	state := readPublicState(t, localDir)
	if state.Files["/external/blob.bin"].Encoding != "base64" {
		t.Fatalf("expected public state to track base64 encoding, got %+v", state.Files["/external/blob.bin"])
	}
}

func TestLargeWriteFailureLeavesPendingWritebackVisible(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{},
		bulkWriteResponseFunc: func(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
			return BulkWriteResponse{}, &HTTPError{
				StatusCode: http.StatusRequestEntityTooLarge,
				Code:       "payload_too_large",
				Message:    "payload too large",
			}
		},
	}
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "Large.md"), []byte(strings.Repeat("A", 1024)), 0o644); err != nil {
		t.Fatalf("seed large local file failed: %v", err)
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_large_write_failure",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		Mode:        "poll",
		Interval:    30 * time.Second,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected large write sync to fail")
	}

	assertLocalFileContent(t, filepath.Join(localDir, "Large.md"), strings.Repeat("A", 1024))
	state := readPublicState(t, localDir)
	if state.Status != "writeback-pending" {
		t.Fatalf("expected writeback-pending state, got %+v", state)
	}
	if state.PendingWriteback != 1 {
		t.Fatalf("expected one pending writeback, got %+v", state)
	}
	if state.LastError == nil || state.LastError.StatusCode != http.StatusRequestEntityTooLarge || state.LastError.Code != "payload_too_large" {
		t.Fatalf("expected 413 surfaced in public state, got %+v", state.LastError)
	}
}

func TestBulkWrite_SingleCallForNFiles(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{},
	}
	localDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localDir, "Docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs failed: %v", err)
	}
	expectedPaths := make([]string, 0, 5)
	for idx := 0; idx < 5; idx++ {
		name := fmt.Sprintf("File%d.md", idx+1)
		path := filepath.Join(localDir, "Docs", name)
		if err := os.WriteFile(path, []byte(fmt.Sprintf("# %d", idx+1)), 0o644); err != nil {
			t.Fatalf("seed local file %s failed: %v", name, err)
		}
		expectedPaths = append(expectedPaths, "/notion/Docs/"+name)
	}

	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_bulk_single_call",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync once failed: %v", err)
	}

	if client.bulkWriteCalls != 1 {
		t.Fatalf("expected one bulk write call, got %d", client.bulkWriteCalls)
	}
	if client.writeFileCalls != 0 {
		t.Fatalf("expected zero per-file write calls, got %d", client.writeFileCalls)
	}
	if got := len(client.bulkWriteBatches); got != 1 {
		t.Fatalf("expected one recorded bulk batch, got %d", got)
	}
	if got := len(client.bulkWriteBatches[0]); got != 5 {
		t.Fatalf("expected five files in bulk batch, got %d", got)
	}
	gotPaths := make([]string, 0, len(client.bulkWriteBatches[0]))
	for _, file := range client.bulkWriteBatches[0] {
		gotPaths = append(gotPaths, normalizeRemotePath(file.Path))
	}
	sort.Strings(gotPaths)
	if strings.Join(gotPaths, ",") != strings.Join(expectedPaths, ",") {
		t.Fatalf("expected bulk paths %v, got %v", expectedPaths, gotPaths)
	}
}

func TestBulkWrite_ContentIdentityGoldenVector(t *testing.T) {
	const (
		workspaceID = "ws_test"
		remotePath  = "/slack/channels/C123/messages/messages 5ab77d67.json"
		content     = "{\"channel\":\"C123\",\"text\":\"hello writeback idempotency\"}\n"
		contentHash = "751f9591557700f69b5ceefcdec7ead8563a10f0a712c501a5028699be021511"
		key         = "ws_test:/slack/channels/C123/messages/messages 5ab77d67.json:751f9591557700f69b5ceefcdec7ead8563a10f0a712c501a5028699be021511"
	)

	snapshot := newLocalSnapshot(remotePath, []byte(content))
	if snapshot.Hash != contentHash {
		t.Fatalf("golden vector hash = %q, want %q", snapshot.Hash, contentHash)
	}
	identity := newMountWritebackCreateDraftContentIdentity(workspaceID, remotePath, snapshot.Hash)
	if identity.Kind != mountWritebackCreateDraftContentIdentityKind {
		t.Fatalf("content identity kind = %q, want %q", identity.Kind, mountWritebackCreateDraftContentIdentityKind)
	}
	if identity.Key != key {
		t.Fatalf("content identity key = %q, want %q", identity.Key, key)
	}
	if identity.TTLSeconds != mountWritebackCreateDraftContentIdentityTTLSeconds {
		t.Fatalf("content identity ttl = %d, want %d", identity.TTLSeconds, mountWritebackCreateDraftContentIdentityTTLSeconds)
	}
	if identity.TTLSeconds != 2592000 {
		t.Fatalf("golden vector ttl = %d, want 2592000", identity.TTLSeconds)
	}
	if strings.TrimSpace(identity.Key) != identity.Key {
		t.Fatalf("golden vector key should be trim-stable: %q", identity.Key)
	}
	if !strings.Contains(identity.Key, "messages 5ab77d67.json") {
		t.Fatalf("golden vector key should preserve the internal path space: %q", identity.Key)
	}
}

func TestBulkWrite_ContentIdentityStabilityAndIsolation(t *testing.T) {
	const (
		workspaceID = "ws_test"
		remotePath  = "/slack/channels/C123/messages/messages 5ab77d67-1111-4111-8111-123456789abc.json"
	)

	snapshot := newLocalSnapshot(remotePath, []byte("{\"text\":\"same\"}\n"))
	files := bulkWriteFilesForPending(workspaceID, []pendingBulkWrite{{
		remotePath: remotePath,
		snapshot:   snapshot,
	}})
	reupload := bulkWriteFilesForPending(workspaceID, []pendingBulkWrite{{
		remotePath: remotePath,
		snapshot:   snapshot,
	}})
	if files[0].ContentIdentity == nil || reupload[0].ContentIdentity == nil {
		t.Fatal("expected content identity on both bulk files")
	}
	if files[0].ContentIdentity.Key != reupload[0].ContentIdentity.Key {
		t.Fatalf("same draft re-upload key changed: %q vs %q", files[0].ContentIdentity.Key, reupload[0].ContentIdentity.Key)
	}

	edited := bulkWriteFilesForPending(workspaceID, []pendingBulkWrite{{
		remotePath: remotePath,
		snapshot:   newLocalSnapshot(remotePath, []byte("{\"text\":\"edited\"}\n")),
	}})
	if edited[0].ContentIdentity.Key == files[0].ContentIdentity.Key {
		t.Fatalf("edited draft content should change key %q", edited[0].ContentIdentity.Key)
	}

	otherPath := bulkWriteFilesForPending(workspaceID, []pendingBulkWrite{{
		remotePath: "/slack/channels/C123/messages/messages 6ab77d67-2222-4222-8222-123456789abc.json",
		snapshot:   snapshot,
	}})
	if otherPath[0].ContentIdentity.Key == files[0].ContentIdentity.Key {
		t.Fatalf("different draft path should change key %q", otherPath[0].ContentIdentity.Key)
	}

	otherWorkspace := bulkWriteFilesForPending("ws_other", []pendingBulkWrite{{
		remotePath: remotePath,
		snapshot:   snapshot,
	}})
	if otherWorkspace[0].ContentIdentity.Key == files[0].ContentIdentity.Key {
		t.Fatalf("different workspace should change key %q", otherWorkspace[0].ContentIdentity.Key)
	}
}

func TestBulkWrite_ContentIdentityOnlyForCreateDraftPaths(t *testing.T) {
	const workspaceID = "ws_test"
	snapshot := newLocalSnapshot("/notion/pages/pages 5ab77d67-1111-4111-8111-123456789abc.json", []byte("{}\n"))

	draft := bulkWriteFilesForPending(workspaceID, []pendingBulkWrite{{
		remotePath: "/notion/pages/pages 5ab77d67-1111-4111-8111-123456789abc.json",
		snapshot:   snapshot,
	}})
	if draft[0].ContentIdentity == nil {
		t.Fatal("expected content identity for space-uuid create draft path")
	}

	factoryCreate := bulkWriteFilesForPending(workspaceID, []pendingBulkWrite{{
		remotePath: "/linear/issues/factory-create-ar-272-test.json",
		snapshot:   snapshot,
	}})
	if factoryCreate[0].ContentIdentity == nil {
		t.Fatal("expected content identity for factory-create writeback path")
	}

	stable := bulkWriteFilesForPending(workspaceID, []pendingBulkWrite{{
		remotePath: "/notion/pages/page-1.md",
		snapshot:   snapshot,
	}})
	if stable[0].ContentIdentity != nil {
		t.Fatalf("stable non-draft path should not carry content identity: %+v", stable[0].ContentIdentity)
	}

	nonUUID := bulkWriteFilesForPending(workspaceID, []pendingBulkWrite{{
		remotePath: "/slack/channels/C123/messages/messages not-a-uuid.json",
		snapshot:   snapshot,
	}})
	if nonUUID[0].ContentIdentity != nil {
		t.Fatalf("non-uuid draft-like path should not carry content identity: %+v", nonUUID[0].ContentIdentity)
	}
}

func TestBulkWrite_ContentIdentityOmittedForStablePathRevert(t *testing.T) {
	const (
		workspaceID = "ws_test"
		remotePath  = "/notion/pages/page-1.md"
	)

	for _, content := range []string{"# C\n", "# D\n", "# C\n"} {
		files := bulkWriteFilesForPending(workspaceID, []pendingBulkWrite{{
			remotePath: remotePath,
			snapshot:   newLocalSnapshot(remotePath, []byte(content)),
		}})
		if files[0].ContentIdentity != nil {
			t.Fatalf("stable path content %q should not carry content identity: %+v", content, files[0].ContentIdentity)
		}
	}
}

func TestBulkWrite_FlushSendsContentIdentity(t *testing.T) {
	const (
		workspaceID = "ws_test"
		content     = "{\"channel\":\"C123\",\"text\":\"hello writeback idempotency\"}\n"
		draftID     = "5ab77d67-1111-4111-8111-123456789abc"
	)

	client := &fakeClient{files: map[string]RemoteFile{}}
	localDir := t.TempDir()
	messageDir := filepath.Join(localDir, "slack", "channels", "C123", "messages")
	if err := os.MkdirAll(messageDir, 0o755); err != nil {
		t.Fatalf("mkdir message dir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(messageDir, "messages "+draftID+".json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write draft failed: %v", err)
	}

	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync once failed: %v", err)
	}
	if got := len(client.bulkWriteBatches); got != 1 {
		t.Fatalf("expected one bulk write batch, got %d", got)
	}
	if got := len(client.bulkWriteBatches[0]); got != 1 {
		t.Fatalf("expected one bulk write file, got %d", got)
	}
	identity := client.bulkWriteBatches[0][0].ContentIdentity
	if identity == nil {
		t.Fatal("expected flushed bulk file to carry content identity")
	}
	acked := readOutboxRecordsInDirForTest(t, filepath.Join(localDir, ".relay", "outbox", "acked"))
	if len(acked) != 1 {
		t.Fatalf("expected one acked outbox record, got %+v", acked)
	}
	// A draft-create flush must dedupe on the create-draft identity
	// (workspace:path:hash) — the same key the in-flight pending path and the
	// CLI's direct `writeback push` use — so a daemon flush of a pending push
	// receipt collides with the direct push instead of minting a second
	// idempotency key (which would create duplicate provider drafts).
	remotePath := acked[0].RemotePath
	want := newMountWritebackCreateDraftContentIdentity(workspaceID, remotePath, acked[0].Hash)
	if identity.Kind != mountWritebackCreateDraftContentIdentityKind {
		t.Fatalf("flushed identity kind = %q, want %q", identity.Kind, mountWritebackCreateDraftContentIdentityKind)
	}
	if identity.Key != want.Key {
		t.Fatalf("flushed identity key = %q, want create-draft key %q", identity.Key, want.Key)
	}
	if identity.TTLSeconds != mountWritebackCreateDraftContentIdentityTTLSeconds {
		t.Fatalf("flushed identity ttl = %d, want %d", identity.TTLSeconds, mountWritebackCreateDraftContentIdentityTTLSeconds)
	}
}

func TestOutboxPersistsBeforeBulkWriteFailure(t *testing.T) {
	localDir := t.TempDir()
	client := &fakeClient{
		files: map[string]RemoteFile{},
		bulkWriteResponseFunc: func(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
			_ = ctx
			_ = workspaceID
			_ = files
			records := readPendingOutboxRecordsForTest(t, localDir)
			if len(records) != 1 {
				t.Fatalf("expected outbox record persisted before upload, got %d", len(records))
			}
			if records[0].CommandID == "" || records[0].CorrelationID != records[0].CommandID {
				t.Fatalf("expected stable command/correlation id before upload, got %+v", records[0])
			}
			return BulkWriteResponse{}, context.Canceled
		},
	}
	if err := os.WriteFile(filepath.Join(localDir, "command.json"), []byte(`{"text":"hello"}`), 0o644); err != nil {
		t.Fatalf("seed local command failed: %v", err)
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_outbox_persist_first",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected canceled upload to fail")
	}
	records := readPendingOutboxRecordsForTest(t, localDir)
	if len(records) != 1 || records[0].AttemptCount != 1 {
		t.Fatalf("expected one attempted pending outbox record, got %+v", records)
	}
}

// TestOutboxFlushUsesIndependentDeadlineNotPerCycleCtx proves the fix for the
// churn-digest "context deadline exceeded" / minutes-late-reply failure: the
// durable writeback upload must run under its OWN rootCtx-derived deadline
// (outboxContext), so a tiny per-cycle RELAYFILE_MOUNT_TIMEOUT — or even an
// already-expired inbound ctx — cannot starve or cancel it. Without the fix the
// inbound (expired) ctx flows straight into WriteFilesBulk and the flush fails.
func TestOutboxFlushUsesIndependentDeadlineNotPerCycleCtx(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "command.json"), []byte(`{"text":"hello"}`), 0o644); err != nil {
		t.Fatalf("seed local command failed: %v", err)
	}

	// First pass: persist a pending outbox record via a canceled upload.
	firstClient := &fakeClient{
		files: map[string]RemoteFile{},
		bulkWriteResponseFunc: func(ctx context.Context, _ string, _ []BulkWriteFile) (BulkWriteResponse, error) {
			_ = ctx
			return BulkWriteResponse{}, context.Canceled
		},
	}
	first, err := NewSyncer(firstClient, SyncerOptions{
		WorkspaceID: "ws_outbox_deadline",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer first: %v", err)
	}
	if err := first.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected first upload to fail")
	}
	forcePendingOutboxDueForTest(t, localDir)

	// Second pass: hand the flush an ALREADY-EXPIRED per-cycle ctx. The upload
	// must still run, under the rootCtx-derived outbox deadline (30s here).
	var sawRemaining time.Duration
	secondClient := &fakeClient{
		files: map[string]RemoteFile{},
		bulkWriteResponseFunc: func(ctx context.Context, _ string, _ []BulkWriteFile) (BulkWriteResponse, error) {
			if dl, ok := ctx.Deadline(); !ok {
				t.Error("expected outbox upload ctx to carry a deadline")
			} else {
				sawRemaining = time.Until(dl)
			}
			// A per-cycle-sized expiry must not cancel the upload mid-flight.
			time.Sleep(40 * time.Millisecond)
			if ctx.Err() != nil {
				t.Errorf("outbox upload ctx cancelled mid-flight: %v", ctx.Err())
			}
			return BulkWriteResponse{}, nil
		},
	}
	second, err := NewSyncer(secondClient, SyncerOptions{
		WorkspaceID:        "ws_outbox_deadline",
		RemoteRoot:         "/slack/channels/C123/messages",
		LocalRoot:          localDir,
		RootCtx:            context.Background(),
		OutboxFlushTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewSyncer second: %v", err)
	}

	tinyCtx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	time.Sleep(20 * time.Millisecond) // the inbound per-cycle ctx is now expired
	if tinyCtx.Err() == nil {
		t.Fatal("precondition: expected inbound per-cycle ctx to be already expired")
	}

	if err := second.flushDueOutboxRecords(tinyCtx, nil); err != nil {
		t.Fatalf("outbox flush should survive an expired per-cycle ctx, got: %v", err)
	}
	if secondClient.bulkWriteCalls != 1 {
		t.Fatalf("expected exactly one upload, got %d", secondClient.bulkWriteCalls)
	}
	if sawRemaining < 5*time.Second {
		t.Fatalf("expected upload to run under the ~30s outbox deadline, saw %s remaining (looks like the expired per-cycle ctx leaked through)", sawRemaining)
	}
	if pending := readPendingOutboxRecordsForTest(t, localDir); len(pending) != 0 {
		t.Fatalf("expected pending outbox empty after ack, got %+v", pending)
	}
}

func TestOutboxRecordContentIdentityDraftMatchesDirectPush(t *testing.T) {
	const (
		workspaceID = "ws_test"
		contentHash = "751f9591557700f69b5ceefcdec7ead8563a10f0a712c501a5028699be021511"
		draftPath   = "/slack/channels/C123/messages/messages 5ab77d67-1111-2222-3333-444455556666.json"
	)

	// A draft "create" outbox record must dedupe on the create-draft identity
	// (workspace:path:hash) so a mount-daemon flush of the CLI's pending receipt
	// collides with the CLI's direct `writeback push`, not on the per-record
	// commandId. Otherwise the server sees two idempotency keys for identical
	// content and can mint duplicate provider drafts.
	if !isMountWritebackCreateDraftPath(draftPath) {
		t.Fatalf("expected %q to be recognized as a create-draft path", draftPath)
	}
	draftRecord := outboxRecord{
		CommandID:   "mountcmd_should_not_be_used",
		WorkspaceID: workspaceID,
		RemotePath:  draftPath,
		Hash:        contentHash,
	}
	got := outboxRecordContentIdentity(draftRecord)
	want := newMountWritebackCreateDraftContentIdentity(workspaceID, draftPath, contentHash)
	if got == nil || got.Kind != want.Kind || got.Key != want.Key || got.TTLSeconds != want.TTLSeconds {
		t.Fatalf("draft record identity = %+v, want %+v", got, want)
	}

	// Non-draft mount commands keep the stable commandId identity.
	plainRecord := outboxRecord{
		CommandID:   "mountcmd_abc123",
		WorkspaceID: workspaceID,
		RemotePath:  "/slack/channels/C123/messages/command.json",
		Hash:        contentHash,
	}
	plain := outboxRecordContentIdentity(plainRecord)
	if plain == nil || plain.Kind != "mount-command" || plain.Key != plainRecord.CommandID {
		t.Fatalf("plain record identity = %+v, want mount-command keyed by commandId", plain)
	}
}

func TestOutboxRestartReloadsPendingAndFlushesWithPersistedCommandID(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "command.json"), []byte(`{"text":"hello"}`), 0o644); err != nil {
		t.Fatalf("seed local command failed: %v", err)
	}
	firstClient := &fakeClient{
		files: map[string]RemoteFile{},
		bulkWriteResponseFunc: func(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
			_ = ctx
			_ = workspaceID
			_ = files
			return BulkWriteResponse{}, context.Canceled
		},
	}
	first, err := NewSyncer(firstClient, SyncerOptions{
		WorkspaceID: "ws_outbox_restart",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer first: %v", err)
	}
	if err := first.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected first upload to fail")
	}
	record := forcePendingOutboxDueForTest(t, localDir)
	commandID := record.CommandID

	secondClient := &fakeClient{files: map[string]RemoteFile{}}
	second, err := NewSyncer(secondClient, SyncerOptions{
		WorkspaceID: "ws_outbox_restart",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer second: %v", err)
	}
	if err := second.SyncOnce(context.Background()); err != nil {
		t.Fatalf("restart flush failed: %v", err)
	}
	if secondClient.bulkWriteCalls != 1 {
		t.Fatalf("expected restart to flush pending outbox once, got %d", secondClient.bulkWriteCalls)
	}
	gotIdentity := secondClient.bulkWriteBatches[0][0].ContentIdentity
	if gotIdentity == nil || gotIdentity.Key != commandID || gotIdentity.Kind != "mount-command" {
		t.Fatalf("expected persisted command id in retry contentIdentity, got %+v (want %s)", gotIdentity, commandID)
	}
	if pending := readPendingOutboxRecordsForTest(t, localDir); len(pending) != 0 {
		t.Fatalf("expected pending outbox empty after ack, got %+v", pending)
	}
	acked := readOutboxRecordsInDirForTest(t, filepath.Join(localDir, ".relay", "outbox", "acked"))
	if len(acked) != 1 || acked[0].CommandID != commandID {
		t.Fatalf("expected acked outbox record for %s, got %+v", commandID, acked)
	}
}

func TestOutboxRetryAfterServerCommitUsesSameCommandIDAndDedupes(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "command.json"), []byte(`{"text":"hello"}`), 0o644); err != nil {
		t.Fatalf("seed local command failed: %v", err)
	}
	var seenIdentity string
	var committedEvents int
	client := &fakeClient{
		files:                          map[string]RemoteFile{},
		bulkWriteResponseFuncOwnsWrite: true,
	}
	client.bulkWriteResponseFunc = func(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
		_ = ctx
		_ = workspaceID
		if len(files) != 1 || files[0].ContentIdentity == nil {
			t.Fatalf("expected one content-identified write, got %+v", files)
		}
		path := normalizeRemotePath(files[0].Path)
		if seenIdentity == "" {
			seenIdentity = files[0].ContentIdentity.Key
			client.revisionCounter++
			revision := fmt.Sprintf("rev_%d", client.revisionCounter)
			client.files[path] = RemoteFile{
				Path:        path,
				Revision:    revision,
				ContentType: files[0].ContentType,
				Content:     files[0].Content,
			}
			client.appendEvent("file.created", path, revision)
			committedEvents++
			return BulkWriteResponse{}, context.Canceled
		}
		if files[0].ContentIdentity.Key != seenIdentity {
			t.Fatalf("retry minted a new command id: got %s want %s", files[0].ContentIdentity.Key, seenIdentity)
		}
		remote := client.files[path]
		return BulkWriteResponse{
			Written:       0,
			ErrorCount:    0,
			Errors:        []BulkWriteError{},
			Results:       []BulkWriteResult{{Path: remote.Path, Revision: remote.Revision, ContentType: remote.ContentType}},
			CorrelationID: "corr_deduped",
		}, nil
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_outbox_commit_lost_response",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected first lost response to fail")
	}
	forcePendingOutboxDueForTest(t, localDir)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("retry after lost response failed: %v", err)
	}
	if client.bulkWriteCalls != 2 {
		t.Fatalf("expected exactly two upload attempts, got %d", client.bulkWriteCalls)
	}
	if committedEvents != 1 || len(client.events) != 1 {
		t.Fatalf("expected one provider-facing mutation after deduped retry, committed=%d events=%d", committedEvents, len(client.events))
	}
	if pending := readPendingOutboxRecordsForTest(t, localDir); len(pending) != 0 {
		t.Fatalf("expected outbox acked after deduped retry, got %+v", pending)
	}
}

func TestOutboxAcksOnlyAfterWritebackOperationSucceeded(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "command.json"), []byte(`{"text":"hello"}`), 0o644); err != nil {
		t.Fatalf("seed local command failed: %v", err)
	}
	client := &fakeClient{
		files:      map[string]RemoteFile{},
		operations: map[string]OperationStatus{},
	}
	client.bulkWriteResponseFunc = func(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
		_ = ctx
		_ = workspaceID
		if len(files) != 1 {
			t.Fatalf("expected one outbox file, got %+v", files)
		}
		path := normalizeRemotePath(files[0].Path)
		client.revisionCounter++
		revision := fmt.Sprintf("rev_%d", client.revisionCounter)
		client.files[path] = RemoteFile{Path: path, Revision: revision, ContentType: files[0].ContentType, Content: files[0].Content}
		opID := "op_dispatch_receipt_1"
		client.operations[opID] = OperationStatus{
			OpID:     opID,
			Path:     path,
			Revision: revision,
			Provider: "slack",
			Status:   "running",
		}
		return BulkWriteResponse{
			Written: 1,
			Results: []BulkWriteResult{{
				Path:        path,
				Revision:    revision,
				ContentType: files[0].ContentType,
				OpID:        opID,
				Writeback:   &relayfile.BulkWriteWritebackResult{Provider: "slack", State: "pending"},
			}},
			CorrelationID: "corr_receipt_pending",
		}, nil
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_outbox_dispatch_receipt",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync with running op should keep pending without failing: %v", err)
	}
	pending := readPendingOutboxRecordsForTest(t, localDir)
	if len(pending) != 1 || pending[0].OpID != "op_dispatch_receipt_1" || pending[0].DispatchStatus != "running" {
		t.Fatalf("expected pending outbox with running op id, got %+v", pending)
	}
	if acked := readOutboxRecordsInDirForTest(t, filepath.Join(localDir, ".relay", "outbox", "acked")); len(acked) != 0 {
		t.Fatalf("expected no acked record before op succeeded, got %+v", acked)
	}

	client.operations["op_dispatch_receipt_1"] = OperationStatus{
		OpID:     "op_dispatch_receipt_1",
		Path:     "/slack/channels/C123/messages/command.json",
		Revision: pending[0].Revision,
		Provider: "slack",
		Status:   "succeeded",
	}
	forcePendingOutboxDueForTest(t, localDir)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync after op success failed: %v", err)
	}
	if client.bulkWriteCalls != 1 {
		t.Fatalf("expected success polling to avoid re-upload, got %d bulk calls", client.bulkWriteCalls)
	}
	if pending := readPendingOutboxRecordsForTest(t, localDir); len(pending) != 0 {
		t.Fatalf("expected pending empty after op success, got %+v", pending)
	}
	acked := readOutboxRecordsInDirForTest(t, filepath.Join(localDir, ".relay", "outbox", "acked"))
	if len(acked) != 1 || acked[0].OpID != "op_dispatch_receipt_1" || acked[0].DispatchStatus != "succeeded" {
		t.Fatalf("expected acked succeeded receipt, got %+v", acked)
	}
}

func TestOutboxRestartDuringInFlightOperationResumesPolling(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "command.json"), []byte(`{"text":"hello"}`), 0o644); err != nil {
		t.Fatalf("seed local command failed: %v", err)
	}
	const opID = "op_restart_in_flight_1"
	firstClient := &fakeClient{
		files:      map[string]RemoteFile{},
		operations: map[string]OperationStatus{},
	}
	firstClient.bulkWriteResponseFunc = func(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
		_ = ctx
		_ = workspaceID
		if len(files) != 1 {
			t.Fatalf("expected one outbox file, got %+v", files)
		}
		path := normalizeRemotePath(files[0].Path)
		firstClient.revisionCounter++
		revision := fmt.Sprintf("rev_%d", firstClient.revisionCounter)
		firstClient.files[path] = RemoteFile{Path: path, Revision: revision, ContentType: files[0].ContentType, Content: files[0].Content}
		firstClient.operations[opID] = OperationStatus{
			OpID:     opID,
			Path:     path,
			Revision: revision,
			Provider: "slack",
			Status:   "running",
		}
		return BulkWriteResponse{
			Written: 1,
			Results: []BulkWriteResult{{
				Path:        path,
				Revision:    revision,
				ContentType: files[0].ContentType,
				OpID:        opID,
				Writeback:   &relayfile.BulkWriteWritebackResult{Provider: "slack", State: "pending"},
			}},
			CorrelationID: "corr_restart_in_flight",
		}, nil
	}
	first, err := NewSyncer(firstClient, SyncerOptions{
		WorkspaceID: "ws_outbox_restart_in_flight",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer first: %v", err)
	}
	// Command accepted by the cloud, writeback op still in flight: the record
	// stays pending with the op id persisted, and the syncer "dies" here.
	if err := first.SyncOnce(context.Background()); err != nil {
		t.Fatalf("first sync with in-flight op failed: %v", err)
	}
	pending := readPendingOutboxRecordsForTest(t, localDir)
	if len(pending) != 1 || pending[0].OpID != opID || pending[0].DispatchStatus != "running" {
		t.Fatalf("expected one pending record carrying in-flight op id, got %+v", pending)
	}
	commandID := pending[0].CommandID
	remotePath := pending[0].RemotePath

	// Restart with a fresh syncer and client; meanwhile the op has succeeded
	// server-side.
	secondClient := &fakeClient{
		files: map[string]RemoteFile{
			remotePath: firstClient.files[remotePath],
		},
		operations: map[string]OperationStatus{
			opID: {
				OpID:     opID,
				Path:     remotePath,
				Revision: pending[0].Revision,
				Provider: "slack",
				Status:   "succeeded",
			},
		},
	}
	second, err := NewSyncer(secondClient, SyncerOptions{
		WorkspaceID: "ws_outbox_restart_in_flight",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer second: %v", err)
	}
	forcePendingOutboxDueForTest(t, localDir)
	if err := second.SyncOnce(context.Background()); err != nil {
		t.Fatalf("restart sync failed: %v", err)
	}
	if secondClient.bulkWriteCalls != 0 {
		t.Fatalf("expected restart to resume polling without re-uploading, got %d bulk calls", secondClient.bulkWriteCalls)
	}
	if secondClient.getOperationCalls == 0 {
		t.Fatal("expected restart to poll the in-flight operation")
	}
	if remaining := readPendingOutboxRecordsForTest(t, localDir); len(remaining) != 0 {
		t.Fatalf("expected pending outbox empty after op success, got %+v", remaining)
	}
	acked := readOutboxRecordsInDirForTest(t, filepath.Join(localDir, ".relay", "outbox", "acked"))
	if len(acked) != 1 || acked[0].CommandID != commandID || acked[0].OpID != opID || acked[0].DispatchStatus != "succeeded" {
		t.Fatalf("expected exactly one acked record for %s, got %+v", commandID, acked)
	}

	// A further cycle must not re-dispatch or ack a second time.
	if err := second.SyncOnce(context.Background()); err != nil {
		t.Fatalf("steady-state sync after ack failed: %v", err)
	}
	if secondClient.bulkWriteCalls != 0 {
		t.Fatalf("expected no upload after ack, got %d bulk calls", secondClient.bulkWriteCalls)
	}
	acked = readOutboxRecordsInDirForTest(t, filepath.Join(localDir, ".relay", "outbox", "acked"))
	if len(acked) != 1 || acked[0].CommandID != commandID {
		t.Fatalf("expected outbox entry acked exactly once, got %+v", acked)
	}
}

func TestOutboxLostBulkResponseRetryRecoversOpIDWithoutDuplicateDispatch(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "command.json"), []byte(`{"text":"hello"}`), 0o644); err != nil {
		t.Fatalf("seed local command failed: %v", err)
	}
	client := &fakeClient{
		files:                          map[string]RemoteFile{},
		operations:                     map[string]OperationStatus{},
		bulkWriteResponseFuncOwnsWrite: true,
	}
	var seenIdentity string
	var providerDispatches int
	client.bulkWriteResponseFunc = func(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
		_ = ctx
		_ = workspaceID
		if len(files) != 1 || files[0].ContentIdentity == nil {
			t.Fatalf("expected one content-identified write, got %+v", files)
		}
		path := normalizeRemotePath(files[0].Path)
		opID := "op_lost_response_1"
		if seenIdentity == "" {
			seenIdentity = files[0].ContentIdentity.Key
			client.revisionCounter++
			revision := fmt.Sprintf("rev_%d", client.revisionCounter)
			client.files[path] = RemoteFile{Path: path, Revision: revision, ContentType: files[0].ContentType, Content: files[0].Content}
			client.operations[opID] = OperationStatus{OpID: opID, Path: path, Revision: revision, Provider: "slack", Status: "succeeded"}
			providerDispatches++
			return BulkWriteResponse{}, context.Canceled
		}
		if files[0].ContentIdentity.Key != seenIdentity {
			t.Fatalf("retry minted new content identity: got %s want %s", files[0].ContentIdentity.Key, seenIdentity)
		}
		remote := client.files[path]
		return BulkWriteResponse{
			Written: 0,
			Results: []BulkWriteResult{{
				Path:            remote.Path,
				Revision:        remote.Revision,
				ContentType:     remote.ContentType,
				OpID:            opID,
				ContentIdentity: files[0].ContentIdentity,
				Writeback:       &relayfile.BulkWriteWritebackResult{Provider: "slack", State: "succeeded"},
			}},
			CorrelationID: "corr_lost_response_recovered",
		}, nil
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_outbox_lost_response_receipt",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected first lost response to fail")
	}
	forcePendingOutboxDueForTest(t, localDir)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("retry after lost response failed: %v", err)
	}
	if client.bulkWriteCalls != 2 {
		t.Fatalf("expected exactly two upload attempts, got %d", client.bulkWriteCalls)
	}
	if providerDispatches != 1 {
		t.Fatalf("expected one provider dispatch after deduped retry, got %d", providerDispatches)
	}
	if pending := readPendingOutboxRecordsForTest(t, localDir); len(pending) != 0 {
		t.Fatalf("expected pending outbox empty after recovered receipt, got %+v", pending)
	}
	acked := readOutboxRecordsInDirForTest(t, filepath.Join(localDir, ".relay", "outbox", "acked"))
	if len(acked) != 1 || acked[0].OpID != "op_lost_response_1" || acked[0].DispatchStatus != "succeeded" {
		t.Fatalf("expected acked receipt for recovered op, got %+v", acked)
	}
}

func TestOutboxRetryCapSurfacesNeedsAttention(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "command.json"), []byte(`{"text":"hello"}`), 0o644); err != nil {
		t.Fatalf("seed local command failed: %v", err)
	}
	client := &fakeClient{
		files: map[string]RemoteFile{},
		bulkWriteResponseFunc: func(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
			_ = ctx
			_ = workspaceID
			_ = files
			return BulkWriteResponse{}, context.Canceled
		},
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_outbox_retry_cap",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
		Mode:        "poll",
		Interval:    30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	syncer.maxOutboxAttempts = 2
	if err := syncer.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected first upload to fail")
	}
	forcePendingOutboxDueForTest(t, localDir)
	if err := syncer.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected second upload to fail")
	}
	state := readPublicState(t, localDir)
	if state.Status != "writeback-needs-attention" || !state.States.OutboxNeedsAttention || state.Outbox.NeedsAttention != 1 {
		t.Fatalf("expected needs-attention public state, got %+v", state)
	}
	calls := client.bulkWriteCalls
	forcePendingOutboxDueForTest(t, localDir)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("needs-attention steady-state sync should not retry: %v", err)
	}
	if client.bulkWriteCalls != calls {
		t.Fatalf("expected retry cap to stop further uploads, got %d -> %d", calls, client.bulkWriteCalls)
	}
}

func TestFlushOutboxOnceFlushesPendingWithoutMirrorScan(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "command.json"), []byte(`{"text":"hello"}`), 0o644); err != nil {
		t.Fatalf("seed local command failed: %v", err)
	}
	firstClient := &fakeClient{
		files: map[string]RemoteFile{},
		bulkWriteResponseFunc: func(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
			_ = ctx
			_ = workspaceID
			_ = files
			return BulkWriteResponse{}, context.Canceled
		},
	}
	first, err := NewSyncer(firstClient, SyncerOptions{
		WorkspaceID: "ws_flush_outbox_once",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer first: %v", err)
	}
	if err := first.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected first upload to fail")
	}
	record := readPendingOutboxRecordsForTest(t, localDir)[0]
	if strings.TrimSpace(record.NextAttemptAt) == "" {
		t.Fatalf("expected retry backoff to be set on pending record: %+v", record)
	}
	if err := os.WriteFile(filepath.Join(localDir, "untracked-local.txt"), []byte("do not scan me"), 0o644); err != nil {
		t.Fatalf("seed untracked file failed: %v", err)
	}

	baseClient := &fakeClient{files: map[string]RemoteFile{}}
	secondClient := &fakeExportClient{fakeClient: baseClient}
	second, err := NewSyncer(secondClient, SyncerOptions{
		WorkspaceID: "ws_flush_outbox_once",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
		Interval:    30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewSyncer second: %v", err)
	}
	if err := second.FlushOutboxOnce(context.Background()); err != nil {
		t.Fatalf("FlushOutboxOnce failed: %v", err)
	}
	if baseClient.bulkWriteCalls != 1 {
		t.Fatalf("expected one outbox bulk upload, got %d", baseClient.bulkWriteCalls)
	}
	if secondClient.exportCalls != 0 || baseClient.listTreeCalls != 0 || baseClient.listEventsCalls != 0 {
		t.Fatalf("FlushOutboxOnce reconciled remote tree: export=%d listTree=%d listEvents=%d", secondClient.exportCalls, baseClient.listTreeCalls, baseClient.listEventsCalls)
	}
	if pending := readPendingOutboxRecordsForTest(t, localDir); len(pending) != 0 {
		t.Fatalf("expected pending outbox empty after flush, got %+v", pending)
	}
	state := readPublicState(t, localDir)
	if state.Status != "ready" || state.States.HasPendingWriteback || state.PendingWriteback != 0 || state.Outbox.Pending != 0 || state.Outbox.NeedsAttention != 0 {
		t.Fatalf("expected drained public state without scanning untracked local file, got %+v", state)
	}
	if !state.LowMemory || state.Files != nil {
		t.Fatalf("expected flush public state to be written without per-file mirror scan, got lowMemory=%v files=%d", state.LowMemory, len(state.Files))
	}
}

// A draft written but never ingested by a sync cycle (the teardown race: a
// final fire-and-forget reply right before shutdown) is on disk but not in the
// outbox. FlushOutboxOnce drops it (outbox-only, no local scan);
// PushLocalAndFlushOnce ingests it by scanning the on-disk mirror, then flushes.
func TestPushLocalAndFlushOnceIngestsUnsyncedLocalDraft(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "command.json"), []byte(`{"text":"hello"}`), 0o644); err != nil {
		t.Fatalf("seed local command failed: %v", err)
	}

	// Baseline: FlushOutboxOnce must NOT ingest the unsynced draft (the bug).
	flushClient := &fakeClient{files: map[string]RemoteFile{}}
	flushOnly, err := NewSyncer(flushClient, SyncerOptions{
		WorkspaceID: "ws_push_local_once",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer flushOnly: %v", err)
	}
	if err := flushOnly.FlushOutboxOnce(context.Background()); err != nil {
		t.Fatalf("FlushOutboxOnce failed: %v", err)
	}
	if flushClient.bulkWriteCalls != 0 {
		t.Fatalf("FlushOutboxOnce must not ingest an unsynced local draft, got %d uploads", flushClient.bulkWriteCalls)
	}

	// Fix: PushLocalAndFlushOnce scans the on-disk mirror, ingests the draft,
	// uploads it, and drains the outbox.
	pushClient := &fakeClient{files: map[string]RemoteFile{}}
	drain, err := NewSyncer(pushClient, SyncerOptions{
		WorkspaceID: "ws_push_local_once",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer drain: %v", err)
	}
	if err := drain.PushLocalAndFlushOnce(context.Background()); err != nil {
		t.Fatalf("PushLocalAndFlushOnce failed: %v", err)
	}
	if pushClient.bulkWriteCalls != 1 {
		t.Fatalf("expected the unsynced draft to be ingested + uploaded once, got %d", pushClient.bulkWriteCalls)
	}
	if pending := readPendingOutboxRecordsForTest(t, localDir); len(pending) != 0 {
		t.Fatalf("expected outbox drained after push+flush, got %+v", pending)
	}
}

func TestPushLocalAndFlushOnceSkipsSelfReferentialOutboxControlFiles(t *testing.T) {
	localDir := t.TempDir()
	client := &fakeClient{files: map[string]RemoteFile{}}
	logger := &captureLogger{}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_skip_outbox_control",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
		Logger:      logger,
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	remotePath := "/slack/channels/C123/messages/1781905242_715929/.relay/outbox/pending/mountcmd_self.json"
	if err := syncer.saveOutboxRecord(outboxRecord{
		CommandID:   "mountcmd_self",
		WorkspaceID: "ws_skip_outbox_control",
		RemotePath:  remotePath,
		ContentType: "application/json",
		Content:     `{"path":"/slack/channels/C123/messages/1781905242_715929/plan-ack.json"}`,
		Hash:        "sha256:control",
		Exists:      true,
		FirstSeenAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("seed self-referential outbox record: %v", err)
	}

	if err := syncer.PushLocalAndFlushOnce(context.Background()); err != nil {
		t.Fatalf("PushLocalAndFlushOnce failed: %v", err)
	}

	if client.bulkWriteCalls != 0 || client.writeFileCalls != 0 {
		t.Fatalf("control outbox file must not be uploaded, bulk=%d write=%d", client.bulkWriteCalls, client.writeFileCalls)
	}
	if pending := readPendingOutboxRecordsForTest(t, localDir); len(pending) != 0 {
		t.Fatalf("expected self-referential control record removed from pending outbox, got %+v", pending)
	}
	acked := readOutboxRecordsInDirForTest(t, filepath.Join(localDir, ".relay", "outbox", "acked"))
	if len(acked) != 1 || acked[0].CommandID != "mountcmd_self" || acked[0].DispatchStatus != "skipped_control_path" {
		t.Fatalf("expected skipped control record in acked diagnostics, got %+v", acked)
	}
	skipLogs := 0
	for _, line := range logger.lines {
		if strings.Contains(line, "skipping mount control path before upload") &&
			strings.Contains(line, "1781905242_715929/.relay/outbox/pending/mountcmd_self.json") {
			skipLogs++
		}
	}
	if skipLogs != 1 {
		t.Fatalf("expected one control skip log, got %d lines: %#v", skipLogs, logger.lines)
	}
}

func TestFlushOutboxOnceWritesReceiptCapabilityMarkerWithEmptyOutbox(t *testing.T) {
	localDir := t.TempDir()
	client := &fakeClient{files: map[string]RemoteFile{}}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_flush_outbox_capabilities",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
		Interval:    30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	if err := syncer.FlushOutboxOnce(context.Background()); err != nil {
		t.Fatalf("FlushOutboxOnce with empty outbox failed: %v", err)
	}
	capabilitiesPath := filepath.Join(localDir, ".relay", "outbox", "capabilities.json")
	data, err := os.ReadFile(capabilitiesPath)
	if err != nil {
		t.Fatalf("expected outbox capabilities marker: %v", err)
	}
	var capabilities outboxCapabilities
	if err := json.Unmarshal(data, &capabilities); err != nil {
		t.Fatalf("decode outbox capabilities marker: %v", err)
	}
	if capabilities.SchemaVersion != outboxCapabilitiesSchemaVersion || !capabilities.DispatchReceipts {
		t.Fatalf("unexpected capabilities marker: %+v", capabilities)
	}

	// A second flush must not rewrite an up-to-date marker.
	firstStat, err := os.Stat(capabilitiesPath)
	if err != nil {
		t.Fatalf("stat capabilities marker: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := syncer.FlushOutboxOnce(context.Background()); err != nil {
		t.Fatalf("second FlushOutboxOnce failed: %v", err)
	}
	secondStat, err := os.Stat(capabilitiesPath)
	if err != nil {
		t.Fatalf("stat capabilities marker after second flush: %v", err)
	}
	if !secondStat.ModTime().Equal(firstStat.ModTime()) {
		t.Fatalf("expected capabilities marker not rewritten, mtime %v -> %v", firstStat.ModTime(), secondStat.ModTime())
	}
	secondData, err := os.ReadFile(capabilitiesPath)
	if err != nil {
		t.Fatalf("read capabilities marker after second flush: %v", err)
	}
	if !bytes.Equal(secondData, data) {
		t.Fatalf("expected capabilities marker content unchanged, got %s", secondData)
	}

	// A semantically-equal marker with reordered fields and different
	// whitespace must also be left untouched.
	reordered := []byte(fmt.Sprintf("{\n  \"dispatchReceipts\": true,\n  \"schemaVersion\": %d\n}\n", outboxCapabilitiesSchemaVersion))
	if err := os.WriteFile(capabilitiesPath, reordered, 0o644); err != nil {
		t.Fatalf("rewrite capabilities marker: %v", err)
	}
	if err := syncer.FlushOutboxOnce(context.Background()); err != nil {
		t.Fatalf("third FlushOutboxOnce failed: %v", err)
	}
	got, err := os.ReadFile(capabilitiesPath)
	if err != nil {
		t.Fatalf("read capabilities marker after third flush: %v", err)
	}
	if !bytes.Equal(got, reordered) {
		t.Fatalf("expected semantically-equal capabilities marker to be preserved, got %s", got)
	}
}

func TestFlushOutboxOnceReturnsErrorAndPreservesPendingState(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "command.json"), []byte(`{"text":"hello"}`), 0o644); err != nil {
		t.Fatalf("seed local command failed: %v", err)
	}
	client := &fakeClient{
		files: map[string]RemoteFile{},
		bulkWriteResponseFunc: func(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
			_ = ctx
			_ = workspaceID
			_ = files
			return BulkWriteResponse{}, context.Canceled
		},
	}
	first, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_flush_outbox_once_failure",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer first: %v", err)
	}
	if err := first.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected first upload to fail")
	}
	attemptsAfterSetup := client.bulkWriteCalls

	second, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_flush_outbox_once_failure",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("NewSyncer second: %v", err)
	}
	if err := second.FlushOutboxOnce(context.Background()); err == nil {
		t.Fatal("expected FlushOutboxOnce to fail when upload fails")
	}
	if client.bulkWriteCalls != attemptsAfterSetup+1 {
		t.Fatalf("expected exactly one forced flush attempt, got %d -> %d", attemptsAfterSetup, client.bulkWriteCalls)
	}
	pending := readPendingOutboxRecordsForTest(t, localDir)
	if len(pending) != 1 {
		t.Fatalf("expected pending outbox preserved after failed flush, got %+v", pending)
	}
	state := readPublicState(t, localDir)
	if state.Status != "writeback-pending" || !state.States.HasPendingWriteback || state.Outbox.Pending != 1 {
		t.Fatalf("expected failed flush public state to surface pending outbox, got %+v", state)
	}
}

func TestBulkWrite_MixedCreateAndUpdateBatch(t *testing.T) {
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
		WorkspaceID: "ws_mount_mixed_bulk",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}

	localA := filepath.Join(localDir, "Docs", "A.md")
	if err := os.WriteFile(localA, []byte("# A updated"), 0o644); err != nil {
		t.Fatalf("update local file failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "Docs", "B.md"), []byte("# B"), 0o644); err != nil {
		t.Fatalf("create local file failed: %v", err)
	}
	markLocalDirtyForTest(t, syncer, "/notion/Docs/A.md", localA)

	client.bulkWriteCalls = 0
	client.bulkWriteBatches = nil
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("mixed batch sync failed: %v", err)
	}

	if client.bulkWriteCalls != 1 {
		t.Fatalf("expected one mixed bulk write call, got %d", client.bulkWriteCalls)
	}
	if got := len(client.bulkWriteBatches); got != 1 {
		t.Fatalf("expected one mixed bulk batch, got %d", got)
	}
	gotPaths := make([]string, 0, len(client.bulkWriteBatches[0]))
	for _, file := range client.bulkWriteBatches[0] {
		gotPaths = append(gotPaths, normalizeRemotePath(file.Path))
	}
	sort.Strings(gotPaths)
	wantPaths := []string{"/notion/Docs/A.md", "/notion/Docs/B.md"}
	if strings.Join(gotPaths, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("expected mixed bulk paths %v, got %v", wantPaths, gotPaths)
	}
}

func TestBulkWrite_OverwritesRemoteChangeInSingleCycle(t *testing.T) {
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
	markLocalDirtyForTest(t, syncer, "/notion/Docs/A.md", localFile)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("bulk write sync failed: %v", err)
	}

	localAfterWrite, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatalf("read local file after bulk write failed: %v", err)
	}
	if string(localAfterWrite) != "# local" {
		t.Fatalf("expected local buffer to remain '# local', got %q", string(localAfterWrite))
	}
	if client.files["/notion/Docs/A.md"].Content != "# local" {
		t.Fatalf("expected bulk write to push local content immediately, got %q", client.files["/notion/Docs/A.md"].Content)
	}
	if syncer.state.Files["/notion/Docs/A.md"].Dirty {
		t.Fatalf("expected tracked file to be clean after bulk reconciliation")
	}
}

func TestBulkWrite_SkipsRedundantWriteAfterReconcile(t *testing.T) {
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
		WorkspaceID: "ws_mount_bulk_reconcile",
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
	if err := os.WriteFile(localFile, []byte("# local"), 0o644); err != nil {
		t.Fatalf("write local edit failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("bulk write sync failed: %v", err)
	}

	client.bulkWriteCalls = 0
	client.bulkWriteBatches = nil
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("steady-state sync failed: %v", err)
	}
	if client.bulkWriteCalls != 0 {
		t.Fatalf("expected no redundant bulk write after reconciliation, got %d", client.bulkWriteCalls)
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

func TestFullReconcileBypassesQuietEventsShortCircuit(t *testing.T) {
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
		WorkspaceID: "ws_force_full_quiet",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}
	syncer.state.IncrementalBacklogDraining = true
	if err := syncer.saveState(); err != nil {
		t.Fatalf("persist stale backlog-draining state: %v", err)
	}

	client.files["/notion/Docs/B.md"] = RemoteFile{
		Path:        "/notion/Docs/B.md",
		Revision:    "rev_2",
		ContentType: "text/markdown",
		Content:     "# B",
	}
	syncer.forceFullReconcile = true
	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("forced reconcile failed: %v", err)
	}
	if syncer.state.IncrementalBacklogDraining {
		t.Fatalf("forced full reconcile should clear stale backlog-draining state")
	}
	status := readPublicState(t, localDir)
	if status.Status != "ready" || status.States.Syncing {
		t.Fatalf("expected forced full reconcile to report ready, got %+v", status)
	}
	assertLocalFileContent(t, filepath.Join(localDir, "Docs", "B.md"), "# B")
}

func TestBulkSeedThenSync(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)

	workspaceID := "ws_mount_bulk_seed"
	handler := newMountsyncAPIHandler(t, store)
	api := httptest.NewServer(handler)
	defer api.Close()

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
	disableWebSocket := false
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		WebSocket:   &disableWebSocket,
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

func TestBulkWrite_FirstWriteNoIfMatch(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)

	workspaceID := "ws_mount_first_write_bulk"
	handler := newMountsyncAPIHandler(t, store)

	var bulkCalls atomic.Int32
	var filePutCalls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/fs/bulk") && r.Method == http.MethodPost:
			bulkCalls.Add(1)
		case strings.HasSuffix(r.URL.Path, "/fs/file") && r.Method == http.MethodPut:
			filePutCalls.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer api.Close()

	token := mustMountsyncTestJWT(t, "dev-secret", workspaceID, "MountSync", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))
	localDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localDir, "Docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs failed: %v", err)
	}
	localPath := filepath.Join(localDir, "Docs", "First.md")
	if err := os.WriteFile(localPath, []byte("# First"), 0o644); err != nil {
		t.Fatalf("write local file failed: %v", err)
	}

	client := NewHTTPClient(api.URL, token, api.Client())
	websocketEnabled := false
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		WebSocket:   &websocketEnabled,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync once failed: %v", err)
	}

	if bulkCalls.Load() != 1 {
		t.Fatalf("expected one bulk write call, got %d", bulkCalls.Load())
	}
	if filePutCalls.Load() != 0 {
		t.Fatalf("expected zero per-file PUTs for first write, got %d", filePutCalls.Load())
	}
	remoteFile, err := client.ReadFile(context.Background(), workspaceID, "/notion/Docs/First.md")
	if err != nil {
		t.Fatalf("read first written remote file failed: %v", err)
	}
	if remoteFile.Content != "# First" {
		t.Fatalf("expected remote content '# First', got %q", remoteFile.Content)
	}
}

func TestBulkMigrationReducesHTTPCalls(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)

	workspaceID := "ws_mount_bulk_http_volume"
	handler := newMountsyncAPIHandler(t, store)

	var requestCounts sync.Map
	var requestsMu sync.Mutex
	requests := make([]string, 0)
	var fsFileRequestsMu sync.Mutex
	fsFileRequests := make([]string, 0)
	counterFor := func(path string) *atomic.Int32 {
		counter, _ := requestCounts.LoadOrStore(path, &atomic.Int32{})
		return counter.(*atomic.Int32)
	}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counterFor(r.URL.Path).Add(1)
		requestsMu.Lock()
		requests = append(requests, r.Method+" "+r.URL.String())
		requestsMu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/fs/file") {
			fsFileRequestsMu.Lock()
			fsFileRequests = append(fsFileRequests, r.Method+" "+r.URL.String())
			fsFileRequestsMu.Unlock()
		}

		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)

		for key, values := range recorder.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(recorder.Code)
		if _, err := w.Write(recorder.Body.Bytes()); err != nil {
			t.Fatalf("write recorded response failed: %v", err)
		}
	}))
	defer api.Close()

	token := mustMountsyncTestJWT(t, "dev-secret", workspaceID, "MountSync", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))
	localDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localDir, "Docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs failed: %v", err)
	}
	for idx := 0; idx < 10; idx++ {
		name := fmt.Sprintf("File%02d.md", idx+1)
		path := filepath.Join(localDir, "Docs", name)
		if err := os.WriteFile(path, []byte(fmt.Sprintf("# File %d", idx+1)), 0o644); err != nil {
			t.Fatalf("write local file %s failed: %v", name, err)
		}
	}

	client := NewHTTPClient(api.URL, token, api.Client())
	websocketEnabled := false
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		WebSocket:   &websocketEnabled,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync once failed: %v", err)
	}

	bulkPath := fmt.Sprintf("/v1/workspaces/%s/fs/bulk", workspaceID)
	if got := counterFor(bulkPath).Load(); got != 1 {
		t.Fatalf("expected exactly one bulk request to %s, got %d", bulkPath, got)
	}

	if got := len(fsFileRequests); got != 0 {
		t.Fatalf("expected zero /fs/file requests after bulk migration, got %d: %v", got, fsFileRequests)
	}
	t.Logf("bulk migration verified: 1 POST /fs/bulk, 0 total requests on /fs/file")
}

func TestBulkWrite_ChunkAtThreshold(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{},
	}
	localDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localDir, "Docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs failed: %v", err)
	}
	for idx := 0; idx < 300; idx++ {
		name := fmt.Sprintf("File%03d.md", idx+1)
		path := filepath.Join(localDir, "Docs", name)
		if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
			t.Fatalf("seed file %s failed: %v", name, err)
		}
	}

	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_chunked_bulk",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	syncer.bulkFlushThreshold = 256

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("chunked sync failed: %v", err)
	}

	if client.bulkWriteCalls != 2 {
		t.Fatalf("expected two bulk write calls, got %d", client.bulkWriteCalls)
	}
	if got := len(client.bulkWriteBatches); got != 2 {
		t.Fatalf("expected two recorded bulk batches, got %d", got)
	}
	if got := len(client.bulkWriteBatches[0]); got != 256 {
		t.Fatalf("expected first batch size 256, got %d", got)
	}
	if got := len(client.bulkWriteBatches[1]); got != 44 {
		t.Fatalf("expected second batch size 44, got %d", got)
	}
}

func TestBulkWrite_ChunksBySerializedRequestSize(t *testing.T) {
	t.Setenv("RELAYFILE_MAX_WRITEBACK_BATCH_BYTES", "650")

	client := &fakeClient{
		files: map[string]RemoteFile{},
	}
	localDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localDir, "Docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs failed: %v", err)
	}
	for idx := 0; idx < 4; idx++ {
		name := fmt.Sprintf("Chunk%02d.md", idx+1)
		path := filepath.Join(localDir, "Docs", name)
		if err := os.WriteFile(path, []byte(strings.Repeat(name, 20)), 0o644); err != nil {
			t.Fatalf("seed file %s failed: %v", name, err)
		}
	}

	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_chunked_bulk_bytes",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("chunked sync failed: %v", err)
	}

	if client.bulkWriteCalls < 2 {
		t.Fatalf("expected serialized-size chunking to split bulk writes, got %d call(s)", client.bulkWriteCalls)
	}
	for idx, batch := range client.bulkWriteBatches {
		if size := bulkWriteRequestSize(batch); size > maxWritebackBatchBytes() {
			t.Fatalf("batch %d serialized to %d bytes, over cap %d", idx, size, maxWritebackBatchBytes())
		}
	}
}

func TestPushLocalRequiresDirtyForTrackedHashDrift(t *testing.T) {
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
		WorkspaceID: "ws_mount_hash_drift_no_push",
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
	if err := os.WriteFile(localFile, []byte("# local drift"), 0o644); err != nil {
		t.Fatalf("write local drift failed: %v", err)
	}
	client.bulkWriteCalls = 0
	client.bulkWriteBatches = nil
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync after local drift failed: %v", err)
	}

	if client.bulkWriteCalls != 0 {
		t.Fatalf("expected tracked hash drift without Dirty state not to write back, got %d bulk calls", client.bulkWriteCalls)
	}
	if got := client.files["/notion/Docs/A.md"].Content; got != "# A" {
		t.Fatalf("expected remote content to remain unchanged, got %q", got)
	}
}

func TestRemotePullOverwritesUnmarkedLocalDrift(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     "# A",
			},
		},
		events: []FilesystemEvent{{
			EventID:  "evt_1",
			Type:     "file.created",
			Path:     "/notion/Docs/A.md",
			Revision: "rev_1",
		}},
		revisionCounter: 1,
		eventCounter:    1,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_remote_overwrites_unmarked_drift",
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
	if err := os.WriteFile(localFile, []byte("# local drift"), 0o644); err != nil {
		t.Fatalf("write local drift failed: %v", err)
	}
	client.files["/notion/Docs/A.md"] = RemoteFile{
		Path:        "/notion/Docs/A.md",
		Revision:    "rev_2",
		ContentType: "text/markdown",
		Content:     "# remote v2",
	}
	client.appendEvent("file.updated", "/notion/Docs/A.md", "rev_2")
	client.bulkWriteCalls = 0
	client.bulkWriteBatches = nil

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync after remote update failed: %v", err)
	}

	if client.bulkWriteCalls != 0 {
		t.Fatalf("expected remote pull not to write back unmarked local drift, got %d bulk calls", client.bulkWriteCalls)
	}
	assertLocalFileContent(t, localFile, "# remote v2")
	if syncer.state.Files["/notion/Docs/A.md"].Dirty {
		t.Fatalf("expected remote pull to keep tracked file clean")
	}
}

func TestMissingTrackedFileRequiresDeletePending(t *testing.T) {
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
		WorkspaceID: "ws_mount_missing_without_delete_event",
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
	if err := os.Remove(localFile); err != nil {
		t.Fatalf("remove local file failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync after unmarked local removal failed: %v", err)
	}

	if len(client.deleteCalls) != 0 {
		t.Fatalf("expected no remote delete without deletePending, got %+v", client.deleteCalls)
	}
	if _, exists := client.files["/notion/Docs/A.md"]; !exists {
		t.Fatalf("expected remote file to remain after unmarked local removal")
	}
}

func TestBulkWrite_PartialErrors(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{},
		bulkWriteResponseFunc: func(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
			return BulkWriteResponse{
				Written:    len(files) - 1,
				ErrorCount: 1,
				Errors: []BulkWriteError{{
					Path:    "/notion/Docs/Denied.md",
					Code:    "forbidden",
					Message: "denied",
				}},
			}, nil
		},
	}
	localDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localDir, "Docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs failed: %v", err)
	}
	files := map[string]string{
		"AllowedA.md": "# Allowed A",
		"AllowedB.md": "# Allowed B",
		"Denied.md":   "# Denied",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(localDir, "Docs", name), []byte(content), 0o644); err != nil {
			t.Fatalf("seed local file %s failed: %v", name, err)
		}
	}

	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_partial_bulk",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("partial bulk sync failed: %v", err)
	}

	allowed := syncer.state.Files["/notion/Docs/AllowedA.md"]
	if allowed.Revision == "" || allowed.WriteDenied {
		t.Fatalf("expected allowed file to reconcile successfully, got %+v", allowed)
	}
	denied := syncer.state.Files["/notion/Docs/Denied.md"]
	if !denied.WriteDenied || denied.DeniedHash == "" || denied.Revision != "" {
		t.Fatalf("expected denied file to remain write-denied, got %+v", denied)
	}

	callsBefore := client.bulkWriteCalls
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("steady-state sync after denial failed: %v", err)
	}
	if client.bulkWriteCalls != callsBefore {
		t.Fatalf("expected denied file to be skipped on unchanged retry, got %d -> %d bulk calls", callsBefore, client.bulkWriteCalls)
	}
}

func TestBulkWrite_PerFileConflictCreatesArtifactAndRefreshesRemote(t *testing.T) {
	partialError := true
	client := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     "# remote",
			},
		},
		revisionCounter: 1,
		bulkWriteResponseFunc: func(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
			if !partialError {
				return BulkWriteResponse{}, nil
			}
			partialError = false
			return BulkWriteResponse{
				Written:    len(files) - 1,
				ErrorCount: 1,
				Errors: []BulkWriteError{{
					Path:    "/notion/Docs/A.md",
					Code:    "conflict",
					Message: "revision conflict",
				}},
			}, nil
		},
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_partial_retry",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}

	client.files["/notion/Docs/A.md"] = RemoteFile{
		Path:        "/notion/Docs/A.md",
		Revision:    "rev_remote",
		ContentType: "text/markdown",
		Content:     "# remote newer",
	}
	localA := filepath.Join(localDir, "Docs", "A.md")
	if err := os.WriteFile(localA, []byte("# local A"), 0o644); err != nil {
		t.Fatalf("write local A failed: %v", err)
	}
	markLocalDirtyForTest(t, syncer, "/notion/Docs/A.md", localA)
	localB := filepath.Join(localDir, "Docs", "B.md")
	if err := os.WriteFile(localB, []byte("# local B"), 0o644); err != nil {
		t.Fatalf("write local B failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync with per-file bulk error failed: %v", err)
	}

	trackedA := syncer.state.Files["/notion/Docs/A.md"]
	if trackedA.Revision != "rev_remote" {
		t.Fatalf("expected failed path to refresh tracked revision to remote state, got %+v", trackedA)
	}
	if trackedA.Dirty {
		t.Fatalf("expected failed path to reconcile to remote clean state, got %+v", trackedA)
	}
	if trackedA.Hash != hashString("# remote newer") {
		t.Fatalf("expected failed path to track remote hash after refresh, got %+v", trackedA)
	}
	trackedB := syncer.state.Files["/notion/Docs/B.md"]
	if trackedB.Dirty || trackedB.Revision == "" {
		t.Fatalf("expected successful path to reconcile in same cycle, got %+v", trackedB)
	}
	if client.files["/notion/Docs/A.md"].Content != "# remote newer" {
		t.Fatalf("expected failed path to leave remote content untouched, got %q", client.files["/notion/Docs/A.md"].Content)
	}
	if client.files["/notion/Docs/B.md"].Content != "# local B" {
		t.Fatalf("expected successful path to be written during partial failure cycle, got %q", client.files["/notion/Docs/B.md"].Content)
	}
	assertLocalFileContent(t, localA, "# remote newer")
	conflictPath := filepath.Join(localDir, ".relay", "conflicts", "notion", "Docs", "A.md.rev_1.local")
	assertLocalFileContent(t, conflictPath, "# local A")

	state := readPublicState(t, localDir)
	if state.Status != "conflict" || state.PendingConflicts != 1 {
		t.Fatalf("expected conflict status in public state, got %+v", state)
	}
	if state.Files["/notion/Docs/A.md"].Status != "conflict" {
		t.Fatalf("expected file state conflict, got %+v", state.Files["/notion/Docs/A.md"])
	}

	if err := os.WriteFile(localA, []byte("# local A resolved"), 0o644); err != nil {
		t.Fatalf("write resolved local A failed: %v", err)
	}
	markLocalDirtyForTest(t, syncer, "/notion/Docs/A.md", localA)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("resolved retry sync failed: %v", err)
	}
	if client.files["/notion/Docs/A.md"].Content != "# local A resolved" {
		t.Fatalf("expected resolved retry to write local A, got %q", client.files["/notion/Docs/A.md"].Content)
	}
	if _, err := os.Stat(filepath.Join(localDir, ".relay", "conflicts", "resolved", "notion", "Docs", "A.md.rev_1.local")); err != nil {
		t.Fatalf("expected conflict artifact moved to resolved, got %v", err)
	}
	state = readPublicState(t, localDir)
	if state.PendingConflicts != 0 || state.Status != "ready" {
		t.Fatalf("expected ready status after resolution, got %+v", state)
	}
}

func TestBulkWrite_SchemaValidationQuarantinesLocalAndRestoresRemote(t *testing.T) {
	rejectOnce := true
	client := &fakeClient{
		files: map[string]RemoteFile{
			"/github/repos/acme/api/pulls/42/reviews/draft.json": {
				Path:        "/github/repos/acme/api/pulls/42/reviews/draft.json",
				Revision:    "rev_1",
				ContentType: "application/json",
				Content:     `{"event":"APPROVE"}`,
			},
		},
		revisionCounter: 1,
		bulkWriteResponseFunc: func(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
			if !rejectOnce {
				return BulkWriteResponse{}, nil
			}
			rejectOnce = false
			return BulkWriteResponse{
				Written:    0,
				ErrorCount: 1,
				Errors: []BulkWriteError{{
					Path:    "/github/repos/acme/api/pulls/42/reviews/draft.json",
					Code:    "schema_validation_failed",
					Message: "body.event must be one of APPROVE,REQUEST_CHANGES,COMMENT (line 3)",
				}},
			}, nil
		},
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_schema_invalid",
		RemoteRoot:  "/github",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}

	localPath := filepath.Join(localDir, "repos", "acme", "api", "pulls", "42", "reviews", "draft.json")
	if err := os.WriteFile(localPath, []byte(`{"event":"PLEASE_APPROVE"}`), 0o644); err != nil {
		t.Fatalf("write local draft failed: %v", err)
	}
	markLocalDirtyForTest(t, syncer, "/github/repos/acme/api/pulls/42/reviews/draft.json", localPath)

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync with schema validation failure failed: %v", err)
	}

	assertLocalFileContent(t, localPath, `{"event":"APPROVE"}`)

	conflictsRoot := filepath.Join(localDir, ".relay", "conflicts", "github", "repos", "acme", "api", "pulls", "42", "reviews")
	matches, err := filepath.Glob(filepath.Join(conflictsRoot, "draft.json.invalid.*"))
	if err != nil {
		t.Fatalf("glob conflict artifact failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one .invalid.<ts> artifact, got %d (%v)", len(matches), matches)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read invalid artifact failed: %v", err)
	}
	if string(body) != `{"event":"PLEASE_APPROVE"}` {
		t.Fatalf("expected invalid artifact to hold local body, got %q", body)
	}

	tracked := syncer.state.Files["/github/repos/acme/api/pulls/42/reviews/draft.json"]
	if tracked.Dirty {
		t.Fatalf("expected tracked entry to clear dirty flag after restore, got %+v", tracked)
	}
	if tracked.Revision != "rev_1" {
		t.Fatalf("expected tracked entry to reflect restored remote revision, got %+v", tracked)
	}
}

func TestBulkWrite_SchemaValidationOnCreateRemovesLocal(t *testing.T) {
	rejectOnce := true
	client := &fakeClient{
		files: map[string]RemoteFile{},
		bulkWriteResponseFunc: func(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
			if !rejectOnce {
				return BulkWriteResponse{}, nil
			}
			rejectOnce = false
			return BulkWriteResponse{
				Written:    0,
				ErrorCount: 1,
				Errors: []BulkWriteError{{
					Path:    "/github/repos/acme/api/pulls/42/reviews/draft.json",
					Code:    "schema_validation_failed",
					Message: "missing required property: event",
				}},
			}, nil
		},
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_schema_create_invalid",
		RemoteRoot:  "/github",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	localPath := filepath.Join(localDir, "repos", "acme", "api", "pulls", "42", "reviews", "draft.json")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir local path failed: %v", err)
	}
	if err := os.WriteFile(localPath, []byte(`{"unexpected":"shape"}`), 0o644); err != nil {
		t.Fatalf("write local create body failed: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync with schema validation create failure failed: %v", err)
	}

	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Fatalf("expected invalid create body removed from mirror, got err=%v", err)
	}
	conflictsRoot := filepath.Join(localDir, ".relay", "conflicts", "github", "repos", "acme", "api", "pulls", "42", "reviews")
	matches, err := filepath.Glob(filepath.Join(conflictsRoot, "draft.json.invalid.*"))
	if err != nil {
		t.Fatalf("glob conflict artifact failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one .invalid.<ts> artifact, got %d (%v)", len(matches), matches)
	}
	if _, ok := syncer.state.Files["/github/repos/acme/api/pulls/42/reviews/draft.json"]; ok {
		t.Fatalf("expected tracked entry cleared for unrestorable schema-invalid create")
	}
}

func TestBulkWrite_DeletesStayPerFile(t *testing.T) {
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
		WorkspaceID: "ws_mount_delete_per_file",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}

	client.bulkWriteCalls = 0
	client.bulkWriteBatches = nil
	client.deleteCalls = nil

	localPath := filepath.Join(localDir, "Docs", "A.md")
	if err := os.Remove(localPath); err != nil {
		t.Fatalf("remove local file failed: %v", err)
	}
	if err := syncer.HandleLocalChange(context.Background(), "Docs/A.md", fsnotify.Remove); err != nil {
		t.Fatalf("delete local change failed: %v", err)
	}

	if client.bulkWriteCalls != 0 {
		t.Fatalf("expected no bulk write for delete cycle, got %d", client.bulkWriteCalls)
	}
	if got := len(client.deleteCalls); got != 1 {
		t.Fatalf("expected one delete call, got %d", got)
	}
	if client.deleteCalls[0].Path != "/notion/Docs/A.md" || client.deleteCalls[0].BaseRevision != "rev_1" {
		t.Fatalf("expected delete to use tracked revision rev_1, got %+v", client.deleteCalls[0])
	}
	state := readPublicState(t, localDir)
	if state.PendingWriteback != 0 || state.Status != "ready" {
		t.Fatalf("expected delete cycle to settle cleanly, got %+v", state)
	}
}

func TestBulkWrite_ReconcileUsesResponseRevision(t *testing.T) {
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
		WorkspaceID: "ws_mount_bulk_response_revision",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}

	localPath := filepath.Join(localDir, "Docs", "A.md")
	if err := os.WriteFile(localPath, []byte("# A updated"), 0o644); err != nil {
		t.Fatalf("write local file failed: %v", err)
	}
	markLocalDirtyForTest(t, syncer, "/notion/Docs/A.md", localPath)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("bulk write sync failed: %v", err)
	}

	if len(client.lastBulkWriteResponse.Results) != 1 {
		t.Fatalf("expected one bulk result, got %+v", client.lastBulkWriteResponse)
	}
	got := syncer.state.Files["/notion/Docs/A.md"].Revision
	want := client.lastBulkWriteResponse.Results[0].Revision
	if got != want {
		t.Fatalf("expected tracked revision %q from bulk response, got %q", want, got)
	}
	if got == "rev_1" {
		t.Fatalf("expected tracked revision to advance beyond pre-write revision")
	}
}

func TestReconcileBulkWriteWhitespaceContentTypeFallsBackToRemote(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_2",
				ContentType: "text/markdown",
				Content:     "# A",
			},
		},
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_mount_bulk_whitespace_content_type",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	if err := syncer.loadState(); err != nil {
		t.Fatalf("load state failed: %v", err)
	}

	pendingWrite := pendingBulkWrite{
		remotePath: "/notion/Docs/A.md",
		localPath:  filepath.Join(localDir, "Docs", "A.md"),
		snapshot: localSnapshot{
			ContentType: " \t ",
			Hash:        "hash_a",
		},
	}
	// An empty revision forces the remote ReadFile fallback. The
	// all-whitespace snapshot content type must fall through to the remote
	// file's content type instead of being kept as whitespace.
	if err := syncer.reconcileBulkWrite(context.Background(), pendingWrite, ""); err != nil {
		t.Fatalf("reconcileBulkWrite failed: %v", err)
	}
	tracked := syncer.state.Files["/notion/Docs/A.md"]
	if tracked.ContentType != "text/markdown" {
		t.Fatalf("expected remote content type fallback, got %q", tracked.ContentType)
	}
	if tracked.Revision != "rev_2" {
		t.Fatalf("expected remote revision, got %q", tracked.Revision)
	}
}

func TestSyncOnceUsesWebSocketForRealtimeUpdatesAndSkipsPollingWhileConnected(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)

	workspaceID := "ws_mount_websocket"
	handler := newMountsyncAPIHandler(t, store)
	token := mustMountsyncTestJWT(t, "dev-secret", workspaceID, "MountSync", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	var treeCalls atomic.Int32
	var eventCalls atomic.Int32
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

func TestScopeGrantsWritePreservesPathCase(t *testing.T) {
	if !scopeGrantsWrite("relayfile:fs:write:/README.md/*", "/README.md") {
		t.Fatalf("write scope over /README.md must grant /README.md (case preserved)")
	}
	if !scopeGrantsWrite("relayfile:fs:write:/packages/web/lib/MyComponent/*", "/packages/web/lib/MyComponent/index.ts") {
		t.Fatalf("write scope over /packages/web/lib/MyComponent must grant files under it")
	}
	if !scopeGrantsWrite("RELAYFILE:FS:WRITE:/Foo/*", "/Foo/bar.ts") {
		t.Fatalf("plane/resource/action must stay case-insensitive")
	}
	if scopeGrantsWrite("relayfile:fs:write:/Foo/*", "/foo/bar.ts") {
		t.Fatalf("case-mismatched path must NOT be granted (paths are case-sensitive)")
	}
}

func TestCanReadPathWithPerFileScopes(t *testing.T) {
	scopes := map[string]struct{}{
		"relayfile:fs:read:/src/app.ts": {},
		"relayfile:fs:read:/README.md":  {},
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

// TestLocalCreatePreservedWhenServerDeniesWrite guards the regression where
// relayfile-mount was destroying local files if the server rejected the
// push with 403. The correct behavior: keep the local file, log the
// denial, skip future push attempts until the content changes, and never
// re-enter the delete path on subsequent reconciles.
func TestLocalCreatePreservedWhenServerDeniesWrite(t *testing.T) {
	disableWebSocket := true
	token := mustMountsyncTestJWT(
		t,
		"dev-secret",
		"ws_local_create_denied",
		"MountSync",
		[]string{"relayfile:fs:write"},
		time.Now().Add(time.Hour),
	)
	localDir := t.TempDir()
	// Server: empty workspace, but configured to reject ANY write to /notion/*.
	server := newMockMountsyncServer(
		t,
		map[string]RemoteFile{},
		nil,
		map[string]struct{}{"/notion/intro-agent.md": {}},
	)
	defer server.Close()

	client := NewHTTPClient(server.URL, token, server.Client())
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_local_create_denied",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		WebSocket:   &disableWebSocket,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	// Initial pull — workspace is empty, nothing to download.
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial pull failed: %v", err)
	}

	// Agent writes a new local file.
	localPath := filepath.Join(localDir, "intro-agent.md")
	const expected = "# Hello from the agent"
	if err := os.WriteFile(localPath, []byte(expected), 0o644); err != nil {
		t.Fatalf("write local file failed: %v", err)
	}

	// Watcher fires → mount pushes → server returns 403.
	if err := syncer.HandleLocalChange(
		context.Background(),
		"intro-agent.md",
		fsnotify.Create,
	); err != nil {
		t.Fatalf("handle local create failed: %v", err)
	}

	// Invariant #1: local file must be preserved.
	assertLocalFileContent(t, localPath, expected)

	// Invariant #2: denial must be logged.
	denialLogPath := filepath.Join(localDir, ".relay", "permissions-denied.log")
	logData, err := os.ReadFile(denialLogPath)
	if err != nil {
		t.Fatalf("read denial log failed: %v", err)
	}
	if !strings.Contains(string(logData), "WRITE_DENIED /notion/intro-agent.md") {
		t.Fatalf("expected WRITE_DENIED entry for /intro-agent.md, got: %q", string(logData))
	}

	// Invariant #3: a subsequent reconcile (full pull + push) must NOT
	// delete the local file or re-push / re-log the denial for the same
	// content. We simulate this by running two more reconcile cycles.
	startingLogLen := len(logData)
	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}
	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("third reconcile failed: %v", err)
	}
	assertLocalFileContent(t, localPath, expected)
	logData2, err := os.ReadFile(denialLogPath)
	if err != nil {
		t.Fatalf("read denial log (2nd) failed: %v", err)
	}
	if len(logData2) != startingLogLen {
		t.Fatalf(
			"expected denial log unchanged across reconciles (len=%d → %d), diff: %q",
			startingLogLen,
			len(logData2),
			string(logData2[startingLogLen:]),
		)
	}

	// Invariant #4: when the user edits the file, we retry the push.
	// With the mock still denying writes the retry fails again, but the
	// fresh denial line must appear (proving we didn't silently skip
	// legitimate content changes).
	if err := os.WriteFile(localPath, []byte("# Updated"), 0o644); err != nil {
		t.Fatalf("update local file failed: %v", err)
	}
	if err := syncer.HandleLocalChange(
		context.Background(),
		"intro-agent.md",
		fsnotify.Write,
	); err != nil {
		t.Fatalf("handle local update failed: %v", err)
	}
	assertLocalFileContent(t, localPath, "# Updated")
	logData3, err := os.ReadFile(denialLogPath)
	if err != nil {
		t.Fatalf("read denial log (3rd) failed: %v", err)
	}
	denialLines := strings.Count(string(logData3), "WRITE_DENIED /notion/intro-agent.md")
	if denialLines < 2 {
		t.Fatalf("expected a second WRITE_DENIED entry after file edit, got: %q", string(logData3))
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

func TestApplyRemoteFile_IndexAndLayoutFiles(t *testing.T) {
	t.Parallel()

	localDir := t.TempDir()
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID: "ws_index_layout",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	indexBody := "{\n  \"title\": \"Caf\\u00e9 \\u2615\",\n  \"rows\": [\n    {\"title\": \"Alpha\", \"file\": \"alpha__page-1.md\"}\n  ]\n}\n"
	layoutBody := "# notion layout\n\nRead `_index.json` first.\n"

	if err := syncer.applyRemoteFile("/notion/pages/_index.json", RemoteFile{
		Path:        "/notion/pages/_index.json",
		Revision:    "rev_index",
		ContentType: "application/json",
		Content:     indexBody,
	}, nil); err != nil {
		t.Fatalf("applyRemoteFile(_index.json) failed: %v", err)
	}
	if err := syncer.applyRemoteFile("/notion/.layout.md", RemoteFile{
		Path:        "/notion/.layout.md",
		Revision:    "rev_layout",
		ContentType: "text/markdown",
		Content:     layoutBody,
	}, nil); err != nil {
		t.Fatalf("applyRemoteFile(.layout.md) failed: %v", err)
	}

	indexPath := filepath.Join(localDir, "notion", "pages", "_index.json")
	layoutPath := filepath.Join(localDir, "notion", ".layout.md")
	indexBytes, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read %s failed: %v", indexPath, err)
	}
	layoutBytes, err := os.ReadFile(layoutPath)
	if err != nil {
		t.Fatalf("read %s failed: %v", layoutPath, err)
	}
	if string(indexBytes) != indexBody {
		t.Fatalf("_index.json content mismatch: got %q, want %q", string(indexBytes), indexBody)
	}
	if string(layoutBytes) != layoutBody {
		t.Fatalf(".layout.md content mismatch: got %q, want %q", string(layoutBytes), layoutBody)
	}
	if got, want := hashBytes(indexBytes), hashBytes([]byte(indexBody)); got != want {
		t.Fatalf("_index.json hash = %s, want %s", got, want)
	}
	if got, want := hashBytes(layoutBytes), hashBytes([]byte(layoutBody)); got != want {
		t.Fatalf(".layout.md hash = %s, want %s", got, want)
	}
	if info, err := os.Stat(filepath.Join(localDir, "notion", "pages")); err != nil {
		t.Fatalf("stat notion/pages failed: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("expected notion/pages to be a directory")
	}
}

// TestApplyRemoteFile_QuarantinesPathCollision pins the resilience fix for the
// Slack-adapter file/dir collision: a thread reply emitted as a leaf file
// `replies/<ts>.json` whose children are also nested under a directory at the
// same stem. The child cannot be materialized (its parent is a regular file →
// ENOTDIR), but that single path must NOT fail the apply — it is quarantined so
// the sync cycle completes and bootstrap can finish. Before the fix this
// returned ENOTDIR, which aborted every cycle and wedged the mount forever.
func TestApplyRemoteFile_QuarantinesPathCollision(t *testing.T) {
	t.Parallel()

	localDir := t.TempDir()
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID: "ws_path_collision",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	// The reply leaf applies fine as a regular file.
	leaf := "/slack/channels/C1/threads/T1/replies/1780911014_625209.json"
	if err := syncer.applyRemoteFile(leaf, RemoteFile{
		Path:        leaf,
		Revision:    "rev_leaf",
		ContentType: "application/json",
		Content:     "{\"text\":\"reply\"}\n",
	}, nil); err != nil {
		t.Fatalf("applyRemoteFile(leaf) failed: %v", err)
	}

	// A child UNDER the leaf forces mkdir of a path whose parent is a regular
	// file. With the fix this is quarantined (nil error), not propagated.
	child := leaf + "/reactions/tada--U1.json"
	if err := syncer.applyRemoteFile(child, RemoteFile{
		Path:        child,
		Revision:    "rev_child",
		ContentType: "application/json",
		Content:     "{\"emoji\":\"tada\"}\n",
	}, nil); err != nil {
		t.Fatalf("applyRemoteFile(child) should quarantine the collision, got error: %v", err)
	}

	// The collision counter advanced, the leaf is intact, and the colliding
	// child was not (and cannot be) materialized.
	if got := syncer.state.Counters.PathCollisionQuarantined; got == 0 {
		t.Fatalf("expected PathCollisionQuarantined > 0, got %d", got)
	}
	leafLocal := filepath.Join(localDir, "slack", "channels", "C1", "threads", "T1", "replies", "1780911014_625209.json")
	if info, err := os.Stat(leafLocal); err != nil {
		t.Fatalf("stat leaf failed: %v", err)
	} else if info.IsDir() {
		t.Fatalf("expected reply leaf to be a regular file, got directory")
	}
	if _, ok := syncer.state.Files[child]; ok {
		t.Fatalf("quarantined child must not be recorded as a tracked file")
	}

	// The inverse collision is also non-fatal: a remote file targets a local
	// path that already exists as a directory.
	dirRemotePath := "/slack/channels/C1/threads/T1/replies"
	if err := syncer.applyRemoteFile(dirRemotePath, RemoteFile{
		Path:        dirRemotePath,
		Revision:    "rev_dir_collision",
		ContentType: "application/json",
		Content:     "{\"kind\":\"file\"}\n",
	}, nil); err != nil {
		t.Fatalf("applyRemoteFile(directory target) should quarantine the collision, got error: %v", err)
	}
	if got := syncer.state.Counters.PathCollisionQuarantined; got < 2 {
		t.Fatalf("expected both path collisions to be counted, got %d", got)
	}
	if info, err := os.Stat(filepath.Join(localDir, "slack", "channels", "C1", "threads", "T1", "replies")); err != nil {
		t.Fatalf("stat replies directory failed: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("expected replies path to remain a directory")
	}

	// Re-applying the SAME colliding path counts each occurrence (cumulative,
	// like the other guard counters) but is logged only once — so a persistent
	// collision doesn't spam the log every cycle.
	countBefore := syncer.state.Counters.PathCollisionQuarantined
	distinctBefore := len(syncer.quarantinedPaths)
	if err := syncer.applyRemoteFile(child, RemoteFile{
		Path:        child,
		Revision:    "rev_child_again",
		ContentType: "application/json",
		Content:     "{\"emoji\":\"tada\"}\n",
	}, nil); err != nil {
		t.Fatalf("re-applying the colliding child should still quarantine, got error: %v", err)
	}
	if got := syncer.state.Counters.PathCollisionQuarantined; got != countBefore+1 {
		t.Fatalf("counter should increment per occurrence: got %d, want %d", got, countBefore+1)
	}
	if got := len(syncer.quarantinedPaths); got != distinctBefore {
		t.Fatalf("distinct quarantined paths should not grow on a repeat: got %d, want %d", got, distinctBefore)
	}
}

func TestApplyRemoteFile_NestedIndexAndLayout(t *testing.T) {
	t.Parallel()

	localDir := t.TempDir()
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID: "ws_nested_indexes",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	cases := []struct {
		remotePath string
		revision   string
		content    string
	}{
		{
			remotePath: "/linear/issues/_index.json",
			revision:   "rev_linear",
			content:    "{\n  \"rows\": [{\"title\": \"Bug 106\", \"file\": \"bug-106__issue-1.md\"}]\n}\n",
		},
		{
			remotePath: "/github/repos/_index.json",
			revision:   "rev_github",
			content:    "{\n  \"rows\": [{\"title\": \"relayfile\", \"file\": \"relayfile__repo-1.md\"}]\n}\n",
		},
	}

	for _, tc := range cases {
		if err := syncer.applyRemoteFile(tc.remotePath, RemoteFile{
			Path:        tc.remotePath,
			Revision:    tc.revision,
			ContentType: "application/json",
			Content:     tc.content,
		}, nil); err != nil {
			t.Fatalf("applyRemoteFile(%s) failed: %v", tc.remotePath, err)
		}
		localPath := filepath.Join(localDir, filepath.FromSlash(strings.TrimPrefix(tc.remotePath, "/")))
		data, err := os.ReadFile(localPath)
		if err != nil {
			t.Fatalf("read %s failed: %v", localPath, err)
		}
		if string(data) != tc.content {
			t.Fatalf("%s content mismatch: got %q, want %q", localPath, string(data), tc.content)
		}
		if got, want := hashBytes(data), hashBytes([]byte(tc.content)); got != want {
			t.Fatalf("%s hash = %s, want %s", localPath, got, want)
		}
		if info, err := os.Stat(filepath.Dir(localPath)); err != nil {
			t.Fatalf("stat %s failed: %v", filepath.Dir(localPath), err)
		} else if !info.IsDir() {
			t.Fatalf("expected %s to be a directory", filepath.Dir(localPath))
		}
	}
}

func TestApplyRemoteSnapshot_PreservesNestedLayoutDotfiles(t *testing.T) {
	t.Parallel()

	localDir := t.TempDir()
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID: "ws_snapshot_layout",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	remoteFiles := map[string]RemoteFile{
		"/notion/.layout.md": {
			Path:        "/notion/.layout.md",
			Revision:    "rev_snapshot_layout",
			ContentType: "text/markdown",
			Content:     "# snapshot layout\n",
		},
		"/notion/pages/_index.json": {
			Path:        "/notion/pages/_index.json",
			Revision:    "rev_snapshot_index",
			ContentType: "application/json",
			Content:     "{\n  \"rows\": []\n}\n",
		},
	}
	if err := syncer.applyRemoteSnapshot(remoteFiles, nil); err != nil {
		t.Fatalf("applyRemoteSnapshot failed: %v", err)
	}

	assertLocalFileContent(t, filepath.Join(localDir, "notion", ".layout.md"), "# snapshot layout\n")
	assertLocalFileContent(t, filepath.Join(localDir, "notion", "pages", "_index.json"), "{\n  \"rows\": []\n}\n")
}

func TestApplyRemoteSnapshot_MaterializesProviderLayouts(t *testing.T) {
	t.Parallel()

	localDir := t.TempDir()
	registrar := &fakeProviderLayoutRegistrar{}
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID:             "ws_snapshot_provider_layouts",
		RemoteRoot:              "/",
		LocalRoot:               localDir,
		ProviderLayoutRegistrar: registrar,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	remoteFiles := map[string]RemoteFile{
		"/linear/issues/AGE-16__issue-1.json": {
			Path:        "/linear/issues/AGE-16__issue-1.json",
			Revision:    "rev_linear_issue",
			ContentType: "application/json",
			Content:     `{"identifier":"AGE-16"}`,
		},
		"/linear/issues/by-state/open/AGE-16__issue-1.json": {
			Path:        "/linear/issues/by-state/open/AGE-16__issue-1.json",
			Revision:    "rev_linear_alias",
			ContentType: "application/json",
			Content:     `{"identifier":"AGE-16"}`,
		},
		"/notion/pages/page-1.md": {
			Path:        "/notion/pages/page-1.md",
			Revision:    "rev_notion_page",
			ContentType: "text/markdown",
			Content:     "# Page",
		},
		"/github/repos/octocat/hello-world/README.md": {
			Path:        "/github/repos/octocat/hello-world/README.md",
			Revision:    "rev_github_readme",
			ContentType: "text/markdown",
			Content:     "# hello-world",
		},
		"/digests/yesterday.md": {
			Path:        "/digests/yesterday.md",
			Revision:    "rev_digest",
			ContentType: "text/markdown",
			Content:     "_no activity_",
		},
		"/.skills/activity-summary.md": {
			Path:        "/.skills/activity-summary.md",
			Revision:    "rev_activity_summary",
			ContentType: "text/markdown",
			Content:     "# activity-summary\n",
		},
		"/.relay/dead-letter/payload.json": {
			Path:        "/.relay/dead-letter/payload.json",
			Revision:    "rev_dead",
			ContentType: "application/json",
			Content:     `{}`,
		},
	}

	if err := syncer.applyRemoteSnapshot(remoteFiles, nil); err != nil {
		t.Fatalf("applyRemoteSnapshot failed: %v", err)
	}

	if got, want := registrar.calls, []string{"github", "linear", "notion"}; !slices.Equal(got, want) {
		t.Fatalf("registered providers = %#v, want %#v", got, want)
	}
	if got, want := registrar.manifest["linear"].Resources, []string{"issues"}; !slices.Equal(got, want) {
		t.Fatalf("linear resources = %#v, want %#v", got, want)
	}
	if got, want := registrar.manifest["github"].Resources, []string{"repos"}; !slices.Equal(got, want) {
		t.Fatalf("github resources = %#v, want %#v", got, want)
	}
	if got, want := registrar.manifest["notion"].Resources, []string{"pages"}; !slices.Equal(got, want) {
		t.Fatalf("notion resources = %#v, want %#v", got, want)
	}
	if got, want := registrar.manifest["linear"].AliasSegments, []string{"by-state"}; !slices.Equal(got, want) {
		t.Fatalf("alias segments = %#v, want %#v", got, want)
	}
	if slices.Contains(registrar.manifest["linear"].AliasSegments, "by-edited") {
		t.Fatalf("linear canonical layout unexpectedly advertised by-edited: %#v", registrar.manifest["linear"].AliasSegments)
	}
	if _, ok := registrar.manifest["digests"]; ok {
		t.Fatalf("reserved digests root should not be registered as a provider")
	}
	if _, ok := registrar.manifest[".relay"]; ok {
		t.Fatalf("reserved .relay root should not be registered as a provider")
	}
	if _, ok := registrar.manifest[".skills"]; ok {
		t.Fatalf("reserved .skills root should not be registered as a provider")
	}
	if _, err := os.Stat(filepath.Join(localDir, "linear", ".layout.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provider layout registration should not write a disk .layout.md, stat err=%v", err)
	}
}

func TestApplyRemoteSnapshot_ProviderLayoutDoesNotAdvertiseAliasesForCanonicalFiles(t *testing.T) {
	t.Parallel()

	localDir := t.TempDir()
	registrar := &fakeProviderLayoutRegistrar{}
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID:             "ws_snapshot_provider_layouts_no_by_edited",
		RemoteRoot:              "/",
		LocalRoot:               localDir,
		ProviderLayoutRegistrar: registrar,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.applyRemoteSnapshot(map[string]RemoteFile{
		"/linear/issues/AGE-16__issue-1.json": {
			Path:        "/linear/issues/AGE-16__issue-1.json",
			Revision:    "rev_linear_issue",
			ContentType: "application/json",
			Content:     `{"identifier":"AGE-16"}`,
		},
	}, nil); err != nil {
		t.Fatalf("applyRemoteSnapshot failed: %v", err)
	}

	manifest, ok := registrar.manifest["linear"]
	if !ok {
		t.Fatalf("expected linear provider layout to be registered")
	}
	if got, want := manifest.Resources, []string{"issues"}; !slices.Equal(got, want) {
		t.Fatalf("linear resources = %#v, want %#v", got, want)
	}
	if len(manifest.AliasSegments) != 0 {
		t.Fatalf("canonical-only provider layout advertised aliases: %#v", manifest.AliasSegments)
	}
}

func TestApplyRemoteSnapshot_ProviderLayoutAdvertisesOnlyObservedAliasesForProvider(t *testing.T) {
	t.Parallel()

	localDir := t.TempDir()
	registrar := &fakeProviderLayoutRegistrar{}
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID:             "ws_snapshot_provider_layouts_observed_by_edited",
		RemoteRoot:              "/",
		LocalRoot:               localDir,
		ProviderLayoutRegistrar: registrar,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.applyRemoteSnapshot(map[string]RemoteFile{
		"/linear/issues/by-id/AGE-16.json": {
			Path:        "/linear/issues/by-id/AGE-16.json",
			Revision:    "rev_linear_by_id",
			ContentType: "application/json",
			Content:     `{"identifier":"AGE-16"}`,
		},
		"/linear/issues/by-state/open/AGE-16__issue-1.json": {
			Path:        "/linear/issues/by-state/open/AGE-16__issue-1.json",
			Revision:    "rev_linear_by_state",
			ContentType: "application/json",
			Content:     `{"identifier":"AGE-16"}`,
		},
		"/notion/pages/by-edited/2026-05-12/page-123__123.json": {
			Path:        "/notion/pages/by-edited/2026-05-12/page-123__123.json",
			Revision:    "rev_notion_by_edited",
			ContentType: "application/json",
			Content:     `{"id":"123"}`,
		},
		"/notion/pages/by-title/Roadmap.json": {
			Path:        "/notion/pages/by-title/Roadmap.json",
			Revision:    "rev_notion_by_title",
			ContentType: "application/json",
			Content:     `{"id":"roadmap"}`,
		},
		"/github/repos/by-name/octocat__hello-world.json": {
			Path:        "/github/repos/by-name/octocat__hello-world.json",
			Revision:    "rev_github_by_name",
			ContentType: "application/json",
			Content:     `{"name":"hello-world"}`,
		},
	}, nil); err != nil {
		t.Fatalf("applyRemoteSnapshot failed: %v", err)
	}

	linearManifest, ok := registrar.manifest["linear"]
	if !ok {
		t.Fatalf("expected linear provider layout to be registered")
	}
	if got, want := linearManifest.AliasSegments, []string{"by-id", "by-state"}; !slices.Equal(got, want) {
		t.Fatalf("linear aliases = %#v, want %#v", got, want)
	}

	notionManifest, ok := registrar.manifest["notion"]
	if !ok {
		t.Fatalf("expected notion provider layout to be registered")
	}
	if got, want := notionManifest.Resources, []string{"pages"}; !slices.Equal(got, want) {
		t.Fatalf("notion resources = %#v, want %#v", got, want)
	}
	if got, want := notionManifest.AliasSegments, []string{"by-edited", "by-title"}; !slices.Equal(got, want) {
		t.Fatalf("notion aliases = %#v, want %#v", got, want)
	}

	githubManifest, ok := registrar.manifest["github"]
	if !ok {
		t.Fatalf("expected github provider layout to be registered")
	}
	if got, want := githubManifest.AliasSegments, []string{"by-name"}; !slices.Equal(got, want) {
		t.Fatalf("github aliases = %#v, want %#v", got, want)
	}
}

func TestApplyRemoteSnapshot_RemoteLayoutPassthroughStillRegistersProviderLayout(t *testing.T) {
	t.Parallel()

	localDir := t.TempDir()
	registrar := &fakeProviderLayoutRegistrar{}
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID:             "ws_snapshot_layout_passthrough",
		RemoteRoot:              "/",
		LocalRoot:               localDir,
		ProviderLayoutRegistrar: registrar,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	remoteFiles := map[string]RemoteFile{
		"/notion/.layout.md": {
			Path:        "/notion/.layout.md",
			Revision:    "rev_snapshot_layout",
			ContentType: "text/markdown",
			Content:     "# remote-authored\n",
		},
		"/notion/pages/page-1.md": {
			Path:        "/notion/pages/page-1.md",
			Revision:    "rev_page",
			ContentType: "text/markdown",
			Content:     "# Page",
		},
	}
	if err := syncer.applyRemoteSnapshot(remoteFiles, nil); err != nil {
		t.Fatalf("applyRemoteSnapshot failed: %v", err)
	}

	assertLocalFileContent(t, filepath.Join(localDir, "notion", ".layout.md"), "# remote-authored\n")
	manifest, ok := registrar.manifest["notion"]
	if !ok {
		t.Fatalf("expected notion provider layout to be registered")
	}
	if got, want := manifest.Resources, []string{"pages"}; !slices.Equal(got, want) {
		t.Fatalf("notion resources = %#v, want %#v", got, want)
	}
}

func TestApplyRemoteSnapshot_ProviderLayoutsNoRegistrarNoOp(t *testing.T) {
	t.Parallel()

	localDir := t.TempDir()
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID: "ws_snapshot_provider_layouts_no_registrar",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.applyRemoteSnapshot(map[string]RemoteFile{
		"/linear/issues/AGE-16__issue-1.json": {
			Path:        "/linear/issues/AGE-16__issue-1.json",
			Revision:    "rev_linear_issue",
			ContentType: "application/json",
			Content:     `{"identifier":"AGE-16"}`,
		},
	}, nil); err != nil {
		t.Fatalf("applyRemoteSnapshot without registrar failed: %v", err)
	}
}

func TestApplyRemoteSnapshot_ProviderLayoutsSkipReservedRoots(t *testing.T) {
	t.Parallel()

	localDir := t.TempDir()
	registrar := &fakeProviderLayoutRegistrar{}
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID:             "ws_snapshot_provider_layouts_reserved",
		RemoteRoot:              "/",
		LocalRoot:               localDir,
		ProviderLayoutRegistrar: registrar,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.applyRemoteSnapshot(map[string]RemoteFile{
		"/.relay/dead-letter/payload.json": {
			Path:        "/.relay/dead-letter/payload.json",
			Revision:    "rev_dead_letter",
			ContentType: "application/json",
			Content:     `{}`,
		},
		"/digests/yesterday.md": {
			Path:        "/digests/yesterday.md",
			Revision:    "rev_digest",
			ContentType: "text/markdown",
			Content:     "_no activity_",
		},
		"/.skills/activity-summary.md": {
			Path:        "/.skills/activity-summary.md",
			Revision:    "rev_activity_summary",
			ContentType: "text/markdown",
			Content:     "# activity-summary\n",
		},
		"/_index.json": {
			Path:        "/_index.json",
			Revision:    "rev_index",
			ContentType: "application/json",
			Content:     `{"rows":[]}`,
		},
		"/LAYOUT.md": {
			Path:        "/LAYOUT.md",
			Revision:    "rev_layout",
			ContentType: "text/markdown",
			Content:     "# Layout\n",
		},
		"/.relayfile-mount-state.json": {
			Path:        "/.relayfile-mount-state.json",
			Revision:    "rev_state",
			ContentType: "application/json",
			Content:     `{}`,
		},
	}, nil); err != nil {
		t.Fatalf("applyRemoteSnapshot failed: %v", err)
	}

	if len(registrar.calls) != 0 {
		t.Fatalf("reserved roots registered provider layouts: %#v", registrar.calls)
	}
}

func TestApplyRemoteSnapshot_ProviderLayoutsUseNonRootProvider(t *testing.T) {
	t.Parallel()

	localDir := t.TempDir()
	registrar := &fakeProviderLayoutRegistrar{}
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID:             "ws_snapshot_provider_layouts_subroot",
		RemoteRoot:              "/notion",
		LocalRoot:               localDir,
		ProviderLayoutRegistrar: registrar,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.applyRemoteSnapshot(map[string]RemoteFile{
		"/notion/pages/page-1.md": {
			Path:        "/notion/pages/page-1.md",
			Revision:    "rev_page",
			ContentType: "text/markdown",
			Content:     "# Page",
		},
	}, nil); err != nil {
		t.Fatalf("applyRemoteSnapshot failed: %v", err)
	}

	if got, want := registrar.calls, []string{"notion"}; !slices.Equal(got, want) {
		t.Fatalf("registered providers = %#v, want %#v", got, want)
	}
	if got, want := registrar.manifest["notion"].Resources, []string{"pages"}; !slices.Equal(got, want) {
		t.Fatalf("notion resources = %#v, want %#v", got, want)
	}
	if _, ok := registrar.manifest["pages"]; ok {
		t.Fatalf("remote root child should not be registered as a provider")
	}
}

func TestProviderLayoutPartsUsesResourceFromNonRootRemoteRoot(t *testing.T) {
	provider, resource, ok := providerLayoutParts("/github/repos", "/github/repos/octocat/hello-world/README.md")
	if !ok {
		t.Fatalf("expected provider layout parts")
	}
	if provider != "github" {
		t.Fatalf("provider = %q, want github", provider)
	}
	if resource != "repos" {
		t.Fatalf("resource = %q, want repos", resource)
	}
}

func TestProviderLayoutPartsKeepsResourceForByEditedAliases(t *testing.T) {
	provider, resource, ok := providerLayoutParts("/", "/notion/pages/by-edited/2026-05-12/page-123__123.json")
	if !ok {
		t.Fatalf("expected provider layout parts")
	}
	if provider != "notion" {
		t.Fatalf("provider = %q, want notion", provider)
	}
	if resource != "pages" {
		t.Fatalf("resource = %q, want pages", resource)
	}
}

func TestApplyWebSocketEvent_PreservesNestedLayoutDotfiles(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/.layout.md": {
				Path:        "/notion/.layout.md",
				Revision:    "rev_ws_layout",
				ContentType: "text/markdown",
				Content:     "# websocket layout\n",
			},
		},
	}

	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_event_layout",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.applyWebSocketEvent(context.Background(), websocketEvent{
		Type:      "file.updated",
		Path:      "/notion/.layout.md",
		Timestamp: "2026-05-09T00:00:00Z",
	}); err != nil {
		t.Fatalf("applyWebSocketEvent failed: %v", err)
	}

	assertLocalFileContent(t, filepath.Join(localDir, "notion", ".layout.md"), "# websocket layout\n")
	if client.readFileCalls != 1 {
		t.Fatalf("expected websocket layout update to perform one ReadFile, got %d", client.readFileCalls)
	}
	if strings.TrimSpace(syncer.state.LastSuccessfulReconcileAt) == "" {
		t.Fatalf("expected websocket apply to mark sync success")
	}
	if syncer.state.LastError != nil {
		t.Fatalf("expected websocket apply to clear last error, got %#v", syncer.state.LastError)
	}
	if got := syncer.state.LastEventAt; got != "2026-05-09T00:00:00Z" {
		t.Fatalf("expected websocket timestamp to be preserved, got %q", got)
	}
}

func TestApplyWebSocketEvent_DirectoryCreatedTriggersProviderLayout(t *testing.T) {
	t.Parallel()

	registrar := &fakeProviderLayoutRegistrar{}
	localDir := t.TempDir()
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID:             "ws_event_provider_layout",
		RemoteRoot:              "/",
		LocalRoot:               localDir,
		ProviderLayoutRegistrar: registrar,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.applyWebSocketEvent(context.Background(), websocketEvent{
		Type:      "directory.created",
		Path:      "/linear/",
		Timestamp: "2026-05-09T01:02:03Z",
	}); err != nil {
		t.Fatalf("applyWebSocketEvent failed: %v", err)
	}

	manifest, ok := registrar.manifest["linear"]
	if !ok {
		t.Fatalf("expected linear provider layout to be registered")
	}
	if manifest.Provider != "linear" {
		t.Fatalf("manifest provider = %q, want linear", manifest.Provider)
	}
	if len(manifest.Resources) != 0 {
		t.Fatalf("directory event manifest resources = %#v, want empty", manifest.Resources)
	}
	if strings.TrimSpace(syncer.state.LastSuccessfulReconcileAt) == "" {
		t.Fatalf("expected directory-created apply to mark sync success")
	}
	if got := syncer.state.LastEventAt; got != "2026-05-09T01:02:03Z" {
		t.Fatalf("expected websocket timestamp to be preserved, got %q", got)
	}
}

func TestScanLocalFilesSkipsSymlinkedDirectories(t *testing.T) {
	t.Parallel()

	localDir := t.TempDir()
	targetDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(targetDir, "ignored.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("seed target dir failed: %v", err)
	}
	if err := os.Symlink(targetDir, filepath.Join(localDir, "node_modules_link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "note.md"), []byte("# ok"), 0o644); err != nil {
		t.Fatalf("seed local file failed: %v", err)
	}

	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID: "ws_scan_symlink_dir",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	files, err := syncer.scanLocalFiles()
	if err != nil {
		t.Fatalf("scanLocalFiles failed: %v", err)
	}
	if _, ok := files["/note.md"]; !ok {
		t.Fatalf("expected regular file to be scanned, got keys %#v", files)
	}
	if got := files["/note.md"]; len(got.RawContent) != 0 || got.WireContent != "" {
		t.Fatalf("expected scanLocalFiles to defer content reads, got raw=%d wire=%q", len(got.RawContent), got.WireContent)
	}
	if _, ok := files["/node_modules_link"]; ok {
		t.Fatalf("expected symlinked directory to be skipped")
	}
}

func TestScanLocalFilesSkipsNestedMountRuntimeState(t *testing.T) {
	t.Parallel()

	localDir := t.TempDir()
	realPath := filepath.Join(localDir, "slack", "channels", "C123", "messages", "1780145510_376649.json")
	if err := os.MkdirAll(filepath.Dir(realPath), 0o755); err != nil {
		t.Fatalf("mkdir real message dir: %v", err)
	}
	if err := os.WriteFile(realPath, []byte(`{"text":"hello"}`), 0o644); err != nil {
		t.Fatalf("write real message: %v", err)
	}
	runtimePath := filepath.Join(localDir, "slack", "channels", "C123", "messages", ".relay", "state.json")
	if err := os.MkdirAll(filepath.Dir(runtimePath), 0o755); err != nil {
		t.Fatalf("mkdir nested .relay: %v", err)
	}
	if err := os.WriteFile(runtimePath, []byte(`{"pendingWriteback":200}`), 0o644); err != nil {
		t.Fatalf("write nested .relay state: %v", err)
	}

	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID: "ws_scan_nested_relay",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	files, err := syncer.scanLocalFiles()
	if err != nil {
		t.Fatalf("scanLocalFiles failed: %v", err)
	}

	if _, ok := files["/slack/channels/C123/messages/1780145510_376649.json"]; !ok {
		t.Fatalf("expected real provider file to be scanned, got keys %#v", files)
	}
	if _, ok := files["/slack/channels/C123/messages/.relay/state.json"]; ok {
		t.Fatalf("nested .relay/state.json must not be scanned as provider content")
	}
}

func TestLowMemoryPublicStateOmitsPerFileDetails(t *testing.T) {
	t.Parallel()

	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "new.md"), []byte("# local"), 0o644); err != nil {
		t.Fatalf("seed local file failed: %v", err)
	}
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID: "ws_low_memory_public_state",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
		LowMemory:   boolPtr(true),
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	syncer.state.Files["/tracked.md"] = trackedFile{
		Revision:    "rev_1",
		ContentType: "text/markdown",
		Hash:        hashString("# tracked"),
		Dirty:       true,
	}
	syncer.state.Files["/clean.md"] = trackedFile{
		Revision:    "rev_2",
		ContentType: "text/markdown",
		Hash:        hashString("# clean"),
	}

	if err := syncer.saveState(); err != nil {
		t.Fatalf("save state failed: %v", err)
	}

	state := readPublicState(t, localDir)
	if !state.LowMemory {
		t.Fatalf("expected public state to record low-memory mode")
	}
	if len(state.Files) != 0 {
		t.Fatalf("expected low-memory public state to omit per-file details, got %#v", state.Files)
	}
	if state.PendingWriteback != 1 {
		t.Fatalf("expected only tracked dirty file to count as pending, got %d", state.PendingWriteback)
	}
	if !state.States.HasPendingWriteback {
		t.Fatalf("expected dirty tracked file to keep pending writeback flag set")
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

// TestAtomicTempPatternHidesTempForDotPrefixedTarget pins the bug where
// writeFileAtomic produced a double-dot-prefixed temp pattern for a
// dot-prefixed target ("..relayfile-mount-state.json.tmp-*"). The
// watcher's exact-name skip missed those temps, so it raced its own
// state writes.
//
// The end-to-end TestWriteFileAtomicReplacesExistingFile above can't
// observe this regression because the temp file is already gone by the
// time the test reads the directory. Testing the pure pattern function
// directly keeps the assertion deterministic.
func TestAtomicTempPatternHidesTempForDotPrefixedTarget(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{
			name: "dot prefixed target uses single dot",
			path: "/x/.relayfile-mount-state.json",
			want: ".relayfile-mount-state.json.tmp-*",
		},
		{
			name: "regular file gets a leading dot to stay hidden",
			path: "/x/notes.md",
			want: ".notes.md.tmp-*",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := atomicTempPattern(tc.path)
			if strings.HasPrefix(got, "..") {
				t.Fatalf("temp pattern used a double-dot prefix: %q", got)
			}
			if got != tc.want {
				t.Fatalf("unexpected temp pattern: got %q, want %q", got, tc.want)
			}
		})
	}
}

type fakeClient struct {
	mu                             sync.Mutex
	files                          map[string]RemoteFile
	events                         []FilesystemEvent
	revisionCounter                int
	eventCounter                   int
	listTreeCalls                  int
	listEventsCalls                int
	latestEventIDCalls             int
	latestEventIDErr               error
	latestEventIDHook              func(call int) (string, error)
	latestEventIDUnsupported       bool
	readFileCalls                  int
	readFileCallsByPath            map[string]int
	writeFileCalls                 int
	bulkWriteCalls                 int
	bulkWriteBatches               [][]BulkWriteFile
	lastBulkWriteResponse          BulkWriteResponse
	bulkWriteResponseFunc          func(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error)
	bulkWriteResponseFuncOwnsWrite bool
	operations                     map[string]OperationStatus
	getOperationCalls              int
	deleteCalls                    []deleteCall
	eventsUnsupported              bool
	listEventsErrAfter             int
	listEventsErr                  error
	listEventsHook                 func(call int, cursor string, limit int)
	listEventsNextCursorByCall     map[int]string
	eventCursorAliases             map[string]string
	readFileErrAfter               int
	readFileErr                    error
}

// requestedReadCalls returns the cumulative number of ReadFile calls made
// against this fake client. Used by tests asserting that quiet reconcile
// cycles do not perform per-file remote reads.
func (c *fakeClient) requestedReadCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readFileCalls
}

type deleteCall struct {
	Path         string
	BaseRevision string
}

type fakeExportClient struct {
	*fakeClient
	exportCalls   int
	tarCalls      int
	tarFiles      map[string][]byte
	tarErr        error
	readFileCalls int
	exportErr     error
	// exportBlockUntilCancel makes ExportFiles block until its ctx is
	// cancelled, simulating a slow atomic export that exceeds the export
	// sub-deadline (or the bootstrap watchdog).
	exportBlockUntilCancel bool
	// exportReturnsEmpty makes ExportFiles return a successful but empty
	// slice regardless of the populated tree, simulating the production
	// empty-200 export for a workspace that DOES have records.
	exportReturnsEmpty bool
}

func (c *fakeExportClient) ExportFiles(ctx context.Context, workspaceID, path string) ([]RemoteFile, error) {
	_ = workspaceID
	c.exportCalls++
	if c.exportBlockUntilCancel {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if c.exportErr != nil {
		return nil, c.exportErr
	}
	if c.exportReturnsEmpty {
		return []RemoteFile{}, nil
	}
	base := normalizeRemotePath(path)
	files := make([]RemoteFile, 0, len(c.files))
	for remotePath, file := range c.files {
		if !isUnderRemoteRoot(base, remotePath) {
			continue
		}
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func (c *fakeExportClient) ReadFile(ctx context.Context, workspaceID, path string) (RemoteFile, error) {
	c.readFileCalls++
	return c.fakeClient.ReadFile(ctx, workspaceID, path)
}

func (c *fakeExportClient) ExportGithubWorkingTreeTar(ctx context.Context, workspaceID string, seed GithubWorkingTreeSeedRequest) (GithubWorkingTreeTar, error) {
	_ = ctx
	_ = workspaceID
	_ = seed
	c.tarCalls++
	if c.tarErr != nil {
		return GithubWorkingTreeTar{}, c.tarErr
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	names := make([]string, 0, len(c.tarFiles))
	for name := range c.tarFiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data := c.tarFiles[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
			return GithubWorkingTreeTar{}, err
		}
		if _, err := tw.Write(data); err != nil {
			return GithubWorkingTreeTar{}, err
		}
	}
	if err := tw.Close(); err != nil {
		return GithubWorkingTreeTar{}, err
	}
	return GithubWorkingTreeTar{
		Body:        io.NopCloser(bytes.NewReader(buf.Bytes())),
		ContentType: "application/x-tar",
	}, nil
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
			Path:        remotePath,
			Type:        "file",
			Revision:    file.Revision,
			ContentHash: file.ContentHash,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return TreeResponse{
		Path:       base,
		Entries:    entries,
		NextCursor: nil,
	}, nil
}

func (c *fakeClient) LatestEventID(ctx context.Context, workspaceID, provider string) (string, error) {
	_ = ctx
	_ = workspaceID
	_ = provider
	c.mu.Lock()
	defer c.mu.Unlock()
	c.latestEventIDCalls++
	if c.latestEventIDHook != nil {
		return c.latestEventIDHook(c.latestEventIDCalls)
	}
	if c.latestEventIDUnsupported {
		return "", &HTTPError{StatusCode: http.StatusBadRequest, Code: "bad_request", Message: "direction=desc unsupported"}
	}
	if c.latestEventIDErr != nil {
		return "", c.latestEventIDErr
	}
	if c.eventsUnsupported {
		return "", &HTTPError{StatusCode: 404, Code: "not_found", Message: "not found"}
	}
	if len(c.events) == 0 {
		return "", nil
	}
	return c.events[len(c.events)-1].EventID, nil
}

func (c *fakeClient) ListEvents(ctx context.Context, workspaceID, provider, cursor string, limit int) (EventFeed, error) {
	_ = ctx
	_ = workspaceID
	_ = provider
	c.listEventsCalls++
	if c.listEventsHook != nil {
		c.listEventsHook(c.listEventsCalls, cursor, limit)
	}
	if c.eventsUnsupported {
		return EventFeed{}, &HTTPError{StatusCode: 404, Code: "not_found", Message: "not found"}
	}
	if c.listEventsErr != nil && c.listEventsCalls > c.listEventsErrAfter {
		return EventFeed{}, c.listEventsErr
	}
	if limit <= 0 {
		limit = 200
	}
	start := 0
	if cursor != "" {
		searchCursor := cursor
		if c.eventCursorAliases != nil {
			if aliased, ok := c.eventCursorAliases[cursor]; ok {
				searchCursor = aliased
			}
		}
		for i := range c.events {
			if c.events[i].EventID == searchCursor {
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
		if c.listEventsNextCursorByCall != nil {
			if override, ok := c.listEventsNextCursorByCall[c.listEventsCalls]; ok {
				next = override
			}
		}
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
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readFileCalls++
	path = normalizeRemotePath(path)
	if c.readFileCallsByPath == nil {
		c.readFileCallsByPath = make(map[string]int)
	}
	c.readFileCallsByPath[path]++
	if c.readFileErr != nil && c.readFileCalls > c.readFileErrAfter {
		return RemoteFile{}, c.readFileErr
	}
	file, ok := c.files[path]
	if !ok {
		return RemoteFile{}, &HTTPError{StatusCode: 404, Code: "not_found", Message: "not found"}
	}
	return file, nil
}

func (c *fakeClient) WriteFile(ctx context.Context, workspaceID, path, baseRevision, contentType, content string) (WriteResult, error) {
	_ = ctx
	_ = workspaceID
	c.writeFileCalls++
	if c.files == nil {
		c.files = make(map[string]RemoteFile)
	}
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
		Encoding:    "",
	}
	c.appendEvent(eventType, path, revision)
	return WriteResult{TargetRevision: revision}, nil
}

func (c *fakeClient) WriteFilesBulk(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
	_ = workspaceID
	if len(files) == 0 {
		return BulkWriteResponse{}, ErrEmptyBulkWrite
	}
	if c.files == nil {
		c.files = make(map[string]RemoteFile)
	}

	c.bulkWriteCalls++
	batch := append([]BulkWriteFile(nil), files...)
	c.bulkWriteBatches = append(c.bulkWriteBatches, batch)

	response := BulkWriteResponse{}
	var err error
	if c.bulkWriteResponseFunc != nil {
		response, err = c.bulkWriteResponseFunc(ctx, workspaceID, batch)
		if err != nil {
			return BulkWriteResponse{}, err
		}
		if c.bulkWriteResponseFuncOwnsWrite {
			c.lastBulkWriteResponse = response
			return response, nil
		}
	}

	errorPaths := make(map[string]BulkWriteError, len(response.Errors))
	for _, writeErr := range response.Errors {
		errorPaths[normalizeRemotePath(writeErr.Path)] = writeErr
	}

	written := 0
	results := make([]BulkWriteResult, 0, len(batch))
	for _, file := range batch {
		path := normalizeRemotePath(file.Path)
		if _, failed := errorPaths[path]; failed {
			continue
		}
		contentType := strings.TrimSpace(file.ContentType)
		if contentType == "" {
			contentType = "text/markdown"
		}
		current, exists := c.files[path]
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
			Content:     file.Content,
			Encoding:    strings.TrimSpace(file.Encoding),
		}
		results = append(results, BulkWriteResult{
			Path:        path,
			Revision:    revision,
			ContentType: contentType,
		})
		c.appendEvent(eventType, path, revision)
		written++
		_ = current
	}

	response.Written = written
	if response.ErrorCount == 0 && len(response.Errors) > 0 {
		response.ErrorCount = len(response.Errors)
	}
	if len(response.Results) == 0 {
		response.Results = results
	}
	c.lastBulkWriteResponse = response
	return response, nil
}

func (c *fakeClient) GetOperation(ctx context.Context, workspaceID, opID string) (OperationStatus, error) {
	_ = ctx
	_ = workspaceID
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getOperationCalls++
	op, ok := c.operations[opID]
	if !ok {
		return OperationStatus{}, &HTTPError{StatusCode: 404, Code: "not_found", Message: "not found"}
	}
	return op, nil
}

func (c *fakeClient) DeleteFile(ctx context.Context, workspaceID, path, baseRevision string) error {
	_ = ctx
	_ = workspaceID
	path = normalizeRemotePath(path)
	c.deleteCalls = append(c.deleteCalls, deleteCall{
		Path:         path,
		BaseRevision: baseRevision,
	})
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
	revisionCounter := len(normalizedFiles)

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
		case strings.HasSuffix(r.URL.Path, "/fs/bulk") && r.Method == http.MethodPost:
			var payload struct {
				Files []BulkWriteFile `json:"files"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				writeJSONResponse(t, w, http.StatusBadRequest, map[string]any{
					"code":    "bad_request",
					"message": "invalid json",
				})
				return
			}
			if len(payload.Files) == 0 {
				writeJSONResponse(t, w, http.StatusBadRequest, map[string]any{
					"code":    "bad_request",
					"message": "missing files",
				})
				return
			}
			errorsOut := make([]BulkWriteError, 0)
			resultsOut := make([]BulkWriteResult, 0, len(payload.Files))
			written := 0
			for _, file := range payload.Files {
				path := normalizeRemotePath(file.Path)
				if _, denied := normalizedWriteDenied[path]; denied {
					errorsOut = append(errorsOut, BulkWriteError{
						Path:    path,
						Code:    "forbidden",
						Message: "denied",
					})
					continue
				}
				current := normalizedFiles[path]
				current.Path = path
				current.Content = file.Content
				if strings.TrimSpace(file.ContentType) != "" {
					current.ContentType = file.ContentType
				} else if current.ContentType == "" {
					current.ContentType = "text/markdown"
				}
				current.Encoding = strings.TrimSpace(file.Encoding)
				revisionCounter++
				current.Revision = fmt.Sprintf("rev_%d", revisionCounter)
				normalizedFiles[path] = current
				resultsOut = append(resultsOut, BulkWriteResult{
					Path:        path,
					Revision:    current.Revision,
					ContentType: current.ContentType,
				})
				written++
			}
			writeJSONResponse(t, w, http.StatusAccepted, BulkWriteResponse{
				Written:       written,
				ErrorCount:    len(errorsOut),
				Errors:        errorsOut,
				Results:       resultsOut,
				CorrelationID: "corr_mock_bulk",
			})
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
				Encoding    string `json:"encoding"`
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
			file.Encoding = strings.TrimSpace(payload.Encoding)
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

func readPublicState(t *testing.T, localDir string) publicState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(localDir, ".relay", "state.json"))
	if err != nil {
		t.Fatalf("read public state failed: %v", err)
	}
	var state publicState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("unmarshal public state failed: %v", err)
	}
	return state
}

func readPendingOutboxRecordsForTest(t *testing.T, localDir string) []outboxRecord {
	t.Helper()
	return readOutboxRecordsInDirForTest(t, filepath.Join(localDir, ".relay", "outbox", "pending"))
}

func readOutboxRecordsInDirForTest(t *testing.T, dir string) []outboxRecord {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("read outbox dir %s failed: %v", dir, err)
	}
	records := make([]outboxRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read outbox record failed: %v", err)
		}
		var record outboxRecord
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatalf("unmarshal outbox record failed: %v", err)
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CommandID < records[j].CommandID })
	return records
}

func forcePendingOutboxDueForTest(t *testing.T, localDir string) outboxRecord {
	t.Helper()
	records := readPendingOutboxRecordsForTest(t, localDir)
	if len(records) != 1 {
		t.Fatalf("expected one pending outbox record, got %+v", records)
	}
	record := records[0]
	record.NextAttemptAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	record.NeedsAttention = false
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal outbox record failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, ".relay", "outbox", "pending", record.CommandID+".json"), data, 0o644); err != nil {
		t.Fatalf("write outbox record failed: %v", err)
	}
	return record
}

func assertLocalFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read local file %s failed: %v", path, err)
	}
	if !bytes.Equal(data, want) {
		t.Fatalf("expected %s to contain %v, got %v", path, want, data)
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

const mountsyncTestJWTKID = "mountsync-test-kid"

var (
	mountsyncTestJWKSOnce   sync.Once
	mountsyncTestJWKSURL    string
	mountsyncTestPrivateKey *rsa.PrivateKey
)

func ensureMountsyncJWTVerifier(t *testing.T) {
	t.Helper()

	mountsyncTestJWKSOnce.Do(func() {
		privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(fmt.Sprintf("generate rsa key: %v", err))
		}
		mountsyncTestPrivateKey = privateKey

		jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			exponent := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.PublicKey.E)).Bytes())
			_ = json.NewEncoder(w).Encode(map[string]any{
				"keys": []map[string]any{
					{
						"kid": mountsyncTestJWTKID,
						"kty": "RSA",
						"alg": "RS256",
						"use": "sig",
						"n":   base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes()),
						"e":   exponent,
					},
				},
			})
		}))
		mountsyncTestJWKSURL = jwksServer.URL
	})
}

func newMountsyncAPIHandler(t *testing.T, store *relayfile.Store) http.Handler {
	t.Helper()
	ensureMountsyncJWTVerifier(t)

	handler, err := httpapi.NewServerWithConfig(store, httpapi.ServerConfig{
		JWKSURL:          mountsyncTestJWKSURL,
		JWKSFetchTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("new mountsync api handler: %v", err)
	}
	return handler
}

func mustMountsyncTestJWT(t *testing.T, secret, workspaceID, agentName string, scopes []string, exp time.Time) string {
	t.Helper()
	_ = secret
	ensureMountsyncJWTVerifier(t)

	headerBytes, err := json.Marshal(map[string]any{
		"alg": "RS256",
		"typ": "JWT",
		"kid": mountsyncTestJWTKID,
	})
	if err != nil {
		t.Fatalf("marshal jwt header: %v", err)
	}
	payloadBytes, err := json.Marshal(map[string]any{
		"wks":          workspaceID,
		"workspace_id": workspaceID,
		"sub":          agentName,
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

	sum := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, mountsyncTestPrivateKey, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
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

func (c *fakeClient) appendEventWithHash(eventType, path, revision, contentHash string) {
	c.eventCounter++
	c.events = append(c.events, FilesystemEvent{
		EventID:     fmt.Sprintf("evt_%d", c.eventCounter),
		Type:        eventType,
		Path:        path,
		Revision:    revision,
		ContentHash: contentHash,
	})
}

// TestPullDetectsRevReuseViaContentHash exercises end-to-end recovery when
// the cloud reuses a revision identifier with new content. file.updated
// events are unconditionally added to the changed set today, so this test
// would still pass even with the ContentHash cross-check removed; what it
// validates is that applyRemoteFile re-hashes content and overwrites stale
// local data when the cloud serves divergent bytes under a reused rev.
// The ContentHash cross-check itself is a logging hook — see syncer.go
// line ~1640 — which surfaces the rev-reuse anomaly to operators without
// changing the changed-set membership.
func TestPullDetectsRevReuseViaContentHash(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_96",
				ContentType: "text/markdown",
				Content:     "# A original",
			},
		},
		events: []FilesystemEvent{
			{
				EventID:  "evt_1",
				Type:     "file.created",
				Path:     "/notion/Docs/A.md",
				Revision: "rev_96",
			},
		},
		revisionCounter: 96,
		eventCounter:    1,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_rev_reuse",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		// Disable periodic full pull so we isolate the cross-check path.
		FullPullEvery: -1,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}
	localFile := filepath.Join(localDir, "Docs", "A.md")
	if data, err := os.ReadFile(localFile); err != nil || string(data) != "# A original" {
		t.Fatalf("expected initial content mirrored, got %q err=%v", string(data), err)
	}

	// Simulate the cloud bug: rev_96 is reused, but the content has
	// changed. The events feed surfaces the new ContentHash so the daemon
	// can detect the drift even though tracked.Revision == event.Revision.
	newContent := "# A reused-rev"
	newHash := hashBytes([]byte(newContent))
	client.files["/notion/Docs/A.md"] = RemoteFile{
		Path:        "/notion/Docs/A.md",
		Revision:    "rev_96", // intentionally reused
		ContentType: "text/markdown",
		Content:     newContent,
		ContentHash: newHash,
	}
	client.appendEventWithHash("file.updated", "/notion/Docs/A.md", "rev_96", newHash)

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("incremental sync after rev reuse failed: %v", err)
	}

	data, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatalf("read local after rev reuse: %v", err)
	}
	if string(data) != newContent {
		t.Fatalf("expected ContentHash divergence to trigger re-fetch; got %q want %q", string(data), newContent)
	}
}

func TestPullRemoteIncrementalSkipsReadFileWhenLocalHashMatchesContentHash(t *testing.T) {
	content := "# A"
	remotePath := "/notion/Docs/A.md"
	contentHash := hashString(content)
	client := &fakeClient{
		files: map[string]RemoteFile{
			remotePath: {
				Path:        remotePath,
				Revision:    "rev_2",
				ContentType: "text/markdown",
				Content:     content,
				ContentHash: contentHash,
			},
		},
		events: []FilesystemEvent{
			{
				EventID:     "evt_1",
				Type:        "file.updated",
				Path:        remotePath,
				Revision:    "rev_2",
				ContentHash: contentHash,
			},
		},
		revisionCounter: 2,
		eventCounter:    1,
	}
	localDir := t.TempDir()
	localPath := filepath.Join(localDir, "Docs", "A.md")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir local dir failed: %v", err)
	}
	if err := os.WriteFile(localPath, []byte(content), 0o644); err != nil {
		t.Fatalf("seed local file failed: %v", err)
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:      "ws_incremental_skip_hash",
		RemoteRoot:       "/notion",
		LocalRoot:        localDir,
		FullPullEvery:    -1,
		WebSocket:        boolPtr(false),
		CursorTimeout:    time.Second,
		BootstrapTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	syncer.loaded = true
	syncer.state = mountState{
		Files: map[string]trackedFile{
			remotePath: {
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Hash:        contentHash,
			},
		},
		EventsCursor:      "evt_0",
		BootstrapComplete: true,
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("incremental sync failed: %v", err)
	}

	if got := client.requestedReadCalls(); got != 0 {
		t.Fatalf("expected matching incremental contentHash to skip ReadFile, got %d read(s)", got)
	}
	tracked := syncer.state.Files[remotePath]
	if tracked.Revision != "rev_2" || tracked.Hash != contentHash || tracked.Dirty {
		t.Fatalf("unexpected tracked state after incremental skip: %+v", tracked)
	}
	assertLocalFileContent(t, localPath, content)
}

// TestPullPeriodicFullCycle asserts that every Nth incremental cycle, a
// full tree pull is forced even when the events cursor is healthy. This is
// the "trust but verify" mitigation for environments where the cloud has
// not yet been updated to surface ContentHash. The full pull self-heals
// any stale state because applyRemoteFile re-hashes content and overwrites
// when on-disk hashes diverge.
//
// Note: each post-bootstrap cycle in this test appends a fresh event so
// that the skip-if-no-events short-circuit (see pullRemote) does not
// suppress incremental pulls. The periodic full-pull cadence is gated on
// non-empty cycles; that is by design — when nothing has happened, there
// is nothing to verify, and forcing a full-tree fetch on a quiet
// workspace was the cause of the production reconcile-stall failure mode.
func TestPullPeriodicFullCycle(t *testing.T) {
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
	const everyN = 3
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:   "ws_periodic_full",
		RemoteRoot:    "/notion",
		LocalRoot:     localDir,
		FullPullEvery: everyN,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	// First sync bootstraps via full pull (EventsCursor empty).
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("bootstrap sync: %v", err)
	}
	if client.listTreeCalls != 1 {
		t.Fatalf("expected 1 list-tree call after bootstrap, got %d", client.listTreeCalls)
	}

	// Subsequent syncs should run incremental. The Nth one (cycles >= N)
	// should force a full pull. Counter increments before the check, so
	// the Nth incremental call (3rd post-bootstrap sync) triggers the
	// forced pull. Append a fresh event before each cycle so that the
	// skip-if-no-events short-circuit does not suppress the incremental
	// pull and prevent the cadence counter from advancing.
	for i := 1; i < everyN; i++ {
		client.appendEvent("file.updated", "/notion/Docs/A.md", fmt.Sprintf("rev_step_%d", i))
		if err := syncer.SyncOnce(context.Background()); err != nil {
			t.Fatalf("incremental sync %d: %v", i, err)
		}
	}
	if client.listTreeCalls != 1 {
		t.Fatalf("expected list-tree calls to stay at 1 across %d incremental cycles, got %d", everyN-1, client.listTreeCalls)
	}

	// Nth incremental cycle: forces a full tree pull regardless of cursor
	// health. Again, append an event so the cycle is non-empty.
	client.appendEvent("file.updated", "/notion/Docs/A.md", "rev_step_full")
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("forced periodic full pull sync: %v", err)
	}
	if client.listTreeCalls != 2 {
		t.Fatalf("expected periodic full pull to bump list-tree calls to 2, got %d", client.listTreeCalls)
	}

	// Counter resets — next cycle should be incremental again. With no
	// new events, the cycle short-circuits before any incremental work,
	// so list-tree count is unchanged.
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("post-reset incremental sync: %v", err)
	}
	if client.listTreeCalls != 2 {
		t.Fatalf("expected counter reset (no list-tree on cycle after periodic full pull); got %d", client.listTreeCalls)
	}
}

// TestPullShortCircuitsWhenNoNewEvents pins the reconcile-stall fix.
//
// Pre-fix, every reconcile cycle on a workspace past the periodic
// full-pull cadence (defaultFullPullEvery cycles) would fire a
// pullRemoteFullTree, which on workspaces with hundreds of files
// performs N+1 sequential ReadFile calls and routinely exceeds the
// per-cycle 15s deadline (RELAYFILE_MOUNT_TIMEOUT). On Notion-shaped
// workspaces (132 pages), this manifested as repeating log lines:
//
//	mount sync cycle failed: context deadline exceeded
//	mount stalled: no successful reconcile for 10m
//
// Post-fix, pullRemote consults the events feed first with limit=1.
// When ListEvents reports no new events since the cursor, the cycle
// returns immediately without touching ListTree, ExportFiles, or
// ReadFile. Only the cheap ListEvents probe is on the hot path.
func TestPullShortCircuitsWhenNoNewEvents(t *testing.T) {
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
		WorkspaceID: "ws_short_circuit",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		// Disable the periodic full-pull cadence so this test isolates
		// the quiet-cycle short-circuit itself.
		FullPullEvery: -1,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	// Bootstrap. This fires the initial full pull and resolves the
	// events cursor.
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("bootstrap sync: %v", err)
	}
	listTreeAtBootstrap := client.listTreeCalls
	readFileAtBootstrap := client.requestedReadCalls()

	// Reset the events probe counter so we can measure post-bootstrap
	// short-circuit calls in isolation.
	client.listEventsCalls = 0

	// Run several quiet reconcile cycles with periodic full pulls disabled.
	// With no new events on the
	// feed, each cycle should:
	//   1. Issue exactly one ListEvents probe.
	//   2. NOT issue a ListTree call (would block reconcile on huge
	//      workspaces).
	//   3. NOT issue any ReadFile calls.
	//   4. Mark the cycle successful so status does not report a stall.
	const quietCycles = 5
	for i := 0; i < quietCycles; i++ {
		if err := syncer.Reconcile(context.Background()); err != nil {
			t.Fatalf("quiet reconcile %d: %v", i, err)
		}
	}

	if client.listEventsCalls != quietCycles {
		t.Fatalf("expected exactly %d ListEvents probes (one per quiet cycle); got %d",
			quietCycles, client.listEventsCalls)
	}
	if client.listTreeCalls != listTreeAtBootstrap {
		t.Fatalf("expected no full-tree pulls during quiet cycles; list-tree went %d -> %d",
			listTreeAtBootstrap, client.listTreeCalls)
	}
	if got := client.requestedReadCalls(); got != readFileAtBootstrap {
		t.Fatalf("expected no ReadFile calls during quiet cycles; read-file went %d -> %d",
			readFileAtBootstrap, got)
	}

	// Confirm the short-circuit still bumps LastSuccessfulReconcileAt
	// so the daemon's stall detector stays clear.
	status := readPublicState(t, localDir)
	if strings.TrimSpace(status.LastSuccessfulReconcileAt) == "" {
		t.Fatalf("expected short-circuited cycle to mark sync success; got empty LastSuccessfulReconcileAt")
	}

	// And once a real event arrives, the next reconcile must do work
	// (no stale short-circuit).
	client.files["/notion/Docs/A.md"] = RemoteFile{
		Path:        "/notion/Docs/A.md",
		Revision:    "rev_2",
		ContentType: "text/markdown",
		Content:     "# A v2",
	}
	client.appendEvent("file.updated", "/notion/Docs/A.md", "rev_2")
	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile with new event: %v", err)
	}
	if got := client.requestedReadCalls(); got <= readFileAtBootstrap {
		t.Fatalf("expected new event to trigger ReadFile; read-file count %d", got)
	}
	data, err := os.ReadFile(filepath.Join(localDir, "Docs", "A.md"))
	if err != nil {
		t.Fatalf("read mirrored file: %v", err)
	}
	if string(data) != "# A v2" {
		t.Fatalf("expected mirrored file to update after event; got %q", string(data))
	}
}

func TestPullRemoteIncrementalPersistsAppliedPageCursorOnListEventsError(t *testing.T) {
	files := map[string]RemoteFile{}
	pageLimit := defaultIncrementalEventPageLimit
	events := make([]FilesystemEvent, 0, pageLimit+1)
	for i := 1; i <= pageLimit+1; i++ {
		remotePath := fmt.Sprintf("/notion/Docs/%03d.md", i)
		revision := fmt.Sprintf("rev_%03d", i)
		files[remotePath] = RemoteFile{
			Path:        remotePath,
			Revision:    revision,
			ContentType: "text/markdown",
			Content:     fmt.Sprintf("# %03d", i),
		}
		events = append(events, FilesystemEvent{
			EventID:  fmt.Sprintf("evt_%03d", i),
			Type:     "file.created",
			Path:     remotePath,
			Revision: revision,
		})
	}
	localDir := t.TempDir()
	statePath := filepath.Join(localDir, ".relayfile-mount-state.json")
	var checkpointErr error
	var sawResumeCursor bool
	client := &fakeClient{
		files:                      files,
		events:                     events,
		revisionCounter:            pageLimit + 1,
		eventCounter:               pageLimit + 1,
		listEventsErrAfter:         2,
		listEventsErr:              context.DeadlineExceeded,
		listEventsNextCursorByCall: map[int]string{2: "cursor_after_page"},
		eventCursorAliases:         map[string]string{"cursor_after_page": fmt.Sprintf("evt_%03d", pageLimit)},
		listEventsHook: func(call int, cursor string, limit int) {
			if call == 2 && limit != defaultIncrementalEventPageLimit {
				checkpointErr = fmt.Errorf("incremental ListEvents limit = %d, want %d", limit, defaultIncrementalEventPageLimit)
			}
			if call == 3 && cursor == "cursor_after_page" {
				sawResumeCursor = true
			}
			if call != 3 {
				return
			}
			data, err := os.ReadFile(statePath)
			if err != nil {
				checkpointErr = fmt.Errorf("read persisted checkpoint before next page: %w", err)
				return
			}
			var persisted mountState
			if err := json.Unmarshal(data, &persisted); err != nil {
				checkpointErr = fmt.Errorf("unmarshal persisted checkpoint before next page: %w", err)
				return
			}
			if persisted.EventsCursor != "cursor_after_page" {
				checkpointErr = fmt.Errorf("persisted EventsCursor before next page = %q, want cursor_after_page", persisted.EventsCursor)
				return
			}
			if persisted.IncrementalCheckpoint != nil {
				checkpointErr = fmt.Errorf("persisted IncrementalCheckpoint before next page = %#v, want nil", persisted.IncrementalCheckpoint)
			}
		},
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:      "ws_backlog",
		RemoteRoot:       "/notion",
		LocalRoot:        localDir,
		FullPullEvery:    -1,
		WebSocket:        boolPtr(false),
		CursorTimeout:    time.Second,
		BootstrapTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	syncer.loaded = true
	syncer.state = mountState{
		Files:             map[string]trackedFile{},
		EventsCursor:      "evt_000",
		BootstrapComplete: true,
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("expected list-events deadline after first applied page to end cleanly after cursor checkpoint, got %v", err)
	}
	if checkpointErr != nil {
		t.Fatal(checkpointErr)
	}
	if !sawResumeCursor {
		t.Fatalf("expected next ListEvents request to use persisted feed resume cursor")
	}
	if got := syncer.state.EventsCursor; got != "cursor_after_page" {
		t.Fatalf("EventsCursor = %q, want first applied page cursor cursor_after_page", got)
	}
	if syncer.state.LastError != nil {
		t.Fatalf("expected checkpointed deadline to avoid recording a sync error, got %#v", syncer.state.LastError)
	}
	if !syncer.state.IncrementalBacklogDraining {
		t.Fatalf("expected backlog-draining state while event feed still has more pages")
	}
	status := readPublicState(t, localDir)
	if status.Status != "syncing" || !status.States.Syncing {
		t.Fatalf("expected public state to report syncing while draining backlog, got %+v", status)
	}
	if strings.TrimSpace(status.LastSuccessfulReconcileAt) != "" {
		t.Fatalf("partial backlog progress should not advance LastSuccessfulReconcileAt, got %q", status.LastSuccessfulReconcileAt)
	}
	if _, err := os.ReadFile(filepath.Join(localDir, "Docs", fmt.Sprintf("%03d.md", pageLimit))); err != nil {
		t.Fatalf("expected page-one file to be applied before cursor advance: %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(localDir, "Docs", fmt.Sprintf("%03d.md", pageLimit+1))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("event after failed page should not be applied yet; stat err=%v", err)
	}

	client.listEventsErr = nil
	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile after backlog error failed: %v", err)
	}
	wantFinalCursor := fmt.Sprintf("evt_%03d", pageLimit+1)
	if got := syncer.state.EventsCursor; got != wantFinalCursor {
		t.Fatalf("EventsCursor = %q, want final cursor %s", got, wantFinalCursor)
	}
	if syncer.state.IncrementalBacklogDraining {
		t.Fatalf("expected backlog-draining state to clear after reaching feed tail")
	}
	status = readPublicState(t, localDir)
	if status.Status != "ready" || status.States.Syncing {
		t.Fatalf("expected public state to return ready after reaching feed tail, got %+v", status)
	}
	if strings.TrimSpace(status.LastSuccessfulReconcileAt) == "" {
		t.Fatalf("expected completed backlog sync to mark LastSuccessfulReconcileAt")
	}
	data, err := os.ReadFile(filepath.Join(localDir, "Docs", fmt.Sprintf("%03d.md", pageLimit+1)))
	if err != nil {
		t.Fatalf("expected remaining event to apply on retry: %v", err)
	}
	wantContent := fmt.Sprintf("# %03d", pageLimit+1)
	if string(data) != wantContent {
		t.Fatalf("unexpected remaining file content: %q", data)
	}
}

func TestPullRemoteIncrementalReturnsDeadlineWhenNoPageProgress(t *testing.T) {
	client := &fakeClient{
		files:              map[string]RemoteFile{},
		events:             []FilesystemEvent{{EventID: "evt_001", Type: "file.created", Path: "/notion/Docs/001.md", Revision: "rev_001"}},
		listEventsErrAfter: 0,
		listEventsErr:      context.DeadlineExceeded,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:      "ws_no_progress_deadline",
		RemoteRoot:       "/notion",
		LocalRoot:        localDir,
		FullPullEvery:    -1,
		WebSocket:        boolPtr(false),
		CursorTimeout:    time.Second,
		BootstrapTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	syncer.loaded = true
	syncer.state = mountState{
		Files:             map[string]trackedFile{},
		EventsCursor:      "evt_000",
		BootstrapComplete: true,
	}

	err = syncer.Reconcile(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline before page progress to be returned, got %v", err)
	}
	if got := syncer.state.EventsCursor; got != "evt_000" {
		t.Fatalf("EventsCursor = %q, want unchanged evt_000", got)
	}
	if syncer.state.IncrementalBacklogDraining {
		t.Fatalf("deadline before page progress should not mark backlog draining")
	}
	if syncer.state.LastError == nil {
		t.Fatalf("expected no-progress deadline to record LastError")
	}
	status := readPublicState(t, localDir)
	if status.LastError == nil || status.Status == "syncing" || status.States.Syncing {
		t.Fatalf("expected public state to record no-progress error without syncing status, got %+v", status)
	}
}

func TestPullRemoteIncrementalResumesWithinAppliedPage(t *testing.T) {
	files := map[string]RemoteFile{}
	events := make([]FilesystemEvent, 0, 10)
	for i := 1; i <= 10; i++ {
		remotePath := fmt.Sprintf("/notion/Docs/%03d.md", i)
		revision := fmt.Sprintf("rev_%03d", i)
		files[remotePath] = RemoteFile{
			Path:        remotePath,
			Revision:    revision,
			ContentType: "text/markdown",
			Content:     fmt.Sprintf("# %03d", i),
		}
		events = append(events, FilesystemEvent{
			EventID:  fmt.Sprintf("evt_%03d", i),
			Type:     "file.created",
			Path:     remotePath,
			Revision: revision,
		})
	}
	client := &fakeClient{
		files:            files,
		events:           events,
		revisionCounter:  10,
		eventCounter:     10,
		readFileErrAfter: 3,
		readFileErr:      context.DeadlineExceeded,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:      "ws_page_resume",
		RemoteRoot:       "/notion",
		LocalRoot:        localDir,
		FullPullEvery:    -1,
		WebSocket:        boolPtr(false),
		CursorTimeout:    time.Second,
		BootstrapTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	syncer.loaded = true
	syncer.state = mountState{
		Files:             map[string]trackedFile{},
		EventsCursor:      "evt_000",
		BootstrapComplete: true,
	}

	err = syncer.Reconcile(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected read-file deadline inside first page, got %v", err)
	}
	if got := syncer.state.EventsCursor; got != "evt_000" {
		t.Fatalf("EventsCursor should stay on the unapplied page cursor; got %q", got)
	}
	if syncer.state.IncrementalCheckpoint == nil {
		t.Fatalf("expected checkpoint after partial page")
	}
	if checkpoint := *syncer.state.IncrementalCheckpoint; checkpoint.Cursor != "evt_000" ||
		checkpoint.PageCursor != "evt_010" ||
		checkpoint.Phase != "changed" ||
		checkpoint.Path != "/notion/Docs/003.md" {
		t.Fatalf("unexpected checkpoint after partial page: %#v", checkpoint)
	}
	var persisted mountState
	stateBytes, err := os.ReadFile(filepath.Join(localDir, ".relayfile-mount-state.json"))
	if err != nil {
		t.Fatalf("read persisted partial-page checkpoint: %v", err)
	}
	if err := json.Unmarshal(stateBytes, &persisted); err != nil {
		t.Fatalf("unmarshal persisted partial-page checkpoint: %v", err)
	}
	if checkpoint := persisted.IncrementalCheckpoint; checkpoint == nil ||
		checkpoint.Cursor != "evt_000" ||
		checkpoint.PageCursor != "evt_010" ||
		checkpoint.Phase != "changed" ||
		checkpoint.Path != "/notion/Docs/003.md" {
		t.Fatalf("unexpected persisted checkpoint after partial page: %#v", checkpoint)
	}
	for i := 1; i <= 3; i++ {
		path := filepath.Join(localDir, "Docs", fmt.Sprintf("%03d.md", i))
		if _, err := os.ReadFile(path); err != nil {
			t.Fatalf("expected file %03d to be applied before timeout: %v", i, err)
		}
	}

	client.readFileErr = nil
	before := make(map[string]int, len(client.readFileCallsByPath))
	for path, calls := range client.readFileCallsByPath {
		before[path] = calls
	}
	resumedSyncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:      "ws_page_resume",
		RemoteRoot:       "/notion",
		LocalRoot:        localDir,
		FullPullEvery:    -1,
		WebSocket:        boolPtr(false),
		CursorTimeout:    time.Second,
		BootstrapTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("new resumed syncer failed: %v", err)
	}
	if err := resumedSyncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile after partial page failed: %v", err)
	}
	for i := 1; i <= 3; i++ {
		remotePath := fmt.Sprintf("/notion/Docs/%03d.md", i)
		if got := client.readFileCallsByPath[remotePath]; got != before[remotePath] {
			t.Fatalf("expected %s to be skipped on resume; read calls went %d -> %d", remotePath, before[remotePath], got)
		}
	}
	if got := client.readFileCallsByPath["/notion/Docs/004.md"]; got == 0 {
		t.Fatalf("expected resume to continue after checkpoint path")
	}
	if got := resumedSyncer.state.EventsCursor; got != "evt_010" {
		t.Fatalf("EventsCursor = %q, want completed page cursor evt_010", got)
	}
	if checkpoint := resumedSyncer.state.IncrementalCheckpoint; checkpoint != nil {
		t.Fatalf("expected checkpoint to clear after completed page, got %#v", checkpoint)
	}
	data, err := os.ReadFile(filepath.Join(localDir, "Docs", "010.md"))
	if err != nil {
		t.Fatalf("expected final file to apply on resume: %v", err)
	}
	if string(data) != "# 010" {
		t.Fatalf("unexpected final file content: %q", data)
	}
}

func TestPullRemoteIncrementalChangedPath404RetriesWithoutAdvancingCursor(t *testing.T) {
	files := map[string]RemoteFile{
		"/notion/Docs/001.md": {
			Path:        "/notion/Docs/001.md",
			Revision:    "rev_001",
			ContentType: "text/markdown",
			Content:     "# 001",
		},
		"/notion/Docs/003.md": {
			Path:        "/notion/Docs/003.md",
			Revision:    "rev_003",
			ContentType: "text/markdown",
			Content:     "# 003",
		},
	}
	client := &fakeClient{
		files: files,
		events: []FilesystemEvent{
			{EventID: "evt_001", Type: "file.updated", Path: "/notion/Docs/001.md", Revision: "rev_001"},
			{EventID: "evt_002", Type: "file.updated", Path: "/notion/Docs/002.md", Revision: "rev_002"},
			{EventID: "evt_003", Type: "file.updated", Path: "/notion/Docs/003.md", Revision: "rev_003"},
		},
		revisionCounter: 3,
		eventCounter:    3,
	}
	localDir := t.TempDir()
	missingPath := filepath.Join(localDir, "Docs", "002.md")
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:      "ws_changed_404_retry",
		RemoteRoot:       "/notion",
		LocalRoot:        localDir,
		FullPullEvery:    -1,
		WebSocket:        boolPtr(false),
		CursorTimeout:    time.Second,
		BootstrapTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	syncer.loaded = true
	syncer.state = mountState{
		Files: map[string]trackedFile{
			"/notion/Docs/002.md": {
				Revision:    "rev_old",
				ContentType: "text/markdown",
				Hash:        hashBytes([]byte("# stale")),
			},
		},
		EventsCursor:      "evt_000",
		BootstrapComplete: true,
	}

	err = syncer.Reconcile(context.Background())
	var notReadyErr *IncrementalReadNotReadyError
	if !errors.As(err, &notReadyErr) || notReadyErr.Path != "/notion/Docs/002.md" {
		t.Fatalf("expected changed-path 404 to fail the cycle for retry, got %v (listEvents=%d readFile=%d readsByPath=%v)", err, client.listEventsCalls, client.readFileCalls, client.readFileCallsByPath)
	}
	if got := syncer.state.EventsCursor; got != "evt_000" {
		t.Fatalf("EventsCursor = %q, want unchanged evt_000", got)
	}
	if _, err := os.Stat(missingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected unreadable changed path to remain absent locally; stat err=%v", err)
	}
	if _, ok := syncer.state.Files["/notion/Docs/002.md"]; !ok {
		t.Fatalf("expected 404 changed path to remain tracked for retry")
	}
	if _, ok := syncer.state.IncrementalReadNotReadySince["/notion/Docs/002.md"]; !ok {
		t.Fatalf("expected read-not-ready timestamp to be recorded for retry")
	}
	if checkpoint := syncer.state.IncrementalCheckpoint; checkpoint == nil ||
		checkpoint.Phase != "changed" ||
		checkpoint.Path != "/notion/Docs/001.md" {
		t.Fatalf("unexpected checkpoint before unreadable changed path: %#v", checkpoint)
	}
	stateBytes, err := os.ReadFile(filepath.Join(localDir, ".relayfile-mount-state.json"))
	if err != nil {
		t.Fatalf("read persisted retry checkpoint: %v", err)
	}
	var persisted mountState
	if err := json.Unmarshal(stateBytes, &persisted); err != nil {
		t.Fatalf("unmarshal persisted retry checkpoint: %v", err)
	}
	if persisted.EventsCursor != "evt_000" {
		t.Fatalf("persisted EventsCursor = %q, want unchanged evt_000", persisted.EventsCursor)
	}
	if _, ok := persisted.IncrementalReadNotReadySince["/notion/Docs/002.md"]; !ok {
		t.Fatalf("expected persisted read-not-ready timestamp for retry")
	}
	if checkpoint := persisted.IncrementalCheckpoint; checkpoint == nil ||
		checkpoint.Phase != "changed" ||
		checkpoint.Path != "/notion/Docs/001.md" {
		t.Fatalf("unexpected persisted retry checkpoint: %#v", checkpoint)
	}

	beforeFirstReads := client.readFileCallsByPath["/notion/Docs/001.md"]
	client.files["/notion/Docs/002.md"] = RemoteFile{
		Path:        "/notion/Docs/002.md",
		Revision:    "rev_002",
		ContentType: "text/markdown",
		Content:     "# 002",
	}
	resumedSyncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:      "ws_changed_404_retry",
		RemoteRoot:       "/notion",
		LocalRoot:        localDir,
		FullPullEvery:    -1,
		WebSocket:        boolPtr(false),
		CursorTimeout:    time.Second,
		BootstrapTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("new resumed syncer failed: %v", err)
	}
	if err := resumedSyncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile after changed path became readable failed: %v", err)
	}
	if got := client.readFileCallsByPath["/notion/Docs/001.md"]; got != beforeFirstReads {
		t.Fatalf("expected checkpointed path to be skipped on resume; read calls went %d -> %d", beforeFirstReads, got)
	}
	if got := resumedSyncer.state.EventsCursor; got != "evt_003" {
		t.Fatalf("EventsCursor = %q, want completed page cursor evt_003", got)
	}
	if checkpoint := resumedSyncer.state.IncrementalCheckpoint; checkpoint != nil {
		t.Fatalf("expected checkpoint to clear after completed page, got %#v", checkpoint)
	}
	if resumedSyncer.state.IncrementalReadNotReadySince != nil {
		t.Fatalf("expected read-not-ready state to clear after materialization, got %#v", resumedSyncer.state.IncrementalReadNotReadySince)
	}
	assertLocalFileContent(t, missingPath, "# 002")
	assertLocalFileContent(t, filepath.Join(localDir, "Docs", "003.md"), "# 003")
}

// TestPullRemoteIncrementalDrainsStaleAliasEventsInOneCycle verifies the
// stuck-event drain (AR-272 Part 2a): a read 404 on a provider-layout-alias
// path (by-state index) is treated as a stale event and skipped immediately,
// so a single cycle chews through several consecutive stuck events instead of
// exiting after the first one. Real (non-alias) records on either side of the
// stuck run are still applied.
func TestPullRemoteIncrementalDrainsStaleAliasEventsInOneCycle(t *testing.T) {
	files := map[string]RemoteFile{
		"/linear/issues/AR-1.json": {
			Path:        "/linear/issues/AR-1.json",
			Revision:    "rev_001",
			ContentType: "application/json",
			Content:     `{"id":"AR-1"}`,
		},
		"/linear/issues/AR-4.json": {
			Path:        "/linear/issues/AR-4.json",
			Revision:    "rev_004",
			ContentType: "application/json",
			Content:     `{"id":"AR-4"}`,
		},
	}
	client := &fakeClient{
		files: files,
		events: []FilesystemEvent{
			{EventID: "evt_001", Type: "file.updated", Path: "/linear/issues/AR-1.json", Revision: "rev_001"},
			// Two by-state alias paths the emitter dropped when the issues
			// left ready-for-agent — the events feed still carries them, and
			// reads 404. These are the stuck-event class.
			{EventID: "evt_002", Type: "file.updated", Path: "/linear/issues/by-state/ready-for-agent/AR-2.json", Revision: "rev_002"},
			{EventID: "evt_003", Type: "file.updated", Path: "/linear/issues/by-state/ready-for-agent/AR-3.json", Revision: "rev_003"},
			{EventID: "evt_004", Type: "file.updated", Path: "/linear/issues/AR-4.json", Revision: "rev_004"},
		},
		revisionCounter: 4,
		eventCounter:    4,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:      "ws_alias_drain",
		RemoteRoot:       "/linear",
		LocalRoot:        localDir,
		FullPullEvery:    -1,
		WebSocket:        boolPtr(false),
		CursorTimeout:    time.Second,
		BootstrapTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	syncer.loaded = true
	syncer.state = mountState{
		Files:             map[string]trackedFile{},
		EventsCursor:      "evt_000",
		BootstrapComplete: true,
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("expected stale alias events to drain in one cycle, got %v", err)
	}
	if got := syncer.state.EventsCursor; got != "evt_004" {
		t.Fatalf("EventsCursor = %q, want fully-drained evt_004", got)
	}
	if got := syncer.StaleAliasSkips(); got != 2 {
		t.Fatalf("StaleAliasSkips = %d, want 2", got)
	}
	if syncer.state.IncrementalReadNotReadySince != nil {
		t.Fatalf("expected no read-not-ready timestamps for stale alias paths, got %#v", syncer.state.IncrementalReadNotReadySince)
	}
	assertLocalFileContent(t, filepath.Join(localDir, "issues", "AR-1.json"), `{"id":"AR-1"}`)
	assertLocalFileContent(t, filepath.Join(localDir, "issues", "AR-4.json"), `{"id":"AR-4"}`)
}

// TestSkipStuckDropsConsecutiveUnreadableEvents verifies the operator escape
// hatch (AR-272 Part 2b): SkipStuck advances the events cursor past every
// read-404 event — including non-alias canonical paths — without waiting the
// read-not-ready TTL, and reports the number skipped.
func TestSkipStuckDropsConsecutiveUnreadableEvents(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/004.md": {
				Path:        "/notion/Docs/004.md",
				Revision:    "rev_004",
				ContentType: "text/markdown",
				Content:     "# 004",
			},
		},
		events: []FilesystemEvent{
			// Non-alias canonical paths that 404 — normally these retry for
			// the full TTL; skip-stuck drops them immediately.
			{EventID: "evt_001", Type: "file.updated", Path: "/notion/Docs/001.md", Revision: "rev_001"},
			{EventID: "evt_002", Type: "file.updated", Path: "/notion/Docs/002.md", Revision: "rev_002"},
			{EventID: "evt_003", Type: "file.updated", Path: "/notion/Docs/003.md", Revision: "rev_003"},
			{EventID: "evt_004", Type: "file.updated", Path: "/notion/Docs/004.md", Revision: "rev_004"},
		},
		revisionCounter: 4,
		eventCounter:    4,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:      "ws_skip_stuck",
		RemoteRoot:       "/notion",
		LocalRoot:        localDir,
		FullPullEvery:    -1,
		WebSocket:        boolPtr(false),
		CursorTimeout:    time.Second,
		BootstrapTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	syncer.loaded = true
	syncer.state = mountState{
		Files:             map[string]trackedFile{},
		EventsCursor:      "evt_000",
		BootstrapComplete: true,
	}

	skipped, err := syncer.SkipStuck(context.Background(), 0)
	if err != nil {
		t.Fatalf("SkipStuck failed: %v", err)
	}
	if skipped != 3 {
		t.Fatalf("skipped = %d, want 3 unreadable events dropped", skipped)
	}
	if got := syncer.state.EventsCursor; got != "evt_004" {
		t.Fatalf("EventsCursor = %q, want evt_004 (caught up to head)", got)
	}
	assertLocalFileContent(t, filepath.Join(localDir, "Docs", "004.md"), "# 004")
}

func TestSkipStuckRefusesConcurrentSync(t *testing.T) {
	syncer, err := NewSyncer(&fakeClient{files: map[string]RemoteFile{}}, SyncerOptions{
		WorkspaceID: "ws_skip_stuck_busy",
		RemoteRoot:  "/",
		LocalRoot:   t.TempDir(),
		WebSocket:   boolPtr(false),
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	syncer.mu.Lock()
	syncer.syncActive = true
	syncer.mu.Unlock()

	skipped, err := syncer.SkipStuck(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "sync already in progress") {
		t.Fatalf("expected concurrent sync error, skipped=%d err=%v", skipped, err)
	}
	syncer.mu.Lock()
	defer syncer.mu.Unlock()
	if syncer.skipStuckMode || syncer.skipStuckMax != 0 {
		t.Fatalf("skip-stuck state leaked after refusal: mode=%v max=%d", syncer.skipStuckMode, syncer.skipStuckMax)
	}
}

func TestPullRemoteIncrementalCreatedThreadReply404RetriesWithoutAdvancingCursor(t *testing.T) {
	const replyPath = "/slack/channels/C123ABC__proj-cloud/threads/1780871788_370329/replies/1780914176_827829.json"
	client := &fakeClient{
		files: map[string]RemoteFile{},
		events: []FilesystemEvent{
			{EventID: "evt_168287", Type: "file.created", Path: replyPath, Revision: "rev_reply"},
		},
		revisionCounter: 1,
		eventCounter:    1,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:      "ws_thread_reply_404_retry",
		RemoteRoot:       "/slack/channels/C123ABC__proj-cloud/threads",
		LocalRoot:        localDir,
		FullPullEvery:    -1,
		WebSocket:        boolPtr(false),
		CursorTimeout:    time.Second,
		BootstrapTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	syncer.loaded = true
	syncer.state = mountState{
		Files:             map[string]trackedFile{},
		EventsCursor:      "evt_168286",
		BootstrapComplete: true,
	}

	err = syncer.Reconcile(context.Background())
	var notReadyErr *IncrementalReadNotReadyError
	if !errors.As(err, &notReadyErr) || notReadyErr.Path != replyPath {
		t.Fatalf("expected created thread reply 404 to fail the cycle for retry, got %v (listEvents=%d readFile=%d readsByPath=%v)", err, client.listEventsCalls, client.readFileCalls, client.readFileCallsByPath)
	}
	if got := syncer.state.EventsCursor; got != "evt_168286" {
		t.Fatalf("EventsCursor = %q, want unchanged evt_168286", got)
	}
	localReplyPath := filepath.Join(localDir, "1780871788_370329", "replies", "1780914176_827829.json")
	if _, err := os.Stat(localReplyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected unreadable thread reply to remain absent locally; stat err=%v", err)
	}

	client.files[replyPath] = RemoteFile{
		Path:        replyPath,
		Revision:    "rev_reply",
		ContentType: "application/json",
		Content:     `{"text":"materialized reply"}`,
	}
	resumedSyncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:      "ws_thread_reply_404_retry",
		RemoteRoot:       "/slack/channels/C123ABC__proj-cloud/threads",
		LocalRoot:        localDir,
		FullPullEvery:    -1,
		WebSocket:        boolPtr(false),
		CursorTimeout:    time.Second,
		BootstrapTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("new resumed syncer failed: %v", err)
	}
	if err := resumedSyncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile after thread reply became readable failed: %v", err)
	}
	if got := resumedSyncer.state.EventsCursor; got != "evt_168287" {
		t.Fatalf("EventsCursor = %q, want evt_168287", got)
	}
	assertLocalFileContent(t, localReplyPath, `{"text":"materialized reply"}`)
}

func TestPullRemoteIncrementalReadNotReadyBeforeTTLStillRetriesAndMaterializes(t *testing.T) {
	const remotePath = "/notion/Docs/slow.md"
	ttl := time.Hour
	client := &fakeClient{
		files: map[string]RemoteFile{},
		events: []FilesystemEvent{
			{EventID: "evt_001", Type: "file.created", Path: remotePath, Revision: "rev_001"},
		},
		revisionCounter: 1,
		eventCounter:    1,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:                "ws_not_ready_before_ttl",
		RemoteRoot:                 "/notion",
		LocalRoot:                  localDir,
		FullPullEvery:              -1,
		WebSocket:                  boolPtr(false),
		CursorTimeout:              time.Second,
		BootstrapTimeout:           time.Second,
		IncrementalReadNotReadyTTL: ttl,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	syncer.loaded = true
	syncer.state = mountState{
		Files:             map[string]trackedFile{},
		EventsCursor:      "evt_000",
		BootstrapComplete: true,
		IncrementalReadNotReadySince: map[string]string{
			remotePath: time.Now().UTC().Add(-ttl + time.Minute).Format(time.RFC3339Nano),
		},
	}

	err = syncer.Reconcile(context.Background())
	var notReadyErr *IncrementalReadNotReadyError
	if !errors.As(err, &notReadyErr) || notReadyErr.Path != remotePath {
		t.Fatalf("expected under-TTL 404 to remain retryable, got %v", err)
	}
	if got := syncer.state.EventsCursor; got != "evt_000" {
		t.Fatalf("EventsCursor = %q, want unchanged evt_000", got)
	}
	if _, err := os.Stat(filepath.Join(localDir, "Docs", "slow.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("under-TTL unreadable file should remain absent locally; stat err=%v", err)
	}

	client.files[remotePath] = RemoteFile{
		Path:        remotePath,
		Revision:    "rev_001",
		ContentType: "text/markdown",
		Content:     "# slow",
	}
	resumedSyncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:                "ws_not_ready_before_ttl",
		RemoteRoot:                 "/notion",
		LocalRoot:                  localDir,
		FullPullEvery:              -1,
		WebSocket:                  boolPtr(false),
		CursorTimeout:              time.Second,
		BootstrapTimeout:           time.Second,
		IncrementalReadNotReadyTTL: ttl,
	})
	if err != nil {
		t.Fatalf("new resumed syncer failed: %v", err)
	}
	if err := resumedSyncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile after under-TTL file appeared failed: %v", err)
	}
	if got := resumedSyncer.state.EventsCursor; got != "evt_001" {
		t.Fatalf("EventsCursor = %q, want evt_001", got)
	}
	if resumedSyncer.state.IncrementalReadNotReadySince != nil {
		t.Fatalf("expected read-not-ready state to clear after materialization, got %#v", resumedSyncer.state.IncrementalReadNotReadySince)
	}
	assertLocalFileContent(t, filepath.Join(localDir, "Docs", "slow.md"), "# slow")
}

func TestPullRemoteIncrementalPersistentReadNotReadyAfterTTLAdvancesAsDelete(t *testing.T) {
	const remotePath = "/notion/Docs/gone.md"
	ttl := time.Minute
	client := &fakeClient{
		files: map[string]RemoteFile{},
		events: []FilesystemEvent{
			{EventID: "evt_001", Type: "file.created", Path: remotePath, Revision: "rev_001"},
			{EventID: "evt_002", Type: "file.created", Path: "/notion/Docs/after.md", Revision: "rev_002"},
		},
		revisionCounter: 2,
		eventCounter:    2,
	}
	client.files["/notion/Docs/after.md"] = RemoteFile{
		Path:        "/notion/Docs/after.md",
		Revision:    "rev_002",
		ContentType: "text/markdown",
		Content:     "# after",
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:                "ws_not_ready_after_ttl",
		RemoteRoot:                 "/notion",
		LocalRoot:                  localDir,
		FullPullEvery:              -1,
		WebSocket:                  boolPtr(false),
		CursorTimeout:              time.Second,
		BootstrapTimeout:           time.Second,
		IncrementalReadNotReadyTTL: ttl,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	syncer.loaded = true
	syncer.state = mountState{
		Files:             map[string]trackedFile{},
		EventsCursor:      "evt_000",
		BootstrapComplete: true,
		IncrementalReadNotReadySince: map[string]string{
			remotePath: time.Now().UTC().Add(-ttl - time.Second).Format(time.RFC3339Nano),
		},
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("expired read-not-ready should advance as delete, got %v", err)
	}
	if got := syncer.state.EventsCursor; got != "evt_002" {
		t.Fatalf("EventsCursor = %q, want evt_002", got)
	}
	if syncer.state.IncrementalReadNotReadySince != nil {
		t.Fatalf("expected expired read-not-ready state to clear, got %#v", syncer.state.IncrementalReadNotReadySince)
	}
	if _, ok := syncer.state.Files[remotePath]; ok {
		t.Fatalf("expired persistent 404 should not remain tracked")
	}
	if _, err := os.Stat(filepath.Join(localDir, "Docs", "gone.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired persistent 404 should not materialize locally; stat err=%v", err)
	}
	assertLocalFileContent(t, filepath.Join(localDir, "Docs", "after.md"), "# after")
}

func TestPullRemoteIncrementalRecreatedPathAfterMarkerClearGetsFreshTTL(t *testing.T) {
	const remotePath = "/notion/Docs/recreated.md"
	ttl := time.Minute
	oldMarker := time.Now().UTC().Add(-ttl - time.Minute).Format(time.RFC3339Nano)
	client := &fakeClient{
		files: map[string]RemoteFile{},
		events: []FilesystemEvent{
			{EventID: "evt_001", Type: "file.created", Path: remotePath, Revision: "rev_recreated"},
		},
		revisionCounter: 1,
		eventCounter:    1,
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:                "ws_not_ready_recreated_fresh_ttl",
		RemoteRoot:                 "/notion",
		LocalRoot:                  localDir,
		FullPullEvery:              -1,
		WebSocket:                  boolPtr(false),
		CursorTimeout:              time.Second,
		BootstrapTimeout:           time.Second,
		IncrementalReadNotReadyTTL: ttl,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	syncer.loaded = true
	syncer.state = mountState{
		Files:             map[string]trackedFile{},
		EventsCursor:      "evt_000",
		BootstrapComplete: true,
		IncrementalReadNotReadySince: map[string]string{
			remotePath: oldMarker,
		},
	}

	if err := syncer.applyRemoteFile(remotePath, RemoteFile{
		Path:        remotePath,
		Revision:    "rev_old_incarnation",
		ContentType: "text/markdown",
		Content:     "# old incarnation",
	}, nil); err != nil {
		t.Fatalf("apply old incarnation: %v", err)
	}
	if syncer.state.IncrementalReadNotReadySince != nil {
		t.Fatalf("expected successful materialization to clear stale marker, got %#v", syncer.state.IncrementalReadNotReadySince)
	}

	err = syncer.Reconcile(context.Background())
	var notReadyErr *IncrementalReadNotReadyError
	if !errors.As(err, &notReadyErr) || notReadyErr.Path != remotePath {
		t.Fatalf("expected recreated path 404 to get fresh retry window, got %v", err)
	}
	if got := syncer.state.EventsCursor; got != "evt_000" {
		t.Fatalf("EventsCursor = %q, want unchanged evt_000", got)
	}
	gotMarker := syncer.state.IncrementalReadNotReadySince[remotePath]
	if gotMarker == "" {
		t.Fatalf("expected recreated path to record a fresh read-not-ready marker")
	}
	if gotMarker == oldMarker {
		t.Fatalf("expected recreated path marker to be refreshed, still %q", gotMarker)
	}
	if _, ok := syncer.state.Files[remotePath]; !ok {
		t.Fatalf("fresh retry should not delete recreated path state")
	}
	assertLocalFileContent(t, filepath.Join(localDir, "Docs", "recreated.md"), "# old incarnation")
}

func TestApplyWebSocketEventClearsReadNotReadyMarker(t *testing.T) {
	const remotePath = "/notion/Docs/ws.md"
	client := &fakeClient{
		files: map[string]RemoteFile{
			remotePath: {
				Path:        remotePath,
				Revision:    "rev_ws",
				ContentType: "text/markdown",
				Content:     "# websocket",
			},
		},
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_event_clears_not_ready",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	syncer.state.IncrementalReadNotReadySince = map[string]string{
		remotePath: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano),
	}

	if err := syncer.applyWebSocketEvent(context.Background(), websocketEvent{
		Type:      "file.updated",
		Path:      remotePath,
		Timestamp: "2026-06-08T12:00:00Z",
	}); err != nil {
		t.Fatalf("apply websocket event: %v", err)
	}
	if syncer.state.IncrementalReadNotReadySince != nil {
		t.Fatalf("expected websocket materialization to clear marker, got %#v", syncer.state.IncrementalReadNotReadySince)
	}
	assertLocalFileContent(t, filepath.Join(localDir, "Docs", "ws.md"), "# websocket")
}

func TestApplyRemoteSnapshotDeletesRevClearsReadNotReadyMarkerAfterConfirmedDelete(t *testing.T) {
	const remotePath = "/notion/Docs/deleted.md"
	localDir := t.TempDir()
	localPath := filepath.Join(localDir, "Docs", "deleted.md")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir local doc dir: %v", err)
	}
	if err := os.WriteFile(localPath, []byte("# deleted"), 0o644); err != nil {
		t.Fatalf("write local doc: %v", err)
	}
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID: "ws_snapshot_delete_clears_not_ready",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	syncer.state.Files[remotePath] = trackedFile{
		Revision:    "rev_old",
		ContentType: "text/markdown",
		Hash:        hashBytes([]byte("# deleted")),
	}
	syncer.state.LastAppliedRevision = "rev_001"
	syncer.state.IncrementalReadNotReadySince = map[string]string{
		remotePath: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano),
	}

	if err := syncer.applyRemoteSnapshotDeletesRev(map[string]struct{}{}, nil, "rev_002"); err != nil {
		t.Fatalf("first snapshot delete observation: %v", err)
	}
	if syncer.state.IncrementalReadNotReadySince == nil {
		t.Fatalf("first tombstone observation should not clear marker before delete succeeds")
	}
	if err := syncer.applyRemoteSnapshotDeletesRev(map[string]struct{}{}, nil, "rev_003"); err != nil {
		t.Fatalf("confirmed snapshot delete: %v", err)
	}
	if syncer.state.IncrementalReadNotReadySince != nil {
		t.Fatalf("expected confirmed snapshot delete to clear marker, got %#v", syncer.state.IncrementalReadNotReadySince)
	}
	if _, ok := syncer.state.Files[remotePath]; ok {
		t.Fatalf("expected confirmed snapshot delete to remove tracked state")
	}
	if _, err := os.Stat(localPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected confirmed snapshot delete to remove local file; stat err=%v", err)
	}
}

func TestMarkBootstrapCompleteClearsReadNotReadyMarkers(t *testing.T) {
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID: "ws_bootstrap_clears_not_ready",
		RemoteRoot:  "/notion",
		LocalRoot:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	syncer.state.IncrementalReadNotReadySince = map[string]string{
		"/notion/Docs/a.md": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano),
	}

	syncer.markBootstrapComplete()

	if syncer.state.IncrementalReadNotReadySince != nil {
		t.Fatalf("expected bootstrap completion to clear markers, got %#v", syncer.state.IncrementalReadNotReadySince)
	}
}

func TestLoadStateResetsBootstrapCompleteOnlyOnWriteOnlyToMirrorSyncMode(t *testing.T) {
	tests := []struct {
		name                  string
		syncMode              string
		writeOnly             bool
		wantBootstrapComplete bool
		wantCleared           bool
	}{
		{
			name:                  "write-only to mirror resets bootstrap",
			syncMode:              "write-only",
			writeOnly:             false,
			wantBootstrapComplete: false,
			wantCleared:           true,
		},
		{
			name:                  "mirror to mirror keeps bootstrap",
			syncMode:              "mirror",
			writeOnly:             false,
			wantBootstrapComplete: true,
		},
		{
			name:                  "legacy unknown to mirror keeps bootstrap",
			syncMode:              "",
			writeOnly:             false,
			wantBootstrapComplete: true,
		},
		{
			name:                  "write-only stays write-only keeps bootstrap",
			syncMode:              "write-only",
			writeOnly:             true,
			wantBootstrapComplete: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stateFile := filepath.Join(t.TempDir(), ".relayfile-mount-state.json")
			if err := writeMountState(stateFile, mountState{
				Files: map[string]trackedFile{
					"/notion/Docs/a.md": {
						Revision:    "rev_1",
						ContentType: "text/markdown",
						Hash:        hashString("# A"),
					},
				},
				BootstrapComplete:    true,
				BootstrapCursor:      "cursor_1",
				BootstrapStartedAt:   time.Now().UTC().Format(time.RFC3339Nano),
				BootstrapFilesSynced: 3,
				BootstrapFilesTotal:  9,
				SyncMode:             tc.syncMode,
			}); err != nil {
				t.Fatalf("seed state: %v", err)
			}

			syncer := &Syncer{stateFile: stateFile, writeOnly: tc.writeOnly}
			if err := syncer.loadState(); err != nil {
				t.Fatalf("load state: %v", err)
			}

			if got := syncer.state.BootstrapComplete; got != tc.wantBootstrapComplete {
				t.Fatalf("BootstrapComplete = %v, want %v", got, tc.wantBootstrapComplete)
			}
			if tc.wantCleared {
				if syncer.state.BootstrapCursor != "" {
					t.Fatalf("BootstrapCursor = %q, want empty", syncer.state.BootstrapCursor)
				}
				if syncer.state.BootstrapStartedAt != "" {
					t.Fatalf("BootstrapStartedAt = %q, want empty", syncer.state.BootstrapStartedAt)
				}
				if syncer.state.BootstrapFilesSynced != 0 {
					t.Fatalf("BootstrapFilesSynced = %d, want 0", syncer.state.BootstrapFilesSynced)
				}
				if syncer.state.BootstrapFilesTotal != 0 {
					t.Fatalf("BootstrapFilesTotal = %d, want 0", syncer.state.BootstrapFilesTotal)
				}
			}
		})
	}
}

func TestPullRemoteIncrementalDeleteEventStillDeletes(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{},
		events: []FilesystemEvent{
			{EventID: "evt_001", Type: "file.deleted", Path: "/notion/Docs/001.md", Revision: "rev_001"},
		},
		revisionCounter: 1,
		eventCounter:    1,
	}
	localDir := t.TempDir()
	localPath := filepath.Join(localDir, "Docs", "001.md")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir local doc dir: %v", err)
	}
	if err := os.WriteFile(localPath, []byte("# stale"), 0o644); err != nil {
		t.Fatalf("write stale local file: %v", err)
	}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:      "ws_deleted_event",
		RemoteRoot:       "/notion",
		LocalRoot:        localDir,
		FullPullEvery:    -1,
		WebSocket:        boolPtr(false),
		CursorTimeout:    time.Second,
		BootstrapTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	syncer.loaded = true
	syncer.state = mountState{
		Files: map[string]trackedFile{
			"/notion/Docs/001.md": {
				Revision:    "rev_001",
				ContentType: "text/markdown",
				Hash:        hashBytes([]byte("# stale")),
			},
		},
		EventsCursor:      "evt_000",
		BootstrapComplete: true,
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile delete event: %v", err)
	}
	if _, err := os.Stat(localPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected delete event to remove local file; stat err=%v", err)
	}
	if _, ok := syncer.state.Files["/notion/Docs/001.md"]; ok {
		t.Fatalf("expected delete event to remove tracked state")
	}
	if got := syncer.state.EventsCursor; got != "evt_001" {
		t.Fatalf("EventsCursor = %q, want evt_001", got)
	}
}

func TestScanLocalFilesLogsOversizedFileOncePerSize(t *testing.T) {
	t.Setenv("RELAYFILE_MAX_WRITEBACK_BYTES", "4")

	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "big.md"), []byte("too large"), 0o644); err != nil {
		t.Fatalf("write oversized file: %v", err)
	}
	logger := &captureLogger{}
	syncer, err := NewSyncer(&fakeClient{files: map[string]RemoteFile{}}, SyncerOptions{
		WorkspaceID:   "ws_oversized",
		RemoteRoot:    "/",
		LocalRoot:     localDir,
		Logger:        logger,
		FullPullEvery: -1,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	for i := 0; i < 2; i++ {
		files, err := syncer.scanLocalFiles()
		if err != nil {
			t.Fatalf("scan %d failed: %v", i, err)
		}
		if snapshot, ok := files["/big.md"]; !ok || !snapshot.SkipWriteback {
			t.Fatalf("scan %d did not mark oversized file as skipped: %#v", i, files["/big.md"])
		}
	}

	oversizedLogs := 0
	for _, line := range logger.lines {
		if strings.Contains(line, "skipping oversized local file") {
			oversizedLogs++
		}
	}
	if oversizedLogs != 1 {
		t.Fatalf("expected one oversized-file log across repeated scans, got %d lines: %#v", oversizedLogs, logger.lines)
	}
}

func TestPullRemoteFullTreeSkipsNestedMountRuntimeState(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{
			"/slack/channels/C123/messages/1780145510_376649.json": {
				Path:        "/slack/channels/C123/messages/1780145510_376649.json",
				Revision:    "rev_msg",
				ContentType: "application/json",
				Content:     `{"text":"hello"}`,
			},
			"/slack/channels/C123/messages/.relay/state.json": {
				Path:        "/slack/channels/C123/messages/.relay/state.json",
				Revision:    "rev_runtime",
				ContentType: "application/json",
				Content:     `{"pendingWriteback":200}`,
			},
		},
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_pull_nested_relay",
		RemoteRoot:  "/slack",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	runtimeLocal := filepath.Join(localDir, "channels", "C123", "messages", ".relay", "state.json")
	if err := os.MkdirAll(filepath.Dir(runtimeLocal), 0o755); err != nil {
		t.Fatalf("mkdir stale nested .relay: %v", err)
	}
	if err := os.WriteFile(runtimeLocal, []byte(`{"stale":true}`), 0o644); err != nil {
		t.Fatalf("write stale nested .relay: %v", err)
	}
	syncer.state.Files["/slack/channels/C123/messages/.relay/state.json"] = trackedFile{
		Revision: "rev_old",
		Hash:     hashString(`{"stale":true}`),
	}

	if err := syncer.pullRemoteFullTree(context.Background(), nil, bootstrapProgress{}); err != nil {
		t.Fatalf("pullRemoteFullTree failed: %v", err)
	}

	realLocal := filepath.Join(localDir, "channels", "C123", "messages", "1780145510_376649.json")
	if _, err := os.Stat(realLocal); err != nil {
		t.Fatalf("expected real message mirrored locally: %v", err)
	}
	if _, err := os.Stat(runtimeLocal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("nested mount runtime file should be absent after full pull, stat err=%v", err)
	}
	if _, ok := syncer.state.Files["/slack/channels/C123/messages/.relay/state.json"]; ok {
		t.Fatalf("nested mount runtime path should not remain tracked")
	}
	if got := client.readFileCallsByPath["/slack/channels/C123/messages/.relay/state.json"]; got != 0 {
		t.Fatalf("nested mount runtime path should not be read from remote, got %d reads", got)
	}
}

func TestPullRemoteIncrementalSkipsNestedMountRuntimeState(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{
			"/slack/channels/C123/messages/1780145510_376649.json": {
				Path:        "/slack/channels/C123/messages/1780145510_376649.json",
				Revision:    "rev_msg",
				ContentType: "application/json",
				Content:     `{"text":"hello"}`,
			},
			"/slack/channels/C123/messages/.relay/state.json": {
				Path:        "/slack/channels/C123/messages/.relay/state.json",
				Revision:    "rev_runtime",
				ContentType: "application/json",
				Content:     `{"pendingWriteback":200}`,
			},
		},
		events: []FilesystemEvent{
			{
				EventID:  "evt_runtime",
				Type:     "file.created",
				Path:     "/slack/channels/C123/messages/.relay/state.json",
				Revision: "rev_runtime",
			},
			{
				EventID:  "evt_msg",
				Type:     "file.created",
				Path:     "/slack/channels/C123/messages/1780145510_376649.json",
				Revision: "rev_msg",
			},
		},
	}
	localDir := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_incremental_nested_relay",
		RemoteRoot:  "/slack",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	runtimeLocal := filepath.Join(localDir, "channels", "C123", "messages", ".relay", "state.json")
	if err := os.MkdirAll(filepath.Dir(runtimeLocal), 0o755); err != nil {
		t.Fatalf("mkdir stale nested .relay: %v", err)
	}
	if err := os.WriteFile(runtimeLocal, []byte(`{"stale":true}`), 0o644); err != nil {
		t.Fatalf("write stale nested .relay: %v", err)
	}
	syncer.state.Files["/slack/channels/C123/messages/.relay/state.json"] = trackedFile{Revision: "rev_old"}

	cursor, err := syncer.pullRemoteIncremental(context.Background(), nil, "")
	if err != nil {
		t.Fatalf("pullRemoteIncremental failed: %v", err)
	}
	if cursor != "evt_msg" {
		t.Fatalf("cursor = %q, want evt_msg", cursor)
	}
	if got := client.readFileCallsByPath["/slack/channels/C123/messages/.relay/state.json"]; got != 0 {
		t.Fatalf("nested mount runtime path should not be read from remote, got %d reads", got)
	}
	if _, err := os.Stat(runtimeLocal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("nested mount runtime file should be absent after incremental pull, stat err=%v", err)
	}
	realLocal := filepath.Join(localDir, "channels", "C123", "messages", "1780145510_376649.json")
	if _, err := os.Stat(realLocal); err != nil {
		t.Fatalf("expected real message mirrored locally: %v", err)
	}
}

func TestQuietEventCyclesEventuallyRunPeriodicFullPull(t *testing.T) {
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
		WorkspaceID:   "ws_quiet_periodic_full",
		RemoteRoot:    "/notion",
		LocalRoot:     localDir,
		FullPullEvery: 2,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("bootstrap sync: %v", err)
	}

	client.files["/notion/Docs/B.md"] = RemoteFile{
		Path:        "/notion/Docs/B.md",
		Revision:    "rev_2",
		ContentType: "text/markdown",
		Content:     "# B",
	}
	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("first quiet reconcile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(localDir, "Docs", "B.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quiet cycle before cadence should not pull B.md yet; stat err=%v", err)
	}
	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("second quiet reconcile: %v", err)
	}
	assertLocalFileContent(t, filepath.Join(localDir, "Docs", "B.md"), "# B")
}

// TestPullRestartFastPathSkipsFullPull pins the daemon-restart half of
// the reconcile-stall fix.
//
// On restart against a workspace that has been synced before, the state
// file persists tracked.Files but loses EventsCursor whenever the prior
// daemon never completed a successful bootstrap (the failure mode this
// PR fixes — once a workspace crosses the file-count threshold, the
// initial full pull times out and the cursor is never seeded). Without
// the fast-path, every restart re-tries that same doomed full pull and
// the daemon stays stalled forever.
//
// The fix: when EventsCursor is empty but tracked.Files is non-empty,
// trust the on-disk state and seed the cursor against the events tip
// instead of performing the bootstrap full pull. The trust-but-verify
// periodic full pull (every fullPullEvery cycles) eventually catches
// any drift on a non-empty cycle.
func TestPullRestartFastPathSkipsFullPull(t *testing.T) {
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
				EventID:  "evt_seed",
				Type:     "file.created",
				Path:     "/notion/Docs/A.md",
				Revision: "rev_1",
			},
		},
		revisionCounter: 1,
		eventCounter:    1,
	}
	localDir := t.TempDir()

	// Pre-populate the state file as if a prior daemon had successfully
	// mirrored the workspace but never resolved an events cursor (this
	// is exactly the production failure mode: pullRemoteFull populated
	// state.Files but resolveLatestEventCursor was never reached because
	// the cycle's context deadline expired first). Note: we also need
	// the local file on disk for state-load consistency.
	if err := os.MkdirAll(filepath.Join(localDir, "Docs"), 0o755); err != nil {
		t.Fatalf("mkdir local: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "Docs", "A.md"), []byte("# A"), 0o644); err != nil {
		t.Fatalf("seed local: %v", err)
	}
	persisted := mountState{
		Files: map[string]trackedFile{
			"/notion/Docs/A.md": {
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Hash:        hashBytes([]byte("# A")),
			},
		},
		// EventsCursor intentionally empty — the prior daemon never got
		// past the bootstrap full pull. LastEventAt set because a
		// previous daemon did successfully observe events at some
		// point; this is the production failure shape (state.json with
		// tracked files + lastEventAt but null eventsCursor).
		LastEventAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		// BootstrapComplete is now the authoritative fast-path gate
		// (the LastEventAt heuristic was unsafe — it let a partial
		// mirror short-circuit the full pull forever, rw_517d60b6).
		// A genuine prior restart that fully mirrored the workspace
		// would have this set; seed it so the fast-path engages.
		BootstrapComplete: true,
	}
	stateBytes, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal seed state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, ".relayfile-mount-state.json"), stateBytes, 0o644); err != nil {
		t.Fatalf("write seed state: %v", err)
	}

	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_restart_fast_path",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("restart reconcile: %v", err)
	}

	// The fast-path must:
	//   (a) NOT call ListTree (the slow path that times out on big
	//       workspaces).
	//   (b) NOT call ReadFile (would scale per-file).
	//   (c) Seed the events cursor against the current tip so the next
	//       cycle uses the incremental path / short-circuit.
	if client.listTreeCalls != 0 {
		t.Fatalf("expected restart fast-path to skip ListTree; got %d calls", client.listTreeCalls)
	}
	if client.readFileCalls != 0 {
		t.Fatalf("expected restart fast-path to skip ReadFile; got %d calls", client.readFileCalls)
	}
	status := readPublicState(t, localDir)
	if strings.TrimSpace(status.LastSuccessfulReconcileAt) == "" {
		t.Fatalf("expected restart fast-path to mark sync success")
	}

	// Quiet next cycle — short-circuit should now apply because the
	// cursor was seeded.
	listEventsBefore := client.listEventsCalls
	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("post-fast-path quiet reconcile: %v", err)
	}
	if client.listEventsCalls-listEventsBefore != 1 {
		t.Fatalf("expected exactly one ListEvents probe on quiet cycle, got %d", client.listEventsCalls-listEventsBefore)
	}
	if client.listTreeCalls != 0 || client.readFileCalls != 0 {
		t.Fatalf("expected quiet post-restart cycle to remain a pure no-op; tree=%d read=%d",
			client.listTreeCalls, client.readFileCalls)
	}
}

func TestPullReusedMountWithPersistedCursorUsesIncrementalOnly(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{
			"/notion/Docs/A.md": {
				Path:        "/notion/Docs/A.md",
				Revision:    "rev_2",
				ContentType: "text/markdown",
				Content:     "# A v2",
			},
		},
		events: []FilesystemEvent{
			{
				EventID:  "evt_1",
				Type:     "file.created",
				Path:     "/notion/Docs/A.md",
				Revision: "rev_1",
			},
			{
				EventID:  "evt_2",
				Type:     "file.updated",
				Path:     "/notion/Docs/A.md",
				Revision: "rev_2",
			},
		},
		revisionCounter: 2,
		eventCounter:    2,
	}
	localDir := t.TempDir()
	localPath := filepath.Join(localDir, "Docs", "A.md")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir local: %v", err)
	}
	if err := os.WriteFile(localPath, []byte("# A v1"), 0o644); err != nil {
		t.Fatalf("seed local: %v", err)
	}
	if err := writeMountState(filepath.Join(localDir, ".relayfile-mount-state.json"), mountState{
		Files: map[string]trackedFile{
			"/notion/Docs/A.md": {
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Hash:        hashBytes([]byte("# A v1")),
			},
		},
		EventsCursor:      "evt_1",
		BootstrapComplete: true,
	}); err != nil {
		t.Fatalf("write seed state: %v", err)
	}

	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_reuse_cursor",
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("reused cursor reconcile: %v", err)
	}

	if client.listTreeCalls != 0 {
		t.Fatalf("expected persisted cursor reuse to skip full tree pull; got %d ListTree call(s)", client.listTreeCalls)
	}
	if client.latestEventIDCalls != 0 {
		t.Fatalf("expected persisted cursor reuse not to resolve latest cursor; got %d call(s)", client.latestEventIDCalls)
	}
	assertLocalFileContent(t, localPath, "# A v2")
	if got := strings.TrimSpace(syncer.state.EventsCursor); got != "evt_2" {
		t.Fatalf("expected cursor to advance to evt_2, got %q", got)
	}
}

func TestPullRestartFastPathRetriesCursorDeadlineBeforeFullPull(t *testing.T) {
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
				EventID:  "evt_seed",
				Type:     "file.created",
				Path:     "/notion/Docs/A.md",
				Revision: "rev_1",
			},
		},
		revisionCounter: 1,
		eventCounter:    1,
	}
	client.latestEventIDHook = func(call int) (string, error) {
		if call == 1 {
			return "", context.DeadlineExceeded
		}
		return "evt_seed", nil
	}
	localDir := t.TempDir()
	localPath := filepath.Join(localDir, "Docs", "A.md")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir local: %v", err)
	}
	if err := os.WriteFile(localPath, []byte("# A"), 0o644); err != nil {
		t.Fatalf("seed local: %v", err)
	}
	if err := writeMountState(filepath.Join(localDir, ".relayfile-mount-state.json"), mountState{
		Files: map[string]trackedFile{
			"/notion/Docs/A.md": {
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Hash:        hashBytes([]byte("# A")),
			},
		},
		BootstrapComplete: true,
	}); err != nil {
		t.Fatalf("write seed state: %v", err)
	}

	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:   "ws_restart_cursor_retry",
		RemoteRoot:    "/notion",
		LocalRoot:     localDir,
		CursorTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("restart reconcile should retry cursor resolution: %v", err)
	}

	if client.latestEventIDCalls != 2 {
		t.Fatalf("expected cursor resolution to retry once, got %d call(s)", client.latestEventIDCalls)
	}
	if client.listTreeCalls != 0 || client.readFileCalls != 0 {
		t.Fatalf("expected retrying fast-path to avoid full pull; tree=%d read=%d", client.listTreeCalls, client.readFileCalls)
	}
	if got := strings.TrimSpace(syncer.state.EventsCursor); got != "evt_seed" {
		t.Fatalf("expected retry to seed evt_seed, got %q", got)
	}
}

func TestPullRestartFastPathTimeoutDoesNotFallBackToFullPull(t *testing.T) {
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
				EventID:  "evt_seed",
				Type:     "file.created",
				Path:     "/notion/Docs/A.md",
				Revision: "rev_1",
			},
		},
		latestEventIDErr: context.DeadlineExceeded,
		revisionCounter:  1,
		eventCounter:     1,
	}
	localDir := t.TempDir()
	localPath := filepath.Join(localDir, "Docs", "A.md")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir local: %v", err)
	}
	if err := os.WriteFile(localPath, []byte("# A"), 0o644); err != nil {
		t.Fatalf("seed local: %v", err)
	}
	if err := writeMountState(filepath.Join(localDir, ".relayfile-mount-state.json"), mountState{
		Files: map[string]trackedFile{
			"/notion/Docs/A.md": {
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Hash:        hashBytes([]byte("# A")),
			},
		},
		BootstrapComplete: true,
	}); err != nil {
		t.Fatalf("write seed state: %v", err)
	}

	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:   "ws_restart_cursor_timeout",
		RemoteRoot:    "/notion",
		LocalRoot:     localDir,
		CursorTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.Reconcile(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline after cursor retries, got %v", err)
	}

	if client.latestEventIDCalls != defaultCursorResolutionAttempts {
		t.Fatalf("expected %d cursor attempts, got %d", defaultCursorResolutionAttempts, client.latestEventIDCalls)
	}
	if client.listTreeCalls != 0 || client.readFileCalls != 0 {
		t.Fatalf("expected cursor timeout to avoid full pull fallback; tree=%d read=%d", client.listTreeCalls, client.readFileCalls)
	}
	if got := strings.TrimSpace(syncer.state.EventsCursor); got != "" {
		t.Fatalf("expected cursor to remain empty after failed resolution, got %q", got)
	}
}

func TestPullRestartFastPathPeriodicFullPullStillSkipsLazyGithubRepos(t *testing.T) {
	client := &fakeClient{
		files: map[string]RemoteFile{
			"/README.md": {
				Path:        "/README.md",
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     "# README v1",
			},
			"/github/repos/octocat/hello-world/_index.json": {
				Path:        "/github/repos/octocat/hello-world/_index.json",
				Revision:    "rev_repo_index",
				ContentType: "application/json",
				Content:     `{"repo":"hello-world"}`,
			},
			"/github/repos/octocat/hello-world/issues/issue-1.json": {
				Path:        "/github/repos/octocat/hello-world/issues/issue-1.json",
				Revision:    "rev_issue_1",
				ContentType: "application/json",
				Content:     `{"id":1}`,
			},
		},
		events: []FilesystemEvent{
			{
				EventID:  "evt_seed",
				Type:     "file.created",
				Path:     "/README.md",
				Revision: "rev_1",
			},
		},
		revisionCounter: 1,
		eventCounter:    1,
	}
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("# README v1"), 0o644); err != nil {
		t.Fatalf("seed local readme: %v", err)
	}
	if err := writeMountState(filepath.Join(localDir, ".relayfile-mount-state.json"), mountState{
		Files: map[string]trackedFile{
			"/README.md": {
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Hash:        hashBytes([]byte("# README v1")),
			},
		},
		LastEventAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		// BootstrapComplete: the workspace was fully mirrored by a prior
		// daemon, so the restart fast-path may legitimately skip the
		// bootstrap full pull (authoritative gate, replaces the unsafe
		// LastEventAt-only heuristic).
		BootstrapComplete: true,
	}); err != nil {
		t.Fatalf("write seed state: %v", err)
	}

	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:   "ws_restart_lazy_periodic",
		RemoteRoot:    "/",
		LocalRoot:     localDir,
		LazyRepos:     boolPtr(true),
		FullPullEvery: 2,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("restart reconcile: %v", err)
	}
	if client.listTreeCalls != 0 {
		t.Fatalf("expected restart fast-path to skip ListTree; got %d calls", client.listTreeCalls)
	}
	if client.readFileCalls != 0 {
		t.Fatalf("expected restart fast-path to skip ReadFile; got %d calls", client.readFileCalls)
	}

	client.files["/README.md"] = RemoteFile{
		Path:        "/README.md",
		Revision:    "rev_2",
		ContentType: "text/markdown",
		Content:     "# README v2",
	}
	client.appendEvent("file.updated", "/README.md", "rev_2")
	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("incremental reconcile after restart: %v", err)
	}

	client.files["/README.md"] = RemoteFile{
		Path:        "/README.md",
		Revision:    "rev_3",
		ContentType: "text/markdown",
		Content:     "# README v3",
	}
	client.appendEvent("file.updated", "/README.md", "rev_3")
	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatalf("periodic full-pull reconcile after restart: %v", err)
	}

	if client.listTreeCalls != 1 {
		t.Fatalf("expected exactly one periodic ListTree call, got %d", client.listTreeCalls)
	}
	if got := client.readFileCallsByPath["/github/repos/octocat/hello-world/_index.json"]; got != 0 {
		t.Fatalf("expected periodic full pull to skip repo _index reads in lazy mode, got %d", got)
	}
	if got := client.readFileCallsByPath["/github/repos/octocat/hello-world/issues/issue-1.json"]; got != 0 {
		t.Fatalf("expected periodic full pull to skip issue reads in lazy mode, got %d", got)
	}
	assertLocalFileContent(t, filepath.Join(localDir, "README.md"), "# README v3")
}
