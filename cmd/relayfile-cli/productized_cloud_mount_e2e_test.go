package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountsync"
	"github.com/fsnotify/fsnotify"
)

func TestProductizedCloudMountE2EProof(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := filepath.Join(t.TempDir(), "relayfile-mount")
	relay := newProductizedRelayfileMock(t)
	defer relay.Close()
	relay.SetProviderStatus("github", "ready", 1)
	relay.UpsertFile("/github/repos/acme/api/pulls/42/metadata.json", "application/json", `{"title":"Initial PR"}`)

	cloud := newProductizedCloudMock(t, relay)
	defer cloud.Close()

	var setupOut bytes.Buffer
	if err := run([]string{
		"setup",
		"--cloud-api-url", cloud.URL(),
		"--cloud-token", cloud.initialAccessToken,
		"--workspace", "demo",
		"--provider", "github",
		"--local-dir", localDir,
		"--no-open",
		"--once",
		"--connect-timeout", "2s",
	}, strings.NewReader(""), &setupOut, &setupOut); err != nil {
		t.Fatalf("setup failed: %v\noutput:\n%s", err, setupOut.String())
	}
	assertFileContentEventually(t, filepath.Join(localDir, "github", "repos", "acme", "api", "pulls", "42", "metadata.json"), `{"title":"Initial PR"}`)
	if cloud.JoinCount() != 1 {
		t.Fatalf("expected one workspace join after setup, got %d", cloud.JoinCount())
	}

	relay.SetProviderStatus("notion", "syncing", 4)
	relay.UpsertFile("/notion/Docs/Plan.md", "text/markdown", "# Seeded notion plan\n")

	var connectOut bytes.Buffer
	if err := run([]string{
		"integration", "connect", "notion",
		"--workspace", "demo",
		"--cloud-api-url", cloud.URL(),
		"--no-open",
		"--timeout", "2s",
	}, strings.NewReader(""), &connectOut, &connectOut); err != nil {
		t.Fatalf("integration connect failed: %v\noutput:\n%s", err, connectOut.String())
	}
	if !strings.Contains(connectOut.String(), "notion connected") {
		t.Fatalf("expected notion connect confirmation, got %q", connectOut.String())
	}
	relay.SetProviderStatus("notion", "ready", 1)

	var listOut bytes.Buffer
	if err := run([]string{
		"integration", "list",
		"--workspace", "demo",
		"--cloud-api-url", cloud.URL(),
		"--json",
	}, strings.NewReader(""), &listOut, &listOut); err != nil {
		t.Fatalf("integration list failed: %v\noutput:\n%s", err, listOut.String())
	}
	var listed []cloudIntegrationListEntry
	if err := json.Unmarshal(listOut.Bytes(), &listed); err != nil {
		t.Fatalf("parse integration list failed: %v\npayload:\n%s", err, listOut.String())
	}
	if providers := integrationProviders(listed); strings.Join(providers, ",") != "github,notion" {
		t.Fatalf("expected github and notion integrations, got %v", providers)
	}

	creds, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials failed: %v", err)
	}
	record, err := resolveWorkspaceRecord("demo")
	if err != nil {
		t.Fatalf("resolveWorkspaceRecord failed: %v", err)
	}

	prevLogWriter := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(prevLogWriter)

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	disableWebSocket := false
	syncer, err := mountsync.NewSyncer(
		mountsync.NewHTTPClient(creds.Server, creds.Token, relay.HTTPClient()),
		mountsync.SyncerOptions{
			WorkspaceID: record.ID,
			RemoteRoot:  "/",
			LocalRoot:   localDir,
			WebSocket:   &disableWebSocket,
			RootCtx:     rootCtx,
			Logger:      log.New(io.Discard, "", 0),
		},
	)
	if err != nil {
		t.Fatalf("NewSyncer failed: %v", err)
	}

	pidFile := mountPIDFile(localDir)
	logFile := mountLogFile(localDir)
	if err := writeDaemonPIDState(pidFile, daemonPIDState{
		PID:         4242,
		WorkspaceID: record.ID,
		LocalDir:    localDir,
		LogFile:     logFile,
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("writeDaemonPIDState failed: %v", err)
	}

	loopErrCh := make(chan error, 1)
	go func() {
		loopErrCh <- runMountLoop(
			rootCtx,
			syncer,
			localDir,
			record.ID,
			creds.Server,
			500*time.Millisecond,
			100*time.Millisecond,
			0,
			false,
			false,
			false,
			pidFile,
			logFile,
		)
	}()

	waitForFile(t, filepath.Join(localDir, ".relay", "state.json"), "public mirror state")

	relay.UpsertFile("/notion/Docs/RemoteAdded.md", "text/markdown", "# Remote added\n")
	assertFileContentEventually(t, filepath.Join(localDir, "notion", "Docs", "RemoteAdded.md"), "# Remote added\n")

	localWritebackPath := filepath.Join(localDir, "notion", "Docs", "RemoteAdded.md")
	if err := os.WriteFile(localWritebackPath, []byte("# Local writeback\n"), 0o644); err != nil {
		t.Fatalf("write local mirror file failed: %v", err)
	}
	if err := syncer.HandleLocalChange(context.Background(), "notion/Docs/RemoteAdded.md", fsnotify.Write); err != nil {
		t.Fatalf("HandleLocalChange writeback failed: %v", err)
	}
	waitForCondition(t, 5*time.Second, "remote writeback", func() bool {
		file, ok := relay.File("/notion/Docs/RemoteAdded.md")
		return ok && file.Content == "# Local writeback\n"
	})

	relay.UpsertFile("/notion/Docs/RemoteAdded.md", "text/markdown", "# Remote conflict\n")
	relay.ConflictOnce("/notion/Docs/RemoteAdded.md")
	if err := os.WriteFile(localWritebackPath, []byte("# Local conflict\n"), 0o644); err != nil {
		t.Fatalf("write local conflicting file failed: %v", err)
	}
	if err := syncer.HandleLocalChange(context.Background(), "notion/Docs/RemoteAdded.md", fsnotify.Write); err != nil {
		t.Fatalf("HandleLocalChange conflict failed: %v", err)
	}
	assertFileContentEventually(t, localWritebackPath, "# Remote conflict\n")
	waitForCondition(t, 5*time.Second, "conflict artifact", func() bool {
		matches, _ := filepath.Glob(filepath.Join(localDir, ".relay", "conflicts", "notion", "Docs", "RemoteAdded.md.*.local"))
		return len(matches) == 1
	})

	var statusOut bytes.Buffer
	if err := run([]string{"status", "demo", "--json"}, strings.NewReader(""), &statusOut, &statusOut); err != nil {
		t.Fatalf("status failed: %v\noutput:\n%s", err, statusOut.String())
	}
	var snapshot syncStateFile
	if err := json.Unmarshal(statusOut.Bytes(), &snapshot); err != nil {
		t.Fatalf("parse status json failed: %v\npayload:\n%s", err, statusOut.String())
	}
	if snapshot.PendingConflicts != 1 {
		t.Fatalf("expected one visible conflict, got %+v", snapshot)
	}
	if snapshot.Daemon == nil || snapshot.Daemon.PID != 4242 {
		t.Fatalf("expected daemon metadata in status snapshot, got %+v", snapshot.Daemon)
	}
	if providers := syncStateProviders(snapshot.Providers); strings.Join(providers, ",") != "github,notion" {
		t.Fatalf("expected github and notion providers in status, got %v", providers)
	}

	if err := saveCloudCredentials(cloudCredentials{
		APIURL:               cloud.URL(),
		AccessToken:          cloud.expiredAccessToken,
		RefreshToken:         cloud.refreshToken,
		AccessTokenExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		UpdatedAt:            time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("saveCloudCredentials failed: %v", err)
	}

	currentToken, err := currentRelayToken()
	if err != nil {
		t.Fatalf("currentRelayToken failed: %v", err)
	}
	relay.RejectToken(currentToken)
	waitForCondition(t, 5*time.Second, "cloud token refresh + workspace rejoin", func() bool {
		creds, loadErr := loadCredentials()
		if loadErr != nil {
			return false
		}
		return cloud.RefreshCount() >= 1 && cloud.JoinCount() >= 3 && creds.Token != currentToken
	})

	relay.UpsertFile("/github/repos/acme/api/pulls/42/after-refresh.md", "text/markdown", "# After refresh\n")
	assertFileContentEventually(t, filepath.Join(localDir, "github", "repos", "acme", "api", "pulls", "42", "after-refresh.md"), "# After refresh\n")

	cancel()
	select {
	case err := <-loopErrCh:
		if err != nil {
			t.Fatalf("mount loop exited with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for mount loop shutdown")
	}
	if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove pid file failed: %v", err)
	}

	relay.UpsertFile("/github/repos/acme/api/pulls/42/restarted.md", "text/markdown", "# Restarted sync\n")
	var restartOut bytes.Buffer
	if err := run([]string{
		"mount", "demo", localDir,
		"--once",
		"--websocket=false",
	}, strings.NewReader(""), &restartOut, &restartOut); err != nil {
		t.Fatalf("restart mount failed: %v\noutput:\n%s", err, restartOut.String())
	}
	assertFileContentEventually(t, filepath.Join(localDir, "github", "repos", "acme", "api", "pulls", "42", "restarted.md"), "# Restarted sync\n")
}

type productizedRelayfileMock struct {
	t *testing.T

	mu             sync.Mutex
	server         *httptest.Server
	revisionCount  int
	files          map[string]productizedRemoteFile
	providers      map[string]*productizedProviderStatus
	validTokens    map[string]struct{}
	rejectedTokens map[string]struct{}
	conflictOnce   map[string]struct{}
}

type productizedRemoteFile struct {
	Path        string
	Revision    string
	ContentType string
	Content     string
	Encoding    string
}

type productizedProviderStatus struct {
	Provider    string
	Status      string
	LagSeconds  int
	LastEventAt string
}

func newProductizedRelayfileMock(t *testing.T) *productizedRelayfileMock {
	t.Helper()
	mock := &productizedRelayfileMock{
		t:             t,
		files:         map[string]productizedRemoteFile{},
		providers:     map[string]*productizedProviderStatus{},
		validTokens:   map[string]struct{}{},
		rejectedTokens: map[string]struct{}{},
		conflictOnce:  map[string]struct{}{},
	}
	mock.server = httptest.NewServer(http.HandlerFunc(mock.serveHTTP))
	return mock
}

func (m *productizedRelayfileMock) Close() {
	m.server.Close()
}

func (m *productizedRelayfileMock) URL() string {
	return m.server.URL
}

func (m *productizedRelayfileMock) HTTPClient() *http.Client {
	return m.server.Client()
}

func (m *productizedRelayfileMock) ActivateToken(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rejectedTokens, token)
	m.validTokens[token] = struct{}{}
}

func (m *productizedRelayfileMock) RejectToken(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rejectedTokens[token] = struct{}{}
}

func (m *productizedRelayfileMock) SetProviderStatus(provider, status string, lagSeconds int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.ensureProvider(provider)
	current.Status = status
	current.LagSeconds = lagSeconds
	if current.LastEventAt == "" {
		current.LastEventAt = time.Now().UTC().Format(time.RFC3339)
	}
}

func (m *productizedRelayfileMock) UpsertFile(path, contentType, content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upsertFileLocked(path, contentType, content)
}

func (m *productizedRelayfileMock) ConflictOnce(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conflictOnce[normalizeMockRemotePath(path)] = struct{}{}
}

func (m *productizedRelayfileMock) File(path string) (productizedRemoteFile, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	file, ok := m.files[normalizeMockRemotePath(path)]
	return file, ok
}

func (m *productizedRelayfileMock) serveHTTP(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if !m.authorized(token) {
		writeMockJSON(w, http.StatusUnauthorized, map[string]any{
			"code":    "unauthorized",
			"message": "token rejected",
		})
		return
	}

	switch {
	case strings.HasSuffix(r.URL.Path, "/sync/status") && r.Method == http.MethodGet:
		m.serveSyncStatus(w)
	case strings.HasSuffix(r.URL.Path, "/fs/export") && r.Method == http.MethodGet:
		m.serveExport(w, r)
	case strings.HasSuffix(r.URL.Path, "/fs/tree") && r.Method == http.MethodGet:
		m.serveTree(w, r)
	case strings.HasSuffix(r.URL.Path, "/fs/events") && r.Method == http.MethodGet:
		writeMockJSON(w, http.StatusOK, mountsync.EventFeed{Events: []mountsync.FilesystemEvent{}})
	case strings.HasSuffix(r.URL.Path, "/fs/file") && r.Method == http.MethodGet:
		m.serveReadFile(w, r)
	case strings.HasSuffix(r.URL.Path, "/fs/bulk") && r.Method == http.MethodPost:
		m.serveBulkWrite(w, r)
	default:
		writeMockJSON(w, http.StatusNotFound, map[string]any{
			"code":    "not_found",
			"message": fmt.Sprintf("unhandled route %s %s", r.Method, r.URL.Path),
		})
	}
}

func (m *productizedRelayfileMock) authorized(token string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if token == "" {
		return false
	}
	if _, rejected := m.rejectedTokens[token]; rejected {
		return false
	}
	_, ok := m.validTokens[token]
	return ok
}

func (m *productizedRelayfileMock) serveSyncStatus(w http.ResponseWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	providers := make([]syncProviderStatus, 0, len(m.providers))
	for _, status := range m.providers {
		lastEvent := status.LastEventAt
		providers = append(providers, syncProviderStatus{
			Provider:    status.Provider,
			Status:      status.Status,
			LagSeconds:  status.LagSeconds,
			WatermarkTs: stringPtr(lastEvent),
		})
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Provider < providers[j].Provider })
	writeMockJSON(w, http.StatusOK, syncStatusResponse{
		WorkspaceID: "ws_productized",
		Providers:   providers,
	})
}

func (m *productizedRelayfileMock) serveExport(w http.ResponseWriter, r *http.Request) {
	root := normalizeMockRemotePath(r.URL.Query().Get("path"))
	m.mu.Lock()
	defer m.mu.Unlock()
	files := make([]exportedFile, 0, len(m.files))
	for _, file := range m.files {
		if !isUnderMockRoot(root, file.Path) {
			continue
		}
		files = append(files, exportedFile{
			Path:        file.Path,
			Revision:    file.Revision,
			ContentType: file.ContentType,
			Content:     file.Content,
			Encoding:    file.Encoding,
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	writeMockJSON(w, http.StatusOK, files)
}

func (m *productizedRelayfileMock) serveTree(w http.ResponseWriter, r *http.Request) {
	root := normalizeMockRemotePath(r.URL.Query().Get("path"))
	m.mu.Lock()
	defer m.mu.Unlock()

	entries := make([]treeEntry, 0)
	seenDirs := map[string]struct{}{}
	for _, file := range m.files {
		if !isUnderMockRoot(root, file.Path) {
			continue
		}
		relative := strings.TrimPrefix(file.Path, root)
		relative = strings.TrimPrefix(relative, "/")
		if relative == "" {
			entries = append(entries, treeEntry{Path: file.Path, Type: "file", Revision: file.Revision})
			continue
		}
		parts := strings.Split(relative, "/")
		if len(parts) == 1 {
			entries = append(entries, treeEntry{Path: file.Path, Type: "file", Revision: file.Revision})
			continue
		}
		dirPath := normalizeMockRemotePath(root + "/" + parts[0])
		if _, seen := seenDirs[dirPath]; seen {
			continue
		}
		seenDirs[dirPath] = struct{}{}
		entries = append(entries, treeEntry{Path: dirPath, Type: "dir", Revision: "dir_rev_1"})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	writeMockJSON(w, http.StatusOK, treeResponse{Path: root, Entries: entries})
}

func (m *productizedRelayfileMock) serveReadFile(w http.ResponseWriter, r *http.Request) {
	path := normalizeMockRemotePath(r.URL.Query().Get("path"))
	m.mu.Lock()
	defer m.mu.Unlock()
	file, ok := m.files[path]
	if !ok {
		writeMockJSON(w, http.StatusNotFound, map[string]any{
			"code":    "not_found",
			"message": "file not found",
		})
		return
	}
	writeMockJSON(w, http.StatusOK, readFileResponse{
		Path:        file.Path,
		Revision:    file.Revision,
		ContentType: file.ContentType,
		Content:     file.Content,
		Encoding:    file.Encoding,
	})
}

func (m *productizedRelayfileMock) serveBulkWrite(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Files []mountsync.BulkWriteFile `json:"files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeMockJSON(w, http.StatusBadRequest, map[string]any{
			"code":    "bad_request",
			"message": "invalid json",
		})
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	response := mountsync.BulkWriteResponse{
		Errors:  []mountsync.BulkWriteError{},
		Results: []mountsync.BulkWriteResult{},
	}
	for _, file := range payload.Files {
		path := normalizeMockRemotePath(file.Path)
		if _, conflict := m.conflictOnce[path]; conflict {
			delete(m.conflictOnce, path)
			response.Errors = append(response.Errors, mountsync.BulkWriteError{
				Path:    path,
				Code:    "conflict",
				Message: "revision conflict",
			})
			continue
		}
		if strings.TrimSpace(file.ContentType) == "" {
			file.ContentType = "text/markdown"
		}
		updated := m.upsertFileLocked(path, file.ContentType, file.Content)
		response.Results = append(response.Results, mountsync.BulkWriteResult{
			Path:        updated.Path,
			Revision:    updated.Revision,
			ContentType: updated.ContentType,
		})
		response.Written++
	}
	response.ErrorCount = len(response.Errors)
	response.CorrelationID = "corr_productized_mock_bulk"
	writeMockJSON(w, http.StatusAccepted, response)
}

func (m *productizedRelayfileMock) upsertFileLocked(path, contentType, content string) productizedRemoteFile {
	path = normalizeMockRemotePath(path)
	m.revisionCount++
	file := productizedRemoteFile{
		Path:        path,
		Revision:    fmt.Sprintf("rev_%d", m.revisionCount),
		ContentType: strings.TrimSpace(contentType),
		Content:     content,
	}
	if file.ContentType == "" {
		file.ContentType = "text/markdown"
	}
	m.files[path] = file
	provider := providerFromRemotePath(path)
	status := m.ensureProvider(provider)
	if status.Status == "" {
		status.Status = "ready"
	}
	status.LastEventAt = time.Now().UTC().Format(time.RFC3339)
	return file
}

func (m *productizedRelayfileMock) ensureProvider(provider string) *productizedProviderStatus {
	current, ok := m.providers[provider]
	if !ok {
		current = &productizedProviderStatus{Provider: provider, Status: "ready"}
		m.providers[provider] = current
	}
	return current
}

type productizedCloudMock struct {
	t *testing.T

	mu                 sync.Mutex
	server             *httptest.Server
	relay              *productizedRelayfileMock
	workspaceID        string
	initialAccessToken string
	expiredAccessToken string
	refreshedToken     string
	refreshToken       string
	joinCount          int
	refreshCount       int
	providers          map[string]string
}

func newProductizedCloudMock(t *testing.T, relay *productizedRelayfileMock) *productizedCloudMock {
	t.Helper()
	mock := &productizedCloudMock{
		t:                  t,
		relay:              relay,
		workspaceID:        "ws_productized",
		initialAccessToken: "cld_setup",
		expiredAccessToken: "cld_expired",
		refreshedToken:     "cld_refreshed",
		refreshToken:       "cld_refresh",
		providers:          map[string]string{},
	}
	mock.server = httptest.NewServer(http.HandlerFunc(mock.serveHTTP))
	return mock
}

func (m *productizedCloudMock) Close() {
	m.server.Close()
}

func (m *productizedCloudMock) URL() string {
	return m.server.URL
}

func (m *productizedCloudMock) JoinCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.joinCount
}

func (m *productizedCloudMock) RefreshCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.refreshCount
}

func (m *productizedCloudMock) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/v1/workspaces" && r.Method == http.MethodPost:
		m.serveCreateWorkspace(w, r)
	case r.URL.Path == "/api/v1/auth/token/refresh" && r.Method == http.MethodPost:
		m.serveRefreshCloudToken(w, r)
	case strings.HasSuffix(r.URL.Path, "/join") && r.Method == http.MethodPost:
		m.serveJoinWorkspace(w, r)
	case strings.HasSuffix(r.URL.Path, "/integrations/connect-session") && r.Method == http.MethodPost:
		m.serveConnectSession(w, r)
	case strings.Contains(r.URL.Path, "/integrations/") && strings.HasSuffix(r.URL.Path, "/status") && r.Method == http.MethodGet:
		m.serveIntegrationStatus(w, r)
	case strings.HasSuffix(r.URL.Path, "/integrations") && r.Method == http.MethodGet:
		m.serveIntegrationList(w, r)
	default:
		writeMockJSON(w, http.StatusNotFound, map[string]any{
			"code":    "not_found",
			"message": fmt.Sprintf("unhandled route %s %s", r.Method, r.URL.Path),
		})
	}
}

func (m *productizedCloudMock) serveCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	if got := bearerToken(r); got != m.initialAccessToken {
		m.t.Fatalf("unexpected create workspace token: %q", got)
	}
	writeMockJSON(w, http.StatusOK, cloudWorkspaceCreateResponse{
		WorkspaceID:  m.workspaceID,
		RelayfileURL: m.relay.URL(),
		CreatedAt:    "2026-05-02T00:00:00Z",
		Name:         "demo",
	})
}

func (m *productizedCloudMock) serveRefreshCloudToken(w http.ResponseWriter, r *http.Request) {
	if got := bearerToken(r); got != m.expiredAccessToken {
		m.t.Fatalf("unexpected refresh token auth header: %q", got)
	}
	var body cloudTokenRefreshRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.t.Fatalf("decode refresh request failed: %v", err)
	}
	if body.RefreshToken != m.refreshToken {
		m.t.Fatalf("unexpected refresh token: %q", body.RefreshToken)
	}
	m.mu.Lock()
	m.refreshCount++
	m.mu.Unlock()
	writeMockJSON(w, http.StatusOK, cloudCredentials{
		APIURL:                m.URL(),
		AccessToken:           m.refreshedToken,
		RefreshToken:          m.refreshToken,
		AccessTokenExpiresAt:  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	})
}

func (m *productizedCloudMock) serveJoinWorkspace(w http.ResponseWriter, r *http.Request) {
	got := bearerToken(r)
	if got != m.initialAccessToken && got != m.refreshedToken {
		m.t.Fatalf("unexpected join token: %q", got)
	}
	m.mu.Lock()
	m.joinCount++
	token := fmt.Sprintf("rf_token_%d", m.joinCount)
	m.mu.Unlock()

	m.relay.ActivateToken(token)
	writeMockJSON(w, http.StatusOK, cloudWorkspaceJoinResponse{
		WorkspaceID: m.workspaceID,
		Token:       token,
		RelayfileURL: m.relay.URL(),
	})
}

func (m *productizedCloudMock) serveConnectSession(w http.ResponseWriter, r *http.Request) {
	if got := bearerToken(r); !strings.HasPrefix(got, "rf_token_") {
		m.t.Fatalf("unexpected connect-session auth token: %q", got)
	}
	var body cloudConnectSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.t.Fatalf("decode connect-session body failed: %v", err)
	}
	if len(body.AllowedIntegrations) != 1 {
		m.t.Fatalf("expected exactly one allowed integration, got %#v", body.AllowedIntegrations)
	}
	provider := body.AllowedIntegrations[0]
	connectionID := "conn_" + provider
	m.mu.Lock()
	m.providers[provider] = connectionID
	m.mu.Unlock()
	writeMockJSON(w, http.StatusOK, cloudConnectSessionResponse{
		ConnectLink:  "https://connect.mock.local/" + provider,
		ConnectionID: connectionID,
	})
}

func (m *productizedCloudMock) serveIntegrationStatus(w http.ResponseWriter, r *http.Request) {
	if got := bearerToken(r); !strings.HasPrefix(got, "rf_token_") {
		m.t.Fatalf("unexpected integration status auth token: %q", got)
	}
	writeMockJSON(w, http.StatusOK, cloudIntegrationReadyResponse{Ready: true})
}

func (m *productizedCloudMock) serveIntegrationList(w http.ResponseWriter, r *http.Request) {
	if got := bearerToken(r); !strings.HasPrefix(got, "rf_token_") {
		m.t.Fatalf("unexpected integration list auth token: %q", got)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	entries := make([]cloudIntegrationListEntry, 0, len(m.providers))
	for provider, connectionID := range m.providers {
		fileStatus, lag, lastEventAt := m.providerSnapshot(provider)
		entries = append(entries, cloudIntegrationListEntry{
			Provider:     provider,
			Status:       fileStatus,
			LagSeconds:   lag,
			LastEventAt:  lastEventAt,
			ConnectionID: connectionID,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Provider < entries[j].Provider })
	writeMockJSON(w, http.StatusOK, entries)
}

func (m *productizedCloudMock) providerSnapshot(provider string) (string, int, string) {
	m.relay.mu.Lock()
	defer m.relay.mu.Unlock()
	current := m.relay.providers[provider]
	if current == nil {
		return "unknown", 0, ""
	}
	return current.Status, current.LagSeconds, current.LastEventAt
}

func integrationProviders(entries []cloudIntegrationListEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Provider)
	}
	sort.Strings(out)
	return out
}

func syncStateProviders(entries []syncStateProvider) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Provider)
	}
	sort.Strings(out)
	return out
}

func currentRelayToken() (string, error) {
	creds, err := loadCredentials()
	if err != nil {
		return "", err
	}
	return creds.Token, nil
}

func waitForCondition(t *testing.T, timeout time.Duration, description string, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func waitForFile(t *testing.T, path, description string) {
	t.Helper()
	waitForCondition(t, 5*time.Second, description, func() bool {
		_, err := os.Stat(path)
		return err == nil
	})
}

func assertFileContentEventually(t *testing.T, path, want string) {
	t.Helper()
	waitForCondition(t, 5*time.Second, path, func() bool {
		data, err := os.ReadFile(path)
		return err == nil && string(data) == want
	})
}

func normalizeMockRemotePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "/" {
		return "/"
	}
	path = strings.ReplaceAll(path, "\\", "/")
	path = strings.TrimPrefix(path, "/")
	return "/" + strings.Trim(path, "/")
}

func isUnderMockRoot(root, path string) bool {
	root = normalizeMockRemotePath(root)
	path = normalizeMockRemotePath(path)
	if root == "/" {
		return true
	}
	return path == root || strings.HasPrefix(path, root+"/")
}

func providerFromRemotePath(path string) string {
	path = strings.TrimPrefix(normalizeMockRemotePath(path), "/")
	first, _, _ := strings.Cut(path, "/")
	if first == "" {
		return "unknown"
	}
	return first
}

func providerFromStatusPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 7 {
		return "unknown"
	}
	return parts[6]
}

func writeMockJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		panic(err)
	}
}

func bearerToken(r *http.Request) string {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	return strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
}

func stringPtr(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}
