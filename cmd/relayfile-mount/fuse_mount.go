//go:build !nofuse

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/agentworkforce/relayfile/internal/mountfuse"
	"github.com/agentworkforce/relayfile/internal/mountsync"
)

func init() {
	defaultFuseRunner = runFuseMount
}

func runFuseMount(ctx context.Context, cfg mountConfig) error {
	httpClient := mountsync.NewHTTPClient(cfg.baseURL, cfg.token, &http.Client{Timeout: cfg.timeout})

	fuseCfg := mountfuse.Config{
		Client:      httpClient,
		WorkspaceID: cfg.workspaceID,
		RemoteRoot:  cfg.remotePath,
		Logger:      log.Default(),
	}

	mounted, err := mountfuse.Mount(cfg.localDir, fuseCfg)
	if err != nil {
		return fmt.Errorf("mount fuse: %w", err)
	}

	// Derive a cancellable context so all goroutines (including the
	// WSInvalidator) are cleaned up when the FUSE server exits for any
	// reason — normal unmount, crash, or kernel-initiated unmount.
	mountCtx, mountCancel := context.WithCancel(ctx)
	defer mountCancel()

	// Start WebSocket invalidation if enabled.
	if cfg.websocketEnabled {
		invalidator := mountfuse.NewWSInvalidator(cfg.baseURL, cfg.token, cfg.workspaceID, mounted.Root.State(), log.Default())
		go invalidator.Run(mountCtx)
	}

	// Wait for context cancellation, then unmount.
	go func() {
		<-mountCtx.Done()
		log.Printf("unmounting FUSE filesystem...")
		if err := mounted.Unmount(); err != nil {
			log.Printf("unmount error: %v", err)
		}
	}()

	mounted.Server.Wait()
	return nil
}
