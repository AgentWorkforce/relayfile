package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	mathrand "math/rand/v2"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/agentworkforce/relayfile/internal/mountsync"
)

const (
	defaultServerURL        = "https://relayfile-api.agentworkforce.workers.dev"
	configDirName           = ".relayfile"
	websocketReconcileEvery = 10
)

type credentials struct {
	Server       string `json:"server"`
	Token        string `json:"token"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
	UpdatedAt    string `json:"updatedAt,omitempty"`
}

type workspaceCatalog struct {
	Default    string            `json:"default,omitempty"`
	Workspaces []workspaceRecord `json:"workspaces"`
}

type workspaceRecord struct {
	Name       string `json:"name"`
	ID         string `json:"id,omitempty"`
	CreatedAt  string `json:"createdAt"`
	LastUsedAt string `json:"lastUsedAt,omitempty"`
}

type apiClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type bulkWriteRequest struct {
	Files []bulkWriteFile `json:"files"`
}

type bulkWriteFile struct {
	Path        string `json:"path"`
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
	Encoding    string `json:"encoding,omitempty"`
}

type bulkWriteResponse struct {
	Written       int              `json:"written"`
	ErrorCount    int              `json:"errorCount"`
	Errors        []bulkWriteError `json:"errors"`
	CorrelationID string           `json:"correlationId"`
}

type bulkWriteError struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type syncStatusResponse struct {
	WorkspaceID string               `json:"workspaceId"`
	Providers   []syncProviderStatus `json:"providers"`
}

type treeResponse struct {
	Path       string      `json:"path"`
	Entries    []treeEntry `json:"entries"`
	NextCursor *string     `json:"nextCursor"`
}

type treeEntry struct {
	Path             string `json:"path"`
	Type             string `json:"type"`
	Revision         string `json:"revision"`
	Provider         string `json:"provider,omitempty"`
	ProviderObjectID string `json:"providerObjectId,omitempty"`
	Size             *int64 `json:"size,omitempty"`
	UpdatedAt        string `json:"updatedAt,omitempty"`
}

type syncProviderStatus struct {
	Provider              string         `json:"provider"`
	Status                string         `json:"status"`
	Cursor                *string        `json:"cursor"`
	WatermarkTs           *string        `json:"watermarkTs"`
	LagSeconds            int            `json:"lagSeconds"`
	LastError             *string        `json:"lastError"`
	FailureCodes          map[string]int `json:"failureCodes"`
	DeadLetteredEnvelopes int            `json:"deadLetteredEnvelopes"`
	DeadLetteredOps       int            `json:"deadLetteredOps"`
}

type exportedFile struct {
	Path        string `json:"path"`
	Revision    string `json:"revision"`
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
	Encoding    string `json:"encoding,omitempty"`
	LastEdited  string `json:"lastEditedAt,omitempty"`
}

type readFileResponse struct {
	Path             string            `json:"path"`
	Revision         string            `json:"revision"`
	ContentType      string            `json:"contentType"`
	Content          string            `json:"content"`
	Encoding         string            `json:"encoding,omitempty"`
	Provider         string            `json:"provider,omitempty"`
	ProviderObjectID string            `json:"providerObjectId,omitempty"`
	LastEdited       string            `json:"lastEditedAt,omitempty"`
	Semantics        map[string]any    `json:"semantics,omitempty"`
	Properties       map[string]string `json:"properties,omitempty"`
}

type adminWorkspaceList struct {
	WorkspaceIDs []string        `json:"workspaceIds"`
	Workspaces   json.RawMessage `json:"workspaces"`
}

type apiError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *apiError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("http %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("http %d %s: %s", e.StatusCode, e.Code, e.Message)
}

func main() {
	log.SetFlags(0)
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stderr)
		return nil
	}

	switch args[0] {
	case "login":
		return runLogin(args[1:], stdin, stdout)
	case "workspace":
		return runWorkspace(args[1:], stdin, stdout)
	case "mount":
		return runMount(args[1:])
	case "tree", "ls":
		return runTree(args[1:], stdout)
	case "read", "cat":
		return runRead(args[1:], stdout)
	case "seed":
		return runSeed(args[1:], stdout)
	case "export":
		return runExport(args[1:], stdout)
	case "status":
		return runStatus(args[1:], stdout)
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `relayfile is the RelayFile CLI.

Usage:
  relayfile login --server URL [--token TOKEN]
  relayfile workspace create NAME
  relayfile workspace use NAME
  relayfile workspace list
  relayfile workspace delete NAME [--yes]
  relayfile mount [WORKSPACE] [LOCAL_DIR]
  relayfile tree [WORKSPACE] [PATH] [--depth N]
  relayfile read [WORKSPACE] PATH
  relayfile seed [WORKSPACE] [DIR]
  relayfile export [WORKSPACE] --format FORMAT [--output FILE]
  relayfile status [WORKSPACE]

Subcommands:
  login       Store credentials in ~/.relayfile/credentials.json
  workspace   Create, select, list, or delete locally tracked workspaces
  mount       Mirror a remote workspace to a local directory
  tree        List a remote workspace path
  read        Print a remote file's content
  seed        Upload a directory tree with bulk writes
  export      Export a workspace as json, tar, or patch
  status      Show sync status for a workspace`)
}

func runLogin(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", envOrDefault("RELAYFILE_SERVER", envOrDefault("RELAYFILE_BASE_URL", defaultServerURL)), "relayfile server URL")
	token := fs.String("token", strings.TrimSpace(os.Getenv("RELAYFILE_TOKEN")), "API token")
	if err := fs.Parse(args); err != nil {
		return err
	}

	serverValue := strings.TrimSpace(*server)
	if serverValue == "" {
		serverValue = defaultServerURL
	}
	tokenValue := strings.TrimSpace(*token)
	if tokenValue == "" {
		prompted, err := promptLine(stdin, stdout, "API key: ")
		if err != nil {
			return err
		}
		tokenValue = strings.TrimSpace(prompted)
	}
	if tokenValue == "" {
		return errors.New("token is required")
	}

	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(serverValue, "/")+"/health", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tokenValue)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check failed with status %d", resp.StatusCode)
	}

	creds := credentials{
		Server:    strings.TrimRight(serverValue, "/"),
		Token:     tokenValue,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := saveCredentials(creds); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Stored credentials for %s\n", creds.Server)
	return nil
}

func runWorkspace(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("workspace subcommand is required: create, use, list, or delete")
	}
	switch args[0] {
	case "create":
		return runWorkspaceCreate(args[1:], stdout)
	case "use":
		return runWorkspaceUse(args[1:], stdout)
	case "list":
		return runWorkspaceList(args[1:], stdout)
	case "delete":
		return runWorkspaceDelete(args[1:], stdin, stdout)
	default:
		return fmt.Errorf("unknown workspace subcommand %q", args[0])
	}
}

func runWorkspaceCreate(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("workspace create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile workspace create NAME")
	}

	name := strings.TrimSpace(fs.Arg(0))
	if name == "" {
		return errors.New("workspace name is required")
	}
	if _, err := loadCredentials(); err != nil {
		return err
	}
	record, err := upsertWorkspace(name)
	if err != nil {
		return err
	}
	workspaceID := record.ID
	if workspaceID == "" {
		workspaceID = name
	}
	fmt.Fprintf(stdout, "Workspace %s ready (id: %s)\n", record.Name, workspaceID)
	return nil
}

func runWorkspaceUse(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("workspace use", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile workspace use NAME")
	}

	record, err := setDefaultWorkspace(fs.Arg(0))
	if err != nil {
		return err
	}
	workspaceID := record.ID
	if workspaceID == "" {
		workspaceID = record.Name
	}
	fmt.Fprintf(stdout, "Default workspace set to %s (id: %s)\n", record.Name, workspaceID)
	return nil
}

func runWorkspaceList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("workspace list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "relayfile server URL override")
	token := fs.String("token", "", "relayfile token override")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server": true,
		"token":  true,
	})); err != nil {
		return err
	}

	creds, _ := loadCredentials()
	tokenValue := resolveToken(*token, creds)
	client, err := newAPIClient(resolveServer(*server, creds), tokenValue)
	if err == nil {
		var remote adminWorkspaceList
		err = client.getJSON(context.Background(), "/v1/admin/workspaces", &remote)
		if err != nil {
			err = client.getJSON(context.Background(), "/v1/admin/sync", &remote)
		}
		if err == nil {
			names := remoteWorkspaceNames(remote)
			if len(names) > 0 {
				for _, name := range names {
					fmt.Fprintln(stdout, name)
				}
				return nil
			}
		}
	}

	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return err
	}
	for _, name := range workspaceCatalogNames(catalog, tokenValue) {
		fmt.Fprintln(stdout, name)
	}
	return nil
}

func runWorkspaceDelete(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("workspace delete", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	yes := fs.Bool("yes", false, "skip confirmation prompt")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"yes": false,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: relayfile workspace delete NAME [--yes]")
	}

	name := strings.TrimSpace(fs.Arg(0))
	if name == "" {
		return errors.New("workspace name is required")
	}
	if !*yes {
		answer, err := promptLine(stdin, stdout, fmt.Sprintf("Delete workspace %q from local config? [y/N]: ", name))
		if err != nil {
			return err
		}
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(stdout, "Aborted")
			return nil
		}
	}

	removed, err := removeWorkspace(name)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("workspace %q not found in %s", name, workspacesPath())
	}
	fmt.Fprintf(stdout, "Removed workspace %s from %s\n", name, workspacesPath())
	return nil
}

func runMount(args []string) error {
	fs := flag.NewFlagSet("mount", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	creds, _ := loadCredentials()
	server := fs.String("server", resolveServer("", creds), "relayfile server URL")
	token := fs.String("token", resolveToken("", creds), "bearer token")
	remotePath := fs.String("remote-path", envOrDefault("RELAYFILE_REMOTE_PATH", "/"), "remote root path")
	eventProvider := fs.String("provider", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_PROVIDER")), "event provider filter")
	stateFile := fs.String("state-file", strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_STATE_FILE")), "state file path")
	localDirFlag := fs.String("local-dir", "", "local mirror directory")
	interval := fs.Duration("interval", durationEnv("RELAYFILE_MOUNT_INTERVAL", 2*time.Second), "sync interval")
	intervalJitter := fs.Float64("interval-jitter", floatEnv("RELAYFILE_MOUNT_INTERVAL_JITTER", 0.2), "sync interval jitter ratio (0.0-1.0)")
	timeout := fs.Duration("timeout", durationEnv("RELAYFILE_MOUNT_TIMEOUT", 15*time.Second), "per-sync timeout")
	websocketEnabled := fs.Bool("websocket", boolEnv("RELAYFILE_MOUNT_WEBSOCKET", true), "enable websocket event streaming when available")
	once := fs.Bool("once", false, "run one sync cycle and exit")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server":          true,
		"token":           true,
		"remote-path":     true,
		"provider":        true,
		"state-file":      true,
		"interval":        true,
		"interval-jitter": true,
		"timeout":         true,
		"websocket":       false,
		"once":            false,
		"local-dir":       true,
	})); err != nil {
		return err
	}
	if fs.NArg() > 2 {
		return errors.New("usage: relayfile mount [WORKSPACE] [LOCAL_DIR]")
	}

	workspaceID := ""
	localDir := "."
	tokenValue := strings.TrimSpace(*token)
	if tokenValue == "" {
		return errors.New("token is required; run relayfile login or pass --token")
	}
	var err error
	switch fs.NArg() {
	case 0:
		workspaceID, err = resolveWorkspaceIDWithToken("", tokenValue)
		localDir = strings.TrimSpace(*localDirFlag)
	case 1:
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
		localDir = strings.TrimSpace(*localDirFlag)
	case 2:
		if strings.TrimSpace(*localDirFlag) != "" {
			return errors.New("local directory specified twice")
		}
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
		localDir = fs.Arg(1)
	}
	if err != nil {
		return err
	}
	if localDir == "" {
		localDir = "."
	}
	absLocalDir, err := filepath.Abs(localDir)
	if err != nil {
		return err
	}

	if *interval <= 0 {
		*interval = 2 * time.Second
	}
	if *timeout <= 0 {
		*timeout = 15 * time.Second
	}
	*intervalJitter = clampJitterRatio(*intervalJitter)

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := mountsync.NewHTTPClient(*server, tokenValue, &http.Client{Timeout: *timeout})
	syncer, err := mountsync.NewSyncer(client, mountsync.SyncerOptions{
		WorkspaceID:   workspaceID,
		RemoteRoot:    *remotePath,
		EventProvider: strings.TrimSpace(*eventProvider),
		LocalRoot:     absLocalDir,
		StateFile:     strings.TrimSpace(*stateFile),
		WebSocket:     boolPtr(*websocketEnabled),
		RootCtx:       rootCtx,
		Logger:        log.Default(),
	})
	if err != nil {
		return fmt.Errorf("failed to initialize mount syncer: %w", err)
	}
	if _, err := upsertWorkspace(workspaceID); err != nil {
		return err
	}

	return runMountLoop(rootCtx, syncer, *timeout, *interval, *intervalJitter, *websocketEnabled, *once)
}

func runTree(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("tree", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "relayfile server URL override")
	token := fs.String("token", "", "relayfile token override")
	pathFlag := fs.String("path", "/", "remote path to list")
	depth := fs.Int("depth", 1, "tree depth")
	jsonOutput := fs.Bool("json", false, "print the raw JSON response")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server": true,
		"token":  true,
		"path":   true,
		"depth":  true,
		"json":   false,
	})); err != nil {
		return err
	}
	if fs.NArg() > 2 {
		return errors.New("usage: relayfile tree [WORKSPACE] [PATH] [--depth N]")
	}

	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	tokenValue := resolveToken(*token, creds)
	client, err := newAPIClient(resolveServer(*server, creds), tokenValue)
	if err != nil {
		return err
	}

	remotePath := strings.TrimSpace(*pathFlag)
	var workspaceID string
	switch fs.NArg() {
	case 0:
		workspaceID, err = resolveWorkspaceIDWithToken("", tokenValue)
	case 1:
		arg := strings.TrimSpace(fs.Arg(0))
		if strings.HasPrefix(arg, "/") {
			workspaceID, err = resolveWorkspaceIDWithToken("", tokenValue)
			remotePath = arg
		} else {
			workspaceID, err = resolveWorkspaceIDWithToken(arg, tokenValue)
		}
	case 2:
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
		remotePath = strings.TrimSpace(fs.Arg(1))
	}
	if err != nil {
		return err
	}
	if remotePath == "" {
		remotePath = "/"
	}
	if *depth < 0 {
		return errors.New("depth must be greater than or equal to 0")
	}

	query := url.Values{}
	query.Set("path", remotePath)
	query.Set("depth", strconv.Itoa(*depth))
	body, _, err := client.getBytes(context.Background(), fmt.Sprintf("/v1/workspaces/%s/fs/tree?%s", url.PathEscape(workspaceID), query.Encode()))
	if err != nil {
		return err
	}
	if _, err := upsertWorkspace(workspaceID); err != nil {
		return err
	}
	if *jsonOutput {
		return writePrettyJSON(stdout, body)
	}

	var tree treeResponse
	if err := json.Unmarshal(body, &tree); err != nil {
		return err
	}
	if strings.TrimSpace(tree.Path) == "" {
		tree.Path = remotePath
	}
	fmt.Fprintf(stdout, "Tree %s\n", tree.Path)
	if len(tree.Entries) == 0 {
		fmt.Fprintln(stdout, "(empty)")
		return nil
	}
	for _, entry := range tree.Entries {
		entryType := strings.TrimSpace(entry.Type)
		if entryType == "" {
			entryType = "file"
		}
		fmt.Fprintf(stdout, "%-4s %s", entryType, entry.Path)
		if entry.Provider != "" {
			fmt.Fprintf(stdout, "  provider=%s", entry.Provider)
		}
		if entry.Size != nil {
			fmt.Fprintf(stdout, "  size=%d", *entry.Size)
		}
		if entry.Revision != "" {
			fmt.Fprintf(stdout, "  rev=%s", entry.Revision)
		}
		fmt.Fprintln(stdout)
	}
	if tree.NextCursor != nil && strings.TrimSpace(*tree.NextCursor) != "" {
		fmt.Fprintf(stdout, "next cursor: %s\n", strings.TrimSpace(*tree.NextCursor))
	}
	return nil
}

func runRead(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "relayfile server URL override")
	token := fs.String("token", "", "relayfile token override")
	output := fs.String("output", "-", "output file path or - for stdout")
	jsonOutput := fs.Bool("json", false, "print the raw JSON response")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server": true,
		"token":  true,
		"output": true,
		"json":   false,
	})); err != nil {
		return err
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		return errors.New("usage: relayfile read [WORKSPACE] PATH")
	}

	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	tokenValue := resolveToken(*token, creds)
	client, err := newAPIClient(resolveServer(*server, creds), tokenValue)
	if err != nil {
		return err
	}

	var workspaceID string
	var remotePath string
	if fs.NArg() == 1 {
		workspaceID, err = resolveWorkspaceIDWithToken("", tokenValue)
		remotePath = strings.TrimSpace(fs.Arg(0))
	} else {
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
		remotePath = strings.TrimSpace(fs.Arg(1))
	}
	if err != nil {
		return err
	}
	if remotePath == "" {
		return errors.New("path is required")
	}

	query := url.Values{}
	query.Set("path", remotePath)
	body, _, err := client.getBytes(context.Background(), fmt.Sprintf("/v1/workspaces/%s/fs/file?%s", url.PathEscape(workspaceID), query.Encode()))
	if err != nil {
		return err
	}
	if _, err := upsertWorkspace(workspaceID); err != nil {
		return err
	}
	if *jsonOutput {
		return writePrettyJSON(stdout, body)
	}

	var file readFileResponse
	if err := json.Unmarshal(body, &file); err != nil {
		return err
	}
	content := []byte(file.Content)
	if strings.EqualFold(strings.TrimSpace(file.Encoding), "base64") {
		decoded, err := base64.StdEncoding.DecodeString(file.Content)
		if err != nil {
			return fmt.Errorf("decode base64 content for %s: %w", remotePath, err)
		}
		content = decoded
	}
	if strings.TrimSpace(*output) == "" || strings.TrimSpace(*output) == "-" {
		_, err = stdout.Write(content)
		return err
	}
	outputPath := strings.TrimSpace(*output)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outputPath, content, 0o644)
}

func runSeed(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "relayfile server URL override")
	token := fs.String("token", "", "relayfile token override")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server": true,
		"token":  true,
	})); err != nil {
		return err
	}
	if fs.NArg() > 2 {
		return errors.New("usage: relayfile seed [WORKSPACE] [DIR]")
	}

	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	tokenValue := resolveToken(*token, creds)
	client, err := newAPIClient(resolveServer(*server, creds), tokenValue)
	if err != nil {
		return err
	}

	workspaceID := ""
	dir := "."
	switch fs.NArg() {
	case 0:
		workspaceID, err = resolveWorkspaceIDWithToken("", tokenValue)
	case 1:
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
	case 2:
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
		dir = fs.Arg(1)
	}
	if err != nil {
		return err
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	files, err := collectSeedFiles(root, stdout)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, "No files to seed")
		return nil
	}

	var response bulkWriteResponse
	if err := client.postJSON(context.Background(), fmt.Sprintf("/v1/workspaces/%s/fs/bulk", url.PathEscape(workspaceID)), bulkWriteRequest{Files: files}, &response); err != nil {
		return err
	}
	if _, err := upsertWorkspace(workspaceID); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Seeded %d files", response.Written)
	if response.ErrorCount > 0 {
		fmt.Fprintf(stdout, " with %d errors", response.ErrorCount)
	}
	fmt.Fprintln(stdout)
	for _, item := range response.Errors {
		fmt.Fprintf(stdout, "%s: %s (%s)\n", item.Path, item.Message, item.Code)
	}
	return nil
}

func runExport(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "relayfile server URL override")
	token := fs.String("token", "", "relayfile token override")
	format := fs.String("format", "json", "export format: tar, json, or patch")
	output := fs.String("output", "-", "output file path or - for stdout")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server": true,
		"token":  true,
		"format": true,
		"output": true,
	})); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: relayfile export [WORKSPACE] --format FORMAT [--output FILE]")
	}

	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	tokenValue := resolveToken(*token, creds)
	client, err := newAPIClient(resolveServer(*server, creds), tokenValue)
	if err != nil {
		return err
	}

	workspaceID, err := resolveWorkspaceIDWithToken("", tokenValue)
	if fs.NArg() == 1 {
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
	}
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/v1/workspaces/%s/fs/export?format=%s", url.PathEscape(workspaceID), url.QueryEscape(strings.ToLower(strings.TrimSpace(*format))))
	body, _, err := client.getBytes(context.Background(), path)
	if err != nil {
		return err
	}
	if _, err := upsertWorkspace(workspaceID); err != nil {
		return err
	}
	if strings.TrimSpace(*output) == "" || strings.TrimSpace(*output) == "-" {
		_, err = stdout.Write(body)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		return err
	}
	return os.WriteFile(*output, body, 0o644)
}

func runStatus(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "relayfile server URL override")
	token := fs.String("token", "", "relayfile token override")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"server": true,
		"token":  true,
	})); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: relayfile status [WORKSPACE]")
	}

	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	tokenValue := resolveToken(*token, creds)
	client, err := newAPIClient(resolveServer(*server, creds), tokenValue)
	if err != nil {
		return err
	}

	workspaceID, err := resolveWorkspaceIDWithToken("", tokenValue)
	if fs.NArg() == 1 {
		workspaceID, err = resolveWorkspaceIDWithToken(fs.Arg(0), tokenValue)
	}
	if err != nil {
		return err
	}
	var status syncStatusResponse
	if err := client.getJSON(context.Background(), fmt.Sprintf("/v1/workspaces/%s/sync/status", url.PathEscape(workspaceID)), &status); err != nil {
		return err
	}
	if _, err := upsertWorkspace(workspaceID); err != nil {
		return err
	}

	fileCountText := "unknown"
	var exported []exportedFile
	if err := client.getJSON(context.Background(), fmt.Sprintf("/v1/workspaces/%s/fs/export?format=json", url.PathEscape(workspaceID)), &exported); err == nil {
		fileCountText = strconv.Itoa(len(exported))
	}

	fmt.Fprintf(stdout, "Workspace: %s\n", status.WorkspaceID)
	fmt.Fprintf(stdout, "File count: %s\n", fileCountText)
	if len(status.Providers) == 0 {
		fmt.Fprintln(stdout, "Last activity: unknown")
		return nil
	}

	lastActivity := "unknown"
	for _, provider := range status.Providers {
		if provider.WatermarkTs != nil && strings.TrimSpace(*provider.WatermarkTs) != "" {
			if lastActivity == "unknown" || strings.TrimSpace(*provider.WatermarkTs) > lastActivity {
				lastActivity = strings.TrimSpace(*provider.WatermarkTs)
			}
		}
	}
	fmt.Fprintf(stdout, "Last activity: %s\n", lastActivity)
	for _, provider := range status.Providers {
		line := fmt.Sprintf("%s: %s", provider.Provider, provider.Status)
		if provider.LagSeconds > 0 {
			line += fmt.Sprintf(" lag=%ds", provider.LagSeconds)
		}
		if provider.LastError != nil && strings.TrimSpace(*provider.LastError) != "" {
			line += fmt.Sprintf(" error=%q", *provider.LastError)
		}
		fmt.Fprintln(stdout, line)
	}
	return nil
}

func newAPIClient(server, token string) (*apiClient, error) {
	server = strings.TrimSpace(server)
	token = strings.TrimSpace(token)
	if server == "" {
		server = defaultServerURL
	}
	if token == "" {
		return nil, errors.New("token is required; run relayfile login or pass --token")
	}
	return &apiClient{
		baseURL:    strings.TrimRight(server, "/"),
		token:      token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (c *apiClient) getJSON(ctx context.Context, path string, out any) error {
	body, _, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, out)
}

func (c *apiClient) postJSON(ctx context.Context, path string, input, out any) error {
	bodyBytes, err := json.Marshal(input)
	if err != nil {
		return err
	}
	body, _, err := c.do(ctx, http.MethodPost, path, bodyBytes)
	if err != nil {
		return err
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, out)
}

func (c *apiClient) getBytes(ctx context.Context, path string) ([]byte, string, error) {
	return c.do(ctx, http.MethodGet, path, nil)
}

func (c *apiClient) do(ctx context.Context, method, path string, body []byte) ([]byte, string, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Correlation-Id", correlationID())
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return payload, resp.Header.Get("Content-Type"), nil
	}

	var errPayload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &errPayload)
	}
	if errPayload.Message == "" {
		errPayload.Message = strings.TrimSpace(string(payload))
	}
	return nil, "", &apiError{
		StatusCode: resp.StatusCode,
		Code:       errPayload.Code,
		Message:    errPayload.Message,
	}
}

func writePrettyJSON(stdout io.Writer, body []byte) error {
	var out bytes.Buffer
	if err := json.Indent(&out, body, "", "  "); err != nil {
		_, writeErr := stdout.Write(body)
		return writeErr
	}
	out.WriteByte('\n')
	_, err := stdout.Write(out.Bytes())
	return err
}

func collectSeedFiles(root string, stdout io.Writer) ([]bulkWriteFile, error) {
	paths, err := collectSeedPaths(root)
	if err != nil {
		return nil, err
	}
	files := make([]bulkWriteFile, 0, len(paths))
	for idx, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		entry := bulkWriteFile{
			Path:        "/" + filepath.ToSlash(rel),
			ContentType: detectContentType(path, content),
		}
		if utf8.Valid(content) {
			entry.Content = string(content)
		} else {
			entry.Content = base64.StdEncoding.EncodeToString(content)
			entry.Encoding = "base64"
		}
		files = append(files, entry)
		if stdout != nil {
			fmt.Fprintf(stdout, "\rSeeding %d/%d files...", idx+1, len(paths))
		}
	}
	if stdout != nil && len(paths) > 0 {
		fmt.Fprintln(stdout)
	}
	return files, nil
}

func collectSeedPaths(root string) ([]string, error) {
	paths := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func detectContentType(path string, content []byte) string {
	extType := strings.TrimSpace(mime.TypeByExtension(strings.ToLower(filepath.Ext(path))))
	if extType != "" {
		return extType
	}
	if len(content) == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(content)
}

func promptLine(stdin io.Reader, stdout io.Writer, prompt string) (string, error) {
	if _, err := io.WriteString(stdout, prompt); err != nil {
		return "", err
	}
	reader := bufio.NewReader(stdin)
	value, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func configDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return configDirName
	}
	return filepath.Join(home, configDirName)
}

func credentialsPath() string {
	return filepath.Join(configDir(), "credentials.json")
}

func workspacesPath() string {
	return filepath.Join(configDir(), "workspaces.json")
}

func ensureConfigDir() error {
	return os.MkdirAll(configDir(), 0o755)
}

func saveCredentials(creds credentials) error {
	if err := ensureConfigDir(); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(credentialsPath(), payload, 0o600)
}

func loadCredentials() (credentials, error) {
	var creds credentials
	payload, err := os.ReadFile(credentialsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return creds, fmt.Errorf("credentials not found at %s; run relayfile login", credentialsPath())
		}
		return creds, err
	}
	if err := json.Unmarshal(payload, &creds); err != nil {
		return creds, fmt.Errorf("parse %s: %w", credentialsPath(), err)
	}
	return creds, nil
}

func loadWorkspaceCatalog() (workspaceCatalog, error) {
	var catalog workspaceCatalog
	payload, err := os.ReadFile(workspacesPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return workspaceCatalog{}, nil
		}
		return catalog, err
	}
	if err := json.Unmarshal(payload, &catalog); err != nil {
		return catalog, fmt.Errorf("parse %s: %w", workspacesPath(), err)
	}
	sort.Slice(catalog.Workspaces, func(i, j int) bool {
		return catalog.Workspaces[i].Name < catalog.Workspaces[j].Name
	})
	return catalog, nil
}

func saveWorkspaceCatalog(catalog workspaceCatalog) error {
	if err := ensureConfigDir(); err != nil {
		return err
	}
	sort.Slice(catalog.Workspaces, func(i, j int) bool {
		return catalog.Workspaces[i].Name < catalog.Workspaces[j].Name
	})
	payload, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(workspacesPath(), payload, 0o644)
}

func setDefaultWorkspace(name string) (workspaceRecord, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return workspaceRecord{}, errors.New("workspace name is required")
	}

	record, err := upsertWorkspace(name)
	if err != nil {
		return workspaceRecord{}, err
	}
	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return workspaceRecord{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for i := range catalog.Workspaces {
		if catalog.Workspaces[i].Name == record.Name {
			if catalog.Workspaces[i].ID == "" {
				catalog.Workspaces[i].ID = record.Name
			}
			catalog.Workspaces[i].LastUsedAt = now
			record = catalog.Workspaces[i]
			break
		}
	}
	catalog.Default = record.Name
	if err := saveWorkspaceCatalog(catalog); err != nil {
		return workspaceRecord{}, err
	}
	return record, nil
}

func upsertWorkspace(name string) (workspaceRecord, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return workspaceRecord{}, errors.New("workspace name is required")
	}

	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return workspaceRecord{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for i := range catalog.Workspaces {
		if catalog.Workspaces[i].Name == name {
			if catalog.Workspaces[i].ID == "" {
				catalog.Workspaces[i].ID = name
			}
			catalog.Workspaces[i].LastUsedAt = now
			if err := saveWorkspaceCatalog(catalog); err != nil {
				return workspaceRecord{}, err
			}
			return catalog.Workspaces[i], nil
		}
	}

	record := workspaceRecord{
		Name:       name,
		ID:         name,
		CreatedAt:  now,
		LastUsedAt: now,
	}
	catalog.Workspaces = append(catalog.Workspaces, record)
	if strings.TrimSpace(catalog.Default) == "" {
		catalog.Default = name
	}
	if err := saveWorkspaceCatalog(catalog); err != nil {
		return workspaceRecord{}, err
	}
	return record, nil
}

func removeWorkspace(name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, errors.New("workspace name is required")
	}

	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return false, err
	}
	filtered := catalog.Workspaces[:0]
	removed := false
	for _, workspace := range catalog.Workspaces {
		if workspace.Name == name {
			removed = true
			continue
		}
		filtered = append(filtered, workspace)
	}
	if !removed {
		return false, nil
	}
	catalog.Workspaces = filtered
	if catalog.Default == name {
		catalog.Default = ""
		if len(catalog.Workspaces) > 0 {
			catalog.Default = catalog.Workspaces[0].Name
		}
	}
	return true, saveWorkspaceCatalog(catalog)
}

func resolveWorkspaceIDWithToken(value, token string) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" {
		if id, ok := catalogWorkspaceID(value); ok {
			return id, nil
		}
		return value, nil
	}

	if workspaceID := strings.TrimSpace(os.Getenv("RELAYFILE_WORKSPACE")); workspaceID != "" {
		if id, ok := catalogWorkspaceID(workspaceID); ok {
			return id, nil
		}
		return workspaceID, nil
	}

	if workspaceID := workspaceIDFromToken(token); workspaceID != "" {
		if id, ok := catalogWorkspaceID(workspaceID); ok {
			return id, nil
		}
		return workspaceID, nil
	}

	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return "", err
	}
	defaultName := strings.TrimSpace(catalog.Default)
	if defaultName != "" {
		if id, ok := catalogWorkspaceIDFromCatalog(catalog, defaultName); ok {
			return id, nil
		}
		return defaultName, nil
	}
	return "", errors.New("workspace is required; pass WORKSPACE, set RELAYFILE_WORKSPACE, or run relayfile workspace use NAME")
}

func catalogWorkspaceID(name string) (string, bool) {
	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		return "", false
	}
	return catalogWorkspaceIDFromCatalog(catalog, name)
}

func catalogWorkspaceIDFromCatalog(catalog workspaceCatalog, name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	for _, workspace := range catalog.Workspaces {
		if workspace.Name == name || workspace.ID == name {
			if strings.TrimSpace(workspace.ID) != "" {
				return strings.TrimSpace(workspace.ID), true
			}
			return strings.TrimSpace(workspace.Name), true
		}
	}
	return "", false
}

func workspaceCatalogNames(catalog workspaceCatalog, token string) []string {
	set := map[string]struct{}{}
	names := make([]string, 0, len(catalog.Workspaces)+3)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			if _, ok := set[value]; ok {
				return
			}
			set[value] = struct{}{}
			names = append(names, value)
		}
	}
	add(os.Getenv("RELAYFILE_WORKSPACE"))
	add(workspaceIDFromToken(token))
	add(catalog.Default)

	localNames := make([]string, 0, len(catalog.Workspaces))
	for _, workspace := range catalog.Workspaces {
		name := strings.TrimSpace(workspace.Name)
		if name == "" {
			continue
		}
		if _, ok := set[name]; ok {
			continue
		}
		localNames = append(localNames, name)
	}
	sort.Strings(localNames)
	names = append(names, localNames...)
	return names
}

func workspaceIDFromToken(token string) string {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
	}
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	for _, key := range []string{"workspace_id", "wks"} {
		if value, ok := claims[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func remoteWorkspaceNames(response adminWorkspaceList) []string {
	set := map[string]struct{}{}
	for _, workspaceID := range response.WorkspaceIDs {
		workspaceID = strings.TrimSpace(workspaceID)
		if workspaceID != "" {
			set[workspaceID] = struct{}{}
		}
	}
	if len(set) == 0 && len(response.Workspaces) > 0 {
		var workspaceIDs []string
		if err := json.Unmarshal(response.Workspaces, &workspaceIDs); err == nil {
			for _, workspaceID := range workspaceIDs {
				workspaceID = strings.TrimSpace(workspaceID)
				if workspaceID != "" {
					set[workspaceID] = struct{}{}
				}
			}
		} else {
			var workspaceMap map[string]json.RawMessage
			if err := json.Unmarshal(response.Workspaces, &workspaceMap); err == nil {
				for workspaceID := range workspaceMap {
					workspaceID = strings.TrimSpace(workspaceID)
					if workspaceID != "" {
						set[workspaceID] = struct{}{}
					}
				}
			}
		}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func normalizeFlagArgs(args []string, flags map[string]bool) []string {
	if len(args) == 0 {
		return nil
	}
	flagArgs := make([]string, 0, len(args))
	positionalArgs := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionalArgs = append(positionalArgs, args[i+1:]...)
			break
		}
		name, ok, hasInlineValue := parseFlagName(arg)
		if !ok {
			positionalArgs = append(positionalArgs, arg)
			continue
		}
		flagArgs = append(flagArgs, arg)
		if takesValue, known := flags[name]; known && takesValue && !hasInlineValue && i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	return append(flagArgs, positionalArgs...)
}

func parseFlagName(arg string) (name string, ok bool, hasInlineValue bool) {
	if !strings.HasPrefix(arg, "-") || arg == "-" {
		return "", false, false
	}
	trimmed := strings.TrimLeft(arg, "-")
	if trimmed == "" {
		return "", false, false
	}
	name = trimmed
	if idx := strings.IndexByte(trimmed, '='); idx >= 0 {
		name = trimmed[:idx]
		hasInlineValue = true
	}
	return name, true, hasInlineValue
}

func resolveServer(flagValue string, creds credentials) string {
	if value := strings.TrimSpace(flagValue); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("RELAYFILE_SERVER")); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("RELAYFILE_BASE_URL")); value != "" {
		return value
	}
	if value := strings.TrimSpace(creds.Server); value != "" {
		return value
	}
	return defaultServerURL
}

func resolveToken(flagValue string, creds credentials) string {
	if value := strings.TrimSpace(flagValue); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("RELAYFILE_TOKEN")); value != "" {
		return value
	}
	return strings.TrimSpace(creds.Token)
}

func envOrDefault(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func durationEnv(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("invalid %s=%q, using fallback %s", name, raw, fallback.String())
		return fallback
	}
	return value
}

func floatEnv(name string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		log.Printf("invalid %s=%q, using fallback %f", name, raw, fallback)
		return fallback
	}
	return value
}

func boolEnv(name string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		log.Printf("invalid %s=%q, using fallback %t", name, raw, fallback)
		return fallback
	}
	return value
}

func boolPtr(value bool) *bool {
	return &value
}

func clampJitterRatio(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func jitteredIntervalWithSample(base time.Duration, jitterRatio, sample float64) time.Duration {
	if base <= 0 {
		return 0
	}
	jitterRatio = clampJitterRatio(jitterRatio)
	if jitterRatio == 0 {
		return base
	}
	if sample < 0 {
		sample = 0
	} else if sample > 1 {
		sample = 1
	}
	factor := 1 + ((sample*2)-1)*jitterRatio
	if factor < 0 {
		factor = 0
	}
	delay := time.Duration(float64(base) * factor)
	if delay < time.Millisecond {
		return time.Millisecond
	}
	return delay
}

func runMountLoop(rootCtx context.Context, syncer *mountsync.Syncer, timeout, interval time.Duration, intervalJitter float64, websocketEnabled, once bool) error {
	runCycle := func(reconcile bool) error {
		ctx, cancel := context.WithTimeout(rootCtx, timeout)
		defer cancel()
		var err error
		if reconcile {
			err = syncer.Reconcile(ctx)
		} else {
			err = syncer.SyncOnce(ctx)
		}
		if err != nil {
			log.Printf("mount sync cycle failed: %v", err)
			return err
		}
		log.Printf("mount sync cycle completed")
		return nil
	}

	initialErr := runCycle(true)
	if once {
		return initialErr
	}

	timer := time.NewTimer(jitteredIntervalWithSample(interval, intervalJitter, mathrand.Float64()))
	defer timer.Stop()
	cycle := 0
	for {
		select {
		case <-rootCtx.Done():
			log.Printf("mount sync stopping: %v", rootCtx.Err())
			return nil
		case <-timer.C:
			cycle++
			reconcile := !websocketEnabled || cycle%websocketReconcileEvery == 0
			_ = runCycle(reconcile)
			timer.Reset(jitteredIntervalWithSample(interval, intervalJitter, mathrand.Float64()))
		}
	}
}

func correlationID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return "corr_" + hex.EncodeToString(buf[:])
	}
	return fmt.Sprintf("corr_%d", time.Now().UnixNano())
}
