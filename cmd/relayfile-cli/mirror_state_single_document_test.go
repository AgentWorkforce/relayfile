package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/delegatedauth"
	"github.com/agentworkforce/relayfile/internal/mountsync"
)

// TestMirrorStateWriteKeepsMountsyncFields pins relayfile#412: `.relay/state.json`
// has two writers in one process — mountsync's public state and the CLI mirror
// snapshot — targeting the identical path. The mirror writer used to serialize
// its own struct over the whole document, so whichever writer ran last decided
// which half of the schema existed and every guard keyed on the missing half
// failed open. A mirror write must now leave mountsync's fields in place.
func TestMirrorStateWriteKeepsMountsyncFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := filepath.Join(t.TempDir(), "relayfile-mount")
	relay := newProductizedRelayfileMock(t)
	defer relay.Close()
	relay.SetProviderStatus("github", "ready", 1)
	relay.UpsertFile("/github/repos/acme/api/pulls/42/metadata.json", "application/json", `{"title":"Initial PR"}`)

	cloud := newProductizedCloudMock(t, relay)
	defer cloud.Close()
	installFakeAgentRelaySession(t, cloud.URL(), cloud.initialAccessToken, "demo", cloud.workspaceID, cloud.workspaceID)

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
	record, err := resolveWorkspaceRecord("demo")
	if err != nil {
		t.Fatalf("resolveWorkspaceRecord failed: %v", err)
	}

	delegatedCredsFile := writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:          relay.URL(),
		RelayfileWorkspaceID:  cloud.workspaceID,
		AccessToken:           cloud.LastRelayfileToken(),
		RefreshToken:          "refresh_mirror_state_1",
		AccessTokenExpiresAt:  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		RelayauthURL:          cloud.URL(),
	})

	prevLogWriter := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(prevLogWriter)

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const mountInterval = 30 * time.Second
	disableWebSocket := false
	syncer, err := mountsync.NewSyncer(
		mountsync.NewHTTPClient(relay.URL(), cloud.LastRelayfileToken(), relay.HTTPClient()),
		mountsync.SyncerOptions{
			WorkspaceID: record.ID,
			RemoteRoot:  "/",
			LocalRoot:   localDir,
			Interval:    mountInterval,
			WebSocket:   &disableWebSocket,
			RootCtx:     rootCtx,
			Logger:      log.New(io.Discard, "", 0),
		},
	)
	if err != nil {
		t.Fatalf("NewSyncer failed: %v", err)
	}

	if err := writeDaemonPIDState(mountPIDFile(localDir), daemonPIDState{
		PID:         4242,
		WorkspaceID: record.ID,
		LocalDir:    localDir,
		LogFile:     mountLogFile(localDir),
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("writeDaemonPIDState failed: %v", err)
	}

	if err := runMountLoop(
		rootCtx,
		syncer,
		localDir,
		record.ID,
		relay.URL(),
		delegatedCredsFile,
		5*time.Second,
		mountInterval,
		0,
		false,
		true,  // once
		false, // daemonized
		mountPIDFile(localDir),
		mountLogFile(localDir),
	); err != nil {
		t.Fatalf("mount --once failed: %v", err)
	}

	statePath := filepath.Join(localDir, ".relay", "state.json")
	payload, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state.json failed: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("parse state.json failed: %v", err)
	}
	keys := make([]string, 0, len(document))
	for key := range document {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	t.Logf("state.json keys=%v", keys)

	// mountsync-only fields: absent before the fix because the CLI mirror
	// snapshot replaced the whole document.
	for _, key := range []string{"localRoot", "syncMode", "states", "files", "counters", "staleAfter"} {
		if _, ok := document[key]; !ok {
			t.Errorf("mountsync field %q was clobbered by the CLI mirror write", key)
		}
	}
	// CLI-only fields must still be present in the same document.
	for _, key := range []string{"providers", "daemon"} {
		if _, ok := document[key]; !ok {
			t.Errorf("CLI mirror field %q missing from state.json", key)
		}
	}
	// intervalMs 0 made every consumer's staleness check early-return "fresh".
	intervalMs, ok := document["intervalMs"].(float64)
	if !ok || intervalMs != float64(mountInterval.Milliseconds()) {
		t.Errorf("intervalMs = %v, want %d", document["intervalMs"], mountInterval.Milliseconds())
	}
}

// TestMirrorStateOwnedKeysMatchSnapshotStruct pins mirrorStateOwnedKeys to the
// syncStateFile JSON surface. The merge in writeMirrorStateFile clears exactly
// these keys before overlaying the snapshot, so a field added to the struct
// without a matching entry here would keep its previous value forever instead
// of being cleared when the CLI drops it.
func TestMirrorStateOwnedKeysMatchSnapshotStruct(t *testing.T) {
	// omitempty hides the zero value of most fields, so walk the struct tags
	// rather than a marshalled zero value.
	structType := reflect.TypeOf(syncStateFile{})
	want := make(map[string]bool, structType.NumField())
	for i := 0; i < structType.NumField(); i++ {
		tag := structType.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		want[name] = true
	}
	got := make(map[string]bool, len(mirrorStateOwnedKeys))
	for _, key := range mirrorStateOwnedKeys {
		got[key] = true
	}
	for key := range want {
		if !got[key] {
			t.Errorf("syncStateFile field %q is missing from mirrorStateOwnedKeys", key)
		}
	}
	for key := range got {
		if !want[key] {
			t.Errorf("mirrorStateOwnedKeys lists %q, which syncStateFile does not emit", key)
		}
	}
}
