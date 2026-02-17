package main

import (
	"context"
	"flag"
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
)

func main() {
	baseURL := flag.String("base-url", envOrDefault("RELAYFILE_BASE_URL", "http://127.0.0.1:8080"), "relayfile base URL")
	token := flag.String("token", strings.TrimSpace(os.Getenv("RELAYFILE_TOKEN")), "bearer token")
	workspaceID := flag.String("workspace", strings.TrimSpace(os.Getenv("RELAYFILE_WORKSPACE")), "workspace ID")
	remotePath := flag.String("remote-path", envOrDefault("RELAYFILE_REMOTE_PATH", "/"), "remote root path")
	eventProvider := flag.String("provider", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_PROVIDER")), "event provider filter")
	localDir := flag.String("local-dir", strings.TrimSpace(os.Getenv("RELAYFILE_LOCAL_DIR")), "local mirror directory")
	stateFile := flag.String("state-file", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_STATE_FILE")), "state file path")
	interval := flag.Duration("interval", durationEnv("RELAYFILE_MOUNT_INTERVAL", 2*time.Second), "sync interval")
	intervalJitter := flag.Float64("interval-jitter", floatEnv("RELAYFILE_MOUNT_INTERVAL_JITTER", 0.2), "sync interval jitter ratio (0.0-1.0)")
	timeout := flag.Duration("timeout", durationEnv("RELAYFILE_MOUNT_TIMEOUT", 15*time.Second), "per-sync timeout")
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
		*interval = 2 * time.Second
	}
	if *timeout <= 0 {
		*timeout = 15 * time.Second
	}
	*intervalJitter = clampJitterRatio(*intervalJitter)

	client := mountsync.NewHTTPClient(*baseURL, *token, &http.Client{Timeout: *timeout})
	syncer, err := mountsync.NewSyncer(client, mountsync.SyncerOptions{
		WorkspaceID:   strings.TrimSpace(*workspaceID),
		RemoteRoot:    *remotePath,
		EventProvider: strings.TrimSpace(*eventProvider),
		LocalRoot:     *localDir,
		StateFile:     *stateFile,
		Logger:        log.Default(),
	})
	if err != nil {
		log.Fatalf("failed to initialize mount syncer: %v", err)
	}
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	run := func() {
		ctx, cancel := context.WithTimeout(rootCtx, *timeout)
		defer cancel()
		if err := syncer.SyncOnce(ctx); err != nil {
			log.Printf("mount sync cycle failed: %v", err)
			return
		}
		log.Printf("mount sync cycle completed")
	}

	run()
	if *once {
		return
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	timer := time.NewTimer(jitteredIntervalWithSample(*interval, *intervalJitter, rng.Float64()))
	defer timer.Stop()
	for {
		select {
		case <-rootCtx.Done():
			log.Printf("mount sync stopping: %v", rootCtx.Err())
			return
		case <-timer.C:
			run()
			timer.Reset(jitteredIntervalWithSample(*interval, *intervalJitter, rng.Float64()))
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
