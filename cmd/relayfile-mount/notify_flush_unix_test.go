//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountlease"
)

func TestExecuteMountNotifyFlushDoesNotTakeLease(t *testing.T) {
	err := executeMount(context.Background(), mountConfig{
		notifyFlush: true,
		baseURL:     "https://file.example.test",
		workspaceID: "rw_missing",
		localDir:    t.TempDir(),
		timeout:     time.Second,
	}, func(context.Context, mountConfig) error {
		t.Fatal("notify-flush must not start a supervisor")
		return nil
	}, func(context.Context, mountConfig) error {
		t.Fatal("notify-flush must not start a supervisor")
		return nil
	})
	if err == nil {
		t.Fatal("expected notify-flush without a daemon to fail")
	}
	if !errors.Is(err, mountlease.ErrNotHeld) {
		t.Fatalf("expected ErrNotHeld, got %v", err)
	}
}

func TestNotifyFlushKicksHeldLeaseDaemon(t *testing.T) {
	if os.Getenv("RELAYFILE_NOTIFY_FLUSH_HELPER") == "1" {
		runNotifyFlushHelper(t)
		return
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	if err := os.MkdirAll(filepath.Join(home, "Library", "Caches"), 0o700); err != nil {
		t.Fatal(err)
	}

	localRoot := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestNotifyFlushKicksHeldLeaseDaemon")
	cmd.Env = append(os.Environ(),
		"RELAYFILE_NOTIFY_FLUSH_HELPER=1",
		"RELAYFILE_NOTIFY_FLUSH_LOCAL_ROOT="+localRoot,
		"HOME="+home,
		"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	deadline := time.Now().Add(5 * time.Second)
	var info *mountlease.Info
	for time.Now().Before(deadline) {
		got, err := mountlease.Inspect("https://file.example.test", "rw_notify", localRoot)
		if err == nil && got.PID == cmd.Process.Pid {
			info = got
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if info == nil {
		t.Fatal("helper never published a mount lease")
	}

	before, err := mountlease.ReadFlushAck("https://file.example.test", "rw_notify", localRoot)
	if err != nil {
		t.Fatalf("read ack before: %v", err)
	}

	err = executeMount(context.Background(), mountConfig{
		notifyFlush: true,
		baseURL:     "https://file.example.test",
		workspaceID: "rw_notify",
		localDir:    localRoot,
		timeout:     5 * time.Second,
	}, func(context.Context, mountConfig) error {
		t.Fatal("notify-flush must not start a second supervisor")
		return nil
	}, func(context.Context, mountConfig) error {
		t.Fatal("notify-flush must not start a second supervisor")
		return nil
	})
	if err != nil {
		t.Fatalf("notify-flush: %v", err)
	}

	ack, err := mountlease.ReadFlushAck("https://file.example.test", "rw_notify", localRoot)
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	if ack.Seq != before.Seq+1 || ack.PID != cmd.Process.Pid || !ack.OK {
		t.Fatalf("ack = %+v, want seq=%d pid=%d ok", ack, before.Seq+1, cmd.Process.Pid)
	}

	if _, err := mountlease.Acquire("https://file.example.test", "rw_notify", localRoot); !errors.Is(err, mountlease.ErrHeld) {
		t.Fatalf("daemon must still hold the lease after notify-flush, got %v", err)
	}
}

func runNotifyFlushHelper(t *testing.T) {
	t.Helper()
	localRoot := os.Getenv("RELAYFILE_NOTIFY_FLUSH_LOCAL_ROOT")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	flushReq := listenFlushRequests(ctx)
	lease, err := mountlease.Acquire("https://file.example.test", "rw_notify", localRoot)
	if err != nil {
		t.Fatalf("helper acquire: %v", err)
	}
	defer lease.Release()

	<-flushReq
	cfg := mountConfig{
		baseURL:     "https://file.example.test",
		workspaceID: "rw_notify",
		localDir:    localRoot,
	}
	if err := recordFlushAck(cfg, nil); err != nil {
		t.Fatalf("helper ack: %v", err)
	}
	select {}
}
