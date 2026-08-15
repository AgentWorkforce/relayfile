package relayfile

import (
	"encoding/json"
	"strings"
)

// slackProviderOwnedFields is the top-level canonical-record metadata Slack
// owns. These fields may be present in provider materializations and receipts,
// but they are never valid create/reply/update inputs. Keep this list aligned
// with the Slack adapter writeback contract.
var slackProviderOwnedFields = map[string]struct{}{
	"id":           {},
	"ts":           {},
	"createdAt":    {},
	"updatedAt":    {},
	"url":          {},
	"identifier":   {},
	"provider":     {},
	"objectType":   {},
	"objectId":     {},
	"workspaceId":  {},
	"connectionId": {},
	"_webhook":     {},
	"_connection":  {},
}

// providerWritableContent derives the requested provider input from a
// materialized canonical record. It deliberately leaves the stored file
// untouched: provider-owned receipt fields remain available locally, while
// the outbound adapter sees only fields the caller can write.
func providerWritableContent(provider, content string) string {
	provider = normalizeProvider(provider)
	if provider != "slack" && !strings.HasPrefix(provider, "slack-") {
		return content
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &object); err != nil || object == nil {
		return content
	}
	removed := false
	for field := range slackProviderOwnedFields {
		if _, exists := object[field]; !exists {
			continue
		}
		delete(object, field)
		removed = true
	}
	if !removed {
		return content
	}
	writable, err := json.Marshal(object)
	if err != nil {
		return content
	}
	return string(writable)
}

// providerReceiptExternalID resolves the provider-assigned object identity
// without ever treating the receipt as writable content. externalId is the
// provider-neutral contract; Slack's native ts is accepted for provider-style
// receipts that have not normalized it yet.
func providerReceiptExternalID(provider, explicit string, receipt map[string]any) string {
	if value := strings.TrimSpace(explicit); value != "" {
		return value
	}
	if value := receiptString(receipt, "externalId"); value != "" {
		return value
	}
	provider = normalizeProvider(provider)
	if provider == "slack" || strings.HasPrefix(provider, "slack-") {
		return receiptString(receipt, "ts")
	}
	return ""
}

func providerReceiptCanonicalPath(receipt map[string]any) string {
	return receiptString(receipt, "canonicalPath")
}

func receiptString(receipt map[string]any, field string) string {
	if len(receipt) == 0 {
		return ""
	}
	value, _ := receipt[field].(string)
	return strings.TrimSpace(value)
}
