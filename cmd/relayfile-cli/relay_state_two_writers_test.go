package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountsync"
)

// newPublicStateWriter builds a real mountsync.Syncer rooted at localDir.
// Its LocalRoot is the same directory the CLI mirror writer is handed in
// cmd/relayfile-cli/main.go:6852-6892, so both writers derive the identical
// <localDir>/.relay/state.json path — that shared derivation is the defect.
//
// No network call is made: FlushOutboxOnce with an empty outbox goes straight
// to markSyncSuccess -> saveStateWithoutLocalScan -> savePublicState.
func newPublicStateWriter(t *testing.T, localDir string) *mountsync.Syncer {
	t.Helper()
	client := mountsync.NewHTTPClient("http://127.0.0.1:1", "test-token", &http.Client{
		Timeout: time.Second,
	})
	websocketDisabled := false
	syncer, err := mountsync.NewSyncer(client, mountsync.SyncerOptions{
		WorkspaceID: "rw_twowriters",
		RemoteRoot:  "/linear",
		LocalRoot:   localDir,
		WebSocket:   &websocketDisabled,
		RootCtx:     context.Background(),
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	return syncer
}

// writeMountsyncPublicState drives WRITER 1 (internal/mountsync/syncer.go:7200,
// path derived at syncer.go:1608).
func writeMountsyncPublicState(t *testing.T, syncer *mountsync.Syncer) {
	t.Helper()
	if err := syncer.FlushOutboxOnce(context.Background()); err != nil {
		t.Fatalf("FlushOutboxOnce (writer 1): %v", err)
	}
}

// writeCLIMirrorState drives WRITER 2 (cmd/relayfile-cli/main.go:10998), with a
// snapshot shaped like the one buildSyncStateSnapshot produces for a live
// mount: providers from the cloud feed, a daemon block, guards, a stall reason.
func writeCLIMirrorState(t *testing.T, localDir string) {
	t.Helper()
	snapshot := syncStateFile{
		WorkspaceID: "rw_twowriters",
		RemoteRoot:  "/linear",
		Mode:        defaultMountMode,
		Status:      "ready",
		// Provider-feed clock. Deliberately old: this is the field an
		// operator misread as "the feed moved".
		LastEventAt: "2026-08-03T07:26:26.334Z",
		IntervalMs:  30000,
		Providers: []syncStateProvider{
			{Provider: "linear", Status: "ready", LastEventAt: "2026-08-03T07:26:26.334Z"},
		},
		StallReason: "provider feed frozen",
		Daemon:      &syncStateDaemon{PID: 63173},
		Guards: &syncStateGuards{
			CircuitOpenEvents:   7,
			TombstonesConfirmed: 3,
		},
	}
	if err := writeMirrorStateFile(localDir, snapshot); err != nil {
		t.Fatalf("writeMirrorStateFile (writer 2): %v", err)
	}
}

func readStateDocument(t *testing.T, localDir string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(localDir, ".relay", "state.json"))
	if err != nil {
		t.Fatalf("read .relay/state.json: %v", err)
	}
	document := map[string]any{}
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("unmarshal .relay/state.json: %v", err)
	}
	return document
}

// TestRelayStateJSONHasExactlyOneWriter is the contract this file exists to
// pin: <localDir>/.relay/state.json must be a single document with a single
// owner. Today two writers in the same binary emit two disjoint schemas to
// that one path, so whichever wrote last defines what every consumer sees.
//
// The test drives writer 1 then writer 2 then writer 1 again against one
// localDir and asserts, after each write, that the document still satisfies
// BOTH consumer contracts. It fails on HEAD at the first assertion.
func TestRelayStateJSONHasExactlyOneWriter(t *testing.T) {
	localDir := t.TempDir()
	syncer := newPublicStateWriter(t, localDir)

	// --- writer 1 (mountsync) writes first ---------------------------------
	writeMountsyncPublicState(t, syncer)
	afterWriter1 := readStateDocument(t, localDir)
	if _, ok := afterWriter1["localRoot"]; !ok {
		t.Fatalf("precondition failed: writer 1 did not emit its own document; keys=%v", sortedKeys(afterWriter1))
	}

	// --- writer 2 (CLI mirror) writes second --------------------------------
	writeCLIMirrorState(t, localDir)
	afterWriter2 := readStateDocument(t, localDir)

	// Writer 1's fields must survive writer 2. They do not: writeMirrorStateFile
	// marshals a syncStateFile from scratch, so every publicState-only field is
	// dropped from the file on disk.
	for _, field := range []string{"localRoot", "states", "counters", "files", "outbox", "credExpiresInSecs"} {
		if _, ok := afterWriter2[field]; !ok {
			t.Errorf("writer 2 clobbered writer 1: field %q is absent from .relay/state.json after the CLI mirror write (keys now: %v)",
				field, sortedKeys(afterWriter2))
		}
	}

	// --- writer 1 writes again (this is what happens ~1x/second live) -------
	writeMountsyncPublicState(t, syncer)
	afterWriter1Again := readStateDocument(t, localDir)

	// Writer 2's fields must survive writer 1. They do not.
	for _, field := range []string{"providers", "daemon", "guards", "stallReason"} {
		if _, ok := afterWriter1Again[field]; !ok {
			t.Errorf("writer 1 clobbered writer 2: field %q is absent from .relay/state.json after the mountsync write (keys now: %v)",
				field, sortedKeys(afterWriter1Again))
		}
	}

	// The production readers in this package must keep working across both
	// writes. They do not: each reads a field only one writer emits.
	// readGuardCounters is the subtle one. It does not return nil here — it
	// falls through to writer 1's `counters`/`circuit` block and returns a
	// *different* guards document than the one writer 2 persisted. The CLI
	// surface silently swaps its data source depending on who wrote last.
	guards := readGuardCounters(localDir)
	if guards == nil {
		t.Errorf("readGuardCounters (main.go:10912) returned nil after a mountsync write")
	} else {
		if guards.CircuitOpenEvents != 7 {
			t.Errorf("readGuardCounters (main.go:10912) circuitOpenEvents = %d, want 7 — it silently switched from writer 2's persisted `guards` block to writer 1's `counters` block",
				guards.CircuitOpenEvents)
		}
		if guards.TombstonesConfirmed != 3 {
			t.Errorf("readGuardCounters (main.go:10912) tombstonesConfirmed = %d, want 3 — writer 2's guard telemetry is gone from the file",
				guards.TombstonesConfirmed)
		}
	}
	if reason := readPersistedStallReason(localDir); reason != "provider feed frozen" {
		t.Errorf("readPersistedStallReason (main.go:8556) = %q, want %q — the stall reason is erased every time writer 1 wins the race",
			reason, "provider feed frozen")
	}
	state, err := readWritebackState(localDir)
	if err != nil {
		t.Fatalf("readWritebackState: %v", err)
	}
	if len(state.Providers) == 0 {
		t.Errorf("readWritebackState (main.go:5487) sees 0 providers — the TypeScript SDK readiness check (packages/sdk/typescript/src/mount-launcher.ts:355) treats an empty providers list as ready, so this races into a false ready")
	}
}

// TestRelayStateJSONLastEventAtHasOneMeaning pins the field that produced the
// real-world misreport. Both writers emit `lastEventAt`, with different
// semantics: writer 1 means "mountsync's own event clock" and writer 2 means
// "the cloud provider feed". A consumer polling this one field gets a value
// whose meaning changes between reads.
func TestRelayStateJSONLastEventAtHasOneMeaning(t *testing.T) {
	localDir := t.TempDir()
	syncer := newPublicStateWriter(t, localDir)

	writeCLIMirrorState(t, localDir)
	fromProviderFeed, _ := readStateDocument(t, localDir)["lastEventAt"].(string)
	if fromProviderFeed != "2026-08-03T07:26:26.334Z" {
		t.Fatalf("precondition failed: writer 2 lastEventAt = %q", fromProviderFeed)
	}

	writeMountsyncPublicState(t, syncer)
	afterMountsync := readStateDocument(t, localDir)
	fromMountsync, present := afterMountsync["lastEventAt"].(string)

	if !present {
		t.Errorf("lastEventAt vanished from .relay/state.json after a mountsync write: a consumer polling this field sees the provider-feed timestamp %q disappear and reappear as the mount loop and the CLI mirror alternate",
			fromProviderFeed)
	}
	if present && fromMountsync != fromProviderFeed {
		t.Errorf("lastEventAt changed meaning without changing name: %q (cloud provider feed, writer 2) then %q (mountsync event clock, writer 1)",
			fromProviderFeed, fromMountsync)
	}
}

func sortedKeys(document map[string]any) []string {
	keys := make([]string, 0, len(document))
	for key := range document {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
