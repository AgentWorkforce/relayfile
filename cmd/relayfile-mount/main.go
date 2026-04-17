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
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountsync"
	"github.com/fsnotify/fsnotify"
)

const (
	mountModePoll           = "poll"
	mountModeFuse           = "fuse"
	websocketReconcileEvery = 10
)

var errFuseModeUnavailable = errors.New("fuse mode is not available in this build")

type mountConfig struct {
	baseURL          string
	token            string
	workspaceID      string
	remotePath       string
	eventProvider    string
	localDir         string
	stateFile        string
	interval         time.Duration
	intervalJitter   float64
	timeout          time.Duration
	websocketEnabled bool
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
	remotePath := flag.String("remote-path", envOrDefault("RELAYFILE_REMOTE_PATH", "/"), "remote root path")
	eventProvider := flag.String("provider", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_PROVIDER")), "event provider filter")
	localDir := flag.String("local-dir", strings.TrimSpace(os.Getenv("RELAYFILE_LOCAL_DIR")), "local mirror directory")
	stateFile := flag.String("state-file", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_STATE_FILE")), "state file path")
	interval := flag.Duration("interval", durationEnv("RELAYFILE_MOUNT_INTERVAL", 30*time.Second), "sync interval")
	intervalJitter := flag.Float64("interval-jitter", floatEnv("RELAYFILE_MOUNT_INTERVAL_JITTER", 0.2), "sync interval jitter ratio (0.0-1.0)")
	timeout := flag.Duration("timeout", durationEnv("RELAYFILE_MOUNT_TIMEOUT", 15*time.Second), "per-sync timeout")
	websocketEnabled := flag.Bool("websocket", boolEnv("RELAYFILE_MOUNT_WEBSOCKET", true), "enable websocket event streaming when available")
	mode := flag.String("mode", envOrDefault("RELAYFILE_MOUNT_MODE", mountModePoll), "mount mode: poll or fuse")
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
	if *timeout <= 0 {
		*timeout = 15 * time.Second
	}
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
		remotePath:       *remotePath,
		eventProvider:    strings.TrimSpace(*eventProvider),
		localDir:         *localDir,
		stateFile:        *stateFile,
		interval:         *interval,
		intervalJitter:   *intervalJitter,
		timeout:          *timeout,
		websocketEnabled: *websocketEnabled,
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
	client := mountsync.NewHTTPClient(cfg.baseURL, cfg.token, &http.Client{Timeout: cfg.timeout})
	syncer, err := mountsync.NewSyncer(client, mountsync.SyncerOptions{
		WorkspaceID:   cfg.workspaceID,
		RemoteRoot:    cfg.remotePath,
		EventProvider: cfg.eventProvider,
		LocalRoot:     cfg.localDir,
		StateFile:     cfg.stateFile,
		Scopes:        cfg.scopes,
		WebSocket:     boolPtr(cfg.websocketEnabled),
		RootCtx:       rootCtx,
		Logger:        log.Default(),
	})
	if err != nil {
		return fmt.Errorf("initialize mount syncer: %w", err)
	}

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
	cycle := 0
	for {
		select {
		case <-rootCtx.Done():
			log.Printf("mount sync stopping: %v", rootCtx.Err())
			return nil
		case <-timer.C:
			cycle++
			reconcile := !cfg.websocketEnabled || cycle%websocketReconcileEvery == 0
			if reconcile {
				run(true)
			}
			timer.Reset(jitteredIntervalWithSample(cfg.interval, cfg.intervalJitter, rng.Float64()))
		}
	}
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
