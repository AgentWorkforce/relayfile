//go:build unix

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountlease"
)

func listenFlushRequests(ctx context.Context) <-chan struct{} {
	req := make(chan struct{}, 1)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGUSR1)
	go func() {
		defer signal.Stop(sigs)
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigs:
				select {
				case req <- struct{}{}:
				default:
				}
			}
		}
	}()
	return req
}

func ignoreNotifyFlushSignal() {
	signal.Ignore(syscall.SIGUSR1)
}

func notifyRunningMountFlush(ctx context.Context, cfg mountConfig) error {
	info, err := mountlease.Inspect(cfg.baseURL, cfg.workspaceID, cfg.localDir)
	if err != nil {
		return fmt.Errorf("notify flush: %w", err)
	}
	if err := syscall.Kill(info.PID, 0); err != nil {
		return fmt.Errorf("notify flush: mount daemon pid %d is not running: %w", info.PID, err)
	}
	before, err := mountlease.ReadFlushAck(cfg.baseURL, cfg.workspaceID, cfg.localDir)
	if err != nil {
		return fmt.Errorf("notify flush: read ack: %w", err)
	}
	if err := syscall.Kill(info.PID, syscall.SIGUSR1); err != nil {
		return fmt.Errorf("notify flush: signal daemon pid %d: %w", info.PID, err)
	}
	deadline := time.Now().Add(notifyFlushWait(cfg))
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		ack, err := mountlease.ReadFlushAck(cfg.baseURL, cfg.workspaceID, cfg.localDir)
		if err != nil {
			return fmt.Errorf("notify flush: read ack: %w", err)
		}
		if ack.Seq > before.Seq && ack.PID == info.PID {
			if !ack.OK {
				if ack.Error == "" {
					return fmt.Errorf("notify flush: daemon pid %d flush failed", info.PID)
				}
				return fmt.Errorf("notify flush: daemon pid %d flush failed: %s", info.PID, ack.Error)
			}
			log.Printf("notified mount daemon pid %d; flush ack seq %d", info.PID, ack.Seq)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("notify flush: timed out waiting for daemon pid %d to ack SIGUSR1 (last seq %d)", info.PID, ack.Seq)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("notify flush: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
