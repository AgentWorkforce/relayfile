package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
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
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/agentworkforce/relayfile/internal/delegatedauth"
	"github.com/agentworkforce/relayfile/internal/mountsync"
	"github.com/agentworkforce/relayfile/internal/relayfile"
	"github.com/agentworkforce/relayfile/internal/writeback"
	"github.com/fsnotify/fsnotify"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

const (
	defaultServerURL        = "https://file.agentrelay.com"
	defaultCloudAPIURL      = "https://agentrelay.com/cloud"
	defaultObserverURL      = "https://agentrelay.com/observer/file"
	minAgentRelayCLIVersion = "8.7.0"
	configDirName           = ".relayfile"
	websocketReconcileEvery = 10
	defaultMountMode        = "poll"
	defaultMountInterval    = 30 * time.Second
	minMountPollInterval    = 5 * time.Second
	defaultMountTimeout     = 15 * time.Second
)

// defaultJoinScopes are the scopes minted for every delegated-credential
// workspace join. ops:read is required for writeback op-status polling
// (/v1/workspaces/{id}/ops/{opId}); sync:trigger is required for
// server-side reconcile kicks. Both must be present on every credential
// so narrow prior credentials cannot silently break a subset of providers.
var defaultJoinScopes = []string{"fs:read", "fs:write", "ops:read", "sync:trigger"}
var defaultInspectScopes = []string{"relayfile:fs:read:*"}

type credentials struct {
	Server    string `json:"server"`
	Token     string `json:"token"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type cloudCredentials struct {
	APIURL               string `json:"apiUrl"`
	AccessToken          string `json:"accessToken"`
	AccessTokenExpiresAt string `json:"accessTokenExpiresAt,omitempty"`
	UpdatedAt            string `json:"updatedAt,omitempty"`
}

type agentRelayCloudSession struct {
	APIURL      string `json:"apiUrl"`
	AccessToken string `json:"accessToken"`
	Auth        struct {
		APIURL      string `json:"apiUrl"`
		AccessToken string `json:"accessToken"`
	} `json:"auth"`
}

type agentRelayActiveWorkspace struct {
	ID                       string            `json:"id"`
	Name                     string            `json:"name"`
	Key                      string            `json:"key"`
	Slug                     string            `json:"slug"`
	CloudWorkspaceID         string            `json:"cloudWorkspaceId"`
	RelayfileWorkspaceID     string            `json:"relayfileWorkspaceId"`
	RelaycastWorkspaceID     string            `json:"relaycastWorkspaceId"`
	RelayauthWorkspaceID     string            `json:"relayauthWorkspaceId"`
	OrganizationID           string            `json:"organizationId"`
	URLs                     map[string]string `json:"urls"`
	RelayfileURL             string            `json:"relayfileUrl"`
	RelayfileURLAlternateKey string            `json:"relayfileURL"`
}

type workspaceCatalog struct {
	Default    string            `json:"default,omitempty"`
	Workspaces []workspaceRecord `json:"workspaces"`
}

type workspaceRecord struct {
	Name             string   `json:"name"`
	ID               string   `json:"id,omitempty"`
	RelayWorkspaceID string   `json:"relayWorkspaceId,omitempty"`
	CreatedAt        string   `json:"createdAt"`
	LastUsedAt       string   `json:"lastUsedAt,omitempty"`
	LocalDir         string   `json:"localDir,omitempty"`
	Server           string   `json:"server,omitempty"`
	CloudAPIURL      string   `json:"cloudApiUrl,omitempty"`
	AgentName        string   `json:"agentName,omitempty"`
	Scopes           []string `json:"scopes,omitempty"`
	Timezone         string   `json:"timezone,omitempty"`
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
	Path            string           `json:"path"`
	ContentType     string           `json:"contentType"`
	Content         string           `json:"content"`
	Encoding        string           `json:"encoding,omitempty"`
	ContentIdentity *contentIdentity `json:"contentIdentity,omitempty"`
	WritebackIntent string           `json:"writebackIntent,omitempty"`
}

type bulkWriteResponse struct {
	Written       int               `json:"written"`
	ErrorCount    int               `json:"errorCount"`
	Errors        []bulkWriteError  `json:"errors"`
	Results       []bulkWriteResult `json:"results,omitempty"`
	CorrelationID string            `json:"correlationId"`
}

type bulkWriteResult struct {
	Path            string           `json:"path"`
	Revision        string           `json:"revision"`
	ContentType     string           `json:"contentType,omitempty"`
	OpID            string           `json:"opId,omitempty"`
	ContentIdentity *contentIdentity `json:"contentIdentity,omitempty"`
	Writeback       *struct {
		Provider string `json:"provider,omitempty"`
		State    string `json:"state,omitempty"`
	} `json:"writeback,omitempty"`
}

type bulkWriteError struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type writeQueuedResponse struct {
	OpID           string `json:"opId"`
	Status         string `json:"status"`
	TargetRevision string `json:"targetRevision"`
	Writeback      struct {
		Provider string `json:"provider"`
		State    string `json:"state"`
	} `json:"writeback"`
}

type contentIdentity struct {
	Kind       string `json:"kind"`
	Key        string `json:"key"`
	TTLSeconds int    `json:"ttlSeconds,omitempty"`
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
	Ready                 bool           `json:"ready,omitempty"`
	Cursor                *string        `json:"cursor"`
	WatermarkTs           *string        `json:"watermarkTs"`
	LagSeconds            int            `json:"lagSeconds"`
	LastError             *string        `json:"lastError"`
	FailureCodes          map[string]int `json:"failureCodes"`
	DeadLetteredEnvelopes int            `json:"deadLetteredEnvelopes"`
	DeadLetteredOps       int            `json:"deadLetteredOps"`
	WebhookHealthy        *bool          `json:"webhookHealthy,omitempty"`
}

type syncIngressStatusResponse struct {
	WorkspaceID          string                               `json:"workspaceId"`
	QueueDepth           int                                  `json:"queueDepth"`
	PendingTotal         int                                  `json:"pendingTotal"`
	DeadLetterTotal      int                                  `json:"deadLetterTotal"`
	DeadLetterByProvider map[string]int                       `json:"deadLetterByProvider"`
	AcceptedTotal        int                                  `json:"acceptedTotal"`
	DroppedTotal         int                                  `json:"droppedTotal"`
	DedupedTotal         int                                  `json:"dedupedTotal"`
	CoalescedTotal       int                                  `json:"coalescedTotal"`
	SuppressedTotal      int                                  `json:"suppressedTotal"`
	StaleTotal           int                                  `json:"staleTotal"`
	IngressByProvider    map[string]syncIngressProviderStatus `json:"ingressByProvider"`
}

type syncIngressProviderStatus struct {
	AcceptedTotal           int `json:"acceptedTotal"`
	DroppedTotal            int `json:"droppedTotal"`
	DedupedTotal            int `json:"dedupedTotal"`
	CoalescedTotal          int `json:"coalescedTotal"`
	PendingTotal            int `json:"pendingTotal"`
	OldestPendingAgeSeconds int `json:"oldestPendingAgeSeconds"`
	SuppressedTotal         int `json:"suppressedTotal"`
	StaleTotal              int `json:"staleTotal"`
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

type cloudRelayfileDelegatedTokenRequest struct {
	AgentName string   `json:"agentName,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
}

type cloudConnectSessionRequest struct {
	AllowedIntegrations []string `json:"allowedIntegrations,omitempty"`
	RequestedBackend    string   `json:"requestedBackend,omitempty"`
}

type cloudConnectSessionResponse struct {
	Token        string `json:"token,omitempty"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
	ConnectLink  string `json:"connectLink,omitempty"`
	ConnectionID string `json:"connectionId,omitempty"`
	Backend      string `json:"backend,omitempty"`
}

type cloudIntegrationReadyResponse struct {
	Ready        bool   `json:"ready"`
	Provider     string `json:"provider,omitempty"`
	ConnectionID string `json:"connectionId,omitempty"`
	State        string `json:"state,omitempty"`
}

type cloudIntegrationCatalogResponse struct {
	Providers []integrationCatalogEntry `json:"providers"`
	Version   string                    `json:"version,omitempty"`
}

type integrationCatalogEntry struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"displayName,omitempty"`
	ConfigKey   string   `json:"configKey,omitempty"`
	VFSRoot     string   `json:"vfsRoot,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`
	Backend     string   `json:"backend,omitempty"`
	Backends    []string `json:"backends,omitempty"`
	Sources     []string `json:"sources,omitempty"`
	AuthMode    string   `json:"authMode,omitempty"`
	Categories  []string `json:"categories,omitempty"`
	Docs        string   `json:"docs,omitempty"`
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
	WorkspaceID                  string              `json:"workspaceId"`
	RemoteRoot                   string              `json:"remoteRoot,omitempty"`
	Mode                         string              `json:"mode"`
	Status                       string              `json:"status,omitempty"`
	LastReconcileAt              string              `json:"lastReconcileAt,omitempty"`
	LastSuccessfulReconcileAt    string              `json:"lastSuccessfulReconcileAt,omitempty"`
	LastEventAt                  string              `json:"lastEventAt,omitempty"`
	IntervalMs                   int64               `json:"intervalMs"`
	Providers                    []syncStateProvider `json:"providers,omitempty"`
	PendingWriteback             int                 `json:"pendingWriteback"`
	PendingConflicts             int                 `json:"pendingConflicts"`
	DeniedPaths                  int                 `json:"deniedPaths"`
	FailedWritebacks             uint64              `json:"failedWritebacks"`
	StallReason                  string              `json:"stallReason,omitempty"`
	LastError                    *statusError        `json:"lastError,omitempty"`
	IncrementalReadNotReadySince map[string]string   `json:"incrementalReadNotReadySince,omitempty"`
	Daemon                       *syncStateDaemon    `json:"daemon,omitempty"`
	// Guards surfaces defensive-guard telemetry from the in-process
	// mountsync state. Existing consumers can ignore the field; it is
	// additive and uses omitempty. Counters come from .relay/state.json.
	Guards *syncStateGuards `json:"guards,omitempty"`
	// Bootstrap mirrors the in-progress full-tree bootstrap state from
	// .relay/state.json so `relayfile status` can show progress instead of
	// a misleading stall. Additive/omitempty; nil when not bootstrapping.
	Bootstrap *syncStateBootstrap `json:"bootstrap,omitempty"`
}

// syncStateBootstrap is the CLI-surface mirror of mountsync's public
// bootstrap status block.
type syncStateBootstrap struct {
	Phase       string `json:"phase"`
	FilesSynced int    `json:"filesSynced"`
	FilesTotal  int    `json:"filesTotal,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
}

// syncStateGuards mirrors mountsync.telemetryCounters and the circuit
// breaker snapshot at the CLI status surface, so operators (and any
// scripted consumer of `relayfile status --json`) can see breaker state
// and guard activity without parsing the underlying .relay/state.json.
type syncStateGuards struct {
	SkippedOversizeWriteback uint64              `json:"skippedOversizeWriteback,omitempty"`
	DeniedRootTarget         uint64              `json:"deniedRootTarget,omitempty"`
	SnapshotDeleteBlocked    uint64              `json:"snapshotDeleteBlocked,omitempty"`
	CircuitOpenEvents        uint64              `json:"circuitOpenEvents,omitempty"`
	TombstonesPending        uint64              `json:"tombstonesPending,omitempty"`
	TombstonesConfirmed      uint64              `json:"tombstonesConfirmed,omitempty"`
	TombstonesAgedOut        uint64              `json:"tombstonesAgedOut,omitempty"`
	PathCollisionQuarantined uint64              `json:"pathCollisionQuarantined,omitempty"`
	LastAppliedRevision      string              `json:"lastAppliedRevision,omitempty"`
	Circuit                  *syncStateGuardCirc `json:"circuit,omitempty"`
}

type statusError struct {
	Kind       string `json:"kind"`
	StatusCode int    `json:"statusCode,omitempty"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message"`
	At         string `json:"at"`
}

// syncStateGuardCirc is the JSON shape of the cloud-error circuit breaker
// state surfaced via status. Mirrors mountsync.CircuitState fields.
type syncStateGuardCirc struct {
	Open       bool   `json:"open"`
	OpenedAt   string `json:"openedAt,omitempty"`
	OpenEvents uint64 `json:"openEvents,omitempty"`
	Failures   int    `json:"failures,omitempty"`
	NextRetry  string `json:"nextRetry,omitempty"`
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
	Backend      string `json:"backend,omitempty"`
	ConnectedAt  string `json:"connectedAt,omitempty"`
	UpdatedAt    string `json:"updatedAt,omitempty"`
}

type relayIntegrationBinding struct {
	Provider       string `json:"provider"`
	PathGlob       string `json:"pathGlob"`
	Channel        string `json:"channel"`
	WebhookID      string `json:"webhookId"`
	WebhookToken   string `json:"webhookToken"`
	SubscriptionID string `json:"subscriptionId,omitempty"`
	CreatedAt      string `json:"createdAt,omitempty"`
	UpdatedAt      string `json:"updatedAt,omitempty"`
}

type relayIntegrationBindingStore struct {
	Bindings []relayIntegrationBinding `json:"bindings"`
}

type daemonPIDState struct {
	PID         int    `json:"pid"`
	WorkspaceID string `json:"workspaceId"`
	LocalDir    string `json:"localDir"`
	LogFile     string `json:"logFile"`
	StartedAt   string `json:"startedAt"`
	// Executable is the resolved path of the daemon binary. It lets
	// stop/restart confirm a recorded PID still belongs to a relayfile
	// daemon before signaling it, guarding against PID reuse.
	Executable string `json:"executable,omitempty"`
}

type mountDaemonProcess struct {
	PID     int
	Command string
	Source  string
}

type processCommandSnapshot struct {
	PID     int
	Command string
}

var failedWritebacksStateMu sync.Mutex

var listProcessCommands = defaultListProcessCommands

type apiError struct {
	StatusCode int
	Code       string
	Message    string
	// RetryAfter carries the parsed `Retry-After` response header when the
	// server returns 429 or 503. Zero means "no hint was provided" — callers
	// (in particular politePoll) should fall back to their own backoff
	// schedule in that case.
	RetryAfter time.Duration
}

func (e *apiError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("http %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("http %d %s: %s", e.StatusCode, e.Code, e.Message)
}

// parseRetryAfter parses an HTTP Retry-After header value. The spec allows
// either a delta-seconds integer or an HTTP-date; we accept both and return
// zero for anything we can't parse (callers treat zero as "use default
// backoff").
func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func main() {
	log.SetFlags(0)
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if wantsHelp(args) {
		printHelpForArgs(args, stdout)
		return nil
	}
	if len(args) == 0 {
		return runSetup(nil, stdin, stdout)
	}

	switch args[0] {
	case "setup":
		return runSetup(args[1:], stdin, stdout)
	case "login":
		return runLogin(args[1:], stdin, stdout)
	case "logout":
		return runLogout(args[1:], stdout)
	case "workspace":
		return runWorkspace(args[1:], stdin, stdout)
	case "integration":
		return runIntegration(args[1:], stdin, stdout)
	case "ops":
		return runOps(args[1:], stdin, stdout)
	case "writeback":
		return runWriteback(args[1:], stdout)
	case "digest":
		return runDigest(args[1:], stdout)
	case "pull":
		return runPull(args[1:], stdout)
	case "mount", "start", "on":
		// `start` and `on` are friendlier aliases for `mount`. Same flags,
		// same foreground/background behavior; pass --background to detach.
		// `on` migrates the agent-relay `relay on` mount UX into relayfile.
		return runMount(args[1:])
	case "restart":
		return runRestart(args[1:], stdout)
	case "supervisor":
		return runSupervisor(args[1:], stdout)
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
	case "stop", "off":
		// `off` is the friendlier alias for `stop`, migrating the
		// agent-relay `relay off` unmount UX into relayfile.
		return runStop(args[1:], stdout)
	case "logs":
		return runLogs(args[1:], stdout)
	case "observer":
		return runObserver(args[1:], stdout)
	case "listen", "watch":
		return runListen(args[1:], stdout)
	case "dev":
		return runDev(args[1:], nil, stdout)
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func wantsHelp(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

func printHelpForArgs(args []string, stdout io.Writer) {
	if len(args) == 0 {
		printUsage(stdout)
		return
	}
	command := args[0]
	if command == "-h" || command == "--help" {
		printUsage(stdout)
		return
	}
	subcommand := ""
	if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
		subcommand = args[1]
	}

	switch command {
	case "setup":
		fmt.Fprintln(stdout, "Usage: relayfile setup [--provider PROVIDER] [--backend BACKEND] [--workspace NAME] [--local-dir DIR]")
	case "login":
		fmt.Fprintln(stdout, "Usage: relayfile login [--no-open] [--api-key] [--server URL] [--token TOKEN]")
	case "logout":
		fmt.Fprintln(stdout, "Usage: relayfile logout")
	case "workspace":
		printWorkspaceUsage(stdout, subcommand)
	case "integration":
		printIntegrationUsage(stdout, subcommand)
	case "ops":
		printOpsUsage(stdout, subcommand)
	case "writeback":
		printWritebackUsage(stdout, subcommand)
	case "digest":
		printDigestUsage(stdout, subcommand)
	case "pull":
		fmt.Fprintln(stdout, "Usage: relayfile pull [--workspace NAME] [--provider PROVIDER] [--reason TEXT]")
	case "mount", "start", "on":
		printMountHelp(stdout)
	case "restart":
		fmt.Fprintln(stdout, "Usage: relayfile restart [WORKSPACE] [--foreground]")
	case "tree", "ls":
		fmt.Fprintln(stdout, "Usage: relayfile tree [WORKSPACE] [PATH] [--depth N] [--json]")
	case "read", "cat":
		fmt.Fprintln(stdout, "Usage: relayfile read [WORKSPACE] PATH [--output FILE] [--json]")
	case "seed":
		fmt.Fprintln(stdout, "Usage: relayfile seed [WORKSPACE] [DIR]")
	case "export":
		fmt.Fprintln(stdout, "Usage: relayfile export [WORKSPACE] --format FORMAT [--output FILE]")
	case "status":
		fmt.Fprintln(stdout, "Usage: relayfile status [WORKSPACE] [--json]")
	case "stop", "off":
		fmt.Fprintln(stdout, "Usage: relayfile stop [WORKSPACE]")
	case "supervisor":
		fmt.Fprintln(stdout, "Usage: relayfile supervisor <install|uninstall|status> [WORKSPACE] [--interval 30s]")
	case "logs":
		fmt.Fprintln(stdout, "Usage: relayfile logs [WORKSPACE] [--lines N]")
	case "observer":
		fmt.Fprintln(stdout, "Usage: relayfile observer [WORKSPACE] [--no-open]")
	case "listen", "watch":
		fmt.Fprintln(stdout, "Usage: relayfile listen [WORKSPACE] [--provider PROVIDER] [--path GLOB] [--event TYPE] [--run CMD] [--format text|json] [--background]")
	case "dev":
		fmt.Fprintln(stdout, "Usage: relayfile dev [WORKSPACE] [--provider PROVIDER] [--path GLOB] [--event TYPE] [--run CMD]")
	case "help":
		printUsage(stdout)
	default:
		printUsage(stdout)
	}
}

func printWorkspaceUsage(w io.Writer, subcommand string) {
	switch subcommand {
	case "create":
		fmt.Fprintln(w, "Usage: relayfile workspace create NAME")
	case "join":
		fmt.Fprintln(w, "Usage: relayfile workspace join WORKSPACE_ID [--name NAME] [--write]")
	case "use":
		fmt.Fprintln(w, "Usage: relayfile workspace use NAME")
	case "list":
		fmt.Fprintln(w, "Usage: relayfile workspace list [--names-only]")
	case "current":
		fmt.Fprintln(w, "Usage: relayfile workspace current [--verbose]")
	case "status":
		fmt.Fprintln(w, "Usage: relayfile workspace status [--workspace NAME] [--json]")
	case "delete":
		fmt.Fprintln(w, "Usage: relayfile workspace delete NAME [--yes]")
	default:
		fmt.Fprintln(w, `Usage:
  relayfile workspace create NAME
  relayfile workspace join WORKSPACE_ID [--name NAME] [--write]
  relayfile workspace use NAME
  relayfile workspace list [--names-only]
  relayfile workspace current [--verbose]
  relayfile workspace status [--workspace NAME] [--json]
  relayfile workspace delete NAME [--yes]`)
	}
}

func printIntegrationUsage(w io.Writer, subcommand string) {
	switch subcommand {
	case "connect":
		fmt.Fprintln(w, "Usage: relayfile integration connect PROVIDER [--backend BACKEND] [--workspace NAME] [--no-open] [--timeout 5m] [--wait-sync]")
	case "available", "catalog", "providers":
		fmt.Fprintln(w, "Usage: relayfile integration available [--search QUERY] [--backend BACKEND] [--json] [--refresh]")
	case "search":
		fmt.Fprintln(w, "Usage: relayfile integration search QUERY [--backend BACKEND] [--json] [--refresh]")
	case "list":
		fmt.Fprintln(w, "Usage: relayfile integration list [--workspace NAME] [--json]")
	case "disconnect":
		fmt.Fprintln(w, "Usage: relayfile integration disconnect PROVIDER [--workspace NAME] [--yes]")
	case "adopt":
		fmt.Fprintln(w, "Usage: relayfile integration adopt PROVIDER --connection-id ID [--workspace NAME] [--provider-config-key KEY] [--yes]")
	case "set-metadata":
		fmt.Fprintln(w, "Usage: relayfile integration set-metadata PROVIDER KEY=VALUE [KEY=VALUE...] [--workspace NAME] [--yes]")
	case "bind":
		fmt.Fprintln(w, "Usage: relayfile integration bind PROVIDER RESOURCE_OR_PATH_GLOB --channel CHANNEL --webhook ID --webhook-token TOKEN")
	case "resolve-path":
		fmt.Fprintln(w, "Usage: relayfile integration resolve-path PROVIDER RESOURCE [--json]")
	case "unbind":
		fmt.Fprintln(w, "Usage: relayfile integration unbind PROVIDER [RESOURCE_OR_PATH_GLOB|--resource RESOURCE_OR_PATH_GLOB]")
	default:
		fmt.Fprintln(w, `Usage:
  relayfile integration connect PROVIDER [--backend BACKEND] [--workspace NAME] [--wait-sync]
  relayfile integration available [--search QUERY] [--backend BACKEND] [--json] [--refresh]
  relayfile integration search QUERY [--backend BACKEND] [--json] [--refresh]
  relayfile integration list [--workspace NAME] [--json]
  relayfile integration disconnect PROVIDER [--workspace NAME] [--yes]
  relayfile integration adopt PROVIDER --connection-id ID [--workspace NAME] [--provider-config-key KEY] [--yes]
  relayfile integration set-metadata PROVIDER KEY=VALUE [KEY=VALUE...] [--workspace NAME] [--yes]
  relayfile integration bind PROVIDER RESOURCE_OR_PATH_GLOB --channel CHANNEL --webhook ID --webhook-token TOKEN
  relayfile integration resolve-path PROVIDER RESOURCE [--json]
  relayfile integration unbind PROVIDER [RESOURCE_OR_PATH_GLOB|--resource RESOURCE_OR_PATH_GLOB]
  relayfile integration writeback-secret --channel CHANNEL [--workspace WS] [--json]`)
	}
}

func printOpsUsage(w io.Writer, subcommand string) {
	switch subcommand {
	case "list":
		fmt.Fprintln(w, "Usage: relayfile ops list [--workspace NAME] [--json] [--no-refresh]")
	case "replay":
		fmt.Fprintln(w, "Usage: relayfile ops replay OPID [--workspace NAME]")
	default:
		fmt.Fprintln(w, `Usage:
  relayfile ops list [--workspace NAME] [--json]
  relayfile ops replay OPID [--workspace NAME]`)
	}
}

func printWritebackUsage(w io.Writer, subcommand string) {
	switch subcommand {
	case "list":
		fmt.Fprintln(w, writebackListUsage)
	case "status":
		fmt.Fprintln(w, "Usage: relayfile writeback status [WORKSPACE] [--json]")
	case "push":
		fmt.Fprintln(w, "Usage: relayfile writeback push LOCAL_PATH [--workspace WS] [--json] [--timeout 90s]")
	case "update":
		fmt.Fprintln(w, "Usage: relayfile writeback update LOCAL_PATH [--workspace WS] [--json] [--timeout 90s]")
	case "delete":
		fmt.Fprintln(w, "Usage: relayfile writeback delete LOCAL_PATH [--workspace WS] [--json] [--timeout 90s]")
	case "retry":
		fmt.Fprintln(w, "Usage: relayfile writeback retry --opId OP [WORKSPACE]")
	case "skip-stuck":
		fmt.Fprintln(w, "Usage: relayfile writeback skip-stuck [WORKSPACE] [--workspace WS] [--max N] [--json]")
	case "sweep-drafts":
		fmt.Fprintln(w, writebackSweepUsage)
	default:
		fmt.Fprintln(w, `Usage:
  relayfile writeback list --state pending|dead [--workspace WS] [--json]
  relayfile writeback status [WORKSPACE] [--json]
  relayfile writeback retry --opId OP [WORKSPACE]
  relayfile writeback push LOCAL_PATH [--workspace WS] [--json] [--timeout 90s]
  relayfile writeback update LOCAL_PATH [--workspace WS] [--json] [--timeout 90s]
  relayfile writeback delete LOCAL_PATH [--workspace WS] [--json] [--timeout 90s]
  relayfile writeback skip-stuck [WORKSPACE] [--workspace WS] [--max N] [--json]
  relayfile writeback sweep-drafts [WORKSPACE] [--path-prefix PREFIX] [--pattern GLOB ...] [--apply] [--json]`)
	}
}

func printDigestUsage(w io.Writer, subcommand string) {
	switch subcommand {
	case "rebuild":
		fmt.Fprintln(w, digestRebuildUsage)
	default:
		fmt.Fprintln(w, `Usage:
  relayfile digest rebuild --window today|yesterday|YYYY-MM-DD|this-week|last-week [--workspace NAME] [--json]`)
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `relayfile is the RelayFile CLI.

Usage:
  relayfile
  relayfile setup [--provider PROVIDER] [--backend BACKEND] [--workspace NAME] [--local-dir DIR]
  relayfile login [--no-open] [--api-key] [--server URL] [--token TOKEN]
  relayfile logout
  relayfile workspace create NAME
  relayfile workspace join WORKSPACE_ID [--name NAME] [--write]
  relayfile workspace use NAME
  relayfile workspace list [--names-only]
  relayfile workspace current [--verbose]
  relayfile workspace status [--workspace NAME] [--json]
  relayfile workspace delete NAME [--yes]
  relayfile integration connect PROVIDER [--backend BACKEND] [--workspace NAME]
    (for jira/confluence: prompts for the Atlassian site to bind after OAuth completes)
  relayfile integration available [--search QUERY] [--backend BACKEND] [--json] [--refresh]
  relayfile integration search QUERY [--backend BACKEND] [--json] [--refresh]
  relayfile integration list [--workspace NAME] [--json]
  relayfile integration disconnect PROVIDER [--workspace NAME] [--yes]
  relayfile integration adopt PROVIDER --connection-id ID [--workspace NAME] [--provider-config-key KEY] [--yes]
  relayfile integration set-metadata PROVIDER KEY=VALUE [KEY=VALUE...] [--workspace NAME] [--yes]
  relayfile integration bind PROVIDER RESOURCE_OR_PATH_GLOB --channel CHANNEL --webhook ID --webhook-token TOKEN
  relayfile integration resolve-path PROVIDER RESOURCE [--json]
  relayfile integration unbind PROVIDER [RESOURCE_OR_PATH_GLOB|--resource RESOURCE_OR_PATH_GLOB]
  relayfile ops list [--workspace NAME] [--json]
  relayfile ops replay OPID [--workspace NAME]
  relayfile writeback list --state pending|dead [--workspace WS] [--json]
  relayfile writeback status [WORKSPACE] [--json]
  relayfile writeback retry --opId OP [WORKSPACE]
  relayfile writeback push LOCAL_PATH [--workspace WS] [--json] [--timeout 90s]
  relayfile writeback update LOCAL_PATH [--workspace WS] [--json] [--timeout 90s]
  relayfile writeback delete LOCAL_PATH [--workspace WS] [--json] [--timeout 90s]
  relayfile writeback skip-stuck [WORKSPACE] [--workspace WS] [--max N] [--json]
  relayfile digest rebuild --window today|yesterday|YYYY-MM-DD|this-week|last-week [--workspace NAME] [--json]
  relayfile pull [--workspace NAME] [--provider PROVIDER] [--reason TEXT]
  relayfile mount [WORKSPACE] [LOCAL_DIR]
  relayfile start [WORKSPACE] [LOCAL_DIR]            (alias for mount; pass --background to detach)
  relayfile on [WORKSPACE] [LOCAL_DIR]              (alias for mount; pass --background to detach)
  relayfile stop [WORKSPACE]
  relayfile off [WORKSPACE]                         (alias for stop)
  relayfile restart [WORKSPACE] [--foreground]
  relayfile supervisor install [WORKSPACE] [--interval 30s]
  relayfile supervisor uninstall [WORKSPACE]
  relayfile supervisor status [WORKSPACE]
  relayfile tree [WORKSPACE] [PATH] [--depth N]
  relayfile read [WORKSPACE] PATH
  relayfile seed [WORKSPACE] [DIR]
  relayfile export [WORKSPACE] --format FORMAT [--output FILE]
  relayfile status [WORKSPACE]
  relayfile logs [WORKSPACE]
  relayfile observer [WORKSPACE] [--no-open]

Subcommands:
  setup       Sign in, connect an integration, and mount the workspace
  login       Sign in via agent-relay cloud login (or --api-key for self-hosted)
  logout      Clear Relayfile credentials from this machine
  workspace   Create, join, select via agent-relay, list, show current, or delete locally tracked workspaces
  integration Connect, discover, list, disconnect, or adopt workspace integrations
  ops         List or replay dead-lettered writeback ops
  writeback   Inspect or retry local writeback failures
  writeback list
              List local writeback items by state
  writeback status
              Show local pending, failed, and dead-lettered writebacks
  writeback retry
              Re-enqueue a local dead-lettered writeback op
  writeback skip-stuck
              Walk the events cursor past stuck (404) events without waiting the treat-as-deleted timer
  digest      Regenerate workspace digests
  digest rebuild
              Regenerate daily, weekly, or date-stamped digest artifacts
  pull        Trigger an immediate sync refresh for one or all providers
  mount       Mirror a remote workspace to a local directory; add --background to detach
  start       Alias for mount; pass --background to detach
  on          Alias for mount; pass --background to detach
  stop        Stop a background mount
  off         Alias for stop
  restart     Stop and start a workspace's mount in one step (--foreground to attach)
  supervisor  Install/uninstall/status launchd (macOS) or systemd (Linux) service for auto-restart
  tree        List a remote workspace path
  read        Print a remote file's content
  seed        Upload a directory tree with bulk writes
  export      Export a workspace as json, tar, or patch
  status      Show sync status and local mirror state for a workspace
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
	backend := fs.String("backend", "", "integration backend to request (nango or composio)")
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
		"backend":         true,
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
		return errors.New("usage: relayfile setup [--provider PROVIDER] [--backend BACKEND] [--workspace NAME] [--local-dir DIR]")
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
	requestedBackend, err := normalizeIntegrationBackend(*backend)
	if err != nil {
		return err
	}

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

	controlToken := tokenSet.AccessToken
	delegated, err := delegatedRelayfileTokenViaCloud(tokenSet, record.ID, record.AgentName, record.Scopes)
	if err != nil {
		return err
	}
	delegatedCredsFile := delegatedCredentialsPathForRequest(record.ID, record.Scopes)
	if err := delegatedauth.SaveAtomic(delegatedCredsFile, delegated); err != nil {
		return fmt.Errorf("persist delegated relayfile credentials: %w", err)
	}
	record, err = persistDelegatedWorkspace(record, delegated, absLocalDir, true)
	if err != nil {
		return err
	}

	if createdWorkspace {
		fmt.Fprintf(stdout, "Workspace %s ready (id: %s)\n", record.Name, record.ID)
	} else {
		fmt.Fprintf(stdout, "Workspace %s reused (id: %s)\n", record.Name, record.ID)
	}

	if selectedProvider != "" && selectedProvider != "none" && selectedProvider != "skip" {
		createdConnection := false
		if createdWorkspace {
			if err := connectCloudIntegration(tokenSet.APIURL, record.ID, controlToken, selectedProvider, requestedBackend, absLocalDir, *connectTimeout, !*noOpen, stdout); err != nil {
				return err
			}
			createdConnection = true
		} else {
			var err error
			createdConnection, err = ensureCloudIntegration(tokenSet.APIURL, record.ID, controlToken, selectedProvider, requestedBackend, absLocalDir, *connectTimeout, !*noOpen, stdout)
			if err != nil {
				return err
			}
		}
		if createdConnection && isAtlassianProvider(selectedProvider) {
			if err := runAtlassianSitePicker(tokenSet.APIURL, record.ID, controlToken, selectedProvider, stdin, stdout); err != nil {
				return err
			}
		}
		// We used to poll /sync/status here until the provider reported
		// `ready`. That hung forever for providers without a sync handler
		// (e.g. docker_hub) and was redundant for providers that do sync —
		// the mount daemon below polls /sync/status on its own cadence.
	} else {
		fmt.Fprintln(stdout, "Integration connection skipped")
	}

	mountArgs := []string{
		"--server", strings.TrimRight(delegated.ServerURL(), "/"),
		"--creds-file", delegatedCredsFile,
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
		return creds, nil
	}

	creds, err := cloudCredentialsFromAgentRelay()
	if err != nil {
		return cloudCredentials{}, err
	}
	return creds, nil
}

func agentRelayBinary() string {
	if value := strings.TrimSpace(os.Getenv("AGENT_RELAY_BIN")); value != "" {
		return value
	}
	return "agent-relay"
}

func ensureAgentRelayCLICompatible() error {
	bin := agentRelayBinary()
	versionOutput, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(versionOutput))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("agent-relay CLI >= %s required; run `npm install -g agent-relay@%s` or set AGENT_RELAY_BIN to a compatible binary (%s)", minAgentRelayCLIVersion, minAgentRelayCLIVersion, detail)
	}
	version := firstSemver(string(versionOutput))
	if version == "" || compareSemver(version, minAgentRelayCLIVersion) < 0 {
		if version == "" {
			version = strings.TrimSpace(string(versionOutput))
		}
		return fmt.Errorf("agent-relay CLI >= %s required; found %q. Run `npm install -g agent-relay@%s` or update the sandbox image", minAgentRelayCLIVersion, version, minAgentRelayCLIVersion)
	}
	for _, args := range [][]string{
		{"cloud", "session", "--help"},
		{"workspace", "active", "--help"},
		{"workspace", "switch", "--help"},
	} {
		output, err := exec.Command(bin, args...).CombinedOutput()
		if err != nil {
			detail := strings.TrimSpace(string(output))
			if detail == "" {
				detail = err.Error()
			}
			return fmt.Errorf("agent-relay CLI >= %s required with `%s`; run `npm install -g agent-relay@%s` or update the sandbox image (%s)", minAgentRelayCLIVersion, strings.Join(append([]string{"agent-relay"}, args...), " "), minAgentRelayCLIVersion, detail)
		}
	}
	return nil
}

func firstSemver(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	for _, field := range fields {
		field = strings.TrimPrefix(field, "v")
		if isSemver(field) {
			return field
		}
	}
	return ""
}

func isSemver(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func compareSemver(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	maxParts := len(aParts)
	if len(bParts) > maxParts {
		maxParts = len(bParts)
	}
	for i := 0; i < maxParts; i++ {
		var aValue, bValue int
		if i < len(aParts) {
			aValue, _ = strconv.Atoi(aParts[i])
		}
		if i < len(bParts) {
			bValue, _ = strconv.Atoi(bParts[i])
		}
		if aValue < bValue {
			return -1
		}
		if aValue > bValue {
			return 1
		}
	}
	return 0
}

func runAgentRelayJSON(args []string, out any) error {
	if err := ensureAgentRelayCLICompatible(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, agentRelayBinary(), args...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("agent-relay %s timed out; run 'agent-relay cloud login' and try again", strings.Join(args, " "))
	}
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("agent-relay %s failed: %s", strings.Join(args, " "), detail)
	}
	if err := json.Unmarshal(output, out); err != nil {
		return fmt.Errorf("parse agent-relay %s output: %w", strings.Join(args, " "), err)
	}
	return nil
}

func runAgentRelayLogin(stdin io.Reader, stdout io.Writer, noOpen bool) error {
	if err := ensureAgentRelayCLICompatible(); err != nil {
		return err
	}
	args := []string{"cloud", "login"}
	if noOpen {
		args = append(args, "--no-open")
	}
	cmd := exec.Command(agentRelayBinary(), args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("agent-relay cloud login failed: %w", err)
	}
	return nil
}

func cloudCredentialsFromAgentRelay() (cloudCredentials, error) {
	var session agentRelayCloudSession
	if err := runAgentRelayJSON([]string{"cloud", "session", "--json"}, &session); err != nil {
		return cloudCredentials{}, err
	}
	apiURL := strings.TrimRight(strings.TrimSpace(session.APIURL), "/")
	accessToken := strings.TrimSpace(session.AccessToken)
	if apiURL == "" {
		apiURL = strings.TrimRight(strings.TrimSpace(session.Auth.APIURL), "/")
	}
	if accessToken == "" {
		accessToken = strings.TrimSpace(session.Auth.AccessToken)
	}
	if apiURL == "" {
		apiURL = defaultCloudAPIURL
	}
	if accessToken == "" {
		return cloudCredentials{}, errors.New("agent-relay cloud session --json did not include an accessToken")
	}
	return cloudCredentials{
		APIURL:      apiURL,
		AccessToken: accessToken,
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func activeWorkspaceFromAgentRelay() (agentRelayActiveWorkspace, error) {
	var workspace agentRelayActiveWorkspace
	if err := runAgentRelayJSON([]string{"workspace", "active", "--json"}, &workspace); err != nil {
		return agentRelayActiveWorkspace{}, err
	}
	if strings.TrimSpace(workspace.RelayfileWorkspaceID) == "" {
		return agentRelayActiveWorkspace{}, errors.New("agent-relay workspace active --json did not include relayfileWorkspaceId")
	}
	if strings.TrimSpace(workspace.CloudWorkspaceID) == "" {
		workspace.CloudWorkspaceID = workspace.ID
	}
	if strings.TrimSpace(workspace.Name) == "" {
		workspace.Name = firstNonEmpty(workspace.Slug, workspace.RelayfileWorkspaceID, workspace.CloudWorkspaceID, workspace.ID)
	}
	return workspace, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func agentRelayWorkspaceMatches(value string, workspace agentRelayActiveWorkspace) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	for _, candidate := range []string{
		workspace.ID,
		workspace.Name,
		workspace.Key,
		workspace.Slug,
		workspace.CloudWorkspaceID,
		workspace.RelayfileWorkspaceID,
		workspace.RelaycastWorkspaceID,
		workspace.RelayauthWorkspaceID,
	} {
		if value == strings.TrimSpace(candidate) {
			return true
		}
	}
	if id, ok := catalogWorkspaceID(value); ok {
		for _, candidate := range []string{
			workspace.ID,
			workspace.CloudWorkspaceID,
			workspace.RelayfileWorkspaceID,
		} {
			if strings.TrimSpace(id) == strings.TrimSpace(candidate) {
				return true
			}
		}
	}
	return false
}

func workspaceRequestMatchesDelegatedCredentials(value, relayfileWorkspaceID string) bool {
	value = strings.TrimSpace(value)
	relayfileWorkspaceID = strings.TrimSpace(relayfileWorkspaceID)
	if value == "" {
		return true
	}
	if relayfileWorkspaceID == "" {
		return false
	}
	if value == relayfileWorkspaceID {
		return true
	}
	if id, ok := catalogWorkspaceID(value); ok && strings.TrimSpace(id) == relayfileWorkspaceID {
		return true
	}
	for _, lookup := range []func(string) (workspaceRecord, bool){workspaceRecordByName, workspaceRecordByID} {
		record, ok := lookup(value)
		if !ok {
			continue
		}
		for _, candidate := range []string{record.ID, record.RelayWorkspaceID, record.Name} {
			if strings.TrimSpace(candidate) == relayfileWorkspaceID {
				return true
			}
		}
	}
	return false
}

// loadLegacyCloudCredentials reads the pre-Agent Relay cloud credential store.
// New cloud auth flows must use cloudCredentialsFromAgentRelay instead.
func loadLegacyCloudCredentials() (cloudCredentials, error) {
	var creds cloudCredentials
	payload, err := os.ReadFile(cloudCredentialsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return creds, fmt.Errorf("legacy cloud credentials not found at %s; run agent-relay cloud login", cloudCredentialsPath())
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

// ErrCloudRefreshExpired indicates the cloud refresh token cannot mint new
// access tokens. The mount loop uses this to enter the read-only degraded
// state described in the productized cloud-mount contract acceptance test
// A9 ("Cloud refresh token expired").
var ErrCloudRefreshExpired = errors.New("cloud session expired. Run 'agent-relay cloud login' to sign in again.")

var ErrDelegatedRelayfileCredentialsExpired = errors.New("delegated relayfile credentials expired or revoked. Re-bootstrap relayfile credentials with agent-relay cloud login.")

// ErrDelegatedScopeInsufficient is returned when the cloud delegated-token
// mint rejects the requested scopes. This requires human/admin intervention
// (re-mint with corrected scopes) and cannot be recovered automatically.
var ErrDelegatedScopeInsufficient = errors.New("delegated relayfile credentials have insufficient scope — re-mint with broader scopes")

// ErrDelegatedScopeInvalid is returned when the cloud delegated-token mint
// rejects the request because the requested scopes are malformed — e.g. not
// valid relayfile path scopes. Unlike a transient backend failure, retrying
// re-sends the identical bad scopes and can never succeed, so this is a
// permanent client error that must surface to a human (correct the scope shape)
// rather than drive an endless retry loop.
var ErrDelegatedScopeInvalid = errors.New("delegated relayfile credentials requested invalid scopes — scopes must be valid relayfile path scopes; retrying will not succeed, re-mint with corrected scopes")

func isMountCredentialExpired(err error) bool {
	return errors.Is(err, ErrCloudRefreshExpired) ||
		errors.Is(err, ErrDelegatedRelayfileCredentialsExpired) ||
		errors.Is(err, ErrDelegatedScopeInsufficient) ||
		errors.Is(err, ErrDelegatedScopeInvalid)
}

// mapDelegatedTokenCloudError translates structured cloud error codes returned
// by the delegated-token route into typed sentinel errors so the mount loop
// can distinguish needs_reauth (pause-for-human) from transient failures
// (back off and retry).
func mapDelegatedTokenCloudError(err error) error {
	if err == nil {
		return nil
	}
	var ae *apiError
	if !errors.As(err, &ae) {
		return fmt.Errorf("mint delegated relayfile credentials: %w", err)
	}
	switch ae.Code {
	case "needs_reauth":
		return fmt.Errorf("%w: %s", ErrDelegatedRelayfileCredentialsExpired, ae.Message)
	case "scope_insufficient":
		return fmt.Errorf("%w: %s", ErrDelegatedScopeInsufficient, ae.Message)
	case "invalid_scope":
		// The requested scopes are malformed (e.g. not relayfile path scopes).
		// This is a permanent client error: retrying re-sends the same bad
		// scopes and cannot succeed, so surface it as needs-human rather than
		// letting the mount loop back off and retry forever.
		return fmt.Errorf("%w: %s", ErrDelegatedScopeInvalid, ae.Message)
	default:
		// relayauth_unavailable and other codes are transient — wrap plainly
		// so the caller can back off and retry.
		return fmt.Errorf("mint delegated relayfile credentials: %w", err)
	}
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
		ids = []string{"github", "notion", "linear", "slack", "none"}
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
		return "slack"
	default:
		return value
	}
}

func validateLocalProviderID(provider string) error {
	if provider == "" {
		return errors.New("provider is required")
	}
	for _, ch := range provider {
		if ch >= 'a' && ch <= 'z' {
			continue
		}
		if ch >= '0' && ch <= '9' {
			continue
		}
		if ch == '-' || ch == '_' {
			continue
		}
		return fmt.Errorf("invalid provider %q (use lowercase letters, numbers, dashes, or underscores)", provider)
	}
	return nil
}

func normalizeIntegrationBackend(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "default":
		return "", nil
	case "nango", "composio":
		return value, nil
	default:
		return "", fmt.Errorf("unsupported integration backend %q (expected nango or composio)", value)
	}
}

// integrationCatalogTTL bounds how long a cached catalog response remains
// authoritative without revalidation per contract §6 / verdict §A12.
const integrationCatalogTTL = time.Hour

type integrationCatalogCacheEntry struct {
	APIURL    string                    `json:"apiUrl"`
	FetchedAt string                    `json:"fetchedAt"`
	Version   string                    `json:"version,omitempty"`
	Providers []integrationCatalogEntry `json:"providers"`
}

func integrationCatalogCachePath(cacheKey string) string {
	if strings.Contains(cacheKey, "?dynamic=true") {
		return filepath.Join(configDir(), "catalog-cache-dynamic.json")
	}
	return filepath.Join(configDir(), "catalog-cache.json")
}

func readIntegrationCatalogCache(cacheKey string) (integrationCatalogCacheEntry, bool) {
	payload, err := os.ReadFile(integrationCatalogCachePath(cacheKey))
	if err != nil {
		return integrationCatalogCacheEntry{}, false
	}
	var entry integrationCatalogCacheEntry
	if err := json.Unmarshal(payload, &entry); err != nil {
		return integrationCatalogCacheEntry{}, false
	}
	return entry, true
}

func writeIntegrationCatalogCache(entry integrationCatalogCacheEntry) error {
	if err := ensureConfigDir(); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomically(integrationCatalogCachePath(entry.APIURL), payload, 0o644)
}

func invalidateIntegrationCatalogCache() {
	_ = os.Remove(integrationCatalogCachePath(""))
	_ = os.Remove(integrationCatalogCachePath("?dynamic=true"))
}

func cachedCatalogIsFresh(entry integrationCatalogCacheEntry, apiURL string) bool {
	if entry.APIURL != apiURL || len(entry.Providers) == 0 {
		return false
	}
	fetched, err := time.Parse(time.RFC3339, entry.FetchedAt)
	if err != nil {
		return false
	}
	return time.Since(fetched) < integrationCatalogTTL
}

func loadIntegrationCatalog(cloudAPIURL, accessToken string) ([]integrationCatalogEntry, error) {
	if entry, ok := readIntegrationCatalogCache(cloudAPIURL); ok && cachedCatalogIsFresh(entry, cloudAPIURL) {
		return entry.Providers, nil
	}
	return fetchIntegrationCatalog(cloudAPIURL, accessToken)
}

func dynamicIntegrationCatalogCacheKey(cloudAPIURL string) string {
	return strings.TrimRight(cloudAPIURL, "/") + "?dynamic=true"
}

func loadAvailableIntegrationCatalog(cloudAPIURL string, refresh bool) ([]integrationCatalogEntry, error) {
	cacheKey := dynamicIntegrationCatalogCacheKey(cloudAPIURL)
	if !refresh {
		if entry, ok := readIntegrationCatalogCache(cacheKey); ok && cachedCatalogIsFresh(entry, cacheKey) {
			return entry.Providers, nil
		}
	}
	return fetchIntegrationCatalogPath(cloudAPIURL, "", true, cacheKey)
}

func fetchIntegrationCatalog(cloudAPIURL, accessToken string) ([]integrationCatalogEntry, error) {
	return fetchIntegrationCatalogPath(cloudAPIURL, accessToken, false, cloudAPIURL)
}

func fetchIntegrationCatalogPath(cloudAPIURL, accessToken string, dynamic bool, cacheKey string) ([]integrationCatalogEntry, error) {
	cloudAPIURL = strings.TrimRight(strings.TrimSpace(cloudAPIURL), "/")
	if cloudAPIURL == "" {
		cloudAPIURL = defaultCloudAPIURL
	}
	catalogURL, err := url.Parse(cloudAPIURL + "/api/v1/integrations/catalog")
	if err != nil {
		return fallbackIntegrationCatalog(), nil
	}
	if dynamic {
		query := catalogURL.Query()
		query.Set("dynamic", "true")
		catalogURL.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, catalogURL.String(), nil)
	if err != nil {
		return fallbackIntegrationCatalog(), nil
	}
	if strings.TrimSpace(accessToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	}
	var payload cloudIntegrationCatalogResponse
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fallbackIntegrationCatalog(), nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fallbackIntegrationCatalog(), nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fallbackIntegrationCatalog(), nil
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fallbackIntegrationCatalog(), nil
	}
	if len(payload.Providers) == 0 {
		return fallbackIntegrationCatalog(), nil
	}
	_ = writeIntegrationCatalogCache(integrationCatalogCacheEntry{
		APIURL:    cacheKey,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Version:   payload.Version,
		Providers: payload.Providers,
	})
	return payload.Providers, nil
}

// isAPIConflict reports whether err is a 409 from the cloud API. A 409 forces
// catalog revalidation per contract verdict §A12.
func isAPIConflict(err error) bool {
	var apiErr *apiError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict
}

func fallbackIntegrationCatalog() []integrationCatalogEntry {
	return []integrationCatalogEntry{
		{ID: "github", DisplayName: "GitHub", VFSRoot: "/github"},
		{ID: "notion", DisplayName: "Notion", VFSRoot: "/notion"},
		{ID: "linear", DisplayName: "Linear", VFSRoot: "/linear"},
		{ID: "slack", DisplayName: "Slack", VFSRoot: "/slack"},
		{ID: "slack-my-senior-dev", DisplayName: "Slack (MSD)", VFSRoot: "/slack-msd"},
		{ID: "slack-nightcto", DisplayName: "Slack (NightCTO)", VFSRoot: "/slack-nightcto"},
	}
}

// preflightMountRootInvariant enforces the recovery-mode contract before
// the daemon recreates a mount layout under absLocalDir. The clobber
// pathology is: a regular file at the path that used to be the mount
// directory. Recreating around it would silently restart the data-loss
// signature, so we refuse unless the operator explicitly acknowledges via
// --reset-after-clobber (or RELAYFILE_RESET_AFTER_CLOBBER=1).
//
// Plain "missing root" (the fresh-install case) is allowed without the
// flag, but only when the parent of localDir exists — we refuse to mount
// at a path whose parent itself is missing or a file (likely a typo).
func preflightMountRootInvariant(absLocalDir string, ack bool) error {
	clean := filepath.Clean(absLocalDir)
	info, err := os.Lstat(clean)
	if err != nil {
		if os.IsNotExist(err) {
			// Fresh-install / first-mount case. Require the parent to be
			// a directory; otherwise refuse to create the mount in a
			// likely-bogus location.
			parent := filepath.Dir(clean)
			pInfo, pErr := os.Lstat(parent)
			if pErr != nil {
				return fmt.Errorf("mount parent %s is not accessible: %w", parent, pErr)
			}
			if !pInfo.IsDir() {
				return fmt.Errorf("mount parent %s is not a directory; refusing to create %s underneath",
					parent, clean)
			}
			return nil
		}
		return fmt.Errorf("inspect mount root %s: %w", clean, err)
	}
	if info.IsDir() {
		return nil
	}
	// Not a directory — this is the clobber signature.
	if !ack {
		path, _ := mountsync.WriteIncidentReport(clean, &mountsync.MountRootInvariantError{
			Path:   clean,
			Kind:   classifyMountRootKind(info),
			Reason: fmt.Sprintf("mount root is not a directory (mode=%s, size=%d)", info.Mode().String(), info.Size()),
		})
		return fmt.Errorf(
			"refusing to start mount: %s exists but is not a directory (mode=%s). "+
				"This matches the clobber data-loss signature. Inspect the path, "+
				"back up anything you need, move it aside, then re-run with "+
				"--reset-after-clobber (or RELAYFILE_RESET_AFTER_CLOBBER=1). "+
				"Incident report: %s",
			clean, info.Mode().String(), path)
	}
	// Acknowledged. Move the offending file aside (do not rm — let the
	// operator examine it) and then let ensureMirrorLayout recreate the
	// directory.
	backup := fmt.Sprintf("%s.clobbered-%s", clean, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.Rename(clean, backup); err != nil {
		return fmt.Errorf("move clobbered mount root aside: %w", err)
	}
	log.Printf("mount root clobber acknowledged: %s moved to %s; recreating clean mount", clean, backup)
	return nil
}

// classifyMountRootKind reports the kind string used in incident reports.
func classifyMountRootKind(info os.FileInfo) string {
	switch {
	case info.Mode().IsRegular():
		return "regular_file"
	case info.Mode()&os.ModeSymlink != 0:
		return "symlink"
	default:
		return "other"
	}
}

func ensureMirrorLayout(localDir string) error {
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(localDir, "digests"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(localDir, ".skills"), 0o755); err != nil {
		return err
	}
	skillPath := filepath.Join(localDir, ".skills", "activity-summary.md")
	skillContent := []byte(activitySummarySkillMarkdown)
	if current, err := os.ReadFile(skillPath); err == nil {
		if !bytes.Equal(current, skillContent) {
			if err := writeFileAtomically(skillPath, skillContent, 0o644); err != nil {
				return err
			}
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := writeFileAtomically(skillPath, skillContent, 0o644); err != nil {
			return err
		}
	} else {
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

const activitySummarySkillMarkdown = `# activity-summary

Start activity questions from the digest surface before walking provider trees.

- Read ` + "`digests/yesterday.md`" + ` for yesterday, ` + "`digests/today.md`" + ` for today, or ` + "`digests/YYYY-MM-DD.md`" + ` for a specific UTC date.
- If a digest is missing or stale, run ` + "`relayfile digest rebuild --window yesterday`" + `, ` + "`--window today`" + `, ` + "`--window YYYY-MM-DD`" + `, ` + "`--window this-week`" + `, or ` + "`--window last-week`" + `.
- Use provider ` + "`_index.json`" + ` files for date filtering and only open individual entity files after the index has narrowed the set.
- Read ` + "`LAYOUT.md`" + ` and provider ` + "`<integration>/LAYOUT.md`" + ` files when you need path conventions or alias views.
`

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

func delegatedRelayfileTokenViaCloud(cloud cloudCredentials, workspaceID, agentName string, scopes []string) (delegatedauth.Bundle, error) {
	if agentName == "" {
		agentName = "relayfile-cli"
	}
	if len(scopes) == 0 {
		scopes = append([]string(nil), defaultJoinScopes...)
	}
	client, err := newAPIClient(cloud.APIURL, cloud.AccessToken)
	if err != nil {
		return delegatedauth.Bundle{}, err
	}
	var bundle delegatedauth.Bundle
	if err := client.postJSON(
		context.Background(),
		fmt.Sprintf("/api/v1/workspaces/%s/relayfile/delegated-token", url.PathEscape(workspaceID)),
		cloudRelayfileDelegatedTokenRequest{
			AgentName: agentName,
			Scopes:    scopes,
		},
		&bundle,
	); err != nil {
		return delegatedauth.Bundle{}, mapDelegatedTokenCloudError(err)
	}
	if bundle.Workspace() == "" {
		bundle.RelayfileWorkspaceID = workspaceID
	}
	if len(bundle.Scopes) == 0 {
		bundle.Scopes = append([]string(nil), scopes...)
	}
	if err := bundle.ValidateForUse(); err != nil {
		return delegatedauth.Bundle{}, fmt.Errorf("delegated relayfile credential bundle invalid: %w", err)
	}
	return bundle, nil
}

func bootstrapDelegatedCredentialsFromAgentRelay(workspaceValue string, scopes []string) (workspaceRecord, string, error) {
	cloudCreds, err := cloudCredentialsFromAgentRelay()
	if err != nil {
		return workspaceRecord{}, "", err
	}
	workspace, err := activeWorkspaceFromAgentRelay()
	if err != nil {
		return workspaceRecord{}, "", err
	}
	if !agentRelayWorkspaceMatches(workspaceValue, workspace) {
		return workspaceRecord{}, "", fmt.Errorf("active agent-relay workspace %s does not match requested workspace %q", workspace.Name, workspaceValue)
	}
	cloudWorkspaceID := firstNonEmpty(workspace.CloudWorkspaceID, workspace.ID, workspace.RelayfileWorkspaceID)
	if cloudWorkspaceID == "" {
		return workspaceRecord{}, "", errors.New("agent-relay workspace active --json did not include a cloud workspace id")
	}
	mintScopes := append([]string(nil), scopes...)
	if len(mintScopes) == 0 {
		mintScopes = append([]string(nil), defaultJoinScopes...)
	}
	recordScopes := delegatedWorkspaceCatalogScopes(cloudWorkspaceID, workspace, mintScopes)
	now := time.Now().UTC().Format(time.RFC3339)
	record := workspaceRecord{
		Name:             firstNonEmpty(workspace.Name, workspace.Slug, workspace.Key, cloudWorkspaceID, workspace.RelayfileWorkspaceID),
		ID:               cloudWorkspaceID,
		RelayWorkspaceID: strings.TrimSpace(workspace.RelayfileWorkspaceID),
		CreatedAt:        now,
		LastUsedAt:       now,
		AgentName:        "relayfile-cli",
		Scopes:           recordScopes,
	}
	delegated, err := delegatedRelayfileTokenViaCloud(cloudCreds, cloudWorkspaceID, record.AgentName, mintScopes)
	if err != nil {
		return workspaceRecord{}, "", err
	}
	credsPath := delegatedCredentialsPathForRequest(record.ID, mintScopes)
	if err := delegatedauth.SaveAtomic(credsPath, delegated); err != nil {
		return workspaceRecord{}, "", fmt.Errorf("persist delegated relayfile credentials: %w", err)
	}
	persisted, err := persistDelegatedWorkspace(record, delegated, "", true)
	if err != nil {
		return workspaceRecord{}, "", err
	}
	return persisted, credsPath, nil
}

func delegatedWorkspaceCatalogScopes(cloudWorkspaceID string, workspace agentRelayActiveWorkspace, fallback []string) []string {
	candidates := []string{
		cloudWorkspaceID,
		workspace.RelayfileWorkspaceID,
		workspace.Name,
		workspace.Slug,
		workspace.Key,
		workspace.ID,
	}
	for _, candidate := range candidates {
		if record, ok := workspaceRecordByID(candidate); ok && len(record.Scopes) > 0 {
			return append([]string(nil), record.Scopes...)
		}
		if record, ok := workspaceRecordByName(candidate); ok && len(record.Scopes) > 0 {
			return append([]string(nil), record.Scopes...)
		}
	}
	return append([]string(nil), fallback...)
}

func loadOrBootstrapDelegatedCredentials(workspaceValue string, scopes []string) (delegatedauth.Bundle, string, error) {
	bundle, path, err := loadDelegatedCredentialsForRequest("", workspaceValue, scopes)
	if err == nil {
		return bundle, path, nil
	}
	if !errors.Is(err, delegatedauth.ErrMissingCredentials) {
		return delegatedauth.Bundle{}, path, err
	}
	if _, bootstrappedPath, bootstrapErr := bootstrapDelegatedCredentialsFromAgentRelay(workspaceValue, scopes); bootstrapErr != nil {
		return delegatedauth.Bundle{}, path, bootstrapErr
	} else if strings.TrimSpace(bootstrappedPath) != "" {
		path = bootstrappedPath
	}
	return loadDelegatedCredentials(path)
}

func persistDelegatedWorkspace(record workspaceRecord, bundle delegatedauth.Bundle, localDir string, setDefault bool) (workspaceRecord, error) {
	relayWorkspaceID := bundle.Workspace()
	if relayWorkspaceID != "" {
		if strings.TrimSpace(record.ID) == "" {
			record.ID = relayWorkspaceID
		} else if strings.TrimSpace(record.ID) != relayWorkspaceID {
			record.RelayWorkspaceID = relayWorkspaceID
		}
	}
	record.Server = strings.TrimRight(bundle.ServerURL(), "/")
	record.LocalDir = localDir
	record.CloudAPIURL = ""
	record.LastUsedAt = time.Now().UTC().Format(time.RFC3339)
	if record.AgentName == "" {
		record.AgentName = "relayfile-cli"
	}
	if len(record.Scopes) == 0 {
		record.Scopes = append([]string(nil), defaultJoinScopes...)
	}
	return upsertWorkspaceDetails(record)
}

func ensureCloudIntegration(cloudAPIURL, workspaceID, workspaceToken, provider, requestedBackend, localDir string, timeout time.Duration, shouldOpenBrowser bool, stdout io.Writer) (bool, error) {
	savedConnection := loadSavedConnection(localDir, provider)
	connectionID := strings.TrimSpace(savedConnection.ConnectionID)
	savedBackend := strings.TrimSpace(savedConnection.Backend)
	if connectionID != "" && (requestedBackend == "" || requestedBackend == savedBackend) {
		if ready, err := cloudIntegrationReady(cloudAPIURL, workspaceID, workspaceToken, provider, connectionID); err == nil && ready {
			fmt.Fprintf(stdout, "%s already connected\n", provider)
			return false, nil
		}
	}
	if err := connectCloudIntegration(cloudAPIURL, workspaceID, workspaceToken, provider, requestedBackend, localDir, timeout, shouldOpenBrowser, stdout); err != nil {
		return false, err
	}
	return true, nil
}

func connectCloudIntegration(cloudAPIURL, workspaceID, workspaceToken, provider, requestedBackend, localDir string, timeout time.Duration, shouldOpenBrowser bool, stdout io.Writer) error {
	client, err := newAPIClient(cloudAPIURL, workspaceToken)
	if err != nil {
		return err
	}
	var session cloudConnectSessionResponse
	if err := client.postJSON(context.Background(), fmt.Sprintf("/api/v1/workspaces/%s/integrations/connect-session", url.PathEscape(workspaceID)), cloudConnectSessionRequest{
		AllowedIntegrations: []string{provider},
		RequestedBackend:    requestedBackend,
	}, &session); err != nil {
		// Per contract verdict §A12, a 409 from connect-session means the
		// requested provider is no longer in the catalog. Drop the cached
		// catalog so the next invocation pulls fresh provider ids before
		// surfacing the error to the user.
		if isAPIConflict(err) {
			invalidateIntegrationCatalogCache()
		}
		return fmt.Errorf("create %s connect session: %w", provider, err)
	}

	connectionID := strings.TrimSpace(session.ConnectionID)
	if connectionID == "" {
		connectionID = workspaceID
	}
	backend := strings.TrimSpace(session.Backend)
	if backend == "" {
		backend = requestedBackend
	}
	_ = saveIntegrationConnection(localDir, integrationConnectionState{
		Provider:     provider,
		ConnectionID: connectionID,
		Backend:      backend,
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
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	deadline := time.Now().Add(timeout)
	var pollErr error
	pollErr = politePoll(ctx, func(pollCtx context.Context) pollResult {
		status, err := cloudIntegrationStatus(cloudAPIURL, workspaceID, workspaceToken, provider, connectionID)
		if err != nil {
			pollErr = err
			return pollResult{err: err, httpStatus: httpStatusFromErr(err), retryAfter: retryAfterFromErr(err), done: true}
		}
		if !cloudIntegrationConnected(status) && connectionID != "" {
			fallbackStatus, fallbackErr := cloudIntegrationStatus(cloudAPIURL, workspaceID, workspaceToken, provider, "")
			if fallbackErr == nil && cloudIntegrationConnected(fallbackStatus) {
				status = fallbackStatus
			}
		}
		if cloudIntegrationConnected(status) {
			if resolvedConnectionID := strings.TrimSpace(status.ConnectionID); resolvedConnectionID != "" && resolvedConnectionID != connectionID {
				connectionID = resolvedConnectionID
				_ = saveIntegrationConnection(localDir, integrationConnectionState{
					Provider:     provider,
					ConnectionID: connectionID,
					Backend:      backend,
					ConnectedAt:  time.Now().UTC().Format(time.RFC3339),
					UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
				})
			}
			fmt.Fprintf(stdout, "%s connected. Files will appear under %s/%s as Relayfile syncs in the background.\n", provider, localDir, providerRootDir(provider))
			pollErr = nil
			return pollResult{done: true}
		}
		return pollResult{}
	}, politeOpts{})
	if pollErr != nil {
		return pollErr
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || time.Now().After(deadline) {
		return fmt.Errorf("timed out waiting for %s connection after %s", provider, timeout)
	}
	return nil
}

func cloudIntegrationReady(cloudAPIURL, workspaceID, workspaceToken, provider, connectionID string) (bool, error) {
	status, err := cloudIntegrationStatus(cloudAPIURL, workspaceID, workspaceToken, provider, connectionID)
	if err != nil {
		return false, err
	}
	return cloudIntegrationConnected(status), nil
}

func cloudIntegrationStatus(cloudAPIURL, workspaceID, workspaceToken, provider, connectionID string) (cloudIntegrationReadyResponse, error) {
	client, err := newAPIClient(cloudAPIURL, workspaceToken)
	if err != nil {
		return cloudIntegrationReadyResponse{}, err
	}
	query := url.Values{}
	if strings.TrimSpace(connectionID) != "" {
		query.Set("connectionId", connectionID)
	}
	var status cloudIntegrationReadyResponse
	if err := client.getJSON(context.Background(), fmt.Sprintf("/api/v1/workspaces/%s/integrations/%s/status?%s", url.PathEscape(workspaceID), url.PathEscape(provider), query.Encode()), &status); err != nil {
		return cloudIntegrationReadyResponse{}, fmt.Errorf("check %s connection: %w", provider, err)
	}
	return status, nil
}

func cloudIntegrationConnected(status cloudIntegrationReadyResponse) bool {
	if status.Ready {
		return true
	}
	switch strings.TrimSpace(strings.ToLower(status.State)) {
	case "connected", "oauth_connected", "sync_queued", "syncing", "ready":
		return true
	default:
		return false
	}
}

func waitForInitialSync(serverURL, token, workspaceID, provider, localDir string, timeout time.Duration, stdout io.Writer) error {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	client, err := newAPIClient(serverURL, token)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Waiting for %s initial sync. Leave this command running; Relayfile is preparing files in the background.\n", provider)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	lastPrinted := time.Time{}
	var finalStatus syncStatusResponse
	var ready bool
	pollErr := politePoll(ctx, func(pollCtx context.Context) pollResult {
		status, err := fetchWorkspaceSyncStatus(client, workspaceID)
		if err != nil {
			return pollResult{err: err, httpStatus: httpStatusFromErr(err), retryAfter: retryAfterFromErr(err)}
		}
		providerStatus, ok := syncProviderByName(status, provider)
		if ok && providerReadyForMirror(client, workspaceID, provider, providerStatus) {
			finalStatus = status
			ready = true
			return pollResult{done: true}
		}
		if time.Since(lastPrinted) >= 5*time.Second {
			lastPrinted = time.Now()
			if ok {
				fmt.Fprintf(stdout, "Syncing %s... lag %ds status=%s\n", provider, providerStatus.LagSeconds, providerStatus.Status)
			} else {
				fmt.Fprintf(stdout, "Waiting for %s sync status...\n", provider)
			}
		}
		return pollResult{}
	}, politeOpts{})

	if ready {
		return writeMirrorStateFile(localDir, buildSyncStateSnapshot(finalStatus, workspaceID, defaultMountMode, defaultMountInterval, localDir, readDaemonPID(localDir), ""))
	}
	if pollErr != nil && !errors.Is(pollErr, context.DeadlineExceeded) && !errors.Is(pollErr, context.Canceled) {
		return pollErr
	}
	fmt.Fprintf(stdout, "%s still syncing in the background. Files will continue to populate. See 'relayfile status'.\n", provider)
	return nil
}

// httpStatusFromErr extracts the HTTP status code from an apiError, returning
// 0 for network errors or non-apiError errors so that politePoll treats them
// as "transport failure, back off exponentially".
func httpStatusFromErr(err error) int {
	if err == nil {
		return 0
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// retryAfterFromErr extracts the parsed Retry-After hint from an apiError.
// Returns zero if there's no hint (e.g. a transport error or a response
// without the header).
func retryAfterFromErr(err error) time.Duration {
	if err == nil {
		return 0
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.RetryAfter
	}
	return 0
}

func runLogin(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", envOrDefault("RELAYFILE_SERVER", envOrDefault("RELAYFILE_BASE_URL", defaultServerURL)), "relayfile server URL (only used with --api-key)")
	token := fs.String("token", strings.TrimSpace(os.Getenv("RELAYFILE_TOKEN")), "relayfile API token")
	cloudAPIURL := fs.String("cloud-api-url", envOrDefault("RELAYFILE_CLOUD_API_URL", defaultCloudAPIURL), "Relayfile Cloud API URL")
	cloudToken := fs.String("cloud-token", strings.TrimSpace(os.Getenv("RELAYFILE_CLOUD_TOKEN")), "Relayfile Cloud access token; skips browser login when set")
	apiKey := fs.Bool("api-key", false, "use the legacy API-key flow against --server instead of the cloud browser login")
	noOpen := fs.Bool("no-open", false, "print the cloud sign-in URL instead of opening it")
	loginTimeout := fs.Duration("login-timeout", 5*time.Minute, "cloud login timeout")
	workspaceFlag := fs.String("workspace", "", "workspace name or id to refresh; defaults to the active workspace")
	skipWorkspace := fs.Bool("skip-workspace-refresh", false, "sign into the cloud only; do not refresh the workspace token")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server":                 true,
		"token":                  true,
		"cloud-api-url":          true,
		"cloud-token":            true,
		"api-key":                false,
		"no-open":                false,
		"login-timeout":          true,
		"workspace":              true,
		"skip-workspace-refresh": false,
	})); err != nil {
		return err
	}

	if *skipWorkspace && strings.TrimSpace(*workspaceFlag) != "" {
		return errors.New("--workspace cannot be used with --skip-workspace-refresh: pick one")
	}

	tokenValue := strings.TrimSpace(*token)

	// Token explicitly provided → legacy server-credential flow.
	if tokenValue != "" {
		return loginWithAPIKey(strings.TrimSpace(*server), tokenValue, stdout)
	}

	// Legacy interactive prompt, opt-in.
	if *apiKey {
		prompted, err := promptLine(stdin, stdout, "API key: ")
		if err != nil {
			return err
		}
		prompted = strings.TrimSpace(prompted)
		if prompted == "" {
			return errors.New("token is required")
		}
		return loginWithAPIKey(strings.TrimSpace(*server), prompted, stdout)
	}

	if (strings.TrimSpace(*cloudAPIURL) != "" && strings.TrimRight(strings.TrimSpace(*cloudAPIURL), "/") != defaultCloudAPIURL) ||
		strings.TrimSpace(*cloudToken) != "" || *loginTimeout != 5*time.Minute || *skipWorkspace || strings.TrimSpace(*workspaceFlag) != "" {
		fmt.Fprintln(stdout, "warning: relayfile login delegates cloud sign-in to agent-relay; relayfile cloud flags are deprecated")
	}
	if err := runAgentRelayLogin(stdin, stdout, *noOpen); err != nil {
		return err
	}
	if err := removeCredentialFile(cloudCredentialsPath()); err != nil {
		fmt.Fprintf(stdout, "warning: could not clear stale relayfile cloud credentials: %v\n", err)
	}
	if *skipWorkspace {
		fmt.Fprintln(stdout, "Relayfile now uses the active agent-relay cloud session.")
		return nil
	}
	record, _, err := bootstrapDelegatedCredentialsFromAgentRelay(strings.TrimSpace(*workspaceFlag), defaultJoinScopes)
	if err != nil {
		return fmt.Errorf("bootstrap delegated relayfile credentials: %w", err)
	}
	if err := removeCredentialFile(credentialsPath()); err != nil {
		fmt.Fprintf(stdout, "warning: could not clear stale relayfile credentials: %v\n", err)
	}
	fmt.Fprintf(stdout, "Relayfile now uses the active agent-relay cloud session and workspace %s.\n", record.Name)
	return nil
}

func runLogout(args []string, stdout io.Writer) error {
	if len(args) > 0 {
		return errors.New("usage: relayfile logout")
	}
	removed, err := clearAuthCredentials()
	if err != nil {
		return err
	}
	if removed == 0 {
		fmt.Fprintln(stdout, "Relayfile is already logged out.")
		fmt.Fprintln(stdout, "Agent Relay cloud session left intact; run `agent-relay cloud logout` to fully sign out.")
		return nil
	}
	fmt.Fprintln(stdout, "Logged out of Relayfile on this machine.")
	fmt.Fprintln(stdout, "Agent Relay cloud session left intact; run `agent-relay cloud logout` to fully sign out.")
	return nil
}

func loginWithAPIKey(serverValue, tokenValue string, stdout io.Writer) error {
	serverValue = strings.TrimSpace(serverValue)
	if serverValue == "" {
		serverValue = defaultServerURL
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
	if err := removeCredentialFile(cloudCredentialsPath()); err != nil {
		return fmt.Errorf("clear stale cloud credentials: %w", err)
	}
	fmt.Fprintf(stdout, "Stored credentials for %s\n", creds.Server)
	return nil
}

func runWorkspace(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("workspace subcommand is required: create, join, use, list, current, status, or delete")
	}
	switch args[0] {
	case "create":
		return runWorkspaceCreate(args[1:], stdout)
	case "join":
		return runWorkspaceJoin(args[1:], stdout)
	case "use":
		return runWorkspaceUse(args[1:], stdout)
	case "list":
		return runWorkspaceList(args[1:], stdout)
	case "current":
		return runWorkspaceCurrent(args[1:], stdout)
	case "status":
		return runWorkspaceStatus(args[1:], stdout)
	case "delete":
		return runWorkspaceDelete(args[1:], stdin, stdout)
	default:
		return fmt.Errorf("unknown workspace subcommand %q", args[0])
	}
}

func runIntegration(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("integration subcommand is required: connect, available, search, list, disconnect, adopt, set-metadata, bind, or unbind")
	}
	switch args[0] {
	case "connect":
		return runIntegrationConnect(args[1:], stdin, stdout)
	case "available", "catalog", "providers":
		return runIntegrationAvailable(args[1:], stdout)
	case "search":
		return runIntegrationSearch(args[1:], stdout)
	case "list":
		return runIntegrationList(args[1:], stdout)
	case "disconnect":
		return runIntegrationDisconnect(args[1:], stdin, stdout)
	case "adopt":
		return runIntegrationAdopt(args[1:], stdin, stdout)
	case "set-metadata":
		return runIntegrationSetMetadata(args[1:], stdin, stdout)
	case "bind":
		return runIntegrationBind(args[1:], stdout)
	case "resolve-path":
		return runIntegrationResolvePath(args[1:], stdout)
	case "unbind":
		return runIntegrationUnbind(args[1:], stdout)
	case "writeback-secret":
		return runIntegrationWritebackSecret(args[1:], stdout)
	default:
		return fmt.Errorf("unknown integration subcommand %q", args[0])
	}
}

func runIntegrationBind(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("integration bind", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	list := fs.Bool("list", false, "list active relay bindings as JSON")
	channel := fs.String("channel", "", "relay channel to receive provider records")
	webhookID := fs.String("webhook", "", "RelayCast inbound webhook id")
	webhookToken := fs.String("webhook-token", "", "RelayCast inbound webhook token")
	subscriptionID := fs.String("subscription", "", "relay integration subscription id")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"list":          false,
		"channel":       true,
		"webhook":       true,
		"webhook-token": true,
		"subscription":  true,
	})); err != nil {
		return err
	}
	if *list {
		if fs.NArg() != 0 {
			return errors.New("usage: relayfile integration bind --list")
		}
		bindings, err := readRelayIntegrationBindings()
		if err != nil {
			return err
		}
		return writeJSON(stdout, bindings)
	}
	if fs.NArg() != 2 {
		return errors.New("usage: relayfile integration bind PROVIDER RESOURCE_OR_PATH_GLOB --channel CHANNEL --webhook ID --webhook-token TOKEN")
	}
	provider := normalizeProviderID(fs.Arg(0))
	if err := validateLocalProviderID(provider); err != nil {
		return err
	}
	resolved, err := resolveIntegrationBindPathGlob(provider, fs.Arg(1))
	if err != nil {
		return err
	}
	binding := relayIntegrationBinding{
		Provider:       provider,
		PathGlob:       resolved.PathGlob,
		Channel:        strings.TrimSpace(*channel),
		WebhookID:      strings.TrimSpace(*webhookID),
		WebhookToken:   strings.TrimSpace(*webhookToken),
		SubscriptionID: strings.TrimSpace(*subscriptionID),
	}
	if binding.Channel == "" {
		return errors.New("--channel is required")
	}
	if binding.WebhookID == "" {
		return errors.New("--webhook is required")
	}
	if binding.WebhookToken == "" {
		return errors.New("--webhook-token is required")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	bindings, err := readRelayIntegrationBindings()
	if err != nil {
		return err
	}
	replaced := false
	for i := range bindings {
		if bindings[i].Provider == binding.Provider && bindings[i].PathGlob == binding.PathGlob {
			binding.CreatedAt = bindings[i].CreatedAt
			binding.UpdatedAt = now
			if binding.CreatedAt == "" {
				binding.CreatedAt = now
			}
			if binding.SubscriptionID == "" {
				binding.SubscriptionID = bindings[i].SubscriptionID
			}
			bindings[i] = binding
			replaced = true
			break
		}
	}
	if !replaced {
		binding.CreatedAt = now
		binding.UpdatedAt = now
		bindings = append(bindings, binding)
	}
	if err := writeRelayIntegrationBindings(bindings); err != nil {
		return err
	}
	if resolved.Warning != "" {
		fmt.Fprintf(stdout, "Warning: %s\n", resolved.Warning)
	}
	if replaced {
		fmt.Fprintf(stdout, "%s binding updated: %s -> %s\n", binding.Provider, binding.PathGlob, binding.Channel)
	} else {
		fmt.Fprintf(stdout, "%s binding created: %s -> %s\n", binding.Provider, binding.PathGlob, binding.Channel)
	}
	return nil
}

func runIntegrationResolvePath(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("integration resolve-path", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"json": false,
	})); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("usage: relayfile integration resolve-path PROVIDER RESOURCE [--json]")
	}
	provider := normalizeProviderID(fs.Arg(0))
	if err := validateLocalProviderID(provider); err != nil {
		return err
	}
	resolved, err := resolveIntegrationBindPathGlob(provider, fs.Arg(1))
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, struct {
			Provider string `json:"provider"`
			Resource string `json:"resource"`
			PathGlob string `json:"pathGlob"`
			Warning  string `json:"warning,omitempty"`
		}{
			Provider: provider,
			Resource: strings.TrimSpace(fs.Arg(1)),
			PathGlob: resolved.PathGlob,
			Warning:  resolved.Warning,
		})
	}
	if resolved.Warning != "" {
		fmt.Fprintf(stdout, "Warning: %s\n", resolved.Warning)
	}
	fmt.Fprintln(stdout, resolved.PathGlob)
	return nil
}

func runIntegrationUnbind(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("integration unbind", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	resource := fs.String("resource", "", "path glob/resource to unbind")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"resource": true,
	})); err != nil {
		return err
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		return errors.New("usage: relayfile integration unbind PROVIDER [RESOURCE_OR_PATH_GLOB|--resource RESOURCE_OR_PATH_GLOB]")
	}
	provider := normalizeProviderID(fs.Arg(0))
	if err := validateLocalProviderID(provider); err != nil {
		return err
	}
	pathGlob := strings.TrimSpace(*resource)
	if fs.NArg() == 2 {
		if pathGlob != "" {
			return errors.New("pass RESOURCE_OR_PATH_GLOB either positionally or with --resource, not both")
		}
		pathGlob = strings.TrimSpace(fs.Arg(1))
	}
	matchGlobs := []string{}
	if pathGlob != "" {
		matchGlobs = append(matchGlobs, pathGlob)
	}
	nativeResource := pathGlob != "" && !strings.HasPrefix(pathGlob, "/")
	if nativeResource {
		resolved, err := resolveIntegrationBindPathGlob(provider, pathGlob)
		if err != nil {
			return err
		}
		pathGlob = resolved.PathGlob
		matchGlobs = append([]string{pathGlob}, fallbackUnbindPathGlobsForNativeResource(provider)...)
		if resolved.Warning != "" {
			fmt.Fprintf(stdout, "Warning: %s\n", resolved.Warning)
		}
	}
	bindings, err := readRelayIntegrationBindings()
	if err != nil {
		return err
	}
	kept := bindings[:0]
	removed := 0
	for _, binding := range bindings {
		if binding.Provider == provider && (pathGlob == "" || pathGlobMatchesAny(binding.PathGlob, matchGlobs)) {
			removed++
			continue
		}
		kept = append(kept, binding)
	}
	if removed == 0 {
		if pathGlob != "" {
			return fmt.Errorf("no binding found for provider %q with path glob %q", provider, pathGlob)
		}
		return fmt.Errorf("no binding found for provider %q", provider)
	}
	if err := writeRelayIntegrationBindings(kept); err != nil {
		return err
	}
	if pathGlob == "" {
		fmt.Fprintf(stdout, "%s bindings removed: %d\n", provider, removed)
	} else {
		fmt.Fprintf(stdout, "%s binding removed: %s\n", provider, pathGlob)
	}
	return nil
}

// writebackSecretResponse is the relayfile-cloud GET writeback-secret payload.
type writebackSecretResponse struct {
	OK   bool `json:"ok"`
	Data struct {
		URL    string `json:"url"`
		Secret string `json:"secret"`
	} `json:"data"`
}

// runIntegrationWritebackSecret fetches the per-channel writeback signing secret
// (and ingress URL) for a relay channel from relayfile-cloud, over the
// authenticated relayfile session. The relay CLI calls this at `subscribe` time
// and signs the relay subscription with the returned secret, so there is no
// static shared secret to provision. The secret is derived server-side from the
// internal master key, so this endpoint just returns it to the workspace owner.
func runIntegrationWritebackSecret(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("integration writeback-secret", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceName := fs.String("workspace", "", "workspace name or id")
	channel := fs.String("channel", "", "relay channel the binding delivers to")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"workspace": true,
		"channel":   true,
		"json":      false,
	})); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: relayfile integration writeback-secret --channel CHANNEL [--workspace WS] [--json]")
	}
	channelValue := strings.TrimSpace(*channel)
	if channelValue == "" {
		return errors.New("--channel is required")
	}

	commandClient, err := prepareWorkspaceCommandClient(strings.TrimSpace(*workspaceName), "", "", defaultJoinScopes)
	if err != nil {
		return err
	}
	workspaceID := strings.TrimSpace(commandClient.workspaceID)
	if workspaceID == "" {
		return errors.New("could not resolve relayfile workspace id")
	}

	path := fmt.Sprintf(
		"/v1/workspaces/%s/integrations/relay/writeback-secret?channel=%s",
		url.PathEscape(workspaceID),
		url.QueryEscape(channelValue),
	)
	var resp writebackSecretResponse
	if err := commandClient.client.getJSON(context.Background(), path, &resp); err != nil {
		return fmt.Errorf("fetch writeback secret: %w", err)
	}
	if !resp.OK {
		return errors.New("relayfile-cloud returned an unsuccessful writeback-secret response")
	}
	if strings.TrimSpace(resp.Data.Secret) == "" {
		return errors.New("relayfile-cloud returned an empty writeback secret")
	}

	if *jsonOutput {
		return writeJSON(stdout, resp.Data)
	}
	fmt.Fprintf(stdout, "url: %s\nsecret: %s\n", resp.Data.URL, resp.Data.Secret)
	return nil
}

func runIntegrationConnect(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("integration connect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceName := fs.String("workspace", "", "workspace name or id")
	cloudAPIURL := fs.String("cloud-api-url", envOrDefault("RELAYFILE_CLOUD_API_URL", defaultCloudAPIURL), "Relayfile Cloud API URL")
	backend := fs.String("backend", "", "integration backend to request (nango or composio)")
	noOpen := fs.Bool("no-open", false, "print the hosted URL instead of opening it")
	timeout := fs.Duration("timeout", 5*time.Minute, "integration readiness timeout")
	waitSync := fs.Bool("wait-sync", false, "wait for initial sync before returning")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"workspace":     true,
		"cloud-api-url": true,
		"backend":       true,
		"no-open":       false,
		"timeout":       true,
		"wait-sync":     false,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile integration connect PROVIDER [--backend BACKEND] [--workspace NAME] [--no-open] [--timeout 5m] [--wait-sync]")
	}
	provider := normalizeProviderID(fs.Arg(0))
	requestedBackend, err := normalizeIntegrationBackend(*backend)
	if err != nil {
		return err
	}
	record, err := resolveWorkspaceRecord(strings.TrimSpace(*workspaceName))
	if err != nil {
		return err
	}
	cloudCreds, err := ensureCloudCredentials(strings.TrimSpace(*cloudAPIURL), "", 5*time.Minute, false, stdout)
	if err != nil {
		return err
	}
	delegated, err := delegatedRelayfileTokenViaCloud(cloudCreds, record.ID, record.AgentName, record.Scopes)
	if err != nil {
		return err
	}
	if err := delegatedauth.SaveAtomic(delegatedCredentialsPathForRequest(record.ID, record.Scopes), delegated); err != nil {
		return fmt.Errorf("persist delegated relayfile credentials: %w", err)
	}
	record, err = persistDelegatedWorkspace(record, delegated, record.LocalDir, true)
	if err != nil {
		return err
	}
	createdConnection, err := ensureCloudIntegration(cloudCreds.APIURL, record.ID, cloudCreds.AccessToken, provider, requestedBackend, record.LocalDir, *timeout, !*noOpen, stdout)
	if err != nil {
		return err
	}
	relayWorkspaceID := delegated.Workspace()
	if relayWorkspaceID != "" && relayWorkspaceID != record.ID {
		fmt.Fprintf(stdout, "Workspace: %s (Relayfile runtime: %s)\n", record.ID, relayWorkspaceID)
	}
	// Atlassian-family providers: a single OAuth grant can cover multiple
	// sites (cloudIds). Cloud's Jira/Confluence sync bails with a clear
	// error if `metadata.cloudId` is unset and the grant has >1 site.
	// Run the site picker here, after OAuth completes but before we wait
	// on the initial sync, so the operator picks a target without having
	// to dive into the Nango dashboard. For other providers this is a
	// no-op so we preserve the existing flow.
	if createdConnection && isAtlassianProvider(provider) {
		if err := runAtlassianSitePicker(cloudCreds.APIURL, record.ID, cloudCreds.AccessToken, provider, stdin, stdout); err != nil {
			return err
		}
	}
	if *waitSync {
		return waitForInitialSync(delegated.ServerURL(), delegated.BearerToken(), relayWorkspaceID, provider, record.LocalDir, *timeout, stdout)
	}
	if record.LocalDir != "" {
		fmt.Fprintf(stdout, "Run `relayfile mount %s %s` to mirror files locally, or rerun with --wait-sync to block until initial data is ready.\n", record.ID, record.LocalDir)
	} else {
		fmt.Fprintln(stdout, "Run `relayfile mount` to mirror files locally, or rerun with --wait-sync to block until initial data is ready.")
	}
	return nil
}

func isAtlassianProvider(provider string) bool {
	switch provider {
	case "jira", "confluence":
		return true
	}
	return false
}

// accessibleResourceEntry mirrors the SDK-facing shape returned by Cloud's
// GET .../accessible-resources route. Cloud already normalizes the upstream
// Atlassian payload, so the CLI only needs id+url here. We keep name and
// avatarUrl so the picker can render a friendly label.
type accessibleResourceEntry struct {
	ID        string   `json:"id"`
	URL       string   `json:"url"`
	Name      string   `json:"name,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
	AvatarURL string   `json:"avatarUrl,omitempty"`
}

type accessibleResourcesResponse struct {
	OK        bool                      `json:"ok"`
	Resources []accessibleResourceEntry `json:"resources"`
}

// runAtlassianSitePicker fetches the accessible resources for `provider`
// and, depending on how many came back, either:
//   - 0: warns and exits (something is upstream-wrong; surfacing as error
//     prevents the operator from sitting through a 5-minute initial-sync
//     wait that will never succeed).
//   - 1: auto-binds the single site as { cloudId, baseUrl } and logs a
//     "auto-selected" line so the operator sees what happened.
//   - >1: prompts interactively with a numbered picker. Default to 1 on
//     blank input, validate range, retry up to 3 times on invalid input.
//
// The picked metadata is forwarded to Cloud's PUT .../metadata which
// hands it to nango.setMetadata. On success the CLI prints a confirmation
// before falling through to the initial-sync wait so the operator knows
// which site they bound.
func runAtlassianSitePicker(cloudAPIURL, workspaceID, workspaceToken, provider string, stdin io.Reader, stdout io.Writer) error {
	client, err := newAPIClient(cloudAPIURL, workspaceToken)
	if err != nil {
		return err
	}
	var resp accessibleResourcesResponse
	if err := client.getJSON(
		context.Background(),
		fmt.Sprintf("/api/v1/workspaces/%s/integrations/%s/accessible-resources", url.PathEscape(workspaceID), url.PathEscape(provider)),
		&resp,
	); err != nil {
		return fmt.Errorf("list %s accessible resources: %w", provider, err)
	}
	resources := resp.Resources
	if len(resources) == 0 {
		// Upstream gave us zero sites — there's nothing we can bind, so
		// continuing the connect flow would just bake in a guaranteed
		// failure later. Fail fast with a message that points at the
		// likely root causes (revoked OAuth grant or missing scopes).
		fmt.Fprintf(stdout, "Warning: %s reports zero accessible sites for this OAuth grant.\n", provider)
		fmt.Fprintln(stdout, "  Check that the operator who completed OAuth has access to at least one Atlassian site,")
		fmt.Fprintln(stdout, "  and that the grant covers the required scopes.")
		return fmt.Errorf("%s accessible-resources returned 0 sites", provider)
	}
	var chosen accessibleResourceEntry
	if len(resources) == 1 {
		chosen = resources[0]
		label := chosen.URL
		if chosen.Name != "" {
			label = fmt.Sprintf("%s (%s)", chosen.Name, chosen.URL)
		}
		fmt.Fprintf(stdout, "Auto-selected site %s for %s\n", label, provider)
	} else {
		picked, err := promptAtlassianSiteChoice(resources, stdin, stdout)
		if err != nil {
			return err
		}
		chosen = picked
	}
	metadata := map[string]any{
		"cloudId": chosen.ID,
		"baseUrl": chosen.URL,
	}
	bodyBytes, err := json.Marshal(map[string]any{"metadata": metadata})
	if err != nil {
		return err
	}
	if _, _, err := client.do(
		context.Background(),
		http.MethodPut,
		fmt.Sprintf("/api/v1/workspaces/%s/integrations/%s/metadata", url.PathEscape(workspaceID), url.PathEscape(provider)),
		bodyBytes,
	); err != nil {
		return fmt.Errorf("set %s metadata: %w", provider, err)
	}
	fmt.Fprintf(stdout, "%s metadata set: cloudId=%s baseUrl=%s\n", provider, chosen.ID, chosen.URL)
	return nil
}

// promptAtlassianSiteChoice renders the numbered picker and parses the
// operator's response. Defaults to entry 1 on blank input. Retries up to
// 3 times on invalid input (non-numeric or out-of-range) before erroring
// out so an automated/dumb-pipe stdin doesn't loop forever.
//
// We construct a single bufio.Reader for the entire picker rather than
// going through `promptLine`, because `promptLine` creates a new bufio
// reader per call and drops any bytes the previous call buffered past
// the first newline — which breaks the retry loop when stdin is a
// pre-loaded reader (as in tests / piped scripts).
func promptAtlassianSiteChoice(resources []accessibleResourceEntry, stdin io.Reader, stdout io.Writer) (accessibleResourceEntry, error) {
	fmt.Fprintln(stdout, "Multiple Atlassian sites available; choose which to bind to this workspace:")
	for i, r := range resources {
		label := r.URL
		if r.Name != "" {
			label = fmt.Sprintf("%s  (%s)", r.URL, r.Name)
		}
		fmt.Fprintf(stdout, "  [%d] %s  (cloudId=%s)\n", i+1, label, r.ID)
	}
	reader := bufio.NewReader(stdin)
	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if _, err := io.WriteString(stdout, "Site number [1]: "); err != nil {
			return accessibleResourceEntry{}, err
		}
		raw, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return accessibleResourceEntry{}, err
		}
		answer := strings.TrimSpace(raw)
		if answer == "" {
			// Two cases: operator hit enter (default to 1) OR stdin is
			// closed/exhausted. Either way the "default to 1" behaviour
			// is operator-friendly and matches the prompt text.
			if errors.Is(err, io.EOF) && raw == "" {
				// Truly empty stdin — accept default and bail before we
				// loop forever on the same EOF.
				return resources[0], nil
			}
			return resources[0], nil
		}
		idx, err := strconv.Atoi(answer)
		if err != nil || idx < 1 || idx > len(resources) {
			fmt.Fprintf(stdout, "Invalid choice %q; pick a number between 1 and %d.\n", answer, len(resources))
			continue
		}
		return resources[idx-1], nil
	}
	return accessibleResourceEntry{}, fmt.Errorf("could not read a valid site selection after %d attempts", maxAttempts)
}

func runIntegrationSearch(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("integration search", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cloudAPIURL := fs.String("cloud-api-url", envOrDefault("RELAYFILE_CLOUD_API_URL", defaultCloudAPIURL), "Relayfile Cloud API URL")
	backend := fs.String("backend", "", "filter by backend (nango or composio)")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	refresh := fs.Bool("refresh", false, "refresh the cached provider catalog")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"cloud-api-url": true,
		"backend":       true,
		"json":          false,
		"refresh":       false,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile integration search QUERY [--backend BACKEND] [--json] [--refresh]")
	}
	availableArgs := []string{
		"--cloud-api-url", *cloudAPIURL,
		"--search", fs.Arg(0),
	}
	if strings.TrimSpace(*backend) != "" {
		availableArgs = append(availableArgs, "--backend", *backend)
	}
	if *jsonOutput {
		availableArgs = append(availableArgs, "--json")
	}
	if *refresh {
		availableArgs = append(availableArgs, "--refresh")
	}
	return runIntegrationAvailable(availableArgs, stdout)
}

func runIntegrationAvailable(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("integration available", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cloudAPIURL := fs.String("cloud-api-url", envOrDefault("RELAYFILE_CLOUD_API_URL", defaultCloudAPIURL), "Relayfile Cloud API URL")
	backend := fs.String("backend", "", "filter by backend (nango or composio)")
	search := fs.String("search", "", "search provider id, display name, category, or backend")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	refresh := fs.Bool("refresh", false, "refresh the cached provider catalog")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"cloud-api-url": true,
		"backend":       true,
		"search":        true,
		"json":          false,
		"refresh":       false,
	})); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: relayfile integration available [--search QUERY] [--backend BACKEND] [--json] [--refresh]")
	}
	requestedBackend, err := normalizeIntegrationBackend(*backend)
	if err != nil {
		return err
	}
	entries, err := loadAvailableIntegrationCatalog(strings.TrimSpace(*cloudAPIURL), *refresh)
	if err != nil {
		return err
	}
	filterBackend := requestedBackend
	if filterBackend != "" && !catalogEntriesHaveBackendMetadata(entries) {
		filterBackend = ""
	}
	entries = filterIntegrationCatalog(entries, filterBackend, strings.TrimSpace(*search))
	if *jsonOutput {
		return writeJSON(stdout, entries)
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "No matching integrations")
		return nil
	}
	fmt.Fprintln(stdout, "provider\tdisplay_name\tbackends\tvfs_root")
	for _, entry := range entries {
		fmt.Fprintf(
			stdout,
			"%s\t%s\t%s\t%s\n",
			entry.ID,
			defaultIfBlank(entry.DisplayName, "-"),
			formatCatalogBackends(entry),
			defaultIfBlank(entry.VFSRoot, "-"),
		)
	}
	return nil
}

func filterIntegrationCatalog(entries []integrationCatalogEntry, backend, query string) []integrationCatalogEntry {
	filtered := make([]integrationCatalogEntry, 0, len(entries))
	query = strings.ToLower(strings.TrimSpace(query))
	for _, entry := range entries {
		if backend != "" && !catalogEntrySupportsBackend(entry, backend) {
			continue
		}
		if query != "" && !catalogEntryMatchesQuery(entry, query) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func catalogEntrySupportsBackend(entry integrationCatalogEntry, backend string) bool {
	if strings.EqualFold(entry.Backend, backend) {
		return true
	}
	for _, candidate := range entry.Backends {
		if strings.EqualFold(candidate, backend) {
			return true
		}
	}
	return false
}

func catalogEntriesHaveBackendMetadata(entries []integrationCatalogEntry) bool {
	for _, entry := range entries {
		if strings.TrimSpace(entry.Backend) != "" || len(entry.Backends) > 0 {
			return true
		}
	}
	return false
}

func catalogEntryMatchesQuery(entry integrationCatalogEntry, query string) bool {
	values := []string{
		entry.ID,
		entry.DisplayName,
		entry.ConfigKey,
		entry.VFSRoot,
		entry.Backend,
		entry.AuthMode,
		entry.Docs,
	}
	values = append(values, entry.Backends...)
	values = append(values, entry.Sources...)
	values = append(values, entry.Categories...)
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

func formatCatalogBackends(entry integrationCatalogEntry) string {
	backends := append([]string{}, entry.Backends...)
	if len(backends) == 0 && strings.TrimSpace(entry.Backend) != "" {
		backends = append(backends, strings.TrimSpace(entry.Backend))
	}
	if len(backends) == 0 {
		return "-"
	}
	sort.Strings(backends)
	return strings.Join(backends, ",")
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
	client, err := newAPIClient(cloudCreds.APIURL, cloudCreds.AccessToken)
	if err != nil {
		return err
	}
	var entries []cloudIntegrationListEntry
	if err := client.getJSON(context.Background(), fmt.Sprintf("/api/v1/workspaces/%s/integrations", url.PathEscape(record.ID)), &entries); err != nil {
		return err
	}
	entries = overlayIntegrationListRuntimeStatus(entries, record)
	if *jsonOutput {
		return writeJSON(stdout, entries)
	}
	if relayWorkspaceID := strings.TrimSpace(record.RelayWorkspaceID); relayWorkspaceID != "" && relayWorkspaceID != record.ID {
		fmt.Fprintf(stdout, "Workspace: %s (Relayfile runtime: %s)\n", record.ID, relayWorkspaceID)
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

func overlayIntegrationListRuntimeStatus(entries []cloudIntegrationListEntry, record workspaceRecord) []cloudIntegrationListEntry {
	if len(entries) == 0 {
		return entries
	}
	runtimeStatus, ok := runtimeSyncStatusForIntegrationList(record)
	if !ok {
		return entries
	}
	overlaid := append([]cloudIntegrationListEntry(nil), entries...)
	for i := range overlaid {
		runtimeProvider, found := syncProviderByName(runtimeStatus, overlaid[i].Provider)
		if !found || !syncProviderHasDataReadyEvidence(runtimeProvider) || !cloudIntegrationListStatusUpgradeable(overlaid[i].Status) {
			continue
		}
		overlaid[i].Status = "ready"
		if strings.TrimSpace(overlaid[i].LastEventAt) == "" && hasNonEmptyString(runtimeProvider.WatermarkTs) {
			overlaid[i].LastEventAt = strings.TrimSpace(*runtimeProvider.WatermarkTs)
		}
	}
	return overlaid
}

func runtimeSyncStatusForIntegrationList(record workspaceRecord) (syncStatusResponse, bool) {
	workspaceID := strings.TrimSpace(record.ID)
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(record.RelayWorkspaceID)
	}
	scopes := normalizedScopeSet(record.Scopes)
	if len(scopes) == 0 {
		scopes = append([]string(nil), defaultJoinScopes...)
	}
	bundle, _, err := loadDelegatedCredentialsForRequest("", workspaceID, scopes)
	if err != nil {
		return syncStatusResponse{}, false
	}
	client, err := newAPIClient(bundle.ServerURL(), bundle.BearerToken())
	if err != nil {
		return syncStatusResponse{}, false
	}
	runtimeWorkspaceID := strings.TrimSpace(bundle.Workspace())
	if runtimeWorkspaceID == "" {
		runtimeWorkspaceID = workspaceID
	}
	status, err := fetchWorkspaceSyncStatus(client, runtimeWorkspaceID)
	if err != nil {
		return syncStatusResponse{}, false
	}
	return status, true
}

func syncProviderHasDataReadyEvidence(status syncProviderStatus) bool {
	if status.Ready {
		return true
	}
	switch strings.TrimSpace(strings.ToLower(status.Status)) {
	case "ready":
		return true
	case "healthy":
		return hasNonEmptyString(status.Cursor) || hasNonEmptyString(status.WatermarkTs)
	default:
		return false
	}
}

func cloudIntegrationListStatusUpgradeable(status string) bool {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "connected", "oauth_connected", "sync_queued", "syncing":
		return true
	default:
		return false
	}
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
	client, err := newAPIClient(cloudCreds.APIURL, cloudCreds.AccessToken)
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

// runIntegrationAdopt asks Cloud to bind an existing Nango connection to the
// current workspace + provider slot without re-running OAuth. Mirrors
// runIntegrationDisconnect's flag parsing and confirmation prompt because the
// failure mode of a bad adopt (overwriting a live binding for a different
// tenant) is comparable to that of a bad disconnect.
//
// The local on-disk state is updated via saveIntegrationConnection so the
// mount + status probes pick up the new connectionId without requiring the
// operator to also run `relayfile integration connect` afterwards. We do this
// only after Cloud reports success, so a 409/404 from Cloud leaves the local
// state untouched.
func runIntegrationAdopt(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("integration adopt", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceName := fs.String("workspace", "", "workspace name or id")
	cloudAPIURL := fs.String("cloud-api-url", envOrDefault("RELAYFILE_CLOUD_API_URL", defaultCloudAPIURL), "Relayfile Cloud API URL")
	connectionID := fs.String("connection-id", "", "Nango connection id to adopt (required)")
	providerConfigKey := fs.String("provider-config-key", "", "optional Nango providerConfigKey override")
	yes := fs.Bool("yes", false, "skip confirmation")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"workspace":           true,
		"cloud-api-url":       true,
		"connection-id":       true,
		"provider-config-key": true,
		"yes":                 false,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile integration adopt PROVIDER --connection-id ID [--workspace NAME] [--provider-config-key KEY] [--yes]")
	}
	provider := normalizeProviderID(fs.Arg(0))
	if err := validateLocalProviderID(provider); err != nil {
		return err
	}
	trimmedConnectionID := strings.TrimSpace(*connectionID)
	if trimmedConnectionID == "" {
		return errors.New("--connection-id is required")
	}
	trimmedProviderConfigKey := strings.TrimSpace(*providerConfigKey)
	record, err := resolveWorkspaceRecord(strings.TrimSpace(*workspaceName))
	if err != nil {
		return err
	}
	if !*yes {
		answer, err := promptLine(stdin, stdout, fmt.Sprintf("Adopt %s connection %q into workspace %q? [y/N]: ", provider, trimmedConnectionID, record.Name))
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
	client, err := newAPIClient(cloudCreds.APIURL, cloudCreds.AccessToken)
	if err != nil {
		return err
	}
	requestBody := map[string]string{"connectionId": trimmedConnectionID}
	if trimmedProviderConfigKey != "" {
		requestBody["providerConfigKey"] = trimmedProviderConfigKey
	}
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return err
	}
	respBody, _, err := client.do(
		context.Background(),
		http.MethodPost,
		fmt.Sprintf("/api/v1/workspaces/%s/integrations/%s/adopt", url.PathEscape(record.ID), url.PathEscape(provider)),
		bodyBytes,
	)
	if err != nil {
		return err
	}
	var parsed struct {
		Ok                   bool   `json:"ok"`
		ConnectionID         string `json:"connectionId"`
		ReplacedConnectionID string `json:"replacedConnectionId"`
	}
	if len(respBody) > 0 {
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			return fmt.Errorf("parse adopt response: %w", err)
		}
	}
	boundConnectionID := strings.TrimSpace(parsed.ConnectionID)
	if boundConnectionID == "" {
		boundConnectionID = trimmedConnectionID
	}
	// Persist the new binding locally only after Cloud confirms success.
	// On a 4xx the saved connection on disk still reflects the previous
	// state, which is what the operator wants — a failed adopt is a no-op
	// for the workspace's local view.
	if err := saveIntegrationConnection(record.LocalDir, integrationConnectionState{
		Provider:     provider,
		ConnectionID: boundConnectionID,
		Backend:      "nango",
		ConnectedAt:  time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return err
	}
	// Remove any disconnect marker left from a prior `disconnect` so the
	// status probe doesn't keep reporting the workspace as disconnected.
	if record.LocalDir != "" {
		markerPath := filepath.Join(record.LocalDir, ".relay", "disconnected", provider+".json")
		if err := os.Remove(markerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove disconnect marker: %w", err)
		}
	}
	replaced := strings.TrimSpace(parsed.ReplacedConnectionID)
	if replaced != "" {
		fmt.Fprintf(stdout, "%s adopted into workspace %s (connectionId=%s, replaced previous connection %s)\n", provider, record.Name, boundConnectionID, replaced)
	} else {
		fmt.Fprintf(stdout, "%s adopted into workspace %s (connectionId=%s)\n", provider, record.Name, boundConnectionID)
	}
	return nil
}

// runIntegrationSetMetadata is a general-purpose verb for writing operator-
// controlled connection metadata. Today the Atlassian post-OAuth picker
// (`integration connect jira`) uses the same endpoint to set `cloudId` and
// `baseUrl` automatically. This verb exists for the cases the auto-picker
// doesn't cover: changing metadata post-connect, setting non-Atlassian
// connection-level config (e.g. linear `team_id`, a custom API host), or
// scripting metadata writes from CI.
//
// Cloud forwards the payload to `nango.setMetadata` (full replacement, not
// merge). v1 accepts flat KEY=VALUE pairs only; nested keys are documented
// as a future enhancement so the prompt and confirmation stay legible.
func runIntegrationSetMetadata(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("integration set-metadata", flag.ContinueOnError)
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
	rest := fs.Args()
	if len(rest) < 2 {
		return errors.New("usage: relayfile integration set-metadata PROVIDER KEY=VALUE [KEY=VALUE...] [--workspace NAME] [--yes]\n\n" +
			"  v1 accepts flat KEY=VALUE pairs only; nested keys are not yet supported.\n" +
			"  Example: relayfile integration set-metadata jira cloudId=abc-123 baseUrl=https://foo.atlassian.net")
	}
	provider := normalizeProviderID(rest[0])
	if err := validateLocalProviderID(provider); err != nil {
		return err
	}
	metadata := map[string]any{}
	for _, raw := range rest[1:] {
		key, value, ok := strings.Cut(raw, "=")
		if !ok {
			return fmt.Errorf("metadata argument %q is not in KEY=VALUE form", raw)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			return fmt.Errorf("metadata argument %q has an empty key", raw)
		}
		if strings.ContainsAny(key, ".[]") {
			return fmt.Errorf("metadata key %q must be flat; nested keys are not supported yet", key)
		}
		metadata[key] = value
	}
	record, err := resolveWorkspaceRecord(strings.TrimSpace(*workspaceName))
	if err != nil {
		return err
	}
	if !*yes {
		fmt.Fprintf(stdout, "About to set %s metadata in workspace %q:\n", provider, record.Name)
		// Print keys in alphabetical order so the confirmation is stable
		// across runs and easy to eyeball.
		keys := make([]string, 0, len(metadata))
		for k := range metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(stdout, "  %s = %v\n", k, metadata[k])
		}
		answer, err := promptLine(stdin, stdout, "Continue? [y/N]: ")
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
	client, err := newAPIClient(cloudCreds.APIURL, cloudCreds.AccessToken)
	if err != nil {
		return err
	}
	bodyBytes, err := json.Marshal(map[string]any{"metadata": metadata})
	if err != nil {
		return err
	}
	if _, _, err := client.do(
		context.Background(),
		http.MethodPut,
		fmt.Sprintf("/api/v1/workspaces/%s/integrations/%s/metadata", url.PathEscape(record.ID), url.PathEscape(provider)),
		bodyBytes,
	); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s metadata updated in workspace %s\n", provider, record.Name)
	return nil
}

// deadLetterRecord matches the on-disk shape produced by the mount writeback
// path per contract §8.4. Fields are kept lenient so we can read records
// written by older syncer versions without failing the list output.
type deadLetterRecord struct {
	OpID            string `json:"opId"`
	Path            string `json:"path,omitempty"`
	Code            string `json:"code,omitempty"`
	Message         string `json:"message,omitempty"`
	CreatedAt       string `json:"createdAt,omitempty"`
	LastAttemptedAt string `json:"lastAttemptedAt,omitempty"`
	Attempts        int    `json:"attempts,omitempty"`
	LastStatus      int    `json:"lastStatus,omitempty"`
	LastBody        string `json:"lastBody,omitempty"`
	Timestamp       string `json:"ts,omitempty"`
	ReplayURL       string `json:"replayUrl,omitempty"`
}

type deadLetterErrorDetail struct {
	Code             string          `json:"code"`
	Message          string          `json:"message"`
	ProviderStatus   int             `json:"providerStatus,omitempty"`
	ProviderResponse json.RawMessage `json:"providerResponse,omitempty"`
	Attempts         int             `json:"attempts"`
	FirstAttemptAt   string          `json:"firstAttemptAt"`
	LastAttemptAt    string          `json:"lastAttemptAt"`
	OpID             string          `json:"opId"`
}

type writebackStatusDeadLetter struct {
	OpID       string `json:"opId"`
	Path       string `json:"path,omitempty"`
	LastStatus int    `json:"lastStatus"`
	TS         string `json:"ts,omitempty"`
}

type writebackStatusReport struct {
	WorkspaceID         string                      `json:"workspaceId"`
	Pending             int                         `json:"pending"`
	Failed              uint64                      `json:"failed"`
	DeadLettered        []writebackStatusDeadLetter `json:"deadLettered"`
	LastErrorByProvider map[string]string           `json:"lastErrorByProvider"`
}

type writebackPushResolvedPath struct {
	LocalPath   string
	MountRoot   string
	WorkspaceID string
	RemoteRoot  string
	RemotePath  string
}

type writebackPushReceipt struct {
	CommandID       string           `json:"commandId"`
	WorkspaceID     string           `json:"workspaceId"`
	RemotePath      string           `json:"remotePath"`
	Action          string           `json:"action,omitempty"`
	ContentType     string           `json:"contentType"`
	Content         string           `json:"content"`
	Encoding        string           `json:"encoding,omitempty"`
	Hash            string           `json:"hash"`
	Exists          bool             `json:"exists"`
	Status          string           `json:"status"`
	FirstSeenAt     string           `json:"firstSeenAt"`
	LastAttemptAt   string           `json:"lastAttemptAt,omitempty"`
	NextAttemptAt   string           `json:"nextAttemptAt,omitempty"`
	AttemptCount    int              `json:"attemptCount"`
	LastError       string           `json:"lastError,omitempty"`
	NeedsAttention  bool             `json:"needsAttention,omitempty"`
	OpID            string           `json:"opId,omitempty"`
	DispatchStatus  string           `json:"dispatchStatus,omitempty"`
	AckedAt         string           `json:"ackedAt,omitempty"`
	Revision        string           `json:"revision,omitempty"`
	CorrelationID   string           `json:"correlationId,omitempty"`
	ContentIdentity *contentIdentity `json:"contentIdentity,omitempty"`
}

// withoutBody returns a copy with the file content stripped. Persisted
// terminal receipts (acked/failed) and any printed receipt must not retain or
// log arbitrary user file contents beyond the in-flight writeback; only the
// transient pending receipt keeps the body so a retry can resend it.
func (r writebackPushReceipt) withoutBody() writebackPushReceipt {
	r.Content = ""
	r.Encoding = ""
	return r
}

type workspaceHealthReport struct {
	WorkspaceID                string `json:"workspaceId"`
	Name                       string `json:"name,omitempty"`
	LocalDir                   string `json:"localDir,omitempty"`
	Status                     string `json:"status,omitempty"`
	LastSuccessfulReconcileAt  string `json:"lastSuccessfulReconcileAt,omitempty"`
	LastReconcileAt            string `json:"lastReconcileAt,omitempty"`
	LastError                  string `json:"lastError,omitempty"`
	StuckEventCount            int    `json:"stuckEventCount"`
	OutboxPending              int    `json:"outboxPending"`
	OutboxFailed               int    `json:"outboxFailed"`
	OutboxAcked                int    `json:"outboxAcked"`
	IncrementalBacklogDraining bool   `json:"incrementalBacklogDraining,omitempty"`
}

var errWritebackFailuresPresent = errors.New("writeback failures present")

type writebackCommandMode string

const (
	writebackCommandPush   writebackCommandMode = "push"
	writebackCommandUpdate writebackCommandMode = "update"
	writebackCommandDelete writebackCommandMode = "delete"
)

func deadLetterDirFor(localDir string) string {
	return filepath.Join(localDir, ".relay", "dead-letter")
}

func deadLetterErrorPathFor(localDir, opID string) string {
	return filepath.Join(deadLetterDirFor(localDir), opID+".error.json")
}

func runWritebackPush(args []string, stdout io.Writer) error {
	return runWritebackFileMutation(writebackCommandPush, args, stdout)
}

func runWritebackUpdate(args []string, stdout io.Writer) error {
	return runWritebackFileMutation(writebackCommandUpdate, args, stdout)
}

func runWritebackDelete(args []string, stdout io.Writer) error {
	return runWritebackFileMutation(writebackCommandDelete, args, stdout)
}

func runWritebackFileMutation(mode writebackCommandMode, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("writeback "+string(mode), flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceName := fs.String("workspace", "", "workspace name or id")
	server := fs.String("server", "", "relayfile server URL override")
	tokenOverride := fs.String("token", "", "relayfile token override")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	timeout := fs.Duration("timeout", 90*time.Second, "operation receipt wait timeout")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"workspace": true,
		"server":    true,
		"token":     true,
		"json":      false,
		"timeout":   true,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: relayfile writeback %s LOCAL_PATH [--workspace WS] [--json] [--timeout 90s]", mode)
	}

	resolved, err := resolveWritebackPushPath(fs.Arg(0), strings.TrimSpace(*workspaceName))
	if err != nil {
		return err
	}
	if mode != writebackCommandPush && !isCanonicalWritebackTargetPath(resolved.RemotePath) {
		return fmt.Errorf("writeback %s requires a canonical provider record path, got %s", mode, resolved.RemotePath)
	}
	joinScopes, requiredRelayfileScopes, err := writebackPushScopes(resolved.RemotePath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*tokenOverride) == "" {
		if err := ensureWritebackDelegatedCredentials(resolved.WorkspaceID, joinScopes, requiredRelayfileScopes); err != nil {
			return err
		}
	}
	commandClient, err := prepareWorkspaceCommandClient(resolved.WorkspaceID, *server, *tokenOverride, joinScopes)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	firstSeenAt := now.Format(time.RFC3339Nano)
	raw := []byte(nil)
	content := ""
	encoding := ""
	contentType := ""
	contentHash := ""
	var identity *contentIdentity
	exists := mode != writebackCommandDelete
	if exists {
		raw, err = os.ReadFile(resolved.LocalPath)
		if err != nil {
			return err
		}
		content, encoding = encodeLocalWritebackContent(raw)
		contentType = detectContentType(resolved.LocalPath, raw)
		contentHash = hashBytes(raw)
		identity = writebackPushContentIdentity(resolved.WorkspaceID, resolved.RemotePath, contentHash)
	}
	commandID := newWritebackPushCommandID(resolved.WorkspaceID, resolved.RemotePath, string(mode)+":"+contentHash, firstSeenAt)
	pendingReceipt := writebackPushReceipt{
		CommandID:       commandID,
		WorkspaceID:     resolved.WorkspaceID,
		RemotePath:      resolved.RemotePath,
		Action:          string(mode),
		ContentType:     contentType,
		Content:         content,
		Encoding:        encoding,
		Hash:            contentHash,
		Exists:          exists,
		Status:          "pending",
		FirstSeenAt:     firstSeenAt,
		CorrelationID:   commandID,
		ContentIdentity: identity,
	}
	if err := writeWritebackPushReceipt(resolved.MountRoot, pendingReceipt); err != nil {
		return err
	}

	dispatch := writebackDispatchResult{}
	if mode == writebackCommandDelete {
		dispatch, err = dispatchWritebackDelete(commandClient, resolved.RemotePath)
	} else {
		file := bulkWriteFile{
			Path:            resolved.RemotePath,
			ContentType:     contentType,
			Content:         content,
			Encoding:        encoding,
			ContentIdentity: identity,
		}
		if mode == writebackCommandUpdate {
			file.WritebackIntent = "update"
		}
		dispatch, err = dispatchWritebackUpsert(commandClient, file)
	}
	if err != nil {
		failed := pendingReceipt
		failed.Status = "failed"
		failed.LastAttemptAt = time.Now().UTC().Format(time.RFC3339Nano)
		failed.AttemptCount = 1
		failed.LastError = sanitizeCLIReceiptError(err)
		failed.DispatchStatus = "failed"
		if receiptErr := writeWritebackPushReceipt(resolved.MountRoot, failed); receiptErr != nil {
			return fmt.Errorf("push failed: %w; additionally failed to write failure receipt: %v", err, receiptErr)
		}
		return err
	}
	revision := firstNonBlank(dispatch.Revision, "")
	opID := strings.TrimSpace(dispatch.OpID)
	dispatchStatus := firstNonBlank(dispatch.State, "succeeded")
	if mode == writebackCommandDelete && opID == "" {
		err := fmt.Errorf("writeback %s did not return an operation id; provider writeback was not dispatched", mode)
		failed := pendingReceipt
		failed.Status = "failed"
		failed.LastAttemptAt = time.Now().UTC().Format(time.RFC3339Nano)
		failed.AttemptCount = 1
		failed.LastError = sanitizeCLIReceiptError(err)
		failed.DispatchStatus = dispatchStatus
		failed.CorrelationID = firstNonBlank(dispatch.CorrelationID, failed.CorrelationID)
		if receiptErr := writeWritebackPushReceipt(resolved.MountRoot, failed); receiptErr != nil {
			return fmt.Errorf("%w; additionally failed to write failure receipt: %v", err, receiptErr)
		}
		return err
	}
	if mode == writebackCommandUpdate && opID == "" && !isSuccessfulWritebackDispatchStatus(dispatchStatus) {
		err := fmt.Errorf("writeback update did not return an operation id; provider writeback status %q cannot be tracked", dispatchStatus)
		receipt := pendingReceipt
		receipt.Status = "pending"
		receipt.LastAttemptAt = time.Now().UTC().Format(time.RFC3339Nano)
		receipt.AttemptCount = 1
		receipt.LastError = sanitizeCLIReceiptError(err)
		receipt.DispatchStatus = dispatchStatus
		receipt.NeedsAttention = true
		receipt.CorrelationID = firstNonBlank(dispatch.CorrelationID, receipt.CorrelationID)
		if receiptErr := writeWritebackPushReceipt(resolved.MountRoot, receipt); receiptErr != nil {
			return fmt.Errorf("%w; additionally failed to write pending receipt: %v", err, receiptErr)
		}
		return err
	}
	if opID == "" && isTerminalWritebackOpStatus(dispatchStatus) {
		err := fmt.Errorf("writeback operation %s", dispatchStatus)
		failed := pendingReceipt
		failed.Status = "failed"
		failed.LastAttemptAt = time.Now().UTC().Format(time.RFC3339Nano)
		failed.AttemptCount = 1
		failed.LastError = sanitizeCLIReceiptError(err)
		failed.DispatchStatus = dispatchStatus
		failed.CorrelationID = firstNonBlank(dispatch.CorrelationID, failed.CorrelationID)
		if receiptErr := writeWritebackPushReceipt(resolved.MountRoot, failed); receiptErr != nil {
			return fmt.Errorf("%w; additionally failed to write failure receipt: %v", err, receiptErr)
		}
		return err
	}
	if opID != "" {
		waitTimeout := *timeout
		if waitTimeout <= 0 {
			waitTimeout = 90 * time.Second
		}
		waitCtx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		op, err := waitForWritebackOperation(waitCtx, commandClient, opID)
		cancel()
		if err != nil {
			// The write already landed on the cloud (we have an opID). A
			// terminal op status (failed/dead_lettered/canceled) is a real
			// failure; a poll timeout or transient GET error is "unknown" —
			// the op may still succeed cloud-side, so keep the receipt pending
			// (flagged needsAttention) instead of diverging local state with a
			// premature failed receipt.
			receipt := pendingReceipt
			receipt.OpID = opID
			receipt.LastAttemptAt = time.Now().UTC().Format(time.RFC3339Nano)
			receipt.AttemptCount = 1
			receipt.LastError = sanitizeCLIReceiptError(err)
			receipt.CorrelationID = firstNonBlank(dispatch.CorrelationID, receipt.CorrelationID)
			if isTerminalWritebackOpStatus(op.Status) {
				receipt.Status = "failed"
				receipt.DispatchStatus = firstNonBlank(strings.TrimSpace(op.Status), "failed")
			} else {
				receipt.Status = "pending"
				receipt.DispatchStatus = firstNonBlank(strings.TrimSpace(op.Status), "dispatched")
				receipt.NeedsAttention = true
			}
			if receiptErr := writeWritebackPushReceipt(resolved.MountRoot, receipt); receiptErr != nil {
				return fmt.Errorf("%w; additionally failed to write receipt: %v", err, receiptErr)
			}
			return err
		}
		revision = firstNonBlank(revision, op.Revision)
		dispatchStatus = firstNonBlank(op.Status, dispatchStatus)
	}
	acked := pendingReceipt
	acked.Status = "acked"
	acked.LastAttemptAt = time.Now().UTC().Format(time.RFC3339Nano)
	acked.AttemptCount = 1
	acked.OpID = opID
	acked.DispatchStatus = dispatchStatus
	acked.AckedAt = time.Now().UTC().Format(time.RFC3339Nano)
	acked.Revision = revision
	acked.CorrelationID = firstNonBlank(dispatch.CorrelationID, acked.CorrelationID)
	if err := writeWritebackPushReceipt(resolved.MountRoot, acked); err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, acked.withoutBody())
	}
	switch mode {
	case writebackCommandUpdate:
		fmt.Fprintf(stdout, "Updated %s -> %s", resolved.LocalPath, resolved.RemotePath)
	case writebackCommandDelete:
		fmt.Fprintf(stdout, "Deleted %s", resolved.RemotePath)
	default:
		fmt.Fprintf(stdout, "Pushed %s -> %s", resolved.LocalPath, resolved.RemotePath)
	}
	if opID != "" {
		fmt.Fprintf(stdout, " (%s)", opID)
	}
	fmt.Fprintln(stdout)
	return nil
}

type writebackDispatchResult struct {
	OpID          string
	Revision      string
	State         string
	CorrelationID string
}

func dispatchWritebackUpsert(commandClient *workspaceCommandClient, file bulkWriteFile) (writebackDispatchResult, error) {
	var response bulkWriteResponse
	err := commandClient.postWorkspaceJSON(context.Background(), func(workspaceID string) string {
		return fmt.Sprintf("/v1/workspaces/%s/fs/bulk", url.PathEscape(workspaceID))
	}, bulkWriteRequest{Files: []bulkWriteFile{file}}, &response)
	if err != nil {
		return writebackDispatchResult{}, err
	}
	if response.ErrorCount > 0 {
		reason := firstBulkWriteErrorForPath(response.Errors, file.Path)
		if reason == "" {
			reason = fmt.Sprintf("bulk write returned %d error(s)", response.ErrorCount)
		}
		return writebackDispatchResult{CorrelationID: response.CorrelationID}, errors.New(reason)
	}
	result := firstBulkWriteResultForPath(response.Results, file.Path)
	state := "succeeded"
	if result.Writeback != nil && strings.TrimSpace(result.Writeback.State) != "" {
		state = strings.TrimSpace(result.Writeback.State)
	}
	return writebackDispatchResult{
		OpID:          strings.TrimSpace(result.OpID),
		Revision:      result.Revision,
		State:         state,
		CorrelationID: response.CorrelationID,
	}, nil
}

func dispatchWritebackDelete(commandClient *workspaceCommandClient, remotePath string) (writebackDispatchResult, error) {
	var result writeQueuedResponse
	err := commandClient.deleteWorkspaceJSON(context.Background(), func(workspaceID string) string {
		query := url.Values{}
		query.Set("path", remotePath)
		return fmt.Sprintf("/v1/workspaces/%s/fs/file?%s", url.PathEscape(workspaceID), query.Encode())
	}, "*", &result)
	if err != nil {
		return writebackDispatchResult{}, err
	}
	state := result.Writeback.State
	if strings.TrimSpace(state) == "" {
		state = result.Status
	}
	return writebackDispatchResult{
		OpID:     strings.TrimSpace(result.OpID),
		Revision: result.TargetRevision,
		State:    state,
	}, nil
}

type writebackOperationStatus struct {
	OpID     string `json:"opId,omitempty"`
	Path     string `json:"path,omitempty"`
	Status   string `json:"status,omitempty"`
	Revision string `json:"revision,omitempty"`
}

// isTerminalWritebackOpStatus reports whether a cloud op status is a confirmed
// terminal failure. Anything else (empty, in-flight, or unknown) is treated as
// not-yet-final so the push keeps the receipt pending rather than failing it.
func isTerminalWritebackOpStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "failed", "dead_lettered", "canceled":
		return true
	default:
		return false
	}
}

func isSuccessfulWritebackDispatchStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "succeeded", "completed":
		return true
	default:
		return false
	}
}

func waitForWritebackOperation(ctx context.Context, commandClient *workspaceCommandClient, opID string) (writebackOperationStatus, error) {
	if strings.TrimSpace(opID) == "" {
		return writebackOperationStatus{Status: "succeeded"}, nil
	}
	for {
		var op writebackOperationStatus
		err := commandClient.getWorkspaceJSON(ctx, func(workspaceID string) string {
			return fmt.Sprintf("/v1/workspaces/%s/ops/%s", url.PathEscape(workspaceID), url.PathEscape(opID))
		}, &op)
		if err == nil {
			switch strings.TrimSpace(op.Status) {
			case "succeeded":
				return op, nil
			case "failed", "dead_lettered", "canceled":
				return op, fmt.Errorf("writeback operation %s %s", opID, strings.TrimSpace(op.Status))
			}
		}
		// A transient poll error (network glitch, 5xx) should not abort the
		// whole push — keep retrying until the context deadline fires. The
		// select also makes the poll cancellable (e.g. Ctrl+C) instead of
		// sleeping blindly.
		select {
		case <-ctx.Done():
			if err != nil {
				return op, fmt.Errorf("timed out waiting for writeback operation %s: %w", opID, err)
			}
			return op, fmt.Errorf("timed out waiting for writeback operation %s: %w", opID, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func ensureWritebackDelegatedCredentials(workspaceID string, joinScopes, requiredRelayfileScopes []string) error {
	if bundle, _, err := loadDelegatedCredentialsForRequest("", workspaceID, joinScopes); err == nil &&
		workspaceRequestMatchesDelegatedCredentials(workspaceID, bundle.Workspace()) &&
		delegatedBundleHasScopes(bundle, requiredRelayfileScopes) {
		return nil
	}
	if _, _, err := bootstrapDelegatedCredentialsFromAgentRelay(workspaceID, joinScopes); err != nil {
		return err
	}
	// Re-validate after bootstrap: if Cloud returns a delegated bundle that
	// omits the compiled relayfile:fs:write:/<provider>/** scope, fail loudly
	// here rather than proceeding to write receipts and call /fs/bulk with an
	// under-scoped token.
	bundle, _, err := loadDelegatedCredentialsForRequest("", workspaceID, joinScopes)
	if err != nil {
		return err
	}
	if !workspaceRequestMatchesDelegatedCredentials(workspaceID, bundle.Workspace()) ||
		!delegatedBundleHasScopes(bundle, requiredRelayfileScopes) {
		return fmt.Errorf("delegated relayfile credentials for workspace %s do not include required scope(s): %s", workspaceID, strings.Join(requiredRelayfileScopes, ", "))
	}
	return nil
}

func delegatedBundleHasScopes(bundle delegatedauth.Bundle, required []string) bool {
	available := append(append([]string(nil), bundle.Scopes...), bundle.RelayfileScopes...)
	for _, want := range required {
		want = strings.TrimSpace(want)
		if want == "" {
			continue
		}
		if !scopeSetAllows(available, want) {
			return false
		}
	}
	return true
}

func resolveWritebackPushPath(localPath, workspaceValue string) (writebackPushResolvedPath, error) {
	abs, err := filepath.Abs(localPath)
	if err != nil {
		return writebackPushResolvedPath{}, err
	}
	if evaluated, err := filepath.EvalSymlinks(abs); err == nil {
		abs = evaluated
	}
	info, err := os.Stat(abs)
	if err != nil {
		return writebackPushResolvedPath{}, err
	}
	if info.IsDir() {
		return writebackPushResolvedPath{}, fmt.Errorf("%s is a directory", abs)
	}
	// Reject FIFOs, device nodes, sockets, etc. os.ReadFile on a special file
	// can hang or read an unintended stream; direct push only accepts regular
	// files.
	if !info.Mode().IsRegular() {
		return writebackPushResolvedPath{}, fmt.Errorf("%s is not a regular file", abs)
	}
	mountRoot, err := findRelayMountRoot(filepath.Dir(abs))
	if err != nil {
		return writebackPushResolvedPath{}, err
	}
	state, err := readWritebackState(mountRoot)
	if err != nil {
		return writebackPushResolvedPath{}, err
	}
	workspaceID := strings.TrimSpace(state.WorkspaceID)
	if workspaceID == "" {
		return writebackPushResolvedPath{}, fmt.Errorf("%s missing workspaceId", filepath.Join(mountRoot, ".relay", "state.json"))
	}
	if strings.TrimSpace(workspaceValue) != "" && !workspaceRequestMatchesDelegatedCredentials(workspaceValue, workspaceID) {
		return writebackPushResolvedPath{}, fmt.Errorf("local path belongs to workspace %s, not %s", workspaceID, workspaceValue)
	}
	root := mountRoot
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return writebackPushResolvedPath{}, err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
		return writebackPushResolvedPath{}, fmt.Errorf("%s is outside mount root %s", abs, root)
	}
	remotePath := joinRemotePath(readMountRemoteRoot(mountRoot), filepath.ToSlash(rel))
	return writebackPushResolvedPath{
		LocalPath:   abs,
		MountRoot:   mountRoot,
		WorkspaceID: workspaceID,
		RemoteRoot:  readMountRemoteRoot(mountRoot),
		RemotePath:  remotePath,
	}, nil
}

func findRelayMountRoot(start string) (string, error) {
	dir := filepath.Clean(start)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".relay", "state.json")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("could not find .relay/state.json above %s", start)
}

func joinRemotePath(root, rel string) string {
	// Trim a trailing slash from the mount's remoteRoot (e.g. "/linear/") so
	// the join does not produce "/linear//file.json"; the server may treat the
	// double-slash path literally and break path matching/dedupe.
	root = strings.TrimRight(normalizeWritebackFailurePath(root), "/")
	if root == "" {
		root = "/"
	}
	rel = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(rel)), "/")
	if rel == "." || rel == "" {
		return root
	}
	if root == "/" {
		return "/" + rel
	}
	return root + "/" + rel
}

func encodeLocalWritebackContent(raw []byte) (string, string) {
	if utf8.Valid(raw) {
		return string(raw), ""
	}
	return base64.StdEncoding.EncodeToString(raw), "base64"
}

func hashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func writebackPushScopes(remotePath string) ([]string, []string, error) {
	provider, ok := writebackPushProvider(remotePath)
	if !ok {
		return nil, nil, fmt.Errorf("writeback requires a provider-scoped remote path, got %s", normalizeWritebackFailurePath(remotePath))
	}
	return []string{fmt.Sprintf("fs:write:/%s/**", provider), "ops:read"},
		[]string{fmt.Sprintf("relayfile:fs:write:/%s/**", provider)},
		nil
}

func writebackPushProvider(remotePath string) (string, bool) {
	parts := strings.Split(strings.Trim(normalizeWritebackFailurePath(remotePath), "/"), "/")
	if len(parts) < 2 {
		return "", false
	}
	provider := strings.ToLower(strings.TrimSpace(parts[0]))
	if err := validateLocalProviderID(provider); err != nil {
		return "", false
	}
	return provider, true
}

func writebackPushContentIdentity(workspaceID, remotePath, contentHash string) *contentIdentity {
	if !isWritebackDraftPath(remotePath) {
		return nil
	}
	return &contentIdentity{
		Kind:       "mount-writeback-create-draft",
		Key:        fmt.Sprintf("%s:%s:%s", strings.TrimSpace(workspaceID), normalizeWritebackFailurePath(remotePath), strings.TrimSpace(contentHash)),
		TTLSeconds: 2592000,
	}
}

func isWritebackDraftPath(remotePath string) bool {
	// Remote paths are slash-separated regardless of host OS, so use
	// path.Base (not filepath.Base, which splits on "\\" on Windows).
	base := path.Base(normalizeWritebackFailurePath(remotePath))
	return strings.HasPrefix(base, "factory-create-") && strings.HasSuffix(base, ".json") ||
		relayfile.IsDraftFilePath(remotePath)
}

func isCanonicalWritebackTargetPath(remotePath string) bool {
	remotePath = normalizeWritebackFailurePath(remotePath)
	if remotePath == "" || remotePath == "/" || isWritebackDraftPath(remotePath) {
		return false
	}
	base := path.Base(remotePath)
	return strings.HasSuffix(base, ".json")
}

func newWritebackPushCommandID(workspaceID, remotePath, hash, firstSeenAt string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(workspaceID),
		normalizeWritebackFailurePath(remotePath),
		strings.TrimSpace(hash),
		strings.TrimSpace(firstSeenAt),
	}, "\x00")))
	return "mountcmd_" + hex.EncodeToString(sum[:])[:32]
}

func writeWritebackPushReceipt(mountRoot string, receipt writebackPushReceipt) error {
	status := strings.TrimSpace(receipt.Status)
	if status == "" {
		status = "pending"
	}
	receipt.Status = status
	if receipt.CorrelationID == "" {
		receipt.CorrelationID = receipt.CommandID
	}
	outboxRoot := filepath.Join(mountRoot, ".relay", "outbox")
	for _, dir := range []string{"pending", "acked", "failed"} {
		if err := os.MkdirAll(filepath.Join(outboxRoot, dir), 0o755); err != nil {
			return err
		}
	}
	targetDir := "pending"
	// Only the transient pending receipt retains the file body (so a retry can
	// resend it), and it is written 0600 to limit exposure. Terminal receipts
	// drop the body entirely.
	perm := os.FileMode(0o600)
	switch status {
	case "acked":
		targetDir = "acked"
		receipt = receipt.withoutBody()
		perm = 0o644
	case "failed":
		targetDir = "failed"
		receipt = receipt.withoutBody()
		perm = 0o644
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if err := writeFileAtomically(filepath.Join(outboxRoot, targetDir, receipt.CommandID+".json"), data, perm); err != nil {
		return err
	}
	if status != "pending" {
		if err := os.Remove(filepath.Join(outboxRoot, "pending", receipt.CommandID+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func firstBulkWriteResultForPath(results []bulkWriteResult, remotePath string) bulkWriteResult {
	remotePath = normalizeWritebackFailurePath(remotePath)
	for _, result := range results {
		if normalizeWritebackFailurePath(result.Path) == remotePath {
			return result
		}
	}
	if len(results) > 0 {
		return results[0]
	}
	return bulkWriteResult{}
}

func firstBulkWriteErrorForPath(errors []bulkWriteError, remotePath string) string {
	remotePath = normalizeWritebackFailurePath(remotePath)
	for _, item := range errors {
		if normalizeWritebackFailurePath(item.Path) == remotePath {
			return firstNonBlank(item.Message, item.Code)
		}
	}
	if len(errors) > 0 {
		return firstNonBlank(errors[0].Message, errors[0].Code)
	}
	return ""
}

func sanitizeCLIReceiptError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 2000 {
		return message[:2000]
	}
	return message
}

func runWriteback(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("writeback subcommand is required: list, push, update, delete, status, retry, or sweep-drafts")
	}
	switch args[0] {
	case "list":
		return runWritebackList(args[1:], stdout)
	case "push":
		return runWritebackPush(args[1:], stdout)
	case "update":
		return runWritebackUpdate(args[1:], stdout)
	case "delete":
		return runWritebackDelete(args[1:], stdout)
	case "status":
		return runWritebackStatus(args[1:], stdout)
	case "retry":
		return runWritebackRetry(args[1:], stdout)
	case "skip-stuck":
		return runWritebackSkipStuck(args[1:], stdout)
	case "sweep-drafts":
		return runWritebackSweepDrafts(args[1:], stdout)
	default:
		return fmt.Errorf("unknown writeback subcommand %q", args[0])
	}
}

// runWritebackSkipStuck is the operator escape hatch for a wedged events
// cursor. It walks the events cursor forward, dropping consecutive read-404
// ("not readable yet") events immediately without waiting the 5-minute
// treat-as-deleted timer, and reports how many stuck events it skipped.
func runWritebackSkipStuck(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("writeback skip-stuck", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceName := fs.String("workspace", "", "workspace name or id")
	maxSkips := fs.Int("max", 0, "maximum number of stuck events to skip (0 = unbounded)")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"workspace": true,
		"max":       true,
		"json":      false,
	})); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: relayfile writeback skip-stuck [WORKSPACE] [--workspace WS] [--max N] [--json]")
	}
	if *maxSkips < 0 {
		return errors.New("--max must be >= 0")
	}

	workspaceValue := firstNonBlank(strings.TrimSpace(*workspaceName), firstArg(fs))
	workspaceID, record, err := resolveWorkspaceLikeStatus(workspaceValue)
	if err != nil {
		return err
	}
	if strings.TrimSpace(record.LocalDir) == "" {
		return errors.New("workspace has no local mirror")
	}

	commandClient, err := prepareWorkspaceCommandClient(workspaceID, "", "", defaultJoinScopes)
	if err != nil {
		return err
	}
	tokenValue := commandClient.client.token
	server := strings.TrimSpace(commandClient.client.baseURL)
	timeout := durationEnv("RELAYFILE_MOUNT_TIMEOUT", defaultMountTimeout)
	if timeout <= 0 {
		timeout = defaultMountTimeout
	}
	client := mountsync.NewHTTPClient(server, tokenValue, &http.Client{
		Transport: newWritebackFailureTransport(record.LocalDir, log.Default(), mountsync.NewSyncTransport()),
	})
	remoteRoot := readMountRemoteRoot(record.LocalDir)
	websocketDisabled := false
	syncer, err := mountsync.NewSyncer(client, mountsync.SyncerOptions{
		WorkspaceID: workspaceID,
		RemoteRoot:  remoteRoot,
		LocalRoot:   record.LocalDir,
		WebSocket:   &websocketDisabled,
		RootCtx:     context.Background(),
		Logger:      log.Default(),
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	skipped, syncErr := syncer.SkipStuck(ctx, *maxSkips)
	backlog := syncer.BacklogDraining()

	if *jsonOutput {
		result := struct {
			Workspace string `json:"workspace"`
			Skipped   int    `json:"skipped"`
			Backlog   bool   `json:"backlogRemaining"`
			Error     string `json:"error,omitempty"`
		}{Workspace: workspaceID, Skipped: skipped, Backlog: backlog}
		if syncErr != nil {
			result.Error = syncErr.Error()
		}
		if err := writeJSON(stdout, result); err != nil {
			return err
		}
		return syncErr
	}

	fmt.Fprintf(stdout, "Skipped %d stuck event(s)\n", skipped)
	if backlog {
		fmt.Fprintln(stdout, "Backlog remains — re-run 'relayfile writeback skip-stuck' to continue clearing")
	} else {
		fmt.Fprintln(stdout, "Events cursor caught up to live head")
	}
	return syncErr
}

func runWritebackStatus(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("writeback status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"json": false,
	})); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: relayfile writeback status [WORKSPACE] [--json]")
	}

	workspaceID, record, err := resolveWorkspaceLikeStatus(firstArg(fs))
	if err != nil {
		return err
	}
	report, err := buildWritebackStatusReport(workspaceID, record.LocalDir)
	if err != nil {
		return err
	}
	if *jsonOutput {
		if err := writeJSON(stdout, report); err != nil {
			return err
		}
	} else {
		printWritebackStatus(stdout, record, report)
	}
	// Exit code reflects ACTIONABLE failures (writebacks still in the
	// dead-letter queue) — not the lifetime `failedWritebacks` counter,
	// which only ever increments. Codex/CodeRabbit flagged on PR #84:
	// once any transient 429/5xx fires, the counter would keep this
	// command exiting non-zero forever even after retries succeed.
	// `Failed` and `Pending` stay in the report for observability.
	if len(report.DeadLettered) > 0 {
		return errWritebackFailuresPresent
	}
	return nil
}

func runWritebackRetry(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("writeback retry", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opID := fs.String("opId", "", "dead-lettered operation id")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"opId": true,
	})); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: relayfile writeback retry --opId OP [WORKSPACE]")
	}
	op := strings.TrimSpace(*opID)
	if op == "" {
		return errors.New("opId is required")
	}
	if strings.ContainsAny(op, `/\`) {
		return fmt.Errorf("invalid opId %q", op)
	}

	workspaceID, record, err := resolveWorkspaceLikeStatus(firstArg(fs))
	if err != nil {
		return err
	}
	if strings.TrimSpace(record.LocalDir) == "" {
		return fmt.Errorf("unknown dead-letter op %q: workspace %s has no local mirror", op, workspaceID)
	}
	recordPath := filepath.Join(deadLetterDirFor(record.LocalDir), op+".json")
	payload, err := os.ReadFile(recordPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("unknown dead-letter op %q", op)
		}
		return err
	}
	var dl deadLetterRecord
	if err := json.Unmarshal(payload, &dl); err != nil {
		return fmt.Errorf("invalid dead-letter record %s: %w", recordPath, err)
	}
	if strings.TrimSpace(dl.OpID) == "" {
		dl.OpID = op
	}
	if dl.OpID != op {
		return fmt.Errorf("dead-letter record %s contains opId %q, expected %q", recordPath, dl.OpID, op)
	}

	if err := retryDeadLetterWriteback(workspaceID, record, dl); err != nil {
		return fmt.Errorf("retry op %s: %w", op, err)
	}
	if err := os.Remove(recordPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("retry queued but failed to remove %s: %w", recordPath, err)
	}
	sidecarPath := deadLetterErrorPathFor(record.LocalDir, op)
	if err := os.Remove(sidecarPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("retry queued but failed to remove %s: %w", sidecarPath, err)
	}
	fmt.Fprintf(stdout, "Retry queued for op %s\n", op)
	return nil
}

func runOps(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("ops subcommand is required: list or replay")
	}
	switch args[0] {
	case "list":
		return runOpsList(args[1:], stdout)
	case "replay":
		return runOpsReplay(args[1:], stdin, stdout)
	default:
		return fmt.Errorf("unknown ops subcommand %q", args[0])
	}
}

func runOpsList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("ops list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceName := fs.String("workspace", "", "workspace name or id")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	noRefresh := fs.Bool("no-refresh", false, "skip refreshing the local mirror from the server")
	server := fs.String("server", "", "relayfile server URL override")
	tokenOverride := fs.String("token", "", "relayfile token override")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"workspace":  true,
		"json":       false,
		"no-refresh": false,
		"server":     true,
		"token":      true,
	})); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: relayfile ops list [--workspace NAME] [--json] [--no-refresh]")
	}
	record, err := resolveWorkspaceRecord(strings.TrimSpace(*workspaceName))
	if err != nil {
		return err
	}
	if record.LocalDir == "" {
		if *jsonOutput {
			return writeJSON(stdout, []deadLetterRecord{})
		}
		fmt.Fprintln(stdout, "No dead-lettered ops")
		return nil
	}

	if !*noRefresh {
		// Best-effort refresh: surface the warning but keep showing the
		// local mirror so users can still see and replay known failures
		// when offline.
		if err := refreshDeadLetterMirror(record, strings.TrimSpace(*server), strings.TrimSpace(*tokenOverride)); err != nil {
			fmt.Fprintf(stdout, "warning: could not refresh from server: %v\n", err)
		}
	}

	records, err := readDeadLetterRecords(record.LocalDir)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, records)
	}
	if len(records) == 0 {
		fmt.Fprintln(stdout, "No dead-lettered ops")
		return nil
	}
	fmt.Fprintln(stdout, "op_id\tpath\tcode\tattempts\tlast_attempted_at")
	for _, r := range records {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%d\t%s\n",
			r.OpID,
			defaultIfBlank(r.Path, "-"),
			defaultIfBlank(r.Code, "-"),
			r.Attempts,
			defaultIfBlank(r.LastAttemptedAt, "-"),
		)
	}
	return nil
}

func readDeadLetterRecords(localDir string) ([]deadLetterRecord, error) {
	dir := deadLetterDirFor(localDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	records := make([]deadLetterRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".error.json") {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var record deadLetterRecord
		if err := json.Unmarshal(payload, &record); err != nil {
			return nil, fmt.Errorf("invalid dead-letter record %s: %w", entry.Name(), err)
		}
		if strings.TrimSpace(record.OpID) == "" {
			// Use filename as a fallback so the user can still replay it.
			record.OpID = strings.TrimSuffix(entry.Name(), ".json")
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].OpID < records[j].OpID
	})
	return records, nil
}

func resolveWorkspaceLikeStatus(value string) (string, workspaceRecord, error) {
	// CodeRabbit flagged on PR #84: `writeback status` should work
	// offline / with expired creds — it only inspects local mirror
	// state. Try the local workspace registry first when the user
	// supplies a name/id; only fall back to the credentials path when
	// nothing local matches (or when no value was given and we need
	// the JWT's `wks` claim to identify the default).
	//
	// When no value is given, resolve the modern selection chain (env
	// RELAYFILE_WORKSPACE, active Agent Relay workspace, catalog default)
	// against the local registry before the credentials path: in the
	// delegated Agent Relay flow credentials.json is removed, so falling
	// straight through to loadCredentials would fail even when a default
	// local mirror exists.
	trimmed := strings.TrimSpace(value)
	candidate := trimmed
	if candidate == "" {
		if name, _ := activeWorkspaceName(""); strings.TrimSpace(name) != "" {
			candidate = strings.TrimSpace(name)
		}
	}
	if candidate != "" {
		if local, ok := workspaceRecordByName(candidate); ok {
			workspaceID := firstNonBlank(strings.TrimSpace(local.RelayWorkspaceID), strings.TrimSpace(local.ID))
			if workspaceID == "" {
				workspaceID = local.Name
			}
			return workspaceID, local, nil
		}
		if local, ok := workspaceRecordByID(candidate); ok {
			return firstNonBlank(strings.TrimSpace(local.RelayWorkspaceID), strings.TrimSpace(local.ID)), local, nil
		}
	}

	creds, err := loadCredentials()
	if err != nil {
		return "", workspaceRecord{}, err
	}
	tokenValue := resolveToken("", creds)
	workspaceID, err := resolveWorkspaceIDWithToken("", tokenValue)
	if trimmed != "" {
		workspaceID, err = resolveWorkspaceIDWithToken(trimmed, tokenValue)
	}
	if err != nil {
		return "", workspaceRecord{}, err
	}
	record, _ := workspaceRecordByID(workspaceID)
	if strings.TrimSpace(record.ID) == "" {
		record.ID = workspaceID
	}
	if strings.TrimSpace(record.Name) == "" {
		record.Name = workspaceID
	}
	return workspaceID, record, nil
}

func buildWritebackStatusReport(workspaceID, localDir string) (writebackStatusReport, error) {
	report := writebackStatusReport{
		WorkspaceID:         workspaceID,
		DeadLettered:        []writebackStatusDeadLetter{},
		LastErrorByProvider: map[string]string{},
	}
	if strings.TrimSpace(localDir) == "" {
		return report, nil
	}

	state, err := readWritebackState(localDir)
	if err != nil {
		return writebackStatusReport{}, err
	}
	report.Pending = state.PendingWriteback
	report.Failed = state.FailedWritebacks
	for _, provider := range state.Providers {
		name := strings.TrimSpace(provider.Provider)
		lastError := strings.TrimSpace(provider.LastError)
		if name != "" && lastError != "" {
			report.LastErrorByProvider[name] = lastError
		}
	}

	records, err := readDeadLetterRecords(localDir)
	if err != nil {
		return writebackStatusReport{}, err
	}
	report.DeadLettered = make([]writebackStatusDeadLetter, 0, len(records))
	for _, record := range records {
		report.DeadLettered = append(report.DeadLettered, writebackStatusDeadLetter{
			OpID:       record.OpID,
			Path:       record.Path,
			LastStatus: record.LastStatus,
			TS:         firstNonBlank(record.Timestamp, record.LastAttemptedAt, record.CreatedAt),
		})
	}
	return report, nil
}

func readWritebackState(localDir string) (syncStateFile, error) {
	payload, err := os.ReadFile(filepath.Join(localDir, ".relay", "state.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return syncStateFile{}, nil
		}
		return syncStateFile{}, err
	}
	var state syncStateFile
	if err := json.Unmarshal(payload, &state); err != nil {
		return syncStateFile{}, fmt.Errorf("invalid writeback state: %w", err)
	}
	return state, nil
}

func printWritebackStatus(stdout io.Writer, record workspaceRecord, report writebackStatusReport) {
	workspaceLabel := report.WorkspaceID
	if strings.TrimSpace(record.Name) != "" && record.Name != report.WorkspaceID {
		workspaceLabel = fmt.Sprintf("%s (%s)", report.WorkspaceID, record.Name)
	}
	fmt.Fprintf(stdout, "workspace: %s\n", workspaceLabel)
	if strings.TrimSpace(record.LocalDir) != "" {
		fmt.Fprintf(stdout, "local mirror: %s\n", record.LocalDir)
	} else {
		fmt.Fprintln(stdout, "local mirror: not configured")
	}
	fmt.Fprintf(stdout, "pending: %d\n", report.Pending)
	fmt.Fprintf(stdout, "failed: %d\n", report.Failed)
	fmt.Fprintf(stdout, "dead-lettered: %d\n", len(report.DeadLettered))
	if len(report.DeadLettered) > 0 {
		fmt.Fprintln(stdout, "\nDead-lettered ops:")
		fmt.Fprintln(stdout, "op_id\tpath\tlast_status\tts")
		for _, item := range report.DeadLettered {
			lastStatus := "-"
			if item.LastStatus != 0 {
				lastStatus = strconv.Itoa(item.LastStatus)
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n",
				item.OpID,
				defaultIfBlank(item.Path, "-"),
				lastStatus,
				defaultIfBlank(item.TS, "-"),
			)
		}
	}
	if len(report.LastErrorByProvider) > 0 {
		providers := make([]string, 0, len(report.LastErrorByProvider))
		for provider := range report.LastErrorByProvider {
			providers = append(providers, provider)
		}
		sort.Strings(providers)
		fmt.Fprintln(stdout, "\nLast errors by provider:")
		for _, provider := range providers {
			fmt.Fprintf(stdout, "  %s: %s\n", provider, report.LastErrorByProvider[provider])
		}
	}
	if report.Failed == 0 && len(report.DeadLettered) == 0 {
		fmt.Fprintln(stdout, "\nno failures")
	}
}

func retryDeadLetterWriteback(workspaceID string, record workspaceRecord, dl deadLetterRecord) error {
	if strings.TrimSpace(record.LocalDir) == "" {
		return errors.New("workspace has no local mirror")
	}
	paths := deadLetterRetryPaths(dl.Path)
	if len(paths) == 0 {
		return fmt.Errorf("dead-letter record %s has no retryable path", dl.OpID)
	}

	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	tokenValue := resolveToken("", creds)
	if tokenValue == "" {
		return errors.New("token is required; set RELAYFILE_TOKEN or pass explicit relayfile credentials")
	}
	server := strings.TrimSpace(record.Server)
	if server == "" {
		server = resolveServer("", creds)
	}
	retryTimeout := durationEnv("RELAYFILE_MOUNT_TIMEOUT", defaultMountTimeout)
	if retryTimeout <= 0 {
		retryTimeout = defaultMountTimeout
	}
	// No whole-request Timeout: net/http enforces http.Client.Timeout
	// independent of context and would kill a long-but-progressing
	// bootstrap body read. Cancellation is owned by the per-cycle /
	// bootstrap / cursor contexts; the sync transport bounds
	// connect/handshake/time-to-first-byte instead.
	client := mountsync.NewHTTPClient(server, tokenValue, &http.Client{
		Transport: newWritebackFailureTransport(record.LocalDir, log.Default(), mountsync.NewSyncTransport()),
	})
	// Read the live mount's remoteRoot from .relay/state.json instead
	// of hardcoding "/". CodeRabbit flagged on PR #84: a mount created
	// with `--remote-path /github` has dead-letter paths under /github,
	// and retrying with RemoteRoot:"/" would look up `<localDir>/github/...`
	// instead of `<localDir>/...`, so replay would fail even though the
	// mirrored file exists.
	remoteRoot := readMountRemoteRoot(record.LocalDir)
	websocketDisabled := false
	syncer, err := mountsync.NewSyncer(client, mountsync.SyncerOptions{
		WorkspaceID: workspaceID,
		RemoteRoot:  remoteRoot,
		LocalRoot:   record.LocalDir,
		WebSocket:   &websocketDisabled,
		RootCtx:     context.Background(),
		Logger:      log.Default(),
	})
	if err != nil {
		return err
	}

	for _, remotePath := range paths {
		relativePath, err := retryRelativePath(record.LocalDir, remoteRoot, remotePath)
		if err != nil {
			return err
		}
		retryCtx, cancel := context.WithTimeout(context.Background(), retryTimeout)
		err = syncer.HandleLocalChange(retryCtx, relativePath, fsnotify.Write)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

// readMountRemoteRoot reads the live mount's remoteRoot from
// <localDir>/.relay/state.json. Defaults to "/" when missing or
// unparseable so retry on a root mount works without state.json
// being present.
func readMountRemoteRoot(localDir string) string {
	if strings.TrimSpace(localDir) == "" {
		return "/"
	}
	data, err := os.ReadFile(filepath.Join(localDir, ".relay", "state.json"))
	if err != nil {
		return "/"
	}
	var s struct {
		RemoteRoot string `json:"remoteRoot"`
	}
	if json.Unmarshal(data, &s) != nil {
		return "/"
	}
	if root := strings.TrimSpace(s.RemoteRoot); root != "" {
		return root
	}
	return "/"
}

func deadLetterRetryPaths(raw string) []string {
	parts := strings.Split(raw, ",")
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		path := normalizeWritebackFailurePath(part)
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func retryRelativePath(localDir, remoteRoot, remotePath string) (string, error) {
	remotePath = normalizeWritebackFailurePath(remotePath)
	if remotePath == "" || remotePath == "/" {
		return "", fmt.Errorf("invalid retry path %q", remotePath)
	}
	// Strip the mount's remoteRoot from the dead-letter path so the
	// remaining suffix can be joined to the local mirror's root. For
	// a mount with RemoteRoot=/github and dead-letter path
	// /github/file.md, this yields a relative path of `file.md` which
	// correctly resolves to <localDir>/file.md.
	root := normalizeWritebackFailurePath(remoteRoot)
	relPath := remotePath
	if root != "" && root != "/" {
		if relPath != root && !strings.HasPrefix(relPath, root+"/") {
			return "", fmt.Errorf("retry path %q is not under mount root %q", remotePath, root)
		}
		relPath = strings.TrimPrefix(relPath, root)
	}
	relPath = strings.TrimPrefix(relPath, "/")
	cleanRelative := filepath.Clean(filepath.FromSlash(relPath))
	if cleanRelative == "." || strings.HasPrefix(cleanRelative, ".."+string(os.PathSeparator)) || cleanRelative == ".." {
		return "", fmt.Errorf("invalid retry path %q", remotePath)
	}
	localPath := filepath.Join(localDir, cleanRelative)
	info, err := os.Stat(localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("local file for retry path %s does not exist", remotePath)
		}
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("local retry path %s is a directory", remotePath)
	}
	return filepath.ToSlash(cleanRelative), nil
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type opsListResponse struct {
	Items []struct {
		OpID          string  `json:"opId"`
		Path          string  `json:"path,omitempty"`
		Action        string  `json:"action,omitempty"`
		Provider      string  `json:"provider,omitempty"`
		Status        string  `json:"status"`
		AttemptCount  int     `json:"attemptCount"`
		LastError     *string `json:"lastError,omitempty"`
		CreatedAt     string  `json:"createdAt,omitempty"`
		UpdatedAt     string  `json:"updatedAt,omitempty"`
		CorrelationID string  `json:"correlationId,omitempty"`
	} `json:"items"`
	NextCursor *string `json:"nextCursor,omitempty"`
}

// refreshDeadLetterMirror reconciles the local .relay/dead-letter/ directory
// against the server's view per contract §8.4. New dead-lettered ops are
// written; ops the server no longer reports as dead-lettered are pruned so
// the local mirror does not show stale entries after replay or ack.
func refreshDeadLetterMirror(record workspaceRecord, serverOverride, tokenOverride string) error {
	if record.LocalDir == "" {
		return nil
	}
	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	tokenValue := resolveToken(tokenOverride, creds)
	client, err := newAPIClient(resolveServer(serverOverride, creds), tokenValue)
	if err != nil {
		return err
	}

	var feed opsListResponse
	if err := client.getJSON(
		context.Background(),
		fmt.Sprintf("/v1/workspaces/%s/ops?status=dead_lettered&limit=200", url.PathEscape(record.ID)),
		&feed,
	); err != nil {
		return err
	}

	dir := deadLetterDirFor(record.LocalDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	keep := make(map[string]struct{}, len(feed.Items))
	baseURL := strings.TrimRight(client.baseURL, "/")
	for _, item := range feed.Items {
		opID := safeWritebackOpID(item.OpID)
		if opID == "" {
			if trimmed := strings.TrimSpace(item.OpID); trimmed != "" {
				fmt.Fprintf(os.Stderr, "warning: skipping dead-letter op with unsafe id %q\n", trimmed)
			}
			continue
		}
		keep[opID] = struct{}{}
		message := ""
		if item.LastError != nil {
			message = strings.TrimSpace(*item.LastError)
		}
		code := classifyDeadLetterCode(message)
		dl := deadLetterRecord{
			OpID:            opID,
			Path:            item.Path,
			Code:            code,
			Message:         message,
			CreatedAt:       item.CreatedAt,
			LastAttemptedAt: item.UpdatedAt,
			Attempts:        item.AttemptCount,
			ReplayURL: fmt.Sprintf(
				"%s/v1/workspaces/%s/ops/%s/replay",
				baseURL,
				url.PathEscape(record.ID),
				url.PathEscape(opID),
			),
		}
		payload, err := json.MarshalIndent(dl, "", "  ")
		if err != nil {
			return err
		}
		if err := writeFileAtomically(filepath.Join(dir, opID+".json"), payload, 0o644); err != nil {
			return err
		}
	}

	// Prune local payload records the server no longer reports as
	// dead-lettered. Diagnostic sidecars (<opID>.error.json) are bound to
	// their payload's lifecycle: they are skipped here and removed together
	// with the payload, never evaluated as standalone payload records.
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".error.json") {
			continue
		}
		opID := strings.TrimSuffix(name, ".json")
		if _, ok := keep[opID]; ok {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
		_ = os.Remove(deadLetterErrorPathFor(record.LocalDir, opID))
	}
	return nil
}

func classifyDeadLetterCode(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "schema") || strings.Contains(lower, "validation"):
		return "validation_error"
	case strings.Contains(lower, "permission") || strings.Contains(lower, "forbidden"):
		return "forbidden"
	case strings.Contains(lower, "not found"):
		return "not_found"
	case message == "":
		return "non_retryable"
	default:
		return "non_retryable"
	}
}

func runOpsReplay(args []string, stdin io.Reader, stdout io.Writer) error {
	_ = stdin
	fs := flag.NewFlagSet("ops replay", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceName := fs.String("workspace", "", "workspace name or id")
	cloudAPIURL := fs.String("cloud-api-url", envOrDefault("RELAYFILE_CLOUD_API_URL", defaultCloudAPIURL), "Relayfile Cloud API URL")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"workspace":     true,
		"cloud-api-url": true,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile ops replay OPID [--workspace NAME]")
	}
	opID := strings.TrimSpace(fs.Arg(0))
	if opID == "" {
		return errors.New("opId is required")
	}
	record, err := resolveWorkspaceRecord(strings.TrimSpace(*workspaceName))
	if err != nil {
		return err
	}
	cloudCreds, err := ensureCloudCredentials(strings.TrimSpace(*cloudAPIURL), "", 5*time.Minute, false, io.Discard)
	if err != nil {
		return err
	}
	client, err := newAPIClient(cloudCreds.APIURL, cloudCreds.AccessToken)
	if err != nil {
		return err
	}
	if err := client.postJSON(
		context.Background(),
		fmt.Sprintf("/api/v1/workspaces/%s/ops/%s/replay", url.PathEscape(record.ID), url.PathEscape(opID)),
		struct{}{},
		nil,
	); err != nil {
		return err
	}
	// Per contract §8.4, on successful replay the local mirror record is
	// removed so the user's view stays in sync with the queue.
	if record.LocalDir != "" {
		path := filepath.Join(deadLetterDirFor(record.LocalDir), opID+".json")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(stdout, "warning: replay queued but failed to remove %s: %v\n", path, err)
		}
		sidecar := deadLetterErrorPathFor(record.LocalDir, opID)
		if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(stdout, "warning: replay queued but failed to remove %s: %v\n", sidecar, err)
		}
	}
	fmt.Fprintf(stdout, "Replay queued for op %s\n", opID)
	return nil
}

func runPull(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceName := fs.String("workspace", "", "workspace name or id")
	provider := fs.String("provider", "", "provider id (default: refresh all connected providers)")
	reason := fs.String("reason", "manual", "free-form reason recorded server-side")
	server := fs.String("server", "", "relayfile server URL override")
	tokenOverride := fs.String("token", "", "relayfile token override")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"workspace": true,
		"provider":  true,
		"reason":    true,
		"server":    true,
		"token":     true,
	})); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: relayfile pull [--workspace NAME] [--provider PROVIDER] [--reason TEXT]")
	}

	commandClient, err := prepareWorkspaceCommandClient(strings.TrimSpace(*workspaceName), *server, *tokenOverride, defaultJoinScopes)
	if err != nil {
		return err
	}

	providers, err := resolvePullProviders(commandClient, strings.TrimSpace(*provider))
	if err != nil {
		return err
	}
	if len(providers) == 0 {
		fmt.Fprintln(stdout, "No connected providers to refresh")
		return nil
	}

	for _, p := range providers {
		var queued struct {
			Status string `json:"status"`
			ID     string `json:"id"`
		}
		body := struct {
			Provider string `json:"provider"`
			Reason   string `json:"reason,omitempty"`
		}{Provider: p, Reason: strings.TrimSpace(*reason)}
		if err := commandClient.postWorkspaceJSON(
			context.Background(),
			func(workspaceID string) string {
				return fmt.Sprintf("/v1/workspaces/%s/sync/refresh", url.PathEscape(workspaceID))
			},
			body,
			&queued,
		); err != nil {
			return fmt.Errorf("refresh %s: %w", p, err)
		}
		fmt.Fprintf(stdout, "%s refresh queued (%s)\n", p, defaultIfBlank(queued.ID, "queued"))
	}
	return nil
}

func resolvePullProviders(commandClient *workspaceCommandClient, requested string) ([]string, error) {
	if requested != "" {
		return []string{normalizeProviderID(requested)}, nil
	}
	var status syncStatusResponse
	err := commandClient.getWorkspaceJSON(context.Background(), func(workspaceID string) string {
		return fmt.Sprintf("/v1/workspaces/%s/sync/status", url.PathEscape(workspaceID))
	}, &status)
	if err != nil {
		return nil, err
	}
	providers := make([]string, 0, len(status.Providers))
	for _, entry := range status.Providers {
		if strings.TrimSpace(entry.Provider) == "" {
			continue
		}
		providers = append(providers, entry.Provider)
	}
	return providers, nil
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

func runWorkspaceJoin(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("workspace join", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cloudAPIURL := fs.String("cloud-api-url", envOrDefault("RELAYFILE_CLOUD_API_URL", defaultCloudAPIURL), "Relayfile Cloud API URL")
	cloudToken := fs.String("cloud-token", strings.TrimSpace(os.Getenv("RELAYFILE_CLOUD_TOKEN")), "Relayfile Cloud access token; skips browser login when set")
	name := fs.String("name", "", "local workspace name")
	writeAccess := fs.Bool("write", false, "request read/write workspace token scopes")
	noOpen := fs.Bool("no-open", false, "print browser URLs instead of opening them")
	loginTimeout := fs.Duration("login-timeout", 5*time.Minute, "cloud login timeout")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"cloud-api-url": true,
		"cloud-token":   true,
		"name":          true,
		"write":         false,
		"no-open":       false,
		"login-timeout": true,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile workspace join WORKSPACE_ID [--name NAME] [--write]")
	}

	workspaceID := strings.TrimSpace(fs.Arg(0))
	if workspaceID == "" {
		return errors.New("workspace id is required")
	}
	localName := strings.TrimSpace(*name)
	if localName == "" {
		if record, ok := workspaceRecordByID(workspaceID); ok && strings.TrimSpace(record.Name) != "" {
			localName = record.Name
		} else {
			localName = workspaceID
		}
	}
	scopes := append([]string(nil), defaultInspectScopes...)
	if *writeAccess {
		scopes = append([]string(nil), defaultJoinScopes...)
	}
	record := workspaceRecord{
		Name:      localName,
		ID:        workspaceID,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		AgentName: "relayfile-cli",
		Scopes:    scopes,
	}
	cloudCreds, err := ensureCloudCredentials(strings.TrimSpace(*cloudAPIURL), strings.TrimSpace(*cloudToken), *loginTimeout, !*noOpen, stdout)
	if err != nil {
		return err
	}
	delegated, err := delegatedRelayfileTokenViaCloud(cloudCreds, workspaceID, record.AgentName, record.Scopes)
	if err != nil {
		return err
	}
	if err := delegatedauth.SaveAtomic(delegatedCredentialsPathForRequest(record.ID, record.Scopes), delegated); err != nil {
		return fmt.Errorf("persist delegated relayfile credentials: %w", err)
	}
	record, err = persistDelegatedWorkspace(record, delegated, "", true)
	if err != nil {
		return err
	}
	mode := "read-only"
	if *writeAccess {
		mode = "read/write"
	}
	fmt.Fprintf(stdout, "Joined workspace %s (id: %s, %s)\n", localName, record.ID, mode)
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

	if err := ensureAgentRelayCLICompatible(); err != nil {
		return err
	}
	cmd := exec.Command(agentRelayBinary(), "workspace", "switch", fs.Arg(0))
	cmd.Stdout = stdout
	cmd.Stderr = stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("agent-relay workspace switch failed: %w", err)
	}
	fmt.Fprintln(stdout, "Relayfile uses the active agent-relay workspace.")
	return nil
}

func runWorkspaceList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("workspace list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "relayfile server URL override")
	token := fs.String("token", "", "relayfile token override")
	namesOnly := fs.Bool("names-only", false, "print bare workspace names without an active marker")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server":     true,
		"token":      true,
		"names-only": false,
	})); err != nil {
		return err
	}

	creds, _ := loadCredentials()
	tokenValue := resolveToken(*token, creds)
	activeName, _ := activeWorkspaceName(tokenValue)
	emit := func(name string) {
		if *namesOnly || activeName == "" {
			fmt.Fprintln(stdout, name)
			return
		}
		marker := "  "
		if name == activeName {
			marker = "* "
		}
		fmt.Fprintln(stdout, marker+name)
	}

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
					emit(name)
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
		emit(name)
	}
	return nil
}

// activeWorkspaceName returns the name (preferred) or id of the workspace
// that resolveWorkspaceRecord would pick when no explicit value is passed,
// alongside a short human-readable source ("env RELAYFILE_WORKSPACE",
// "token", "default") for diagnostics. Returns "" if no active workspace
// can be resolved.
func activeWorkspaceName(token string) (string, string) {
	if envValue := strings.TrimSpace(os.Getenv("RELAYFILE_WORKSPACE")); envValue != "" {
		if record, ok := workspaceRecordByName(envValue); ok {
			return record.Name, "env RELAYFILE_WORKSPACE"
		}
		if record, ok := workspaceRecordByID(envValue); ok {
			return record.Name, "env RELAYFILE_WORKSPACE"
		}
		return envValue, "env RELAYFILE_WORKSPACE"
	}
	if tokenWS := workspaceIDFromToken(token); tokenWS != "" {
		if record, ok := workspaceRecordByID(tokenWS); ok {
			return record.Name, "token"
		}
		return tokenWS, "token"
	}
	if workspace, err := activeWorkspaceFromAgentRelay(); err == nil {
		return workspace.Name, "agent-relay"
	}
	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return "", ""
	}
	if name := strings.TrimSpace(catalog.Default); name != "" {
		return name, "default"
	}
	return "", ""
}

func runWorkspaceCurrent(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("workspace current", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	token := fs.String("token", "", "relayfile token override")
	verbose := fs.Bool("verbose", false, "include workspace id and selection source")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"token":   true,
		"verbose": false,
	})); err != nil {
		return err
	}

	creds, _ := loadCredentials()
	tokenValue := resolveToken(*token, creds)
	name, source := activeWorkspaceName(tokenValue)
	if name == "" {
		return errors.New("no active workspace; pass WORKSPACE, set RELAYFILE_WORKSPACE, or run 'agent-relay workspace switch NAME'")
	}
	if !*verbose {
		fmt.Fprintln(stdout, name)
		return nil
	}
	id := name
	if record, ok := workspaceRecordByName(name); ok && strings.TrimSpace(record.ID) != "" {
		id = record.ID
	}
	if id != "" && id != name {
		fmt.Fprintf(stdout, "%s (id: %s, source: %s)\n", name, id, source)
	} else {
		fmt.Fprintf(stdout, "%s (source: %s)\n", name, source)
	}
	return nil
}

func runWorkspaceStatus(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("workspace status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceName := fs.String("workspace", "", "workspace name or id")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"workspace": true,
		"json":      false,
	})); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: relayfile workspace status [--workspace NAME] [--json]")
	}
	value := strings.TrimSpace(*workspaceName)
	if value == "" && fs.NArg() == 1 {
		value = strings.TrimSpace(fs.Arg(0))
	}
	workspaceID, record, err := resolveWorkspaceLikeStatus(value)
	if err != nil {
		return err
	}
	report := buildWorkspaceHealthReport(workspaceID, record)
	if *jsonOutput {
		return writeJSON(stdout, report)
	}
	printWorkspaceHealthReport(stdout, report)
	return nil
}

func buildWorkspaceHealthReport(workspaceID string, record workspaceRecord) workspaceHealthReport {
	report := workspaceHealthReport{
		WorkspaceID: strings.TrimSpace(workspaceID),
		Name:        strings.TrimSpace(record.Name),
		LocalDir:    strings.TrimSpace(record.LocalDir),
	}
	if report.LocalDir == "" {
		return report
	}
	state := readWritebackStateBestEffort(report.LocalDir)
	report.Status = strings.TrimSpace(state.Status)
	report.LastSuccessfulReconcileAt = strings.TrimSpace(state.LastSuccessfulReconcileAt)
	report.LastReconcileAt = strings.TrimSpace(state.LastReconcileAt)
	if state.LastError != nil {
		report.LastError = strings.TrimSpace(firstNonBlank(state.LastError.Message, state.LastError.Code))
	}
	// Use the public sync state's not-ready set as the stuck-event baseline so
	// the count is non-zero even when the private cursor files are absent or
	// the first readable one lacks the field; then take the max with the
	// private cursor health (which also carries the backlog-draining flag).
	report.StuckEventCount = len(state.IncrementalReadNotReadySince)
	cursorStuckCount, backlogDraining := readLocalMountCursorHealth(report.LocalDir)
	if cursorStuckCount > report.StuckEventCount {
		report.StuckEventCount = cursorStuckCount
	}
	report.IncrementalBacklogDraining = backlogDraining
	report.OutboxPending = countJSONFiles(filepath.Join(report.LocalDir, ".relay", "outbox", "pending"))
	report.OutboxFailed = countJSONFiles(filepath.Join(report.LocalDir, ".relay", "outbox", "failed"))
	report.OutboxAcked = countJSONFiles(filepath.Join(report.LocalDir, ".relay", "outbox", "acked"))
	return report
}

func readWritebackStateBestEffort(localDir string) syncStateFile {
	state, err := readWritebackState(localDir)
	if err == nil {
		return state
	}
	return syncStateFile{}
}

func readLocalMountCursorHealth(localDir string) (stuckCount int, backlogDraining bool) {
	for _, path := range []string{
		filepath.Join(localDir, ".relayfile-mount-state.json"),
		filepath.Join(localDir, mountsync.DefaultMountStateDirName, "state.json"),
	} {
		payload, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var state struct {
			IncrementalReadNotReadySince map[string]string `json:"incrementalReadNotReadySince"`
			IncrementalBacklogDraining   bool              `json:"incrementalBacklogDraining"`
		}
		if json.Unmarshal(payload, &state) != nil {
			continue
		}
		if count := len(state.IncrementalReadNotReadySince); count > stuckCount {
			stuckCount = count
		}
		backlogDraining = backlogDraining || state.IncrementalBacklogDraining
	}
	return stuckCount, backlogDraining
}

func countJSONFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	return count
}

func printWorkspaceHealthReport(stdout io.Writer, report workspaceHealthReport) {
	label := report.WorkspaceID
	if report.Name != "" && report.Name != report.WorkspaceID {
		label = fmt.Sprintf("%s (%s)", report.WorkspaceID, report.Name)
	}
	fmt.Fprintf(stdout, "workspace: %s\n", label)
	if report.LocalDir == "" {
		fmt.Fprintln(stdout, "local mirror: not configured")
		return
	}
	fmt.Fprintf(stdout, "local mirror: %s\n", report.LocalDir)
	fmt.Fprintf(stdout, "status: %s\n", defaultIfBlank(report.Status, "-"))
	fmt.Fprintf(stdout, "last successful reconcile: %s\n", defaultIfBlank(report.LastSuccessfulReconcileAt, "-"))
	fmt.Fprintf(stdout, "last reconcile: %s\n", defaultIfBlank(report.LastReconcileAt, "-"))
	fmt.Fprintf(stdout, "stuck events: %d\n", report.StuckEventCount)
	fmt.Fprintf(stdout, "outbox: pending=%d failed=%d acked=%d\n", report.OutboxPending, report.OutboxFailed, report.OutboxAcked)
	if report.LastError != "" {
		fmt.Fprintf(stdout, "last error: %s\n", report.LastError)
	}
	if report.IncrementalBacklogDraining {
		fmt.Fprintln(stdout, "incremental backlog: draining")
	}
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

	server := fs.String("server", resolveServer("", credentials{}), "relayfile server URL")
	token := fs.String("token", strings.TrimSpace(os.Getenv("RELAYFILE_TOKEN")), "bearer token")
	credsFile := fs.String("creds-file", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_CREDS_FILE")), "delegated relayfile credentials file")
	remotePath := fs.String("remote-path", envOrDefault("RELAYFILE_REMOTE_PATH", "/"), "remote root path")
	eventProvider := fs.String("provider", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_PROVIDER")), "event provider filter")
	stateFile := fs.String("state-file", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_STATE_FILE")), "state file path")
	stateDir := fs.String("state-dir", envOrDefault("RELAYFILE_MOUNT_STATE_DIR", mountsync.DefaultMountStateDir()), "directory for private mount state")
	mountKind := fs.String("mount-kind", envOrDefault("RELAYFILE_MOUNT_KIND", mountsync.MountKindDaemon), "private state identity kind: daemon, flush, or initial-sync")
	localDirFlag := fs.String("local-dir", "", "local mirror directory")
	mode := fs.String("mode", envOrDefault("RELAYFILE_MOUNT_MODE", defaultMountMode), "mount mode: poll (recommended) or fuse")
	interval := fs.Duration("interval", durationEnv("RELAYFILE_MOUNT_INTERVAL", defaultMountInterval), "sync interval")
	intervalJitter := fs.Float64("interval-jitter", floatEnv("RELAYFILE_MOUNT_INTERVAL_JITTER", 0.2), "sync interval jitter ratio (0.0-1.0)")
	timeout := fs.Duration("timeout", durationEnv("RELAYFILE_MOUNT_TIMEOUT", defaultMountTimeout), "per-sync timeout")
	bootstrapTimeout := fs.Duration("bootstrap-timeout", durationEnv("RELAYFILE_BOOTSTRAP_TIMEOUT", 0), "hard cap for the one-time/full-tree bootstrap pull (0 = unbounded while making progress)")
	cursorTimeout := fs.Duration("cursor-timeout", durationEnv("RELAYFILE_CURSOR_TIMEOUT", 60*time.Second), "independent timeout for events-cursor resolution")
	fullReconcile := fs.Bool("full-reconcile", boolEnv("RELAYFILE_FORCE_FULL_RECONCILE", false), "force one full reconcile regardless of bootstrap-complete state (escape hatch)")
	websocketEnabled := fs.Bool("websocket", boolEnv("RELAYFILE_MOUNT_WEBSOCKET", true), "enable websocket event streaming when available")
	lowMemory := fs.Bool("low-memory", boolEnv("RELAYFILE_MOUNT_LOW_MEMORY", false), "reduce mount memory use by omitting per-file public state and deferring content reads")
	pprofAddr := fs.String("pprof-addr", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_PPROF_ADDR")), "optional pprof listen address, e.g. 127.0.0.1:6060")
	memlogInterval := fs.Duration("memlog-interval", durationEnv("RELAYFILE_MOUNT_MEMLOG_INTERVAL", 0), "optional interval for logging runtime memory stats")
	background := fs.Bool("background", false, "detach and keep syncing in the background")
	pidFileFlag := fs.String("pid-file", "", "pid file path for background mode")
	logFileFlag := fs.String("log-file", "", "log file path for background mode")
	daemonized := fs.Bool("daemonized", false, "internal flag used by relayfile mount --background")
	once := fs.Bool("once", false, "run one sync cycle and exit")
	resetAfterClobber := fs.Bool("reset-after-clobber", boolEnv("RELAYFILE_RESET_AFTER_CLOBBER", false), "acknowledge a mount-root clobber and authorize daemon to recreate the directory")
	rehome := fs.Bool("rehome", false, "allow re-homing an already-registered workspace mirror to a different LOCAL_DIR")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server":              true,
		"token":               true,
		"creds-file":          true,
		"remote-path":         true,
		"provider":            true,
		"state-file":          true,
		"state-dir":           true,
		"mount-kind":          true,
		"mode":                true,
		"interval":            true,
		"interval-jitter":     true,
		"timeout":             true,
		"bootstrap-timeout":   true,
		"cursor-timeout":      true,
		"full-reconcile":      false,
		"websocket":           false,
		"low-memory":          false,
		"pprof-addr":          true,
		"memlog-interval":     true,
		"background":          false,
		"pid-file":            true,
		"log-file":            true,
		"daemonized":          false,
		"once":                false,
		"reset-after-clobber": false,
		"rehome":              false,
		"local-dir":           true,
	})); err != nil {
		// `--help` / `-h` come back from flag.ContinueOnError as
		// flag.ErrHelp. Per contract A13 §3.6, surface the synced-mirror
		// limitations alongside the flag list so users know what the
		// default mode does and does not provide.
		if errors.Is(err, flag.ErrHelp) {
			printMountHelp(os.Stdout)
			return nil
		}
		return err
	}
	if fs.NArg() > 2 {
		return errors.New("usage: relayfile mount [WORKSPACE] [LOCAL_DIR]")
	}

	workspaceID := ""
	localDir := ""
	localDirExplicit := false
	tokenValue := strings.TrimSpace(*token)
	canonicalWorkspaceID := ""
	requestedWorkspace := ""
	delegatedCredsPath := resolveDelegatedCredentialsPath(*credsFile)
	usesDelegatedWorkspace := false
	initialCredExpiresAt := ""
	if fs.NArg() > 0 {
		requestedWorkspace = strings.TrimSpace(fs.Arg(0))
	}
	if tokenValue == "" {
		bundle, path, berr := loadDelegatedCredentialsForRequest(*credsFile, requestedWorkspace, defaultJoinScopes)
		if berr != nil {
			return fmt.Errorf("resolve delegated relayfile credentials: %w", berr)
		}
		delegatedCredsPath = path
		bundle, berr = refreshDelegatedCredentials(path, bundle, false)
		if berr != nil {
			return fmt.Errorf("refresh delegated relayfile credentials: %w", berr)
		}
		canonicalWorkspaceID = bundle.Workspace()
		if requestedWorkspace != "" && !workspaceRequestMatchesDelegatedCredentials(requestedWorkspace, canonicalWorkspaceID) {
			return fmt.Errorf(
				"relayfile mount without --token uses delegated relayfile workspace %s; pass --token for explicit workspace %q or re-bootstrap delegated credentials for that workspace",
				canonicalWorkspaceID,
				requestedWorkspace,
			)
		}
		tokenValue = bundle.BearerToken()
		initialCredExpiresAt = bundle.BearerExpiresAt()
		*server = strings.TrimRight(bundle.ServerURL(), "/")
		usesDelegatedWorkspace = true
	}
	var err error
	switch fs.NArg() {
	case 0:
		if canonicalWorkspaceID != "" {
			workspaceID = canonicalWorkspaceID
		} else {
			workspaceID, err = resolveWorkspaceIDWithToken("", tokenValue)
		}
		localDir = strings.TrimSpace(*localDirFlag)
		localDirExplicit = localDir != ""
	case 1:
		if canonicalWorkspaceID != "" {
			workspaceID = canonicalWorkspaceID
		} else {
			workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
		}
		localDir = strings.TrimSpace(*localDirFlag)
		localDirExplicit = localDir != ""
	case 2:
		if strings.TrimSpace(*localDirFlag) != "" {
			return errors.New("local directory specified twice")
		}
		if canonicalWorkspaceID != "" {
			workspaceID = canonicalWorkspaceID
		} else {
			workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
		}
		localDir = fs.Arg(1)
		localDirExplicit = true
	}
	if err != nil {
		return err
	}
	recordedLocalDir := ""
	if record, ok := workspaceRecordByID(workspaceID); ok {
		recordedLocalDir = strings.TrimSpace(record.LocalDir)
	} else if record, ok := workspaceRecordByName(workspaceID); ok && strings.TrimSpace(record.ID) == "" {
		recordedLocalDir = strings.TrimSpace(record.LocalDir)
	}
	if recordedLocalDir == "" && usesDelegatedWorkspace && requestedWorkspace != "" && workspaceRequestMatchesDelegatedCredentials(requestedWorkspace, canonicalWorkspaceID) {
		if record, ok := workspaceRecordByName(requestedWorkspace); ok {
			recordedLocalDir = strings.TrimSpace(record.LocalDir)
		}
	}
	if localDir == "" {
		localDir = recordedLocalDir
	}
	if localDir == "" {
		return fmt.Errorf(
			"workspace %s has no recorded local mirror directory; pass LOCAL_DIR or --local-dir to choose one",
			workspaceID,
		)
	}
	absLocalDir, err := filepath.Abs(localDir)
	if err != nil {
		return err
	}
	if localDirExplicit && recordedLocalDir != "" {
		absRecorded, aerr := filepath.Abs(recordedLocalDir)
		if aerr != nil {
			absRecorded = recordedLocalDir
		}
		if absRecorded != absLocalDir && !*rehome {
			return fmt.Errorf(
				"workspace %s is already mirrored at %s; refusing to silently re-home it to %s.\n"+
					"Pass --rehome to move the mirror there, or omit LOCAL_DIR to use the registered directory",
				workspaceID, absRecorded, absLocalDir,
			)
		}
		if absRecorded != absLocalDir && *rehome {
			pid, verified := verifyDaemonProcess(recordedLocalDir, workspaceID)
			if pid != 0 && verified {
				return fmt.Errorf(
					"workspace %s has a running mount at %s (pid %d); stop it before re-homing to %s",
					workspaceID, absRecorded, pid, absLocalDir,
				)
			}
			if pid != 0 && !verified {
				return fmt.Errorf(
					"workspace %s has unverified mount state at %s (pid %d); "+
						"stop the existing mount or remove %s after confirming it is stale before re-homing to %s",
					workspaceID, absRecorded, pid, mountPIDFile(recordedLocalDir), absLocalDir,
				)
			}
		}
	}
	// Recovery-mode precheck: refuse to (re)create a mount root if a prior
	// run's directory was clobbered (replaced by a file, or missing in a
	// surprising way) unless the operator explicitly acknowledges. The
	// acknowledgment flag also tolerates the "missing root" case so that
	// fresh installs continue to work.
	if err := preflightMountRootInvariant(absLocalDir, *resetAfterClobber); err != nil {
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
	*interval = enforcePollIntervalFloor(*interval)
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

	if shouldRefuseCompetingMount(*daemonized, *once) {
		running, stalePID, derr := runningMountDaemons(absLocalDir, workspaceID, workspaceNameForStart(workspaceID))
		if derr != nil {
			return fmt.Errorf("check for existing mount daemon: %w", derr)
		}
		if stalePID != 0 {
			if rerr := os.Remove(mountPIDFile(absLocalDir)); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
				return fmt.Errorf("failed to clear stale background mount state for %s: %w", workspaceID, rerr)
			}
		}
		if len(running) > 0 {
			return fmt.Errorf(
				"workspace %s already has a running mount for %s (%s); stop it before starting another",
				workspaceID,
				absLocalDir,
				formatDaemonPIDs(running),
			)
		}
	}

	if *background && !*daemonized {
		return spawnBackgroundMountProcessFn(args, absLocalDir, pidFile, logFile)
	}
	registerPID := shouldRegisterMountPID(*daemonized, *once)
	if *daemonized {
		if err := rotateLogFile(logFile); err != nil {
			return err
		}
	}
	if registerPID {
		if err := writeDaemonPIDState(pidFile, daemonPIDState{
			PID:         os.Getpid(),
			WorkspaceID: workspaceID,
			LocalDir:    absLocalDir,
			LogFile:     logFile,
			StartedAt:   time.Now().UTC().Format(time.RFC3339),
			Executable:  resolvedSelfExecutable(),
		}); err != nil {
			return err
		}
		defer os.Remove(pidFile)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// No whole-request Timeout (see dead-letter syncer above): the
	// bootstrap full-pull streams large bodies well past *timeout, and
	// net/http's http.Client.Timeout would abort it mid-stream regardless
	// of context. Per-cycle/bootstrap/cursor contexts own cancellation.
	client := mountsync.NewHTTPClient(*server, tokenValue, &http.Client{
		Transport: newWritebackFailureTransport(absLocalDir, log.Default(), mountsync.NewSyncTransport()),
	})
	syncer, err := mountsync.NewSyncer(client, mountsync.SyncerOptions{
		WorkspaceID:        workspaceID,
		RemoteRoot:         *remotePath,
		EventProvider:      strings.TrimSpace(*eventProvider),
		LocalRoot:          absLocalDir,
		StateFile:          strings.TrimSpace(*stateFile),
		StateDir:           strings.TrimSpace(*stateDir),
		MountKind:          strings.TrimSpace(*mountKind),
		ValidateState:      true,
		WebSocket:          boolPtr(*websocketEnabled),
		LowMemory:          boolPtr(*lowMemory),
		RootCtx:            rootCtx,
		Logger:             log.Default(),
		BootstrapTimeout:   *bootstrapTimeout,
		CursorTimeout:      *cursorTimeout,
		ForceFullReconcile: boolPtr(*fullReconcile),
	})
	if err != nil {
		return fmt.Errorf("failed to initialize mount syncer: %w", err)
	}
	if initialCredExpiresAt != "" {
		syncer.SetCredentialExpiry(initialCredExpiresAt)
	}
	if _, err := mountsync.StartDiagnostics(rootCtx, strings.TrimSpace(*pprofAddr), *memlogInterval, log.Default()); err != nil {
		return fmt.Errorf("start diagnostics: %w", err)
	}
	if _, err := upsertWorkspace(workspaceID); err != nil {
		return err
	}
	record, _ := workspaceRecordByID(workspaceID)
	record.ID = workspaceID
	if record.Name == "" {
		record.Name = workspaceID
	}
	if localDirExplicit || strings.TrimSpace(record.LocalDir) == "" {
		record.LocalDir = absLocalDir
	}
	record.Server = strings.TrimRight(strings.TrimSpace(*server), "/")
	if record.AgentName == "" {
		record.AgentName = "relayfile-cli"
	}
	if len(record.Scopes) == 0 {
		record.Scopes = append([]string(nil), defaultJoinScopes...)
	}
	_, _ = upsertWorkspaceDetails(record)

	loopDelegatedCredsPath := ""
	if usesDelegatedWorkspace {
		loopDelegatedCredsPath = delegatedCredsPath
	}
	return runMountLoop(rootCtx, syncer, absLocalDir, workspaceID, strings.TrimRight(strings.TrimSpace(*server), "/"), loopDelegatedCredsPath, *timeout, *interval, *intervalJitter, *websocketEnabled, *once, *daemonized, pidFile, logFile)
}

func shouldRegisterMountPID(daemonized, once bool) bool {
	return daemonized || !once
}

// mountStartBanner formats the user-facing line printed when the mount loop
// starts. Per contract A13 / §3.1, the banner identifies the default mode
// as a "synced mirror" so users do not assume kernel-level FUSE semantics.
func mountStartBanner(localDir string, interval time.Duration, intervalJitter float64) string {
	return fmt.Sprintf(
		"Synced mirror started at %s. Sync interval %s ±%.0f%%. Type 'relayfile status' for live state.",
		localDir,
		interval.Round(time.Second).String(),
		intervalJitter*100,
	)
}

// printMountHelp surfaces the contract §3.6 list of synced-mirror
// limitations alongside `relayfile mount`'s flag summary so that
// `relayfile mount --help` is self-describing per A13.
func printMountHelp(w io.Writer) {
	fmt.Fprintln(w, `Usage: relayfile mount [WORKSPACE] [LOCAL_DIR]

Mirror a remote workspace to a local directory. The default mode is a
synced mirror (--mode=poll): ordinary files on disk that a daemon polls
the cloud for every 30 s and writes back through. FUSE is opt-in via
--mode=fuse.

Synced-mirror limitations (§3.6 of the productized cloud-mount contract):
  - File handles are not stable across syncs; an editor that holds a
    file open during a remote update will see content change underneath
    it on the next reconcile.
  - mtime reflects the local write time, not the source-of-truth event
    time. Use .relay/state.json for ordering.
  - Directory listings can briefly omit a newly created remote file
    until the next reconcile (default 30 s) or a websocket event.
  - inotify/fsevents watchers downstream of the mirror will see
    synthetic create events on every reconcile of new content; debounce
    >= 1 s if a downstream tool requires single-shot events.

Common flags:
  --workspace NAME     workspace name or id (defaults to the active workspace)
  --local-dir DIR      local mirror directory (defaults to the recorded mirror)
  --mode poll|fuse     poll (synced mirror, default) or fuse
  --interval 30s       sync interval (default 30s)
  --background         detach and keep syncing in the background
  --once               run one sync cycle and exit (used by setup/CI)
  --timeout 5m         per-sync timeout
  --bootstrap-timeout 0s
                       hard cap for initial/full-tree bootstrap (0 = progress-based)
  --cursor-timeout 60s timeout for events-cursor resolution
  --full-reconcile     force one full reconcile regardless of bootstrap state
  --state-dir DIR      private mount state directory (default $HOME/.relayfile-mount-state)
  --state-file FILE    exact private state file override; wins over --state-dir
  --rehome             allow moving an already-registered mirror to a new LOCAL_DIR
  --no-websocket       disable websocket event streaming
  --low-memory         skip detailed per-file public state and defer content reads
  --pprof-addr ADDR    expose pprof diagnostics, e.g. 127.0.0.1:6060
  --memlog-interval 1m log runtime memory stats periodically

See 'relayfile help' for the full command list and
docs/guides/vfs-cloud-setup.md#known-limitations for details.`)
}

type workspaceCommandClient struct {
	workspaceID string
	record      workspaceRecord
	client      *apiClient
	scopes      []string
	directToken bool
	credsFile   string
}

func prepareWorkspaceCommandClient(workspaceValue, serverFlag, tokenFlag string, requestedScopes []string) (*workspaceCommandClient, error) {
	creds, _ := loadCredentials()
	tokenValue := resolveExplicitToken(tokenFlag)
	directToken := tokenValue != ""
	credsFile := ""
	var bundle delegatedauth.Bundle
	var err error
	if !directToken && strings.TrimSpace(tokenValue) == "" {
		bundle, credsFile, err = loadOrBootstrapDelegatedCredentials(workspaceValue, requestedScopes)
		if err != nil {
			return nil, fmt.Errorf("resolve delegated relayfile credentials: %w", err)
		}
		bundle, err = refreshDelegatedCredentials(credsFile, bundle, false)
		if err != nil {
			return nil, fmt.Errorf("refresh delegated relayfile credentials: %w", err)
		}
		tokenValue = bundle.BearerToken()
		if strings.TrimSpace(serverFlag) == "" {
			serverFlag = bundle.ServerURL()
		}
	}
	workspaceID := ""
	if credsFile != "" {
		workspaceID = bundle.Workspace()
		if !workspaceRequestMatchesDelegatedCredentials(workspaceValue, workspaceID) {
			return nil, fmt.Errorf(
				"delegated relayfile credentials are for workspace %s; pass --token for explicit workspace %q or re-bootstrap delegated credentials for that workspace",
				workspaceID,
				workspaceValue,
			)
		}
	} else {
		workspaceID, err = resolveWorkspaceIDWithToken(workspaceValue, tokenValue)
		if err != nil {
			return nil, err
		}
	}
	record := workspaceRecordForCommand(workspaceValue, workspaceID)
	if credsFile != "" {
		if strings.TrimSpace(record.ID) == "" {
			record.ID = workspaceID
		} else if strings.TrimSpace(record.ID) != workspaceID {
			record.RelayWorkspaceID = workspaceID
		}
		record.Server = strings.TrimRight(bundle.ServerURL(), "/")
		record.CloudAPIURL = ""
		record.LastUsedAt = time.Now().UTC().Format(time.RFC3339)
	}
	scopes := effectiveCommandScopes(record, requestedScopes)
	commandClient := &workspaceCommandClient{
		workspaceID: workspaceID,
		record:      record,
		scopes:      scopes,
		directToken: directToken,
		credsFile:   credsFile,
	}
	if credsFile != "" {
		persisted, err := upsertWorkspaceDetails(record)
		if err != nil {
			return nil, err
		}
		commandClient.record = persisted
	}

	shouldRefreshDelegated := !directToken && credsFile != "" && (strings.TrimSpace(tokenValue) == "" || relayfileTokenNeedsRefresh(tokenValue))
	if shouldRefreshDelegated {
		if err := commandClient.refreshFromDelegated(); err == nil {
			return commandClient, nil
		} else if strings.TrimSpace(tokenValue) == "" {
			return nil, err
		}
	}

	client, err := newAPIClient(resolveServer(serverFlag, creds), tokenValue)
	if err != nil {
		return nil, err
	}
	commandClient.client = client
	if credsFile != "" {
		if err := removeCredentialFile(credentialsPath()); err != nil {
			return nil, fmt.Errorf("clear stale relayfile credentials: %w", err)
		}
	}
	return commandClient, nil
}

func workspaceRecordForCommand(workspaceValue, workspaceID string) workspaceRecord {
	if record, ok := workspaceRecordByName(strings.TrimSpace(workspaceValue)); ok {
		return normalizeWorkspaceCommandRecord(record, workspaceID)
	}
	if record, ok := workspaceRecordByID(workspaceID); ok {
		return normalizeWorkspaceCommandRecord(record, workspaceID)
	}
	return workspaceRecord{
		Name:      workspaceID,
		ID:        workspaceID,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		AgentName: "relayfile-cli",
	}
}

func normalizeWorkspaceCommandRecord(record workspaceRecord, workspaceID string) workspaceRecord {
	if strings.TrimSpace(record.ID) == "" {
		record.ID = workspaceID
	}
	if strings.TrimSpace(record.Name) == "" {
		record.Name = record.ID
	}
	if strings.TrimSpace(record.CreatedAt) == "" {
		record.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if strings.TrimSpace(record.AgentName) == "" {
		record.AgentName = "relayfile-cli"
	}
	return record
}

func effectiveCommandScopes(record workspaceRecord, requestedScopes []string) []string {
	if len(requestedScopes) > 0 {
		return append([]string(nil), requestedScopes...)
	}
	if len(record.Scopes) > 0 {
		return append([]string(nil), record.Scopes...)
	}
	return append([]string(nil), defaultJoinScopes...)
}

func (c *workspaceCommandClient) refreshFromDelegated() error {
	if c == nil {
		return errors.New("workspace command client is nil")
	}
	if strings.TrimSpace(c.credsFile) == "" {
		return delegatedauth.ErrMissingCredentials
	}
	bundle, _, err := loadDelegatedCredentials(c.credsFile)
	if err != nil {
		return err
	}
	if bundle.Workspace() != "" && bundle.Workspace() != c.workspaceID {
		return fmt.Errorf("delegated relayfile credentials are for workspace %s, expected %s", bundle.Workspace(), c.workspaceID)
	}
	renewed, err := refreshDelegatedCredentials(c.credsFile, bundle, true)
	if err != nil {
		return err
	}
	c.workspaceID = renewed.Workspace()
	record := c.record
	if strings.TrimSpace(record.ID) == "" {
		record.ID = c.workspaceID
	} else if strings.TrimSpace(record.ID) != c.workspaceID {
		record.RelayWorkspaceID = c.workspaceID
	}
	record.Server = strings.TrimRight(renewed.ServerURL(), "/")
	record.CloudAPIURL = ""
	record.LastUsedAt = time.Now().UTC().Format(time.RFC3339)
	record, err = upsertWorkspaceDetails(record)
	if err != nil {
		return err
	}
	client, err := newAPIClient(strings.TrimRight(renewed.ServerURL(), "/"), renewed.BearerToken())
	if err != nil {
		return err
	}
	c.record = record
	c.client = client
	return nil
}

func (c *workspaceCommandClient) getWorkspaceBytes(ctx context.Context, pathForWorkspace func(string) string) ([]byte, string, error) {
	body, contentType, err := c.client.getBytes(ctx, pathForWorkspace(c.workspaceID))
	if err == nil || c.directToken || !isAPIAuthError(err) {
		return body, contentType, err
	}
	if refreshErr := c.refreshFromDelegated(); refreshErr != nil {
		return body, contentType, err
	}
	return c.client.getBytes(ctx, pathForWorkspace(c.workspaceID))
}

func (c *workspaceCommandClient) getWorkspaceJSON(ctx context.Context, pathForWorkspace func(string) string, out any) error {
	err := c.client.getJSON(ctx, pathForWorkspace(c.workspaceID), out)
	if err == nil || c.directToken || !isAPIAuthError(err) {
		return err
	}
	if refreshErr := c.refreshFromDelegated(); refreshErr != nil {
		return err
	}
	return c.client.getJSON(ctx, pathForWorkspace(c.workspaceID), out)
}

func (c *workspaceCommandClient) postWorkspaceJSON(ctx context.Context, pathForWorkspace func(string) string, body any, out any) error {
	err := c.client.postJSON(ctx, pathForWorkspace(c.workspaceID), body, out)
	if err == nil || c.directToken || !isAPIAuthError(err) {
		return err
	}
	if refreshErr := c.refreshFromDelegated(); refreshErr != nil {
		return err
	}
	return c.client.postJSON(ctx, pathForWorkspace(c.workspaceID), body, out)
}

func (c *workspaceCommandClient) deleteWorkspaceJSON(ctx context.Context, pathForWorkspace func(string) string, ifMatch string, out any) error {
	err := c.client.deleteJSON(ctx, pathForWorkspace(c.workspaceID), ifMatch, out)
	if err == nil || c.directToken || !isAPIAuthError(err) {
		return err
	}
	if refreshErr := c.refreshFromDelegated(); refreshErr != nil {
		return err
	}
	return c.client.deleteJSON(ctx, pathForWorkspace(c.workspaceID), ifMatch, out)
}

func isAPIAuthError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *apiError
	return errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden)
}

// listenEvent is the wire format for events delivered over /fs/ws.
type listenEvent struct {
	EventID       string `json:"eventId"`
	Type          string `json:"type"`
	Path          string `json:"path"`
	Revision      string `json:"revision"`
	ContentHash   string `json:"contentHash,omitempty"`
	Origin        string `json:"origin,omitempty"`
	Provider      string `json:"provider,omitempty"`
	CorrelationID string `json:"correlationId,omitempty"`
	Timestamp     string `json:"timestamp,omitempty"`
}

const listenRunDuplicateWindow = 2 * time.Second
const listenRunDuplicateSweepInterval = 100

type listenRunDuplicateSuppressor struct {
	window time.Duration
	seen   map[string]time.Time
	calls  int
}

func newListenRunDuplicateSuppressor(window time.Duration) *listenRunDuplicateSuppressor {
	if window <= 0 {
		window = listenRunDuplicateWindow
	}
	return &listenRunDuplicateSuppressor{
		window: window,
		seen:   map[string]time.Time{},
	}
}

func (s *listenRunDuplicateSuppressor) shouldSuppress(evt listenEvent, now time.Time) bool {
	if s == nil {
		return false
	}
	key := listenRunDuplicateKey(evt)
	if key == "" {
		return false
	}
	s.calls++
	if s.calls%listenRunDuplicateSweepInterval == 0 {
		for existingKey, seenAt := range s.seen {
			if now.Sub(seenAt) > s.window {
				delete(s.seen, existingKey)
			}
		}
	}
	if seenAt, ok := s.seen[key]; ok {
		if now.Sub(seenAt) <= s.window {
			return true
		}
		delete(s.seen, key)
	}
	s.seen[key] = now
	return false
}

func listenRunDuplicateKey(evt listenEvent) string {
	eventType := strings.TrimSpace(evt.Type)
	eventPath := strings.TrimSpace(evt.Path)
	if eventType == "" || eventPath == "" {
		return ""
	}
	if contentHash := strings.TrimSpace(evt.ContentHash); contentHash != "" {
		return eventType + "\x00" + eventPath + "\x00hash:" + contentHash
	}
	if correlationID := strings.TrimSpace(evt.CorrelationID); correlationID != "" {
		return eventType + "\x00" + eventPath + "\x00corr:" + correlationID
	}
	return ""
}

func printListenUsage(w io.Writer) {
	fmt.Fprintln(w, `relayfile listen streams live file events from a workspace and optionally
runs a command for each matching event.

Usage:
  relayfile listen [WORKSPACE] [--provider PROVIDER] [--path GLOB] [--event TYPE] [--run CMD] [--format text|json]

Flags:
  --provider PROVIDER  filter to a specific integration (linear, notion, hubspot, …)
                       shorthand for --path /PROVIDER/**
  --path GLOB          glob path filter. The workspace tree has alias views that let
                       you filter far below the provider level — by status, label,
                       project, channel, and more. See examples below.
  --event TYPE         filter by event type: file.created, file.updated, file.deleted
                       (default: all types)
  --run CMD            shell command to execute per matching event.
                       Use {{path}}, {{type}}, {{provider}}, {{revision}}, and
                       {{event}} (full JSON) as placeholders.
  --format text|json   output format when --run is not set (default: text)

Examples:

  # Stream all events from the default workspace
  relayfile listen

  # --- Linear ---

  # New issue filed anywhere in Linear
  relayfile listen --provider linear --event file.created \
    --run "claude --print 'New Linear issue at {{path}}. Suggest a priority and owner.'"

  # New issue filed, but only when it lands in the Triage state
  relayfile listen --path "/linear/issues/by-state/triage/**" --event file.created \
    --run "claude --print 'Untriaged issue at {{path}}. Assign priority, owner, and cycle.'"

  # Any In Progress issue updated (catch status changes, description edits, etc.)
  relayfile listen --path "/linear/issues/by-state/in-progress/**" --event file.updated \
    --run "claude --print 'In-progress issue changed at {{path}}. Check for blockers.'"

  # --- GitHub ---

  # New PR opened on any repo in the org
  relayfile listen --path "/github/repos/**/pulls/**" --event file.created \
    --run "claude --print 'New PR at {{path}}. Write a one-paragraph review summary.'"

  # New PR labeled needs-review on a specific repo
  relayfile listen --path "/github/repos/acme/api/pulls/by-label/needs-review/**" --event file.created \
    --run "claude --print 'PR needs review at {{path}}. Summarise the diff and flag risks.'"

  # --- Notion ---

  # Any page edited across the whole workspace
  relayfile listen --provider notion --event file.updated \
    --run "claude --print 'Notion page changed at {{path}}. Summarise the update.'"

  # Edits only inside a specific Notion database
  relayfile listen --path "/notion/databases/roadmap/**" --event file.updated \
    --run "claude --print 'Roadmap item changed at {{path}}. Send a Slack digest.'"

  # --- Slack ---

  # New message in a specific channel
  relayfile listen --path "/slack/channels/incidents/**" --event file.created \
    --run "claude --print 'New incident message at {{path}}. Draft a status-page update.'"

  # --- HubSpot ---

  # New contact created
  relayfile listen --path "/hubspot/contacts/**" --event file.created \
    --run "claude --print 'New HubSpot contact at {{path}}. Draft a personalised intro email.'"

  # Deal moved to a new stage
  relayfile listen --path "/hubspot/deals/**" --event file.updated \
    --run "claude --print 'Deal updated at {{path}}. Draft a follow-up for the new stage.'"

  # --- Asana ---

  # New task in a specific project
  relayfile listen --path "/asana/projects/q3-launch/**" --event file.created \
    --run "claude --print 'New task in Q3 launch at {{path}}. Break it into subtasks.'"

  # --- Shortcut ---

  # New story under a specific epic
  relayfile listen --path "/shortcut/stories/by-epic/payments/**" --event file.created \
    --run "claude --print 'New payments story at {{path}}. Suggest an implementation approach.'"

  # --- Granola / Fathom ---

  # New meeting notes → extract action items
  relayfile listen --provider granola --event file.created \
    --run "claude --print 'New meeting notes at {{path}}. Extract action items and owners.'"

  # New Fathom call recording → follow-up email
  relayfile listen --provider fathom --event file.created \
    --run "claude --print 'New call at {{path}}. Write a follow-up email with key decisions.'"

  # --- Scripting ---

  # Print raw JSON events for piping
  relayfile listen --provider linear --format json | jq '.path'

The workspace tree has alias views (by-state/, by-label/, by-epic/, by-name/, by-id/, …)
for every provider. Run 'relayfile tree / --depth 3' to explore what's available.

Want this running headlessly for your whole team — turning issues into reviewed PRs automatically?
See https://github.com/AgentWorkforce/factory`)
}

// runDev is the zero-friction entry point for reactive local agents.
// It checks credentials and integration status, prints a status header,
// then hands off to the listen loop.
func runDev(args []string, stdin io.Reader, stdout io.Writer) error {
	// Peek at flags without consuming them — runListen re-parses the same slice.
	peek := flag.NewFlagSet("dev-peek", flag.ContinueOnError)
	peek.SetOutput(io.Discard)
	serverPeek := peek.String("server", "", "")
	tokenPeek := peek.String("token", "", "")
	providerPeek := peek.String("provider", "", "")
	_ = peek.Parse(normalizeFlagArgs(args, map[string]bool{
		"server": true, "token": true, "provider": true,
		"path": true, "event": true, "run": true, "format": true,
		"background": false, "daemonized": false,
	}))

	commandClient, err := prepareWorkspaceCommandClient("", *serverPeek, *tokenPeek, defaultInspectScopes)
	if err != nil {
		provider := strings.TrimSpace(*providerPeek)
		if provider == "" {
			provider = "linear"
		}
		fmt.Fprintln(stdout, "Not connected to Agent Relay. Get started with:")
		fmt.Fprintf(stdout, "\n  relayfile setup --provider %s\n\n", provider)
		fmt.Fprintln(stdout, "Then re-run: relayfile dev "+strings.Join(args, " "))
		return err
	}

	fmt.Fprintf(stdout, "Workspace: %s\n", commandClient.workspaceID)
	if p := strings.TrimSpace(*providerPeek); p != "" {
		fmt.Fprintf(stdout, "Provider filter: %s\n", p)
		fmt.Fprintf(stdout, "Tip: run 'relayfile integration list' to see connected providers.\n")
	}
	fmt.Fprintln(stdout)

	return runListen(args, stdout)
}

// matchListenPath reports whether eventPath matches the glob filter used by
// relayfile listen. It handles the double-star (**) recursive wildcard that
// path.Match does not support: "/**" suffix matches any path rooted at the
// prefix, and "**" alone matches everything.
func matchListenPath(glob, eventPath string) bool {
	if glob == "" || glob == "**" || glob == "/**" {
		return true
	}
	// "/foo/**" matches "/foo" and anything under it.
	if strings.HasSuffix(glob, "/**") {
		prefix := strings.TrimSuffix(glob, "/**")
		return eventPath == prefix || strings.HasPrefix(eventPath, prefix+"/")
	}
	// Fall back to path.Match for single-star globs.
	matched, err := path.Match(glob, eventPath)
	return err == nil && matched
}

// wsEncodeGlob encodes a path glob for a WebSocket URL query parameter,
// preserving /, *, and ? as literal characters so server-side glob matching works.
func wsEncodeGlob(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '/' || r == '*' || r == '?' || r == '-' || r == '_' || r == '.' || r == '~':
			b.WriteRune(r)
		case (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		default:
			// QueryEscape encodes spaces as "+"; rewrite to "%20" so the
			// encoded value is unambiguous in both query and path contexts.
			// Both decode to a space server-side via url.Query(), so this is
			// purely a more robust encoding, not a behavior change.
			b.WriteString(strings.ReplaceAll(url.QueryEscape(string(r)), "+", "%20"))
		}
	}
	return b.String()
}

func runListen(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("listen", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "relayfile server URL override")
	token := fs.String("token", "", "relayfile token override")
	providerFlag := fs.String("provider", "", "filter to a specific provider (e.g. linear, notion)")
	pathFlag := fs.String("path", "", "glob path filter (e.g. /linear/issues/**)")
	eventFlag := fs.String("event", "", "event type filter: file.created, file.updated, file.deleted")
	runFlag := fs.String("run", "", "shell command per event; supports {{path}}, {{type}}, {{provider}}, {{revision}}, {{event}}")
	formatFlag := fs.String("format", "text", "output format when --run is not set: text or json")
	background := fs.Bool("background", false, "run in background; logs to ~/.relayfile/listen.log")
	daemonized := fs.Bool("daemonized", false, "internal flag used by relayfile listen --background")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server":     true,
		"token":      true,
		"provider":   true,
		"path":       true,
		"event":      true,
		"run":        true,
		"format":     true,
		"background": false,
		"daemonized": false,
	})); err != nil {
		return err
	}
	var workspaceValue string
	if fs.NArg() > 0 {
		workspaceValue = strings.TrimSpace(fs.Arg(0))
	}

	if *background && !*daemonized {
		logFile := listenLogFile()
		pidFile := listenPIDFile()
		return spawnBackgroundListenProcess(args, pidFile, logFile)
	}
	if *daemonized {
		if err := rotateLogFile(listenLogFile()); err != nil {
			return err
		}
	}

	commandClient, err := prepareWorkspaceCommandClient(workspaceValue, *server, *token, defaultInspectScopes)
	if err != nil {
		return err
	}

	// Build WebSocket URL from the HTTP base URL.
	// relayfile listen connects directly to Agent Relay Cloud — no local
	// daemon or FUSE mount is required.
	base, err := url.Parse(strings.TrimRight(commandClient.client.baseURL, "/"))
	if err != nil {
		return fmt.Errorf("invalid server URL: %w", err)
	}
	switch base.Scheme {
	case "http":
		base.Scheme = "ws"
	case "https":
		base.Scheme = "wss"
	}
	base.Path = fmt.Sprintf("/v1/workspaces/%s/fs/ws", url.PathEscape(commandClient.workspaceID))

	pathFilter := strings.TrimSpace(*pathFlag)
	if pathFilter == "" && strings.TrimSpace(*providerFlag) != "" {
		pathFilter = fmt.Sprintf("/%s/**", strings.TrimSpace(*providerFlag))
	}
	// Build the raw query manually so that path glob characters (/ * ?) are NOT
	// percent-encoded. url.Values.Encode() encodes them, which causes the server
	// glob matcher to receive a literal "%2Flinear%2F%2A%2A" and match nothing.
	rawParts := []string{"from=now"}
	if pathFilter != "" {
		rawParts = append(rawParts, "path="+wsEncodeGlob(pathFilter))
	}
	// Token in query param for WS upgrade (server does not yet support Authorization on upgrade).
	rawParts = append(rawParts, "token="+url.QueryEscape(commandClient.client.token))
	base.RawQuery = strings.Join(rawParts, "&")

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// WebSocket upgrade requires HTTP/1.1; disable h2 so TLS ALPN negotiation
	// doesn't select HTTP/2 (which rejects the Upgrade header).
	wsTransport := http.DefaultTransport.(*http.Transport).Clone()
	wsTransport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	wsHTTPClient := &http.Client{Transport: wsTransport}

	conn, _, err := websocket.Dial(rootCtx, base.String(), &websocket.DialOptions{
		HTTPClient: wsHTTPClient,
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + commandClient.client.token},
		},
	})
	if err != nil {
		return fmt.Errorf("connect to event stream: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	typeFilter := strings.TrimSpace(*eventFlag)
	runCmd := strings.TrimSpace(*runFlag)
	format := strings.TrimSpace(*formatFlag)
	var runDuplicateSuppressor *listenRunDuplicateSuppressor
	if runCmd != "" {
		runDuplicateSuppressor = newListenRunDuplicateSuppressor(listenRunDuplicateWindow)
	}

	label := "all events"
	if pathFilter != "" {
		label = pathFilter
	}
	if typeFilter != "" {
		label += " (" + typeFilter + ")"
	}
	if !*daemonized {
		fmt.Fprintf(stdout, "Listening on %s — Ctrl+C to stop\n", label)
		if runCmd == "" && format == "text" {
			fmt.Fprintln(stdout, "Tip: pass --run to execute a command per event.")
			fmt.Fprintln(stdout, "     See 'relayfile help listen' for examples with Linear, Notion, HubSpot, and more.")
			fmt.Fprintln(stdout, "     Add --background to detach; 'relayfile supervisor install --listen' to survive reboots.")
		}
		fmt.Fprintln(stdout)
	}

	for {
		var raw json.RawMessage
		if err := wsjson.Read(rootCtx, conn, &raw); err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
				websocket.CloseStatus(err) == websocket.StatusGoingAway ||
				errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("event stream error: %w", err)
		}

		var evt listenEvent
		if err := json.Unmarshal(raw, &evt); err != nil || evt.Type == "" || evt.Type == "pong" {
			continue
		}
		if typeFilter != "" && evt.Type != typeFilter {
			continue
		}
		if pathFilter != "" && !matchListenPath(pathFilter, evt.Path) {
			continue
		}

		if runCmd != "" {
			if runDuplicateSuppressor.shouldSuppress(evt, time.Now()) {
				continue
			}
			expanded := listenExpandTemplate(runCmd, evt, raw)
			cmd := exec.CommandContext(rootCtx, "sh", "-c", expanded)
			cmd.Stdout = stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil && !errors.Is(err, context.Canceled) {
				fmt.Fprintf(os.Stderr, "run error for %s: %v\n", evt.Path, err)
			}
			continue
		}

		if format == "json" {
			fmt.Fprintf(stdout, "%s\n", string(raw))
			continue
		}

		// Default text output.
		ts := strings.TrimSpace(evt.Timestamp)
		if ts == "" {
			ts = time.Now().UTC().Format(time.RFC3339)
		}
		provider := strings.TrimSpace(evt.Provider)
		if provider == "" {
			// Infer provider from the leading path segment.
			seg := strings.TrimPrefix(evt.Path, "/")
			if i := strings.IndexByte(seg, '/'); i > 0 {
				provider = seg[:i]
			}
		}
		if provider != "" {
			fmt.Fprintf(stdout, "%-20s  %-30s  %s  [%s]\n", evt.Type, evt.Path, ts, provider)
		} else {
			fmt.Fprintf(stdout, "%-20s  %-30s  %s\n", evt.Type, evt.Path, ts)
		}
	}
}

func listenPIDFile() string {
	return filepath.Join(configDir(), "listen.pid")
}

func listenLogFile() string {
	return filepath.Join(configDir(), "listen.log")
}

func spawnBackgroundListenProcess(originalArgs []string, pidFile, logFile string) error {
	if err := rotateLogFile(logFile); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	filtered := make([]string, 0, len(originalArgs))
	for _, arg := range originalArgs {
		if arg == "--background" || arg == "-background" ||
			strings.HasPrefix(arg, "--background=") || strings.HasPrefix(arg, "-background=") {
			continue
		}
		filtered = append(filtered, arg)
	}
	childArgs := append([]string{"listen"}, filtered...)
	childArgs = append(childArgs, "--daemonized", "--pid-file", pidFile)
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
	fmt.Fprintf(os.Stdout, "Listen started in background. Logs: %s\n", logFile)
	return nil
}

func listenExpandTemplate(tmpl string, evt listenEvent, raw json.RawMessage) string {
	return strings.NewReplacer(
		"{{path}}", evt.Path,
		"{{type}}", evt.Type,
		"{{provider}}", evt.Provider,
		"{{revision}}", evt.Revision,
		"{{event}}", string(raw),
	).Replace(tmpl)
}

func printSupervisorUsage(w io.Writer) {
	fmt.Fprintln(w, `relayfile supervisor manages the listen daemon as a system service.

On Linux  it writes a systemd user unit (~/.config/systemd/user/relayfile-listen.service).
On macOS  it writes a launchd agent  (~/Library/LaunchAgents/com.relayfile.listen.plist).

Usage:
  relayfile supervisor install [LISTEN_FLAGS...]   install and start the service
  relayfile supervisor uninstall                   stop, disable, and remove the service
  relayfile supervisor status                      show service status

Examples:

  # Install: react to every new Linear triage issue
  relayfile supervisor install \
    --path "/linear/issues/by-state/triage/**" --event file.created \
    --run "claude --print 'New triage issue at {{path}}. Assign it.'"

  # Install: all Linear events, background agent
  relayfile supervisor install --provider linear --run "my-agent --event '{{event}}'"

  relayfile supervisor status
  relayfile supervisor uninstall

All flags accepted by 'relayfile listen' are accepted here and are embedded
verbatim into the unit file. The service restarts automatically on failure.`)
}

const (
	supervisorServiceName  = "relayfile-listen"
	supervisorLaunchdLabel = "com.relayfile.listen"
)

func supervisorSystemdUnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", supervisorServiceName+".service"), nil
}

func supervisorLaunchdPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", supervisorLaunchdLabel+".plist"), nil
}

func runSupervisor(args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printSupervisorUsage(stdout)
		return nil
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "install":
		return supervisorInstall(rest, stdout)
	case "uninstall", "remove":
		return supervisorUninstall(stdout)
	case "status":
		return supervisorStatus(stdout)
	default:
		printSupervisorUsage(stdout)
		return fmt.Errorf("unknown supervisor subcommand %q", sub)
	}
}

func supervisorInstall(listenArgs []string, stdout io.Writer) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate relayfile binary: %w", err)
	}
	logFile := listenLogFile()

	switch runtime.GOOS {
	case "linux":
		return supervisorInstallSystemd(executable, listenArgs, logFile, stdout)
	case "darwin":
		return supervisorInstallLaunchd(executable, listenArgs, logFile, stdout)
	default:
		return fmt.Errorf("supervisor install is not supported on %s; run 'relayfile listen --background' instead", runtime.GOOS)
	}
}

func supervisorInstallSystemd(executable string, listenArgs []string, logFile string, stdout io.Writer) error {
	unitPath, err := supervisorSystemdUnitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return fmt.Errorf("create systemd user unit dir: %w", err)
	}

	// Build ExecStart line: quote args that contain spaces or special chars.
	execArgs := []string{executable, "listen"}
	execArgs = append(execArgs, listenArgs...)
	execStart := shelljoin(execArgs)

	unit := fmt.Sprintf(`[Unit]
Description=Relayfile listen — reactive agent event stream
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=5s
StandardOutput=append:%s
StandardError=append:%s

[Install]
WantedBy=default.target
`, execStart, logFile, logFile)

	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}
	fmt.Fprintf(stdout, "Wrote %s\n", unitPath)

	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", "--now", supervisorServiceName},
	} {
		cmd := exec.Command("systemctl", args...)
		cmd.Stdout = stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
		}
	}
	fmt.Fprintf(stdout, "\nService started. Logs: %s\n", logFile)
	fmt.Fprintf(stdout, "Status: systemctl --user status %s\n", supervisorServiceName)
	return nil
}

func supervisorInstallLaunchd(executable string, listenArgs []string, logFile string, stdout io.Writer) error {
	plistPath, err := supervisorLaunchdPlistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}

	// Build <array> of <string> elements for ProgramArguments.
	programArgs := append([]string{executable, "listen"}, listenArgs...)
	var argElems strings.Builder
	for _, a := range programArgs {
		argElems.WriteString("\t\t<string>")
		argElems.WriteString(plistEscapeXML(a))
		argElems.WriteString("</string>\n")
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
%s	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, supervisorLaunchdLabel, argElems.String(), plistEscapeXML(logFile), plistEscapeXML(logFile))

	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	fmt.Fprintf(stdout, "Wrote %s\n", plistPath)

	// Unload first in case an old version is loaded.
	unload := exec.Command("launchctl", "unload", "-w", plistPath)
	_ = unload.Run()

	load := exec.Command("launchctl", "load", "-w", plistPath)
	load.Stdout = stdout
	load.Stderr = os.Stderr
	if err := load.Run(); err != nil {
		return fmt.Errorf("launchctl load: %w", err)
	}
	fmt.Fprintf(stdout, "\nService started. Logs: %s\n", logFile)
	fmt.Fprintf(stdout, "Status: launchctl list %s\n", supervisorLaunchdLabel)
	return nil
}

func supervisorUninstall(stdout io.Writer) error {
	switch runtime.GOOS {
	case "linux":
		return supervisorUninstallSystemd(stdout)
	case "darwin":
		return supervisorUninstallLaunchd(stdout)
	default:
		return fmt.Errorf("supervisor uninstall is not supported on %s", runtime.GOOS)
	}
}

func supervisorUninstallSystemd(stdout io.Writer) error {
	for _, args := range [][]string{
		{"--user", "disable", "--now", supervisorServiceName},
	} {
		cmd := exec.Command("systemctl", args...)
		cmd.Stdout = stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run() // best-effort; unit may not be loaded
	}
	unitPath, err := supervisorSystemdUnitPath()
	if err != nil {
		return err
	}
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit file: %w", err)
	}
	exec.Command("systemctl", "--user", "daemon-reload").Run() //nolint
	fmt.Fprintln(stdout, "Service stopped and removed.")
	return nil
}

func supervisorUninstallLaunchd(stdout io.Writer) error {
	plistPath, err := supervisorLaunchdPlistPath()
	if err != nil {
		return err
	}
	unload := exec.Command("launchctl", "unload", "-w", plistPath)
	unload.Stdout = stdout
	unload.Stderr = os.Stderr
	_ = unload.Run() // best-effort
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist: %w", err)
	}
	fmt.Fprintln(stdout, "Service stopped and removed.")
	return nil
}

func supervisorStatus(stdout io.Writer) error {
	switch runtime.GOOS {
	case "linux":
		cmd := exec.Command("systemctl", "--user", "status", supervisorServiceName)
		cmd.Stdout = stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	case "darwin":
		cmd := exec.Command("launchctl", "list", supervisorLaunchdLabel)
		cmd.Stdout = stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	default:
		fmt.Fprintf(stdout, "supervisor status is not supported on %s\n", runtime.GOOS)
	}
	return nil
}

// shelljoin builds a shell-safe ExecStart string by quoting arguments that
// contain spaces or shell metacharacters.
func shelljoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t\"'\\$`{}[]|&;<>()#~!") {
			a = "\"" + strings.ReplaceAll(a, "\"", "\\\"") + "\""
		}
		quoted[i] = a
	}
	return strings.Join(quoted, " ")
}

// plistEscapeXML escapes the five XML entities that can appear in plist string values.
func plistEscapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
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

	remotePath := strings.TrimSpace(*pathFlag)
	var workspaceValue string
	switch fs.NArg() {
	case 0:
	case 1:
		arg := strings.TrimSpace(fs.Arg(0))
		if looksLikeRemotePathArg(arg) {
			remotePath = normalizeCLIPathArg(arg)
		} else {
			workspaceValue = arg
		}
	case 2:
		workspaceValue = strings.TrimSpace(fs.Arg(0))
		remotePath = strings.TrimSpace(fs.Arg(1))
	}
	commandClient, err := prepareWorkspaceCommandClient(workspaceValue, *server, *token, defaultInspectScopes)
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
	body, _, err := commandClient.getWorkspaceBytes(context.Background(), func(workspaceID string) string {
		return fmt.Sprintf("/v1/workspaces/%s/fs/tree?%s", url.PathEscape(workspaceID), query.Encode())
	})
	if err != nil {
		return err
	}
	if _, err := upsertWorkspaceDetails(commandClient.record); err != nil {
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

func looksLikeRemotePathArg(arg string) bool {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return false
	}
	if strings.HasPrefix(arg, "/") {
		return true
	}
	if _, ok := catalogWorkspaceID(arg); ok {
		return false
	}
	first, _, ok := strings.Cut(arg, "/")
	if !ok {
		return false
	}
	return knownRemoteRootSegment(first)
}

func normalizeCLIPathArg(arg string) string {
	arg = strings.TrimSpace(arg)
	if arg == "" || strings.HasPrefix(arg, "/") {
		return arg
	}
	return "/" + arg
}

func knownRemoteRootSegment(segment string) bool {
	segment = strings.Trim(strings.TrimSpace(segment), "/")
	if segment == "" {
		return false
	}
	switch segment {
	case "digests", ".skills", ".relay":
		return true
	}
	for _, entry := range fallbackIntegrationCatalog() {
		root := strings.Trim(strings.TrimSpace(entry.VFSRoot), "/")
		if root != "" && segment == root {
			return true
		}
		if dir := providerRootDir(entry.ID); dir != "" && segment == dir {
			return true
		}
	}
	switch segment {
	case "google-mail", "google-calendar", "jira", "confluence":
		return true
	default:
		return false
	}
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

	var workspaceValue string
	var remotePath string
	if fs.NArg() == 1 {
		remotePath = strings.TrimSpace(fs.Arg(0))
	} else {
		workspaceValue = strings.TrimSpace(fs.Arg(0))
		remotePath = strings.TrimSpace(fs.Arg(1))
	}
	commandClient, err := prepareWorkspaceCommandClient(workspaceValue, *server, *token, defaultInspectScopes)
	if err != nil {
		return err
	}
	if remotePath == "" {
		return errors.New("path is required")
	}

	query := url.Values{}
	query.Set("path", remotePath)
	body, _, err := commandClient.getWorkspaceBytes(context.Background(), func(workspaceID string) string {
		return fmt.Sprintf("/v1/workspaces/%s/fs/file?%s", url.PathEscape(workspaceID), query.Encode())
	})
	if err != nil {
		return err
	}
	if _, err := upsertWorkspaceDetails(commandClient.record); err != nil {
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

	workspaceValue := ""
	if fs.NArg() == 1 {
		workspaceValue = strings.TrimSpace(fs.Arg(0))
	}
	commandClient, err := prepareWorkspaceCommandClient(workspaceValue, *server, *token, defaultInspectScopes)
	if err != nil {
		return err
	}
	exportFormat := url.QueryEscape(strings.ToLower(strings.TrimSpace(*format)))
	body, _, err := commandClient.getWorkspaceBytes(context.Background(), func(workspaceID string) string {
		return fmt.Sprintf("/v1/workspaces/%s/fs/export?format=%s", url.PathEscape(workspaceID), exportFormat)
	})
	if err != nil {
		return err
	}
	if _, err := upsertWorkspaceDetails(commandClient.record); err != nil {
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

	workspaceValue := ""
	if fs.NArg() == 1 {
		workspaceValue = strings.TrimSpace(fs.Arg(0))
	}
	commandClient, err := prepareWorkspaceCommandClient(workspaceValue, *server, *token, defaultInspectScopes)
	if err != nil {
		return err
	}
	var status syncStatusResponse
	err = commandClient.getWorkspaceJSON(context.Background(), func(workspaceID string) string {
		return fmt.Sprintf("/v1/workspaces/%s/sync/status", url.PathEscape(workspaceID))
	}, &status)
	if err != nil {
		if isUnauthorizedAPIError(err) {
			return ErrCloudRefreshExpired
		}
		return err
	}
	var ingress *syncIngressStatusResponse
	if statusNeedsIngressDiagnostics(status) {
		var ingressStatus syncIngressStatusResponse
		if err := commandClient.getWorkspaceJSON(context.Background(), func(workspaceID string) string {
			return fmt.Sprintf("/v1/workspaces/%s/sync/ingress", url.PathEscape(workspaceID))
		}, &ingressStatus); err == nil {
			ingress = &ingressStatus
		}
	}
	record, err := upsertWorkspaceDetails(commandClient.record)
	if err != nil {
		return err
	}
	workspaceID := commandClient.workspaceID
	persistedStallReason := readPersistedStallReason(record.LocalDir)
	snapshot := buildSyncStateSnapshot(status, workspaceID, defaultMountMode, defaultMountInterval, record.LocalDir, readDaemonPID(record.LocalDir), persistedStallReason)
	if *jsonOutput {
		return writeJSON(stdout, snapshot)
	}
	workspaceLabel := workspaceID
	if strings.TrimSpace(record.Name) != "" && record.Name != workspaceID {
		workspaceLabel = fmt.Sprintf("%s (%s)", workspaceID, record.Name)
	}
	fmt.Fprintf(stdout, "workspace %s   mode: %s   lag: %s\n", workspaceLabel, snapshot.Mode, formatLag(maxLagSeconds(status.Providers)))
	if authLine := statusAuthLine(record.LocalDir, time.Now().UTC()); authLine != "" {
		fmt.Fprintln(stdout, authLine)
	}
	for _, provider := range status.Providers {
		lastEvent := "-"
		if hasNonEmptyString(provider.WatermarkTs) {
			lastEvent = humanizeRecentTime(strings.TrimSpace(*provider.WatermarkTs))
		}
		line := fmt.Sprintf("  %-12s %-8s lag %s", provider.Provider, provider.Status, formatLag(provider.LagSeconds))
		if lastEvent != "-" {
			line += "   last event " + lastEvent
		}
		if provider.LastError != nil && strings.TrimSpace(*provider.LastError) != "" {
			line += "   last error: " + strings.TrimSpace(*provider.LastError)
		}
		if reason := syncProviderLagReason(provider, ingress); reason != "" {
			line += "   reason: " + reason
		}
		fmt.Fprintln(stdout, line)
		// Contract §7.4: warn when the webhook is unhealthy and the watermark
		// has fallen behind, so users know polling is the only thing keeping
		// the mirror current.
		if provider.WebhookHealthy != nil && !*provider.WebhookHealthy && provider.LagSeconds > 60 {
			fmt.Fprintf(stdout, "    %s webhook unhealthy — falling back to periodic sync (lag %s)\n",
				provider.Provider, formatLag(provider.LagSeconds))
		}
	}
	if record.LocalDir != "" {
		fmt.Fprintf(stdout, "\nlocal mirror: %s\n", record.LocalDir)
		fmt.Fprintln(stdout, daemonStatusLine(record))
	}
	if snapshot.Bootstrap != nil {
		// Initial mirror in progress: show progress instead of a
		// misleading generic stall.
		line := fmt.Sprintf("\nbootstrapping: %d", snapshot.Bootstrap.FilesSynced)
		if snapshot.Bootstrap.FilesTotal > 0 {
			line += fmt.Sprintf("/%d", snapshot.Bootstrap.FilesTotal)
		}
		line += " files"
		if started := strings.TrimSpace(snapshot.Bootstrap.StartedAt); started != "" {
			line += " (started " + humanizeRecentTime(started) + ")"
		}
		fmt.Fprintln(stdout, line)
	} else if persistedStallReason != "" {
		fmt.Fprintf(stdout, "\nstall: %s\n", persistedStallReason)
	}
	fmt.Fprintf(stdout, "\npending writebacks: %d    conflicts: %d    denied: %d\n", snapshot.PendingWriteback, snapshot.PendingConflicts, snapshot.DeniedPaths)
	return nil
}

func readPersistedStallReason(localDir string) string {
	if localDir == "" {
		return ""
	}
	payload, err := os.ReadFile(filepath.Join(localDir, ".relay", "state.json"))
	if err != nil {
		return ""
	}
	var snapshot syncStateFile
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return ""
	}
	return strings.TrimSpace(snapshot.StallReason)
}

func statusAuthLine(localDir string, now time.Time) string {
	if line := daemonCredentialFreshnessAuthLine(localDir); line != "" {
		return line
	}
	return cloudCredentialAuthLine(now)
}

func daemonCredentialFreshnessAuthLine(localDir string) string {
	state, ok := readDaemonPIDState(localDir)
	if !ok || strings.TrimSpace(state.StartedAt) == "" {
		return ""
	}
	startedAt, ok := parseRFC3339(state.StartedAt)
	if !ok {
		return ""
	}
	latest, ok := latestCredentialModTime(credentialsPath(), agentRelayCloudAuthPath())
	if ok && latest.After(startedAt) {
		return "auth: daemon predates last login - restart the daemon"
	}
	return ""
}

func latestCredentialModTime(paths ...string) (time.Time, bool) {
	var latest time.Time
	found := false
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if !found || info.ModTime().After(latest) {
			latest = info.ModTime()
			found = true
		}
	}
	return latest, found
}

func cloudCredentialAuthLine(now time.Time) string {
	if _, err := cloudCredentialsFromAgentRelay(); err != nil {
		return "auth: agent-relay session unavailable - run 'agent-relay cloud login'"
	}
	return "auth: agent-relay session ok"
}

func formatAuthDuration(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}
	minutes := int((d + time.Minute - time.Nanosecond) / time.Minute)
	if minutes < 1 {
		return "<1m"
	}
	return fmt.Sprintf("%dm", minutes)
}

func isUnauthorizedAPIError(err error) bool {
	var apiErr *apiError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized
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
	return stopWorkspaceMountDaemons(record, stdout, false)
}

// runRestart stops a running daemon (if any) and starts a fresh background
// mount using the workspace's recorded localDir. This is the operationally
// correct way to recover from a stalled daemon: the new daemon's initial
// recursive walk re-subscribes to every directory in the mirror, which is
// what the watcher needs after a sync-down created new nested subtrees
// (see watcher.go addDirRecursive).
//
// Forwarded flags: any flag accepted by `mount` can be passed after the
// workspace name and is propagated to the start phase. The restart always
// runs in `--background` mode (the natural default for a long-running
// mount); pass `--foreground` to override and run attached.
func runRestart(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("restart", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	foreground := fs.Bool("foreground", false, "run the restarted mount in the foreground instead of detaching")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"foreground": false,
	})); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, "usage: relayfile restart [WORKSPACE] [--foreground]")
			return nil
		}
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: relayfile restart [WORKSPACE] [--foreground]")
	}

	record, err := resolveWorkspaceRecord(firstArg(fs))
	if err != nil {
		return err
	}
	// Reject workspaces that have no recorded local mirror — passing an
	// empty positional `localDir` to runMount would cause it to fall back
	// to `"."` and mirror into the caller's current working directory.
	localDir := strings.TrimSpace(record.LocalDir)
	if localDir == "" {
		return fmt.Errorf(
			"workspace %s has no recorded local mirror directory; run "+
				"`relayfile start %s <LOCAL_DIR>` first to register one",
			record.Name, record.Name,
		)
	}

	// Stop the current daemon if it's running. Tolerate "not running" so
	// `restart` works as a safe "ensure running" verb.
	if err := stopWorkspaceMountDaemons(record, stdout, true); err != nil {
		return err
	}

	// Start the new daemon. Default is --background; --foreground overrides.
	mountArgs := []string{record.Name, localDir}
	if !*foreground {
		mountArgs = append(mountArgs, "--background")
	}
	return runMount(mountArgs)
}

// isProcessAlreadyGone reports whether a signal-delivery failure means the
// target process has already exited. Treats both `os.ErrProcessDone` (the
// modern Go check) and `syscall.ESRCH` (raw signal failure) as "gone".
func isProcessAlreadyGone(err error) bool {
	return errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || !isProcessAlreadyGone(err)
}

// waitForDaemonExit polls the pid file and the process itself, returning
// nil once both have gone away. The pid-file check covers the clean-exit
// case (the daemon's `defer os.Remove(pidFile)` runs). The process check
// covers the case where the pid file lingers — for example if the daemon
// was SIGKILL'd by something else after we sent SIGTERM.
//
// Returns an error only if the deadline is reached with the daemon still
// alive AND the pid file still pointing at it. The default deadline (5s)
// is generous: a healthy daemon shuts down in <100ms on every platform.
func waitForDaemonExit(localDir string, oldPid int, timeout time.Duration) error {
	const pollInterval = 50 * time.Millisecond
	deadline := time.Now().Add(timeout)
	for {
		pidNow := readDaemonPID(localDir)
		// Pid file gone, or it's been claimed by a different pid (which
		// shouldn't happen mid-restart but is harmless if it does).
		if pidNow == 0 || pidNow != oldPid {
			return nil
		}
		// Process check: signal 0 reports liveness without delivering anything.
		if process, perr := os.FindProcess(oldPid); perr == nil {
			if serr := process.Signal(syscall.Signal(0)); serr != nil {
				// Signal 0 failed — process is gone even if the pid file lingered.
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon still alive after %s", timeout)
		}
		time.Sleep(pollInterval)
	}
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

	workspaceValue := ""
	if fs.NArg() == 1 {
		workspaceValue = strings.TrimSpace(fs.Arg(0))
	}
	commandClient, err := prepareWorkspaceCommandClient(workspaceValue, *server, *token, defaultInspectScopes)
	if err != nil {
		return err
	}
	launchURL, err := buildObserverURL(*observerURL, commandClient.client.baseURL, commandClient.client.token, commandClient.workspaceID)
	if err != nil {
		return err
	}
	if _, err := upsertWorkspaceDetails(commandClient.record); err != nil {
		return err
	}
	if *noOpen {
		fmt.Fprintln(stdout, launchURL)
		return nil
	}
	fmt.Fprintf(stdout, "Opening observer for workspace %s\n", commandClient.workspaceID)
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
		return nil, errors.New("token is required; set RELAYFILE_TOKEN or pass --token")
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

func (c *apiClient) deleteJSON(ctx context.Context, path string, ifMatch string, out any) error {
	body, _, err := c.doWithHeaders(ctx, http.MethodDelete, path, nil, map[string]string{
		"If-Match": ifMatch,
	})
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
	return c.doWithHeaders(ctx, method, path, body, nil)
}

func (c *apiClient) doWithHeaders(ctx context.Context, method, path string, body []byte, headers map[string]string) ([]byte, string, error) {
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
	for key, value := range headers {
		if strings.TrimSpace(value) != "" {
			req.Header.Set(key, value)
		}
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
		// Cloud's Next.js routes typically return `{ error, code }` rather
		// than `{ message, code }`. Accept either so commands that hit
		// either shape (e.g. adopt vs. join) produce consistent messages
		// instead of falling through to the raw JSON blob.
		Error string `json:"error"`
	}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &errPayload)
	}
	if errPayload.Message == "" {
		errPayload.Message = errPayload.Error
	}
	if errPayload.Message == "" {
		errPayload.Message = strings.TrimSpace(string(payload))
	}
	return nil, "", &apiError{
		StatusCode: resp.StatusCode,
		Code:       errPayload.Code,
		Message:    errPayload.Message,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
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

func relayIntegrationBindingsPath() string {
	return filepath.Join(configDir(), "bindings.json")
}

func readRelayIntegrationBindings() ([]relayIntegrationBinding, error) {
	payload, err := os.ReadFile(relayIntegrationBindingsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []relayIntegrationBinding{}, nil
		}
		return nil, err
	}
	var store relayIntegrationBindingStore
	if err := json.Unmarshal(payload, &store); err == nil && len(payload) > 0 && payload[0] == '{' {
		if store.Bindings == nil {
			return []relayIntegrationBinding{}, nil
		}
		return store.Bindings, nil
	}
	var bindings []relayIntegrationBinding
	if err := json.Unmarshal(payload, &bindings); err != nil {
		return nil, fmt.Errorf("parse relay integration bindings: %w", err)
	}
	return bindings, nil
}

func writeRelayIntegrationBindings(bindings []relayIntegrationBinding) error {
	sort.SliceStable(bindings, func(i, j int) bool {
		if bindings[i].Provider == bindings[j].Provider {
			return bindings[i].PathGlob < bindings[j].PathGlob
		}
		return bindings[i].Provider < bindings[j].Provider
	})
	payload, err := json.MarshalIndent(relayIntegrationBindingStore{Bindings: bindings}, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomically(relayIntegrationBindingsPath(), payload, 0o600)
}

func pathGlobMatchesAny(pathGlob string, candidates []string) bool {
	for _, candidate := range candidates {
		if pathGlob == candidate {
			return true
		}
	}
	return false
}

func fallbackUnbindPathGlobsForNativeResource(provider string) []string {
	switch provider {
	case "slack":
		return []string{"/slack/channels/**", "/slack/channels/*/**"}
	case "telegram":
		return []string{"/telegram/chats/**", "/telegram/chats/*/**"}
	case "linear":
		return []string{"/linear/teams/*"}
	default:
		return nil
	}
}

func delegatedCredentialsPath() string {
	return filepath.Join(configDir(), "delegated-credentials.json")
}

type integrationBindPathResolution struct {
	PathGlob string
	Warning  string
}

type integrationContainerResolver struct {
	Provider       string
	Root           string
	Container      string
	Aliases        []string
	StripSigils    string
	DirectoryGlob  bool
	DirectID       func(string) bool
	AdditionalKeys func(map[string]any) []string
}

func resolveIntegrationBindPathGlob(provider, resource string) (integrationBindPathResolution, error) {
	resource = strings.TrimSpace(resource)
	if resource == "" {
		return integrationBindPathResolution{}, errors.New("PATH_GLOB/resource is required")
	}
	if strings.HasPrefix(resource, "/") {
		return integrationBindPathResolution{PathGlob: resource}, nil
	}

	switch provider {
	case "github":
		return resolveGitHubBindPathGlob(resource)
	case "slack":
		return resolveContainerBindPathGlob(resource, integrationContainerResolver{
			Provider:      "slack",
			Root:          "/slack",
			Container:     "channels",
			Aliases:       []string{"by-name"},
			StripSigils:   "#",
			DirectoryGlob: true,
			DirectID: func(value string) bool {
				return len(value) >= 6 && strings.IndexFunc(value, func(r rune) bool {
					return !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
				}) < 0
			},
		})
	case "telegram":
		return resolveContainerBindPathGlob(resource, integrationContainerResolver{
			Provider:      "telegram",
			Root:          "/telegram",
			Container:     "chats",
			Aliases:       []string{"by-title", "by-username"},
			StripSigils:   "@",
			DirectoryGlob: true,
			DirectID: func(value string) bool {
				trimmed := strings.TrimPrefix(value, "-")
				return trimmed != "" && strings.IndexFunc(trimmed, func(r rune) bool {
					return r < '0' || r > '9'
				}) < 0
			},
		})
	case "linear":
		return resolveContainerBindPathGlob(resource, integrationContainerResolver{
			Provider:      "linear",
			Root:          "/linear",
			Container:     "teams",
			Aliases:       []string{"by-name"},
			StripSigils:   "",
			DirectoryGlob: false,
			DirectID: func(value string) bool {
				return isUUIDLike(value) || strings.HasPrefix(strings.ToLower(value), "team")
			},
			AdditionalKeys: func(row map[string]any) []string {
				return []string{stringField(row, "key"), stringField(row, "team_key")}
			},
		})
	default:
		return integrationBindPathResolution{}, fmt.Errorf("PATH_GLOB must start with /; provider %q has no native resource resolver", provider)
	}
}

func resolveGitHubBindPathGlob(resource string) (integrationBindPathResolution, error) {
	parts := strings.Split(strings.Trim(resource, "/"), "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return integrationBindPathResolution{}, errors.New("github resource must be owner/repo or an explicit /-prefixed path glob")
	}
	return integrationBindPathResolution{
		PathGlob: path.Join("/github/repos", url.PathEscape(parts[0]), url.PathEscape(parts[1]), "**"),
	}, nil
}

func resolveContainerBindPathGlob(resource string, cfg integrationContainerResolver) (integrationBindPathResolution, error) {
	native := strings.TrimSpace(resource)
	lookup := strings.Trim(native, " \t\r\n")
	if cfg.StripSigils != "" {
		lookup = strings.TrimLeft(lookup, cfg.StripSigils)
	}
	if lookup == "" {
		return integrationBindPathResolution{}, fmt.Errorf("%s resource is empty after trimming sigils", cfg.Provider)
	}

	if cfg.DirectID != nil && cfg.DirectID(lookup) {
		if canonical, ok := resolveContainerResourceByID(cfg, lookup); ok {
			return integrationBindPathResolution{PathGlob: canonicalPathToBindGlob(canonical, cfg)}, nil
		}
		if cfg.DirectoryGlob {
			return integrationBindPathResolution{
				PathGlob: path.Join(cfg.Root, cfg.Container, encodeVFSPathSegment(lookup), "**"),
				Warning: fmt.Sprintf("could not find %s resource %q in the active mount; bound %s and it may miss id-slug paths until the mount index is available",
					cfg.Provider, native, path.Join(cfg.Root, cfg.Container, encodeVFSPathSegment(lookup), "**")),
			}, nil
		}
		return integrationBindPathResolution{PathGlob: path.Join(cfg.Root, cfg.Container, encodeVFSPathSegment(lookup)+".json")}, nil
	}

	if canonical, ok := resolveContainerResourceByAliasOrIndex(cfg, lookup); ok {
		return integrationBindPathResolution{PathGlob: canonicalPathToBindGlob(canonical, cfg)}, nil
	}

	fallback := unresolvedContainerFallbackGlob(cfg)
	return integrationBindPathResolution{
		PathGlob: fallback,
		Warning: fmt.Sprintf("could not resolve %s resource %q from the active mount; bound fallback glob %s, which may match more than the requested resource",
			cfg.Provider, native, fallback),
	}, nil
}

func resolveContainerResourceByID(cfg integrationContainerResolver, id string) (string, bool) {
	if canonical, ok := findContainerIndexCanonical(cfg, func(row map[string]any) bool {
		return stringEqualFold(stringField(row, "id"), id) || stringEqualFold(stringField(row, "objectId"), id)
	}); ok {
		return canonical, true
	}
	return findMountedContainerEntry(cfg, id)
}

func resolveContainerResourceByAliasOrIndex(cfg integrationContainerResolver, name string) (string, bool) {
	slug := slugifyNativeResource(name)
	for _, alias := range cfg.Aliases {
		aliasDir := path.Join(cfg.Root, cfg.Container, alias)
		if canonical, ok := readAliasCanonicalPath(path.Join(aliasDir, slug+".json")); ok {
			return canonical, true
		}
		if canonical, ok := scanAliasCanonicalPath(aliasDir, slug); ok {
			return canonical, true
		}
		if alias == "by-username" {
			if canonical, ok := readAliasCanonicalPath(path.Join(aliasDir, strings.TrimPrefix(slug, "@")+".json")); ok {
				return canonical, true
			}
		}
	}
	if canonical, ok := findContainerIndexCanonical(cfg, func(row map[string]any) bool {
		keys := []string{
			stringField(row, "title"),
			stringField(row, "name"),
			stringField(row, "username"),
			stringField(row, "id"),
		}
		if cfg.AdditionalKeys != nil {
			keys = append(keys, cfg.AdditionalKeys(row)...)
		}
		for _, key := range keys {
			if key == "" {
				continue
			}
			if stringEqualFold(key, name) || slugifyNativeResource(key) == slug {
				return true
			}
		}
		return false
	}); ok {
		return canonical, true
	}
	if cfg.Provider == "linear" {
		return scanLinearTeamFiles(name)
	}
	return "", false
}

func scanAliasCanonicalPath(aliasDirRemotePath, slug string) (string, bool) {
	record, ok := activeWorkspaceRecordForMountResolution()
	if !ok || strings.TrimSpace(record.LocalDir) == "" {
		return "", false
	}
	aliasDir := filepath.Join(record.LocalDir, filepath.FromSlash(strings.TrimPrefix(aliasDirRemotePath, "/")))
	entries, err := os.ReadDir(aliasDir)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".json") && (name == slug+".json" || strings.HasPrefix(name, slug+"__")) {
			if canonical, ok := readAliasCanonicalPath(path.Join(aliasDirRemotePath, name)); ok {
				return canonical, true
			}
		}
	}
	return "", false
}

func findContainerIndexCanonical(cfg integrationContainerResolver, match func(map[string]any) bool) (string, bool) {
	rows, ok := readIndexRows(path.Join(cfg.Root, cfg.Container, "_index.json"))
	if !ok {
		return "", false
	}
	for _, row := range rows {
		if !match(row) {
			continue
		}
		if canonical := stringField(row, "canonicalPath"); canonical != "" {
			return canonical, true
		}
		if p := stringField(row, "path"); p != "" {
			return p, true
		}
		if id := stringField(row, "id"); id != "" {
			return findMountedContainerEntry(cfg, id)
		}
	}
	return "", false
}

func readAliasCanonicalPath(remotePath string) (string, bool) {
	var payload map[string]any
	if !readMountedJSON(remotePath, &payload) {
		return "", false
	}
	for _, key := range []string{"canonicalPath", "path", "targetPath"} {
		if value := stringField(payload, key); value != "" {
			return value, true
		}
	}
	if id := stringField(payload, "id"); id != "" {
		if strings.Contains(remotePath, "/slack/channels/") {
			return findMountedContainerEntry(integrationContainerResolver{Root: "/slack", Container: "channels", DirectoryGlob: true}, id)
		}
		if strings.Contains(remotePath, "/telegram/chats/") {
			return findMountedContainerEntry(integrationContainerResolver{Root: "/telegram", Container: "chats", DirectoryGlob: true}, id)
		}
		if strings.Contains(remotePath, "/linear/teams/") {
			return path.Join("/linear/teams", encodeVFSPathSegment(id)+".json"), true
		}
	}
	return "", false
}

func readIndexRows(remotePath string) ([]map[string]any, bool) {
	var raw any
	if !readMountedJSON(remotePath, &raw) {
		return nil, false
	}
	switch value := raw.(type) {
	case []any:
		return mapsFromArray(value), true
	case map[string]any:
		if rows, ok := value["rows"].([]any); ok {
			return mapsFromArray(rows), true
		}
	}
	return nil, false
}

func mapsFromArray(values []any) []map[string]any {
	rows := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if row, ok := value.(map[string]any); ok {
			rows = append(rows, row)
		}
	}
	return rows
}

func findMountedContainerEntry(cfg integrationContainerResolver, id string) (string, bool) {
	record, ok := activeWorkspaceRecordForMountResolution()
	if !ok || strings.TrimSpace(record.LocalDir) == "" {
		return "", false
	}
	dir := filepath.Join(record.LocalDir, filepath.FromSlash(strings.TrimPrefix(path.Join(cfg.Root, cfg.Container), "/")))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	encodedID := encodeVFSPathSegment(id)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "by-") || name == "_index.json" {
			continue
		}
		if cfg.DirectoryGlob {
			if entry.IsDir() && (name == encodedID || strings.HasPrefix(name, encodedID+"__")) {
				return path.Join(cfg.Root, cfg.Container, name), true
			}
			continue
		}
		if !entry.IsDir() && name == encodedID+".json" {
			return path.Join(cfg.Root, cfg.Container, name), true
		}
	}
	return "", false
}

func scanLinearTeamFiles(resource string) (string, bool) {
	record, ok := activeWorkspaceRecordForMountResolution()
	if !ok || strings.TrimSpace(record.LocalDir) == "" {
		return "", false
	}
	dir := filepath.Join(record.LocalDir, "linear", "teams")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	slug := slugifyNativeResource(resource)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, "by-") || !strings.HasSuffix(name, ".json") || name == "_index.json" {
			continue
		}
		var payload map[string]any
		if !readMountedJSON(path.Join("/linear/teams", name), &payload) {
			continue
		}
		recordPayload, _ := payload["payload"].(map[string]any)
		keys := []string{
			stringField(payload, "id"),
			stringField(payload, "key"),
			stringField(payload, "name"),
			stringField(recordPayload, "id"),
			stringField(recordPayload, "key"),
			stringField(recordPayload, "name"),
		}
		for _, key := range keys {
			if key != "" && (stringEqualFold(key, resource) || slugifyNativeResource(key) == slug) {
				return path.Join("/linear/teams", name), true
			}
		}
	}
	return "", false
}

func canonicalPathToBindGlob(canonical string, cfg integrationContainerResolver) string {
	clean := "/" + strings.TrimLeft(strings.TrimSpace(canonical), "/")
	clean = path.Clean(clean)
	if cfg.DirectoryGlob {
		parts := strings.Split(strings.Trim(clean, "/"), "/")
		if len(parts) >= 3 && "/"+parts[0] == cfg.Root && parts[1] == cfg.Container {
			return path.Join(cfg.Root, cfg.Container, parts[2], "**")
		}
		if strings.HasSuffix(clean, "/meta.json") {
			return path.Join(path.Dir(clean), "**")
		}
		return path.Join(clean, "**")
	}
	return clean
}

func unresolvedContainerFallbackGlob(cfg integrationContainerResolver) string {
	if cfg.DirectoryGlob {
		return path.Join(cfg.Root, cfg.Container, "**")
	}
	return path.Join(cfg.Root, cfg.Container, "*")
}

func readMountedJSON(remotePath string, out any) bool {
	record, ok := activeWorkspaceRecordForMountResolution()
	if !ok || strings.TrimSpace(record.LocalDir) == "" {
		return false
	}
	localPath := filepath.Join(record.LocalDir, filepath.FromSlash(strings.TrimPrefix(remotePath, "/")))
	payload, err := os.ReadFile(localPath)
	if err != nil {
		return false
	}
	return json.Unmarshal(payload, out) == nil
}

var (
	activeWorkspaceRecordCache     workspaceRecord
	activeWorkspaceRecordCacheOK   bool
	activeWorkspaceRecordCacheOnce sync.Once
)

func activeWorkspaceRecordForMountResolution() (workspaceRecord, bool) {
	activeWorkspaceRecordCacheOnce.Do(func() {
		creds, _ := loadCredentials()
		name, _ := activeWorkspaceName(resolveToken("", creds))
		if strings.TrimSpace(name) == "" {
			return
		}
		if record, ok := workspaceRecordByName(name); ok {
			activeWorkspaceRecordCache = record
			activeWorkspaceRecordCacheOK = true
			return
		}
		if record, ok := workspaceRecordByID(name); ok {
			activeWorkspaceRecordCache = record
			activeWorkspaceRecordCacheOK = true
			return
		}
	})
	return activeWorkspaceRecordCache, activeWorkspaceRecordCacheOK
}

func resetActiveWorkspaceRecordForMountResolutionCache() {
	activeWorkspaceRecordCache = workspaceRecord{}
	activeWorkspaceRecordCacheOK = false
	activeWorkspaceRecordCacheOnce = sync.Once{}
}

func stringField(record map[string]any, key string) string {
	if record == nil {
		return ""
	}
	value, ok := record[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func stringEqualFold(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func slugifyNativeResource(value string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func encodeVFSPathSegment(value string) string {
	return url.PathEscape(strings.TrimSpace(value))
}

func isUUIDLike(value string) bool {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) == 36 {
		for i := 0; i < len(trimmed); i++ {
			r := rune(trimmed[i])
			if i == 8 || i == 13 || i == 18 || i == 23 {
				if r != '-' {
					return false
				}
				continue
			}
			if !isHexRune(r) {
				return false
			}
		}
		return true
	}
	if len(trimmed) == 32 {
		for i := 0; i < len(trimmed); i++ {
			r := rune(trimmed[i])
			if !isHexRune(r) {
				return false
			}
		}
		return true
	}
	return false
}

func isHexRune(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'
}

func delegatedCredentialsPathForRequest(workspaceValue string, scopes []string) string {
	workspaceKey := workspaceShardKey(workspaceValue)
	scopeKey := delegatedCredentialsScopeKey(scopes)
	return filepath.Join(configDir(), "delegated", workspaceKey, scopeKey+".json")
}

func delegatedCredentialsRawPathForRequest(workspaceValue string, scopes []string) string {
	workspaceKey := rawWorkspaceShardKey(workspaceValue)
	scopeKey := delegatedCredentialsScopeKey(scopes)
	return filepath.Join(configDir(), "delegated", workspaceKey, scopeKey+".json")
}

func workspaceShardKey(workspaceValue string) string {
	workspaceValue = canonicalWorkspaceShardValue(workspaceValue)
	sum := sha256.Sum256([]byte(workspaceValue))
	return hex.EncodeToString(sum[:])[:24]
}

func rawWorkspaceShardKey(workspaceValue string) string {
	workspaceValue = strings.TrimSpace(workspaceValue)
	if workspaceValue == "" {
		workspaceValue = "active"
	}
	sum := sha256.Sum256([]byte(workspaceValue))
	return hex.EncodeToString(sum[:])[:24]
}

func canonicalWorkspaceShardValue(workspaceValue string) string {
	value, _ := canonicalWorkspaceShardValueStatus(workspaceValue)
	return value
}

func canonicalWorkspaceShardValueStatus(workspaceValue string) (string, bool) {
	workspaceValue = strings.TrimSpace(workspaceValue)
	if workspaceValue == "" || strings.EqualFold(workspaceValue, "active") {
		if name, _ := activeWorkspaceName(""); strings.TrimSpace(name) != "" {
			workspaceValue = strings.TrimSpace(name)
		} else if workspaceValue == "" {
			workspaceValue = "active"
		}
	}
	if record, ok := workspaceRecordForShardValue(workspaceValue); ok {
		return strings.TrimSpace(record.ID), true
	}
	return workspaceValue, false
}

func workspaceRecordForShardValue(workspaceValue string) (workspaceRecord, bool) {
	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return workspaceRecord{}, false
	}
	workspaceValue = strings.TrimSpace(workspaceValue)
	var match workspaceRecord
	canonicalID := ""
	for _, record := range catalog.Workspaces {
		if record.Name != workspaceValue && record.ID != workspaceValue && record.RelayWorkspaceID != workspaceValue {
			continue
		}
		recordID := strings.TrimSpace(record.ID)
		if recordID == "" {
			return workspaceRecord{}, false
		}
		if canonicalID == "" {
			canonicalID = recordID
			match = record
			continue
		}
		if canonicalID != recordID {
			return workspaceRecord{}, false
		}
		if strings.TrimSpace(match.Name) == "" {
			match.Name = strings.TrimSpace(record.Name)
		}
		if strings.TrimSpace(match.RelayWorkspaceID) == "" {
			match.RelayWorkspaceID = strings.TrimSpace(record.RelayWorkspaceID)
		}
	}
	if canonicalID == "" {
		return workspaceRecord{}, false
	}
	match.ID = canonicalID
	return match, true
}

func delegatedCredentialsScopeKey(scopes []string) string {
	normalized := normalizedScopeSet(scopes)
	if len(normalized) == 0 {
		normalized = normalizedScopeSet(defaultJoinScopes)
	}
	sum := sha256.Sum256([]byte(strings.Join(normalized, "\n")))
	return hex.EncodeToString(sum[:])[:24]
}

func normalizedScopeSet(scopes []string) []string {
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		normalized = append(normalized, scope)
	}
	sort.Strings(normalized)
	return normalized
}

func explicitDelegatedCredentialsPath(flagValue string) (string, bool) {
	if value := strings.TrimSpace(flagValue); value != "" {
		return value, true
	}
	if value := strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_CREDS_FILE")); value != "" {
		return value, true
	}
	if value := strings.TrimSpace(os.Getenv("RELAYFILE_DELEGATED_CREDENTIALS_FILE")); value != "" {
		return value, true
	}
	return "", false
}

func resolveDelegatedCredentialsPath(flagValue string) string {
	if value, ok := explicitDelegatedCredentialsPath(flagValue); ok {
		return value
	}
	return delegatedCredentialsPath()
}

func resolveDelegatedCredentialsPathForRequest(flagValue, workspaceValue string, scopes []string) (string, bool) {
	if value, ok := explicitDelegatedCredentialsPath(flagValue); ok {
		return value, true
	}
	if strings.TrimSpace(workspaceValue) != "" || len(scopes) > 0 {
		return delegatedCredentialsPathForRequest(workspaceValue, scopes), false
	}
	return delegatedCredentialsPath(), false
}

func loadDelegatedCredentials(path string) (delegatedauth.Bundle, string, error) {
	resolvedPath := resolveDelegatedCredentialsPath(path)
	bundle, err := delegatedauth.Load(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return delegatedauth.Bundle{}, resolvedPath, fmt.Errorf("%w: %s not found", delegatedauth.ErrMissingCredentials, resolvedPath)
		}
		return delegatedauth.Bundle{}, resolvedPath, err
	}
	if err := bundle.ValidateForUse(); err != nil {
		return delegatedauth.Bundle{}, resolvedPath, err
	}
	return bundle, resolvedPath, nil
}

func loadDelegatedCredentialsForRequest(path, workspaceValue string, scopes []string) (delegatedauth.Bundle, string, error) {
	canonicalWorkspaceValue, canonicalWorkspaceOK := canonicalWorkspaceShardValueStatus(workspaceValue)
	if !canonicalWorkspaceOK && strings.TrimSpace(workspaceValue) != "" {
		fmt.Fprintf(os.Stderr, "warning: delegated credential workspace %q was not uniquely resolved in %s; using raw workspace shard and probing alias shards\n", canonicalWorkspaceValue, workspacesPath())
	}
	resolvedPath, explicit := resolveDelegatedCredentialsPathForRequest(path, workspaceValue, scopes)
	bundle, loadedPath, err := loadDelegatedCredentials(resolvedPath)
	if err == nil || explicit || resolvedPath == delegatedCredentialsPath() || !errors.Is(err, delegatedauth.ErrMissingCredentials) {
		return bundle, loadedPath, err
	}
	for _, legacyPath := range legacyDelegatedCredentialsPathsForRequest(workspaceValue, scopes) {
		legacyBundle, loadedLegacyPath, legacyErr := loadDelegatedCredentials(legacyPath)
		if legacyErr == nil && delegatedBundleSatisfiesRequestedScopes(legacyBundle, scopes) {
			if created, migrateErr := delegatedauth.SaveAtomicIfMissing(resolvedPath, legacyBundle); migrateErr == nil && created {
				return legacyBundle, resolvedPath, nil
			} else if migrateErr == nil && !created {
				canonicalBundle, canonicalLoadedPath, canonicalErr := loadDelegatedCredentials(resolvedPath)
				if canonicalErr == nil && delegatedBundleSatisfiesRequestedScopes(canonicalBundle, scopes) {
					return canonicalBundle, canonicalLoadedPath, nil
				}
			}
			return legacyBundle, loadedLegacyPath, nil
		}
	}
	legacyBundle, legacyPath, legacyErr := loadDelegatedCredentials(delegatedCredentialsPath())
	if legacyErr == nil && delegatedBundleSatisfiesRequestedScopes(legacyBundle, scopes) {
		return legacyBundle, legacyPath, nil
	}
	return bundle, loadedPath, err
}

func legacyDelegatedCredentialsPathsForRequest(workspaceValue string, scopes []string) []string {
	aliases := legacyDelegatedCredentialsWorkspaceAliases(workspaceValue)
	paths := make([]string, 0, len(aliases))
	seen := map[string]struct{}{delegatedCredentialsPathForRequest(workspaceValue, scopes): {}}
	for _, alias := range aliases {
		path := delegatedCredentialsRawPathForRequest(alias, scopes)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths
}

func legacyDelegatedCredentialsWorkspaceAliases(workspaceValue string) []string {
	value := strings.TrimSpace(workspaceValue)
	if value == "" || strings.EqualFold(value, "active") {
		if name, _ := activeWorkspaceName(""); strings.TrimSpace(name) != "" {
			value = strings.TrimSpace(name)
		}
	}
	aliases := make([]string, 0, 3)
	addAlias := func(alias string) {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			return
		}
		for _, existing := range aliases {
			if existing == alias {
				return
			}
		}
		aliases = append(aliases, alias)
	}
	addAlias(value)
	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return aliases
	}
	for _, record := range catalog.Workspaces {
		if strings.TrimSpace(record.Name) != value &&
			strings.TrimSpace(record.ID) != value &&
			strings.TrimSpace(record.RelayWorkspaceID) != value {
			continue
		}
		addAlias(record.ID)
		addAlias(record.RelayWorkspaceID)
		addAlias(record.Name)
	}
	return aliases
}

func delegatedBundleSatisfiesRequestedScopes(bundle delegatedauth.Bundle, requested []string) bool {
	requested = normalizedScopeSet(requested)
	if len(requested) == 0 {
		return true
	}
	available := delegatedBundleAvailableScopes(bundle)
	if len(available) == 0 {
		return false
	}
	for _, want := range requested {
		if !scopeSetAllows(available, want) {
			return false
		}
	}
	return true
}

func delegatedBundleAvailableScopes(bundle delegatedauth.Bundle) []string {
	available := append(append([]string(nil), bundle.Scopes...), bundle.RelayfileScopes...)
	if claims, ok := parseJWTClaims(bundle.BearerToken()); ok {
		if raw, ok := claims["scopes"]; ok {
			available = append(available, normalizeScopeClaim(raw)...)
		} else if raw, ok := claims["scope"]; ok {
			available = append(available, normalizeScopeClaim(raw)...)
		}
	}
	return normalizedScopeSet(available)
}

func normalizeScopeClaim(raw any) []string {
	var scopes []string
	add := func(scope string) {
		scope = strings.TrimSpace(scope)
		if scope != "" {
			scopes = append(scopes, scope)
		}
	}
	switch value := raw.(type) {
	case []any:
		for _, item := range value {
			if scope, ok := item.(string); ok {
				add(scope)
			}
		}
	case []string:
		for _, scope := range value {
			add(scope)
		}
	case string:
		for _, scope := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ' ' || r == ',' || r == '\t' || r == '\n' || r == '\r'
		}) {
			add(scope)
		}
	}
	return scopes
}

func scopeSetAllows(available []string, requested string) bool {
	for _, grant := range available {
		if scopeAllows(grant, requested) {
			return true
		}
	}
	return false
}

func scopeAllows(grant, requested string) bool {
	grant = strings.TrimSpace(grant)
	requested = strings.TrimSpace(requested)
	if grant == "" || requested == "" {
		return false
	}
	if grant == requested {
		return true
	}
	grant = strings.TrimPrefix(grant, "relayfile:")
	requested = strings.TrimPrefix(requested, "relayfile:")
	if grant == requested {
		return true
	}
	grantParts := strings.Split(grant, ":")
	requestParts := strings.Split(requested, ":")
	if len(grantParts) < 2 || len(requestParts) < 2 {
		return false
	}
	if grantParts[0] != requestParts[0] || grantParts[1] != requestParts[1] {
		return false
	}
	grantPath := ""
	if len(grantParts) > 2 {
		grantPath = strings.Join(grantParts[2:], ":")
	}
	requestPath := ""
	if len(requestParts) > 2 {
		requestPath = strings.Join(requestParts[2:], ":")
	}
	if grantPath == "" || grantPath == "*" || grantPath == "/*" || grantPath == "/**" {
		return true
	}
	return grantPath == requestPath
}

func refreshDelegatedCredentials(path string, bundle delegatedauth.Bundle, force bool) (delegatedauth.Bundle, error) {
	resolvedPath := resolveDelegatedCredentialsPath(path)
	if !force && !relayfileTokenNeedsRefresh(bundle.BearerToken()) {
		return bundle, nil
	}
	var renewed delegatedauth.Bundle
	attemptedToken := strings.TrimSpace(bundle.BearerToken())
	if err := withDelegatedCredentialsLock(resolvedPath, func() error {
		latest, loadErr := delegatedauth.Load(resolvedPath)
		if loadErr == nil {
			bundle = latest
		} else if !errors.Is(loadErr, os.ErrNotExist) {
			return loadErr
		}
		latestToken := strings.TrimSpace(bundle.BearerToken())
		if latestToken != "" && latestToken != attemptedToken && !relayfileTokenNeedsRefresh(latestToken) {
			renewed = bundle
			return nil
		}
		if !force && !relayfileTokenNeedsRefresh(bundle.BearerToken()) {
			renewed = bundle
			return nil
		}
		refreshed, changed, err := delegatedauth.RenewFile(context.Background(), nil, resolvedPath, delegatedauth.DefaultRefreshTimeout)
		if err != nil {
			reminted, remintErr := remintDelegatedCredentialsFromCloud(resolvedPath, bundle)
			if remintErr == nil {
				renewed = reminted
				return nil
			}
			if errors.Is(err, delegatedauth.ErrRefreshRejected) {
				return fmt.Errorf("%w; cloud re-mint fallback failed: %v", ErrDelegatedRelayfileCredentialsExpired, remintErr)
			}
			return fmt.Errorf("%w; cloud re-mint fallback failed: %v", err, remintErr)
		}
		if !changed {
			renewed = bundle
			return nil
		}
		renewed = refreshed
		return nil
	}); err != nil {
		return bundle, err
	}
	if err := renewed.ValidateForUse(); err != nil {
		return bundle, err
	}
	return renewed, nil
}

func withDelegatedCredentialsLock(path string, fn func() error) error {
	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	unlock, err := lockFileExclusive(file)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

func remintDelegatedCredentialsFromCloud(path string, bundle delegatedauth.Bundle) (delegatedauth.Bundle, error) {
	workspaceID := cloudWorkspaceIDForDelegatedBundle(bundle)
	if workspaceID == "" {
		return delegatedauth.Bundle{}, errors.New("delegated relayfile credentials missing workspace id")
	}
	scopes := delegatedBundleMintScopes(bundle)
	cloudCreds, err := cloudCredentialsFromAgentRelay()
	if err != nil {
		return delegatedauth.Bundle{}, err
	}
	renewed, err := delegatedRelayfileTokenViaCloud(cloudCreds, workspaceID, bundle.AgentName, scopes)
	if err != nil {
		return delegatedauth.Bundle{}, err
	}
	if err := delegatedauth.SaveAtomic(path, renewed); err != nil {
		return delegatedauth.Bundle{}, fmt.Errorf("persist re-minted delegated relayfile credentials: %w", err)
	}
	return renewed, nil
}

func cloudWorkspaceIDForDelegatedBundle(bundle delegatedauth.Bundle) string {
	if workspaceID := strings.TrimSpace(bundle.WorkspaceID); workspaceID != "" {
		return workspaceID
	}
	relayWorkspaceID := strings.TrimSpace(bundle.Workspace())
	if relayWorkspaceID == "" {
		return ""
	}
	if record, ok := workspaceRecordByID(relayWorkspaceID); ok && strings.TrimSpace(record.ID) != "" {
		return record.ID
	}
	if record, ok := workspaceRecordByName(relayWorkspaceID); ok && strings.TrimSpace(record.ID) != "" {
		return record.ID
	}
	catalog, err := loadWorkspaceCatalog()
	if err == nil {
		for _, record := range catalog.Workspaces {
			if strings.TrimSpace(record.RelayWorkspaceID) == relayWorkspaceID && strings.TrimSpace(record.ID) != "" {
				return strings.TrimSpace(record.ID)
			}
		}
	}
	return relayWorkspaceID
}

func delegatedBundleMintScopes(bundle delegatedauth.Bundle) []string {
	if len(bundle.Scopes) > 0 {
		return append([]string(nil), bundle.Scopes...)
	}
	if len(bundle.RelayfileScopes) > 0 {
		return append([]string(nil), bundle.RelayfileScopes...)
	}
	return append([]string(nil), defaultJoinScopes...)
}

func agentRelayCloudAuthPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".agentworkforce", "relay", "cloud-auth.json")
	}
	return filepath.Join(home, ".agentworkforce", "relay", "cloud-auth.json")
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

func removeCredentialFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func removeCredentialFileIfExists(path string) (bool, error) {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func removeCredentialDirIfExists(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if err := os.RemoveAll(path); err != nil {
		return false, err
	}
	return true, nil
}

func clearAuthCredentials() (int, error) {
	removed := 0
	for _, path := range []string{
		credentialsPath(),
		cloudCredentialsPath(),
		delegatedCredentialsPath(),
	} {
		ok, err := removeCredentialFileIfExists(path)
		if err != nil {
			return removed, fmt.Errorf("remove %s: %w", path, err)
		}
		if ok {
			removed++
		}
	}
	ok, err := removeCredentialDirIfExists(filepath.Join(configDir(), "delegated"))
	if err != nil {
		return removed, fmt.Errorf("remove delegated credential cache: %w", err)
	}
	if ok {
		removed++
	}
	return removed, nil
}

// saveLegacyCloudCredentials is retained for stale-store cleanup tests only.
// Relayfile no longer owns the canonical cloud session.
func saveLegacyCloudCredentials(creds cloudCredentials) error {
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
			return creds, fmt.Errorf("credentials not found at %s; run relayfile login --api-key for self-hosted credentials or pass --token", credentialsPath())
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
	record.Timezone = strings.TrimSpace(record.Timezone)
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
	if update.RelayWorkspaceID != "" {
		merged.RelayWorkspaceID = update.RelayWorkspaceID
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
	if update.Timezone != "" {
		merged.Timezone = update.Timezone
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
		if record.ID == id || record.RelayWorkspaceID == id {
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

	if workspace, err := activeWorkspaceFromAgentRelay(); err == nil {
		return strings.TrimSpace(workspace.RelayfileWorkspaceID), nil
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
	return "", errors.New("workspace is required; pass WORKSPACE, set RELAYFILE_WORKSPACE, or run 'agent-relay workspace switch NAME'")
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

func fetchWorkspaceSyncStatus(client *apiClient, workspaceID string) (syncStatusResponse, error) {
	var status syncStatusResponse
	if err := client.getJSON(context.Background(), fmt.Sprintf("/v1/workspaces/%s/sync/status", url.PathEscape(workspaceID)), &status); err != nil {
		return syncStatusResponse{}, err
	}
	return status, nil
}

func fetchWorkspaceSyncIngressStatus(client *apiClient, workspaceID string) (syncIngressStatusResponse, error) {
	var status syncIngressStatusResponse
	if err := client.getJSON(context.Background(), fmt.Sprintf("/v1/workspaces/%s/sync/ingress", url.PathEscape(workspaceID)), &status); err != nil {
		return syncIngressStatusResponse{}, err
	}
	return status, nil
}

func statusNeedsIngressDiagnostics(status syncStatusResponse) bool {
	for _, provider := range status.Providers {
		if provider.Status == "lagging" && provider.LagSeconds == 0 && !hasNonEmptyString(provider.Cursor) && !hasNonEmptyString(provider.WatermarkTs) {
			return true
		}
	}
	return false
}

func syncProviderLagReason(provider syncProviderStatus, ingress *syncIngressStatusResponse) string {
	if provider.Status != "lagging" || provider.LagSeconds != 0 || hasNonEmptyString(provider.Cursor) || hasNonEmptyString(provider.WatermarkTs) {
		return ""
	}
	if ingress == nil {
		return "no sync cursor or watermark reported"
	}
	providerIngress, ok := ingressProviderStatusFor(ingress, provider.Provider)
	if !ok {
		if observed := unattributedIngressObservedTotal(ingress); observed > 0 {
			return fmt.Sprintf("no sync cursor or watermark; %d workspace ingress event(s) observed without provider breakdown", observed)
		}
		return "no sync cursor or watermark; no provider-specific ingress events recorded"
	}
	if providerIngress.PendingTotal > 0 {
		return fmt.Sprintf("%d pending ingress event(s), oldest %s", providerIngress.PendingTotal, formatLag(providerIngress.OldestPendingAgeSeconds))
	}
	if providerIngress.AcceptedTotal > 0 {
		return fmt.Sprintf("%d ingress event(s) accepted but no cursor or watermark reported", providerIngress.AcceptedTotal)
	}
	if observed := ingressProviderObservedTotal(providerIngress); observed > 0 {
		return fmt.Sprintf("%d ingress event(s) observed but no cursor or watermark reported", observed)
	}
	return "no sync cursor or watermark; no provider-specific ingress events recorded"
}

func hasNonEmptyString(value *string) bool {
	return value != nil && strings.TrimSpace(*value) != ""
}

func ingressProviderStatusFor(ingress *syncIngressStatusResponse, provider string) (syncIngressProviderStatus, bool) {
	if ingress == nil || len(ingress.IngressByProvider) == 0 {
		return syncIngressProviderStatus{}, false
	}
	if status, ok := ingress.IngressByProvider[provider]; ok {
		return status, true
	}
	normalizedProvider := normalizeProviderID(provider)
	if status, ok := ingress.IngressByProvider[normalizedProvider]; ok {
		return status, true
	}
	for key, status := range ingress.IngressByProvider {
		if normalizeProviderID(key) == normalizedProvider {
			return status, true
		}
	}
	return syncIngressProviderStatus{}, false
}

func ingressProviderObservedTotal(status syncIngressProviderStatus) int {
	return status.DroppedTotal + status.DedupedTotal + status.CoalescedTotal + status.SuppressedTotal + status.StaleTotal
}

func unattributedIngressObservedTotal(ingress *syncIngressStatusResponse) int {
	if ingress == nil {
		return 0
	}
	total := ingress.DroppedTotal + ingress.DedupedTotal + ingress.CoalescedTotal + ingress.SuppressedTotal + ingress.StaleTotal
	for _, provider := range ingress.IngressByProvider {
		total -= ingressProviderObservedTotal(provider)
	}
	if total < 0 {
		return 0
	}
	return total
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
		RemoteRoot:       readMountRemoteRoot(localDir),
		Mode:             defaultIfBlank(mode, defaultMountMode),
		IntervalMs:       interval.Milliseconds(),
		PendingWriteback: countDirtyTrackedFiles(localDir),
		PendingConflicts: countFilesInDir(filepath.Join(localDir, ".relay", "conflicts")),
		DeniedPaths:      countLines(filepath.Join(localDir, ".relay", "permissions-denied.log")),
		FailedWritebacks: readPersistedFailedWritebacks(localDir),
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
	snapshot.Guards = readGuardCounters(localDir)
	snapshot.Bootstrap = readBootstrapStatus(localDir)
	return snapshot
}

// readBootstrapStatus reads the bootstrap progress block from the
// mountsync public state file under .relay/state.json. Returns nil if the
// file is missing, unparseable, or there is no bootstrap in progress.
// Purely additive status, never load-bearing.
func readBootstrapStatus(localDir string) *syncStateBootstrap {
	if localDir == "" {
		return nil
	}
	payload, err := os.ReadFile(filepath.Join(localDir, ".relay", "state.json"))
	if err != nil {
		return nil
	}
	var view struct {
		Bootstrap *syncStateBootstrap `json:"bootstrap"`
	}
	if err := json.Unmarshal(payload, &view); err != nil {
		return nil
	}
	return view.Bootstrap
}

// readGuardCounters reads the mountsync public state file under
// .relay/state.json and copies the telemetry counters + circuit snapshot
// into the CLI-surface shape. Returns nil if the state file is missing
// or unparseable; this is purely additive status, never load-bearing.
func readGuardCounters(localDir string) *syncStateGuards {
	if localDir == "" {
		return nil
	}
	payload, err := os.ReadFile(filepath.Join(localDir, ".relay", "state.json"))
	if err != nil {
		return nil
	}
	var view struct {
		Counters struct {
			SkippedOversizeWriteback uint64 `json:"skippedOversizeWriteback"`
			DeniedRootTarget         uint64 `json:"deniedRootTarget"`
			SnapshotDeleteBlocked    uint64 `json:"snapshotDeleteBlocked"`
			CircuitOpenEvents        uint64 `json:"circuitOpenEvents"`
			TombstonesPending        uint64 `json:"tombstonesPending"`
			TombstonesConfirmed      uint64 `json:"tombstonesConfirmed"`
			TombstonesAgedOut        uint64 `json:"tombstonesAgedOut"`
			PathCollisionQuarantined uint64 `json:"pathCollisionQuarantined"`
		} `json:"counters"`
		LastAppliedRevision string `json:"lastAppliedRevision"`
		Circuit             *struct {
			Open       bool   `json:"open"`
			OpenedAt   string `json:"openedAt"`
			OpenEvents uint64 `json:"openEvents"`
			Failures   int    `json:"failures"`
			NextRetry  string `json:"nextRetry"`
		} `json:"circuit"`
		Guards *syncStateGuards `json:"guards"`
	}
	if err := json.Unmarshal(payload, &view); err != nil {
		return nil
	}
	if view.Guards != nil {
		return view.Guards
	}
	// If everything is zero/empty, return nil so the JSON stays compact.
	zero := view.Counters.SkippedOversizeWriteback == 0 &&
		view.Counters.DeniedRootTarget == 0 &&
		view.Counters.SnapshotDeleteBlocked == 0 &&
		view.Counters.CircuitOpenEvents == 0 &&
		view.Counters.TombstonesPending == 0 &&
		view.Counters.TombstonesConfirmed == 0 &&
		view.Counters.TombstonesAgedOut == 0 &&
		view.Counters.PathCollisionQuarantined == 0 &&
		view.LastAppliedRevision == "" &&
		view.Circuit == nil
	if zero {
		return nil
	}
	g := &syncStateGuards{
		SkippedOversizeWriteback: view.Counters.SkippedOversizeWriteback,
		DeniedRootTarget:         view.Counters.DeniedRootTarget,
		SnapshotDeleteBlocked:    view.Counters.SnapshotDeleteBlocked,
		CircuitOpenEvents:        view.Counters.CircuitOpenEvents,
		TombstonesPending:        view.Counters.TombstonesPending,
		TombstonesConfirmed:      view.Counters.TombstonesConfirmed,
		TombstonesAgedOut:        view.Counters.TombstonesAgedOut,
		PathCollisionQuarantined: view.Counters.PathCollisionQuarantined,
		LastAppliedRevision:      view.LastAppliedRevision,
	}
	if view.Circuit != nil {
		g.Circuit = &syncStateGuardCirc{
			Open:       view.Circuit.Open,
			OpenedAt:   view.Circuit.OpenedAt,
			OpenEvents: view.Circuit.OpenEvents,
			Failures:   view.Circuit.Failures,
			NextRetry:  view.Circuit.NextRetry,
		}
	}
	return g
}

func writeMirrorStateFile(localDir string, snapshot syncStateFile) error {
	if localDir == "" {
		return nil
	}
	if err := ensureMirrorLayout(localDir); err != nil {
		return err
	}
	failedWritebacksStateMu.Lock()
	defer failedWritebacksStateMu.Unlock()
	if persisted := readPersistedFailedWritebacksUnlocked(localDir); persisted > snapshot.FailedWritebacks {
		snapshot.FailedWritebacks = persisted
	}
	snapshot.LastReconcileAt = time.Now().UTC().Format(time.RFC3339)
	payload, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomically(filepath.Join(localDir, ".relay", "state.json"), payload, 0o644)
}

func readPersistedFailedWritebacks(localDir string) uint64 {
	if localDir == "" {
		return 0
	}
	failedWritebacksStateMu.Lock()
	defer failedWritebacksStateMu.Unlock()
	return readPersistedFailedWritebacksUnlocked(localDir)
}

func readPersistedFailedWritebacksUnlocked(localDir string) uint64 {
	payload, err := os.ReadFile(filepath.Join(localDir, ".relay", "state.json"))
	if err != nil {
		return 0
	}
	var snapshot syncStateFile
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return 0
	}
	return snapshot.FailedWritebacks
}

func incrementFailedWritebacksInState(localDir string) error {
	if strings.TrimSpace(localDir) == "" {
		return nil
	}
	failedWritebacksStateMu.Lock()
	defer failedWritebacksStateMu.Unlock()

	statePath := filepath.Join(localDir, ".relay", "state.json")
	document := map[string]any{}
	if payload, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(payload, &document)
	}
	if document == nil {
		document = map[string]any{}
	}
	document["failedWritebacks"] = uint64FromJSONValue(document["failedWritebacks"]) + 1
	payload, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return err
	}
	return writeFileAtomically(statePath, payload, 0o644)
}

func uint64FromJSONValue(value any) uint64 {
	switch v := value.(type) {
	case float64:
		if v > 0 {
			return uint64(v)
		}
	case int:
		if v > 0 {
			return uint64(v)
		}
	case uint64:
		return v
	case json.Number:
		if n, err := strconv.ParseUint(string(v), 10, 64); err == nil {
			return n
		}
	}
	return 0
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

// readDaemonPIDState returns the structured pid state. ok is false for a
// missing, legacy bare-int, or unparseable pid file.
func readDaemonPIDState(localDir string) (daemonPIDState, bool) {
	if localDir == "" {
		return daemonPIDState{}, false
	}
	payload, err := os.ReadFile(mountPIDFile(localDir))
	if err != nil {
		return daemonPIDState{}, false
	}
	var state daemonPIDState
	if json.Unmarshal(payload, &state) == nil && state.PID > 0 {
		return state, true
	}
	return daemonPIDState{}, false
}

func resolvedSelfExecutable() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		return resolved
	}
	return exe
}

// verifyDaemonProcess decides whether the PID recorded for a workspace
// genuinely belongs to that workspace's relayfile daemon. It guards
// stop/restart against PID reuse and tampered/stale pid files causing a
// SIGTERM to an unrelated same-user process.
//
// Returns the recorded pid and whether it is safe to signal. When pid is
// non-zero but verified is false, the pid file is stale/tampered and the
// caller should clear it instead of signaling.
func verifyDaemonProcess(localDir, workspaceID string) (pid int, verified bool) {
	state, structured := readDaemonPIDState(localDir)
	if !structured {
		// Legacy bare-int or unreadable pid file: preserve the prior
		// liveness-only behavior so pre-existing daemons keep working.
		legacy := readDaemonPID(localDir)
		return legacy, legacy != 0
	}
	if state.PID <= 0 {
		return 0, false
	}
	if ws := strings.TrimSpace(workspaceID); ws != "" &&
		strings.TrimSpace(state.WorkspaceID) != "" && state.WorkspaceID != ws {
		return state.PID, false
	}
	if absLocal, err := filepath.Abs(localDir); err == nil && strings.TrimSpace(state.LocalDir) != "" {
		if filepath.Clean(state.LocalDir) != filepath.Clean(absLocal) {
			return state.PID, false
		}
	}
	// Best-effort: when the platform lets us resolve the running
	// process's executable, it must match the recorded daemon binary.
	if exe, known := processExecutablePath(state.PID); known {
		if state.Executable == "" {
			return state.PID, false
		}
		want := state.Executable
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
		if filepath.Clean(exe) != filepath.Clean(want) {
			return state.PID, false
		}
	}
	return state.PID, true
}

func shouldRefuseCompetingMount(daemonized, once bool) bool {
	return !daemonized && !once
}

func workspaceNameForStart(workspaceID string) string {
	if record, ok := workspaceRecordByID(workspaceID); ok && strings.TrimSpace(record.Name) != "" {
		return record.Name
	}
	if record, ok := workspaceRecordByName(workspaceID); ok && strings.TrimSpace(record.Name) != "" {
		return record.Name
	}
	return workspaceID
}

func runningMountDaemons(localDir, workspaceID, workspaceName string) ([]mountDaemonProcess, int, error) {
	processes := make([]mountDaemonProcess, 0, 2)
	seen := map[int]struct{}{}
	stalePID := 0

	discovered, err := discoverMountDaemonProcesses(localDir, workspaceID, workspaceName)
	if err != nil {
		return processes, stalePID, err
	}
	for _, process := range discovered {
		if process.PID == os.Getpid() {
			continue
		}
		if _, ok := seen[process.PID]; ok {
			continue
		}
		processes = append(processes, process)
		seen[process.PID] = struct{}{}
	}

	if pid, verified, strong := verifyDaemonProcessForDiscovery(localDir, workspaceID); pid != 0 {
		_, foundByScan := seen[pid]
		switch {
		case !processAlive(pid):
			stalePID = pid
		case verified && strong && !foundByScan:
			processes = append(processes, mountDaemonProcess{PID: pid, Source: "pidfile"})
			seen[pid] = struct{}{}
		case verified && foundByScan:
			for i := range processes {
				if processes[i].PID == pid {
					processes[i].Source = "pidfile"
					break
				}
			}
		default:
			stalePID = pid
		}
	}
	return processes, stalePID, nil
}

func verifyDaemonProcessForDiscovery(localDir, workspaceID string) (pid int, verified bool, strong bool) {
	if state, structured := readDaemonPIDState(localDir); structured {
		pid, verified = verifyDaemonProcess(localDir, workspaceID)
		return pid, verified, verified && state.PID == pid
	}
	pid, verified = verifyDaemonProcess(localDir, workspaceID)
	return pid, verified, false
}

func discoverMountDaemonProcesses(localDir, workspaceID, workspaceName string) ([]mountDaemonProcess, error) {
	snapshots, err := listProcessCommands()
	if err != nil {
		return nil, err
	}
	processes := make([]mountDaemonProcess, 0)
	for _, snapshot := range snapshots {
		if snapshot.PID <= 0 || snapshot.PID == os.Getpid() {
			continue
		}
		if mountDaemonCommandMatches(snapshot.Command, localDir, workspaceID, workspaceName) {
			processes = append(processes, mountDaemonProcess{
				PID:     snapshot.PID,
				Command: snapshot.Command,
				Source:  "process-scan",
			})
		}
	}
	return processes, nil
}

func mountDaemonCommandMatches(command, localDir, workspaceID, workspaceName string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	fields := strings.Fields(command)
	if len(fields) == 0 || !isRelayfileExecutable(fields[0]) {
		return false
	}
	if !commandHasMountSubcommand(fields) || commandHasOnceFlag(fields) {
		return false
	}
	targets := daemonWorkspaceTargets(workspaceID, workspaceName)
	if commandMatchesWorkspace(fields, targets) {
		return true
	}
	if commandMatchesLocalDir(command, fields, localDir) {
		return true
	}
	return false
}

func isRelayfileExecutable(arg0 string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(arg0)))
	base = strings.TrimSuffix(base, ".exe")
	if base == "" {
		return false
	}
	if base == "relayfile" {
		return true
	}
	return strings.HasPrefix(base, "relayfile-cli")
}

func commandHasMountSubcommand(fields []string) bool {
	for _, field := range fields {
		if field == "mount" || field == "start" || field == "on" {
			return true
		}
	}
	return false
}

func commandHasOnceFlag(fields []string) bool {
	for _, field := range fields {
		if field == "--once" || field == "-once" {
			return true
		}
		if strings.HasPrefix(field, "--once=") || strings.HasPrefix(field, "-once=") {
			value := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(field, "--once="), "-once="))
			if value == "" {
				return true
			}
			parsed, err := strconv.ParseBool(value)
			return err != nil || parsed
		}
	}
	return false
}

func daemonWorkspaceTargets(workspaceID, workspaceName string) []string {
	seen := map[string]struct{}{}
	targets := make([]string, 0, 2)
	for _, value := range []string{workspaceID, workspaceName} {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		targets = append(targets, value)
	}
	return targets
}

func commandMatchesWorkspace(fields []string, targets []string) bool {
	if len(targets) == 0 {
		return false
	}
	for i, field := range fields {
		for _, target := range targets {
			if field == target {
				return true
			}
			if strings.HasPrefix(field, "--workspace=") && strings.TrimPrefix(field, "--workspace=") == target {
				return true
			}
			if (field == "--workspace" || field == "-workspace") && i+1 < len(fields) && fields[i+1] == target {
				return true
			}
		}
	}
	return false
}

func commandMatchesLocalDir(_ string, fields []string, localDir string) bool {
	localDir = strings.TrimSpace(localDir)
	if localDir == "" {
		return false
	}
	absLocal, err := filepath.Abs(localDir)
	if err == nil {
		localDir = absLocal
	}
	cleanLocal := filepath.Clean(localDir)
	for i, field := range fields {
		if pathTokenMatchesLocalDir(field, cleanLocal) {
			return true
		}
		if strings.HasPrefix(field, "--local-dir=") &&
			pathTokenMatchesLocalDir(strings.TrimPrefix(field, "--local-dir="), cleanLocal) {
			return true
		}
		if (field == "--local-dir" || field == "-local-dir") && i+1 < len(fields) &&
			pathTokenMatchesLocalDir(fields[i+1], cleanLocal) {
			return true
		}
	}
	return false
}

func pathTokenMatchesLocalDir(token, cleanLocal string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	absToken, err := filepath.Abs(token)
	if err == nil {
		token = absToken
	}
	cleanToken := filepath.Clean(token)
	return cleanToken == cleanLocal ||
		strings.HasPrefix(cleanToken, cleanLocal+string(os.PathSeparator))
}

func defaultListProcessCommands() ([]processCommandSnapshot, error) {
	if runtime.GOOS == "windows" {
		return nil, nil
	}
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil, err
	}
	return parseProcessCommandSnapshot(out), nil
}

func parseProcessCommandSnapshot(out []byte) []processCommandSnapshot {
	lines := strings.Split(string(out), "\n")
	snapshots := make([]processCommandSnapshot, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		command := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
		if command == "" {
			continue
		}
		snapshots = append(snapshots, processCommandSnapshot{PID: pid, Command: command})
	}
	return snapshots
}

func daemonStatusLine(record workspaceRecord) string {
	running, _, err := runningMountDaemons(record.LocalDir, record.ID, record.Name)
	if err != nil {
		if pid := readDaemonPID(record.LocalDir); pid != 0 {
			return fmt.Sprintf("daemon: running (pid %d)", pid)
		}
		return "daemon: not running"
	}
	if len(running) == 0 {
		if pid := readDaemonPID(record.LocalDir); pid != 0 {
			return fmt.Sprintf("daemon: running (pid %d)", pid)
		}
		return "daemon: not running"
	}
	if hasPIDFileDaemon(running) {
		pidfileDaemons, orphanDaemons := partitionDaemonSources(running)
		if len(orphanDaemons) > 0 {
			return fmt.Sprintf("daemon: running (%s); orphan (%s)", formatDaemonPIDs(pidfileDaemons), formatDaemonPIDs(orphanDaemons))
		}
		return fmt.Sprintf("daemon: running (%s)", formatDaemonPIDs(pidfileDaemons))
	}
	return fmt.Sprintf("daemon: orphan (%s)", formatDaemonPIDs(running))
}

func partitionDaemonSources(processes []mountDaemonProcess) ([]mountDaemonProcess, []mountDaemonProcess) {
	pidfileDaemons := make([]mountDaemonProcess, 0, len(processes))
	orphanDaemons := make([]mountDaemonProcess, 0)
	for _, process := range processes {
		if process.Source == "pidfile" {
			pidfileDaemons = append(pidfileDaemons, process)
			continue
		}
		orphanDaemons = append(orphanDaemons, process)
	}
	return pidfileDaemons, orphanDaemons
}

func hasPIDFileDaemon(processes []mountDaemonProcess) bool {
	for _, process := range processes {
		if process.Source == "pidfile" {
			return true
		}
	}
	return false
}

func formatDaemonPIDs(processes []mountDaemonProcess) string {
	pids := make([]int, 0, len(processes))
	seen := map[int]struct{}{}
	for _, process := range processes {
		if process.PID <= 0 {
			continue
		}
		if _, ok := seen[process.PID]; ok {
			continue
		}
		seen[process.PID] = struct{}{}
		pids = append(pids, process.PID)
	}
	sort.Ints(pids)
	parts := make([]string, 0, len(pids))
	for _, pid := range pids {
		parts = append(parts, fmt.Sprintf("pid %d", pid))
	}
	return strings.Join(parts, ", ")
}

var (
	daemonGracefulStopTimeout = 5 * time.Second
	daemonForceStopTimeout    = 2 * time.Second
)

func stopWorkspaceMountDaemons(record workspaceRecord, stdout io.Writer, tolerateMissing bool) error {
	running, stalePID, err := runningMountDaemons(record.LocalDir, record.ID, record.Name)
	if err != nil {
		return fmt.Errorf("discover running mounts for %s: %w", record.Name, err)
	}
	if stalePID != 0 {
		if rerr := os.Remove(mountPIDFile(record.LocalDir)); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return fmt.Errorf("failed to clear stale background mount state for %s: %w", record.Name, rerr)
		}
		fmt.Fprintf(stdout, "Cleared stale background mount state for %s (pid %d was not a relayfile daemon)\n", record.Name, stalePID)
	}
	if len(running) == 0 {
		if stalePID != 0 {
			return nil
		}
		if tolerateMissing {
			return nil
		}
		return fmt.Errorf("no running mount found for workspace %s", record.Name)
	}
	stopped := make([]mountDaemonProcess, 0, len(running))
	for _, daemon := range running {
		process, perr := os.FindProcess(daemon.PID)
		if perr != nil {
			return fmt.Errorf("failed to find background mount for %s (pid %d): %w", record.Name, daemon.PID, perr)
		}
		if serr := signalDaemonStop(process); serr != nil {
			if isProcessAlreadyGone(serr) {
				if rerr := os.Remove(mountPIDFile(record.LocalDir)); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
					return fmt.Errorf("failed to clear stale background mount state for %s (pid %d): %w", record.Name, daemon.PID, rerr)
				}
				fmt.Fprintf(stdout, "Cleared stale background mount state for %s (pid %d)\n", record.Name, daemon.PID)
				continue
			}
			return fmt.Errorf("failed to stop background mount for %s (pid %d): %w", record.Name, daemon.PID, serr)
		}
		stopped = append(stopped, daemon)
	}
	if len(stopped) == 0 {
		return nil
	}
	if werr := waitForDaemonDiscoveryClear(record.LocalDir, record.ID, record.Name, stopped, daemonGracefulStopTimeout); werr != nil {
		var aliveErr daemonStillAliveError
		if !errors.As(werr, &aliveErr) {
			return werr
		}
		fmt.Fprintf(stdout, "Background mount for %s did not exit after SIGTERM; sending SIGKILL (%s)\n", record.Name, formatDaemonPIDs(stopped))
		for _, daemon := range stopped {
			process, perr := os.FindProcess(daemon.PID)
			if perr != nil {
				return fmt.Errorf("failed to find background mount for %s (pid %d) for force stop: %w", record.Name, daemon.PID, perr)
			}
			if kerr := forceDaemonStop(process); kerr != nil && !isProcessAlreadyGone(kerr) {
				return fmt.Errorf("failed to force stop background mount for %s (pid %d): %w", record.Name, daemon.PID, kerr)
			}
		}
		if werr := waitForDaemonDiscoveryClear(record.LocalDir, record.ID, record.Name, stopped, daemonForceStopTimeout); werr != nil {
			return werr
		}
	}
	_ = os.Remove(mountPIDFile(record.LocalDir))
	fmt.Fprintf(stdout, "Stopped background mount for %s (%s)\n", record.Name, formatDaemonPIDs(stopped))
	return nil
}

type daemonStillAliveError struct {
	timeout time.Duration
	running []mountDaemonProcess
}

func (e daemonStillAliveError) Error() string {
	return fmt.Sprintf("daemon still alive after %s (%s)", e.timeout, formatDaemonPIDs(e.running))
}

func waitForDaemonDiscoveryClear(localDir, workspaceID, workspaceName string, signaled []mountDaemonProcess, timeout time.Duration) error {
	if len(signaled) == 0 {
		return nil
	}
	const pollInterval = 50 * time.Millisecond
	deadline := time.Now().Add(timeout)
	for {
		running, err := discoverMountDaemonProcesses(localDir, workspaceID, workspaceName)
		if err != nil {
			return fmt.Errorf("confirm daemon stop: %w", err)
		}
		stillAlive := signaledDaemonsStillAlive(signaled, running)
		if len(stillAlive) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return daemonStillAliveError{timeout: timeout, running: stillAlive}
		}
		time.Sleep(pollInterval)
	}
}

func signaledDaemonsStillAlive(signaled, discovered []mountDaemonProcess) []mountDaemonProcess {
	if len(signaled) == 0 {
		return nil
	}
	discoveredByPID := map[int]mountDaemonProcess{}
	for _, daemon := range discovered {
		if daemon.PID > 0 {
			discoveredByPID[daemon.PID] = daemon
		}
	}
	stillAlive := make([]mountDaemonProcess, 0, len(signaled))
	seen := map[int]struct{}{}
	for _, daemon := range signaled {
		if daemon.PID <= 0 {
			continue
		}
		if _, ok := seen[daemon.PID]; ok {
			continue
		}
		seen[daemon.PID] = struct{}{}
		if discoveredDaemon, ok := discoveredByPID[daemon.PID]; ok {
			stillAlive = append(stillAlive, discoveredDaemon)
			continue
		}
		if processAlive(daemon.PID) {
			stillAlive = append(stillAlive, daemon)
		}
	}
	return stillAlive
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

func loadSavedConnection(localDir, provider string) integrationConnectionState {
	if localDir == "" || strings.TrimSpace(provider) == "" {
		return integrationConnectionState{}
	}
	payload, err := os.ReadFile(integrationConnectionPath(localDir, provider))
	if err != nil {
		return integrationConnectionState{}
	}
	var state integrationConnectionState
	if err := json.Unmarshal(payload, &state); err != nil {
		return integrationConnectionState{}
	}
	return state
}

func loadSavedConnectionID(localDir, provider string) string {
	state := loadSavedConnection(localDir, provider)
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

// providerRootDir maps a provider id to the directory name it occupies
// inside the local mirror. The mapping must stay aligned with the
// `vfsRoot` values that fallbackIntegrationCatalog and the cloud
// catalog endpoint advertise — otherwise status probes and disconnect
// cleanup would target the wrong path for that provider.
func providerRootDir(provider string) string {
	provider = normalizeProviderID(provider)
	switch provider {
	case "slack", "slack-sage":
		return "slack"
	case "slack-my-senior-dev":
		return "slack-msd"
	case "slack-nightcto":
		return "slack-nightcto"
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

const (
	writebackFailureBodyLimit = 1024
	writebackMaxHTTPAttempts  = 4
)

type writebackFailureTransport struct {
	base     http.RoundTripper
	localDir string
	logger   *log.Logger
	mu       sync.Mutex
	attempts map[string]int
	// firstAttemptAt tracks the timestamp of the first failed attempt per
	// request key so the dead-letter sidecar can populate `firstAttemptAt`
	// when retries are exhausted.
	firstAttemptAt map[string]time.Time
}

type writebackFailureSample struct {
	OpID          string
	Path          string
	Status        int
	Body          string
	BodyTruncated bool
}

type replayReadCloser struct {
	reader io.Reader
	closer io.Closer
}

func (r replayReadCloser) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r replayReadCloser) Close() error {
	return r.closer.Close()
}

func newWritebackFailureTransport(localDir string, logger *log.Logger, base http.RoundTripper) *writebackFailureTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	if logger == nil {
		logger = log.Default()
	}
	return &writebackFailureTransport{
		base:           base,
		localDir:       localDir,
		logger:         logger,
		attempts:       map[string]int{},
		firstAttemptAt: map[string]time.Time{},
	}
}

func (t *writebackFailureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	writebackRequest := isWritebackRequest(req)
	requestBody := ""
	if writebackRequest {
		requestBody = readAndRestoreWritebackRequestBody(req)
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || !writebackRequest {
		return resp, err
	}
	key := writebackAttemptKey(req)
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		t.clearAttempt(key)
		return resp, nil
	}

	sample := sampleWritebackFailure(req, resp, requestBody)
	attempts, firstAttemptAt := t.recordFailureAttempt(key)
	if t.logger != nil {
		t.logger.Printf("WARN writeback request failed opId=%s path=%s status=%d bodyTruncated=%t body=%q",
			sample.OpID, sample.Path, sample.Status, sample.BodyTruncated, sample.Body)
	}
	if err := incrementFailedWritebacksInState(t.localDir); err != nil && t.logger != nil {
		t.logger.Printf("WARN failed to persist failedWritebacks path=%s error=%v", sample.Path, err)
	}
	if writebackRetriesExhausted(resp.StatusCode, attempts) {
		if err := writeDeadLetterWriteback(t.localDir, sample, attempts, firstAttemptAt); err != nil && t.logger != nil {
			t.logger.Printf("WARN failed to write dead-letter opId=%s path=%s error=%v", sample.OpID, sample.Path, err)
		}
		t.clearAttempt(key)
	}
	return resp, nil
}

func isWritebackRequest(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	switch req.Method {
	case http.MethodPut:
		return strings.HasSuffix(req.URL.Path, "/fs/file")
	case http.MethodPost:
		return strings.HasSuffix(req.URL.Path, "/fs/bulk")
	default:
		return false
	}
}

func writebackAttemptKey(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	return req.Method + " " + req.URL.String()
}

func (t *writebackFailureTransport) recordFailureAttempt(key string) (int, time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.attempts[key]++
	if _, ok := t.firstAttemptAt[key]; !ok {
		t.firstAttemptAt[key] = time.Now().UTC()
	}
	return t.attempts[key], t.firstAttemptAt[key]
}

func (t *writebackFailureTransport) clearAttempt(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.attempts, key)
	delete(t.firstAttemptAt, key)
}

func readAndRestoreWritebackRequestBody(req *http.Request) string {
	if req == nil || req.Body == nil {
		return ""
	}
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return ""
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	return string(bodyBytes)
}

func sampleWritebackFailure(req *http.Request, resp *http.Response, requestBody string) writebackFailureSample {
	path := writebackFailurePath(req, requestBody)
	var bodyBytes []byte
	bodyTruncated := false
	if resp.Body != nil {
		bodyBytes, _ = io.ReadAll(io.LimitReader(resp.Body, writebackFailureBodyLimit))
		bodyTruncated = responseBodyWasTruncated(resp, len(bodyBytes))
		resp.Body = replayReadCloser{
			reader: io.MultiReader(bytes.NewReader(bodyBytes), resp.Body),
			closer: resp.Body,
		}
	}
	body := string(bodyBytes)
	opID := writebackFailureOpID(req, resp, body)
	return writebackFailureSample{
		OpID:          opID,
		Path:          path,
		Status:        resp.StatusCode,
		Body:          body,
		BodyTruncated: bodyTruncated,
	}
}

func writebackFailurePath(req *http.Request, requestBody string) string {
	if req == nil || req.URL == nil {
		return ""
	}
	if req.Method == http.MethodPut {
		return normalizeWritebackFailurePath(req.URL.Query().Get("path"))
	}
	if req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/fs/bulk") {
		return ""
	}
	var payload struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(requestBody), &payload); err != nil || len(payload.Files) == 0 {
		return ""
	}
	paths := make([]string, 0, len(payload.Files))
	for _, file := range payload.Files {
		if path := normalizeWritebackFailurePath(file.Path); path != "" {
			paths = append(paths, path)
		}
	}
	return strings.Join(paths, ",")
}

func normalizeWritebackFailurePath(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	for strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}
	return path
}

func responseBodyWasTruncated(resp *http.Response, sampled int) bool {
	if resp == nil {
		return false
	}
	if resp.ContentLength > int64(sampled) {
		return true
	}
	return resp.ContentLength < 0 && sampled == writebackFailureBodyLimit
}

func writebackFailureOpID(req *http.Request, resp *http.Response, body string) string {
	for _, key := range []string{"X-Relayfile-Op-Id", "X-Operation-Id", "X-Op-Id"} {
		if resp != nil {
			if value := safeWritebackOpID(resp.Header.Get(key)); value != "" {
				return value
			}
		}
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err == nil {
		for _, key := range []string{"opId", "opID", "operationId", "id"} {
			if value, ok := payload[key].(string); ok {
				if opID := safeWritebackOpID(value); opID != "" {
					return opID
				}
			}
		}
	}
	if req != nil {
		if correlation := strings.TrimSpace(req.Header.Get("X-Correlation-Id")); correlation != "" {
			return "op_failed_" + strings.TrimPrefix(correlation, "corr_")
		}
	}
	return fmt.Sprintf("op_failed_%d", time.Now().UTC().UnixNano())
}

func safeWritebackOpID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, `/\`) {
		return ""
	}
	return value
}

func writebackRetriesExhausted(status, attempts int) bool {
	if status == http.StatusTooManyRequests || (status >= 500 && status <= 599) {
		return attempts >= writebackMaxHTTPAttempts
	}
	return true
}

func writeDeadLetterWriteback(localDir string, sample writebackFailureSample, attempts int, firstAttemptAt time.Time) error {
	opID := safeWritebackOpID(sample.OpID)
	if strings.TrimSpace(localDir) == "" || opID == "" {
		return nil
	}
	now := time.Now().UTC()
	if firstAttemptAt.IsZero() {
		firstAttemptAt = now
	}
	record := struct {
		OpID       string `json:"opId"`
		Path       string `json:"path"`
		Attempts   int    `json:"attempts"`
		LastStatus int    `json:"lastStatus"`
		LastBody   string `json:"lastBody"`
		Timestamp  string `json:"ts"`
	}{
		OpID:       opID,
		Path:       sample.Path,
		Attempts:   attempts,
		LastStatus: sample.Status,
		LastBody:   sample.Body,
		Timestamp:  now.Format(time.RFC3339),
	}
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	// Marshal the sidecar BEFORE either file lands on disk so a sidecar
	// serialization error fails the whole operation without leaving an
	// orphan payload. Consumers (CLI `writeback list`, FUSE readers,
	// replay tooling) rely on both files being present together.
	sidecar, err := writeback.MarshalSidecar(writeback.ErrorContext{
		Code:             classifyDeadLetterSidecarCode(sample.Status),
		Message:          deadLetterErrorMessage(sample),
		ProviderStatus:   sample.Status,
		ProviderResponse: deadLetterProviderResponse(sample),
		Attempts:         attempts,
		FirstAttemptAt:   firstAttemptAt,
		LastAttemptAt:    now,
		OpID:             opID,
	})
	if err != nil {
		return err
	}
	dir := deadLetterDirFor(localDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	payloadPath := filepath.Join(dir, opID+".json")
	sidecarPath := deadLetterErrorPathFor(localDir, opID)
	if err := writeFileAtomically(payloadPath, payload, 0o644); err != nil {
		return err
	}
	// If the sidecar write fails after the payload has landed, remove the
	// payload so the dead-letter directory never holds a half-committed
	// record. Best-effort: a remove failure is still surfaced as the
	// underlying sidecar-write error, not masked.
	if err := writeFileAtomically(sidecarPath, sidecar, 0o644); err != nil {
		_ = os.Remove(payloadPath)
		return err
	}
	return nil
}

// classifyDeadLetterSidecarCode maps an upstream HTTP status to the four
// sidecar codes enumerated in `schemas/relay/dead-letter-error.schema.json`.
// 4xx (non-429) is non-retryable provider error; 5xx and 429 retried until
// exhaustion become `provider_5xx_exhausted`. Statuses below 400 or zero
// indicate a transport-level failure recorded as a timeout.
func classifyDeadLetterSidecarCode(status int) writeback.ErrorCode {
	switch {
	case status >= 400 && status < 500 && status != http.StatusTooManyRequests:
		return writeback.CodeProvider4xx
	case status == http.StatusTooManyRequests, status >= 500 && status <= 599:
		return writeback.CodeProvider5xxExhaust
	default:
		return writeback.CodeTimeout
	}
}

func deadLetterErrorMessage(sample writebackFailureSample) string {
	if msg := strings.TrimSpace(sample.Body); msg != "" {
		return fmt.Sprintf("writeback failed with HTTP %d: %s", sample.Status, msg)
	}
	return fmt.Sprintf("writeback failed with HTTP %d after retries", sample.Status)
}

// deadLetterProviderResponse returns the provider response body as either a
// decoded object/array (matching the schema's `type: ["object", "array"]`)
// or, when the body is not JSON, an envelope object preserving the raw bytes.
// Returning `nil` would drop the field entirely; the lead plan requires the
// raw upstream response be retained for replay/debugging.
func deadLetterProviderResponse(sample writebackFailureSample) interface{} {
	body := strings.TrimSpace(sample.Body)
	if body == "" {
		return nil
	}
	var decoded interface{}
	if err := json.Unmarshal([]byte(body), &decoded); err == nil {
		switch decoded.(type) {
		case map[string]interface{}, []interface{}:
			return decoded
		}
	}
	return map[string]interface{}{
		"raw":       sample.Body,
		"truncated": sample.BodyTruncated,
	}
}

func readDeadLetterErrorSidecar(localDir, opID string) (deadLetterErrorDetail, bool, error) {
	opID = safeWritebackOpID(opID)
	if strings.TrimSpace(localDir) == "" || opID == "" {
		return deadLetterErrorDetail{}, false, nil
	}
	payload, err := os.ReadFile(deadLetterErrorPathFor(localDir, opID))
	if err != nil {
		if os.IsNotExist(err) {
			return deadLetterErrorDetail{}, false, nil
		}
		return deadLetterErrorDetail{}, false, err
	}
	var detail deadLetterErrorDetail
	if err := json.Unmarshal(payload, &detail); err != nil {
		return deadLetterErrorDetail{}, false, fmt.Errorf("invalid dead-letter sidecar %s: %w", deadLetterErrorPathFor(localDir, opID), err)
	}
	return detail, true, nil
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

func resolveExplicitToken(flagValue string) string {
	if value := strings.TrimSpace(flagValue); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv("RELAYFILE_TOKEN"))
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

func enforcePollIntervalFloor(interval time.Duration) time.Duration {
	if interval > 0 && interval < minMountPollInterval {
		return minMountPollInterval
	}
	return interval
}

func jitteredIntervalWithSample(base time.Duration, jitterRatio, sample float64) time.Duration {
	if base <= 0 {
		return 0
	}
	jitterRatio = clampJitterRatio(jitterRatio)
	if jitterRatio == 0 {
		return enforcePollIntervalFloor(base)
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
	return enforcePollIntervalFloor(delay)
}

var spawnBackgroundMountProcessFn = spawnBackgroundMountProcess

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

// logStuckEventSummary surfaces the stuck-event drain outcome of a reconcile
// cycle. In the request-driven, one-shot pull model a cycle can be canceled
// mid-drain; without this the CLI exits silently and the operator cannot tell
// whether the cursor caught up. When events were skipped or a backlog remains,
// it points the operator at `relayfile writeback skip-stuck`.
func logStuckEventSummary(syncer *mountsync.Syncer, cycleErr error) {
	skipped := syncer.StaleAliasSkips()
	backlog := syncer.BacklogDraining()
	if skipped == 0 && !backlog {
		return
	}
	if backlog || errors.Is(cycleErr, context.Canceled) || errors.Is(cycleErr, context.DeadlineExceeded) {
		log.Printf("stuck-event drain: %d stale index event(s) skipped this cycle; backlog remains — re-run or use 'relayfile writeback skip-stuck' to clear", skipped)
		return
	}
	log.Printf("stuck-event drain: %d stale index event(s) skipped this cycle; events cursor caught up to live head", skipped)
}

func runMountLoop(rootCtx context.Context, syncer *mountsync.Syncer, localDir, workspaceID, serverURL, delegatedCredsFile string, timeout, interval time.Duration, intervalJitter float64, websocketEnabled, once, daemonized bool, pidFile, logFile string) error {
	interval = enforcePollIntervalFloor(interval)
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
	degraded := false
	var lastDegradedNotice time.Time
	// Exponential backoff for degraded credential recovery: base 30s, cap 10m.
	// Avoids hammering the auth endpoint if the operator session is truly gone.
	const degradedBackoffBase = 30 * time.Second
	const degradedBackoffMax = 10 * time.Minute
	const degradedNoticeInterval = time.Minute
	degradedAttempts := 0
	var nextDegradedAttempt time.Time
	const degradedStallReason = "delegated relayfile credentials expired or revoked — re-bootstrap relayfile credentials with agent-relay cloud login"

	enterDegraded := func() {
		if !degraded {
			degraded = true
			stallReason = degradedStallReason
			lastDegradedNotice = time.Time{}
			nextDegradedAttempt = time.Time{}
			log.Printf("mount entering read-only degraded state: %s", degradedStallReason)
		}
	}
	exitDegraded := func() {
		if degraded {
			degraded = false
			stallReason = ""
			lastDegradedNotice = time.Time{}
			degradedAttempts = 0
			nextDegradedAttempt = time.Time{}
			log.Printf("mount exiting degraded state; delegated relayfile credentials restored")
		}
	}
	maybePrintRecovery := func() {
		if !degraded {
			return
		}
		if time.Since(lastDegradedNotice) < degradedNoticeInterval {
			return
		}
		log.Printf("mount degraded: %s", degradedStallReason)
		lastDegradedNotice = time.Now()
	}

	refreshMountAuth := func(force bool) error {
		if httpClient == nil {
			return nil
		}
		if !force && !relayfileTokenNeedsRefresh(httpClient.Token()) {
			return nil
		}
		if strings.TrimSpace(delegatedCredsFile) == "" {
			return nil
		}
		bundle, _, err := loadDelegatedCredentials(delegatedCredsFile)
		if err != nil {
			return err
		}
		if bundle.Workspace() != "" && bundle.Workspace() != workspaceID {
			return fmt.Errorf("delegated relayfile credentials are for workspace %s, expected %s", bundle.Workspace(), workspaceID)
		}
		renewed, err := refreshDelegatedCredentials(delegatedCredsFile, bundle, force)
		if err != nil {
			return err
		}
		httpClient.SetToken(renewed.BearerToken())
		syncer.SetCredentialExpiry(renewed.BearerExpiresAt())
		syncer.ResetWebSocket()
		record.Server = strings.TrimRight(renewed.ServerURL(), "/")
		record.CloudAPIURL = ""
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

	// writeSnapshot is invoked from many places: each periodic sync cycle, each
	// auth refresh, and — most dangerously — every fsnotify event from the
	// local file watcher. A noisy editor that touches one file repeatedly can
	// fire 30+ events per second, and each one used to call
	// fetchWorkspaceSyncStatus directly. That's the polling-storm shape we hit
	// on 2026-05-14. Wrap the status fetch in a per-daemon rate gate so that
	// snapshots are still written from cached state but the upstream API is
	// hit no more than once per syncStatusMinPollInterval.
	var (
		snapshotMu          sync.Mutex
		lastSnapshotStatus  syncStatusResponse
		lastSnapshotFetchAt time.Time
		hasCachedStatus     bool
	)
	writeSnapshot := func() {
		if httpClient == nil {
			return
		}
		snapshotMu.Lock()
		needFetch := !hasCachedStatus || time.Since(lastSnapshotFetchAt) >= syncStatusMinPollInterval
		cached := lastSnapshotStatus
		snapshotMu.Unlock()

		status := cached
		if needFetch {
			client, err := newAPIClient(serverURL, httpClient.Token())
			if err != nil {
				return
			}
			fetched, err := fetchWorkspaceSyncStatus(client, workspaceID)
			if err != nil {
				// Fetch failed; fall back to cached snapshot if we have one,
				// otherwise skip this write — we'd rather have a stale
				// snapshot than spin retrying on every file event.
				if !hasCachedStatus {
					return
				}
			} else {
				snapshotMu.Lock()
				lastSnapshotStatus = fetched
				lastSnapshotFetchAt = time.Now()
				hasCachedStatus = true
				snapshotMu.Unlock()
				status = fetched
			}
		}
		_ = writeMirrorStateFile(localDir, buildSyncStateSnapshot(status, workspaceID, defaultMountMode, interval, localDir, readDaemonPID(localDir), stallReason))
	}

	runCycle := func(reconcile bool) error {
		if degraded {
			// Exponential backoff: skip recovery attempts until nextDegradedAttempt.
			// This prevents hammering the auth endpoint on consecutive cycles.
			if !nextDegradedAttempt.IsZero() && time.Now().Before(nextDegradedAttempt) {
				maybePrintRecovery()
				writeSnapshot()
				return errors.New(degradedStallReason)
			}
			// Try to recover by re-running auth refresh.
			if err := refreshMountAuth(true); err != nil {
				// Compute backoff for next attempt: base * 2^attempts, capped.
				// Cap the exponent at 14 to prevent int64 overflow on 30s<<uint(n)
				// after ~28 consecutive failures (would shift to negative).
				exp := degradedAttempts
				if exp > 14 {
					exp = 14
				}
				backoff := degradedBackoffBase << uint(exp)
				if backoff > degradedBackoffMax {
					backoff = degradedBackoffMax
				}
				degradedAttempts++
				nextDegradedAttempt = time.Now().Add(backoff)
				if isMountCredentialExpired(err) {
					maybePrintRecovery()
					writeSnapshot()
					return err
				}
				log.Printf("mount degraded: refresh attempt failed (next retry in %s): %v", backoff, err)
				writeSnapshot()
				return err
			}
			exitDegraded()
		}
		err := withAuthRefresh(func(ctx context.Context) error {
			if reconcile {
				return syncer.Reconcile(ctx)
			}
			return syncer.SyncOnce(ctx)
		})
		if err != nil {
			if isMountCredentialExpired(err) {
				enterDegraded()
				maybePrintRecovery()
				writeSnapshot()
				return err
			}
			// Mid-bootstrap, a per-cycle deadline exceeded is expected
			// progress, not a stall: the rootCtx-derived bootstrap
			// context keeps the heavy pull alive across cycles and
			// persists a resume cursor. Suppress the scary "stall:"
			// only for that expected timeout case.
			if errors.Is(err, context.DeadlineExceeded) {
				if bs := readBootstrapStatus(localDir); bs != nil {
					stallReason = ""
					log.Printf("mount bootstrapping: %d/%d files (in progress)", bs.FilesSynced, bs.FilesTotal)
					writeSnapshot()
					return err
				}
			}
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

	log.Print(mountStartBanner(localDir, interval, intervalJitter))
	initialErr := runCycle(true)
	logStuckEventSummary(syncer, initialErr)
	if once {
		return initialErr
	}

	watcher, err := mountsync.NewFileWatcher(localDir, func(relativePath string, op fsnotify.Op) {
		if degraded {
			// Local edit observed while delegated credentials are unusable.
			// Leave the dirty state in place so the next successful cycle
			// after re-login picks it up; do not attempt to push now.
			maybePrintRecovery()
			return
		}
		if err := withAuthRefresh(func(ctx context.Context) error {
			return syncer.HandleLocalChange(ctx, relativePath, op)
		}); err != nil {
			if isMountCredentialExpired(err) {
				enterDegraded()
				maybePrintRecovery()
				writeSnapshot()
				return
			}
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
	wsTicker := time.NewTicker(mountsync.DefaultWebSocketMaintenanceEvery)
	defer wsTicker.Stop()
	cycle := 0
	for {
		select {
		case <-rootCtx.Done():
			log.Printf("mount sync stopping: %v", rootCtx.Err())
			writeSnapshot()
			return nil
		case <-wsTicker.C:
			if websocketEnabled {
				ctx, cancel := context.WithTimeout(rootCtx, timeout)
				if err := syncer.MaintainWebSocket(ctx); err != nil {
					log.Printf("websocket unavailable; using polling sync: %v", err)
				}
				cancel()
			}
		case <-timer.C:
			cycle++
			reconcile := shouldReconcileMountCycle(websocketEnabled, cycle)
			if reconcile {
				_ = runCycle(true)
			}
			if !degraded && time.Since(lastSuccess) >= 10*time.Minute {
				if bs := readBootstrapStatus(localDir); bs != nil {
					// Long-running initial mirror is making progress
					// across cycles — not a stall.
					stallReason = ""
					log.Printf("mount bootstrapping: %d/%d files (in progress)", bs.FilesSynced, bs.FilesTotal)
					writeSnapshot()
				} else {
					stallReason = "no successful reconcile for 10m"
					log.Printf("mount stalled: %s", stallReason)
					writeSnapshot()
				}
			}
			timer.Reset(jitteredIntervalWithSample(interval, intervalJitter, mathrand.Float64()))
		}
	}
}

func shouldReconcileMountCycle(websocketEnabled bool, cycle int) bool {
	return !websocketEnabled || cycle%websocketReconcileEvery == 0
}

func correlationID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return "corr_" + hex.EncodeToString(buf[:])
	}
	return fmt.Sprintf("corr_%d", time.Now().UnixNano())
}
