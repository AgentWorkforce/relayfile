package mountsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

type bulkReadTestClient struct {
	RemoteClient

	mu               sync.Mutex
	files            map[string]RemoteFile
	bulkCalls        [][]string
	bulkErr          error
	pointErrs        map[string]error
	pointReadCalls   int
	activePointReads int
	maxPointReads    int
	pointReadDelay   time.Duration
}

func (c *bulkReadTestClient) ReadFilesBulk(_ context.Context, _ string, paths []string) (BulkReadResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bulkCalls = append(c.bulkCalls, append([]string(nil), paths...))
	if c.bulkErr != nil {
		return BulkReadResponse{}, c.bulkErr
	}
	response := BulkReadResponse{Files: make([]BulkReadFileResult, 0, len(paths))}
	for _, path := range paths {
		file, ok := c.files[path]
		if !ok {
			response.Files = append(response.Files, BulkReadFileResult{
				Path:  path,
				Error: &BulkReadFileError{Status: http.StatusNotFound, Code: "not_found", Message: "not found"},
			})
			continue
		}
		response.Files = append(response.Files, BulkReadFileResult{
			Path: file.Path, Revision: file.Revision, ContentType: file.ContentType,
			Content: file.Content, Encoding: file.Encoding, ContentHash: file.ContentHash,
		})
	}
	return response, nil
}

func (c *bulkReadTestClient) ReadFile(_ context.Context, _ string, path string) (RemoteFile, error) {
	c.mu.Lock()
	c.pointReadCalls++
	c.activePointReads++
	if c.activePointReads > c.maxPointReads {
		c.maxPointReads = c.activePointReads
	}
	file, ok := c.files[path]
	delay := c.pointReadDelay
	c.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	c.mu.Lock()
	c.activePointReads--
	c.mu.Unlock()
	if !ok {
		return RemoteFile{}, &HTTPError{StatusCode: http.StatusNotFound, Code: "not_found", Message: "not found"}
	}
	if err := c.pointErrs[path]; err != nil {
		return RemoteFile{}, err
	}
	return file, nil
}

func TestBootstrapBulkReadBatchesAt32AndDeclaredByteLimit(t *testing.T) {
	client := &bulkReadTestClient{files: map[string]RemoteFile{}}
	jobs := make([]bootstrapReadJob, 0, 70)
	for i := 0; i < 70; i++ {
		path := fmt.Sprintf("/fixture/%03d.bin", i)
		client.files[path] = RemoteFile{Path: path, Revision: fmt.Sprintf("rev_%d", i+1), ContentType: "application/octet-stream", Content: "x"}
		jobs = append(jobs, bootstrapReadJob{Index: i, RemotePath: path, Size: 2 << 20})
	}

	syncer := &Syncer{workspace: "ws_bulk", client: client}
	results := syncer.readBootstrapFiles(context.Background(), jobs, bootstrapProgress{})
	if len(results) != len(jobs) {
		t.Fatalf("results = %d, want %d", len(results), len(jobs))
	}
	if len(client.bulkCalls) != 5 {
		t.Fatalf("bulk calls = %d, want 5 byte-bounded batches", len(client.bulkCalls))
	}
	for call, paths := range client.bulkCalls {
		if len(paths) > 16 {
			t.Fatalf("bulk call %d has %d paths, want <=16 for 2 MiB declared files", call, len(paths))
		}
	}
	if client.pointReadCalls != 0 {
		t.Fatalf("point reads = %d, want 0", client.pointReadCalls)
	}
}

func TestBootstrapBulkReadFallbackRequiresExplicitUnsupported(t *testing.T) {
	jobs := []bootstrapReadJob{{Index: 0, RemotePath: "/a", Size: 1}}
	file := RemoteFile{Path: "/a", Revision: "rev_1", ContentType: "text/plain", Content: "a"}

	t.Run("typed 501 falls back", func(t *testing.T) {
		client := &bulkReadTestClient{
			files:   map[string]RemoteFile{"/a": file},
			bulkErr: &HTTPError{StatusCode: http.StatusNotImplemented, Code: "bulk_read_unsupported", Message: "unsupported"},
		}
		syncer := &Syncer{workspace: "ws", client: client}
		results := syncer.readBootstrapFiles(context.Background(), jobs, bootstrapProgress{})
		if len(results) != 1 || results[0].Err != nil {
			t.Fatalf("fallback results = %#v", results)
		}
		if client.pointReadCalls != 1 {
			t.Fatalf("point reads = %d, want 1", client.pointReadCalls)
		}
		secondJobs := []bootstrapReadJob{{Index: 1, RemotePath: "/b", Size: 1}}
		client.files["/b"] = RemoteFile{Path: "/b", Revision: "rev_2", ContentType: "text/plain", Content: "b"}
		second := syncer.readBootstrapFiles(context.Background(), secondJobs, bootstrapProgress{})
		if len(second) != 1 || second[0].Err != nil {
			t.Fatalf("second fallback results = %#v", second)
		}
		if len(client.bulkCalls) != 1 {
			t.Fatalf("bulk compatibility probes = %d, want exactly 1", len(client.bulkCalls))
		}
		if client.pointReadCalls != 2 {
			t.Fatalf("point reads = %d, want 2 across both checkpoints", client.pointReadCalls)
		}
	})

	t.Run("ordinary 404 stays a bulk failure", func(t *testing.T) {
		client := &bulkReadTestClient{
			files:   map[string]RemoteFile{"/a": file},
			bulkErr: &HTTPError{StatusCode: http.StatusNotFound, Code: "not_found", Message: "route not found"},
		}
		results := (&Syncer{workspace: "ws", client: client}).readBootstrapFiles(context.Background(), jobs, bootstrapProgress{})
		if len(results) != 1 || results[0].Err == nil {
			t.Fatalf("404 results = %#v, want failure", results)
		}
		if client.pointReadCalls != 0 {
			t.Fatalf("point reads = %d, want 0", client.pointReadCalls)
		}
	})
}

func TestBootstrapBulkReadUnsupportedDoesNotRereadOversizedPointJobs(t *testing.T) {
	client := &bulkReadTestClient{
		files: map[string]RemoteFile{
			"/large": {Path: "/large", Revision: "rev_large", ContentType: "application/octet-stream", Content: "large"},
			"/small": {Path: "/small", Revision: "rev_small", ContentType: "text/plain", Content: "small"},
		},
		bulkErr: &HTTPError{StatusCode: http.StatusNotImplemented, Code: "bulk_read_unsupported", Message: "unsupported"},
	}
	syncer := &Syncer{workspace: "ws", client: client}
	results := syncer.readBootstrapFiles(context.Background(), []bootstrapReadJob{
		{Index: 0, RemotePath: "/large", Size: defaultBulkReadMaxBytes + 1},
		{Index: 1, RemotePath: "/small", Size: 1},
	}, bootstrapProgress{})
	if len(results) != 2 || client.pointReadCalls != 2 {
		t.Fatalf("results=%d point reads=%d, want 2 and 2", len(results), client.pointReadCalls)
	}
}

func TestBootstrapBulkReadPointReadsDeclaredOversizedFiles(t *testing.T) {
	path := "/large.bin"
	client := &bulkReadTestClient{files: map[string]RemoteFile{
		path: {Path: path, Revision: "rev_large", ContentType: "application/octet-stream", Content: "x"},
	}}
	syncer := &Syncer{workspace: "ws", client: client}
	results := syncer.readBootstrapFiles(context.Background(), []bootstrapReadJob{{
		Index: 0, RemotePath: path, Size: defaultBulkReadMaxBytes + 1,
	}}, bootstrapProgress{})
	if len(results) != 1 || results[0].Err != nil || results[0].File.Content != "x" {
		t.Fatalf("oversized point-read results = %#v", results)
	}
	if len(client.bulkCalls) != 0 || client.pointReadCalls != 1 {
		t.Fatalf("bulk calls = %d, point reads = %d; want 0, 1", len(client.bulkCalls), client.pointReadCalls)
	}
}

func TestBootstrapBulkReadOversizedPointReadsAreAppliedIncrementally(t *testing.T) {
	client := &bulkReadTestClient{
		files: map[string]RemoteFile{
			"/large-a": {Path: "/large-a", Revision: "rev_a", ContentType: "application/octet-stream", Content: "a"},
			"/large-b": {Path: "/large-b", Revision: "rev_b", ContentType: "application/octet-stream", Content: "b"},
		},
		pointReadDelay: 5 * time.Millisecond,
	}
	var applied []string
	err := (&Syncer{workspace: "ws", client: client}).readBootstrapFilesEach(context.Background(), []bootstrapReadJob{
		{Index: 0, RemotePath: "/large-a", Size: defaultBulkReadMaxBytes + 1},
		{Index: 1, RemotePath: "/large-b", Size: defaultBulkReadMaxBytes + 1},
	}, bootstrapProgress{}, func(result bootstrapReadResult) error {
		if result.Err != nil {
			return result.Err
		}
		applied = append(applied, result.RemotePath)
		return nil
	})
	if err != nil {
		t.Fatalf("incremental read = %v", err)
	}
	if len(applied) != 2 || client.maxPointReads != 1 {
		t.Fatalf("applied=%v max concurrent point reads=%d, want 2 and 1", applied, client.maxPointReads)
	}
}

func TestBootstrapBulkReadPreservesMixedJobIndexOrder(t *testing.T) {
	client := &bulkReadTestClient{
		files: map[string]RemoteFile{
			"/small": {Path: "/small", Revision: "rev_small", ContentType: "text/plain", Content: "small"},
			"/large": {Path: "/large", Revision: "rev_large", ContentType: "application/octet-stream", Content: "large"},
		},
		pointErrs: map[string]error{
			"/large": &HTTPError{StatusCode: http.StatusServiceUnavailable, Code: "unavailable", Message: "retry"},
		},
	}
	var seen []string
	err := (&Syncer{workspace: "ws", client: client}).readBootstrapFilesEach(context.Background(), []bootstrapReadJob{
		{Index: 0, RemotePath: "/small", Size: 1},
		{Index: 1, RemotePath: "/large", Size: defaultBulkReadMaxBytes + 1},
	}, bootstrapProgress{}, func(result bootstrapReadResult) error {
		seen = append(seen, result.RemotePath)
		return result.Err
	})
	if err == nil {
		t.Fatal("expected transient point-read error")
	}
	if got, want := fmt.Sprint(seen), "[/small /large]"; got != want {
		t.Fatalf("callback order = %s, want %s", got, want)
	}
	if len(client.bulkCalls) != 1 || len(client.bulkCalls[0]) != 1 || client.bulkCalls[0][0] != "/small" {
		t.Fatalf("bulk calls = %#v, want one call for /small before point read", client.bulkCalls)
	}
}

func TestBootstrapBulkReadRejectsCustomClientResultCountMismatch(t *testing.T) {
	for _, count := range []int{0, 2} {
		t.Run(fmt.Sprintf("count_%d", count), func(t *testing.T) {
			client := &bulkReadTestClient{files: map[string]RemoteFile{}}
			client.bulkErr = nil
			// Override through a wrapper that returns the requested mismatch.
			mismatch := &bulkReadCountMismatchClient{bulkReadTestClient: client, count: count}
			syncer := &Syncer{workspace: "ws", client: mismatch}
			results := syncer.readBootstrapFiles(context.Background(), []bootstrapReadJob{{Index: 0, RemotePath: "/a", Size: 1}}, bootstrapProgress{})
			if len(results) != 1 || results[0].Err == nil {
				t.Fatalf("mismatch results = %#v, want one error", results)
			}
		})
	}
}

type bulkReadCountMismatchClient struct {
	*bulkReadTestClient
	count int
}

func (c *bulkReadCountMismatchClient) ReadFilesBulk(context.Context, string, []string) (BulkReadResponse, error) {
	files := make([]BulkReadFileResult, c.count)
	for i := range files {
		files[i] = BulkReadFileResult{Path: "/a", Revision: "rev", ContentType: "text/plain", Content: "a"}
	}
	return BulkReadResponse{Files: files}, nil
}

func TestValidateBulkReadResponseRejectsAggregateDecodedOverflow(t *testing.T) {
	content := make([]byte, defaultBulkReadMaxBytes+1)
	err := validateBulkReadResponse([]string{"/large"}, BulkReadResponse{Files: []BulkReadFileResult{{
		Path: "/large", Revision: "rev_1", ContentType: "application/octet-stream", Content: string(content),
	}}})
	if err == nil {
		t.Fatal("expected aggregate byte overflow")
	}
}

func TestValidateBulkReadResponseRejectsMalformedResultVariants(t *testing.T) {
	tests := []struct {
		name   string
		result BulkReadFileResult
	}{
		{
			name:   "invalid error status",
			result: BulkReadFileResult{Path: "/a", Error: &BulkReadFileError{Status: 0, Code: "not_found", Message: "missing"}},
		},
		{
			name:   "invalid success encoding",
			result: BulkReadFileResult{Path: "/a", Revision: "rev_1", Content: "a", Encoding: "gzip"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateBulkReadResponse([]string{"/a"}, BulkReadResponse{Files: []BulkReadFileResult{test.result}}); err == nil {
				t.Fatal("expected malformed result rejection")
			}
		})
	}
}

func TestBulkReadFileResultRejectsNullContentFields(t *testing.T) {
	for _, payload := range []string{
		`{"path":"/a","revision":"rev_1","content":null,"contentType":"text/plain"}`,
		`{"path":"/a","revision":"rev_1","content":"a","contentType":null}`,
	} {
		var result BulkReadFileResult
		if err := json.Unmarshal([]byte(payload), &result); err == nil {
			t.Fatalf("payload %s was accepted", payload)
		}
	}
}
