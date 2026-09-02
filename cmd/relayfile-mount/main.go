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
	"github.com/agentworkforce/relayfile/internal/mountlease"
	"github.com/agentworkforce/relayfile/internal/mountscope"
	"github.com/agentworkforce/relayfile/internal/mountsync"
	"github.com/fsnotify/fsnotify"
)

const (
	mountModePoll        = "poll"
	mountModeFuse        = "fuse"
	localLayoutExact     = mountscope.LayoutExact
	localLayoutScoped    = mountscope.LayoutScoped
	syncModeMirror       = "mirror"
	syncModePullOnly     = "pull-only"
	syncModeWriteOnly    = "write-only"
	minMountPollInterval = 5 * time.Second
)

var errFuseModeUnavailable = errors.New("fuse mode is not available in this build")

type mountConfig struct {
	baseURL               string
	token                 string
	credsFile             string
	workspaceID           string
	remotePath            string
	remotePaths           []string
	eventProvider         string
	localDir              string
	localLayout           string
	stateFile             string
	stateDir              string
	mountKind             string
	syncMode              string
	interval              time.Duration
	intervalJitter        float64
	timeout               time.Duration
	bootstrapTimeout      time.Duration
	bootstrapMaxFiles     int
	fullPullMinInterval   time.Duration
	cursorTimeout         time.Duration
	forceFullRecon        bool
	websocketEnabled      bool
	fileSettleDelay       time.Duration
	atomicSaveSettleDelay time.Duration
	changeBatchWindow     time.Duration
	lazyRepos             bool
	lazySkipUntrackedPush bool
	lowMemory             bool
	pprofAddr             string
	memlogInterval        time.Duration
	logHTTPStatus         bool
	scopes                []string
	scopedChild           bool
	once                  bool
	notifyFlush           bool
	flushOutboxOnce       bool
	pushLocalOnce         bool
	checkpointAndSeal     bool
	checkpointSession     string
	checkpointGeneration  uint64
	checkpointSealTTL     time.Duration
	mode                  string
	fuseContentTTL        time.Duration
	flushReq              <-chan struct{}
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
	var remotePaths mountscope.StringListFlag
	flag.Var(&remotePaths, "remote-path", "remote root path (may be repeated)")
	pathsFile := flag.String("paths-file", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_PATHS_FILE")), "file containing remote root paths, as JSON array or newline-separated list")
	eventProvider := flag.String("provider", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_PROVIDER")), "event provider filter")
	localDir := flag.String("local-dir", strings.TrimSpace(os.Getenv("RELAYFILE_LOCAL_DIR")), "local mirror directory")
	localLayout := flag.String("local-layout", envOrDefault("RELAYFILE_MOUNT_LOCAL_LAYOUT", localLayoutExact), "local directory layout: exact (scoped layout is temporarily unavailable until operator surfaces are ready)")
	stateFile := flag.String("state-file", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_STATE_FILE")), "state file path")
	stateDir := flag.String("state-dir", envOrDefault("RELAYFILE_MOUNT_STATE_DIR", mountsync.DefaultMountStateDir()), "directory for private mount state")
	mountKind := flag.String("mount-kind", envOrDefault("RELAYFILE_MOUNT_KIND", mountsync.MountKindDaemon), "private state identity kind: daemon, flush, or initial-sync")
	syncModeFlag := flag.String("sync-mode", envOrDefault("RELAYFILE_MOUNT_SYNC_MODE", syncModeMirror), "sync behavior: mirror (pull and push), pull-only (poll mode only; mirror remote changes without writeback), or write-only (push local changes without mirroring provider history)")
	interval := flag.Duration("interval", durationEnv("RELAYFILE_MOUNT_INTERVAL", 30*time.Second), "sync interval")
	intervalJitter := flag.Float64("interval-jitter", floatEnv("RELAYFILE_MOUNT_INTERVAL_JITTER", 0.2), "sync interval jitter ratio (0.0-1.0)")
	timeout := flag.Duration("timeout", durationEnv("RELAYFILE_MOUNT_TIMEOUT", 15*time.Second), "per-sync timeout")
	bootstrapTimeout := flag.Duration("bootstrap-timeout", durationEnv("RELAYFILE_BOOTSTRAP_TIMEOUT", 0), "hard cap for the one-time/full-tree bootstrap pull (0 = unbounded while making progress)")
	bootstrapMaxFiles := flag.Int("bootstrap-max-files-per-cycle", intEnv("RELAYFILE_BOOTSTRAP_MAX_FILES_PER_CYCLE", 2000), "maximum files materialized per resumable tree-bootstrap cycle (-1 = legacy unbounded tree behavior)")
	fullPullMinIntervalArg := flag.String("full-pull-min-interval", durationEnv("RELAYFILE_MOUNT_FULL_PULL_MIN_INTERVAL", 24*time.Hour).String(), "minimum wall-clock interval between completed periodic full-tree audits (-1 disables the time guard)")
	cursorTimeout := flag.Duration("cursor-timeout", durationEnv("RELAYFILE_CURSOR_TIMEOUT", 60*time.Second), "independent timeout for events-cursor resolution")
	fullReconcile := flag.Bool("full-reconcile", boolEnv("RELAYFILE_FORCE_FULL_RECONCILE", false), "force one full reconcile regardless of bootstrap-complete state (escape hatch)")
	websocketEnabled := flag.Bool("websocket", boolEnv("RELAYFILE_MOUNT_WEBSOCKET", true), "enable websocket event streaming when available")
	fileSettleDelay := flag.Duration("file-settle-delay", durationEnv("RELAYFILE_MOUNT_FILE_SETTLE_DELAY", mountsync.DefaultFileChangeSettleDelay), "settle delay for noisy in-place file writes")
	atomicSaveSettleDelay := flag.Duration("atomic-save-settle-delay", durationEnv("RELAYFILE_MOUNT_ATOMIC_SAVE_SETTLE_DELAY", mountsync.DefaultAtomicSaveSettleDelay), "settle delay for committed atomic rename/create/remove events")
	changeBatchWindow := flag.Duration("change-batch-window", durationEnv("RELAYFILE_MOUNT_CHANGE_BATCH_WINDOW", 5*time.Millisecond), "quiet window for grouping near-simultaneous changed paths into one bulk write")
	lazyRepos := flag.Bool("lazy-repos", lazyReposEnv(), "lazily materialize GitHub repo subtrees on first access")
	lazySkipUntrackedPush := flag.Bool("lazy-skip-untracked-push", boolEnv("RELAYFILE_LAZY_SKIP_UNTRACKED_PUSH", true), "when --lazy-repos is set, skip pushLocal for local files under a lazy GitHub repo subtree that this daemon does not track in its state (e.g. pre-pulled by an isolated non-lazy mount); writeback drafts/commands (including arbitrary-name GitHub adapter create leaves, e.g. issues/comments/reviews/replies drafts and merge.json) are exempt and still push. Trade-off: this also skips edits to untracked numeric/meta canonical leaves (the adapter's PATCH-by-editing-record surface, e.g. pulls/<n>/reviews/<id>.json) — already nonfunctional under lazy mounts today since those records never materialize locally without an external pre-pull, so nothing that works regresses")
	lowMemory := flag.Bool("low-memory", boolEnv("RELAYFILE_MOUNT_LOW_MEMORY", false), "reduce mount memory use by omitting per-file public state and deferring content reads")
	pprofAddr := flag.String("pprof-addr", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_PPROF_ADDR")), "optional pprof listen address, e.g. 127.0.0.1:6060")
	memlogInterval := flag.Duration("memlog-interval", durationEnv("RELAYFILE_MOUNT_MEMLOG_INTERVAL", 0), "optional interval for logging runtime memory stats")
	logHTTPStatus := flag.Bool("log-http-status", boolEnv("RELAYFILE_MOUNT_LOG_HTTP_STATUS", false), "log Relayfile HTTP response statuses for mount observability")
	mode := flag.String("mode", envOrDefault("RELAYFILE_MOUNT_MODE", mountModePoll), "mount mode: poll (synced mirror, recommended) or fuse")
	fuse := flag.Bool("fuse", boolEnv("RELAYFILE_MOUNT_FUSE", false), "shortcut for --mode=fuse")
	fuseContentTTL := flag.Duration("fuse-content-ttl", durationEnv("RELAYFILE_MOUNT_FUSE_CONTENT_TTL", 0), "FUSE in-memory file content cache TTL (default 30s; 0 = use default)")
	once := flag.Bool("once", false, "run one sync cycle and exit")
	notifyFlush := flag.Bool("notify-flush", false, "ask the already-running mount daemon for this local root to run one reconcile and exit; does not take the mount lease")
	flushOutboxOnce := flag.Bool("flush-outbox-once", false, "flush durable writeback outbox once and exit without reconciling the local mirror")
	pushLocalOnce := flag.Bool("push-local-once", false, "ingest pending local writeback drafts (one pushLocal pass) then flush the outbox once and exit; no pullRemote/digest/reconcile — the teardown drain for last-moment drafts")
	checkpointAndSeal := flag.Bool("checkpoint-and-seal", false, "drain a managed mount, verify durable convergence, emit a one-use server checkpoint seal as JSON, and exit")
	checkpointSession := flag.String("checkpoint-session", "", "session identifier bound into --checkpoint-and-seal")
	checkpointGeneration := flag.Uint64("checkpoint-generation", 0, "strictly increasing migration generation bound into --checkpoint-and-seal")
	checkpointSealTTL := flag.Duration("checkpoint-seal-ttl", mountsync.DefaultCheckpointSealTTL, "one-use checkpoint seal lifetime (maximum 5m)")
	flag.Parse()
	fullPullMinInterval, err := parseDurationWithNegativeOne(*fullPullMinIntervalArg)
	if err != nil {
		log.Fatalf("invalid --full-pull-min-interval: %v", err)
	}

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
	fileRemotePaths, err := mountscope.ReadPathsFile(*pathsFile)
	if err != nil {
		log.Fatalf("read paths-file: %v", err)
	}
	if err := mountscope.ValidateExplicitPathsFile(*pathsFile, fileRemotePaths, remotePaths.Values()); err != nil {
		log.Fatalf("invalid paths-file: %v", err)
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
	if err := validateCLIRequestedLocalLayout(resolvedLocalLayout); err != nil {
		log.Fatalf("unsupported local layout: %v", err)
	}
	resolvedSyncMode, err := resolveSyncMode(*syncModeFlag)
	if err != nil {
		log.Fatalf("invalid sync mode: %v", err)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := mountConfig{
		baseURL:               *baseURL,
		token:                 resolvedToken,
		credsFile:             resolvedCredsFile,
		workspaceID:           strings.TrimSpace(*workspaceID),
		remotePath:            mountscope.FirstPath(allRemotePaths, envOrDefault("RELAYFILE_REMOTE_PATH", "/")),
		remotePaths:           mountscope.NormalizePaths(allRemotePaths, envOrDefault("RELAYFILE_REMOTE_PATH", "/")),
		eventProvider:         strings.TrimSpace(*eventProvider),
		localDir:              *localDir,
		localLayout:           resolvedLocalLayout,
		stateFile:             *stateFile,
		stateDir:              *stateDir,
		mountKind:             *mountKind,
		syncMode:              resolvedSyncMode,
		interval:              *interval,
		intervalJitter:        *intervalJitter,
		timeout:               *timeout,
		bootstrapTimeout:      *bootstrapTimeout,
		bootstrapMaxFiles:     *bootstrapMaxFiles,
		fullPullMinInterval:   fullPullMinInterval,
		cursorTimeout:         *cursorTimeout,
		forceFullRecon:        *fullReconcile,
		websocketEnabled:      *websocketEnabled,
		fileSettleDelay:       *fileSettleDelay,
		atomicSaveSettleDelay: *atomicSaveSettleDelay,
		changeBatchWindow:     *changeBatchWindow,
		lazyRepos:             *lazyRepos,
		lazySkipUntrackedPush: *lazySkipUntrackedPush,
		lowMemory:             *lowMemory,
		pprofAddr:             strings.TrimSpace(*pprofAddr),
		memlogInterval:        *memlogInterval,
		logHTTPStatus:         *logHTTPStatus,
		scopes:                parseTokenScopes(resolvedToken),
		once:                  *once,
		notifyFlush:           *notifyFlush,
		flushOutboxOnce:       *flushOutboxOnce,
		pushLocalOnce:         *pushLocalOnce,
		checkpointAndSeal:     *checkpointAndSeal,
		checkpointSession:     strings.TrimSpace(*checkpointSession),
		checkpointGeneration:  *checkpointGeneration,
		checkpointSealTTL:     *checkpointSealTTL,
		mode:                  resolvedMode,
		fuseContentTTL:        *fuseContentTTL,
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
	return mountscope.ResolveLayout(layout)
}

// Scoped runtime state is implemented below this CLI boundary, but its
// operator surfaces are not yet complete. Refuse the user-facing capability
// until status/list/retry can see every scoped child state location.
func validateCLIRequestedLocalLayout(layout string) error {
	if layout == localLayoutScoped {
		return fmt.Errorf("--local-layout=%s is temporarily unavailable until scoped operator surfaces are ready; use --local-layout=%s", localLayoutScoped, localLayoutExact)
	}
	return nil
}

func resolveSyncMode(mode string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	if normalized == "" {
		return syncModeMirror, nil
	}
	switch normalized {
	case syncModeMirror, syncModePullOnly, syncModeWriteOnly:
		return normalized, nil
	default:
		return "", fmt.Errorf("%q (supported: %s, %s, %s)", mode, syncModeMirror, syncModePullOnly, syncModeWriteOnly)
	}
}

func executeMount(rootCtx context.Context, cfg mountConfig, runPoll pollRunner, runFuse fuseRunner) error {
	if cfg.checkpointAndSeal && cfg.mode != mountModePoll {
		return fmt.Errorf("--checkpoint-and-seal requires --mode=%s", mountModePoll)
	}
	if cfg.checkpointAndSeal && (cfg.once || cfg.notifyFlush || cfg.flushOutboxOnce || cfg.pushLocalOnce) {
		return errors.New("--checkpoint-and-seal cannot be combined with --once, --notify-flush, --flush-outbox-once, or --push-local-once")
	}
	if cfg.checkpointAndSeal && (cfg.checkpointSession == "" || cfg.checkpointGeneration == 0) {
		return errors.New("--checkpoint-and-seal requires --checkpoint-session and a positive --checkpoint-generation")
	}
	if cfg.checkpointAndSeal && (cfg.checkpointSealTTL < time.Second || cfg.checkpointSealTTL > mountsync.MaxCheckpointSealTTL) {
		return fmt.Errorf("--checkpoint-seal-ttl must be between 1s and %s", mountsync.MaxCheckpointSealTTL)
	}
	if cfg.mode == mountModeFuse && cfg.syncMode == syncModePullOnly {
		return fmt.Errorf("--sync-mode=%s is not supported with --mode=%s; use --mode=%s", syncModePullOnly, mountModeFuse, mountModePoll)
	}
	if cfg.notifyFlush {
		if cfg.once || cfg.flushOutboxOnce || cfg.pushLocalOnce {
			return fmt.Errorf("--notify-flush cannot be combined with --once, --flush-outbox-once, or --push-local-once")
		}
		return notifyRunningMountFlush(rootCtx, cfg)
	}
	// SIGUSR1 terminates by default. The lease is about to be published, so
	// either listen for --notify-flush or ignore the signal. One-shot and
	// FUSE supervisors ignore it; a poll daemon queues it before Acquire.
	if acceptsNotifyFlush(cfg) {
		cfg.flushReq = listenFlushRequests(rootCtx)
	} else {
		ignoreNotifyFlushSignal()
	}
	if strings.TrimSpace(cfg.baseURL) != "" && strings.TrimSpace(cfg.workspaceID) != "" {
		lease, err := mountlease.Acquire(cfg.baseURL, cfg.workspaceID, cfg.localDir)
		if err != nil {
			return fmt.Errorf("acquire workspace mount lease: %w", err)
		}
		defer lease.Release()
	}
	switch cfg.mode {
	case mountModePoll:
		return runPoll(rootCtx, cfg)
	case mountModeFuse:
		remotePaths := cfg.remotePaths
		if len(remotePaths) == 0 {
			remotePaths = []string{cfg.remotePath}
		}
		if len(mountscope.NormalizePaths(remotePaths, "/")) > 1 {
			return fmt.Errorf("multiple --remote-path values are not supported with --mode=%s; use --mode=%s", mountModeFuse, mountModePoll)
		}
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
	if err := logStandaloneMountContentPolicy([]mountscope.Scope{{
		RemotePath: normalizeMountRemotePath(remotePaths[0]),
		LocalDir:   cfg.localDir,
	}}); err != nil {
		return err
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
	plan, err := mountscope.Plan(cfg.localDir, localLayoutScoped, remotePaths, "/", cfg.stateFile)
	if err != nil {
		return err
	}
	if err := mountscope.ValidateEventProvider(remotePaths, cfg.eventProvider); err != nil {
		return err
	}
	if err := logStandaloneMountContentPolicy(plan); err != nil {
		return err
	}
	scopedMounts := make([]scopedMount, 0, len(plan))
	for _, scope := range plan {
		scoped := cfg
		scoped.remotePath = scope.RemotePath
		scoped.remotePaths = nil
		scoped.localDir = scope.LocalDir
		scoped.scopedChild = true
		scoped.stateFile = cfg.stateFile
		if err := os.MkdirAll(scoped.localDir, 0o755); err != nil {
			return fmt.Errorf("create scoped local dir for %s: %w", scope.RemotePath, err)
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

func logStandaloneMountContentPolicy(scopes []mountscope.Scope) error {
	for _, scope := range scopes {
		report, err := mountscope.InspectLocalContentPolicy(scope.LocalDir)
		if err != nil {
			return fmt.Errorf("inspect local mount content policy for %s: %w", scope.LocalDir, err)
		}
		if len(report.ExcludedInfrastructure) > 0 {
			log.Printf(
				"excluded incidental infrastructure from %s (not synced): %s",
				scope.LocalDir,
				strings.Join(report.ExcludedInfrastructure, ", "),
			)
		}
		if len(report.SensitiveUserContent) > 0 {
			log.Printf(
				"warning: convention-sensitive user content under %s will sync: %s; review or move it if that is not intended",
				scope.LocalDir,
				strings.Join(report.SensitiveUserContent, ", "),
			)
		}
	}
	return nil
}

func readRemotePathsFile(path string) ([]string, error) {
	return mountscope.ReadPathsFile(path)
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
		WorkspaceID:               cfg.workspaceID,
		RemoteRoot:                cfg.remotePath,
		EventProvider:             cfg.eventProvider,
		ScopedChild:               cfg.scopedChild,
		LocalRoot:                 cfg.localDir,
		StateFile:                 cfg.stateFile,
		StateDir:                  cfg.stateDir,
		MountKind:                 cfg.mountKind,
		ValidateState:             true,
		Scopes:                    cfg.scopes,
		WebSocket:                 boolPtr(cfg.websocketEnabled),
		RootCtx:                   rootCtx,
		Logger:                    log.Default(),
		Mode:                      cfg.mode,
		Interval:                  cfg.interval,
		LazyRepos:                 boolPtr(cfg.lazyRepos),
		LazySkipUntrackedPush:     boolPtr(cfg.lazySkipUntrackedPush),
		LowMemory:                 boolPtr(cfg.lowMemory),
		BootstrapTimeout:          cfg.bootstrapTimeout,
		BootstrapMaxFilesPerCycle: cfg.bootstrapMaxFiles,
		FullPullMinInterval:       cfg.fullPullMinInterval,
		CursorTimeout:             cfg.cursorTimeout,
		ForceFullReconcile:        boolPtr(cfg.forceFullRecon),
		SyncMode:                  cfg.syncMode,
	})
	if err != nil {
		return fmt.Errorf("initialize mount syncer: %w", err)
	}
	if _, err := mountsync.StartDiagnostics(rootCtx, cfg.pprofAddr, cfg.memlogInterval, log.Default()); err != nil {
		return fmt.Errorf("start diagnostics: %w", err)
	}
	if cfg.checkpointAndSeal {
		ctx, cancel := context.WithTimeout(rootCtx, checkpointOperationTimeout(cfg.timeout))
		defer cancel()
		seal, err := syncer.CheckpointAndSeal(ctx, mountsync.CheckpointAndSealOptions{
			SessionID: cfg.checkpointSession, Generation: cfg.checkpointGeneration,
			TTLSeconds: int(cfg.checkpointSealTTL / time.Second),
		})
		if err != nil {
			return fmt.Errorf("checkpoint and seal: %w", err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(seal); err != nil {
			return fmt.Errorf("encode checkpoint seal: %w", err)
		}
		return nil
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

	// lastCycleErr records the most recent cycle failure that `run` swallowed
	// as nonfatal. The `--once` bootstrap resume loop reads it so it only
	// continues a traversal that yielded on its file budget, never one that
	// failed: a failing cycle keeps its historical single-attempt behavior.
	var lastCycleErr error
	run := func(reconcile bool) error {
		ctx, cancel := context.WithTimeout(rootCtx, cfg.timeout)
		defer cancel()
		var err error
		if reconcile {
			err = syncer.Reconcile(ctx)
		} else {
			err = syncer.SyncOnce(ctx)
		}
		lastCycleErr = err
		if err != nil {
			if mountsync.IsBootstrapTerminalError(err) {
				// This is an operator-actionable hard stop, not a transient
				// cycle failure. Returning it terminates this runner (and, for
				// scoped layouts, cancels sibling runners) instead of letting
				// the polling ticker retry the same persisted checkpoint forever.
				return err
			}
			if errors.Is(err, context.DeadlineExceeded) {
				if synced, total, ok := readBootstrapProgress(cfg.localDir); ok {
					log.Printf("mount bootstrapping: %s (in progress)", formatBootstrapProgress(synced, total))
					return nil
				}
			}
			log.Printf("mount sync cycle failed: %v", err)
			return nil
		}
		log.Printf("mount sync cycle completed")
		return nil
	}

	if err := run(true); err != nil {
		return err
	}
	if cfg.once {
		return finishInitialBootstrap(rootCtx, cfg, run, func() error { return lastCycleErr })
	}

	var watcher *mountsync.FileWatcher
	if mountWatchesLocalChanges(cfg) {
		changeBatcher := mountsync.NewLocalChangeBatcher(cfg.changeBatchWindow, func(changes []mountsync.LocalChange) {
			ctx, cancel := context.WithTimeout(rootCtx, cfg.timeout)
			defer cancel()
			if err := syncer.HandleLocalChanges(ctx, changes); err != nil {
				log.Printf("mount local change failed: %v", err)
			}
		})
		watcher, err = syncer.NewFileWatcherWithTimings(mountsync.FileWatcherTimings{
			SettleDelay:       cfg.fileSettleDelay,
			AtomicSettleDelay: cfg.atomicSaveSettleDelay,
		}, func(relativePath string, op fsnotify.Op) {
			changeBatcher.Add(relativePath, op)
		})
		if err != nil {
			changeBatcher.Close()
			syncer.EnablePollingLocalChangeDetection()
			log.Printf("file watcher unavailable; continuing with polling sync: %v", err)
		} else if err := watcher.Start(rootCtx); err != nil {
			_ = watcher.Close()
			changeBatcher.Close()
			syncer.EnablePollingLocalChangeDetection()
			log.Printf("file watcher disabled; continuing with polling sync: %v", err)
			watcher = nil
		} else {
			defer changeBatcher.Close()
		}
	} else {
		log.Printf("local change watcher disabled for %s sync", syncModePullOnly)
	}
	if watcher != nil {
		defer watcher.Close()
	}

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
		case <-cfg.flushReq:
			kickErr := kickReconcile(rootCtx, cfg, syncer)
			if recErr := recordFlushAck(cfg, kickErr); recErr != nil {
				log.Printf("mount flush ack failed: %v", recErr)
			} else if kickErr != nil {
				log.Printf("mount flush requested via SIGUSR1; failed: %v", kickErr)
			} else {
				log.Printf("mount flush requested via SIGUSR1; ack recorded")
			}
			if kickErr != nil && mountsync.IsBootstrapTerminalError(kickErr) {
				return kickErr
			}
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
			watcherHealthy := watcher != nil && watcher.Healthy()
			if watcher != nil && !watcherHealthy {
				// An asynchronous fsnotify failure breaks event continuity. Switch
				// permanently to the correctness-first scan path for this process;
				// keeping the stale watcher marker would skip local drift detection.
				syncer.EnablePollingLocalChangeDetection()
				log.Printf("file watcher became unhealthy; continuing with polling reconciliation")
				watcher = nil
			}
			realtimeHealthy := mountReconcileUsesWebSocketCadence(cfg, watcherHealthy) && syncer.WebSocketConnected()
			reconcile := shouldReconcileMountCycle(realtimeHealthy, cycle)
			if reconcile {
				if err := run(true); err != nil {
					return err
				}
			} else {
				ctx, cancel := context.WithTimeout(rootCtx, cfg.timeout)
				err := syncer.RefreshRealtimeStateWithContext(ctx)
				cancel()
				if err != nil {
					log.Printf("real-time state refresh failed: %v", err)
				}
			}
			timer.Reset(jitteredIntervalWithSample(cfg.interval, cfg.intervalJitter, rng.Float64()))
		}
	}
}

func checkpointOperationTimeout(configured time.Duration) time.Duration {
	const minimum = 30 * time.Second
	if configured < minimum {
		return minimum
	}
	return configured
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
		if loadErr == nil {
			// A credential supervisor may have already rotated the shared file.
			// Prefer that newer bearer before making a refresh-token request: the
			// mount may be able to reach Relayfile while provider egress policy
			// temporarily blocks the separate RelayAuth hostname.
			fileToken := strings.TrimSpace(bundle.BearerToken())
			if fileToken != "" &&
				fileToken != strings.TrimSpace(currentToken) &&
				delegatedBearerUsable(bundle, time.Now()) {
				return fileToken, true, nil
			}
		}
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

func delegatedBearerUsable(bundle delegatedauth.Bundle, now time.Time) bool {
	expiresAt := strings.TrimSpace(bundle.BearerExpiresAt())
	if expiresAt == "" {
		// Preserve compatibility with older static credential files that never
		// carried an expiry. If an expiry is present, however, an invalid or
		// elapsed value must never consume HTTPClient's one unauthorized retry.
		return true
	}
	expires, err := time.Parse(time.RFC3339, expiresAt)
	return err == nil && expires.After(now)
}

type repeatedStringFlag = mountscope.StringListFlag

func firstRemotePath(paths []string, fallback string) string {
	return mountscope.FirstPath(paths, fallback)
}

func normalizeRemotePaths(paths []string, fallback string) []string {
	return mountscope.NormalizePaths(paths, fallback)
}

func normalizeMountRemotePath(path string) string {
	return mountscope.NormalizePath(path)
}

func scopedLocalDir(localRoot, remotePath string) string {
	return mountscope.LocalDir(localRoot, remotePath)
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

// maxOnceBootstrapResumeCycles bounds the resumable `--once` bootstrap loop.
// One cycle mirrors at most defaultBootstrapMaxFilesPerCycle (2000) files, so
// this admits workspaces up to ~1M files while still refusing to spin forever
// if the traversal reports progress that never terminates. The syncer's own
// stall guard (RELAYFILE_BOOTSTRAP_STALL_CYCLES) normally fires long before
// this, returning a terminal BootstrapStalledError.
const maxOnceBootstrapResumeCycles = 500

// onceBootstrapStableCycleLimit is how many consecutive successful cycles may
// leave the resumable checkpoint untouched before `--once` gives up on it.
const onceBootstrapStableCycleLimit = 3

// finishInitialBootstrap keeps resuming the persisted traversal checkpoint
// until the full-tree bootstrap completes, then returns.
//
// `--once` used to return immediately after a single reconcile. One reconcile
// mirrors at most defaultBootstrapMaxFilesPerCycle files and then yields with
// a persisted resume cursor, reporting cycle success — so on any workspace
// larger than that budget `--once` exited 0 while .relay/state.json still
// carried a non-null `bootstrap` block. AgentWorkforce/sandbox reads exactly
// that field as the initial-sync readiness barrier and exits 75 (TEMPFAIL,
// "relayfile initial sync paused before complete readiness"), which is why
// every JIT sandbox provision failed rather than only large or slow ones.
// See relayfile#455.
//
// The loop is bounded three ways: rootCtx cancellation (the caller's
// `timeout`/idle watchdog), a terminal error from the cycle, and a
// no-progress guard. Cancellation deliberately returns nil so the exit code
// keeps its historical meaning; the readiness guard downstream still sees the
// incomplete bootstrap and reports a resumable TEMPFAIL.
func finishInitialBootstrap(rootCtx context.Context, cfg mountConfig, run func(reconcile bool) error, lastCycleErr func() error) error {
	if err := lastCycleErr(); err != nil {
		// The cycle failed rather than yielding on its budget. Retrying here
		// would turn one transient cloud error into a stall escalation, so
		// keep `--once`'s historical single-attempt behavior and let the
		// readiness guard downstream report a resumable TEMPFAIL.
		return nil
	}
	synced, total, inProgress := readBootstrapProgress(cfg.localDir)
	if !inProgress {
		return nil
	}
	log.Printf("initial sync: bootstrap incomplete after first cycle (%s); resuming from the persisted checkpoint", formatBootstrapProgress(synced, total))
	lastCheckpoint := readBootstrapCheckpoint(cfg.localDir)
	stableCycles := 0
	for cycle := 0; cycle < maxOnceBootstrapResumeCycles; cycle++ {
		if err := rootCtx.Err(); err != nil {
			log.Printf("initial sync: stopping before bootstrap completed: %v", err)
			return nil
		}
		if err := run(true); err != nil {
			return err
		}
		if err := lastCycleErr(); err != nil {
			log.Printf("initial sync: stopping after a failed resume cycle: %v", err)
			return nil
		}
		synced, total, inProgress = readBootstrapProgress(cfg.localDir)
		if !inProgress {
			log.Printf("initial sync: bootstrap complete")
			return nil
		}
		if checkpoint := readBootstrapCheckpoint(cfg.localDir); checkpoint != lastCheckpoint {
			lastCheckpoint = checkpoint
			stableCycles = 0
			log.Printf("initial sync: bootstrapping %s", formatBootstrapProgress(synced, total))
			continue
		}
		// A successful cycle that moved no part of the resumable checkpoint
		// cannot be resumed into completion. The syncer's own stall guard
		// escalates this to a terminal BootstrapStalledError over several
		// cycles; allow it a couple of cycles to do so, then stop rather than
		// spin.
		stableCycles++
		if stableCycles >= onceBootstrapStableCycleLimit {
			log.Printf("initial sync: bootstrap checkpoint stopped advancing at %s; leaving it for the next run", formatBootstrapProgress(synced, total))
			return nil
		}
	}
	log.Printf("initial sync: bootstrap still incomplete after %d resume cycles; leaving the checkpoint for the next run", maxOnceBootstrapResumeCycles)
	return nil
}

// readBootstrapCheckpoint fingerprints every resumable coordinate the public
// bootstrap block exposes. Keying progress on filesSynced alone is not enough:
// a cycle can advance the directory queue or the page offset while mirroring
// no new files, and treating that as a stall would abandon a bootstrap that is
// still moving. Returns "" when no bootstrap is in progress.
func readBootstrapCheckpoint(localDir string) string {
	if strings.TrimSpace(localDir) == "" {
		return ""
	}
	payload, err := os.ReadFile(filepath.Join(localDir, ".relay", "state.json"))
	if err != nil {
		return ""
	}
	var view struct {
		Bootstrap *struct {
			CurrentPath           string `json:"currentPath"`
			FilesSynced           int    `json:"filesSynced"`
			PageOffset            int    `json:"pageOffset"`
			DirectoriesPending    int    `json:"directoriesPending"`
			DirectoriesDiscovered int    `json:"directoriesDiscovered"`
		} `json:"bootstrap"`
	}
	if err := json.Unmarshal(payload, &view); err != nil || view.Bootstrap == nil {
		return ""
	}
	return fmt.Sprintf("%s|%d|%d|%d|%d",
		view.Bootstrap.CurrentPath,
		view.Bootstrap.FilesSynced,
		view.Bootstrap.PageOffset,
		view.Bootstrap.DirectoriesPending,
		view.Bootstrap.DirectoriesDiscovered,
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

func formatBootstrapProgress(synced, total int) string {
	if total > 0 {
		return fmt.Sprintf("%d/%d files", synced, total)
	}
	return fmt.Sprintf("%d files synced (authoritative total unavailable)", synced)
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
	if raw == "-1" {
		return -1
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("invalid %s=%q, using fallback %s", name, raw, fallback.String())
		return fallback
	}
	return value
}

func parseDurationWithNegativeOne(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "-1" {
		return -1, nil
	}
	return time.ParseDuration(raw)
}

func intEnv(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("invalid %s=%q, using fallback %d", name, raw, fallback)
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

func shouldReconcileMountCycle(realtimeHealthy bool, _ int) bool {
	return !realtimeHealthy
}

func mountWebSocketEnabled(cfg mountConfig) bool {
	return cfg.websocketEnabled && cfg.syncMode != syncModeWriteOnly
}

func mountWatchesLocalChanges(cfg mountConfig) bool {
	return cfg.syncMode != syncModePullOnly
}

func mountReconcileUsesWebSocketCadence(cfg mountConfig, watcherActive bool) bool {
	return mountWebSocketEnabled(cfg) && (cfg.syncMode == syncModePullOnly || watcherActive)
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
