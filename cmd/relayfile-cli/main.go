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
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/agentworkforce/relayfile/internal/mountsync"
	"github.com/agentworkforce/relayfile/internal/writeback"
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
	minMountPollInterval    = 5 * time.Second
	defaultMountTimeout     = 15 * time.Second
)

var defaultJoinScopes = []string{"fs:read", "fs:write"}
var defaultInspectScopes = []string{"relayfile:fs:read:*"}

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

type cloudTokenRefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
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
	WorkspaceID      string              `json:"workspaceId"`
	RemoteRoot       string              `json:"remoteRoot,omitempty"`
	Mode             string              `json:"mode"`
	LastReconcileAt  string              `json:"lastReconcileAt,omitempty"`
	LastEventAt      string              `json:"lastEventAt,omitempty"`
	IntervalMs       int64               `json:"intervalMs"`
	Providers        []syncStateProvider `json:"providers,omitempty"`
	PendingWriteback int                 `json:"pendingWriteback"`
	PendingConflicts int                 `json:"pendingConflicts"`
	DeniedPaths      int                 `json:"deniedPaths"`
	FailedWritebacks uint64              `json:"failedWritebacks"`
	StallReason      string              `json:"stallReason,omitempty"`
	Daemon           *syncStateDaemon    `json:"daemon,omitempty"`
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
	LastAppliedRevision      string              `json:"lastAppliedRevision,omitempty"`
	Circuit                  *syncStateGuardCirc `json:"circuit,omitempty"`
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

// cloudLoginStateMismatchExitCode is the exit code mandated by productized
// cloud-mount contract A2 when the browser callback's state parameter does
// not match the value the CLI generated. The dedicated code lets shells and
// CI distinguish a tampered/replayed login flow from a generic CLI error.
const cloudLoginStateMismatchExitCode = 10

// ErrCloudLoginStateMismatch is returned by runCloudLogin when the OAuth
// callback's `state` query parameter does not match the value the CLI
// generated. main() maps this sentinel to exit code 10 per A2.
var ErrCloudLoginStateMismatch = errors.New("Relayfile Cloud login state mismatch")

func main() {
	log.SetFlags(0)
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		if errors.Is(err, ErrCloudLoginStateMismatch) {
			os.Exit(cloudLoginStateMismatchExitCode)
		}
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
	case "logs":
		fmt.Fprintln(stdout, "Usage: relayfile logs [WORKSPACE] [--lines N]")
	case "observer":
		fmt.Fprintln(stdout, "Usage: relayfile observer [WORKSPACE] [--no-open]")
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
	case "delete":
		fmt.Fprintln(w, "Usage: relayfile workspace delete NAME [--yes]")
	default:
		fmt.Fprintln(w, `Usage:
  relayfile workspace create NAME
  relayfile workspace join WORKSPACE_ID [--name NAME] [--write]
  relayfile workspace use NAME
  relayfile workspace list [--names-only]
  relayfile workspace current [--verbose]
  relayfile workspace delete NAME [--yes]`)
	}
}

func printIntegrationUsage(w io.Writer, subcommand string) {
	switch subcommand {
	case "connect":
		fmt.Fprintln(w, "Usage: relayfile integration connect PROVIDER [--backend BACKEND] [--workspace NAME] [--no-open] [--timeout 5m]")
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
	default:
		fmt.Fprintln(w, `Usage:
  relayfile integration connect PROVIDER [--backend BACKEND] [--workspace NAME]
  relayfile integration available [--search QUERY] [--backend BACKEND] [--json] [--refresh]
  relayfile integration search QUERY [--backend BACKEND] [--json] [--refresh]
  relayfile integration list [--workspace NAME] [--json]
  relayfile integration disconnect PROVIDER [--workspace NAME] [--yes]
  relayfile integration adopt PROVIDER --connection-id ID [--workspace NAME] [--provider-config-key KEY] [--yes]
  relayfile integration set-metadata PROVIDER KEY=VALUE [KEY=VALUE...] [--workspace NAME] [--yes]`)
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
	case "retry":
		fmt.Fprintln(w, "Usage: relayfile writeback retry --opId OP [WORKSPACE]")
	default:
		fmt.Fprintln(w, `Usage:
  relayfile writeback list --state pending|dead [--workspace WS] [--json]
  relayfile writeback status [WORKSPACE] [--json]
  relayfile writeback retry --opId OP [WORKSPACE]`)
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
  relayfile workspace create NAME
  relayfile workspace join WORKSPACE_ID [--name NAME] [--write]
  relayfile workspace use NAME
  relayfile workspace list [--names-only]
  relayfile workspace current [--verbose]
  relayfile workspace delete NAME [--yes]
  relayfile integration connect PROVIDER [--backend BACKEND] [--workspace NAME]
    (for jira/confluence: prompts for the Atlassian site to bind after OAuth completes)
  relayfile integration available [--search QUERY] [--backend BACKEND] [--json] [--refresh]
  relayfile integration search QUERY [--backend BACKEND] [--json] [--refresh]
  relayfile integration list [--workspace NAME] [--json]
  relayfile integration disconnect PROVIDER [--workspace NAME] [--yes]
  relayfile integration adopt PROVIDER --connection-id ID [--workspace NAME] [--provider-config-key KEY] [--yes]
  relayfile integration set-metadata PROVIDER KEY=VALUE [KEY=VALUE...] [--workspace NAME] [--yes]
  relayfile ops list [--workspace NAME] [--json]
  relayfile ops replay OPID [--workspace NAME]
  relayfile writeback list --state pending|dead [--workspace WS] [--json]
  relayfile writeback status [WORKSPACE] [--json]
  relayfile writeback retry --opId OP [WORKSPACE]
  relayfile digest rebuild --window today|yesterday|YYYY-MM-DD|this-week|last-week [--workspace NAME] [--json]
  relayfile pull [--workspace NAME] [--provider PROVIDER] [--reason TEXT]
  relayfile mount [WORKSPACE] [LOCAL_DIR]
  relayfile start [WORKSPACE] [LOCAL_DIR]            (alias for mount; pass --background to detach)
  relayfile on [WORKSPACE] [LOCAL_DIR]              (alias for mount; pass --background to detach)
  relayfile stop [WORKSPACE]
  relayfile off [WORKSPACE]                         (alias for stop)
  relayfile restart [WORKSPACE] [--foreground]
  relayfile tree [WORKSPACE] [PATH] [--depth N]
  relayfile read [WORKSPACE] PATH
  relayfile seed [WORKSPACE] [DIR]
  relayfile export [WORKSPACE] --format FORMAT [--output FILE]
  relayfile status [WORKSPACE]
  relayfile logs [WORKSPACE]
  relayfile observer [WORKSPACE] [--no-open]

Subcommands:
  setup       Sign in, connect an integration, and mount the workspace
  login       Sign in via the Relayfile Cloud browser flow (or --api-key for self-hosted)
  workspace   Create, join, select, list, show current, or delete locally tracked workspaces
  integration Connect, discover, list, disconnect, or adopt workspace integrations
  ops         List or replay dead-lettered writeback ops
  writeback   Inspect or retry local writeback failures
  writeback list
              List local writeback items by state
  writeback status
              Show local pending, failed, and dead-lettered writebacks
  writeback retry
              Re-enqueue a local dead-lettered writeback op
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

	joined, err := joinWorkspaceViaCloud(tokenSet, record.ID, record.AgentName, record.Scopes)
	if err != nil {
		return err
	}
	record, err = persistJoinedWorkspace(record, joined, tokenSet.APIURL, absLocalDir, true)
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
			if err := connectCloudIntegration(tokenSet.APIURL, record.ID, joined.Token, selectedProvider, requestedBackend, absLocalDir, *connectTimeout, !*noOpen, stdout); err != nil {
				return err
			}
			createdConnection = true
		} else {
			var err error
			createdConnection, err = ensureCloudIntegration(tokenSet.APIURL, record.ID, joined.Token, selectedProvider, requestedBackend, absLocalDir, *connectTimeout, !*noOpen, stdout)
			if err != nil {
				return err
			}
		}
		if createdConnection && isAtlassianProvider(selectedProvider) {
			if err := runAtlassianSitePicker(tokenSet.APIURL, record.ID, joined.Token, selectedProvider, stdin, stdout); err != nil {
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

// ErrCloudRefreshExpired indicates the cloud refresh token cannot mint new
// access tokens. The mount loop uses this to enter the read-only degraded
// state described in the productized cloud-mount contract acceptance test
// A9 ("Cloud refresh token expired").
var ErrCloudRefreshExpired = errors.New("cloud session expired. Run 'relayfile login' to sign in again.")

func refreshCloudCredentials(creds cloudCredentials) (cloudCredentials, error) {
	if strings.TrimSpace(creds.RefreshToken) == "" {
		return creds, ErrCloudRefreshExpired
	}
	refreshed, err := refreshCloudCredentialsOnce(creds)
	if err == nil {
		return refreshed, nil
	}
	if !isCloudRefreshAuthRejection(err) {
		return creds, fmt.Errorf("refresh cloud session: %w", err)
	}

	diskCreds, diskErr := loadCloudCredentials()
	if diskErr == nil {
		if strings.TrimSpace(diskCreds.APIURL) == "" {
			diskCreds.APIURL = creds.APIURL
		}
		diskChanged := !sameCloudRefreshMaterial(creds, diskCreds)
		if diskChanged && !cloudAccessTokenExpiredSoon(diskCreds) {
			return diskCreds, nil
		}
		if diskChanged {
			refreshed, retryErr := refreshCloudCredentialsOnce(diskCreds)
			if retryErr == nil {
				return refreshed, nil
			}
			if !isCloudRefreshAuthRejection(retryErr) {
				return diskCreds, fmt.Errorf("refresh cloud session: %w", retryErr)
			}
		}
	}
	return creds, ErrCloudRefreshExpired
}

func refreshCloudCredentialsOnce(creds cloudCredentials) (cloudCredentials, error) {
	client, err := newAPIClient(creds.APIURL, creds.AccessToken)
	if err != nil {
		return creds, err
	}
	var refreshed cloudCredentials
	err = client.postJSON(context.Background(), "/api/v1/auth/token/refresh", cloudTokenRefreshRequest{
		RefreshToken: creds.RefreshToken,
	}, &refreshed)
	if err != nil {
		return creds, err
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

func isCloudRefreshAuthRejection(err error) bool {
	var httpErr *apiError
	if !errors.As(err, &httpErr) {
		return false
	}
	return httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden
}

func sameCloudRefreshMaterial(a, b cloudCredentials) bool {
	return strings.TrimSpace(a.APIURL) == strings.TrimSpace(b.APIURL) &&
		strings.TrimSpace(a.AccessToken) == strings.TrimSpace(b.AccessToken) &&
		strings.TrimSpace(a.AccessTokenExpiresAt) == strings.TrimSpace(b.AccessTokenExpiresAt) &&
		strings.TrimSpace(a.RefreshToken) == strings.TrimSpace(b.RefreshToken) &&
		strings.TrimSpace(a.RefreshTokenExpiresAt) == strings.TrimSpace(b.RefreshTokenExpiresAt)
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

func persistJoinedWorkspace(record workspaceRecord, joined cloudWorkspaceJoinResponse, cloudAPIURL, localDir string, setDefault bool) (workspaceRecord, error) {
	serverURL := strings.TrimRight(strings.TrimSpace(joined.RelayfileURL), "/")
	if err := saveCredentials(credentials{
		Server:    serverURL,
		Token:     strings.TrimSpace(joined.Token),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return workspaceRecord{}, err
	}
	if joinedWorkspaceID := strings.TrimSpace(joined.WorkspaceID); joinedWorkspaceID != "" {
		if strings.TrimSpace(record.ID) == "" {
			record.ID = joinedWorkspaceID
		} else {
			record.RelayWorkspaceID = joinedWorkspaceID
		}
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
	persisted, err := upsertWorkspaceDetails(record)
	if err != nil {
		return workspaceRecord{}, err
	}
	if !setDefault {
		return persisted, nil
	}
	persisted, err = setDefaultWorkspace(persisted.Name)
	if err != nil {
		return workspaceRecord{}, err
	}
	return persisted, nil
}

func relayWorkspaceIDForRecord(record workspaceRecord, joined cloudWorkspaceJoinResponse) string {
	if relayID := strings.TrimSpace(joined.WorkspaceID); relayID != "" {
		return relayID
	}
	if relayID := strings.TrimSpace(record.RelayWorkspaceID); relayID != "" {
		return relayID
	}
	return strings.TrimSpace(record.ID)
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
			errCh <- ErrCloudLoginStateMismatch
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
			fmt.Fprintf(stdout, "%s connected. Files will appear under %s/%s as Relayfile syncs in the background; keep this command running while the mount is active.\n", provider, localDir, providerRootDir(provider))
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

	// Default: cloud browser flow.
	cloudAPI := strings.TrimRight(strings.TrimSpace(*cloudAPIURL), "/")
	if cloudAPI == "" {
		cloudAPI = defaultCloudAPIURL
	}
	creds, err := ensureCloudCredentials(cloudAPI, strings.TrimSpace(*cloudToken), *loginTimeout, !*noOpen, stdout)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Signed in to Relayfile Cloud at %s\n", creds.APIURL)

	if !*skipWorkspace {
		record, rerr := resolveWorkspaceRecord(strings.TrimSpace(*workspaceFlag))
		if rerr == nil {
			joined, jerr := joinWorkspaceViaCloud(creds, record.ID, record.AgentName, record.Scopes)
			if jerr == nil {
				if persisted, perr := persistJoinedWorkspace(record, joined, creds.APIURL, record.LocalDir, true); perr == nil {
					record = persisted
					fmt.Fprintf(stdout, "Refreshed workspace token for %s (%s)\n", record.Name, record.ID)
					return nil
				} else if strings.TrimSpace(*workspaceFlag) != "" {
					return fmt.Errorf("refresh workspace token: persist refreshed credentials: %w", perr)
				} else {
					fmt.Fprintf(stdout, "warning: workspace token refresh succeeded but persisting it failed: %v\n", perr)
				}
			} else if strings.TrimSpace(*workspaceFlag) != "" {
				return fmt.Errorf("refresh workspace token: %w", jerr)
			} else {
				fmt.Fprintf(stdout, "warning: could not refresh workspace token for %s: %v\n", record.Name, jerr)
			}
		} else if strings.TrimSpace(*workspaceFlag) != "" {
			return rerr
		}
	}

	if !*skipWorkspace {
		if err := removeCredentialFile(credentialsPath()); err != nil {
			fmt.Fprintf(stdout, "warning: could not clear stale server credentials: %v\n", err)
		}
		fmt.Fprintln(stdout, "Run 'relayfile setup' to create or join a workspace, or 'relayfile mount WORKSPACE' if you already have one.")
	}
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
		return errors.New("workspace subcommand is required: create, join, use, list, current, or delete")
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
	case "delete":
		return runWorkspaceDelete(args[1:], stdin, stdout)
	default:
		return fmt.Errorf("unknown workspace subcommand %q", args[0])
	}
}

func runIntegration(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("integration subcommand is required: connect, available, search, list, disconnect, adopt, or set-metadata")
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
	default:
		return fmt.Errorf("unknown integration subcommand %q", args[0])
	}
}

func runIntegrationConnect(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("integration connect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceName := fs.String("workspace", "", "workspace name or id")
	cloudAPIURL := fs.String("cloud-api-url", envOrDefault("RELAYFILE_CLOUD_API_URL", defaultCloudAPIURL), "Relayfile Cloud API URL")
	backend := fs.String("backend", "", "integration backend to request (nango or composio)")
	noOpen := fs.Bool("no-open", false, "print the hosted URL instead of opening it")
	timeout := fs.Duration("timeout", 5*time.Minute, "integration readiness timeout")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"workspace":     true,
		"cloud-api-url": true,
		"backend":       true,
		"no-open":       false,
		"timeout":       true,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile integration connect PROVIDER [--backend BACKEND] [--workspace NAME] [--no-open] [--timeout 5m]")
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
	joined, err := joinWorkspaceViaCloud(cloudCreds, record.ID, record.AgentName, record.Scopes)
	if err != nil {
		return err
	}
	record, err = persistJoinedWorkspace(record, joined, cloudCreds.APIURL, record.LocalDir, true)
	if err != nil {
		return err
	}
	createdConnection, err := ensureCloudIntegration(cloudCreds.APIURL, record.ID, joined.Token, provider, requestedBackend, record.LocalDir, *timeout, !*noOpen, stdout)
	if err != nil {
		return err
	}
	relayWorkspaceID := relayWorkspaceIDForRecord(record, joined)
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
		if err := runAtlassianSitePicker(cloudCreds.APIURL, record.ID, joined.Token, provider, stdin, stdout); err != nil {
			return err
		}
	}
	return waitForInitialSync(joined.RelayfileURL, joined.Token, relayWorkspaceID, provider, record.LocalDir, *timeout, stdout)
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
	relayWorkspaceID := relayWorkspaceIDForRecord(record, joined)
	if relayWorkspaceID != "" && relayWorkspaceID != record.ID {
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
	joined, err := joinWorkspaceViaCloud(cloudCreds, record.ID, record.AgentName, record.Scopes)
	if err != nil {
		return err
	}
	client, err := newAPIClient(cloudCreds.APIURL, joined.Token)
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
	joined, err := joinWorkspaceViaCloud(cloudCreds, record.ID, record.AgentName, record.Scopes)
	if err != nil {
		return err
	}
	client, err := newAPIClient(cloudCreds.APIURL, joined.Token)
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

var errWritebackFailuresPresent = errors.New("writeback failures present")

func deadLetterDirFor(localDir string) string {
	return filepath.Join(localDir, ".relay", "dead-letter")
}

func deadLetterErrorPathFor(localDir, opID string) string {
	return filepath.Join(deadLetterDirFor(localDir), opID+".error.json")
}

func runWriteback(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("writeback subcommand is required: list, status, or retry")
	}
	switch args[0] {
	case "list":
		return runWritebackList(args[1:], stdout)
	case "status":
		return runWritebackStatus(args[1:], stdout)
	case "retry":
		return runWritebackRetry(args[1:], stdout)
	default:
		return fmt.Errorf("unknown writeback subcommand %q", args[0])
	}
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
	trimmed := strings.TrimSpace(value)
	if trimmed != "" {
		if local, ok := workspaceRecordByName(trimmed); ok {
			workspaceID := strings.TrimSpace(local.ID)
			if workspaceID == "" {
				workspaceID = local.Name
			}
			return workspaceID, local, nil
		}
		if local, ok := workspaceRecordByID(trimmed); ok {
			return strings.TrimSpace(local.ID), local, nil
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
		return errors.New("token is required; run relayfile login or set RELAYFILE_TOKEN")
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
	joined, err := joinWorkspaceViaCloud(cloudCreds, record.ID, record.AgentName, record.Scopes)
	if err != nil {
		return err
	}
	client, err := newAPIClient(cloudCreds.APIURL, joined.Token)
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

	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	tokenValue := resolveToken(*tokenOverride, creds)
	client, err := newAPIClient(resolveServer(*server, creds), tokenValue)
	if err != nil {
		return err
	}

	workspaceID, err := resolveWorkspaceIDWithToken(strings.TrimSpace(*workspaceName), tokenValue)
	if err != nil {
		return err
	}

	providers, err := resolvePullProviders(client, workspaceID, strings.TrimSpace(*provider))
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
		if err := client.postJSON(
			context.Background(),
			fmt.Sprintf("/v1/workspaces/%s/sync/refresh", url.PathEscape(workspaceID)),
			body,
			&queued,
		); err != nil {
			return fmt.Errorf("refresh %s: %w", p, err)
		}
		fmt.Fprintf(stdout, "%s refresh queued (%s)\n", p, defaultIfBlank(queued.ID, "queued"))
	}
	return nil
}

func resolvePullProviders(client *apiClient, workspaceID, requested string) ([]string, error) {
	if requested != "" {
		return []string{normalizeProviderID(requested)}, nil
	}
	status, err := fetchWorkspaceSyncStatus(client, workspaceID)
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
	joined, err := joinWorkspaceViaCloud(cloudCreds, workspaceID, record.AgentName, record.Scopes)
	if err != nil {
		return err
	}
	record, err = persistJoinedWorkspace(record, joined, cloudCreds.APIURL, "", true)
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
		return errors.New("no active workspace; pass WORKSPACE, set RELAYFILE_WORKSPACE, or run 'relayfile workspace use NAME'")
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
	if tokenValue == "" {
		return errors.New("token is required; run relayfile login or pass --token")
	}
	var err error
	switch fs.NArg() {
	case 0:
		workspaceID, err = resolveWorkspaceIDWithToken("", tokenValue)
		localDir = strings.TrimSpace(*localDirFlag)
		localDirExplicit = localDir != ""
	case 1:
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
		localDir = strings.TrimSpace(*localDirFlag)
		localDirExplicit = localDir != ""
	case 2:
		if strings.TrimSpace(*localDirFlag) != "" {
			return errors.New("local directory specified twice")
		}
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
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

	return runMountLoop(rootCtx, syncer, absLocalDir, workspaceID, strings.TrimRight(strings.TrimSpace(*server), "/"), *timeout, *interval, *intervalJitter, *websocketEnabled, *once, *daemonized, pidFile, logFile)
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
}

func prepareWorkspaceCommandClient(workspaceValue, serverFlag, tokenFlag string, requestedScopes []string) (*workspaceCommandClient, error) {
	creds, _ := loadCredentials()
	tokenValue := resolveToken(tokenFlag, creds)
	directToken := strings.TrimSpace(tokenFlag) != "" || strings.TrimSpace(os.Getenv("RELAYFILE_TOKEN")) != ""
	workspaceID, err := resolveWorkspaceIDWithToken(workspaceValue, tokenValue)
	if err != nil {
		return nil, err
	}
	record := workspaceRecordForCommand(workspaceValue, workspaceID)
	scopes := effectiveCommandScopes(record, requestedScopes)
	commandClient := &workspaceCommandClient{
		workspaceID: workspaceID,
		record:      record,
		scopes:      scopes,
		directToken: directToken,
	}

	tokenWorkspaceID := workspaceIDFromToken(tokenValue)
	shouldJoin := !directToken && (strings.TrimSpace(tokenValue) == "" || relayfileTokenNeedsRefresh(tokenValue) || (tokenWorkspaceID != "" && tokenWorkspaceID != workspaceID))
	if shouldJoin {
		if err := commandClient.refreshFromCloud(); err == nil {
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

func (c *workspaceCommandClient) refreshFromCloud() error {
	if c == nil {
		return errors.New("workspace command client is nil")
	}
	cloudCreds, err := loadCloudCredentials()
	if err != nil {
		return err
	}
	cloudCreds, err = refreshCloudCredentialsIfNeeded(cloudCreds)
	if err != nil {
		return err
	}
	joined, err := joinWorkspaceViaCloud(cloudCreds, c.workspaceID, c.record.AgentName, c.scopes)
	if err != nil {
		return err
	}
	requestedWorkspaceID := c.workspaceID
	if joinedWorkspaceID := strings.TrimSpace(joined.WorkspaceID); joinedWorkspaceID != "" {
		c.workspaceID = joinedWorkspaceID
	}
	record := c.record
	if strings.TrimSpace(record.ID) == "" {
		record.ID = strings.TrimSpace(requestedWorkspaceID)
	}
	record.Scopes = append([]string(nil), c.scopes...)
	record, err = persistJoinedWorkspace(record, joined, cloudCreds.APIURL, record.LocalDir, false)
	if err != nil {
		return err
	}
	client, err := newAPIClient(strings.TrimRight(joined.RelayfileURL, "/"), joined.Token)
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
	if refreshErr := c.refreshFromCloud(); refreshErr != nil {
		return body, contentType, err
	}
	return c.client.getBytes(ctx, pathForWorkspace(c.workspaceID))
}

func (c *workspaceCommandClient) getWorkspaceJSON(ctx context.Context, pathForWorkspace func(string) string, out any) error {
	err := c.client.getJSON(ctx, pathForWorkspace(c.workspaceID), out)
	if err == nil || c.directToken || !isAPIAuthError(err) {
		return err
	}
	if refreshErr := c.refreshFromCloud(); refreshErr != nil {
		return err
	}
	return c.client.getJSON(ctx, pathForWorkspace(c.workspaceID), out)
}

func isAPIAuthError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *apiError
	return errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden)
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
	latest, ok := latestCredentialModTime(credentialsPath(), cloudCredentialsPath())
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
	if _, err := os.Stat(cloudCredentialsPath()); errors.Is(err, os.ErrNotExist) {
		return "auth: server credentials only"
	}
	creds, err := loadCloudCredentials()
	if err != nil {
		return "auth: cloud credentials unreadable - run 'relayfile login'"
	}
	if strings.TrimSpace(creds.AccessToken) == "" {
		return "auth: cloud access token missing - run 'relayfile login'"
	}
	if expiry, ok := parseRFC3339(creds.RefreshTokenExpiresAt); ok && !now.Before(expiry) {
		return "auth: cloud session expired - run 'relayfile login'"
	}
	if expiry, ok := parseRFC3339(creds.AccessTokenExpiresAt); ok {
		remaining := expiry.Sub(now)
		if remaining <= 0 {
			return "auth: access token expired - run 'relayfile login'"
		}
		if remaining <= 15*time.Minute {
			return fmt.Sprintf("auth: access token expires in %s", formatAuthDuration(remaining))
		}
	}
	return "auth: ok"
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
		if field == "mount" || field == "start" {
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

func runMountLoop(rootCtx context.Context, syncer *mountsync.Syncer, localDir, workspaceID, serverURL string, timeout, interval time.Duration, intervalJitter float64, websocketEnabled, once, daemonized bool, pidFile, logFile string) error {
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
	const degradedRecoveryInterval = time.Minute
	const degradedStallReason = "cloud session expired — run 'relayfile login' to refresh"

	enterDegraded := func() {
		if !degraded {
			degraded = true
			stallReason = degradedStallReason
			lastDegradedNotice = time.Time{}
			log.Printf("mount entering read-only degraded state: %s", degradedStallReason)
		}
	}
	exitDegraded := func() {
		if degraded {
			degraded = false
			stallReason = ""
			lastDegradedNotice = time.Time{}
			log.Printf("mount exiting degraded state; cloud session restored")
		}
	}
	maybePrintRecovery := func() {
		if !degraded {
			return
		}
		if time.Since(lastDegradedNotice) < degradedRecoveryInterval {
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
		cloudCreds, err := loadCloudCredentials()
		if err != nil {
			return nil
		}
		cloudCreds, err = refreshCloudCredentialsIfNeeded(cloudCreds)
		if err != nil {
			// Surface the typed error so the loop can enter degraded
			// state per A9. Other refresh errors stay non-fatal.
			if errors.Is(err, ErrCloudRefreshExpired) {
				return err
			}
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
			// Try to recover by re-running auth refresh. If creds are
			// still missing/expired, stay degraded and emit the
			// recovery notice on the configured cadence.
			if err := refreshMountAuth(true); err != nil {
				if errors.Is(err, ErrCloudRefreshExpired) {
					maybePrintRecovery()
					writeSnapshot()
					return err
				}
				log.Printf("mount degraded: refresh attempt failed: %v", err)
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
			if errors.Is(err, ErrCloudRefreshExpired) {
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
	if once {
		return initialErr
	}

	watcher, err := mountsync.NewFileWatcher(localDir, func(relativePath string, op fsnotify.Op) {
		if degraded {
			// Local edit observed while we have no usable cloud session.
			// Leave the dirty state in place so the next successful cycle
			// after re-login picks it up; do not attempt to push now.
			maybePrintRecovery()
			return
		}
		if err := withAuthRefresh(func(ctx context.Context) error {
			return syncer.HandleLocalChange(ctx, relativePath, op)
		}); err != nil {
			if errors.Is(err, ErrCloudRefreshExpired) {
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
