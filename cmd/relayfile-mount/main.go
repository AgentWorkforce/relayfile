package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/agentworkforce/relayfile/internal/delegatedauth"
	"github.com/agentworkforce/relayfile/internal/mountsync"
	"github.com/fsnotify/fsnotify"
)

const (
	mountModePoll           = "poll"
	mountModeFuse           = "fuse"
	localLayoutExact        = "exact"
	localLayoutScoped       = "scoped"
	syncModeMirror          = "mirror"
	syncModeWriteOnly       = "write-only"
	websocketReconcileEvery = 10
	minMountPollInterval    = 5 * time.Second
)

var errFuseModeUnavailable = errors.New("fuse mode is not available in this build")

type mountConfig struct {
	baseURL          string
	token            string
	credsFile        string
	workspaceID      string
	remotePath       string
	remotePaths      []string
	eventProvider    string
	localDir         string
	localLayout      string
	stateFile        string
	stateDir         string
	mountKind        string
	syncMode         string
	interval         time.Duration
	intervalJitter   float64
	timeout          time.Duration
	bootstrapTimeout time.Duration
	cursorTimeout    time.Duration
	forceFullRecon   bool
	websocketEnabled bool
	lazyRepos        bool
	lowMemory        bool
	pprofAddr        string
	memlogInterval   time.Duration
	logHTTPStatus    bool
	scopes           []string
	once             bool
	flushOutboxOnce  bool
	pushLocalOnce    bool
	mode             string
}

type pollRunner func(context.Context, mountConfig) error
type fuseRunner func(context.Context, mountConfig) error

var defaultFuseRunner fuseRunner = func(context.Context, mountConfig) error {
	return errFuseModeUnavailable
}

func main() {
	baseURL := flag.String("base-url", envOrDefault("RELAYFILE_BASE_URL", "http://127.0.0.1:8080"), "relayfile base URL")
	token := flag.String("token", strings.TrimSpace(os.Getenv("RELAYFILE_TOKEN")), "bearer token")
	credsFile := flag.String("creds-file", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_CREDS_FILE")), "JSON credentials file containing a relayfile bearer token; takes precedence over --token")
	workspaceID := flag.String("workspace", strings.TrimSpace(os.Getenv("RELAYFILE_WORKSPACE")), "workspace ID")
	var remotePaths repeatedStringFlag
	flag.Var(&remotePaths, "remote-path", "remote root path (may be repeated)")
	pathsFile := flag.String("paths-file", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_PATHS_FILE")), "file containing remote root paths, as JSON array or newline-separated list")
	eventProvider := flag.String("provider", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_PROVIDER")), "event provider filter")
	localDir := flag.String("local-dir", strings.TrimSpace(os.Getenv("RELAYFILE_LOCAL_DIR")), "local mirror directory")
	localLayout := flag.String("local-layout", envOrDefault("RELAYFILE_MOUNT_LOCAL_LAYOUT", localLayoutExact), "local directory layout: exact (local-dir is mirror root) or scoped (remote path is appended under local-dir)")
	stateFile := flag.String("state-file", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_STATE_FILE")), "state file path")
	stateDir := flag.String("state-dir", envOrDefault("RELAYFILE_MOUNT_STATE_DIR", mountsync.DefaultMountStateDir()), "directory for private mount state")
	mountKind := flag.String("mount-kind", envOrDefault("RELAYFILE_MOUNT_KIND", mountsync.MountKindDaemon), "private state identity kind: daemon, flush, or initial-sync")
	syncModeFlag := flag.String("sync-mode", envOrDefault("RELAYFILE_MOUNT_SYNC_MODE", syncModeMirror), "sync behavior: mirror (pull and push) or write-only (push local changes without mirroring provider history)")
	interval := flag.Duration("interval", durationEnv("RELAYFILE_MOUNT_INTERVAL", 30*time.Second), "sync interval")
	intervalJitter := flag.Float64("interval-jitter", floatEnv("RELAYFILE_MOUNT_INTERVAL_JITTER", 0.2), "sync interval jitter ratio (0.0-1.0)")
	timeout := flag.Duration("timeout", durationEnv("RELAYFILE_MOUNT_TIMEOUT", 15*time.Second), "per-sync timeout")
	bootstrapTimeout := flag.Duration("bootstrap-timeout", durationEnv("RELAYFILE_BOOTSTRAP_TIMEOUT", 0), "hard cap for the one-time/full-tree bootstrap pull (0 = unbounded while making progress)")
	cursorTimeout := flag.Duration("cursor-timeout", durationEnv("RELAYFILE_CURSOR_TIMEOUT", 60*time.Second), "independent timeout for events-cursor resolution")
	fullReconcile := flag.Bool("full-reconcile", boolEnv("RELAYFILE_FORCE_FULL_RECONCILE", false), "force one full reconcile regardless of bootstrap-complete state (escape hatch)")
	websocketEnabled := flag.Bool("websocket", boolEnv("RELAYFILE_MOUNT_WEBSOCKET", true), "enable websocket event streaming when available")
	lazyRepos := flag.Bool("lazy-repos", lazyReposEnv(), "lazily materialize GitHub repo subtrees on first access")
	lowMemory := flag.Bool("low-memory", boolEnv("RELAYFILE_MOUNT_LOW_MEMORY", false), "reduce mount memory use by omitting per-file public state and deferring content reads")
	pprofAddr := flag.String("pprof-addr", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_PPROF_ADDR")), "optional pprof listen address, e.g. 127.0.0.1:6060")
	memlogInterval := flag.Duration("memlog-interval", durationEnv("RELAYFILE_MOUNT_MEMLOG_INTERVAL", 0), "optional interval for logging runtime memory stats")
	logHTTPStatus := flag.Bool("log-http-status", boolEnv("RELAYFILE_MOUNT_LOG_HTTP_STATUS", false), "log Relayfile HTTP response statuses for mount observability")
	mode := flag.String("mode", envOrDefault("RELAYFILE_MOUNT_MODE", mountModePoll), "mount mode: poll (synced mirror, recommended) or fuse")
	fuse := flag.Bool("fuse", boolEnv("RELAYFILE_MOUNT_FUSE", false), "shortcut for --mode=fuse")
	once := flag.Bool("once", false, "run one sync cycle and exit")
	flushOutboxOnce := flag.Bool("flush-outbox-once", false, "flush durable writeback outbox once and exit without reconciling the local mirror")
	pushLocalOnce := flag.Bool("push-local-once", false, "ingest pending local writeback drafts (one pushLocal pass) then flush the outbox once and exit; no pullRemote/digest/reconcile — the teardown drain for last-moment drafts")
	flag.Parse()

	resolvedToken := strings.TrimSpace(*token)
	resolvedCredsFile := strings.TrimSpace(*credsFile)
	if resolvedCredsFile != "" {
		credsToken, err := readMountCredsToken(resolvedCredsFile)
		if err != nil {
			log.Fatalf("read creds-file: %v", err)
		}
		resolvedToken = credsToken
	}
	if resolvedToken == "" {
		log.Fatalf("token is required (--token, RELAYFILE_TOKEN, or --creds-file)")
	}
	if strings.TrimSpace(*workspaceID) == "" {
		log.Fatalf("workspace is required (--workspace or RELAYFILE_WORKSPACE)")
	}
	if strings.TrimSpace(*localDir) == "" {
		log.Fatalf("local-dir is required (--local-dir or RELAYFILE_LOCAL_DIR)")
	}
	if *interval <= 0 {
		*interval = 30 * time.Second
	}
	*interval = enforcePollIntervalFloor(*interval)
	if *timeout <= 0 {
		*timeout = 15 * time.Second
	}
	fileRemotePaths, err := readRemotePathsFile(*pathsFile)
	if err != nil {
		log.Fatalf("read paths-file: %v", err)
	}
	allRemotePaths := append(remotePaths.Values(), fileRemotePaths...)
	*intervalJitter = clampJitterRatio(*intervalJitter)
	resolvedMode, err := resolveMountMode(*mode, *fuse)
	if err != nil {
		log.Fatalf("invalid mount mode: %v", err)
	}
	resolvedLocalLayout, err := resolveLocalLayout(*localLayout)
	if err != nil {
		log.Fatalf("invalid local layout: %v", err)
	}
	resolvedSyncMode, err := resolveSyncMode(*syncModeFlag)
	if err != nil {
		log.Fatalf("invalid sync mode: %v", err)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := mountConfig{
		baseURL:          *baseURL,
		token:            resolvedToken,
		credsFile:        resolvedCredsFile,
		workspaceID:      strings.TrimSpace(*workspaceID),
		remotePath:       firstRemotePath(allRemotePaths, envOrDefault("RELAYFILE_REMOTE_PATH", "/")),
		remotePaths:      normalizeRemotePaths(allRemotePaths, envOrDefault("RELAYFILE_REMOTE_PATH", "/")),
		eventProvider:    strings.TrimSpace(*eventProvider),
		localDir:         *localDir,
		localLayout:      resolvedLocalLayout,
		stateFile:        *stateFile,
		stateDir:         *stateDir,
		mountKind:        *mountKind,
		syncMode:         resolvedSyncMode,
		interval:         *interval,
		intervalJitter:   *intervalJitter,
		timeout:          *timeout,
		bootstrapTimeout: *bootstrapTimeout,
		cursorTimeout:    *cursorTimeout,
		forceFullRecon:   *fullReconcile,
		websocketEnabled: *websocketEnabled,
		lazyRepos:        *lazyRepos,
		lowMemory:        *lowMemory,
		pprofAddr:        strings.TrimSpace(*pprofAddr),
		memlogInterval:   *memlogInterval,
		logHTTPStatus:    *logHTTPStatus,
		scopes:           parseTokenScopes(resolvedToken),
		once:             *once,
		flushOutboxOnce:  *flushOutboxOnce,
		pushLocalOnce:    *pushLocalOnce,
		mode:             resolvedMode,
	}

	if err := executeMount(rootCtx, cfg, runPollingMount, defaultFuseRunner); err != nil {
		if errors.Is(err, errFuseModeUnavailable) {
			log.Fatalf("failed to start %s mount: %v; rerun with --mode=%s", cfg.mode, err, mountModePoll)
		}
		log.Fatalf("failed to start %s mount: %v", cfg.mode, err)
	}
}

func resolveMountMode(mode string, fuse bool) (string, error) {
	if fuse {
		return mountModeFuse, nil
	}
	normalized := strings.ToLower(strings.TrimSpace(mode))
	if normalized == "" {
		return mountModePoll, nil
	}
	switch normalized {
	case mountModePoll, mountModeFuse:
		return normalized, nil
	default:
		return "", fmt.Errorf("%q (supported: %s, %s)", mode, mountModePoll, mountModeFuse)
	}
}

func resolveLocalLayout(layout string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(layout))
	if normalized == "" {
		return localLayoutExact, nil
	}
	switch normalized {
	case localLayoutExact, localLayoutScoped:
		return normalized, nil
	default:
		return "", fmt.Errorf("%q (supported: %s, %s)", layout, localLayoutExact, localLayoutScoped)
	}
}

func resolveSyncMode(mode string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	if normalized == "" {
		return syncModeMirror, nil
	}
	switch normalized {
	case syncModeMirror, syncModeWriteOnly:
		return normalized, nil
	default:
		return "", fmt.Errorf("%q (supported: %s, %s)", mode, syncModeMirror, syncModeWriteOnly)
	}
}

func executeMount(rootCtx context.Context, cfg mountConfig, runPoll pollRunner, runFuse fuseRunner) error {
	switch cfg.mode {
	case mountModePoll:
		return runPoll(rootCtx, cfg)
	case mountModeFuse:
		return runFuse(rootCtx, cfg)
	default:
		return fmt.Errorf("unsupported mount mode %q", cfg.mode)
	}
}

func runPollingMount(rootCtx context.Context, cfg mountConfig) error {
	return runPollingMountWithRunner(rootCtx, cfg, runSinglePollingMount)
}

func runPollingMountWithRunner(rootCtx context.Context, cfg mountConfig, run pollRunner) error {
	remotePaths := cfg.remotePaths
	if len(remotePaths) == 0 {
		remotePaths = []string{cfg.remotePath}
	}
	if cfg.localLayout == localLayoutScoped {
		return runScopedPollingMountsWithRunner(rootCtx, cfg, remotePaths, run)
	}
	if len(remotePaths) > 1 {
		return fmt.Errorf("multiple --remote-path values require --local-layout=%s", localLayoutScoped)
	}
	cfg.remotePath = normalizeMountRemotePath(remotePaths[0])
	cfg.remotePaths = nil
	return run(rootCtx, cfg)
}

func runScopedPollingMounts(rootCtx context.Context, cfg mountConfig, remotePaths []string) error {
	return runScopedPollingMountsWithRunner(rootCtx, cfg, remotePaths, runSinglePollingMount)
}

func runScopedPollingMountsWithRunner(
	rootCtx context.Context,
	cfg mountConfig,
	remotePaths []string,
	run pollRunner,
) error {
	type scopedMount struct {
		cfg mountConfig
	}
	scopedMounts := make([]scopedMount, 0, len(remotePaths))
	seen := map[string]struct{}{}
	if len(remotePaths) > 1 && strings.TrimSpace(cfg.stateFile) != "" {
		return fmt.Errorf("--state-file cannot be shared across multiple scoped mounts; use --state-dir instead")
	}
	for _, remotePath := range remotePaths {
		remotePath := normalizeMountRemotePath(remotePath)
		if _, ok := seen[remotePath]; ok {
			continue
		}
		seen[remotePath] = struct{}{}
		scoped := cfg
		scoped.remotePath = remotePath
		scoped.remotePaths = nil
		scoped.localDir = scopedLocalDir(cfg.localDir, remotePath)
		scoped.stateFile = cfg.stateFile
		if err := os.MkdirAll(scoped.localDir, 0o755); err != nil {
			return fmt.Errorf("create scoped local dir for %s: %w", remotePath, err)
		}
		scopedMounts = append(scopedMounts, scopedMount{cfg: scoped})
	}
	if len(scopedMounts) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	errCh := make(chan error, len(remotePaths))
	var wg sync.WaitGroup
	for _, mount := range scopedMounts {
		mount := mount
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- run(ctx, mount.cfg)
		}()
	}
	go func() {
		wg.Wait()
		close(errCh)
	}()
	var firstErr error
	for err := range errCh {
		if err != nil && firstErr == nil {
			firstErr = err
			cancel()
		}
	}
	return firstErr
}

func readRemotePathsFile(path string) ([]string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" {
		return nil, nil
	}
	var jsonPaths []string
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal(payload, &jsonPaths); err != nil {
			return nil, err
		}
		return jsonPaths, nil
	}
	var paths []string
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		paths = append(paths, line)
	}
	return paths, nil
}

func runSinglePollingMount(rootCtx context.Context, cfg mountConfig) error {
	// No whole-request Timeout: net/http enforces http.Client.Timeout
	// independent of context and would abort a long-but-progressing
	// bootstrap body read mid-stream. Cancellation is owned by the
	// per-cycle / bootstrap / cursor contexts; NewSyncHTTPClient wires a
	// transport that bounds connect/handshake/time-to-first-byte only.
	client := mountsync.NewHTTPClient(cfg.baseURL, cfg.token, mountsync.NewSyncHTTPClient())
	installCredsFileRefresh(client, cfg)
	if cfg.logHTTPStatus {
		client.SetHTTPStatusLogger(log.Default())
	}
	syncer, err := mountsync.NewSyncer(client, mountsync.SyncerOptions{
		WorkspaceID:        cfg.workspaceID,
		RemoteRoot:         cfg.remotePath,
		EventProvider:      cfg.eventProvider,
		LocalRoot:          cfg.localDir,
		StateFile:          cfg.stateFile,
		StateDir:           cfg.stateDir,
		MountKind:          cfg.mountKind,
		ValidateState:      true,
		Scopes:             cfg.scopes,
		WebSocket:          boolPtr(cfg.websocketEnabled),
		RootCtx:            rootCtx,
		Logger:             log.Default(),
		Mode:               cfg.mode,
		Interval:           cfg.interval,
		LazyRepos:          boolPtr(cfg.lazyRepos),
		LowMemory:          boolPtr(cfg.lowMemory),
		BootstrapTimeout:   cfg.bootstrapTimeout,
		CursorTimeout:      cfg.cursorTimeout,
		ForceFullReconcile: boolPtr(cfg.forceFullRecon),
		SyncMode:           cfg.syncMode,
	})
	if err != nil {
		return fmt.Errorf("initialize mount syncer: %w", err)
	}
	if _, err := mountsync.StartDiagnostics(rootCtx, cfg.pprofAddr, cfg.memlogInterval, log.Default()); err != nil {
		return fmt.Errorf("start diagnostics: %w", err)
	}
	if cfg.pushLocalOnce {
		ctx, cancel := context.WithTimeout(rootCtx, cfg.timeout)
		defer cancel()
		if err := syncer.PushLocalAndFlushOnce(ctx); err != nil {
			return fmt.Errorf("push local and flush once: %w", err)
		}
		log.Printf("local push + outbox flush completed")
		return nil
	}
	if cfg.flushOutboxOnce {
		ctx, cancel := context.WithTimeout(rootCtx, cfg.timeout)
		defer cancel()
		if err := syncer.FlushOutboxOnce(ctx); err != nil {
			return fmt.Errorf("flush outbox once: %w", err)
		}
		log.Printf("outbox flush completed")
		return nil
	}
	log.Printf("%s", mountStartupLogLine(cfg))
	log.Printf("Mirror started at %s. Sync interval %s +/- %.0f%%. Public state: %s", cfg.localDir, cfg.interval.Round(time.Second), cfg.intervalJitter*100, filepath.Join(cfg.localDir, ".relay", "state.json"))

	run := func(reconcile bool) {
		ctx, cancel := context.WithTimeout(rootCtx, cfg.timeout)
		defer cancel()
		var err error
		if reconcile {
			err = syncer.Reconcile(ctx)
		} else {
			err = syncer.SyncOnce(ctx)
		}
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				if synced, total, ok := readBootstrapProgress(cfg.localDir); ok {
					log.Printf("mount bootstrapping: %d/%d files (in progress)", synced, total)
					return
				}
			}
			log.Printf("mount sync cycle failed: %v", err)
			return
		}
		log.Printf("mount sync cycle completed")
	}

	run(true)
	if cfg.once {
		return nil
	}

	watcher, err := mountsync.NewFileWatcher(cfg.localDir, func(relativePath string, op fsnotify.Op) {
		ctx, cancel := context.WithTimeout(rootCtx, cfg.timeout)
		defer cancel()
		if err := syncer.HandleLocalChange(ctx, relativePath, op); err != nil {
			log.Printf("mount local change failed: %v", err)
		}
	})
	if err != nil {
		return fmt.Errorf("create file watcher: %w", err)
	}
	if err := watcher.Start(rootCtx); err != nil {
		return fmt.Errorf("start file watcher: %w", err)
	}
	defer watcher.Close()

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	timer := time.NewTimer(jitteredIntervalWithSample(cfg.interval, cfg.intervalJitter, rng.Float64()))
	defer timer.Stop()
	wsTicker := time.NewTicker(mountsync.DefaultWebSocketMaintenanceEvery)
	defer wsTicker.Stop()
	cycle := 0
	for {
		select {
		case <-rootCtx.Done():
			log.Printf("mount sync stopping: %v", rootCtx.Err())
			return nil
		case <-wsTicker.C:
			if mountWebSocketEnabled(cfg) {
				ctx, cancel := context.WithTimeout(rootCtx, cfg.timeout)
				if err := syncer.MaintainWebSocket(ctx); err != nil {
					log.Printf("websocket unavailable; using polling sync: %v", err)
				}
				cancel()
			}
		case <-timer.C:
			cycle++
			reconcile := shouldReconcileMountCycle(mountWebSocketEnabled(cfg), cycle)
			if reconcile {
				run(true)
			}
			timer.Reset(jitteredIntervalWithSample(cfg.interval, cfg.intervalJitter, rng.Float64()))
		}
	}
}

type mountCredsFile struct {
	Token                   string `json:"token"`
	AccessToken             string `json:"accessToken,omitempty"`
	RelayfileToken          string `json:"relayfileToken,omitempty"`
	RefreshToken            string `json:"refreshToken,omitempty"`
	RelayfileRefreshToken   string `json:"relayfileRefreshToken,omitempty"`
	RelayauthURL            string `json:"relayauthUrl,omitempty"`
	RefreshURL              string `json:"refreshUrl,omitempty"`
	AccessTokenExpiresAt    string `json:"accessTokenExpiresAt,omitempty"`
	RelayfileTokenExpiresAt string `json:"relayfileTokenExpiresAt,omitempty"`
}

func readMountCredsToken(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is required")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var creds mountCredsFile
	if err := json.Unmarshal(payload, &creds); err != nil {
		return "", err
	}
	token := firstNonEmpty(creds.Token, creds.AccessToken, creds.RelayfileToken)
	if token == "" {
		return "", errors.New("missing token")
	}
	return token, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func installCredsFileRefresh(client *mountsync.HTTPClient, cfg mountConfig) {
	credsFile := strings.TrimSpace(cfg.credsFile)
	if client == nil || credsFile == "" {
		return
	}
	client.SetTokenRefreshFunc(func(currentToken string) (string, bool, error) {
		bundle, loadErr := delegatedauth.Load(credsFile)
		if loadErr == nil && bundle.RotationToken() != "" {
			renewed, changed, err := delegatedauth.RenewFile(context.Background(), nil, credsFile, delegatedauth.DefaultRefreshTimeout)
			if err != nil {
				log.Printf("relayfile delegated credential refresh failed: %v", err)
				return "", false, err
			}
			return renewed.BearerToken(), changed || renewed.BearerToken() != strings.TrimSpace(currentToken), nil
		}
		token, err := readMountCredsToken(credsFile)
		if err != nil {
			log.Printf("relayfile creds-file refresh failed: %v", err)
			return "", false, err
		}
		changed := token != strings.TrimSpace(currentToken)
		return token, changed, nil
	})
}

type repeatedStringFlag []string

func (f *repeatedStringFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *repeatedStringFlag) Set(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	*f = append(*f, trimmed)
	return nil
}

func (f repeatedStringFlag) Values() []string {
	return append([]string(nil), f...)
}

func firstRemotePath(paths []string, fallback string) string {
	normalized := normalizeRemotePaths(paths, fallback)
	if len(normalized) == 0 {
		return "/"
	}
	return normalized[0]
}

func normalizeRemotePaths(paths []string, fallback string) []string {
	if len(paths) == 0 {
		paths = []string{fallback}
	}
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		cleaned := normalizeMountRemotePath(path)
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		normalized = append(normalized, cleaned)
	}
	if len(normalized) == 0 {
		return []string{"/"}
	}
	return normalized
}

func normalizeMountRemotePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || trimmed == "/" {
		return "/"
	}
	trimmed = strings.ReplaceAll(trimmed, "\\", "/")
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	cleaned := filepath.Clean(trimmed)
	if cleaned == "." || cleaned == string(filepath.Separator) {
		return "/"
	}
	return filepath.ToSlash(cleaned)
}

func scopedLocalDir(localRoot, remotePath string) string {
	remotePath = normalizeMountRemotePath(remotePath)
	if remotePath == "/" {
		return localRoot
	}
	return filepath.Join(localRoot, filepath.FromSlash(strings.TrimPrefix(remotePath, "/")))
}

func mountStartupLogLine(cfg mountConfig) string {
	layout := cfg.localLayout
	if layout == "" {
		layout = localLayoutExact
	}
	syncMode := cfg.syncMode
	if syncMode == "" {
		syncMode = syncModeMirror
	}
	return fmt.Sprintf(
		"mount layout=%s remote=%s local=%s sync=%s mode=%s state=%s",
		layout,
		normalizeMountRemotePath(cfg.remotePath),
		cfg.localDir,
		syncMode,
		cfg.mode,
		filepath.Join(cfg.localDir, ".relay", "state.json"),
	)
}

// readBootstrapProgress reads the in-progress bootstrap block from the
// mountsync public state file. ok is false when there is no bootstrap in
// progress (or the file is missing/unparseable).
func readBootstrapProgress(localDir string) (synced, total int, ok bool) {
	if strings.TrimSpace(localDir) == "" {
		return 0, 0, false
	}
	payload, err := os.ReadFile(filepath.Join(localDir, ".relay", "state.json"))
	if err != nil {
		return 0, 0, false
	}
	var view struct {
		Bootstrap *struct {
			FilesSynced int `json:"filesSynced"`
			FilesTotal  int `json:"filesTotal"`
		} `json:"bootstrap"`
	}
	if err := json.Unmarshal(payload, &view); err != nil || view.Bootstrap == nil {
		return 0, 0, false
	}
	return view.Bootstrap.FilesSynced, view.Bootstrap.FilesTotal, true
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

func lazyReposEnv() bool {
	return boolEnv("RELAYFILE_LAZY_REPOS", boolEnv("RELAYFILE_MOUNT_LAZY_GITHUB_REPOS", false))
}

func boolPtr(value bool) *bool {
	return &value
}

func parseTokenScopes(token string) []string {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}

	claimsBytes, err := decodeBase64URLSegment(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return nil
	}

	rawScopes, ok := claims["scopes"]
	if !ok {
		rawScopes, ok = claims["scope"]
	}
	if !ok {
		return nil
	}
	return normalizeTokenScopes(rawScopes)
}

func decodeBase64URLSegment(segment string) ([]byte, error) {
	segment = strings.TrimSpace(segment)
	segment = strings.TrimRight(segment, "=")

	decoded, err := base64.RawURLEncoding.DecodeString(segment)
	if err == nil {
		return decoded, nil
	}

	if rem := len(segment) % 4; rem != 0 {
		segment += strings.Repeat("=", 4-rem)
	}
	return base64.URLEncoding.DecodeString(segment)
}

func normalizeTokenScopes(raw any) []string {
	seen := map[string]struct{}{}
	values := make([]string, 0)

	addScope := func(scope string) {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			return
		}
		if _, exists := seen[scope]; exists {
			return
		}
		seen[scope] = struct{}{}
		values = append(values, scope)
	}

	switch v := raw.(type) {
	case []any:
		for _, scope := range v {
			strScope, ok := scope.(string)
			if !ok {
				continue
			}
			addScope(strScope)
		}
	case []string:
		for _, scope := range v {
			addScope(scope)
		}
	case string:
		for _, scope := range strings.FieldsFunc(v, func(r rune) bool {
			return r == ' ' || r == ',' || r == '\t' || r == '\n' || r == '\r'
		}) {
			addScope(scope)
		}
	}
	return values
}

func shouldReconcileMountCycle(websocketEnabled bool, cycle int) bool {
	return !websocketEnabled || cycle%websocketReconcileEvery == 0
}

func mountWebSocketEnabled(cfg mountConfig) bool {
	return cfg.websocketEnabled && cfg.syncMode != syncModeWriteOnly
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
