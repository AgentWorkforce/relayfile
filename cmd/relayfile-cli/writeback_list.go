package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const writebackListUsage = "usage: relayfile writeback list --state pending|dead [--workspace WS] [--json]"

// writebackListItem mirrors the WritebackItem TypeScript shape exported from
// packages/sdk/typescript/src/types.ts. Field names must stay in sync.
type writebackListItem struct {
	ID               string                 `json:"id"`
	WorkspaceID      string                 `json:"workspaceId"`
	Path             string                 `json:"path"`
	Revision         string                 `json:"revision"`
	CorrelationID    string                 `json:"correlationId"`
	State            string                 `json:"state,omitempty"`
	Provider         string                 `json:"provider,omitempty"`
	Timestamp        string                 `json:"ts,omitempty"`
	Code             string                 `json:"code,omitempty"`
	Message          string                 `json:"message,omitempty"`
	ProviderStatus   int                    `json:"providerStatus,omitempty"`
	ProviderResponse json.RawMessage        `json:"providerResponse,omitempty"`
	Attempts         int                    `json:"attempts,omitempty"`
	FirstAttemptAt   string                 `json:"firstAttemptAt,omitempty"`
	LastAttemptAt    string                 `json:"lastAttemptAt,omitempty"`
	Error            *deadLetterErrorDetail `json:"error,omitempty"`
}

func runWritebackList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("writeback list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	state := fs.String("state", "", "writeback state: pending or dead")
	workspace := fs.String("workspace", "", "workspace name or id")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"state":     true,
		"workspace": true,
		"json":      false,
	})); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New(writebackListUsage)
	}

	if strings.TrimSpace(*state) == "" {
		return errors.New(writebackListUsage)
	}
	normalizedState := strings.ToLower(strings.TrimSpace(*state))
	if !validWritebackListState(normalizedState) {
		return fmt.Errorf("unknown state %q: %s", *state, writebackListUsage)
	}

	workspaceID, record, err := resolveWorkspaceLikeStatus(strings.TrimSpace(*workspace))
	if err != nil {
		return err
	}
	items, err := listLocalWritebackItems(workspaceID, record.LocalDir, normalizedState)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, items)
	}
	printWritebackList(stdout, items)
	return nil
}

func validWritebackListState(state string) bool {
	switch state {
	case "pending", "dead":
		return true
	default:
		return false
	}
}

// listLocalWritebackItems returns per-operation writeback rows for the given
// state. `pending` is sourced from dirty tracked files in
// `<localDir>/.relayfile-mount-state.json`; `dead` is sourced from per-op
// records under `<localDir>/.relay/dead-letter/`. Aggregate counters in
// `.relay/state.json` are deliberately not expanded into synthetic rows.
func listLocalWritebackItems(workspaceID, localDir, state string) ([]writebackListItem, error) {
	if strings.TrimSpace(localDir) == "" {
		return []writebackListItem{}, nil
	}
	if state == "dead" {
		return readDeadWritebackItems(workspaceID, localDir)
	}
	return readPendingWritebackItems(workspaceID, localDir)
}

func readPendingWritebackItems(workspaceID, localDir string) ([]writebackListItem, error) {
	var state struct {
		Files map[string]struct {
			Revision    string `json:"revision"`
			Hash        string `json:"hash"`
			Dirty       bool   `json:"dirty"`
			Denied      bool   `json:"denied"`
			WriteDenied bool   `json:"writeDenied"`
			ReadOnly    bool   `json:"readonly"`
		} `json:"files"`
	}
	payload, err := os.ReadFile(filepath.Join(localDir, ".relayfile-mount-state.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return []writebackListItem{}, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		return nil, fmt.Errorf("invalid mount state: %w", err)
	}
	remoteRoot := readMountRemoteRoot(localDir)
	localHashes, err := localWritebackHashes(localDir, remoteRoot)
	if err != nil {
		return nil, err
	}
	items := make([]writebackListItem, 0, len(state.Files)+len(localHashes))
	seen := map[string]struct{}{}
	for rawPath, tracked := range state.Files {
		path := normalizeWritebackListPath(rawPath)
		seen[path] = struct{}{}
		if tracked.ReadOnly {
			continue
		}
		localHash, hasLocal := localHashes[path]
		pending := tracked.Dirty
		if !pending && !tracked.Denied && !tracked.WriteDenied {
			switch {
			case hasLocal && tracked.Hash != "" && localHash != tracked.Hash:
				pending = true
			case !hasLocal && tracked.Hash != "":
				pending = true
			}
		}
		if !pending {
			continue
		}
		item := writebackListItem{
			ID:            defaultIfBlank(path, rawPath),
			WorkspaceID:   workspaceID,
			Path:          path,
			Revision:      strings.TrimSpace(tracked.Revision),
			CorrelationID: defaultIfBlank(path, rawPath),
			State:         "pending",
			Provider:      providerFromPath(path),
		}
		items = append(items, item)
	}
	for path := range localHashes {
		if _, ok := seen[path]; ok {
			continue
		}
		items = append(items, writebackListItem{
			ID:            path,
			WorkspaceID:   workspaceID,
			Path:          path,
			CorrelationID: path,
			State:         "pending",
			Provider:      providerFromPath(path),
		})
	}
	sortWritebackListItems(items)
	return items, nil
}

func localWritebackHashes(localDir, remoteRoot string) (map[string]string, error) {
	hashes := map[string]string{}
	err := filepath.WalkDir(localDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(localDir, path)
		if relErr != nil || rel == "." {
			return relErr
		}
		first := strings.SplitN(rel, string(os.PathSeparator), 2)[0]
		if entry.IsDir() {
			if first == entry.Name() && writebackListReservedTopLevel(first) {
				return filepath.SkipDir
			}
			return nil
		}
		if first == ".relayfile-mount-state.json" ||
			strings.HasPrefix(first, ".relayfile-mount-state.json.tmp-") ||
			writebackListReservedTopLevel(first) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		hash, err := hashLocalWritebackFile(path)
		if err != nil {
			return err
		}
		hashes[remotePathForLocalRel(remoteRoot, filepath.ToSlash(rel))] = hash
		return nil
	})
	return hashes, err
}

func remotePathForLocalRel(remoteRoot, rel string) string {
	rel = strings.Trim(strings.TrimSpace(filepath.ToSlash(rel)), "/")
	root := normalizeWritebackListPath(remoteRoot)
	if root == "" || root == "/" {
		if rel == "" {
			return "/"
		}
		return normalizeWritebackListPath(rel)
	}
	if rel == "" {
		return root
	}
	return normalizeWritebackListPath(strings.TrimRight(root, "/") + "/" + rel)
}

func writebackListReservedTopLevel(name string) bool {
	return name == ".git" || name == ".relay" || name == ".skills" ||
		name == "digests" || name == "node_modules" ||
		name == "_PERMISSIONS.md"
}

func hashLocalWritebackFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func readDeadWritebackItems(workspaceID, localDir string) ([]writebackListItem, error) {
	records, err := readDeadLetterRecords(localDir)
	if err != nil {
		return nil, err
	}
	items := make([]writebackListItem, 0, len(records))
	for _, record := range records {
		path := normalizeWritebackListPath(record.Path)
		opID := strings.TrimSpace(record.OpID)
		ts := firstNonBlank(record.LastAttemptedAt, record.Timestamp, record.CreatedAt)
		item := writebackListItem{
			ID:             defaultIfBlank(opID, path),
			WorkspaceID:    workspaceID,
			Path:           path,
			Revision:       ts,
			CorrelationID:  opID,
			State:          "dead",
			Provider:       providerFromPath(path),
			Timestamp:      ts,
			Code:           strings.TrimSpace(record.Code),
			Message:        strings.TrimSpace(record.Message),
			ProviderStatus: record.LastStatus,
			Attempts:       record.Attempts,
			LastAttemptAt:  ts,
			FirstAttemptAt: strings.TrimSpace(record.CreatedAt),
		}
		if detail, ok, err := readDeadLetterErrorSidecar(localDir, opID); err != nil {
			return nil, err
		} else if ok {
			item.Error = &detail
			if strings.TrimSpace(detail.Code) != "" {
				item.Code = detail.Code
			}
			if strings.TrimSpace(detail.Message) != "" {
				item.Message = detail.Message
			}
			if detail.ProviderStatus != 0 {
				item.ProviderStatus = detail.ProviderStatus
			}
			if len(detail.ProviderResponse) > 0 {
				item.ProviderResponse = detail.ProviderResponse
			}
			if detail.Attempts > 0 {
				item.Attempts = detail.Attempts
			}
			if strings.TrimSpace(detail.FirstAttemptAt) != "" {
				item.FirstAttemptAt = detail.FirstAttemptAt
			}
			if strings.TrimSpace(detail.LastAttemptAt) != "" {
				item.LastAttemptAt = detail.LastAttemptAt
				item.Revision = detail.LastAttemptAt
				item.Timestamp = detail.LastAttemptAt
			}
			if strings.TrimSpace(detail.OpID) != "" {
				item.CorrelationID = detail.OpID
			}
		}
		if item.CorrelationID == "" {
			item.CorrelationID = item.ID
		}
		items = append(items, item)
	}
	sortWritebackListItems(items)
	return items, nil
}

func sortWritebackListItems(items []writebackListItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].ID == items[j].ID {
			return items[i].Path < items[j].Path
		}
		return items[i].ID < items[j].ID
	})
}

func printWritebackList(stdout io.Writer, items []writebackListItem) {
	fmt.Fprintln(stdout, "op_id\tpath\tstate\tts\tprovider")
	for _, item := range items {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n",
			defaultIfBlank(item.ID, "-"),
			defaultIfBlank(item.Path, "-"),
			defaultIfBlank(item.State, "-"),
			defaultIfBlank(item.Timestamp, "-"),
			defaultIfBlank(item.Provider, "-"),
		)
	}
}

func normalizeWritebackListPath(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	for strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}
	return path
}

func providerFromPath(path string) string {
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == "" {
		return ""
	}
	if idx := strings.Index(trimmed, "/"); idx >= 0 {
		return trimmed[:idx]
	}
	return trimmed
}
