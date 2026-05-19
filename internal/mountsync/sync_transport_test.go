package mountsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestSyncTransportHasNoWholeRequestTimeout asserts that the helper-built
// sync client carries NO whole-request http.Client.Timeout (the blunt cap
// that net/http enforces independent of context and that aborted the
// 581-file bootstrap mid-body-read), while still bounding the connection
// lifecycle with granular transport timeouts.
func TestSyncTransportHasNoWholeRequestTimeout(t *testing.T) {
	client := NewSyncHTTPClient()
	if client.Timeout != 0 {
		t.Fatalf("NewSyncHTTPClient must have Timeout==0 (no whole-request cap); got %s", client.Timeout)
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if tr.TLSHandshakeTimeout != 10*time.Second {
		t.Errorf("TLSHandshakeTimeout = %s, want 10s", tr.TLSHandshakeTimeout)
	}
	if tr.ResponseHeaderTimeout != 30*time.Second {
		t.Errorf("ResponseHeaderTimeout = %s, want 30s", tr.ResponseHeaderTimeout)
	}
	if tr.ExpectContinueTimeout != 1*time.Second {
		t.Errorf("ExpectContinueTimeout = %s, want 1s", tr.ExpectContinueTimeout)
	}
	if tr.IdleConnTimeout != 90*time.Second {
		t.Errorf("IdleConnTimeout = %s, want 90s", tr.IdleConnTimeout)
	}
	if tr.DialContext == nil {
		t.Error("DialContext must be set (granular dial timeout)")
	}
}

// TestNewHTTPClientPreservesSuppliedClientTimeout proves NewHTTPClient does
// not mutate or silently relax caller-owned request bounds. Poll/bootstrap
// paths that need no whole-request cap pass NewSyncHTTPClient explicitly.
func TestNewHTTPClientPreservesSuppliedClientTimeout(t *testing.T) {
	supplied := &http.Client{Timeout: 15 * time.Second}
	client := NewHTTPClient("http://example.invalid", "tok", supplied)
	if supplied.Timeout != 15*time.Second {
		t.Fatalf("NewHTTPClient mutated supplied Timeout to %s", supplied.Timeout)
	}
	if client.httpClient == supplied {
		t.Fatal("expected NewHTTPClient to clone the supplied client")
	}
	if client.httpClient.Timeout != 15*time.Second {
		t.Fatalf("expected cloned client to preserve Timeout, got %s", client.httpClient.Timeout)
	}
}

// TestSyncClientCompletesSlowProgressingBodyPastOldCap is the focused
// regression test for THIS gap: a server that trickles a large body slowly
// so the TOTAL request wall-clock far exceeds the old 15s
// http.Client.Timeout. With the whole-request cap removed it must succeed,
// because the request is only bounded by the caller's context (here a
// generous bootstrap-style deadline). Run with -race.
func TestSyncClientCompletesSlowProgressingBodyPastOldCap(t *testing.T) {
	if testing.Short() {
		t.Skip("slow trickle test; skipped under -short")
	}

	// Build a JSON ExportFiles response body, then stream it in chunks
	// with a small inter-chunk delay so the cumulative read time exceeds
	// the legacy 15s whole-request cap but each chunk makes progress.
	files := make([]RemoteFile, 0, 64)
	for i := 0; i < 64; i++ {
		files = append(files, RemoteFile{
			Path:        "/big/file_" + itoa(i) + ".txt",
			Revision:    "rev_1",
			ContentType: "text/plain",
			Content:     "hello world payload chunk for file",
		})
	}
	payload, err := json.Marshal(files)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	const chunks = 40
	const perChunkDelay = 450 * time.Millisecond
	// total stream time ~= 40 * 450ms = 18s  > old 15s whole-request cap.

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		size := len(payload)
		step := (size + chunks - 1) / chunks
		for off := 0; off < size; off += step {
			end := off + step
			if end > size {
				end = size
			}
			_, _ = w.Write(payload[off:end])
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(perChunkDelay)
		}
	}))
	defer srv.Close()

	// Sync client: zero whole-request timeout, granular transport.
	client := NewHTTPClient(srv.URL, "tok", NewSyncHTTPClient())

	// Caller-side context models the progress-extending bootstrap ctx:
	// generous enough to allow the full (slow) body to stream.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := time.Now()
	got, err := client.ExportFiles(ctx, "ws_1", "/")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ExportFiles failed on slow-but-progressing body (the regression): %v", err)
	}
	if elapsed < 15*time.Second {
		t.Fatalf("test did not actually exceed the old 15s cap (elapsed %s); not a valid regression", elapsed)
	}
	if len(got) != len(files) {
		t.Fatalf("incomplete body: got %d files, want %d", len(got), len(files))
	}
}

// itoa avoids importing strconv just for the test fixture loop.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
