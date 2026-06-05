package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/agentworkforce/relayfile/internal/relayfile"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

type fileEventMessage struct {
	EventID       string `json:"eventId,omitempty"`
	Type          string `json:"type"`
	Path          string `json:"path,omitempty"`
	Revision      string `json:"revision,omitempty"`
	ContentHash   string `json:"contentHash,omitempty"`
	Origin        string `json:"origin,omitempty"`
	Provider      string `json:"provider,omitempty"`
	CorrelationID string `json:"correlationId,omitempty"`
	Timestamp     string `json:"timestamp,omitempty"`
}

type websocketClientMessage struct {
	Type string `json:"type"`
}

type websocketSubscriptionOptions struct {
	From   string
	Cursor string
	Paths  []string
}

func (s *Server) handleFileEventsWebSocket(w http.ResponseWriter, r *http.Request, workspaceID string) {
	claims, authErr := authorizeBearer("Bearer "+strings.TrimSpace(r.URL.Query().Get("token")), s.bearerVerifier, workspaceID, "fs:read", "", time.Now().UTC())
	if authErr != nil {
		writeError(w, authErr.status, authErr.code, authErr.message, "")
		return
	}
	if s.rateLimiter != nil {
		key := workspaceID + "|" + claims.AgentName
		if !s.rateLimiter.allow(key, time.Now().UTC()) {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded", "")
			return
		}
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := r.Context()
	options := parseWebSocketSubscriptionOptions(r)

	// Subscribe FIRST, then catch up, so no events are missed in between.
	subscriptionCh := make(chan relayfile.Event, 256)
	unsubscribe := s.store.Subscribe(workspaceID, subscriptionCh)
	defer unsubscribe()

	catchUp, err := s.webSocketCatchUpEvents(workspaceID, options)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "failed to load catch-up events")
		return
	}
	// Track catch-up event IDs to deduplicate against live events
	catchUpIDs := make(map[string]struct{}, len(catchUp))
	for _, event := range catchUp {
		if event.EventID != "" {
			catchUpIDs[event.EventID] = struct{}{}
		}
		if !webSocketEventMatchesPaths(event, options.Paths) {
			continue
		}
		if err := s.writeWebSocketEvent(ctx, conn, event); err != nil {
			return
		}
	}

	controlCh := make(chan fileEventMessage, 8)
	readErrCh := make(chan error, 1)
	go s.readWebSocketMessages(ctx, conn, controlCh, readErrCh)

	for {
		select {
		case <-ctx.Done():
			_ = conn.Close(websocket.StatusGoingAway, "request context canceled")
			return
		case err := <-readErrCh:
			if err != nil && websocket.CloseStatus(err) == -1 {
				_ = conn.Close(websocket.StatusInternalError, "websocket read failed")
			}
			return
		case msg := <-controlCh:
			if err := wsjson.Write(ctx, conn, msg); err != nil {
				return
			}
		case event := <-subscriptionCh:
			if !webSocketEventMatchesPaths(event, options.Paths) {
				continue
			}
			// Skip events already sent during catch-up to avoid duplicates
			if event.EventID != "" {
				if _, dup := catchUpIDs[event.EventID]; dup {
					delete(catchUpIDs, event.EventID)
					continue
				}
			}
			if err := s.writeWebSocketEvent(ctx, conn, event); err != nil {
				return
			}
		}
	}
}

func parseWebSocketSubscriptionOptions(r *http.Request) websocketSubscriptionOptions {
	query := r.URL.Query()
	return websocketSubscriptionOptions{
		From:   strings.ToLower(strings.TrimSpace(query.Get("from"))),
		Cursor: strings.TrimSpace(query.Get("cursor")),
		Paths:  normalizeWebSocketPathFilters(query["path"]),
	}
}

func (s *Server) webSocketCatchUpEvents(workspaceID string, options websocketSubscriptionOptions) ([]relayfile.Event, error) {
	if len(options.Paths) > 0 {
		matchesPath := func(event relayfile.Event) bool {
			return webSocketEventMatchesPaths(event, options.Paths)
		}
		if options.Cursor != "" {
			return s.store.GetEventsAfterCursorMatching(workspaceID, options.Cursor, 100, matchesPath)
		}
		if options.From == "now" {
			return []relayfile.Event{}, nil
		}
		return s.store.GetRecentEventsMatching(workspaceID, 100, matchesPath)
	}
	if options.Cursor != "" {
		return s.store.GetEventsAfterCursor(workspaceID, options.Cursor, 100)
	}
	if options.From == "now" {
		return []relayfile.Event{}, nil
	}
	return s.store.GetRecentEvents(workspaceID, 100)
}

func normalizeWebSocketPathFilters(values []string) []string {
	seen := map[string]struct{}{}
	paths := make([]string, 0, len(values))
	for _, value := range values {
		path := strings.TrimSpace(value)
		if path == "" {
			continue
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths
}

func webSocketEventMatchesPaths(event relayfile.Event, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, filter := range filters {
		if webSocketPathMatches(filter, event.Path) {
			return true
		}
	}
	return false
}

func webSocketPathMatches(pattern, eventPath string) bool {
	pattern = strings.TrimSpace(pattern)
	eventPath = strings.TrimSpace(eventPath)
	if pattern == "" || eventPath == "" {
		return false
	}
	if pattern == eventPath {
		return true
	}
	patternSegments := strings.Split(strings.Trim(pattern, "/"), "/")
	pathSegments := strings.Split(strings.Trim(eventPath, "/"), "/")
	for index, segment := range patternSegments {
		if segment == "**" && index == len(patternSegments)-1 {
			return true
		}
		if index >= len(pathSegments) {
			return false
		}
		if segment == "*" {
			continue
		}
		if segment != pathSegments[index] {
			return false
		}
	}
	return len(patternSegments) == len(pathSegments)
}

func (s *Server) readWebSocketMessages(ctx context.Context, conn *websocket.Conn, controlCh chan<- fileEventMessage, readErrCh chan<- error) {
	defer close(readErrCh)
	for {
		var msg websocketClientMessage
		err := wsjson.Read(ctx, conn, &msg)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
				websocket.CloseStatus(err) == websocket.StatusGoingAway {
				readErrCh <- nil
				return
			}
			if errors.Is(err, context.Canceled) {
				readErrCh <- nil
				return
			}
			readErrCh <- err
			return
		}
		if strings.EqualFold(strings.TrimSpace(msg.Type), "ping") {
			select {
			case controlCh <- fileEventMessage{Type: "pong", Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}:
			case <-ctx.Done():
				readErrCh <- nil
				return
			}
		}
	}
}

func (s *Server) writeWebSocketEvent(ctx context.Context, conn *websocket.Conn, event relayfile.Event) error {
	return wsjson.Write(ctx, conn, fileEventMessage{
		EventID:       event.EventID,
		Type:          event.Type,
		Path:          event.Path,
		Revision:      event.Revision,
		ContentHash:   event.ContentHash,
		Origin:        event.Origin,
		Provider:      event.Provider,
		CorrelationID: event.CorrelationID,
		Timestamp:     event.Timestamp,
	})
}
