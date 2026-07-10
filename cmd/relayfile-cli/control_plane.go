package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const relayfileControlPlaneAPIVersion uint32 = 3

var relayfileControlPlaneSupportedVersions = []uint32{1, 2, relayfileControlPlaneAPIVersion}

type controlPlaneErrorCode string

const (
	controlPlaneErrInvalidArgument     controlPlaneErrorCode = "INVALID_ARGUMENT"
	controlPlaneErrVersionIncompatible controlPlaneErrorCode = "VERSION_INCOMPATIBLE"
	controlPlaneErrResourceUnresolved  controlPlaneErrorCode = "RESOURCE_UNRESOLVED"
	controlPlaneErrBindingNotFound     controlPlaneErrorCode = "BINDING_NOT_FOUND"
	controlPlaneErrDaemonUnavailable   controlPlaneErrorCode = "DAEMON_UNAVAILABLE"
)

type controlPlaneError struct {
	Code    controlPlaneErrorCode `json:"code"`
	Message string                `json:"message"`
}

type helloRequest struct {
	APIVersion uint32 `json:"apiVersion,omitempty"`
}

type helloResponse struct {
	DaemonVersion        string   `json:"daemonVersion"`
	APIVersion           uint32   `json:"apiVersion"`
	SupportedAPIVersions []uint32 `json:"supportedApiVersions"`
}

type listProvidersResponse struct {
	Providers []integrationCatalogEntry `json:"providers"`
}

type providerStatusRequest struct {
	Provider    string `json:"provider,omitempty"`
	Workspace   string `json:"workspace,omitempty"`
	CloudAPIURL string `json:"cloudApiUrl,omitempty"`
}

type connectProviderRequest struct {
	Provider    string `json:"provider"`
	Workspace   string `json:"workspace,omitempty"`
	CloudAPIURL string `json:"cloudApiUrl,omitempty"`
	Backend     string `json:"backend,omitempty"`
	NoOpen      bool   `json:"noOpen,omitempty"`
	Timeout     string `json:"timeout,omitempty"`
	WaitSync    bool   `json:"waitSync,omitempty"`
}

type connectProviderResponse struct {
	Provider string `json:"provider"`
	Output   string `json:"output,omitempty"`
}

type resolveResourcePathRequest struct {
	Provider string `json:"provider"`
	Resource string `json:"resource"`
}

type resolveResourcePathResponse struct {
	Provider      string `json:"provider"`
	Resource      string `json:"resource"`
	PathGlob      string `json:"pathGlob"`
	ResolvedExact bool   `json:"resolvedExact"`
	Warning       string `json:"warning,omitempty"`
}

type bindRequest struct {
	Provider              string `json:"provider"`
	Resource              string `json:"resource"`
	Channel               string `json:"channel"`
	WebhookID             string `json:"webhookId"`
	WebhookToken          string `json:"webhookToken"`
	SubscriptionID        string `json:"subscriptionId,omitempty"`
	WebhookSubscriptionID string `json:"webhookSubscriptionId,omitempty"`
}

type bindResponse struct {
	Binding  relayIntegrationBinding `json:"binding"`
	Replaced bool                    `json:"replaced"`
	Warning  string                  `json:"warning,omitempty"`
}

type listBindingsResponse struct {
	Bindings []relayIntegrationBinding `json:"bindings"`
}

type unbindRequest struct {
	Provider string `json:"provider"`
	Resource string `json:"resource,omitempty"`
}

type writebackSecretRequest struct {
	Workspace string `json:"workspace,omitempty"`
	Channel   string `json:"channel"`
}

type writebackSecretData struct {
	URL    string `json:"url"`
	Secret string `json:"secret"`
}

type webhookSubscriptionRequest struct {
	Workspace string   `json:"workspace,omitempty"`
	URL       string   `json:"url"`
	PathGlobs []string `json:"pathGlobs"`
	Secret    string   `json:"secret"`
}

type webhookSubscriptionResponse struct {
	SubscriptionID string `json:"subscriptionId"`
	Secret         string `json:"secret,omitempty"`
}

type deleteWebhookSubscriptionRequest struct {
	Workspace      string `json:"workspace,omitempty"`
	SubscriptionID string `json:"subscriptionId"`
}

func runControlPlane(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("control-plane subcommand is required: serve")
	}
	switch args[0] {
	case "serve":
		return runControlPlaneServe(args[1:], stdout)
	default:
		return fmt.Errorf("unknown control-plane subcommand %q", args[0])
	}
}

func runControlPlaneServe(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("control-plane serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("sock", defaultRelayfileSocketPath(), "unix socket path")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{"sock": true})); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: relayfile control-plane serve [--sock PATH]")
	}
	return serveControlPlaneSocket(strings.TrimSpace(*sock), stdout)
}

func serveControlPlaneSocket(sock string, stdout io.Writer) error {
	if sock == "" {
		sock = defaultRelayfileSocketPath()
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return err
	}
	_ = os.Remove(sock)
	listener, err := listenControlPlaneSocket(sock)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(sock)
	if err := os.Chmod(sock, 0o600); err != nil {
		return err
	}

	server := &http.Server{
		Handler:           newControlPlaneHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	fmt.Fprintf(stdout, "relayfile control-plane listening on %s\n", sock)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case sig := <-sigCh:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return err
		}
		if sig != os.Interrupt && sig != syscall.SIGTERM {
			return fmt.Errorf("control-plane stopped by signal %s", sig)
		}
		return nil
	case err := <-errCh:
		return err
	}
}

func newControlPlaneHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/hello", handleControlPlaneHello)
	mux.HandleFunc("/v1/integrations/providers", withControlPlaneVersion(handleControlPlaneListProviders))
	mux.HandleFunc("/v1/integrations/provider-status", withControlPlaneVersion(handleControlPlaneProviderStatus))
	mux.HandleFunc("/v1/integrations/connect", withControlPlaneVersion(handleControlPlaneConnectProvider))
	mux.HandleFunc("/v1/integrations/resolve-path", withControlPlaneVersion(handleControlPlaneResolvePath))
	mux.HandleFunc("/v1/integrations/bind", withControlPlaneVersion(handleControlPlaneBind))
	mux.HandleFunc("/v1/integrations/bindings", withControlPlaneVersion(handleControlPlaneListBindings))
	mux.HandleFunc("/v1/integrations/unbind", withControlPlaneVersion(handleControlPlaneUnbind))
	mux.HandleFunc("/v1/integrations/writeback-secret", withControlPlaneVersion(handleControlPlaneWritebackSecret))
	mux.HandleFunc("/v1/integrations/webhook-subscriptions", withControlPlaneVersion(handleControlPlaneWebhookSubscription))
	return mux
}

func withControlPlaneVersion(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := checkControlPlaneVersion(r); err != nil {
			writeControlPlaneError(w, http.StatusUpgradeRequired, controlPlaneErrVersionIncompatible, err.Error())
			return
		}
		next(w, r)
	}
}

func handleControlPlaneHello(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeControlPlaneError(w, http.StatusMethodNotAllowed, controlPlaneErrInvalidArgument, "method not allowed")
		return
	}
	var requested uint32
	if r.Method == http.MethodPost && r.Body != nil {
		var req helloRequest
		if err := decodeControlPlaneJSON(r, &req); err != nil {
			writeControlPlaneError(w, http.StatusBadRequest, controlPlaneErrInvalidArgument, err.Error())
			return
		}
		requested = req.APIVersion
	}
	if requested == 0 {
		requested = controlPlaneVersionFromRequest(r)
	}
	if requested != 0 && !controlPlaneSupportsVersion(requested) {
		writeControlPlaneError(
			w,
			http.StatusUpgradeRequired,
			controlPlaneErrVersionIncompatible,
			fmt.Sprintf("relayfile daemon speaks API v%d; this client needs v%d. Upgrade relayfile (or agent-relay).", relayfileControlPlaneAPIVersion, requested),
		)
		return
	}
	writeControlPlaneJSON(w, http.StatusOK, controlPlaneHello())
}

func handleControlPlaneListProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeControlPlaneError(w, http.StatusMethodNotAllowed, controlPlaneErrInvalidArgument, "method not allowed")
		return
	}
	writeControlPlaneJSON(w, http.StatusOK, listProvidersResponse{Providers: fallbackIntegrationCatalog()})
}

func handleControlPlaneProviderStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeControlPlaneError(w, http.StatusMethodNotAllowed, controlPlaneErrInvalidArgument, "method not allowed")
		return
	}
	req := providerStatusRequest{
		Provider:    r.URL.Query().Get("provider"),
		Workspace:   r.URL.Query().Get("workspace"),
		CloudAPIURL: r.URL.Query().Get("cloudApiUrl"),
	}
	if r.Method == http.MethodPost {
		if err := decodeControlPlaneJSON(r, &req); err != nil {
			writeControlPlaneError(w, http.StatusBadRequest, controlPlaneErrInvalidArgument, err.Error())
			return
		}
	}
	provider := normalizeProviderID(req.Provider)
	if provider == "" {
		writeControlPlaneError(w, http.StatusBadRequest, controlPlaneErrInvalidArgument, "provider is required")
		return
	}
	entries, err := controlPlaneListIntegrationConnections(req.Workspace, req.CloudAPIURL)
	if err != nil {
		writeControlPlaneMappedError(w, err)
		return
	}
	for _, entry := range entries {
		if normalizeProviderID(entry.Provider) == provider {
			writeControlPlaneJSON(w, http.StatusOK, entry)
			return
		}
	}
	writeControlPlaneError(w, http.StatusNotFound, controlPlaneErrBindingNotFound, fmt.Sprintf("provider %q is not connected", provider))
}

func handleControlPlaneConnectProvider(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeControlPlaneError(w, http.StatusMethodNotAllowed, controlPlaneErrInvalidArgument, "method not allowed")
		return
	}
	var req connectProviderRequest
	if err := decodeControlPlaneJSON(r, &req); err != nil {
		writeControlPlaneError(w, http.StatusBadRequest, controlPlaneErrInvalidArgument, err.Error())
		return
	}
	provider := normalizeProviderID(req.Provider)
	if provider == "" {
		writeControlPlaneError(w, http.StatusBadRequest, controlPlaneErrInvalidArgument, "provider is required")
		return
	}
	args := []string{provider}
	if strings.TrimSpace(req.Workspace) != "" {
		args = append(args, "--workspace", strings.TrimSpace(req.Workspace))
	}
	if strings.TrimSpace(req.CloudAPIURL) != "" {
		args = append(args, "--cloud-api-url", strings.TrimSpace(req.CloudAPIURL))
	}
	if strings.TrimSpace(req.Backend) != "" {
		args = append(args, "--backend", strings.TrimSpace(req.Backend))
	}
	if req.NoOpen {
		args = append(args, "--no-open")
	}
	if strings.TrimSpace(req.Timeout) != "" {
		args = append(args, "--timeout", strings.TrimSpace(req.Timeout))
	}
	if req.WaitSync {
		args = append(args, "--wait-sync")
	}
	var out bytes.Buffer
	if err := runIntegrationConnect(args, strings.NewReader(""), &out); err != nil {
		writeControlPlaneMappedError(w, err)
		return
	}
	writeControlPlaneJSON(w, http.StatusOK, connectProviderResponse{Provider: provider, Output: out.String()})
}

func handleControlPlaneResolvePath(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeControlPlaneError(w, http.StatusMethodNotAllowed, controlPlaneErrInvalidArgument, "method not allowed")
		return
	}
	var req resolveResourcePathRequest
	if err := decodeControlPlaneJSON(r, &req); err != nil {
		writeControlPlaneError(w, http.StatusBadRequest, controlPlaneErrInvalidArgument, err.Error())
		return
	}
	provider := normalizeProviderID(req.Provider)
	if err := validateLocalProviderID(provider); err != nil {
		writeControlPlaneError(w, http.StatusBadRequest, controlPlaneErrInvalidArgument, err.Error())
		return
	}
	resolved, err := resolveIntegrationBindPathGlob(provider, req.Resource)
	if err != nil {
		writeControlPlaneMappedError(w, err)
		return
	}
	writeControlPlaneJSON(w, http.StatusOK, resolveResourcePathResponse{
		Provider:      provider,
		Resource:      strings.TrimSpace(req.Resource),
		PathGlob:      resolved.PathGlob,
		ResolvedExact: resolved.Warning == "",
		Warning:       resolved.Warning,
	})
}

func handleControlPlaneBind(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeControlPlaneError(w, http.StatusMethodNotAllowed, controlPlaneErrInvalidArgument, "method not allowed")
		return
	}
	var req bindRequest
	if err := decodeControlPlaneJSON(r, &req); err != nil {
		writeControlPlaneError(w, http.StatusBadRequest, controlPlaneErrInvalidArgument, err.Error())
		return
	}
	binding, replaced, warning, err := bindRelayIntegration(relayIntegrationBindInput{
		Provider:              req.Provider,
		Resource:              req.Resource,
		Channel:               req.Channel,
		WebhookID:             req.WebhookID,
		WebhookToken:          req.WebhookToken,
		SubscriptionID:        req.SubscriptionID,
		WebhookSubscriptionID: req.WebhookSubscriptionID,
	})
	if err != nil {
		writeControlPlaneMappedError(w, err)
		return
	}
	writeControlPlaneJSON(w, http.StatusOK, bindResponse{Binding: binding, Replaced: replaced, Warning: warning})
}

func handleControlPlaneListBindings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeControlPlaneError(w, http.StatusMethodNotAllowed, controlPlaneErrInvalidArgument, "method not allowed")
		return
	}
	bindings, err := listRelayIntegrationBindings()
	if err != nil {
		writeControlPlaneMappedError(w, err)
		return
	}
	writeControlPlaneJSON(w, http.StatusOK, listBindingsResponse{Bindings: bindings})
}

func handleControlPlaneUnbind(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeControlPlaneError(w, http.StatusMethodNotAllowed, controlPlaneErrInvalidArgument, "method not allowed")
		return
	}
	var req unbindRequest
	if err := decodeControlPlaneJSON(r, &req); err != nil {
		writeControlPlaneError(w, http.StatusBadRequest, controlPlaneErrInvalidArgument, err.Error())
		return
	}
	result, err := unbindRelayIntegration(req.Provider, req.Resource)
	if err != nil {
		writeControlPlaneMappedError(w, err)
		return
	}
	writeControlPlaneJSON(w, http.StatusOK, result)
}

func handleControlPlaneWritebackSecret(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeControlPlaneError(w, http.StatusMethodNotAllowed, controlPlaneErrInvalidArgument, "method not allowed")
		return
	}
	var req writebackSecretRequest
	if err := decodeControlPlaneJSON(r, &req); err != nil {
		writeControlPlaneError(w, http.StatusBadRequest, controlPlaneErrInvalidArgument, err.Error())
		return
	}
	args := []string{"--channel", strings.TrimSpace(req.Channel), "--json"}
	if strings.TrimSpace(req.Workspace) != "" {
		args = append(args, "--workspace", strings.TrimSpace(req.Workspace))
	}
	var out bytes.Buffer
	if err := runIntegrationWritebackSecret(args, &out); err != nil {
		writeControlPlaneMappedError(w, err)
		return
	}
	var data writebackSecretData
	if err := json.Unmarshal(out.Bytes(), &data); err != nil {
		writeControlPlaneMappedError(w, fmt.Errorf("parse writeback secret response: %w", err))
		return
	}
	writeControlPlaneJSON(w, http.StatusOK, data)
}

func handleControlPlaneWebhookSubscription(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var req webhookSubscriptionRequest
		if err := decodeControlPlaneJSON(r, &req); err != nil {
			writeControlPlaneError(w, http.StatusBadRequest, controlPlaneErrInvalidArgument, err.Error())
			return
		}
		if strings.TrimSpace(req.URL) == "" || len(req.PathGlobs) == 0 || strings.TrimSpace(req.Secret) == "" {
			writeControlPlaneError(w, http.StatusBadRequest, controlPlaneErrInvalidArgument, "url, pathGlobs, and secret are required")
			return
		}
		commandClient, err := prepareWorkspaceCommandClient(strings.TrimSpace(req.Workspace), "", "", defaultJoinScopes)
		if err != nil {
			writeControlPlaneMappedError(w, err)
			return
		}
		workspaceID := strings.TrimSpace(commandClient.workspaceID)
		if workspaceID == "" {
			writeControlPlaneMappedError(w, errors.New("could not resolve relayfile workspace id"))
			return
		}
		var response webhookSubscriptionResponse
		if err := commandClient.client.postJSON(
			r.Context(),
			fmt.Sprintf("/v1/workspaces/%s/webhooks", url.PathEscape(workspaceID)),
			map[string]any{
				"url":       strings.TrimSpace(req.URL),
				"pathGlobs": req.PathGlobs,
				"secret":    strings.TrimSpace(req.Secret),
			},
			&response,
		); err != nil {
			writeControlPlaneMappedError(w, err)
			return
		}
		writeControlPlaneJSON(w, http.StatusOK, response)
	case http.MethodDelete:
		var req deleteWebhookSubscriptionRequest
		if err := decodeControlPlaneJSON(r, &req); err != nil {
			writeControlPlaneError(w, http.StatusBadRequest, controlPlaneErrInvalidArgument, err.Error())
			return
		}
		subscriptionID := strings.TrimSpace(req.SubscriptionID)
		if subscriptionID == "" {
			writeControlPlaneError(w, http.StatusBadRequest, controlPlaneErrInvalidArgument, "subscriptionId is required")
			return
		}
		commandClient, err := prepareWorkspaceCommandClient(strings.TrimSpace(req.Workspace), "", "", defaultJoinScopes)
		if err != nil {
			writeControlPlaneMappedError(w, err)
			return
		}
		workspaceID := strings.TrimSpace(commandClient.workspaceID)
		if workspaceID == "" {
			writeControlPlaneMappedError(w, errors.New("could not resolve relayfile workspace id"))
			return
		}
		if err := commandClient.client.deleteJSON(
			r.Context(),
			fmt.Sprintf("/v1/workspaces/%s/webhooks/%s", url.PathEscape(workspaceID), url.PathEscape(subscriptionID)),
			"",
			nil,
		); err != nil {
			writeControlPlaneMappedError(w, err)
			return
		}
		writeControlPlaneJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeControlPlaneError(w, http.StatusMethodNotAllowed, controlPlaneErrInvalidArgument, "method not allowed")
	}
}

func controlPlaneHello() helloResponse {
	return helloResponse{
		DaemonVersion:        relayfileVersion,
		APIVersion:           relayfileControlPlaneAPIVersion,
		SupportedAPIVersions: append([]uint32(nil), relayfileControlPlaneSupportedVersions...),
	}
}

func checkControlPlaneVersion(r *http.Request) error {
	requested := controlPlaneVersionFromRequest(r)
	if requested == 0 {
		return nil
	}
	if !controlPlaneSupportsVersion(requested) {
		return fmt.Errorf("relayfile daemon speaks API v%d; this client needs v%d. Upgrade relayfile (or agent-relay).", relayfileControlPlaneAPIVersion, requested)
	}
	return nil
}

func controlPlaneVersionFromRequest(r *http.Request) uint32 {
	raw := strings.TrimSpace(r.Header.Get("X-Relayfile-API-Version"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("apiVersion"))
	}
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return ^uint32(0)
	}
	return uint32(value)
}

func controlPlaneSupportsVersion(version uint32) bool {
	for _, supported := range relayfileControlPlaneSupportedVersions {
		if supported == version {
			return true
		}
	}
	return false
}

func controlPlaneListIntegrationConnections(workspace, cloudAPIURL string) ([]cloudIntegrationListEntry, error) {
	args := []string{"--json"}
	if strings.TrimSpace(workspace) != "" {
		args = append(args, "--workspace", strings.TrimSpace(workspace))
	}
	if strings.TrimSpace(cloudAPIURL) != "" {
		args = append(args, "--cloud-api-url", strings.TrimSpace(cloudAPIURL))
	}
	var out bytes.Buffer
	if err := runIntegrationList(args, &out); err != nil {
		return nil, err
	}
	var entries []cloudIntegrationListEntry
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		return nil, fmt.Errorf("parse integration list response: %w", err)
	}
	return entries, nil
}

func decodeControlPlaneJSON(r *http.Request, out any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	return nil
}

func writeControlPlaneMappedError(w http.ResponseWriter, err error) {
	message := err.Error()
	status := http.StatusBadRequest
	code := controlPlaneErrInvalidArgument
	switch {
	case strings.Contains(message, "no binding found"):
		status = http.StatusNotFound
		code = controlPlaneErrBindingNotFound
	case strings.Contains(message, "PATH_GLOB") || strings.Contains(message, "resource"):
		status = http.StatusUnprocessableEntity
		code = controlPlaneErrResourceUnresolved
	case strings.Contains(message, "connection refused") || strings.Contains(message, "credentials not found") || strings.Contains(message, "agent-relay"):
		status = http.StatusServiceUnavailable
		code = controlPlaneErrDaemonUnavailable
	}
	writeControlPlaneError(w, status, code, message)
}

func writeControlPlaneError(w http.ResponseWriter, status int, code controlPlaneErrorCode, message string) {
	writeControlPlaneJSON(w, status, map[string]controlPlaneError{
		"error": {
			Code:    code,
			Message: message,
		},
	})
}

func writeControlPlaneJSON(w http.ResponseWriter, status int, value any) {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		status = http.StatusInternalServerError
		payload = []byte(`{"error":{"code":"DAEMON_UNAVAILABLE","message":"failed to encode response"}}`)
	}
	payload = append(payload, '\n')
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

func defaultRelayfileSocketPath() string {
	if value := strings.TrimSpace(os.Getenv("RELAYFILE_SOCK")); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); value != "" {
		return filepath.Join(value, "relayfile.sock")
	}
	return filepath.Join(os.TempDir(), "relayfile.sock")
}
