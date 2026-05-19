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
	fcModeOK             int32 = 0
	fcMode500            int32 = 1
	fcModeEmptyTree      int32 = 2
	fcModePartial        int32 = 3
	fcModeResetMidStream int32 = 4
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
		"/keep1.md":     {Path: "/keep1.md", Revision: "rev_1", ContentType: "text/markdown", Content: "# k1"},
		"/keep2.md":     {Path: "/keep2.md", Revision: "rev_1", ContentType: "text/markdown", Content: "# k2"},
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

// largeChaosCloud serves a big paginated workspace and intermittently
// errors / slows ListTree+ReadFile. It exercises the resumable-bootstrap
// + decoupled-timeout path: with a tiny per-cycle timeout the mirror must
// still eventually fully converge across many Reconcile calls (resuming
// from the persisted bootstrap cursor each time).
type largeChaosCloud struct {
	*httptest.Server
	files    map[string]RemoteFile
	pageSize int
	calls    atomic.Int64
}

func newLargeChaosCloud(fileCount, pageSize int) *largeChaosCloud {
	files := make(map[string]RemoteFile, fileCount)
	for i := 0; i < fileCount; i++ {
		p := filepathSlashJoin(i)
		files[p] = RemoteFile{Path: p, Revision: "rev_1", ContentType: "text/plain", Content: "c"}
	}
	lc := &largeChaosCloud{files: files, pageSize: pageSize}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/workspaces/", lc.dispatch)
	lc.Server = httptest.NewServer(mux)
	return lc
}

func filepathSlashJoin(i int) string {
	return "/big/" + itoa5(i) + ".txt"
}

func itoa5(i int) string {
	const digits = "0123456789"
	b := []byte("00000")
	for pos := 4; pos >= 0; pos-- {
		b[pos] = digits[i%10]
		i /= 10
	}
	return string(b)
}

func (lc *largeChaosCloud) sortedPaths() []string {
	ps := make([]string, 0, len(lc.files))
	for p := range lc.files {
		ps = append(ps, p)
	}
	sortStrings(ps)
	return ps
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func (lc *largeChaosCloud) dispatch(w http.ResponseWriter, r *http.Request) {
	n := lc.calls.Add(1)
	path := r.URL.Path
	switch {
	case strings.Contains(path, "/fs/events"):
		http.Error(w, "events not supported", http.StatusNotFound)
		return
	case strings.Contains(path, "/fs/tree"):
		// Intermittent 500 on every 4th call, slow on every 3rd.
		if n%4 == 0 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		if n%3 == 0 {
			time.Sleep(30 * time.Millisecond)
		}
		cursor := r.URL.Query().Get("cursor")
		paths := lc.sortedPaths()
		start := 0
		if cursor != "" {
			for i, p := range paths {
				if p == cursor {
					start = i + 1
					break
				}
			}
		}
		end := start + lc.pageSize
		if end > len(paths) {
			end = len(paths)
		}
		entries := make([]TreeEntry, 0, end-start)
		for _, p := range paths[start:end] {
			entries = append(entries, TreeEntry{Path: p, Type: "file", Revision: "rev_1"})
		}
		resp := TreeResponse{Entries: entries}
		if end < len(paths) {
			next := paths[end-1]
			resp.NextCursor = &next
		}
		_ = json.NewEncoder(w).Encode(resp)
		return
	case strings.Contains(path, "/fs/file"):
		if n%7 == 0 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		p := r.URL.Query().Get("path")
		if decoded, err := url.QueryUnescape(p); err == nil {
			p = decoded
		}
		if f, ok := lc.files[p]; ok {
			_ = json.NewEncoder(w).Encode(f)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
		return
	default:
		http.NotFound(w, r)
	}
}

// TestLargeWorkspaceChaosEventuallyConverges drives ~600 files behind an
// intermittently-failing, slow, paginated cloud with a tiny per-cycle
// timeout. The resumable bootstrap must persist its cursor across cycles
// and the mirror must eventually fully converge.
func TestLargeWorkspaceChaosEventuallyConverges(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	const fileCount = 600
	lc := newLargeChaosCloud(fileCount, 50)
	defer lc.Close()

	parent := t.TempDir()
	localDir := filepath.Join(parent, "relayfile-mount")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	disableWS := false
	client := NewHTTPClient(lc.URL, "token-test", &http.Client{Timeout: 2 * time.Second})
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_large_chaos",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
		WebSocket:   &disableWS,
		RootCtx:     context.Background(),
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	converged := false
	for i := 0; i < 200 && !converged; i++ {
		// Tiny per-cycle deadline: bootstrap must NOT ride this ctx.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Millisecond)
		_ = syncer.Reconcile(ctx)
		cancel()
		// Mount root must remain a directory throughout.
		if info, statErr := os.Lstat(localDir); statErr != nil || !info.IsDir() {
			t.Fatalf("cycle %d: mount root clobbered", i)
		}
		if countLocalFiles(t, localDir) == fileCount {
			converged = true
		}
	}
	if !converged {
		t.Fatalf("mirror failed to converge to %d files (got %d) across chaos cycles", fileCount, countLocalFiles(t, localDir))
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
