package main

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMatchListenPath(t *testing.T) {
	cases := []struct {
		glob      string
		eventPath string
		want      bool
	}{
		// Empty glob passes everything.
		{"", "/linear/issues/AR-1.json", true},
		// Provider glob.
		{"/linear/**", "/linear/issues/AR-1.json", true},
		{"/linear/**", "/linear/issues/by-state/backlog/AR-1.json", true},
		{"/linear/**", "/linear", true},
		{"/linear/**", "/github/repos/foo.json", false},
		{"/linear/**", "/digests/today.md", false},
		// Exact single-star glob via path.Match.
		{"/linear/issues/*.json", "/linear/issues/AR-1.json", true},
		{"/linear/issues/*.json", "/linear/issues/by-state/backlog/AR-1.json", false},
		// Double-star catch-all.
		{"**", "/anything/at/all", true},
		{"/**", "/anything/at/all", true},
		// Other providers.
		{"/github/**", "/github/repos/foo.json", true},
		{"/github/**", "/linear/issues/AR-1.json", false},
	}
	for _, c := range cases {
		got := matchListenPath(c.glob, c.eventPath)
		if got != c.want {
			t.Errorf("matchListenPath(%q, %q) = %v, want %v", c.glob, c.eventPath, got, c.want)
		}
	}
}

func TestWsEncodeGlob(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"/linear/**", "/linear/**"},
		{"/linear/issues/*.json", "/linear/issues/*.json"},
		{"/github/**", "/github/**"},
		// Spaces should be encoded.
		{"/foo bar/**", "/foo%20bar/**"},
	}
	for _, c := range cases {
		got := wsEncodeGlob(c.input)
		if got != c.want {
			t.Errorf("wsEncodeGlob(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestListenRunDuplicateSuppressorSuppressesSameContentHashWithinWindow(t *testing.T) {
	suppressor := newListenRunDuplicateSuppressor(2 * time.Second)
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	evt := listenEvent{
		Type:        "file.updated",
		Path:        "/linear/issues/by-state/in-planning/AR-322.json",
		ContentHash: "sha256:a",
	}

	if suppressor.shouldSuppress(evt, now) {
		t.Fatal("first event should not be suppressed")
	}
	if !suppressor.shouldSuppress(evt, now.Add(time.Second)) {
		t.Fatal("duplicate event within window should be suppressed")
	}
	if suppressor.shouldSuppress(evt, now.Add(4*time.Second)) {
		t.Fatal("duplicate event after window should not be suppressed")
	}
}

func TestListenRunDuplicateSuppressorUsesFixedWindow(t *testing.T) {
	suppressor := newListenRunDuplicateSuppressor(2 * time.Second)
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	evt := listenEvent{
		Type:        "file.updated",
		Path:        "/linear/issues/by-state/in-planning/AR-322.json",
		ContentHash: "sha256:a",
	}

	if suppressor.shouldSuppress(evt, now) {
		t.Fatal("first event should not be suppressed")
	}
	if !suppressor.shouldSuppress(evt, now.Add(time.Second)) {
		t.Fatal("duplicate event within window should be suppressed")
	}
	if suppressor.shouldSuppress(evt, now.Add(2500*time.Millisecond)) {
		t.Fatal("duplicate event after original fixed window should not be suppressed")
	}
}

func TestListenRunDuplicateSuppressorSweepsPeriodically(t *testing.T) {
	suppressor := newListenRunDuplicateSuppressor(2 * time.Second)
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)

	for i := 0; i < listenRunDuplicateSweepInterval-1; i++ {
		evt := listenEvent{
			Type:        "file.updated",
			Path:        "/linear/issues/by-state/in-planning/AR-322.json",
			ContentHash: "sha256:" + strconv.Itoa(i),
		}
		if suppressor.shouldSuppress(evt, now) {
			t.Fatalf("first event %d should not be suppressed", i)
		}
	}
	if len(suppressor.seen) != listenRunDuplicateSweepInterval-1 {
		t.Fatalf("expected unswept keys to remain, got %d", len(suppressor.seen))
	}

	evt := listenEvent{
		Type:        "file.updated",
		Path:        "/linear/issues/by-state/in-planning/AR-322.json",
		ContentHash: "sha256:trigger",
	}
	if suppressor.shouldSuppress(evt, now.Add(3*time.Second)) {
		t.Fatal("first trigger event should not be suppressed")
	}
	if len(suppressor.seen) != 1 {
		t.Fatalf("expected periodic sweep to retain only current key, got %d", len(suppressor.seen))
	}
}

func TestListenRunDuplicateSuppressorAllowsDifferentContentHashes(t *testing.T) {
	suppressor := newListenRunDuplicateSuppressor(2 * time.Second)
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	first := listenEvent{
		Type:        "file.updated",
		Path:        "/linear/issues/by-state/in-planning/AR-322.json",
		ContentHash: "sha256:a",
	}
	second := first
	second.ContentHash = "sha256:b"

	if suppressor.shouldSuppress(first, now) {
		t.Fatal("first event should not be suppressed")
	}
	if suppressor.shouldSuppress(second, now.Add(time.Second)) {
		t.Fatal("changed content hash should not be suppressed")
	}
}

func TestListenRunDuplicateSuppressorFallsBackToCorrelationID(t *testing.T) {
	suppressor := newListenRunDuplicateSuppressor(2 * time.Second)
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	evt := listenEvent{
		Type:          "file.updated",
		Path:          "/linear/issues/by-state/in-planning/AR-322.json",
		CorrelationID: "corr_1",
	}

	if suppressor.shouldSuppress(evt, now) {
		t.Fatal("first event should not be suppressed")
	}
	if !suppressor.shouldSuppress(evt, now.Add(time.Second)) {
		t.Fatal("same correlation ID should be suppressed when content hash is absent")
	}

	next := evt
	next.CorrelationID = "corr_2"
	if suppressor.shouldSuppress(next, now.Add(time.Second)) {
		t.Fatal("different correlation ID should not be suppressed")
	}
}

func TestListenRunDuplicateKeyRequiresStableIdentity(t *testing.T) {
	key := listenRunDuplicateKey(listenEvent{
		Type: "file.updated",
		Path: "/linear/issues/by-state/in-planning/AR-322.json",
	})
	if key != "" {
		t.Fatalf("expected no key without content hash or correlation ID, got %q", key)
	}

	key = listenRunDuplicateKey(listenEvent{
		Type:        "file.updated",
		Path:        "/linear/issues/by-state/in-planning/AR-322.json",
		ContentHash: "sha256:a",
	})
	if !strings.Contains(key, "hash:sha256:a") {
		t.Fatalf("expected content hash in duplicate key, got %q", key)
	}
}

// captureStderr runs fn with os.Stderr redirected, and returns what it wrote.
//
// The workspace-resolution warning below goes straight to os.Stderr rather
// than through the io.Writer the command is handed, so there is no other seam
// to read it from.
func captureStderr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("open pipe: %v", err)
	}
	original := os.Stderr
	os.Stderr = writer
	fnErr := fn()
	os.Stderr = original
	if err := writer.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	captured, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close pipe reader: %v", err)
	}
	return string(captured), fnErr
}

// TestListenReadsTheWorkspaceFromItsFirstPositional pins the behaviour the
// command table has to declare. runListen takes the workspace as a positional
// (`relayfile listen WORKSPACE`), but the table declared no args for `listen`
// or for `dev`, which forwards its argv here — so `agent-relay file`, which
// builds its parser from the emitted spec, refused a workspace-qualified
// invocation before the binary ever saw it.
//
// HOME is empty, so the run stops at the local credential lookup and touches
// no network. What it names on the way there is the proof.
func TestListenReadsTheWorkspaceFromItsFirstPositional(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspace = "listen-positional-probe"
	withPositional, err := captureStderr(t, func() error {
		return runListen([]string{workspace}, io.Discard)
	})
	if err == nil {
		t.Fatal("expected a credential-resolution error with an empty HOME")
	}
	if !strings.Contains(withPositional, strconv.Quote(workspace)) {
		t.Errorf("listen %s resolved no workspace; stderr = %q", workspace, withPositional)
	}

	withoutPositional, err := captureStderr(t, func() error {
		return runListen(nil, io.Discard)
	})
	if err == nil {
		t.Fatal("expected a credential-resolution error with an empty HOME")
	}
	if strings.Contains(withoutPositional, strconv.Quote(workspace)) {
		t.Errorf("bare listen named a workspace it was never given; stderr = %q", withoutPositional)
	}
}

// TestListenRejectsAFlagItDoesNotRegister is why the command table may not
// advertise a listen flag that runListen has never parsed: `supervisor
// install` embeds its argv into the unit's ExecStart as `relayfile listen
// ...`, under Restart=on-failure. A flag like --interval — which the table
// did advertise — makes that unit exit on every single start, forever.
func TestListenRejectsAFlagItDoesNotRegister(t *testing.T) {
	err := runListen([]string{"--interval", "30s", "-h"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined: -interval") {
		t.Fatalf("runListen --interval error = %v, want an undefined-flag error", err)
	}
}

// TestSupervisorInstallFlagsReachAParsingListener closes that loop from the
// other side: every flag the table declares for `supervisor install` must be
// one runListen accepts.
//
// The trailing -h aborts the parse as soon as the flag before it is accepted,
// so this exercises argument parsing only: no network, and no background
// process for --background.
func TestSupervisorInstallFlagsReachAParsingListener(t *testing.T) {
	var install *cliCommandSpec
	walkSpec(publicCommandSpec(), nil, func(path []string, command cliCommandSpec) {
		if strings.Join(path, " ") == "supervisor install" {
			declared := command
			install = &declared
		}
	})
	if install == nil {
		t.Fatal("no `supervisor install` in the command table")
	}
	if len(install.Options) == 0 {
		t.Fatal("`supervisor install` declares no options; it forwards listen's")
	}

	for _, option := range install.Options {
		name := optionLongName(option.Flags)
		if name == "" {
			t.Errorf("option %q has no long flag", option.Flags)
			continue
		}
		err := runListen([]string{"--" + name, "probe", "-h"}, io.Discard)
		if err != nil && strings.Contains(err.Error(), "flag provided but not defined") {
			t.Errorf("supervisor install declares --%s, but the listener it installs rejects it: %v", name, err)
		}
	}
}

// supervisorInstallSpec returns the `supervisor install` entry from the
// emitted public command tree — the tree `agent-relay file` builds its parser
// from, so it is the surface these assertions are about.
func supervisorInstallSpec(t *testing.T) cliCommandSpec {
	t.Helper()
	var install *cliCommandSpec
	walkSpec(publicCommandSpec(), nil, func(path []string, command cliCommandSpec) {
		if strings.Join(path, " ") == "supervisor install" {
			declared := command
			install = &declared
		}
	})
	if install == nil {
		t.Fatal("no `supervisor install` in the command table")
	}
	return *install
}

// TestSupervisorInstallAdvertisesFiltersNotProcessModelFlags is the other half
// of TestSupervisorInstallFlagsReachAParsingListener. That test only asks
// whether runListen *parses* a declared flag; --background parses fine and
// still breaks the service, because runListen re-execs a detached child and
// returns, leaving launchd's KeepAlive relaunching a process that exits every
// time and systemd killing the orphan with the unit's cgroup.
//
// So the surface must advertise listen's filters and withhold its
// process-model flags — while `listen` itself, which is not supervised, keeps
// advertising both.
func TestSupervisorInstallAdvertisesFiltersNotProcessModelFlags(t *testing.T) {
	declared := map[string]bool{}
	for _, option := range supervisorInstallSpec(t).Options {
		if name := optionLongName(option.Flags); name != "" {
			declared[name] = true
		}
	}
	if len(declared) == 0 {
		t.Fatal("`supervisor install` declares no options; it forwards listen's filters")
	}

	for _, option := range listenProcessModelOptions() {
		name := optionLongName(option.Flags)
		if declared[name] {
			t.Errorf("`supervisor install` advertises --%s; embedding it in ExecStart installs a service that detaches and exits on every start", name)
		}
	}
	for _, option := range listenFilterOptions() {
		name := optionLongName(option.Flags)
		if !declared[name] {
			t.Errorf("`supervisor install` no longer advertises the listen filter --%s", name)
		}
	}

	var listenDeclared map[string]bool
	walkSpec(publicCommandSpec(), nil, func(path []string, command cliCommandSpec) {
		if strings.Join(path, " ") != "listen" {
			return
		}
		listenDeclared = map[string]bool{}
		for _, option := range command.Options {
			if name := optionLongName(option.Flags); name != "" {
				listenDeclared[name] = true
			}
		}
	})
	if listenDeclared == nil {
		t.Fatal("no `listen` in the command table")
	}
	for _, option := range listenProcessModelOptions() {
		name := optionLongName(option.Flags)
		if !listenDeclared[name] {
			t.Errorf("`listen` no longer advertises --%s; only the supervised copy withholds it", name)
		}
	}
}

// TestSupervisorInstallRejectsProcessModelFlags covers the same rule at the
// binary's own boundary. The emitted spec stops `agent-relay file supervisor
// install --background`, but `relayfile supervisor install --background` run
// directly reaches supervisorInstall with that argv, and it used to write it
// straight into ExecStart.
//
// HOME is a temp dir, so a unit written despite the rejection is visible as a
// file. PATH is emptied as well: the guard returns before any supervisor
// process runs, and if it ever stops doing so this test must fail on the
// assertions below rather than enable a real service on the machine running
// it.
func TestSupervisorInstallRejectsProcessModelFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--background"},
		{"-background"},
		{"--daemonized=true"},
		{"--path", "/linear/**", "--background", "--run", "notify"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("PATH", t.TempDir())

			err := supervisorInstall(args, io.Discard)
			if err == nil {
				t.Fatalf("supervisorInstall(%q) succeeded; it must refuse a process-model flag", args)
			}
			if !strings.Contains(err.Error(), "cannot embed") {
				t.Fatalf("supervisorInstall(%q) error = %v, want a refusal naming the flag", args, err)
			}

			unit := filepath.Join(home, ".config", "systemd", "user")
			if entries, readErr := os.ReadDir(unit); readErr == nil && len(entries) > 0 {
				t.Fatalf("supervisorInstall(%q) wrote %d unit file(s) despite refusing", args, len(entries))
			}
			plist := filepath.Join(home, "Library", "LaunchAgents")
			if entries, readErr := os.ReadDir(plist); readErr == nil && len(entries) > 0 {
				t.Fatalf("supervisorInstall(%q) wrote %d plist(s) despite refusing", args, len(entries))
			}
		})
	}
}

// TestSupervisorInstallAcceptsListenFilters is the negative control: the guard
// must reject the process model only, not the filters the feature exists for.
func TestSupervisorInstallAcceptsListenFilters(t *testing.T) {
	for _, option := range listenFilterOptions() {
		name := optionLongName(option.Flags)
		if err := rejectSupervisorProcessModelFlags([]string{"--" + name, "value"}); err != nil {
			t.Errorf("supervisor install refuses the listen filter --%s: %v", name, err)
		}
	}
}
