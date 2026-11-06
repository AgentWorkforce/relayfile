package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// runIntegrationSetMetadata is a general-purpose verb for writing operator-
// controlled connection metadata. Today the Atlassian post-OAuth picker
// (`integration connect jira`) uses the same endpoint to set `cloudId` and
// `baseUrl` automatically. This verb exists for the cases the auto-picker
// doesn't cover: changing metadata post-connect, setting non-Atlassian
// connection-level config (e.g. linear `team_id`, a custom API host), or
// scripting metadata writes from CI.
//
// KEY=VALUE retains its string-only contract. JSON files preserve arrays and
// booleans needed for project selection and externally managed provider hooks.
func runIntegrationSetMetadata(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("integration set-metadata", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceName := fs.String("workspace", "", "workspace name or id")
	cloudAPIURL := fs.String("cloud-api-url", envOrDefault("RELAYFILE_CLOUD_API_URL", defaultCloudAPIURL), "Relayfile Cloud API URL")
	metadataFile := fs.String("metadata-file", "", "JSON object file (instead of KEY=VALUE pairs)")
	yes := fs.Bool("yes", false, "skip confirmation")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"workspace":     true,
		"cloud-api-url": true,
		"metadata-file": true,
		"yes":           false,
	})); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 1 || (len(rest) < 2 && *metadataFile == "") {
		return fmt.Errorf(
			"usage: %s integration set-metadata PROVIDER (KEY=VALUE... | --metadata-file FILE) [--workspace NAME] [--yes]\n\n"+
				"  Use --metadata-file for a JSON object with arrays, booleans, or nested values.\n"+
				"  Example: %s integration set-metadata jira cloudId=abc-123 baseUrl=https://foo.atlassian.net",
			programName(),
			programName(),
		)
	}
	if *metadataFile != "" && len(rest) != 1 {
		return errors.New("cannot combine --metadata-file with KEY=VALUE assignments")
	}
	provider := normalizeProviderID(rest[0])
	if err := validateLocalProviderID(provider); err != nil {
		return err
	}
	metadata := map[string]any{}
	for _, raw := range rest[1:] {
		key, value, ok := strings.Cut(raw, "=")
		if !ok {
			return fmt.Errorf("metadata argument %q is not in KEY=VALUE form", raw)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			return fmt.Errorf("metadata argument %q has an empty key", raw)
		}
		if strings.ContainsAny(key, ".[]") {
			return fmt.Errorf("metadata key %q must be flat; nested keys are not supported yet", key)
		}
		metadata[key] = value
	}
	if *metadataFile != "" {
		var err error
		metadata, err = readIntegrationMetadataFile(*metadataFile)
		if err != nil {
			return err
		}
	}
	record, err := resolveWorkspaceRecord(strings.TrimSpace(*workspaceName))
	if err != nil {
		return err
	}
	if !*yes {
		fmt.Fprintf(stdout, "About to set %s metadata in workspace %q:\n", provider, record.Name)
		// Print keys in alphabetical order so the confirmation is stable
		// across runs and easy to eyeball.
		keys := make([]string, 0, len(metadata))
		for k := range metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			value, _ := json.Marshal(redactIntegrationMetadataValue(k, metadata[k]))
			fmt.Fprintf(stdout, "  %q = %s\n", k, value)
		}
		answer, err := promptLine(stdin, stdout, "Continue? [y/N]: ")
		if err != nil {
			return err
		}
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(stdout, "Aborted")
			return nil
		}
	}
	cloudCreds, err := ensureCloudCredentials(strings.TrimSpace(*cloudAPIURL), "", 5*time.Minute, false, io.Discard)
	if err != nil {
		return err
	}
	// Older catalogs can store a Relayfile rw_ ID. Cloud metadata authorization
	// requires its Cloud workspace ID, resolved through the normal access check.
	workspaceID := record.ID
	if strings.HasPrefix(workspaceID, "rw_") {
		resolved, err := resolveCloudWorkspaceForRelayfile(cloudCreds, workspaceID)
		if err != nil {
			return err
		}
		workspaceID = resolved.CloudWorkspaceID
	}
	client, err := newAPIClient(cloudCreds.APIURL, cloudCreds.AccessToken)
	if err != nil {
		return err
	}
	bodyBytes, err := json.Marshal(map[string]any{"metadata": metadata})
	if err != nil {
		return err
	}
	if _, _, err := client.do(
		context.Background(),
		http.MethodPut,
		fmt.Sprintf("/api/v1/workspaces/%s/integrations/%s/metadata", url.PathEscape(workspaceID), url.PathEscape(provider)),
		bodyBytes,
	); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s metadata updated in workspace %s\n", provider, record.Name)
	return nil
}

func readIntegrationMetadataFile(path string) (map[string]any, error) {
	file, err := openIntegrationMetadataFile(path)
	if err != nil {
		return nil, fmt.Errorf("read metadata JSON file: %w", err)
	}
	defer file.Close()
	const maxBytes = 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || len(data) > maxBytes {
		return nil, errors.New("metadata JSON file must be readable and at most 1 MiB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var metadata map[string]any
	if err := decoder.Decode(&metadata); err != nil || metadata == nil {
		return nil, errors.New("metadata JSON must contain one object")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("metadata JSON must contain one object with no trailing content")
	}
	return metadata, nil
}

func redactIntegrationMetadataValue(key string, value any) any {
	lower := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
	for _, sensitive := range []string{"secret", "token", "password", "credential", "authorization", "apikey", "cookie"} {
		if strings.Contains(lower, sensitive) {
			return "[redacted]"
		}
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, entry := range typed {
			out[key] = redactIntegrationMetadataValue(key, entry)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, entry := range typed {
			out[index] = redactIntegrationMetadataValue("", entry)
		}
		return out
	default:
		return value
	}
}
