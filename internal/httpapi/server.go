package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
}

type Server struct {
	store *relayfile.Store
	cfg   ServerConfig
	rateLimiter *rateLimiter
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
	var limiter *rateLimiter
	if cfg.RateLimitMax > 0 {
		limiter = &rateLimiter{
			window:  cfg.RateLimitWindow,
			max:     cfg.RateLimitMax,
			entries: map[string]rateEntry{},
		}
	}
	return &Server{
		store:       store,
		cfg:         cfg,
		rateLimiter: limiter,
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
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "failed to read request body", correlationID)
		return
	}
	if authErr := verifyInternalHMAC(
		s.cfg.InternalHMACSecret,
		r.Header.Get("X-Relay-Timestamp"),
		r.Header.Get("X-Relay-Signature"),
		body,
		time.Now().UTC(),
		s.cfg.InternalMaxSkew,
	); authErr != nil {
		writeError(w, authErr.status, authErr.code, authErr.message, correlationID)
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
	case len(parts) == 5 && parts[3] == "envelopes":
		resp, err = s.store.ReplayEnvelope(parts[4], correlationID)
	case len(parts) == 5 && parts[3] == "ops":
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

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request, workspaceID, correlationID string) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	depth := 2
	if v := r.URL.Query().Get("depth"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			depth = parsed
		}
	}
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
	if ifMatch == "" {
		writeError(w, http.StatusPreconditionFailed, "precondition_failed", "missing If-Match header", correlationID)
		return
	}

	var body struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json body", correlationID)
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
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			limit = parsed
		}
	}
	feed, err := s.store.GetEvents(workspaceID, r.URL.Query().Get("cursor"), limit)
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
	status, err := s.store.GetIngressStatus(workspaceID)
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
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			limit = parsed
		}
	}
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json body", correlationID)
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
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			limit = parsed
		}
	}
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
