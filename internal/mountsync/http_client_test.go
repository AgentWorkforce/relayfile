package mountsync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestHTTPClientRetriesTransientFailure(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&calls, 1)
		if call == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code":"unavailable","message":"retry"}`))
			return
		}
		if r.URL.Path != "/v1/workspaces/ws_retry/fs/tree" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/notion","entries":[],"nextCursor":null}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "token", server.Client())
	tree, err := client.ListTree(context.Background(), "ws_retry", "/notion", 2, "")
	if err != nil {
		t.Fatalf("expected retry to recover from transient 503, got error: %v", err)
	}
	if tree.Path != "/notion" {
		t.Fatalf("expected path /notion, got %s", tree.Path)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected exactly 2 calls (1 retry), got %d", atomic.LoadInt32(&calls))
	}
}

func TestHTTPClientListEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspaces/ws_events/fs/events" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("provider") != "notion" {
			t.Fatalf("expected provider query to be forwarded, got %q", r.URL.Query().Get("provider"))
		}
		if r.URL.Query().Get("cursor") != "evt_1" {
			t.Fatalf("expected cursor query to be forwarded, got %q", r.URL.Query().Get("cursor"))
		}
		if r.URL.Query().Get("limit") != "50" {
			t.Fatalf("expected limit query to be forwarded, got %q", r.URL.Query().Get("limit"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[{"eventId":"evt_2","type":"file.updated","path":"/notion/Docs/A.md","revision":"rev_2"}],"nextCursor":"evt_2"}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "token", server.Client())
	feed, err := client.ListEvents(context.Background(), "ws_events", "notion", "evt_1", 50)
	if err != nil {
		t.Fatalf("list events failed: %v", err)
	}
	if len(feed.Events) != 1 {
		t.Fatalf("expected one event, got %d", len(feed.Events))
	}
	if feed.Events[0].EventID != "evt_2" {
		t.Fatalf("expected event id evt_2, got %s", feed.Events[0].EventID)
	}
	if feed.NextCursor == nil || *feed.NextCursor != "evt_2" {
		t.Fatalf("expected nextCursor evt_2, got %+v", feed.NextCursor)
	}
}

func TestHTTPClientExportFilesUsesPathFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspaces/ws_export/fs/export" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("format") != "json" {
			t.Fatalf("expected json export format, got %q", r.URL.Query().Get("format"))
		}
		if r.URL.Query().Get("path") != "/github" {
			t.Fatalf("expected path filter /github, got %q", r.URL.Query().Get("path"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"path":"/github/repos/demo/README.md","revision":"rev_1","contentType":"text/markdown","content":"# Demo"}]`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "token", server.Client())
	files, err := client.ExportFiles(context.Background(), "ws_export", "/github")
	if err != nil {
		t.Fatalf("export files failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one exported file, got %d", len(files))
	}
	if files[0].Path != "/github/repos/demo/README.md" || files[0].Revision != "rev_1" || files[0].Content != "# Demo" {
		t.Fatalf("unexpected exported file: %+v", files[0])
	}
}

func TestHTTPClientWriteFilesBulkUsesSinglePOSTWithAllFiles(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspaces/ws_bulk/fs/bulk" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST method, got %s", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("expected application/json content type, got %q", got)
		}
		if atomic.AddInt32(&calls, 1) != 1 {
			t.Fatalf("expected exactly one bulk request")
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body failed: %v", err)
		}

		var payload struct {
			Files []BulkWriteFile `json:"files"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("unmarshal request body failed: %v", err)
		}
		if len(payload.Files) != 2 {
			t.Fatalf("expected two files in bulk payload, got %d", len(payload.Files))
		}
		if payload.Files[0].Path != "/notion/Docs/A.md" || payload.Files[0].Content != "# A" {
			t.Fatalf("unexpected first bulk file: %+v", payload.Files[0])
		}
		if payload.Files[1].Path != "/notion/Docs/B.md" || payload.Files[1].Content != "# B" {
			t.Fatalf("unexpected second bulk file: %+v", payload.Files[1])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"written":2,"errorCount":0,"errors":[],"correlationId":"corr_bulk"}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "token", server.Client())
	files := []BulkWriteFile{
		{Path: "/notion/Docs/A.md", ContentType: "text/markdown", Content: "# A"},
		{Path: "/notion/Docs/B.md", ContentType: "text/markdown", Content: "# B"},
	}

	response, err := client.WriteFilesBulk(context.Background(), "ws_bulk", files)
	if err != nil {
		t.Fatalf("bulk write failed: %v", err)
	}
	if response.Written != 2 || response.ErrorCount != 0 || response.CorrelationID != "corr_bulk" {
		t.Fatalf("unexpected bulk response: %+v", response)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected exactly one bulk request, got %d", atomic.LoadInt32(&calls))
	}
}

func TestHTTPClientWriteFilesBulkRejectsEmptyBatch(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "token", server.Client())
	response, err := client.WriteFilesBulk(context.Background(), "ws_bulk", nil)
	if !errors.Is(err, ErrEmptyBulkWrite) {
		t.Fatalf("expected ErrEmptyBulkWrite, got response=%+v err=%v", response, err)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("expected empty batch to fail before issuing a request, got %d calls", atomic.LoadInt32(&calls))
	}
}
