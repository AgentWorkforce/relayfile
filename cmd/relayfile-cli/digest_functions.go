package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentworkforce/relayfile/internal/digest"
	"github.com/agentworkforce/relayfile/internal/wasmrun"
)

const digestFunctionUsage = "usage: relayfile digest function {deploy|list|show|disable|logs|test} ..."

type digestFunctionDeployRequest struct {
	Slug        string               `json:"slug"`
	DisplayName string               `json:"displayName,omitempty"`
	Source      digestFunctionSource `json:"source"`
}

type digestFunctionSource struct {
	Runtime    string                     `json:"runtime"`
	Entrypoint string                     `json:"entrypoint"`
	Files      []digestFunctionSourceFile `json:"files"`
}

type digestFunctionSourceFile struct {
	Path     string `json:"path"`
	Contents string `json:"contents"`
}

type digestFunctionDeployResponse struct {
	DigestFunctionID string `json:"digestFunctionId"`
	Version          int    `json:"version"`
	Status           string `json:"status,omitempty"`
	SHA256           string `json:"sha256"`
}

type digestFunctionRecord struct {
	DigestFunctionID string          `json:"digestFunctionId,omitempty"`
	FunctionID       string          `json:"functionId,omitempty"`
	Name             string          `json:"name"`
	Version          int             `json:"version,omitempty"`
	Status           string          `json:"status,omitempty"`
	SHA256           string          `json:"sha256,omitempty"`
	ContentHash      string          `json:"contentHash,omitempty"`
	Bytes            int64           `json:"bytes,omitempty"`
	CreatedAt        string          `json:"createdAt,omitempty"`
	UpdatedAt        string          `json:"updatedAt,omitempty"`
	DeployedAt       string          `json:"deployedAt,omitempty"`
	DeployedBy       string          `json:"deployedBy,omitempty"`
	Entrypoint       string          `json:"entrypoint,omitempty"`
	Runtime          string          `json:"runtime,omitempty"`
	Source           string          `json:"source,omitempty"`
	Manifest         json.RawMessage `json:"manifest,omitempty"`
	LastRun          *digestLastRun  `json:"lastRun,omitempty"`
}

type digestLastRun struct {
	Timestamp  string `json:"timestamp,omitempty"`
	DurationMS int64  `json:"durationMs,omitempty"`
	Warning    string `json:"warning,omitempty"`
}

type digestFunctionListResponse struct {
	DigestFunctions []digestFunctionRecord `json:"digestFunctions"`
	Functions       []digestFunctionRecord `json:"functions"`
	NextCursor      string                 `json:"nextCursor,omitempty"`
}

type digestFunctionDisableResponse struct {
	DigestFunctionID string `json:"digestFunctionId,omitempty"`
	Name             string `json:"name,omitempty"`
	Status           string `json:"status"`
	DisabledAt       string `json:"disabledAt,omitempty"`
	AlreadyDisabled  bool   `json:"alreadyDisabled,omitempty"`
}

type digestFunctionLogEntry struct {
	InvocationID string `json:"invocationId,omitempty"`
	OccurredAt   string `json:"occurredAt,omitempty"`
	TS           string `json:"ts,omitempty"`
	Level        string `json:"level,omitempty"`
	Message      string `json:"message,omitempty"`
	DurationMS   int64  `json:"durationMs,omitempty"`
}

type digestFunctionLogsResponse struct {
	DigestFunctionID string                   `json:"digestFunctionId,omitempty"`
	Logs             []digestFunctionLogEntry `json:"logs"`
	NextCursor       string                   `json:"nextCursor,omitempty"`
}

type digestCommandConfig struct {
	workspace   string
	cloudAPIURL string
	cloudToken  string
}

var digestFunctionWASM = []byte{
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
	0x01, 0x05, 0x01, 0x60, 0x00, 0x01, 0x7f,
	0x03, 0x02, 0x01, 0x00,
	0x07, 0x0a, 0x01, 0x06, 0x64, 0x69, 0x67, 0x65, 0x73, 0x74, 0x00, 0x00,
	0x0a, 0x06, 0x01, 0x04, 0x00, 0x41, 0x2a, 0x0b,
}

func runDigest(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(digestFunctionUsage)
	}
	switch args[0] {
	case "function":
		return runDigestFunction(args[1:], stdout)
	default:
		return fmt.Errorf("unknown digest subcommand %q", args[0])
	}
}

func runDigestFunction(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(digestFunctionUsage)
	}
	switch args[0] {
	case "deploy":
		return runDigestFunctionDeploy(args[1:], stdout)
	case "list":
		return runDigestFunctionList(args[1:], stdout)
	case "show":
		return runDigestFunctionShow(args[1:], stdout)
	case "disable":
		return runDigestFunctionDisable(args[1:], stdout)
	case "logs":
		return runDigestFunctionLogs(args[1:], stdout)
	case "test":
		return runDigestFunctionTest(args[1:], stdout)
	default:
		return fmt.Errorf("unknown digest function subcommand %q", args[0])
	}
}

func runDigestFunctionDeploy(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("digest function deploy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := registerDigestCommandFlags(fs)
	name := fs.String("name", "", "digest function name")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(normalizeFlagArgs(args, digestFlagValues(map[string]bool{
		"name": true,
		"json": false,
	}))); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile digest function deploy PATH [--workspace NAME] [--name NAME] [--json]")
	}
	sourcePath := fs.Arg(0)
	if err := validateDigestFunctionSourcePath(sourcePath); err != nil {
		return err
	}
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read digest function %s: %w", sourcePath, err)
	}

	client, workspaceID, err := newDigestFunctionClient(cfg)
	if err != nil {
		return err
	}
	entrypoint := filepath.Base(sourcePath)
	slug := digestFunctionSlug(defaultIfBlank(strings.TrimSpace(*name), digestFunctionNameFromPath(sourcePath)))
	request := digestFunctionDeployRequest{
		Slug:        slug,
		DisplayName: defaultIfBlank(strings.TrimSpace(*name), digestFunctionNameFromPath(sourcePath)),
		Source: digestFunctionSource{
			Runtime:    "node20",
			Entrypoint: entrypoint,
			Files: []digestFunctionSourceFile{
				{
					Path:     entrypoint,
					Contents: string(source),
				},
			},
		},
	}
	var response digestFunctionDeployResponse
	if err := client.postJSON(context.Background(), digestFunctionPath(workspaceID), request, &response); err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, response)
	}
	fmt.Fprintf(stdout, "function_id: %s\n", defaultIfBlank(response.DigestFunctionID, "-"))
	fmt.Fprintf(stdout, "slug: %s\n", request.Slug)
	fmt.Fprintf(stdout, "version: %d\n", response.Version)
	fmt.Fprintf(stdout, "sha256: %s\n", defaultIfBlank(response.SHA256, "-"))
	if strings.TrimSpace(response.Status) != "" {
		fmt.Fprintf(stdout, "status: %s\n", response.Status)
	}
	return nil
}

func runDigestFunctionList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("digest function list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := registerDigestCommandFlags(fs)
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(normalizeFlagArgs(args, digestFlagValues(map[string]bool{
		"json": false,
	}))); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: relayfile digest function list [--workspace NAME] [--json]")
	}
	client, workspaceID, err := newDigestFunctionClient(cfg)
	if err != nil {
		return err
	}
	body, _, err := client.getBytes(context.Background(), digestFunctionPath(workspaceID))
	if err != nil {
		return err
	}
	records, err := decodeDigestFunctionList(body)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, records)
	}
	if len(records) == 0 {
		fmt.Fprintln(stdout, "no digest functions deployed")
		return nil
	}
	fmt.Fprintln(stdout, "name\tstatus\tsha256\tcreated_at\tbytes")
	for _, record := range records {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n",
			defaultIfBlank(record.Name, "-"),
			defaultIfBlank(record.Status, "unknown"),
			digestHashPrefix(record.digestHash()),
			defaultIfBlank(defaultIfBlank(record.CreatedAt, record.DeployedAt), "-"),
			formatDigestBytes(record.Bytes),
		)
	}
	return nil
}

func runDigestFunctionShow(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("digest function show", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := registerDigestCommandFlags(fs)
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(normalizeFlagArgs(args, digestFlagValues(map[string]bool{
		"json": false,
	}))); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile digest function show NAME [--workspace NAME] [--json]")
	}
	record, err := fetchDigestFunctionRecord(cfg, fs.Arg(0))
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, record)
	}
	fmt.Fprintf(stdout, "name: %s\n", defaultIfBlank(record.Name, fs.Arg(0)))
	fmt.Fprintf(stdout, "function_id: %s\n", defaultIfBlank(record.id(), "-"))
	if record.Version > 0 {
		fmt.Fprintf(stdout, "version: %d\n", record.Version)
	}
	fmt.Fprintf(stdout, "status: %s\n", defaultIfBlank(record.Status, "unknown"))
	fmt.Fprintf(stdout, "sha256: %s\n", defaultIfBlank(record.digestHash(), "-"))
	if record.Bytes > 0 {
		fmt.Fprintf(stdout, "bytes: %d\n", record.Bytes)
	}
	if record.Entrypoint != "" {
		fmt.Fprintf(stdout, "entrypoint: %s\n", record.Entrypoint)
	}
	if record.Runtime != "" {
		fmt.Fprintf(stdout, "runtime: %s\n", record.Runtime)
	}
	if record.LastRun != nil {
		fmt.Fprintf(stdout, "last_run: %s duration_ms=%d warning=%s\n",
			defaultIfBlank(record.LastRun.Timestamp, "-"),
			record.LastRun.DurationMS,
			defaultIfBlank(record.LastRun.Warning, "-"),
		)
	}
	if len(record.Manifest) > 0 {
		fmt.Fprintln(stdout, "manifest:")
		var pretty bytesLikeJSON
		if err := json.Unmarshal(record.Manifest, &pretty); err == nil {
			_ = writeJSON(stdout, pretty)
		} else {
			fmt.Fprintln(stdout, string(record.Manifest))
		}
	}
	if strings.TrimSpace(record.Source) != "" {
		fmt.Fprintln(stdout, "source:")
		fmt.Fprintln(stdout, record.Source)
	}
	return nil
}

func runDigestFunctionDisable(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("digest function disable", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := registerDigestCommandFlags(fs)
	if err := fs.Parse(normalizeFlagArgs(args, digestFlagValues(nil))); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile digest function disable NAME [--workspace NAME]")
	}
	client, workspaceID, err := newDigestFunctionClient(cfg)
	if err != nil {
		return err
	}
	var response digestFunctionDisableResponse
	name := fs.Arg(0)
	digestFunctionID, err := resolveDigestFunctionID(client, workspaceID, name)
	if err != nil {
		return err
	}
	if err := client.postJSON(context.Background(), digestFunctionPath(workspaceID)+"/"+url.PathEscape(digestFunctionID)+"/disable", map[string]any{}, &response); err != nil {
		return err
	}
	if response.Name == "" {
		response.Name = name
	}
	fmt.Fprintf(stdout, "name: %s\n", response.Name)
	fmt.Fprintf(stdout, "function_id: %s\n", defaultIfBlank(defaultIfBlank(response.DigestFunctionID, digestFunctionID), "-"))
	fmt.Fprintf(stdout, "status: %s\n", defaultIfBlank(response.Status, "disabled"))
	return nil
}

func runDigestFunctionLogs(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("digest function logs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := registerDigestCommandFlags(fs)
	tail := fs.Bool("tail", false, "request streamed logs")
	if err := fs.Parse(normalizeFlagArgs(args, digestFlagValues(map[string]bool{
		"tail": false,
	}))); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile digest function logs NAME [--workspace NAME] [--tail]")
	}
	client, workspaceID, err := newDigestFunctionClient(cfg)
	if err != nil {
		return err
	}
	digestFunctionID, err := resolveDigestFunctionID(client, workspaceID, fs.Arg(0))
	if err != nil {
		return err
	}
	path := digestFunctionPath(workspaceID) + "/" + url.PathEscape(digestFunctionID) + "/logs"
	if *tail {
		path += "?tail=true"
	}
	body, contentType, err := client.getBytes(context.Background(), path)
	if err != nil {
		return err
	}
	if strings.Contains(contentType, "application/json") {
		var response digestFunctionLogsResponse
		if err := json.Unmarshal(body, &response); err == nil {
			for _, entry := range response.Logs {
				fmt.Fprintf(stdout, "%s\t%s\t%s\n", defaultIfBlank(defaultIfBlank(entry.OccurredAt, entry.TS), "-"), defaultIfBlank(entry.Level, "info"), entry.Message)
			}
			return nil
		}
	}
	_, err = stdout.Write(body)
	if err == nil && len(body) > 0 && body[len(body)-1] != '\n' {
		_, err = fmt.Fprintln(stdout)
	}
	return err
}

func runDigestFunctionTest(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("digest function test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fixturePath := fs.String("fixture", "", "change-event fixture JSON path")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"fixture": true,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile digest function test PATH [--fixture EVENTS_JSON]")
	}

	sourcePath := fs.Arg(0)
	if err := validateDigestFunctionSourcePath(sourcePath); err != nil {
		return err
	}
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read digest function %s: %w", sourcePath, err)
	}
	if err := validateDigestFunctionNetworkIsolation(string(source)); err != nil {
		return err
	}

	var events []wasmrun.ChangeEvent
	if strings.TrimSpace(*fixturePath) != "" {
		events, err = readDigestFunctionFixture(*fixturePath)
		if err != nil {
			return err
		}
	}

	name := digestFunctionNameFromPath(sourcePath)
	section, warnings, err := runDigestFunctionSource(context.Background(), name, sourcePath, events)
	if err != nil {
		return err
	}
	rendered := renderDigestFunctionResult(name, section, warnings, len(events))
	fmt.Fprint(stdout, rendered)
	if !strings.HasSuffix(rendered, "\n") {
		fmt.Fprintln(stdout)
	}
	return nil
}

func runDigestFunctionSource(ctx context.Context, name, sourcePath string, events []wasmrun.ChangeEvent) (*wasmrun.DigestSection, []wasmrun.Warning, error) {
	engine, err := wasmrun.NewEngine(ctx, wasmrun.Options{
		EventsProvider: staticDigestFunctionEvents{events: events},
		Runner:         digestFunctionSourceRunner{sourcePath: sourcePath},
		HardTimeout:    wasmrun.DefaultHardTimeout,
	})
	if err != nil {
		return nil, nil, err
	}
	defer engine.Close(ctx)

	env := wasmrun.SignedModule{
		Manifest: wasmrun.Manifest{
			FunctionID:  name,
			Name:        name,
			ContentHash: wasmrun.ContentHash(digestFunctionWASM),
			EntryPoint:  "digest",
			SDKVersion:  "m1-local-test",
		},
		Wasm: digestFunctionWASM,
	}
	module, err := engine.Compile(ctx, env)
	if err != nil {
		return nil, nil, err
	}
	return engine.Invoke(ctx, module, wasmrun.DigestContext{
		WorkspaceID: "preview",
		FunctionID:  name,
		WindowFrom:  time.Time{},
		WindowTo:    time.Time{},
		PathScope:   []string{"*"},
	})
}

func renderDigestFunctionResult(name string, section *wasmrun.DigestSection, warnings []wasmrun.Warning, eventCount int) string {
	window := digest.Window{
		WorkspaceID: "preview",
		Covers:      "preview",
		Date:        time.Time{},
		From:        time.Time{},
		To:          time.Time{},
		GeneratedAt: time.Time{},
		TotalEvents: eventCount,
	}
	d := digest.Digest{
		Window: window,
	}
	if section != nil {
		renderedSection := digest.DigestSection{
			Provider: defaultIfBlank(section.Provider, name),
			Source:   digest.SectionSourceCustomer,
		}
		for _, bullet := range section.Bullets {
			renderedSection.Bullets = append(renderedSection.Bullets, digest.DigestBullet{
				Text:          bullet.Text,
				CanonicalPath: bullet.CanonicalPath,
			})
		}
		d.Sections = []digest.DigestSection{renderedSection}
	}
	for _, warning := range warnings {
		d.Warnings = append(d.Warnings, digest.DigestWarning{
			Function: defaultIfBlank(warning.Function, name),
			Reason:   string(warning.Kind),
		})
	}
	return digest.RenderMarkdown(d)
}

type staticDigestFunctionEvents struct {
	events []wasmrun.ChangeEvent
}

func (p staticDigestFunctionEvents) ChangeEvents(context.Context, string, wasmrun.ChangeEventsFilter) ([]wasmrun.ChangeEvent, error) {
	return append([]wasmrun.ChangeEvent(nil), p.events...), nil
}

type digestFunctionSourceRunner struct {
	sourcePath string
}

func (r digestFunctionSourceRunner) Run(ctx context.Context, req wasmrun.Invocation) (*wasmrun.DigestSection, error) {
	events, err := req.ChangeEvents(ctx, wasmrun.ChangeEventsFilter{})
	if err != nil {
		return nil, err
	}
	input := map[string]any{
		"workspaceId": req.DigestContext.WorkspaceID,
		"functionId":  req.DigestContext.FunctionID,
		"provider":    req.DigestContext.FunctionID,
		"events":      events,
		"window": map[string]any{
			"from": req.DigestContext.WindowFrom.UTC().Format(time.RFC3339),
			"to":   req.DigestContext.WindowTo.UTC().Format(time.RFC3339),
		},
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}

	runner, err := digestFunctionJSRunner(filepath.Ext(r.sourcePath))
	if err != nil {
		return nil, err
	}
	scriptPath, cleanup, err := writeDigestFunctionRunnerScript()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	cmd := exec.CommandContext(ctx, runner, scriptPath, r.sourcePath)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("run digest function %s: %s", r.sourcePath, detail)
	}

	var section *wasmrun.DigestSection
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &section); err != nil {
		return nil, fmt.Errorf("parse digest function output: %w", err)
	}
	return section, nil
}

func digestFunctionJSRunner(ext string) (string, error) {
	if runner := findLocalExecutable("tsx"); runner != "" {
		return runner, nil
	}
	if strings.EqualFold(ext, ".ts") {
		return "", errors.New("digest function test requires the local tsx runner for TypeScript sources; run npm install or pass a .js source")
	}
	if runner, err := exec.LookPath("node"); err == nil {
		return runner, nil
	}
	return "", errors.New("digest function test requires node or tsx on PATH")
}

func findLocalExecutable(name string) string {
	if cwd, err := os.Getwd(); err == nil {
		for dir := cwd; ; dir = filepath.Dir(dir) {
			candidates := []string{
				filepath.Join(dir, "node_modules", ".bin", name),
				filepath.Join(dir, "packages", "sdk", "typescript", "node_modules", ".bin", name),
			}
			for _, path := range candidates {
				if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
					return path
				}
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
		}
	}
	if runner, err := exec.LookPath(name); err == nil {
		return runner
	}
	return ""
}

func writeDigestFunctionRunnerScript() (string, func(), error) {
	f, err := os.CreateTemp("", "relayfile-digest-function-test-*.mjs")
	if err != nil {
		return "", func() {}, err
	}
	path := f.Name()
	cleanup := func() {
		_ = os.Remove(path)
	}
	if _, err := f.WriteString(digestFunctionRunnerScript); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

const digestFunctionRunnerScript = `
import { pathToFileURL } from "node:url";

const sourcePath = process.argv[2];
let input = "";
for await (const chunk of process.stdin) {
  input += chunk;
}

delete globalThis.fetch;
delete globalThis.XMLHttpRequest;
delete globalThis.WebSocket;
delete globalThis.EventSource;

const payload = JSON.parse(input);
const allEvents = Object.freeze(
  (payload.events ?? []).map((event) => Object.freeze(event)),
);

function globToRegExp(pattern) {
  let out = "^";
  for (const ch of pattern) {
    if (ch === "*") {
      out += "[^/]*";
    } else if (ch === "?") {
      out += "[^/]";
    } else if ("\\.+^$|()[]{}".includes(ch)) {
      out += "\\\\" + ch;
    } else {
      out += ch;
    }
  }
  return new RegExp(out + "$");
}

function matchesPath(pattern, value) {
  if (pattern === "*" || pattern === "**") return true;
  if (globToRegExp(pattern).test(value)) return true;
  if (pattern.endsWith("/*")) {
    return value.startsWith(pattern.slice(0, -1));
  }
  return pattern === value;
}

function eventPath(event) {
  if (event && typeof event.path === "string" && event.path !== "") return event.path;
  if (event && event.resource && typeof event.resource.path === "string") return event.resource.path;
  return "";
}

const ctx = Object.freeze({
  workspaceId: payload.workspaceId,
  functionId: payload.functionId,
  provider: payload.provider,
  window: Object.freeze({
    from: payload.window?.from ?? "",
    to: payload.window?.to ?? "",
  }),
  async changeEvents(filter) {
    const paths = filter?.paths;
    const providers = filter?.providers;
    let result = allEvents;
    if (Array.isArray(paths) && paths.length > 0) {
      result = result.filter((event) => {
        const p = eventPath(event);
        return paths.some((pattern) => matchesPath(pattern, p));
      });
    }
    if (Array.isArray(providers) && providers.length > 0) {
      result = result.filter((event) => providers.includes(event.provider));
    }
    return result;
  },
});

const mod = await import(pathToFileURL(sourcePath).href + "?relayfile_digest_test=" + Date.now());
const handler = mod.digest ?? mod.default;
if (typeof handler !== "function") {
  throw new Error("digest function must export a digest(ctx) function or default handler");
}

const result = await handler(ctx);
process.stdout.write(JSON.stringify(result ?? null));
`

type bytesLikeJSON map[string]any

func registerDigestCommandFlags(fs *flag.FlagSet) *digestCommandConfig {
	cfg := &digestCommandConfig{}
	fs.StringVar(&cfg.workspace, "workspace", "", "workspace name or id")
	fs.StringVar(&cfg.cloudAPIURL, "cloud-api-url", envOrDefault("RELAYFILE_CLOUD_API_URL", defaultCloudAPIURL), "Relayfile Cloud API URL")
	fs.StringVar(&cfg.cloudToken, "cloud-token", strings.TrimSpace(os.Getenv("RELAYFILE_CLOUD_TOKEN")), "Relayfile Cloud access token override")
	return cfg
}

func digestFlagValues(extra map[string]bool) map[string]bool {
	flags := map[string]bool{
		"workspace":     true,
		"cloud-api-url": true,
		"cloud-token":   true,
	}
	for name, takesValue := range extra {
		flags[name] = takesValue
	}
	return flags
}

func newDigestFunctionClient(cfg *digestCommandConfig) (*apiClient, string, error) {
	record, err := resolveWorkspaceRecord(strings.TrimSpace(cfg.workspace))
	if err != nil {
		return nil, "", err
	}
	cloudCreds, err := ensureCloudCredentials(strings.TrimSpace(cfg.cloudAPIURL), strings.TrimSpace(cfg.cloudToken), 5*time.Minute, false, io.Discard)
	if err != nil {
		return nil, "", err
	}
	workspaceID := strings.TrimSpace(record.ID)
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(record.Name)
	}
	client, err := newAPIClient(cloudCreds.APIURL, cloudCreds.AccessToken)
	if err != nil {
		return nil, "", err
	}
	return client, workspaceID, nil
}

func fetchDigestFunctionRecord(cfg *digestCommandConfig, name string) (digestFunctionRecord, error) {
	client, workspaceID, err := newDigestFunctionClient(cfg)
	if err != nil {
		return digestFunctionRecord{}, err
	}
	digestFunctionID, err := resolveDigestFunctionID(client, workspaceID, name)
	if err != nil {
		return digestFunctionRecord{}, err
	}
	var record digestFunctionRecord
	err = client.getJSON(context.Background(), digestFunctionPath(workspaceID)+"/"+url.PathEscape(digestFunctionID), &record)
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return digestFunctionRecord{}, fmt.Errorf("digest function %q was not found in workspace %s", name, workspaceID)
		}
		return digestFunctionRecord{}, err
	}
	if record.Name == "" {
		record.Name = name
	}
	return record, nil
}

func resolveDigestFunctionID(client *apiClient, workspaceID, name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", errors.New("digest function name is required")
	}
	var record digestFunctionRecord
	err := client.getJSON(context.Background(), digestFunctionPath(workspaceID)+"/"+url.PathEscape(trimmed), &record)
	if err == nil {
		if id := record.id(); id != "" {
			return id, nil
		}
		return trimmed, nil
	}
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 404 {
		return "", err
	}
	body, _, err := client.getBytes(context.Background(), digestFunctionPath(workspaceID))
	if err != nil {
		return "", err
	}
	records, err := decodeDigestFunctionList(body)
	if err != nil {
		return "", err
	}
	for _, candidate := range records {
		if candidate.Name == trimmed || candidate.id() == trimmed {
			if id := candidate.id(); id != "" {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf("digest function %q was not found in workspace %s", name, workspaceID)
}

func digestFunctionPath(workspaceID string) string {
	return "/api/v1/workspaces/" + url.PathEscape(workspaceID) + "/digest-functions"
}

func decodeDigestFunctionList(body []byte) ([]digestFunctionRecord, error) {
	var response digestFunctionListResponse
	if err := json.Unmarshal(body, &response); err == nil && response.DigestFunctions != nil {
		return response.DigestFunctions, nil
	}
	if err := json.Unmarshal(body, &response); err == nil && response.Functions != nil {
		return response.Functions, nil
	}
	var records []digestFunctionRecord
	if err := json.Unmarshal(body, &records); err != nil {
		return nil, fmt.Errorf("parse digest function list response: %w", err)
	}
	return records, nil
}

func validateDigestFunctionSourcePath(path string) error {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".ts" && ext != ".js" {
		return fmt.Errorf("only .ts/.js sources are supported in M1: %s", path)
	}
	return nil
}

func digestFunctionNameFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func digestFunctionSlug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var out strings.Builder
	lastHyphen := false
	for _, ch := range name {
		valid := (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')
		if valid {
			out.WriteRune(ch)
			lastHyphen = false
			continue
		}
		if !lastHyphen && out.Len() > 0 {
			out.WriteByte('-')
			lastHyphen = true
		}
	}
	slug := strings.Trim(out.String(), "-")
	if slug == "" {
		return "digest-function"
	}
	if len(slug) > 63 {
		slug = strings.TrimRight(slug[:63], "-")
	}
	return defaultIfBlank(slug, "digest-function")
}

func digestFunctionContentHash(source []byte) string {
	sum := sha256.Sum256(source)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func digestHashPrefix(hash string) string {
	hash = strings.TrimSpace(hash)
	if strings.HasPrefix(hash, "sha256:") {
		hash = strings.TrimPrefix(hash, "sha256:")
	}
	if len(hash) > 12 {
		return hash[:12]
	}
	return defaultIfBlank(hash, "-")
}

func formatDigestBytes(bytes int64) string {
	if bytes <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", bytes)
}

func (r digestFunctionRecord) id() string {
	return defaultIfBlank(r.DigestFunctionID, r.FunctionID)
}

func (r digestFunctionRecord) digestHash() string {
	return defaultIfBlank(r.SHA256, r.ContentHash)
}

func readDigestFunctionFixture(path string) ([]wasmrun.ChangeEvent, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read digest fixture %s: %w", path, err)
	}
	var events []wasmrun.ChangeEvent
	if err := json.Unmarshal(payload, &events); err != nil {
		return nil, fmt.Errorf("parse digest fixture %s: expected JSON array: %w", path, err)
	}
	return events, nil
}

func validateDigestFunctionNetworkIsolation(source string) error {
	scannable := stripJavaScriptCommentsAndStrings(source)
	for _, identifier := range []string{"fetch", "XMLHttpRequest", "WebSocket", "EventSource"} {
		if containsJavaScriptIdentifier(scannable, identifier) {
			return fmt.Errorf("digest function test failed sandbox preflight: network API %q is not available; custom digest functions can only use DigestContext", identifier)
		}
	}
	return nil
}

func stripJavaScriptCommentsAndStrings(source string) string {
	var out strings.Builder
	out.Grow(len(source))
	for i := 0; i < len(source); {
		if strings.HasPrefix(source[i:], "//") {
			out.WriteString("  ")
			i += 2
			for i < len(source) && source[i] != '\n' {
				out.WriteByte(' ')
				i++
			}
			continue
		}
		if strings.HasPrefix(source[i:], "/*") {
			out.WriteString("  ")
			i += 2
			for i < len(source) {
				if strings.HasPrefix(source[i:], "*/") {
					out.WriteString("  ")
					i += 2
					break
				}
				if source[i] == '\n' {
					out.WriteByte('\n')
				} else {
					out.WriteByte(' ')
				}
				i++
			}
			continue
		}
		if source[i] == '\'' || source[i] == '"' || source[i] == '`' {
			quote := source[i]
			out.WriteByte(' ')
			i++
			for i < len(source) {
				if source[i] == '\\' {
					out.WriteByte(' ')
					i++
					if i < len(source) {
						out.WriteByte(' ')
						i++
					}
					continue
				}
				if source[i] == quote {
					out.WriteByte(' ')
					i++
					break
				}
				if source[i] == '\n' {
					out.WriteByte('\n')
				} else {
					out.WriteByte(' ')
				}
				i++
			}
			continue
		}
		out.WriteByte(source[i])
		i++
	}
	return out.String()
}

func containsJavaScriptIdentifier(source, identifier string) bool {
	for offset := 0; ; {
		index := strings.Index(source[offset:], identifier)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(identifier)
		if !isJavaScriptIdentifierByte(source, start-1) && !isJavaScriptIdentifierByte(source, end) {
			return true
		}
		offset = end
	}
}

func isJavaScriptIdentifierByte(source string, index int) bool {
	if index < 0 || index >= len(source) {
		return false
	}
	ch := source[index]
	return ch == '_' || ch == '$' || (ch >= '0' && ch <= '9') || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z')
}
