package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentworkforce/relayfile/internal/relayfile"
)

type ServerConfig struct {
	JWTSecret          string
	InternalHMACSecret string
	InternalMaxSkew    time.Duration
	RateLimitMax       int
	RateLimitWindow    time.Duration
	MaxBodyBytes       int64
}

type Server struct {
	store              *relayfile.Store
	cfg                ServerConfig
	rateLimiter        *rateLimiter
	internalReplayMu   sync.Mutex
	internalReplaySeen map[string]time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	max     int
	entries map[string]rateEntry
}

type rateEntry struct {
	count   int
	resetAt time.Time
}

func NewServer(store *relayfile.Store) *Server {
	return NewServerWithConfig(store, ServerConfig{})
}

func NewServerWithConfig(store *relayfile.Store, cfg ServerConfig) *Server {
	if cfg.JWTSecret == "" {
		cfg.JWTSecret = "dev-secret"
	}
	if cfg.InternalHMACSecret == "" {
		cfg.InternalHMACSecret = "dev-internal-secret"
	}
	if cfg.InternalMaxSkew == 0 {
		cfg.InternalMaxSkew = 5 * time.Minute
	}
	if cfg.RateLimitMax < 0 {
		cfg.RateLimitMax = 0
	}
	if cfg.RateLimitWindow <= 0 {
		cfg.RateLimitWindow = time.Minute
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 1 << 20
	}
	var limiter *rateLimiter
	if cfg.RateLimitMax > 0 {
		limiter = &rateLimiter{
			window:  cfg.RateLimitWindow,
			max:     cfg.RateLimitMax,
			entries: map[string]rateEntry{},
		}
	}
	return &Server{
		store:              store,
		cfg:                cfg,
		rateLimiter:        limiter,
		internalReplaySeen: map[string]time.Time{},
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	if r.URL.Path == "/v1/internal/webhook-envelopes" && r.Method == http.MethodPost {
		s.handleInternalWebhookEnvelope(w, r)
		return
	}
	if r.URL.Path == "/v1/admin/backends" && r.Method == http.MethodGet {
		s.handleAdminBackends(w, r)
		return
	}
	if r.URL.Path == "/v1/admin/ingress" && r.Method == http.MethodGet {
		s.handleAdminIngress(w, r)
		return
	}
	if r.URL.Path == "/v1/admin/sync" && r.Method == http.MethodGet {
		s.handleAdminSync(w, r)
		return
	}

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) >= 5 && parts[0] == "v1" && parts[1] == "admin" && parts[2] == "replay" {
		s.handleAdminReplay(w, r, parts)
		return
	}

	if len(parts) < 4 || parts[0] != "v1" || parts[1] != "workspaces" {
		writeError(w, http.StatusNotFound, "not_found", "route not found", getCorrelationID(r))
		return
	}
	workspaceID := parts[2]

	var requiredScope string
	var route string
	switch {
	case len(parts) == 5 && parts[3] == "fs" && parts[4] == "tree" && r.Method == http.MethodGet:
		requiredScope = "fs:read"
		route = "tree"
	case len(parts) == 5 && parts[3] == "fs" && parts[4] == "file" && r.Method == http.MethodGet:
		requiredScope = "fs:read"
		route = "read_file"
	case len(parts) == 5 && parts[3] == "fs" && parts[4] == "file" && r.Method == http.MethodPut:
		requiredScope = "fs:write"
		route = "write_file"
	case len(parts) == 5 && parts[3] == "fs" && parts[4] == "file" && r.Method == http.MethodDelete:
		requiredScope = "fs:write"
		route = "delete_file"
	case len(parts) == 5 && parts[3] == "fs" && parts[4] == "events" && r.Method == http.MethodGet:
		requiredScope = "fs:read"
		route = "events"
	case len(parts) == 5 && parts[3] == "sync" && parts[4] == "status" && r.Method == http.MethodGet:
		requiredScope = "sync:read"
		route = "sync_status"
	case len(parts) == 5 && parts[3] == "sync" && parts[4] == "ingress" && r.Method == http.MethodGet:
		requiredScope = "sync:read"
		route = "sync_ingress"
	case len(parts) == 5 && parts[3] == "sync" && parts[4] == "dead-letter" && r.Method == http.MethodGet:
		requiredScope = "sync:read"
		route = "sync_dead_letter"
	case len(parts) == 6 && parts[3] == "sync" && parts[4] == "dead-letter" && r.Method == http.MethodGet:
		requiredScope = "sync:read"
		route = "sync_dead_letter_item"
	case len(parts) == 7 && parts[3] == "sync" && parts[4] == "dead-letter" && parts[6] == "ack" && r.Method == http.MethodPost:
		requiredScope = "sync:trigger"
		route = "sync_dead_letter_ack"
	case len(parts) == 7 && parts[3] == "sync" && parts[4] == "dead-letter" && parts[6] == "replay" && r.Method == http.MethodPost:
		requiredScope = "sync:trigger"
		route = "sync_dead_letter_replay"
	case len(parts) == 5 && parts[3] == "sync" && parts[4] == "refresh" && r.Method == http.MethodPost:
		requiredScope = "sync:trigger"
		route = "sync_refresh"
	case len(parts) == 4 && parts[3] == "ops" && r.Method == http.MethodGet:
		requiredScope = "ops:read"
		route = "ops_list"
	case len(parts) == 6 && parts[3] == "ops" && parts[5] == "replay" && r.Method == http.MethodPost:
		requiredScope = "ops:replay"
		route = "op_replay"
	case len(parts) == 5 && parts[3] == "ops" && r.Method == http.MethodGet:
		requiredScope = "ops:read"
		route = "op"
	default:
		writeError(w, http.StatusNotFound, "not_found", "route not found", getCorrelationID(r))
		return
	}

	claims, authErr := authorizeBearer(r.Header.Get("Authorization"), s.cfg.JWTSecret, workspaceID, requiredScope, time.Now().UTC())
	if authErr != nil {
		writeError(w, authErr.status, authErr.code, authErr.message, getCorrelationID(r))
		return
	}
	correlationID := getCorrelationID(r)
	if correlationID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing X-Correlation-Id header", "")
		return
	}
	if s.rateLimiter != nil {
		key := workspaceID + "|" + claims.AgentName
		if !s.rateLimiter.allow(key, time.Now().UTC()) {
			retryAfter := int(math.Ceil(s.rateLimiter.window.Seconds()))
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			writeError(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded", correlationID)
			return
		}
	}

	switch route {
	case "tree":
		s.handleTree(w, r, workspaceID, correlationID)
	case "read_file":
		s.handleReadFile(w, r, workspaceID, correlationID)
	case "write_file":
		s.handleWriteFile(w, r, workspaceID, correlationID)
	case "delete_file":
		s.handleDeleteFile(w, r, workspaceID, correlationID)
	case "events":
		s.handleEvents(w, r, workspaceID, correlationID)
	case "sync_status":
		s.handleSyncStatus(w, r, workspaceID, correlationID)
	case "sync_ingress":
		s.handleSyncIngress(w, r, workspaceID, correlationID)
	case "sync_dead_letter":
		s.handleSyncDeadLetter(w, r, workspaceID, correlationID)
	case "sync_dead_letter_item":
		s.handleSyncDeadLetterItem(w, r, workspaceID, parts[5], correlationID)
	case "sync_dead_letter_ack":
		s.handleSyncDeadLetterAck(w, r, workspaceID, parts[5], correlationID)
	case "sync_dead_letter_replay":
		s.handleSyncDeadLetterReplay(w, r, workspaceID, parts[5], correlationID)
	case "sync_refresh":
		s.handleSyncRefresh(w, r, workspaceID, correlationID)
	case "ops_list":
		s.handleOpsList(w, r, workspaceID, correlationID)
	case "op_replay":
		s.handleOpReplay(w, r, workspaceID, parts[4], correlationID)
	case "op":
		s.handleOp(w, r, workspaceID, parts[4], correlationID)
	default:
		writeError(w, http.StatusNotFound, "not_found", "route not found", correlationID)
	}
}

func (s *Server) handleInternalWebhookEnvelope(w http.ResponseWriter, r *http.Request) {
	correlationID := getCorrelationID(r)
	if correlationID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing X-Correlation-Id header", "")
		return
	}
	body, ok := s.readRequestBody(w, r, correlationID)
	if !ok {
		return
	}
	now := time.Now().UTC()
	if authErr := verifyInternalHMAC(
		s.cfg.InternalHMACSecret,
		r.Header.Get("X-Relay-Timestamp"),
		r.Header.Get("X-Relay-Signature"),
		body,
		now,
		s.cfg.InternalMaxSkew,
	); authErr != nil {
		writeError(w, authErr.status, authErr.code, authErr.message, correlationID)
		return
	}
	if !s.markInternalReplaySeen(r.Header.Get("X-Relay-Timestamp"), r.Header.Get("X-Relay-Signature"), now) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "internal request replay detected", correlationID)
		return
	}

	var req relayfile.WebhookEnvelopeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json body", correlationID)
		return
	}
	if req.CorrelationID == "" {
		req.CorrelationID = correlationID
	}
	queued, err := s.store.IngestEnvelope(req)
	if err != nil {
		switch {
		case errors.Is(err, relayfile.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, "bad_request", err.Error(), correlationID)
		case errors.Is(err, relayfile.ErrQueueFull):
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "queue_full", err.Error(), correlationID)
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), correlationID)
		}
		return
	}
	writeJSON(w, http.StatusAccepted, queued)
}

func (s *Server) handleAdminReplay(w http.ResponseWriter, r *http.Request, parts []string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusNotFound, "not_found", "route not found", getCorrelationID(r))
		return
	}
	if _, authErr := authorizeBearer(r.Header.Get("Authorization"), s.cfg.JWTSecret, "", "admin:replay", time.Now().UTC()); authErr != nil {
		writeError(w, authErr.status, authErr.code, authErr.message, getCorrelationID(r))
		return
	}
	correlationID := getCorrelationID(r)
	if correlationID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing X-Correlation-Id header", "")
		return
	}

	var (
		resp relayfile.QueuedResponse
		err  error
	)
	switch {
	case len(parts) == 5 && (parts[3] == "envelope" || parts[3] == "envelopes"):
		resp, err = s.store.ReplayEnvelope(parts[4], correlationID)
	case len(parts) == 5 && (parts[3] == "op" || parts[3] == "ops"):
		resp, err = s.store.ReplayOperationAny(parts[4], correlationID)
	default:
		writeError(w, http.StatusNotFound, "not_found", "route not found", correlationID)
		return
	}
	if err != nil {
		if err == relayfile.ErrNotFound {
			writeError(w, http.StatusNotFound, "not_found", err.Error(), correlationID)
			return
		}
		if err == relayfile.ErrInvalidState {
			writeError(w, http.StatusConflict, "invalid_state", err.Error(), correlationID)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), correlationID)
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *Server) handleAdminBackends(w http.ResponseWriter, r *http.Request) {
	claims, authErr := authorizeBearer(r.Header.Get("Authorization"), s.cfg.JWTSecret, "", "", time.Now().UTC())
	if authErr != nil {
		writeError(w, authErr.status, authErr.code, authErr.message, getCorrelationID(r))
		return
	}
	if !hasAnyScope(claims.Scopes, "admin:read", "admin:replay") {
		writeError(w, http.StatusForbidden, "forbidden", "missing required scope: admin:read", getCorrelationID(r))
		return
	}
	correlationID := getCorrelationID(r)
	if correlationID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing X-Correlation-Id header", "")
		return
	}
	writeJSON(w, http.StatusOK, s.store.GetBackendStatus())
}

func (s *Server) handleAdminIngress(w http.ResponseWriter, r *http.Request) {
	claims, authErr := authorizeBearer(r.Header.Get("Authorization"), s.cfg.JWTSecret, "", "", time.Now().UTC())
	if authErr != nil {
		writeError(w, authErr.status, authErr.code, authErr.message, getCorrelationID(r))
		return
	}
	if !hasAnyScope(claims.Scopes, "admin:read", "admin:replay") {
		writeError(w, http.StatusForbidden, "forbidden", "missing required scope: admin:read", getCorrelationID(r))
		return
	}
	correlationID := getCorrelationID(r)
	if correlationID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing X-Correlation-Id header", "")
		return
	}
	workspaceID := strings.TrimSpace(r.URL.Query().Get("workspaceId"))
	provider := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("provider")))
	alertProfile := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("alertProfile")))
	pendingThresholdRaw := strings.TrimSpace(r.URL.Query().Get("pendingThreshold"))
	deadLetterThresholdRaw := strings.TrimSpace(r.URL.Query().Get("deadLetterThreshold"))
	staleThresholdRaw := strings.TrimSpace(r.URL.Query().Get("staleThreshold"))
	dropRateThresholdRaw := strings.TrimSpace(r.URL.Query().Get("dropRateThreshold"))
	pendingThresholdDefault := 100
	deadLetterThresholdDefault := 1
	staleThresholdDefault := 10
	dropRateThresholdDefault := 0.05
	switch alertProfile {
	case "", "balanced":
		alertProfile = "balanced"
	case "strict":
		pendingThresholdDefault = 25
		deadLetterThresholdDefault = 1
		staleThresholdDefault = 5
		dropRateThresholdDefault = 0.02
	case "relaxed":
		pendingThresholdDefault = 500
		deadLetterThresholdDefault = 5
		staleThresholdDefault = 50
		dropRateThresholdDefault = 0.2
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "invalid alertProfile", correlationID)
		return
	}
	pendingThreshold, pendingThresholdErr := parseOptionalBoundedInt(pendingThresholdRaw, pendingThresholdDefault, 1, 1_000_000)
	if pendingThresholdErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid pendingThreshold", correlationID)
		return
	}
	deadLetterThreshold, deadLetterThresholdErr := parseOptionalBoundedInt(deadLetterThresholdRaw, deadLetterThresholdDefault, 1, 1_000_000)
	if deadLetterThresholdErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid deadLetterThreshold", correlationID)
		return
	}
	staleThreshold, staleThresholdErr := parseOptionalBoundedInt(staleThresholdRaw, staleThresholdDefault, 1, 1_000_000)
	if staleThresholdErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid staleThreshold", correlationID)
		return
	}
	dropRateThreshold, dropRateThresholdErr := parseOptionalBoundedFloat(dropRateThresholdRaw, dropRateThresholdDefault, 0, 1)
	if dropRateThresholdErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid dropRateThreshold", correlationID)
		return
	}
	effectiveAlertProfile := alertProfile
	if pendingThresholdRaw != "" || deadLetterThresholdRaw != "" || staleThresholdRaw != "" || dropRateThresholdRaw != "" {
		effectiveAlertProfile = "custom"
	}
	nonZeroOnly, nonZeroOnlyErr := parseOptionalBool(r.URL.Query().Get("nonZeroOnly"), false)
	if nonZeroOnlyErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid nonZeroOnly", correlationID)
		return
	}
	maxAlerts, maxAlertsErr := parseOptionalBoundedInt(r.URL.Query().Get("maxAlerts"), 200, 0, 10_000)
	if maxAlertsErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid maxAlerts", correlationID)
		return
	}
	workspaceCursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	workspaceLimit, workspaceLimitErr := parseOptionalBoundedInt(r.URL.Query().Get("limit"), 200, 1, 5_000)
	if workspaceLimitErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid limit", correlationID)
		return
	}
	includeWorkspaces, includeWorkspacesErr := parseOptionalBool(r.URL.Query().Get("includeWorkspaces"), true)
	if includeWorkspacesErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid includeWorkspaces", correlationID)
		return
	}
	includeAlerts, includeAlertsErr := parseOptionalBool(r.URL.Query().Get("includeAlerts"), true)
	if includeAlertsErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid includeAlerts", correlationID)
		return
	}

	statuses := map[string]relayfile.IngressStatus{}
	if workspaceID != "" {
		status, err := s.store.GetIngressStatus(workspaceID)
		if err != nil {
			if err == relayfile.ErrInvalidInput {
				writeError(w, http.StatusBadRequest, "bad_request", err.Error(), correlationID)
				return
			}
			writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), correlationID)
			return
		}
		if provider != "" {
			status = filterIngressStatusByProvider(status, provider)
		}
		statuses[workspaceID] = status
	} else {
		statuses = s.store.ListIngressStatuses()
		if provider != "" {
			for wsID, status := range statuses {
				statuses[wsID] = filterIngressStatusByProvider(status, provider)
			}
		}
	}
	if nonZeroOnly {
		for wsID, status := range statuses {
			if isIngressStatusZero(status) {
				delete(statuses, wsID)
			}
		}
	}
	workspaceIDs := make([]string, 0, len(statuses))
	for wsID := range statuses {
		workspaceIDs = append(workspaceIDs, wsID)
	}
	sort.Strings(workspaceIDs)
	totalWorkspaceCount := len(workspaceIDs)
	pagedWorkspaceIDs := []string{}
	pagedStatuses := map[string]relayfile.IngressStatus{}
	var nextCursor *string
	if includeWorkspaces {
		start := 0
		if workspaceCursor != "" {
			idx := sort.SearchStrings(workspaceIDs, workspaceCursor)
			if idx >= len(workspaceIDs) || workspaceIDs[idx] != workspaceCursor {
				writeError(w, http.StatusBadRequest, "bad_request", "invalid cursor", correlationID)
				return
			}
			start = idx + 1
		}
		end := start + workspaceLimit
		if end > len(workspaceIDs) {
			end = len(workspaceIDs)
		}
		pagedWorkspaceIDs = workspaceIDs[start:end]
		pagedStatuses = make(map[string]relayfile.IngressStatus, len(pagedWorkspaceIDs))
		for _, wsID := range pagedWorkspaceIDs {
			pagedStatuses[wsID] = statuses[wsID]
		}
		if end < len(workspaceIDs) && len(pagedWorkspaceIDs) > 0 {
			next := pagedWorkspaceIDs[len(pagedWorkspaceIDs)-1]
			nextCursor = &next
		}
	}
	pendingTotal := 0
	deadLetterTotal := 0
	acceptedTotal := uint64(0)
	droppedTotal := uint64(0)
	dedupedTotal := uint64(0)
	coalescedTotal := uint64(0)
	suppressedTotal := uint64(0)
	staleTotal := uint64(0)
	for _, status := range statuses {
		pendingTotal += status.PendingTotal
		deadLetterTotal += status.DeadLetterTotal
		acceptedTotal += status.AcceptedTotal
		droppedTotal += status.DroppedTotal
		dedupedTotal += status.DedupedTotal
		coalescedTotal += status.CoalescedTotal
		suppressedTotal += status.SuppressedTotal
		staleTotal += status.StaleTotal
	}
	type ingressAlert struct {
		WorkspaceID string  `json:"workspaceId"`
		Type        string  `json:"type"`
		Severity    string  `json:"severity"`
		Value       float64 `json:"value"`
		Threshold   float64 `json:"threshold"`
		Message     string  `json:"message"`
	}
	alerts := make([]ingressAlert, 0)
	for wsID, status := range statuses {
		if status.DeadLetterTotal >= deadLetterThreshold {
			alerts = append(alerts, ingressAlert{
				WorkspaceID: wsID,
				Type:        "dead_letters",
				Severity:    "critical",
				Value:       float64(status.DeadLetterTotal),
				Threshold:   float64(deadLetterThreshold),
				Message:     "dead-letter envelopes present",
			})
		}
		if status.PendingTotal >= pendingThreshold {
			alerts = append(alerts, ingressAlert{
				WorkspaceID: wsID,
				Type:        "pending_backlog",
				Severity:    "warning",
				Value:       float64(status.PendingTotal),
				Threshold:   float64(pendingThreshold),
				Message:     "pending webhook backlog exceeds threshold",
			})
		}
		totalAttempts := status.AcceptedTotal + status.DroppedTotal + status.DedupedTotal + status.CoalescedTotal
		if totalAttempts > 0 {
			dropRate := float64(status.DroppedTotal) / float64(totalAttempts)
			if dropRate >= dropRateThreshold {
				alerts = append(alerts, ingressAlert{
					WorkspaceID: wsID,
					Type:        "drop_rate",
					Severity:    "warning",
					Value:       dropRate,
					Threshold:   dropRateThreshold,
					Message:     "ingress drop rate exceeds threshold",
				})
			}
		}
		if status.StaleTotal >= uint64(staleThreshold) {
			alerts = append(alerts, ingressAlert{
				WorkspaceID: wsID,
				Type:        "stale_events",
				Severity:    "warning",
				Value:       float64(status.StaleTotal),
				Threshold:   float64(staleThreshold),
				Message:     "stale provider events exceed threshold",
			})
		}
	}
	severityRank := func(severity string) int {
		if severity == "critical" {
			return 0
		}
		return 1
	}
	sort.Slice(alerts, func(i, j int) bool {
		if severityRank(alerts[i].Severity) != severityRank(alerts[j].Severity) {
			return severityRank(alerts[i].Severity) < severityRank(alerts[j].Severity)
		}
		if alerts[i].WorkspaceID != alerts[j].WorkspaceID {
			return alerts[i].WorkspaceID < alerts[j].WorkspaceID
		}
		if alerts[i].Type != alerts[j].Type {
			return alerts[i].Type < alerts[j].Type
		}
		return alerts[i].Value > alerts[j].Value
	})
	alertTotals := struct {
		Total    int            `json:"total"`
		Critical int            `json:"critical"`
		Warning  int            `json:"warning"`
		ByType   map[string]int `json:"byType"`
	}{
		ByType: map[string]int{},
	}
	for _, alert := range alerts {
		alertTotals.Total++
		alertTotals.ByType[alert.Type]++
		if alert.Severity == "critical" {
			alertTotals.Critical++
		} else if alert.Severity == "warning" {
			alertTotals.Warning++
		}
	}
	alertsTruncated := false
	if len(alerts) > maxAlerts {
		alerts = alerts[:maxAlerts]
		alertsTruncated = true
	}
	if !includeAlerts {
		alerts = []ingressAlert{}
		alertsTruncated = false
	}
	writeJSON(w, http.StatusOK, struct {
		GeneratedAt    string                           `json:"generatedAt"`
		AlertProfile   string                           `json:"alertProfile"`
		EffectiveAlertProfile string                    `json:"effectiveAlertProfile"`
		WorkspaceCount int                              `json:"workspaceCount"`
		ReturnedWorkspaceCount int                      `json:"returnedWorkspaceCount"`
		WorkspaceIDs   []string                         `json:"workspaceIds"`
		NextCursor     *string                          `json:"nextCursor"`
		PendingTotal   int                              `json:"pendingTotal"`
		DeadLetterTotal int                             `json:"deadLetterTotal"`
		AcceptedTotal  uint64                           `json:"acceptedTotal"`
		DroppedTotal   uint64                           `json:"droppedTotal"`
		DedupedTotal   uint64                           `json:"dedupedTotal"`
		CoalescedTotal uint64                           `json:"coalescedTotal"`
		SuppressedTotal uint64                          `json:"suppressedTotal"`
		StaleTotal     uint64                           `json:"staleTotal"`
		Thresholds     struct {
			Pending    int     `json:"pending"`
			DeadLetter int     `json:"deadLetter"`
			Stale      int     `json:"stale"`
			DropRate   float64 `json:"dropRate"`
		} `json:"thresholds"`
		AlertTotals    struct {
			Total    int            `json:"total"`
			Critical int            `json:"critical"`
			Warning  int            `json:"warning"`
			ByType   map[string]int `json:"byType"`
		} `json:"alertTotals"`
		AlertsTruncated bool                            `json:"alertsTruncated"`
		Alerts         []ingressAlert                   `json:"alerts"`
		Workspaces     map[string]relayfile.IngressStatus `json:"workspaces"`
	}{
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		AlertProfile:    alertProfile,
		EffectiveAlertProfile: effectiveAlertProfile,
		WorkspaceCount:  totalWorkspaceCount,
		ReturnedWorkspaceCount: len(pagedStatuses),
		WorkspaceIDs:    pagedWorkspaceIDs,
		NextCursor:      nextCursor,
		PendingTotal:    pendingTotal,
		DeadLetterTotal: deadLetterTotal,
		AcceptedTotal:   acceptedTotal,
		DroppedTotal:    droppedTotal,
		DedupedTotal:    dedupedTotal,
		CoalescedTotal:  coalescedTotal,
		SuppressedTotal: suppressedTotal,
		StaleTotal:      staleTotal,
		Thresholds: struct {
			Pending    int     `json:"pending"`
			DeadLetter int     `json:"deadLetter"`
			Stale      int     `json:"stale"`
			DropRate   float64 `json:"dropRate"`
		}{
			Pending:    pendingThreshold,
			DeadLetter: deadLetterThreshold,
			Stale:      staleThreshold,
			DropRate:   dropRateThreshold,
		},
		AlertTotals:     alertTotals,
		AlertsTruncated: alertsTruncated,
		Alerts:          alerts,
		Workspaces:      pagedStatuses,
	})
}

func (s *Server) handleAdminSync(w http.ResponseWriter, r *http.Request) {
	claims, authErr := authorizeBearer(r.Header.Get("Authorization"), s.cfg.JWTSecret, "", "", time.Now().UTC())
	if authErr != nil {
		writeError(w, authErr.status, authErr.code, authErr.message, getCorrelationID(r))
		return
	}
	if !hasAnyScope(claims.Scopes, "admin:read", "admin:replay") {
		writeError(w, http.StatusForbidden, "forbidden", "missing required scope: admin:read", getCorrelationID(r))
		return
	}
	correlationID := getCorrelationID(r)
	if correlationID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing X-Correlation-Id header", "")
		return
	}

	workspaceID := strings.TrimSpace(r.URL.Query().Get("workspaceId"))
	provider := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("provider")))
	nonZeroOnly, nonZeroOnlyErr := parseOptionalBool(r.URL.Query().Get("nonZeroOnly"), false)
	if nonZeroOnlyErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid nonZeroOnly", correlationID)
		return
	}
	workspaceCursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	workspaceLimit, workspaceLimitErr := parseOptionalBoundedInt(r.URL.Query().Get("limit"), 200, 1, 5_000)
	if workspaceLimitErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid limit", correlationID)
		return
	}
	includeWorkspaces, includeWorkspacesErr := parseOptionalBool(r.URL.Query().Get("includeWorkspaces"), true)
	if includeWorkspacesErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid includeWorkspaces", correlationID)
		return
	}
	statusErrorThreshold, statusErrorThresholdErr := parseOptionalBoundedInt(r.URL.Query().Get("statusErrorThreshold"), 1, 1, 1_000_000)
	if statusErrorThresholdErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid statusErrorThreshold", correlationID)
		return
	}
	lagSecondsThreshold, lagSecondsThresholdErr := parseOptionalBoundedInt(r.URL.Query().Get("lagSecondsThreshold"), 30, 1, 1_000_000)
	if lagSecondsThresholdErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid lagSecondsThreshold", correlationID)
		return
	}
	deadLetteredEnvelopesThreshold, deadLetteredEnvelopesThresholdErr := parseOptionalBoundedInt(r.URL.Query().Get("deadLetteredEnvelopesThreshold"), 1, 1, 1_000_000)
	if deadLetteredEnvelopesThresholdErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid deadLetteredEnvelopesThreshold", correlationID)
		return
	}
	deadLetteredOpsThreshold, deadLetteredOpsThresholdErr := parseOptionalBoundedInt(r.URL.Query().Get("deadLetteredOpsThreshold"), 1, 1, 1_000_000)
	if deadLetteredOpsThresholdErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid deadLetteredOpsThreshold", correlationID)
		return
	}
	maxAlerts, maxAlertsErr := parseOptionalBoundedInt(r.URL.Query().Get("maxAlerts"), 200, 0, 10_000)
	if maxAlertsErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid maxAlerts", correlationID)
		return
	}
	includeAlerts, includeAlertsErr := parseOptionalBool(r.URL.Query().Get("includeAlerts"), true)
	if includeAlertsErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid includeAlerts", correlationID)
		return
	}

	statuses := map[string]relayfile.SyncStatus{}
	if workspaceID != "" {
		status, err := s.store.GetSyncStatus(workspaceID, provider)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), correlationID)
			return
		}
		statuses[workspaceID] = status
	} else {
		statuses = s.store.ListSyncStatuses(provider)
	}

	if nonZeroOnly {
		for wsID, status := range statuses {
			filteredProviders := make([]relayfile.SyncProviderStatus, 0, len(status.Providers))
			for _, providerStatus := range status.Providers {
				if isSyncProviderStatusZero(providerStatus) {
					continue
				}
				filteredProviders = append(filteredProviders, providerStatus)
			}
			if len(filteredProviders) == 0 {
				delete(statuses, wsID)
				continue
			}
			status.Providers = filteredProviders
			statuses[wsID] = status
		}
	}

	workspaceIDs := make([]string, 0, len(statuses))
	for wsID := range statuses {
		workspaceIDs = append(workspaceIDs, wsID)
	}
	sort.Strings(workspaceIDs)
	totalWorkspaceCount := len(workspaceIDs)
	pagedWorkspaceIDs := []string{}
	pagedStatuses := map[string]relayfile.SyncStatus{}
	var nextCursor *string
	if includeWorkspaces {
		start := 0
		if workspaceCursor != "" {
			idx := sort.SearchStrings(workspaceIDs, workspaceCursor)
			if idx >= len(workspaceIDs) || workspaceIDs[idx] != workspaceCursor {
				writeError(w, http.StatusBadRequest, "bad_request", "invalid cursor", correlationID)
				return
			}
			start = idx + 1
		}
		end := start + workspaceLimit
		if end > len(workspaceIDs) {
			end = len(workspaceIDs)
		}
		pagedWorkspaceIDs = workspaceIDs[start:end]
		pagedStatuses = make(map[string]relayfile.SyncStatus, len(pagedWorkspaceIDs))
		for _, wsID := range pagedWorkspaceIDs {
			pagedStatuses[wsID] = statuses[wsID]
		}
		if end < len(workspaceIDs) && len(pagedWorkspaceIDs) > 0 {
			next := pagedWorkspaceIDs[len(pagedWorkspaceIDs)-1]
			nextCursor = &next
		}
	}

	providerStatusCount := 0
	healthyCount := 0
	laggingCount := 0
	errorCount := 0
	pausedCount := 0
	deadLetteredEnvelopesTotal := 0
	deadLetteredOpsTotal := 0
	failureCodes := map[string]int{}
	type syncAlert struct {
		WorkspaceID string  `json:"workspaceId"`
		Provider    string  `json:"provider"`
		Type        string  `json:"type"`
		Severity    string  `json:"severity"`
		Value       float64 `json:"value"`
		Threshold   float64 `json:"threshold"`
		Message     string  `json:"message"`
	}
	alerts := make([]syncAlert, 0)
	for _, status := range statuses {
		for _, providerStatus := range status.Providers {
			providerStatusCount++
			switch providerStatus.Status {
			case "error":
				errorCount++
			case "lagging":
				laggingCount++
			case "paused":
				pausedCount++
			default:
				healthyCount++
			}
			deadLetteredEnvelopesTotal += providerStatus.DeadLetteredEnvelopes
			deadLetteredOpsTotal += providerStatus.DeadLetteredOps
			for code, count := range providerStatus.FailureCodes {
				failureCodes[code] += count
			}
		}
	}
	for wsID, status := range statuses {
		for _, providerStatus := range status.Providers {
			if providerStatus.Status == "error" && statusErrorThreshold <= 1 {
				alerts = append(alerts, syncAlert{
					WorkspaceID: wsID,
					Provider:    providerStatus.Provider,
					Type:        "status_error",
					Severity:    "critical",
					Value:       1,
					Threshold:   float64(statusErrorThreshold),
					Message:     "provider sync status is error",
				})
			}
			if providerStatus.LagSeconds >= lagSecondsThreshold {
				alerts = append(alerts, syncAlert{
					WorkspaceID: wsID,
					Provider:    providerStatus.Provider,
					Type:        "lag_seconds",
					Severity:    "warning",
					Value:       float64(providerStatus.LagSeconds),
					Threshold:   float64(lagSecondsThreshold),
					Message:     "provider lag exceeds threshold",
				})
			}
			if providerStatus.DeadLetteredEnvelopes >= deadLetteredEnvelopesThreshold {
				alerts = append(alerts, syncAlert{
					WorkspaceID: wsID,
					Provider:    providerStatus.Provider,
					Type:        "dead_lettered_envelopes",
					Severity:    "critical",
					Value:       float64(providerStatus.DeadLetteredEnvelopes),
					Threshold:   float64(deadLetteredEnvelopesThreshold),
					Message:     "dead-lettered envelopes exceed threshold",
				})
			}
			if providerStatus.DeadLetteredOps >= deadLetteredOpsThreshold {
				alerts = append(alerts, syncAlert{
					WorkspaceID: wsID,
					Provider:    providerStatus.Provider,
					Type:        "dead_lettered_ops",
					Severity:    "warning",
					Value:       float64(providerStatus.DeadLetteredOps),
					Threshold:   float64(deadLetteredOpsThreshold),
					Message:     "dead-lettered writeback operations exceed threshold",
				})
			}
		}
	}
	severityRank := func(severity string) int {
		if severity == "critical" {
			return 0
		}
		return 1
	}
	sort.Slice(alerts, func(i, j int) bool {
		if severityRank(alerts[i].Severity) != severityRank(alerts[j].Severity) {
			return severityRank(alerts[i].Severity) < severityRank(alerts[j].Severity)
		}
		if alerts[i].WorkspaceID != alerts[j].WorkspaceID {
			return alerts[i].WorkspaceID < alerts[j].WorkspaceID
		}
		if alerts[i].Provider != alerts[j].Provider {
			return alerts[i].Provider < alerts[j].Provider
		}
		if alerts[i].Type != alerts[j].Type {
			return alerts[i].Type < alerts[j].Type
		}
		return alerts[i].Value > alerts[j].Value
	})
	alertTotals := struct {
		Total    int            `json:"total"`
		Critical int            `json:"critical"`
		Warning  int            `json:"warning"`
		ByType   map[string]int `json:"byType"`
	}{
		ByType: map[string]int{},
	}
	for _, alert := range alerts {
		alertTotals.Total++
		alertTotals.ByType[alert.Type]++
		if alert.Severity == "critical" {
			alertTotals.Critical++
		} else if alert.Severity == "warning" {
			alertTotals.Warning++
		}
	}
	alertsTruncated := false
	if len(alerts) > maxAlerts {
		alerts = alerts[:maxAlerts]
		alertsTruncated = true
	}
	if !includeAlerts {
		alerts = []syncAlert{}
		alertsTruncated = false
	}

	writeJSON(w, http.StatusOK, struct {
		GeneratedAt                  string                           `json:"generatedAt"`
		WorkspaceCount               int                              `json:"workspaceCount"`
		ReturnedWorkspaceCount       int                              `json:"returnedWorkspaceCount"`
		WorkspaceIDs                 []string                         `json:"workspaceIds"`
		NextCursor                   *string                          `json:"nextCursor"`
		ProviderStatusCount          int                              `json:"providerStatusCount"`
		HealthyCount                 int                              `json:"healthyCount"`
		LaggingCount                 int                              `json:"laggingCount"`
		ErrorCount                   int                              `json:"errorCount"`
		PausedCount                  int                              `json:"pausedCount"`
		DeadLetteredEnvelopesTotal   int                              `json:"deadLetteredEnvelopesTotal"`
		DeadLetteredOpsTotal         int                              `json:"deadLetteredOpsTotal"`
		Thresholds                   struct {
			StatusError         int `json:"statusError"`
			LagSeconds          int `json:"lagSeconds"`
			DeadLetteredEnvelopes int `json:"deadLetteredEnvelopes"`
			DeadLetteredOps     int `json:"deadLetteredOps"`
		} `json:"thresholds"`
		AlertTotals                  struct {
			Total    int            `json:"total"`
			Critical int            `json:"critical"`
			Warning  int            `json:"warning"`
			ByType   map[string]int `json:"byType"`
		} `json:"alertTotals"`
		AlertsTruncated              bool                             `json:"alertsTruncated"`
		Alerts                       []syncAlert                      `json:"alerts"`
		FailureCodes                 map[string]int                   `json:"failureCodes"`
		Workspaces                   map[string]relayfile.SyncStatus  `json:"workspaces"`
	}{
		GeneratedAt:                time.Now().UTC().Format(time.RFC3339Nano),
		WorkspaceCount:             totalWorkspaceCount,
		ReturnedWorkspaceCount:     len(pagedStatuses),
		WorkspaceIDs:               pagedWorkspaceIDs,
		NextCursor:                 nextCursor,
		ProviderStatusCount:        providerStatusCount,
		HealthyCount:               healthyCount,
		LaggingCount:               laggingCount,
		ErrorCount:                 errorCount,
		PausedCount:                pausedCount,
		DeadLetteredEnvelopesTotal: deadLetteredEnvelopesTotal,
		DeadLetteredOpsTotal:       deadLetteredOpsTotal,
		Thresholds: struct {
			StatusError         int `json:"statusError"`
			LagSeconds          int `json:"lagSeconds"`
			DeadLetteredEnvelopes int `json:"deadLetteredEnvelopes"`
			DeadLetteredOps     int `json:"deadLetteredOps"`
		}{
			StatusError:         statusErrorThreshold,
			LagSeconds:          lagSecondsThreshold,
			DeadLetteredEnvelopes: deadLetteredEnvelopesThreshold,
			DeadLetteredOps:     deadLetteredOpsThreshold,
		},
		AlertTotals:                alertTotals,
		AlertsTruncated:            alertsTruncated,
		Alerts:                     alerts,
		FailureCodes:               failureCodes,
		Workspaces:                 pagedStatuses,
	})
}

func filterIngressStatusByProvider(status relayfile.IngressStatus, provider string) relayfile.IngressStatus {
	providerStatus, ok := status.IngressByProvider[provider]
	if !ok {
		providerStatus = relayfile.IngressProviderStatus{}
	}
	filteredDeadLetters := map[string]int{}
	if deadCount, ok := status.DeadLetterByProvider[provider]; ok && deadCount > 0 {
		filteredDeadLetters[provider] = deadCount
	}
	status.QueueDepth = providerStatus.PendingTotal
	if status.QueueCapacity > 0 {
		status.QueueUtilization = float64(providerStatus.PendingTotal) / float64(status.QueueCapacity)
	} else {
		status.QueueUtilization = 0
	}
	status.PendingTotal = providerStatus.PendingTotal
	status.OldestPendingAgeSeconds = providerStatus.OldestPendingAgeSeconds
	status.DeadLetterTotal = filteredDeadLetters[provider]
	status.DeadLetterByProvider = filteredDeadLetters
	status.AcceptedTotal = providerStatus.AcceptedTotal
	status.DroppedTotal = providerStatus.DroppedTotal
	status.DedupedTotal = providerStatus.DedupedTotal
	status.CoalescedTotal = providerStatus.CoalescedTotal
	status.DedupeRate = providerStatus.DedupeRate
	status.CoalesceRate = providerStatus.CoalesceRate
	status.SuppressedTotal = providerStatus.SuppressedTotal
	status.StaleTotal = providerStatus.StaleTotal
	status.IngressByProvider = map[string]relayfile.IngressProviderStatus{
		provider: providerStatus,
	}
	return status
}

func isIngressStatusZero(status relayfile.IngressStatus) bool {
	return status.PendingTotal == 0 &&
		status.DeadLetterTotal == 0 &&
		status.AcceptedTotal == 0 &&
		status.DroppedTotal == 0 &&
		status.DedupedTotal == 0 &&
		status.CoalescedTotal == 0 &&
		status.SuppressedTotal == 0 &&
		status.StaleTotal == 0
}

func isSyncProviderStatusZero(status relayfile.SyncProviderStatus) bool {
	return status.Status == "healthy" &&
		status.LagSeconds == 0 &&
		status.LastError == nil &&
		len(status.FailureCodes) == 0 &&
		status.DeadLetteredEnvelopes == 0 &&
		status.DeadLetteredOps == 0
}

func hasAnyScope(scopes map[string]struct{}, required ...string) bool {
	if len(required) == 0 {
		return true
	}
	for _, scope := range required {
		if _, ok := scopes[scope]; ok {
			return true
		}
	}
	return false
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request, workspaceID, correlationID string) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	depth := parseBoundedInt(r.URL.Query().Get("depth"), 2, 1, 10)
	resp, err := s.store.ListTree(workspaceID, path, depth, r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error(), correlationID)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request, workspaceID, correlationID string) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing path query", correlationID)
		return
	}
	file, err := s.store.ReadFile(workspaceID, path)
	if err != nil {
		if err == relayfile.ErrNotFound {
			writeError(w, http.StatusNotFound, "not_found", err.Error(), correlationID)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), correlationID)
		return
	}
	w.Header().Set("ETag", file.Revision)
	writeJSON(w, http.StatusOK, file)
}

func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request, workspaceID, correlationID string) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing path query", correlationID)
		return
	}
	ifMatch := r.Header.Get("If-Match")
	ifMatch = normalizeIfMatchHeader(ifMatch)
	if ifMatch == "" {
		writeError(w, http.StatusPreconditionFailed, "precondition_failed", "missing If-Match header", correlationID)
		return
	}

	var body struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	}
	if !s.decodeJSONBody(w, r, correlationID, &body) {
		return
	}

	result, err := s.store.WriteFile(relayfile.WriteRequest{
		WorkspaceID:   workspaceID,
		Path:          path,
		IfMatch:       ifMatch,
		ContentType:   body.ContentType,
		Content:       body.Content,
		CorrelationID: correlationID,
	})
	if err != nil {
		var conflict *relayfile.ConflictError
		if errors.As(err, &conflict) {
			payload := map[string]any{
				"code":             "revision_conflict",
				"message":          err.Error(),
				"correlationId":    correlationID,
				"expectedRevision": conflict.ExpectedRevision,
				"currentRevision":  conflict.CurrentRevision,
			}
			if conflict.CurrentContentPreview != "" {
				payload["currentContentPreview"] = conflict.CurrentContentPreview
			}
			writeJSON(w, http.StatusConflict, payload)
			return
		}
		switch err {
		case relayfile.ErrMissingPrecondition:
			writeError(w, http.StatusPreconditionFailed, "precondition_failed", err.Error(), correlationID)
		case relayfile.ErrNotFound:
			writeError(w, http.StatusNotFound, "not_found", err.Error(), correlationID)
		case relayfile.ErrInvalidInput:
			writeError(w, http.StatusBadRequest, "bad_request", err.Error(), correlationID)
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), correlationID)
		}
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request, workspaceID, correlationID string) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing path query", correlationID)
		return
	}
	ifMatch := r.Header.Get("If-Match")
	ifMatch = normalizeIfMatchHeader(ifMatch)
	if ifMatch == "" {
		writeError(w, http.StatusPreconditionFailed, "precondition_failed", "missing If-Match header", correlationID)
		return
	}
	result, err := s.store.DeleteFile(relayfile.DeleteRequest{
		WorkspaceID:   workspaceID,
		Path:          path,
		IfMatch:       ifMatch,
		CorrelationID: correlationID,
	})
	if err != nil {
		var conflict *relayfile.ConflictError
		if errors.As(err, &conflict) {
			payload := map[string]any{
				"code":             "revision_conflict",
				"message":          err.Error(),
				"correlationId":    correlationID,
				"expectedRevision": conflict.ExpectedRevision,
				"currentRevision":  conflict.CurrentRevision,
			}
			if conflict.CurrentContentPreview != "" {
				payload["currentContentPreview"] = conflict.CurrentContentPreview
			}
			writeJSON(w, http.StatusConflict, payload)
			return
		}
		switch err {
		case relayfile.ErrNotFound:
			writeError(w, http.StatusNotFound, "not_found", err.Error(), correlationID)
		case relayfile.ErrMissingPrecondition:
			writeError(w, http.StatusPreconditionFailed, "precondition_failed", err.Error(), correlationID)
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), correlationID)
		}
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, workspaceID, correlationID string) {
	limit := parseBoundedInt(r.URL.Query().Get("limit"), 200, 1, 1000)
	feed, err := s.store.GetEvents(workspaceID, r.URL.Query().Get("provider"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), correlationID)
		return
	}
	writeJSON(w, http.StatusOK, feed)
}

func (s *Server) handleOp(w http.ResponseWriter, _ *http.Request, workspaceID, opID, correlationID string) {
	op, err := s.store.GetOperation(workspaceID, opID)
	if err != nil {
		if err == relayfile.ErrNotFound {
			writeError(w, http.StatusNotFound, "not_found", err.Error(), correlationID)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), correlationID)
		return
	}
	writeJSON(w, http.StatusOK, op)
}

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request, workspaceID, correlationID string) {
	status, err := s.store.GetSyncStatus(workspaceID, r.URL.Query().Get("provider"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), correlationID)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleSyncIngress(w http.ResponseWriter, r *http.Request, workspaceID, correlationID string) {
	status, err := s.store.GetIngressStatusForProvider(workspaceID, r.URL.Query().Get("provider"))
	if err != nil {
		if err == relayfile.ErrInvalidInput {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error(), correlationID)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), correlationID)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleSyncDeadLetter(w http.ResponseWriter, r *http.Request, workspaceID, correlationID string) {
	limit := parseBoundedInt(r.URL.Query().Get("limit"), 100, 1, 1000)
	feed, err := s.store.ListDeadLetters(
		workspaceID,
		r.URL.Query().Get("provider"),
		r.URL.Query().Get("cursor"),
		limit,
	)
	if err != nil {
		if err == relayfile.ErrInvalidInput {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error(), correlationID)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), correlationID)
		return
	}
	writeJSON(w, http.StatusOK, feed)
}

func (s *Server) handleSyncDeadLetterItem(w http.ResponseWriter, r *http.Request, workspaceID, envelopeID, correlationID string) {
	item, err := s.store.GetDeadLetter(workspaceID, envelopeID)
	if err != nil {
		if err == relayfile.ErrNotFound {
			writeError(w, http.StatusNotFound, "not_found", err.Error(), correlationID)
			return
		}
		if err == relayfile.ErrInvalidInput {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error(), correlationID)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), correlationID)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleSyncDeadLetterAck(w http.ResponseWriter, r *http.Request, workspaceID, envelopeID, correlationID string) {
	resp, err := s.store.AcknowledgeDeadLetter(workspaceID, envelopeID, correlationID)
	if err != nil {
		if err == relayfile.ErrNotFound {
			writeError(w, http.StatusNotFound, "not_found", err.Error(), correlationID)
			return
		}
		if err == relayfile.ErrInvalidInput {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error(), correlationID)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), correlationID)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSyncDeadLetterReplay(w http.ResponseWriter, r *http.Request, workspaceID, envelopeID, correlationID string) {
	resp, err := s.store.ReplayEnvelopeForWorkspace(workspaceID, envelopeID, correlationID)
	if err != nil {
		if err == relayfile.ErrNotFound {
			writeError(w, http.StatusNotFound, "not_found", err.Error(), correlationID)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), correlationID)
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *Server) handleSyncRefresh(w http.ResponseWriter, r *http.Request, workspaceID, correlationID string) {
	var body struct {
		Provider string `json:"provider"`
		Reason   string `json:"reason"`
	}
	if !s.decodeJSONBody(w, r, correlationID, &body) {
		return
	}
	resp, err := s.store.TriggerSyncRefresh(workspaceID, body.Provider, body.Reason, correlationID)
	if err != nil {
		if err == relayfile.ErrInvalidInput {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error(), correlationID)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), correlationID)
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *Server) handleOpsList(w http.ResponseWriter, r *http.Request, workspaceID, correlationID string) {
	limit := parseBoundedInt(r.URL.Query().Get("limit"), 100, 1, 1000)
	feed, err := s.store.ListOperations(
		workspaceID,
		r.URL.Query().Get("status"),
		r.URL.Query().Get("action"),
		r.URL.Query().Get("provider"),
		r.URL.Query().Get("cursor"),
		limit,
	)
	if err != nil {
		if err == relayfile.ErrInvalidInput {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error(), correlationID)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), correlationID)
		return
	}
	writeJSON(w, http.StatusOK, feed)
}

func (s *Server) handleOpReplay(w http.ResponseWriter, r *http.Request, workspaceID, opID, correlationID string) {
	resp, err := s.store.ReplayOperation(workspaceID, opID, correlationID)
	if err != nil {
		if err == relayfile.ErrNotFound {
			writeError(w, http.StatusNotFound, "not_found", err.Error(), correlationID)
			return
		}
		if err == relayfile.ErrInvalidState {
			writeError(w, http.StatusConflict, "invalid_state", err.Error(), correlationID)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), correlationID)
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func getCorrelationID(r *http.Request) string {
	return r.Header.Get("X-Correlation-Id")
}

func (s *Server) readRequestBody(w http.ResponseWriter, r *http.Request, correlationID string) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds configured limit", correlationID)
			return nil, false
		}
		writeError(w, http.StatusBadRequest, "bad_request", "failed to read request body", correlationID)
		return nil, false
	}
	return body, true
}

func (s *Server) decodeJSONBody(w http.ResponseWriter, r *http.Request, correlationID string, dst any) bool {
	body, ok := s.readRequestBody(w, r, correlationID)
	if !ok {
		return false
	}
	if err := json.Unmarshal(body, dst); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json body", correlationID)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, code, message, correlationID string) {
	writeJSON(w, status, map[string]any{
		"code":          code,
		"message":       message,
		"correlationId": correlationID,
	})
}

func (r *rateLimiter) allow(key string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.entries[key]
	if !ok || now.After(entry.resetAt) {
		r.entries[key] = rateEntry{
			count:   1,
			resetAt: now.Add(r.window),
		}
		return true
	}
	if entry.count >= r.max {
		return false
	}
	entry.count++
	r.entries[key] = entry
	return true
}

func (s *Server) markInternalReplaySeen(timestamp, signature string, now time.Time) bool {
	key := strings.TrimSpace(strings.ToLower(timestamp)) + "|" + strings.TrimSpace(strings.ToLower(signature))
	if key == "|" {
		return false
	}
	window := s.cfg.InternalMaxSkew
	if window <= 0 {
		window = 5 * time.Minute
	}
	s.internalReplayMu.Lock()
	defer s.internalReplayMu.Unlock()
	for replayKey, expiresAt := range s.internalReplaySeen {
		if !now.Before(expiresAt) {
			delete(s.internalReplaySeen, replayKey)
		}
	}
	if expiresAt, exists := s.internalReplaySeen[key]; exists && now.Before(expiresAt) {
		return false
	}
	s.internalReplaySeen[key] = now.Add(window)
	return true
}

func normalizeIfMatchHeader(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "W/") || strings.HasPrefix(value, "w/") {
		value = strings.TrimSpace(value[2:])
	}
	if len(value) >= 2 && strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	return value
}

func parseBoundedInt(raw string, fallback, min, max int) int {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	if parsed < min {
		return fallback
	}
	if parsed > max {
		return max
	}
	return parsed
}

func parseBoundedFloat(raw string, fallback, min, max float64) float64 {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return fallback
	}
	if parsed < min {
		return min
	}
	if parsed > max {
		return max
	}
	return parsed
}

func parseBool(raw string, fallback bool) bool {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return parsed
}

func parseOptionalBoundedInt(raw string, fallback, min, max int) (int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, err
	}
	if parsed < min || parsed > max {
		return 0, fmt.Errorf("out of range")
	}
	return parsed, nil
}

func parseOptionalBoundedFloat(raw string, fallback, min, max float64) (float64, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, err
	}
	if parsed < min || parsed > max {
		return 0, fmt.Errorf("out of range")
	}
	return parsed, nil
}

func parseOptionalBool(raw string, fallback bool) (bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(trimmed)
	if err != nil {
		return false, err
	}
	return parsed, nil
}
