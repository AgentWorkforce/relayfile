package mountsync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/agentworkforce/relayfile/internal/mountstate"
)

// TestPublicStateOwnedKeysMatchStruct pins publicStateOwnedKeys to the
// publicState JSON surface. savePublicState clears exactly these keys before
// overlaying, so a field added to the struct without an entry here would stop
// being cleared and could keep a stale value across cycles.
func TestPublicStateOwnedKeysMatchStruct(t *testing.T) {
	structType := reflect.TypeOf(publicState{})
	want := make(map[string]bool, structType.NumField())
	for i := 0; i < structType.NumField(); i++ {
		tag := structType.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		want[name] = true
	}
	got := make(map[string]bool, len(publicStateOwnedKeys))
	for _, key := range publicStateOwnedKeys {
		got[key] = true
	}
	for key := range want {
		if !got[key] {
			t.Errorf("publicState field %q is missing from publicStateOwnedKeys", key)
		}
	}
	for key := range got {
		if !want[key] {
			t.Errorf("publicStateOwnedKeys lists %q, which publicState does not emit", key)
		}
	}
}

// TestSavePublicStateKeepsCLIMirrorFields covers the path Codex flagged on
// PR #457: in a healthy websocket/watcher mount the CLI timer refreshes
// realtime state -- which publishes public state -- with no following mirror
// snapshot. Before the shared merge, every such refresh deleted the CLI-only
// keys, so `providers`, `daemon` and `guards` vanished from the published
// document until the next snapshot happened to run.
func TestSavePublicStateKeepsCLIMirrorFields(t *testing.T) {
	localRoot := t.TempDir()
	statePath := filepath.Join(localRoot, ".relay", "state.json")

	// Stand in for the CLI mirror writer: keys only it emits.
	if err := mountstate.Merge(statePath, []string{"providers", "daemon", "guards", "stallReason"}, map[string]any{
		"providers":   []map[string]any{{"provider": "github", "status": "ready"}},
		"daemon":      map[string]any{"pid": 4242},
		"guards":      map[string]any{"deniedRootTarget": 3},
		"stallReason": "",
	}); err != nil {
		t.Fatalf("seed CLI mirror state: %v", err)
	}

	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID: "ws_merge",
		RemoteRoot:  "/",
		LocalRoot:   localRoot,
		StateDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	if got := syncer.publicStatePath; got != statePath {
		t.Fatalf("public state path = %q, want %q", got, statePath)
	}
	if err := syncer.savePublicStateWithLocalScan(false); err != nil {
		t.Fatalf("savePublicState: %v", err)
	}

	payload, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("parse state.json: %v", err)
	}
	for _, key := range []string{"providers", "daemon", "guards"} {
		if _, ok := document[key]; !ok {
			t.Errorf("CLI mirror field %q was clobbered by savePublicState", key)
		}
	}
	// And this writer's own fields are published in the same document.
	for _, key := range []string{"workspaceId", "localRoot", "status"} {
		if _, ok := document[key]; !ok {
			t.Errorf("publicState field %q missing after merge", key)
		}
	}
}
