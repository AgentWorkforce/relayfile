package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/delegatedauth"
	"github.com/agentworkforce/relayfile/internal/mountscope"
	"github.com/agentworkforce/relayfile/internal/mountsync"
)

func TestWorkspaceCreateStoresCatalogEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if err := saveCredentials(credentials{
		Server: defaultServerURL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "create", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace create failed: %v", err)
	}

	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		t.Fatalf("loadWorkspaceCatalog failed: %v", err)
	}
	if len(catalog.Workspaces) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(catalog.Workspaces))
	}
	if catalog.Workspaces[0].Name != "demo" {
		t.Fatalf("expected workspace demo, got %q", catalog.Workspaces[0].Name)
	}
	if catalog.Workspaces[0].ID != "demo" {
		t.Fatalf("expected workspace id demo, got %q", catalog.Workspaces[0].ID)
	}
}

func TestResolveServerDefaultsToHostedRelayfile(t *testing.T) {
	clearRelayfileEnv(t)

	if got := resolveServer("", credentials{}); got != "https://file.agentrelay.com" {
		t.Fatalf("expected hosted Relayfile default server, got %q", got)
	}
}

func TestEnforcePollIntervalFloor(t *testing.T) {
	if got := enforcePollIntervalFloor(time.Second); got != minMountPollInterval {
		t.Fatalf("expected interval floor %s, got %s", minMountPollInterval, got)
	}
	if got := enforcePollIntervalFloor(defaultMountInterval); got != defaultMountInterval {
		t.Fatalf("expected default interval passthrough, got %s", got)
	}
	if got := jitteredIntervalWithSample(minMountPollInterval, 0.2, 0); got != minMountPollInterval {
		t.Fatalf("expected jittered interval floor %s, got %s", minMountPollInterval, got)
	}
	if got := jitteredIntervalWithSample(time.Second, 0, 0.5); got != minMountPollInterval {
		t.Fatalf("expected non-jittered interval floor %s, got %s", minMountPollInterval, got)
	}
}

func TestWebSocketMaintenanceDoesNotLowerReconcileCadence(t *testing.T) {
	for cycle := 1; cycle < websocketReconcileEvery; cycle++ {
		if shouldReconcileMountCycle(true, cycle) {
			t.Fatalf("websocket-enabled cycle %d reconciled before cadence floor", cycle)
		}
	}
	if !shouldReconcileMountCycle(true, websocketReconcileEvery) {
		t.Fatalf("expected websocket-enabled cycle %d to reconcile", websocketReconcileEvery)
	}
	for cycle := 1; cycle <= websocketReconcileEvery; cycle++ {
		if !shouldReconcileMountCycle(false, cycle) {
			t.Fatalf("expected websocket-disabled cycle %d to reconcile", cycle)
		}
	}
}

func TestObserverPrintsFragmentLaunchURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	token := testJWTWithWorkspace("ws_observer")
	t.Setenv("RELAYFILE_TOKEN", token)
	t.Setenv("RELAYFILE_SERVER", "https://api.example.test")

	var stdout bytes.Buffer
	if err := run([]string{"observer", "--no-open", "--url", "https://files.example.test/app"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run observer failed: %v", err)
	}

	launchURL := strings.TrimSpace(stdout.String())
	parsed, err := url.Parse(launchURL)
	if err != nil {
		t.Fatalf("parse observer url failed: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "files.example.test" || parsed.Path != "/app" {
		t.Fatalf("unexpected observer url: %s", parsed.String())
	}
	fragment := parseBrowserFragment(t, launchURL)
	if fragment.Get("baseUrl") != "https://api.example.test" {
		t.Fatalf("expected baseUrl fragment, got %q", fragment.Get("baseUrl"))
	}
	if fragment.Get("workspaceId") != "ws_observer" {
		t.Fatalf("expected workspaceId fragment, got %q", fragment.Get("workspaceId"))
	}
	if fragment.Get("token") != token {
		t.Fatalf("expected token fragment to match")
	}
}

func TestBuildObserverURLDefaultsToHostedRouterPath(t *testing.T) {
	got, err := buildObserverURL("", "https://api.example.test", "test-token", "ws_observer")
	if err != nil {
		t.Fatalf("build observer url failed: %v", err)
	}

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse observer url failed: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "agentrelay.com" || parsed.Path != "/observer/file" {
		t.Fatalf("unexpected default observer url: %s", parsed.String())
	}

	fragment := parseBrowserFragment(t, got)
	if fragment.Get("baseUrl") != "https://api.example.test" {
		t.Fatalf("expected browser-decoded baseUrl fragment, got %q", fragment.Get("baseUrl"))
	}
}

func parseBrowserFragment(t *testing.T, rawURL string) url.Values {
	t.Helper()

	_, rawFragment, ok := strings.Cut(rawURL, "#")
	if !ok {
		t.Fatalf("expected observer url to include a fragment: %s", rawURL)
	}
	if strings.Contains(rawFragment, "%25") {
		t.Fatalf("observer fragment appears double-encoded: %s", rawFragment)
	}
	fragment, err := url.ParseQuery(rawFragment)
	if err != nil {
		t.Fatalf("parse observer fragment failed: %v", err)
	}
	return fragment
}

func TestHelpFlagPrintsUsageForCommandsAndSubcommands(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "root short", args: []string{"-h"}, want: "relayfile is the RelayFile CLI."},
		{name: "root long", args: []string{"--help"}, want: "relayfile is the RelayFile CLI."},
		{name: "setup", args: []string{"setup", "-h"}, want: "Usage: relayfile setup"},
		{name: "login", args: []string{"login", "-h"}, want: "Usage: relayfile login"},
		{name: "logout", args: []string{"logout", "-h"}, want: "Usage: relayfile logout"},
		{name: "workspace group", args: []string{"workspace", "-h"}, want: "relayfile workspace create NAME"},
		{name: "workspace create", args: []string{"workspace", "create", "-h"}, want: "Usage: relayfile workspace create NAME"},
		{name: "workspace join", args: []string{"workspace", "join", "-h"}, want: "Usage: relayfile workspace join WORKSPACE_ID"},
		{name: "workspace use", args: []string{"workspace", "use", "-h"}, want: "Usage: relayfile workspace use NAME"},
		{name: "workspace list", args: []string{"workspace", "list", "-h"}, want: "Usage: relayfile workspace list"},
		{name: "workspace current", args: []string{"workspace", "current", "-h"}, want: "Usage: relayfile workspace current"},
		{name: "workspace delete", args: []string{"workspace", "delete", "-h"}, want: "Usage: relayfile workspace delete NAME"},
		{name: "integration group", args: []string{"integration", "-h"}, want: "relayfile integration search QUERY"},
		{name: "integration connect", args: []string{"integration", "connect", "-h"}, want: "Usage: relayfile integration connect PROVIDER"},
		{name: "integration available", args: []string{"integration", "available", "-h"}, want: "Usage: relayfile integration available"},
		{name: "integration catalog alias", args: []string{"integration", "catalog", "-h"}, want: "Usage: relayfile integration available"},
		{name: "integration providers alias", args: []string{"integration", "providers", "-h"}, want: "Usage: relayfile integration available"},
		{name: "integration search", args: []string{"integration", "search", "-h"}, want: "Usage: relayfile integration search QUERY"},
		{name: "integration search after query", args: []string{"integration", "search", "docker", "-h"}, want: "Usage: relayfile integration search QUERY"},
		{name: "integration list", args: []string{"integration", "list", "-h"}, want: "Usage: relayfile integration list"},
		{name: "integration disconnect", args: []string{"integration", "disconnect", "-h"}, want: "Usage: relayfile integration disconnect PROVIDER"},
		{name: "integration adopt", args: []string{"integration", "adopt", "-h"}, want: "Usage: relayfile integration adopt PROVIDER"},
		{name: "integration set metadata", args: []string{"integration", "set-metadata", "-h"}, want: "Usage: relayfile integration set-metadata PROVIDER"},
		{name: "integration bind", args: []string{"integration", "bind", "-h"}, want: "Usage: relayfile integration bind PROVIDER"},
		{name: "integration unbind", args: []string{"integration", "unbind", "-h"}, want: "Usage: relayfile integration unbind PROVIDER"},
		{name: "ops group", args: []string{"ops", "-h"}, want: "relayfile ops replay OPID"},
		{name: "ops list", args: []string{"ops", "list", "-h"}, want: "Usage: relayfile ops list"},
		{name: "ops replay", args: []string{"ops", "replay", "-h"}, want: "Usage: relayfile ops replay OPID"},
		{name: "writeback group", args: []string{"writeback", "-h"}, want: "relayfile writeback retry --opId OP"},
		{name: "writeback list", args: []string{"writeback", "list", "-h"}, want: writebackListUsage},
		{name: "writeback status", args: []string{"writeback", "status", "-h"}, want: "Usage: relayfile writeback status"},
		{name: "writeback update", args: []string{"writeback", "update", "-h"}, want: "Usage: relayfile writeback update"},
		{name: "writeback delete", args: []string{"writeback", "delete", "-h"}, want: "Usage: relayfile writeback delete"},
		{name: "writeback retry", args: []string{"writeback", "retry", "-h"}, want: "Usage: relayfile writeback retry --opId OP"},
		{name: "digest group", args: []string{"digest", "-h"}, want: "today|yesterday|YYYY-MM-DD|this-week|last-week"},
		{name: "digest rebuild", args: []string{"digest", "rebuild", "-h"}, want: digestRebuildUsage},
		{name: "pull", args: []string{"pull", "-h"}, want: "Usage: relayfile pull"},
		{name: "mount", args: []string{"mount", "-h"}, want: "Usage: relayfile mount"},
		{name: "start alias", args: []string{"start", "-h"}, want: "Usage: relayfile mount"},
		{name: "on alias", args: []string{"on", "-h"}, want: "Usage: relayfile mount"},
		{name: "restart", args: []string{"restart", "-h"}, want: "Usage: relayfile restart"},
		{name: "tree", args: []string{"tree", "-h"}, want: "Usage: relayfile tree"},
		{name: "ls alias", args: []string{"ls", "-h"}, want: "Usage: relayfile tree"},
		{name: "read", args: []string{"read", "-h"}, want: "Usage: relayfile read"},
		{name: "cat alias", args: []string{"cat", "-h"}, want: "Usage: relayfile read"},
		{name: "seed", args: []string{"seed", "-h"}, want: "Usage: relayfile seed"},
		{name: "export", args: []string{"export", "-h"}, want: "Usage: relayfile export"},
		{name: "status", args: []string{"status", "-h"}, want: "Usage: relayfile status"},
		{name: "stop", args: []string{"stop", "-h"}, want: "Usage: relayfile stop"},
		{name: "off alias", args: []string{"off", "-h"}, want: "Usage: relayfile stop"},
		{name: "logs", args: []string{"logs", "-h"}, want: "Usage: relayfile logs"},
		{name: "observer", args: []string{"observer", "-h"}, want: "Usage: relayfile observer"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			clearRelayfileEnv(t)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if err := run(tc.args, strings.NewReader(""), &stdout, &stderr); err != nil {
				t.Fatalf("run(%v) returned error: %v\nstdout:\n%s\nstderr:\n%s", tc.args, err, stdout.String(), stderr.String())
			}
			if got := stdout.String(); !strings.Contains(got, tc.want) {
				t.Fatalf("expected help output to contain %q, got:\n%s", tc.want, got)
			}
			if got := stderr.String(); got != "" {
				t.Fatalf("expected no stderr for help, got %q", got)
			}
		})
	}
}

func TestIntegrationBindListAndUnbind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var stdout bytes.Buffer
	if err := run([]string{
		"integration", "bind", "linear", "/linear/issues/**",
		"--channel", "#issues",
		"--webhook", "wh_1",
		"--webhook-token", "tok_1",
		"--webhook-subscription", "relayfile_whsub_1", "--webhook-subscription-workspace", "rw_test_pin",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("bind failed: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "linear binding created") {
		t.Fatalf("expected bind confirmation, got %q", stdout.String())
	}

	stdout.Reset()
	if err := run([]string{"integration", "bind", "--list"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("bind --list failed: %v", err)
	}
	var bindings []relayIntegrationBinding
	if err := json.Unmarshal(stdout.Bytes(), &bindings); err != nil {
		t.Fatalf("parse bindings list failed: %v\n%s", err, stdout.String())
	}
	if len(bindings) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(bindings))
	}
	if bindings[0].Provider != "linear" || bindings[0].PathGlob != "/linear/issues/**" || bindings[0].Channel != "#issues" || bindings[0].WebhookSubscriptionID != "relayfile_whsub_1" {
		t.Fatalf("unexpected binding: %#v", bindings[0])
	}

	// A legacy caller that does not know the webhook-subscription flag must not
	// erase the ID needed by a later agent-relay unsubscribe cleanup.
	stdout.Reset()
	if err := run([]string{
		"integration", "bind", "linear", "/linear/issues/**",
		"--channel", "#issues",
		"--webhook", "wh_2",
		"--webhook-token", "tok_2",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("replacement bind failed: %v\n%s", err, stdout.String())
	}
	stdout.Reset()
	if err := run([]string{"integration", "bind", "--list"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("bind --list after replacement failed: %v", err)
	}
	if err := json.Unmarshal(stdout.Bytes(), &bindings); err != nil {
		t.Fatalf("parse replacement bindings failed: %v\n%s", err, stdout.String())
	}
	if len(bindings) != 1 || bindings[0].WebhookID != "wh_2" || bindings[0].WebhookSubscriptionID != "relayfile_whsub_1" {
		t.Fatalf("replacement lost webhook subscription id: %#v", bindings)
	}

	stdout.Reset()
	if err := run([]string{"integration", "unbind", "linear", "--resource", "/linear/issues/**"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("unbind failed: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "linear binding removed") {
		t.Fatalf("expected unbind confirmation, got %q", stdout.String())
	}

	stdout.Reset()
	if err := run([]string{"integration", "bind", "--list"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("bind --list after unbind failed: %v", err)
	}
	if err := json.Unmarshal(stdout.Bytes(), &bindings); err != nil {
		t.Fatalf("parse empty bindings list failed: %v\n%s", err, stdout.String())
	}
	if len(bindings) != 0 {
		t.Fatalf("expected bindings to be empty, got %#v", bindings)
	}
}

func TestIntegrationBindUsageListsOptionalSubscriptionFlags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var stdout bytes.Buffer
	err := run([]string{"integration", "bind", "slack"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatal("integration bind with missing resource unexpectedly succeeded")
	}
	if got, want := err.Error(), "[--subscription ID] [--webhook-subscription ID --webhook-subscription-workspace WS]"; !strings.Contains(got, want) {
		t.Fatalf("usage = %q, want to contain %q", got, want)
	}
}

func TestIntegrationUnbindNativeResourceRemovesStoredFallbackGlob(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	localDir := t.TempDir()
	if _, err := upsertWorkspaceDetails(workspaceRecord{Name: "demo", ID: "ws_demo", LocalDir: localDir}); err != nil {
		t.Fatalf("upsert workspace failed: %v", err)
	}
	writeTestMountFile(t, localDir, "slack/channels/by-name/watchdog-test.json", `{"id":"C123","canonicalPath":"/slack/channels/C123__watchdog-test/meta.json"}`)

	if err := writeRelayIntegrationBindings([]relayIntegrationBinding{
		{
			Provider:     "slack",
			PathGlob:     "/slack/channels/*/**",
			Channel:      "#events",
			WebhookID:    "wh_1",
			WebhookToken: "tok_1",
		},
		{
			Provider:     "github",
			PathGlob:     "/github/repos/AgentWorkforce/relayfile/**",
			Channel:      "#events",
			WebhookID:    "wh_2",
			WebhookToken: "tok_2",
		},
	}); err != nil {
		t.Fatalf("seed bindings failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"integration", "unbind", "slack", "--resource", "#watchdog-test"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("unbind failed: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "slack binding removed") {
		t.Fatalf("expected unbind confirmation, got %q", stdout.String())
	}

	bindings, err := readRelayIntegrationBindings()
	if err != nil {
		t.Fatalf("read bindings failed: %v", err)
	}
	if len(bindings) != 1 || bindings[0].Provider != "github" {
		t.Fatalf("expected only github binding to remain, got %#v", bindings)
	}
}

func TestIntegrationBindResolvesNativeResources(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		resource  string
		files     map[string]string
		wantGlob  string
		wantWarn  bool
		wantError string
	}{
		{
			name:     "explicit glob passes through",
			provider: "slack",
			resource: "/slack/channels/C123__watchdog-test/**",
			wantGlob: "/slack/channels/C123__watchdog-test/**",
		},
		{
			name:     "slack channel name resolves through by-name alias",
			provider: "slack",
			resource: "#watchdog-test",
			files: map[string]string{
				"slack/channels/by-name/watchdog-test.json": `{"id":"C123","canonicalPath":"/slack/channels/C123__watchdog-test/meta.json"}`,
			},
			wantGlob: "/slack/channels/C123__watchdog-test/**",
		},
		{
			name:     "github owner repo maps to repo subtree",
			provider: "github",
			resource: "AgentWorkforce/relayfile",
			wantGlob: "/github/repos/AgentWorkforce/relayfile/**",
		},
		{
			name:     "linear team key resolves by mounted team payload",
			provider: "linear",
			resource: "AR",
			files: map[string]string{
				"linear/teams/team-ar.json": `{"provider":"linear","payload":{"id":"team-ar","key":"AR","name":"Agent Relay"}}`,
			},
			wantGlob: "/linear/teams/team-ar.json",
		},
		{
			name:     "telegram chat title resolves through id-suffixed alias",
			provider: "telegram",
			resource: "@release-room",
			files: map[string]string{
				"telegram/chats/by-username/release-room__-100123.json": `{"id":"-100123","canonicalPath":"/telegram/chats/-100123__release-room/meta.json"}`,
			},
			wantGlob: "/telegram/chats/-100123__release-room/**",
		},
		{
			name:     "unresolved slack name warns and binds matcher-safe fallback",
			provider: "slack",
			resource: "#missing-channel",
			wantGlob: "/slack/channels/**",
			wantWarn: true,
		},
		{
			name:      "unknown native provider still requires explicit VFS glob",
			provider:  "notion",
			resource:  "Engineering",
			wantError: `provider "notion" has no native resource resolver`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			clearRelayfileEnv(t)
			localDir := t.TempDir()
			if _, err := upsertWorkspaceDetails(workspaceRecord{Name: "demo", ID: "ws_demo", LocalDir: localDir}); err != nil {
				t.Fatalf("upsert workspace failed: %v", err)
			}
			for rel, content := range tc.files {
				writeTestMountFile(t, localDir, rel, content)
			}

			var stdout bytes.Buffer
			err := run([]string{
				"integration", "bind", tc.provider, tc.resource,
				"--channel", "#events",
				"--webhook", "wh_1",
				"--webhook-token", "tok_1",
			}, strings.NewReader(""), &stdout, &stdout)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected error containing %q, got err=%v output=%q", tc.wantError, err, stdout.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("bind failed: %v\n%s", err, stdout.String())
			}
			if tc.wantWarn && !strings.Contains(stdout.String(), "Warning:") {
				t.Fatalf("expected warning, got %q", stdout.String())
			}
			bindings, err := readRelayIntegrationBindings()
			if err != nil {
				t.Fatalf("read bindings failed: %v", err)
			}
			if len(bindings) != 1 {
				t.Fatalf("expected one binding, got %#v", bindings)
			}
			if bindings[0].PathGlob != tc.wantGlob {
				t.Fatalf("expected glob %q, got %#v", tc.wantGlob, bindings[0])
			}
		})
	}
}

func writeTestMountFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir mount fixture failed: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write mount fixture failed: %v", err)
	}
}

// TestOnOffAliasesDispatchLikeMountAndStop verifies that the `on` and `off`
// verbs migrated from the agent-relay CLI route to the same handlers as
// `mount` and `stop`. We assert on the handler-specific error surfaced for an
// identical invocation: `on`/`mount` with no credentials demand a token, and
// `off`/`stop` for an unknown workspace report the same not-found error.
func TestOnOffAliasesDispatchLikeMountAndStop(t *testing.T) {
	const unknownWorkspace = "ws_does_not_exist"

	runVerb := func(t *testing.T, args []string) (string, error) {
		t.Helper()
		t.Setenv("HOME", t.TempDir())
		clearRelayfileEnv(t)
		var stdout, stderr bytes.Buffer
		err := run(args, strings.NewReader(""), &stdout, &stderr)
		return stdout.String(), err
	}

	t.Run("on matches mount when token is missing", func(t *testing.T) {
		_, mountErr := runVerb(t, []string{"mount", unknownWorkspace})
		_, onErr := runVerb(t, []string{"on", unknownWorkspace})
		if mountErr == nil || onErr == nil {
			t.Fatalf("expected both mount and on to error without a token; mount=%v on=%v", mountErr, onErr)
		}
		const wantFragment = "delegated relayfile credentials are required"
		if !strings.Contains(mountErr.Error(), wantFragment) {
			t.Fatalf("unexpected mount error shape: %q", mountErr.Error())
		}
		if !strings.Contains(onErr.Error(), wantFragment) {
			t.Fatalf("on did not dispatch like mount: %q", onErr.Error())
		}
	})

	t.Run("off matches stop for an unknown workspace", func(t *testing.T) {
		_, stopErr := runVerb(t, []string{"stop", unknownWorkspace})
		_, offErr := runVerb(t, []string{"off", unknownWorkspace})
		if stopErr == nil || offErr == nil {
			t.Fatalf("expected both stop and off to error for unknown workspace; stop=%v off=%v", stopErr, offErr)
		}
		// Each invocation runs under its own temp HOME, so the workspaces.json
		// path embedded in the error differs. Compare the stable, path-free
		// prefix to prove `off` reached the same handler as `stop`.
		const wantPrefix = `workspace "` + unknownWorkspace + `" not found`
		if !strings.HasPrefix(stopErr.Error(), wantPrefix) {
			t.Fatalf("unexpected stop error shape: %q", stopErr.Error())
		}
		if !strings.HasPrefix(offErr.Error(), wantPrefix) {
			t.Fatalf("off did not dispatch like stop: %q", offErr.Error())
		}
	})
}

func TestWorkspaceRequiresSubcommandMentionsJoin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var stdout bytes.Buffer
	err := run([]string{"workspace"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatal("expected workspace without subcommand to fail")
	}
	if got := err.Error(); !strings.Contains(got, "create, join, use, list, current, status, or delete") {
		t.Fatalf("workspace subcommand error = %q", got)
	}
}

func TestWorkspaceUseSetsDefaultWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	logPath := filepath.Join(t.TempDir(), "agent-relay.log")
	t.Setenv("AGENT_RELAY_LOG", logPath)
	installFakeAgentRelay(t, `
printf '%s\n' "$*" >> "$AGENT_RELAY_LOG"
if [ "$*" = "workspace switch ws_cloud" ]; then
  echo "agent-relay workspace switch ok"
  exit 0
fi
echo "unexpected args: $*" >&2
exit 2
`)

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "use", "ws_cloud"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace use failed: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake agent-relay log failed: %v", err)
	}
	if strings.TrimSpace(string(logBytes)) != "workspace switch ws_cloud" {
		t.Fatalf("expected agent-relay workspace switch call, got %q", string(logBytes))
	}
	if got := stdout.String(); !strings.Contains(got, "Relayfile uses the active agent-relay workspace") {
		t.Fatalf("unexpected workspace use output: %q", got)
	}
}

func TestWorkspaceJoinStoresDelegatedRelayfileCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var seenBootstrap bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspaces/ws_cloud/relayfile/delegated-token" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		seenBootstrap = true
		if got := r.Header.Get("Authorization"); got != "Bearer cld_access" {
			t.Fatalf("unexpected bootstrap Authorization: %q", got)
		}
		var body cloudRelayfileDelegatedTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode bootstrap body: %v", err)
		}
		if got, want := strings.Join(body.Scopes, ","), "relayfile:fs:read:*"; got != want {
			t.Fatalf("bootstrap scopes = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		writeDelegatedBundleResponse(t, w, "https://relayfile.test", "ws_cloud", "rf_read", "refresh_read")
	}))
	defer server.Close()
	installFakeAgentRelaySession(t, server.URL, "cld_access", "cloud-prod", "ws_cloud", "ws_cloud")

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "join", "ws_cloud", "--name", "cloud-prod", "--cloud-api-url", server.URL}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace join failed: %v", err)
	}
	if !seenBootstrap {
		t.Fatalf("expected delegated-token bootstrap call")
	}
	if _, err := os.Stat(credentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected workspace join not to persist relayfile token credentials, got err=%v", err)
	}
	delegated, err := delegatedauth.Load(delegatedCredentialsPathForRequest("ws_cloud", defaultInspectScopes))
	if err != nil {
		t.Fatalf("expected delegated credentials to be stored: %v", err)
	}
	if delegated.BearerToken() != "rf_read" || delegated.RotationToken() != "refresh_read" {
		t.Fatalf("unexpected delegated credentials: %#v", delegated)
	}
	record, ok := workspaceRecordByID("ws_cloud")
	if !ok {
		t.Fatalf("expected workspace catalog record")
	}
	if record.Name != "cloud-prod" || strings.Join(record.Scopes, ",") != "relayfile:fs:read:*" {
		t.Fatalf("unexpected workspace record: %#v", record)
	}
	if got := stdout.String(); !strings.Contains(got, "Joined workspace cloud-prod") {
		t.Fatalf("unexpected stdout: %q", got)
	}
}

func TestTreeJoinsCloudWorkspaceWithoutServerCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:             "cloud-prod",
		ID:               "ws_cloud",
		RelayWorkspaceID: "rw_cloud",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		AgentName:        "relayfile-cli",
		Scopes:           append([]string(nil), defaultJoinScopes...),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/workspaces/rw_cloud/fs/tree":
			if got := r.Header.Get("Authorization"); got != "Bearer rf_delegated" {
				t.Fatalf("unexpected tree Authorization: %q", got)
			}
			if got := r.URL.Query().Get("path"); got != "/google-mail" {
				t.Fatalf("unexpected tree path: %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"path":"/google-mail","entries":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:          server.URL,
		RelayfileWorkspaceID:  "rw_cloud",
		AccessToken:           "rf_delegated",
		RefreshToken:          "refresh_delegated",
		AccessTokenExpiresAt:  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		RelayauthURL:          server.URL,
	})

	var stdout bytes.Buffer
	if err := run([]string{"tree", "ws_cloud", "/google-mail"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run tree failed: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "Tree /google-mail") {
		t.Fatalf("unexpected stdout: %q", got)
	}
}

func TestTreeRefreshesDelegatedWorkspaceTokenAndRetriesCanonicalWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	oldToken := testJWTWithWorkspaceAgentAndExpiry("ws_cloud", "old", time.Now().Add(-time.Minute))
	newToken := testJWTWithWorkspaceAndAgent("rw_cloud", "new")
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:             "cloud-prod",
		ID:               "ws_cloud",
		RelayWorkspaceID: "rw_cloud",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		AgentName:        "relayfile-cli",
		Scopes:           append([]string(nil), defaultInspectScopes...),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	if _, err := setDefaultWorkspace("cloud-prod"); err != nil {
		t.Fatalf("setDefaultWorkspace failed: %v", err)
	}

	var refreshCount int
	var treeCount int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tokens/refresh":
			refreshCount++
			var body struct {
				RefreshToken string `json:"refreshToken"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode refresh body: %v", err)
			}
			if body.RefreshToken != "refresh_old" {
				t.Fatalf("unexpected refresh token %q", body.RefreshToken)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"accessToken":           newToken,
				"refreshToken":          "refresh_new",
				"accessTokenExpiresAt":  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				"refreshTokenExpiresAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
			})
		case "/v1/workspaces/rw_cloud/fs/tree":
			treeCount++
			if got := r.Header.Get("Authorization"); got != "Bearer "+newToken {
				t.Fatalf("unexpected refreshed tree Authorization: %q", got)
			}
			if got := r.URL.Query().Get("path"); got != "/google-mail" {
				t.Fatalf("unexpected tree path: %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"path":"/google-mail","entries":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:          server.URL,
		RelayfileWorkspaceID:  "rw_cloud",
		AccessToken:           oldToken,
		RefreshToken:          "refresh_old",
		AccessTokenExpiresAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		RelayauthURL:          server.URL,
	})

	var stdout bytes.Buffer
	if err := run([]string{"tree", "cloud-prod", "/google-mail"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run tree failed: %v", err)
	}
	if refreshCount != 1 || treeCount != 1 {
		t.Fatalf("refreshCount/treeCount = %d/%d, want 1/1", refreshCount, treeCount)
	}
	record, ok := workspaceRecordByName("cloud-prod")
	if !ok {
		t.Fatalf("expected refreshed workspace catalog record")
	}
	if record.ID != "ws_cloud" {
		t.Fatalf("workspace record ID = %q, want ws_cloud", record.ID)
	}
	if record.RelayWorkspaceID != "rw_cloud" {
		t.Fatalf("relay workspace ID = %q, want rw_cloud", record.RelayWorkspaceID)
	}
	if record.Server != server.URL {
		t.Fatalf("workspace record Server = %q, want %q", record.Server, server.URL)
	}
	if record.CloudAPIURL != "" {
		t.Fatalf("workspace record CloudAPIURL = %q, want empty", record.CloudAPIURL)
	}
	if record.LastUsedAt == "" {
		t.Fatal("workspace record LastUsedAt was not refreshed")
	}
	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		t.Fatalf("loadWorkspaceCatalog failed: %v", err)
	}
	if catalog.Default != "cloud-prod" {
		t.Fatalf("default workspace = %q, want cloud-prod", catalog.Default)
	}
	if got := stdout.String(); !strings.Contains(got, "Tree /google-mail") {
		t.Fatalf("unexpected stdout: %q", got)
	}
}

func TestReadRefreshesDelegatedWorkspaceTokenAfterUnauthorized(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	oldToken := testJWTWithWorkspaceAgentAndExpiry("rw_cloud", "old", time.Now().Add(-time.Minute))
	newToken := testJWTWithWorkspaceAndAgent("rw_cloud", "new")
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:             "cloud-prod",
		ID:               "ws_cloud",
		RelayWorkspaceID: "rw_cloud",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		AgentName:        "relayfile-cli",
		Scopes:           append([]string(nil), defaultInspectScopes...),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:      "other-prod",
		ID:        "ws_other",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails(other) failed: %v", err)
	}
	if _, err := setDefaultWorkspace("other-prod"); err != nil {
		t.Fatalf("setDefaultWorkspace(other) failed: %v", err)
	}

	var readCount int
	var refreshCount int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tokens/refresh":
			refreshCount++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"accessToken":           newToken,
				"refreshToken":          "refresh_new",
				"accessTokenExpiresAt":  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				"refreshTokenExpiresAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
			})
		case "/v1/workspaces/rw_cloud/fs/file":
			readCount++
			if got := r.Header.Get("Authorization"); got != "Bearer "+newToken {
				t.Fatalf("unexpected refreshed read Authorization: %q", got)
			}
			if got := r.URL.Query().Get("path"); got != "/google-mail/msg.json" {
				t.Fatalf("unexpected read path: %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"path":"/google-mail/msg.json","revision":"1","contentType":"application/json","content":"ok"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:          server.URL,
		RelayfileWorkspaceID:  "rw_cloud",
		AccessToken:           oldToken,
		RefreshToken:          "refresh_old",
		AccessTokenExpiresAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		RelayauthURL:          server.URL,
	})

	var stdout bytes.Buffer
	if err := run([]string{"read", "cloud-prod", "/google-mail/msg.json"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run read failed: %v", err)
	}
	if refreshCount != 1 || readCount != 1 {
		t.Fatalf("refreshCount/readCount = %d/%d, want 1/1", refreshCount, readCount)
	}
	record, ok := workspaceRecordByName("cloud-prod")
	if !ok {
		t.Fatalf("expected refreshed workspace catalog record")
	}
	if record.ID != "ws_cloud" {
		t.Fatalf("workspace record ID = %q, want ws_cloud", record.ID)
	}
	if record.RelayWorkspaceID != "rw_cloud" {
		t.Fatalf("relay workspace ID = %q, want rw_cloud", record.RelayWorkspaceID)
	}
	if record.Server != server.URL {
		t.Fatalf("workspace record Server = %q, want %q", record.Server, server.URL)
	}
	if record.CloudAPIURL != "" {
		t.Fatalf("workspace record CloudAPIURL = %q, want empty", record.CloudAPIURL)
	}
	if record.LastUsedAt == "" {
		t.Fatal("workspace record LastUsedAt was not refreshed")
	}
	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		t.Fatalf("loadWorkspaceCatalog failed: %v", err)
	}
	if catalog.Default != "other-prod" {
		t.Fatalf("default workspace = %q, want other-prod", catalog.Default)
	}
	if got := stdout.String(); got != "ok" {
		t.Fatalf("unexpected stdout: %q", got)
	}
}

func TestWorkspaceDeleteRemovesCatalogEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if _, err := upsertWorkspace("demo"); err != nil {
		t.Fatalf("upsertWorkspace failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "delete", "demo", "--yes"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace delete failed: %v", err)
	}

	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		t.Fatalf("loadWorkspaceCatalog failed: %v", err)
	}
	if len(catalog.Workspaces) != 0 {
		t.Fatalf("expected workspace catalog to be empty, got %d entries", len(catalog.Workspaces))
	}
}

func TestWorkspaceListIncludesEnvWorkspaceWhenRemoteListUnavailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Setenv("RELAYFILE_WORKSPACE", "ws_env")
	if _, err := upsertWorkspace(".relay/vfs"); err != nil {
		t.Fatalf("upsertWorkspace .relay/vfs failed: %v", err)
	}
	if _, err := upsertWorkspace("relay-test"); err != nil {
		t.Fatalf("upsertWorkspace relay-test failed: %v", err)
	}

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "list"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace list failed: %v", err)
	}

	if got := strings.TrimSpace(stdout.String()); got != "* ws_env\n  .relay/vfs\n  relay-test" {
		t.Fatalf("unexpected workspace list output: %q", got)
	}
}

func TestWorkspaceListIncludesTokenWorkspaceWhenRemoteListUnavailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  testJWTWithWorkspace("ws_token"),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "list"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace list failed: %v", err)
	}

	if got := strings.TrimSpace(stdout.String()); got != "* ws_token" {
		t.Fatalf("unexpected workspace list output: %q", got)
	}
}

func TestWorkspaceListFallsBackToAdminSync(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/admin/workspaces":
			http.NotFound(w, r)
		case "/v1/admin/sync":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"workspaceIds":["ws_bravo","ws_alpha"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "list"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace list failed: %v", err)
	}

	if got := strings.TrimSpace(stdout.String()); got != "ws_alpha\nws_bravo" {
		t.Fatalf("unexpected workspace list output: %q", got)
	}
}

func TestTreeUsesEnvWorkspaceWhenWorkspaceArgOmitted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Setenv("RELAYFILE_WORKSPACE", "ws_env")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspaces/ws_env/fs/tree" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("path"); got != "/github" {
			t.Fatalf("expected path /github, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/github","entries":[],"nextCursor":null}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_env",
		AccessToken:          "token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"tree", "/github"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run tree failed: %v", err)
	}

	if got := stdout.String(); !strings.Contains(got, "Tree /github") {
		t.Fatalf("expected tree header, got %q", got)
	}
}

func TestTreeTreatsPathLikeSingleArgAsRemotePath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Setenv("RELAYFILE_WORKSPACE", "ws_env")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspaces/ws_env/fs/tree" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("path"); got != "/linear/projects" {
			t.Fatalf("expected path /linear/projects, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/linear/projects","entries":[],"nextCursor":null}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_env",
		AccessToken:          "token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"tree", "linear/projects"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run tree failed: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "Tree /linear/projects") {
		t.Fatalf("expected tree header, got %q", got)
	}
}

func TestTreePreservesUncatalogedSlashWorkspaceWhenRootIsUnknown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.EscapedPath(); got != "/v1/workspaces/team%2Fproject/fs/tree" {
			t.Fatalf("unexpected escaped path: %s", got)
		}
		if got := r.URL.Query().Get("path"); got != "/" {
			t.Fatalf("expected root path, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/","entries":[],"nextCursor":null}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "team/project",
		AccessToken:          "token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"tree", "team/project"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run tree failed: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "Tree /") {
		t.Fatalf("expected tree header, got %q", got)
	}
}

func TestTreeReadsRemoteWorkspacePath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspaces/ws_cloud/fs/tree" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("path"); got != "/github" {
			t.Fatalf("expected path /github, got %q", got)
		}
		if got := r.URL.Query().Get("depth"); got != "5" {
			t.Fatalf("expected depth 5, got %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("unexpected Authorization header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/github","entries":[{"path":"/github/repos","type":"dir","revision":"rev_dir"},{"path":"/github/README.md","type":"file","revision":"rev_file","provider":"github","size":12}],"nextCursor":null}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_cloud",
		AccessToken:          "token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"tree", "ws_cloud", "/github", "--depth", "5"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run tree failed: %v", err)
	}

	got := stdout.String()
	if !strings.Contains(got, "Tree /github") {
		t.Fatalf("expected tree header, got %q", got)
	}
	if !strings.Contains(got, "dir  /github/repos") {
		t.Fatalf("expected dir entry, got %q", got)
	}
	if !strings.Contains(got, "file /github/README.md  provider=github  size=12  rev=rev_file") {
		t.Fatalf("expected file metadata entry, got %q", got)
	}
}

func TestTreeCanPrintPrettyJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/","entries":[],"nextCursor":null}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_cloud",
		AccessToken:          "token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"tree", "ws_cloud", "--json"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run tree --json failed: %v", err)
	}

	if got := stdout.String(); !strings.Contains(got, "\n  \"path\": \"/\"") {
		t.Fatalf("expected pretty JSON, got %q", got)
	}
}

func TestReadPrintsRemoteFileContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspaces/ws_cloud/fs/file" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("path"); got != "/github/README.md" {
			t.Fatalf("expected file path, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/github/README.md","revision":"rev_1","contentType":"text/markdown","content":"# readme\n"}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_cloud",
		AccessToken:          "token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"read", "ws_cloud", "/github/README.md"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run read failed: %v", err)
	}

	if got := stdout.String(); got != "# readme\n" {
		t.Fatalf("unexpected file content: %q", got)
	}
}

func TestReadUsesTokenWorkspaceWhenWorkspaceArgOmitted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	token := testJWTWithWorkspace("ws_token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspaces/ws_token/fs/file" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("path"); got != "/docs/readme.md" {
			t.Fatalf("expected file path, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/docs/readme.md","revision":"rev_1","contentType":"text/markdown","content":"ok\n"}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_token",
		AccessToken:          token,
	})

	var stdout bytes.Buffer
	if err := run([]string{"read", "/docs/readme.md"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run read failed: %v", err)
	}

	if got := stdout.String(); got != "ok\n" {
		t.Fatalf("unexpected file content: %q", got)
	}
}

func TestReadDecodesBase64Content(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	encoded := base64.StdEncoding.EncodeToString([]byte{0x00, 0x7f, 0xff, 0x10})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/external/blob.bin","revision":"rev_1","contentType":"application/octet-stream","content":"` + encoded + `","encoding":"base64"}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_cloud",
		AccessToken:          "token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"cat", "ws_cloud", "/external/blob.bin"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run cat failed: %v", err)
	}

	if got := stdout.Bytes(); !bytes.Equal(got, []byte{0x00, 0x7f, 0xff, 0x10}) {
		t.Fatalf("unexpected decoded content: %#v", got)
	}
}

func TestCollectSeedFilesReportsProgress(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatalf("write a.txt failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("bravo"), 0o644); err != nil {
		t.Fatalf("write b.txt failed: %v", err)
	}

	var stdout bytes.Buffer
	files, err := collectSeedFiles(root, &stdout)
	if err != nil {
		t.Fatalf("collectSeedFiles failed: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if !strings.Contains(stdout.String(), "Seeding 2/2 files...") {
		t.Fatalf("expected progress output, got %q", stdout.String())
	}
}

func TestBuildCloudURLKeepsBasePath(t *testing.T) {
	got, err := buildCloudURL("https://agentrelay.test/cloud", "api/v1/cli/login")
	if err != nil {
		t.Fatalf("buildCloudURL failed: %v", err)
	}
	if got.String() != "https://agentrelay.test/cloud/api/v1/cli/login" {
		t.Fatalf("unexpected cloud URL: %s", got.String())
	}
}

func TestSetupCreatesWorkspaceConnectsIntegrationAndSkipsMount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Cleanup(func() { _ = os.RemoveAll("vfs") })

	seen := map[string]bool{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces":
			seen["create"] = true
			if r.Method != http.MethodPost {
				t.Fatalf("expected create POST, got %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cld_test" {
				t.Fatalf("unexpected create Authorization: %q", got)
			}
			var body cloudWorkspaceCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create body failed: %v", err)
			}
			if body.Name != "demo" {
				t.Fatalf("expected workspace name demo, got %q", body.Name)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","relayfileUrl":"https://relayfile.test","createdAt":"2026-05-01T00:00:00Z","name":"demo"}`))
		case "/api/v1/workspaces/ws_123/relayfile/delegated-token":
			seen["delegated"] = true
			if r.Method != http.MethodPost {
				t.Fatalf("expected delegated-token POST, got %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cld_test" {
				t.Fatalf("unexpected delegated-token Authorization: %q", got)
			}
			writeDelegatedBundleResponse(t, w, "https://relayfile.test", "ws_123", "rf_join", "refresh_join")
		case "/api/v1/workspaces/ws_123/integrations/connect-session":
			seen["connect"] = true
			if r.Method != http.MethodPost {
				t.Fatalf("expected connect POST, got %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cld_test" {
				t.Fatalf("unexpected connect Authorization: %q", got)
			}
			var body cloudConnectSessionRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode connect body failed: %v", err)
			}
			if len(body.AllowedIntegrations) != 1 || body.AllowedIntegrations[0] != "github" {
				t.Fatalf("unexpected allowed integrations: %#v", body.AllowedIntegrations)
			}
			_, _ = w.Write([]byte(`{"token":"session_token","expiresAt":"2026-05-01T01:00:00Z","connectLink":"https://connect.test/github","connectionId":"conn_123"}`))
		case "/api/v1/workspaces/ws_123/integrations/github/status":
			seen["status"] = true
			if got := r.Header.Get("Authorization"); got != "Bearer cld_test" {
				t.Fatalf("unexpected status Authorization: %q", got)
			}
			if got := r.URL.Query().Get("connectionId"); got != "conn_123" {
				t.Fatalf("unexpected connectionId: %q", got)
			}
			_, _ = w.Write([]byte(`{"ready":true}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run([]string{
		"setup",
		"--cloud-api-url", server.URL,
		"--cloud-token", "cld_test",
		"--workspace", "demo",
		"--provider", "github",
		"--local-dir", "./vfs",
		"--no-open",
		"--skip-mount",
	}, strings.NewReader(""), &stdout, &stdout)
	if err != nil {
		t.Fatalf("run setup failed: %v\noutput:\n%s", err, stdout.String())
	}
	for _, key := range []string{"create", "delegated", "connect", "status"} {
		if !seen[key] {
			t.Fatalf("expected %s request", key)
		}
	}

	if _, err := os.Stat(credentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected setup not to persist relayfile token credentials, got err=%v", err)
	}
	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		t.Fatalf("loadWorkspaceCatalog failed: %v", err)
	}
	if catalog.Default != "demo" || len(catalog.Workspaces) != 1 || catalog.Workspaces[0].ID != "ws_123" {
		t.Fatalf("unexpected workspace catalog: %#v", catalog)
	}
	gotOutput := stdout.String()
	if !strings.Contains(gotOutput, "Connect github: https://connect.test/github") {
		t.Fatalf("expected printed connect link, got %q", gotOutput)
	}
	if !strings.Contains(gotOutput, "relayfile mount ws_123 ./vfs") {
		t.Fatalf("expected mount command guidance, got %q", gotOutput)
	}
}

func TestSetupJiraRunsSitePickerAfterFreshConnect(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Cleanup(func() { _ = os.RemoveAll("vfs") })

	seen := map[string]bool{}
	var picked map[string]any
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces":
			seen["create"] = true
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","relayfileUrl":"https://relayfile.test","createdAt":"2026-05-01T00:00:00Z","name":"demo"}`))
		case "/api/v1/workspaces/ws_123/relayfile/delegated-token":
			seen["delegated"] = true
			writeDelegatedBundleResponse(t, w, "https://relayfile.test", "ws_123", "rf_join", "refresh_join")
		case "/api/v1/workspaces/ws_123/integrations/connect-session":
			seen["connect"] = true
			_, _ = w.Write([]byte(`{"connectionId":"conn_jira","connectLink":"","backend":"nango"}`))
		case "/api/v1/workspaces/ws_123/integrations/jira/status":
			seen["status"] = true
			_, _ = w.Write([]byte(`{"ready":true,"state":"ready","provider":"jira","backend":"nango"}`))
		case "/api/v1/workspaces/ws_123/integrations/jira/accessible-resources":
			seen["accessible-resources"] = true
			_, _ = w.Write([]byte(`{"ok":true,"resources":[{"id":"cloud-1","url":"https://foo.atlassian.net","name":"Foo"}]}`))
		case "/api/v1/workspaces/ws_123/integrations/jira/metadata":
			seen["metadata"] = true
			if r.Method != http.MethodPut {
				t.Fatalf("expected metadata PUT, got %s", r.Method)
			}
			var body map[string]map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode metadata body failed: %v", err)
			}
			picked = body["metadata"]
			_, _ = w.Write([]byte(`{"ok":true,"metadata":` + jsonMarshalOrPanic(picked) + `}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run([]string{
		"setup",
		"--cloud-api-url", server.URL,
		"--cloud-token", "cld_test",
		"--workspace", "demo",
		"--provider", "jira",
		"--local-dir", "./vfs",
		"--no-open",
		"--skip-mount",
	}, strings.NewReader(""), &stdout, &stdout)
	if err != nil {
		t.Fatalf("run setup failed: %v\noutput:\n%s", err, stdout.String())
	}
	for _, key := range []string{"create", "delegated", "connect", "status", "accessible-resources", "metadata"} {
		if !seen[key] {
			t.Fatalf("expected %s request", key)
		}
	}
	if picked["cloudId"] != "cloud-1" || picked["baseUrl"] != "https://foo.atlassian.net" {
		t.Fatalf("unexpected site metadata: %#v", picked)
	}
}

func TestIntegrationConnectRefreshesCloudAccessTokenAndReusesWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_123",
		LocalDir:   localDir,
		AgentName:  "relayfile-cli",
		Scopes:     append([]string(nil), defaultJoinScopes...),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	seen := map[string]bool{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_123/relayfile/delegated-token":
			seen["delegated"] = true
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected delegated-token Authorization: %q", got)
			}
			writeDelegatedBundleResponse(t, w, server.URL, "ws_123", "rf_join", "refresh_join")
		case "/api/v1/workspaces/ws_123/integrations/connect-session":
			seen["connect"] = true
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected connect Authorization: %q", got)
			}
			var body cloudConnectSessionRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode connect body failed: %v", err)
			}
			if len(body.AllowedIntegrations) != 1 || body.AllowedIntegrations[0] != "notion" {
				t.Fatalf("unexpected allowed integrations: %#v", body.AllowedIntegrations)
			}
			if body.RequestedBackend != "composio" {
				t.Fatalf("expected requested backend composio, got %q", body.RequestedBackend)
			}
			_, _ = w.Write([]byte(`{"connectLink":"https://connect.test/notion","connectionId":"conn_789","backend":"composio"}`))
		case "/api/v1/workspaces/ws_123/integrations/notion/status":
			seen["status"] = true
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected status Authorization: %q", got)
			}
			if got := r.URL.Query().Get("connectionId"); got != "conn_789" {
				t.Fatalf("unexpected connectionId: %q", got)
			}
			_, _ = w.Write([]byte(`{"ready":true}`))
		case "/v1/workspaces/ws_123/sync/status":
			seen["sync"] = true
			t.Fatalf("integration connect should not poll sync status unless --wait-sync is set")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	installFakeAgentRelaySession(t, server.URL, "cld_new", "demo", "ws_123", "ws_123")

	var stdout bytes.Buffer
	err := run([]string{
		"integration", "connect", "notion",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--backend", "composio",
		"--no-open",
		"--timeout", "1s",
	}, strings.NewReader(""), &stdout, &stdout)
	if err != nil {
		t.Fatalf("run integration connect failed: %v\noutput:\n%s", err, stdout.String())
	}
	for _, key := range []string{"delegated", "connect", "status"} {
		if !seen[key] {
			t.Fatalf("expected %s request", key)
		}
	}
	if seen["sync"] {
		t.Fatalf("did not expect sync status request without --wait-sync")
	}
	got := stdout.String()
	if !strings.Contains(got, "notion connected") {
		t.Fatalf("unexpected integration connect output: %q", got)
	}
	if strings.Contains(got, "keep this command running while the mount is active") {
		t.Fatalf("did not expect keep-running guidance by default, got %q", got)
	}
	if strings.Contains(got, "Waiting for notion initial sync") {
		t.Fatalf("did not expect initial sync wait by default, got %q", got)
	}
	if !strings.Contains(got, "relayfile mount") {
		t.Fatalf("expected mount guidance, got %q", got)
	}
	state := loadSavedConnection(localDir, "notion")
	if state.ConnectionID != "conn_789" || state.Backend != "composio" {
		t.Fatalf("unexpected saved integration connection: %#v", state)
	}
}

func TestIntegrationConnectWaitSyncPreservesInitialSyncGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_123",
		LocalDir:   localDir,
		AgentName:  "relayfile-cli",
		Scopes:     append([]string(nil), defaultJoinScopes...),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	seen := map[string]bool{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_123/relayfile/delegated-token":
			seen["delegated"] = true
			writeDelegatedBundleResponse(t, w, server.URL, "ws_123", "rf_join", "refresh_join")
		case "/api/v1/workspaces/ws_123/integrations/connect-session":
			seen["connect"] = true
			_, _ = w.Write([]byte(`{"connectLink":"https://connect.test/notion","connectionId":"conn_789","backend":"composio"}`))
		case "/api/v1/workspaces/ws_123/integrations/notion/status":
			seen["status"] = true
			_, _ = w.Write([]byte(`{"ready":true}`))
		case "/v1/workspaces/ws_123/sync/status":
			seen["sync"] = true
			if got := r.Header.Get("Authorization"); got != "Bearer rf_join" {
				t.Fatalf("unexpected sync Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","providers":[{"provider":"notion","status":"ready","lagSeconds":4,"watermarkTs":"2026-05-02T18:00:00Z"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	installFakeAgentRelaySession(t, server.URL, "cld_new", "demo", "ws_123", "ws_123")

	var stdout bytes.Buffer
	err := run([]string{
		"integration", "connect", "notion",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--backend", "composio",
		"--no-open",
		"--timeout", "1s",
		"--wait-sync",
	}, strings.NewReader(""), &stdout, &stdout)
	if err != nil {
		t.Fatalf("run integration connect --wait-sync failed: %v\noutput:\n%s", err, stdout.String())
	}
	for _, key := range []string{"delegated", "connect", "status", "sync"} {
		if !seen[key] {
			t.Fatalf("expected %s request", key)
		}
	}
	if got := stdout.String(); !strings.Contains(got, "Waiting for notion initial sync. Leave this command running") {
		t.Fatalf("expected initial sync wait guidance, got %q", got)
	}
}

func TestConnectCloudIntegrationAcceptsWorkspaceScopedGitHubStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	var seenConnectionScopedStatus atomic.Bool
	var seenWorkspaceStatus atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_123/integrations/connect-session":
			if r.Method != http.MethodPost {
				t.Fatalf("expected connect POST, got %s", r.Method)
			}
			_, _ = w.Write([]byte(`{"connectLink":"https://connect.test/github","connectionId":"conn_session","backend":"nango"}`))
		case "/api/v1/workspaces/ws_123/integrations/github/status":
			switch r.URL.Query().Get("connectionId") {
			case "conn_session":
				seenConnectionScopedStatus.Store(true)
				_, _ = w.Write([]byte(`{"ready":false,"state":"not_connected","connectionId":"conn_session"}`))
			case "":
				seenWorkspaceStatus.Store(true)
				_, _ = w.Write([]byte(`{"ready":false,"state":"oauth_connected","provider":"github","connectionId":"conn_actual"}`))
			default:
				t.Fatalf("unexpected connectionId query: %q", r.URL.Query().Get("connectionId"))
			}
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := connectCloudIntegration(server.URL, "ws_123", "rf_join", "github", "nango", localDir, time.Second, false, &stdout); err != nil {
		t.Fatalf("connectCloudIntegration failed: %v\noutput:\n%s", err, stdout.String())
	}
	if !seenConnectionScopedStatus.Load() {
		t.Fatalf("expected connection-scoped status probe before workspace fallback")
	}
	if !seenWorkspaceStatus.Load() {
		t.Fatalf("expected workspace-scoped status fallback")
	}
	if got := stdout.String(); !strings.Contains(got, "github connected") {
		t.Fatalf("expected connected output, got %q", got)
	}
	state := loadSavedConnection(localDir, "github")
	if state.ConnectionID != "conn_actual" || state.Backend != "nango" {
		t.Fatalf("expected resolved github connection to be saved, got %#v", state)
	}
}

func TestIntegrationListOverlaysRuntimeReadyStatus(t *testing.T) {
	_, _ = setupAdoptWorkspace(t)
	var seenCloudIntegrations bool
	var seenRuntimeStatus bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_123/integrations":
			seenCloudIntegrations = true
			_, _ = w.Write([]byte(`[{"provider":"slack","status":"syncing","lagSeconds":0}]`))
		case "/v1/workspaces/ws_123/sync/status":
			seenRuntimeStatus = true
			if got := r.Header.Get("Authorization"); got != "Bearer rf_join" {
				t.Fatalf("unexpected runtime Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","providers":[{"provider":"slack","status":"ready","lagSeconds":0,"watermarkTs":"2026-05-28T22:30:46Z"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_123",
		AccessToken:          "rf_join",
	})

	var stdout bytes.Buffer
	if err := run([]string{"integration", "list", "--workspace", "demo", "--cloud-api-url", server.URL}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("integration list failed: %v\noutput:\n%s", err, stdout.String())
	}
	if !seenCloudIntegrations || !seenRuntimeStatus {
		t.Fatalf("expected cloud integrations and runtime sync status requests, got cloud=%v runtime=%v", seenCloudIntegrations, seenRuntimeStatus)
	}
	got := stdout.String()
	if !strings.Contains(got, "slack\tready\t0s\t2026-05-28T22:30:46Z") {
		t.Fatalf("expected runtime-ready slack row, got %q", got)
	}
}

func TestIntegrationListUsesSavedWorkspaceScopesForRuntimeStatus(t *testing.T) {
	record, _ := setupAdoptWorkspace(t)
	var seenRuntimeStatus bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_123/integrations":
			_, _ = w.Write([]byte(`[{"provider":"slack","status":"syncing","lagSeconds":0}]`))
		case "/v1/workspaces/ws_123/sync/status":
			seenRuntimeStatus = true
			if got := r.Header.Get("Authorization"); got != "Bearer rf_join" {
				t.Fatalf("unexpected runtime Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","providers":[{"provider":"slack","status":"ready","lagSeconds":0,"watermarkTs":"2026-05-28T22:30:46Z"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)
	if err := delegatedauth.SaveAtomic(delegatedCredentialsPathForRequest(record.ID, record.Scopes), delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_123",
		AccessToken:          "rf_join",
		RefreshToken:         "refresh_join",
		Scopes:               append([]string(nil), record.Scopes...),
	}); err != nil {
		t.Fatalf("save join-scoped delegated credentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"integration", "list", "--workspace", "demo", "--cloud-api-url", server.URL}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("integration list failed: %v\noutput:\n%s", err, stdout.String())
	}
	if !seenRuntimeStatus {
		t.Fatal("expected runtime sync status request using saved workspace scopes")
	}
	if got := stdout.String(); !strings.Contains(got, "slack\tready\t0s\t2026-05-28T22:30:46Z") {
		t.Fatalf("expected join-scoped runtime-ready overlay, got %q", got)
	}
}

func TestIntegrationListDoesNotMaskCloudActionRequiredStatus(t *testing.T) {
	_, _ = setupAdoptWorkspace(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_123/integrations":
			_, _ = w.Write([]byte(`[{"provider":"slack","status":"error","lagSeconds":0}]`))
		case "/v1/workspaces/ws_123/sync/status":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","providers":[{"provider":"slack","status":"ready","lagSeconds":0,"watermarkTs":"2026-05-28T22:30:46Z"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_123",
		AccessToken:          "rf_join",
	})

	var stdout bytes.Buffer
	if err := run([]string{"integration", "list", "--workspace", "demo", "--cloud-api-url", server.URL}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("integration list failed: %v\noutput:\n%s", err, stdout.String())
	}
	if got := stdout.String(); !strings.Contains(got, "slack\terror\t0s\t-") {
		t.Fatalf("expected cloud error status to remain visible, got %q", got)
	}
}

func TestIntegrationListDoesNotUpgradeBareHealthyRuntimeStatus(t *testing.T) {
	_, _ = setupAdoptWorkspace(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_123/integrations":
			_, _ = w.Write([]byte(`[{"provider":"slack","status":"syncing","lagSeconds":0}]`))
		case "/v1/workspaces/ws_123/sync/status":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","providers":[{"provider":"slack","status":"healthy","lagSeconds":0}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_123",
		AccessToken:          "rf_join",
	})

	var stdout bytes.Buffer
	if err := run([]string{"integration", "list", "--workspace", "demo", "--cloud-api-url", server.URL}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("integration list failed: %v\noutput:\n%s", err, stdout.String())
	}
	if got := stdout.String(); !strings.Contains(got, "slack\tsyncing\t0s\t-") {
		t.Fatalf("expected bare healthy runtime status not to upgrade cloud syncing, got %q", got)
	}
}

func TestIntegrationListDoesNotUpgradeUnknownCloudStatus(t *testing.T) {
	_, _ = setupAdoptWorkspace(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_123/integrations":
			_, _ = w.Write([]byte(`[{"provider":"slack","status":"needs_operator","lagSeconds":0}]`))
		case "/v1/workspaces/ws_123/sync/status":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","providers":[{"provider":"slack","status":"ready","lagSeconds":0,"watermarkTs":"2026-05-28T22:30:46Z"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_123",
		AccessToken:          "rf_join",
	})

	var stdout bytes.Buffer
	if err := run([]string{"integration", "list", "--workspace", "demo", "--cloud-api-url", server.URL}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("integration list failed: %v\noutput:\n%s", err, stdout.String())
	}
	if got := stdout.String(); !strings.Contains(got, "slack\tneeds_operator\t0s\t-") {
		t.Fatalf("expected unknown cloud status not to be upgraded, got %q", got)
	}
}

func TestIntegrationListDoesNotUpgradeBlankOrUnknownCloudStatus(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cloudJSON  string
		wantStatus string
	}{
		{
			name:       "blank",
			cloudJSON:  `[{"provider":"slack","lagSeconds":0}]`,
			wantStatus: "unknown",
		},
		{
			name:       "unknown",
			cloudJSON:  `[{"provider":"slack","status":"unknown","lagSeconds":0}]`,
			wantStatus: "unknown",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _ = setupAdoptWorkspace(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/workspaces/ws_123/integrations":
					_, _ = w.Write([]byte(tc.cloudJSON))
				case "/v1/workspaces/ws_123/sync/status":
					_, _ = w.Write([]byte(`{"workspaceId":"ws_123","providers":[{"provider":"slack","status":"ready","lagSeconds":0,"watermarkTs":"2026-05-28T22:30:46Z"}]}`))
				default:
					t.Fatalf("unexpected path: %s", r.URL.Path)
				}
			}))
			defer server.Close()
			pointAdoptCloudCredentials(t, server.URL)
			writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
				RelayfileURL:         server.URL,
				RelayfileWorkspaceID: "ws_123",
				AccessToken:          "rf_join",
			})

			var stdout bytes.Buffer
			if err := run([]string{"integration", "list", "--workspace", "demo", "--cloud-api-url", server.URL}, strings.NewReader(""), &stdout, &stdout); err != nil {
				t.Fatalf("integration list failed: %v\noutput:\n%s", err, stdout.String())
			}
			want := "slack\t" + tc.wantStatus + "\t0s\t-"
			if got := stdout.String(); !strings.Contains(got, want) {
				t.Fatalf("expected %q, got %q", want, got)
			}
		})
	}
}

func TestStatusIncludesLocalMirrorAndDaemonCounts(t *testing.T) {
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
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, ".relayfile-mount-state.json"), []byte(`{"files":{"a":{"dirty":true},"b":{"dirty":false}}}`), 0o644); err != nil {
		t.Fatalf("write mount state failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, ".relay", "permissions-denied.log"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write denied log failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, ".relay", "conflicts", "note.local"), []byte("conflict"), 0o644); err != nil {
		t.Fatalf("write conflict file failed: %v", err)
	}
	if err := writeDaemonPIDState(mountPIDFile(localDir), daemonPIDState{PID: 4242, WorkspaceID: "ws_demo", LocalDir: localDir}); err != nil {
		t.Fatalf("writeDaemonPIDState failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/workspaces/ws_demo/sync/status" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer delegated_token" {
			t.Fatalf("expected delegated Authorization, got %q", got)
		}
		_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[{"provider":"notion","status":"ready","lagSeconds":4,"watermarkTs":"2026-05-02T18:00:00Z"}]}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "delegated_token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
	}
	if _, err := os.Stat(credentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected stale relayfile credentials removed, got err=%v", err)
	}
	got := stdout.String()
	for _, fragment := range []string{
		"workspace ws_demo (demo)",
		"daemon: running (pid 4242)",
		"pending writebacks: 1",
		"conflicts: 1",
		"denied: 2",
	} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("expected %q in status output, got %q", fragment, got)
		}
	}
}

func TestStatusRendersUnavailableAgentRelaySessionAuthLine(t *testing.T) {
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[]}`))
	}))
	defer server.Close()
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "token",
	})
	installFakeAgentRelay(t, `
echo "session expired" >&2
exit 1
`)

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "auth: agent-relay session unavailable - run 'agent-relay cloud login'") {
		t.Fatalf("expected agent-relay unavailable auth line, got %q", got)
	}
}

func TestStatusWarnsWhenDaemonPredatesLastLogin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	startedAt := time.Now().UTC().Add(-time.Hour)
	if err := writeDaemonPIDState(mountPIDFile(localDir), daemonPIDState{
		PID:         4242,
		WorkspaceID: "ws_demo",
		LocalDir:    localDir,
		StartedAt:   startedAt.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("writeDaemonPIDState failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  startedAt.Format(time.RFC3339),
		LastUsedAt: startedAt.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[]}`))
	}))
	defer server.Close()
	if err := saveCredentials(credentials{Server: "https://legacy.relayfile.invalid", Token: "legacy_token"}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}
	oldLoginTime := startedAt.Add(-30 * time.Minute)
	if err := os.Chtimes(credentialsPath(), oldLoginTime, oldLoginTime); err != nil {
		t.Fatalf("chtimes credentials failed: %v", err)
	}
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "delegated_token",
	})
	agentRelayAuthPath := agentRelayCloudAuthPath()
	if err := os.MkdirAll(filepath.Dir(agentRelayAuthPath), 0o700); err != nil {
		t.Fatalf("mkdir agent-relay auth dir failed: %v", err)
	}
	if err := os.WriteFile(agentRelayAuthPath, []byte(`{"accessToken":"new"}`), 0o600); err != nil {
		t.Fatalf("write agent-relay auth failed: %v", err)
	}
	loginTime := startedAt.Add(30 * time.Minute)
	if err := os.Chtimes(agentRelayAuthPath, loginTime, loginTime); err != nil {
		t.Fatalf("chtimes agent-relay auth failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "auth: daemon predates last login - restart the daemon") {
		t.Fatalf("expected daemon stale auth line, got %q", got)
	}
}

func TestStatusUnauthorizedReturnsLoginHint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:      "demo",
		ID:        "ws_demo",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Token has expired"}`))
	}))
	defer server.Close()
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "expired",
	})

	var stdout bytes.Buffer
	err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout)
	if !errors.Is(err, ErrCloudRefreshExpired) {
		t.Fatalf("expected ErrCloudRefreshExpired, got %v", err)
	}
}

func TestStatusSurfacesOrphanDaemonFromProcessScan(t *testing.T) {
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/workspaces/ws_demo/sync/status" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[]}`))
	}))
	defer server.Close()
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "delegated_token",
	})

	oldList := listProcessCommands
	listProcessCommands = func() ([]processCommandSnapshot, error) {
		return []processCommandSnapshot{{
			PID:     7777,
			Command: "relayfile mount ws_demo " + localDir + " --daemonized",
		}}, nil
	}
	t.Cleanup(func() { listProcessCommands = oldList })

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "daemon: orphan (pid 7777)") {
		t.Fatalf("expected orphan daemon status, got %q", got)
	}
}

func TestMountDaemonCommandMatchesAliasesAndLocalDirBoundaries(t *testing.T) {
	localDir := filepath.Join(t.TempDir(), "ws")
	if !mountDaemonCommandMatches("relayfile start demo "+localDir+" --daemonized", localDir, "ws_demo", "demo") {
		t.Fatalf("expected relayfile start alias to match daemon command")
	}
	if !mountDaemonCommandMatches("relayfile on demo "+localDir+" --daemonized", localDir, "ws_demo", "demo") {
		t.Fatalf("expected relayfile on alias to match daemon command")
	}
	if mountDaemonCommandMatches("relayfile mount other "+localDir+"-old --daemonized", localDir, "ws_demo", "demo") {
		t.Fatalf("expected sibling path with shared prefix not to match")
	}
	if !mountDaemonCommandMatches("relayfile mount other "+filepath.Join(localDir, "nested")+" --daemonized", localDir, "ws_demo", "demo") {
		t.Fatalf("expected localDir subpath token to match")
	}
}

func TestRestartRequiresRecordedLocalMirror(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:       "demo",
			ID:         "ws_demo",
			CreatedAt:  now,
			LastUsedAt: now,
		}},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}

	var stdout bytes.Buffer
	err := runRestart([]string{"demo"}, &stdout)
	if err == nil {
		t.Fatalf("expected restart to fail without a recorded local mirror")
	}
	if !strings.Contains(err.Error(), "no recorded local mirror directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMountRequiresLocalDirWhenWorkspaceHasNoRecordedMirror(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:       "demo",
			ID:         "ws_demo",
			CreatedAt:  now,
			LastUsedAt: now,
		}},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}
	if err := saveCredentials(credentials{
		Server: defaultServerURL,
		Token:  testJWTWithWorkspace("ws_demo"),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	err := run([]string{"mount", "demo", "--token", testJWTWithWorkspace("ws_demo"), "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("expected mount without LOCAL_DIR to fail when no localDir is recorded")
	}
	if !strings.Contains(err.Error(), "no recorded local mirror directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMountUsesRecordedLocalDirWhenOmitted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:       "demo",
			ID:         "ws_demo",
			LocalDir:   localDir,
			CreatedAt:  now,
			LastUsedAt: now,
		}},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/workspaces/ws_demo/fs/export":
			_, _ = w.Write([]byte(`[{"path":"/notion/Docs/A.md","revision":"rev_1","contentType":"text/markdown","content":"# A"}]`))
		case "/v1/workspaces/ws_demo/fs/events":
			_, _ = w.Write([]byte(`{"events":[{"eventId":"evt_1","type":"file.created","path":"/notion/Docs/A.md","revision":"rev_1"}]}`))
		case "/v1/workspaces/ws_demo/sync/status":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  testJWTWithWorkspace("ws_demo"),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	if err := run([]string{"mount", "demo", "--server", server.URL, "--token", testJWTWithWorkspace("ws_demo"), "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run mount failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(localDir, "notion", "Docs", "A.md"))
	if err != nil {
		t.Fatalf("expected file under recorded localDir: %v", err)
	}
	if string(data) != "# A" {
		t.Fatalf("unexpected mirrored content: %q", data)
	}
}

func TestMountMirrorsRepeatedRemotePathsUnderScopedLayout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localRoot := t.TempDir()
	token := testJWTWithWorkspace("ws_demo")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/workspaces/ws_demo/fs/export":
			switch got := r.URL.Query().Get("path"); got {
			case "/github/repos/acme/cloud":
				_, _ = w.Write([]byte(`[{"path":"/github/repos/acme/cloud/README.md","revision":"rev_github","contentType":"text/markdown","content":"# Cloud"}]`))
			case "/slack/channels/proj-cloud":
				_, _ = w.Write([]byte(`[{"path":"/slack/channels/proj-cloud/topic.md","revision":"rev_slack","contentType":"text/markdown","content":"Scoped channel"}]`))
			default:
				t.Fatalf("unexpected export path: %q", got)
			}
		case "/v1/workspaces/ws_demo/fs/events":
			_, _ = w.Write([]byte(`{"events":[]}`))
		case "/v1/workspaces/ws_demo/sync/status":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	err := run([]string{
		"mount", "ws_demo", localRoot,
		"--server", server.URL,
		"--token", token,
		"--remote-path", "/github/repos/acme/cloud",
		"--remote-path", "/slack/channels/proj-cloud",
		"--local-layout", "scoped",
		"--once",
		"--websocket=false",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("run scoped multi-path mount failed: %v", err)
	}

	assertFileContent := func(relativePath, want string) {
		t.Helper()
		payload, readErr := os.ReadFile(filepath.Join(localRoot, filepath.FromSlash(relativePath)))
		if readErr != nil {
			t.Fatalf("read scoped file %s: %v", relativePath, readErr)
		}
		if got := string(payload); got != want {
			t.Fatalf("scoped file %s = %q, want %q", relativePath, got, want)
		}
	}
	assertFileContent("github/repos/acme/cloud/README.md", "# Cloud")
	assertFileContent("slack/channels/proj-cloud/topic.md", "Scoped channel")

	githubChild := filepath.Join(localRoot, "github", "repos", "acme", "cloud")
	for _, relativePath := range []string{
		filepath.Join(".relay", "integrations"),
		filepath.Join(".relay", "disconnected"),
		filepath.Join(".relay", "conflicts"),
	} {
		info, statErr := os.Stat(filepath.Join(githubChild, relativePath))
		if statErr != nil {
			t.Fatalf("expected scoped child runtime directory %s: %v", relativePath, statErr)
		}
		if !info.IsDir() {
			t.Fatalf("scoped child runtime path %s is not a directory", relativePath)
		}
	}
	for _, rootOnlyPath := range []string{"digests", ".skills"} {
		if _, statErr := os.Stat(filepath.Join(githubChild, rootOnlyPath)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("scoped child root-only path %s exists or could not be inspected: %v", rootOnlyPath, statErr)
		}
		if _, statErr := os.Stat(filepath.Join(localRoot, rootOnlyPath)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("scoped catalog root falsely exposes unsynchronized %s: %v", rootOnlyPath, statErr)
		}
	}

	record, ok := workspaceRecordByID("ws_demo")
	if !ok {
		t.Fatal("expected mount to persist workspace record")
	}
	if got, want := strings.Join(record.RemotePaths, ","), "/github/repos/acme/cloud,/slack/channels/proj-cloud"; got != want {
		t.Fatalf("persisted remote paths = %q, want %q", got, want)
	}
	if record.LocalLayout != "scoped" {
		t.Fatalf("persisted local layout = %q, want scoped", record.LocalLayout)
	}

	// A later start/restart supplies no path flags, so the catalog must carry
	// the allowlist instead of silently widening the mount back to "/".
	err = run([]string{
		"mount", "ws_demo", localRoot,
		"--server", server.URL,
		"--token", token,
		"--once",
		"--websocket=false",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("rerun with persisted scoped paths failed: %v", err)
	}
}

func TestMountRejectsRepeatedRemotePathsWithoutScopedLayout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	err := run([]string{
		"mount", "ws_demo", t.TempDir(),
		"--token", testJWTWithWorkspace("ws_demo"),
		"--remote-path", "/github",
		"--remote-path", "/slack",
		"--once",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--local-layout=scoped") {
		t.Fatalf("expected scoped-layout guidance, got %v", err)
	}
}

func TestMountRejectsProviderFilterAcrossHeterogeneousScopesBeforeInitializingMirror(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	localRoot := filepath.Join(t.TempDir(), "mirror")

	err := run([]string{
		"mount", "ws_demo", localRoot,
		"--token", testJWTWithWorkspace("ws_demo"),
		"--remote-path", "/github",
		"--remote-path", "/slack",
		"--local-layout", "scoped",
		"--provider", "github",
		"--once",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "/slack") || !strings.Contains(err.Error(), "omit --provider") {
		t.Fatalf("expected heterogeneous provider-filter refusal, got %v", err)
	}
	if _, statErr := os.Stat(localRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("provider-filter refusal initialized mirror: %v", statErr)
	}
}

func TestMountRejectsExplicitlyEmptyPathsFileWithoutWidening(t *testing.T) {
	for name, contents := range map[string]string{
		"empty":      "",
		"comments":   "\n# intentionally no roots\n",
		"empty-json": "[]",
		"blank-json": `[null, "", "  "]`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			clearRelayfileEnv(t)

			pathsFile := filepath.Join(t.TempDir(), "paths")
			if err := os.WriteFile(pathsFile, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			localRoot := filepath.Join(t.TempDir(), "mirror")
			err := run([]string{
				"mount", "ws_demo", localRoot,
				"--token", testJWTWithWorkspace("ws_demo"),
				"--paths-file", pathsFile,
				"--local-layout", "scoped",
				"--once",
			}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "contains no usable remote roots") ||
				!strings.Contains(err.Error(), "refusing to widen") {
				t.Fatalf("expected explicit empty allowlist refusal, got %v", err)
			}
			if _, statErr := os.Stat(localRoot); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("empty allowlist initialized mirror before refusal: %v", statErr)
			}
		})
	}
}

func TestValidateExplicitPathsFileAllowlistDistinguishesUnsetFromEmpty(t *testing.T) {
	if err := validateExplicitPathsFileAllowlist("", nil, nil); err != nil {
		t.Fatalf("unset paths-file must retain the default mount fallback: %v", err)
	}
	if err := validateExplicitPathsFileAllowlist("/tmp/paths", []string{"", "  "}, nil); err == nil {
		t.Fatal("configured paths-file with no usable roots must refuse")
	}
	if err := validateExplicitPathsFileAllowlist("/tmp/paths", nil, []string{"/github"}); err != nil {
		t.Fatalf("direct remote path should make an empty companion file harmless: %v", err)
	}
}

func TestMountRefusesScopedResetBeforeInitializingMirror(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	parent := t.TempDir()
	localRoot := filepath.Join(parent, "mirror")
	err := run([]string{
		"mount", "ws_demo", localRoot,
		"--token", testJWTWithWorkspace("ws_demo"),
		"--remote-path", "/github",
		"--remote-path", "/slack",
		"--local-layout", "scoped",
		"--reset-after-clobber",
		"--once",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "transactionally") || !strings.Contains(err.Error(), "--rehome") {
		t.Fatalf("expected scoped reset refusal with remedy, got %v", err)
	}
	if _, statErr := os.Stat(localRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("scoped reset initialized mirror before refusing: %v", statErr)
	}
}

func TestMountPersistsScopedTopologyBeforeBackgroundSpawnFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	oldSpawn := spawnBackgroundMountProcessFn
	spawnBackgroundMountProcessFn = func([]string, []string, string, string, string, string) error {
		return errors.New("synthetic spawn failure")
	}
	t.Cleanup(func() { spawnBackgroundMountProcessFn = oldSpawn })

	localRoot := filepath.Join(t.TempDir(), "mirror")
	err := run([]string{
		"mount", "ws_demo", localRoot,
		"--token", testJWTWithWorkspace("ws_demo"),
		"--remote-path", "/github",
		"--remote-path", "/slack",
		"--local-layout", "scoped",
		"--background",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "synthetic spawn failure") {
		t.Fatalf("expected synthetic background failure, got %v", err)
	}
	record, ok := workspaceRecordByID("ws_demo")
	if !ok {
		t.Fatal("expected failed current writer to leave a catalog record")
	}
	if record.LocalLayout != mountscope.LayoutScoped {
		t.Fatalf("failed current writer left blank/incorrect layout %q", record.LocalLayout)
	}
	if got := strings.Join(record.RemotePaths, ","); got != "/github,/slack" {
		t.Fatalf("failed current writer persisted paths %q, want /github,/slack", got)
	}
}

func TestPrepareBackgroundMountLayoutPreservesScopedArtifactAbsence(t *testing.T) {
	localRoot := filepath.Join(t.TempDir(), "mirror")
	logFile := filepath.Join(localRoot, ".relay", "mount.log")

	if err := prepareBackgroundMountLayout(localRoot, logFile, mountscope.LayoutScoped); err != nil {
		t.Fatalf("prepare scoped background layout: %v", err)
	}
	for _, artifact := range []string{
		filepath.Join(localRoot, mountscope.DigestsTopLevel),
		filepath.Join(localRoot, mountscope.SkillsTopLevel),
	} {
		if _, err := os.Stat(artifact); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("scoped background preparation created unsynchronized root artifact %s: %v", artifact, err)
		}
	}
	if info, err := os.Stat(filepath.Join(localRoot, mountscope.RuntimeTopLevel)); err != nil || !info.IsDir() {
		t.Fatalf("scoped background preparation did not create runtime root: info=%v err=%v", info, err)
	}
}

func TestPrepareScopedCatalogRootRemovesOnlyGeneratedArtifacts(t *testing.T) {
	localRoot := filepath.Join(t.TempDir(), "mirror")
	if err := ensureMirrorLayout(localRoot); err != nil {
		t.Fatal(err)
	}
	if err := prepareScopedCatalogRoot(localRoot); err != nil {
		t.Fatalf("prepare generated setup root for scoped mount: %v", err)
	}
	for _, artifact := range []string{
		filepath.Join(localRoot, mountscope.DigestsTopLevel),
		filepath.Join(localRoot, mountscope.SkillsTopLevel),
	} {
		if _, err := os.Stat(artifact); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("generated artifact remains after scoped preparation %s: %v", artifact, err)
		}
	}
}

func TestPrepareScopedCatalogRootRefusesUnknownContentWithoutMutation(t *testing.T) {
	localRoot := filepath.Join(t.TempDir(), "mirror")
	if err := ensureMirrorLayout(localRoot); err != nil {
		t.Fatal(err)
	}
	unknownPath := filepath.Join(localRoot, mountscope.DigestsTopLevel, "today.md")
	if err := os.WriteFile(unknownPath, []byte("existing digest"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := prepareScopedCatalogRoot(localRoot)
	if err == nil || !strings.Contains(err.Error(), unknownPath) || !strings.Contains(err.Error(), "--rehome") {
		t.Fatalf("expected actionable unknown-artifact refusal, got %v", err)
	}
	for _, path := range []string{
		unknownPath,
		filepath.Join(localRoot, mountscope.SkillsTopLevel, "activity-summary.md"),
	} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("refusal mutated existing artifact %s: %v", path, statErr)
		}
	}
}

func TestSetupMirrorLayoutPreservesPersistedScopedTopology(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:        "demo",
		ID:          "ws_demo",
		LocalLayout: mountscope.LayoutScoped,
	}); err != nil {
		t.Fatal(err)
	}
	if got := setupMirrorLayout("demo"); got != mountscope.LayoutScoped {
		t.Fatalf("setup layout = %q, want scoped", got)
	}
	if got := setupMirrorLayout("new-workspace"); got != mountscope.LayoutExact {
		t.Fatalf("new workspace setup layout = %q, want exact", got)
	}
}

func TestValidateMountLayoutTransitionRejectsInPlaceChange(t *testing.T) {
	localDir := t.TempDir()
	record := workspaceRecord{LocalDir: localDir, LocalLayout: mountscope.LayoutScoped}
	err := validateMountLayoutTransition(record, localDir, mountscope.LayoutExact)
	if err == nil || !strings.Contains(err.Error(), "--rehome") {
		t.Fatalf("expected rehome guidance, got %v", err)
	}
	if err := validateMountLayoutTransition(record, filepath.Join(t.TempDir(), "new-root"), mountscope.LayoutExact); err != nil {
		t.Fatalf("layout change at a new root should be allowed, got %v", err)
	}
}

func TestValidateMountLayoutTransitionTreatsLegacyBlankAsUnknown(t *testing.T) {
	localDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localDir, ".relay"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, ".relay", "state.json"), []byte(`{"remoteRoot":"/"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	record := workspaceRecord{LocalDir: localDir}
	err := validateMountLayoutTransition(record, localDir, mountscope.LayoutScoped)
	if err == nil ||
		!strings.Contains(err.Error(), "unknown") ||
		!strings.Contains(err.Error(), "--rehome") ||
		!strings.Contains(err.Error(), localDir) {
		t.Fatalf("expected legacy-topology refusal with path and remedy, got %v", err)
	}
	if err := validateMountLayoutTransition(record, filepath.Join(t.TempDir(), "new-root"), mountscope.LayoutScoped); err != nil {
		t.Fatalf("legacy record should allow explicit re-home to a new root, got %v", err)
	}
	if err := validateMountLayoutTransition(record, localDir, mountscope.LayoutExact); err != nil {
		t.Fatalf("legacy exact mount should remain restartable, got %v", err)
	}
}

func TestValidateMountLayoutTransitionAllowsUnstartedBlankRecordToStartScoped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	localDir := t.TempDir()
	record := workspaceRecord{LocalDir: localDir}
	if err := validateMountLayoutTransition(record, localDir, mountscope.LayoutScoped); err != nil {
		t.Fatalf("pre-mount record has no state to orphan and should start scoped, got %v", err)
	}
}

func TestValidateMountLayoutTransitionFindsPrivateLegacyState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name      string
		configure func(*workspaceRecord)
		statePath func(workspaceRecord) (string, error)
	}{
		{
			name:      "default-dir-unknown-legacy-root",
			configure: func(*workspaceRecord) {},
			statePath: func(record workspaceRecord) (string, error) {
				resolved, err := mountsync.ResolveMountStatePath(mountsync.MountStatePathOptions{
					WorkspaceID: record.ID,
					RemoteRoot:  "/notion",
					LocalRoot:   record.LocalDir,
				})
				return resolved.StateFile, err
			},
		},
		{
			name: "state-dir",
			configure: func(record *workspaceRecord) {
				record.MountStateDir = t.TempDir()
			},
			statePath: func(record workspaceRecord) (string, error) {
				resolved, err := mountsync.ResolveMountStatePath(mountsync.MountStatePathOptions{
					WorkspaceID: record.ID,
					RemoteRoot:  "/",
					LocalRoot:   record.LocalDir,
					StateDir:    record.MountStateDir,
				})
				return resolved.StateFile, err
			},
		},
		{
			name: "state-file",
			configure: func(record *workspaceRecord) {
				record.MountStateFile = filepath.Join(t.TempDir(), "explicit-state.json")
			},
			statePath: func(record workspaceRecord) (string, error) {
				return record.MountStateFile, nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			localDir := t.TempDir()
			record := workspaceRecord{ID: "ws_demo", LocalDir: localDir}
			tc.configure(&record)
			statePath, err := tc.statePath(record)
			if err != nil {
				t.Fatalf("resolve private state: %v", err)
			}
			if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
				t.Fatalf("mkdir private state: %v", err)
			}
			if err := os.WriteFile(statePath, []byte(`{"files":{}}`), 0o600); err != nil {
				t.Fatalf("write private state: %v", err)
			}
			err = validateMountLayoutTransition(record, localDir, mountscope.LayoutScoped)
			if err == nil || !strings.Contains(err.Error(), "unknown") || !strings.Contains(err.Error(), "--rehome") {
				t.Fatalf("expected private-state migration refusal, got %v", err)
			}
		})
	}
}

func TestValidateMountLayoutTransitionIgnoresAttributedUnrelatedPrivateState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	targetRoot := t.TempDir()
	otherRoot := t.TempDir()
	resolvedState, err := mountsync.ResolveMountStatePath(mountsync.MountStatePathOptions{
		WorkspaceID: "ws_other",
		RemoteRoot:  "/notion",
		LocalRoot:   otherRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	statePath := resolvedState.StateFile
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(
		`{"workspaceId":"ws_other","remoteRoot":"/notion","localRoot":%q,"files":{}}`,
		otherRoot,
	)
	if err := os.WriteFile(statePath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	record := workspaceRecord{ID: "ws_target", LocalDir: targetRoot}
	if err := validateMountLayoutTransition(record, targetRoot, mountscope.LayoutScoped); err != nil {
		t.Fatalf("unrelated attributed private state blocked an unstarted record: %v", err)
	}
}

func TestPrivateMountStateIdentityMatchesWorkspaceAndFilesystemRoot(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	localRoot := t.TempDir()
	alias := filepath.Join(t.TempDir(), "mirror-alias")
	if err := os.Symlink(localRoot, alias); err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(`{"workspaceId":"ws_demo","localRoot":%q,"files":{}}`, alias)
	if err := os.WriteFile(statePath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	belongs, attributable, err := privateMountStateMatchesRecord(
		statePath,
		workspaceRecord{ID: "ws_demo"},
		localRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !belongs || !attributable {
		t.Fatalf("identity match = belongs %v, attributable %v", belongs, attributable)
	}
}

func TestMountTransitionGuardsUseFilesystemIdentity(t *testing.T) {
	localRoot := t.TempDir()
	alias := filepath.Join(t.TempDir(), "mirror-alias")
	if err := os.Symlink(localRoot, alias); err != nil {
		t.Fatalf("create mount-root alias: %v", err)
	}
	record := workspaceRecord{
		LocalDir:    localRoot,
		LocalLayout: mountscope.LayoutScoped,
		RemotePaths: []string{"/github", "/slack"},
	}
	if err := validateMountScopeTransition(record, alias, mountscope.LayoutScoped, []string{"/slack"}); err == nil || !strings.Contains(err.Error(), "--rehome") {
		t.Fatalf("expected aliased root to retain in-place scope refusal, got %v", err)
	}
	if err := validateMountLayoutTransition(record, alias, mountscope.LayoutExact); err == nil || !strings.Contains(err.Error(), "--rehome") {
		t.Fatalf("expected aliased root to retain in-place layout refusal, got %v", err)
	}
}

func TestValidateMountScopeTransitionRejectsInPlaceRemoval(t *testing.T) {
	localRoot := t.TempDir()
	previous := workspaceRecord{
		LocalDir:    localRoot,
		LocalLayout: mountscope.LayoutScoped,
		RemotePaths: []string{"/github", "/slack"},
	}
	err := validateMountScopeTransition(previous, localRoot, mountscope.LayoutScoped, []string{"/slack"})
	if err == nil || !strings.Contains(err.Error(), "/github") || !strings.Contains(err.Error(), "--rehome") {
		t.Fatalf("expected removed-scope rehome guidance, got %v", err)
	}
	if err := validateMountScopeTransition(previous, localRoot, mountscope.LayoutScoped, []string{"/github", "/slack", "/email"}); err != nil {
		t.Fatalf("adding a scope should not require lifecycle migration, got %v", err)
	}
	if err := validateMountScopeTransition(previous, filepath.Join(t.TempDir(), "new-root"), mountscope.LayoutScoped, []string{"/slack"}); err != nil {
		t.Fatalf("scope change at a new root should be allowed, got %v", err)
	}
}

func TestValidateMountScopeTransitionRejectsPersistedNestedTopology(t *testing.T) {
	localRoot := t.TempDir()
	previous := workspaceRecord{
		LocalDir:    localRoot,
		LocalLayout: mountscope.LayoutScoped,
		RemotePaths: []string{"/github", "/github/repos/acme"},
	}
	err := validateMountScopeTransition(
		previous,
		localRoot,
		mountscope.LayoutScoped,
		[]string{"/github"},
	)
	if err == nil ||
		!strings.Contains(err.Error(), "cannot be represented safely") ||
		!strings.Contains(err.Error(), "--rehome") {
		t.Fatalf("expected persisted nested-scope refusal, got %v", err)
	}
}

func TestValidateMountScopeTransitionAllowsMalformedLegacyTopologyToRehome(t *testing.T) {
	previousRoot := t.TempDir()
	previous := workspaceRecord{
		LocalDir:    previousRoot,
		LocalLayout: mountscope.LayoutScoped,
		RemotePaths: []string{"/github", "/github/repos/acme"},
	}
	if err := validateMountScopeTransition(
		previous,
		filepath.Join(t.TempDir(), "new-root"),
		mountscope.LayoutScoped,
		[]string{"/github", "/slack"},
	); err != nil {
		t.Fatalf("safe rehome was blocked by malformed persisted topology: %v", err)
	}
}

func TestResolveCLIMountRemotePathsAppliesEnvironmentBeforeTransitionValidation(t *testing.T) {
	got := resolveCLIMountRemotePaths(nil, []string{"/github"}, "/github")
	if len(got) != 1 || got[0] != "/github" {
		t.Fatalf("environment path resolved to %v, want [/github]", got)
	}
	previous := workspaceRecord{
		LocalDir:    t.TempDir(),
		LocalLayout: mountscope.LayoutScoped,
		RemotePaths: []string{"/github"},
	}
	if err := validateMountScopeTransition(previous, previous.LocalDir, mountscope.LayoutScoped, got); err != nil {
		t.Fatalf("matching environment path should not look like removal: %v", err)
	}

	got = resolveCLIMountRemotePaths(nil, []string{"/github", "/slack"}, "")
	if want := "/github,/slack"; strings.Join(got, ",") != want {
		t.Fatalf("recorded restart paths = %q, want %q", strings.Join(got, ","), want)
	}
}

func TestValidateMountResetModeRefusesScopedAcknowledgedReset(t *testing.T) {
	localRoot := t.TempDir()
	err := validateMountResetMode(localRoot, mountscope.LayoutScoped, true)
	if err == nil ||
		!strings.Contains(err.Error(), "transactionally") ||
		!strings.Contains(err.Error(), "--rehome") ||
		!strings.Contains(err.Error(), localRoot) {
		t.Fatalf("expected scoped reset refusal with reason and remedy, got %v", err)
	}
	if err := validateMountResetMode(localRoot, mountscope.LayoutExact, true); err != nil {
		t.Fatalf("exact acknowledged reset should remain supported, got %v", err)
	}
}

func TestPreflightScopedMountRootInvariantRefusesWithoutMutating(t *testing.T) {
	localRoot := t.TempDir()
	clobbered := filepath.Join(localRoot, "github")
	if err := os.WriteFile(clobbered, []byte("preserve me"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(clobbered, "repos", "acme")
	err := preflightScopedMountRootInvariant(localRoot, child)
	if err == nil || !strings.Contains(err.Error(), clobbered) || !strings.Contains(err.Error(), "--rehome") {
		t.Fatalf("expected scoped clobber refusal, got %v", err)
	}
	content, readErr := os.ReadFile(clobbered)
	if readErr != nil || string(content) != "preserve me" {
		t.Fatalf("preflight mutated clobbered path: content=%q err=%v", content, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(localRoot, "github", "repos")); statErr == nil {
		t.Fatal("preflight initialized child path")
	}
}

func TestPreflightScopedMountRootInvariantAllowsMissingChildTree(t *testing.T) {
	localRoot := t.TempDir()
	child := filepath.Join(localRoot, "github", "repos", "acme")
	if err := preflightScopedMountRootInvariant(localRoot, child); err != nil {
		t.Fatalf("fresh scoped child should be allowed, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(localRoot, "github")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight initialized missing child: %v", err)
	}
}

func TestAcquireMountStartLockSerializesSamePortableRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	localRoot := filepath.Join(t.TempDir(), "Mirror")
	release, err := acquireMountStartLock(localRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireMountStartLock(strings.ToUpper(localRoot)); err == nil ||
		!strings.Contains(err.Error(), "already in progress") {
		release()
		t.Fatalf("portable alias acquired a second mount-start lock: %v", err)
	}
	release()
	releaseAgain, err := acquireMountStartLock(localRoot)
	if err != nil {
		t.Fatalf("mount-start lock remained held after release: %v", err)
	}
	releaseAgain()
}

func TestAcquireMountStartLockDoesNotDependOnDefaultStateDirectory(t *testing.T) {
	homeFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(homeFile, []byte("read-only home sentinel"), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeFile)

	release, err := acquireMountStartLock(filepath.Join(t.TempDir(), "mirror"))
	if err != nil {
		t.Fatalf("mount-start lock depended on default state directory: %v", err)
	}
	release()
}

func TestTrustedMountStartLockBaseRejectsAttackerWritableCandidate(t *testing.T) {
	parent := t.TempDir()
	attackerWritable := filepath.Join(parent, "shared")
	trusted := filepath.Join(parent, "owned")
	if err := os.Mkdir(attackerWritable, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(attackerWritable, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(trusted, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := trustedMountStartLockBaseFrom([]string{attackerWritable, trusted})
	if err != nil {
		t.Fatal(err)
	}
	if got != trusted {
		t.Fatalf("selected lock base = %q, want trusted fallback %q", got, trusted)
	}
}

func TestTrustedMountStartLockBaseRejectsSymlinkCandidate(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("filesystem does not support symlinks: %v", err)
	}
	trusted := filepath.Join(parent, "owned")
	if err := os.Mkdir(trusted, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := trustedMountStartLockBaseFrom([]string{alias, trusted})
	if err != nil {
		t.Fatal(err)
	}
	if got != trusted {
		t.Fatalf("selected lock base = %q, want non-symlink fallback %q", got, trusted)
	}
}

func TestAcquireMountStartLockSerializesSymlinkAliasesAndMissingDescendants(t *testing.T) {
	parent := t.TempDir()
	realParent := filepath.Join(parent, "real")
	if err := os.MkdirAll(realParent, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasParent := filepath.Join(parent, "alias")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Skipf("filesystem does not support symlinks: %v", err)
	}
	realRoot := filepath.Join(realParent, "not-created-yet")
	aliasRoot := filepath.Join(aliasParent, "not-created-yet")

	release, err := acquireMountStartLock(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := acquireMountStartLock(aliasRoot); err == nil ||
		!strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("symlink alias acquired a second mount-start lock: %v", err)
	}
}

func TestReportMountContentPolicyEnumeratesExclusionsAndSensitiveContent(t *testing.T) {
	localRoot := t.TempDir()
	scopeDir := filepath.Join(localRoot, "github")
	if err := os.MkdirAll(filepath.Join(scopeDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scopeDir, ".env"), []byte("SECRET=value"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := reportMountContentPolicy(&output, localRoot, []mountscope.Scope{{
		RemotePath: "/github",
		LocalDir:   scopeDir,
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if !strings.Contains(got, "github/.git") || !strings.Contains(got, "not synced") {
		t.Fatalf("infrastructure exclusion was not visible: %q", got)
	}
	if !strings.Contains(got, "github/.env") || !strings.Contains(got, "will sync") {
		t.Fatalf("sensitive user content was not surfaced: %q", got)
	}
}

func TestValidateMountStatePathsRefusesScopedPrivateStateBeforeMutation(t *testing.T) {
	localRoot := t.TempDir()
	scopes, err := mountscope.Plan(
		localRoot,
		mountscope.LayoutScoped,
		[]string{"/github", "/slack"},
		"/",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(scopes[0].LocalDir, ".relay", "private-state.json")
	err = validateMountStatePaths(
		"ws_demo",
		scopes,
		stateFile,
		"",
		mountsync.MountKindDaemon,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "/github") ||
		!strings.Contains(err.Error(), "before updating workspace topology") {
		t.Fatalf("expected pre-persistence private-state refusal, got %v", err)
	}
	entries, readErr := os.ReadDir(localRoot)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("private-state preflight mutated mount root: %v", entries)
	}
}

func TestMergeWorkspaceRecordsUpdatesMountStateIdentityOnlyWhenExplicit(t *testing.T) {
	current := workspaceRecord{
		ID:             "ws_demo",
		MountStateFile: "/old/state.json",
		MountStateDir:  "/old",
		MountKind:      mountsync.MountKindDaemon,
	}
	ordinary := mergeWorkspaceRecords(current, workspaceRecord{ID: "ws_demo", Name: "demo"})
	if ordinary.MountStateFile != current.MountStateFile || ordinary.MountStateDir != current.MountStateDir || ordinary.MountKind != current.MountKind {
		t.Fatalf("ordinary catalog update changed mount state identity: %#v", ordinary)
	}
	explicit := mergeWorkspaceRecords(current, workspaceRecord{
		ID:            "ws_demo",
		MountStateDir: "/new",
		MountKind:     mountsync.MountKindFlush,
		mountStateSet: true,
	})
	if explicit.MountStateFile != "" || explicit.MountStateDir != "/new" || explicit.MountKind != mountsync.MountKindFlush {
		t.Fatalf("explicit catalog update did not replace mount state identity: %#v", explicit)
	}
}

func TestMergeWorkspaceRecordsReplacesCurrentRemotePaths(t *testing.T) {
	current := workspaceRecord{
		ID:          "ws_demo",
		RemotePaths: []string{"/github", "/retired"},
	}
	merged := mergeWorkspaceRecords(current, workspaceRecord{
		ID:          "ws_demo",
		RemotePaths: []string{"/github", "/slack"},
	})
	if got, want := strings.Join(merged.RemotePaths, ","), "/github,/slack"; got != want {
		t.Fatalf("merged remote paths = %q, want %q", got, want)
	}
}

func TestMountWithoutTokenRejectsWorkspaceOutsideAgentRelayActive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "stale",
		Workspaces: []workspaceRecord{
			{
				Name:       "active",
				ID:         "rw_active",
				LocalDir:   localDir,
				CreatedAt:  now,
				LastUsedAt: now,
			},
			{
				Name:       "stale",
				ID:         "rw_stale",
				LocalDir:   localDir,
				CreatedAt:  now,
				LastUsedAt: now,
			},
		},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}

	joinCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		joinCalls++
		t.Fatalf("unexpected path: %s", r.URL.Path)
	}))
	defer server.Close()
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:          server.URL,
		RelayfileWorkspaceID:  "rw_active",
		AccessToken:           testJWTWithWorkspace("rw_active"),
		RefreshToken:          "refresh_active",
		AccessTokenExpiresAt:  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		RelayauthURL:          server.URL,
	})

	err := run([]string{"mount", "stale", localDir, "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("expected mount to reject stale workspace")
	}
	if !strings.Contains(err.Error(), "uses delegated relayfile workspace rw_active") {
		t.Fatalf("unexpected error: %v", err)
	}
	if joinCalls != 0 {
		t.Fatalf("expected no runtime token mint for mismatched workspace, got %d calls", joinCalls)
	}
}

func TestMountUsesLegacyRecordedLocalDirWhenOmitted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:       "demo",
			LocalDir:   localDir,
			CreatedAt:  now,
			LastUsedAt: now,
		}},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/workspaces/demo/fs/export":
			_, _ = w.Write([]byte(`[{"path":"/notion/Docs/Legacy.md","revision":"rev_1","contentType":"text/markdown","content":"# Legacy"}]`))
		case "/v1/workspaces/demo/fs/events":
			_, _ = w.Write([]byte(`{"events":[{"eventId":"evt_1","type":"file.created","path":"/notion/Docs/Legacy.md","revision":"rev_1"}]}`))
		case "/v1/workspaces/demo/sync/status":
			_, _ = w.Write([]byte(`{"workspaceId":"demo","providers":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  testJWTWithWorkspace("demo"),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	if err := run([]string{"mount", "demo", "--server", server.URL, "--token", testJWTWithWorkspace("demo"), "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run mount failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(localDir, "notion", "Docs", "Legacy.md"))
	if err != nil {
		t.Fatalf("expected file under legacy recorded localDir: %v", err)
	}
	if string(data) != "# Legacy" {
		t.Fatalf("unexpected mirrored content: %q", data)
	}
}

func TestShouldRegisterMountPID(t *testing.T) {
	tests := []struct {
		name       string
		daemonized bool
		once       bool
		want       bool
	}{
		{name: "background child", daemonized: true, once: false, want: true},
		{name: "foreground loop", daemonized: false, once: false, want: true},
		{name: "one shot", daemonized: false, once: true, want: false},
		{name: "daemonized one shot still visible", daemonized: true, once: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRegisterMountPID(tt.daemonized, tt.once); got != tt.want {
				t.Fatalf("shouldRegisterMountPID(%v, %v) = %v, want %v", tt.daemonized, tt.once, got, tt.want)
			}
		})
	}
}

func TestMountRefusesExplicitLocalDirThatRehomesRecordedMirror(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	otherDir := filepath.Join(t.TempDir(), "other-mirror")
	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:       "demo",
			ID:         "ws_demo",
			LocalDir:   localDir,
			CreatedAt:  now,
			LastUsedAt: now,
		}},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}
	if err := saveCredentials(credentials{
		Server: defaultServerURL,
		Token:  testJWTWithWorkspace("ws_demo"),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	err := run([]string{"mount", "demo", otherDir, "--token", testJWTWithWorkspace("ws_demo"), "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("expected mount to refuse re-homing a recorded mirror")
	}
	if !strings.Contains(err.Error(), "refusing to silently re-home") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(otherDir, ".relay")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused re-home should not initialize other mirror dir; stat err=%v", statErr)
	}
}

func TestMountRehomeRefusesRunningRecordedDaemon(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	otherDir := filepath.Join(t.TempDir(), "other-mirror")
	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:       "demo",
			ID:         "ws_demo",
			LocalDir:   localDir,
			CreatedAt:  now,
			LastUsedAt: now,
		}},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := os.WriteFile(mountPIDFile(localDir), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatalf("write legacy pid failed: %v", err)
	}
	if err := saveCredentials(credentials{
		Server: defaultServerURL,
		Token:  testJWTWithWorkspace("ws_demo"),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	err := run([]string{"mount", "demo", otherDir, "--token", testJWTWithWorkspace("ws_demo"), "--rehome", "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("expected rehome to refuse while old mirror has a running daemon")
	}
	if !strings.Contains(err.Error(), "has a running mount") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(otherDir, ".relay")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused rehome should not initialize other mirror dir; stat err=%v", statErr)
	}
}

func TestMountRefusesCompetingDaemonBeforePersistingAddedScope(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:        "demo",
			ID:          "ws_demo",
			LocalDir:    localDir,
			LocalLayout: mountscope.LayoutScoped,
			RemotePaths: []string{"/github"},
			CreatedAt:   now,
			LastUsedAt:  now,
		}},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeDaemonPIDState(mountPIDFile(localDir), daemonPIDState{
		PID:         os.Getpid(),
		WorkspaceID: "ws_demo",
		LocalDir:    localDir,
		Executable:  resolvedSelfExecutable(),
	}); err != nil {
		t.Fatalf("write daemon pid state failed: %v", err)
	}

	err := run([]string{
		"mount", "demo", localDir,
		"--token", testJWTWithWorkspace("ws_demo"),
		"--remote-path", "/github",
		"--remote-path", "/slack",
		"--local-layout", "scoped",
		"--background",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "already has a running mount") {
		t.Fatalf("expected competing-daemon refusal, got %v", err)
	}
	record, ok := workspaceRecordByID("ws_demo")
	if !ok {
		t.Fatal("expected persisted workspace record")
	}
	if got := strings.Join(record.RemotePaths, ","); got != "/github" {
		t.Fatalf("refused start changed persisted paths to %q", got)
	}
	if _, statErr := os.Stat(filepath.Join(localDir, "slack")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused start initialized added scope: %v", statErr)
	}
}

func TestMountOnceRefusesCompetingDaemonBeforePersistingAddedScope(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:        "demo",
			ID:          "ws_demo",
			LocalDir:    localDir,
			LocalLayout: mountscope.LayoutScoped,
			RemotePaths: []string{"/github"},
			CreatedAt:   now,
			LastUsedAt:  now,
		}},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeDaemonPIDState(mountPIDFile(localDir), daemonPIDState{
		PID:         os.Getpid(),
		WorkspaceID: "ws_demo",
		LocalDir:    localDir,
		Executable:  resolvedSelfExecutable(),
	}); err != nil {
		t.Fatalf("write daemon pid state failed: %v", err)
	}

	err := run([]string{
		"mount", "demo", localDir,
		"--token", testJWTWithWorkspace("ws_demo"),
		"--remote-path", "/github",
		"--remote-path", "/slack",
		"--local-layout", "scoped",
		"--once",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "already has a running mount") {
		t.Fatalf("expected one-shot competing-daemon refusal, got %v", err)
	}
	record, ok := workspaceRecordByID("ws_demo")
	if !ok {
		t.Fatal("expected persisted workspace record")
	}
	if got := strings.Join(record.RemotePaths, ","); got != "/github" {
		t.Fatalf("refused one-shot changed persisted paths to %q", got)
	}
	if _, statErr := os.Stat(filepath.Join(localDir, "slack")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused one-shot initialized added scope: %v", statErr)
	}
}

func TestMountRehomeRefusesUnverifiedRecordedDaemon(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	otherDir := filepath.Join(t.TempDir(), "other-mirror")
	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:       "demo",
			ID:         "ws_demo",
			LocalDir:   localDir,
			CreatedAt:  now,
			LastUsedAt: now,
		}},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeDaemonPIDState(mountPIDFile(localDir), daemonPIDState{
		PID:         os.Getpid(),
		WorkspaceID: "ws_other",
		LocalDir:    localDir,
	}); err != nil {
		t.Fatalf("write daemon pid state failed: %v", err)
	}
	if err := saveCredentials(credentials{
		Server: defaultServerURL,
		Token:  testJWTWithWorkspace("ws_demo"),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	err := run([]string{"mount", "demo", otherDir, "--token", testJWTWithWorkspace("ws_demo"), "--rehome", "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("expected rehome to refuse unverified mount state")
	}
	if !strings.Contains(err.Error(), "unverified mount state") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(mountPIDFile(localDir)); statErr != nil {
		t.Fatalf("unverified pid file should remain in place: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(otherDir, ".relay")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused rehome should not initialize other mirror dir; stat err=%v", statErr)
	}
}

func TestMountRehomeAllowsExplicitMoveAndPersistsLocalDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	otherDir := t.TempDir()
	oldStateFile := filepath.Join(t.TempDir(), "old-explicit-state.json")
	if err := os.WriteFile(oldStateFile, []byte(`not valid mount state`), 0o600); err != nil {
		t.Fatalf("write old explicit state file: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:           "demo",
			ID:             "ws_demo",
			LocalDir:       localDir,
			MountStateFile: oldStateFile,
			CreatedAt:      now,
			LastUsedAt:     now,
		}},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/workspaces/ws_demo/fs/export":
			_, _ = w.Write([]byte(`[{"path":"/notion/Docs/Rehomed.md","revision":"rev_1","contentType":"text/markdown","content":"# Rehomed"}]`))
		case "/v1/workspaces/ws_demo/fs/events":
			_, _ = w.Write([]byte(`{"events":[{"eventId":"evt_1","type":"file.created","path":"/notion/Docs/Rehomed.md","revision":"rev_1"}]}`))
		case "/v1/workspaces/ws_demo/sync/status":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  testJWTWithWorkspace("ws_demo"),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	if err := run([]string{"mount", "demo", otherDir, "--server", server.URL, "--token", testJWTWithWorkspace("ws_demo"), "--rehome", "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run mount --rehome failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(otherDir, "notion", "Docs", "Rehomed.md"))
	if err != nil {
		t.Fatalf("expected file under rehomed localDir: %v", err)
	}
	if string(data) != "# Rehomed" {
		t.Fatalf("unexpected mirrored content: %q", data)
	}
	record, ok := workspaceRecordByID("ws_demo")
	if !ok {
		t.Fatalf("expected workspace record")
	}
	wantLocalDir, _ := filepath.Abs(otherDir)
	if record.LocalDir != wantLocalDir {
		t.Fatalf("LocalDir = %q, want %q", record.LocalDir, wantLocalDir)
	}
	if record.MountStateFile != "" {
		t.Fatalf("rehomed mount retained old explicit state file %q", record.MountStateFile)
	}
}

func TestLogsPrintsTailForWorkspace(t *testing.T) {
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
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := os.WriteFile(mountLogFile(localDir), []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
		t.Fatalf("write mount log failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"logs", "demo", "--lines", "2"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run logs failed: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "three\nfour" {
		t.Fatalf("unexpected logs output: %q", got)
	}
}

func TestMountOnceRefreshesDelegatedWorkspaceTokenBeforeSync(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	oldToken := testJWTWithWorkspaceAgentAndExpiry("ws_refresh", "old", time.Now().Add(-time.Minute))
	newToken := testJWTWithWorkspaceAndAgent("ws_refresh", "new")
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_refresh",
		LocalDir:   localDir,
		AgentName:  "relayfile-cli",
		Scopes:     append([]string(nil), defaultJoinScopes...),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	eventCalls := 0
	exportCalls := 0
	refreshCalls := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/tokens/refresh":
			refreshCalls++
			var body struct {
				RefreshToken string `json:"refreshToken"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode refresh body: %v", err)
			}
			if body.RefreshToken != "refresh_old" {
				t.Fatalf("unexpected refresh token %q", body.RefreshToken)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"accessToken":           newToken,
				"refreshToken":          "refresh_new",
				"accessTokenExpiresAt":  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				"refreshTokenExpiresAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
			})
		case "/v1/workspaces/ws_refresh/fs/events":
			eventCalls++
			gotAuth := r.Header.Get("Authorization")
			if gotAuth != "Bearer "+newToken {
				t.Fatalf("unexpected events Authorization: %q", gotAuth)
			}
			_, _ = w.Write([]byte(`{"events":[]}`))
		case "/v1/workspaces/ws_refresh/fs/export":
			exportCalls++
			gotAuth := r.Header.Get("Authorization")
			if gotAuth != "Bearer "+newToken {
				t.Fatalf("unexpected refreshed export Authorization: %q", gotAuth)
			}
			_, _ = w.Write([]byte(`[]`))
		case "/v1/workspaces/ws_refresh/sync/status":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_refresh","providers":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:          server.URL,
		RelayfileWorkspaceID:  "ws_refresh",
		AccessToken:           oldToken,
		RefreshToken:          "refresh_old",
		AccessTokenExpiresAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		RelayauthURL:          server.URL,
	})

	if err := run([]string{
		"mount", "ws_refresh", localDir,
		"--server", server.URL,
		"--once",
		"--websocket=false",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run mount failed: %v", err)
	}
	if refreshCalls != 1 || exportCalls != 1 {
		t.Fatalf("expected 1 refresh and 1 export call, got refresh=%d events=%d export=%d", refreshCalls, eventCalls, exportCalls)
	}
	if _, err := os.Stat(credentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected refreshed relayfile token not to be persisted, got err=%v", err)
	}
}

func TestMountSkipsDataPlaneWhenDelegatedRefreshRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	oldToken := testJWTWithWorkspaceAgentAndExpiry("ws_refresh", "old", time.Now().Add(-time.Minute))
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_refresh",
		LocalDir:   localDir,
		AgentName:  "relayfile-cli",
		Scopes:     append([]string(nil), defaultJoinScopes...),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	refreshCalls := 0
	dataPlaneCalls := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/tokens/refresh":
			refreshCalls++
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"delegation_expired","message":"delegation expired"}`))
		case "/v1/workspaces/ws_refresh/fs/events", "/v1/workspaces/ws_refresh/fs/export", "/v1/workspaces/ws_refresh/sync/status":
			dataPlaneCalls++
			t.Fatalf("data-plane call should be skipped after rejected refresh: %s", r.URL.Path)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	credsPath := writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:          server.URL,
		RelayfileWorkspaceID:  "ws_refresh",
		AccessToken:           oldToken,
		RefreshToken:          "refresh_old",
		AccessTokenExpiresAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		RelayauthURL:          server.URL,
	})
	before, err := os.ReadFile(credsPath)
	if err != nil {
		t.Fatalf("read delegated credentials before mount failed: %v", err)
	}

	err = run([]string{
		"mount", "ws_refresh", localDir,
		"--server", server.URL,
		"--once",
		"--websocket=false",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("expected mount to fail fast after rejected delegated refresh")
	}
	if !errors.Is(err, ErrDelegatedRelayfileCredentialsExpired) {
		t.Fatalf("expected delegated credential expiry error, got %v", err)
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls = %d, want 1", refreshCalls)
	}
	if dataPlaneCalls != 0 {
		t.Fatalf("dataPlaneCalls = %d, want 0", dataPlaneCalls)
	}
	after, err := os.ReadFile(credsPath)
	if err != nil {
		t.Fatalf("read delegated credentials after mount failed: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("delegated credentials changed after rejected refresh\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func clearRelayfileEnv(t *testing.T) {
	t.Helper()
	resetActiveWorkspaceRecordForMountResolutionCache()
	t.Setenv("RELAYFILE_SERVER", "")
	t.Setenv("RELAYFILE_BASE_URL", "")
	t.Setenv("RELAYFILE_TOKEN", "")
	t.Setenv("RELAYFILE_WORKSPACE", "")
	t.Setenv("RELAYFILE_CLOUD_API_URL", "")
	t.Setenv("RELAYFILE_CLOUD_TOKEN", "")
	t.Setenv("RELAYFILE_MOUNT_CREDS_FILE", "")
	t.Setenv("RELAYFILE_DELEGATED_CREDENTIALS_FILE", "")
	t.Setenv("RELAYCAST_BASE_URL", "")
	t.Setenv("RELAY_BASE_URL", "")
	t.Setenv("AGENT_RELAY_BIN", "")
}

func installFakeAgentRelay(t *testing.T, scriptBody string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-relay")
	script := `#!/bin/sh
set -eu
if [ "$*" = "--version" ]; then
  echo "8.7.0"
  exit 0
fi
if [ "$*" = "cloud session --help" ]; then
  echo "Usage: agent-relay cloud session [options]"
  echo "  --json"
  exit 0
fi
if [ "$*" = "workspace active --help" ]; then
  echo "Usage: agent-relay workspace active [options]"
  echo "  --json"
  exit 0
fi
if [ "$*" = "workspace switch --help" ]; then
  echo "Usage: agent-relay workspace switch [options] <name>"
  exit 0
fi
` + scriptBody + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake agent-relay failed: %v", err)
	}
	t.Setenv("AGENT_RELAY_BIN", path)
	return path
}

func installFakeAgentRelaySession(t *testing.T, apiURL, accessToken, name, cloudWorkspaceID, relayfileWorkspaceID string) {
	t.Helper()
	body := fmt.Sprintf(`
if [ "$*" = "cloud session --json --reveal-token" ] || [ "$*" = "cloud session --json" ]; then
  echo '{"apiUrl":%q,"accessToken":%q}'
  exit 0
fi
if [ "$*" = "workspace active --json --reveal-secrets" ] || [ "$*" = "workspace active --json" ]; then
  echo '{"name":%q,"cloudWorkspaceId":%q,"relayfileWorkspaceId":%q}'
  exit 0
fi
echo "unexpected args: $*" >&2
exit 2
`, apiURL, accessToken, name, cloudWorkspaceID, relayfileWorkspaceID)
	installFakeAgentRelay(t, body)
}

func TestCloudCredentialsFallBackWhenRevealTokenUnsupported(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	installFakeAgentRelay(t, `
if [ "$*" = "cloud session --json --reveal-token" ]; then
  echo "error: unknown option '--reveal-token'" >&2
  exit 1
fi
if [ "$*" = "cloud session --json" ]; then
  echo '{"apiUrl":"https://cloud.test","accessToken":"cld_at_raw_fallback_token"}'
  exit 0
fi
echo "unexpected args: $*" >&2
exit 2
`)

	creds, err := cloudCredentialsFromAgentRelay()
	if err != nil {
		t.Fatalf("cloudCredentialsFromAgentRelay failed: %v", err)
	}
	if creds.AccessToken != "cld_at_raw_fallback_token" {
		t.Fatalf("unexpected access token: %q", creds.AccessToken)
	}
}

func TestCloudCredentialsRejectMaskedAccessToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	installFakeAgentRelay(t, `
if [ "$*" = "cloud session --json --reveal-token" ] || [ "$*" = "cloud session --json" ]; then
  echo '{"apiUrl":"https://cloud.test","accessToken":"cld_at_…oken"}'
  exit 0
fi
echo "unexpected args: $*" >&2
exit 2
`)

	_, err := cloudCredentialsFromAgentRelay()
	if err == nil || !strings.Contains(err.Error(), "masked accessToken") {
		t.Fatalf("expected masked accessToken error, got %v", err)
	}
}

func TestActiveWorkspaceFallsBackWhenRevealSecretsUnsupported(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	installFakeAgentRelay(t, `
if [ "$*" = "workspace active --json --reveal-secrets" ]; then
  echo "error: unknown option '--reveal-secrets'" >&2
  exit 1
fi
if [ "$*" = "workspace active --json" ]; then
  echo '{"name":"legacy","cloudWorkspaceId":"rw_cloud","relayfileWorkspaceId":"rw_relayfile"}'
  exit 0
fi
echo "unexpected args: $*" >&2
exit 2
`)

	workspace, err := activeWorkspaceFromAgentRelay()
	if err != nil {
		t.Fatalf("activeWorkspaceFromAgentRelay failed: %v", err)
	}
	if workspace.Name != "legacy" || workspace.CloudWorkspaceID != "rw_cloud" || workspace.RelayfileWorkspaceID != "rw_relayfile" {
		t.Fatalf("unexpected workspace fallback result: %+v", workspace)
	}
}

func relayfileCLITestFixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read CLI test fixture %s: %v", name, err)
	}
	return strings.TrimSpace(string(content))
}

func agentRelayResolver404Error(workspaceKey, body string) error {
	return errors.New("Workspace resolve failed at /api/v1/workspaces/active?key=" + workspaceKey + ": 404 " + body)
}

func TestActiveWorkspaceClassifierFallsBackToStoreWhenErrorRedacted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	for _, name := range []string{"AGENT_RELAY_WORKSPACE_KEY", "RELAY_WORKSPACE_KEY", "RELAY_API_KEY", "AGENT_RELAY_HOME"} {
		t.Setenv(name, "")
	}
	// A masked environment value must normalize to the empty sentinel so it
	// cannot block fallback to the usable key in the shared workspace store.
	const maskedWorkspaceKey = "rk_live_…masked"
	if got := usableAgentRelayWorkspaceKey(maskedWorkspaceKey); got != "" {
		t.Fatalf("masked workspace key normalized to %q, want empty sentinel", got)
	}
	t.Setenv("AGENT_RELAY_WORKSPACE_KEY", maskedWorkspaceKey)

	const workspaceKey = "rk_live_messaging_only_redacted"
	var validationCalls int
	relaycast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		validationCalls++
		if got := r.Header.Get("Authorization"); got != "Bearer "+workspaceKey {
			t.Fatalf("unexpected Relaycast Authorization: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"data":{"id":"rc_123","name":"chat-only"}}`))
	}))
	defer relaycast.Close()
	t.Setenv("RELAYCAST_BASE_URL", relaycast.URL)

	storeDir := filepath.Join(os.Getenv("HOME"), ".agentworkforce", "relay")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir workspace store dir failed: %v", err)
	}
	store := `{"active":"demo","workspaces":{"demo":{"key":"` + workspaceKey + `"}}}`
	if err := os.WriteFile(filepath.Join(storeDir, "workspaces.json"), []byte(store), 0o600); err != nil {
		t.Fatalf("write workspace store failed: %v", err)
	}

	// A CLI that redacts credentials emits a masked key the resolver-error
	// regex cannot capture; the classifier must fall back to the store.
	resolverFailure := agentRelayResolver404Error("rk_live_…cted", relayfileCLITestFixture(t, "cloud-workspace-not-found.json"))
	installFakeAgentRelay(t, fmt.Sprintf(`
if [ "$*" = "workspace active --json --reveal-secrets" ] || [ "$*" = "workspace active --json" ]; then
  printf '%%s\n' %s >&2
  exit 1
fi
echo "unexpected args: $*" >&2
exit 2
`, strconv.Quote(resolverFailure.Error())))

	_, err := activeWorkspaceFromAgentRelay()
	if err == nil {
		t.Fatal("expected messaging-only workspace error")
	}
	var messagingOnly *agentRelayMessagingOnlyWorkspaceError
	if !errors.As(err, &messagingOnly) {
		t.Fatalf("expected agentRelayMessagingOnlyWorkspaceError, got %T: %v", err, err)
	}
	if messagingOnly.Name != "chat-only" {
		t.Fatalf("messaging-only workspace name = %q, want chat-only", messagingOnly.Name)
	}
	if validationCalls != 1 {
		t.Fatalf("expected exactly one Relaycast validation call, got %d", validationCalls)
	}
}

func TestActiveWorkspaceFromAgentRelayReportsMessagingOnlyWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspaceKey = "rk_live_messaging_only_test"
	var validationCalls int
	relaycast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		validationCalls++
		if r.Method != http.MethodGet || r.URL.Path != "/v1/workspace" {
			t.Fatalf("unexpected Relaycast request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+workspaceKey {
			t.Fatalf("unexpected Relaycast Authorization: %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "relayfile-cli/"+relayfileVersion {
			t.Fatalf("unexpected Relaycast User-Agent: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"data":{"id":"rc_123","name":"chat-only"}}`))
	}))
	defer relaycast.Close()
	t.Setenv("RELAYCAST_BASE_URL", relaycast.URL)
	resolverFailure := agentRelayResolver404Error(workspaceKey, relayfileCLITestFixture(t, "cloud-workspace-not-found.json"))
	installFakeAgentRelay(t, fmt.Sprintf(`
if [ "$*" = "workspace active --json --reveal-secrets" ] || [ "$*" = "workspace active --json" ]; then
  printf '%%s\n' %s >&2
  exit 1
fi
echo "unexpected args: $*" >&2
exit 2
`, strconv.Quote(resolverFailure.Error())))

	_, err := activeWorkspaceFromAgentRelay()
	if err == nil {
		t.Fatal("expected messaging-only workspace error")
	}
	var messagingOnly *agentRelayMessagingOnlyWorkspaceError
	if !errors.As(err, &messagingOnly) {
		t.Fatalf("expected agentRelayMessagingOnlyWorkspaceError, got %T: %v", err, err)
	}
	if messagingOnly.Name != "chat-only" {
		t.Fatalf("messaging-only workspace name = %q, want chat-only", messagingOnly.Name)
	}
	got := err.Error()
	for _, want := range []string{
		"messaging-only (Relaycast-only)",
		"not Relayfile-backed",
		`relayfile setup --workspace "chat-only"`,
		"relayfile login --provision-messaging-only",
		"messaging workspace and its key will remain unchanged",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("messaging-only error missing %q: %q", want, got)
		}
	}
	for _, unwanted := range []string{"404", "Workspace resolve failed", workspaceKey} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("messaging-only error leaked opaque resolver detail %q: %q", unwanted, got)
		}
	}
	if validationCalls != 1 {
		t.Fatalf("Relaycast validation calls = %d, want 1", validationCalls)
	}
}

func TestClassifyAgentRelayActiveWorkspaceErrorReportsRelaycastNetworkError(t *testing.T) {
	clearRelayfileEnv(t)

	const workspaceKey = "rk_live_network_error_test"
	relaycast := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	baseURL := relaycast.URL
	relaycast.Close()
	t.Setenv("RELAYCAST_BASE_URL", baseURL)

	err := classifyAgentRelayActiveWorkspaceError(agentRelayResolver404Error(workspaceKey, "upstream response body omitted"))
	if err == nil {
		t.Fatal("expected Relaycast network verification error")
	}
	var messagingOnly *agentRelayMessagingOnlyWorkspaceError
	var invalidKey *agentRelayInvalidWorkspaceKeyError
	if errors.As(err, &messagingOnly) || errors.As(err, &invalidKey) {
		t.Fatalf("network failure was misclassified: %T: %v", err, err)
	}
	for _, want := range []string{"could not verify it with Relaycast", "Check network connectivity and try again"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("network verification error missing %q: %q", want, err)
		}
	}
}

func TestClassifyAgentRelayActiveWorkspaceErrorReportsUnexpectedRelaycastStatus(t *testing.T) {
	clearRelayfileEnv(t)

	const workspaceKey = "rk_live_unexpected_status_test"
	relaycast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/workspace" {
			t.Fatalf("unexpected Relaycast request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer relaycast.Close()
	t.Setenv("RELAYCAST_BASE_URL", relaycast.URL)

	err := classifyAgentRelayActiveWorkspaceError(agentRelayResolver404Error(workspaceKey, "opaque Cloud miss"))
	if err == nil || !strings.Contains(err.Error(), "Relaycast verification returned HTTP 503") {
		t.Fatalf("unexpected-status error = %v", err)
	}
	var messagingOnly *agentRelayMessagingOnlyWorkspaceError
	var invalidKey *agentRelayInvalidWorkspaceKeyError
	if errors.As(err, &messagingOnly) || errors.As(err, &invalidKey) {
		t.Fatalf("unexpected status was misclassified: %T: %v", err, err)
	}
}

func TestClassifyAgentRelayActiveWorkspaceErrorPreservesRegexMiss(t *testing.T) {
	// A regex miss falls through to Agent Relay's shared workspace store.
	// Isolate every store input so this test cannot read an operator's live key.
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	for _, name := range []string{"AGENT_RELAY_WORKSPACE_KEY", "RELAY_WORKSPACE_KEY", "RELAY_API_KEY"} {
		t.Setenv(name, "")
	}
	t.Setenv("AGENT_RELAY_HOME", t.TempDir())

	var validationCalls int
	relaycast := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		validationCalls++
	}))
	defer relaycast.Close()
	t.Setenv("RELAYCAST_BASE_URL", relaycast.URL)

	original := errors.New("Workspace resolve failed without an embedded key: 404 " + relayfileCLITestFixture(t, "cloud-workspace-not-found.json"))
	classified := classifyAgentRelayActiveWorkspaceError(original)
	if classified != original {
		t.Fatalf("regex-miss fallback changed the original error: got %v, want %v", classified, original)
	}
	if validationCalls != 0 {
		t.Fatalf("Relaycast validation calls = %d, want 0", validationCalls)
	}
}

func TestActiveWorkspaceFromAgentRelayDoesNotMisclassifyInvalidKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspaceKey = "rk_live_invalid_test"
	relaycast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+workspaceKey {
			t.Fatalf("unexpected Relaycast Authorization: %q", got)
		}
		http.Error(w, "invalid key", http.StatusUnauthorized)
	}))
	defer relaycast.Close()
	t.Setenv("RELAYCAST_BASE_URL", relaycast.URL)
	installFakeAgentRelay(t, `
if [ "$*" = "workspace active --json --reveal-secrets" ] || [ "$*" = "workspace active --json" ]; then
  echo 'Workspace resolve failed at /api/v1/workspaces/`+workspaceKey+`/resolve: 404 Workspace not found' >&2
  exit 1
fi
echo "unexpected args: $*" >&2
exit 2
`)

	_, err := activeWorkspaceFromAgentRelay()
	if err == nil {
		t.Fatal("expected invalid workspace key error")
	}
	var messagingOnly *agentRelayMessagingOnlyWorkspaceError
	if errors.As(err, &messagingOnly) {
		t.Fatalf("invalid key was misclassified as messaging-only: %v", err)
	}
	var invalidKey *agentRelayInvalidWorkspaceKeyError
	if !errors.As(err, &invalidKey) {
		t.Fatalf("expected agentRelayInvalidWorkspaceKeyError, got %T: %v", err, err)
	}
	got := err.Error()
	if !strings.Contains(got, "invalid or unknown") || !strings.Contains(got, "agent-relay workspace switch <name>") {
		t.Fatalf("invalid-key error was not actionable: %q", got)
	}
	if strings.Contains(got, "404") || strings.Contains(got, workspaceKey) {
		t.Fatalf("invalid-key error leaked opaque resolver detail: %q", got)
	}
}

func TestLoginCanProvisionSeparateWorkspaceForMessagingOnlyRelaycastWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspaceKey = "rk_live_messaging_only_provision_test"
	relaycast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/workspace" || r.Header.Get("Authorization") != "Bearer "+workspaceKey {
			t.Fatalf("unexpected Relaycast validation request: %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"data":{"id":"rc_messaging","name":"chat-only"}}`))
	}))
	defer relaycast.Close()
	t.Setenv("RELAYCAST_BASE_URL", relaycast.URL)

	var createCalls, mintCalls int
	var cloud *httptest.Server
	cloud = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces":
			createCalls++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode Cloud create body: %v", err)
			}
			if len(body) != 1 || body["name"] != "chat-only (Relayfile)" {
				t.Fatalf("Cloud create must contain only the messaging workspace name, got %#v", body)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"rw_relayfile","relayfileUrl":"` + cloud.URL + `","createdAt":"2026-07-13T00:00:00Z"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces/rw_relayfile/relayfile/delegated-token":
			mintCalls++
			if got := r.Header.Get("Authorization"); got != "Bearer cld_access" {
				t.Fatalf("unexpected delegated-token Authorization: %q", got)
			}
			writeDelegatedBundleResponse(t, w, cloud.URL, "rw_relayfile", "rf_access", "rf_refresh")
		default:
			t.Fatalf("unexpected Cloud request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer cloud.Close()

	resolverFailure := agentRelayResolver404Error(workspaceKey, relayfileCLITestFixture(t, "cloud-workspace-not-found.json"))
	installFakeAgentRelay(t, fmt.Sprintf(`
if [ "$*" = "cloud login --no-open" ]; then
  echo "agent-relay login ok"
  exit 0
fi
if [ "$*" = "cloud session --json --reveal-token" ] || [ "$*" = "cloud session --json" ]; then
  echo '{"apiUrl":"`+cloud.URL+`","accessToken":"cld_access"}'
  exit 0
fi
if [ "$*" = "workspace active --json --reveal-secrets" ] || [ "$*" = "workspace active --json" ]; then
  printf '%%s\n' %s >&2
  exit 1
fi
echo "unexpected args: $*" >&2
exit 2
`, strconv.Quote(resolverFailure.Error())))

	var stdout bytes.Buffer
	if err := run([]string{"login", "--no-open", "--provision-messaging-only"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run login with messaging-only provisioning failed: %v\noutput:\n%s", err, stdout.String())
	}
	if createCalls != 1 || mintCalls != 1 {
		t.Fatalf("Cloud calls create=%d mint=%d, want 1 each", createCalls, mintCalls)
	}
	gotOutput := stdout.String()
	for _, want := range []string{"separate Relayfile-backed workspace chat-only (Relayfile)", "id: rw_relayfile", "messaging-only Agent Relay workspace and its key were left unchanged"} {
		if !strings.Contains(gotOutput, want) {
			t.Fatalf("provisioning output missing %q: %q", want, gotOutput)
		}
	}
	record, ok := workspaceRecordByName("chat-only (Relayfile)")
	if !ok || record.ID != "rw_relayfile" {
		t.Fatalf("separate Relayfile workspace was not persisted: ok=%v record=%+v", ok, record)
	}
	delegated, err := delegatedauth.Load(delegatedCredentialsPathForRequest("rw_relayfile", defaultJoinScopes))
	if err != nil {
		t.Fatalf("load provisioned delegated credentials: %v", err)
	}
	if delegated.Workspace() != "rw_relayfile" || delegated.BearerToken() != "rf_access" {
		t.Fatalf("unexpected provisioned delegated credentials: %+v", delegated)
	}

	stdout.Reset()
	if err := run([]string{"login", "--no-open", "--provision-messaging-only"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("repeat local provisioning failed: %v\noutput:\n%s", err, stdout.String())
	}
	if createCalls != 1 || mintCalls != 2 {
		t.Fatalf("repeat local provisioning calls create=%d mint=%d, want create=1 mint=2", createCalls, mintCalls)
	}
}

func testJWTWithWorkspace(workspaceID string) string {
	return testJWTWithWorkspaceAndAgent(workspaceID, "test")
}

func testJWTWithWorkspaceAndAgent(workspaceID, agentName string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"workspace_id":"` + workspaceID + `","agent_name":"` + agentName + `","aud":"relayfile"}`))
	return header + "." + payload + ".sig"
}

func testJWTWithWorkspaceAndScopes(workspaceID, agentName string, scopes []string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]any{
		"workspace_id": workspaceID,
		"agent_name":   agentName,
		"aud":          "relayfile",
		"scopes":       scopes,
	})
	if err != nil {
		panic(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func testJWTWithWorkspaceAgentAndExpiry(workspaceID, agentName string, exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]any{
		"workspace_id": workspaceID,
		"agent_name":   agentName,
		"aud":          "relayfile",
		"iat":          exp.Add(-time.Hour).Unix(),
		"exp":          exp.Unix(),
	})
	if err != nil {
		panic(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func writeDelegatedBundleResponse(t *testing.T, w http.ResponseWriter, relayfileURL, workspaceID, accessToken, refreshToken string) {
	t.Helper()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"relayfileUrl":                   relayfileURL,
		"relayauthUrl":                   relayfileURL,
		"relayfileWorkspaceId":           workspaceID,
		"relayfileToken":                 accessToken,
		"relayfileTokenExpiresAt":        time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"relayfileRefreshToken":          refreshToken,
		"relayfileRefreshTokenExpiresAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		"relayfileScopes":                []string{"relayfile:fs:read:*", "relayfile:fs:write:*"},
		"delegationNotAfter":             time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		"relayfileMountPaths":            []string{"/"},
	})
}

func writeDelegatedCredentialsForTest(t *testing.T, bundle delegatedauth.Bundle) string {
	t.Helper()
	if bundle.RelayfileURL == "" && bundle.BaseURL == "" && bundle.Server == "" {
		bundle.RelayfileURL = defaultServerURL
	}
	if bundle.RelayfileWorkspaceID == "" && bundle.WorkspaceID == "" {
		bundle.RelayfileWorkspaceID = "ws_test"
	}
	if len(bundle.Scopes) == 0 && len(bundle.RelayfileScopes) == 0 {
		bundle.Scopes = append([]string(nil), defaultJoinScopes...)
	}
	if bundle.AccessToken == "" && bundle.Token == "" && bundle.RelayfileToken == "" {
		bundle.AccessToken = testJWTWithWorkspaceAndScopes(bundle.Workspace(), "test", bundle.Scopes)
	}
	if bundle.RefreshToken == "" && bundle.RelayfileRefreshToken == "" {
		bundle.RefreshToken = "refresh_test"
	}
	if bundle.RelayauthURL == "" && bundle.RefreshURL == "" {
		bundle.RelayauthURL = bundle.ServerURL()
	}
	path := delegatedCredentialsPath()
	if err := delegatedauth.SaveAtomic(path, bundle); err != nil {
		t.Fatalf("save delegated credentials failed: %v", err)
	}
	return path
}

func TestDelegatedBundleHasScopesAllowsBroadWildcardGrant(t *testing.T) {
	bundle := delegatedauth.Bundle{
		RelayfileScopes: []string{"relayfile:fs:write:/**"},
	}
	if !delegatedBundleHasScopes(bundle, []string{"relayfile:fs:write:/github/**"}) {
		t.Fatal("expected broad write scope to satisfy provider write scope")
	}
	if delegatedBundleHasScopes(bundle, []string{"relayfile:fs:read:/github/**"}) {
		t.Fatal("did not expect write scope to satisfy read scope")
	}
}

func TestWritebackPushScopesRequireProviderSubtree(t *testing.T) {
	for _, tc := range []struct {
		remotePath          string
		wantJoinScopes      string
		wantRelayfileScopes string
	}{
		{
			remotePath:          "/linear/issues/issue-55.json",
			wantJoinScopes:      "fs:write:/linear/**,ops:read",
			wantRelayfileScopes: "relayfile:fs:write:/linear/**",
		},
		{
			remotePath:          "/GitHub/issues/issue-55.json",
			wantJoinScopes:      "fs:write:/github/**,ops:read",
			wantRelayfileScopes: "relayfile:fs:write:/github/**",
		},
	} {
		t.Run(tc.remotePath, func(t *testing.T) {
			joinScopes, requiredRelayfileScopes, err := writebackPushScopes(tc.remotePath)
			if err != nil {
				t.Fatalf("writebackPushScopes returned error: %v", err)
			}
			if got := strings.Join(joinScopes, ","); got != tc.wantJoinScopes {
				t.Fatalf("join scopes = %q, want %q", got, tc.wantJoinScopes)
			}
			if got := strings.Join(requiredRelayfileScopes, ","); got != tc.wantRelayfileScopes {
				t.Fatalf("required relayfile scopes = %q, want %q", got, tc.wantRelayfileScopes)
			}
		})
	}

	for _, remotePath := range []string{"", "/", "///", "/foo.json", "/linear", "/**", "/*", "/./record.json", "/../record.json", "/bad provider/record.json"} {
		joinScopes, requiredRelayfileScopes, err := writebackPushScopes(remotePath)
		if err == nil {
			t.Fatalf("writebackPushScopes(%q) succeeded with join=%v relayfile=%v", remotePath, joinScopes, requiredRelayfileScopes)
		}
		if strings.Contains(strings.Join(joinScopes, ","), "/**") || strings.Contains(strings.Join(requiredRelayfileScopes, ","), "/**") {
			t.Fatalf("writebackPushScopes(%q) returned whole-tree scopes join=%v relayfile=%v", remotePath, joinScopes, requiredRelayfileScopes)
		}
		if got := err.Error(); !strings.Contains(got, "provider-scoped remote path") {
			t.Fatalf("writebackPushScopes(%q) error = %q, want provider-scoped remote path", remotePath, got)
		}
	}
}

func TestReadWorkspaceMountCursorHealthUsesPersistedPrivateState(t *testing.T) {
	localDir := t.TempDir()
	record := workspaceRecord{
		ID:            "ws_demo",
		LocalDir:      localDir,
		LocalLayout:   mountscope.LayoutExact,
		RemotePaths:   []string{"/github"},
		MountStateDir: t.TempDir(),
		MountKind:     mountsync.MountKindDaemon,
	}
	scope := workspaceMountScopes(record)[0]
	stateFile, err := workspaceMountStateFile("ws_demo", record, scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
		t.Fatalf("mkdir private state failed: %v", err)
	}
	if err := os.WriteFile(stateFile, []byte(`{"incrementalReadNotReadySince":{"a":"now","b":"now"},"incrementalBacklogDraining":true}`), 0o644); err != nil {
		t.Fatalf("write private state failed: %v", err)
	}

	stuck, backlog := readWorkspaceMountCursorHealth("ws_demo", record, scope)
	if stuck != 2 || !backlog {
		t.Fatalf("cursor health = %d/%v, want 2/true", stuck, backlog)
	}
}

func TestResolveWorkspaceLikeStatusPrefersRelayWorkspaceID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:             "demo",
		ID:               "ws_cloud",
		RelayWorkspaceID: "rw_cloud",
		LocalDir:         t.TempDir(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	workspaceID, _, err := resolveWorkspaceLikeStatus("demo")
	if err != nil {
		t.Fatalf("resolveWorkspaceLikeStatus failed: %v", err)
	}
	if workspaceID != "rw_cloud" {
		t.Fatalf("workspaceID = %q, want rw_cloud", workspaceID)
	}
}

func TestLoadDelegatedCredentialsForActiveWorkspaceUsesCatalogDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:      "cloud-prod",
		ID:        "ws_cloud",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Scopes:    append([]string(nil), defaultInspectScopes...),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	if _, err := setDefaultWorkspace("cloud-prod"); err != nil {
		t.Fatalf("setDefaultWorkspace failed: %v", err)
	}
	path := delegatedCredentialsPathForRequest("cloud-prod", defaultInspectScopes)
	if err := delegatedauth.SaveAtomic(path, delegatedauth.Bundle{
		RelayfileURL:         "https://relayfile.test",
		RelayfileWorkspaceID: "ws_cloud",
		AccessToken:          testJWTWithWorkspaceAndScopes("ws_cloud", "relayfile-cli", defaultInspectScopes),
		RefreshToken:         "refresh_read",
		Scopes:               append([]string(nil), defaultInspectScopes...),
	}); err != nil {
		t.Fatalf("save delegated credentials failed: %v", err)
	}

	_, loadedPath, err := loadDelegatedCredentialsForRequest("", "active", defaultInspectScopes)
	if err != nil {
		t.Fatalf("loadDelegatedCredentialsForRequest failed: %v", err)
	}
	if loadedPath != path {
		t.Fatalf("loadedPath = %q, want %q", loadedPath, path)
	}
}

func TestDelegatedCredentialsPathCanonicalizesWorkspaceAliases(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "default",
		Workspaces: []workspaceRecord{
			{Name: "50587328-441d-4acb-b8f3-dbe1b3c5de99", ID: "50587328-441d-4acb-b8f3-dbe1b3c5de99", RelayWorkspaceID: "rw_7ccfea89", CreatedAt: now, Scopes: append([]string(nil), defaultJoinScopes...)},
			{Name: "default", ID: "50587328-441d-4acb-b8f3-dbe1b3c5de99", RelayWorkspaceID: "rw_7ccfea89", CreatedAt: now, Scopes: append([]string(nil), defaultJoinScopes...)},
			{Name: "default", ID: "50587328-441d-4acb-b8f3-dbe1b3c5de99", RelayWorkspaceID: "rw_7ccfea89", CreatedAt: now, Scopes: append([]string(nil), defaultJoinScopes...)},
			{Name: "other", ID: "60698439-552e-4bdc-c9f4-ecf2c4d6efa0", RelayWorkspaceID: "rw_other", CreatedAt: now, Scopes: append([]string(nil), defaultJoinScopes...)},
		},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}

	want := delegatedCredentialsPathForRequest("50587328-441d-4acb-b8f3-dbe1b3c5de99", defaultJoinScopes)
	for _, input := range []string{"rw_7ccfea89", "default", "active", ""} {
		if got := delegatedCredentialsPathForRequest(input, defaultJoinScopes); got != want {
			t.Fatalf("path for %q = %q, want %q", input, got, want)
		}
	}
	other := delegatedCredentialsPathForRequest("rw_other", defaultJoinScopes)
	if other == want {
		t.Fatalf("rw_ aliases were not one-to-one: other path %q matched default path", other)
	}
	if got := delegatedCredentialsPathForRequest("other", defaultJoinScopes); got != other {
		t.Fatalf("path for other name = %q, want %q", got, other)
	}
}

func TestDelegatedCredentialsPathLeavesAmbiguousRelayWorkspaceAliasRaw(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Workspaces: []workspaceRecord{
			{Name: "one", ID: "11111111-1111-1111-1111-111111111111", RelayWorkspaceID: "rw_shared", CreatedAt: now},
			{Name: "two", ID: "22222222-2222-2222-2222-222222222222", RelayWorkspaceID: "rw_shared", CreatedAt: now},
		},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}

	if value, ok := canonicalWorkspaceShardValueStatus("rw_shared"); ok || value != "rw_shared" {
		t.Fatalf("ambiguous relay alias canonicalized to value=%q ok=%v, want raw rw_shared false", value, ok)
	}
	if got, want := workspaceShardKey("rw_shared"), rawWorkspaceShardKey("rw_shared"); got != want {
		t.Fatalf("ambiguous relay alias shard key = %q, want raw %q", got, want)
	}
}

func TestLoadDelegatedCredentialsMigratesLegacyRelayWorkspaceShard(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	records := []workspaceRecord{
		{Name: "default", ID: "50587328-441d-4acb-b8f3-dbe1b3c5de99", RelayWorkspaceID: "rw_7ccfea89", CreatedAt: now, Scopes: append([]string(nil), defaultJoinScopes...)},
		{Name: "default", ID: "50587328-441d-4acb-b8f3-dbe1b3c5de99", RelayWorkspaceID: "rw_7ccfea89", CreatedAt: now, Scopes: append([]string(nil), defaultJoinScopes...)},
		{Name: "rw_7ccfea89", ID: "rw_7ccfea89", RelayWorkspaceID: "50587328-441d-4acb-b8f3-dbe1b3c5de99", CreatedAt: now, Scopes: append([]string(nil), defaultJoinScopes...)},
		{Name: "rw_7ccfea89", ID: "rw_7ccfea89", CreatedAt: now, Scopes: append([]string(nil), defaultJoinScopes...)},
	}
	run := func(t *testing.T, records []workspaceRecord) {
		t.Helper()
		t.Setenv("HOME", t.TempDir())
		clearRelayfileEnv(t)
		if err := saveWorkspaceCatalog(workspaceCatalog{
			Default:    "default",
			Workspaces: records,
		}); err != nil {
			t.Fatalf("saveWorkspaceCatalog failed: %v", err)
		}
		mintedPath := delegatedCredentialsPathForRequest("50587328-441d-4acb-b8f3-dbe1b3c5de99", defaultJoinScopes)
		resolvedPath := delegatedCredentialsPathForRequest("rw_7ccfea89", defaultJoinScopes)
		if mintedPath == resolvedPath {
			t.Fatalf("test setup expected polluted catalog to produce distinct mint/resolve paths, got %q", mintedPath)
		}
		if err := delegatedauth.SaveAtomic(mintedPath, delegatedauth.Bundle{
			RelayfileURL:         "https://relayfile.test",
			RelayfileWorkspaceID: "rw_7ccfea89",
			AccessToken:          testJWTWithWorkspaceAndScopes("rw_7ccfea89", "relayfile-cli", defaultJoinScopes),
			RefreshToken:         "refresh_join",
			Scopes:               append([]string(nil), defaultJoinScopes...),
		}); err != nil {
			t.Fatalf("save minted delegated credentials failed: %v", err)
		}
		if _, err := os.Stat(resolvedPath); !os.IsNotExist(err) {
			t.Fatalf("resolved path unexpectedly exists before heal: %v", err)
		}

		bundle, loadedPath, err := loadDelegatedCredentialsForRequest("", "rw_7ccfea89", defaultJoinScopes)
		if err != nil {
			t.Fatalf("loadDelegatedCredentialsForRequest failed: %v", err)
		}
		if loadedPath != resolvedPath {
			t.Fatalf("loadedPath = %q, want healed resolve path %q", loadedPath, resolvedPath)
		}
		if bundle.BearerToken() == "" {
			t.Fatal("expected delegated bundle from minted shard")
		}
		if _, err := delegatedauth.Load(resolvedPath); err != nil {
			t.Fatalf("expected healed delegated credentials at resolved path: %v", err)
		}

		if err := os.Remove(mintedPath); err != nil {
			t.Fatalf("remove minted path failed: %v", err)
		}
		_, loadedPath, err = loadDelegatedCredentialsForRequest("", "rw_7ccfea89", defaultJoinScopes)
		if err != nil {
			t.Fatalf("second loadDelegatedCredentialsForRequest failed: %v", err)
		}
		if loadedPath != resolvedPath {
			t.Fatalf("second loadedPath = %q, want healed resolve path %q", loadedPath, resolvedPath)
		}
	}
	t.Run("polluted catalog order", func(t *testing.T) {
		run(t, records)
	})
	t.Run("reversed polluted catalog order", func(t *testing.T) {
		reversed := append([]workspaceRecord(nil), records...)
		for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
			reversed[i], reversed[j] = reversed[j], reversed[i]
		}
		run(t, reversed)
	})
}

func TestWritebackSkipStuckUsesDelegatedCredentialsWithoutLegacyCredentials(t *testing.T) {
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
		Scopes:     append([]string(nil), defaultJoinScopes...),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/workspaces/ws_demo/fs/export":
			_, _ = w.Write([]byte(`[]`))
		case "/v1/workspaces/ws_demo/fs/events":
			_, _ = w.Write([]byte(`{"events":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	if err := delegatedauth.SaveAtomic(delegatedCredentialsPathForRequest("ws_demo", defaultJoinScopes), delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          testJWTWithWorkspaceAndScopes("ws_demo", "relayfile-cli", defaultJoinScopes),
		RefreshToken:         "refresh_join",
		RelayauthURL:         server.URL,
		Scopes:               append([]string(nil), defaultJoinScopes...),
	}); err != nil {
		t.Fatalf("save delegated credentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"writeback", "skip-stuck", "demo", "--json"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("writeback skip-stuck failed without legacy credentials: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), `"workspace": "ws_demo"`) {
		t.Fatalf("unexpected skip-stuck output: %s", stdout.String())
	}
}

func TestWritebackPushPostsBulkAndWritesAckedReceipt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspaceID = "ws_demo"
	const remotePath = "/linear/issues/factory-create-ar-272-test.json"
	const opID = "op_ar_272"
	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/linear/issues",
		Mode:        defaultMountMode,
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         workspaceID,
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	localPath := filepath.Join(localDir, "factory-create-ar-272-test.json")
	content := []byte(`{"title":"AR-272 synthetic test"}` + "\n")
	if err := os.WriteFile(localPath, content, 0o644); err != nil {
		t.Fatalf("write local payload failed: %v", err)
	}

	if err := saveLegacyCloudCredentials(cloudCredentials{
		APIURL:      "https://legacy-cloud-credentials.invalid",
		AccessToken: "legacy_token_must_not_be_used",
	}); err != nil {
		t.Fatalf("save legacy cloud credentials failed: %v", err)
	}

	var sawDelegated atomic.Bool
	var sawBulk atomic.Bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces/"+workspaceID+"/relayfile/delegated-token":
			sawDelegated.Store(true)
			if got := r.Header.Get("Authorization"); got != "Bearer cld_access" {
				t.Fatalf("unexpected delegated-token Authorization: %q", got)
			}
			var body cloudRelayfileDelegatedTokenRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode delegated-token body failed: %v", err)
			}
			if body.AgentName != "relayfile-cli" {
				t.Fatalf("agentName = %q, want relayfile-cli", body.AgentName)
			}
			if got, want := strings.Join(body.Scopes, ","), "fs:write:/linear/**,ops:read"; got != want {
				t.Fatalf("delegated-token scopes = %q, want %q", got, want)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"relayfileUrl":                   server.URL,
				"relayauthUrl":                   server.URL,
				"relayfileWorkspaceId":           workspaceID,
				"relayfileToken":                 "rf_write",
				"relayfileTokenExpiresAt":        time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				"relayfileRefreshToken":          "refresh_write",
				"relayfileRefreshTokenExpiresAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
				"relayfileScopes":                []string{"relayfile:fs:write:/linear/**"},
				"delegationNotAfter":             time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
				"relayfileMountPaths":            []string{"/linear/**"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspaces/"+workspaceID+"/fs/bulk":
			sawBulk.Store(true)
			if got := r.Header.Get("Authorization"); got != "Bearer rf_write" {
				t.Fatalf("unexpected bulk Authorization: %q", got)
			}
			var req bulkWriteRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode bulk request failed: %v", err)
			}
			if len(req.Files) != 1 {
				t.Fatalf("bulk files = %d, want 1", len(req.Files))
			}
			file := req.Files[0]
			if file.Path != remotePath {
				t.Fatalf("bulk path = %q, want %q", file.Path, remotePath)
			}
			if file.Content != string(content) {
				t.Fatalf("bulk content = %q, want %q", file.Content, string(content))
			}
			if file.ContentIdentity == nil {
				t.Fatal("expected content identity for factory-create writeback")
			}
			if file.ContentIdentity.Kind != "mount-writeback-create-draft" {
				t.Fatalf("content identity kind = %q", file.ContentIdentity.Kind)
			}
			if !strings.HasPrefix(file.ContentIdentity.Key, workspaceID+":"+remotePath+":") {
				t.Fatalf("content identity key = %q", file.ContentIdentity.Key)
			}
			_, _ = io.WriteString(w, `{"written":1,"errorCount":0,"correlationId":"corr_ar_272","results":[{"path":"`+remotePath+`","revision":"rev_1","opId":"`+opID+`","writeback":{"provider":"linear","state":"succeeded"}}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspaces/"+workspaceID+"/ops/"+opID:
			if got := r.Header.Get("Authorization"); got != "Bearer rf_write" {
				t.Fatalf("unexpected op Authorization: %q", got)
			}
			_, _ = io.WriteString(w, `{"opId":"`+opID+`","path":"`+remotePath+`","status":"succeeded","revision":"rev_1"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	installFakeAgentRelaySession(t, server.URL, "cld_access", "demo", workspaceID, workspaceID)

	var stdout bytes.Buffer
	if err := run([]string{"writeback", "push", localPath, "--workspace", "demo", "--timeout", "1s"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("writeback push failed: %v\n%s", err, stdout.String())
	}
	if !sawDelegated.Load() {
		t.Fatal("expected delegated-token request via agent-relay cloud session")
	}
	if !sawBulk.Load() {
		t.Fatal("expected bulk write request")
	}
	if got := stdout.String(); !strings.Contains(got, "Pushed ") || !strings.Contains(got, opID) {
		t.Fatalf("unexpected stdout: %s", got)
	}
	if got := countJSONFiles(filepath.Join(localDir, ".relay", "outbox", "pending")); got != 0 {
		t.Fatalf("pending receipt count = %d, want 0", got)
	}
	ackedDir := filepath.Join(localDir, ".relay", "outbox", "acked")
	entries, err := os.ReadDir(ackedDir)
	if err != nil {
		t.Fatalf("read acked dir failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("acked receipt count = %d, want 1", len(entries))
	}
	payload, err := os.ReadFile(filepath.Join(ackedDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read acked receipt failed: %v", err)
	}
	var receipt writebackPushReceipt
	if err := json.Unmarshal(payload, &receipt); err != nil {
		t.Fatalf("decode acked receipt failed: %v", err)
	}
	if receipt.Status != "acked" || receipt.OpID != opID || receipt.RemotePath != remotePath || receipt.Revision != "rev_1" {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	// Terminal receipts must not retain the user's file body on disk.
	if receipt.Content != "" || receipt.Encoding != "" {
		t.Fatalf("acked receipt must redact content/encoding, got content=%q encoding=%q", receipt.Content, receipt.Encoding)
	}
	// The acked JSON printed to stdout must likewise omit the body.
	if strings.Contains(stdout.String(), "AR-272 synthetic test") {
		t.Fatalf("stdout leaked file content: %s", stdout.String())
	}
}

func TestWritebackPushCanonicalPathPostsBulkWithoutContentIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspaceID = "ws_demo"
	const remotePath = "/linear/issues/AR-272__00000000-0000-0000-0000-000000000272.json"
	const opID = "op_update_272"
	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/linear/issues",
		Mode:        defaultMountMode,
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}

	localPath := filepath.Join(localDir, "AR-272__00000000-0000-0000-0000-000000000272.json")
	content := []byte(`{"title":"updated title","stateId":"state_1"}` + "\n")
	if err := os.WriteFile(localPath, content, 0o644); err != nil {
		t.Fatalf("write local payload failed: %v", err)
	}

	var sawBulk atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspaces/"+workspaceID+"/fs/bulk":
			sawBulk.Store(true)
			if got := r.Header.Get("Authorization"); got != "Bearer rf_write" {
				t.Fatalf("unexpected bulk Authorization: %q", got)
			}
			var req bulkWriteRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode bulk request failed: %v", err)
			}
			if len(req.Files) != 1 {
				t.Fatalf("bulk files = %d, want 1", len(req.Files))
			}
			file := req.Files[0]
			if file.Path != remotePath {
				t.Fatalf("bulk path = %q, want %q", file.Path, remotePath)
			}
			if file.Content != string(content) {
				t.Fatalf("bulk content = %q, want %q", file.Content, string(content))
			}
			if file.ContentIdentity != nil {
				t.Fatalf("canonical update must not carry draft content identity: %+v", file.ContentIdentity)
			}
			_, _ = io.WriteString(w, `{"written":1,"errorCount":0,"correlationId":"corr_update_272","results":[{"path":"`+remotePath+`","revision":"rev_2","opId":"`+opID+`","writeback":{"provider":"linear","state":"pending"}}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspaces/"+workspaceID+"/ops/"+opID:
			_, _ = io.WriteString(w, `{"opId":"`+opID+`","path":"`+remotePath+`","status":"succeeded","revision":"rev_2"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run([]string{"writeback", "push", localPath, "--server", server.URL, "--token", "rf_write", "--timeout", "1s"}, strings.NewReader(""), &stdout, &stdout)
	if err != nil {
		t.Fatalf("writeback push canonical failed: %v\n%s", err, stdout.String())
	}
	if !sawBulk.Load() {
		t.Fatal("expected bulk write request")
	}
	if got := stdout.String(); !strings.Contains(got, "Pushed ") || !strings.Contains(got, opID) {
		t.Fatalf("unexpected stdout: %s", got)
	}
}

func TestWritebackUpdateCanonicalPathAllowsMissingOperationAndSendsIntent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspaceID = "ws_demo"
	const remotePath = "/linear/issues/AR-292__00000000-0000-0000-0000-000000000292.json"
	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/linear/issues",
		Mode:        defaultMountMode,
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}

	localPath := filepath.Join(localDir, "AR-292__00000000-0000-0000-0000-000000000292.json")
	content := []byte(`{"labels":[{"name":"harness"}]}` + "\n")
	if err := os.WriteFile(localPath, content, 0o644); err != nil {
		t.Fatalf("write local payload failed: %v", err)
	}

	var sawBulk atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspaces/"+workspaceID+"/fs/bulk":
			sawBulk.Store(true)
			var req bulkWriteRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode bulk request failed: %v", err)
			}
			if len(req.Files) != 1 {
				t.Fatalf("bulk files = %d, want 1", len(req.Files))
			}
			file := req.Files[0]
			if file.Path != remotePath {
				t.Fatalf("bulk path = %q, want %q", file.Path, remotePath)
			}
			if file.Content != string(content) {
				t.Fatalf("bulk content = %q, want %q", file.Content, string(content))
			}
			if file.ContentIdentity != nil {
				t.Fatalf("canonical update must not carry draft content identity: %+v", file.ContentIdentity)
			}
			if file.WritebackIntent != "update" {
				t.Fatalf("writebackIntent = %q, want update", file.WritebackIntent)
			}
			_, _ = io.WriteString(w, `{"written":1,"errorCount":0,"correlationId":"corr_update_292","results":[{"path":"`+remotePath+`","revision":"rev_292","writeback":{"provider":"linear","state":"succeeded"}}]}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run([]string{"writeback", "update", localPath, "--server", server.URL, "--token", "rf_write", "--timeout", "1s"}, strings.NewReader(""), &stdout, &stdout)
	if err != nil {
		t.Fatalf("writeback update failed: %v\n%s", err, stdout.String())
	}
	if !sawBulk.Load() {
		t.Fatal("expected bulk write request")
	}
	if got := stdout.String(); !strings.Contains(got, "Updated ") || !strings.Contains(got, " -> "+remotePath) {
		t.Fatalf("unexpected stdout: %s", got)
	}
	if got := countJSONFiles(filepath.Join(localDir, ".relay", "outbox", "failed")); got != 0 {
		t.Fatalf("failed receipt count = %d, want 0", got)
	}
	if got := countJSONFiles(filepath.Join(localDir, ".relay", "outbox", "acked")); got != 1 {
		t.Fatalf("acked receipt count = %d, want 1", got)
	}
	ackedDir := filepath.Join(localDir, ".relay", "outbox", "acked")
	entries, err := os.ReadDir(ackedDir)
	if err != nil {
		t.Fatalf("read acked dir failed: %v", err)
	}
	payload, err := os.ReadFile(filepath.Join(ackedDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read acked receipt failed: %v", err)
	}
	var receipt writebackPushReceipt
	if err := json.Unmarshal(payload, &receipt); err != nil {
		t.Fatalf("decode acked receipt failed: %v", err)
	}
	if receipt.Status != "acked" || receipt.OpID != "" || receipt.DispatchStatus != "succeeded" || receipt.Revision != "rev_292" {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
}

func TestWritebackUpdateMissingOperationIDPendingStateNeedsAttention(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspaceID = "ws_demo"
	const remotePath = "/linear/issues/AR-295__00000000-0000-0000-0000-000000000295.json"
	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/linear/issues",
		Mode:        defaultMountMode,
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}

	localPath := filepath.Join(localDir, "AR-295__00000000-0000-0000-0000-000000000295.json")
	content := []byte(`{"priority":2}` + "\n")
	if err := os.WriteFile(localPath, content, 0o644); err != nil {
		t.Fatalf("write local payload failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspaces/"+workspaceID+"/fs/bulk":
			var req bulkWriteRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode bulk request failed: %v", err)
			}
			if len(req.Files) != 1 || req.Files[0].WritebackIntent != "update" {
				t.Fatalf("unexpected bulk request: %+v", req.Files)
			}
			_, _ = io.WriteString(w, `{"written":1,"errorCount":0,"correlationId":"corr_update_295","results":[{"path":"`+remotePath+`","revision":"rev_295","writeback":{"provider":"linear","state":"pending"}}]}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run([]string{"writeback", "update", localPath, "--server", server.URL, "--token", "rf_write", "--timeout", "1s"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatal("expected missing operation id tracking error")
	}
	if got := err.Error(); !strings.Contains(got, "cannot be tracked") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := countJSONFiles(filepath.Join(localDir, ".relay", "outbox", "failed")); got != 0 {
		t.Fatalf("failed receipt count = %d, want 0", got)
	}
	if got := countJSONFiles(filepath.Join(localDir, ".relay", "outbox", "acked")); got != 0 {
		t.Fatalf("acked receipt count = %d, want 0", got)
	}
	pendingDir := filepath.Join(localDir, ".relay", "outbox", "pending")
	entries, err := os.ReadDir(pendingDir)
	if err != nil {
		t.Fatalf("read pending dir failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("pending receipt count = %d, want 1", len(entries))
	}
	payload, err := os.ReadFile(filepath.Join(pendingDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read pending receipt failed: %v", err)
	}
	var receipt writebackPushReceipt
	if err := json.Unmarshal(payload, &receipt); err != nil {
		t.Fatalf("decode pending receipt failed: %v", err)
	}
	if receipt.Status != "pending" || receipt.OpID != "" || receipt.DispatchStatus != "pending" || !receipt.NeedsAttention {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
}

func TestWritebackUpdateRejectsDraftPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspaceID = "ws_demo"
	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/linear/issues",
		Mode:        defaultMountMode,
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}
	localPath := filepath.Join(localDir, "factory-create-ar-272-test.json")
	if err := os.WriteFile(localPath, []byte(`{"title":"draft"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write local payload failed: %v", err)
	}

	var stdout bytes.Buffer
	err := run([]string{"writeback", "update", localPath, "--token", "rf_write"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatal("expected draft path rejection")
	}
	if got := err.Error(); !strings.Contains(got, "requires a canonical provider record path") {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, state := range []string{"pending", "acked", "failed"} {
		if got := countJSONFiles(filepath.Join(localDir, ".relay", "outbox", state)); got != 0 {
			t.Fatalf("%s receipt count = %d, want 0", state, got)
		}
	}
}

func TestWritebackDeleteCallsFilesystemDeleteAndPollsOperation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspaceID = "ws_demo"
	const remotePath = "/linear/issues/AR-272__00000000-0000-0000-0000-000000000272.json"
	const opID = "op_delete_272"
	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/linear/issues",
		Mode:        defaultMountMode,
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}
	localPath := filepath.Join(localDir, "AR-272__00000000-0000-0000-0000-000000000272.json")
	if err := os.WriteFile(localPath, []byte(`{"title":"delete me"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write local payload failed: %v", err)
	}

	var sawDelete atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/workspaces/"+workspaceID+"/fs/file":
			sawDelete.Store(true)
			if got := r.Header.Get("If-Match"); got != "*" {
				t.Fatalf("If-Match = %q, want *", got)
			}
			if got := r.URL.Query().Get("path"); got != remotePath {
				t.Fatalf("delete path = %q, want %q", got, remotePath)
			}
			_, _ = io.WriteString(w, `{"opId":"`+opID+`","status":"queued","targetRevision":"rev_3","writeback":{"provider":"linear","state":"pending"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspaces/"+workspaceID+"/ops/"+opID:
			_, _ = io.WriteString(w, `{"opId":"`+opID+`","path":"`+remotePath+`","status":"succeeded","revision":"rev_3"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := run([]string{"writeback", "delete", localPath, "--server", server.URL, "--token", "rf_write", "--timeout", "1s"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("writeback delete failed: %v\n%s", err, stdout.String())
	}
	if !sawDelete.Load() {
		t.Fatal("expected delete request")
	}
	if got := stdout.String(); !strings.Contains(got, "Deleted "+remotePath) || !strings.Contains(got, opID) {
		t.Fatalf("unexpected stdout: %s", got)
	}
}

func TestWritebackDeleteRequiresOperationID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspaceID = "ws_demo"
	const remotePath = "/linear/issues/AR-292__00000000-0000-0000-0000-000000000292.json"
	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/linear/issues",
		Mode:        defaultMountMode,
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}
	localPath := filepath.Join(localDir, "AR-292__00000000-0000-0000-0000-000000000292.json")
	if err := os.WriteFile(localPath, []byte(`{"title":"delete me"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write local payload failed: %v", err)
	}

	var sawDelete atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/workspaces/"+workspaceID+"/fs/file":
			sawDelete.Store(true)
			_, _ = io.WriteString(w, `{"status":"queued","targetRevision":"rev_delete_missing_op","writeback":{"provider":"linear","state":"pending"}}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run([]string{"writeback", "delete", localPath, "--server", server.URL, "--token", "rf_write", "--timeout", "1s"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatal("expected missing operation id error")
	}
	if got := err.Error(); !strings.Contains(got, "writeback delete did not return an operation id") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sawDelete.Load() {
		t.Fatal("expected delete request")
	}
	if got := countJSONFiles(filepath.Join(localDir, ".relay", "outbox", "failed")); got != 1 {
		t.Fatalf("failed receipt count = %d, want 1", got)
	}
	if got := countJSONFiles(filepath.Join(localDir, ".relay", "outbox", "acked")); got != 0 {
		t.Fatalf("acked receipt count = %d, want 0", got)
	}
}

func TestWritebackUpdateFailsWhenOperationFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspaceID = "ws_demo"
	const remotePath = "/linear/issues/AR-272__00000000-0000-0000-0000-000000000272.json"
	const opID = "op_failed_272"
	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/linear/issues",
		Mode:        defaultMountMode,
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}
	localPath := filepath.Join(localDir, "AR-272__00000000-0000-0000-0000-000000000272.json")
	if err := os.WriteFile(localPath, []byte(`{"stateId":"missing"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write local payload failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspaces/"+workspaceID+"/fs/bulk":
			_, _ = io.WriteString(w, `{"written":1,"errorCount":0,"correlationId":"corr_failed_272","results":[{"path":"`+remotePath+`","revision":"rev_4","opId":"`+opID+`","writeback":{"provider":"linear","state":"pending"}}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspaces/"+workspaceID+"/ops/"+opID:
			_, _ = io.WriteString(w, `{"opId":"`+opID+`","path":"`+remotePath+`","status":"failed","revision":"rev_4","lastError":"ADAPTER_ERROR: not found"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run([]string{"writeback", "update", localPath, "--server", server.URL, "--token", "rf_write", "--timeout", "1s"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatal("expected failed operation error")
	}
	if got := err.Error(); !strings.Contains(got, "writeback operation "+opID+" failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := countJSONFiles(filepath.Join(localDir, ".relay", "outbox", "failed")); got != 1 {
		t.Fatalf("failed receipt count = %d, want 1", got)
	}
	if got := countJSONFiles(filepath.Join(localDir, ".relay", "outbox", "acked")); got != 0 {
		t.Fatalf("acked receipt count = %d, want 0", got)
	}
}

func TestWorkspaceStatusReportsLocalMountHealth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID:               "ws_demo",
		RemoteRoot:                "/linear/issues",
		Mode:                      defaultMountMode,
		Status:                    "syncing",
		LastSuccessfulReconcileAt: "2026-06-11T15:14:48Z",
		LastReconcileAt:           "2026-06-15T10:00:00Z",
		LastError:                 &statusError{Message: "changed event not readable yet"},
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}
	mountState := map[string]any{
		"incrementalReadNotReadySince": map[string]string{
			"/linear/issues/by-state/ready-for-agent/AR-1.json": "2026-06-15T09:00:00Z",
			"/linear/issues/by-state/ready-for-agent/AR-2.json": "2026-06-15T09:01:00Z",
		},
		"incrementalBacklogDraining": true,
	}
	mountStatePayload, err := json.Marshal(mountState)
	if err != nil {
		t.Fatalf("marshal mount state failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, ".relayfile-mount-state.json"), mountStatePayload, 0o644); err != nil {
		t.Fatalf("write mount state failed: %v", err)
	}
	for _, path := range []string{
		filepath.Join(localDir, ".relay", "outbox", "pending", "mountcmd_pending.json"),
		filepath.Join(localDir, ".relay", "outbox", "failed", "mountcmd_failed.json"),
		filepath.Join(localDir, ".relay", "outbox", "acked", "mountcmd_acked.json"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s failed: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write %s failed: %v", path, err)
		}
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
	if err := run([]string{"workspace", "status", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("workspace status failed: %v\n%s", err, stdout.String())
	}
	output := stdout.String()
	for _, want := range []string{
		"workspace: ws_demo (demo)",
		"status: syncing",
		"last successful reconcile: 2026-06-11T15:14:48Z",
		"stuck events: 2",
		"outbox: pending=1 failed=1 acked=1",
		"last error: changed event not readable yet",
		"incremental backlog: draining",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("workspace status output missing %q:\n%s", want, output)
		}
	}
}

func TestStatusSurfacesDegradedStallReason(t *testing.T) {
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
	persisted := syncStateFile{
		WorkspaceID: "ws_demo",
		Mode:        defaultMountMode,
		StallReason: "cloud session expired — run 'agent-relay login' to refresh",
	}
	if err := writeMirrorStateFile(localDir, persisted); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[{"provider":"notion","status":"ready","lagSeconds":4,"watermarkTs":"2026-05-02T18:00:00Z"}]}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "delegated_token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "stall: cloud session expired") {
		t.Fatalf("expected degraded stall reason in status output, got: %q", got)
	}
	if !strings.Contains(got, "agent-relay login") {
		t.Fatalf("expected recovery hint in status output, got: %q", got)
	}
}

func TestEnsureMirrorLayoutDoesNotRewriteUnchangedSkill(t *testing.T) {
	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	skillPath := filepath.Join(localDir, ".skills", "activity-summary.md")
	before, err := os.Stat(skillPath)
	if err != nil {
		t.Fatalf("stat skill before second layout failed: %v", err)
	}
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("second ensureMirrorLayout failed: %v", err)
	}
	after, err := os.Stat(skillPath)
	if err != nil {
		t.Fatalf("stat skill after second layout failed: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatalf("expected unchanged skill file not to be atomically replaced")
	}
}

func TestBuildSyncStateSnapshotPreservesRemoteRoot(t *testing.T) {
	localDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localDir, ".relay"), 0o755); err != nil {
		t.Fatalf("mkdir .relay failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, ".relay", "state.json"), []byte(`{"remoteRoot":"/notion"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write state failed: %v", err)
	}

	snapshot := buildSyncStateSnapshot(syncStatusResponse{}, "ws_demo", defaultMountMode, defaultMountInterval, localDir, 0, "")
	if snapshot.RemoteRoot != "/notion" {
		t.Fatalf("expected remoteRoot to be preserved, got %q", snapshot.RemoteRoot)
	}
}

func TestReadGuardCountersAcceptsMirrorStateGuardsShape(t *testing.T) {
	localDir := t.TempDir()
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID: "ws_demo",
		Mode:        defaultMountMode,
		Guards: &syncStateGuards{
			SkippedOversizeWriteback: 3,
			SnapshotDeleteBlocked:    2,
			PathCollisionQuarantined: 5,
			LastAppliedRevision:      "rev_9",
			Circuit: &syncStateGuardCirc{
				Open:       true,
				OpenEvents: 1,
				Failures:   4,
			},
		},
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}

	got := readGuardCounters(localDir)
	if got == nil {
		t.Fatalf("expected guard counters")
	}
	if got.SkippedOversizeWriteback != 3 || got.SnapshotDeleteBlocked != 2 || got.PathCollisionQuarantined != 5 || got.LastAppliedRevision != "rev_9" {
		t.Fatalf("unexpected guard counters: %#v", got)
	}
	if got.Circuit == nil || !got.Circuit.Open || got.Circuit.OpenEvents != 1 || got.Circuit.Failures != 4 {
		t.Fatalf("unexpected circuit counters: %#v", got.Circuit)
	}
}

func TestReadGuardCountersAcceptsMountsyncCountersShape(t *testing.T) {
	localDir := t.TempDir()
	stateDir := filepath.Join(localDir, ".relay")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir .relay failed: %v", err)
	}
	payload := []byte(`{"counters":{"pathCollisionQuarantined":7}}` + "\n")
	if err := os.WriteFile(filepath.Join(stateDir, "state.json"), payload, 0o644); err != nil {
		t.Fatalf("write state failed: %v", err)
	}

	got := readGuardCounters(localDir)
	if got == nil {
		t.Fatalf("expected guard counters")
	}
	if got.PathCollisionQuarantined != 7 {
		t.Fatalf("expected path collision counter, got %#v", got)
	}
}

func TestStatusRendersWebhookUnhealthyWarning(t *testing.T) {
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/workspaces/ws_demo/sync/status" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[{"provider":"notion","status":"lagging","lagSeconds":80,"watermarkTs":"2026-05-02T18:00:00Z","webhookHealthy":false}]}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "delegated_token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "notion webhook unhealthy") {
		t.Fatalf("expected webhook-unhealthy warning row, got: %q", got)
	}
	if !strings.Contains(got, "lag 1m20s") {
		t.Fatalf("expected lag in warning row, got: %q", got)
	}
}

func TestStatusExplainsLaggingProviderWithoutWatermark(t *testing.T) {
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

	var ingressCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/workspaces/ws_demo/sync/status":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[{"provider":"github","status":"lagging","cursor":"","watermarkTs":" ","lagSeconds":0}]}`))
		case "/v1/workspaces/ws_demo/sync/ingress":
			ingressCalls.Add(1)
			_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","queueDepth":0,"pendingTotal":0,"deadLetterTotal":0,"acceptedTotal":55,"ingressByProvider":{"linear":{"acceptedTotal":55,"pendingTotal":0}}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "delegated_token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "github       lagging  lag 0s") {
		t.Fatalf("expected lagging provider row, got: %q", got)
	}
	if !strings.Contains(got, "reason: no sync cursor or watermark; no provider-specific ingress events recorded") {
		t.Fatalf("expected lag reason in status output, got: %q", got)
	}
	if ingressCalls.Load() != 1 {
		t.Fatalf("expected status command to request /sync/ingress once, got %d", ingressCalls.Load())
	}
}

func TestSyncProviderLagReasonUsesAliasesAndObservedCounters(t *testing.T) {
	blank := ""
	provider := syncProviderStatus{
		Provider:    "slack-sage",
		Status:      "lagging",
		Cursor:      &blank,
		WatermarkTs: &blank,
	}
	ingress := &syncIngressStatusResponse{
		IngressByProvider: map[string]syncIngressProviderStatus{
			"slack": {DedupedTotal: 2, CoalescedTotal: 1},
		},
	}

	got := syncProviderLagReason(provider, ingress)
	if !strings.Contains(got, "3 ingress event(s) observed") {
		t.Fatalf("expected alias ingress observation reason, got %q", got)
	}
}

func TestStatusOmitsWebhookWarningWhenHealthy(t *testing.T) {
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[{"provider":"notion","status":"ready","lagSeconds":4,"watermarkTs":"2026-05-02T18:00:00Z","webhookHealthy":true}]}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "delegated_token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
	}
	if got := stdout.String(); strings.Contains(got, "webhook unhealthy") {
		t.Fatalf("did not expect webhook-unhealthy warning, got: %q", got)
	}
}

func TestOpsListReadsLocalDeadLetterMirror(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	scopedLocalDir := filepath.Join(localDir, "notion")
	dlDir := filepath.Join(scopedLocalDir, ".relay", "dead-letter")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		t.Fatalf("mkdir dead-letter failed: %v", err)
	}
	record := `{"opId":"op_abc","path":"/notion/page.json","code":"validation_error","message":"body must include event","attempts":3,"lastAttemptedAt":"2026-05-02T18:05:00Z","replayUrl":"https://cloud.test/api/v1/workspaces/ws_demo/ops/op_abc/replay"}`
	if err := os.WriteFile(filepath.Join(dlDir, "op_abc.json"), []byte(record), 0o644); err != nil {
		t.Fatalf("write dead-letter record failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:        "demo",
		ID:          "ws_demo",
		LocalDir:    localDir,
		RemotePaths: []string{"/notion"},
		LocalLayout: "scoped",
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"ops", "list", "--workspace", "demo", "--no-refresh"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run ops list failed: %v", err)
	}
	got := stdout.String()
	for _, fragment := range []string{"op_abc", "/notion/page.json", "validation_error", "2026-05-02T18:05:00Z"} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("expected %q in ops list output, got %q", fragment, got)
		}
	}
}

func TestScopedWorkspaceConsumersAggregateChildRuntimeState(t *testing.T) {
	localRoot := t.TempDir()
	record := workspaceRecord{
		ID:            "ws_demo",
		LocalDir:      localRoot,
		LocalLayout:   "scoped",
		RemotePaths:   []string{"/github", "/slack"},
		MountStateDir: t.TempDir(),
		MountKind:     mountsync.MountKindDaemon,
	}
	scopes := workspaceMountScopes(record)
	if len(scopes) != 2 {
		t.Fatalf("workspaceMountScopes() = %#v, want 2 scopes", scopes)
	}
	for index, scope := range scopes {
		if err := os.MkdirAll(filepath.Join(scope.LocalDir, ".relay", "dead-letter"), 0o755); err != nil {
			t.Fatal(err)
		}
		state := fmt.Sprintf(
			`{"status":"scope-%d","lastReconcileAt":"2026-07-30T03:0%d:00Z","pendingWriteback":99,"failedWritebacks":%d,"stallReason":"scope-%d stalled","incrementalReadNotReadySince":{"event":"now"},"bootstrap":{"phase":"pull","filesSynced":%d,"filesTotal":10}}`,
			index+1,
			index+1,
			index+3,
			index+1,
			index+2,
		)
		if err := os.WriteFile(filepath.Join(scope.LocalDir, ".relay", "state.json"), []byte(state), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(scope.LocalDir, ".relay", "dead-letter", fmt.Sprintf("op_%d.json", index+1)),
			[]byte(fmt.Sprintf(`{"opId":"op_%d","path":"%s/file.md"}`, index+1, scope.RemotePath)),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		stateFile, err := workspaceMountStateFile("ws_demo", record, scope)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
			t.Fatal(err)
		}
		pendingField := `"dirty":true`
		if index == 1 {
			pendingField = `"deletePending":true`
		}
		privateState := fmt.Sprintf(
			`{"files":{"%s/pending.md":{%s}},"incrementalReadNotReadySince":{"private":"now"}}`,
			scope.RemotePath,
			pendingField,
		)
		if err := os.WriteFile(stateFile, []byte(privateState), 0o644); err != nil {
			t.Fatal(err)
		}
		conflictDir := filepath.Join(scope.LocalDir, ".relay", "conflicts")
		if err := os.MkdirAll(conflictDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(conflictDir, "conflict.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(scope.LocalDir, ".relay", "permissions-denied.log"), []byte("one\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		outboxDir := filepath.Join(scope.LocalDir, ".relay", "outbox", "pending")
		if err := os.MkdirAll(outboxDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outboxDir, "pending.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for state, name := range map[string]string{
		"pending": "compat-pending.json",
		"failed":  "compat-failed.json",
		"acked":   "compat-acked.json",
	} {
		dir := filepath.Join(localRoot, ".relay", "outbox", state)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeback, err := buildWritebackStatusReport("ws_demo", record)
	if err != nil {
		t.Fatal(err)
	}
	if writeback.Pending != 2 || writeback.Failed != 7 || len(writeback.DeadLettered) != 2 {
		t.Fatalf("scoped writeback report = %#v, want private pending=2 failed=7 dead-lettered=2", writeback)
	}

	snapshot := buildWorkspaceSyncStateSnapshot(syncStatusResponse{}, "ws_demo", record)
	if snapshot.PendingWriteback != 2 || snapshot.PendingConflicts != 2 || snapshot.DeniedPaths != 2 {
		t.Fatalf("scoped sync snapshot = %#v, want pending/conflicts/denied = 2/2/2", snapshot)
	}
	if snapshot.FailedWritebacks != 7 {
		t.Fatalf("failed writebacks = %d, want 7", snapshot.FailedWritebacks)
	}
	if snapshot.Bootstrap == nil || snapshot.Bootstrap.FilesSynced != 5 || snapshot.Bootstrap.FilesTotal != 20 {
		t.Fatalf("aggregated bootstrap = %#v, want files 5/20", snapshot.Bootstrap)
	}
	if got := snapshot.StallReason; got != "scope-1 stalled; scope-2 stalled" {
		t.Fatalf("aggregated stall reason = %q", got)
	}

	health := buildWorkspaceHealthReport("ws_demo", record)
	if health.Status != "scope-1; scope-2" ||
		health.StuckEventCount != 2 ||
		health.OutboxPending != 3 ||
		health.OutboxFailed != 1 ||
		health.OutboxAcked != 1 {
		t.Fatalf("scoped health report = %#v, want child states and child/catalog queues", health)
	}
	if health.LastReconcileAt != "2026-07-30T03:02:00Z" {
		t.Fatalf("last reconcile = %q, want newest child timestamp", health.LastReconcileAt)
	}

	compatibilityDir := filepath.Join(localRoot, ".relay", "dead-letter")
	if err := os.MkdirAll(compatibilityDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(compatibilityDir, "op_2.json"),
		[]byte(`{"opId":"op_2","path":"/github/stale.md"}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	path, localDir, _, err := findWorkspaceDeadLetterRecord(record, "op_2")
	if err != nil {
		t.Fatal(err)
	}
	if localDir != scopes[1].LocalDir || path != filepath.Join(scopes[1].LocalDir, ".relay", "dead-letter", "op_2.json") {
		t.Fatalf("scoped retry lookup = %q in %q, want authoritative second scope instead of catalog duplicate", path, localDir)
	}
}

func TestWorkspaceMountScopesRecoversLegacyExactRuntimeRoot(t *testing.T) {
	localDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localDir, ".relay"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(localDir, ".relay", "state.json"),
		[]byte(`{"remoteRoot":"/github"}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	record := workspaceRecord{LocalDir: localDir, RemotePaths: []string{"/slack"}}
	scopes := workspaceMountScopes(record)
	if len(scopes) != 1 || scopes[0].RemotePath != "/github" || scopes[0].LocalDir != localDir {
		t.Fatalf("legacy exact scopes = %#v, want persisted runtime root /github", scopes)
	}
	remoteRoot, err := workspaceRemoteRootForLocalDir(record, localDir)
	if err != nil {
		t.Fatal(err)
	}
	if remoteRoot != "/github" {
		t.Fatalf("legacy exact retry root = %q, want persisted runtime root /github", remoteRoot)
	}
}

func TestWorkspaceRetryTargetRoutesCatalogCompatibilityRecordToScopedChild(t *testing.T) {
	localRoot := t.TempDir()
	record := workspaceRecord{
		LocalDir:    localRoot,
		LocalLayout: mountscope.LayoutScoped,
		RemotePaths: []string{"/github", "/slack/channels/project"},
	}
	localDir, remoteRoot, err := workspaceRetryTarget(record, localRoot, "/slack/channels/project/message.json")
	if err != nil {
		t.Fatal(err)
	}
	if want := mountscope.LocalDir(localRoot, "/slack/channels/project"); localDir != want || remoteRoot != "/slack/channels/project" {
		t.Fatalf("retry target = %q at %q, want %q at /slack/channels/project", localDir, remoteRoot, want)
	}
}

func TestWorkspaceStateDirForDeadLetterPathsKeepsCrossScopeAtCatalogRoot(t *testing.T) {
	localRoot := t.TempDir()
	record := workspaceRecord{
		LocalDir:    localRoot,
		LocalLayout: mountscope.LayoutScoped,
		RemotePaths: []string{"/github", "/slack"},
	}
	for _, rawPaths := range []string{
		"/github/repos/acme/issue.json,/slack/channels/project/message.json",
		"/slack/channels/project/message.json,/github/repos/acme/issue.json",
	} {
		if got := workspaceStateDirForDeadLetterPaths(record, rawPaths); got != localRoot {
			t.Fatalf("cross-scope dead-letter dir for %q = %q, want catalog root %q", rawPaths, got, localRoot)
		}
	}
	if got, want := workspaceStateDirForDeadLetterPaths(
		record,
		"/github/repos/acme/issue.json,/github/repos/acme/pr.json",
	), mountscope.LocalDir(localRoot, "/github"); got != want {
		t.Fatalf("single-scope dead-letter dir = %q, want %q", got, want)
	}
}

func TestWorkspaceDeadLetterRetryGroupsRoutesEveryScopedRoot(t *testing.T) {
	localRoot := t.TempDir()
	record := workspaceRecord{
		LocalDir:    localRoot,
		LocalLayout: mountscope.LayoutScoped,
		RemotePaths: []string{"/github", "/slack/channels/project"},
	}
	for _, remotePath := range []string{
		"/github/repos/acme/cloud/issue.json",
		"/github/repos/acme/cloud/pr.json",
		"/slack/channels/project/message.json",
	} {
		scope, ok := workspaceMountScopeForRemotePath(record, remotePath)
		if !ok {
			t.Fatalf("missing test scope for %s", remotePath)
		}
		relative := strings.TrimPrefix(remotePath, scope.RemotePath)
		localPath := filepath.Join(scope.LocalDir, filepath.FromSlash(strings.TrimPrefix(relative, "/")))
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(localPath, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name     string
		rawPaths string
		want     []workspaceDeadLetterRetryGroup
	}{
		{
			name:     "allowlist order",
			rawPaths: "/github/repos/acme/cloud/issue.json,/slack/channels/project/message.json",
			want: []workspaceDeadLetterRetryGroup{
				{
					LocalDir:   mountscope.LocalDir(localRoot, "/github"),
					RemoteRoot: "/github",
					Paths:      []string{"/github/repos/acme/cloud/issue.json"},
				},
				{
					LocalDir:   mountscope.LocalDir(localRoot, "/slack/channels/project"),
					RemoteRoot: "/slack/channels/project",
					Paths:      []string{"/slack/channels/project/message.json"},
				},
			},
		},
		{
			name:     "reverse order",
			rawPaths: "/slack/channels/project/message.json,/github/repos/acme/cloud/issue.json",
			want: []workspaceDeadLetterRetryGroup{
				{
					LocalDir:   mountscope.LocalDir(localRoot, "/slack/channels/project"),
					RemoteRoot: "/slack/channels/project",
					Paths:      []string{"/slack/channels/project/message.json"},
				},
				{
					LocalDir:   mountscope.LocalDir(localRoot, "/github"),
					RemoteRoot: "/github",
					Paths:      []string{"/github/repos/acme/cloud/issue.json"},
				},
			},
		},
		{
			name:     "same root stays one retry",
			rawPaths: "/github/repos/acme/cloud/issue.json,/github/repos/acme/cloud/pr.json",
			want: []workspaceDeadLetterRetryGroup{
				{
					LocalDir:   mountscope.LocalDir(localRoot, "/github"),
					RemoteRoot: "/github",
					Paths: []string{
						"/github/repos/acme/cloud/issue.json",
						"/github/repos/acme/cloud/pr.json",
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := workspaceDeadLetterRetryGroups(record, localRoot, tt.rawPaths)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("retry groups = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestWorkspaceDeadLetterRetryGroupsRejectsInvalidPathBeforeRetry(t *testing.T) {
	localRoot := t.TempDir()
	record := workspaceRecord{
		LocalDir:    localRoot,
		LocalLayout: mountscope.LayoutScoped,
		RemotePaths: []string{"/github", "/slack"},
	}
	githubPath := filepath.Join(mountscope.LocalDir(localRoot, "/github"), "repos", "acme", "cloud", "issue.json")
	if err := os.MkdirAll(filepath.Dir(githubPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(githubPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := workspaceDeadLetterRetryGroups(
		record,
		localRoot,
		"/github/repos/acme/cloud/issue.json,/linear/issues/ENG-123.json",
	)
	if err == nil || !strings.Contains(err.Error(), "outside the persisted scoped allowlist") {
		t.Fatalf("retry groups error = %v, want scoped allowlist refusal", err)
	}
}

func TestWorkspaceDeadLetterRetryGroupsIgnoreCompatibilityRecordStorageRoot(t *testing.T) {
	localRoot := t.TempDir()
	record := workspaceRecord{
		LocalDir:    localRoot,
		LocalLayout: mountscope.LayoutScoped,
		RemotePaths: []string{"/github", "/slack"},
	}
	for _, remotePath := range []string{
		"/github/repos/acme/cloud/issue.json",
		"/slack/channels/project/message.json",
	} {
		scope, ok := workspaceMountScopeForRemotePath(record, remotePath)
		if !ok {
			t.Fatalf("missing test scope for %s", remotePath)
		}
		relative := strings.TrimPrefix(strings.TrimPrefix(remotePath, scope.RemotePath), "/")
		localPath := filepath.Join(scope.LocalDir, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(localPath, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	githubChild := mountscope.LocalDir(localRoot, "/github")
	groups, err := workspaceDeadLetterRetryGroups(
		record,
		githubChild,
		"/github/repos/acme/cloud/issue.json,/slack/channels/project/message.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 ||
		groups[0].RemoteRoot != "/github" ||
		groups[1].RemoteRoot != "/slack" ||
		groups[1].LocalDir != mountscope.LocalDir(localRoot, "/slack") {
		t.Fatalf("retry groups from child-stored compatibility record = %#v, want both scoped roots", groups)
	}
}

func TestWorkspaceSyncStatusJSONMakesTopologyClaimsExclusive(t *testing.T) {
	snapshot := syncStateFile{
		WorkspaceID: "ws_demo",
		RemoteRoot:  "/github",
		Mode:        defaultMountMode,
	}
	tests := []struct {
		name       string
		record     workspaceRecord
		wantLayout string
		wantRoot   bool
		wantRoots  bool
	}{
		{
			name:       "exact",
			record:     workspaceRecord{LocalLayout: mountscope.LayoutExact, RemotePaths: []string{"/github"}},
			wantLayout: mountscope.LayoutExact,
			wantRoot:   true,
		},
		{
			name:       "scoped",
			record:     workspaceRecord{LocalLayout: mountscope.LayoutScoped, RemotePaths: []string{"/github", "/slack"}},
			wantLayout: mountscope.LayoutScoped,
			wantRoots:  true,
		},
		{
			name:       "legacy unknown",
			record:     workspaceRecord{},
			wantLayout: "unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := json.Marshal(workspaceSyncStatusJSON(snapshot, tt.record))
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(payload, &got); err != nil {
				t.Fatal(err)
			}
			if got["localLayout"] != tt.wantLayout {
				t.Fatalf("localLayout = %v, want %q; payload=%s", got["localLayout"], tt.wantLayout, payload)
			}
			_, hasRoot := got["remoteRoot"]
			_, hasRoots := got["remoteRoots"]
			if hasRoot != tt.wantRoot || hasRoots != tt.wantRoots {
				t.Fatalf("topology fields root=%v roots=%v, want %v/%v; payload=%s", hasRoot, hasRoots, tt.wantRoot, tt.wantRoots, payload)
			}
		})
	}
}

func TestWorkspaceSyncStatusExactUsesPersistedRootWithoutReadableRuntimeState(t *testing.T) {
	for _, tt := range []struct {
		name           string
		prepareRuntime func(t *testing.T, localDir string)
	}{
		{name: "absent"},
		{
			name: "unreadable",
			prepareRuntime: func(t *testing.T, localDir string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(localDir, ".relay", "state.json"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			localDir := t.TempDir()
			if tt.prepareRuntime != nil {
				tt.prepareRuntime(t, localDir)
			}
			record := workspaceRecord{
				LocalDir:    localDir,
				LocalLayout: mountscope.LayoutExact,
				RemotePaths: []string{"/github"},
			}
			snapshot := buildWorkspaceSyncStateSnapshot(syncStatusResponse{}, "ws_demo", record)
			payload, err := json.Marshal(workspaceSyncStatusJSON(snapshot, record))
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(payload, &got); err != nil {
				t.Fatal(err)
			}
			if got["remoteRoot"] != "/github" {
				t.Fatalf("remoteRoot = %v, want persisted /github; payload=%s", got["remoteRoot"], payload)
			}
		})
	}
}

func TestSkipStuckAcrossScopesVisitsEachCursorAndSharesLimit(t *testing.T) {
	scopes := []mountscope.Scope{
		{RemotePath: "/github", LocalDir: "/tmp/github"},
		{RemotePath: "/slack", LocalDir: "/tmp/slack"},
	}
	var calls []string
	skipped, backlog, err := skipStuckAcrossScopes(scopes, 3, func(scope mountscope.Scope, max int) (int, bool, error) {
		calls = append(calls, fmt.Sprintf("%s:%d", scope.RemotePath, max))
		if scope.RemotePath == "/github" {
			return 1, false, nil
		}
		return 2, false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 3 || backlog {
		t.Fatalf("skip result = %d/%v, want 3/false", skipped, backlog)
	}
	if want := []string{"/github:3", "/slack:2"}; strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("scope calls = %v, want %v", calls, want)
	}
}

func TestOpsListReportsEmptyWhenNoDeadLetterDir(t *testing.T) {
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

	var stdout bytes.Buffer
	if err := run([]string{"ops", "list", "--workspace", "demo", "--no-refresh"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run ops list failed: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "No dead-lettered ops" {
		t.Fatalf("expected empty marker, got: %q", got)
	}
}

func TestOpsListRefreshesMirrorFromServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	dlDir := filepath.Join(localDir, "notion", ".relay", "dead-letter")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		t.Fatalf("mkdir dead-letter failed: %v", err)
	}
	// Pre-existing record that the server no longer reports — must be pruned.
	if err := os.WriteFile(filepath.Join(dlDir, "op_old.json"), []byte(`{"opId":"op_old"}`), 0o644); err != nil {
		t.Fatalf("write stale record failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:        "demo",
		ID:          "ws_demo",
		LocalDir:    localDir,
		RemotePaths: []string{"/notion"},
		LocalLayout: "scoped",
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/workspaces/ws_demo/ops" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("status"); got != "dead_lettered" {
			t.Fatalf("unexpected status filter: %q", got)
		}
		_, _ = w.Write([]byte(`{"items":[{"opId":"op_new","path":"/notion/page.json","status":"dead_lettered","attemptCount":3,"lastError":"schema validation failed: body must include event","createdAt":"2026-05-02T17:00:00Z","updatedAt":"2026-05-02T18:05:00Z"}]}`))
	}))
	defer server.Close()

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"ops", "list", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run ops list failed: %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "op_new") {
		t.Fatalf("expected refreshed record op_new in output: %q", got)
	}
	if strings.Contains(got, "op_old") {
		t.Fatalf("expected stale record op_old to be pruned, got: %q", got)
	}
	// Verify the new record landed on disk with the expected shape.
	payload, err := os.ReadFile(filepath.Join(dlDir, "op_new.json"))
	if err != nil {
		t.Fatalf("read refreshed record failed: %v", err)
	}
	var record deadLetterRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		t.Fatalf("unmarshal refreshed record failed: %v", err)
	}
	if record.Code != "validation_error" {
		t.Fatalf("expected validation_error code, got %q", record.Code)
	}
	if !strings.HasSuffix(record.ReplayURL, "/v1/workspaces/ws_demo/ops/op_new/replay") {
		t.Fatalf("unexpected replay URL: %q", record.ReplayURL)
	}
	if _, err := os.Stat(filepath.Join(dlDir, "op_old.json")); !os.IsNotExist(err) {
		t.Fatalf("expected stale record removed, got err=%v", err)
	}
}

func TestOpsListRefreshRejectsUnsafeOpIDAndKeepsSidecar(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	dlDir := filepath.Join(localDir, ".relay", "dead-letter")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		t.Fatalf("mkdir dead-letter failed: %v", err)
	}
	// Diagnostic sidecar for a still-live op — must survive a refresh.
	if err := os.WriteFile(filepath.Join(dlDir, "op_keep.error.json"), []byte(`{"providerResponse":"boom"}`), 0o644); err != nil {
		t.Fatalf("write sidecar failed: %v", err)
	}
	// Stale payload the server no longer reports — must be pruned along
	// with any matching sidecar.
	if err := os.WriteFile(filepath.Join(dlDir, "op_old.json"), []byte(`{"opId":"op_old"}`), 0o644); err != nil {
		t.Fatalf("write stale record failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dlDir, "op_old.error.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write stale sidecar failed: %v", err)
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[` +
			`{"opId":"op_keep","path":"/n/p.json","status":"dead_lettered","lastError":"schema validation failed"},` +
			`{"opId":"../../pwn","path":"/n/x.json","status":"dead_lettered","lastError":"oops"}` +
			`]}`))
	}))
	defer server.Close()

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"ops", "list", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run ops list failed: %v", err)
	}

	// The traversal op id must not escape the dead-letter directory.
	traversal := filepath.Join(localDir, "pwn.json")
	if _, err := os.Stat(traversal); !os.IsNotExist(err) {
		t.Fatalf("unsafe opId escaped dead-letter dir: %s exists (err=%v)", traversal, err)
	}
	if _, err := os.Stat(filepath.Join(dlDir, "op_keep.json")); err != nil {
		t.Fatalf("expected op_keep payload written: %v", err)
	}
	// Sidecar for a still-live op must be preserved, not pruned as if it
	// were a standalone payload named "op_keep.error".
	if _, err := os.Stat(filepath.Join(dlDir, "op_keep.error.json")); err != nil {
		t.Fatalf("expected op_keep.error.json preserved: %v", err)
	}
	// Stale payload and its sidecar must both be removed.
	if _, err := os.Stat(filepath.Join(dlDir, "op_old.json")); !os.IsNotExist(err) {
		t.Fatalf("expected stale op_old.json pruned, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dlDir, "op_old.error.json")); !os.IsNotExist(err) {
		t.Fatalf("expected stale op_old.error.json pruned, err=%v", err)
	}
}

func TestPullTriggersSyncRefreshForConnectedProviders(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	refreshes := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/workspaces/ws_demo/sync/status":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[{"provider":"notion","status":"ready","lagSeconds":4},{"provider":"github","status":"ready","lagSeconds":1}]}`))
		case "/v1/workspaces/ws_demo/sync/refresh":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			var body struct {
				Provider string `json:"provider"`
				Reason   string `json:"reason"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode refresh body failed: %v", err)
			}
			if body.Reason != "manual" {
				t.Fatalf("unexpected refresh reason: %q", body.Reason)
			}
			refreshes[body.Provider] = true
			_, _ = w.Write([]byte(`{"status":"queued","id":"sync_job_1"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "delegated_token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"pull", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run pull failed: %v\noutput:\n%s", err, stdout.String())
	}
	for _, provider := range []string{"notion", "github"} {
		if !refreshes[provider] {
			t.Fatalf("expected refresh for %s", provider)
		}
	}
	if got := stdout.String(); !strings.Contains(got, "notion refresh queued") || !strings.Contains(got, "github refresh queued") {
		t.Fatalf("unexpected pull output: %q", got)
	}
}

func TestPullScopedToSingleProviderSkipsStatusCall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/workspaces/ws_demo/sync/refresh" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"queued","id":"sync_job_2"}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "delegated_token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"pull", "--workspace", "demo", "--provider", "notion", "--reason", "investigation"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run pull failed: %v\noutput:\n%s", err, stdout.String())
	}
	if got := stdout.String(); !strings.Contains(got, "notion refresh queued") {
		t.Fatalf("unexpected pull output: %q", got)
	}
}

func TestIntegrationCatalogCacheServesWithinTTL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/integrations/catalog" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"providers":[{"id":"notion","displayName":"Notion","vfsRoot":"/notion"}],"version":"v1"}`))
	}))
	defer server.Close()

	first, err := loadIntegrationCatalog(server.URL, "tok")
	if err != nil {
		t.Fatalf("loadIntegrationCatalog first call failed: %v", err)
	}
	second, err := loadIntegrationCatalog(server.URL, "tok")
	if err != nil {
		t.Fatalf("loadIntegrationCatalog second call failed: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 catalog fetch, got %d", calls)
	}
	if len(first) != 1 || first[0].ID != "notion" || len(second) != 1 || second[0].ID != "notion" {
		t.Fatalf("unexpected catalog contents: %+v / %+v", first, second)
	}
}

func TestIntegrationAvailableSearchesDynamicCatalog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/integrations/catalog" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("dynamic") != "true" {
			t.Fatalf("expected dynamic catalog query, got %s", r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("available catalog should not require auth, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"providers":[{"id":"salesforce","displayName":"Salesforce","backend":"nango","backends":["nango"],"vfsRoot":"/salesforce"},{"id":"docker_hub","displayName":"Docker Hub","backend":"composio","backends":["composio"],"vfsRoot":"/docker_hub"}],"version":"v_dynamic"}`))
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := run([]string{"integration", "search", "--cloud-api-url", server.URL, "--backend", "composio", "docker"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run integration available failed: %v\noutput:\n%s", err, stdout.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "docker_hub") || !strings.Contains(got, "Docker Hub") {
		t.Fatalf("expected docker_hub in output, got %q", got)
	}
	if strings.Contains(got, "salesforce") {
		t.Fatalf("expected backend/search filter to exclude salesforce, got %q", got)
	}
}

func TestIntegrationAvailableFallsBackWhenDynamicCatalogUnavailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/integrations/catalog" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("dynamic") != "true" {
			t.Fatalf("expected dynamic catalog query, got %s", r.URL.RawQuery)
		}
		http.Error(w, "catalog unavailable", http.StatusInternalServerError)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := run([]string{"integration", "available", "--cloud-api-url", server.URL, "--backend", "composio", "--search", "github"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run integration available failed: %v\noutput:\n%s", err, stdout.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "github") || !strings.Contains(got, "GitHub") {
		t.Fatalf("expected fallback github entry in output, got %q", got)
	}
}

func TestIntegrationAvailableUsesDynamicCatalogCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/integrations/catalog" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"providers":[{"id":"docker_hub","displayName":"Docker Hub","backend":"composio","backends":["composio"],"vfsRoot":"/docker_hub"}],"version":"v_dynamic"}`))
	}))
	defer server.Close()

	for i := 0; i < 2; i++ {
		var stdout bytes.Buffer
		if err := run([]string{"integration", "search", "docker", "--cloud-api-url", server.URL}, strings.NewReader(""), &stdout, &stdout); err != nil {
			t.Fatalf("run integration search failed: %v\noutput:\n%s", err, stdout.String())
		}
	}
	if calls != 1 {
		t.Fatalf("expected dynamic catalog cache to serve second search, got %d calls", calls)
	}
}

func TestIntegrationCatalogCachesDynamicAndRegularSeparately(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	regularCalls := 0
	dynamicCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/integrations/catalog" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("dynamic") == "true" {
			dynamicCalls++
			_, _ = w.Write([]byte(`{"providers":[{"id":"docker_hub","displayName":"Docker Hub","backend":"composio","backends":["composio"],"vfsRoot":"/docker_hub"}],"version":"v_dynamic"}`))
			return
		}
		regularCalls++
		_, _ = w.Write([]byte(`{"providers":[{"id":"notion","displayName":"Notion","vfsRoot":"/notion"}],"version":"v_regular"}`))
	}))
	defer server.Close()

	if _, err := loadIntegrationCatalog(server.URL, "tok"); err != nil {
		t.Fatalf("loadIntegrationCatalog first call failed: %v", err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"integration", "search", "docker", "--cloud-api-url", server.URL}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run integration search failed: %v\noutput:\n%s", err, stdout.String())
	}
	if _, err := loadIntegrationCatalog(server.URL, "tok"); err != nil {
		t.Fatalf("loadIntegrationCatalog second call failed: %v", err)
	}
	if regularCalls != 1 {
		t.Fatalf("expected regular catalog cache to survive dynamic fetch, got %d regular calls", regularCalls)
	}
	if dynamicCalls != 1 {
		t.Fatalf("expected one dynamic catalog fetch, got %d", dynamicCalls)
	}
}

func TestIntegrationCatalogCacheRefetchesAfterInvalidate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/integrations/catalog" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"providers":[{"id":"notion","displayName":"Notion","vfsRoot":"/notion"}],"version":"v1"}`))
	}))
	defer server.Close()

	if _, err := loadIntegrationCatalog(server.URL, "tok"); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	invalidateIntegrationCatalogCache()
	if _, err := loadIntegrationCatalog(server.URL, "tok"); err != nil {
		t.Fatalf("post-invalidate call failed: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 catalog fetches after invalidate, got %d", calls)
	}
}

func TestOpsReplayPostsToCloudAndRemovesLocalRecord(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	dlDir := filepath.Join(localDir, "github", ".relay", "dead-letter")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		t.Fatalf("mkdir dead-letter failed: %v", err)
	}
	dlPath := filepath.Join(dlDir, "op_abc.json")
	if err := os.WriteFile(dlPath, []byte(`{"opId":"op_abc","attempts":3}`), 0o644); err != nil {
		t.Fatalf("write dead-letter record failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:        "demo",
		ID:          "ws_demo",
		LocalDir:    localDir,
		RemotePaths: []string{"/github"},
		LocalLayout: "scoped",
		AgentName:   "relayfile-cli",
		Scopes:      append([]string(nil), defaultJoinScopes...),
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_demo/ops/op_abc/replay":
			seen["replay"] = true
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected replay method: %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cld_token" {
				t.Fatalf("unexpected replay Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"status":"queued","id":"op_abc"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	installFakeAgentRelaySession(t, server.URL, "cld_token", "demo", "ws_demo", "ws_demo")

	var stdout bytes.Buffer
	if err := run([]string{
		"ops", "replay", "op_abc",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run ops replay failed: %v\noutput:\n%s", err, stdout.String())
	}
	for _, key := range []string{"replay"} {
		if !seen[key] {
			t.Fatalf("expected %s request", key)
		}
	}
	if got := stdout.String(); !strings.Contains(got, "Replay queued for op op_abc") {
		t.Fatalf("unexpected replay output: %q", got)
	}
	if _, err := os.Stat(dlPath); !os.IsNotExist(err) {
		t.Fatalf("expected dead-letter record removed, got err=%v", err)
	}
}

// TestLoginWithExplicitTokenPersistsServerCreds covers the legacy API-key
// path: when --token is supplied, runLogin validates it against /health and
// writes ~/.relayfile/credentials.json (no cloud creds touched).
func TestLoginWithExplicitTokenPersistsServerCreds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if err := saveLegacyCloudCredentials(cloudCredentials{
		APIURL:      "https://cloud.relayfile.test",
		AccessToken: "stale_cloud_token",
	}); err != nil {
		t.Fatalf("saveCloudCredentials failed: %v", err)
	}

	var healthCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer rf_test" {
			t.Fatalf("unexpected Authorization: %q", got)
		}
		healthCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := run([]string{"login", "--server", server.URL, "--token", "rf_test"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run login failed: %v\noutput:\n%s", err, stdout.String())
	}
	if healthCalls != 1 {
		t.Fatalf("expected 1 /health call, got %d", healthCalls)
	}
	creds, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials failed: %v", err)
	}
	if creds.Token != "rf_test" {
		t.Fatalf("expected stored token rf_test, got %q", creds.Token)
	}
	if _, err := os.Stat(cloudCredentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected stale cloud credentials removed, got err=%v", err)
	}
}

// TestLoginDelegatesToAgentRelay covers the unified auth behavior: relayfile
// login no longer writes its own cloud credential store; it delegates to the
// canonical agent-relay login command.
func TestLoginDelegatesToAgentRelay(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if err := saveLegacyCloudCredentials(cloudCredentials{
		APIURL:      "https://cloud.relayfile.test",
		AccessToken: "stale_cloud_token",
	}); err != nil {
		t.Fatalf("saveCloudCredentials failed: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "agent-relay.log")
	t.Setenv("AGENT_RELAY_LOG", logPath)
	var seenDelegated bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspaces/ws_123/relayfile/delegated-token" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		seenDelegated = true
		if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
			t.Fatalf("unexpected delegated-token Authorization: %q", got)
		}
		var body cloudRelayfileDelegatedTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode delegated-token body: %v", err)
		}
		if body.AgentName != "relayfile-cli" {
			t.Fatalf("unexpected agentName: %q", body.AgentName)
		}
		if got, want := strings.Join(body.Scopes, ","), strings.Join(defaultJoinScopes, ","); got != want {
			t.Fatalf("delegated-token scopes = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		writeDelegatedBundleResponse(t, w, "https://relayfile.test", "ws_123", "rf_login", "refresh_login")
	}))
	defer server.Close()
	installFakeAgentRelay(t, `
printf '%s\n' "$*" >> "$AGENT_RELAY_LOG"
if [ "$*" = "cloud login --no-open" ]; then
  echo "agent-relay login ok"
  exit 0
fi
if [ "$*" = "cloud session --json --reveal-token" ] || [ "$*" = "cloud session --json" ]; then
  echo '{"apiUrl":"`+server.URL+`","accessToken":"cld_new"}'
  exit 0
fi
if [ "$*" = "workspace active --json --reveal-secrets" ] || [ "$*" = "workspace active --json" ]; then
  echo '{"name":"demo","cloudWorkspaceId":"ws_123","relayfileWorkspaceId":"ws_123"}'
  exit 0
fi
echo "unexpected args: $*" >&2
exit 2
`)

	var stdout bytes.Buffer
	if err := run([]string{
		"login",
		"--no-open",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run login failed: %v\noutput:\n%s", err, stdout.String())
	}

	got := stdout.String()
	if !strings.Contains(got, "agent-relay login ok") {
		t.Fatalf("expected delegated agent-relay output, got %q", got)
	}
	if !strings.Contains(got, "Relayfile now uses the active agent-relay cloud session and workspace demo.") {
		t.Fatalf("expected relayfile canonical-session note, got %q", got)
	}
	if !seenDelegated {
		t.Fatalf("expected delegated-token bootstrap call")
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake agent-relay log failed: %v", err)
	}
	gotLog := strings.TrimSpace(string(logBytes))
	for _, want := range []string{"cloud login --no-open", "cloud session --json", "workspace active --json"} {
		if !strings.Contains(gotLog, want) {
			t.Fatalf("expected agent-relay %s call, got %q", want, string(logBytes))
		}
	}
	if _, err := os.Stat(cloudCredentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected stale relayfile cloud credentials removed, got err=%v", err)
	}
	if _, err := os.Stat(credentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected stale relayfile credentials removed, got err=%v", err)
	}
	delegated, err := delegatedauth.Load(delegatedCredentialsPathForRequest("ws_123", defaultJoinScopes))
	if err != nil {
		t.Fatalf("expected delegated credentials to be stored: %v", err)
	}
	if delegated.BearerToken() != "rf_login" || delegated.RotationToken() != "refresh_login" {
		t.Fatalf("unexpected delegated credentials: %#v", delegated)
	}
}

func TestLogoutClearsAuthCredentialsOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	if err := saveCredentials(credentials{Server: "https://relayfile.test", Token: "rf_legacy"}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}
	if err := saveLegacyCloudCredentials(cloudCredentials{
		APIURL:      "https://cloud.relayfile.test",
		AccessToken: "cld_legacy",
	}); err != nil {
		t.Fatalf("saveLegacyCloudCredentials failed: %v", err)
	}
	defaultDelegated := delegatedCredentialsPath()
	if err := delegatedauth.SaveAtomic(defaultDelegated, delegatedauth.Bundle{
		Token:                "rf_delegated",
		RefreshToken:         "refresh_delegated",
		RelayfileWorkspaceID: "ws_demo",
		RelayfileURL:         "https://relayfile.test",
		RelayauthURL:         "https://relayauth.test",
	}); err != nil {
		t.Fatalf("save default delegated credentials failed: %v", err)
	}
	cachedDelegated := delegatedCredentialsPathForRequest("ws_demo", defaultJoinScopes)
	if err := delegatedauth.SaveAtomic(cachedDelegated, delegatedauth.Bundle{
		Token:                "rf_cached",
		RefreshToken:         "refresh_cached",
		RelayfileWorkspaceID: "ws_demo",
		RelayfileURL:         "https://relayfile.test",
		RelayauthURL:         "https://relayauth.test",
	}); err != nil {
		t.Fatalf("save cached delegated credentials failed: %v", err)
	}
	agentRelayAuth := agentRelayCloudAuthPath()
	if err := os.MkdirAll(filepath.Dir(agentRelayAuth), 0o700); err != nil {
		t.Fatalf("mkdir agent-relay auth dir failed: %v", err)
	}
	if err := os.WriteFile(agentRelayAuth, []byte(`{"accessToken":"cld_agent"}`), 0o600); err != nil {
		t.Fatalf("write agent-relay cloud auth failed: %v", err)
	}
	catalog := workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:      "demo",
			ID:        "ws_demo",
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}},
	}
	if err := saveWorkspaceCatalog(catalog); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"logout"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run logout failed: %v\noutput:\n%s", err, stdout.String())
	}
	if got := stdout.String(); !strings.Contains(got, "Logged out of Relayfile on this machine.") {
		t.Fatalf("expected logout success message, got %q", got)
	}
	if got := stdout.String(); !strings.Contains(got, "Agent Relay cloud session left intact") {
		t.Fatalf("expected shared-session note, got %q", got)
	}
	for _, path := range []string{
		credentialsPath(),
		cloudCredentialsPath(),
		defaultDelegated,
		filepath.Join(configDir(), "delegated"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s removed, got err=%v", path, err)
		}
	}
	if _, err := os.Stat(agentRelayAuth); err != nil {
		t.Fatalf("expected shared agent-relay cloud auth preserved, got err=%v", err)
	}
	loadedCatalog, err := loadWorkspaceCatalog()
	if err != nil {
		t.Fatalf("loadWorkspaceCatalog failed: %v", err)
	}
	if loadedCatalog.Default != "demo" || len(loadedCatalog.Workspaces) != 1 {
		t.Fatalf("expected workspace catalog preserved, got %+v", loadedCatalog)
	}
}

func TestLogoutIsIdempotentWhenNoCredentialsExist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var stdout bytes.Buffer
	if err := run([]string{"logout"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run logout failed: %v\noutput:\n%s", err, stdout.String())
	}
	if got := stdout.String(); !strings.Contains(got, "Relayfile is already logged out.") {
		t.Fatalf("expected already-logged-out message, got %q", got)
	}
	if got := stdout.String(); !strings.Contains(got, "Agent Relay cloud session left intact") {
		t.Fatalf("expected shared-session note, got %q", got)
	}
}

func TestLogoutRejectsExtraArguments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var stdout bytes.Buffer
	err := run([]string{"logout", "demo"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatal("expected logout with extra arg to fail")
	}
	if got := err.Error(); !strings.Contains(got, "usage: relayfile logout") {
		t.Fatalf("expected logout usage error, got %q", got)
	}
}

func TestPrepareWorkspaceCommandClientBootstrapsDelegatedCredentialsDespiteStaleLegacyToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if err := saveCredentials(credentials{
		Server:    "https://stale-relayfile.test",
		Token:     "stale_legacy_token",
		UpdatedAt: time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var seenDelegated bool
	var seenTree bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_cloud/relayfile/delegated-token":
			seenDelegated = true
			if got := r.Header.Get("Authorization"); got != "Bearer cld_access" {
				t.Fatalf("unexpected delegated-token Authorization: %q", got)
			}
			var body cloudRelayfileDelegatedTokenRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode delegated-token body: %v", err)
			}
			if got, want := strings.Join(body.Scopes, ","), "relayfile:fs:read:*"; got != want {
				t.Fatalf("delegated-token scopes = %q, want %q", got, want)
			}
			writeDelegatedBundleResponse(t, w, server.URL, "ws_cloud", "rf_read", "refresh_read")
		case "/v1/workspaces/ws_cloud/fs/tree":
			seenTree = true
			if got := r.Header.Get("Authorization"); got != "Bearer rf_read" {
				t.Fatalf("unexpected data-plane Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"path":"/github","entries":[],"nextCursor":null}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	installFakeAgentRelaySession(t, server.URL, "cld_access", "cloud-prod", "ws_cloud", "ws_cloud")

	var stdout bytes.Buffer
	if err := run([]string{"tree", "cloud-prod", "/github"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run tree failed: %v\noutput:\n%s", err, stdout.String())
	}
	if !seenDelegated {
		t.Fatalf("expected delegated-token bootstrap call")
	}
	if !seenTree {
		t.Fatalf("expected tree data-plane call")
	}
	if _, err := os.Stat(credentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected legacy relayfile credentials removed, got err=%v", err)
	}
	delegated, err := delegatedauth.Load(delegatedCredentialsPathForRequest("cloud-prod", defaultInspectScopes))
	if err != nil {
		t.Fatalf("expected delegated credentials to be stored: %v", err)
	}
	if delegated.BearerToken() != "rf_read" || delegated.RotationToken() != "refresh_read" {
		t.Fatalf("unexpected delegated credentials: %#v", delegated)
	}
}

func TestWorkspaceCommandClientRefreshesExpiredDelegatedAccessToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var sawRefresh bool
	var sawStatus bool
	expired := testJWTWithWorkspaceAgentAndExpiry("ws_refresh", "relayfile-cli", time.Now().Add(-time.Minute))
	renewedAccessToken := testJWTWithWorkspaceAgentAndExpiry("ws_refresh", "relayfile-cli", time.Now().Add(time.Hour))
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/tokens/refresh":
			sawRefresh = true
			var body struct {
				RefreshToken string `json:"refreshToken"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode refresh body: %v", err)
			}
			if body.RefreshToken != "refresh_old" {
				t.Fatalf("refresh token = %q, want refresh_old", body.RefreshToken)
			}
			_ = json.NewEncoder(w).Encode(delegatedauth.TokenPair{
				AccessToken:           renewedAccessToken,
				RefreshToken:          "refresh_new",
				AccessTokenExpiresAt:  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
				DelegationNotAfter:    time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
			})
		case "/v1/workspaces/ws_refresh/sync/status":
			sawStatus = true
			if got, want := r.Header.Get("Authorization"), "Bearer "+renewedAccessToken; got != want {
				t.Fatalf("unexpected status Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_refresh","providers":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	credsPath := delegatedCredentialsPathForRequest("ws_refresh", defaultInspectScopes)
	if err := delegatedauth.SaveAtomic(credsPath, delegatedauth.Bundle{
		RelayfileURL:          server.URL,
		RelayauthURL:          server.URL,
		RelayfileWorkspaceID:  "ws_refresh",
		AccessToken:           expired,
		RefreshToken:          "refresh_old",
		AccessTokenExpiresAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save delegated credentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"status", "ws_refresh"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v\noutput:\n%s", err, stdout.String())
	}
	if !sawRefresh {
		t.Fatalf("expected delegated refresh call")
	}
	if !sawStatus {
		t.Fatalf("expected status data-plane call")
	}
	renewed, err := delegatedauth.Load(credsPath)
	if err != nil {
		t.Fatalf("load renewed delegated credentials failed: %v", err)
	}
	if renewed.RotationToken() != "refresh_new" {
		t.Fatalf("expected rotated refresh token, got %#v", renewed)
	}
}

func TestLoadDelegatedCredentialsForRequestIgnoresInsufficientLegacyBundle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	if err := delegatedauth.SaveAtomic(delegatedCredentialsPath(), delegatedauth.Bundle{
		RelayfileURL:         "https://relayfile.test",
		RelayfileWorkspaceID: "ws_cloud",
		AccessToken:          testJWTWithWorkspaceAndScopes("ws_cloud", "relayfile-cli", []string{"relayfile:fs:read:*"}),
		RefreshToken:         "refresh_read",
		RelayfileScopes:      append([]string(nil), defaultInspectScopes...),
	}); err != nil {
		t.Fatalf("save legacy delegated credentials failed: %v", err)
	}

	var sawBootstrap bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_cloud/relayfile/delegated-token":
			sawBootstrap = true
			var body cloudRelayfileDelegatedTokenRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode delegated-token body: %v", err)
			}
			if got, want := strings.Join(body.Scopes, ","), strings.Join(defaultJoinScopes, ","); got != want {
				t.Fatalf("delegated-token scopes = %q, want %q", got, want)
			}
			writeDelegatedBundleResponse(t, w, server.URL, "ws_cloud", "rf_join", "refresh_join")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	installFakeAgentRelaySession(t, server.URL, "cld_access", "cloud-prod", "ws_cloud", "ws_cloud")

	bundle, path, err := loadOrBootstrapDelegatedCredentials("cloud-prod", defaultJoinScopes)
	if err != nil {
		t.Fatalf("loadOrBootstrapDelegatedCredentials failed: %v", err)
	}
	if !sawBootstrap {
		t.Fatalf("expected insufficient legacy delegated bundle to be ignored")
	}
	if path == delegatedCredentialsPath() {
		t.Fatalf("expected scoped delegated credential path, got legacy path")
	}
	if bundle.BearerToken() != "rf_join" {
		t.Fatalf("expected bootstrapped bundle, got %#v", bundle)
	}
}

func TestRefreshDelegatedCredentialsFallsBackToCloudRemint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	expired := testJWTWithWorkspaceAgentAndExpiry("ws_relay", "relayfile-cli", time.Now().Add(-time.Minute))
	reminted := testJWTWithWorkspaceAgentAndExpiry("ws_relay", "relayfile-cli", time.Now().Add(time.Hour))
	var sawRemint bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_cloud/relayfile/delegated-token":
			sawRemint = true
			if got := r.Header.Get("Authorization"); got != "Bearer cld_access" {
				t.Fatalf("unexpected remint Authorization: %q", got)
			}
			var body cloudRelayfileDelegatedTokenRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode remint body: %v", err)
			}
			if got, want := strings.Join(body.Scopes, ","), "relayfile:fs:read:*"; got != want {
				t.Fatalf("remint scopes = %q, want %q", got, want)
			}
			writeDelegatedBundleResponse(t, w, server.URL, "ws_relay", reminted, "refresh_new")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	installFakeAgentRelaySession(t, server.URL, "cld_access", "demo", "ws_cloud", "ws_relay")

	path := delegatedCredentialsPathForRequest("ws_cloud", defaultInspectScopes)
	if err := delegatedauth.SaveAtomic(path, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_relay",
		WorkspaceID:          "ws_cloud",
		AccessToken:          expired,
		RefreshToken:         "refresh_old",
		AgentName:            "relayfile-cli",
		Scopes:               append([]string(nil), defaultInspectScopes...),
	}); err != nil {
		t.Fatalf("save delegated credentials failed: %v", err)
	}

	renewed, err := refreshDelegatedCredentials(path, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_relay",
		WorkspaceID:          "ws_cloud",
		AccessToken:          expired,
		RefreshToken:         "refresh_old",
		AgentName:            "relayfile-cli",
		Scopes:               append([]string(nil), defaultInspectScopes...),
	}, false)
	if err != nil {
		t.Fatalf("refreshDelegatedCredentials failed: %v", err)
	}
	if !sawRemint {
		t.Fatalf("expected cloud delegated-token re-mint")
	}
	if renewed.BearerToken() != reminted || renewed.RotationToken() != "refresh_new" {
		t.Fatalf("unexpected renewed bundle: %#v", renewed)
	}
	persisted, err := delegatedauth.Load(path)
	if err != nil {
		t.Fatalf("load persisted remint failed: %v", err)
	}
	if persisted.BearerToken() != reminted {
		t.Fatalf("expected reminted token persisted, got %#v", persisted)
	}
}

func TestRefreshDelegatedCredentialsSurfacesRemintFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	installFakeAgentRelay(t, `
if [ "$*" = "cloud session --json --reveal-token" ] || [ "$*" = "cloud session --json" ]; then
  echo "no active cloud session" >&2
  exit 2
fi
echo "unexpected args: $*" >&2
exit 2
`)

	expired := testJWTWithWorkspaceAgentAndExpiry("ws_refresh", "relayfile-cli", time.Now().Add(-time.Minute))
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/tokens/refresh":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"delegation_expired"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	path := delegatedCredentialsPathForRequest("ws_refresh", defaultInspectScopes)
	bundle := delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayauthURL:         server.URL,
		RelayfileWorkspaceID: "ws_refresh",
		AccessToken:          expired,
		RefreshToken:         "refresh_old",
		Scopes:               append([]string(nil), defaultInspectScopes...),
	}
	if err := delegatedauth.SaveAtomic(path, bundle); err != nil {
		t.Fatalf("save delegated credentials failed: %v", err)
	}

	_, err := refreshDelegatedCredentials(path, bundle, false)
	if err == nil {
		t.Fatal("expected refresh failure")
	}
	if !errors.Is(err, ErrDelegatedRelayfileCredentialsExpired) {
		t.Fatalf("expected delegated expiry sentinel, got %v", err)
	}
	if !strings.Contains(err.Error(), "no active cloud session") {
		t.Fatalf("expected remint failure detail, got %v", err)
	}
}

func TestWorkspaceCommandRefreshPreservesCatalogDefaultScopes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_refresh",
		AgentName:  "relayfile-cli",
		Scopes:     append([]string(nil), defaultJoinScopes...),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	expired := testJWTWithWorkspaceAgentAndExpiry("ws_refresh", "relayfile-cli", time.Now().Add(-time.Minute))
	renewedAccessToken := testJWTWithWorkspaceAgentAndExpiry("ws_refresh", "relayfile-cli", time.Now().Add(time.Hour))
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/tokens/refresh":
			_ = json.NewEncoder(w).Encode(delegatedauth.TokenPair{
				AccessToken:           renewedAccessToken,
				RefreshToken:          "refresh_new",
				AccessTokenExpiresAt:  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
			})
		case "/v1/workspaces/ws_refresh/sync/status":
			if got, want := r.Header.Get("Authorization"), "Bearer "+renewedAccessToken; got != want {
				t.Fatalf("unexpected status Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_refresh","providers":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	if err := delegatedauth.SaveAtomic(delegatedCredentialsPathForRequest("ws_refresh", defaultInspectScopes), delegatedauth.Bundle{
		RelayfileURL:          server.URL,
		RelayauthURL:          server.URL,
		RelayfileWorkspaceID:  "ws_refresh",
		AccessToken:           expired,
		RefreshToken:          "refresh_old",
		AccessTokenExpiresAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		Scopes:                append([]string(nil), defaultInspectScopes...),
	}); err != nil {
		t.Fatalf("save delegated credentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"status", "ws_refresh"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v\noutput:\n%s", err, stdout.String())
	}
	record, ok := workspaceRecordByID("ws_refresh")
	if !ok {
		t.Fatalf("expected workspace record")
	}
	if got, want := strings.Join(record.Scopes, ","), strings.Join(defaultJoinScopes, ","); got != want {
		t.Fatalf("workspace scopes were clobbered: got %q want %q", got, want)
	}
}

func TestDelegatedRelayfileTokenViaCloudDefaultsToCoarseScopes(t *testing.T) {
	var sawRequest bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspaces/ws_contract/relayfile/delegated-token" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		sawRequest = true
		if got := r.Header.Get("Authorization"); got != "Bearer cld_contract" {
			t.Fatalf("unexpected Authorization: %q", got)
		}
		var body cloudRelayfileDelegatedTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode delegated-token body: %v", err)
		}
		if body.AgentName != "relayfile-cli" {
			t.Fatalf("agentName = %q, want relayfile-cli", body.AgentName)
		}
		if got, want := strings.Join(body.Scopes, ","), "fs:read,fs:write,ops:read,sync:trigger"; got != want {
			t.Fatalf("scopes = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		writeDelegatedBundleResponse(t, w, server.URL, "ws_contract", "rf_contract", "refresh_contract")
	}))
	defer server.Close()

	bundle, err := delegatedRelayfileTokenViaCloud(cloudCredentials{
		APIURL:      server.URL,
		AccessToken: "cld_contract",
	}, "ws_contract", "", nil)
	if err != nil {
		t.Fatalf("delegatedRelayfileTokenViaCloud failed: %v", err)
	}
	if !sawRequest {
		t.Fatalf("expected delegated-token request")
	}
	if bundle.BearerToken() != "rf_contract" {
		t.Fatalf("unexpected bundle: %#v", bundle)
	}
}

func TestEnsureCloudCredentialsUsesAgentRelaySession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if err := saveLegacyCloudCredentials(cloudCredentials{
		APIURL:      "https://stale-cloud.test",
		AccessToken: "stale_cloud_token",
	}); err != nil {
		t.Fatalf("saveCloudCredentials failed: %v", err)
	}
	installFakeAgentRelay(t, `
if [ "$*" = "cloud session --json --reveal-token" ] || [ "$*" = "cloud session --json" ]; then
  echo '{"apiUrl":"https://relay-cloud.test","accessToken":"agent_cloud_token"}'
  exit 0
fi
echo "unexpected args: $*" >&2
exit 2
`)

	creds, err := ensureCloudCredentials("", "", 0, false, io.Discard)
	if err != nil {
		t.Fatalf("ensureCloudCredentials failed: %v", err)
	}
	if creds.APIURL != "https://relay-cloud.test" {
		t.Fatalf("expected agent-relay APIURL, got %q", creds.APIURL)
	}
	if creds.AccessToken != "agent_cloud_token" {
		t.Fatalf("expected agent-relay access token, got %q", creds.AccessToken)
	}
	stale, err := loadLegacyCloudCredentials()
	if err != nil {
		t.Fatalf("loadCloudCredentials failed: %v", err)
	}
	if stale.AccessToken != "stale_cloud_token" {
		t.Fatalf("expected relayfile cloud credential file to be ignored and left untouched, got %q", stale.AccessToken)
	}
}

func TestEnsureCloudCredentialsRejectsStaleAgentRelayBeforeSessionCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	marker := filepath.Join(t.TempDir(), "session-called")
	path := filepath.Join(t.TempDir(), "agent-relay")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
if [ "$*" = "--version" ]; then
  echo "8.3.7"
  exit 0
fi
if [ "$*" = "cloud session --json --reveal-token" ] || [ "$*" = "cloud session --json" ]; then
  touch %q
  echo '{"apiUrl":"https://relay-cloud.test","accessToken":"agent_cloud_token"}'
  exit 0
fi
echo "unexpected args: $*" >&2
exit 2
`, marker)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake stale agent-relay failed: %v", err)
	}
	t.Setenv("AGENT_RELAY_BIN", path)

	_, err := ensureCloudCredentials("", "", 0, false, io.Discard)
	if err == nil {
		t.Fatal("expected stale agent-relay CLI to be rejected")
	}
	got := err.Error()
	for _, want := range []string{"agent-relay CLI >= 8.7.0 required", "8.3.7", "npm install -g agent-relay@8.7.0", "sandbox image"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected error to contain %q, got %q", want, got)
		}
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("expected cloud session command to be skipped, stat err=%v", statErr)
	}
}

// TestLoginAPIKeyFlagPromptsForToken covers the opt-in legacy interactive
// flow: --api-key forces runLogin to prompt for an API key on stdin instead
// of running the cloud browser flow.
func TestLoginAPIKeyFlagPromptsForToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer prompted_key" {
			t.Fatalf("unexpected Authorization: %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	stdin := strings.NewReader("prompted_key\n")
	var stdout bytes.Buffer
	if err := run([]string{"login", "--api-key", "--server", server.URL}, stdin, &stdout, &stdout); err != nil {
		t.Fatalf("run login --api-key failed: %v\noutput:\n%s", err, stdout.String())
	}
	creds, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials failed: %v", err)
	}
	if creds.Token != "prompted_key" {
		t.Fatalf("expected stored token prompted_key, got %q", creds.Token)
	}
}

// TestWorkspaceCurrentPrintsActiveWorkspace covers the new
// `relayfile workspace current` subcommand: it returns the workspace
// resolveWorkspaceRecord("") would pick, and `--verbose` annotates it
// with the resolution source.
func TestWorkspaceCurrentPrintsActiveWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if _, err := upsertWorkspace("alpha"); err != nil {
		t.Fatalf("upsertWorkspace alpha failed: %v", err)
	}
	if _, err := setDefaultWorkspace("alpha"); err != nil {
		t.Fatalf("setDefaultWorkspace failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "current"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace current failed: %v\noutput:\n%s", err, stdout.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "alpha" {
		t.Fatalf("unexpected workspace current output: %q", got)
	}

	stdout.Reset()
	if err := run([]string{"workspace", "current", "--verbose"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace current --verbose failed: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); !strings.Contains(got, "source: default") {
		t.Fatalf("expected verbose output to include source, got %q", got)
	}
}

func TestWorkspaceCurrentPrefersAgentRelayActiveWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if _, err := upsertWorkspace("stale-local-default"); err != nil {
		t.Fatalf("upsertWorkspace failed: %v", err)
	}
	if _, err := setDefaultWorkspace("stale-local-default"); err != nil {
		t.Fatalf("setDefaultWorkspace failed: %v", err)
	}
	installFakeAgentRelay(t, `
if [ "$*" = "workspace active --json --reveal-secrets" ] || [ "$*" = "workspace active --json" ]; then
  echo '{"name":"canonical","cloudWorkspaceId":"rw_cloud","relayfileWorkspaceId":"rw_relayfile","relaycastWorkspaceId":"rw_relaycast","relayauthWorkspaceId":"rw_relayauth"}'
  exit 0
fi
echo "unexpected args: $*" >&2
exit 2
`)

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "current", "--verbose"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace current failed: %v\noutput:\n%s", err, stdout.String())
	}
	got := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(got, "canonical") {
		t.Fatalf("expected canonical active workspace, got %q", got)
	}
	if !strings.Contains(got, "source: agent-relay") {
		t.Fatalf("expected agent-relay source, got %q", got)
	}
}

func TestResolveWorkspaceUsesAgentRelayRelayfileWorkspaceID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if _, err := upsertWorkspace("stale-local-default"); err != nil {
		t.Fatalf("upsertWorkspace failed: %v", err)
	}
	if _, err := setDefaultWorkspace("stale-local-default"); err != nil {
		t.Fatalf("setDefaultWorkspace failed: %v", err)
	}
	installFakeAgentRelay(t, `
if [ "$*" = "workspace active --json --reveal-secrets" ] || [ "$*" = "workspace active --json" ]; then
  echo '{"name":"canonical","cloudWorkspaceId":"rw_cloud","relayfileWorkspaceId":"rw_relayfile"}'
  exit 0
fi
echo "unexpected args: $*" >&2
exit 2
`)

	got, err := resolveWorkspaceIDWithToken("", "")
	if err != nil {
		t.Fatalf("resolveWorkspaceIDWithToken failed: %v", err)
	}
	if got != "rw_relayfile" {
		t.Fatalf("expected relayfile workspace id from agent-relay descriptor, got %q", got)
	}
}

// TestWorkspaceCurrentReportsEnvOverride confirms env-var override beats
// the catalog default, matching resolveWorkspaceRecord's precedence.
func TestWorkspaceCurrentReportsEnvOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if _, err := upsertWorkspace("alpha"); err != nil {
		t.Fatalf("upsertWorkspace alpha failed: %v", err)
	}
	if _, err := setDefaultWorkspace("alpha"); err != nil {
		t.Fatalf("setDefaultWorkspace failed: %v", err)
	}
	t.Setenv("RELAYFILE_WORKSPACE", "from_env")

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "current", "--verbose"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace current failed: %v", err)
	}
	got := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(got, "from_env") {
		t.Fatalf("expected 'from_env' to win, got %q", got)
	}
	if !strings.Contains(got, "RELAYFILE_WORKSPACE") {
		t.Fatalf("expected source to mention RELAYFILE_WORKSPACE, got %q", got)
	}
}

// TestWorkspaceCurrentErrorsWhenNoneActive covers the error path: with
// nothing in env/token/catalog, runWorkspaceCurrent returns an error
// rather than printing an empty line.
func TestWorkspaceCurrentErrorsWhenNoneActive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var stdout bytes.Buffer
	err := run([]string{"workspace", "current"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected error when no active workspace, got output %q", stdout.String())
	}
	if !strings.Contains(err.Error(), "no active workspace") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestNormalizeProviderIDCanonicalizesSlackAlias asserts that both the
// canonical provider id ("slack") and the demoted alias ("slack-sage")
// normalize to the canonical id. Before this fix the function returned
// "slack-sage" for both inputs, poisoning every connect-session request
// with the demoted alias even after the cloud registry migrated to
// "slack" with "slack-sage" retained only as a backwards-compat alias.
func TestNormalizeProviderIDCanonicalizesSlackAlias(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"slack", "slack"},
		{"slack-sage", "slack"},
		{"Slack", "slack"},
		{"  slack-sage  ", "slack"},
		{"github", "github"},
		{"linear", "linear"},
		{"notion", "notion"},
		{"slack-my-senior-dev", "slack-my-senior-dev"},
	}
	for _, tc := range cases {
		if got := normalizeProviderID(tc.input); got != tc.want {
			t.Fatalf("normalizeProviderID(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestFallbackIntegrationCatalogUsesCanonicalSlackId guards the offline
// fallback catalog so it advertises "slack" rather than "slack-sage"
// when the cloud catalog endpoint is unreachable.
func TestFallbackIntegrationCatalogUsesCanonicalSlackId(t *testing.T) {
	entries := fallbackIntegrationCatalog()
	var slack *integrationCatalogEntry
	for i := range entries {
		if entries[i].ID == "slack-sage" {
			t.Fatalf("fallback catalog still advertises legacy id slack-sage: %#v", entries[i])
		}
		if entries[i].ID == "slack" {
			slack = &entries[i]
		}
	}
	if slack == nil {
		t.Fatalf("fallback catalog missing canonical slack entry: %#v", entries)
	}
	if slack.VFSRoot != "/slack" {
		t.Fatalf("expected slack VFSRoot /slack, got %q", slack.VFSRoot)
	}
}

// TestWorkspaceListNamesOnlyRestoresBareOutput verifies the --names-only
// escape hatch for scripts that parsed the legacy unmarked output.
func TestWorkspaceListNamesOnlyRestoresBareOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Setenv("RELAYFILE_WORKSPACE", "ws_env")
	if _, err := upsertWorkspace("alpha"); err != nil {
		t.Fatalf("upsertWorkspace alpha failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()
	if err := saveCredentials(credentials{Server: server.URL, Token: "token"}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "list", "--names-only"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace list --names-only failed: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "ws_env\nalpha" {
		t.Fatalf("unexpected --names-only output: %q", got)
	}
}

// adoptTestServer wires the cloud endpoint the adopt verb exercises:
// POST .../adopt.
// Each test injects its own adopt handler so it can simulate the four
// distinct outcomes — success (fresh insert), success (replacement),
// 409 refusal, 404 missing-connection — without rebuilding the rest of
// the bootstrap chain.
func newAdoptTestServer(t *testing.T, adoptHandler func(http.ResponseWriter, *http.Request)) (*httptest.Server, map[string]int) {
	t.Helper()
	seen := map[string]int{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/github/adopt" && r.Method == http.MethodPost:
			seen["adopt"]++
			adoptHandler(w, r)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	return server, seen
}

func setupAdoptWorkspace(t *testing.T) (workspaceRecord, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	localDir := t.TempDir()
	record, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_123",
		LocalDir:   localDir,
		AgentName:  "relayfile-cli",
		Scopes:     append([]string(nil), defaultJoinScopes...),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	return record, localDir
}

func pointAdoptCloudCredentials(t *testing.T, apiURL string) {
	t.Helper()
	installFakeAgentRelaySession(t, apiURL, "cld_new", "demo", "ws_123", "ws_123")
}

func TestIntegrationAdoptForwardsConnectionIdAndPersistsLocalState(t *testing.T) {
	// Happy path: operator runs `relayfile integration adopt github
	// --connection-id conn_adopt` against a workspace whose row Cloud has
	// not yet seen. Cloud returns ok with no replacedConnectionId; the CLI
	// must (1) POST the request body in the expected shape, (2) persist
	// the new local connection state so subsequent mount / status probes
	// pick it up, and (3) print a single-line confirmation that does NOT
	// mention a replaced connection.
	record, localDir := setupAdoptWorkspace(t)
	server, seen := newAdoptTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode adopt body failed: %v", err)
		}
		if body["connectionId"] != "conn_adopt_new" {
			t.Fatalf("unexpected connectionId: %q", body["connectionId"])
		}
		if body["providerConfigKey"] != "github-relay" {
			t.Fatalf("unexpected providerConfigKey: %q", body["providerConfigKey"])
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
			t.Fatalf("unexpected Authorization: %q", got)
		}
		_, _ = w.Write([]byte(`{"ok":true,"connectionId":"conn_adopt_new"}`))
	})
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	err := run([]string{
		"integration", "adopt", "github",
		"--connection-id", "conn_adopt_new",
		"--provider-config-key", "github-relay",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout)
	if err != nil {
		t.Fatalf("run integration adopt failed: %v\noutput:\n%s", err, stdout.String())
	}
	if seen["adopt"] != 1 {
		t.Fatalf("expected 1 adopt call, got %d", seen["adopt"])
	}
	got := stdout.String()
	if !strings.Contains(got, "github adopted into workspace demo") {
		t.Fatalf("unexpected output (missing confirmation): %q", got)
	}
	if strings.Contains(got, "replaced previous connection") {
		t.Fatalf("did not expect replacement note on fresh insert: %q", got)
	}
	state := loadSavedConnection(localDir, "github")
	if state.ConnectionID != "conn_adopt_new" || state.Backend != "nango" {
		t.Fatalf("unexpected saved integration connection: %#v", state)
	}
	_ = record
}

func TestIntegrationAdoptReportsReplacedConnection(t *testing.T) {
	// Stale-row migration path: Cloud detected the existing row pointed at
	// a connection Nango reports as gone, swapped it atomically, and
	// returned replacedConnectionId. The CLI must surface that id to the
	// operator so they know an old binding was overwritten — silent
	// replacement here would hide a meaningful state change.
	_, localDir := setupAdoptWorkspace(t)
	server, _ := newAdoptTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"connectionId":"conn_new","replacedConnectionId":"conn_old_dead"}`))
	})
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{
		"integration", "adopt", "github",
		"--connection-id", "conn_new",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run integration adopt failed: %v\noutput:\n%s", err, stdout.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "replaced previous connection conn_old_dead") {
		t.Fatalf("expected replacement note in output, got: %q", got)
	}
	state := loadSavedConnection(localDir, "github")
	if state.ConnectionID != "conn_new" {
		t.Fatalf("expected local state to bind conn_new, got %#v", state)
	}
}

func TestIntegrationAdoptReturnsErrorOnConflictAndPreservesLocalState(t *testing.T) {
	// Refusal path: Cloud reports a 409 (e.g. another connection is still
	// live for this workspace + provider). The CLI must return a non-nil
	// error so the shell exit code is non-zero, AND must NOT overwrite the
	// local saved connection — the operator should see no local drift from
	// a failed adopt.
	_, localDir := setupAdoptWorkspace(t)
	// Prime existing local state so we can prove it survives.
	if err := saveIntegrationConnection(localDir, integrationConnectionState{
		Provider:     "github",
		ConnectionID: "conn_existing_local",
		Backend:      "nango",
	}); err != nil {
		t.Fatalf("seed saveIntegrationConnection failed: %v", err)
	}
	server, _ := newAdoptTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"ok":false,"code":"existing_connection_live_or_unknown","error":"Workspace ws_123 already has a live github connection (conn_live). Disconnect it first."}`))
	})
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	err := run([]string{
		"integration", "adopt", "github",
		"--connection-id", "conn_intruder",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected error on 409, got nil; stdout=%q", stdout.String())
	}
	if !strings.Contains(err.Error(), "Disconnect it first") {
		t.Fatalf("expected error to surface Cloud message, got: %v", err)
	}
	var httpErr *apiError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusConflict {
		t.Fatalf("expected apiError with 409 status, got %#v", err)
	}
	state := loadSavedConnection(localDir, "github")
	if state.ConnectionID != "conn_existing_local" {
		t.Fatalf("expected local state to be preserved, got %#v", state)
	}
}

func TestIntegrationAdoptRequiresConnectionID(t *testing.T) {
	// Flag validation: omitting --connection-id must fail before the CLI
	// even hits the network. Otherwise the operator could silently POST
	// an empty body and get a confusing 400 from Cloud.
	_, _ = setupAdoptWorkspace(t)
	var stdout bytes.Buffer
	err := run([]string{
		"integration", "adopt", "github",
		"--workspace", "demo",
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected error when --connection-id is missing; stdout=%q", stdout.String())
	}
	if !strings.Contains(err.Error(), "connection-id") {
		t.Fatalf("expected error to mention --connection-id, got: %v", err)
	}
}

func TestIntegrationAdoptRejectsUnsafeProvider(t *testing.T) {
	var stdout bytes.Buffer
	err := run([]string{
		"integration", "adopt", "../github",
		"--connection-id", "conn_adopt",
		"--workspace", "demo",
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected error for unsafe provider; stdout=%q", stdout.String())
	}
	if !strings.Contains(err.Error(), "invalid provider") {
		t.Fatalf("expected invalid provider error, got: %v", err)
	}
}

func TestIntegrationAdoptClearsDisconnectMarker(t *testing.T) {
	// After a `disconnect` the CLI writes a marker file under
	// .relay/disconnected/{provider}.json. Adopting the provider again
	// must clear that marker so the status probe stops reporting the
	// workspace as disconnected — otherwise the operator gets a stale
	// "disconnected" reading for a workspace they just adopted into.
	record, localDir := setupAdoptWorkspace(t)
	if err := os.WriteFile(filepath.Join(localDir, mountsync.LegacyMountStateFileName), []byte(`{"files":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := markProviderDisconnected(record, "github"); err != nil {
		t.Fatalf("markProviderDisconnected failed: %v", err)
	}
	markerPath := filepath.Join(localDir, ".relay", "disconnected", "github.json")
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("expected disconnect marker to exist, got: %v", err)
	}
	server, _ := newAdoptTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"connectionId":"conn_adopt"}`))
	})
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{
		"integration", "adopt", "github",
		"--connection-id", "conn_adopt",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run integration adopt failed: %v", err)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("expected disconnect marker to be removed, got: %v", err)
	}
}

func TestMarkProviderDisconnectedPreservesScopedRuntimeState(t *testing.T) {
	localRoot := t.TempDir()
	record := workspaceRecord{
		ID:            "ws_demo",
		LocalDir:      localRoot,
		LocalLayout:   mountscope.LayoutScoped,
		MountStateDir: t.TempDir(),
		RemotePaths: []string{
			"/github/repos/acme",
			"/slack/channels/project",
		},
	}
	githubScope := mountscope.LocalDir(localRoot, "/github/repos/acme")
	githubMirror := filepath.Join(githubScope, "README.md")
	githubOutbox := filepath.Join(githubScope, ".relay", "outbox", "pending", "queued.json")
	githubConflict := filepath.Join(githubScope, ".relay", "conflicts", "README.md.local")
	githubDeadLetter := filepath.Join(githubScope, ".relay", "dead-letter", "op_dead.json")
	githubInfrastructure := filepath.Join(githubScope, ".git", "config")
	slackMirror := filepath.Join(mountscope.LocalDir(localRoot, "/slack/channels/project"), "topic.md")
	for _, scope := range workspaceMountScopes(record) {
		stateFile, err := workspaceMountStateFile(record.ID, record, scope)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stateFile, []byte(`{"files":{}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		githubMirror:         "mirrored",
		githubOutbox:         "queued",
		githubConflict:       "unresolved",
		githubDeadLetter:     `{"opId":"op_dead"}`,
		githubInfrastructure: "[core]\n",
		slackMirror:          "unrelated",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	err := markProviderDisconnected(record, "github")
	if err == nil ||
		!strings.Contains(err.Error(), "outbox=1") ||
		!strings.Contains(err.Error(), "conflicts=1") ||
		!strings.Contains(err.Error(), "deadLetters=1") {
		t.Fatalf("expected actionable pending-state refusal, got %v", err)
	}
	for _, path := range []string{githubMirror, githubOutbox, githubConflict, githubDeadLetter, githubInfrastructure, slackMirror} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected refused disconnect to preserve %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(localRoot, ".relay", "disconnected", "github.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected refused disconnect not to write marker, got %v", err)
	}

	for _, path := range []string{githubOutbox, githubConflict, githubDeadLetter} {
		if err := os.Remove(path); err != nil {
			t.Fatalf("clear pending state %s: %v", path, err)
		}
	}
	if err := markProviderDisconnected(record, "github"); err != nil {
		t.Fatalf("markProviderDisconnected after clearing pending state failed: %v", err)
	}
	if _, err := os.Stat(githubMirror); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected github mirror content removed, got %v", err)
	}
	if payload, err := os.ReadFile(githubInfrastructure); err != nil || string(payload) != "[core]\n" {
		t.Fatalf("expected excluded github infrastructure preserved, payload=%q err=%v", payload, err)
	}
	if payload, err := os.ReadFile(slackMirror); err != nil || string(payload) != "unrelated" {
		t.Fatalf("expected unrelated scoped mirror preserved, payload=%q err=%v", payload, err)
	}
	markerPath := filepath.Join(localRoot, ".relay", "disconnected", "github.json")
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("expected common-root disconnect marker: %v", err)
	}
}

func TestProviderDisconnectPreflightAllowsCloudOnlyWorkspace(t *testing.T) {
	record := workspaceRecord{ID: "ws_cloud_only", Name: "cloud-only"}
	plan, err := planProviderDisconnect(record, "github")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.cleanScopeDirs) != 0 || len(plan.removeSubtrees) != 0 || len(plan.stateDirs) != 0 ||
		len(plan.wholeProviderDirs) != 0 || len(plan.stateFiles) != 0 || len(plan.unknownStateRoots) != 0 {
		t.Fatalf("cloud-only disconnect produced local cleanup plan: %#v", plan)
	}
	if err := preflightProviderDisconnect(record, "github"); err != nil {
		t.Fatalf("cloud-only disconnect preflight = %v, want no local gate", err)
	}
}

func TestIntegrationDisconnectRefusesBeforeCloudMutationWhenScopedStateIsPending(t *testing.T) {
	record, localRoot := setupAdoptWorkspace(t)
	record.RelayWorkspaceID = "ws_relay_runtime"
	record.LocalLayout = mountscope.LayoutScoped
	record.RemotePaths = []string{"/github/repos/acme"}
	record.MountStateDir = t.TempDir()
	record.mountStateSet = true
	if _, err := upsertWorkspaceDetails(record); err != nil {
		t.Fatalf("persist scoped workspace: %v", err)
	}
	githubScope := mountscope.LocalDir(localRoot, "/github/repos/acme")
	githubMirror := filepath.Join(githubScope, "README.md")
	if err := os.MkdirAll(filepath.Dir(githubMirror), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(githubMirror), err)
	}
	if err := os.WriteFile(githubMirror, []byte("mirrored"), 0o644); err != nil {
		t.Fatalf("write %s: %v", githubMirror, err)
	}
	stateFile, err := workspaceMountStateFile(record.RelayWorkspaceID, record, workspaceMountScopes(record)[0])
	if err != nil {
		t.Fatalf("resolve private state: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
		t.Fatalf("mkdir private state: %v", err)
	}
	if err := os.WriteFile(stateFile, []byte(`{"files":{"/github/repos/acme/README.md":{"dirty":true},"/github/repos/acme/removed.md":{"deletePending":true}}}`), 0o644); err != nil {
		t.Fatalf("write private state: %v", err)
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Fatalf("refused disconnect reached Cloud: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	err = runIntegrationDisconnect(
		[]string{"github", "--workspace", "demo", "--yes", "--cloud-api-url", server.URL},
		strings.NewReader(""),
		io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), "privatePending=2") {
		t.Fatalf("expected private pending-state refusal, got %v", err)
	}
	if requests != 0 {
		t.Fatalf("Cloud requests = %d, want 0", requests)
	}
	for _, path := range []string{githubMirror, stateFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected refused disconnect to preserve %s: %v", path, err)
		}
	}
}

func TestProviderDisconnectPreflightIgnoresOtherProvidersInBroadScope(t *testing.T) {
	localRoot := t.TempDir()
	record := workspaceRecord{
		ID:               "ws_cloud",
		RelayWorkspaceID: "ws_runtime",
		LocalDir:         localRoot,
		LocalLayout:      mountscope.LayoutExact,
		RemotePaths:      []string{"/"},
		MountStateDir:    t.TempDir(),
		MountKind:        mountsync.MountKindDaemon,
	}
	scope := workspaceMountScopes(record)[0]
	stateFile, err := workspaceMountStateFile(record.RelayWorkspaceID, record, scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		stateFile,
		[]byte(`{"files":{"/slack/channels/project/message.json":{"dirty":true}}}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	outboxDir := filepath.Join(localRoot, ".relay", "outbox", "pending")
	if err := os.MkdirAll(outboxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(outboxDir, "slack.json"),
		[]byte(`{"remotePath":"/slack/channels/project/message.json"}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	conflictPath := filepath.Join(localRoot, ".relay", "conflicts", "slack", "channels", "project", "message.json.rev.local")
	if err := os.MkdirAll(filepath.Dir(conflictPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conflictPath, []byte("conflict"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadLetterDir := filepath.Join(localRoot, ".relay", "dead-letter")
	if err := os.MkdirAll(deadLetterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(deadLetterDir, "slack-op.json"),
		[]byte(`{"opId":"slack-op","path":"/slack/channels/project/message.json"}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := preflightProviderDisconnect(record, "github"); err != nil {
		t.Fatalf("GitHub disconnect was blocked by unrelated Slack state: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(deadLetterDir, "mixed-op.json"),
		[]byte(`{"opId":"mixed-op","path":"/slack/channels/project/other.json,/github/repos/acme/pending.json"}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	err = preflightProviderDisconnect(record, "github")
	if err == nil || !strings.Contains(err.Error(), "deadLetters=1") {
		t.Fatalf("GitHub disconnect with second-position bulk dead letter = %v, want refusal", err)
	}
}

func TestProviderDisconnectPreflightInspectsScopedCatalogCompatibilityState(t *testing.T) {
	localRoot := t.TempDir()
	record := workspaceRecord{
		ID:               "ws_cloud",
		RelayWorkspaceID: "ws_runtime",
		LocalDir:         localRoot,
		LocalLayout:      mountscope.LayoutScoped,
		RemotePaths:      []string{"/github", "/slack"},
		MountStateDir:    t.TempDir(),
		MountKind:        mountsync.MountKindDaemon,
	}
	for _, scope := range workspaceMountScopes(record) {
		stateFile, err := workspaceMountStateFile(record.RelayWorkspaceID, record, scope)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stateFile, []byte(`{"files":{}}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	deadLetterDir := filepath.Join(localRoot, ".relay", "dead-letter")
	if err := os.MkdirAll(deadLetterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(deadLetterDir, "mixed-op.json"),
		[]byte(`{"opId":"mixed-op","path":"/slack/channels/project/message.json,/github/repos/acme/pending.json"}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	err := preflightProviderDisconnect(record, "github")
	if err == nil || !strings.Contains(err.Error(), "deadLetters=1") {
		t.Fatalf("scoped catalog compatibility dead letter preflight = %v, want refusal", err)
	}
}

func TestProviderDisconnectPreflightRefusesUnknownPrivateState(t *testing.T) {
	record := workspaceRecord{
		ID:               "ws_cloud",
		RelayWorkspaceID: "ws_runtime",
		LocalDir:         t.TempDir(),
		LocalLayout:      mountscope.LayoutExact,
		RemotePaths:      []string{"/github"},
		MountStateDir:    t.TempDir(),
		MountKind:        mountsync.MountKindDaemon,
	}
	err := preflightProviderDisconnect(record, "github")
	if err == nil || !strings.Contains(err.Error(), "private mount state is unknown") {
		t.Fatalf("missing private state preflight = %v, want explicit unknown-state refusal", err)
	}
}

func TestDeadLetterRecordBelongsToProviderChecksEveryBulkPath(t *testing.T) {
	for _, rawPath := range []string{
		"/github/repos/acme/pending.json,/slack/channels/project/other.json",
		"/slack/channels/project/other.json,/github/repos/acme/pending.json",
	} {
		if !deadLetterRecordBelongsToProvider(rawPath, "/github") {
			t.Fatalf("mixed-provider dead letter %q did not match GitHub", rawPath)
		}
	}
	if deadLetterRecordBelongsToProvider("/slack/channels/project/other.json", "/github") {
		t.Fatal("Slack-only dead letter matched GitHub")
	}
}

// ----------------------------------------------------------------------
// Integration set-metadata + post-OAuth Atlassian site picker
// ----------------------------------------------------------------------

// newSetMetadataTestServer mirrors newAdoptTestServer but stubs the
// metadata PUT endpoint so set-metadata happy-path tests can record
// the request body and return a canned response.
func newSetMetadataTestServer(t *testing.T, metadataHandler func(http.ResponseWriter, *http.Request)) (*httptest.Server, map[string]int) {
	t.Helper()
	seen := map[string]int{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/metadata") && r.Method == http.MethodPut:
			seen["metadata"]++
			metadataHandler(w, r)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	return server, seen
}

func TestIntegrationSetMetadataHappyPath(t *testing.T) {
	// Operator runs `relayfile integration set-metadata jira cloudId=... baseUrl=...`.
	// The CLI must parse KEY=VALUE args, PUT them under
	// /api/v1/workspaces/.../metadata wrapped in { metadata: { ... } },
	// and exit 0 on cloud's 200.
	_, _ = setupAdoptWorkspace(t)
	server, seen := newSetMetadataTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode metadata body failed: %v", err)
		}
		md := body["metadata"]
		if md["cloudId"] != "cloud-1" || md["baseUrl"] != "https://foo.atlassian.net" {
			t.Fatalf("unexpected metadata body: %#v", md)
		}
		if r.URL.Path != "/api/v1/workspaces/ws_123/integrations/jira/metadata" {
			t.Fatalf("unexpected metadata path: %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
			t.Fatalf("unexpected metadata Authorization: %q", got)
		}
		_, _ = w.Write([]byte(`{"ok":true,"metadata":{"cloudId":"cloud-1","baseUrl":"https://foo.atlassian.net"}}`))
	})
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{
		"integration", "set-metadata", "jira",
		"cloudId=cloud-1",
		"baseUrl=https://foo.atlassian.net",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("set-metadata failed: %v\noutput:\n%s", err, stdout.String())
	}
	if seen["metadata"] != 1 {
		t.Fatalf("expected 1 metadata PUT, got %d", seen["metadata"])
	}
	if !strings.Contains(stdout.String(), "jira metadata updated") {
		t.Fatalf("expected confirmation in output: %q", stdout.String())
	}
}

func TestIntegrationSetMetadataRejectsMissingArgs(t *testing.T) {
	// Missing KEY=VALUE args must fail before we hit the network so the
	// operator sees usage text instead of a confusing cloud 400.
	_, _ = setupAdoptWorkspace(t)
	var stdout bytes.Buffer
	err := run([]string{
		"integration", "set-metadata", "jira",
		"--workspace", "demo",
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected error when no KEY=VALUE pairs provided")
	}
	if !strings.Contains(err.Error(), "KEY=VALUE") {
		t.Fatalf("expected usage hint in error, got: %v", err)
	}
}

func TestIntegrationSetMetadataRejectsMalformedPair(t *testing.T) {
	_, _ = setupAdoptWorkspace(t)
	var stdout bytes.Buffer
	err := run([]string{
		"integration", "set-metadata", "jira",
		"this-is-not-a-pair",
		"--workspace", "demo",
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected error for malformed metadata arg")
	}
	if !strings.Contains(err.Error(), "KEY=VALUE form") {
		t.Fatalf("expected KEY=VALUE form error, got: %v", err)
	}
}

func TestIntegrationSetMetadataRejectsNestedKey(t *testing.T) {
	_, _ = setupAdoptWorkspace(t)
	var stdout bytes.Buffer
	err := run([]string{
		"integration", "set-metadata", "jira",
		"site.cloudId=cloud-1",
		"--workspace", "demo",
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected error for nested-looking metadata key")
	}
	if !strings.Contains(err.Error(), "must be flat") {
		t.Fatalf("expected flat key error, got: %v", err)
	}
}

// newConnectJiraTestServer stubs the cloud routes the
// `integration connect jira` path touches: delegated-token bootstrap, the
// connect-session POST, the status GET (which the wait loop polls), the
// accessible-resources GET (post-OAuth picker), and the metadata PUT.
// `sites` controls how many sites accessible-resources returns; `chosen`
// receives the metadata PUT body so the test can assert on what was set.
func newConnectJiraTestServer(t *testing.T, sites []accessibleResourceEntry, chosen *map[string]any) (*httptest.Server, map[string]int) {
	t.Helper()
	seen := map[string]int{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/workspaces/ws_123/relayfile/delegated-token":
			seen["delegated"]++
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected delegated-token Authorization: %q", got)
			}
			writeDelegatedBundleResponse(t, w, server.URL, "ws_123", "rf_join", "refresh_join")
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/connect-session" && r.Method == http.MethodPost:
			seen["connect-session"]++
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected connect-session Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"connectionId":"conn_jira","connectLink":"","backend":"nango"}`))
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/jira/status":
			seen["status"]++
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected status Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"ready":true,"state":"ready","provider":"jira","backend":"nango"}`))
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/jira/accessible-resources":
			seen["accessible-resources"]++
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected accessible-resources Authorization: %q", got)
			}
			payload := struct {
				OK        bool                      `json:"ok"`
				Resources []accessibleResourceEntry `json:"resources"`
			}{OK: true, Resources: sites}
			_ = json.NewEncoder(w).Encode(payload)
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/jira/metadata" && r.Method == http.MethodPut:
			seen["metadata"]++
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected metadata Authorization: %q", got)
			}
			var body map[string]map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode metadata body failed: %v", err)
			}
			if chosen != nil {
				*chosen = body["metadata"]
			}
			_, _ = w.Write([]byte(`{"ok":true,"metadata":` + jsonMarshalOrPanic(body["metadata"]) + `}`))
		case r.URL.Path == "/v1/workspaces/ws_123/sync/status":
			// waitForInitialSync calls the relayfile data plane; stub it
			// out with a "complete" reading so the test doesn't hang.
			seen["sync-status"]++
			if got := r.Header.Get("Authorization"); got != "Bearer rf_join" {
				t.Fatalf("unexpected sync Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","providers":[{"provider":"jira","status":"ready","lagSeconds":0}]}`))
		case strings.HasPrefix(r.URL.Path, "/v1/workspaces/ws_123/fs/tree"):
			_, _ = w.Write([]byte(`{"entries":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"unexpected request"}`))
		}
	}))
	return server, seen
}

func jsonMarshalOrPanic(value any) string {
	b, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestIntegrationConnectJiraAutoSelectsSingleSite(t *testing.T) {
	// One Atlassian site: the picker should auto-bind it without prompting.
	// Importantly this also proves the picker fires at all on jira/confluence
	// (i.e. isAtlassianProvider returns true and runAtlassianSitePicker is
	// dispatched).
	_, _ = setupAdoptWorkspace(t)
	sites := []accessibleResourceEntry{
		{ID: "cloud-only", URL: "https://only.atlassian.net", Name: "Only Site"},
	}
	var picked map[string]any
	server, seen := newConnectJiraTestServer(t, sites, &picked)
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	err := run([]string{
		"integration", "connect", "jira",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--no-open",
		"--timeout", "5s",
	}, strings.NewReader(""), &stdout, &stdout)
	if err != nil {
		t.Fatalf("integration connect jira failed: %v\noutput:\n%s", err, stdout.String())
	}
	if seen["accessible-resources"] == 0 {
		t.Fatalf("expected accessible-resources to be called for jira")
	}
	if seen["metadata"] == 0 {
		t.Fatalf("expected metadata PUT to fire for the only site")
	}
	if picked["cloudId"] != "cloud-only" || picked["baseUrl"] != "https://only.atlassian.net" {
		t.Fatalf("unexpected auto-selected metadata: %#v", picked)
	}
	if !strings.Contains(stdout.String(), "Auto-selected site") {
		t.Fatalf("expected auto-select log line, got: %q", stdout.String())
	}
}

func TestIntegrationConnectJiraPromptsAndSetsSelectedSite(t *testing.T) {
	// Multi-site path: operator picks site 2 via stdin. We assert (a) the
	// numbered picker rendered, (b) the chosen site's cloudId+baseUrl were
	// PUT to /metadata, and (c) the connect flow completed without hanging.
	_, _ = setupAdoptWorkspace(t)
	sites := []accessibleResourceEntry{
		{ID: "cloud-1", URL: "https://foo.atlassian.net"},
		{ID: "cloud-2", URL: "https://bar.atlassian.net"},
		{ID: "cloud-3", URL: "https://baz.atlassian.net"},
	}
	var picked map[string]any
	server, _ := newConnectJiraTestServer(t, sites, &picked)
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{
		"integration", "connect", "jira",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--no-open",
		"--timeout", "5s",
	}, strings.NewReader("2\n"), &stdout, &stdout); err != nil {
		t.Fatalf("integration connect jira failed: %v\noutput:\n%s", err, stdout.String())
	}
	if picked["cloudId"] != "cloud-2" || picked["baseUrl"] != "https://bar.atlassian.net" {
		t.Fatalf("expected site 2 to be picked, got %#v", picked)
	}
	got := stdout.String()
	if !strings.Contains(got, "Multiple Atlassian sites available") {
		t.Fatalf("expected multi-site prompt header in output: %q", got)
	}
	if !strings.Contains(got, "cloudId=cloud-1") || !strings.Contains(got, "cloudId=cloud-3") {
		t.Fatalf("expected picker to list all sites, got: %q", got)
	}
}

func TestIntegrationConnectJiraDefaultsToFirstSiteOnBlankInput(t *testing.T) {
	// Operator just hits Enter at the picker. The CLI must default to the
	// first site rather than re-prompting or erroring out.
	_, _ = setupAdoptWorkspace(t)
	sites := []accessibleResourceEntry{
		{ID: "cloud-a", URL: "https://a.atlassian.net"},
		{ID: "cloud-b", URL: "https://b.atlassian.net"},
	}
	var picked map[string]any
	server, _ := newConnectJiraTestServer(t, sites, &picked)
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{
		"integration", "connect", "jira",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--no-open",
		"--timeout", "5s",
	}, strings.NewReader("\n"), &stdout, &stdout); err != nil {
		t.Fatalf("integration connect jira failed: %v\noutput:\n%s", err, stdout.String())
	}
	if picked["cloudId"] != "cloud-a" {
		t.Fatalf("expected default site to be picked, got %#v", picked)
	}
}

func TestIntegrationConnectJiraRetriesOnInvalidInputThenErrors(t *testing.T) {
	// Three invalid attempts in a row must abort with a clear error so a
	// dumb-pipe stdin doesn't spin forever. We don't care which exact
	// invalid value we send, only that the CLI gives up after the cap.
	_, _ = setupAdoptWorkspace(t)
	sites := []accessibleResourceEntry{
		{ID: "cloud-1", URL: "https://foo.atlassian.net"},
		{ID: "cloud-2", URL: "https://bar.atlassian.net"},
	}
	server, _ := newConnectJiraTestServer(t, sites, nil)
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	err := run([]string{
		"integration", "connect", "jira",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--no-open",
		"--timeout", "5s",
	}, strings.NewReader("nope\n9999\nstill-bad\n"), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected error after exhausted prompt attempts; stdout=%q", stdout.String())
	}
	if !strings.Contains(err.Error(), "valid site selection") {
		t.Fatalf("expected error to mention site selection, got: %v", err)
	}
}

func TestIntegrationConnectJiraAlreadyConnectedSkipsPicker(t *testing.T) {
	_, localDir := setupAdoptWorkspace(t)
	if err := saveIntegrationConnection(localDir, integrationConnectionState{
		Provider:     "jira",
		ConnectionID: "conn_jira",
		Backend:      "nango",
		ConnectedAt:  time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save integration connection failed: %v", err)
	}

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/workspaces/ws_123/relayfile/delegated-token":
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected delegated-token Authorization: %q", got)
			}
			writeDelegatedBundleResponse(t, w, server.URL, "ws_123", "rf_join", "refresh_join")
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/jira/status":
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected status Authorization: %q", got)
			}
			if got := r.URL.Query().Get("connectionId"); got != "conn_jira" {
				t.Fatalf("unexpected connectionId: %q", got)
			}
			_, _ = w.Write([]byte(`{"ready":true,"state":"ready","provider":"jira","backend":"nango"}`))
		case r.URL.Path == "/v1/workspaces/ws_123/sync/status":
			if got := r.Header.Get("Authorization"); got != "Bearer rf_join" {
				t.Fatalf("unexpected sync Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","providers":[{"provider":"jira","status":"ready","lagSeconds":0}]}`))
		case strings.Contains(r.URL.Path, "/accessible-resources"):
			t.Fatalf("accessible-resources must not be called for already-connected jira, hit: %s", r.URL.Path)
		case strings.HasSuffix(r.URL.Path, "/metadata"):
			t.Fatalf("metadata PUT must not fire for already-connected jira, hit: %s", r.URL.Path)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{
		"integration", "connect", "jira",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--no-open",
		"--timeout", "5s",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("integration connect jira failed: %v\noutput:\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "jira already connected") {
		t.Fatalf("expected already-connected message, got: %q", stdout.String())
	}
}

func TestIntegrationConnectNonAtlassianSkipsPicker(t *testing.T) {
	// Non-Atlassian providers must NOT invoke the picker — the existing
	// connect flow has to keep working unchanged. We verify by stubbing a
	// cloud that fails the test if accessible-resources is ever called.
	_, _ = setupAdoptWorkspace(t)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/workspaces/ws_123/relayfile/delegated-token":
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected delegated-token Authorization: %q", got)
			}
			writeDelegatedBundleResponse(t, w, server.URL, "ws_123", "rf_join", "refresh_join")
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/connect-session":
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected connect-session Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"connectionId":"conn_github","connectLink":"","backend":"nango"}`))
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/github/status":
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected status Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"ready":true,"state":"ready","provider":"github","backend":"nango"}`))
		case r.URL.Path == "/v1/workspaces/ws_123/sync/status":
			if got := r.Header.Get("Authorization"); got != "Bearer rf_join" {
				t.Fatalf("unexpected sync Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","providers":[{"provider":"github","status":"ready","lagSeconds":0}]}`))
		case strings.Contains(r.URL.Path, "/accessible-resources"):
			t.Fatalf("accessible-resources must not be called for non-Atlassian providers, hit: %s", r.URL.Path)
		case strings.HasSuffix(r.URL.Path, "/metadata"):
			t.Fatalf("metadata PUT must not fire for non-Atlassian providers, hit: %s", r.URL.Path)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{
		"integration", "connect", "github",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--no-open",
		"--timeout", "5s",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("integration connect github failed: %v", err)
	}
}

func TestIntegrationConnectKeepsSelectedWorkspaceAndUsesJoinedRelayWorkspaceForSync(t *testing.T) {
	record, _ := setupAdoptWorkspace(t)
	record.ID = "50587328-441d-4acb-b8f3-dbe1b3c5de99"
	if _, err := upsertWorkspaceDetails(record); err != nil {
		t.Fatalf("upsert app workspace record failed: %v", err)
	}

	var server *httptest.Server
	seen := map[string]int{}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		seen[r.URL.Path]++
		switch r.URL.Path {
		case "/api/v1/workspaces/50587328-441d-4acb-b8f3-dbe1b3c5de99/relayfile/delegated-token":
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected delegated-token Authorization: %q", got)
			}
			writeDelegatedBundleResponse(t, w, server.URL, "rw_7ccfea89", "rf_join", "refresh_join")
		case "/api/v1/workspaces/50587328-441d-4acb-b8f3-dbe1b3c5de99/integrations/connect-session":
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected connect-session Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"connectionId":"conn_slack","connectLink":"","backend":"nango"}`))
		case "/api/v1/workspaces/50587328-441d-4acb-b8f3-dbe1b3c5de99/integrations/slack/status":
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected status Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"ready":true,"state":"ready","provider":"slack","backend":"nango"}`))
		case "/v1/workspaces/rw_7ccfea89/sync/status":
			if got := r.Header.Get("Authorization"); got != "Bearer rf_join" {
				t.Fatalf("unexpected sync Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"rw_7ccfea89","providers":[{"provider":"slack","status":"ready","lagSeconds":0}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{
		"integration", "connect", "slack",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--no-open",
		"--timeout", "5s",
		"--wait-sync",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("integration connect slack failed: %v\noutput:\n%s", err, stdout.String())
	}
	if seen["/api/v1/workspaces/50587328-441d-4acb-b8f3-dbe1b3c5de99/integrations/connect-session"] != 1 {
		t.Fatalf("connect-session did not use selected app workspace; seen=%v", seen)
	}
	if seen["/v1/workspaces/rw_7ccfea89/sync/status"] == 0 {
		t.Fatalf("sync wait did not use joined relay workspace; seen=%v", seen)
	}
	persisted, ok := workspaceRecordByName("demo")
	if !ok {
		t.Fatal("expected persisted workspace record")
	}
	if persisted.ID != "50587328-441d-4acb-b8f3-dbe1b3c5de99" {
		t.Fatalf("workspace ID = %q, want selected app workspace", persisted.ID)
	}
	if persisted.RelayWorkspaceID != "rw_7ccfea89" {
		t.Fatalf("relay workspace ID = %q, want rw_7ccfea89", persisted.RelayWorkspaceID)
	}
	if !strings.Contains(stdout.String(), "Relayfile runtime: rw_7ccfea89") {
		t.Fatalf("expected explicit runtime workspace output, got %q", stdout.String())
	}
}

func TestIsWritebackDraftPathSlashSeparated(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/linear/issues/factory-create-abc.json", true},
		{"linear/issues/factory-create-abc.json", true},
		{"/linear/issues/by-state/ready-for-agent/factory-create-9f.json", true},
		{"/linear/issues/AR-1.json", false},
		{"/linear/issues/factory-create-abc.txt", false},
		{"/notion/Docs/notes.md", false},
	}
	for _, tc := range cases {
		if got := isWritebackDraftPath(tc.path); got != tc.want {
			t.Errorf("isWritebackDraftPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestWriteWritebackPushReceiptRedactsTerminalReceipts(t *testing.T) {
	mountRoot := t.TempDir()
	base := writebackPushReceipt{
		CommandID:   "mountcmd_test",
		WorkspaceID: "ws_demo",
		RemotePath:  "/linear/issues/factory-create-x.json",
		ContentType: "application/json",
		Content:     "secret-body",
		Encoding:    "",
		Hash:        "deadbeef",
	}

	// Pending receipt: retains the body (for retry) and is written 0600.
	pending := base
	pending.Status = "pending"
	if err := writeWritebackPushReceipt(mountRoot, pending); err != nil {
		t.Fatalf("write pending receipt: %v", err)
	}
	pendingPath := filepath.Join(mountRoot, ".relay", "outbox", "pending", "mountcmd_test.json")
	info, err := os.Stat(pendingPath)
	if err != nil {
		t.Fatalf("stat pending receipt: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("pending receipt mode = %o, want 600", perm)
	}
	var pendingReceipt writebackPushReceipt
	readReceiptJSON(t, pendingPath, &pendingReceipt)
	if pendingReceipt.Content != "secret-body" {
		t.Fatalf("pending receipt must retain content, got %q", pendingReceipt.Content)
	}

	// Terminal receipts (acked/failed): body stripped, pending copy removed.
	for _, status := range []string{"acked", "failed"} {
		r := base
		r.Status = status
		if err := writeWritebackPushReceipt(mountRoot, r); err != nil {
			t.Fatalf("write %s receipt: %v", status, err)
		}
		p := filepath.Join(mountRoot, ".relay", "outbox", status, "mountcmd_test.json")
		var got writebackPushReceipt
		readReceiptJSON(t, p, &got)
		if got.Content != "" || got.Encoding != "" {
			t.Fatalf("%s receipt must redact content/encoding, got content=%q", status, got.Content)
		}
		if _, err := os.Stat(pendingPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("writing %s receipt should remove the pending copy; stat err=%v", status, err)
		}
		// Re-create the pending copy for the next iteration's removal check.
		if err := writeWritebackPushReceipt(mountRoot, pending); err != nil {
			t.Fatalf("recreate pending receipt: %v", err)
		}
	}
}

func TestIsTerminalWritebackOpStatus(t *testing.T) {
	for _, s := range []string{"failed", "dead_lettered", "canceled", " failed "} {
		if !isTerminalWritebackOpStatus(s) {
			t.Errorf("isTerminalWritebackOpStatus(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "succeeded", "running", "pending", "dispatched"} {
		if isTerminalWritebackOpStatus(s) {
			t.Errorf("isTerminalWritebackOpStatus(%q) = true, want false", s)
		}
	}
}

func readReceiptJSON(t *testing.T, path string, out *writebackPushReceipt) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read receipt %s: %v", path, err)
	}
	if err := json.Unmarshal(payload, out); err != nil {
		t.Fatalf("decode receipt %s: %v", path, err)
	}
}
