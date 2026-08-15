package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	bootstrapBlackBoxWorkspace       = "ws_bootstrap_convergence"
	bootstrapBlackBoxPath            = "/neon/advisors/by-project"
	bootstrapBlackBoxPendingDirs     = 14822
	bootstrapBlackBoxStartingFiles   = 27392
	bootstrapBlackBoxPageFiles       = 140
	bootstrapBlackBoxCheckpointFiles = 32
)

// TestBootstrapConvergenceAgainstV01039 is intentionally opt-in because it
// builds and executes two real CLIs and drains a production-sized, 14,822
// directory frontier. scripts/test-bootstrap-convergence-v01039.sh supplies
// binaries built from the v0.10.39 tag and the current checkout.
func TestBootstrapConvergenceAgainstV01039(t *testing.T) {
	baselineBin := strings.TrimSpace(os.Getenv("RELAYFILE_BASELINE_BIN"))
	candidateBin := strings.TrimSpace(os.Getenv("RELAYFILE_CANDIDATE_BIN"))
	if baselineBin == "" || candidateBin == "" {
		t.Skip("run scripts/test-bootstrap-convergence-v01039.sh to execute the built-CLI regression")
	}

	fixture := newBootstrapBlackBoxFixture(t)
	server := httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	defer server.Close()

	baselineVersion := runCLI(t, baselineBin, nil, "--version")
	candidateVersion := runCLI(t, candidateBin, nil, "--version")
	t.Logf("binaries: baseline=%q candidate=%q", strings.TrimSpace(baselineVersion.output), strings.TrimSpace(candidateVersion.output))

	t.Run("v0.10.39_repeats_the_stable_checkpoint", func(t *testing.T) {
		scenario := prepareBootstrapBlackBoxScenario(t, server.URL, "baseline")
		for cycle := 1; cycle <= 2; cycle++ {
			started := time.Now()
			treeCallsBefore := fixture.treeCalls.Load()
			firstReadBefore := fixture.readCount(bootstrapBlackBoxFilePath(0))
			result := runCLI(t, baselineBin, scenario.env, mountArgs(scenario, "300ms", true)...)
			if result.err == nil {
				t.Fatalf("baseline cycle %d unexpectedly completed: %s", cycle, result.output)
			}
			if !strings.Contains(result.output, "context deadline exceeded") {
				t.Fatalf("baseline cycle %d terminal cause did not name its deadline: %s", cycle, result.output)
			}
			state := readBootstrapBlackBoxState(t, scenario.stateFile)
			assertBootstrapCheckpoint(t, state, 0, bootstrapBlackBoxPendingDirs, bootstrapBlackBoxStartingFiles)
			waitForNoFixtureRequests(t, fixture, 3*time.Second)
			if fixture.treeCalls.Load() != treeCallsBefore+1 || fixture.readCount(bootstrapBlackBoxFilePath(0)) != firstReadBefore+1 {
				t.Fatalf("baseline cycle %d did not reach one ListTree followed by ReadFile: tree calls %d->%d first-file reads %d->%d", cycle,
					treeCallsBefore, fixture.treeCalls.Load(), firstReadBefore, fixture.readCount(bootstrapBlackBoxFilePath(0)))
			}
			t.Logf("BEFORE cycle=%d elapsed=%s path=%s cursor=%q page_offset=%d directories_pending=%d files_synced=%d blocked_operation=ReadFile terminal=%q",
				cycle, time.Since(started).Round(time.Millisecond), state.currentPath(), state.BootstrapCursor,
				state.BootstrapPageOffset, len(state.BootstrapDirectories), state.BootstrapFilesSynced, "context deadline exceeded")
		}
		assertPendingWritebackPreserved(t, scenario.outboxFile)
	})

	t.Run("candidate_checkpoints_restarts_and_completes", func(t *testing.T) {
		fixture.resetReads()
		scenario := prepareBootstrapBlackBoxScenario(t, server.URL, "candidate")
		started := time.Now()

		cmd, output := startCLI(t, candidateBin, scenario.env, mountArgs(scenario, "2m", false)...)
		progress := waitForBootstrapProgress(t, scenario.stateFile, fixture, 10*time.Second)
		if progress.BootstrapComplete {
			t.Fatal("candidate completed before the forced interruption")
		}
		if progress.BootstrapPageOffset < bootstrapBlackBoxCheckpointFiles {
			t.Fatalf("candidate page offset = %d, want at least %d", progress.BootstrapPageOffset, bootstrapBlackBoxCheckpointFiles)
		}
		if got := progress.BootstrapFilesSynced; got <= bootstrapBlackBoxStartingFiles {
			t.Fatalf("candidate files synced = %d, want > %d", got, bootstrapBlackBoxStartingFiles)
		}
		if got := len(progress.BootstrapDirectories); got != bootstrapBlackBoxPendingDirs {
			t.Fatalf("candidate pending directories = %d, want %d before the first page drains", got, bootstrapBlackBoxPendingDirs)
		}

		incompleteJSON := runCLI(t, candidateBin, scenario.env, statusArgs(scenario, true)...)
		if incompleteJSON.err != nil {
			t.Fatalf("candidate incomplete JSON status: %v\n%s", incompleteJSON.err, incompleteJSON.output)
		}
		assertStatus(t, incompleteJSON.output, "bootstrapping", true)
		incompleteText := runCLI(t, candidateBin, scenario.env, statusArgs(scenario, false)...)
		if incompleteText.err != nil || !strings.Contains(incompleteText.output, "mount: bootstrapping") {
			t.Fatalf("candidate incomplete text status did not report mount-level bootstrapping: %v\n%s", incompleteText.err, incompleteText.output)
		}
		t.Logf("STATUS incomplete (scratch path redacted):\n%s", redactBootstrapBlackBoxOutput(incompleteText.output, scenario))

		canceledBefore := fixture.canceled.Load()
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatalf("interrupt candidate: %v", err)
		}
		waitForProcess(t, cmd, output, 5*time.Second)
		waitForNoFixtureRequests(t, fixture, 3*time.Second)
		if fixture.canceled.Load() <= canceledBefore {
			t.Fatal("fixture did not observe an in-flight HTTP request canceled at the process boundary")
		}
		restartState := readBootstrapBlackBoxState(t, scenario.stateFile)
		if restartState.BootstrapPageOffset < bootstrapBlackBoxCheckpointFiles {
			t.Fatalf("interrupt lost checkpoint: page offset = %d", restartState.BootstrapPageOffset)
		}
		assertPendingWritebackPreserved(t, scenario.outboxFile)
		t.Logf("AFTER interrupted elapsed=%s path=%s cursor=%q page_offset=%d directories_pending=%d files_synced=%d active_requests=%d",
			time.Since(started).Round(time.Millisecond), restartState.currentPath(), restartState.BootstrapCursor,
			restartState.BootstrapPageOffset, len(restartState.BootstrapDirectories), restartState.BootstrapFilesSynced, fixture.active.Load())

		fixture.blockSlowReads.Store(false)
		makePendingWritebackDue(t, scenario.outboxFile)
		completionStarted := time.Now()
		completedRun := runCLI(t, candidateBin, scenario.env, mountArgs(scenario, "2m", true)...)
		if completedRun.err != nil {
			t.Fatalf("candidate restart did not complete: %v\n%s", completedRun.err, completedRun.output)
		}
		completed := readBootstrapBlackBoxState(t, scenario.stateFile)
		if !completed.BootstrapComplete {
			t.Fatalf("candidate returned success without bootstrapComplete=true: %+v", completed)
		}
		if got := countMirroredFixtureFiles(t, scenario.mirrorDir); got != bootstrapBlackBoxPageFiles {
			t.Fatalf("mirrored fixture files = %d, want %d", got, bootstrapBlackBoxPageFiles)
		}
		if got := fixture.readCount(bootstrapBlackBoxFilePath(0)); got != 1 {
			t.Fatalf("first committed file was reread after restart %d times; want exactly once", got)
		}
		assertWritebackAcknowledged(t, scenario)
		if fixture.bulkWrites.Load() != 1 {
			t.Fatalf("preserved writeback dispatches = %d, want 1", fixture.bulkWrites.Load())
		}
		waitForNoFixtureRequests(t, fixture, 3*time.Second)

		completeJSON := runCLI(t, candidateBin, scenario.env, statusArgs(scenario, true)...)
		if completeJSON.err != nil {
			t.Fatalf("candidate completed JSON status: %v\n%s", completeJSON.err, completeJSON.output)
		}
		assertStatus(t, completeJSON.output, "ready", false)
		completeText := runCLI(t, candidateBin, scenario.env, statusArgs(scenario, false)...)
		if completeText.err != nil || !strings.Contains(completeText.output, "mount: healthy") {
			t.Fatalf("candidate completed text status did not report mount-level healthy: %v\n%s", completeText.err, completeText.output)
		}
		t.Logf("STATUS complete (scratch path redacted):\n%s", redactBootstrapBlackBoxOutput(completeText.output, scenario))
		t.Logf("AFTER complete restart_elapsed=%s total_elapsed=%s bootstrap_complete=%t directories_pending=%d mirrored_files=%d active_requests=%d child_alive=false",
			time.Since(completionStarted).Round(time.Millisecond), time.Since(started).Round(time.Millisecond), completed.BootstrapComplete,
			len(completed.BootstrapDirectories), bootstrapBlackBoxPageFiles, fixture.active.Load())
	})
}

type bootstrapBlackBoxFixture struct {
	t              *testing.T
	active         atomic.Int64
	canceled       atomic.Int64
	bulkWrites     atomic.Int64
	treeCalls      atomic.Int64
	blockSlowReads atomic.Bool
	mu             sync.Mutex
	reads          map[string]int
}

func newBootstrapBlackBoxFixture(t *testing.T) *bootstrapBlackBoxFixture {
	t.Helper()
	f := &bootstrapBlackBoxFixture{t: t, reads: make(map[string]int)}
	f.blockSlowReads.Store(true)
	return f
}

func (f *bootstrapBlackBoxFixture) resetReads() {
	f.mu.Lock()
	f.reads = make(map[string]int)
	f.mu.Unlock()
	f.blockSlowReads.Store(true)
	f.treeCalls.Store(0)
}

func (f *bootstrapBlackBoxFixture) readCount(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads[path]
}

func (f *bootstrapBlackBoxFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/fs/export"):
		http.Error(w, `{"error":{"code":"not_found","message":"export disabled by fixture"}}`, http.StatusNotFound)
	case strings.HasSuffix(r.URL.Path, "/fs/tree"):
		f.treeCalls.Add(1)
		path := r.URL.Query().Get("path")
		entries := make([]map[string]any, 0)
		if path == bootstrapBlackBoxPath {
			entries = make([]map[string]any, 0, bootstrapBlackBoxPageFiles)
			for i := 0; i < bootstrapBlackBoxPageFiles; i++ {
				entries = append(entries, map[string]any{
					"path":     bootstrapBlackBoxFilePath(i),
					"type":     "file",
					"revision": fmt.Sprintf("rev_%03d", i),
					"size":     18,
				})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"path": path, "entries": entries, "nextCursor": nil})
	case strings.HasSuffix(r.URL.Path, "/fs/file") && r.Method == http.MethodGet:
		f.serveFile(w, r)
	case strings.HasSuffix(r.URL.Path, "/fs/bulk") && r.Method == http.MethodPost:
		var request struct {
			Files []struct {
				Path        string `json:"path"`
				ContentType string `json:"contentType"`
			} `json:"files"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Files) != 1 {
			http.Error(w, `{"code":"bad_request"}`, http.StatusBadRequest)
			return
		}
		f.bulkWrites.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"written": 1, "errorCount": 0, "errors": []any{}, "correlationId": "corr_fixture",
			"results": []map[string]any{{"path": request.Files[0].Path, "revision": "rev_writeback", "contentType": request.Files[0].ContentType}},
		})
	case strings.HasSuffix(r.URL.Path, "/fs/events"):
		_ = json.NewEncoder(w).Encode(map[string]any{"events": []any{}, "nextCursor": nil})
	case strings.HasSuffix(r.URL.Path, "/sync/status"):
		_ = json.NewEncoder(w).Encode(map[string]any{
			"workspaceId": bootstrapBlackBoxWorkspace,
			"providers":   []map[string]any{{"provider": "fixture", "status": "ready", "ready": true, "cursor": nil, "watermarkTs": nil, "lagSeconds": 0, "lastError": nil, "failureCodes": map[string]int{}}},
		})
	case strings.HasSuffix(r.URL.Path, "/sync/ingress"):
		_ = json.NewEncoder(w).Encode(map[string]any{"workspaceId": bootstrapBlackBoxWorkspace, "queueDepth": 0, "pendingTotal": 0})
	default:
		http.NotFound(w, r)
	}
}

func (f *bootstrapBlackBoxFixture) serveFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	f.mu.Lock()
	f.reads[path]++
	f.mu.Unlock()
	f.active.Add(1)
	defer f.active.Add(-1)

	index := bootstrapBlackBoxFileIndex(path)
	if f.blockSlowReads.Load() && index >= bootstrapBlackBoxCheckpointFiles {
		<-r.Context().Done()
		f.canceled.Add(1)
		return
	}
	delay := 70 * time.Millisecond
	if !f.blockSlowReads.Load() {
		delay = time.Millisecond
	}
	select {
	case <-r.Context().Done():
		f.canceled.Add(1)
		return
	case <-time.After(delay):
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"path": path, "revision": fmt.Sprintf("rev_%03d", index), "contentType": "application/json", "content": `{"fixture":true}`,
	})
}

func bootstrapBlackBoxFilePath(index int) string {
	return fmt.Sprintf("%s/project-%03d/advisor.json", bootstrapBlackBoxPath, index)
}

func bootstrapBlackBoxFileIndex(path string) int {
	marker := "/project-"
	start := strings.Index(path, marker)
	if start < 0 {
		return -1
	}
	start += len(marker)
	end := strings.IndexByte(path[start:], '/')
	if end < 0 {
		return -1
	}
	value, err := strconv.Atoi(path[start : start+end])
	if err != nil {
		return -1
	}
	return value
}

type bootstrapBlackBoxScenario struct {
	mirrorDir  string
	stateFile  string
	outboxFile string
	serverURL  string
	env        []string
}

func prepareBootstrapBlackBoxScenario(t *testing.T, serverURL, name string) bootstrapBlackBoxScenario {
	t.Helper()
	root := t.TempDir()
	mirrorDir := filepath.Join(root, "mirror")
	stateFile := filepath.Join(root, "private", "state.json")
	homeDir := filepath.Join(root, "home")
	outboxFile := filepath.Join(mirrorDir, ".relay", "outbox", "pending", "mountcmd_fixture.json")
	for _, dir := range []string{mirrorDir, filepath.Dir(stateFile), filepath.Dir(outboxFile), homeDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("prepare %s scenario: %v", name, err)
		}
	}
	directories := make([]string, 0, bootstrapBlackBoxPendingDirs)
	directories = append(directories, bootstrapBlackBoxPath)
	for i := 1; i < bootstrapBlackBoxPendingDirs; i++ {
		directories = append(directories, fmt.Sprintf("/q/%05d", i))
	}
	state := bootstrapBlackBoxState{
		WorkspaceID:                    bootstrapBlackBoxWorkspace,
		RemoteRoot:                     "/",
		LocalRoot:                      mirrorDir,
		Files:                          map[string]json.RawMessage{},
		BootstrapDirectories:           directories,
		BootstrapFilesSynced:           bootstrapBlackBoxStartingFiles,
		BootstrapStartedAt:             "2026-08-07T15:52:51Z",
		BootstrapDirectoriesDiscovered: bootstrapBlackBoxPendingDirs,
		SyncMode:                       "mirror",
	}
	writeJSONFile(t, stateFile, state, 0o600)
	writeJSONFile(t, outboxFile, map[string]any{
		"commandId": "mountcmd_fixture", "workspaceId": bootstrapBlackBoxWorkspace,
		"remotePath": "/fixture/writeback/preserved.md", "contentType": "text/markdown", "content": "preserve me",
		"hash": "fixture-hash", "exists": false, "status": "pending", "firstSeenAt": "2026-08-15T00:00:00Z",
		"nextAttemptAt": "2099-01-01T00:00:00Z", "attemptCount": 0, "expectedRevision": "0",
	}, 0o600)
	return bootstrapBlackBoxScenario{
		mirrorDir: mirrorDir, stateFile: stateFile, outboxFile: outboxFile, serverURL: serverURL,
		env: []string{
			"HOME=" + homeDir,
			"RELAYFILE_BOOTSTRAP_READ_CONCURRENCY=16",
			"RELAYFILE_BOOTSTRAP_STALL_CYCLES=20",
			"RELAYFILE_MOUNT_INTERVAL_JITTER=0",
		},
	}
}

type bootstrapBlackBoxState struct {
	WorkspaceID                    string                     `json:"workspaceId"`
	RemoteRoot                     string                     `json:"remoteRoot"`
	LocalRoot                      string                     `json:"localRoot"`
	Files                          map[string]json.RawMessage `json:"files"`
	BootstrapComplete              bool                       `json:"bootstrapComplete"`
	BootstrapDirectories           []string                   `json:"bootstrapDirectories"`
	BootstrapCursor                string                     `json:"bootstrapCursor"`
	BootstrapPageOffset            int                        `json:"bootstrapPageOffset"`
	BootstrapFilesSynced           int                        `json:"bootstrapFilesSynced"`
	BootstrapStartedAt             string                     `json:"bootstrapStartedAt"`
	BootstrapDirectoriesDiscovered int                        `json:"bootstrapDirectoriesDiscovered"`
	SyncMode                       string                     `json:"syncMode"`
}

func (s bootstrapBlackBoxState) currentPath() string {
	if len(s.BootstrapDirectories) == 0 {
		return ""
	}
	return s.BootstrapDirectories[0]
}

func mountArgs(s bootstrapBlackBoxScenario, bootstrapTimeout string, once bool) []string {
	args := []string{
		"mount", bootstrapBlackBoxWorkspace, s.mirrorDir,
		"--server", s.serverURL, "--token", "fixture-token", "--state-file", s.stateFile,
		"--timeout", "5s", "--bootstrap-timeout", bootstrapTimeout,
		"--websocket=false", "--low-memory=true",
	}
	if once {
		args = append(args, "--once")
	}
	return args
}

func statusArgs(s bootstrapBlackBoxScenario, jsonOutput bool) []string {
	args := []string{"status", bootstrapBlackBoxWorkspace, "--server", s.serverURL, "--token", "fixture-token"}
	if jsonOutput {
		args = append(args, "--json")
	}
	return args
}

type cliResult struct {
	output string
	err    error
}

func runCLI(t *testing.T, binary string, extraEnv []string, args ...string) cliResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("CLI exceeded process bound: %s %s\n%s", binary, strings.Join(args, " "), output)
	}
	return cliResult{output: string(output), err: err}
}

func startCLI(t *testing.T, binary string, extraEnv []string, args ...string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	output := &bytes.Buffer{}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start CLI: %v", err)
	}
	return cmd, output
}

func waitForProcess(t *testing.T, cmd *exec.Cmd, output *bytes.Buffer, timeout time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		return
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("CLI remained alive after interrupt:\n%s", output.String())
	}
}

func waitForBootstrapProgress(t *testing.T, stateFile string, fixture *bootstrapBlackBoxFixture, timeout time.Duration) bootstrapBlackBoxState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state := readBootstrapBlackBoxState(t, stateFile)
		if state.BootstrapPageOffset >= bootstrapBlackBoxCheckpointFiles && fixture.active.Load() > 0 {
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("candidate did not persist an in-page checkpoint and enter the next blocked read within %s", timeout)
	return bootstrapBlackBoxState{}
}

func waitForNoFixtureRequests(t *testing.T, fixture *bootstrapBlackBoxFixture, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fixture.active.Load() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%d fixture request(s) remained active after process exit", fixture.active.Load())
}

func readBootstrapBlackBoxState(t *testing.T, path string) bootstrapBlackBoxState {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read private state: %v", err)
	}
	var state bootstrapBlackBoxState
	if err := json.Unmarshal(payload, &state); err != nil {
		t.Fatalf("decode private state: %v", err)
	}
	return state
}

func assertBootstrapCheckpoint(t *testing.T, state bootstrapBlackBoxState, pageOffset, pendingDirs, filesSynced int) {
	t.Helper()
	if state.currentPath() != bootstrapBlackBoxPath || state.BootstrapCursor != "" || state.BootstrapPageOffset != pageOffset ||
		len(state.BootstrapDirectories) != pendingDirs || state.BootstrapFilesSynced != filesSynced {
		t.Fatalf("checkpoint mismatch: path=%q cursor=%q pageOffset=%d pending=%d filesSynced=%d",
			state.currentPath(), state.BootstrapCursor, state.BootstrapPageOffset, len(state.BootstrapDirectories), state.BootstrapFilesSynced)
	}
}

func assertStatus(t *testing.T, payload, wantStatus string, wantBootstrap bool) {
	t.Helper()
	var status struct {
		Status    string          `json:"status"`
		Bootstrap json.RawMessage `json:"bootstrap"`
	}
	if err := json.Unmarshal([]byte(payload), &status); err != nil {
		t.Fatalf("decode status JSON: %v\n%s", err, payload)
	}
	if status.Status != wantStatus {
		t.Fatalf("mount status = %q, want %q\n%s", status.Status, wantStatus, payload)
	}
	hasBootstrap := len(status.Bootstrap) > 0 && string(status.Bootstrap) != "null"
	if hasBootstrap != wantBootstrap {
		t.Fatalf("bootstrap presence = %t, want %t\n%s", hasBootstrap, wantBootstrap, payload)
	}
}

func assertPendingWritebackPreserved(t *testing.T, path string) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("pending writeback was lost: %v", err)
	}
	if !bytes.Contains(payload, []byte(`"status": "pending"`)) && !bytes.Contains(payload, []byte(`"status":"pending"`)) {
		t.Fatalf("pending writeback changed unexpectedly: %s", payload)
	}
}

func makePendingWritebackDue(t *testing.T, path string) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pending writeback before restart: %v", err)
	}
	var record map[string]any
	if err := json.Unmarshal(payload, &record); err != nil {
		t.Fatalf("decode pending writeback before restart: %v", err)
	}
	record["nextAttemptAt"] = "2026-08-15T00:00:00Z"
	writeJSONFile(t, path, record, 0o600)
}

func assertWritebackAcknowledged(t *testing.T, scenario bootstrapBlackBoxScenario) {
	t.Helper()
	if _, err := os.Stat(scenario.outboxFile); !os.IsNotExist(err) {
		t.Fatalf("pending writeback remained after successful dispatch: %v", err)
	}
	acked := filepath.Join(scenario.mirrorDir, ".relay", "outbox", "acked", "mountcmd_fixture.json")
	payload, err := os.ReadFile(acked)
	if err != nil {
		t.Fatalf("preserved writeback was not acknowledged: %v", err)
	}
	if !bytes.Contains(payload, []byte(`"status":"acked"`)) {
		t.Fatalf("acknowledged writeback has unexpected state: %s", payload)
	}
}

func countMirroredFixtureFiles(t *testing.T, mirrorDir string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(filepath.Join(mirrorDir, "neon", "advisors", "by-project"), func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("count mirrored fixture files: %v", err)
	}
	return count
}

func redactBootstrapBlackBoxOutput(output string, scenario bootstrapBlackBoxScenario) string {
	return strings.ReplaceAll(output, scenario.mirrorDir, "[scratch mirror]")
}

func writeJSONFile(t *testing.T, path string, value any, mode os.FileMode) {
	t.Helper()
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(path, payload, mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
