package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	mathrand "math/rand/v2"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/agentworkforce/relayfile/internal/mountsync"
	"github.com/fsnotify/fsnotify"
)

const (
	defaultServerURL        = "https://api.relayfile.dev"
	defaultCloudAPIURL      = "https://agentrelay.com/cloud"
	defaultObserverURL      = "https://agentrelay.com/observer/file"
	configDirName           = ".relayfile"
	websocketReconcileEvery = 10
	defaultMountMode        = "poll"
	defaultMountInterval    = 30 * time.Second
	defaultMountTimeout     = 15 * time.Second
)

var defaultJoinScopes = []string{"fs:read", "fs:write"}

type credentials struct {
	Server    string `json:"server"`
	Token     string `json:"token"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type cloudCredentials struct {
	APIURL                string `json:"apiUrl"`
	AccessToken           string `json:"accessToken"`
	RefreshToken          string `json:"refreshToken,omitempty"`
	AccessTokenExpiresAt  string `json:"accessTokenExpiresAt,omitempty"`
	RefreshTokenExpiresAt string `json:"refreshTokenExpiresAt,omitempty"`
	UpdatedAt             string `json:"updatedAt,omitempty"`
}

type workspaceCatalog struct {
	Default    string            `json:"default,omitempty"`
	Workspaces []workspaceRecord `json:"workspaces"`
}

type workspaceRecord struct {
	Name        string   `json:"name"`
	ID          string   `json:"id,omitempty"`
	CreatedAt   string   `json:"createdAt"`
	LastUsedAt  string   `json:"lastUsedAt,omitempty"`
	LocalDir    string   `json:"localDir,omitempty"`
	Server      string   `json:"server,omitempty"`
	CloudAPIURL string   `json:"cloudApiUrl,omitempty"`
	AgentName   string   `json:"agentName,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
}

type apiClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type bulkWriteRequest struct {
	Files []bulkWriteFile `json:"files"`
}

type bulkWriteFile struct {
	Path        string `json:"path"`
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
	Encoding    string `json:"encoding,omitempty"`
}

type bulkWriteResponse struct {
	Written       int              `json:"written"`
	ErrorCount    int              `json:"errorCount"`
	Errors        []bulkWriteError `json:"errors"`
	CorrelationID string           `json:"correlationId"`
}

type bulkWriteError struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type syncStatusResponse struct {
	WorkspaceID string               `json:"workspaceId"`
	Providers   []syncProviderStatus `json:"providers"`
}

type treeResponse struct {
	Path       string      `json:"path"`
	Entries    []treeEntry `json:"entries"`
	NextCursor *string     `json:"nextCursor"`
}

type treeEntry struct {
	Path             string `json:"path"`
	Type             string `json:"type"`
	Revision         string `json:"revision"`
	Provider         string `json:"provider,omitempty"`
	ProviderObjectID string `json:"providerObjectId,omitempty"`
	Size             *int64 `json:"size,omitempty"`
	UpdatedAt        string `json:"updatedAt,omitempty"`
}

type syncProviderStatus struct {
	Provider              string         `json:"provider"`
	Status                string         `json:"status"`
	Cursor                *string        `json:"cursor"`
	WatermarkTs           *string        `json:"watermarkTs"`
	LagSeconds            int            `json:"lagSeconds"`
	LastError             *string        `json:"lastError"`
	FailureCodes          map[string]int `json:"failureCodes"`
	DeadLetteredEnvelopes int            `json:"deadLetteredEnvelopes"`
	DeadLetteredOps       int            `json:"deadLetteredOps"`
}

type exportedFile struct {
	Path        string `json:"path"`
	Revision    string `json:"revision"`
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
	Encoding    string `json:"encoding,omitempty"`
	LastEdited  string `json:"lastEditedAt,omitempty"`
}

type readFileResponse struct {
	Path             string            `json:"path"`
	Revision         string            `json:"revision"`
	ContentType      string            `json:"contentType"`
	Content          string            `json:"content"`
	Encoding         string            `json:"encoding,omitempty"`
	Provider         string            `json:"provider,omitempty"`
	ProviderObjectID string            `json:"providerObjectId,omitempty"`
	LastEdited       string            `json:"lastEditedAt,omitempty"`
	Semantics        map[string]any    `json:"semantics,omitempty"`
	Properties       map[string]string `json:"properties,omitempty"`
}

type adminWorkspaceList struct {
	WorkspaceIDs []string        `json:"workspaceIds"`
	Workspaces   json.RawMessage `json:"workspaces"`
}

type cloudWorkspaceCreateRequest struct {
	Name string `json:"name,omitempty"`
}

type cloudWorkspaceCreateResponse struct {
	WorkspaceID  string `json:"workspaceId"`
	RelayfileURL string `json:"relayfileUrl,omitempty"`
	CreatedAt    string `json:"createdAt,omitempty"`
	Name         string `json:"name,omitempty"`
}

type cloudWorkspaceJoinRequest struct {
	AgentName string   `json:"agentName,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
}

type cloudWorkspaceJoinResponse struct {
	WorkspaceID      string `json:"workspaceId"`
	Token            string `json:"token"`
	RelayfileURL     string `json:"relayfileUrl"`
	WSURL            string `json:"wsUrl,omitempty"`
	RelaycastAPIKey  string `json:"relaycastApiKey,omitempty"`
	RelaycastBaseURL string `json:"relaycastBaseUrl,omitempty"`
}

type cloudConnectSessionRequest struct {
	AllowedIntegrations []string `json:"allowedIntegrations,omitempty"`
}

type cloudConnectSessionResponse struct {
	Token        string `json:"token,omitempty"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
	ConnectLink  string `json:"connectLink,omitempty"`
	ConnectionID string `json:"connectionId,omitempty"`
}

type cloudIntegrationReadyResponse struct {
	Ready bool `json:"ready"`
}

type cloudTokenRefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

type cloudIntegrationCatalogResponse struct {
	Providers []integrationCatalogEntry `json:"providers"`
	Version   string                    `json:"version,omitempty"`
}

type integrationCatalogEntry struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName,omitempty"`
	ConfigKey   string `json:"configKey,omitempty"`
	VFSRoot     string `json:"vfsRoot,omitempty"`
	Deprecated  bool   `json:"deprecated,omitempty"`
}

type cloudIntegrationListEntry struct {
	Provider       string `json:"provider"`
	Status         string `json:"status,omitempty"`
	LagSeconds     int    `json:"lagSeconds,omitempty"`
	LastEventAt    string `json:"lastEventAt,omitempty"`
	ConnectionID   string `json:"connectionId,omitempty"`
	WebhookHealthy *bool  `json:"webhookHealthy,omitempty"`
	Deprecated     bool   `json:"deprecated,omitempty"`
}

type syncStateFile struct {
	WorkspaceID      string              `json:"workspaceId"`
	Mode             string              `json:"mode"`
	LastReconcileAt  string              `json:"lastReconcileAt,omitempty"`
	LastEventAt      string              `json:"lastEventAt,omitempty"`
	IntervalMs       int64               `json:"intervalMs"`
	Providers        []syncStateProvider `json:"providers,omitempty"`
	PendingWriteback int                 `json:"pendingWriteback"`
	PendingConflicts int                 `json:"pendingConflicts"`
	DeniedPaths      int                 `json:"deniedPaths"`
	StallReason      string              `json:"stallReason,omitempty"`
	Daemon           *syncStateDaemon    `json:"daemon,omitempty"`
}

type syncStateProvider struct {
	Provider        string `json:"provider"`
	Status          string `json:"status"`
	LagSeconds      int    `json:"lagSeconds"`
	DeadLetteredOps int    `json:"deadLetteredOps"`
	LastError       string `json:"lastError,omitempty"`
	LastEventAt     string `json:"lastEventAt,omitempty"`
}

type syncStateDaemon struct {
	PID     int    `json:"pid,omitempty"`
	LogFile string `json:"logFile,omitempty"`
	PIDFile string `json:"pidFile,omitempty"`
}

type integrationConnectionState struct {
	Provider     string `json:"provider"`
	ConnectionID string `json:"connectionId,omitempty"`
	ConnectedAt  string `json:"connectedAt,omitempty"`
	UpdatedAt    string `json:"updatedAt,omitempty"`
}

type daemonPIDState struct {
	PID         int    `json:"pid"`
	WorkspaceID string `json:"workspaceId"`
	LocalDir    string `json:"localDir"`
	LogFile     string `json:"logFile"`
	StartedAt   string `json:"startedAt"`
}

type apiError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *apiError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("http %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("http %d %s: %s", e.StatusCode, e.Code, e.Message)
}

func main() {
	log.SetFlags(0)
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return runSetup(nil, stdin, stdout)
	}

	switch args[0] {
	case "setup":
		return runSetup(args[1:], stdin, stdout)
	case "login":
		return runLogin(args[1:], stdin, stdout)
	case "workspace":
		return runWorkspace(args[1:], stdin, stdout)
	case "integration":
		return runIntegration(args[1:], stdin, stdout)
	case "mount":
		return runMount(args[1:])
	case "tree", "ls":
		return runTree(args[1:], stdout)
	case "read", "cat":
		return runRead(args[1:], stdout)
	case "seed":
		return runSeed(args[1:], stdout)
	case "export":
		return runExport(args[1:], stdout)
	case "status":
		return runStatus(args[1:], stdout)
	case "stop":
		return runStop(args[1:], stdout)
	case "logs":
		return runLogs(args[1:], stdout)
	case "observer":
		return runObserver(args[1:], stdout)
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `relayfile is the RelayFile CLI.

Usage:
  relayfile
  relayfile setup [--provider PROVIDER] [--workspace NAME] [--local-dir DIR]
  relayfile login --server URL [--token TOKEN]
  relayfile workspace create NAME
  relayfile workspace use NAME
  relayfile workspace list
  relayfile workspace delete NAME [--yes]
  relayfile integration connect PROVIDER [--workspace NAME]
  relayfile integration list [--workspace NAME] [--json]
  relayfile integration disconnect PROVIDER [--workspace NAME] [--yes]
  relayfile mount [WORKSPACE] [LOCAL_DIR]
  relayfile tree [WORKSPACE] [PATH] [--depth N]
  relayfile read [WORKSPACE] PATH
  relayfile seed [WORKSPACE] [DIR]
  relayfile export [WORKSPACE] --format FORMAT [--output FILE]
  relayfile status [WORKSPACE]
  relayfile stop [WORKSPACE]
  relayfile logs [WORKSPACE]
  relayfile observer [WORKSPACE] [--no-open]

Subcommands:
  setup       Sign in, connect an integration, and mount the workspace
  login       Store credentials in ~/.relayfile/credentials.json
  workspace   Create, select, list, or delete locally tracked workspaces
  integration Connect, list, or disconnect workspace integrations
  mount       Mirror a remote workspace to a local directory; add --background to detach
  tree        List a remote workspace path
  read        Print a remote file's content
  seed        Upload a directory tree with bulk writes
  export      Export a workspace as json, tar, or patch
  status      Show sync status and local mirror state for a workspace
  stop        Stop a background mount
  logs        Print the background mount log
  observer    Open the hosted file observer for a workspace`)
}

func runSetup(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cloudAPIURL := fs.String("cloud-api-url", envOrDefault("RELAYFILE_CLOUD_API_URL", defaultCloudAPIURL), "Relayfile Cloud API URL")
	cloudToken := fs.String("cloud-token", strings.TrimSpace(os.Getenv("RELAYFILE_CLOUD_TOKEN")), "Relayfile Cloud access token; skips browser login when set")
	workspaceName := fs.String("workspace", "", "workspace name to create")
	provider := fs.String("provider", "", "integration provider to connect; use none to skip")
	localDirFlag := fs.String("local-dir", "", "local mount directory")
	noOpen := fs.Bool("no-open", false, "print browser URLs instead of opening them")
	skipMount := fs.Bool("skip-mount", false, "finish after setup without starting the mount process")
	once := fs.Bool("once", false, "run one mount sync cycle and exit")
	loginTimeout := fs.Duration("login-timeout", 5*time.Minute, "cloud login timeout")
	connectTimeout := fs.Duration("connect-timeout", 5*time.Minute, "integration connection timeout")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"cloud-api-url":   true,
		"cloud-token":     true,
		"workspace":       true,
		"provider":        true,
		"local-dir":       true,
		"no-open":         false,
		"skip-mount":      false,
		"once":            false,
		"login-timeout":   true,
		"connect-timeout": true,
	})); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: relayfile setup [--provider PROVIDER] [--workspace NAME] [--local-dir DIR]")
	}

	cloudAPI := strings.TrimRight(strings.TrimSpace(*cloudAPIURL), "/")
	if cloudAPI == "" {
		cloudAPI = defaultCloudAPIURL
	}

	fmt.Fprintln(stdout, "Relayfile setup. This signs you in, connects an integration, and prepares a local VFS mount.")

	tokenSet, err := ensureCloudCredentials(cloudAPI, strings.TrimSpace(*cloudToken), *loginTimeout, !*noOpen, stdout)
	if err != nil {
		return err
	}

	name := strings.TrimSpace(*workspaceName)
	if name == "" {
		defaultName := "relayfile-" + time.Now().UTC().Format("20060102-150405")
		prompted, err := promptLine(stdin, stdout, fmt.Sprintf("Workspace name [%s]: ", defaultName))
		if err != nil {
			return err
		}
		name = strings.TrimSpace(prompted)
		if name == "" {
			name = defaultName
		}
	}

	selectedProvider := strings.TrimSpace(*provider)
	if selectedProvider == "" {
		providers, _ := loadIntegrationCatalog(tokenSet.APIURL, tokenSet.AccessToken)
		prompted, err := promptLine(stdin, stdout, fmt.Sprintf("Integration (%s) [github]: ", providerPromptText(providers)))
		if err != nil {
			return err
		}
		selectedProvider = strings.TrimSpace(prompted)
		if selectedProvider == "" {
			selectedProvider = "github"
		}
	}
	selectedProvider = normalizeProviderID(selectedProvider)

	localDir := strings.TrimSpace(*localDirFlag)
	if localDir == "" {
		prompted, err := promptLine(stdin, stdout, "Local mount directory [./relayfile-mount]: ")
		if err != nil {
			return err
		}
		localDir = strings.TrimSpace(prompted)
		if localDir == "" {
			localDir = "./relayfile-mount"
		}
	}
	absLocalDir, err := filepath.Abs(localDir)
	if err != nil {
		return err
	}
	if err := ensureMirrorLayout(absLocalDir); err != nil {
		return err
	}

	record, createdWorkspace, err := ensureWorkspaceForSetup(tokenSet, name, absLocalDir)
	if err != nil {
		return err
	}

	joined, err := joinWorkspaceViaCloud(tokenSet, record.ID, record.AgentName, record.Scopes)
	if err != nil {
		return err
	}
	if err := persistJoinedWorkspace(record, joined, tokenSet.APIURL, absLocalDir); err != nil {
		return err
	}

	if createdWorkspace {
		fmt.Fprintf(stdout, "Workspace %s ready (id: %s)\n", record.Name, record.ID)
	} else {
		fmt.Fprintf(stdout, "Workspace %s reused (id: %s)\n", record.Name, record.ID)
	}

	if selectedProvider != "" && selectedProvider != "none" && selectedProvider != "skip" {
		if createdWorkspace {
			if err := connectCloudIntegration(tokenSet.APIURL, record.ID, joined.Token, selectedProvider, absLocalDir, *connectTimeout, !*noOpen, stdout); err != nil {
				return err
			}
		} else {
			if err := ensureCloudIntegration(tokenSet.APIURL, record.ID, joined.Token, selectedProvider, absLocalDir, *connectTimeout, !*noOpen, stdout); err != nil {
				return err
			}
		}
		if !*skipMount {
			if err := waitForInitialSync(joined.RelayfileURL, joined.Token, record.ID, selectedProvider, absLocalDir, *connectTimeout, stdout); err != nil {
				return err
			}
		}
	} else {
		fmt.Fprintln(stdout, "Integration connection skipped")
	}

	mountArgs := []string{
		"--server", strings.TrimRight(joined.RelayfileURL, "/"),
		"--token", joined.Token,
		record.ID,
		localDir,
	}
	if *once {
		mountArgs = append(mountArgs, "--once")
	}
	if *skipMount {
		fmt.Fprintf(stdout, "Setup complete. Start the VFS mount with:\n  relayfile mount %s %s\n", record.ID, localDir)
		return nil
	}

	fmt.Fprintf(stdout, "Starting VFS mount at %s\n", localDir)
	return runMount(mountArgs)
}

func ensureCloudCredentials(cloudAPIURL, explicitToken string, timeout time.Duration, shouldOpenBrowser bool, stdout io.Writer) (cloudCredentials, error) {
	explicitToken = strings.TrimSpace(explicitToken)
	cloudAPIURL = strings.TrimRight(strings.TrimSpace(cloudAPIURL), "/")
	if cloudAPIURL == "" {
		cloudAPIURL = defaultCloudAPIURL
	}
	if explicitToken != "" {
		creds := cloudCredentials{
			APIURL:      cloudAPIURL,
			AccessToken: explicitToken,
			UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
		}
		if err := saveCloudCredentials(creds); err != nil {
			return cloudCredentials{}, err
		}
		return creds, nil
	}

	creds, err := loadCloudCredentials()
	if err == nil {
		if strings.TrimSpace(creds.APIURL) == "" {
			creds.APIURL = cloudAPIURL
		}
		if strings.TrimRight(strings.TrimSpace(creds.APIURL), "/") != cloudAPIURL {
			creds.APIURL = cloudAPIURL
		}
		refreshed, refreshErr := refreshCloudCredentialsIfNeeded(creds)
		if refreshErr == nil {
			return refreshed, nil
		}
	}

	creds, err = runCloudLogin(cloudAPIURL, timeout, shouldOpenBrowser, stdout)
	if err != nil {
		return cloudCredentials{}, err
	}
	if err := saveCloudCredentials(creds); err != nil {
		return cloudCredentials{}, err
	}
	return creds, nil
}

func loadCloudCredentials() (cloudCredentials, error) {
	var creds cloudCredentials
	payload, err := os.ReadFile(cloudCredentialsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return creds, fmt.Errorf("cloud credentials not found at %s; run relayfile setup or relayfile login", cloudCredentialsPath())
		}
		return creds, err
	}
	if err := json.Unmarshal(payload, &creds); err != nil {
		return creds, fmt.Errorf("parse %s: %w", cloudCredentialsPath(), err)
	}
	if strings.TrimSpace(creds.APIURL) == "" {
		creds.APIURL = defaultCloudAPIURL
	}
	return creds, nil
}

func refreshCloudCredentialsIfNeeded(creds cloudCredentials) (cloudCredentials, error) {
	if !cloudAccessTokenExpiredSoon(creds) {
		if strings.TrimSpace(creds.APIURL) == "" {
			creds.APIURL = defaultCloudAPIURL
		}
		return creds, nil
	}
	return refreshCloudCredentials(creds)
}

func cloudAccessTokenExpiredSoon(creds cloudCredentials) bool {
	if strings.TrimSpace(creds.AccessToken) == "" {
		return true
	}
	expiry, ok := parseRFC3339(strings.TrimSpace(creds.AccessTokenExpiresAt))
	if !ok {
		return false
	}
	return !time.Now().UTC().Before(expiry.Add(-60 * time.Second))
}

func refreshCloudCredentials(creds cloudCredentials) (cloudCredentials, error) {
	if strings.TrimSpace(creds.RefreshToken) == "" {
		return creds, errors.New("cloud session expired. Run 'relayfile login' to sign in again.")
	}
	client, err := newAPIClient(creds.APIURL, creds.AccessToken)
	if err != nil {
		return creds, err
	}
	var refreshed cloudCredentials
	err = client.postJSON(context.Background(), "/api/v1/auth/token/refresh", cloudTokenRefreshRequest{
		RefreshToken: creds.RefreshToken,
	}, &refreshed)
	if err != nil {
		var httpErr *apiError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusForbidden && strings.EqualFold(strings.TrimSpace(httpErr.Code), "invalid_grant") {
			return creds, errors.New("cloud session expired. Run 'relayfile login' to sign in again.")
		}
		return creds, fmt.Errorf("refresh cloud session: %w", err)
	}
	if strings.TrimSpace(refreshed.APIURL) == "" {
		refreshed.APIURL = creds.APIURL
	}
	if strings.TrimSpace(refreshed.RefreshToken) == "" {
		refreshed.RefreshToken = creds.RefreshToken
	}
	if strings.TrimSpace(refreshed.RefreshTokenExpiresAt) == "" {
		refreshed.RefreshTokenExpiresAt = creds.RefreshTokenExpiresAt
	}
	refreshed.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := saveCloudCredentials(refreshed); err != nil {
		return creds, err
	}
	return refreshed, nil
}

func providerPromptText(entries []integrationCatalogEntry) string {
	ids := make([]string, 0, len(entries)+1)
	for _, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		ids = []string{"github", "notion", "linear", "slack-sage", "none"}
	}
	if !containsString(ids, "none") {
		ids = append(ids, "none")
	}
	return strings.Join(ids, ", ")
}

func normalizeProviderID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "slack", "slack-sage":
		return "slack-sage"
	default:
		return value
	}
}

func loadIntegrationCatalog(cloudAPIURL, accessToken string) ([]integrationCatalogEntry, error) {
	client, err := newAPIClient(cloudAPIURL, accessToken)
	if err != nil {
		return fallbackIntegrationCatalog(), err
	}
	var payload cloudIntegrationCatalogResponse
	if err := client.getJSON(context.Background(), "/api/v1/integrations/catalog", &payload); err != nil {
		return fallbackIntegrationCatalog(), err
	}
	if len(payload.Providers) == 0 {
		return fallbackIntegrationCatalog(), nil
	}
	return payload.Providers, nil
}

func fallbackIntegrationCatalog() []integrationCatalogEntry {
	return []integrationCatalogEntry{
		{ID: "github", DisplayName: "GitHub", VFSRoot: "/github"},
		{ID: "notion", DisplayName: "Notion", VFSRoot: "/notion"},
		{ID: "linear", DisplayName: "Linear", VFSRoot: "/linear"},
		{ID: "slack-sage", DisplayName: "Slack", VFSRoot: "/slack"},
		{ID: "slack-my-senior-dev", DisplayName: "Slack (MSD)", VFSRoot: "/slack-msd"},
		{ID: "slack-nightcto", DisplayName: "Slack (NightCTO)", VFSRoot: "/slack-nightcto"},
	}
}

func ensureMirrorLayout(localDir string) error {
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(localDir, ".relay"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(localDir, ".relay", "integrations"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(localDir, ".relay", "disconnected"), 0o755); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(localDir, ".relay", "conflicts"), 0o755)
}

func ensureWorkspaceForSetup(cloud cloudCredentials, name, localDir string) (workspaceRecord, bool, error) {
	if record, ok := workspaceRecordByName(name); ok {
		if record.ID == "" {
			record.ID = record.Name
		}
		if record.AgentName == "" {
			record.AgentName = "relayfile-cli"
		}
		if len(record.Scopes) == 0 {
			record.Scopes = append([]string(nil), defaultJoinScopes...)
		}
		return record, false, nil
	}
	client, err := newAPIClient(cloud.APIURL, cloud.AccessToken)
	if err != nil {
		return workspaceRecord{}, false, err
	}
	var created cloudWorkspaceCreateResponse
	if err := client.postJSON(context.Background(), "/api/v1/workspaces", cloudWorkspaceCreateRequest{Name: name}, &created); err != nil {
		return workspaceRecord{}, false, fmt.Errorf("create cloud workspace: %w", err)
	}
	record := workspaceRecord{
		Name:        name,
		ID:          strings.TrimSpace(created.WorkspaceID),
		CreatedAt:   strings.TrimSpace(created.CreatedAt),
		LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
		LocalDir:    localDir,
		CloudAPIURL: cloud.APIURL,
		Server:      strings.TrimRight(strings.TrimSpace(created.RelayfileURL), "/"),
		AgentName:   "relayfile-cli",
		Scopes:      append([]string(nil), defaultJoinScopes...),
	}
	if record.CreatedAt == "" {
		record.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if record.ID == "" {
		return workspaceRecord{}, false, errors.New("cloud workspace response missing workspaceId")
	}
	return record, true, nil
}

func persistJoinedWorkspace(record workspaceRecord, joined cloudWorkspaceJoinResponse, cloudAPIURL, localDir string) error {
	serverURL := strings.TrimRight(strings.TrimSpace(joined.RelayfileURL), "/")
	if err := saveCredentials(credentials{
		Server:    serverURL,
		Token:     strings.TrimSpace(joined.Token),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return err
	}
	record.Server = serverURL
	record.LocalDir = localDir
	record.CloudAPIURL = cloudAPIURL
	record.LastUsedAt = time.Now().UTC().Format(time.RFC3339)
	if record.AgentName == "" {
		record.AgentName = "relayfile-cli"
	}
	if len(record.Scopes) == 0 {
		record.Scopes = append([]string(nil), defaultJoinScopes...)
	}
	if _, err := upsertWorkspaceDetails(record); err != nil {
		return err
	}
	_, err := setDefaultWorkspace(record.Name)
	return err
}

func joinWorkspaceViaCloud(cloud cloudCredentials, workspaceID, agentName string, scopes []string) (cloudWorkspaceJoinResponse, error) {
	if agentName == "" {
		agentName = "relayfile-cli"
	}
	if len(scopes) == 0 {
		scopes = append([]string(nil), defaultJoinScopes...)
	}
	client, err := newAPIClient(cloud.APIURL, cloud.AccessToken)
	if err != nil {
		return cloudWorkspaceJoinResponse{}, err
	}
	var joined cloudWorkspaceJoinResponse
	if err := client.postJSON(context.Background(), fmt.Sprintf("/api/v1/workspaces/%s/join", url.PathEscape(workspaceID)), cloudWorkspaceJoinRequest{
		AgentName: agentName,
		Scopes:    scopes,
	}, &joined); err != nil {
		return cloudWorkspaceJoinResponse{}, fmt.Errorf("join cloud workspace: %w", err)
	}
	if strings.TrimSpace(joined.WorkspaceID) == "" {
		joined.WorkspaceID = workspaceID
	}
	if strings.TrimSpace(joined.Token) == "" {
		return cloudWorkspaceJoinResponse{}, errors.New("cloud join response missing token")
	}
	if strings.TrimSpace(joined.RelayfileURL) == "" {
		return cloudWorkspaceJoinResponse{}, errors.New("cloud join response missing relayfileUrl")
	}
	return joined, nil
}

func runCloudLogin(cloudAPIURL string, timeout time.Duration, shouldOpenBrowser bool, stdout io.Writer) (cloudCredentials, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	state, err := randomURLSafe(24)
	if err != nil {
		return cloudCredentials{}, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return cloudCredentials{}, err
	}
	defer listener.Close()

	tokenCh := make(chan cloudCredentials, 1)
	errCh := make(chan error, 1)
	server := &http.Server{}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("state"); got != state {
			http.Error(w, "Relayfile Cloud login state mismatch", http.StatusBadRequest)
			errCh <- errors.New("Relayfile Cloud login state mismatch")
			return
		}
		if loginError := strings.TrimSpace(r.URL.Query().Get("error")); loginError != "" {
			http.Error(w, "Relayfile Cloud login failed", http.StatusBadRequest)
			errCh <- fmt.Errorf("Relayfile Cloud login failed: %s", loginError)
			return
		}
		tokens, err := readCloudCredentialsFromQuery(r.URL.Query(), cloudAPIURL)
		if err != nil {
			http.Error(w, "Relayfile Cloud login callback was missing token fields", http.StatusBadRequest)
			errCh <- err
			return
		}
		fmt.Fprintln(w, "Relayfile Cloud login complete. You can close this tab.")
		tokenCh <- tokens
	})

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	defer server.Close()

	redirectURI := "http://" + listener.Addr().String() + "/callback"
	loginURL, err := buildCloudURL(cloudAPIURL, "api/v1/cli/login")
	if err != nil {
		return cloudCredentials{}, err
	}
	query := loginURL.Query()
	query.Set("redirect_uri", redirectURI)
	query.Set("state", state)
	loginURL.RawQuery = query.Encode()

	fmt.Fprintf(stdout, "Sign in to Relayfile Cloud: %s\n", loginURL.String())
	if shouldOpenBrowser {
		if err := openBrowser(loginURL.String()); err != nil {
			return cloudCredentials{}, fmt.Errorf("open cloud login: %w", err)
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case tokens := <-tokenCh:
		return tokens, nil
	case err := <-errCh:
		return cloudCredentials{}, err
	case <-timer.C:
		return cloudCredentials{}, fmt.Errorf("Relayfile Cloud login timed out after %s", timeout)
	}
}

func ensureCloudIntegration(cloudAPIURL, workspaceID, workspaceToken, provider, localDir string, timeout time.Duration, shouldOpenBrowser bool, stdout io.Writer) error {
	connectionID := loadSavedConnectionID(localDir, provider)
	if connectionID != "" {
		if ready, err := cloudIntegrationReady(cloudAPIURL, workspaceID, workspaceToken, provider, connectionID); err == nil && ready {
			fmt.Fprintf(stdout, "%s already connected\n", provider)
			return nil
		}
	}
	return connectCloudIntegration(cloudAPIURL, workspaceID, workspaceToken, provider, localDir, timeout, shouldOpenBrowser, stdout)
}

func connectCloudIntegration(cloudAPIURL, workspaceID, workspaceToken, provider, localDir string, timeout time.Duration, shouldOpenBrowser bool, stdout io.Writer) error {
	client, err := newAPIClient(cloudAPIURL, workspaceToken)
	if err != nil {
		return err
	}
	var session cloudConnectSessionResponse
	if err := client.postJSON(context.Background(), fmt.Sprintf("/api/v1/workspaces/%s/integrations/connect-session", url.PathEscape(workspaceID)), cloudConnectSessionRequest{
		AllowedIntegrations: []string{provider},
	}, &session); err != nil {
		return fmt.Errorf("create %s connect session: %w", provider, err)
	}

	connectionID := strings.TrimSpace(session.ConnectionID)
	if connectionID == "" {
		connectionID = workspaceID
	}
	_ = saveIntegrationConnection(localDir, integrationConnectionState{
		Provider:     provider,
		ConnectionID: connectionID,
		ConnectedAt:  time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
	})

	if connectLink := strings.TrimSpace(session.ConnectLink); connectLink != "" {
		fmt.Fprintf(stdout, "Connect %s: %s\n", provider, connectLink)
		if shouldOpenBrowser {
			if err := openBrowser(connectLink); err != nil {
				return fmt.Errorf("open %s connect link: %w", provider, err)
			}
		}
	} else {
		fmt.Fprintf(stdout, "%s already has a hosted connect session\n", provider)
	}

	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		ready, err := cloudIntegrationReady(cloudAPIURL, workspaceID, workspaceToken, provider, connectionID)
		if err != nil {
			return err
		}
		if ready {
			fmt.Fprintf(stdout, "%s connected. Files will appear under %s/%s within ~30s.\n", provider, localDir, providerRootDir(provider))
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s connection after %s", provider, timeout)
		}
		time.Sleep(2 * time.Second)
	}
}

func cloudIntegrationReady(cloudAPIURL, workspaceID, workspaceToken, provider, connectionID string) (bool, error) {
	client, err := newAPIClient(cloudAPIURL, workspaceToken)
	if err != nil {
		return false, err
	}
	query := url.Values{}
	if strings.TrimSpace(connectionID) != "" {
		query.Set("connectionId", connectionID)
	}
	var status cloudIntegrationReadyResponse
	if err := client.getJSON(context.Background(), fmt.Sprintf("/api/v1/workspaces/%s/integrations/%s/status?%s", url.PathEscape(workspaceID), url.PathEscape(provider), query.Encode()), &status); err != nil {
		return false, fmt.Errorf("check %s connection: %w", provider, err)
	}
	return status.Ready, nil
}

func waitForInitialSync(serverURL, token, workspaceID, provider, localDir string, timeout time.Duration, stdout io.Writer) error {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	client, err := newAPIClient(serverURL, token)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	lastPrinted := time.Time{}
	for {
		status, err := fetchWorkspaceSyncStatus(client, workspaceID)
		if err != nil {
			return err
		}
		providerStatus, ok := syncProviderByName(status, provider)
		if ok && providerReadyForMirror(client, workspaceID, provider, providerStatus) {
			return writeMirrorStateFile(localDir, buildSyncStateSnapshot(status, workspaceID, defaultMountMode, defaultMountInterval, localDir, readDaemonPID(localDir), ""))
		}
		if time.Since(lastPrinted) >= 5*time.Second {
			lastPrinted = time.Now()
			if ok {
				fmt.Fprintf(stdout, "Syncing %s... lag %ds status=%s\n", provider, providerStatus.LagSeconds, providerStatus.Status)
			}
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(stdout, "%s still syncing in the background. Files will continue to populate. See 'relayfile status'.\n", provider)
			return nil
		}
		time.Sleep(2 * time.Second)
	}
}

func runLogin(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", envOrDefault("RELAYFILE_SERVER", envOrDefault("RELAYFILE_BASE_URL", defaultServerURL)), "relayfile server URL")
	token := fs.String("token", strings.TrimSpace(os.Getenv("RELAYFILE_TOKEN")), "API token")
	if err := fs.Parse(args); err != nil {
		return err
	}

	serverValue := strings.TrimSpace(*server)
	if serverValue == "" {
		serverValue = defaultServerURL
	}
	tokenValue := strings.TrimSpace(*token)
	if tokenValue == "" {
		prompted, err := promptLine(stdin, stdout, "API key: ")
		if err != nil {
			return err
		}
		tokenValue = strings.TrimSpace(prompted)
	}
	if tokenValue == "" {
		return errors.New("token is required")
	}

	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(serverValue, "/")+"/health", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tokenValue)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check failed with status %d", resp.StatusCode)
	}

	creds := credentials{
		Server:    strings.TrimRight(serverValue, "/"),
		Token:     tokenValue,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := saveCredentials(creds); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Stored credentials for %s\n", creds.Server)
	return nil
}

func runWorkspace(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("workspace subcommand is required: create, use, list, or delete")
	}
	switch args[0] {
	case "create":
		return runWorkspaceCreate(args[1:], stdout)
	case "use":
		return runWorkspaceUse(args[1:], stdout)
	case "list":
		return runWorkspaceList(args[1:], stdout)
	case "delete":
		return runWorkspaceDelete(args[1:], stdin, stdout)
	default:
		return fmt.Errorf("unknown workspace subcommand %q", args[0])
	}
}

func runIntegration(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("integration subcommand is required: connect, list, or disconnect")
	}
	switch args[0] {
	case "connect":
		return runIntegrationConnect(args[1:], stdin, stdout)
	case "list":
		return runIntegrationList(args[1:], stdout)
	case "disconnect":
		return runIntegrationDisconnect(args[1:], stdin, stdout)
	default:
		return fmt.Errorf("unknown integration subcommand %q", args[0])
	}
}

func runIntegrationConnect(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("integration connect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceName := fs.String("workspace", "", "workspace name or id")
	cloudAPIURL := fs.String("cloud-api-url", envOrDefault("RELAYFILE_CLOUD_API_URL", defaultCloudAPIURL), "Relayfile Cloud API URL")
	noOpen := fs.Bool("no-open", false, "print the hosted URL instead of opening it")
	timeout := fs.Duration("timeout", 5*time.Minute, "integration readiness timeout")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"workspace":     true,
		"cloud-api-url": true,
		"no-open":       false,
		"timeout":       true,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile integration connect PROVIDER [--workspace NAME] [--no-open] [--timeout 5m]")
	}
	provider := normalizeProviderID(fs.Arg(0))
	record, err := resolveWorkspaceRecord(strings.TrimSpace(*workspaceName))
	if err != nil {
		return err
	}
	cloudCreds, err := ensureCloudCredentials(strings.TrimSpace(*cloudAPIURL), "", 5*time.Minute, false, stdout)
	if err != nil {
		return err
	}
	joined, err := joinWorkspaceViaCloud(cloudCreds, record.ID, record.AgentName, record.Scopes)
	if err != nil {
		return err
	}
	if err := persistJoinedWorkspace(record, joined, cloudCreds.APIURL, record.LocalDir); err != nil {
		return err
	}
	if err := ensureCloudIntegration(cloudCreds.APIURL, record.ID, joined.Token, provider, record.LocalDir, *timeout, !*noOpen, stdout); err != nil {
		return err
	}
	return waitForInitialSync(joined.RelayfileURL, joined.Token, record.ID, provider, record.LocalDir, *timeout, stdout)
}

func runIntegrationList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("integration list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceName := fs.String("workspace", "", "workspace name or id")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	cloudAPIURL := fs.String("cloud-api-url", envOrDefault("RELAYFILE_CLOUD_API_URL", defaultCloudAPIURL), "Relayfile Cloud API URL")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"workspace":     true,
		"json":          false,
		"cloud-api-url": true,
	})); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: relayfile integration list [--workspace NAME] [--json]")
	}
	record, err := resolveWorkspaceRecord(strings.TrimSpace(*workspaceName))
	if err != nil {
		return err
	}
	cloudCreds, err := ensureCloudCredentials(strings.TrimSpace(*cloudAPIURL), "", 5*time.Minute, false, io.Discard)
	if err != nil {
		return err
	}
	joined, err := joinWorkspaceViaCloud(cloudCreds, record.ID, record.AgentName, record.Scopes)
	if err != nil {
		return err
	}
	client, err := newAPIClient(cloudCreds.APIURL, joined.Token)
	if err != nil {
		return err
	}
	var entries []cloudIntegrationListEntry
	if err := client.getJSON(context.Background(), fmt.Sprintf("/api/v1/workspaces/%s/integrations", url.PathEscape(record.ID)), &entries); err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, entries)
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "No integrations connected")
		return nil
	}
	fmt.Fprintln(stdout, "provider\tstatus\tlag\tlast_event_at")
	for _, entry := range entries {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", entry.Provider, defaultIfBlank(entry.Status, "unknown"), formatLag(entry.LagSeconds), defaultIfBlank(entry.LastEventAt, "-"))
	}
	return nil
}

func runIntegrationDisconnect(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("integration disconnect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceName := fs.String("workspace", "", "workspace name or id")
	cloudAPIURL := fs.String("cloud-api-url", envOrDefault("RELAYFILE_CLOUD_API_URL", defaultCloudAPIURL), "Relayfile Cloud API URL")
	yes := fs.Bool("yes", false, "skip confirmation")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"workspace":     true,
		"cloud-api-url": true,
		"yes":           false,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile integration disconnect PROVIDER [--workspace NAME] [--yes]")
	}
	provider := normalizeProviderID(fs.Arg(0))
	record, err := resolveWorkspaceRecord(strings.TrimSpace(*workspaceName))
	if err != nil {
		return err
	}
	if !*yes {
		answer, err := promptLine(stdin, stdout, fmt.Sprintf("Disconnect %q from workspace %q? [y/N]: ", provider, record.Name))
		if err != nil {
			return err
		}
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(stdout, "Aborted")
			return nil
		}
	}
	cloudCreds, err := ensureCloudCredentials(strings.TrimSpace(*cloudAPIURL), "", 5*time.Minute, false, io.Discard)
	if err != nil {
		return err
	}
	joined, err := joinWorkspaceViaCloud(cloudCreds, record.ID, record.AgentName, record.Scopes)
	if err != nil {
		return err
	}
	client, err := newAPIClient(cloudCreds.APIURL, joined.Token)
	if err != nil {
		return err
	}
	if _, _, err := client.do(context.Background(), http.MethodDelete, fmt.Sprintf("/api/v1/workspaces/%s/integrations/%s/status", url.PathEscape(record.ID), url.PathEscape(provider)), nil); err != nil {
		return err
	}
	if err := markProviderDisconnected(record.LocalDir, provider); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s disconnected from workspace %s\n", provider, record.Name)
	return nil
}

func runWorkspaceCreate(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("workspace create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile workspace create NAME")
	}

	name := strings.TrimSpace(fs.Arg(0))
	if name == "" {
		return errors.New("workspace name is required")
	}
	if _, err := loadCredentials(); err != nil {
		return err
	}
	record, err := upsertWorkspace(name)
	if err != nil {
		return err
	}
	workspaceID := record.ID
	if workspaceID == "" {
		workspaceID = name
	}
	fmt.Fprintf(stdout, "Workspace %s ready (id: %s)\n", record.Name, workspaceID)
	return nil
}

func runWorkspaceUse(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("workspace use", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile workspace use NAME")
	}

	record, err := setDefaultWorkspace(fs.Arg(0))
	if err != nil {
		return err
	}
	workspaceID := record.ID
	if workspaceID == "" {
		workspaceID = record.Name
	}
	fmt.Fprintf(stdout, "Default workspace set to %s (id: %s)\n", record.Name, workspaceID)
	return nil
}

func runWorkspaceList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("workspace list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "relayfile server URL override")
	token := fs.String("token", "", "relayfile token override")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server": true,
		"token":  true,
	})); err != nil {
		return err
	}

	creds, _ := loadCredentials()
	tokenValue := resolveToken(*token, creds)
	client, err := newAPIClient(resolveServer(*server, creds), tokenValue)
	if err == nil {
		var remote adminWorkspaceList
		err = client.getJSON(context.Background(), "/v1/admin/workspaces", &remote)
		if err != nil {
			err = client.getJSON(context.Background(), "/v1/admin/sync", &remote)
		}
		if err == nil {
			names := remoteWorkspaceNames(remote)
			if len(names) > 0 {
				for _, name := range names {
					fmt.Fprintln(stdout, name)
				}
				return nil
			}
		}
	}

	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return err
	}
	for _, name := range workspaceCatalogNames(catalog, tokenValue) {
		fmt.Fprintln(stdout, name)
	}
	return nil
}

func runWorkspaceDelete(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("workspace delete", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	yes := fs.Bool("yes", false, "skip confirmation prompt")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"yes": false,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile workspace delete NAME [--yes]")
	}

	name := strings.TrimSpace(fs.Arg(0))
	if name == "" {
		return errors.New("workspace name is required")
	}
	if !*yes {
		answer, err := promptLine(stdin, stdout, fmt.Sprintf("Delete workspace %q from local config? [y/N]: ", name))
		if err != nil {
			return err
		}
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(stdout, "Aborted")
			return nil
		}
	}

	removed, err := removeWorkspace(name)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("workspace %q not found in %s", name, workspacesPath())
	}
	fmt.Fprintf(stdout, "Removed workspace %s from %s\n", name, workspacesPath())
	return nil
}

func runMount(args []string) error {
	fs := flag.NewFlagSet("mount", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	creds, _ := loadCredentials()
	server := fs.String("server", resolveServer("", creds), "relayfile server URL")
	token := fs.String("token", resolveToken("", creds), "bearer token")
	remotePath := fs.String("remote-path", envOrDefault("RELAYFILE_REMOTE_PATH", "/"), "remote root path")
	eventProvider := fs.String("provider", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_PROVIDER")), "event provider filter")
	stateFile := fs.String("state-file", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_STATE_FILE")), "state file path")
	localDirFlag := fs.String("local-dir", "", "local mirror directory")
	mode := fs.String("mode", envOrDefault("RELAYFILE_MOUNT_MODE", defaultMountMode), "mount mode: poll (recommended) or fuse")
	interval := fs.Duration("interval", durationEnv("RELAYFILE_MOUNT_INTERVAL", defaultMountInterval), "sync interval")
	intervalJitter := fs.Float64("interval-jitter", floatEnv("RELAYFILE_MOUNT_INTERVAL_JITTER", 0.2), "sync interval jitter ratio (0.0-1.0)")
	timeout := fs.Duration("timeout", durationEnv("RELAYFILE_MOUNT_TIMEOUT", defaultMountTimeout), "per-sync timeout")
	websocketEnabled := fs.Bool("websocket", boolEnv("RELAYFILE_MOUNT_WEBSOCKET", true), "enable websocket event streaming when available")
	background := fs.Bool("background", false, "detach and keep syncing in the background")
	pidFileFlag := fs.String("pid-file", "", "pid file path for background mode")
	logFileFlag := fs.String("log-file", "", "log file path for background mode")
	daemonized := fs.Bool("daemonized", false, "internal flag used by relayfile mount --background")
	once := fs.Bool("once", false, "run one sync cycle and exit")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server":          true,
		"token":           true,
		"remote-path":     true,
		"provider":        true,
		"state-file":      true,
		"mode":            true,
		"interval":        true,
		"interval-jitter": true,
		"timeout":         true,
		"websocket":       false,
		"background":      false,
		"pid-file":        true,
		"log-file":        true,
		"daemonized":      false,
		"once":            false,
		"local-dir":       true,
	})); err != nil {
		return err
	}
	if fs.NArg() > 2 {
		return errors.New("usage: relayfile mount [WORKSPACE] [LOCAL_DIR]")
	}

	workspaceID := ""
	localDir := "."
	tokenValue := strings.TrimSpace(*token)
	if tokenValue == "" {
		return errors.New("token is required; run relayfile login or pass --token")
	}
	var err error
	switch fs.NArg() {
	case 0:
		workspaceID, err = resolveWorkspaceIDWithToken("", tokenValue)
		localDir = strings.TrimSpace(*localDirFlag)
	case 1:
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
		localDir = strings.TrimSpace(*localDirFlag)
	case 2:
		if strings.TrimSpace(*localDirFlag) != "" {
			return errors.New("local directory specified twice")
		}
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
		localDir = fs.Arg(1)
	}
	if err != nil {
		return err
	}
	if localDir == "" {
		localDir = "."
	}
	absLocalDir, err := filepath.Abs(localDir)
	if err != nil {
		return err
	}
	if err := ensureMirrorLayout(absLocalDir); err != nil {
		return err
	}

	if strings.EqualFold(strings.TrimSpace(*mode), "fuse") || boolEnv("RELAYFILE_MOUNT_FUSE", false) {
		return errors.New("fuse mode is not available in this build; rerun with --mode=poll")
	}
	if *interval <= 0 {
		*interval = defaultMountInterval
	}
	if *timeout <= 0 {
		*timeout = defaultMountTimeout
	}
	*intervalJitter = clampJitterRatio(*intervalJitter)

	pidFile := strings.TrimSpace(*pidFileFlag)
	if pidFile == "" {
		pidFile = mountPIDFile(absLocalDir)
	}
	logFile := strings.TrimSpace(*logFileFlag)
	if logFile == "" {
		logFile = mountLogFile(absLocalDir)
	}

	if *background && !*daemonized {
		return spawnBackgroundMountProcess(args, absLocalDir, pidFile, logFile)
	}
	if *daemonized {
		if err := rotateLogFile(logFile); err != nil {
			return err
		}
		if err := writeDaemonPIDState(pidFile, daemonPIDState{
			PID:         os.Getpid(),
			WorkspaceID: workspaceID,
			LocalDir:    absLocalDir,
			LogFile:     logFile,
			StartedAt:   time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			return err
		}
		defer os.Remove(pidFile)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := mountsync.NewHTTPClient(*server, tokenValue, &http.Client{Timeout: *timeout})
	syncer, err := mountsync.NewSyncer(client, mountsync.SyncerOptions{
		WorkspaceID:   workspaceID,
		RemoteRoot:    *remotePath,
		EventProvider: strings.TrimSpace(*eventProvider),
		LocalRoot:     absLocalDir,
		StateFile:     strings.TrimSpace(*stateFile),
		WebSocket:     boolPtr(*websocketEnabled),
		RootCtx:       rootCtx,
		Logger:        log.Default(),
	})
	if err != nil {
		return fmt.Errorf("failed to initialize mount syncer: %w", err)
	}
	if _, err := upsertWorkspace(workspaceID); err != nil {
		return err
	}
	record, _ := workspaceRecordByID(workspaceID)
	record.ID = workspaceID
	if record.Name == "" {
		record.Name = workspaceID
	}
	record.LocalDir = absLocalDir
	record.Server = strings.TrimRight(strings.TrimSpace(*server), "/")
	if record.AgentName == "" {
		record.AgentName = "relayfile-cli"
	}
	if len(record.Scopes) == 0 {
		record.Scopes = append([]string(nil), defaultJoinScopes...)
	}
	_, _ = upsertWorkspaceDetails(record)

	return runMountLoop(rootCtx, syncer, absLocalDir, workspaceID, strings.TrimRight(strings.TrimSpace(*server), "/"), *timeout, *interval, *intervalJitter, *websocketEnabled, *once, *daemonized, pidFile, logFile)
}

func runTree(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("tree", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "relayfile server URL override")
	token := fs.String("token", "", "relayfile token override")
	pathFlag := fs.String("path", "/", "remote path to list")
	depth := fs.Int("depth", 1, "tree depth")
	jsonOutput := fs.Bool("json", false, "print the raw JSON response")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server": true,
		"token":  true,
		"path":   true,
		"depth":  true,
		"json":   false,
	})); err != nil {
		return err
	}
	if fs.NArg() > 2 {
		return errors.New("usage: relayfile tree [WORKSPACE] [PATH] [--depth N]")
	}

	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	tokenValue := resolveToken(*token, creds)
	client, err := newAPIClient(resolveServer(*server, creds), tokenValue)
	if err != nil {
		return err
	}

	remotePath := strings.TrimSpace(*pathFlag)
	var workspaceID string
	switch fs.NArg() {
	case 0:
		workspaceID, err = resolveWorkspaceIDWithToken("", tokenValue)
	case 1:
		arg := strings.TrimSpace(fs.Arg(0))
		if strings.HasPrefix(arg, "/") {
			workspaceID, err = resolveWorkspaceIDWithToken("", tokenValue)
			remotePath = arg
		} else {
			workspaceID, err = resolveWorkspaceIDWithToken(arg, tokenValue)
		}
	case 2:
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
		remotePath = strings.TrimSpace(fs.Arg(1))
	}
	if err != nil {
		return err
	}
	if remotePath == "" {
		remotePath = "/"
	}
	if *depth < 0 {
		return errors.New("depth must be greater than or equal to 0")
	}

	query := url.Values{}
	query.Set("path", remotePath)
	query.Set("depth", strconv.Itoa(*depth))
	body, _, err := client.getBytes(context.Background(), fmt.Sprintf("/v1/workspaces/%s/fs/tree?%s", url.PathEscape(workspaceID), query.Encode()))
	if err != nil {
		return err
	}
	if _, err := upsertWorkspace(workspaceID); err != nil {
		return err
	}
	if *jsonOutput {
		return writePrettyJSON(stdout, body)
	}

	var tree treeResponse
	if err := json.Unmarshal(body, &tree); err != nil {
		return err
	}
	if strings.TrimSpace(tree.Path) == "" {
		tree.Path = remotePath
	}
	fmt.Fprintf(stdout, "Tree %s\n", tree.Path)
	if len(tree.Entries) == 0 {
		fmt.Fprintln(stdout, "(empty)")
		return nil
	}
	for _, entry := range tree.Entries {
		entryType := strings.TrimSpace(entry.Type)
		if entryType == "" {
			entryType = "file"
		}
		fmt.Fprintf(stdout, "%-4s %s", entryType, entry.Path)
		if entry.Provider != "" {
			fmt.Fprintf(stdout, "  provider=%s", entry.Provider)
		}
		if entry.Size != nil {
			fmt.Fprintf(stdout, "  size=%d", *entry.Size)
		}
		if entry.Revision != "" {
			fmt.Fprintf(stdout, "  rev=%s", entry.Revision)
		}
		fmt.Fprintln(stdout)
	}
	if tree.NextCursor != nil && strings.TrimSpace(*tree.NextCursor) != "" {
		fmt.Fprintf(stdout, "next cursor: %s\n", strings.TrimSpace(*tree.NextCursor))
	}
	return nil
}

func runRead(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "relayfile server URL override")
	token := fs.String("token", "", "relayfile token override")
	output := fs.String("output", "-", "output file path or - for stdout")
	jsonOutput := fs.Bool("json", false, "print the raw JSON response")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server": true,
		"token":  true,
		"output": true,
		"json":   false,
	})); err != nil {
		return err
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		return errors.New("usage: relayfile read [WORKSPACE] PATH")
	}

	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	tokenValue := resolveToken(*token, creds)
	client, err := newAPIClient(resolveServer(*server, creds), tokenValue)
	if err != nil {
		return err
	}

	var workspaceID string
	var remotePath string
	if fs.NArg() == 1 {
		workspaceID, err = resolveWorkspaceIDWithToken("", tokenValue)
		remotePath = strings.TrimSpace(fs.Arg(0))
	} else {
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
		remotePath = strings.TrimSpace(fs.Arg(1))
	}
	if err != nil {
		return err
	}
	if remotePath == "" {
		return errors.New("path is required")
	}

	query := url.Values{}
	query.Set("path", remotePath)
	body, _, err := client.getBytes(context.Background(), fmt.Sprintf("/v1/workspaces/%s/fs/file?%s", url.PathEscape(workspaceID), query.Encode()))
	if err != nil {
		return err
	}
	if _, err := upsertWorkspace(workspaceID); err != nil {
		return err
	}
	if *jsonOutput {
		return writePrettyJSON(stdout, body)
	}

	var file readFileResponse
	if err := json.Unmarshal(body, &file); err != nil {
		return err
	}
	content := []byte(file.Content)
	if strings.EqualFold(strings.TrimSpace(file.Encoding), "base64") {
		decoded, err := base64.StdEncoding.DecodeString(file.Content)
		if err != nil {
			return fmt.Errorf("decode base64 content for %s: %w", remotePath, err)
		}
		content = decoded
	}
	if strings.TrimSpace(*output) == "" || strings.TrimSpace(*output) == "-" {
		_, err = stdout.Write(content)
		return err
	}
	outputPath := strings.TrimSpace(*output)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outputPath, content, 0o644)
}

func runSeed(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "relayfile server URL override")
	token := fs.String("token", "", "relayfile token override")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server": true,
		"token":  true,
	})); err != nil {
		return err
	}
	if fs.NArg() > 2 {
		return errors.New("usage: relayfile seed [WORKSPACE] [DIR]")
	}

	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	tokenValue := resolveToken(*token, creds)
	client, err := newAPIClient(resolveServer(*server, creds), tokenValue)
	if err != nil {
		return err
	}

	workspaceID := ""
	dir := "."
	switch fs.NArg() {
	case 0:
		workspaceID, err = resolveWorkspaceIDWithToken("", tokenValue)
	case 1:
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
	case 2:
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
		dir = fs.Arg(1)
	}
	if err != nil {
		return err
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	files, err := collectSeedFiles(root, stdout)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, "No files to seed")
		return nil
	}

	var response bulkWriteResponse
	if err := client.postJSON(context.Background(), fmt.Sprintf("/v1/workspaces/%s/fs/bulk", url.PathEscape(workspaceID)), bulkWriteRequest{Files: files}, &response); err != nil {
		return err
	}
	if _, err := upsertWorkspace(workspaceID); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Seeded %d files", response.Written)
	if response.ErrorCount > 0 {
		fmt.Fprintf(stdout, " with %d errors", response.ErrorCount)
	}
	fmt.Fprintln(stdout)
	for _, item := range response.Errors {
		fmt.Fprintf(stdout, "%s: %s (%s)\n", item.Path, item.Message, item.Code)
	}
	return nil
}

func runExport(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "relayfile server URL override")
	token := fs.String("token", "", "relayfile token override")
	format := fs.String("format", "json", "export format: tar, json, or patch")
	output := fs.String("output", "-", "output file path or - for stdout")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server": true,
		"token":  true,
		"format": true,
		"output": true,
	})); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: relayfile export [WORKSPACE] --format FORMAT [--output FILE]")
	}

	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	tokenValue := resolveToken(*token, creds)
	client, err := newAPIClient(resolveServer(*server, creds), tokenValue)
	if err != nil {
		return err
	}

	workspaceID, err := resolveWorkspaceIDWithToken("", tokenValue)
	if fs.NArg() == 1 {
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
	}
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/v1/workspaces/%s/fs/export?format=%s", url.PathEscape(workspaceID), url.QueryEscape(strings.ToLower(strings.TrimSpace(*format))))
	body, _, err := client.getBytes(context.Background(), path)
	if err != nil {
		return err
	}
	if _, err := upsertWorkspace(workspaceID); err != nil {
		return err
	}
	if strings.TrimSpace(*output) == "" || strings.TrimSpace(*output) == "-" {
		_, err = stdout.Write(body)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		return err
	}
	return os.WriteFile(*output, body, 0o644)
}

func runStatus(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "relayfile server URL override")
	token := fs.String("token", "", "relayfile token override")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server": true,
		"token":  true,
		"json":   false,
	})); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: relayfile status [WORKSPACE] [--json]")
	}

	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	tokenValue := resolveToken(*token, creds)
	client, err := newAPIClient(resolveServer(*server, creds), tokenValue)
	if err != nil {
		return err
	}

	workspaceID, err := resolveWorkspaceIDWithToken("", tokenValue)
	if fs.NArg() == 1 {
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
	}
	if err != nil {
		return err
	}
	status, err := fetchWorkspaceSyncStatus(client, workspaceID)
	if err != nil {
		return err
	}
	if _, err := upsertWorkspace(workspaceID); err != nil {
		return err
	}
	record, _ := workspaceRecordByID(workspaceID)
	snapshot := buildSyncStateSnapshot(status, workspaceID, defaultMountMode, defaultMountInterval, record.LocalDir, readDaemonPID(record.LocalDir), "")
	if *jsonOutput {
		return writeJSON(stdout, snapshot)
	}
	workspaceLabel := workspaceID
	if strings.TrimSpace(record.Name) != "" && record.Name != workspaceID {
		workspaceLabel = fmt.Sprintf("%s (%s)", workspaceID, record.Name)
	}
	fmt.Fprintf(stdout, "workspace %s   mode: %s   lag: %s\n", workspaceLabel, snapshot.Mode, formatLag(maxLagSeconds(status.Providers)))
	for _, provider := range status.Providers {
		lastEvent := "-"
		if provider.WatermarkTs != nil {
			lastEvent = humanizeRecentTime(strings.TrimSpace(*provider.WatermarkTs))
		}
		line := fmt.Sprintf("  %-12s %-8s lag %s", provider.Provider, provider.Status, formatLag(provider.LagSeconds))
		if lastEvent != "-" {
			line += "   last event " + lastEvent
		}
		if provider.LastError != nil && strings.TrimSpace(*provider.LastError) != "" {
			line += "   last error: " + strings.TrimSpace(*provider.LastError)
		}
		fmt.Fprintln(stdout, line)
	}
	if record.LocalDir != "" {
		fmt.Fprintf(stdout, "\nlocal mirror: %s\n", record.LocalDir)
		if pid := readDaemonPID(record.LocalDir); pid != 0 {
			fmt.Fprintf(stdout, "daemon: running (pid %d)\n", pid)
		} else {
			fmt.Fprintln(stdout, "daemon: not running")
		}
	}
	fmt.Fprintf(stdout, "\npending writebacks: %d    conflicts: %d    denied: %d\n", snapshot.PendingWriteback, snapshot.PendingConflicts, snapshot.DeniedPaths)
	return nil
}

func runStop(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{})); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: relayfile stop [WORKSPACE]")
	}
	record, err := resolveWorkspaceRecord(firstArg(fs))
	if err != nil {
		return err
	}
	pid := readDaemonPID(record.LocalDir)
	if pid == 0 {
		return fmt.Errorf("no running mount found for workspace %s", record.Name)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Stopped background mount for %s (pid %d)\n", record.Name, pid)
	return nil
}

func runLogs(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	lines := fs.Int("lines", 40, "number of lines to print")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"lines": true,
	})); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: relayfile logs [WORKSPACE] [--lines N]")
	}
	record, err := resolveWorkspaceRecord(firstArg(fs))
	if err != nil {
		return err
	}
	payload, err := os.ReadFile(mountLogFile(record.LocalDir))
	if err != nil {
		return err
	}
	linesOut := strings.Split(strings.TrimRight(string(payload), "\n"), "\n")
	if *lines > 0 && len(linesOut) > *lines {
		linesOut = linesOut[len(linesOut)-*lines:]
	}
	for _, line := range linesOut {
		fmt.Fprintln(stdout, line)
	}
	return nil
}

func runObserver(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("observer", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "relayfile server URL override")
	token := fs.String("token", "", "relayfile token override")
	observerURL := fs.String("url", envOrDefault("RELAYFILE_OBSERVER_URL", defaultObserverURL), "observer URL")
	noOpen := fs.Bool("no-open", false, "print the observer URL without opening a browser")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server":  true,
		"token":   true,
		"url":     true,
		"no-open": false,
	})); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: relayfile observer [WORKSPACE] [--no-open]")
	}

	creds, err := loadCredentials()
	if err != nil {
		if strings.TrimSpace(*token) == "" && strings.TrimSpace(os.Getenv("RELAYFILE_TOKEN")) == "" {
			return err
		}
		creds = credentials{}
	}
	tokenValue := resolveToken(*token, creds)
	if strings.TrimSpace(tokenValue) == "" {
		return errors.New("token is required; run relayfile login or pass --token")
	}
	serverValue := resolveServer(*server, creds)
	workspaceID, err := resolveWorkspaceIDWithToken("", tokenValue)
	if fs.NArg() == 1 {
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
	}
	if err != nil {
		return err
	}
	launchURL, err := buildObserverURL(*observerURL, serverValue, tokenValue, workspaceID)
	if err != nil {
		return err
	}
	if _, err := upsertWorkspace(workspaceID); err != nil {
		return err
	}
	if *noOpen {
		fmt.Fprintln(stdout, launchURL)
		return nil
	}
	fmt.Fprintf(stdout, "Opening observer for workspace %s\n", workspaceID)
	if err := openBrowser(launchURL); err != nil {
		return fmt.Errorf("open observer: %w (rerun with --no-open to print the URL)", err)
	}
	return nil
}

func buildObserverURL(observerURL, server, token, workspaceID string) (string, error) {
	observerURL = strings.TrimSpace(observerURL)
	if observerURL == "" {
		observerURL = defaultObserverURL
	}
	parsed, err := url.Parse(observerURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("observer URL must be absolute: %s", observerURL)
	}
	fragment := url.Values{}
	fragment.Set("baseUrl", strings.TrimRight(strings.TrimSpace(server), "/"))
	fragment.Set("token", strings.TrimSpace(token))
	fragment.Set("workspaceId", strings.TrimSpace(workspaceID))
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String() + "#" + fragment.Encode(), nil
}

func openBrowser(targetURL string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", targetURL).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", targetURL).Start()
	default:
		return exec.Command("xdg-open", targetURL).Start()
	}
}

func newAPIClient(server, token string) (*apiClient, error) {
	server = strings.TrimSpace(server)
	token = strings.TrimSpace(token)
	if server == "" {
		server = defaultServerURL
	}
	if token == "" {
		return nil, errors.New("token is required; run relayfile login or pass --token")
	}
	return &apiClient{
		baseURL:    strings.TrimRight(server, "/"),
		token:      token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (c *apiClient) getJSON(ctx context.Context, path string, out any) error {
	body, _, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, out)
}

func (c *apiClient) postJSON(ctx context.Context, path string, input, out any) error {
	bodyBytes, err := json.Marshal(input)
	if err != nil {
		return err
	}
	body, _, err := c.do(ctx, http.MethodPost, path, bodyBytes)
	if err != nil {
		return err
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, out)
}

func (c *apiClient) getBytes(ctx context.Context, path string) ([]byte, string, error) {
	return c.do(ctx, http.MethodGet, path, nil)
}

func (c *apiClient) do(ctx context.Context, method, path string, body []byte) ([]byte, string, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Correlation-Id", correlationID())
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return payload, resp.Header.Get("Content-Type"), nil
	}

	var errPayload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &errPayload)
	}
	if errPayload.Message == "" {
		errPayload.Message = strings.TrimSpace(string(payload))
	}
	return nil, "", &apiError{
		StatusCode: resp.StatusCode,
		Code:       errPayload.Code,
		Message:    errPayload.Message,
	}
}

func writePrettyJSON(stdout io.Writer, body []byte) error {
	var out bytes.Buffer
	if err := json.Indent(&out, body, "", "  "); err != nil {
		_, writeErr := stdout.Write(body)
		return writeErr
	}
	out.WriteByte('\n')
	_, err := stdout.Write(out.Bytes())
	return err
}

func collectSeedFiles(root string, stdout io.Writer) ([]bulkWriteFile, error) {
	paths, err := collectSeedPaths(root)
	if err != nil {
		return nil, err
	}
	files := make([]bulkWriteFile, 0, len(paths))
	for idx, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		entry := bulkWriteFile{
			Path:        "/" + filepath.ToSlash(rel),
			ContentType: detectContentType(path, content),
		}
		if utf8.Valid(content) {
			entry.Content = string(content)
		} else {
			entry.Content = base64.StdEncoding.EncodeToString(content)
			entry.Encoding = "base64"
		}
		files = append(files, entry)
		if stdout != nil {
			fmt.Fprintf(stdout, "\rSeeding %d/%d files...", idx+1, len(paths))
		}
	}
	if stdout != nil && len(paths) > 0 {
		fmt.Fprintln(stdout)
	}
	return files, nil
}

func collectSeedPaths(root string) ([]string, error) {
	paths := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func detectContentType(path string, content []byte) string {
	extType := strings.TrimSpace(mime.TypeByExtension(strings.ToLower(filepath.Ext(path))))
	if extType != "" {
		return extType
	}
	if len(content) == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(content)
}

func promptLine(stdin io.Reader, stdout io.Writer, prompt string) (string, error) {
	if _, err := io.WriteString(stdout, prompt); err != nil {
		return "", err
	}
	reader := bufio.NewReader(stdin)
	value, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func configDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return configDirName
	}
	return filepath.Join(home, configDirName)
}

func credentialsPath() string {
	return filepath.Join(configDir(), "credentials.json")
}

func cloudCredentialsPath() string {
	return filepath.Join(configDir(), "cloud-credentials.json")
}

func workspacesPath() string {
	return filepath.Join(configDir(), "workspaces.json")
}

func ensureConfigDir() error {
	return os.MkdirAll(configDir(), 0o755)
}

func saveCredentials(creds credentials) error {
	if err := ensureConfigDir(); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomically(credentialsPath(), payload, 0o600)
}

func saveCloudCredentials(creds cloudCredentials) error {
	if err := ensureConfigDir(); err != nil {
		return err
	}
	if strings.TrimSpace(creds.APIURL) == "" {
		creds.APIURL = defaultCloudAPIURL
	}
	if strings.TrimSpace(creds.UpdatedAt) == "" {
		creds.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	payload, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomically(cloudCredentialsPath(), payload, 0o600)
}

func loadCredentials() (credentials, error) {
	var creds credentials
	payload, err := os.ReadFile(credentialsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return creds, fmt.Errorf("credentials not found at %s; run relayfile login", credentialsPath())
		}
		return creds, err
	}
	if err := json.Unmarshal(payload, &creds); err != nil {
		return creds, fmt.Errorf("parse %s: %w", credentialsPath(), err)
	}
	return creds, nil
}

func loadWorkspaceCatalog() (workspaceCatalog, error) {
	var catalog workspaceCatalog
	payload, err := os.ReadFile(workspacesPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return workspaceCatalog{}, nil
		}
		return catalog, err
	}
	if err := json.Unmarshal(payload, &catalog); err != nil {
		return catalog, fmt.Errorf("parse %s: %w", workspacesPath(), err)
	}
	sort.Slice(catalog.Workspaces, func(i, j int) bool {
		return catalog.Workspaces[i].Name < catalog.Workspaces[j].Name
	})
	return catalog, nil
}

func saveWorkspaceCatalog(catalog workspaceCatalog) error {
	if err := ensureConfigDir(); err != nil {
		return err
	}
	sort.Slice(catalog.Workspaces, func(i, j int) bool {
		return catalog.Workspaces[i].Name < catalog.Workspaces[j].Name
	})
	payload, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomically(workspacesPath(), payload, 0o644)
}

func setDefaultWorkspace(name string) (workspaceRecord, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return workspaceRecord{}, errors.New("workspace name is required")
	}

	record, err := upsertWorkspace(name)
	if err != nil {
		return workspaceRecord{}, err
	}
	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return workspaceRecord{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for i := range catalog.Workspaces {
		if catalog.Workspaces[i].Name == record.Name {
			if catalog.Workspaces[i].ID == "" {
				catalog.Workspaces[i].ID = record.Name
			}
			catalog.Workspaces[i].LastUsedAt = now
			record = catalog.Workspaces[i]
			break
		}
	}
	catalog.Default = record.Name
	if err := saveWorkspaceCatalog(catalog); err != nil {
		return workspaceRecord{}, err
	}
	return record, nil
}

func upsertWorkspace(name string) (workspaceRecord, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return workspaceRecord{}, errors.New("workspace name is required")
	}

	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return workspaceRecord{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for i := range catalog.Workspaces {
		if catalog.Workspaces[i].Name == name {
			if catalog.Workspaces[i].ID == "" {
				catalog.Workspaces[i].ID = name
			}
			catalog.Workspaces[i].LastUsedAt = now
			if err := saveWorkspaceCatalog(catalog); err != nil {
				return workspaceRecord{}, err
			}
			return catalog.Workspaces[i], nil
		}
	}

	record := workspaceRecord{
		Name:       name,
		ID:         name,
		CreatedAt:  now,
		LastUsedAt: now,
	}
	catalog.Workspaces = append(catalog.Workspaces, record)
	if strings.TrimSpace(catalog.Default) == "" {
		catalog.Default = name
	}
	if err := saveWorkspaceCatalog(catalog); err != nil {
		return workspaceRecord{}, err
	}
	return record, nil
}

func removeWorkspace(name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, errors.New("workspace name is required")
	}

	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return false, err
	}
	filtered := catalog.Workspaces[:0]
	removed := false
	for _, workspace := range catalog.Workspaces {
		if workspace.Name == name {
			removed = true
			continue
		}
		filtered = append(filtered, workspace)
	}
	if !removed {
		return false, nil
	}
	catalog.Workspaces = filtered
	if catalog.Default == name {
		catalog.Default = ""
		if len(catalog.Workspaces) > 0 {
			catalog.Default = catalog.Workspaces[0].Name
		}
	}
	return true, saveWorkspaceCatalog(catalog)
}

func upsertWorkspaceRecord(name, id string) (workspaceRecord, error) {
	name = strings.TrimSpace(name)
	id = strings.TrimSpace(id)
	if name == "" {
		return workspaceRecord{}, errors.New("workspace name is required")
	}
	if id == "" {
		id = name
	}

	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return workspaceRecord{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for i := range catalog.Workspaces {
		if catalog.Workspaces[i].Name == name || catalog.Workspaces[i].ID == id {
			catalog.Workspaces[i].Name = name
			catalog.Workspaces[i].ID = id
			catalog.Workspaces[i].LastUsedAt = now
			if strings.TrimSpace(catalog.Workspaces[i].CreatedAt) == "" {
				catalog.Workspaces[i].CreatedAt = now
			}
			if err := saveWorkspaceCatalog(catalog); err != nil {
				return workspaceRecord{}, err
			}
			return catalog.Workspaces[i], nil
		}
	}

	record := workspaceRecord{
		Name:       name,
		ID:         id,
		CreatedAt:  now,
		LastUsedAt: now,
	}
	catalog.Workspaces = append(catalog.Workspaces, record)
	if strings.TrimSpace(catalog.Default) == "" {
		catalog.Default = name
	}
	if err := saveWorkspaceCatalog(catalog); err != nil {
		return workspaceRecord{}, err
	}
	return record, nil
}

func upsertWorkspaceDetails(record workspaceRecord) (workspaceRecord, error) {
	record.Name = strings.TrimSpace(record.Name)
	record.ID = strings.TrimSpace(record.ID)
	if record.Name == "" {
		return workspaceRecord{}, errors.New("workspace name is required")
	}
	if record.ID == "" {
		record.ID = record.Name
	}
	if record.CreatedAt == "" {
		record.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	record.LastUsedAt = time.Now().UTC().Format(time.RFC3339)
	record.LocalDir = strings.TrimSpace(record.LocalDir)
	record.Server = strings.TrimRight(strings.TrimSpace(record.Server), "/")
	record.CloudAPIURL = strings.TrimRight(strings.TrimSpace(record.CloudAPIURL), "/")
	record.AgentName = strings.TrimSpace(record.AgentName)
	if len(record.Scopes) == 0 {
		record.Scopes = append([]string(nil), defaultJoinScopes...)
	}

	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return workspaceRecord{}, err
	}
	for i := range catalog.Workspaces {
		current := catalog.Workspaces[i]
		if current.Name == record.Name || (record.ID != "" && current.ID == record.ID) {
			if record.CreatedAt == "" {
				record.CreatedAt = current.CreatedAt
			}
			catalog.Workspaces[i] = mergeWorkspaceRecords(current, record)
			if err := saveWorkspaceCatalog(catalog); err != nil {
				return workspaceRecord{}, err
			}
			return catalog.Workspaces[i], nil
		}
	}
	catalog.Workspaces = append(catalog.Workspaces, record)
	if strings.TrimSpace(catalog.Default) == "" {
		catalog.Default = record.Name
	}
	if err := saveWorkspaceCatalog(catalog); err != nil {
		return workspaceRecord{}, err
	}
	return record, nil
}

func mergeWorkspaceRecords(current, update workspaceRecord) workspaceRecord {
	merged := current
	if update.Name != "" {
		merged.Name = update.Name
	}
	if update.ID != "" {
		merged.ID = update.ID
	}
	if update.CreatedAt != "" {
		merged.CreatedAt = update.CreatedAt
	}
	if update.LastUsedAt != "" {
		merged.LastUsedAt = update.LastUsedAt
	}
	if update.LocalDir != "" {
		merged.LocalDir = update.LocalDir
	}
	if update.Server != "" {
		merged.Server = update.Server
	}
	if update.CloudAPIURL != "" {
		merged.CloudAPIURL = update.CloudAPIURL
	}
	if update.AgentName != "" {
		merged.AgentName = update.AgentName
	}
	if len(update.Scopes) > 0 {
		merged.Scopes = append([]string(nil), update.Scopes...)
	}
	return merged
}

func workspaceRecordByName(name string) (workspaceRecord, bool) {
	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return workspaceRecord{}, false
	}
	name = strings.TrimSpace(name)
	for _, record := range catalog.Workspaces {
		if record.Name == name {
			return record, true
		}
	}
	return workspaceRecord{}, false
}

func workspaceRecordByID(id string) (workspaceRecord, bool) {
	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return workspaceRecord{}, false
	}
	id = strings.TrimSpace(id)
	for _, record := range catalog.Workspaces {
		if record.ID == id {
			return record, true
		}
	}
	return workspaceRecord{}, false
}

func resolveWorkspaceRecord(nameOrID string) (workspaceRecord, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	if nameOrID != "" {
		if record, ok := workspaceRecordByName(nameOrID); ok {
			return record, nil
		}
		if record, ok := workspaceRecordByID(nameOrID); ok {
			return record, nil
		}
		return workspaceRecord{}, fmt.Errorf("workspace %q not found in %s", nameOrID, workspacesPath())
	}
	workspaceID, err := resolveWorkspaceIDWithToken("", "")
	if err != nil {
		return workspaceRecord{}, err
	}
	if record, ok := workspaceRecordByID(workspaceID); ok {
		return record, nil
	}
	return workspaceRecord{Name: workspaceID, ID: workspaceID}, nil
}

func resolveWorkspaceIDWithToken(value, token string) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" {
		if id, ok := catalogWorkspaceID(value); ok {
			return id, nil
		}
		return value, nil
	}

	if workspaceID := strings.TrimSpace(os.Getenv("RELAYFILE_WORKSPACE")); workspaceID != "" {
		if id, ok := catalogWorkspaceID(workspaceID); ok {
			return id, nil
		}
		return workspaceID, nil
	}

	if workspaceID := workspaceIDFromToken(token); workspaceID != "" {
		if id, ok := catalogWorkspaceID(workspaceID); ok {
			return id, nil
		}
		return workspaceID, nil
	}

	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return "", err
	}
	defaultName := strings.TrimSpace(catalog.Default)
	if defaultName != "" {
		if id, ok := catalogWorkspaceIDFromCatalog(catalog, defaultName); ok {
			return id, nil
		}
		return defaultName, nil
	}
	return "", errors.New("workspace is required; pass WORKSPACE, set RELAYFILE_WORKSPACE, or run relayfile workspace use NAME")
}

func catalogWorkspaceID(name string) (string, bool) {
	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return "", false
	}
	return catalogWorkspaceIDFromCatalog(catalog, name)
}

func catalogWorkspaceIDFromCatalog(catalog workspaceCatalog, name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	for _, workspace := range catalog.Workspaces {
		if workspace.Name == name || workspace.ID == name {
			if strings.TrimSpace(workspace.ID) != "" {
				return strings.TrimSpace(workspace.ID), true
			}
			return strings.TrimSpace(workspace.Name), true
		}
	}
	return "", false
}

func workspaceCatalogNames(catalog workspaceCatalog, token string) []string {
	set := map[string]struct{}{}
	names := make([]string, 0, len(catalog.Workspaces)+3)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			if _, ok := set[value]; ok {
				return
			}
			set[value] = struct{}{}
			names = append(names, value)
		}
	}
	add(os.Getenv("RELAYFILE_WORKSPACE"))
	add(workspaceIDFromToken(token))
	add(catalog.Default)

	localNames := make([]string, 0, len(catalog.Workspaces))
	for _, workspace := range catalog.Workspaces {
		name := strings.TrimSpace(workspace.Name)
		if name == "" {
			continue
		}
		if _, ok := set[name]; ok {
			continue
		}
		localNames = append(localNames, name)
	}
	sort.Strings(localNames)
	names = append(names, localNames...)
	return names
}

func workspaceIDFromToken(token string) string {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
	}
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	for _, key := range []string{"workspace_id", "wks"} {
		if value, ok := claims[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func readCloudCredentialsFromQuery(values url.Values, fallbackAPIURL string) (cloudCredentials, error) {
	accessToken := strings.TrimSpace(values.Get("access_token"))
	refreshToken := strings.TrimSpace(values.Get("refresh_token"))
	accessTokenExpiresAt := strings.TrimSpace(values.Get("access_token_expires_at"))
	if accessToken == "" {
		return cloudCredentials{}, errors.New("cloud login callback missing access_token")
	}
	if refreshToken == "" {
		return cloudCredentials{}, errors.New("cloud login callback missing refresh_token")
	}
	if accessTokenExpiresAt == "" {
		return cloudCredentials{}, errors.New("cloud login callback missing access_token_expires_at")
	}
	apiURL := strings.TrimRight(strings.TrimSpace(values.Get("api_url")), "/")
	if apiURL == "" {
		apiURL = strings.TrimRight(strings.TrimSpace(fallbackAPIURL), "/")
	}
	return cloudCredentials{
		APIURL:                apiURL,
		AccessToken:           accessToken,
		RefreshToken:          refreshToken,
		AccessTokenExpiresAt:  accessTokenExpiresAt,
		RefreshTokenExpiresAt: strings.TrimSpace(values.Get("refresh_token_expires_at")),
		UpdatedAt:             time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func fetchWorkspaceSyncStatus(client *apiClient, workspaceID string) (syncStatusResponse, error) {
	var status syncStatusResponse
	if err := client.getJSON(context.Background(), fmt.Sprintf("/v1/workspaces/%s/sync/status", url.PathEscape(workspaceID)), &status); err != nil {
		return syncStatusResponse{}, err
	}
	return status, nil
}

func syncProviderByName(status syncStatusResponse, provider string) (syncProviderStatus, bool) {
	provider = normalizeProviderID(provider)
	for _, entry := range status.Providers {
		if normalizeProviderID(entry.Provider) == provider {
			return entry, true
		}
	}
	return syncProviderStatus{}, false
}

func providerReadyForMirror(client *apiClient, workspaceID, provider string, status syncProviderStatus) bool {
	switch status.Status {
	case "ready":
		return true
	case "syncing":
		if status.LagSeconds >= 30 {
			return false
		}
		body, _, err := client.getBytes(context.Background(), fmt.Sprintf("/v1/workspaces/%s/fs/tree?path=%s&depth=1", url.PathEscape(workspaceID), url.QueryEscape("/"+providerRootDir(provider))))
		if err != nil {
			return false
		}
		var tree treeResponse
		if err := json.Unmarshal(body, &tree); err != nil {
			return false
		}
		return len(tree.Entries) > 0
	default:
		return false
	}
}

func buildSyncStateSnapshot(status syncStatusResponse, workspaceID, mode string, interval time.Duration, localDir string, pid int, stallReason string) syncStateFile {
	snapshot := syncStateFile{
		WorkspaceID:      workspaceID,
		Mode:             defaultIfBlank(mode, defaultMountMode),
		IntervalMs:       interval.Milliseconds(),
		PendingWriteback: countDirtyTrackedFiles(localDir),
		PendingConflicts: countFilesInDir(filepath.Join(localDir, ".relay", "conflicts")),
		DeniedPaths:      countLines(filepath.Join(localDir, ".relay", "permissions-denied.log")),
		StallReason:      stallReason,
	}
	if pid != 0 {
		snapshot.Daemon = &syncStateDaemon{
			PID:     pid,
			LogFile: mountLogFile(localDir),
			PIDFile: mountPIDFile(localDir),
		}
	}
	providers := make([]syncStateProvider, 0, len(status.Providers))
	var lastEvent string
	for _, provider := range status.Providers {
		item := syncStateProvider{
			Provider:        provider.Provider,
			Status:          provider.Status,
			LagSeconds:      provider.LagSeconds,
			DeadLetteredOps: provider.DeadLetteredOps,
		}
		if provider.LastError != nil {
			item.LastError = strings.TrimSpace(*provider.LastError)
		}
		if provider.WatermarkTs != nil {
			item.LastEventAt = strings.TrimSpace(*provider.WatermarkTs)
			if item.LastEventAt > lastEvent {
				lastEvent = item.LastEventAt
			}
		}
		providers = append(providers, item)
	}
	snapshot.Providers = providers
	snapshot.LastEventAt = lastEvent
	return snapshot
}

func writeMirrorStateFile(localDir string, snapshot syncStateFile) error {
	if localDir == "" {
		return nil
	}
	if err := ensureMirrorLayout(localDir); err != nil {
		return err
	}
	snapshot.LastReconcileAt = time.Now().UTC().Format(time.RFC3339)
	payload, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomically(filepath.Join(localDir, ".relay", "state.json"), payload, 0o644)
}

func countDirtyTrackedFiles(localDir string) int {
	if localDir == "" {
		return 0
	}
	var state struct {
		Files map[string]struct {
			Dirty bool `json:"dirty"`
		} `json:"files"`
	}
	payload, err := os.ReadFile(filepath.Join(localDir, ".relayfile-mount-state.json"))
	if err != nil {
		return 0
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		return 0
	}
	count := 0
	for _, tracked := range state.Files {
		if tracked.Dirty {
			count++
		}
	}
	return count
}

func countFilesInDir(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			if entry.Name() == "resolved" {
				continue
			}
			count += countFilesInDir(filepath.Join(dir, entry.Name()))
			continue
		}
		if !entry.IsDir() {
			count++
		}
	}
	return count
}

func countLines(path string) int {
	payload, err := os.ReadFile(path)
	if err != nil || len(payload) == 0 {
		return 0
	}
	return bytes.Count(payload, []byte{'\n'})
}

func mountPIDFile(localDir string) string {
	return filepath.Join(localDir, ".relay", "mount.pid")
}

func mountLogFile(localDir string) string {
	return filepath.Join(localDir, ".relay", "mount.log")
}

func writeDaemonPIDState(path string, state daemonPIDState) error {
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomically(path, payload, 0o644)
}

func readDaemonPID(localDir string) int {
	if localDir == "" {
		return 0
	}
	payload, err := os.ReadFile(mountPIDFile(localDir))
	if err != nil {
		return 0
	}
	var state daemonPIDState
	if json.Unmarshal(payload, &state) == nil && state.PID > 0 {
		return state.PID
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(payload)))
	if err != nil {
		return 0
	}
	return pid
}

func rotateLogFile(path string) error {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Size() < 10*1024*1024 {
		return nil
	}
	_ = os.Remove(path + ".3")
	for idx := 2; idx >= 1; idx-- {
		src := fmt.Sprintf("%s.%d", path, idx)
		dst := fmt.Sprintf("%s.%d", path, idx+1)
		if _, err := os.Stat(src); err == nil {
			if err := os.Rename(src, dst); err != nil {
				return err
			}
		}
	}
	return os.Rename(path, path+".1")
}

func integrationConnectionPath(localDir, provider string) string {
	return filepath.Join(localDir, ".relay", "integrations", provider+".json")
}

func saveIntegrationConnection(localDir string, state integrationConnectionState) error {
	if localDir == "" || strings.TrimSpace(state.Provider) == "" {
		return nil
	}
	if err := ensureMirrorLayout(localDir); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomically(integrationConnectionPath(localDir, state.Provider), payload, 0o644)
}

func loadSavedConnectionID(localDir, provider string) string {
	if localDir == "" || strings.TrimSpace(provider) == "" {
		return ""
	}
	payload, err := os.ReadFile(integrationConnectionPath(localDir, provider))
	if err != nil {
		return ""
	}
	var state integrationConnectionState
	if err := json.Unmarshal(payload, &state); err != nil {
		return ""
	}
	return strings.TrimSpace(state.ConnectionID)
}

func markProviderDisconnected(localDir, provider string) error {
	if localDir == "" {
		return nil
	}
	_ = os.RemoveAll(filepath.Join(localDir, providerRootDir(provider)))
	if err := ensureMirrorLayout(localDir); err != nil {
		return err
	}
	marker := map[string]string{
		"provider":       provider,
		"disconnectedAt": time.Now().UTC().Format(time.RFC3339),
	}
	payload, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := writeFileAtomically(filepath.Join(localDir, ".relay", "disconnected", provider+".json"), payload, 0o644); err != nil {
		return err
	}
	_ = os.Remove(integrationConnectionPath(localDir, provider))
	return nil
}

func providerRootDir(provider string) string {
	provider = normalizeProviderID(provider)
	switch provider {
	case "slack-sage":
		return "slack"
	default:
		return provider
	}
}

func writeJSON(w io.Writer, value any) error {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	_, err = w.Write(payload)
	return err
}

func writeFileAtomically(path string, payload []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func parseRFC3339(value string) (time.Time, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func defaultIfBlank(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func firstArg(fs *flag.FlagSet) string {
	if fs.NArg() == 0 {
		return ""
	}
	return fs.Arg(0)
}

func formatLag(seconds int) string {
	if seconds <= 0 {
		return "0s"
	}
	return (time.Duration(seconds) * time.Second).String()
}

func humanizeRecentTime(value string) string {
	parsed, ok := parseRFC3339(value)
	if !ok {
		return value
	}
	ago := time.Since(parsed)
	if ago < time.Second {
		return "just now"
	}
	return fmt.Sprintf("%s ago", ago.Round(time.Second).String())
}

func maxLagSeconds(providers []syncProviderStatus) int {
	maxLag := 0
	for _, provider := range providers {
		if provider.LagSeconds > maxLag {
			maxLag = provider.LagSeconds
		}
	}
	return maxLag
}

func syncerClient(syncer *mountsync.Syncer) (*mountsync.HTTPClient, bool) {
	return syncer.HTTPClient()
}

func relayfileTokenNeedsRefresh(token string) bool {
	claims, ok := parseJWTClaims(token)
	if !ok {
		return false
	}
	expUnix, ok := claims["exp"].(float64)
	if !ok {
		return false
	}
	exp := time.Unix(int64(expUnix), 0)
	threshold := 5 * time.Minute
	if iatUnix, ok := claims["iat"].(float64); ok {
		issuedAt := time.Unix(int64(iatUnix), 0)
		lifetime := exp.Sub(issuedAt)
		if lifetime > 0 {
			candidate := lifetime / 10
			if candidate > threshold {
				threshold = candidate
			}
		}
	}
	return !time.Now().UTC().Before(exp.Add(-threshold))
}

func parseJWTClaims(token string) (map[string]any, bool) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
	}
	if err != nil {
		return nil, false
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, false
	}
	return claims, true
}

func isMountAuthError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *mountsync.HTTPError
	return errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden)
}

func buildCloudURL(baseURL, path string) (*url.URL, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultCloudAPIURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("cloud API URL must be absolute: %s", baseURL)
	}
	if !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}
	return parsed.ResolveReference(&url.URL{Path: strings.TrimLeft(path, "/")}), nil
}

func randomURLSafe(bytesCount int) (string, error) {
	if bytesCount <= 0 {
		bytesCount = 24
	}
	buf := make([]byte, bytesCount)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func remoteWorkspaceNames(response adminWorkspaceList) []string {
	set := map[string]struct{}{}
	for _, workspaceID := range response.WorkspaceIDs {
		workspaceID = strings.TrimSpace(workspaceID)
		if workspaceID != "" {
			set[workspaceID] = struct{}{}
		}
	}
	if len(set) == 0 && len(response.Workspaces) > 0 {
		var workspaceIDs []string
		if err := json.Unmarshal(response.Workspaces, &workspaceIDs); err == nil {
			for _, workspaceID := range workspaceIDs {
				workspaceID = strings.TrimSpace(workspaceID)
				if workspaceID != "" {
					set[workspaceID] = struct{}{}
				}
			}
		} else {
			var workspaceMap map[string]json.RawMessage
			if err := json.Unmarshal(response.Workspaces, &workspaceMap); err == nil {
				for workspaceID := range workspaceMap {
					workspaceID = strings.TrimSpace(workspaceID)
					if workspaceID != "" {
						set[workspaceID] = struct{}{}
					}
				}
			}
		}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func normalizeFlagArgs(args []string, flags map[string]bool) []string {
	if len(args) == 0 {
		return nil
	}
	flagArgs := make([]string, 0, len(args))
	positionalArgs := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionalArgs = append(positionalArgs, args[i+1:]...)
			break
		}
		name, ok, hasInlineValue := parseFlagName(arg)
		if !ok {
			positionalArgs = append(positionalArgs, arg)
			continue
		}
		flagArgs = append(flagArgs, arg)
		if takesValue, known := flags[name]; known && takesValue && !hasInlineValue && i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	return append(flagArgs, positionalArgs...)
}

func parseFlagName(arg string) (name string, ok bool, hasInlineValue bool) {
	if !strings.HasPrefix(arg, "-") || arg == "-" {
		return "", false, false
	}
	trimmed := strings.TrimLeft(arg, "-")
	if trimmed == "" {
		return "", false, false
	}
	name = trimmed
	if idx := strings.IndexByte(trimmed, '='); idx >= 0 {
		name = trimmed[:idx]
		hasInlineValue = true
	}
	return name, true, hasInlineValue
}

func resolveServer(flagValue string, creds credentials) string {
	if value := strings.TrimSpace(flagValue); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("RELAYFILE_SERVER")); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("RELAYFILE_BASE_URL")); value != "" {
		return value
	}
	if value := strings.TrimSpace(creds.Server); value != "" {
		return value
	}
	return defaultServerURL
}

func resolveToken(flagValue string, creds credentials) string {
	if value := strings.TrimSpace(flagValue); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("RELAYFILE_TOKEN")); value != "" {
		return value
	}
	return strings.TrimSpace(creds.Token)
}

func envOrDefault(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func durationEnv(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("invalid %s=%q, using fallback %s", name, raw, fallback.String())
		return fallback
	}
	return value
}

func floatEnv(name string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		log.Printf("invalid %s=%q, using fallback %f", name, raw, fallback)
		return fallback
	}
	return value
}

func boolEnv(name string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		log.Printf("invalid %s=%q, using fallback %t", name, raw, fallback)
		return fallback
	}
	return value
}

func boolPtr(value bool) *bool {
	return &value
}

func clampJitterRatio(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func jitteredIntervalWithSample(base time.Duration, jitterRatio, sample float64) time.Duration {
	if base <= 0 {
		return 0
	}
	jitterRatio = clampJitterRatio(jitterRatio)
	if jitterRatio == 0 {
		return base
	}
	if sample < 0 {
		sample = 0
	} else if sample > 1 {
		sample = 1
	}
	factor := 1 + ((sample*2)-1)*jitterRatio
	if factor < 0 {
		factor = 0
	}
	delay := time.Duration(float64(base) * factor)
	if delay < time.Millisecond {
		return time.Millisecond
	}
	return delay
}

func spawnBackgroundMountProcess(originalArgs []string, localDir, pidFile, logFile string) error {
	if err := ensureMirrorLayout(localDir); err != nil {
		return err
	}
	if err := rotateLogFile(logFile); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	filteredArgs := make([]string, 0, len(originalArgs)+5)
	for i := 0; i < len(originalArgs); i++ {
		arg := originalArgs[i]
		if arg == "--background" || arg == "-background" {
			continue
		}
		if strings.HasPrefix(arg, "--background=") || strings.HasPrefix(arg, "-background=") {
			continue
		}
		filteredArgs = append(filteredArgs, arg)
	}
	childArgs := append([]string{"mount"}, filteredArgs...)
	childArgs = append(childArgs, "--daemonized", "--pid-file", pidFile, "--log-file", logFile)
	logHandle, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logHandle.Close()
	cmd := exec.Command(executable, childArgs...)
	cmd.Stdout = logHandle
	cmd.Stderr = logHandle
	if err := configureDetachedProcess(cmd); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := cmd.Process.Release(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Mirror started in background at %s. Logs: %s\n", localDir, logFile)
	return nil
}

func runMountLoop(rootCtx context.Context, syncer *mountsync.Syncer, localDir, workspaceID, serverURL string, timeout, interval time.Duration, intervalJitter float64, websocketEnabled, once, daemonized bool, pidFile, logFile string) error {
	httpClient, _ := syncerClient(syncer)
	record, _ := workspaceRecordByID(workspaceID)
	if record.ID == "" {
		record.ID = workspaceID
	}
	if record.Name == "" {
		record.Name = workspaceID
	}
	lastSuccess := time.Now()
	stallReason := ""

	refreshMountAuth := func(force bool) error {
		if httpClient == nil {
			return nil
		}
		if !force && !relayfileTokenNeedsRefresh(httpClient.Token()) {
			return nil
		}
		cloudCreds, err := loadCloudCredentials()
		if err != nil {
			return nil
		}
		cloudCreds, err = refreshCloudCredentialsIfNeeded(cloudCreds)
		if err != nil {
			log.Printf("cloud session refresh failed: %v", err)
			return nil
		}
		joined, err := joinWorkspaceViaCloud(cloudCreds, workspaceID, record.AgentName, record.Scopes)
		if err != nil {
			return err
		}
		httpClient.SetToken(joined.Token)
		syncer.ResetWebSocket()
		if err := saveCredentials(credentials{
			Server:    strings.TrimRight(joined.RelayfileURL, "/"),
			Token:     joined.Token,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			return err
		}
		record.Server = strings.TrimRight(joined.RelayfileURL, "/")
		record.CloudAPIURL = cloudCreds.APIURL
		if _, err := upsertWorkspaceDetails(record); err != nil {
			return err
		}
		return nil
	}

	withAuthRefresh := func(operation func(context.Context) error) error {
		if err := refreshMountAuth(false); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(rootCtx, timeout)
		defer cancel()
		err := operation(ctx)
		if isMountAuthError(err) {
			if refreshErr := refreshMountAuth(true); refreshErr != nil {
				return refreshErr
			}
			retryCtx, retryCancel := context.WithTimeout(rootCtx, timeout)
			defer retryCancel()
			return operation(retryCtx)
		}
		return err
	}

	writeSnapshot := func() {
		if httpClient == nil {
			return
		}
		client, err := newAPIClient(serverURL, httpClient.Token())
		if err != nil {
			return
		}
		status, err := fetchWorkspaceSyncStatus(client, workspaceID)
		if err != nil {
			return
		}
		_ = writeMirrorStateFile(localDir, buildSyncStateSnapshot(status, workspaceID, defaultMountMode, interval, localDir, readDaemonPID(localDir), stallReason))
	}

	runCycle := func(reconcile bool) error {
		err := withAuthRefresh(func(ctx context.Context) error {
			if reconcile {
				return syncer.Reconcile(ctx)
			}
			return syncer.SyncOnce(ctx)
		})
		if err != nil {
			stallReason = err.Error()
			log.Printf("mount sync cycle failed: %v", err)
			writeSnapshot()
			return err
		}
		stallReason = ""
		lastSuccess = time.Now()
		log.Printf("mount sync cycle completed")
		writeSnapshot()
		return nil
	}

	log.Printf("Mirror started at %s. Sync interval %s ±%.0f%%. Type 'relayfile status' for live state.", localDir, interval.String(), intervalJitter*100)
	initialErr := runCycle(true)
	if once {
		return initialErr
	}

	watcher, err := mountsync.NewFileWatcher(localDir, func(relativePath string, op fsnotify.Op) {
		if err := withAuthRefresh(func(ctx context.Context) error {
			return syncer.HandleLocalChange(ctx, relativePath, op)
		}); err != nil {
			log.Printf("mount local change failed: %v", err)
		}
		writeSnapshot()
	})
	if err != nil {
		return fmt.Errorf("create file watcher: %w", err)
	}
	if err := watcher.Start(rootCtx); err != nil {
		_ = watcher.Close()
		return fmt.Errorf("start file watcher: %w", err)
	}
	defer watcher.Close()

	timer := time.NewTimer(jitteredIntervalWithSample(interval, intervalJitter, mathrand.Float64()))
	defer timer.Stop()
	cycle := 0
	for {
		select {
		case <-rootCtx.Done():
			log.Printf("mount sync stopping: %v", rootCtx.Err())
			writeSnapshot()
			return nil
		case <-timer.C:
			cycle++
			reconcile := !websocketEnabled || cycle%websocketReconcileEvery == 0
			if reconcile {
				_ = runCycle(true)
			}
			if time.Since(lastSuccess) >= 10*time.Minute {
				stallReason = "no successful reconcile for 10m"
				log.Printf("mount stalled: %s", stallReason)
				writeSnapshot()
			}
			timer.Reset(jitteredIntervalWithSample(interval, intervalJitter, mathrand.Float64()))
		}
	}
}

func correlationID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return "corr_" + hex.EncodeToString(buf[:])
	}
	return fmt.Sprintf("corr_%d", time.Now().UnixNano())
}
