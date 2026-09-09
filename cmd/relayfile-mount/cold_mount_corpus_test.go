package main

// Relayfile cold-mount corpus qualification (local encoding of the
// Relayfile Cloud schema-v3 acceptance corpus).
//
// This test is opt-in because it materializes the exact 258 MiB fixture three
// times (one cold mount plus two concurrent consumers) through a real
// relayfile-mount binary. It is skipped unless:
//
//	RELAYFILE_COLD_MOUNT_QUALIFY=1 go test ./cmd/relayfile-mount -run TestColdMountScaleCorpusContract
//
// The corpus is the byte-exact Go port of relayfile-cloud
// local/cold-mount-fixture.ts: 851 files, 454 directories, 270,532,608 bytes,
// manifest SHA-256 905968a14268ec5e8ec38ae1d6b24749e855cac035976a87a65ef43f6
// 612a55a. One cold mount plus two genuinely concurrent consumers must
// complete using at least one bulk read, zero point reads, at most 10 minutes
// wall time, 120,000 ms of mount CPU time, and 3 GiB of mount peak RSS. The
// two concurrent consumers must have overlapping process lifetimes. These
// are the same per-mount telemetry bounds the Cloud candidate acceptance
// verifies, but this local test does not replace that production gate.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	coldCorpusFiles       = 851
	coldCorpusDirectories = 454
	coldCorpusTotalBytes  = 270_532_608
	coldCorpusRoot        = "/cold-mount-scale"

	coldCorpusManifestSHA256 = "905968a14268ec5e8ec38ae1d6b24749e855cac035976a87a65ef43f6612a55a"

	coldCorpusMaxCPUMs        = 120_000
	coldCorpusMaxWallMs       = 10 * 60 * 1000
	coldCorpusMaxPeakRSSBytes = 3 << 30

	coldCorpusTreePageSize = 652

	coldCorpusMountTimeout = 15 * time.Minute

	// coldCorpusQualifyEnv explicitly opts in to this heavyweight gate.
	coldCorpusQualifyEnv = "RELAYFILE_COLD_MOUNT_QUALIFY"
	coldCorpusToken      = "token-cold-corpus"
)

type coldCorpusFile struct {
	index        int
	remotePath   string
	relativePath string
	size         int64
}

type coldCorpusIndex struct {
	files  []coldCorpusFile
	byPath map[string]coldCorpusFile
	// treeEntries is the sorted depth-3 tree listing with precomputed content
	// hashes, built once (one 258 MiB hashing pass) so tree requests do not
	// re-hash the fixture per request.
	treeEntries []treeEntryJSON
}

var (
	coldCorpusIndexOnce sync.Once
	coldCorpusIndexData *coldCorpusIndex
)

// coldCorpusFixture returns the deterministic fixture layout: file i lives in
// d{i%453} as file-{i}.bin, sized floor(total/files) plus one byte for the
// first (total%files) indexes.
func coldCorpusFixture() *coldCorpusIndex {
	coldCorpusIndexOnce.Do(func() {
		childDirectories := coldCorpusDirectories - 1
		baseBytes := coldCorpusTotalBytes / coldCorpusFiles
		largeFiles := coldCorpusTotalBytes % coldCorpusFiles
		files := make([]coldCorpusFile, 0, coldCorpusFiles)
		byPath := make(map[string]coldCorpusFile, coldCorpusFiles)
		treeEntries := make([]treeEntryJSON, 0, coldCorpusFiles+coldCorpusDirectories-1)
		seenDirs := make(map[string]struct{})
		for index := 0; index < coldCorpusFiles; index++ {
			directory := fmt.Sprintf("d%03d", index%childDirectories)
			filename := fmt.Sprintf("file-%04d.bin", index)
			size := int64(baseBytes)
			if index < largeFiles {
				size++
			}
			file := coldCorpusFile{
				index:        index,
				remotePath:   coldCorpusRoot + "/" + directory + "/" + filename,
				relativePath: directory + "/" + filename,
				size:         size,
			}
			files = append(files, file)
			byPath[file.remotePath] = file
			if _, ok := seenDirs[file.remotePath[:strings.LastIndex(file.remotePath, "/")]]; !ok {
				dirPath := file.remotePath[:strings.LastIndex(file.remotePath, "/")]
				seenDirs[dirPath] = struct{}{}
				treeEntries = append(treeEntries, treeEntryJSON{Path: dirPath, Type: "dir", Revision: "rev_dir"})
			}
			treeEntries = append(treeEntries, treeEntryJSON{
				Path: file.remotePath, Type: "file", Revision: "rev_1",
				ContentHash: coldCorpusSHA256Hex(coldCorpusBody(file)), Size: file.size,
			})
		}
		sort.Slice(treeEntries, func(i, j int) bool { return treeEntries[i].Path < treeEntries[j].Path })
		coldCorpusIndexData = &coldCorpusIndex{files: files, byPath: byPath, treeEntries: treeEntries}
	})
	return coldCorpusIndexData
}

// coldCorpusBody regenerates one deterministic file body without retaining the
// fixture: fill byte 65+(index%26) prefixed by the fixture identity marker.
func coldCorpusBody(file coldCorpusFile) []byte {
	fillByte := byte(65 + file.index%26)
	body := bytes.Repeat([]byte{fillByte}, int(file.size))
	copy(body, fmt.Sprintf("relayfile-cold-mount-v1:%d:", file.index))
	return body
}

// coldCorpusContentBase64 encodes one file body on demand. The encoded corpus
// (~344 MiB) is deliberately not cached; only the tiny hash/path index is.
func coldCorpusContentBase64(file coldCorpusFile) string {
	return base64.StdEncoding.EncodeToString(coldCorpusBody(file))
}

func coldCorpusSHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type coldCorpusManifestEntry struct {
	relativePath string
	size         int64
	sha256       string
}

func coldCorpusManifest(files map[string]coldCorpusManifestEntry) string {
	lines := make([]string, 0, len(files))
	for _, entry := range files {
		lines = append(lines, entry.relativePath+"\x00"+fmt.Sprintf("%d", entry.size)+"\x00"+strings.ToLower(entry.sha256)+"\n")
	}
	sort.Strings(lines)
	return strings.Join(lines, "")
}

type treeEntryJSON struct {
	Path        string `json:"path"`
	Type        string `json:"type"`
	Revision    string `json:"revision"`
	ContentHash string `json:"contentHash,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

type treeResponseJSON struct {
	Path       string          `json:"path"`
	Entries    []treeEntryJSON `json:"entries"`
	NextCursor *string         `json:"nextCursor"`
	TotalFiles int             `json:"totalFiles,omitempty"`
}

// coldCorpusCloud serves the exact fixture over real HTTP with the wire shapes
// the production cloud uses: a paginated depth-3 tree, base64 bulk reads, and
// a point-read route that exists only so a regression can be counted.
type coldCorpusCloud struct {
	*httptest.Server

	bulkReads  atomic.Int64
	pointReads atomic.Int64
}

func newColdCorpusCloud() *coldCorpusCloud {
	cloud := &coldCorpusCloud{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/workspaces/", cloud.dispatch)
	cloud.Server = httptest.NewServer(mux)
	return cloud
}

func (c *coldCorpusCloud) dispatch(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/workspaces/ws_cold_corpus/fs/bulk-read" && r.Method == http.MethodPost {
		if !c.authorize(w, r) {
			return
		}
		c.handleBulkRead(w, r)
		return
	}
	if r.URL.Path == "/v1/workspaces/ws_cold_corpus/fs/tree" && r.Method == http.MethodGet {
		if !c.authorize(w, r) {
			return
		}
		c.handleTree(w, r)
		return
	}
	if r.URL.Path == "/v1/workspaces/ws_cold_corpus/fs/events" && r.Method == http.MethodGet {
		if !c.authorize(w, r) {
			return
		}
		http.Error(w, "events not supported", http.StatusNotFound)
		return
	}
	if r.URL.Path == "/v1/workspaces/ws_cold_corpus/fs/file" && r.Method == http.MethodGet {
		if !c.authorize(w, r) {
			return
		}
		c.handleReadFile(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/workspaces/") {
		http.Error(w, "unsupported relayfile request", http.StatusMethodNotAllowed)
		return
	}
	http.NotFound(w, r)
}

func (c *coldCorpusCloud) authorize(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer "+coldCorpusToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func requireColdCorpusQuery(w http.ResponseWriter, r *http.Request, required map[string]string) bool {
	for key, expected := range required {
		if r.URL.Query().Get(key) != expected {
			http.Error(w, "invalid relayfile query", http.StatusBadRequest)
			return false
		}
	}
	return true
}

func validColdCorpusCursor(cursor string, entries []treeEntryJSON) bool {
	if cursor == "" {
		return true
	}
	for _, entry := range entries {
		if entry.Path == cursor {
			return true
		}
	}
	return false
}

func parseColdCorpusPath(raw string) (string, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Path != raw || !strings.HasPrefix(raw, coldCorpusRoot+"/") {
		return "", false
	}
	return raw, true
}

func (c *coldCorpusCloud) handleTree(w http.ResponseWriter, r *http.Request) {
	if !requireColdCorpusQuery(w, r, map[string]string{
		"path": coldCorpusRoot, "depth": "3", "excludeMountRuntime": "true",
	}) {
		return
	}
	entries := coldCorpusFixture().treeEntries
	cursor := r.URL.Query().Get("cursor")
	if !validColdCorpusCursor(cursor, entries) {
		http.Error(w, "invalid relayfile cursor", http.StatusBadRequest)
		return
	}
	start := 0
	if cursor != "" {
		for i, entry := range entries {
			if entry.Path == cursor {
				start = i + 1
				break
			}
		}
	}
	end := start + coldCorpusTreePageSize
	if end > len(entries) {
		end = len(entries)
	}
	response := treeResponseJSON{Path: coldCorpusRoot, Entries: entries[start:end], TotalFiles: coldCorpusFiles}
	if end < len(entries) {
		next := entries[end-1].Path
		response.NextCursor = &next
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (c *coldCorpusCloud) handleBulkRead(w http.ResponseWriter, r *http.Request) {
	c.bulkReads.Add(1)
	var request struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(request.Paths) == 0 || len(request.Paths) > 32 {
		http.Error(w, "invalid bulk-read paths", http.StatusBadRequest)
		return
	}
	seen := make(map[string]struct{}, len(request.Paths))
	for _, path := range request.Paths {
		path, valid := parseColdCorpusPath(path)
		if !valid {
			http.Error(w, "invalid bulk-read path", http.StatusBadRequest)
			return
		}
		if _, duplicate := seen[path]; duplicate {
			http.Error(w, "duplicate bulk-read path", http.StatusBadRequest)
			return
		}
		seen[path] = struct{}{}
	}
	byPath := coldCorpusFixture().byPath
	results := make([]map[string]any, 0, len(request.Paths))
	for _, path := range request.Paths {
		file, ok := byPath[path]
		if !ok {
			results = append(results, map[string]any{
				"path":  path,
				"error": map[string]any{"status": 404, "code": "not_found", "message": "not found"},
			})
			continue
		}
		results = append(results, map[string]any{
			"path":        file.remotePath,
			"revision":    "rev_1",
			"contentType": "application/octet-stream",
			"content":     coldCorpusContentBase64(file),
			"encoding":    "base64",
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"files": results})
}

func (c *coldCorpusCloud) handleReadFile(w http.ResponseWriter, r *http.Request) {
	c.pointReads.Add(1)
	path, valid := parseColdCorpusPath(r.URL.Query().Get("path"))
	if !valid {
		http.Error(w, "invalid file path", http.StatusBadRequest)
		return
	}
	file, ok := coldCorpusFixture().byPath[path]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"path":        file.remotePath,
		"revision":    "rev_1",
		"contentType": "application/octet-stream",
		"content":     coldCorpusContentBase64(file),
		"encoding":    "base64",
	})
}

// coldCorpusMountResult captures the measured outcome of one mount process.
type coldCorpusMountResult struct {
	label        string
	startedAt    time.Time
	finishedAt   time.Time
	exitCode     int
	wallMs       int64
	cpuMs        int64
	peakRSSBytes int64
	output       string
}

var (
	coldCorpusBinaryOnce sync.Once
	coldCorpusBinaryPath string
	coldCorpusBinaryErr  error
)

func coldCorpusMountBinary(t *testing.T) string {
	t.Helper()
	coldCorpusBinaryOnce.Do(func() {
		binaryPath := filepath.Join(t.TempDir(), "relayfile-mount")
		// The test binary's working directory is this package directory.
		output, err := exec.Command("go", "build", "-trimpath", "-o", binaryPath, ".").CombinedOutput()
		if err != nil {
			coldCorpusBinaryErr = fmt.Errorf("go build failed: %v: %s", err, output)
			return
		}
		coldCorpusBinaryPath = binaryPath
	})
	if coldCorpusBinaryErr != nil {
		t.Fatalf("mount binary: %v", coldCorpusBinaryErr)
	}
	return coldCorpusBinaryPath
}

// runColdCorpusMount spawns one relayfile-mount process with the exact
// qualification flags. CPU time and peak RSS come from the child's rusage on
// unix (see cold_mount_corpus_unix_test.go), matching the cgroup-v2 telemetry
// the Cloud acceptance verifies; the windows helper reports them unavailable.
func runColdCorpusMount(t *testing.T, label, baseURL, localDir, stateFile string) coldCorpusMountResult {
	t.Helper()
	binary := coldCorpusMountBinary(t)
	args := []string{
		"-base-url", baseURL,
		"-workspace", "ws_cold_corpus",
		"-remote-path", coldCorpusRoot,
		"-local-dir", localDir,
		"-state-file", stateFile,
		"-state-dir", filepath.Dir(stateFile),
		"-mode", "poll",
		"-sync-mode", "pull-only",
		"-websocket=false",
		"-once",
		"-timeout", "10m",
		"-bootstrap-timeout", "10m",
		"-bootstrap-max-files-per-cycle", "-1",
		"-log-http-status=true",
	}
	cmd := exec.Command(binary, args...)
	cmd.Env = append(os.Environ(), "RELAYFILE_TOKEN="+coldCorpusToken)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("%s stdout pipe: %v", label, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("%s stderr pipe: %v", label, err)
	}
	result := coldCorpusMountResult{label: label, startedAt: time.Now()}
	if err := cmd.Start(); err != nil {
		t.Fatalf("%s start: %v", label, err)
	}
	output := make(chan string, 2)
	go func() { data, _ := io.ReadAll(stdout); output <- string(data) }()
	go func() { data, _ := io.ReadAll(stderr); output <- string(data) }()

	deadline := time.After(coldCorpusMountTimeout)
	done := make(chan struct{})
	go func() {
		defer close(done)
		result.exitCode, result.cpuMs, result.peakRSSBytes = awaitColdCorpusChild(cmd)
	}()
	select {
	case <-done:
	case <-deadline:
		_ = cmd.Process.Kill()
		<-done
		result.exitCode = -1
	}
	result.finishedAt = time.Now()
	result.wallMs = result.finishedAt.Sub(result.startedAt).Milliseconds()
	result.output = <-output + <-output
	return result
}

func verifyColdCorpusMount(t *testing.T, label, localDir string) {
	t.Helper()
	fixture := coldCorpusFixture()
	manifestEntries := make(map[string]coldCorpusManifestEntry, len(fixture.files))
	var totalBytes int64
	directories := map[string]struct{}{".": {}}
	for _, file := range fixture.files {
		localPath := filepath.Join(localDir, filepath.FromSlash(file.relativePath))
		info, err := os.Stat(localPath)
		if err != nil {
			t.Fatalf("%s is missing %s: %v", label, file.relativePath, err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("%s path is not a regular file: %s", label, file.relativePath)
		}
		if info.Size() != file.size {
			t.Fatalf("%s size mismatch for %s: got %d want %d", label, file.relativePath, info.Size(), file.size)
		}
		digest, err := hashFileSHA256(localPath)
		if err != nil {
			t.Fatalf("%s hash %s: %v", label, file.relativePath, err)
		}
		totalBytes += info.Size()
		directories[filepath.ToSlash(filepath.Dir(file.relativePath))] = struct{}{}
		manifestEntries[file.relativePath] = coldCorpusManifestEntry{
			relativePath: file.relativePath, size: file.size, sha256: digest,
		}
	}
	if len(manifestEntries) != coldCorpusFiles {
		t.Fatalf("%s file count %d, want %d", label, len(manifestEntries), coldCorpusFiles)
	}
	if len(directories) != coldCorpusDirectories {
		t.Fatalf("%s directory count %d, want %d", label, len(directories), coldCorpusDirectories)
	}
	if totalBytes != coldCorpusTotalBytes {
		t.Fatalf("%s byte count %d, want %d", label, totalBytes, coldCorpusTotalBytes)
	}
	manifestSHA := coldCorpusSHA256Hex([]byte(coldCorpusManifest(manifestEntries)))
	if manifestSHA != coldCorpusManifestSHA256 {
		t.Fatalf("%s manifest SHA-256 %s, want %s", label, manifestSHA, coldCorpusManifestSHA256)
	}
	statePath := filepath.Join(localDir, ".relay", "state.json")
	stateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("%s public mount state is missing: %v", label, err)
	}
	var publicState struct {
		Bootstrap map[string]any `json:"bootstrap"`
		Status    string         `json:"status"`
	}
	if err := json.Unmarshal(stateBytes, &publicState); err != nil {
		t.Fatalf("%s public mount state is malformed: %v", label, err)
	}
	if publicState.Bootstrap != nil {
		t.Fatalf("%s bootstrap state did not clear", label)
	}
	if publicState.Status == "error" {
		t.Fatalf("%s public mount state reports error", label)
	}
}

func hashFileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func TestColdCorpusCloudRejectsMalformedRequestsWithoutMaterializingFixture(t *testing.T) {
	cloud := newColdCorpusCloud()
	defer cloud.Close()

	tests := []struct {
		name       string
		method     string
		path       string
		authority  string
		body       string
		wantStatus int
	}{
		{
			name:       "missing authorization",
			method:     http.MethodGet,
			path:       "/v1/workspaces/ws_cold_corpus/fs/tree?path=" + url.QueryEscape(coldCorpusRoot) + "&depth=3&excludeMountRuntime=true",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong method",
			method:     http.MethodPost,
			path:       "/v1/workspaces/ws_cold_corpus/fs/tree",
			authority:  coldCorpusToken,
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "missing tree query",
			method:     http.MethodGet,
			path:       "/v1/workspaces/ws_cold_corpus/fs/tree",
			authority:  coldCorpusToken,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty bulk request",
			method:     http.MethodPost,
			path:       "/v1/workspaces/ws_cold_corpus/fs/bulk-read",
			authority:  coldCorpusToken,
			body:       `{"paths":[]}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid bulk path",
			method:     http.MethodPost,
			path:       "/v1/workspaces/ws_cold_corpus/fs/bulk-read",
			authority:  coldCorpusToken,
			body:       `{"paths":["/outside"]}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid file path",
			method:     http.MethodGet,
			path:       "/v1/workspaces/ws_cold_corpus/fs/file?path=%2Foutside",
			authority:  coldCorpusToken,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(test.method, cloud.URL+test.path, strings.NewReader(test.body))
			if err != nil {
				t.Fatal(err)
			}
			if test.authority != "" {
				request.Header.Set("Authorization", "Bearer "+test.authority)
			}
			response, err := cloud.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.wantStatus)
			}
		})
	}
}

func verifyColdCorpusResources(t *testing.T, result coldCorpusMountResult) {
	t.Helper()
	if result.exitCode != 0 {
		t.Fatalf("%s exited code=%d:\n%s", result.label, result.exitCode, tailString(result.output, 4000))
	}
	if result.cpuMs < 0 || result.cpuMs > coldCorpusMaxCPUMs {
		t.Fatalf("%s CPU %dms is unavailable or exceeds %dms", result.label, result.cpuMs, coldCorpusMaxCPUMs)
	}
	if result.wallMs < 0 || result.wallMs > coldCorpusMaxWallMs {
		t.Fatalf("%s wall %dms is unavailable or exceeds %dms", result.label, result.wallMs, coldCorpusMaxWallMs)
	}
	if result.peakRSSBytes < 0 || result.peakRSSBytes > coldCorpusMaxPeakRSSBytes {
		t.Fatalf("%s peak RSS %d is unavailable or exceeds %d", result.label, result.peakRSSBytes, coldCorpusMaxPeakRSSBytes)
	}
	if strings.Contains(strings.ToLower(result.output), "relayfile http 429") ||
		strings.Contains(result.output, "connection reset") {
		t.Fatalf("%s logged an overload/reset failure:\n%s", result.label, tailString(result.output, 4000))
	}
	t.Logf("%s wall=%dms cpu=%dms peakRSS=%d", result.label, result.wallMs, result.cpuMs, result.peakRSSBytes)
}

func coldCorpusMountsOverlap(results []coldCorpusMountResult) bool {
	if len(results) < 2 {
		return false
	}
	overlapStart := results[0].startedAt
	overlapEnd := results[0].finishedAt
	for _, result := range results[1:] {
		if result.startedAt.After(overlapStart) {
			overlapStart = result.startedAt
		}
		if result.finishedAt.Before(overlapEnd) {
			overlapEnd = result.finishedAt
		}
	}
	return overlapStart.Before(overlapEnd)
}

func TestColdCorpusMountIntervalsRequireStrictOverlap(t *testing.T) {
	base := time.Unix(100, 0)
	if !coldCorpusMountsOverlap([]coldCorpusMountResult{
		{startedAt: base, finishedAt: base.Add(2 * time.Second)},
		{startedAt: base.Add(time.Second), finishedAt: base.Add(3 * time.Second)},
	}) {
		t.Fatal("overlapping mount intervals were rejected")
	}
	if coldCorpusMountsOverlap([]coldCorpusMountResult{
		{startedAt: base, finishedAt: base.Add(time.Second)},
		{startedAt: base.Add(time.Second), finishedAt: base.Add(2 * time.Second)},
	}) {
		t.Fatal("non-overlapping mount intervals were accepted")
	}
}

func tailString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}

// coldCorpusQualificationEnabled reports whether the operator explicitly
// opted in to this heavyweight gate.
func coldCorpusQualificationEnabled(t *testing.T) bool {
	t.Helper()
	switch strings.TrimSpace(os.Getenv(coldCorpusQualifyEnv)) {
	case "1", "true", "yes", "on":
		return true
	default:
		t.Skipf("skipping opt-in cold-mount corpus qualification; set %s=1 to run", coldCorpusQualifyEnv)
		return false
	}
}

// TestColdMountScaleCorpusContract is the local encoding of the schema-v3
// cold-mount acceptance: one cold mount plus two genuinely concurrent
// consumers of the exact 258 MiB corpus, bulk-only reads, bounded CPU and RSS.
func TestColdMountScaleCorpusContract(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("skipping on %s: child rusage RSS units are not qualified", runtime.GOOS)
	}
	if !coldCorpusQualificationEnabled(t) {
		return
	}

	cloud := newColdCorpusCloud()
	defer cloud.Close()

	scratch := t.TempDir()
	mountA := filepath.Join(scratch, "mount-a")
	mountB := filepath.Join(scratch, "mount-b")
	mountC := filepath.Join(scratch, "mount-c")
	stateDir := filepath.Join(scratch, "state")
	for _, dir := range []string{mountA, mountB, mountC, stateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	initial := runColdCorpusMount(t, "initial cold mount", cloud.URL, mountA, filepath.Join(stateDir, "mount-a.json"))
	verifyColdCorpusResources(t, initial)
	verifyColdCorpusMount(t, "initial cold mount", mountA)

	if bulk, point := cloud.bulkReads.Load(), cloud.pointReads.Load(); bulk < 1 || point != 0 {
		t.Fatalf("initial cold mount used %d bulk reads and %d point reads; want >=1 and 0", bulk, point)
	}

	var concurrentWait sync.WaitGroup
	concurrentResults := make([]coldCorpusMountResult, 2)
	concurrentResults[0].label, concurrentResults[1].label = "concurrent cold mount B", "concurrent cold mount C"
	concurrentDirs := []string{mountB, mountC}
	concurrentStates := []string{filepath.Join(stateDir, "mount-b.json"), filepath.Join(stateDir, "mount-c.json")}
	for i := range concurrentResults {
		concurrentWait.Add(1)
		go func(i int) {
			defer concurrentWait.Done()
			concurrentResults[i] = runColdCorpusMount(t, concurrentResults[i].label, cloud.URL, concurrentDirs[i], concurrentStates[i])
		}(i)
	}
	concurrentWait.Wait()
	if !coldCorpusMountsOverlap(concurrentResults) {
		t.Fatalf("concurrent cold mounts did not overlap: %s-%s and %s-%s", concurrentResults[0].startedAt.Format(time.RFC3339Nano), concurrentResults[0].finishedAt.Format(time.RFC3339Nano), concurrentResults[1].startedAt.Format(time.RFC3339Nano), concurrentResults[1].finishedAt.Format(time.RFC3339Nano))
	}
	for _, result := range concurrentResults {
		verifyColdCorpusResources(t, result)
	}
	verifyColdCorpusMount(t, "concurrent cold mount B", mountB)
	verifyColdCorpusMount(t, "concurrent cold mount C", mountC)

	bulkTotal, pointTotal := cloud.bulkReads.Load(), cloud.pointReads.Load()
	if pointTotal != 0 {
		t.Fatalf("corpus mounts regressed to %d point reads in total", pointTotal)
	}
	if bulkTotal < 3 {
		t.Fatalf("corpus mounts used %d bulk reads in total, want at least one per mount", bulkTotal)
	}
}
