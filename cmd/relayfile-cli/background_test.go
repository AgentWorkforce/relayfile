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
	"strconv"
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
	// verifyDaemonProcess confirms a recorded PID still belongs to a
	// relayfile daemon by matching the running process's executable
	// against daemonPIDState.Executable (guards against PID reuse). The
	// test's stand-in daemon is `sleep`, so record its resolved binary
	// path; otherwise stop() would treat it as a stale/foreign PID.
	daemonExe := cmd.Path
	if resolved, rerr := filepath.EvalSymlinks(cmd.Path); rerr == nil {
		daemonExe = resolved
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	exited := make(chan error, 1)
	go func() {
		_, err := cmd.Process.Wait()
		exited <- err
	}()

	pidFile := mountPIDFile(localDir)
	logFile := mountLogFile(localDir)
	if err := writeDaemonPIDState(pidFile, daemonPIDState{
		PID:         cmd.Process.Pid,
		WorkspaceID: "ws_demo",
		LocalDir:    localDir,
		LogFile:     logFile,
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
		Executable:  daemonExe,
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

func TestStopEscalatesToKillWhenDaemonIgnoresTerm(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	oldGracefulTimeout := daemonGracefulStopTimeout
	oldForceTimeout := daemonForceStopTimeout
	daemonGracefulStopTimeout = 100 * time.Millisecond
	daemonForceStopTimeout = 2 * time.Second
	t.Cleanup(func() {
		daemonGracefulStopTimeout = oldGracefulTimeout
		daemonForceStopTimeout = oldForceTimeout
	})

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

	cmd := exec.Command("sh", "-c", "trap '' TERM; exec sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start TERM-ignoring subprocess failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	exited := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		exited <- cmd.Wait()
		close(done)
	}()
	waited := false
	t.Cleanup(func() {
		if waited {
			return
		}
		select {
		case <-done:
			<-exited
		default:
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-done
			<-exited
		}
	})

	daemonExe, lerr := exec.LookPath("sleep")
	if lerr != nil {
		t.Fatalf("look up sleep executable failed: %v", lerr)
	}
	if resolved, rerr := filepath.EvalSymlinks(daemonExe); rerr == nil {
		daemonExe = resolved
	}
	if err := writeDaemonPIDState(mountPIDFile(localDir), daemonPIDState{
		PID:         cmd.Process.Pid,
		WorkspaceID: "ws_demo",
		LocalDir:    localDir,
		Executable:  daemonExe,
	}); err != nil {
		t.Fatalf("writeDaemonPIDState failed: %v", err)
	}

	oldList := listProcessCommands
	listProcessCommands = func() ([]processCommandSnapshot, error) {
		select {
		case <-done:
			return nil, nil
		default:
			return []processCommandSnapshot{{
				PID:     cmd.Process.Pid,
				Command: "relayfile mount demo " + localDir + " --daemonized",
			}}, nil
		}
	}
	t.Cleanup(func() { listProcessCommands = oldList })

	var stdout bytes.Buffer
	if err := run([]string{"stop", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run stop failed: %v\noutput:\n%s", err, stdout.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "did not exit after SIGTERM; sending SIGKILL") ||
		!strings.Contains(got, "Stopped background mount for demo") {
		t.Fatalf("expected force-stop output, got %q", got)
	}
	if _, err := os.Stat(mountPIDFile(localDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected pid file to be removed after force stop, got %v", err)
	}

	select {
	case err := <-exited:
		waited = true
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("expected signal exit after force stop, got %v", err)
		}
		if status, ok := exitErr.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
			t.Fatalf("expected SIGKILL exit after force stop, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("subprocess did not exit after force stop")
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

func TestStopDiscoversOrphanDaemonWithoutPIDFile(t *testing.T) {
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
	exited := make(chan error, 1)
	go func() {
		_, err := cmd.Process.Wait()
		exited <- err
	}()
	waited := false
	t.Cleanup(func() {
		if waited {
			return
		}
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-exited
	})

	oldList := listProcessCommands
	calls := 0
	listProcessCommands = func() ([]processCommandSnapshot, error) {
		calls++
		if calls == 1 {
			return []processCommandSnapshot{{
				PID:     cmd.Process.Pid,
				Command: "relayfile mount ws_demo " + localDir + " --daemonized",
			}}, nil
		}
		return nil, nil
	}
	t.Cleanup(func() { listProcessCommands = oldList })

	var stdout bytes.Buffer
	if err := run([]string{"stop", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run stop failed: %v\noutput:\n%s", err, stdout.String())
	}
	if got := stdout.String(); !strings.Contains(got, "Stopped background mount for demo") ||
		!strings.Contains(got, strconv.Itoa(cmd.Process.Pid)) {
		t.Fatalf("unexpected stop output: %q", got)
	}

	select {
	case err := <-exited:
		waited = true
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			t.Fatalf("subprocess exited with unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("orphan subprocess did not exit within 5s of SIGTERM")
	}
}

func TestStopClearsLegacyPIDWithoutProcessScanMatch(t *testing.T) {
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
	if err := os.WriteFile(mountPIDFile(localDir), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatalf("write legacy pid failed: %v", err)
	}

	oldList := listProcessCommands
	listProcessCommands = func() ([]processCommandSnapshot, error) { return nil, nil }
	t.Cleanup(func() { listProcessCommands = oldList })

	var stdout bytes.Buffer
	if err := run([]string{"stop", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run stop failed: %v\noutput:\n%s", err, stdout.String())
	}
	if got := stdout.String(); !strings.Contains(got, "Cleared stale background mount state") {
		t.Fatalf("expected stale cleanup output, got %q", got)
	}
	if _, err := os.Stat(mountPIDFile(localDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected legacy pid file to be removed, got %v", err)
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("legacy pid without process scan match should not be signaled, got %v", err)
	}
}

func TestMountBackgroundRefusesExistingDaemonFromProcessScan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	if err := saveCredentials(credentials{
		Server: defaultServerURL,
		Token:  testJWTWithWorkspace("ws_demo"),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	oldList := listProcessCommands
	listProcessCommands = func() ([]processCommandSnapshot, error) {
		return []processCommandSnapshot{{
			PID:     7777,
			Command: "relayfile mount demo " + localDir + " --daemonized",
		}}, nil
	}
	t.Cleanup(func() { listProcessCommands = oldList })

	var stdout bytes.Buffer
	err := run([]string{"mount", "demo", "--background"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected mount --background to refuse an existing daemon")
	}
	if !strings.Contains(err.Error(), "already has a running mount") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(stdout.String(), "Mirror started in background") {
		t.Fatalf("mount should not have spawned a background daemon: %q", stdout.String())
	}
}

func TestMountBackgroundClearsDeadStructuredPIDBeforeStart(t *testing.T) {
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
	if err := saveCredentials(credentials{
		Server: defaultServerURL,
		Token:  testJWTWithWorkspace("ws_demo"),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}
	if err := writeDaemonPIDState(mountPIDFile(localDir), daemonPIDState{
		PID:         1 << 30,
		WorkspaceID: "ws_demo",
		LocalDir:    localDir,
	}); err != nil {
		t.Fatalf("writeDaemonPIDState failed: %v", err)
	}

	oldList := listProcessCommands
	listProcessCommands = func() ([]processCommandSnapshot, error) { return nil, nil }
	t.Cleanup(func() { listProcessCommands = oldList })

	oldSpawn := spawnBackgroundMountProcessFn
	spawned := false
	spawnBackgroundMountProcessFn = func(originalArgs []string, absLocalDir, pidFile, logFile string) error {
		spawned = true
		if absLocalDir != localDir {
			t.Fatalf("spawn localDir = %q, want %q", absLocalDir, localDir)
		}
		if _, err := os.Stat(pidFile); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected stale pid file to be cleared before spawn, got %v", err)
		}
		return nil
	}
	t.Cleanup(func() { spawnBackgroundMountProcessFn = oldSpawn })

	var stdout bytes.Buffer
	if err := run([]string{"mount", "demo", "--background"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run mount failed: %v\noutput:\n%s", err, stdout.String())
	}
	if !spawned {
		t.Fatalf("expected mount to proceed after clearing stale pid file")
	}
	if _, err := os.Stat(mountPIDFile(localDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected stale pid file to remain cleared, got %v", err)
	}
}

// TestMountDaemonCommandMatchesRequiresRelayfileExecutable guards against a
// regression where a parent shell (or pipeline sibling like `tee`) whose argv
// happens to contain "relayfile mount <workspace> <localDir>" was matched as a
// competing daemon, breaking `relayfile mount --background` whenever it was
// invoked from a shell wrapper that captured the same command line.
func TestMountDaemonCommandMatchesRequiresRelayfileExecutable(t *testing.T) {
	localDir := "/private/tmp/relayfile-mount"
	workspaceID := "ws_demo"
	workspaceName := "demo"

	nonMatching := []struct {
		name    string
		command string
	}{
		{
			name:    "zsh -c wrapper",
			command: "zsh -c relayfile mount " + workspaceID + " " + localDir + " --background",
		},
		{
			name:    "bash -c wrapper",
			command: "bash -c relayfile mount " + workspaceID + " " + localDir + " --background",
		},
		{
			name:    "tee sibling with relayfile in log path",
			command: "tee /tmp/relayfile-mount-start.log",
		},
		{
			name:    "claude tool invocation including localDir",
			command: "claude --workspace " + workspaceID + " " + localDir,
		},
	}
	for _, tc := range nonMatching {
		t.Run(tc.name, func(t *testing.T) {
			if mountDaemonCommandMatches(tc.command, localDir, workspaceID, workspaceName) {
				t.Fatalf("expected non-relayfile argv[0] not to match: %q", tc.command)
			}
		})
	}

	matching := []struct {
		name    string
		command string
	}{
		{
			name:    "bare relayfile binary",
			command: "relayfile mount " + workspaceID + " " + localDir + " --daemonized",
		},
		{
			name:    "absolute path to relayfile",
			command: "/usr/local/bin/relayfile mount " + workspaceID + " " + localDir + " --daemonized",
		},
		{
			name:    "platform-suffixed relayfile-cli binary",
			command: "/opt/relayfile/relayfile-cli-darwin-arm64 mount " + workspaceID + " " + localDir + " --daemonized",
		},
	}
	for _, tc := range matching {
		t.Run(tc.name, func(t *testing.T) {
			if !mountDaemonCommandMatches(tc.command, localDir, workspaceID, workspaceName) {
				t.Fatalf("expected real relayfile daemon to match: %q", tc.command)
			}
		})
	}
}

func TestRestartClearsStaleDaemonPID(t *testing.T) {
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
	if err := writeDaemonPIDState(mountPIDFile(localDir), daemonPIDState{
		PID:         1 << 30,
		WorkspaceID: "ws_demo",
		LocalDir:    localDir,
	}); err != nil {
		t.Fatalf("writeDaemonPIDState failed: %v", err)
	}

	var stdout bytes.Buffer
	err := runRestart([]string{"demo"}, &stdout)
	if err == nil {
		t.Fatalf("expected restart to reach mount and fail without credentials")
	}
	if !strings.Contains(err.Error(), "token is required") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(stdout.String(), "Stopped background mount") {
		t.Fatalf("restart reported stop success for stale pid: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Cleared stale background mount state") {
		t.Fatalf("restart did not report stale pid cleanup: %q", stdout.String())
	}
	if _, err := os.Stat(mountPIDFile(localDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected stale pid file to be removed, got err=%v", err)
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
