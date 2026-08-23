package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEnsureSetupCloudCredentialsLogsInWithoutAgentRelayCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Setenv("PATH", t.TempDir())

	var cloud *httptest.Server
	cloud = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/cli/login":
			callback, err := url.Parse(r.URL.Query().Get("redirect_uri"))
			if err != nil {
				t.Errorf("parse callback URL: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			query := callback.Query()
			query.Set("state", r.URL.Query().Get("state"))
			query.Set("access_token", "cld_at_setup_secret")
			query.Set("refresh_token", "cld_rt_setup_secret")
			query.Set("access_token_expires_at", time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
			query.Set("refresh_token_expires_at", time.Now().Add(30*24*time.Hour).UTC().Format(time.RFC3339))
			query.Set("api_url", cloud.URL)
			callback.RawQuery = query.Encode()
			http.Redirect(w, r, callback.String(), http.StatusFound)
		case "/cli/auth-result":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "Signed in")
		default:
			t.Errorf("unexpected cloud path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer cloud.Close()

	opened := make(chan string, 1)
	browserResult := make(chan error, 1)
	previousOpener := cloudLoginBrowserOpener
	cloudLoginBrowserOpener = func(target string) error {
		opened <- target
		go func() {
			response, err := http.Get(target)
			if response != nil {
				_ = response.Body.Close()
			}
			browserResult <- err
		}()
		return nil
	}
	t.Cleanup(func() { cloudLoginBrowserOpener = previousOpener })

	var stdout bytes.Buffer
	creds, err := ensureSetupCloudCredentials(cloud.URL, "", 2*time.Second, true, &stdout)
	if err != nil {
		t.Fatalf("ensure setup credentials: %v\noutput:\n%s", err, stdout.String())
	}
	if creds.APIURL != cloud.URL || creds.AccessToken != "cld_at_setup_secret" {
		t.Fatalf("unexpected setup credentials: %#v", creds)
	}
	select {
	case loginURL := <-opened:
		if !strings.HasPrefix(loginURL, cloud.URL+"/api/v1/cli/login?") {
			t.Fatalf("unexpected login URL: %s", loginURL)
		}
	case <-time.After(time.Second):
		t.Fatal("browser login was not opened")
	}
	select {
	case browserErr := <-browserResult:
		if browserErr != nil {
			t.Fatalf("browser login request failed: %v", browserErr)
		}
	case <-time.After(time.Second):
		t.Fatal("browser login request did not complete")
	}

	auth, err := readAgentRelayStoredAuthFile()
	if err != nil {
		t.Fatalf("read canonical cloud auth: %v", err)
	}
	if auth.AccessToken != "cld_at_setup_secret" || auth.RefreshToken != "cld_rt_setup_secret" {
		t.Fatalf("unexpected persisted cloud auth: %#v", auth)
	}
	if output := stdout.String(); strings.Contains(output, auth.AccessToken) || strings.Contains(output, auth.RefreshToken) {
		t.Fatalf("setup output leaked a cloud credential: %q", output)
	}

	cloudLoginBrowserOpener = func(string) error {
		t.Fatal("a valid stored login must be reused without opening a browser")
		return nil
	}
	reused, err := ensureSetupCloudCredentials(cloud.URL, "", 2*time.Second, true, io.Discard)
	if err != nil {
		t.Fatalf("reuse setup credentials: %v", err)
	}
	if reused.AccessToken != auth.AccessToken {
		t.Fatalf("reused access token = %q, want stored token", reused.AccessToken)
	}
}

func TestEnsureSetupCloudCredentialsKeepsExplicitTokenNonInteractive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	previousOpener := cloudLoginBrowserOpener
	cloudLoginBrowserOpener = func(string) error {
		t.Fatal("an explicit token must not start browser login")
		return nil
	}
	t.Cleanup(func() { cloudLoginBrowserOpener = previousOpener })

	creds, err := ensureSetupCloudCredentials("https://cloud.example", "cld_explicit", time.Second, true, io.Discard)
	if err != nil {
		t.Fatalf("ensure explicit setup credentials: %v", err)
	}
	if creds.AccessToken != "cld_explicit" {
		t.Fatalf("explicit token was not preserved: %#v", creds)
	}
	if _, err := os.Stat(mustAgentRelayCloudAuthPath(t)); !os.IsNotExist(err) {
		t.Fatalf("explicit token unexpectedly created a stored login: %v", err)
	}
}
