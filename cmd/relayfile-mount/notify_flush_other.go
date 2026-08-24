//go:build !unix

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountlease"
)

func listenFlushRequests(_ context.Context) <-chan struct{} {
	return make(chan struct{})
}

func notifyRunningMountFlush(_ context.Context, _ mountConfig) error {
	return fmt.Errorf("notify flush: SIGUSR1 kick is not supported on this platform")
}

func recordFlushAck(cfg mountConfig) error {
	prev, err := mountlease.ReadFlushAck(cfg.baseURL, cfg.workspaceID, cfg.localDir)
	if err != nil {
		return err
	}
	return mountlease.WriteFlushAck(cfg.baseURL, cfg.workspaceID, cfg.localDir, mountlease.FlushAck{
		Seq: prev.Seq + 1,
		PID: os.Getpid(),
		At:  time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func notifyFlushWait(cfg mountConfig) time.Duration {
	if cfg.timeout > 0 {
		return cfg.timeout
	}
	return 120 * time.Second
}
