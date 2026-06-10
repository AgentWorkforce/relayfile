package httpapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/digest"
	"github.com/agentworkforce/relayfile/internal/relayfile"
)

type tarballTestEntry struct {
	path    string
	content []byte
}

func buildTestGithubTarball(t *testing.T, entries []tarballTestEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for _, entry := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name:     entry.path,
			Mode:     0o644,
			Size:     int64(len(entry.content)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write(entry.content); err != nil {
			t.Fatalf("write tar content: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return buf.Bytes()
}

type githubTarImportResponse struct {
	Imported   int `json:"imported"`
	ErrorCount int `json:"errorCount"`
	Errors     []struct {
		Path    string `json:"path"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Skipped []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"skipped"`
	BytesWritten  int64  `json:"bytesWritten"`
	CorrelationID string `json:"correlationId"`
}

func TestGithubTarballImportEndpoint(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	server := NewServer(store)
	token := mustTestJWT(t, "dev-secret", "ws_tarball", "Worker1", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	oversized := bytes.Repeat([]byte("x"), githubTarballMaxFileBytes+1)
	archive := buildTestGithubTarball(t, []tarballTestEntry{
		{path: "octo-demo-abc123/README.md", content: []byte("# Demo\n")},
		{path: "octo-demo-abc123/src/app.ts", content: []byte("export const ok = true;\n")},
		{path: "octo-demo-abc123/src/logo.png", content: []byte{0x00, 0x01, 0x02, 0x03}},
		{path: "octo-demo-abc123/node_modules/skip.js", content: []byte("ignored\n")},
		{path: "octo-demo-abc123/big.txt", content: oversized},
	})

	resp := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_tarball/fs/import/github-tarball?owner=octo&repo=demo&headSha=abc123&jobId=job-direct",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"Content-Type":     "application/gzip",
			"X-Correlation-Id": "corr_tarball_import",
		},
		body: archive,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 on tarball import, got %d (%s)", resp.Code, resp.Body.String())
	}
	var payload githubTarImportResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if payload.Imported != 3 {
		t.Fatalf("expected imported=3, got %d (%+v)", payload.Imported, payload)
	}
	if payload.ErrorCount != 0 || len(payload.Errors) != 0 {
		t.Fatalf("expected no errors, got %+v", payload.Errors)
	}
	skippedReasons := map[string]string{}
	for _, skip := range payload.Skipped {
		skippedReasons[skip.Path] = skip.Reason
	}
	if skippedReasons["node_modules/skip.js"] != "ignored" {
		t.Fatalf("expected node_modules/skip.js skipped as ignored, got %+v", payload.Skipped)
	}
	if skippedReasons["big.txt"] != "too-large" {
		t.Fatalf("expected big.txt skipped as too-large, got %+v", payload.Skipped)
	}
	wantBytes := int64(len("# Demo\n") + len("export const ok = true;\n") + 4)
	if payload.BytesWritten != wantBytes {
		t.Fatalf("expected bytesWritten=%d, got %d", wantBytes, payload.BytesWritten)
	}

	readResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_tarball/fs/file?path=/github/repos/octo/demo/contents/README.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_tarball_read",
		},
	})
	if readResp.Code != http.StatusOK {
		t.Fatalf("expected 200 reading imported file, got %d (%s)", readResp.Code, readResp.Body.String())
	}
	var file relayfile.File
	if err := json.NewDecoder(readResp.Body).Decode(&file); err != nil {
		t.Fatalf("decode file response: %v", err)
	}
	if file.Content != "# Demo\n" {
		t.Fatalf("unexpected imported content: %q", file.Content)
	}

	binResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_tarball/fs/file?path=/github/repos/octo/demo/contents/src/logo.png",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_tarball_read_bin",
		},
	})
	if binResp.Code != http.StatusOK {
		t.Fatalf("expected 200 reading binary file, got %d (%s)", binResp.Code, binResp.Body.String())
	}
	var binFile relayfile.File
	if err := json.NewDecoder(binResp.Body).Decode(&binFile); err != nil {
		t.Fatalf("decode binary file response: %v", err)
	}
	if binFile.Encoding != "base64" {
		t.Fatalf("expected base64 encoding for binary file, got %q", binFile.Encoding)
	}

	// The direct import also records a pollable job when jobId is supplied.
	jobResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_tarball/fs/import/github-tarball/jobs/job-direct",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_tarball_job",
		},
	})
	if jobResp.Code != http.StatusOK {
		t.Fatalf("expected 200 reading direct import job, got %d (%s)", jobResp.Code, jobResp.Body.String())
	}
	var job map[string]any
	if err := json.NewDecoder(jobResp.Body).Decode(&job); err != nil {
		t.Fatalf("decode job response: %v", err)
	}
	if job["status"] != "completed" || int(job["imported"].(float64)) != 3 {
		t.Fatalf("unexpected direct import job state: %+v", job)
	}
}

func TestGithubTarballImportValidation(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	server := NewServer(store)
	token := mustTestJWT(t, "dev-secret", "ws_tarball_validation", "Worker1", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	missingParams := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_tarball_validation/fs/import/github-tarball?owner=octo",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_missing_params",
		},
		body: []byte("ignored"),
	})
	if missingParams.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing query params, got %d (%s)", missingParams.Code, missingParams.Body.String())
	}

	traversalOwner := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_tarball_validation/fs/import/github-tarball?owner=..&repo=demo&headSha=abc123",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_traversal_owner",
		},
		body: []byte("ignored"),
	})
	if traversalOwner.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for traversal owner, got %d (%s)", traversalOwner.Code, traversalOwner.Body.String())
	}

	invalidGzip := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_tarball_validation/fs/import/github-tarball?owner=octo&repo=demo&headSha=abc123",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_invalid_gzip",
		},
		body: []byte("not a gzip stream"),
	})
	if invalidGzip.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid gzip, got %d (%s)", invalidGzip.Code, invalidGzip.Body.String())
	}
	if !strings.Contains(invalidGzip.Body.String(), "invalid_tarball") {
		t.Fatalf("expected invalid_tarball error code, got %s", invalidGzip.Body.String())
	}

	noAuth := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_tarball_validation/fs/import/github-tarball?owner=octo&repo=demo&headSha=abc123",
		headers: map[string]string{
			"X-Correlation-Id": "corr_no_auth",
		},
		body: []byte("ignored"),
	})
	if noAuth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without bearer token, got %d (%s)", noAuth.Code, noAuth.Body.String())
	}
}

func TestGithubTarballImportEnforcesAclForContentAndMarker(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	server := NewServer(store)
	ownerToken := mustTestJWT(t, "dev-secret", "ws_tarball_acl", "Owner", []string{"fs:read", "fs:write", "finance"}, time.Now().Add(time.Hour))
	limitedToken := mustTestJWT(t, "dev-secret", "ws_tarball_acl", "Limited", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	aclWrite := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_tarball_acl/fs/file?path=/github/repos/octo/demo/.relayfile.acl",
		headers: map[string]string{
			"Authorization":    "Bearer " + ownerToken,
			"X-Correlation-Id": "corr_tarball_acl_seed",
			"If-Match":         "0",
		},
		body: map[string]any{
			"contentType": "text/plain",
			"content":     "acl marker",
			"semantics": map[string]any{
				"permissions": []string{"scope:finance"},
			},
		},
	})
	if aclWrite.Code != http.StatusAccepted {
		t.Fatalf("expected acl marker write 202, got %d (%s)", aclWrite.Code, aclWrite.Body.String())
	}

	archive := buildTestGithubTarball(t, []tarballTestEntry{
		{path: "octo-demo-abc123/README.md", content: []byte("# Demo\n")},
	})
	resp := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_tarball_acl/fs/import/github-tarball?owner=octo&repo=demo&headSha=abc123&jobId=job-acl",
		headers: map[string]string{
			"Authorization":    "Bearer " + limitedToken,
			"Content-Type":     "application/gzip",
			"X-Correlation-Id": "corr_tarball_acl_import",
		},
		body: archive,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 with per-file ACL errors, got %d (%s)", resp.Code, resp.Body.String())
	}
	var payload githubTarImportResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if payload.Imported != 0 {
		t.Fatalf("expected imported=0, got %d (%+v)", payload.Imported, payload)
	}
	if payload.ErrorCount != 2 || len(payload.Errors) != 2 {
		t.Fatalf("expected content and marker ACL errors, got %+v", payload.Errors)
	}
	errorPaths := map[string]string{}
	for _, item := range payload.Errors {
		errorPaths[item.Path] = item.Code
	}
	if errorPaths["/github/repos/octo/demo/contents/README.md"] != "forbidden" {
		t.Fatalf("expected content forbidden error, got %+v", payload.Errors)
	}
	if errorPaths["/github/repos/octo/demo/.relayfile/clone.json"] != "forbidden" {
		t.Fatalf("expected marker forbidden error, got %+v", payload.Errors)
	}

	for _, path := range []string{
		"/github/repos/octo/demo/contents/README.md",
		"/github/repos/octo/demo/.relayfile/clone.json",
	} {
		readResp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/ws_tarball_acl/fs/file?path=" + path,
			headers: map[string]string{
				"Authorization":    "Bearer " + ownerToken,
				"X-Correlation-Id": "corr_tarball_acl_read",
			},
		})
		if readResp.Code != http.StatusNotFound {
			t.Fatalf("expected %s to remain unwritten, got %d (%s)", path, readResp.Code, readResp.Body.String())
		}
	}
}

func TestGithubTarballImportEmitsFileEvents(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	server := NewServer(store)
	token := mustTestJWT(t, "dev-secret", "ws_tarball_events", "Worker1", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	archive := buildTestGithubTarball(t, []tarballTestEntry{
		{path: "octo-demo-abc123/docs/guide.md", content: []byte("# Guide\n")},
	})
	resp := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_tarball_events/fs/import/github-tarball?owner=octo&repo=demo&headSha=abc123",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"Content-Type":     "application/gzip",
			"X-Correlation-Id": "corr_tarball_events",
		},
		body: archive,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 on tarball import, got %d (%s)", resp.Code, resp.Body.String())
	}

	eventsResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_tarball_events/fs/events",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_tarball_events_read",
		},
	})
	if eventsResp.Code != http.StatusOK {
		t.Fatalf("expected 200 on events feed, got %d (%s)", eventsResp.Code, eventsResp.Body.String())
	}
	var feed relayfile.EventFeed
	if err := json.NewDecoder(eventsResp.Body).Decode(&feed); err != nil {
		t.Fatalf("decode events feed: %v", err)
	}
	var contentEvent, markerEvent *relayfile.Event
	for i := range feed.Events {
		event := feed.Events[i]
		if strings.HasPrefix(event.Path, "/digests/") {
			t.Fatalf("tarball import must not write digest paths directly, saw event for %s", event.Path)
		}
		switch event.Path {
		case "/github/repos/octo/demo/contents/docs/guide.md":
			contentEvent = &feed.Events[i]
		case "/github/repos/octo/demo/.relayfile/clone.json":
			markerEvent = &feed.Events[i]
		}
	}
	if contentEvent == nil {
		t.Fatalf("expected file event for imported content, got %+v", feed.Events)
	}
	if contentEvent.Type != "file.created" {
		t.Fatalf("expected file.created event, got %q", contentEvent.Type)
	}
	if contentEvent.Provider != "github" {
		t.Fatalf("expected provider github on import event, got %q", contentEvent.Provider)
	}
	if markerEvent == nil {
		t.Fatalf("expected file event for clone marker, got %+v", feed.Events)
	}
}

// TestGithubTarballImportDigestArtifact covers the digest runtime contract:
// import events for non-digest paths feed digest regeneration and produce a
// today.md artifact, while /digests/* events are excluded so regeneration
// does not recurse.
func TestGithubTarballImportDigestArtifact(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	server := NewServer(store)
	token := mustTestJWT(t, "dev-secret", "ws_tarball_digest", "Worker1", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	archive := buildTestGithubTarball(t, []tarballTestEntry{
		{path: "octo-demo-abc123/docs/guide.md", content: []byte("# Guide\n")},
	})
	resp := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_tarball_digest/fs/import/github-tarball?owner=octo&repo=demo&headSha=abc123",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"Content-Type":     "application/gzip",
			"X-Correlation-Id": "corr_tarball_digest",
		},
		body: archive,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 on tarball import, got %d (%s)", resp.Code, resp.Body.String())
	}

	feed, err := store.GetEvents("ws_tarball_digest", "", "", 1000)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	// Simulate one digest artifact write event alongside the import events
	// and verify the regeneration source excludes it (non-recursion).
	sourceEvents := append([]relayfile.Event(nil), feed.Events...)
	sourceEvents = append(sourceEvents, relayfile.Event{
		Type:      "file.updated",
		Path:      "/digests/today.md",
		Provider:  "digests",
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	})

	var changeEvents []digest.ChangeEvent
	for _, event := range sourceEvents {
		rel := strings.TrimPrefix(event.Path, "/")
		if digest.IsDigestPath(rel) {
			continue
		}
		ts, parseErr := time.Parse(time.RFC3339Nano, event.Timestamp)
		if parseErr != nil {
			t.Fatalf("parse event timestamp: %v", parseErr)
		}
		changeEvents = append(changeEvents, digest.ChangeEvent{
			Provider:      event.Provider,
			Timestamp:     ts,
			Identifier:    rel,
			Verb:          "updated",
			CanonicalPath: rel,
		})
	}
	for _, event := range changeEvents {
		if strings.HasPrefix(event.CanonicalPath, "digests/") {
			t.Fatalf("digest source must exclude /digests/* events, got %+v", event)
		}
	}

	mountRoot := t.TempDir()
	now := time.Now().UTC()
	if _, err := digest.WriteToday(context.Background(), mountRoot, digest.SliceSource{Items: changeEvents}, []string{"github"}, now, time.UTC, digest.BuildOptions{}); err != nil {
		t.Fatalf("write today digest: %v", err)
	}
	todayBytes, err := os.ReadFile(filepath.Join(mountRoot, "digests", "today.md"))
	if err != nil {
		t.Fatalf("read today.md: %v", err)
	}
	today := string(todayBytes)
	if !strings.Contains(today, "github/repos/octo/demo/contents/docs/guide.md") {
		t.Fatalf("expected today.md to cover imported file, got:\n%s", today)
	}
	if strings.Contains(today, "digests/today.md") {
		t.Fatalf("today.md must not list digest artifacts as activity, got:\n%s", today)
	}
}

func pollGithubTarballJob(t *testing.T, server http.Handler, workspaceID, jobID, token string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/" + workspaceID + "/fs/import/github-tarball/jobs/" + jobID,
			headers: map[string]string{
				"Authorization":    "Bearer " + token,
				"X-Correlation-Id": "corr_tarball_poll",
			},
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("expected 200 polling job, got %d (%s)", resp.Code, resp.Body.String())
		}
		var job map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
			t.Fatalf("decode job poll response: %v", err)
		}
		status, _ := job["status"].(string)
		if status == "completed" || status == "failed" {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not reach a terminal status in time: %+v", jobID, job)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestGithubTarballFetchImportJobLifecycle(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	server := NewServer(store)
	token := mustTestJWT(t, "dev-secret", "ws_tarball_fetch", "Worker1", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	archive := buildTestGithubTarball(t, []tarballTestEntry{
		{path: "octo-demo-abc123/a.txt", content: []byte("a")},
		{path: "octo-demo-abc123/b.txt", content: []byte("b")},
	})
	var sawAuth string
	tarballServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(archive)
	}))
	t.Cleanup(tarballServer.Close)

	startResp := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_tarball_fetch/fs/import/github-tarball/fetch",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_tarball_fetch",
			"X-GitHub-Token":   "gh-token",
		},
		body: map[string]any{
			"owner":      "octo",
			"repo":       "demo",
			"ref":        "main",
			"headSha":    "abc123",
			"jobId":      "job-fetch-1",
			"tarballUrl": tarballServer.URL + "/repos/octo/demo/tarball/main",
		},
	})
	if startResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on fetch start, got %d (%s)", startResp.Code, startResp.Body.String())
	}
	var started map[string]any
	if err := json.NewDecoder(startResp.Body).Decode(&started); err != nil {
		t.Fatalf("decode fetch start response: %v", err)
	}
	if started["jobId"] != "job-fetch-1" {
		t.Fatalf("expected jobId job-fetch-1, got %+v", started)
	}
	status, _ := started["status"].(string)
	switch status {
	case "queued", "fetching", "importing", "completed":
	default:
		t.Fatalf("unexpected start status %q", status)
	}

	job := pollGithubTarballJob(t, server, "ws_tarball_fetch", "job-fetch-1", token)
	if job["status"] != "completed" {
		t.Fatalf("expected completed job, got %+v", job)
	}
	if int(job["imported"].(float64)) != 2 {
		t.Fatalf("expected imported=2, got %+v", job)
	}
	if int(job["errorCount"].(float64)) != 0 {
		t.Fatalf("expected errorCount=0, got %+v", job)
	}
	if sawAuth != "token gh-token" {
		t.Fatalf("expected github token auth header, got %q", sawAuth)
	}

	readResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_tarball_fetch/fs/file?path=/github/repos/octo/demo/contents/a.txt",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_tarball_fetch_read",
		},
	})
	if readResp.Code != http.StatusOK {
		t.Fatalf("expected 200 reading fetched file, got %d (%s)", readResp.Code, readResp.Body.String())
	}
}

func TestGithubTarballFetchDeduplicatesActiveJob(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	server := NewServer(store)
	token := mustTestJWT(t, "dev-secret", "ws_tarball_dedupe", "Worker1", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	archive := buildTestGithubTarball(t, []tarballTestEntry{
		{path: "octo-demo-abc123/a.txt", content: []byte("a")},
	})
	release := make(chan struct{})
	tarballServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write(archive)
	}))
	t.Cleanup(tarballServer.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	body := map[string]any{
		"owner":      "octo",
		"repo":       "demo",
		"ref":        "main",
		"headSha":    "abc123",
		"jobId":      "job-dedupe-a",
		"tarballUrl": tarballServer.URL + "/tarball",
	}
	first := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_tarball_dedupe/fs/import/github-tarball/fetch",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_dedupe_a",
			"X-GitHub-Token":   "gh-token",
		},
		body: body,
	})
	if first.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on first fetch start, got %d (%s)", first.Code, first.Body.String())
	}

	body["jobId"] = "job-dedupe-b"
	second := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_tarball_dedupe/fs/import/github-tarball/fetch",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_dedupe_b",
			"X-GitHub-Token":   "gh-token",
		},
		body: body,
	})
	if second.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on duplicate fetch start, got %d (%s)", second.Code, second.Body.String())
	}
	var duplicate map[string]any
	if err := json.NewDecoder(second.Body).Decode(&duplicate); err != nil {
		t.Fatalf("decode duplicate response: %v", err)
	}
	if duplicate["jobId"] != "job-dedupe-a" {
		t.Fatalf("expected duplicate start to return active job job-dedupe-a, got %+v", duplicate)
	}

	close(release)
	job := pollGithubTarballJob(t, server, "ws_tarball_dedupe", "job-dedupe-a", token)
	if job["status"] != "completed" {
		t.Fatalf("expected completed job after release, got %+v", job)
	}
}

func TestGithubTarballFetchValidationAndFailure(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	server := NewServer(store)
	token := mustTestJWT(t, "dev-secret", "ws_tarball_fetch_fail", "Worker1", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	missing := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_tarball_fetch_fail/fs/import/github-tarball/fetch",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_fetch_missing",
		},
		body: map[string]any{"owner": "octo", "repo": "demo"},
	})
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing fields, got %d (%s)", missing.Code, missing.Body.String())
	}

	badScheme := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_tarball_fetch_fail/fs/import/github-tarball/fetch",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_fetch_bad_scheme",
		},
		body: map[string]any{
			"owner":      "octo",
			"repo":       "demo",
			"headSha":    "abc123",
			"tarballUrl": "ftp://example.com/tarball",
		},
	})
	if badScheme.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-http tarball url, got %d (%s)", badScheme.Code, badScheme.Body.String())
	}

	tarballServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such tarball", http.StatusNotFound)
	}))
	t.Cleanup(tarballServer.Close)

	startResp := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_tarball_fetch_fail/fs/import/github-tarball/fetch",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_fetch_404",
			"X-GitHub-Token":   "gh-token",
		},
		body: map[string]any{
			"owner":      "octo",
			"repo":       "demo",
			"headSha":    "abc123",
			"jobId":      "job-fetch-404",
			"tarballUrl": tarballServer.URL + "/missing",
		},
	})
	if startResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on fetch start, got %d (%s)", startResp.Code, startResp.Body.String())
	}
	job := pollGithubTarballJob(t, server, "ws_tarball_fetch_fail", "job-fetch-404", token)
	if job["status"] != "failed" {
		t.Fatalf("expected failed job, got %+v", job)
	}
	lastError, _ := job["lastError"].(string)
	if !strings.Contains(lastError, "404") {
		t.Fatalf("expected lastError to mention upstream status, got %q", lastError)
	}

	unknown := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_tarball_fetch_fail/fs/import/github-tarball/jobs/job-unknown",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_job_unknown",
		},
	})
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown job, got %d (%s)", unknown.Code, unknown.Body.String())
	}
}
