package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// The command table below is the single source of truth for relayfile's CLI
// surface. run() dispatches top-level argv through it, so a top-level command
// cannot exist in the table without being routable or be routable without
// being in the table.
//
// `relayfile __command-spec --json` emits the same table as the
// RelayCliCommandSpec[] shape defined by @agent-relay/cli-surface, which
// packages/sdk/typescript checks in as a snapshot so `agent-relay file --help`
// works without the binary present. Regenerate with
// `npm run gen:command-spec --workspace=packages/sdk/typescript`.
//
// Nested levels still dispatch inside their group's own switch (runWorkspace,
// runIntegration, ...). commandspec_test.go parses those switches and each
// leaf's flag.FlagSet out of the source AST and asserts they match what this
// table declares, so the declared tree cannot silently drift from the
// implementation.

// cliArgSpec mirrors RelayCliArgSpec.
type cliArgSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Variadic    bool   `json:"variadic,omitempty"`
}

// cliOptionSpec mirrors RelayCliOptionSpec. Flags is a commander-style flag
// string; the contract's conformance check requires lowercase kebab-case long
// flags, so a Go flag that is not kebab-case must ship a kebab-case alias and
// declare that alias here.
//
// DefaultValue is only populated for defaults that are constant everywhere.
// Defaults derived from the environment or the host filesystem (server URLs,
// state directories, socket paths) are deliberately omitted: this table is
// serialized into a checked-in snapshot, and machine-specific values there
// would both leak local paths and make the snapshot unreproducible.
type cliOptionSpec struct {
	Flags        string `json:"flags"`
	Description  string `json:"description"`
	DefaultValue any    `json:"defaultValue,omitempty"`
}

// cliCommandSpec mirrors RelayCliCommandSpec, plus the unexported bookkeeping
// the dispatcher and the drift test need.
type cliCommandSpec struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Aliases     []string         `json:"aliases,omitempty"`
	Args        []cliArgSpec     `json:"args,omitempty"`
	Options     []cliOptionSpec  `json:"options,omitempty"`
	Subcommands []cliCommandSpec `json:"subcommands,omitempty"`
	Hidden      bool             `json:"hidden,omitempty"`

	// dispatch routes a top-level invocation. Only top-level entries set it.
	dispatch func(cliInvocation) error

	// flagSource names the Go function that owns this command's
	// flag.FlagSet. The drift test asserts Options matches the flags that
	// function registers. Empty means the command registers no flags of its
	// own (it forwards argv, or takes none).
	flagSource string

	// dispatchSource names the Go function whose switch routes this
	// command's subcommands. The drift test asserts Subcommands matches that
	// switch's cases exactly.
	dispatchSource string

	// withheldFlags names flags that flagSource registers but this command
	// deliberately does not advertise. The drift test otherwise requires the
	// declared options to cover the flag set exactly; an entry here is the
	// explicit, checked exception. The test asserts each name is really
	// registered by flagSource and really undeclared, so the list cannot rot
	// into a way of hiding genuine drift.
	withheldFlags []string

	// internal keeps a command out of the emitted spec. Reserved for
	// introspection hooks that the host provides itself or that are not part
	// of the product surface.
	internal bool
}

// cliInvocation carries everything a dispatched command needs. It exists so
// the table can hold one uniform dispatch signature even though the underlying
// run* functions take different subsets.
type cliInvocation struct {
	args   []string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

const commandSpecCommandName = "__command-spec"

var workspaceFlagOption = cliOptionSpec{
	Flags:       "--workspace <name>",
	Description: "workspace name or id",
}

var serverFlagOption = cliOptionSpec{
	Flags:       "--server <url>",
	Description: "relayfile server URL override",
}

var tokenFlagOption = cliOptionSpec{
	Flags:       "--token <token>",
	Description: "relayfile token override",
}

var jsonFlagOption = cliOptionSpec{
	Flags:        "--json",
	Description:  "emit JSON",
	DefaultValue: false,
}

var cloudAPIURLOption = cliOptionSpec{
	Flags:       "--cloud-api-url <url>",
	Description: "Relayfile Cloud API URL (default: $RELAYFILE_CLOUD_API_URL or https://agentrelay.com/cloud)",
}

var workspaceArg = cliArgSpec{
	Name:        "workspace",
	Description: "workspace name or id; defaults to the active workspace",
	Required:    false,
}

// relayfileCommands returns the declared command tree.
func relayfileCommands() []cliCommandSpec {
	return []cliCommandSpec{
		{
			Name:        "setup",
			Description: "Sign in, connect an integration, and mount the workspace",
			flagSource:  "runSetupWithOptions",
			Options: []cliOptionSpec{
				cloudAPIURLOption,
				{Flags: "--cloud-token <token>", Description: "Relayfile Cloud access token; skips browser login when set"},
				{Flags: "--workspace <name>", Description: "workspace name to create"},
				{Flags: "--provider <provider>", Description: "integration provider to connect; use none to skip"},
				{Flags: "--backend <backend>", Description: "integration backend to request (nango or composio)"},
				{Flags: "--local-dir <dir>", Description: "local mount directory"},
				{Flags: "--no-open", Description: "print browser URLs instead of opening them", DefaultValue: false},
				{Flags: "--skip-mount", Description: "finish after setup without starting the mount process", DefaultValue: false},
				{Flags: "--once", Description: "run one mount sync cycle and exit", DefaultValue: false},
				{Flags: "--login-timeout <duration>", Description: "cloud login timeout", DefaultValue: "5m0s"},
				{Flags: "--connect-timeout <duration>", Description: "integration connection timeout", DefaultValue: "5m0s"},
			},
			dispatch: func(inv cliInvocation) error {
				return runSetup(inv.args, inv.stdin, inv.stdout)
			},
		},
		{
			Name:        "login",
			Description: "Sign in via agent-relay cloud login (or --api-key for self-hosted)",
			flagSource:  "runLogin",
			Options: []cliOptionSpec{
				{Flags: "--server <url>", Description: "relayfile server URL (only used with --api-key)"},
				{Flags: "--token <token>", Description: "relayfile API token"},
				cloudAPIURLOption,
				{Flags: "--cloud-token <token>", Description: "Relayfile Cloud access token; skips browser login when set"},
				{Flags: "--api-key", Description: "use the legacy API-key flow against --server instead of the cloud browser login", DefaultValue: false},
				{Flags: "--no-open", Description: "print the cloud sign-in URL instead of opening it", DefaultValue: false},
				{Flags: "--login-timeout <duration>", Description: "cloud login timeout", DefaultValue: "5m0s"},
				{Flags: "--workspace <name>", Description: "workspace name or id to refresh; defaults to the active workspace"},
				{Flags: "--skip-workspace-refresh", Description: "sign into the cloud only; do not refresh the workspace token", DefaultValue: false},
				{Flags: "--provision-messaging-only", Description: "create a separate Relayfile-backed workspace when the active Agent Relay workspace is messaging-only", DefaultValue: false},
			},
			dispatch: func(inv cliInvocation) error {
				return runLogin(inv.args, inv.stdin, inv.stdout)
			},
		},
		{
			Name:        "logout",
			Description: "Clear Relayfile credentials from this machine",
			dispatch: func(inv cliInvocation) error {
				return runLogout(inv.args, inv.stdout)
			},
		},
		{
			Name:           "workspace",
			Description:    "Create, join, select via agent-relay, list, show current, or delete locally tracked workspaces",
			dispatchSource: "runWorkspace",
			Subcommands: []cliCommandSpec{
				{
					Name:        "create",
					Description: "Create a workspace on the relayfile server",
					flagSource:  "runWorkspaceCreate",
					Args:        []cliArgSpec{{Name: "name", Description: "workspace name", Required: true}},
					Options:     []cliOptionSpec{tokenFlagOption},
				},
				{
					Name:        "join",
					Description: "Join an existing workspace by id and track it locally",
					flagSource:  "runWorkspaceJoin",
					Args:        []cliArgSpec{{Name: "workspace-id", Description: "workspace id to join", Required: true}},
					Options: []cliOptionSpec{
						cloudAPIURLOption,
						{Flags: "--cloud-token <token>", Description: "Relayfile Cloud access token; skips browser login when set"},
						{Flags: "--name <name>", Description: "local workspace name"},
						{Flags: "--write", Description: "request read/write workspace token scopes", DefaultValue: false},
						{Flags: "--no-open", Description: "print browser URLs instead of opening them", DefaultValue: false},
						{Flags: "--login-timeout <duration>", Description: "cloud login timeout", DefaultValue: "5m0s"},
					},
				},
				{
					Name:        "use",
					Description: "Select the active workspace for later commands",
					flagSource:  "runWorkspaceUse",
					Args:        []cliArgSpec{{Name: "name", Description: "workspace name or id", Required: true}},
				},
				{
					Name:        "list",
					Description: "List locally tracked workspaces",
					flagSource:  "runWorkspaceList",
					Options: []cliOptionSpec{
						serverFlagOption,
						tokenFlagOption,
						{Flags: "--names-only", Description: "print bare workspace names without an active marker", DefaultValue: false},
					},
				},
				{
					Name:        "current",
					Description: "Show the active workspace",
					flagSource:  "runWorkspaceCurrent",
					Options: []cliOptionSpec{
						tokenFlagOption,
						{Flags: "--verbose", Description: "include workspace id and selection source", DefaultValue: false},
					},
				},
				{
					Name:           "view",
					Description:    "Manage read-only aliases into a registered workspace mirror",
					dispatchSource: "runWorkspaceView",
					Subcommands: []cliCommandSpec{
						{
							Name:        "add",
							Description: "Create an alias directory pointing into the canonical mirror",
							flagSource:  "runWorkspaceViewAdd",
							Args: []cliArgSpec{
								{Name: "remote-path", Description: "remote path to expose", Required: true},
								{Name: "local-dir", Description: "local alias directory", Required: true},
							},
							Options: []cliOptionSpec{
								workspaceFlagOption,
								{Flags: "--replace", Description: "replace an existing relayfile view symlink", DefaultValue: false},
							},
						},
						{
							Name:        "list",
							Description: "List alias directories registered for a workspace",
							flagSource:  "runWorkspaceViewList",
							Options:     []cliOptionSpec{workspaceFlagOption, jsonFlagOption},
						},
						{
							Name:        "remove",
							Description: "Remove an alias directory",
							flagSource:  "runWorkspaceViewRemove",
							Args:        []cliArgSpec{{Name: "local-dir", Description: "local alias directory", Required: true}},
							Options:     []cliOptionSpec{workspaceFlagOption},
						},
					},
				},
				{
					Name:        "status",
					Description: "Show sync status for a workspace",
					flagSource:  "runWorkspaceStatus",
					Args:        []cliArgSpec{workspaceArg},
					Options:     []cliOptionSpec{workspaceFlagOption, jsonFlagOption},
				},
				{
					Name:        "delete",
					Description: "Delete a locally tracked workspace",
					flagSource:  "runWorkspaceDelete",
					Args:        []cliArgSpec{{Name: "name", Description: "workspace name or id", Required: true}},
					Options: []cliOptionSpec{
						{Flags: "--yes", Description: "skip confirmation prompt", DefaultValue: false},
					},
				},
			},
			dispatch: func(inv cliInvocation) error {
				return runWorkspace(inv.args, inv.stdin, inv.stdout)
			},
		},
		{
			Name:           "integration",
			Description:    "Connect, discover, list, disconnect, or adopt workspace integrations",
			dispatchSource: "runIntegration",
			Subcommands: []cliCommandSpec{
				{
					Name:        "connect",
					Description: "Connect a provider integration to a workspace",
					flagSource:  "runIntegrationConnect",
					Args:        []cliArgSpec{{Name: "provider", Description: "provider id, e.g. github or linear", Required: true}},
					Options: []cliOptionSpec{
						workspaceFlagOption,
						cloudAPIURLOption,
						{Flags: "--backend <backend>", Description: "integration backend to request (nango or composio)"},
						{Flags: "--no-open", Description: "print the hosted URL instead of opening it", DefaultValue: false},
						{Flags: "--timeout <duration>", Description: "integration readiness timeout", DefaultValue: "5m0s"},
						{Flags: "--wait-sync", Description: "wait for initial sync before returning", DefaultValue: false},
					},
				},
				{
					Name:        "available",
					Description: "List providers available to connect",
					Aliases:     []string{"catalog", "providers"},
					flagSource:  "runIntegrationAvailable",
					Options: []cliOptionSpec{
						cloudAPIURLOption,
						{Flags: "--backend <backend>", Description: "filter by backend (nango or composio)"},
						{Flags: "--search <query>", Description: "search provider id, display name, category, or backend"},
						jsonFlagOption,
						{Flags: "--refresh", Description: "refresh the cached provider catalog", DefaultValue: false},
					},
				},
				{
					Name:        "search",
					Description: "Search the provider catalog",
					flagSource:  "runIntegrationSearch",
					Args:        []cliArgSpec{{Name: "query", Description: "search query", Required: true}},
					Options: []cliOptionSpec{
						cloudAPIURLOption,
						{Flags: "--backend <backend>", Description: "filter by backend (nango or composio)"},
						jsonFlagOption,
						{Flags: "--refresh", Description: "refresh the cached provider catalog", DefaultValue: false},
					},
				},
				{
					Name:        "list",
					Description: "List a workspace's connected integrations",
					flagSource:  "runIntegrationList",
					Options: []cliOptionSpec{
						workspaceFlagOption,
						jsonFlagOption,
						cloudAPIURLOption,
						{Flags: "--cloud-token <token>", Description: "Relayfile Cloud access token"},
					},
				},
				{
					Name:        "disconnect",
					Description: "Disconnect a provider integration",
					flagSource:  "runIntegrationDisconnect",
					Args:        []cliArgSpec{{Name: "provider", Description: "provider id", Required: true}},
					Options: []cliOptionSpec{
						workspaceFlagOption,
						cloudAPIURLOption,
						{Flags: "--yes", Description: "skip confirmation", DefaultValue: false},
					},
				},
				{
					Name:        "adopt",
					Description: "Adopt an existing Nango connection as a workspace integration",
					flagSource:  "runIntegrationAdopt",
					Args:        []cliArgSpec{{Name: "provider", Description: "provider id", Required: true}},
					Options: []cliOptionSpec{
						workspaceFlagOption,
						cloudAPIURLOption,
						{Flags: "--connection-id <id>", Description: "Nango connection id to adopt (required)"},
						{Flags: "--provider-config-key <key>", Description: "optional Nango providerConfigKey override"},
						{Flags: "--yes", Description: "skip confirmation", DefaultValue: false},
					},
				},
				{
					Name:        "set-metadata",
					Description: "Set provider connection metadata as KEY=VALUE pairs",
					flagSource:  "runIntegrationSetMetadata",
					Args: []cliArgSpec{
						{Name: "provider", Description: "provider id", Required: true},
						{Name: "assignments", Description: "one or more KEY=VALUE metadata assignments", Required: true, Variadic: true},
					},
					Options: []cliOptionSpec{
						workspaceFlagOption,
						cloudAPIURLOption,
						{Flags: "--yes", Description: "skip confirmation", DefaultValue: false},
					},
				},
				{
					Name:        "bind",
					Description: "Bind a provider resource or path glob to a relay channel",
					flagSource:  "runIntegrationBind",
					Args: []cliArgSpec{
						{Name: "provider", Description: "provider id", Required: false},
						{Name: "resource", Description: "provider resource or path glob", Required: false},
					},
					Options: []cliOptionSpec{
						{Flags: "--list", Description: "list active relay bindings as JSON", DefaultValue: false},
						{Flags: "--json", Description: "accepted for consistency with other JSON-emitting integration commands", DefaultValue: false},
						{Flags: "--channel <channel>", Description: "relay channel to receive provider records"},
						{Flags: "--webhook <id>", Description: "RelayCast inbound webhook id"},
						{Flags: "--webhook-token <token>", Description: "RelayCast inbound webhook token"},
						{Flags: "--subscription <id>", Description: "relay integration subscription id"},
						{Flags: "--webhook-subscription <id>", Description: "relayfile-cloud inbound webhook subscription id"},
						{Flags: "--webhook-subscription-workspace <id>", Description: "workspace the webhook subscription was created in (pairs with --webhook-subscription)"},
					},
				},
				{
					Name:        "resolve-path",
					Description: "Resolve a provider resource to its relayfile path",
					flagSource:  "runIntegrationResolvePath",
					Args: []cliArgSpec{
						{Name: "provider", Description: "provider id", Required: true},
						{Name: "resource", Description: "provider resource identifier", Required: true},
					},
					Options: []cliOptionSpec{jsonFlagOption},
				},
				{
					Name:        "unbind",
					Description: "Remove a relay binding for a provider",
					flagSource:  "runIntegrationUnbind",
					Args: []cliArgSpec{
						{Name: "provider", Description: "provider id", Required: true},
						{Name: "resource", Description: "path glob or resource to unbind; may be passed as --resource instead", Required: false},
					},
					Options: []cliOptionSpec{
						{Flags: "--resource <resource>", Description: "path glob/resource to unbind"},
					},
				},
				{
					Name:        "writeback-secret",
					Description: "Print the writeback secret for a bound relay channel",
					flagSource:  "runIntegrationWritebackSecret",
					Options: []cliOptionSpec{
						workspaceFlagOption,
						{Flags: "--channel <channel>", Description: "relay channel the binding delivers to"},
						jsonFlagOption,
					},
				},
			},
			dispatch: func(inv cliInvocation) error {
				return runIntegration(inv.args, inv.stdin, inv.stdout)
			},
		},
		{
			Name:           "ops",
			Description:    "List or replay dead-lettered writeback ops",
			dispatchSource: "runOps",
			Subcommands: []cliCommandSpec{
				{
					Name:        "list",
					Description: "List dead-lettered writeback ops",
					flagSource:  "runOpsList",
					Options: []cliOptionSpec{
						workspaceFlagOption,
						jsonFlagOption,
						{Flags: "--no-refresh", Description: "skip refreshing the local mirror from the server", DefaultValue: false},
						serverFlagOption,
						tokenFlagOption,
					},
				},
				{
					Name:        "replay",
					Description: "Replay one dead-lettered writeback op",
					flagSource:  "runOpsReplay",
					Args:        []cliArgSpec{{Name: "op-id", Description: "dead-lettered operation id", Required: true}},
					Options:     []cliOptionSpec{workspaceFlagOption, cloudAPIURLOption},
				},
			},
			dispatch: func(inv cliInvocation) error {
				return runOps(inv.args, inv.stdin, inv.stdout)
			},
		},
		{
			Name:           "writeback",
			Description:    "Inspect or retry local writeback failures",
			dispatchSource: "runWriteback",
			Subcommands: []cliCommandSpec{
				{
					Name:        "list",
					Description: "List local writeback items by state",
					flagSource:  "runWritebackList",
					Options: []cliOptionSpec{
						{Flags: "--state <state>", Description: "writeback state: pending or dead"},
						workspaceFlagOption,
						jsonFlagOption,
					},
				},
				{
					Name:        "push",
					Description: "Push a local file to the workspace and wait for its receipt",
					flagSource:  "runWritebackFileMutation",
					Args:        []cliArgSpec{{Name: "local-path", Description: "local mirror path to push", Required: true}},
					Options:     writebackMutationOptions(),
				},
				{
					Name:        "update",
					Description: "Update a workspace file from its local mirror copy",
					flagSource:  "runWritebackFileMutation",
					Args:        []cliArgSpec{{Name: "local-path", Description: "local mirror path to update", Required: true}},
					Options:     writebackMutationOptions(),
				},
				{
					Name:        "delete",
					Description: "Delete a workspace file via its local mirror path",
					flagSource:  "runWritebackFileMutation",
					Args:        []cliArgSpec{{Name: "local-path", Description: "local mirror path to delete", Required: true}},
					Options:     writebackMutationOptions(),
				},
				{
					Name:        "status",
					Description: "Show local pending, failed, and dead-lettered writebacks",
					flagSource:  "runWritebackStatus",
					Args:        []cliArgSpec{workspaceArg},
					Options:     []cliOptionSpec{jsonFlagOption},
				},
				{
					Name:        "retry",
					Description: "Re-enqueue a local dead-lettered writeback op",
					flagSource:  "runWritebackRetry",
					Args:        []cliArgSpec{workspaceArg},
					Options: []cliOptionSpec{
						{Flags: "--op-id <id>", Description: "dead-lettered operation id (also accepted as --opId)"},
					},
				},
				{
					Name:        "skip-stuck",
					Description: "Walk the events cursor past stuck (404) events without waiting the treat-as-deleted timer",
					flagSource:  "runWritebackSkipStuck",
					Args:        []cliArgSpec{workspaceArg},
					Options: []cliOptionSpec{
						workspaceFlagOption,
						{Flags: "--max <n>", Description: "maximum number of stuck events to skip (0 = unbounded)", DefaultValue: 0},
						jsonFlagOption,
					},
				},
				{
					Name:        "sweep-drafts",
					Description: "Remove hand-named draft files left behind under a workspace subtree",
					flagSource:  "runWritebackSweepDrafts",
					Args:        []cliArgSpec{workspaceArg},
					Options: []cliOptionSpec{
						{Flags: "--path-prefix <prefix>", Description: "restrict the sweep to a subtree"},
						{Flags: "--pattern <glob>", Description: "basename glob for hand-named drafts (repeatable), e.g. wb-*.json"},
						{Flags: "--apply", Description: "execute removals (default is a dry run)", DefaultValue: false},
						jsonFlagOption,
						serverFlagOption,
						tokenFlagOption,
					},
				},
			},
			dispatch: func(inv cliInvocation) error {
				return runWriteback(inv.args, inv.stdout)
			},
		},
		{
			Name:           "digest",
			Description:    "Regenerate workspace digests",
			dispatchSource: "runDigest",
			Subcommands: []cliCommandSpec{
				{
					Name:        "rebuild",
					Description: "Regenerate daily, weekly, or date-stamped digest artifacts",
					flagSource:  "runDigestRebuild",
					Options: []cliOptionSpec{
						{Flags: "--window <window>", Description: "digest window: today, yesterday, this-week, last-week, or YYYY-MM-DD"},
						workspaceFlagOption,
						{Flags: "--json", Description: "print machine-readable JSON", DefaultValue: false},
					},
				},
			},
			dispatch: func(inv cliInvocation) error {
				return runDigest(inv.args, inv.stdout)
			},
		},
		{
			Name:        "pull",
			Description: "Trigger an immediate sync refresh for one or all providers",
			flagSource:  "runPull",
			Options: []cliOptionSpec{
				workspaceFlagOption,
				{Flags: "--provider <provider>", Description: "provider id (default: refresh all connected providers)"},
				{Flags: "--reason <text>", Description: "free-form reason recorded server-side", DefaultValue: "manual"},
				serverFlagOption,
				tokenFlagOption,
			},
			dispatch: func(inv cliInvocation) error {
				return runPull(inv.args, inv.stdout)
			},
		},
		{
			Name:        "mount",
			Description: "Mirror a remote workspace to a local directory; add --background to detach",
			Aliases:     []string{"start", "on"},
			flagSource:  "runMount",
			Args: []cliArgSpec{
				workspaceArg,
				{Name: "local-dir", Description: "local mirror directory", Required: false},
			},
			Options:     mountOptions(),
			Subcommands: mountSealCommands(),
			dispatch: func(inv cliInvocation) error {
				// `start` and `on` are friendlier aliases for `mount`. Same
				// flags, same foreground/background behavior; pass
				// --background to detach.
				if len(inv.args) > 0 {
					if seal, ok := mountSealDispatch[inv.args[0]]; ok {
						return seal(cliInvocation{
							args:   inv.args[1:],
							stdin:  inv.stdin,
							stdout: inv.stdout,
							stderr: inv.stderr,
						})
					}
				}
				return runMount(inv.args)
			},
		},
		{
			Name:        "restart",
			Description: "Stop and start a workspace's mount in one step (--foreground to attach)",
			flagSource:  "runRestart",
			Args:        []cliArgSpec{workspaceArg},
			Options: []cliOptionSpec{
				{Flags: "--foreground", Description: "run the restarted mount in the foreground instead of detaching", DefaultValue: false},
			},
			dispatch: func(inv cliInvocation) error {
				return runRestart(inv.args, inv.stdout)
			},
		},
		{
			Name:           "supervisor",
			Description:    "Install/uninstall/status launchd (macOS) or systemd (Linux) service for auto-restart",
			dispatchSource: "runSupervisor",
			Subcommands: []cliCommandSpec{
				{
					Name:        "install",
					Description: "Install the auto-restart service for a workspace mount",
					// supervisor install embeds its argv verbatim into the
					// unit's ExecStart as `relayfile listen ...`, so it
					// accepts exactly what runListen parses and nothing else:
					// a flag declared here that runListen does not register
					// installs a service that exits on every start and, under
					// Restart=on-failure, restarts forever.
					//
					// The converse also holds, which is why this takes
					// listen's filters rather than all of listenOptions():
					// runListen's process-model flags parse fine but make the
					// supervised process detach or rotate the unit's own log,
					// so they are withheld from the surface (and rejected at
					// runtime by supervisorInstall).
					flagSource:    "runListen",
					Args:          []cliArgSpec{workspaceArg},
					Options:       listenFilterOptions(),
					withheldFlags: listenProcessModelFlagNames(),
				},
				{
					Name:        "uninstall",
					Description: "Remove the auto-restart service",
					Aliases:     []string{"remove"},
				},
				{
					Name:        "status",
					Description: "Show the auto-restart service state",
				},
			},
			dispatch: func(inv cliInvocation) error {
				return runSupervisor(inv.args, inv.stdout)
			},
		},
		{
			Name:        "tree",
			Description: "List a remote workspace path",
			Aliases:     []string{"ls"},
			flagSource:  "runTree",
			Args: []cliArgSpec{
				workspaceArg,
				{Name: "path", Description: "remote path to list; defaults to /", Required: false},
			},
			Options: []cliOptionSpec{
				serverFlagOption,
				tokenFlagOption,
				{Flags: "--path <path>", Description: "remote path to list", DefaultValue: "/"},
				{Flags: "--depth <n>", Description: "tree depth", DefaultValue: 1},
				{Flags: "--json", Description: "print the raw JSON response", DefaultValue: false},
			},
			dispatch: func(inv cliInvocation) error {
				return runTree(inv.args, inv.stdout)
			},
		},
		{
			Name:        "read",
			Description: "Print a remote file's content",
			Aliases:     []string{"cat"},
			flagSource:  "runRead",
			Args: []cliArgSpec{
				workspaceArg,
				// PATH is required, but the contract forbids a required
				// positional after an optional one and relayfile accepts
				// either `read PATH` or `read WORKSPACE PATH`. Both are
				// declared optional; the command rejects an empty path.
				{Name: "path", Description: "remote file path (required; the sole positional when no workspace is given)", Required: false},
			},
			Options: []cliOptionSpec{
				serverFlagOption,
				tokenFlagOption,
				{Flags: "--output <file>", Description: "output file path or - for stdout", DefaultValue: "-"},
				{Flags: "--json", Description: "print the raw JSON response", DefaultValue: false},
			},
			dispatch: func(inv cliInvocation) error {
				return runRead(inv.args, inv.stdout)
			},
		},
		{
			Name:        "seed",
			Description: "Upload a directory tree with bulk writes",
			flagSource:  "runSeed",
			Args: []cliArgSpec{
				workspaceArg,
				{Name: "dir", Description: "local directory to upload; defaults to the current directory", Required: false},
			},
			Options: []cliOptionSpec{serverFlagOption, tokenFlagOption},
			dispatch: func(inv cliInvocation) error {
				return runSeed(inv.args, inv.stdout)
			},
		},
		{
			Name:        "export",
			Description: "Export a workspace as json, tar, or patch",
			flagSource:  "runExport",
			Args:        []cliArgSpec{workspaceArg},
			Options: []cliOptionSpec{
				serverFlagOption,
				tokenFlagOption,
				{Flags: "--format <format>", Description: "export format: tar, json, or patch", DefaultValue: "json"},
				{Flags: "--output <file>", Description: "output file path or - for stdout", DefaultValue: "-"},
			},
			dispatch: func(inv cliInvocation) error {
				return runExport(inv.args, inv.stdout)
			},
		},
		{
			Name:        "status",
			Description: "Show sync status and local mirror state for a workspace",
			flagSource:  "runStatus",
			Args:        []cliArgSpec{workspaceArg},
			Options:     []cliOptionSpec{serverFlagOption, tokenFlagOption, jsonFlagOption},
			dispatch: func(inv cliInvocation) error {
				return runStatus(inv.args, inv.stdout)
			},
		},
		{
			Name:        "stop",
			Description: "Stop a background mount",
			Aliases:     []string{"off"},
			flagSource:  "runStop",
			Args:        []cliArgSpec{workspaceArg},
			dispatch: func(inv cliInvocation) error {
				// `off` is the friendlier alias for `stop`, migrating the
				// agent-relay `relay off` unmount UX into relayfile.
				return runStop(inv.args, inv.stdout)
			},
		},
		{
			Name:        "logs",
			Description: "Print the background mount log",
			flagSource:  "runLogs",
			Args:        []cliArgSpec{workspaceArg},
			Options: []cliOptionSpec{
				{Flags: "--lines <n>", Description: "number of lines to print", DefaultValue: 40},
			},
			dispatch: func(inv cliInvocation) error {
				return runLogs(inv.args, inv.stdout)
			},
		},
		{
			Name:        "observer",
			Description: "Open the hosted file observer for a workspace",
			flagSource:  "runObserver",
			Args:        []cliArgSpec{workspaceArg},
			Options: []cliOptionSpec{
				serverFlagOption,
				tokenFlagOption,
				{Flags: "--url <url>", Description: "observer URL (default: $RELAYFILE_OBSERVER_URL or the hosted observer)"},
				{Flags: "--no-open", Description: "print the observer URL without opening a browser", DefaultValue: false},
			},
			dispatch: func(inv cliInvocation) error {
				return runObserver(inv.args, inv.stdout)
			},
		},
		{
			Name:        "listen",
			Description: "Stream workspace file events, optionally running a command per event",
			Aliases:     []string{"watch"},
			flagSource:  "runListen",
			Args:        []cliArgSpec{workspaceArg},
			Options:     listenOptions(),
			dispatch: func(inv cliInvocation) error {
				return runListen(inv.args, inv.stdout)
			},
		},
		{
			Name:           "control-plane",
			Description:    "Serve the local relayfile control-plane socket",
			dispatchSource: "runControlPlane",
			Subcommands: []cliCommandSpec{
				{
					Name:        "serve",
					Description: "Serve the control-plane unix socket",
					flagSource:  "runControlPlaneServe",
					Options: []cliOptionSpec{
						{Flags: "--sock <path>", Description: "unix socket path (default: the per-user relayfile socket)"},
					},
				},
			},
			dispatch: func(inv cliInvocation) error {
				return runControlPlane(inv.args, inv.stdout)
			},
		},
		{
			Name:        "dev",
			Description: "Print workspace context, then stream file events like `listen`",
			Hidden:      true,
			// dev forwards its argv verbatim to runListen, so it accepts
			// exactly listen's flags and the same optional workspace.
			flagSource: "runListen",
			Args:       []cliArgSpec{workspaceArg},
			Options:    listenOptions(),
			dispatch: func(inv cliInvocation) error {
				return runDev(inv.args, inv.stdin, inv.stdout)
			},
		},
		{
			// The host CLI renders help from the emitted spec, so relayfile's
			// own `help` stays routable but out of the product surface.
			Name:        "help",
			Description: "Print relayfile usage",
			internal:    true,
			dispatch: func(inv cliInvocation) error {
				printUsage(inv.stdout)
				return nil
			},
		},
		{
			// Introspection hook: emits this table so @relayfile/sdk can
			// snapshot it. Excluded from the emitted spec both because it is
			// not a product command and because its name is not the
			// kebab-case the contract requires.
			Name:        commandSpecCommandName,
			Description: "Emit the relayfile command tree as RelayCliCommandSpec JSON",
			internal:    true,
			dispatch: func(inv cliInvocation) error {
				return runCommandSpec(inv.args, inv.stdout)
			},
		},
	}
}

func writebackMutationOptions() []cliOptionSpec {
	return []cliOptionSpec{
		workspaceFlagOption,
		serverFlagOption,
		tokenFlagOption,
		jsonFlagOption,
		{Flags: "--timeout <duration>", Description: "operation receipt wait timeout", DefaultValue: "1m30s"},
	}
}

// listenFilterOptions are the `listen` flags that describe *what* to stream
// and where to stream it from. runListen turns every one of them into a
// filter, a credential, or an output format, and none of them changes how the
// process itself runs — so they are exactly the flags that can be embedded in
// a launchd/systemd unit's ExecStart.
func listenFilterOptions() []cliOptionSpec {
	return []cliOptionSpec{
		serverFlagOption,
		tokenFlagOption,
		{Flags: "--provider <provider>", Description: "filter to a specific provider (e.g. linear, notion)"},
		{Flags: "--path <glob>", Description: "glob path filter (e.g. /linear/issues/**)"},
		{Flags: "--event <type>", Description: "event type filter: file.created, file.updated, file.deleted"},
		{Flags: "--run <command>", Description: "shell command per event; supports {{path}}, {{type}}, {{provider}}, {{revision}}, {{event}}"},
		{Flags: "--format <format>", Description: "output format when --run is not set: text or json", DefaultValue: "text"},
	}
}

// listenProcessModelOptions are the `listen` flags that choose how the process
// runs rather than what it streams, and runListen acts on both before it opens
// a single connection:
//
//   - --background re-execs a detached `listen --daemonized` child and
//     returns, so the process systemd/launchd is supervising exits
//     immediately. systemd then tears the orphaned grandchild down with the
//     unit's cgroup, and launchd's KeepAlive=true relaunches the exiting
//     parent forever.
//   - --daemonized is the internal marker that detached child is spawned
//     with. It also rotates ~/.relayfile/listen.log, which is the same file
//     the installed unit appends its own stdout and stderr to.
//
// Neither may be advertised on `supervisor install`, whose argv goes verbatim
// into the unit. This is the same class of bug as advertising --interval
// there: a flag that makes the supervised process wrong on every start.
func listenProcessModelOptions() []cliOptionSpec {
	return []cliOptionSpec{
		{Flags: "--background", Description: "run in background; logs to ~/.relayfile/listen.log", DefaultValue: false},
		{Flags: "--daemonized", Description: "internal flag used by relayfile listen --background", DefaultValue: false},
	}
}

// listenProcessModelFlagNames is listenProcessModelOptions as bare long flag
// names, for the places that match argv or withhold flags by name.
func listenProcessModelFlagNames() []string {
	options := listenProcessModelOptions()
	names := make([]string, 0, len(options))
	for _, option := range options {
		if name := optionLongName(option.Flags); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func listenOptions() []cliOptionSpec {
	return append(listenFilterOptions(), listenProcessModelOptions()...)
}

func mountOptions() []cliOptionSpec {
	return []cliOptionSpec{
		{Flags: "--server <url>", Description: "relayfile server URL"},
		{Flags: "--token <token>", Description: "bearer token"},
		{Flags: "--creds-file <file>", Description: "delegated relayfile credentials file"},
		{Flags: "--remote-path <path>", Description: "remote root path (may be repeated)"},
		{Flags: "--paths-file <file>", Description: "file containing remote root paths, as a JSON array or newline-separated list"},
		{Flags: "--local-layout <layout>", Description: "local directory layout: exact or scoped"},
		{Flags: "--provider <provider>", Description: "event provider filter"},
		{Flags: "--state-file <file>", Description: "state file path"},
		{Flags: "--state-dir <dir>", Description: "directory for private mount state"},
		{Flags: "--mount-kind <kind>", Description: "private state identity kind: daemon, flush, or initial-sync"},
		{Flags: "--local-dir <dir>", Description: "local mirror directory"},
		{Flags: "--mode <mode>", Description: "mount mode: poll (recommended) or fuse"},
		{Flags: "--interval <duration>", Description: "sync interval"},
		{Flags: "--interval-jitter <ratio>", Description: "sync interval jitter ratio (0.0-1.0)"},
		{Flags: "--timeout <duration>", Description: "per-sync timeout"},
		{Flags: "--bootstrap-timeout <duration>", Description: "hard cap for the one-time/full-tree bootstrap pull (0 = unbounded while making progress)"},
		{Flags: "--bootstrap-max-files-per-cycle <n>", Description: "maximum files materialized per resumable tree-bootstrap cycle (-1 = legacy unbounded tree behavior)"},
		{Flags: "--full-pull-min-interval <duration>", Description: "minimum wall-clock interval between completed periodic full-tree audits (-1 disables the time guard)"},
		{Flags: "--cursor-timeout <duration>", Description: "independent timeout for events-cursor resolution"},
		{Flags: "--full-reconcile", Description: "force one full reconcile regardless of bootstrap-complete state (escape hatch)"},
		{Flags: "--websocket", Description: "enable websocket event streaming when available"},
		{Flags: "--low-memory", Description: "reduce mount memory use by omitting per-file public state and deferring content reads"},
		{Flags: "--pprof-addr <addr>", Description: "optional pprof listen address, e.g. 127.0.0.1:6060"},
		{Flags: "--memlog-interval <duration>", Description: "optional interval for logging runtime memory stats"},
		{Flags: "--background", Description: "detach and keep syncing in the background", DefaultValue: false},
		{Flags: "--pid-file <file>", Description: "pid file path for background mode"},
		{Flags: "--log-file <file>", Description: "log file path for background mode"},
		{Flags: "--daemonized", Description: "internal flag used by relayfile mount --background", DefaultValue: false},
		{Flags: "--once", Description: "run one sync cycle and exit", DefaultValue: false},
		{Flags: "--reset-after-clobber", Description: "acknowledge a mount-root clobber and authorize daemon to recreate the directory"},
		{Flags: "--rehome", Description: "allow re-homing an already-registered workspace mirror to a different LOCAL_DIR", DefaultValue: false},
	}
}

func mountSealCommands() []cliCommandSpec {
	sealOptions := func(timeoutDescription string) []cliOptionSpec {
		return []cliOptionSpec{
			{Flags: "--root <path>", Description: "absolute local mount root"},
			{Flags: "--timeout <duration>", Description: timeoutDescription},
			{Flags: "--json", Description: "emit the machine contract", DefaultValue: false},
		}
	}
	return []cliCommandSpec{
		{
			Name:        "checkpoint-seal",
			Description: "Seal a mount checkpoint for controller-driven cutover",
			Hidden:      true,
			flagSource:  "runMountCheckpointSeal",
			Options: []cliOptionSpec{
				{Flags: "--root <path>", Description: "absolute local mount root"},
				{Flags: "--lifecycle-id <id>", Description: "stable controller-persisted cutover lifecycle id"},
				{Flags: "--session <id>", Description: "live session identifier"},
				{Flags: "--generation <n>", Description: "strictly increasing migration generation", DefaultValue: 0},
				{Flags: "--timeout <duration>", Description: "checkpoint deadline", DefaultValue: "30s"},
				{Flags: "--ttl <duration>", Description: "server receipt TTL"},
				{Flags: "--json", Description: "emit the machine contract", DefaultValue: false},
			},
		},
		{
			Name:        "resume-seal",
			Description: "Resume a sealed mount checkpoint on the destination host",
			Hidden:      true,
			flagSource:  "runMountResumeSeal",
			Options:     sealOptions("resume readiness deadline"),
		},
		{
			Name:        "verify-seal",
			Description: "Verify a resumed mount checkpoint and recover if needed",
			Hidden:      true,
			flagSource:  "runMountVerifySeal",
			Options:     sealOptions("verification and recovery deadline"),
		},
		{
			Name:        "handback-seal",
			Description: "Drain and hand a verified mount back to its original host",
			Hidden:      true,
			flagSource:  "runMountHandbackSeal",
			Options:     sealOptions("final drain and handback deadline"),
		},
	}
}

// mountSealDispatch routes `relayfile mount <seal>` to the machine-contract
// seal commands. It is keyed by the same names mountSealCommands declares; the
// drift test asserts the two agree.
var mountSealDispatch = map[string]func(cliInvocation) error{
	"checkpoint-seal": func(inv cliInvocation) error {
		return runMountCheckpointSeal(inv.args, inv.stdout)
	},
	"resume-seal": func(inv cliInvocation) error {
		return runMountResumeSeal(inv.args, inv.stdin, inv.stdout)
	},
	"verify-seal": func(inv cliInvocation) error {
		return runMountVerifySeal(inv.args, inv.stdin, inv.stdout)
	},
	"handback-seal": func(inv cliInvocation) error {
		return runMountHandbackSeal(inv.args, inv.stdin, inv.stdout)
	},
}

// lookupCommand resolves a top-level command by name or alias.
func lookupCommand(name string) (cliCommandSpec, bool) {
	for _, command := range relayfileCommands() {
		if command.Name == name {
			return command, true
		}
		for _, alias := range command.Aliases {
			if alias == name {
				return command, true
			}
		}
	}
	return cliCommandSpec{}, false
}

// optionLongName extracts the long flag name from a commander-style flag
// string: "--path <glob>" yields "path". It returns "" when the string
// declares no long flag.
func optionLongName(flags string) string {
	match := optionLongNameRe.FindStringSubmatch(flags)
	if match == nil {
		return ""
	}
	return match[1]
}

var optionLongNameRe = regexp.MustCompile(`--([A-Za-z0-9][A-Za-z0-9-]*)`)

// publicCommandSpec strips the internal bookkeeping and the commands that are
// not part of the product surface, leaving exactly what the contract describes.
func publicCommandSpec() []cliCommandSpec {
	return filterInternalCommands(relayfileCommands())
}

func filterInternalCommands(commands []cliCommandSpec) []cliCommandSpec {
	public := make([]cliCommandSpec, 0, len(commands))
	for _, command := range commands {
		if command.internal {
			continue
		}
		command.dispatch = nil
		command.Subcommands = filterInternalCommands(command.Subcommands)
		public = append(public, command)
	}
	return public
}

// runCommandSpec emits the public command tree as JSON.
func runCommandSpec(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet(commandSpecCommandName, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "emit the command tree as JSON")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{"json": false})); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("usage: relayfile %s --json", commandSpecCommandName)
	}
	if !*asJSON {
		return errors.New("usage: relayfile " + commandSpecCommandName + " --json")
	}

	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(publicCommandSpec())
}

// commandNamesForUsage lists the routable top-level names, aliases included,
// for error messages.
func commandNamesForUsage() string {
	names := make([]string, 0)
	for _, command := range relayfileCommands() {
		if command.internal || command.Hidden {
			continue
		}
		names = append(names, command.Name)
	}
	return strings.Join(names, ", ")
}
