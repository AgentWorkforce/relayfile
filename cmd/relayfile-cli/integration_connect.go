package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func gitlabAuthConfigKey(requestedProvider string) string {
	if normalizeProviderID(requestedProvider) != "gitlab" {
		return ""
	}
	if requestedProvider == "gitlab" {
		return "gitlab-relay"
	}
	return requestedProvider
}

func cloudIntegrationConnectedForConfig(status cloudIntegrationReadyResponse, configKey string) bool {
	return cloudIntegrationConnected(status) && (configKey == "" || status.ConfigKey == configKey)
}

// Connect errors retain their HTTP status for callers without printing raw
// Cloud/provider messages, which can contain private details or credentials.
func integrationConnectError(err error) error {
	var upstream *apiError
	if !errors.As(err, &upstream) {
		return err
	}
	bounded := *upstream
	bounded.Code = ""
	switch {
	case (upstream.StatusCode == 400 || upstream.StatusCode == 409) && upstream.Code == "unknown_provider":
		bounded.Code = "unknown_provider"
		bounded.Message = "This provider is not supported for workspace connections on this Cloud endpoint. Check `relayfile integration available --refresh`, using the same `--cloud-api-url` if set."
	case upstream.StatusCode == 409 && upstream.Code == "already_connected":
		bounded.Code = "already_connected"
		bounded.Message = "Disconnect the existing integration in this workspace scope before changing its auth connection."
	case upstream.StatusCode == 503 && upstream.Code == "upstream_unavailable":
		bounded.Code = "upstream_unavailable"
		bounded.Message = "Cloud could not verify the existing integration connection. Retry when the integration backend is available."
	case upstream.StatusCode == http.StatusUnauthorized:
		bounded.Message = "Cloud login required. Run `relayfile login` and retry."
	case upstream.StatusCode == http.StatusForbidden:
		bounded.Message = "You do not have permission to perform that integration operation."
	case upstream.StatusCode == http.StatusNotFound:
		bounded.Message = "The integration resource was not found or is no longer available."
	case upstream.StatusCode == http.StatusConflict:
		bounded.Message = "The integration operation conflicts with its current lifecycle state."
	case upstream.StatusCode == http.StatusTooManyRequests:
		bounded.Message = "Cloud integration rate limit exceeded. Wait and retry."
	default:
		bounded.Message = fmt.Sprintf("Cloud integration request failed (%d).", upstream.StatusCode)
	}
	return &bounded
}

// integrationConnectPollOpts is the polling schedule used while waiting for a
// connect to finish. Production uses the defaults; tests inject a fast clock.
var integrationConnectPollOpts = politeOpts{}

// connectIdentityBaseline is the workspace-keyed provider status observed
// immediately before a connect session was minted. Cloud answers
// workspace-keyed status polls from whatever row exists for the provider, and
// a Nango mint returns no connection id, so this snapshot is the only way the
// CLI can tell "the OAuth I just started finished" apart from "a row from an
// earlier connect is still marked ready".
type connectIdentityBaseline struct {
	// known is false when the pre-mint status could not be read. Without a
	// baseline no workspace-keyed ready row can be attributed to this
	// attempt, so only a status bound to the minted id is accepted.
	known        bool
	connectionID string
	connectedAt  string
	lastAuthAt   string
}

func newConnectIdentityBaseline(status cloudIntegrationReadyResponse, err error) connectIdentityBaseline {
	if err != nil {
		return connectIdentityBaseline{}
	}
	connectedAt, lastAuthAt := connectStatusAuthTimes(status)
	return connectIdentityBaseline{
		known:        true,
		connectionID: strings.TrimSpace(firstNonEmpty(status.CurrentConnectionID, status.ConnectionID)),
		connectedAt:  connectedAt,
		lastAuthAt:   lastAuthAt,
	}
}

// evidencesNewConnection reports whether status describes a connection that
// did not exist, or had not been authenticated, when the baseline was taken:
// a different connection id, or an OAuth timestamp strictly later than the
// baseline's own timestamp for the same field. Server timestamps are compared
// only with each other, so CLI clock skew does not matter. Same-id evidence
// needs a comparable pair: when the baseline carried no parseable timestamp
// the row's age is unknown, and a timestamp that merely shows up later proves
// nothing. A status identical to the baseline is the stale-row case.
func (b connectIdentityBaseline) evidencesNewConnection(status cloudIntegrationReadyResponse) bool {
	if !b.known {
		return false
	}
	current := strings.TrimSpace(firstNonEmpty(status.CurrentConnectionID, status.ConnectionID))
	if current != "" && current != b.connectionID {
		return true
	}
	connectedAt, lastAuthAt := connectStatusAuthTimes(status)
	return rfc3339After(lastAuthAt, b.lastAuthAt) || rfc3339After(connectedAt, b.connectedAt)
}

// connectStatusBindsMintedIdentity reports whether Cloud explicitly matched the
// status to the connection id the mint returned. Cloud sets connectionMatched
// from "stored row id == requested id" only when the request carried a real
// id; a workspace-keyed poll matches any row, so an empty minted id never
// binds. When Cloud also reports the current id it must agree with the mint.
//
// polledWithMintedID says whether the status came from a poll keyed by the
// minted id. Only then may a missing current id be read as "matched means the
// minted row": a workspace-keyed poll (the "" fallback) reports
// connectionMatched:true for whatever row exists, so there the current id must
// be present and equal to the mint.
func connectStatusBindsMintedIdentity(status cloudIntegrationReadyResponse, mintedConnectionID string, polledWithMintedID bool) bool {
	minted := strings.TrimSpace(mintedConnectionID)
	if minted == "" || status.ConnectionMatched == nil || !*status.ConnectionMatched {
		return false
	}
	current := strings.TrimSpace(firstNonEmpty(status.CurrentConnectionID, status.ConnectionID))
	if current == "" {
		return polledWithMintedID
	}
	return current == minted
}

func connectStatusAuthTimes(status cloudIntegrationReadyResponse) (connectedAt, lastAuthAt string) {
	if status.OAuth == nil {
		return "", ""
	}
	return strings.TrimSpace(status.OAuth.ConnectedAt), strings.TrimSpace(status.OAuth.LastAuthAt)
}

// rfc3339After reports whether candidate and reference are both parseable
// timestamps and candidate is strictly later. A missing or malformed value on
// either side makes the pair incomparable, which is not evidence: a baseline
// that omitted its timestamp must not let an old timestamp revealed later
// count as a new authentication.
func rfc3339After(candidate, reference string) bool {
	candidateAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(candidate))
	if err != nil {
		return false
	}
	referenceAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(reference))
	if err != nil {
		return false
	}
	return candidateAt.After(referenceAt)
}

// describeStaleConnectStatus renders the identity of a rejected pre-existing
// row for the timeout error, so the operator can see what Cloud still holds.
func describeStaleConnectStatus(status cloudIntegrationReadyResponse) string {
	parts := []string{}
	if id := strings.TrimSpace(firstNonEmpty(status.CurrentConnectionID, status.ConnectionID)); id != "" {
		parts = append(parts, "connection "+id)
	}
	connectedAt, lastAuthAt := connectStatusAuthTimes(status)
	if at := firstNonEmpty(lastAuthAt, connectedAt); at != "" {
		parts = append(parts, "authenticated "+at)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
