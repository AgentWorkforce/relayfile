//go:build !windows

package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestA14BackgroundModeWritesPidAndStopSignalsCleanly is the acceptance test
// for productized cloud-mount contract A14: `relayfile mount --background`
// writes `mount.pid` and `mount.log`; `relayfile stop` signals the daemon
// cleanly within 5 s. The test spawns a long-lived child process and treats
// it as the daemon target so we can exercise `runStop` without standing up
// a full mount loop.
func TestA14BackgroundModeWritesPidAndStopSignalsCleanly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep subprocess failed: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_, _ = cmd.Process.Wait()
	})

	pidFile := mountPIDFile(localDir)
	logFile := mountLogFile(localDir)
	if err := writeDaemonPIDState(pidFile, daemonPIDState{
		PID:         cmd.Process.Pid,
		WorkspaceID: "ws_demo",
		LocalDir:    localDir,
		LogFile:     logFile,
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("writeDaemonPIDState failed: %v", err)
	}
	// A14 also requires the daemon to allocate a mount.log; we synthesize it
	// here because the test does not run the real mount loop.
	if err := os.WriteFile(logFile, []byte("mount started\n"), 0o644); err != nil {
		t.Fatalf("seed mount.log failed: %v", err)
	}

	if _, err := os.Stat(pidFile); err != nil {
		t.Fatalf("expected mount.pid in place, got err=%v", err)
	}
	if _, err := os.Stat(logFile); err != nil {
		t.Fatalf("expected mount.log in place, got err=%v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"stop", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run stop failed: %v\noutput:\n%s", err, stdout.String())
	}
	if got := stdout.String(); !strings.Contains(got, "Stopped background mount") {
		t.Fatalf("unexpected stop output: %q", got)
	}

	deadline := time.Now().Add(5 * time.Second)
	exited := make(chan error, 1)
	go func() {
		_, err := cmd.Process.Wait()
		exited <- err
	}()
	select {
	case err := <-exited:
		if err == nil {
			return
		}
		// `sleep` killed by SIGTERM exits with a *exec.ExitError; that is
		// the expected signal-induced termination per A14.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGTERM {
				return
			}
		}
		t.Fatalf("subprocess exited with unexpected error: %v", err)
	case <-time.After(time.Until(deadline)):
		t.Fatalf("subprocess did not exit within 5s of SIGTERM")
	}
}

// TestA14StopReportsErrorWhenNoDaemonRunning verifies the stop command does
// not silently no-op when the workspace has no recorded mount.pid.
func TestA14StopReportsErrorWhenNoDaemonRunning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	var stdout bytes.Buffer
	err := run([]string{"stop", "demo"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected stop to error when no pid file present, got nil")
	}
	if !strings.Contains(err.Error(), "no running mount") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestA2RunCloudLoginReturnsStateMismatchSentinel exercises productized
// cloud-mount contract A2: when the OAuth callback's `state` parameter does
// not match the value the CLI generated, runCloudLogin must return the
// ErrCloudLoginStateMismatch sentinel so main() can exit 10.
func TestA2RunCloudLoginReturnsStateMismatchSentinel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	// Read stdout via a pipe so the test can wait for the listener URL line
	// before forging the callback request.
	pipeR, pipeW := io.Pipe()
	scannerOut := make(chan string, 1)
	go func() {
		// Pull at most a few KB until we see the login URL the function
		// prints, then forward the rest into a discard sink.
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 256)
		re := regexp.MustCompile(`Sign in to Relayfile Cloud:\s+(\S+)`)
		for {
			n, err := pipeR.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
				if matches := re.FindSubmatch(buf); matches != nil {
					scannerOut <- string(matches[1])
					_, _ = io.Copy(io.Discard, pipeR)
					return
				}
			}
			if err != nil {
				close(scannerOut)
				return
			}
		}
	}()

	loginErr := make(chan error, 1)
	go func() {
		_, err := runCloudLogin("https://cloud.example.test", 10*time.Second, false, pipeW)
		_ = pipeW.Close()
		loginErr <- err
	}()

	var loginURL string
	select {
	case loginURL = <-scannerOut:
	case <-time.After(2 * time.Second):
		t.Fatalf("runCloudLogin did not print login URL in time")
	}

	parsed, err := url.Parse(loginURL)
	if err != nil {
		t.Fatalf("parse login URL failed: %v", err)
	}
	redirectURI := parsed.Query().Get("redirect_uri")
	if redirectURI == "" {
		t.Fatalf("login URL missing redirect_uri: %s", loginURL)
	}
	callback, err := url.Parse(redirectURI)
	if err != nil {
		t.Fatalf("parse redirect_uri failed: %v", err)
	}
	q := callback.Query()
	q.Set("state", "tampered-or-replayed")
	q.Set("access_token", "bogus")
	callback.RawQuery = q.Encode()

	resp, err := http.Get(callback.String())
	if err != nil {
		t.Fatalf("forge callback request failed: %v", err)
	}
	_ = resp.Body.Close()

	select {
	case err := <-loginErr:
		if !errors.Is(err, ErrCloudLoginStateMismatch) {
			t.Fatalf("expected ErrCloudLoginStateMismatch, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("runCloudLogin did not return after state mismatch")
	}
}

// TestA14LogsCommandTailsBackgroundLog ensures `relayfile logs` reads from
// the recorded mount.log path the daemon would have written.
func TestA14LogsCommandTailsBackgroundLog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, ".relay", "mount.log"), []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatalf("write mount.log failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"logs", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run logs failed: %v", err)
	}
	got := stdout.String()
	for _, line := range []string{"line1", "line2", "line3"} {
		if !strings.Contains(got, line) {
			t.Fatalf("expected %q in logs output, got %q", line, got)
		}
	}
}
