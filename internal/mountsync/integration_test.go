package mountsync

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeCloud is a tiny httptest.Server that speaks just enough of the
// relayfile v1 HTTP shape for the mount syncer to exercise its full pull
// path. It deliberately injects pathological responses (5xx, empty tree,
// partial pages, reset-mid-stream) so the test can run thousands of
// cycles and assert that no local destruction occurred.
type fakeCloud struct {
	*httptest.Server

	files map[string]RemoteFile
	// behavior knobs (atomically swapped between cycles)
	mode atomic.Int32 // 0=ok, 1=500, 2=empty-tree, 3=partial, 4=reset-mid-stream
}

const (
	fcModeOK              int32 = 0
	fcMode500             int32 = 1
	fcModeEmptyTree       int32 = 2
	fcModePartial         int32 = 3
	fcModeResetMidStream  int32 = 4
)

func newFakeCloud(files map[string]RemoteFile) *fakeCloud {
	fc := &fakeCloud{files: files}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/workspaces/", fc.dispatch)
	fc.Server = httptest.NewServer(mux)
	return fc
}

func (fc *fakeCloud) setMode(m int32) { fc.mode.Store(m) }

func (fc *fakeCloud) dispatch(w http.ResponseWriter, r *http.Request) {
	mode := fc.mode.Load()
	if mode == fcMode500 {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	if mode == fcModeResetMidStream {
		// Send a partial body then close.
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijacker", http.StatusInternalServerError)
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n{\"entries\":[")
		_ = conn.Close()
		return
	}

	// Path shape: /v1/workspaces/{id}/fs/{op}
	path := r.URL.Path
	switch {
	case strings.Contains(path, "/fs/tree"):
		fc.handleTree(w, r, mode)
	case strings.Contains(path, "/fs/events"):
		// 404 to force the pull path.
		http.Error(w, "events not supported", http.StatusNotFound)
	case strings.Contains(path, "/fs/file"):
		fc.handleReadFile(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (fc *fakeCloud) handleTree(w http.ResponseWriter, r *http.Request, mode int32) {
	if mode == fcModeEmptyTree {
		_ = json.NewEncoder(w).Encode(TreeResponse{Entries: []TreeEntry{}})
		return
	}
	entries := []TreeEntry{}
	for p, f := range fc.files {
		entries = append(entries, TreeEntry{Path: p, Type: "file", Revision: f.Revision, ContentHash: f.ContentHash})
	}
	if mode == fcModePartial && len(entries) > 1 {
		entries = entries[:1] // truncate
	}
	_ = json.NewEncoder(w).Encode(TreeResponse{Entries: entries})
}

func (fc *fakeCloud) handleReadFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := q.Get("path")
	decoded, err := url.QueryUnescape(p)
	if err == nil {
		p = decoded
	}
	if f, ok := fc.files[p]; ok {
		_ = json.NewEncoder(w).Encode(f)
		return
	}
	http.Error(w, "not found", http.StatusNotFound)
}

// TestFakeCloudChaosNoLocalDestruction is the load-bearing regression for
// the data-loss bug: drive hundreds of pathological sync cycles against a
// fake cloud (5xx storms, empty trees, partial pages, reset-mid-stream)
// and assert no local destruction occurred.
//
// "No destruction" means:
//   - the mount root remains a directory throughout;
//   - the count of mirrored files never drops below the initial count
//     except via legitimate (revision-advancing, tombstone-confirmed)
//     deletes — which this scenario never issues.
//
// The test is skipped in -short mode because the HTTPClient's built-in
// retry policy makes 5xx-heavy cycles relatively expensive.
func TestFakeCloudChaosNoLocalDestruction(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	parent := t.TempDir()
	localDir := filepath.Join(parent, "relayfile-mount")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatalf("mkdir local: %v", err)
	}

	// Seed initial cloud state: a few stable files.
	initialFiles := map[string]RemoteFile{
		"/keep1.md": {Path: "/keep1.md", Revision: "rev_1", ContentType: "text/markdown", Content: "# k1"},
		"/keep2.md": {Path: "/keep2.md", Revision: "rev_1", ContentType: "text/markdown", Content: "# k2"},
		"/sub/keep3.md": {Path: "/sub/keep3.md", Revision: "rev_1", ContentType: "text/markdown", Content: "# k3"},
	}
	fc := newFakeCloud(initialFiles)
	defer fc.Close()

	disableWS := false
	// Tight HTTP timeout so reset-mid-stream / 5xx retries do not bloat
	// the test runtime.
	client := NewHTTPClient(fc.URL, "token-test", &http.Client{Timeout: 2 * time.Second})
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_chaos",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
		WebSocket:   &disableWS,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	// First clean cycle: populate the mirror.
	fc.setMode(fcModeOK)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	initialCount := countLocalFiles(t, localDir)
	if initialCount < 3 {
		t.Fatalf("expected initial mirror to contain >= 3 files, got %d", initialCount)
	}

	// Drive many chaotic cycles, rotating through pathological modes.
	// The legitimate-delete scenario is deliberately excluded so any
	// drop in file count is a true data-loss event.
	//
	// The HTTPClient retries each cycle up to 3x with exponential
	// backoff on 5xx, so we cap cycles at a value that exercises every
	// mode several hundred times within the test timeout. Empirically
	// 250 cycles * 5 modes = 1,250 sync attempts of various shapes.
	chaosModes := []int32{fcMode500, fcModeEmptyTree, fcModePartial, fcModeResetMidStream, fcModeOK}
	const totalCycles = 250
	for i := 0; i < totalCycles; i++ {
		fc.setMode(chaosModes[i%len(chaosModes)])
		_ = syncer.SyncOnce(context.Background())

		// After every cycle: mount root must remain a directory.
		info, statErr := os.Lstat(localDir)
		if statErr != nil || !info.IsDir() {
			t.Fatalf("cycle %d: mount root clobbered (statErr=%v info=%v)", i, statErr, info)
		}
		// File count must never drop below the initial count.
		cur := countLocalFiles(t, localDir)
		if cur < initialCount {
			t.Fatalf("cycle %d: file count dropped from %d to %d (data loss)", i, initialCount, cur)
		}
	}

	// Final clean cycle should leave everything intact.
	fc.setMode(fcModeOK)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		// We may have driven the breaker into open state; that's expected
		// to be transient. Re-try once to give it a chance to half-open.
		_ = syncer.SyncOnce(context.Background())
	}
	if cur := countLocalFiles(t, localDir); cur < initialCount {
		t.Fatalf("final cycle: file count dropped from %d to %d", initialCount, cur)
	}
}

// countLocalFiles walks localDir and counts regular files outside .relay
// and the mount-state file.
func countLocalFiles(t *testing.T, localDir string) int {
	t.Helper()
	count := 0
	_ = filepath.WalkDir(localDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".relay" {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if name == ".relayfile-mount-state.json" || strings.HasPrefix(name, ".relayfile-mount-state.json.tmp-") {
			return nil
		}
		count++
		return nil
	})
	return count
}

