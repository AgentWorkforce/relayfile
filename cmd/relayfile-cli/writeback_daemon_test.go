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
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/agentworkforce/relayfile/internal/mountsync"
	"github.com/agentworkforce/relayfile/internal/writeback"
	schemaassets "github.com/agentworkforce/relayfile/schemas"
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

	// Work item 6 acceptance: retry-exhaustion must atomically land the
	// canonical `<opID>.error.json` sidecar alongside the legacy payload
	// record and that sidecar must validate against the embedded JSON
	// Schema. The previous fix-loop iteration was reviewed FAIL because
	// only the payload was asserted — keep both assertions co-located.
	sidecarPath := filepath.Join(localDir, ".relay", "dead-letter", opID+".error.json")
	waitForFile(t, sidecarPath, "dead-letter error sidecar")
	sidecarBytes, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("read dead-letter sidecar failed: %v", err)
	}
	var sidecar map[string]any
	if err := json.Unmarshal(sidecarBytes, &sidecar); err != nil {
		t.Fatalf("parse dead-letter sidecar failed: %v\npayload:\n%s", err, string(sidecarBytes))
	}
	if got, _ := sidecar["opId"].(string); got != opID {
		t.Fatalf("sidecar opId = %q, want %q", got, opID)
	}
	// HTTP 400 maps to `provider_4xx` per classifyDeadLetterSidecarCode.
	if got, _ := sidecar["code"].(string); got != string(writeback.CodeProvider4xx) {
		t.Fatalf("sidecar code = %q, want %q", got, writeback.CodeProvider4xx)
	}
	if got, _ := sidecar["providerStatus"].(float64); int(got) != http.StatusBadRequest {
		t.Fatalf("sidecar providerStatus = %v, want %d", sidecar["providerStatus"], http.StatusBadRequest)
	}
	if got, _ := sidecar["attempts"].(float64); int(got) < 1 {
		t.Fatalf("sidecar attempts = %v, want >= 1", sidecar["attempts"])
	}
	if _, ok := sidecar["providerResponse"]; !ok {
		t.Fatalf("sidecar missing providerResponse: %v", sidecar)
	}

	// Round-trip the sidecar through the same JSON Schema validator the
	// daemon embeds. Any drift between Go marshalling and the schema would
	// surface here.
	schemaBytes, err := schemaassets.FS.ReadFile(writeback.SchemaPath)
	if err != nil {
		t.Fatalf("read embedded schema: %v", err)
	}
	var schemaDoc any
	if err := json.Unmarshal(schemaBytes, &schemaDoc); err != nil {
		t.Fatalf("parse embedded schema: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft7)
	compiler.AssertFormat()
	if err := compiler.AddResource(writeback.SchemaPath, schemaDoc); err != nil {
		t.Fatalf("add schema resource: %v", err)
	}
	compiled, err := compiler.Compile(writeback.SchemaPath)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	var sidecarValue any
	if err := json.Unmarshal(sidecarBytes, &sidecarValue); err != nil {
		t.Fatalf("re-parse sidecar for schema validation: %v", err)
	}
	if err := compiled.Validate(sidecarValue); err != nil {
		t.Fatalf("sidecar failed embedded schema validation: %v\npayload:\n%s", err, string(sidecarBytes))
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
