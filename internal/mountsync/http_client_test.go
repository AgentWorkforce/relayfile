package mountsync

import (
	"context"
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
