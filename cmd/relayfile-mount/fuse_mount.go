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

	// Start WebSocket invalidation if enabled.
	if cfg.websocketEnabled {
		invalidator := mountfuse.NewWSInvalidator(cfg.baseURL, cfg.token, cfg.workspaceID, mounted.Root.State(), log.Default())
		go invalidator.Run(ctx)
	}

	// Wait for context cancellation, then unmount.
	go func() {
		<-ctx.Done()
		log.Printf("unmounting FUSE filesystem...")
		if err := mounted.Unmount(); err != nil {
			log.Printf("unmount error: %v", err)
		}
	}()

	mounted.Server.Wait()
	return nil
}
