package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountlease"
	"github.com/agentworkforce/relayfile/internal/mountsync"
)

func kickReconcile(rootCtx context.Context, cfg mountConfig, syncer *mountsync.Syncer) error {
	ctx, cancel := context.WithTimeout(rootCtx, cfg.timeout)
	defer cancel()
	return syncer.Reconcile(ctx)
}

// serviceFlushRequest runs the reconcile a SIGUSR1 flush asked for and
// records its ack. Only a terminal bootstrap error is returned; any other
// failure is reported through the ack.
func serviceFlushRequest(rootCtx context.Context, cfg mountConfig, syncer *mountsync.Syncer) error {
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
	return nil
}

func recordFlushAck(cfg mountConfig, kickErr error) error {
	prev, err := mountlease.ReadFlushAck(cfg.baseURL, cfg.workspaceID, cfg.localDir)
	if err != nil {
		return err
	}
	ack := mountlease.FlushAck{
		Seq: prev.Seq + 1,
		PID: os.Getpid(),
		At:  time.Now().UTC().Format(time.RFC3339Nano),
		OK:  kickErr == nil,
	}
	if kickErr != nil {
		ack.Error = kickErr.Error()
	}
	return mountlease.WriteFlushAck(cfg.baseURL, cfg.workspaceID, cfg.localDir, ack)
}

func notifyFlushWait(cfg mountConfig) time.Duration {
	if cfg.timeout > 0 {
		// Cover an in-progress periodic cycle plus the kicked cycle.
		return 2 * cfg.timeout
	}
	return 120 * time.Second
}

func acceptsNotifyFlush(cfg mountConfig) bool {
	return cfg.mode == mountModePoll &&
		!cfg.once &&
		!cfg.flushOutboxOnce &&
		!cfg.pushLocalOnce &&
		!cfg.checkpointAndSeal
}
