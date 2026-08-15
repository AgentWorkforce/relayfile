//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/delegatedauth"
)

// TestBuiltScopedMountBinaries is deliberately a real-process test. It builds
// and launches both public mount entry points instead of calling run() or an
// injected runner, so flag parsing, signal cancellation, durable restart
// state, scoped path placement, and operator exit codes are covered together.
func TestBuiltScopedMountBinaries(t *testing.T) {
	relayfileBin := buildScopedTestBinary(t, "./cmd/relayfile-cli", "relayfile")
	mountBin := buildScopedTestBinary(t, "./cmd/relayfile-mount", "relayfile-mount")
	home := t.TempDir()
	t.Setenv("HOME", home)
	clearRelayfileEnv(t)
	fixture := newBuiltScopedFixture(t)
	defer fixture.Close()

	t.Run("shipped relayfile lifecycle and operator aggregation", func(t *testing.T) {
		workspaceID := "ws_built_cli"
		token := testJWTWithWorkspace(workspaceID)
		singleWorkspaceID := "ws_built_cli_single"
		singleRoot := filepath.Join(t.TempDir(), "single")
		single := runBuiltScopedProcess(t, relayfileBin,
			"mount", singleWorkspaceID, singleRoot,
			"--server", fixture.URL,
			"--token", testJWTWithWorkspace(singleWorkspaceID),
			"--remote-path", "/notion/projects",
			"--local-layout", "scoped",
			"--once",
			"--websocket=false",
			"--timeout", "2s",
		)
		if single.err != nil {
			t.Fatalf("single-path built relayfile mount: %v\nstdout:\n%s\nstderr:\n%s", single.err, single.stdout, single.stderr)
		}
		waitForBuiltScopedFile(t, filepath.Join(singleRoot, "notion", "projects", "plan.md"))

		localRoot := filepath.Join(t.TempDir(), "mirror")
		mountArgs := []string{
			"mount", workspaceID, localRoot,
			"--server", fixture.URL,
			"--token", token,
			"--remote-path", "/github/repos/acme/cloud",
			"--remote-path", "/linear/issues",
			"--local-layout", "scoped",
			"--websocket=false",
			"--interval", "5s",
			"--timeout", "2s",
		}

		cmd, stdout, stderr := startBuiltScopedProcess(t, relayfileBin, mountArgs...)
		defer stopBuiltScopedProcess(cmd)
		githubFile := filepath.Join(localRoot, "github", "repos", "acme", "cloud", "README.md")
		linearFile := filepath.Join(localRoot, "linear", "issues", "seed.json")
		waitForBuiltScopedFile(t, githubFile)
		waitForBuiltScopedFile(t, linearFile)
		terminateBuiltScopedProcess(t, cmd, stdout, stderr)

		// Restart without path/layout flags. The persisted catalog topology is
		// authoritative and must not widen the mount back to the workspace root.
		restart := []string{
			"mount", workspaceID, localRoot,
			"--server", fixture.URL,
			"--token", token,
			"--once",
			"--websocket=false",
			"--timeout", "2s",
		}
		if result := runBuiltScopedProcess(t, relayfileBin, restart...); result.err != nil {
			t.Fatalf("restart persisted scoped relayfile mount: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
		}

		linearRoot := filepath.Dir(linearFile)
		draftPath := filepath.Join(linearRoot, "built-receipt.json")
		if err := os.WriteFile(draftPath, []byte(`{"title":"built receipt"}`+"\n"), 0o644); err != nil {
			t.Fatalf("write built-process draft: %v", err)
		}
		joinScopes, requiredScopes, err := writebackPushScopes("/linear/issues/built-receipt.json")
		if err != nil {
			t.Fatalf("resolve built-process writeback scopes: %v", err)
		}
		now := time.Now().UTC()
		if err := delegatedauth.SaveAtomic(
			delegatedCredentialsPathForRequest(workspaceID, joinScopes),
			delegatedauth.Bundle{
				RelayfileURL:          fixture.URL,
				RelayfileWorkspaceID:  workspaceID,
				AccessToken:           "rf_built_write",
				AccessTokenExpiresAt:  now.Add(time.Hour).Format(time.RFC3339),
				RefreshToken:          "refresh_built_write",
				RefreshTokenExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339),
				DelegationNotAfter:    now.Add(24 * time.Hour).Format(time.RFC3339),
				RelayauthURL:          fixture.URL,
				Scopes:                joinScopes,
				RelayfileScopes:       requiredScopes,
			},
		); err != nil {
			t.Fatalf("save built-process delegated credentials: %v", err)
		}
		push := runBuiltScopedProcess(t, relayfileBin, "writeback", "push", draftPath, "--workspace", workspaceID, "--timeout", "2s", "--json")
		if push.err != nil {
			t.Fatalf("built relayfile writeback push: %v\nstdout:\n%s\nstderr:\n%s", push.err, push.stdout, push.stderr)
		}
		acked := filepath.Join(linearRoot, ".relay", "outbox", "acked")
		entries, err := os.ReadDir(acked)
		if err != nil || len(entries) != 1 {
			t.Fatalf("acked receipt directory = %v, entries=%d", err, len(entries))
		}
		receiptPayload, err := os.ReadFile(filepath.Join(acked, entries[0].Name()))
		if err != nil {
			t.Fatalf("read built-process receipt: %v", err)
		}
		var receipt writebackPushReceipt
		if err := json.Unmarshal(receiptPayload, &receipt); err != nil {
			t.Fatalf("decode built-process receipt: %v", err)
		}
		if receipt.Status != "acked" || receipt.OpID != "op_built" || receipt.Content != "" {
			t.Fatalf("unexpected built-process receipt: %+v", receipt)
		}

		githubRoot := filepath.Dir(githubFile)
		writeBuiltScopedState(t, githubRoot, syncStateFile{
			WorkspaceID:               workspaceID,
			RemoteRoot:                "/github/repos/acme/cloud",
			Status:                    "healthy",
			LastSuccessfulReconcileAt: "2026-08-15T18:00:00Z",
			EventListener:             &syncStateEventListener{Mode: "websocket", Status: "listening"},
		})
		writeBuiltScopedState(t, linearRoot, syncStateFile{
			WorkspaceID:               workspaceID,
			RemoteRoot:                "/linear/issues",
			Status:                    "stalled",
			LastSuccessfulReconcileAt: "2026-08-15T18:01:00Z",
			LastError:                 &statusError{Code: "linear_failed", Message: "linear child failed"},
			EventListener:             &syncStateEventListener{Mode: "websocket", Status: "retrying"},
		})
		writeBuiltScopedJSON(t, filepath.Join(githubRoot, ".relay", "outbox", "failed", "failed.json"), map[string]any{"path": "/github/repos/acme/cloud/README.md"})
		writeBuiltScopedJSON(t, filepath.Join(linearRoot, ".relay", "outbox", "pending", "pending.json"), map[string]any{"path": "/linear/issues/built-receipt.json"})
		writeBuiltScopedJSON(t, filepath.Join(githubRoot, ".relay", "dead-letter", "op_github.json"), deadLetterRecord{OpID: "op_github", Path: "/github/repos/acme/cloud/README.md", LastStatus: 500})
		writeBuiltScopedJSON(t, filepath.Join(linearRoot, ".relay", "dead-letter", "op_linear.json"), deadLetterRecord{OpID: "op_linear", Path: "/linear/issues/seed.json", LastStatus: 429})

		status := runBuiltScopedProcess(t, relayfileBin, "workspace", "status", workspaceID, "--json")
		if status.err != nil {
			t.Fatalf("built relayfile workspace status: %v\n%s", status.err, status.stderr)
		}
		var health workspaceHealthReport
		if err := json.Unmarshal([]byte(status.stdout), &health); err != nil {
			t.Fatalf("decode aggregate workspace status %q: %v", status.stdout, err)
		}
		if health.Status != "healthy; stalled" || health.Readiness != "healthy; not-listening" ||
			health.LastError != "linear child failed" || health.OutboxPending != 1 || health.OutboxFailed != 1 || len(health.Scopes) != 2 {
			t.Fatalf("unexpected aggregate health: %+v", health)
		}

		deadList := runBuiltScopedProcess(t, relayfileBin, "writeback", "list", "--state", "dead", "--workspace", workspaceID, "--json")
		if deadList.err != nil {
			t.Fatalf("built relayfile writeback list: %v\n%s", deadList.err, deadList.stderr)
		}
		var items []writebackListItem
		if err := json.Unmarshal([]byte(deadList.stdout), &items); err != nil || len(items) != 2 {
			t.Fatalf("aggregate dead list = %q, items=%d, err=%v", deadList.stdout, len(items), err)
		}

		failureStatus := runBuiltScopedProcess(t, relayfileBin, "writeback", "status", workspaceID, "--json")
		if failureStatus.err == nil {
			t.Fatal("aggregate writeback status should exit non-zero for child dead letters")
		}
		var failures writebackStatusReport
		if err := json.Unmarshal([]byte(failureStatus.stdout), &failures); err != nil || len(failures.DeadLettered) != 2 {
			t.Fatalf("aggregate writeback status = %q, dead=%d, err=%v", failureStatus.stdout, len(failures.DeadLettered), err)
		}
	})

	t.Run("standalone relayfile-mount single multi restart and cancellation", func(t *testing.T) {
		workspaceID := "ws_built_daemon"
		token := testJWTWithWorkspace(workspaceID)
		t.Setenv("RELAYFILE_MOUNT_LOCAL_LAYOUT", "scoped")

		// Cloud always exports scoped layout through the environment, including
		// its canonical root-mount persona where no --remote-path is emitted.
		rootMount := filepath.Join(t.TempDir(), "root")
		rootState := filepath.Join(t.TempDir(), "root-state")
		rootResult := runBuiltScopedProcess(t, mountBin,
			"--base-url", fixture.URL,
			"--workspace", workspaceID,
			"--token", token,
			"--local-dir", rootMount,
			"--state-dir", rootState,
			"--once",
			"--websocket=false",
			"--timeout", "2s",
		)
		if rootResult.err != nil {
			t.Fatalf("root built daemon from Cloud env contract: %v\nstdout:\n%s\nstderr:\n%s", rootResult.err, rootResult.stdout, rootResult.stderr)
		}
		waitForBuiltScopedFile(t, filepath.Join(rootMount, "workspace.md"))

		singleRoot := filepath.Join(t.TempDir(), "single")
		singleState := filepath.Join(t.TempDir(), "single-state")
		single := runBuiltScopedProcess(t, mountBin,
			"--base-url", fixture.URL,
			"--workspace", workspaceID,
			"--token", token,
			"--local-dir", singleRoot,
			"--state-dir", singleState,
			"--remote-path", "/notion/projects",
			"--once",
			"--websocket=false",
			"--timeout", "2s",
		)
		if single.err != nil {
			t.Fatalf("single-path built daemon: %v\nstdout:\n%s\nstderr:\n%s", single.err, single.stdout, single.stderr)
		}
		waitForBuiltScopedFile(t, filepath.Join(singleRoot, "notion", "projects", "plan.md"))

		multiRoot := filepath.Join(t.TempDir(), "multi")
		multiState := filepath.Join(t.TempDir(), "multi-state")
		multiArgs := []string{
			"--base-url", fixture.URL,
			"--workspace", workspaceID,
			"--token", token,
			"--local-dir", multiRoot,
			"--state-dir", multiState,
			"--remote-path", "/github/repos/acme/cloud",
			"--remote-path", "/slack/channels/proj-cloud",
			"--websocket=false",
			"--interval", "5s",
			"--timeout", "2s",
		}
		cmd, stdout, stderr := startBuiltScopedProcess(t, mountBin, multiArgs...)
		defer stopBuiltScopedProcess(cmd)
		waitForBuiltScopedFile(t, filepath.Join(multiRoot, "github", "repos", "acme", "cloud", "README.md"))
		waitForBuiltScopedFile(t, filepath.Join(multiRoot, "slack", "channels", "proj-cloud", "topic.md"))
		terminateBuiltScopedProcess(t, cmd, stdout, stderr)

		restartArgs := append(append([]string(nil), multiArgs...), "--once")
		if restart := runBuiltScopedProcess(t, mountBin, restartArgs...); restart.err != nil {
			t.Fatalf("restart built daemon: %v\nstdout:\n%s\nstderr:\n%s", restart.err, restart.stdout, restart.stderr)
		}

		collisionRoot := filepath.Join(t.TempDir(), "collision")
		collision := runBuiltScopedProcess(t, mountBin,
			"--base-url", fixture.URL,
			"--workspace", workspaceID,
			"--token", token,
			"--local-dir", collisionRoot,
			"--state-file", filepath.Join(t.TempDir(), "shared-state.json"),
			"--remote-path", "/github/repos/acme/cloud",
			"--remote-path", "/slack/channels/proj-cloud",
			"--once",
		)
		if collision.err == nil || !strings.Contains(collision.stderr, "state-file") {
			t.Fatalf("shared state-file collision was not rejected: err=%v stderr=%q", collision.err, collision.stderr)
		}
	})
}

type builtScopedResult struct {
	stdout string
	stderr string
	err    error
}

func buildScopedTestBinary(t *testing.T, pkg, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))
	output := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-trimpath", "-o", output, pkg)
	cmd.Dir = repoRoot
	if payload, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, payload)
	}
	return output
}

func runBuiltScopedProcess(t *testing.T, binary string, args ...string) builtScopedResult {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = builtScopedProcessEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return builtScopedResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func startBuiltScopedProcess(t *testing.T, binary string, args ...string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = builtScopedProcessEnv()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", filepath.Base(binary), err)
	}
	return cmd, stdout, stderr
}

func builtScopedProcessEnv() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if (strings.HasPrefix(key, "RELAYFILE_") && key != "RELAYFILE_MOUNT_LOCAL_LAYOUT") || strings.HasPrefix(key, "AGENT_RELAY_") {
			continue
		}
		env = append(env, entry)
	}
	return env
}

func terminateBuiltScopedProcess(t *testing.T, cmd *exec.Cmd, stdout, stderr *bytes.Buffer) {
	t.Helper()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal built process: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			if exit, ok := err.(*exec.ExitError); !ok || exit.ProcessState == nil || !exit.ProcessState.Exited() {
				t.Fatalf("built process cancellation: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			}
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("built process did not exit after SIGTERM\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}

func stopBuiltScopedProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

func waitForBuiltScopedFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for built-process file %s", path)
}

func writeBuiltScopedState(t *testing.T, localDir string, state syncStateFile) {
	t.Helper()
	if err := writeMirrorStateFile(localDir, state); err != nil {
		t.Fatalf("write scoped state for %s: %v", localDir, err)
	}
}

func writeBuiltScopedJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
}

type builtScopedFixture struct {
	*httptest.Server
	mu      sync.Mutex
	exports map[string]int
}

func newBuiltScopedFixture(t *testing.T) *builtScopedFixture {
	t.Helper()
	fixture := &builtScopedFixture{exports: map[string]int{}}
	fixture.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/fs/export"):
			remotePath := r.URL.Query().Get("path")
			fixture.mu.Lock()
			fixture.exports[remotePath]++
			fixture.mu.Unlock()
			files := map[string][]map[string]any{
				"/":                          {{"path": "/workspace.md", "revision": "rev_root", "contentType": "text/markdown", "content": "Root"}},
				"/github/repos/acme/cloud":   {{"path": "/github/repos/acme/cloud/README.md", "revision": "rev_github", "contentType": "text/markdown", "content": "# Cloud"}},
				"/linear/issues":             {{"path": "/linear/issues/seed.json", "revision": "rev_linear", "contentType": "application/json", "content": `{"id":"LIN-1"}`}},
				"/slack/channels/proj-cloud": {{"path": "/slack/channels/proj-cloud/topic.md", "revision": "rev_slack", "contentType": "text/markdown", "content": "Scoped channel"}},
				"/notion/projects":           {{"path": "/notion/projects/plan.md", "revision": "rev_notion", "contentType": "text/markdown", "content": "Plan"}},
			}
			payload, ok := files[remotePath]
			if !ok {
				http.Error(w, fmt.Sprintf("unexpected export path %q", remotePath), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(payload)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/fs/events"):
			_, _ = io.WriteString(w, `{"events":[]}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/sync/status"):
			workspaceID := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/workspaces/"), "/")[0]
			_, _ = fmt.Fprintf(w, `{"workspaceId":%q,"providers":[]}`, workspaceID)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/fs/bulk"):
			var request bulkWriteRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Files) != 1 {
				http.Error(w, "invalid bulk request", http.StatusBadRequest)
				return
			}
			_, _ = fmt.Fprintf(w, `{"written":1,"errorCount":0,"correlationId":"corr_built","results":[{"path":%q,"revision":"rev_built","opId":"op_built","writeback":{"provider":"linear","state":"succeeded"}}]}`, request.Files[0].Path)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/ops/op_built"):
			_, _ = io.WriteString(w, `{"opId":"op_built","path":"/linear/issues/built-receipt.json","status":"succeeded","revision":"rev_built"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tokens/refresh":
			http.Error(w, "refresh should not be needed", http.StatusBadRequest)
		default:
			http.Error(w, "unexpected request "+r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	return fixture
}
