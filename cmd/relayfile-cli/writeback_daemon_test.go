package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/agentworkforce/relayfile/internal/mountsync"
)

func TestWritebackDaemonDeadLettersHTTP400(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const (
		workspaceID = "ws_deadletter"
		opID        = "op_deadletter_400"
		lastBody    = `{"opId":"op_deadletter_400","code":"bad_writeback","message":"known failure"}`
	)

	localDir := filepath.Join(t.TempDir(), "relayfile-mount")
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Relayfile-Op-Id", opID)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(lastBody))
	}))
	defer server.Close()

	baseTransport := server.Client().Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	rootCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var transportLogs bytes.Buffer
	client := mountsync.NewHTTPClient(server.URL, "token", &http.Client{
		Timeout:   time.Second,
		Transport: newWritebackFailureTransport(localDir, log.New(&transportLogs, "", 0), baseTransport),
	})
	syncer, err := mountsync.NewSyncer(client, mountsync.SyncerOptions{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/",
		LocalRoot:   localDir,
		WebSocket:   boolPtr(false),
		RootCtx:     rootCtx,
		Logger:      log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("NewSyncer failed: %v", err)
	}

	pidFile := mountPIDFile(localDir)
	logFile := mountLogFile(localDir)
	if err := writeDaemonPIDState(pidFile, daemonPIDState{
		PID:         4242,
		WorkspaceID: workspaceID,
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
			workspaceID,
			server.URL,
			100*time.Millisecond,
			time.Hour,
			0,
			false,
			false,
			false,
			pidFile,
			logFile,
		)
	}()

	localPath := filepath.Join(localDir, "notion", "Bad.md")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("create local parent dir failed: %v", err)
	}
	if err := os.WriteFile(localPath, []byte("# Bad\n"), 0o644); err != nil {
		t.Fatalf("write local file failed: %v", err)
	}
	if err := syncer.HandleLocalChange(context.Background(), "notion/Bad.md", fsnotify.Write); err == nil {
		t.Fatalf("expected writeback to fail")
	}

	deadLetterPath := filepath.Join(localDir, ".relay", "dead-letter", opID+".json")
	waitForFile(t, deadLetterPath, "dead-letter writeback")
	payload, err := os.ReadFile(deadLetterPath)
	if err != nil {
		t.Fatalf("read dead-letter file failed: %v", err)
	}
	var record struct {
		OpID       string `json:"opId"`
		LastStatus int    `json:"lastStatus"`
		LastBody   string `json:"lastBody"`
	}
	if err := json.Unmarshal(payload, &record); err != nil {
		t.Fatalf("parse dead-letter record failed: %v\npayload:\n%s", err, string(payload))
	}
	if record.OpID != opID {
		t.Fatalf("expected opId %q, got %q", opID, record.OpID)
	}
	if record.LastStatus != http.StatusBadRequest {
		t.Fatalf("expected lastStatus 400, got %d", record.LastStatus)
	}
	if record.LastBody != lastBody {
		t.Fatalf("expected lastBody %q, got %q", lastBody, record.LastBody)
	}

	var snapshot syncStateFile
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot = buildSyncStateSnapshot(syncStatusResponse{}, workspaceID, defaultMountMode, time.Second, localDir, readDaemonPID(localDir), "")
		if snapshot.FailedWritebacks > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if snapshot.FailedWritebacks == 0 {
		statePayload, _ := os.ReadFile(filepath.Join(localDir, ".relay", "state.json"))
		t.Fatalf("expected failedWritebacks > 0, got %+v; state=%q logs=%q", snapshot, string(statePayload), transportLogs.String())
	}

	cancel()
	select {
	case err := <-loopErrCh:
		if err != nil {
			t.Fatalf("mount loop returned error after cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("mount loop did not stop after cancel")
	}
}
