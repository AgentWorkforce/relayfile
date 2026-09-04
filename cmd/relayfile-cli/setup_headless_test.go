package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// refusingStdin fails the run the moment setup reads standard input. A headless
// caller's stdin is an open pipe or a terminal that never delivers a line, so a
// prompt there blocks forever rather than returning EOF the way
// strings.NewReader("") does. Returning an error reproduces "setup must not ask"
// as a test failure instead of a hang.
type refusingStdin struct {
	reads atomic.Int64
}

func (r *refusingStdin) Read([]byte) (int, error) {
	r.reads.Add(1)
	return 0, errors.New("setup read stdin during a headless --skip-mount run")
}

func TestSetupSkipMountDoesNotPromptForLocalMountDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	workDir := t.TempDir()
	t.Chdir(workDir)

	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/workspaces":
			_, _ = w.Write([]byte(`{"workspaceId":"cloud-headless","relayfileUrl":"https://relayfile.test","createdAt":"2026-09-04T00:00:00Z","name":"headless"}`))
		case "GET /api/v1/workspaces/cloud-headless/resolve":
			_, _ = w.Write([]byte(`{"cloudWorkspaceId":"cloud-headless","relayfileWorkspaceId":"rw_headless","provisioned":true}`))
		case "POST /api/v1/workspaces/rw_headless/relayfile/delegated-token":
			writeDelegatedBundleResponse(t, w, "https://relayfile.test", "rw_headless", "rf_join", "refresh_join")
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	stdin := &refusingStdin{}
	var stdout bytes.Buffer
	// No --local-dir: the flag the headless contract does not require is
	// exactly the one that used to trigger the blocking prompt.
	err := run([]string{
		"setup",
		"--cloud-api-url", server.URL,
		"--cloud-token", "cld_test",
		"--workspace", "headless",
		"--provider", "none",
		"--no-open",
		"--skip-mount",
	}, stdin, &stdout, &stdout)
	if err != nil {
		t.Fatalf("headless setup failed: %v\noutput:\n%s", err, stdout.String())
	}
	if reads := stdin.reads.Load(); reads != 0 {
		t.Fatalf("headless setup read stdin %d times; it must not prompt", reads)
	}
	if strings.Contains(stdout.String(), "Local mount directory") {
		t.Fatalf("headless setup printed the local mount directory prompt:\n%s", stdout.String())
	}
	wantHint := "relayfile mount cloud-headless " + defaultSetupLocalDir
	if !strings.Contains(stdout.String(), wantHint) {
		t.Fatalf("setup output does not name the mount command %q:\n%s", wantHint, stdout.String())
	}
	mountDir := filepath.Join(workDir, filepath.Base(defaultSetupLocalDir))
	if info, statErr := os.Stat(mountDir); statErr != nil || !info.IsDir() {
		t.Fatalf("setup did not prepare the default mount directory %s: err=%v", mountDir, statErr)
	}
	wantRequests := []string{
		"POST /api/v1/workspaces",
		"GET /api/v1/workspaces/cloud-headless/resolve",
		"POST /api/v1/workspaces/rw_headless/relayfile/delegated-token",
	}
	if !slices.Equal(requests, wantRequests) {
		t.Fatalf("headless setup request order = %v, want %v", requests, wantRequests)
	}
}

// TestSetupNamesUnboundCloudWorkspaceOnResolve pins the production shape of
// GET /api/v1/workspaces/{rw_*}/resolve for a workspace the CLI just created:
// Cloud returns the Relayfile workspace id but a null cloudWorkspaceId, because
// nothing binds a CLI-created rw_ workspace to a Cloud workspace. Setup must
// stop there and say so — the delegated-token call that would follow authorizes
// through that same binding and answers an opaque 404.
func TestSetupNamesUnboundCloudWorkspaceOnResolve(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Chdir(t.TempDir())

	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/workspaces":
			_, _ = w.Write([]byte(`{"workspaceId":"rw_1234abcd","relayfileUrl":"https://relayfile.test","createdAt":"2026-09-04T00:00:00Z","name":"unbound"}`))
		case "GET /api/v1/workspaces/rw_1234abcd/resolve":
			_, _ = w.Write([]byte(`{"workspaceId":"rw_1234abcd","cloudWorkspaceId":null,"relayfileWorkspaceId":"rw_1234abcd","provisioned":false}`))
		default:
			t.Errorf("setup continued past the unbound workspace: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run([]string{
		"setup",
		"--cloud-api-url", server.URL,
		"--cloud-token", "cld_test",
		"--workspace", "unbound",
		"--provider", "none",
		"--local-dir", t.TempDir(),
		"--no-open",
		"--skip-mount",
	}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("setup succeeded against an unbound Cloud workspace:\n%s", stdout.String())
	}
	for _, want := range []string{"cloudWorkspaceId", "not bound", "rw_1234abcd"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("setup error %q does not name %q", err, want)
		}
	}
	wantRequests := []string{
		"POST /api/v1/workspaces",
		"GET /api/v1/workspaces/rw_1234abcd/resolve",
	}
	if !slices.Equal(requests, wantRequests) {
		t.Fatalf("unbound setup request order = %v, want %v", requests, wantRequests)
	}
}

func TestResolveCloudWorkspaceForRelayfileSeparatesUnprovisionedFromUnbound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	for _, testCase := range []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "missing relayfile workspace is unprovisioned",
			body:    `{"cloudWorkspaceId":"cloud-1","relayfileWorkspaceId":"","provisioned":false}`,
			wantErr: "provisioning did not complete",
		},
		{
			name:    "missing cloud workspace is unbound",
			body:    `{"cloudWorkspaceId":null,"relayfileWorkspaceId":"rw_1234abcd","provisioned":false}`,
			wantErr: "not bound to a Cloud workspace",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, testCase.body)
			}))
			defer server.Close()

			_, err := resolveCloudWorkspaceForRelayfile(
				cloudCredentials{APIURL: server.URL, AccessToken: "cld_test"},
				"rw_1234abcd",
			)
			if err == nil {
				t.Fatal("expected resolve to fail")
			}
			if !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("resolve error %q does not contain %q", err, testCase.wantErr)
			}
		})
	}
}
