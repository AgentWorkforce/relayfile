package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Regression coverage for the connect poll: a freshly started connect must
// not report success from a Cloud ready row that predates it. Cloud answers a
// workspace-keyed status poll from any existing row for the provider, and a
// Nango connect-session mint returns no connection id, so the CLI has to
// prove the row it accepts was created or re-authenticated after the mint.

// fastConnectPoll removes the polling sleeps so multi-iteration scenarios run
// in milliseconds; the context timeout still bounds the loop.
func fastConnectPoll(t *testing.T) {
	t.Helper()
	previous := integrationConnectPollOpts
	integrationConnectPollOpts = politeOpts{
		sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
		now: func() time.Time {
			// Advance well past the hard per-second ceiling on every read.
			return time.Now().Add(time.Duration(time.Now().UnixNano()%1000) * time.Hour)
		},
	}
	t.Cleanup(func() { integrationConnectPollOpts = previous })
}

type connectStatusScript struct {
	t  *testing.T
	mu sync.Mutex
	// before answers status polls made before the mint (the baseline).
	before map[string]any
	// after answers status polls after the mint, by requested connectionId
	// ("" for workspace-keyed polls); each entry is consumed in order and the
	// last one repeats.
	after       map[string][]map[string]any
	mint        map[string]any
	mintCalls   int
	statusCalls []string
}

func (s *connectStatusScript) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/connect-session") {
			if r.Method != http.MethodPost {
				s.t.Fatalf("connect-session must be POST, got %s", r.Method)
			}
			s.mintCalls++
			_ = json.NewEncoder(w).Encode(s.mint)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/status") {
			s.t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		requested := r.URL.Query().Get("connectionId")
		s.statusCalls = append(s.statusCalls, requested)
		if s.mintCalls == 0 {
			_ = json.NewEncoder(w).Encode(s.before)
			return
		}
		queue := s.after[requested]
		if len(queue) == 0 {
			s.t.Fatalf("no scripted status for connectionId=%q after mint", requested)
		}
		body := queue[0]
		if len(queue) > 1 {
			s.after[requested] = queue[1:]
		}
		_ = json.NewEncoder(w).Encode(body)
	})
}

func staleJiraReady(id, connectedAt string) map[string]any {
	return map[string]any{
		"ready": true, "state": "ready", "provider": "jira", "configKey": "jira-relay",
		"requestedConnectionId": nil, "currentConnectionId": id, "connectionMatched": true,
		"oauth": map[string]any{"connected": true, "connectedAt": connectedAt, "lastAuthAt": connectedAt},
	}
}

func pendingJira(requested string, matched bool) map[string]any {
	return map[string]any{
		"ready": false, "state": "pending", "provider": "jira", "configKey": "jira-relay",
		"requestedConnectionId": requested, "currentConnectionId": nil, "connectionMatched": matched,
		"oauth": map[string]any{"connected": false},
	}
}

func runJiraConnect(t *testing.T, script *connectStatusScript, timeout time.Duration) (string, error, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	fastConnectPoll(t)
	server := httptest.NewServer(script.handler())
	defer server.Close()
	localDir := t.TempDir()
	var stdout bytes.Buffer
	err := connectCloudIntegration(server.URL, "ws_demo", "cloud-token", "jira", "nango", localDir, timeout, false, &stdout)
	return stdout.String(), err, loadSavedConnection(localDir, "jira").ConnectionID
}

// The Jira demo: the mint carries no connection id, the only row Cloud has is
// a ready row from an earlier connect, and OAuth never completes. The old poll
// printed "jira connected" on its first iteration.
func TestConnectDoesNotAcceptStaleReadyRow(t *testing.T) {
	stale := staleJiraReady("048e4b44-stale", "2026-11-06T19:29:09.050Z")
	script := &connectStatusScript{
		t:      t,
		before: stale,
		mint:   map[string]any{"connectLink": "https://connect.example/jira", "backend": "nango", "backendIntegrationId": "jira-relay"},
		after:  map[string][]map[string]any{"ws_demo": {stale}, "": {stale}},
	}
	output, err, savedID := runJiraConnect(t, script, 150*time.Millisecond)
	if err == nil || strings.Contains(output, "jira connected") {
		t.Fatalf("stale ready row satisfied a fresh connect: err=%v output=%s", err, output)
	}
	for _, want := range []string{"timed out waiting for jira connection", "existed before this attempt", "048e4b44-stale", "2026-11-06T19:29:09.050Z"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("timeout error does not name the stale row (%q missing): %v", want, err)
		}
	}
	if savedID == "048e4b44-stale" {
		t.Fatalf("stale connection id was adopted into local state")
	}
	if len(script.statusCalls) < 2 || script.statusCalls[0] != "" {
		t.Fatalf("expected a workspace-keyed baseline poll before the mint, got %v", script.statusCalls)
	}
}

// Same stale row, but the mint returns an id and Cloud reports the minted
// row as pending and then ready. The stale row must not short-circuit while
// the minted row is still pending.
func TestConnectAcceptsMintedConnectionWhenItBecomesReady(t *testing.T) {
	stale := staleJiraReady("048e4b44-stale", "2026-11-06T19:29:09.050Z")
	ready := map[string]any{
		"ready": true, "state": "ready", "provider": "jira", "configKey": "jira-relay",
		"requestedConnectionId": "conn-new", "currentConnectionId": "conn-new", "connectionMatched": true,
		"oauth": map[string]any{"connected": true, "connectedAt": "2026-11-06T23:05:00Z", "lastAuthAt": "2026-11-06T23:05:00Z"},
	}
	script := &connectStatusScript{
		t:      t,
		before: stale,
		mint:   map[string]any{"connectLink": "https://connect.example/jira", "connectionId": "conn-new", "backend": "nango", "backendIntegrationId": "jira-relay"},
		after:  map[string][]map[string]any{"conn-new": {pendingJira("conn-new", true), pendingJira("conn-new", true), ready}, "": {stale}},
	}
	output, err, savedID := runJiraConnect(t, script, 5*time.Second)
	if err != nil || !strings.Contains(output, "jira connected") {
		t.Fatalf("minted connection did not complete: err=%v output=%s", err, output)
	}
	if savedID != "conn-new" {
		t.Fatalf("saved connection id = %q, want the minted id", savedID)
	}
	var mintedPolls int
	for _, requested := range script.statusCalls {
		if requested == "conn-new" {
			mintedPolls++
		}
	}
	if mintedPolls != 3 {
		t.Fatalf("expected the poll to keep waiting on the minted id through two pending answers, got %d polls: %v", mintedPolls, script.statusCalls)
	}
}

// No id from the mint and a stale row present: the connect succeeds only once
// Cloud reports a different connection id than the baseline.
func TestConnectWithoutMintedIDAcceptsOnlyNewConnection(t *testing.T) {
	stale := staleJiraReady("048e4b44-stale", "2026-11-06T19:29:09.050Z")
	fresh := staleJiraReady("05a759f3-fresh", "2026-11-06T23:05:00Z")
	script := &connectStatusScript{
		t:      t,
		before: stale,
		mint:   map[string]any{"connectLink": "https://connect.example/jira", "backend": "nango", "backendIntegrationId": "jira-relay"},
		after:  map[string][]map[string]any{"ws_demo": {stale, stale, fresh}},
	}
	output, err, savedID := runJiraConnect(t, script, 5*time.Second)
	if err != nil || !strings.Contains(output, "jira connected") {
		t.Fatalf("new connection was not accepted: err=%v output=%s", err, output)
	}
	if savedID != "05a759f3-fresh" {
		t.Fatalf("saved connection id = %q, want the new Cloud binding", savedID)
	}
}

// A re-authentication of the same connection id is a new connection when
// Cloud's OAuth timestamp advances past the baseline.
func TestConnectWithoutMintedIDAcceptsReauthOfSameConnection(t *testing.T) {
	before := staleJiraReady("048e4b44-same", "2026-11-06T19:29:09.050Z")
	reauthed := staleJiraReady("048e4b44-same", "2026-11-06T23:05:00Z")
	script := &connectStatusScript{
		t:      t,
		before: before,
		mint:   map[string]any{"connectLink": "https://connect.example/jira", "backend": "nango", "backendIntegrationId": "jira-relay"},
		after:  map[string][]map[string]any{"ws_demo": {before, reauthed}},
	}
	output, err, _ := runJiraConnect(t, script, 5*time.Second)
	if err != nil || !strings.Contains(output, "jira connected") {
		t.Fatalf("re-authenticated connection was not accepted: err=%v output=%s", err, output)
	}
}

// First connect ever: no baseline row, so the first ready row is this attempt.
func TestConnectWithoutBaselineRowAcceptsFirstReadyRow(t *testing.T) {
	fresh := staleJiraReady("first-conn", "2026-11-06T23:05:00Z")
	script := &connectStatusScript{
		t:      t,
		before: pendingJira("", true),
		mint:   map[string]any{"connectLink": "https://connect.example/jira", "backend": "nango", "backendIntegrationId": "jira-relay"},
		after:  map[string][]map[string]any{"ws_demo": {pendingJira("ws_demo", true), fresh}},
	}
	output, err, savedID := runJiraConnect(t, script, 5*time.Second)
	if err != nil || !strings.Contains(output, "jira connected") || savedID != "first-conn" {
		t.Fatalf("first connect did not complete: err=%v output=%s saved=%s", err, output, savedID)
	}
}

// When the baseline cannot be read, nothing workspace-keyed can be attributed
// to this attempt: a ready row is refused unless Cloud binds it to the
// minted id.
func TestConnectFailsClosedWithoutBaseline(t *testing.T) {
	stale := staleJiraReady("048e4b44-stale", "2026-11-06T19:29:09.050Z")
	t.Run("workspace keyed ready row is refused", func(t *testing.T) {
		script := &connectStatusScript{
			t:     t,
			mint:  map[string]any{"connectLink": "https://connect.example/jira", "backend": "nango", "backendIntegrationId": "jira-relay"},
			after: map[string][]map[string]any{"ws_demo": {stale}},
		}
		// The pre-mint baseline poll is answered with 502 by the wrapper below.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/status") && script.mintCalls == 0 {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"error":"upstream_unavailable"}`))
				return
			}
			script.handler().ServeHTTP(w, r)
		}))
		defer server.Close()
		t.Setenv("HOME", t.TempDir())
		clearRelayfileEnv(t)
		fastConnectPoll(t)
		var stdout bytes.Buffer
		err := connectCloudIntegration(server.URL, "ws_demo", "cloud-token", "jira", "nango", t.TempDir(), 150*time.Millisecond, false, &stdout)
		if err == nil || strings.Contains(stdout.String(), "jira connected") {
			t.Fatalf("unattributable ready row was accepted: err=%v output=%s", err, stdout.String())
		}
	})
	t.Run("minted id bound by Cloud is accepted", func(t *testing.T) {
		bound := map[string]any{
			"ready": true, "state": "ready", "provider": "jira", "configKey": "jira-relay",
			"requestedConnectionId": "conn-new", "currentConnectionId": "conn-new", "connectionMatched": true,
			"oauth": map[string]any{"connected": true},
		}
		script := &connectStatusScript{
			t:     t,
			mint:  map[string]any{"connectLink": "https://connect.example/jira", "connectionId": "conn-new", "backend": "nango", "backendIntegrationId": "jira-relay"},
			after: map[string][]map[string]any{"conn-new": {bound}},
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/status") && script.mintCalls == 0 {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"error":"upstream_unavailable"}`))
				return
			}
			script.handler().ServeHTTP(w, r)
		}))
		defer server.Close()
		t.Setenv("HOME", t.TempDir())
		clearRelayfileEnv(t)
		fastConnectPoll(t)
		var stdout bytes.Buffer
		localDir := t.TempDir()
		err := connectCloudIntegration(server.URL, "ws_demo", "cloud-token", "jira", "nango", localDir, 5*time.Second, false, &stdout)
		if err != nil || !strings.Contains(stdout.String(), "jira connected") || loadSavedConnection(localDir, "jira").ConnectionID != "conn-new" {
			t.Fatalf("Cloud-bound minted connection was refused: err=%v output=%s", err, stdout.String())
		}
	})
}

// Cloud's connectionMatched only binds when the request carried the minted
// id; a matched workspace-keyed poll says nothing about which row answered.
func TestConnectStatusBindsMintedIdentity(t *testing.T) {
	matched := true
	unmatched := false
	cases := []struct {
		name   string
		status cloudIntegrationReadyResponse
		minted string
		want   bool
	}{
		{"matched minted id", cloudIntegrationReadyResponse{ConnectionMatched: &matched, CurrentConnectionID: "conn-new"}, "conn-new", true},
		{"matched without current id", cloudIntegrationReadyResponse{ConnectionMatched: &matched}, "conn-new", true},
		{"matched but different current id", cloudIntegrationReadyResponse{ConnectionMatched: &matched, CurrentConnectionID: "older"}, "conn-new", false},
		{"no minted id", cloudIntegrationReadyResponse{ConnectionMatched: &matched, CurrentConnectionID: "older"}, "", false},
		{"older Cloud without connectionMatched", cloudIntegrationReadyResponse{CurrentConnectionID: "conn-new"}, "conn-new", false},
		{"explicitly unmatched", cloudIntegrationReadyResponse{ConnectionMatched: &unmatched, CurrentConnectionID: "conn-new"}, "conn-new", false},
	}
	for _, tc := range cases {
		if got := connectStatusBindsMintedIdentity(tc.status, tc.minted); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestConnectIdentityBaselineEvidence(t *testing.T) {
	oauth := func(connectedAt, lastAuthAt string) *struct {
		Connected   bool   `json:"connected"`
		ConnectedAt string `json:"connectedAt,omitempty"`
		LastAuthAt  string `json:"lastAuthAt,omitempty"`
	} {
		return &struct {
			Connected   bool   `json:"connected"`
			ConnectedAt string `json:"connectedAt,omitempty"`
			LastAuthAt  string `json:"lastAuthAt,omitempty"`
		}{Connected: true, ConnectedAt: connectedAt, LastAuthAt: lastAuthAt}
	}
	baseline := newConnectIdentityBaseline(cloudIntegrationReadyResponse{CurrentConnectionID: "old", OAuth: oauth("2026-11-06T19:29:09.050Z", "2026-11-06T19:29:09.050Z")}, nil)
	cases := []struct {
		name   string
		status cloudIntegrationReadyResponse
		want   bool
	}{
		{"identical row", cloudIntegrationReadyResponse{CurrentConnectionID: "old", OAuth: oauth("2026-11-06T19:29:09.050Z", "2026-11-06T19:29:09.050Z")}, false},
		{"same id without timestamps", cloudIntegrationReadyResponse{CurrentConnectionID: "old"}, false},
		{"different id", cloudIntegrationReadyResponse{CurrentConnectionID: "new"}, true},
		{"same id re-authed", cloudIntegrationReadyResponse{CurrentConnectionID: "old", OAuth: oauth("2026-11-06T19:29:09.050Z", "2026-11-06T23:05:00Z")}, true},
		{"same id earlier timestamp", cloudIntegrationReadyResponse{CurrentConnectionID: "old", OAuth: oauth("2026-11-06T18:00:00Z", "2026-11-06T18:00:00Z")}, false},
		{"no id at all", cloudIntegrationReadyResponse{Ready: true}, false},
	}
	for _, tc := range cases {
		if got := baseline.evidencesNewConnection(tc.status); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	empty := newConnectIdentityBaseline(cloudIntegrationReadyResponse{}, nil)
	if !empty.evidencesNewConnection(cloudIntegrationReadyResponse{CurrentConnectionID: "first"}) {
		t.Fatalf("first row after an empty baseline should count as new")
	}
	if empty.evidencesNewConnection(cloudIntegrationReadyResponse{Ready: true}) {
		t.Fatalf("a ready status with no identity must not count as new")
	}
	unknown := newConnectIdentityBaseline(cloudIntegrationReadyResponse{}, context.DeadlineExceeded)
	if unknown.evidencesNewConnection(cloudIntegrationReadyResponse{CurrentConnectionID: "new"}) {
		t.Fatalf("an unreadable baseline must fail closed")
	}
}
