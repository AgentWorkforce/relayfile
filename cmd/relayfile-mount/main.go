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

	"github.com/agentworkforce/relayfile/internal/mountsync"
	"github.com/fsnotify/fsnotify"
)

const (
	mountModePoll           = "poll"
	mountModeFuse           = "fuse"
	websocketReconcileEvery = 10
	minMountPollInterval    = 5 * time.Second
)

var errFuseModeUnavailable = errors.New("fuse mode is not available in this build")

type mountConfig struct {
	baseURL          string
	token            string
	workspaceID      string
	remotePath       string
	remotePaths      []string
	eventProvider    string
	localDir         string
	stateFile        string
	stateDir         string
	mountKind        string
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
	scopes           []string
	once             bool
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
	workspaceID := flag.String("workspace", strings.TrimSpace(os.Getenv("RELAYFILE_WORKSPACE")), "workspace ID")
	var remotePaths repeatedStringFlag
	flag.Var(&remotePaths, "remote-path", "remote root path (may be repeated)")
	pathsFile := flag.String("paths-file", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_PATHS_FILE")), "file containing remote root paths, as JSON array or newline-separated list")
	eventProvider := flag.String("provider", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_PROVIDER")), "event provider filter")
	localDir := flag.String("local-dir", strings.TrimSpace(os.Getenv("RELAYFILE_LOCAL_DIR")), "local mirror directory")
	stateFile := flag.String("state-file", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_STATE_FILE")), "state file path")
	stateDir := flag.String("state-dir", envOrDefault("RELAYFILE_MOUNT_STATE_DIR", mountsync.DefaultMountStateDir()), "directory for private mount state")
	mountKind := flag.String("mount-kind", envOrDefault("RELAYFILE_MOUNT_KIND", mountsync.MountKindDaemon), "private state identity kind: daemon, flush, or initial-sync")
	interval := flag.Duration("interval", durationEnv("RELAYFILE_MOUNT_INTERVAL", 30*time.Second), "sync interval")
	intervalJitter := flag.Float64("interval-jitter", floatEnv("RELAYFILE_MOUNT_INTERVAL_JITTER", 0.2), "sync interval jitter ratio (0.0-1.0)")
	timeout := flag.Duration("timeout", durationEnv("RELAYFILE_MOUNT_TIMEOUT", 15*time.Second), "per-sync timeout")
	bootstrapTimeout := flag.Duration("bootstrap-timeout", durationEnv("RELAYFILE_BOOTSTRAP_TIMEOUT", 0), "hard cap for the one-time/full-tree bootstrap pull (0 = unbounded while making progress)")
	cursorTimeout := flag.Duration("cursor-timeout", durationEnv("RELAYFILE_CURSOR_TIMEOUT", 20*time.Second), "independent timeout for events-cursor resolution")
	fullReconcile := flag.Bool("full-reconcile", boolEnv("RELAYFILE_FORCE_FULL_RECONCILE", false), "force one full reconcile regardless of bootstrap-complete state (escape hatch)")
	websocketEnabled := flag.Bool("websocket", boolEnv("RELAYFILE_MOUNT_WEBSOCKET", true), "enable websocket event streaming when available")
	lazyRepos := flag.Bool("lazy-repos", lazyReposEnv(), "lazily materialize GitHub repo subtrees on first access")
	lowMemory := flag.Bool("low-memory", boolEnv("RELAYFILE_MOUNT_LOW_MEMORY", false), "reduce mount memory use by omitting per-file public state and deferring content reads")
	pprofAddr := flag.String("pprof-addr", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_PPROF_ADDR")), "optional pprof listen address, e.g. 127.0.0.1:6060")
	memlogInterval := flag.Duration("memlog-interval", durationEnv("RELAYFILE_MOUNT_MEMLOG_INTERVAL", 0), "optional interval for logging runtime memory stats")
	mode := flag.String("mode", envOrDefault("RELAYFILE_MOUNT_MODE", mountModePoll), "mount mode: poll (synced mirror, recommended) or fuse")
	fuse := flag.Bool("fuse", boolEnv("RELAYFILE_MOUNT_FUSE", false), "shortcut for --mode=fuse")
	once := flag.Bool("once", false, "run one sync cycle and exit")
	flag.Parse()

	if strings.TrimSpace(*token) == "" {
		log.Fatalf("token is required (--token or RELAYFILE_TOKEN)")
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

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := mountConfig{
		baseURL:          *baseURL,
		token:            strings.TrimSpace(*token),
		workspaceID:      strings.TrimSpace(*workspaceID),
		remotePath:       firstRemotePath(allRemotePaths, envOrDefault("RELAYFILE_REMOTE_PATH", "/")),
		remotePaths:      normalizeRemotePaths(allRemotePaths, envOrDefault("RELAYFILE_REMOTE_PATH", "/")),
		eventProvider:    strings.TrimSpace(*eventProvider),
		localDir:         *localDir,
		stateFile:        *stateFile,
		stateDir:         *stateDir,
		mountKind:        *mountKind,
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
		scopes:           parseTokenScopes(strings.TrimSpace(*token)),
		once:             *once,
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
	remotePaths := cfg.remotePaths
	if len(remotePaths) == 0 {
		remotePaths = []string{cfg.remotePath}
	}
	if len(remotePaths) > 1 || (len(remotePaths) == 1 && normalizeMountRemotePath(remotePaths[0]) != "/") {
		return runScopedPollingMounts(rootCtx, cfg, remotePaths)
	}
	return runSinglePollingMount(rootCtx, cfg)
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
	})
	if err != nil {
		return fmt.Errorf("initialize mount syncer: %w", err)
	}
	if _, err := mountsync.StartDiagnostics(rootCtx, cfg.pprofAddr, cfg.memlogInterval, log.Default()); err != nil {
		return fmt.Errorf("start diagnostics: %w", err)
	}
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
			if cfg.websocketEnabled {
				ctx, cancel := context.WithTimeout(rootCtx, cfg.timeout)
				if err := syncer.MaintainWebSocket(ctx); err != nil {
					log.Printf("websocket unavailable; using polling sync: %v", err)
				}
				cancel()
			}
		case <-timer.C:
			cycle++
			reconcile := shouldReconcileMountCycle(cfg.websocketEnabled, cycle)
			if reconcile {
				run(true)
			}
			timer.Reset(jitteredIntervalWithSample(cfg.interval, cfg.intervalJitter, rng.Float64()))
		}
	}
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
