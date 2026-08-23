package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var cloudLoginBrowserOpener = openBrowser

type cloudBrowserLoginResult struct {
	auth agentRelayStoredAuth
	err  error
}

// ensureSetupCloudCredentials is the clean-machine authentication path used by
// `relayfile setup`. Existing Agent Relay sessions and explicit CI tokens keep
// their current precedence. When neither exists, Relayfile owns the localhost
// browser callback itself so `npx relayfile@latest` does not require a separate
// agent-relay CLI installation.
func ensureSetupCloudCredentials(
	cloudAPIURL string,
	explicitToken string,
	timeout time.Duration,
	shouldOpenBrowser bool,
	stdout io.Writer,
) (cloudCredentials, error) {
	creds, err := ensureCloudCredentials(cloudAPIURL, explicitToken, timeout, shouldOpenBrowser, stdout)
	if err == nil || strings.TrimSpace(explicitToken) != "" {
		return creds, err
	}
	if !errors.Is(err, ErrCloudRefreshExpired) {
		return cloudCredentials{}, err
	}
	// Environment-backed credentials belong to their caller. If they are
	// unusable, do not silently replace them with an interactive file-backed
	// session that the same environment would continue to shadow.
	if strings.TrimSpace(os.Getenv("CLOUD_API_ACCESS_TOKEN")) != "" {
		return cloudCredentials{}, err
	}

	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	auth, loginErr := loginToAgentRelayCloud(ctx, cloudAPIURL, shouldOpenBrowser, stdout)
	if loginErr != nil {
		return cloudCredentials{}, loginErr
	}
	return cloudCredentials{
		APIURL:               strings.TrimRight(auth.APIURL, "/"),
		AccessToken:          auth.AccessToken,
		AccessTokenExpiresAt: auth.AccessTokenExpiresAt,
		UpdatedAt:            time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func loginToAgentRelayCloud(
	ctx context.Context,
	cloudAPIURL string,
	shouldOpenBrowser bool,
	stdout io.Writer,
) (agentRelayStoredAuth, error) {
	if stdout == nil {
		stdout = io.Discard
	}
	cloudAPI, err := buildCloudURL(cloudAPIURL, "")
	if err != nil {
		return agentRelayStoredAuth{}, err
	}
	cloudAPI.RawQuery = ""
	cloudAPI.Fragment = ""
	canonicalCloudAPI := strings.TrimRight(cloudAPI.String(), "/")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return agentRelayStoredAuth{}, fmt.Errorf("start Relayfile cloud login callback: %w", err)
	}
	defer listener.Close()

	state, err := randomURLSafe(32)
	if err != nil {
		return agentRelayStoredAuth{}, fmt.Errorf("create Relayfile cloud login state: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	callbackURL := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		Path:   "/callback",
	}
	loginURL, err := buildCloudURL(canonicalCloudAPI, "api/v1/cli/login")
	if err != nil {
		return agentRelayStoredAuth{}, err
	}
	query := loginURL.Query()
	query.Set("redirect_uri", callbackURL.String())
	query.Set("state", state)
	loginURL.RawQuery = query.Encode()

	result := make(chan cloudBrowserLoginResult, 1)
	var settle sync.Once
	finish := func(value cloudBrowserLoginResult) {
		settle.Do(func() { result <- value })
	}

	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/callback" {
			http.NotFound(response, request)
			return
		}
		returnedState := request.URL.Query().Get("state")
		if subtle.ConstantTimeCompare([]byte(returnedState), []byte(state)) != 1 {
			http.Error(response, "Ignored invalid CLI login callback. Return to your terminal to continue login.", http.StatusBadRequest)
			return
		}
		if callbackError := strings.TrimSpace(request.URL.Query().Get("error")); callbackError != "" {
			redirectCloudLoginResult(response, request, canonicalCloudAPI, "error", callbackError)
			finish(cloudBrowserLoginResult{err: fmt.Errorf("Relayfile cloud login failed: %s", callbackError)})
			return
		}

		auth := agentRelayStoredAuth{
			APIURL:                firstNonEmpty(strings.TrimRight(strings.TrimSpace(request.URL.Query().Get("api_url")), "/"), canonicalCloudAPI),
			AccessToken:           strings.TrimSpace(request.URL.Query().Get("access_token")),
			RefreshToken:          strings.TrimSpace(request.URL.Query().Get("refresh_token")),
			AccessTokenExpiresAt:  strings.TrimSpace(request.URL.Query().Get("access_token_expires_at")),
			RefreshTokenExpiresAt: strings.TrimSpace(request.URL.Query().Get("refresh_token_expires_at")),
		}
		if !auth.valid() {
			const detail = "The CLI login callback was missing a valid API URL or token expiration set."
			redirectCloudLoginResult(response, request, canonicalCloudAPI, "error", detail)
			finish(cloudBrowserLoginResult{err: errors.New(detail)})
			return
		}
		returnedAPI, parseErr := buildCloudURL(auth.APIURL, "")
		if parseErr != nil || (returnedAPI.Scheme != "http" && returnedAPI.Scheme != "https") {
			const detail = "The CLI login callback returned an invalid API URL."
			redirectCloudLoginResult(response, request, canonicalCloudAPI, "error", detail)
			finish(cloudBrowserLoginResult{err: errors.New(detail)})
			return
		}

		redirectCloudLoginResult(response, request, auth.APIURL, "success", "You can return to your terminal.")
		finish(cloudBrowserLoginResult{auth: auth})
	})
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(stdout, "Sign in to Relayfile Cloud:\n  %s\n", loginURL.String())
	if shouldOpenBrowser {
		if err := cloudLoginBrowserOpener(loginURL.String()); err != nil {
			fmt.Fprintf(stdout, "Could not open a browser automatically: %v\nPaste the URL above into your browser.\n", err)
		}
	} else {
		fmt.Fprintln(stdout, "Open the URL above in a browser to continue.")
	}

	var completed cloudBrowserLoginResult
	select {
	case completed = <-result:
	case err := <-serveErr:
		return agentRelayStoredAuth{}, fmt.Errorf("serve Relayfile cloud login callback: %w", err)
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return agentRelayStoredAuth{}, errors.New("timed out waiting for Relayfile cloud login")
		}
		return agentRelayStoredAuth{}, ctx.Err()
	}
	if completed.err != nil {
		return agentRelayStoredAuth{}, completed.err
	}

	release, err := acquireAgentRelayAuthLock(ctx)
	if err != nil {
		return agentRelayStoredAuth{}, fmt.Errorf("lock the Relayfile cloud session: %w", err)
	}
	defer release()
	if err := writeAgentRelayStoredAuthFile(completed.auth); err != nil {
		return agentRelayStoredAuth{}, fmt.Errorf("persist the Relayfile cloud session: %w", err)
	}
	fmt.Fprintln(stdout, "Relayfile Cloud sign-in complete.")
	return completed.auth, nil
}

func redirectCloudLoginResult(
	response http.ResponseWriter,
	request *http.Request,
	cloudAPIURL string,
	status string,
	detail string,
) {
	resultURL, err := buildCloudURL(cloudAPIURL, "cli/auth-result")
	if err != nil {
		http.Error(response, detail, http.StatusBadRequest)
		return
	}
	query := resultURL.Query()
	query.Set("status", status)
	query.Set("detail", detail)
	resultURL.RawQuery = query.Encode()
	http.Redirect(response, request, resultURL.String(), http.StatusFound)
}
