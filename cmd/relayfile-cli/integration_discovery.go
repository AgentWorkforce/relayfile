package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
)

// integrationCatalogEvent is one subscribable provider event as published by
// the cloud integration catalog (GET /api/v1/integrations/catalog). The CLI
// never ships its own per-provider event list: discovery only reports what the
// catalog publishes.
type integrationCatalogEvent struct {
	Name    string   `json:"name"`
	Native  []string `json:"native,omitempty"`
	Object  string   `json:"object,omitempty"`
	Action  string   `json:"action,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	Support string   `json:"support,omitempty"` // subscribable, known-only, unsupported; blank = unknown
	Mode    string   `json:"mode,omitempty"`    // e.g. oauth-dynamic-webhook
	Reason  string   `json:"reason,omitempty"`  // why the event is not subscribable, when it is not
	// Backend is the integration backend (nango or composio) whose metadata
	// published this event; the catalog's ?backend= selector picks it.
	Backend     string                  `json:"backend,omitempty"`
	Schema      *integrationEventSchema `json:"schema,omitempty"`
	Description string                  `json:"description,omitempty"`
}

// integrationEventSchema is the catalog's payload-schema status for an event.
// The catalog publishes {status, reason}; status "unknown" means no typed
// capture exists yet, so the CLI must not present an example as the schema.
type integrationEventSchema struct {
	Status string `json:"status,omitempty"`
	Reason string `json:"reason,omitempty"`
	ID     string `json:"id,omitempty"` // older catalogs published a bare schema id string
}

func (s *integrationEventSchema) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" {
		return nil
	}
	if strings.HasPrefix(trimmed, "\"") {
		var id string
		if err := json.Unmarshal(data, &id); err != nil {
			return err
		}
		*s = integrationEventSchema{ID: id}
		return nil
	}
	type plain integrationEventSchema
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*s = integrationEventSchema(decoded)
	return nil
}

func formatEventSchema(schema *integrationEventSchema) string {
	if schema == nil {
		return "-"
	}
	var parts []string
	if schema.ID != "" {
		parts = append(parts, schema.ID)
	}
	if schema.Status != "" {
		parts = append(parts, schema.Status)
	}
	if schema.Reason != "" {
		parts = append(parts, "("+schema.Reason+")")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

// integrationDiscoveryExitCode is the exit status for "the catalog does not
// publish an event list for this provider". It is distinct from generic
// failures (1) so callers can tell "nothing to discover" from "broken".
const integrationDiscoveryExitCode = 3

type integrationDiscoveryError struct {
	exitCode int
	err      error
}

func (e *integrationDiscoveryError) Error() string { return e.err.Error() }
func (e *integrationDiscoveryError) Unwrap() error { return e.err }
func (e *integrationDiscoveryError) ExitCode() int { return e.exitCode }

const (
	durableSubscriptionSource   = "openapi/relayfile-v1.openapi.yaml#CreateOrRenewDurableResourceSubscriptionRequest"
	durableSubscriptionEndpoint = "POST /v1/workspaces/{workspaceId}/subscriptions"
	webhookSubscriptionSource   = "relayfile-cloud docs/webhook-event-subscriptions.md"
	webhookSubscriptionEndpoint = "POST /v1/workspaces/{workspaceId}/webhooks"
	webhookCapabilityProbe      = "GET /v1/workspaces/{workspaceId}/webhooks?capabilities=eventTypes"
	webhookCapabilityExpect     = `{"eventTypes":true}`
	filesystemEventSchemaName   = "FilesystemEvent"
	filesystemEventSource       = "openapi/relayfile-v1.openapi.yaml#FilesystemEvent"

	integrationEventSubscribable = "subscribable"
	runtimeSubscribeReason       = "this CLI does not create subscriptions, and runtime subscribe capability is not established: the installed runtime does not accept event_types; availability must be confirmed by the runtime, not inferred from the catalog"
	runtimeSubscribePath         = "/integrations/subscribe"
	runtimeSubscribeStatus       = "candidate (Relay2087 proposal), not the installed runtime contract"
	runtimeSubscribeAuth         = "current-session registration; no agent name or other session"
	providerEventTypeNote        = "exact normalized provider event (e.g. comment.created) carried by verified ingress; distinct from the filesystem mutation type (file.created). Durable subscriptions and webhook eventTypes match on it."
	snapshotNote                 = "optional {path, contentType, encoding, content, truncated}; content is adapter-rendered file contents at `path` (provider snapshot), distinct from the event envelope (read it with `relayfile cat <path>`)"
)

type integrationFieldConstraints struct {
	Pattern     string   `json:"pattern,omitempty"`
	MinLength   *int     `json:"minLength,omitempty"`
	MaxLength   *int     `json:"maxLength,omitempty"`
	Minimum     *int     `json:"minimum,omitempty"`
	Maximum     *int     `json:"maximum,omitempty"`
	MinItems    *int     `json:"minItems,omitempty"`
	MaxItems    *int     `json:"maxItems,omitempty"`
	UniqueItems bool     `json:"uniqueItems,omitempty"`
	ItemPattern string   `json:"itemPattern,omitempty"`
	ItemMinLen  *int     `json:"itemMinLength,omitempty"`
	ItemMaxLen  *int     `json:"itemMaxLength,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Default     string   `json:"default,omitempty"`
}

type integrationSubscriptionField struct {
	Name        string                       `json:"name"`
	Required    bool                         `json:"required"`
	Type        string                       `json:"type"`
	Constraints *integrationFieldConstraints `json:"constraints,omitempty"`
	Description string                       `json:"description"`
}

type integrationCapabilityProbe struct {
	Request string `json:"request"`
	Expect  string `json:"expect"`
	Note    string `json:"note"`
}

type integrationSubscriptionMechanism struct {
	Source          string                         `json:"source"`
	Endpoint        string                         `json:"endpoint"`
	CapabilityProbe *integrationCapabilityProbe    `json:"capabilityProbe,omitempty"`
	Fields          []integrationSubscriptionField `json:"fields"`
}

// integrationHandoff is one native current-session subscribe request in the
// shape the agent runtime accepts: exactly provider, one resource glob, and
// event_types. describe emits one per distinct resource glob; the CLI does not
// send it.
type integrationHandoff struct {
	Provider   string   `json:"provider"`
	Resource   string   `json:"resource"`
	EventTypes []string `json:"event_types"`
}

// integrationRuntimeContract documents the runtime's native subscribe call so
// an agent in a supported session can perform it with the handoff values.
type integrationRuntimeContract struct {
	Status string   `json:"status"`
	Method string   `json:"method"`
	Path   string   `json:"path"`
	Body   []string `json:"body"`
	Auth   string   `json:"auth"`
}

type integrationSubscriptionDescription struct {
	DurableResourceSubscription integrationSubscriptionMechanism `json:"durableResourceSubscription"`
	WebhookSubscription         integrationSubscriptionMechanism `json:"webhookSubscription"`
}

type integrationRuntimeSubscribe struct {
	Available bool                       `json:"available"`
	Reason    string                     `json:"reason"`
	Contract  integrationRuntimeContract `json:"contract"`
}

type integrationRuntimeDescription struct {
	Subscribe integrationRuntimeSubscribe `json:"subscribe"`
}

type integrationEnvelopeProperty struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Ref         string   `json:"ref,omitempty"`
	Format      string   `json:"format,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Required    bool     `json:"required"`
	Description string   `json:"description"`
}

type integrationEnvelopeDescription struct {
	Schema            string                        `json:"schema"`
	Source            string                        `json:"source"`
	Properties        []integrationEnvelopeProperty `json:"properties"`
	ProviderEventType string                        `json:"providerEventType"`
	Snapshot          string                        `json:"snapshot"`
}

type integrationEventExample struct {
	Illustrative      bool   `json:"illustrative"`
	EventID           string `json:"eventId"`
	Type              string `json:"type"`
	Path              string `json:"path"`
	Revision          string `json:"revision"`
	Origin            string `json:"origin"`
	Provider          string `json:"provider"`
	ProviderEventType string `json:"providerEventType"`
	CorrelationID     string `json:"correlationId"`
	Timestamp         string `json:"timestamp"`
}

type integrationEventsOutput struct {
	Provider string                    `json:"provider"`
	VFSRoot  string                    `json:"vfsRoot,omitempty"`
	Source   string                    `json:"source,omitempty"`
	Version  string                    `json:"version,omitempty"`
	Backend  string                    `json:"backend,omitempty"`
	Events   []integrationCatalogEvent `json:"events"`
}

// integrationHandoffExclusion records a selected event that gets no handoff
// because the catalog does not mark it subscribable; the catalog's own support
// level and reason are carried so the gap is explained, not hidden.
type integrationHandoffExclusion struct {
	Event   string `json:"event"`
	Support string `json:"support"`
	Reason  string `json:"reason,omitempty"`
}

type integrationDescribeOutput struct {
	Provider        integrationCatalogEntry            `json:"provider"`
	Events          []integrationCatalogEvent          `json:"events"`
	Subscription    integrationSubscriptionDescription `json:"subscription"`
	Handoff         []integrationHandoff               `json:"handoff"`
	HandoffExcluded []integrationHandoffExclusion      `json:"handoffExcluded"`
	Runtime         integrationRuntimeDescription      `json:"runtime"`
	Envelope        integrationEnvelopeDescription     `json:"envelope"`
	// Example is omitted when the selection is empty (for example
	// --subscribable on a provider whose published events are all known-only).
	Example *integrationEventExample `json:"example,omitempty"`
}

func intPtr(value int) *int { return &value }

// durableSubscriptionFields mirrors
// CreateOrRenewDurableResourceSubscriptionRequest in
// openapi/relayfile-v1.openapi.yaml. TestIntegrationDiscoveryMatchesOpenAPI
// fails if the two drift.
func durableSubscriptionFields() []integrationSubscriptionField {
	eventTypeConstraints := func() *integrationFieldConstraints {
		return &integrationFieldConstraints{
			MinItems:    intPtr(1),
			MaxItems:    intPtr(32),
			UniqueItems: true,
			ItemPattern: "^[A-Za-z0-9][A-Za-z0-9._:-]*$",
			ItemMinLen:  intPtr(1),
			ItemMaxLen:  intPtr(128),
		}
	}
	return []integrationSubscriptionField{
		{
			Name: "provider", Required: true, Type: "string",
			Constraints: &integrationFieldConstraints{Pattern: "^[a-z0-9][a-z0-9_-]*$", MinLength: intPtr(1), MaxLength: intPtr(128)},
			Description: "Catalog provider id (e.g. jira).",
		},
		{
			Name: "resourceRef", Required: true, Type: "string",
			Constraints: &integrationFieldConstraints{Pattern: "^/.+/by-id/[^/]+$", MinLength: intPtr(1)},
			Description: "Absolute canonical Relayfile by-id resource leaf.",
		},
		{
			Name: "eventTypes", Required: true, Type: "array<string>",
			Constraints: eventTypeConstraints(),
			Description: "Exact normalized provider event names to deliver (catalog event names).",
		},
		{
			Name: "terminalEventTypes", Required: false, Type: "array<string>",
			Constraints: eventTypeConstraints(),
			Description: "Event names that mark the resource terminal and retire the subscription.",
		},
		{
			Name: "subscriberId", Required: true, Type: "string",
			Constraints: &integrationFieldConstraints{MinLength: intPtr(1), MaxLength: intPtr(512)},
			Description: "Stable id of the consumer that claims deliveries.",
		},
		{
			Name: "intent", Required: false, Type: "string",
			Constraints: &integrationFieldConstraints{MinLength: intPtr(1), MaxLength: intPtr(4096)},
			Description: "Free-text reason for the subscription, returned with deliveries.",
		},
		{
			Name: "ttlSeconds", Required: true, Type: "integer",
			Constraints: &integrationFieldConstraints{Minimum: intPtr(60), Maximum: intPtr(2592000)},
			Description: "Lifetime before the subscription expires unless renewed.",
		},
	}
}

func webhookSubscriptionFields() []integrationSubscriptionField {
	return []integrationSubscriptionField{
		{
			Name: "url", Required: true, Type: "string",
			Description: "Endpoint that receives deliveries.",
		},
		{
			Name: "pathGlobs", Required: true, Type: "array<string>",
			Description: "Relayfile path globs to deliver; `*` matches within a segment, `**` across segments.",
		},
		{
			Name: "eventTypes", Required: false, Type: "array<string>",
			Constraints: &integrationFieldConstraints{MaxItems: intPtr(32)},
			Description: "Exact normalized provider event names, no wildcards; omitting keeps path-only behaviour.",
		},
		{
			Name: "secret", Required: false, Type: "string",
			Description: "Shared secret used to sign deliveries.",
		},
		{
			Name: "signatureScheme", Required: false, Type: "string",
			Constraints: &integrationFieldConstraints{Enum: []string{"relay", "standard"}, Default: "relay"},
			Description: "Delivery signature format.",
		},
	}
}

// filesystemEventProperties mirrors FilesystemEvent in
// openapi/relayfile-v1.openapi.yaml. TestIntegrationDiscoveryMatchesOpenAPI
// fails if the two drift.
func filesystemEventProperties() []integrationEnvelopeProperty {
	return []integrationEnvelopeProperty{
		{Name: "eventId", Type: "string", Required: true, Description: "Unique event id."},
		{
			Name: "type", Type: "string", Required: true,
			Enum: []string{
				"file.created", "file.updated", "file.deleted",
				"dir.created", "dir.deleted",
				"sync.error", "sync.ignored", "sync.suppressed", "sync.stale", "sync.reconcile",
				"writeback.failed", "writeback.succeeded",
			},
			Description: "Filesystem mutation type (not the provider event name).",
		},
		{Name: "path", Type: "string", Required: true, Description: "Relayfile path that changed; empty for pathless control events such as sync.reconcile."},
		{Name: "revision", Type: "string", Required: true, Description: "File revision after the change."},
		{Name: "contentHash", Type: "string", Description: "SHA-256 of the decoded file bytes for create/update events."},
		{Name: "origin", Type: "string", Required: true, Enum: []string{"provider_sync", "agent_write", "system"}, Description: "Who caused the change."},
		{Name: "provider", Type: "string", Description: "Catalog provider id."},
		{Name: "correlationId", Type: "string", Required: true, Description: "Correlation id for tracing."},
		{Name: "timestamp", Type: "string", Format: "date-time", Required: true, Description: "When the change was recorded."},
		{Name: "typeMetadata", Type: "object", Ref: "FileTypeMetadata", Description: "File type metadata (file or symlink)."},
	}
}

func runIntegrationEvents(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("integration events", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cloudAPIURL := fs.String("cloud-api-url", envOrDefault("RELAYFILE_CLOUD_API_URL", defaultCloudAPIURL), "Relayfile Cloud API URL")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	refresh := fs.Bool("refresh", false, "refresh the cached provider catalog")
	subscribable := fs.Bool("subscribable", false, "only list events the catalog marks subscribable")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"cloud-api-url": true,
		"json":          false,
		"refresh":       false,
		"subscribable":  false,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: %s integration events PROVIDER [--subscribable] [--json] [--refresh]", programName())
	}
	entry, events, err := loadIntegrationEventCatalog(*cloudAPIURL, fs.Arg(0), *refresh)
	if err != nil {
		return err
	}
	if *subscribable {
		events = filterSubscribableEvents(events)
	}
	if *jsonOutput {
		return writeJSON(stdout, integrationEventsOutput{
			Provider: entry.ID,
			VFSRoot:  entry.VFSRoot,
			Source:   entry.EventsSource,
			Version:  entry.EventsVersion,
			Backend:  entry.EventsBackend,
			Events:   events,
		})
	}
	fmt.Fprintln(stdout, "event\tnative\tobject\taction\tsupport\tmode\tbackend\tpaths")
	for _, event := range events {
		fmt.Fprintln(stdout, strings.Join(integrationEventRow(event), "\t"))
	}
	if line := formatEventsProvenance(entry); line != "" {
		fmt.Fprintln(stdout, line)
	}
	return nil
}

func filterSubscribableEvents(events []integrationCatalogEvent) []integrationCatalogEvent {
	filtered := make([]integrationCatalogEvent, 0, len(events))
	for _, event := range events {
		if isSubscribableEvent(event) {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func isSubscribableEvent(event integrationCatalogEvent) bool {
	return strings.EqualFold(strings.TrimSpace(event.Support), integrationEventSubscribable)
}

func formatEventsProvenance(entry integrationCatalogEntry) string {
	var parts []string
	if source := strings.TrimSpace(entry.EventsSource); source != "" {
		parts = append(parts, "source: "+source)
	}
	if version := strings.TrimSpace(entry.EventsVersion); version != "" {
		parts = append(parts, "version: "+version)
	}
	if backend := strings.TrimSpace(entry.EventsBackend); backend != "" {
		parts = append(parts, "backend: "+backend)
	}
	return strings.Join(parts, " ")
}

func runIntegrationDescribe(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("integration describe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cloudAPIURL := fs.String("cloud-api-url", envOrDefault("RELAYFILE_CLOUD_API_URL", defaultCloudAPIURL), "Relayfile Cloud API URL")
	eventName := fs.String("event", "", "describe one event by normalized or native name")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	refresh := fs.Bool("refresh", false, "refresh the cached provider catalog")
	subscribable := fs.Bool("subscribable", false, "only describe events the catalog marks subscribable")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"cloud-api-url": true,
		"event":         true,
		"json":          false,
		"refresh":       false,
		"subscribable":  false,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: %s integration describe PROVIDER [--event EVENT] [--subscribable] [--json] [--refresh]", programName())
	}
	entry, events, err := loadIntegrationEventCatalog(*cloudAPIURL, fs.Arg(0), *refresh)
	if err != nil {
		return err
	}
	selected := events
	if *subscribable {
		selected = filterSubscribableEvents(events)
	}
	if name := strings.TrimSpace(*eventName); name != "" {
		event, ok := findIntegrationEvent(events, name)
		if ok && *subscribable && !isSubscribableEvent(event) {
			return fmt.Errorf("event %q for %s is not subscribable (support: %s%s)", event.Name, entry.ID, defaultIfBlank(event.Support, "unknown"), reasonSuffix(event.Reason))
		}
		if !ok {
			names := make([]string, 0, len(events))
			for _, candidate := range events {
				names = append(names, candidate.Name)
			}
			return fmt.Errorf("event %q is not in the %s event catalog; known events: %s", name, entry.ID, strings.Join(names, ", "))
		}
		selected = []integrationCatalogEvent{event}
	}
	output := buildIntegrationDescription(entry, selected)
	if *jsonOutput {
		return writeJSON(stdout, output)
	}
	return writeIntegrationDescription(stdout, output, strings.TrimSpace(*eventName) != "")
}

// loadIntegrationEventCatalog resolves provider in the dynamic catalog and
// returns its normalized (sorted) events. A provider whose catalog entry has
// no events is an integrationDiscoveryError: discovery never substitutes a
// built-in list.
func loadIntegrationEventCatalog(cloudAPIURL, provider string, refresh bool) (integrationCatalogEntry, []integrationCatalogEvent, error) {
	apiURL := strings.TrimRight(strings.TrimSpace(cloudAPIURL), "/")
	if apiURL == "" {
		apiURL = defaultCloudAPIURL
	}
	provider = strings.TrimSpace(provider)
	entries, err := loadAvailableIntegrationCatalog(apiURL, refresh)
	if err != nil {
		return integrationCatalogEntry{}, nil, err
	}
	entry, ok := findIntegrationCatalogEntry(entries, provider)
	if !ok {
		return integrationCatalogEntry{}, nil, fmt.Errorf("provider %q is not in the integration catalog (try: %s integration available --search %s)", provider, programName(), provider)
	}
	if len(entry.Events) == 0 {
		return integrationCatalogEntry{}, nil, &integrationDiscoveryError{
			exitCode: integrationDiscoveryExitCode,
			err:      fmt.Errorf("event catalog not published by %s for %s; discovery needs a catalog that lists provider events", apiURL, entry.ID),
		}
	}
	return entry, normalizeIntegrationEvents(entry.Events), nil
}

func reasonSuffix(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	return "; " + strings.TrimSpace(reason)
}

func findIntegrationCatalogEntry(entries []integrationCatalogEntry, provider string) (integrationCatalogEntry, bool) {
	for _, entry := range entries {
		if strings.EqualFold(entry.ID, provider) {
			return entry, true
		}
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.ConfigKey) != "" && strings.EqualFold(entry.ConfigKey, provider) {
			return entry, true
		}
	}
	return integrationCatalogEntry{}, false
}

func findIntegrationEvent(events []integrationCatalogEvent, name string) (integrationCatalogEvent, bool) {
	for _, event := range events {
		if strings.EqualFold(event.Name, name) {
			return event, true
		}
	}
	for _, event := range events {
		for _, native := range event.Native {
			if strings.EqualFold(native, name) {
				return event, true
			}
		}
	}
	return integrationCatalogEvent{}, false
}

func normalizeIntegrationEvents(events []integrationCatalogEvent) []integrationCatalogEvent {
	normalized := make([]integrationCatalogEvent, 0, len(events))
	for _, event := range events {
		event.Native = sortedStrings(event.Native)
		event.Paths = sortedStrings(event.Paths)
		normalized = append(normalized, event)
	}
	sort.SliceStable(normalized, func(i, j int) bool { return normalized[i].Name < normalized[j].Name })
	return normalized
}

func sortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}

func integrationEventRow(event integrationCatalogEvent) []string {
	return []string{
		defaultIfBlank(event.Name, "-"),
		joinOrDash(event.Native),
		defaultIfBlank(event.Object, "-"),
		defaultIfBlank(event.Action, "-"),
		defaultIfBlank(event.Support, "unknown"),
		defaultIfBlank(event.Mode, "-"),
		defaultIfBlank(event.Backend, "-"),
		joinOrDash(event.Paths),
	}
}

func joinOrDash(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ",")
}

func buildIntegrationDescription(entry integrationCatalogEntry, events []integrationCatalogEvent) integrationDescribeOutput {
	provider := entry
	provider.Events = nil

	// Handoff values are only built from events the catalog marks
	// subscribable, independently of the display filter: a known-only or
	// unsupported event stays describable but never becomes a subscribe
	// request the backend cannot register.
	subscribable := filterSubscribableEvents(events)
	handoff := buildIntegrationHandoff(entry.ID, subscribable)
	excluded := make([]integrationHandoffExclusion, 0, len(events)-len(subscribable))
	for _, event := range events {
		if isSubscribableEvent(event) {
			continue
		}
		excluded = append(excluded, integrationHandoffExclusion{
			Event:   event.Name,
			Support: defaultIfBlank(event.Support, "unknown"),
			Reason:  strings.TrimSpace(event.Reason),
		})
	}
	var example *integrationEventExample
	if len(events) > 0 {
		built := buildIntegrationEventExample(entry, events[0])
		example = &built
	}

	return integrationDescribeOutput{
		Provider: provider,
		Events:   events,
		Subscription: integrationSubscriptionDescription{
			DurableResourceSubscription: integrationSubscriptionMechanism{
				Source:   durableSubscriptionSource,
				Endpoint: durableSubscriptionEndpoint,
				Fields:   durableSubscriptionFields(),
			},
			WebhookSubscription: integrationSubscriptionMechanism{
				Source:   webhookSubscriptionSource,
				Endpoint: webhookSubscriptionEndpoint,
				CapabilityProbe: &integrationCapabilityProbe{
					Request: webhookCapabilityProbe,
					Expect:  webhookCapabilityExpect,
					Note:    "must answer " + webhookCapabilityExpect + " before creating a subscription filtered by eventTypes",
				},
				Fields: webhookSubscriptionFields(),
			},
		},
		Handoff:         handoff,
		HandoffExcluded: excluded,
		Runtime: integrationRuntimeDescription{
			Subscribe: integrationRuntimeSubscribe{
				Available: false,
				Reason:    runtimeSubscribeReason,
				Contract: integrationRuntimeContract{
					Status: runtimeSubscribeStatus,
					Method: "POST",
					Path:   runtimeSubscribePath,
					Body:   []string{"provider", "resource", "event_types"},
					Auth:   runtimeSubscribeAuth,
				},
			},
		},
		Envelope: integrationEnvelopeDescription{
			Schema:            filesystemEventSchemaName,
			Source:            filesystemEventSource,
			Properties:        filesystemEventProperties(),
			ProviderEventType: providerEventTypeNote,
			Snapshot:          snapshotNote,
		},
		Example: example,
	}
}

// buildIntegrationHandoff groups the selected events by delivered path glob,
// one handoff per resource, in resource order. Events without catalog paths
// get a handoff with an empty resource so the gap is visible rather than
// guessed.
func buildIntegrationHandoff(provider string, events []integrationCatalogEvent) []integrationHandoff {
	byResource := map[string][]string{}
	for _, event := range events {
		paths := event.Paths
		if len(paths) == 0 {
			paths = []string{""}
		}
		for _, path := range paths {
			byResource[path] = append(byResource[path], event.Name)
		}
	}
	resources := make([]string, 0, len(byResource))
	for resource := range byResource {
		resources = append(resources, resource)
	}
	sort.Strings(resources)
	handoff := make([]integrationHandoff, 0, len(resources))
	for _, resource := range resources {
		handoff = append(handoff, integrationHandoff{
			Provider:   provider,
			Resource:   resource,
			EventTypes: sortedStrings(byResource[resource]),
		})
	}
	return handoff
}

func buildIntegrationEventExample(entry integrationCatalogEntry, event integrationCatalogEvent) integrationEventExample {
	eventType := "file.updated"
	switch strings.ToLower(strings.TrimSpace(event.Action)) {
	case "created", "updated", "deleted":
		eventType = "file." + strings.ToLower(strings.TrimSpace(event.Action))
	}
	path := strings.TrimRight(defaultIfBlank(entry.VFSRoot, "/"+entry.ID), "/") + "/<path>"
	if len(event.Paths) > 0 {
		path = strings.ReplaceAll(event.Paths[0], "**", "<id>")
		path = strings.ReplaceAll(path, "*", "<id>")
	}
	return integrationEventExample{
		Illustrative:      true,
		EventID:           "<event-id>",
		Type:              eventType,
		Path:              path,
		Revision:          "<revision>",
		Origin:            "provider_sync",
		Provider:          entry.ID,
		ProviderEventType: event.Name,
		CorrelationID:     "<correlation-id>",
		Timestamp:         "<rfc3339-timestamp>",
	}
}

func writeIntegrationDescription(stdout io.Writer, output integrationDescribeOutput, single bool) error {
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	provider := output.Provider

	fmt.Fprintln(tw, "Provider")
	fmt.Fprintf(tw, "  id\t%s\n", provider.ID)
	fmt.Fprintf(tw, "  display name\t%s\n", defaultIfBlank(provider.DisplayName, "-"))
	fmt.Fprintf(tw, "  config key\t%s\n", defaultIfBlank(provider.ConfigKey, "-"))
	fmt.Fprintf(tw, "  vfs root\t%s\n", defaultIfBlank(provider.VFSRoot, "-"))
	fmt.Fprintf(tw, "  backends\t%s\n", formatCatalogBackends(provider))
	fmt.Fprintf(tw, "  auth mode\t%s\n", defaultIfBlank(provider.AuthMode, "-"))
	fmt.Fprintf(tw, "  docs\t%s\n", defaultIfBlank(provider.Docs, "-"))
	if err := tw.Flush(); err != nil {
		return err
	}

	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Events")
	if single {
		event := output.Events[0]
		fmt.Fprintf(tw, "  event\t%s\n", event.Name)
		fmt.Fprintf(tw, "  native\t%s\n", joinOrDash(event.Native))
		fmt.Fprintf(tw, "  object\t%s\n", defaultIfBlank(event.Object, "-"))
		fmt.Fprintf(tw, "  action\t%s\n", defaultIfBlank(event.Action, "-"))
		fmt.Fprintf(tw, "  support\t%s\n", defaultIfBlank(event.Support, "unknown"))
		fmt.Fprintf(tw, "  mode\t%s\n", defaultIfBlank(event.Mode, "-"))
		fmt.Fprintf(tw, "  reason\t%s\n", defaultIfBlank(event.Reason, "-"))
		fmt.Fprintf(tw, "  backend\t%s\n", defaultIfBlank(event.Backend, "-"))
		fmt.Fprintf(tw, "  paths\t%s\n", joinOrDash(event.Paths))
		fmt.Fprintf(tw, "  schema\t%s\n", formatEventSchema(event.Schema))
		fmt.Fprintf(tw, "  description\t%s\n", defaultIfBlank(event.Description, "-"))
	} else {
		fmt.Fprintln(tw, "  event\tnative\tobject\taction\tsupport\tmode\tbackend\tpaths")
		for _, event := range output.Events {
			fmt.Fprintln(tw, "  "+strings.Join(integrationEventRow(event), "\t"))
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if line := formatEventsProvenance(provider); line != "" {
		fmt.Fprintf(stdout, "  %s\n", line)
	}

	sub := output.Subscription
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Subscription options")
	for _, mechanism := range []struct {
		label string
		value integrationSubscriptionMechanism
	}{
		{"durable resource subscription", sub.DurableResourceSubscription},
		{"webhook subscription", sub.WebhookSubscription},
	} {
		fmt.Fprintf(stdout, "  %s: %s\n", mechanism.label, mechanism.value.Endpoint)
		fmt.Fprintf(stdout, "    source: %s\n", mechanism.value.Source)
		if probe := mechanism.value.CapabilityProbe; probe != nil {
			fmt.Fprintf(stdout, "    capability probe: %s must answer %s before creating a filtered subscription\n", probe.Request, probe.Expect)
		}
		for _, field := range mechanism.value.Fields {
			fmt.Fprintf(tw, "    %s\t%s\t%s\t%s\t%s\n", field.Name, requiredLabel(field.Required), field.Type, formatFieldConstraints(field.Constraints), field.Description)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Handoff values (one native subscribe request per resource)")
	fmt.Fprintln(tw, "  provider\tresource\tevent_types")
	for _, handoff := range output.Handoff {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", handoff.Provider, defaultIfBlank(handoff.Resource, "-"), joinOrDash(handoff.EventTypes))
	}
	if len(output.Handoff) == 0 {
		fmt.Fprintln(tw, "  -\t-\t-")
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, excluded := range output.HandoffExcluded {
		fmt.Fprintf(stdout, "  no handoff: %s (support: %s%s)\n", excluded.Event, excluded.Support, reasonSuffix(excluded.Reason))
	}
	contract := output.Runtime.Subscribe.Contract
	fmt.Fprintf(stdout, "  runtime subscribe: not available (%s)\n", output.Runtime.Subscribe.Reason)
	fmt.Fprintf(stdout, "  runtime contract [%s]: %s %s body {%s}; auth: %s\n", contract.Status, contract.Method, contract.Path, strings.Join(contract.Body, ", "), contract.Auth)

	envelope := output.Envelope
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Delivered envelope")
	fmt.Fprintf(stdout, "  schema: %s (%s)\n", envelope.Schema, envelope.Source)
	for _, property := range envelope.Properties {
		typeLabel := property.Type
		if property.Ref != "" {
			typeLabel += " (" + property.Ref + ")"
		}
		if property.Format != "" {
			typeLabel += " (" + property.Format + ")"
		}
		detail := property.Description
		if len(property.Enum) > 0 {
			detail += " One of: " + strings.Join(property.Enum, ", ")
		}
		fmt.Fprintf(tw, "    %s\t%s\t%s\t%s\n", property.Name, requiredLabel(property.Required), typeLabel, detail)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "  providerEventType: %s\n", envelope.ProviderEventType)
	fmt.Fprintf(stdout, "  snapshot: %s\n", envelope.Snapshot)

	example := output.Example
	if example == nil {
		return nil
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Example (illustrative, not a captured payload)")
	for _, pair := range [][2]string{
		{"eventId", example.EventID},
		{"type", example.Type},
		{"path", example.Path},
		{"revision", example.Revision},
		{"origin", example.Origin},
		{"provider", example.Provider},
		{"providerEventType", example.ProviderEventType},
		{"correlationId", example.CorrelationID},
		{"timestamp", example.Timestamp},
	} {
		fmt.Fprintf(tw, "  %s\t%s\n", pair[0], pair[1])
	}
	return tw.Flush()
}

func requiredLabel(required bool) string {
	if required {
		return "required"
	}
	return "optional"
}

func formatFieldConstraints(c *integrationFieldConstraints) string {
	if c == nil {
		return "-"
	}
	var parts []string
	if c.Pattern != "" {
		parts = append(parts, "pattern "+c.Pattern)
	}
	if text := formatRange("length", c.MinLength, c.MaxLength); text != "" {
		parts = append(parts, text)
	}
	if text := formatRange("value", c.Minimum, c.Maximum); text != "" {
		parts = append(parts, text)
	}
	if text := formatRange("items", c.MinItems, c.MaxItems); text != "" {
		parts = append(parts, text)
	}
	if c.UniqueItems {
		parts = append(parts, "unique")
	}
	if c.ItemPattern != "" {
		parts = append(parts, "item pattern "+c.ItemPattern)
	}
	if text := formatRange("item length", c.ItemMinLen, c.ItemMaxLen); text != "" {
		parts = append(parts, text)
	}
	if len(c.Enum) > 0 {
		parts = append(parts, "one of "+strings.Join(c.Enum, "|"))
	}
	if c.Default != "" {
		parts = append(parts, "default "+c.Default)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "; ")
}

func formatRange(label string, min, max *int) string {
	switch {
	case min != nil && max != nil:
		return fmt.Sprintf("%s %d..%d", label, *min, *max)
	case min != nil:
		return fmt.Sprintf("%s >=%d", label, *min)
	case max != nil:
		return fmt.Sprintf("%s <=%d", label, *max)
	default:
		return ""
	}
}
