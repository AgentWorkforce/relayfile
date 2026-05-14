package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
)

const writebackListUsage = "usage: relayfile writeback list --state pending|dead|succeeded|failed [--workspace WS] [--json]"

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
	state := fs.String("state", "", "writeback state: pending, dead, succeeded, or failed")
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
	case "pending", "dead", "succeeded", "failed":
		return true
	default:
		return false
	}
}

// listLocalWritebackItems returns per-operation writeback rows for the given
// state. Only `dead` is sourced from per-op records on disk
// (`<localDir>/.relay/dead-letter/`). `pending`, `succeeded`, and `failed`
// are aggregate counters in the writeback status report today and have no
// row-level history, so this command refuses to fabricate rows for them per
// the workspace-primitives WI5 lead plan (non-goal #9).
func listLocalWritebackItems(workspaceID, localDir, state string) ([]writebackListItem, error) {
	if state == "dead" {
		if strings.TrimSpace(localDir) == "" {
			return []writebackListItem{}, nil
		}
		return readDeadWritebackItems(workspaceID, localDir)
	}
	return nil, fmt.Errorf("%s state not yet tracked; see workspace-primitives WI5", state)
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
