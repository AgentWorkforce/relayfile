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
	Type      string `json:"type"`
	Path      string `json:"path,omitempty"`
	Revision  string `json:"revision,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

type websocketClientMessage struct {
	Type string `json:"type"`
}

func (s *Server) handleFileEventsWebSocket(w http.ResponseWriter, r *http.Request, workspaceID string) {
	claims, authErr := authorizeBearer("Bearer "+strings.TrimSpace(r.URL.Query().Get("token")), s.cfg.JWTSecret, workspaceID, "fs:read", "", time.Now().UTC())
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

	// Subscribe FIRST, then catch up, so no events are missed in between.
	subscriptionCh := make(chan relayfile.Event, 256)
	unsubscribe := s.store.Subscribe(workspaceID, subscriptionCh)
	defer unsubscribe()

	catchUp, err := s.store.GetRecentEvents(workspaceID, 100)
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
		Type:      event.Type,
		Path:      event.Path,
		Revision:  event.Revision,
		Timestamp: event.Timestamp,
	})
}
