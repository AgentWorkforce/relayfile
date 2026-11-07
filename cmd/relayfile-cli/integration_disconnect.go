package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// cloudIntegrationDisconnectResponse is the body Cloud returns from
// DELETE /api/v1/workspaces/{id}/integrations/{provider}/status. Older
// deployments answer a bare {"success":true}; newer ones may add
// localDeleted, which is false when the workspace_integrations row the
// caller meant was not removed (for example because only a named
// service-account row exists and the request was unscoped).
type cloudIntegrationDisconnectResponse struct {
	Success      *bool `json:"success,omitempty"`
	LocalDeleted *bool `json:"localDeleted,omitempty"`
}

// integrationDisconnectOutcome is what the CLI learned after Cloud accepted
// a disconnect and the CLI re-read the connection state.
type integrationDisconnectOutcome struct {
	Provider string
	// Slot is the named service-account connection that was disconnected, or
	// empty for the default (unscoped) provider connection.
	Slot string
	// RemainingNamed lists named rows for the same provider that are still
	// present after an unscoped disconnect. Cloud never removes those
	// implicitly; each needs its own `--name` disconnect.
	RemainingNamed []cloudIntegrationListEntry
	// ListError is set when the post-disconnect list could not be read. The
	// disconnect itself was verified; only the "what remains" report is
	// missing.
	ListError error
}

// integrationDisconnectScopeQuery is the selector Cloud expects for a named
// service-account slot. It is empty for the default connection so older
// Cloud deployments that reject unknown selectors keep working unchanged.
func integrationDisconnectScopeQuery(slot string) string {
	slot = strings.TrimSpace(slot)
	if slot == "" {
		return ""
	}
	query := url.Values{}
	query.Set("scopeKind", "workspace_service_account")
	query.Set("scopeName", slot)
	return "?" + query.Encode()
}

func integrationDisconnectStatusPath(workspaceID, provider, slot string) string {
	return fmt.Sprintf("/api/v1/workspaces/%s/integrations/%s/status%s",
		url.PathEscape(workspaceID), url.PathEscape(provider), integrationDisconnectScopeQuery(slot))
}

// disconnectCloudIntegration asks Cloud to remove one workspace integration
// row and then proves it is gone before the caller touches local state.
//
// A 2xx from the DELETE is not treated as proof: Cloud answers success for
// an already-absent row (idempotent) and, on deployments that select rows
// by name, an unscoped DELETE leaves named rows in place. The CLI therefore
// (1) honours an explicit localDeleted:false, (2) re-reads the status for
// the same scope and refuses to continue while a connection is still
// reported, and (3) after an unscoped disconnect lists the named rows that
// remain so the operator can target them with --name.
func disconnectCloudIntegration(ctx context.Context, client *apiClient, workspaceID, provider, slot string) (integrationDisconnectOutcome, error) {
	provider = normalizeProviderID(provider)
	slot = strings.TrimSpace(slot)
	outcome := integrationDisconnectOutcome{Provider: provider, Slot: slot}
	target := provider
	if slot != "" {
		target = fmt.Sprintf("%s connection %q", provider, slot)
	}

	body, _, err := client.do(ctx, http.MethodDelete, integrationDisconnectStatusPath(workspaceID, provider, slot), nil)
	if err != nil {
		return outcome, err
	}
	if len(strings.TrimSpace(string(body))) > 0 {
		var response cloudIntegrationDisconnectResponse
		if err := json.Unmarshal(body, &response); err != nil {
			return outcome, fmt.Errorf("disconnect %s: unexpected cloud response: %w", target, err)
		}
		if response.Success != nil && !*response.Success {
			return outcome, fmt.Errorf("disconnect %s: cloud did not confirm the disconnect; local mirror left untouched", target)
		}
		if response.LocalDeleted != nil && !*response.LocalDeleted {
			return outcome, fmt.Errorf("disconnect %s: cloud reported the connection row was not removed (localDeleted=false); local mirror left untouched", target)
		}
	}

	present, err := cloudIntegrationStillPresent(ctx, client, workspaceID, provider, slot)
	if err != nil {
		return outcome, fmt.Errorf("verify %s disconnect: %w", target, err)
	}
	if present != "" {
		return outcome, fmt.Errorf("disconnect %s: cloud still reports connection %s after the disconnect; local mirror left untouched", target, present)
	}

	if slot == "" {
		remaining, err := cloudIntegrationNamedRows(ctx, client, workspaceID, provider)
		if err != nil {
			outcome.ListError = err
		} else {
			outcome.RemainingNamed = remaining
		}
	}
	return outcome, nil
}

// cloudIntegrationStillPresent re-reads the status for the same scope that
// was just disconnected. It returns the connection id (or "connected" when
// the status reports a connection without an id) while Cloud still holds a
// row, and "" once the row is gone. A 404 means Cloud no longer knows the
// row at all and counts as absent.
func cloudIntegrationStillPresent(ctx context.Context, client *apiClient, workspaceID, provider, slot string) (string, error) {
	var status cloudIntegrationReadyResponse
	err := client.getJSON(ctx, integrationDisconnectStatusPath(workspaceID, provider, slot), &status)
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return "", nil
		}
		return "", err
	}
	if id := strings.TrimSpace(status.CurrentConnectionID); id != "" {
		return id, nil
	}
	if cloudIntegrationConnected(status) {
		return "connected", nil
	}
	return "", nil
}

// cloudIntegrationNamedRows returns the named (service-account) rows Cloud
// still lists for the provider. Entries without a name are the default
// connection and are not reported here.
func cloudIntegrationNamedRows(ctx context.Context, client *apiClient, workspaceID, provider string) ([]cloudIntegrationListEntry, error) {
	var entries []cloudIntegrationListEntry
	if err := client.getJSON(ctx, fmt.Sprintf("/api/v1/workspaces/%s/integrations", url.PathEscape(workspaceID)), &entries); err != nil {
		return nil, err
	}
	var named []cloudIntegrationListEntry
	for _, entry := range entries {
		if strings.TrimSpace(entry.Name) == "" {
			continue
		}
		if normalizeProviderID(entry.Provider) != provider {
			continue
		}
		named = append(named, entry)
	}
	return named, nil
}

// formatIntegrationDisconnectOutcome renders the operator-facing result.
func formatIntegrationDisconnectOutcome(outcome integrationDisconnectOutcome, workspaceName string) string {
	var sb strings.Builder
	if outcome.Slot != "" {
		fmt.Fprintf(&sb, "%s connection %q disconnected from workspace %s (local mirror unchanged)\n", outcome.Provider, outcome.Slot, workspaceName)
		return sb.String()
	}
	fmt.Fprintf(&sb, "%s disconnected from workspace %s\n", outcome.Provider, workspaceName)
	if outcome.ListError != nil {
		fmt.Fprintf(&sb, "warning: could not list remaining %s connections: %v\n", outcome.Provider, outcome.ListError)
		return sb.String()
	}
	if len(outcome.RemainingNamed) == 0 {
		return sb.String()
	}
	fmt.Fprintf(&sb, "note: %d named %s connection(s) remain in workspace %s and were not disconnected:\n", len(outcome.RemainingNamed), outcome.Provider, workspaceName)
	for _, entry := range outcome.RemainingNamed {
		if strings.TrimSpace(entry.ConnectionID) != "" {
			fmt.Fprintf(&sb, "  %s (%s)\n", entry.Name, entry.ConnectionID)
		} else {
			fmt.Fprintf(&sb, "  %s\n", entry.Name)
		}
	}
	fmt.Fprintf(&sb, "disconnect each with: %s integration disconnect %s --name SLOT --workspace %s\n", programName(), outcome.Provider, workspaceName)
	return sb.String()
}
