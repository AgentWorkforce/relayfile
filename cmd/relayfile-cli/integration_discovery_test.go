package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// discoveryCatalogFixture is deliberately unsorted so the tests prove the CLI
// sorts events, native names and paths itself.
// discoveryCatalogFixture is a synthetic catalog in the cloud helper's flat
// shape (eventCatalogVersion 1). It is test data, not the published Jira or
// Slack event set: it deliberately mixes support values so both the
// subscribable and known-only paths are exercised.
// discoveryCatalogFixture is SYNTHETIC (not captured from a catalog producer).
// It follows the G1 flat event shape. Canonical wire forms: schema is an
// object {status, reason} or null. The bare-string schema on comment.created
// ("jira/comment") is a parse-only legacy tolerance exercised here so an older
// schema-id form cannot crash decoding; it is NOT part of the OpenAPI contract
// and producer conformance must come from a source-derived fixture.
const discoveryCatalogFixture = `{
  "version": "v_test",
  "eventCatalogVersion": 1,
  "providers": [
    {
      "id": "jira",
      "displayName": "Jira",
      "configKey": "jira",
      "vfsRoot": "/jira",
      "backends": ["nango"],
      "authMode": "oauth",
      "eventsSource": "relayfile-cloud/integrations/jira",
      "eventsVersion": "2026-11-01",
      "eventsBackend": "nango",
      "events": [
        {"name": "sprint.started", "native": ["sprint_started"], "object": "sprint", "action": "started", "support": "subscribable", "mode": "oauth-dynamic-webhook", "backend": "nango", "schema": {"status": "unknown", "reason": "no typed capture"}, "paths": ["/jira/sprints/*.json"]},
        {"name": "project.created", "native": ["project_created"], "object": "project", "action": "created", "support": "known-only", "reason": "not in the OAuth dynamic webhook set", "backend": "nango", "schema": {"status": "unknown", "reason": "no typed capture"}, "paths": ["/jira/projects/*.json"]},
        {"name": "issue.updated", "native": ["jira:issue_updated"], "action": null, "schema": null},
        {"name": "comment.created", "native": ["comment_created"], "object": "comment", "action": "created", "support": "subscribable", "mode": "oauth-dynamic-webhook", "backend": "nango", "schema": "jira/comment", "paths": ["/jira/issues/*/comments/*.json"]}
      ]
    },
    {
      "id": "slack",
      "displayName": "Slack",
      "configKey": "slack",
      "vfsRoot": "/slack",
      "backends": ["nango"],
      "authMode": "oauth",
      "eventsSource": "relayfile-cloud/integrations/slack",
      "eventsVersion": "1",
      "eventsBackend": "nango",
      "events": [
        {"name": "message.created", "native": ["message"], "object": "message", "action": "created", "support": "subscribable", "mode": "events-api", "backend": "nango", "schema": {"status": "unknown", "reason": "no typed capture"}, "paths": ["/slack/channels/*/messages/*.json"]},
        {"name": "reaction.added", "native": ["reaction_added"], "object": "reaction", "action": null, "support": "known-only", "reason": "adapter recognizes the event; no backend subscription path", "backend": "nango", "schema": {"status": "unknown", "reason": "no typed capture"}, "paths": ["/slack/channels/*/messages/*.json"]}
      ]
    },
    {"id": "github", "displayName": "GitHub", "vfsRoot": "/github", "backends": ["nango"]}
  ]
}`

func newDiscoveryCatalogServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/integrations/catalog":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(discoveryCatalogFixture))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func setupDiscoveryTest(t *testing.T) *httptest.Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	return newDiscoveryCatalogServer(t)
}

func TestIntegrationEventsHumanTable(t *testing.T) {
	server := setupDiscoveryTest(t)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "all events",
			args: []string{"jira"},
			want: strings.Join([]string{
				"event\tnative\tobject\taction\tsupport\tmode\tbackend\tpaths",
				"comment.created\tcomment_created\tcomment\tcreated\tsubscribable\toauth-dynamic-webhook\tnango\t/jira/issues/*/comments/*.json",
				"issue.updated\tjira:issue_updated\t-\t-\tunknown\t-\t-\t-",
				"project.created\tproject_created\tproject\tcreated\tknown-only\t-\tnango\t/jira/projects/*.json",
				"sprint.started\tsprint_started\tsprint\tstarted\tsubscribable\toauth-dynamic-webhook\tnango\t/jira/sprints/*.json",
				"source: relayfile-cloud/integrations/jira version: 2026-11-01 backend: nango",
				"note: " + eventSupportScopeNote,
				"",
			}, "\n"),
		},
		{
			name: "subscribable only, case-insensitive provider",
			args: []string{"JIRA", "--subscribable"},
			want: strings.Join([]string{
				"event\tnative\tobject\taction\tsupport\tmode\tbackend\tpaths",
				"comment.created\tcomment_created\tcomment\tcreated\tsubscribable\toauth-dynamic-webhook\tnango\t/jira/issues/*/comments/*.json",
				"sprint.started\tsprint_started\tsprint\tstarted\tsubscribable\toauth-dynamic-webhook\tnango\t/jira/sprints/*.json",
				"source: relayfile-cloud/integrations/jira version: 2026-11-01 backend: nango",
				"note: " + eventSupportScopeNote,
				"",
			}, "\n"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			args := append([]string{"events"}, tc.args...)
			args = append(args, "--cloud-api-url", server.URL, "--refresh")
			if err := runIntegration(args, nil, &out); err != nil {
				t.Fatalf("integration events: %v", err)
			}
			if out.String() != tc.want {
				t.Fatalf("unexpected output:\n%s\nwant:\n%s", out.String(), tc.want)
			}
		})
	}
}

func TestIntegrationEventsJSON(t *testing.T) {
	server := setupDiscoveryTest(t)
	var out bytes.Buffer
	if err := runIntegration([]string{"events", "jira", "--json", "--cloud-api-url", server.URL}, nil, &out); err != nil {
		t.Fatalf("integration events --json: %v", err)
	}
	var payload integrationEventsOutput
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v\n%s", err, out.String())
	}
	if payload.Provider != "jira" || payload.VFSRoot != "/jira" {
		t.Fatalf("provider/vfsRoot = %q/%q", payload.Provider, payload.VFSRoot)
	}
	if payload.Source != "relayfile-cloud/integrations/jira" || payload.Version != "2026-11-01" {
		t.Fatalf("source/version = %q/%q", payload.Source, payload.Version)
	}
	var names []string
	for _, event := range payload.Events {
		names = append(names, event.Name)
	}
	if want := []string{"comment.created", "issue.updated", "project.created", "sprint.started"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("event names = %v, want %v", names, want)
	}
	known := payload.Events[2]
	if known.Support != "known-only" || known.Reason != "not in the OAuth dynamic webhook set" {
		t.Fatalf("known-only event = %+v", known)
	}
	if payload.Events[0].Mode != "oauth-dynamic-webhook" || payload.Events[0].Schema == nil || payload.Events[0].Schema.ID != "jira/comment" {
		t.Fatalf("comment.created = %+v", payload.Events[0])
	}
	if payload.Backend != "nango" || payload.Events[0].Backend != "nango" {
		t.Fatalf("backend = %q / %q", payload.Backend, payload.Events[0].Backend)
	}
	if payload.Events[3].Schema == nil || payload.Events[3].Schema.Status != "unknown" || payload.Events[3].Schema.Reason != "no typed capture" {
		t.Fatalf("sprint.started schema = %+v", payload.Events[3].Schema)
	}
	if payload.Events[1].Schema != nil || payload.Events[1].Action != "" {
		t.Fatalf("issue.updated must tolerate null schema/action: %+v", payload.Events[1])
	}
}

// TestIntegrationSlackDiscovery covers a second provider through the same
// catalog contract: no provider-specific code or native aliases in the CLI.
func TestIntegrationSlackDiscovery(t *testing.T) {
	server := setupDiscoveryTest(t)
	var out bytes.Buffer
	if err := runIntegration([]string{"events", "slack", "--cloud-api-url", server.URL}, nil, &out); err != nil {
		t.Fatalf("integration events slack: %v", err)
	}
	want := strings.Join([]string{
		"event\tnative\tobject\taction\tsupport\tmode\tbackend\tpaths",
		"message.created\tmessage\tmessage\tcreated\tsubscribable\tevents-api\tnango\t/slack/channels/*/messages/*.json",
		"reaction.added\treaction_added\treaction\t-\tknown-only\t-\tnango\t/slack/channels/*/messages/*.json",
		"source: relayfile-cloud/integrations/slack version: 1 backend: nango",
		"note: " + eventSupportScopeNote,
		"",
	}, "\n")
	if out.String() != want {
		t.Fatalf("unexpected output:\n%s\nwant:\n%s", out.String(), want)
	}

	out.Reset()
	if err := runIntegration([]string{"describe", "slack", "--event", "message", "--json", "--cloud-api-url", server.URL}, nil, &out); err != nil {
		t.Fatalf("integration describe slack: %v", err)
	}
	var payload integrationDescribeOutput
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v\n%s", err, out.String())
	}
	if want := []integrationHandoff{{Provider: "slack", Resource: "/slack/channels/*/messages/*.json", EventTypes: []string{"message.created"}}}; !reflect.DeepEqual(payload.Handoff, want) {
		t.Fatalf("handoff = %+v, want %+v", payload.Handoff, want)
	}
	if len(payload.Events) != 1 || payload.Events[0].Schema == nil || payload.Events[0].Schema.Status != "unknown" {
		t.Fatalf("events = %+v", payload.Events)
	}
	if payload.Example.Path != "/slack/channels/<id>/messages/<id>.json" || !payload.Example.Illustrative {
		t.Fatalf("example = %+v", payload.Example)
	}

	out.Reset()
	if err := runIntegration([]string{"describe", "slack", "--event", "reaction_added", "--cloud-api-url", server.URL}, nil, &out); err != nil {
		t.Fatalf("integration describe slack known-only: %v", err)
	}
	collapsed := regexp.MustCompile(`[ \t]+`).ReplaceAllString(out.String(), " ")
	for _, want := range []string{" support known-only\n", " schema unknown (no typed capture)\n", " backend nango\n"} {
		if !strings.Contains(collapsed, want) {
			t.Errorf("describe output missing %q:\n%s", want, out.String())
		}
	}
}

func TestIntegrationEventsCatalogNotPublished(t *testing.T) {
	server := setupDiscoveryTest(t)
	for _, sub := range []string{"events", "describe"} {
		t.Run(sub, func(t *testing.T) {
			var out bytes.Buffer
			err := runIntegration([]string{sub, "github", "--cloud-api-url", server.URL + "/"}, nil, &out)
			if err == nil {
				t.Fatalf("expected error, got output:\n%s", out.String())
			}
			want := "event catalog not published by " + server.URL + " for github; discovery needs a catalog that lists provider events"
			if err.Error() != want {
				t.Fatalf("error = %q, want %q", err.Error(), want)
			}
			if code := checkpointExitCode(err); code != 3 {
				t.Fatalf("exit code = %d, want 3", code)
			}
			if out.Len() != 0 {
				t.Fatalf("expected no fallback output, got:\n%s", out.String())
			}
		})
	}
}

func TestIntegrationEventsUnknownProvider(t *testing.T) {
	server := setupDiscoveryTest(t)
	var out bytes.Buffer
	err := runIntegration([]string{"events", "nosuch", "--cloud-api-url", server.URL}, nil, &out)
	if err == nil {
		t.Fatal("expected error")
	}
	want := `provider "nosuch" is not in the integration catalog (try: relayfile integration available --search nosuch)`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		t.Fatalf("unknown provider should be a plain error, got exit code %d", coded.ExitCode())
	}
}

func TestIntegrationDescribeEventByNativeName(t *testing.T) {
	server := setupDiscoveryTest(t)
	var out bytes.Buffer
	if err := runIntegration([]string{"describe", "jira", "--event", "comment_created", "--json", "--cloud-api-url", server.URL}, nil, &out); err != nil {
		t.Fatalf("integration describe: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v\n%s", err, out.String())
	}
	handoffs := payload["handoff"].([]any)
	if len(handoffs) != 1 {
		t.Fatalf("handoff = %#v", handoffs)
	}
	handoff := handoffs[0].(map[string]any)
	if handoff["provider"] != "jira" ||
		handoff["resource"] != "/jira/issues/*/comments/*.json" ||
		!reflect.DeepEqual(handoff["event_types"], []any{"comment.created"}) {
		t.Fatalf("handoff = %#v", handoff)
	}
	for _, forbidden := range []string{"eventTypes", "pathGlobs", "agent", "name", "session"} {
		if _, ok := handoff[forbidden]; ok {
			t.Fatalf("handoff must only carry provider/resource/event_types, got %q", forbidden)
		}
	}
	runtime := payload["runtime"].(map[string]any)["subscribe"].(map[string]any)
	if runtime["available"] != false || !strings.Contains(runtime["reason"].(string), "not established") {
		t.Fatalf("runtime.subscribe = %#v", runtime)
	}
	contract := runtime["contract"].(map[string]any)
	if !strings.Contains(contract["status"].(string), "candidate") || contract["method"] != "POST" || contract["path"] != "/integrations/subscribe" ||
		!reflect.DeepEqual(contract["body"], []any{"provider", "resource", "event_types"}) {
		t.Fatalf("runtime.subscribe.contract = %#v", contract)
	}
	subscription := payload["subscription"].(map[string]any)
	if _, ok := subscription["owner"]; ok {
		t.Fatal("subscription.owner must not be emitted")
	}
	if _, ok := subscription["recommended"]; ok {
		t.Fatal("subscription.recommended must not be emitted")
	}
	durable := subscription["durableResourceSubscription"].(map[string]any)
	if durable["source"] != durableSubscriptionSource || durable["endpoint"] != "POST /v1/workspaces/{workspaceId}/subscriptions" {
		t.Fatalf("durable = %#v", durable)
	}
	webhook := subscription["webhookSubscription"].(map[string]any)
	probe := webhook["capabilityProbe"].(map[string]any)
	if probe["request"] != "GET /v1/workspaces/{workspaceId}/webhooks?capabilities=eventTypes" || probe["expect"] != `{"eventTypes":true}` {
		t.Fatalf("capability probe = %#v", probe)
	}
	envelope := payload["envelope"].(map[string]any)
	if envelope["schema"] != "FilesystemEvent" {
		t.Fatalf("envelope.schema = %v", envelope["schema"])
	}
	if !strings.Contains(envelope["snapshot"].(string), "adapter-rendered file contents at `path` (provider snapshot), distinct from the event envelope") {
		t.Fatalf("envelope.snapshot = %v", envelope["snapshot"])
	}
	example := payload["example"].(map[string]any)
	if example["illustrative"] != true || example["type"] != "file.created" || example["providerEventType"] != "comment.created" ||
		example["path"] != "/jira/issues/<id>/comments/<id>.json" || example["origin"] != "provider_sync" || example["provider"] != "jira" {
		t.Fatalf("example = %#v", example)
	}
	events := payload["events"].([]any)
	if len(events) != 1 || events[0].(map[string]any)["name"] != "comment.created" {
		t.Fatalf("events = %#v", events)
	}
	provider := payload["provider"].(map[string]any)
	if _, ok := provider["events"]; ok || provider["id"] != "jira" {
		t.Fatalf("provider = %#v", provider)
	}
}

func TestIntegrationDescribeHumanOutput(t *testing.T) {
	server := setupDiscoveryTest(t)
	var out bytes.Buffer
	if err := runIntegration([]string{"describe", "jira", "--event", "comment.created", "--cloud-api-url", server.URL}, nil, &out); err != nil {
		t.Fatalf("integration describe: %v", err)
	}
	text := out.String()
	t.Logf("describe output:\n%s", text)
	for _, want := range []string{
		"Provider\n", "Events\n", "Subscription options\n", "Handoff values (one native subscribe request per resource; exact provider-event subscriptions only)\n", "Delivered envelope\n",
		"Example (illustrative, not a captured payload)\n",
		"POST /v1/workspaces/{workspaceId}/subscriptions",
		"POST /v1/workspaces/{workspaceId}/webhooks",
		"runtime subscribe: not available (",
		"runtime contract [candidate (Relay2087 proposal), not the installed runtime contract]: POST /integrations/subscribe body {provider, resource, event_types}",
		"providerEventType: exact normalized provider event",
		"type               file.created",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q", want)
		}
	}
	// The native HTTP contract is documented; an invented runnable CLI
	// subscribe command (relay subscribe --provider/--event) must not be.
	if strings.Contains(text, "relay subscribe") || strings.Contains(text, "--provider") || strings.Contains(text, "--event-type") || strings.Contains(text, "\x1b[") {
		t.Errorf("output must not print a runnable subscribe command or use colors")
	}
}

func TestIntegrationDescribeSubscribableFilter(t *testing.T) {
	server := setupDiscoveryTest(t)
	var out bytes.Buffer
	if err := runIntegration([]string{"describe", "jira", "--subscribable", "--json", "--cloud-api-url", server.URL}, nil, &out); err != nil {
		t.Fatalf("integration describe --subscribable: %v", err)
	}
	var payload integrationDescribeOutput
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []integrationHandoff{
		{Provider: "jira", Resource: "/jira/issues/*/comments/*.json", EventTypes: []string{"comment.created"}},
		{Provider: "jira", Resource: "/jira/sprints/*.json", EventTypes: []string{"sprint.started"}},
	}
	if !reflect.DeepEqual(payload.Handoff, want) {
		t.Fatalf("handoff = %+v, want %+v", payload.Handoff, want)
	}

	out.Reset()
	err := runIntegration([]string{"describe", "jira", "--subscribable", "--event", "project.created", "--cloud-api-url", server.URL}, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "not subscribable (support: known-only; not in the OAuth dynamic webhook set)") {
		t.Fatalf("expected not-subscribable error, got %v", err)
	}

	out.Reset()
	if err := runIntegration([]string{"describe", "jira", "--event", "project.created", "--json", "--cloud-api-url", server.URL}, nil, &out); err != nil {
		t.Fatalf("known-only event must still be describable without the filter: %v", err)
	}
	var knownOnly integrationDescribeOutput
	if err := json.Unmarshal(out.Bytes(), &knownOnly); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(knownOnly.Events) != 1 || len(knownOnly.Handoff) != 0 {
		t.Fatalf("known-only event must be described but get no handoff: events=%d handoff=%+v", len(knownOnly.Events), knownOnly.Handoff)
	}
	wantExcluded := []integrationHandoffExclusion{{Event: "project.created", Support: "known-only", Reason: "not in the OAuth dynamic webhook set"}}
	if !reflect.DeepEqual(knownOnly.HandoffExcluded, wantExcluded) {
		t.Fatalf("handoffExcluded = %+v, want %+v", knownOnly.HandoffExcluded, wantExcluded)
	}
	if knownOnly.Example == nil || knownOnly.Example.ProviderEventType != "project.created" {
		t.Fatalf("example = %+v", knownOnly.Example)
	}
}

// TestIntegrationDescribeHandoffOnlySubscribable: the default (unfiltered)
// describe still lists every published event, but handoff values are built
// only from subscribable ones and every excluded event is named with the
// catalog's support level and reason, in JSON and in the human output.
func TestIntegrationDescribeHandoffOnlySubscribable(t *testing.T) {
	server := setupDiscoveryTest(t)
	var out bytes.Buffer
	if err := runIntegration([]string{"describe", "jira", "--json", "--cloud-api-url", server.URL}, nil, &out); err != nil {
		t.Fatalf("integration describe: %v", err)
	}
	var payload integrationDescribeOutput
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Events) != 4 {
		t.Fatalf("expected all 4 published events, got %d", len(payload.Events))
	}
	wantHandoff := []integrationHandoff{
		{Provider: "jira", Resource: "/jira/issues/*/comments/*.json", EventTypes: []string{"comment.created"}},
		{Provider: "jira", Resource: "/jira/sprints/*.json", EventTypes: []string{"sprint.started"}},
	}
	if !reflect.DeepEqual(payload.Handoff, wantHandoff) {
		t.Fatalf("handoff = %+v, want %+v", payload.Handoff, wantHandoff)
	}
	wantExcluded := []integrationHandoffExclusion{
		{Event: "issue.updated", Support: "unknown"},
		{Event: "project.created", Support: "known-only", Reason: "not in the OAuth dynamic webhook set"},
	}
	if !reflect.DeepEqual(payload.HandoffExcluded, wantExcluded) {
		t.Fatalf("handoffExcluded = %+v, want %+v", payload.HandoffExcluded, wantExcluded)
	}
	for _, handoff := range payload.Handoff {
		for _, name := range handoff.EventTypes {
			if name == "project.created" || name == "issue.updated" {
				t.Fatalf("non-subscribable event %q leaked into handoff %+v", name, handoff)
			}
		}
	}

	out.Reset()
	if err := runIntegration([]string{"describe", "jira", "--cloud-api-url", server.URL}, nil, &out); err != nil {
		t.Fatalf("integration describe (human): %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"no handoff: project.created (support: known-only; not in the OAuth dynamic webhook set)\n",
		"no handoff: issue.updated (support: unknown)\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("human output missing %q:\n%s", want, text)
		}
	}
}

// TestIntegrationDescribeSubscribableEmptySelection: when every published
// event is known-only, `describe --subscribable` returns an empty selection
// (matching `events --subscribable`) instead of failing; exit code 3 stays
// reserved for a provider whose catalog publishes no event list at all.
func TestIntegrationDescribeSubscribableEmptySelection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"eventCatalogVersion":1,"providers":[{"id":"jira","displayName":"Jira","vfsRoot":"/jira","backends":["nango"],"eventsSource":"relayfile-cloud/integrations/jira","eventsVersion":"1","eventsBackend":"nango","events":[{"name":"project.created","native":["project_created"],"object":"project","action":"created","paths":["/jira/projects/*.json"],"support":"known-only","reason":"not in the OAuth dynamic webhook set","backend":"nango","schema":{"status":"unknown","reason":"no schema published"}}]}]}`))
	}))
	t.Cleanup(server.Close)

	var out bytes.Buffer
	if err := runIntegration([]string{"describe", "jira", "--subscribable", "--json", "--cloud-api-url", server.URL}, nil, &out); err != nil {
		t.Fatalf("describe --subscribable must not fail on an empty selection: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if events := payload["events"].([]any); len(events) != 0 {
		t.Fatalf("events = %#v, want empty", events)
	}
	if handoff := payload["handoff"].([]any); len(handoff) != 0 {
		t.Fatalf("handoff = %#v, want empty", handoff)
	}
	if _, ok := payload["example"]; ok {
		t.Fatalf("example must be omitted for an empty selection: %#v", payload["example"])
	}
	if payload["runtime"].(map[string]any)["subscribe"].(map[string]any)["available"] != false {
		t.Fatal("runtime.subscribe.available must stay false")
	}

	out.Reset()
	if err := runIntegration([]string{"describe", "jira", "--subscribable", "--cloud-api-url", server.URL}, nil, &out); err != nil {
		t.Fatalf("describe --subscribable (human): %v", err)
	}
	text := out.String()
	if strings.Contains(text, "Example (illustrative") || !strings.Contains(text, "Handoff values") {
		t.Fatalf("unexpected human output:\n%s", text)
	}

	out.Reset()
	err := runIntegration([]string{"events", "jira", "--subscribable", "--json", "--cloud-api-url", server.URL}, nil, &out)
	if err != nil {
		t.Fatalf("events --subscribable: %v", err)
	}
	var eventsOut integrationEventsOutput
	if err := json.Unmarshal(out.Bytes(), &eventsOut); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(eventsOut.Events) != 0 {
		t.Fatalf("events --subscribable = %+v, want empty", eventsOut.Events)
	}
}

// TestIntegrationEventsRefetchesPreUpgradeCache: a dynamic catalog cache
// written by an older CLI (no format marker, no events) must not satisfy
// discovery for the rest of its TTL; the cloud is asked again and the cache
// is rewritten with the current format.
func TestIntegrationEventsRefetchesPreUpgradeCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if err := ensureConfigDir(); err != nil {
		t.Fatal(err)
	}
	server := newDiscoveryCatalogServer(t)
	cacheKey := dynamicIntegrationCatalogCacheKey(server.URL)
	legacy := []byte(`{"apiUrl":` + strconv.Quote(cacheKey) + `,"fetchedAt":` + strconv.Quote(time.Now().UTC().Format(time.RFC3339)) + `,"version":"2026-11-01","providers":[{"id":"jira","displayName":"Jira","vfsRoot":"/jira","backends":["nango"]}]}`)
	if err := os.WriteFile(integrationCatalogCachePath(cacheKey), legacy, 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runIntegration([]string{"events", "jira", "--json", "--cloud-api-url", server.URL}, nil, &out); err != nil {
		t.Fatalf("events with a pre-upgrade cache must refetch, got %v", err)
	}
	var payload integrationEventsOutput
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Events) != 4 {
		t.Fatalf("expected 4 events after refetch, got %d", len(payload.Events))
	}
	cached, ok := readIntegrationCatalogCache(cacheKey)
	if !ok || cached.Format != integrationCatalogCacheFormat || !cachedCatalogIsFresh(cached, cacheKey) {
		t.Fatalf("rewritten cache = %+v ok=%v", cached, ok)
	}
	if len(cached.Providers) == 0 || len(cached.Providers[0].Events) == 0 {
		t.Fatal("rewritten cache must carry provider events")
	}
}

func TestIntegrationDescribeUnknownEvent(t *testing.T) {
	server := setupDiscoveryTest(t)
	var out bytes.Buffer
	err := runIntegration([]string{"describe", "jira", "--event", "nope.created", "--cloud-api-url", server.URL}, nil, &out)
	if err == nil {
		t.Fatal("expected error")
	}
	want := `event "nope.created" is not in the jira event catalog; known events: comment.created, issue.updated, project.created, sprint.started`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestIntegrationAvailableSearchMatchesEvents(t *testing.T) {
	server := setupDiscoveryTest(t)
	for _, query := range []string{"comment.created", "comment_created"} {
		var out bytes.Buffer
		if err := runIntegration([]string{"available", "--search", query, "--json", "--cloud-api-url", server.URL}, nil, &out); err != nil {
			t.Fatalf("integration available: %v", err)
		}
		var entries []integrationCatalogEntry
		if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(entries) != 1 || entries[0].ID != "jira" || len(entries[0].Events) != 4 {
			t.Fatalf("search %q = %+v, want jira only with events", query, entries)
		}
	}

	var human bytes.Buffer
	if err := runIntegration([]string{"available", "--search", "comment.created", "--cloud-api-url", server.URL}, nil, &human); err != nil {
		t.Fatalf("integration available: %v", err)
	}
	want := "provider\tdisplay_name\tbackends\tvfs_root\njira\tJira\tnango\t/jira\n"
	if human.String() != want {
		t.Fatalf("human output = %q, want %q", human.String(), want)
	}
}

func TestFallbackIntegrationCatalogHasNoEvents(t *testing.T) {
	for _, entry := range fallbackIntegrationCatalog() {
		if len(entry.Events) > 0 {
			t.Fatalf("fallback catalog entry %s must stay metadata-only", entry.ID)
		}
	}
}

// --- spec-drift protection -------------------------------------------------

type openAPISchemaShape struct {
	required   []string
	properties map[string]map[string]string // property -> flattened keyword -> value
	order      []string
}

// readOpenAPISchemaShape scans openapi/relayfile-v1.openapi.yaml by
// indentation (go.mod carries no YAML library) and extracts the required list
// and per-property keywords of components/schemas/<name>. Nested mappings
// such as items are flattened to "items.<keyword>"; enums are joined with ",".
func readOpenAPISchemaShape(t *testing.T, name string) openAPISchemaShape {
	t.Helper()
	file, err := os.Open("../../openapi/relayfile-v1.openapi.yaml")
	if err != nil {
		t.Fatalf("open spec: %v", err)
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read spec: %v", err)
	}

	indentOf := func(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }
	unquote := func(value string) string {
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '\'' || value[0] == '"') && value[len(value)-1] == value[0] {
			return value[1 : len(value)-1]
		}
		return value
	}
	inlineList := func(value string) []string {
		value = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(value), "["), "]")
		var out []string
		for _, item := range strings.Split(value, ",") {
			if item = unquote(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	}

	start := -1
	for i, line := range lines {
		if line == "    "+name+":" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("schema %s not found in spec", name)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "" && indentOf(lines[i]) <= 4 {
			end = i
			break
		}
	}
	block := lines[start+1 : end]

	shape := openAPISchemaShape{properties: map[string]map[string]string{}}
	for i := 0; i < len(block); i++ {
		line := block[i]
		trimmed := strings.TrimSpace(line)
		if indentOf(line) != 6 {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "required:"):
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "required:"))
			if value != "" {
				shape.required = inlineList(value)
				continue
			}
			for i+1 < len(block) && strings.HasPrefix(strings.TrimSpace(block[i+1]), "- ") {
				i++
				shape.required = append(shape.required, unquote(strings.TrimPrefix(strings.TrimSpace(block[i]), "- ")))
			}
		case trimmed == "properties:":
			var current string
			var prefix []string // keyword path for nested mappings, indexed by depth
			for i+1 < len(block) && (strings.TrimSpace(block[i+1]) == "" || indentOf(block[i+1]) > 6) {
				i++
				pline := block[i]
				ptrim := strings.TrimSpace(pline)
				if ptrim == "" {
					continue
				}
				indent := indentOf(pline)
				if indent == 8 {
					current = strings.TrimSuffix(ptrim, ":")
					shape.order = append(shape.order, current)
					shape.properties[current] = map[string]string{}
					prefix = nil
					continue
				}
				depth := (indent - 10) / 2
				if strings.HasPrefix(ptrim, "- ") {
					key := strings.Join(prefix[:depth], ".")
					props := shape.properties[current]
					item := unquote(strings.TrimPrefix(ptrim, "- "))
					if props[key] == "" {
						props[key] = item
					} else {
						props[key] += "," + item
					}
					continue
				}
				colon := strings.Index(ptrim, ":")
				if colon < 0 {
					t.Fatalf("unparsed spec line in %s.%s: %q", name, current, pline)
				}
				keyword, value := ptrim[:colon], strings.TrimSpace(ptrim[colon+1:])
				prefix = append(prefix[:depth], keyword)
				key := strings.Join(prefix, ".")
				if strings.HasPrefix(value, "[") {
					value = strings.Join(inlineList(value), ",")
				} else {
					value = unquote(value)
				}
				if value != "" {
					shape.properties[current][key] = value
				}
			}
		}
	}
	if len(shape.order) == 0 {
		t.Fatalf("schema %s: no properties parsed", name)
	}
	return shape
}

func withoutDescriptions(keywords map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range keywords {
		if key == "description" || strings.HasSuffix(key, ".description") {
			continue
		}
		out[key] = value
	}
	return out
}

func durableFieldKeywords(field integrationSubscriptionField) map[string]string {
	out := map[string]string{}
	setInt := func(key string, value *int) {
		if value != nil {
			out[key] = strconv.Itoa(*value)
		}
	}
	switch field.Type {
	case "array<string>":
		out["type"] = "array"
		out["items.type"] = "string"
	default:
		out["type"] = field.Type
	}
	if c := field.Constraints; c != nil {
		if c.Pattern != "" {
			out["pattern"] = c.Pattern
		}
		setInt("minLength", c.MinLength)
		setInt("maxLength", c.MaxLength)
		setInt("minimum", c.Minimum)
		setInt("maximum", c.Maximum)
		setInt("minItems", c.MinItems)
		setInt("maxItems", c.MaxItems)
		if c.UniqueItems {
			out["uniqueItems"] = "true"
		}
		if c.ItemPattern != "" {
			out["items.pattern"] = c.ItemPattern
		}
		setInt("items.minLength", c.ItemMinLen)
		setInt("items.maxLength", c.ItemMaxLen)
		if len(c.Enum) > 0 {
			out["enum"] = strings.Join(c.Enum, ",")
		}
	}
	return out
}

func envelopePropertyKeywords(property integrationEnvelopeProperty) map[string]string {
	out := map[string]string{}
	if property.Ref != "" {
		out["$ref"] = "#/components/schemas/" + property.Ref
		return out
	}
	out["type"] = property.Type
	if property.Format != "" {
		out["format"] = property.Format
	}
	if len(property.Enum) > 0 {
		out["enum"] = strings.Join(property.Enum, ",")
	}
	return out
}

func TestIntegrationDiscoveryMatchesOpenAPI(t *testing.T) {
	t.Run("CreateOrRenewDurableResourceSubscriptionRequest", func(t *testing.T) {
		shape := readOpenAPISchemaShape(t, "CreateOrRenewDurableResourceSubscriptionRequest")
		fields := durableSubscriptionFields()
		var names, required []string
		for _, field := range fields {
			names = append(names, field.Name)
			if field.Required {
				required = append(required, field.Name)
			}
			got := durableFieldKeywords(field)
			want := withoutDescriptions(shape.properties[field.Name])
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s: CLI describes %v, spec has %v", field.Name, got, want)
			}
		}
		assertSameSet(t, "properties", names, shape.order)
		assertSameSet(t, "required", required, shape.required)
	})
	t.Run("FilesystemEvent", func(t *testing.T) {
		shape := readOpenAPISchemaShape(t, "FilesystemEvent")
		var names, required []string
		for _, property := range filesystemEventProperties() {
			names = append(names, property.Name)
			if property.Required {
				required = append(required, property.Name)
			}
			got := envelopePropertyKeywords(property)
			want := withoutDescriptions(shape.properties[property.Name])
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s: CLI describes %v, spec has %v", property.Name, got, want)
			}
		}
		assertSameSet(t, "properties", names, shape.order)
		assertSameSet(t, "required", required, shape.required)
		if shape.properties["type"]["enum"] == "" || !strings.Contains(shape.properties["type"]["enum"], "file.created") {
			t.Fatalf("type enum not parsed from spec: %v", shape.properties["type"])
		}
	})
}

func assertSameSet(t *testing.T, label string, got, want []string) {
	t.Helper()
	got = append([]string{}, got...)
	want = append([]string{}, want...)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: CLI describes %v, spec has %v", label, got, want)
	}
}

// cloudPR6093CatalogExcerpt is an ACTUAL wire excerpt (public metadata, no
// credentials) from the catalog producer AgentWorkforce/cloud PR 6093,
// branch feat/integration-event-catalog, head
// 5e0337eddb8a792f275596094870ee9cc877ce17 (the excerpt ref; the branch
// merged to trunk as f75efc55fe9fae0911397e9d0c1e118fd8e07193, which also
// tightens alias resolution and is not a refreshed capture) (emission
// packages/web/app/api/v1/integrations/catalog/route.ts, helper
// packages/web/lib/integrations/integration-event-catalog.ts), relayed by the
// catalog owner on 2026-11-06. Unlike discoveryCatalogFixture it is
// source-derived: this test is the producer/consumer drift check.
const cloudPR6093CatalogExcerpt = `{"eventCatalogVersion":1,"version":"a8e5c7f561d5","providers":[{"id":"jira","backend":"nango","backends":["nango"],"vfsRoot":"/jira","eventsBackend":"nango","eventsVersion":"1","eventsSource":"adapter_catalog+nango_registration_manifest","events":[{"name":"comment.created","native":[],"object":"comment","action":"created","paths":["/jira/issues/**/comments/**"],"backend":"nango","support":"known-only","mode":"unknown","reason":"Adapter-known event; this native registration manifest does not declare it and the current Jira forward path does not preserve a separate typed record.","provenance":["adapter_catalog"],"schema":{"status":"unknown","reason":"A full provider-native payload schema is not published by these event sources. This is not a provider payload schema or a live delivery check."}}]},{"id":"slack","backend":"nango","backends":["nango"],"vfsRoot":"/slack","eventsBackend":"nango","eventsVersion":"1","eventsSource":"adapter_catalog+nango_connection_manifest+provider_contract","events":[{"name":"app_mention","native":["app_mention"],"object":"app_mention","action":null,"paths":["/slack/channels/**","/slack/users/**/messages/**"],"backend":"nango","support":"known-only","mode":"webhook","reason":"Slack forwarding recognizes message/reaction types, but ordinary Relayfile writes do not preserve trusted providerEventType for exact subscriptions. Connection subscription declarations alone do not establish typed delivery.","provenance":["nango_connection_manifest"],"schema":{"status":"unknown","reason":"A full provider-native payload schema is not published by these event sources. This is not a provider payload schema or a live delivery check."}},{"name":"message.created","native":[],"object":"message","action":"created","paths":["/slack/channels/**/messages/**","/slack/users/**/messages/**"],"backend":"nango","support":"known-only","mode":"unknown","reason":"Slack forwarding recognizes message/reaction types, but ordinary Relayfile writes do not preserve trusted providerEventType for exact subscriptions. Connection subscription declarations alone do not establish typed delivery.","provenance":["adapter_catalog","provider_contract"],"schema":{"status":"unknown","reason":"A full provider-native payload schema is not published by these event sources. This is not a provider payload schema or a live delivery check."}}]}]}`

func newCatalogServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/integrations/catalog" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

// TestIntegrationDiscoveryDecodesProducerExcerpt decodes the producer excerpt
// end to end. Scope, stated precisely: the excerpt is a selected-field
// projection relayed by the catalog owner (the producer also emits a
// per-provider eventsRegistration list, which the CLI intentionally does not
// model and must ignore; see below), not a fresh emission, and this is not an
// OpenAPI schema validation. Within that projection every field must decode
// without loss (provenance, null action, ** globs, string eventsVersion);
// a null action normalizes to an omitted output field rather than a byte
// round-trip. Every event is known-only, so no handoff may be produced and
// the CLI must not promote any capability the catalog did not state.
func TestIntegrationDiscoveryDecodesProducerExcerpt(t *testing.T) {
	server := newCatalogServer(t, cloudPR6093CatalogExcerpt)

	// Strict decode of the raw excerpt: an unknown field in the parts the CLI
	// models would mean producer/consumer drift within the projection.
	var strict struct {
		EventCatalogVersion int    `json:"eventCatalogVersion"`
		Version             string `json:"version"`
		Providers           []struct {
			ID            string                    `json:"id"`
			Backend       string                    `json:"backend"`
			Backends      []string                  `json:"backends"`
			VFSRoot       string                    `json:"vfsRoot"`
			EventsBackend string                    `json:"eventsBackend"`
			EventsVersion string                    `json:"eventsVersion"`
			EventsSource  string                    `json:"eventsSource"`
			Events        []integrationCatalogEvent `json:"events"`
		} `json:"providers"`
	}
	dec := json.NewDecoder(strings.NewReader(cloudPR6093CatalogExcerpt))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&strict); err != nil {
		t.Fatalf("producer excerpt has fields the CLI does not model (drift): %v", err)
	}
	if strict.EventCatalogVersion != 1 || strict.Version != "a8e5c7f561d5" || len(strict.Providers) != 2 {
		t.Fatalf("top-level = %+v", strict)
	}

	// The producer helper also emits an eventsRegistration OBJECT for Slack
	// ({backend, source, topics, verification}; type in
	// packages/web/lib/integrations/integration-event-catalog.ts at both
	// 5e0337ed and f75efc55). The value below is a labelled SYNTHETIC object
	// of that shape, not relayed bytes. The CLI does not model the field and
	// must ignore it rather than fail or surface its topics as events.
	withRegistration := strings.Replace(cloudPR6093CatalogExcerpt, `"eventsSource":"adapter_catalog+nango_connection_manifest+provider_contract",`,
		`"eventsSource":"adapter_catalog+nango_connection_manifest+provider_contract","eventsRegistration":{"backend":"nango","source":"nango_connection_manifest","topics":["message","message.channels"],"verification":"unknown"},`, 1)
	if withRegistration == cloudPR6093CatalogExcerpt {
		t.Fatal("eventsRegistration injection did not apply")
	}
	var lenient struct {
		Providers []integrationCatalogEntry `json:"providers"`
	}
	if err := json.Unmarshal([]byte(withRegistration), &lenient); err != nil {
		t.Fatalf("decode with eventsRegistration: %v", err)
	}
	if len(lenient.Providers) != 2 || len(lenient.Providers[1].Events) != 2 {
		t.Fatalf("eventsRegistration must be ignored, not change events: %+v", lenient.Providers)
	}
	for _, event := range lenient.Providers[1].Events {
		if event.Name == "message" || event.Name == "message.channels" {
			t.Fatalf("registration topics must not surface as events: %+v", lenient.Providers[1].Events)
		}
	}

	var out bytes.Buffer
	if err := runIntegration([]string{"events", "slack", "--json", "--cloud-api-url", server.URL}, nil, &out); err != nil {
		t.Fatalf("events slack: %v", err)
	}
	var events integrationEventsOutput
	if err := json.Unmarshal(out.Bytes(), &events); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if events.Source != "adapter_catalog+nango_connection_manifest+provider_contract" || events.Version != "1" || events.Backend != "nango" {
		t.Fatalf("provenance line = %+v", events)
	}
	if len(events.Events) != 2 {
		t.Fatalf("events = %+v", events.Events)
	}
	mention := events.Events[0]
	if mention.Name != "app_mention" || mention.Action != "" || mention.Mode != "webhook" || mention.Support != "known-only" ||
		!reflect.DeepEqual(mention.Provenance, []string{"nango_connection_manifest"}) ||
		!reflect.DeepEqual(mention.Paths, []string{"/slack/channels/**", "/slack/users/**/messages/**"}) ||
		mention.Schema == nil || mention.Schema.Status != "unknown" || !strings.HasPrefix(mention.Schema.Reason, "A full provider-native payload schema is not published") {
		t.Fatalf("app_mention = %+v schema=%+v", mention, mention.Schema)
	}
	created := events.Events[1]
	if created.Name != "message.created" || len(created.Native) != 0 || created.Mode != "unknown" ||
		!reflect.DeepEqual(created.Provenance, []string{"adapter_catalog", "provider_contract"}) {
		t.Fatalf("message.created = %+v", created)
	}

	out.Reset()
	if err := runIntegration([]string{"events", "slack", "--subscribable", "--json", "--cloud-api-url", server.URL}, nil, &out); err != nil {
		t.Fatalf("events slack --subscribable: %v", err)
	}
	if err := json.Unmarshal(out.Bytes(), &events); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(events.Events) != 0 {
		t.Fatalf("producer marks every Slack event known-only; --subscribable must be empty, got %+v", events.Events)
	}

	for _, provider := range []string{"jira", "slack"} {
		out.Reset()
		if err := runIntegration([]string{"describe", provider, "--json", "--cloud-api-url", server.URL}, nil, &out); err != nil {
			t.Fatalf("describe %s: %v", provider, err)
		}
		var described integrationDescribeOutput
		if err := json.Unmarshal(out.Bytes(), &described); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(described.Handoff) != 0 {
			t.Fatalf("%s: known-only events must not yield handoff values: %+v", provider, described.Handoff)
		}
		if len(described.HandoffExcluded) != len(described.Events) {
			t.Fatalf("%s: every event must be listed as excluded: %+v", provider, described.HandoffExcluded)
		}
		for _, excluded := range described.HandoffExcluded {
			if excluded.Support != "known-only" || excluded.Reason == "" {
				t.Fatalf("%s: exclusion must carry the producer's support and reason: %+v", provider, excluded)
			}
		}
		if described.Runtime.Subscribe.Available {
			t.Fatalf("%s: runtime.subscribe.available must stay false", provider)
		}
		if described.Example == nil || !described.Example.Illustrative {
			t.Fatalf("%s: example = %+v", provider, described.Example)
		}
		if described.Provider.EventsVersion != "1" || described.Provider.Backend != "nango" {
			t.Fatalf("%s: provider = %+v", provider, described.Provider)
		}
	}

	out.Reset()
	if err := runIntegration([]string{"describe", "jira", "--event", "comment.created", "--cloud-api-url", server.URL}, nil, &out); err != nil {
		t.Fatalf("describe jira --event comment.created: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"no handoff: comment.created (support: known-only; Adapter-known event;",
		"runtime subscribe: not available",
		"/jira/issues/<id>/comments/<id>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("human describe missing %q:\n%s", want, text)
		}
	}
}

// TestIntegrationDiscoveryRequestFailuresAreNotFallback covers CLI-001: with
// a cold or obsolete-format cache, a catalog request that fails (401/403/503,
// malformed JSON, transport failure) is a plain request error (exit 1) for
// both commands, human and --json. Discovery must never substitute the
// metadata-only local fallback, so neither the fallback-based "unknown
// provider" (jira is absent from the fallback) nor the exit-3 "not
// published" result (slack is present without events) may appear.
func TestIntegrationDiscoveryRequestFailuresAreNotFallback(t *testing.T) {
	statusServer := func(code int) func(t *testing.T) string {
		return func(t *testing.T) string {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, http.StatusText(code), code)
			}))
			t.Cleanup(server.Close)
			return server.URL
		}
	}
	failures := []struct {
		name  string
		serve func(t *testing.T) string
		want  string
	}{
		{"http401", statusServer(http.StatusUnauthorized), "returned HTTP 401"},
		{"http403", statusServer(http.StatusForbidden), "returned HTTP 403"},
		{"http503", statusServer(http.StatusServiceUnavailable), "returned HTTP 503"},
		{"malformed", func(t *testing.T) string {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"providers":[{"id":"slack"`))
			}))
			t.Cleanup(server.Close)
			return server.URL
		}, "is not valid JSON"},
		{"transport", func(t *testing.T) string {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
			apiURL := server.URL
			server.Close()
			return apiURL
		}, "failed: "},
	}
	caches := []struct {
		name  string
		prime func(t *testing.T, apiURL string)
	}{
		{"cold", func(t *testing.T, apiURL string) {}},
		{"obsolete", func(t *testing.T, apiURL string) {
			// Pre-upgrade cache without the format field: stale, so the
			// loader must refetch and surface the request failure.
			cacheKey := dynamicIntegrationCatalogCacheKey(apiURL)
			legacy := []byte(`{"apiUrl":` + strconv.Quote(cacheKey) + `,"fetchedAt":` + strconv.Quote(time.Now().UTC().Format(time.RFC3339)) + `,"version":"2026-11-01","providers":[{"id":"jira","displayName":"Jira","vfsRoot":"/jira","backends":["nango"]},{"id":"slack","displayName":"Slack","vfsRoot":"/slack","backends":["nango"]}]}`)
			if err := os.WriteFile(integrationCatalogCachePath(cacheKey), legacy, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, failure := range failures {
		for _, cache := range caches {
			for _, provider := range []string{"slack", "jira"} {
				for _, sub := range []string{"events", "describe"} {
					for _, jsonOutput := range []bool{false, true} {
						name := failure.name + "/" + cache.name + "/" + sub + "/" + provider
						if jsonOutput {
							name += "/json"
						}
						t.Run(name, func(t *testing.T) {
							t.Setenv("HOME", t.TempDir())
							clearRelayfileEnv(t)
							if err := ensureConfigDir(); err != nil {
								t.Fatal(err)
							}
							apiURL := failure.serve(t)
							cache.prime(t, apiURL)
							args := []string{sub, provider, "--cloud-api-url", apiURL}
							if jsonOutput {
								args = append(args, "--json")
							}
							var out bytes.Buffer
							err := runIntegration(args, nil, &out)
							if err == nil {
								t.Fatalf("expected a request failure, got output:\n%s", out.String())
							}
							if !strings.Contains(err.Error(), failure.want) {
								t.Fatalf("error = %q, want it to contain %q", err.Error(), failure.want)
							}
							for _, forbidden := range []string{"is not in the integration catalog", "event catalog not published"} {
								if strings.Contains(err.Error(), forbidden) {
									t.Fatalf("request failure reported as catalog data: %q", err.Error())
								}
							}
							var coded interface{ ExitCode() int }
							if errors.As(err, &coded) {
								t.Fatalf("request failure must be a plain exit-1 error, got exit code %d", coded.ExitCode())
							}
							if out.Len() != 0 {
								t.Fatalf("request failure must produce no discovery output, got:\n%s", out.String())
							}
							if _, ok := readIntegrationCatalogCache(dynamicIntegrationCatalogCacheKey(apiURL)); ok && cache.name == "cold" {
								t.Fatal("a failed request must not write a catalog cache")
							}
						})
					}
				}
			}
		}
	}
}

// TestIntegrationDiscoveryFreshCacheThenStrictRefresh shows the strict loader
// still honours a fresh current-format cache (same behaviour as before) while
// --refresh goes to the network and reports the failure.
func TestIntegrationDiscoveryFreshCacheThenStrictRefresh(t *testing.T) {
	server := setupDiscoveryTest(t)
	var out bytes.Buffer
	if err := runIntegration([]string{"events", "jira", "--json", "--cloud-api-url", server.URL}, nil, &out); err != nil {
		t.Fatalf("priming fetch: %v", err)
	}
	server.Close()

	out.Reset()
	if err := runIntegration([]string{"describe", "jira", "--json", "--cloud-api-url", server.URL}, nil, &out); err != nil {
		t.Fatalf("fresh cache must serve discovery after the server is gone: %v", err)
	}
	var payload integrationDescribeOutput
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Provider.ID != "jira" || len(payload.Events) != 4 {
		t.Fatalf("cached describe = provider %q with %d events", payload.Provider.ID, len(payload.Events))
	}

	out.Reset()
	err := runIntegration([]string{"describe", "jira", "--refresh", "--cloud-api-url", server.URL}, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "integration catalog request to ") || !strings.Contains(err.Error(), " failed: ") {
		t.Fatalf("--refresh must report the request failure, got err=%v output:\n%s", err, out.String())
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output on refresh failure, got:\n%s", out.String())
	}
}

// TestIntegrationAvailableKeepsFallbackOnRequestFailure pins the behaviour
// CLI-001 must preserve: `integration available` still falls back to the
// metadata-only local catalog when the request fails.
func TestIntegrationAvailableKeepsFallbackOnRequestFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	providers, err := loadAvailableIntegrationCatalog(server.URL, true)
	if err != nil {
		t.Fatalf("availability loader must keep its fallback, got %v", err)
	}
	if len(providers) == 0 || len(providers) != len(fallbackIntegrationCatalog()) {
		t.Fatalf("expected the local fallback catalog, got %d providers", len(providers))
	}
}
