package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/delegatedauth"
)

// stubOneShotBusyRetry replaces the sleep hook so tests run instantly and can
// assert on the recorded delays.
func stubOneShotBusyRetry(t *testing.T) *[]time.Duration {
	t.Helper()
	var delays []time.Duration
	prevSleep := oneShotBusyRetrySleep
	oneShotBusyRetrySleep = func(_ context.Context, d time.Duration) error {
		delays = append(delays, d)
		return nil
	}
	t.Cleanup(func() { oneShotBusyRetrySleep = prevSleep })
	return &delays
}

// busyFileServer answers the first busyResponses requests with 429
// workspace_busy and Retry-After: 2, then serves the file.
func busyFileServer(t *testing.T, busyResponses int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n <= busyResponses {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":"workspace_busy","message":"workspace durable object is busy; retry after the advertised delay"}`))
			return
		}
		_, _ = w.Write([]byte(`{"path":"/github/README.md","revision":"rev_1","contentType":"text/markdown","content":"# readme\n"}`))
	}))
	t.Cleanup(server.Close)
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_cloud",
		AccessToken:          "token",
	})
	return server, &requests
}

func TestReadRetriesWorkspaceBusyHonoringRetryAfter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	delays := stubOneShotBusyRetry(t)
	_, requests := busyFileServer(t, 2)

	var stdout, stderr bytes.Buffer
	if err := run([]string{"read", "ws_cloud", "/github/README.md"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run read failed: %v", err)
	}
	if got := stdout.String(); got != "# readme\n" {
		t.Fatalf("unexpected file content: %q", got)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("expected 3 requests (2 busy + 1 success), got %d", got)
	}
	if len(*delays) != 2 {
		t.Fatalf("expected 2 retry sleeps, got %v", *delays)
	}
	for _, d := range *delays {
		if d < 2*time.Second || d > 2*time.Second+2*time.Second/4 {
			t.Fatalf("expected delay to honor Retry-After 2s plus <=25%% jitter, got %s", d)
		}
	}
	// Notices go to the stderr writer passed to run, not the process stderr,
	// and quote the server's error code rather than guessing a cause.
	notices := strings.Count(stderr.String(), "http 429 workspace_busy: workspace durable object is busy; retry after the advertised delay; retrying in ")
	if notices != 2 || !strings.Contains(stderr.String(), "attempt 2/4") {
		t.Fatalf("expected one stderr notice per retry, got %q", stderr.String())
	}
}

func TestReadNoRetryFailsImmediatelyOnWorkspaceBusy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	delays := stubOneShotBusyRetry(t)
	_, requests := busyFileServer(t, 1)

	var stdout, stderr bytes.Buffer
	err := run([]string{"read", "ws_cloud", "/github/README.md", "--no-retry"}, strings.NewReader(""), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "workspace_busy") {
		t.Fatalf("expected workspace_busy error, got %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("expected exactly 1 request with --no-retry, got %d", got)
	}
	if len(*delays) != 0 || stderr.Len() != 0 {
		t.Fatalf("expected no retry sleep or notice, got delays=%v stderr=%q", *delays, stderr.String())
	}
}

func TestReadGivesUpAfterBoundedBusyRetries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	delays := stubOneShotBusyRetry(t)
	_, requests := busyFileServer(t, 100)

	var stdout bytes.Buffer
	err := run([]string{"read", "ws_cloud", "/github/README.md"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil || !strings.Contains(err.Error(), "http 429") {
		t.Fatalf("expected final 429 error, got %v", err)
	}
	if got := requests.Load(); got != oneShotBusyRetryMaxAttempts {
		t.Fatalf("expected %d attempts, got %d", oneShotBusyRetryMaxAttempts, got)
	}
	if len(*delays) != oneShotBusyRetryMaxAttempts-1 {
		t.Fatalf("expected %d sleeps, got %v", oneShotBusyRetryMaxAttempts-1, *delays)
	}
}

func TestOneShotBusyRetryDelayClampsAndBacksOff(t *testing.T) {
	for i := 0; i < 50; i++ {
		if d := oneShotBusyRetryDelay(1, time.Hour); d != oneShotBusyRetryMaxDelay {
			t.Fatalf("expected huge Retry-After clamped to %s, got %s", oneShotBusyRetryMaxDelay, d)
		}
		if d := oneShotBusyRetryDelay(1, 0); d < oneShotBusyRetryBaseDelay || d > oneShotBusyRetryBaseDelay*5/4 {
			t.Fatalf("expected first backoff near %s, got %s", oneShotBusyRetryBaseDelay, d)
		}
		if d := oneShotBusyRetryDelay(3, 0); d < 4*oneShotBusyRetryBaseDelay || d > 5*oneShotBusyRetryBaseDelay {
			t.Fatalf("expected third backoff near %s, got %s", 4*oneShotBusyRetryBaseDelay, d)
		}
	}
}

func TestWorkspaceCommandClientDoesNotRetryWritesOnBusy(t *testing.T) {
	_ = stubOneShotBusyRetry(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, err := newAPIClient(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	commandClient := &workspaceCommandClient{workspaceID: "ws_cloud", client: client, directToken: true, retryBusy: true}
	pathFor := func(id string) string { return "/v1/workspaces/" + id + "/fs/file" }

	if err := commandClient.postWorkspaceJSON(context.Background(), pathFor, map[string]string{"k": "v"}, nil); !isBusyAPIError(err) {
		t.Fatalf("expected busy error from POST, got %v", err)
	}
	if err := commandClient.deleteWorkspaceJSON(context.Background(), pathFor, "rev_1", nil); !isBusyAPIError(err) {
		t.Fatalf("expected busy error from DELETE, got %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("expected writes to be attempted exactly once each, got %d requests", got)
	}
}

func TestOneLineNoticeStripsControlCharacters(t *testing.T) {
	got := oneLineNotice("http 429 workspace_busy: busy\n\x1b[31mred\x1b[0m\tdone\u202e")
	if want := "http 429 workspace_busy: busy [31mred [0m done"; got != want {
		t.Fatalf("oneLineNotice = %q, want %q", got, want)
	}
}
