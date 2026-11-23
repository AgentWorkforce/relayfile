package relayfile

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// EnvelopeKindProviderWrite marks an envelope that was accepted through
// POST /v1/internal/provider-writes rather than the webhook-envelope route.
// The envelope rides the ordinary ingress queue (persistence, retry,
// dead-letter, replay and ingress counters are shared) but is parsed by
// ParseProviderWriteEnvelope instead of a provider adapter.
const EnvelopeKindProviderWrite = "provider_write"

const (
	// providerWriteMaxPathLength mirrors the cloud provider-writes guard.
	providerWriteMaxPathLength = 2048
	// providerWriteMaxFiles bounds one request; the whole body is already
	// capped by the HTTP MaxBodyBytes limit, this keeps the apply loop bounded.
	providerWriteMaxFiles = 1000
)

var (
	providerWriteEventTypePattern = regexp.MustCompile(`^[a-z_]{1,60}(?:\.[a-z_]{1,60})?$`)
	providerWriteRootPattern      = regexp.MustCompile(`^/(github|jira|linear|slack)/`)
)

// ProviderWriteFile is one upsert carried by a provider write. Deletes are
// deliberately not accepted on this route: terminal provider states (closed,
// merged, archived, ...) are written as data, and a genuine upstream deletion
// travels through the webhook-envelope route as a file.deleted action.
type ProviderWriteFile struct {
	Op          string         `json:"op,omitempty"`
	Path        string         `json:"path"`
	Content     string         `json:"content"`
	ContentType string         `json:"contentType,omitempty"`
	Encoding    string         `json:"encoding,omitempty"`
	Semantics   *FileSemantics `json:"semantics,omitempty"`
}

// ProviderWriteEvent names the canonical record of a provider write.
type ProviderWriteEvent struct {
	Path              string `json:"path"`
	ProviderEventType string `json:"providerEventType"`
	ResourceRef       string `json:"resourceRef,omitempty"`
}

// ProviderWriteRequest is the body of POST /v1/internal/provider-writes.
// It matches the cloud provider-writes body shape; CorrelationID comes from
// the X-Correlation-Id header, never from the body.
type ProviderWriteRequest struct {
	WorkspaceID string              `json:"workspaceId"`
	Files       []ProviderWriteFile `json:"files"`
	Event       ProviderWriteEvent  `json:"event"`

	CorrelationID string `json:"-"`
}

// providerWriteSafeAbsolutePath mirrors the cloud guard: leading slash, bounded
// length and no empty, "." or ".." segments.
func providerWriteSafeAbsolutePath(path string) bool {
	if !strings.HasPrefix(path, "/") || len(path) > providerWriteMaxPathLength {
		return false
	}
	for _, part := range strings.Split(path[1:], "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func providerWriteEventPathValid(provider, path string) bool {
	return provider != "" && providerWriteSafeAbsolutePath(path) && strings.HasPrefix(path, "/"+provider+"/")
}

func providerWriteFilePathValid(provider, path string) bool {
	if !providerWriteSafeAbsolutePath(path) {
		return false
	}
	if path == "/LAYOUT.md" {
		return true
	}
	if providerWriteEventPathValid(provider, path) {
		return true
	}
	return provider != "" && strings.HasPrefix(path, "/discovery/"+provider+"/")
}

// ValidateProviderWrite enforces the cloud provider-writes invariants and
// returns the single provider root selected by event.path. Every failure wraps
// ErrInvalidInput.
func ValidateProviderWrite(req ProviderWriteRequest) (string, error) {
	invalid := func(format string, args ...any) (string, error) {
		return "", fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, args...))
	}
	if strings.TrimSpace(req.WorkspaceID) == "" {
		return invalid("workspaceId is required")
	}
	match := providerWriteRootPattern.FindStringSubmatch(req.Event.Path)
	if match == nil {
		return invalid("event.path must start with /github/, /jira/, /linear/ or /slack/")
	}
	provider := match[1]
	if !providerWriteEventPathValid(provider, req.Event.Path) {
		return invalid("event.path is not a safe absolute path")
	}
	if !providerWriteEventTypePattern.MatchString(req.Event.ProviderEventType) {
		return invalid("event.providerEventType must match ^[a-z_]{1,60}(?:\\.[a-z_]{1,60})?$")
	}
	if req.Event.ResourceRef != "" && !providerWriteEventPathValid(provider, req.Event.ResourceRef) {
		return invalid("event.resourceRef must be a safe absolute path under /%s/", provider)
	}
	if len(req.Files) == 0 {
		return invalid("files must not be empty")
	}
	if len(req.Files) > providerWriteMaxFiles {
		return invalid("files exceeds the maximum of %d entries", providerWriteMaxFiles)
	}
	canonicalCount := 0
	for i, file := range req.Files {
		switch strings.ToLower(strings.TrimSpace(file.Op)) {
		case "", "upsert":
		case "delete":
			return invalid("files[%d]: deletes are not accepted on this route; write terminal provider states as data", i)
		default:
			return invalid("files[%d]: unsupported op %q", i, file.Op)
		}
		if !providerWriteFilePathValid(provider, file.Path) {
			return invalid("files[%d]: path must be /LAYOUT.md or lie under /%s/ or /discovery/%s/", i, provider, provider)
		}
		encoding, err := normalizeEncoding(file.Encoding)
		if err != nil {
			return invalid("files[%d]: unsupported encoding %q", i, file.Encoding)
		}
		if err := validateEncodedContent(file.Content, encoding); err != nil {
			return invalid("files[%d]: content is not valid %s", i, encoding)
		}
		if file.Path == req.Event.Path {
			canonicalCount++
		}
	}
	if canonicalCount != 1 {
		return invalid("exactly one file path must equal event.path (found %d)", canonicalCount)
	}
	return provider, nil
}

func newProviderWriteEnvelopeID() string {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Sprintf("pw_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("pw_%d_%s", time.Now().UnixNano(), hex.EncodeToString(suffix[:]))
}

// IngestProviderWrite validates a provider write and queues it as a
// provider_write envelope. It returns ErrInvalidInput for contract violations
// and ErrQueueFull when the ingress queue cannot accept it; the caller maps
// those to 400 and 429. Application is asynchronous: each file becomes a
// provider_sync file.created/file.updated event once the worker runs.
func (s *Store) IngestProviderWrite(req ProviderWriteRequest) (QueuedResponse, error) {
	provider, err := ValidateProviderWrite(req)
	if err != nil {
		return QueuedResponse{}, err
	}
	payload, err := providerWritePayload(req)
	if err != nil {
		return QueuedResponse{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	envelopeID := newProviderWriteEnvelopeID()
	return s.IngestEnvelope(WebhookEnvelopeRequest{
		EnvelopeID:    envelopeID,
		Kind:          EnvelopeKindProviderWrite,
		WorkspaceID:   strings.TrimSpace(req.WorkspaceID),
		Provider:      provider,
		DeliveryID:    envelopeID,
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Payload:       payload,
		CorrelationID: req.CorrelationID,
	})
}

// providerWritePayload stores the validated body as a generic payload so the
// envelope persists, snapshots and replays exactly like any other envelope.
// The keys are nested under "files"/"event" on purpose: the top-level
// "path"/"providerObjectId"/"origin" keys the envelope pipeline inspects for
// coalescing and suppression are absent, so provider writes never coalesce
// with each other or with webhook envelopes.
func providerWritePayload(req ProviderWriteRequest) (map[string]any, error) {
	raw, err := json.Marshal(struct {
		Kind  string              `json:"kind"`
		Files []ProviderWriteFile `json:"files"`
		Event ProviderWriteEvent  `json:"event"`
	}{Kind: EnvelopeKindProviderWrite, Files: req.Files, Event: req.Event})
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// ParseProviderWriteEnvelope turns a persisted provider_write envelope back
// into one upsert action per file. The payload was validated at ingest; the
// invariants are re-checked here so a tampered or legacy snapshot cannot
// smuggle paths outside the provider root into the apply loop.
func ParseProviderWriteEnvelope(req WebhookEnvelopeRequest) ([]ApplyAction, error) {
	raw, err := json.Marshal(req.Payload)
	if err != nil {
		return nil, fmt.Errorf("provider write payload: %w", err)
	}
	var body ProviderWriteRequest
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("provider write payload: %w", err)
	}
	body.WorkspaceID = req.WorkspaceID
	provider, err := ValidateProviderWrite(body)
	if err != nil {
		return nil, err
	}
	if provider != normalizeProvider(req.Provider) {
		return nil, fmt.Errorf("%w: envelope provider %q does not match event.path provider %q", ErrInvalidInput, req.Provider, provider)
	}
	actions := make([]ApplyAction, 0, len(body.Files))
	for _, file := range body.Files {
		encoding, _ := normalizeEncoding(file.Encoding)
		contentType := strings.TrimSpace(file.ContentType)
		if contentType == "" {
			contentType = "text/markdown"
		}
		action := ApplyAction{
			Type:        ActionFileUpsert,
			Path:        normalizePath(file.Path),
			Content:     file.Content,
			ContentType: contentType,
			Encoding:    encoding,
		}
		if file.Semantics != nil {
			action.Semantics = *file.Semantics
		}
		actions = append(actions, action)
	}
	return actions, nil
}

// providerWriteActionProvider attributes each written path. Files under the
// provider tree and that provider's discovery contract belong to the provider;
// the shared root layout is not provider-owned, so its file and event carry no
// provider and the digest attributes it by path like any other root file.
func providerWriteActionProvider(provider, path string) string {
	if normalizePath(path) == "/LAYOUT.md" {
		return ""
	}
	return normalizeProvider(provider)
}
