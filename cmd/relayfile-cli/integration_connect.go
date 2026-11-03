package main

import (
	"errors"
	"fmt"
	"net/http"
)

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
