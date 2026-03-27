package mountfuse

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

const (
	// maxWSReadLimit caps the maximum WebSocket message size to prevent OOM
	// from oversized frames.
	maxWSReadLimit = 1 << 20 // 1 MiB

	// maxConsecutiveWSFailures is the maximum number of consecutive connection
	// failures before giving up. Resets on a successful connection.
	maxConsecutiveWSFailures = 10
)

type wsEvent struct {
	Type string `json:"type"`
	Path string `json:"path"`
}

// WSInvalidator connects to the relayfile WebSocket event stream and
// invalidates cached entries in fsState when files change remotely.
type WSInvalidator struct {
	baseURL     string
	token       string
	workspaceID string
	state       *fsState
	logger      *log.Logger
}

// NewWSInvalidator creates a WSInvalidator that will connect to the event
// stream at baseURL for the given workspace, invalidating cached state.
func NewWSInvalidator(baseURL, token, workspaceID string, state *fsState, logger *log.Logger) *WSInvalidator {
	if logger == nil {
		logger = log.Default()
	}
	return &WSInvalidator{
		baseURL:     baseURL,
		token:       token,
		workspaceID: workspaceID,
		state:       state,
		logger:      logger,
	}
}

// Run connects to the WebSocket event stream and processes events until ctx
// is cancelled. On disconnection it reconnects with exponential backoff
// starting at 1 second and doubling up to 30 seconds. Stops reconnecting on
// authentication failures (401/403) or after maxConsecutiveWSFailures.
func (w *WSInvalidator) Run(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	consecutiveFailures := 0

	for {
		connectedAt := time.Now()
		err := w.listenOnce(ctx)
		if ctx.Err() != nil {
			return
		}

		// Stop reconnecting on auth failures — retrying won't help.
		if isAuthError(err) {
			w.logger.Printf("mountfuse: ws auth failure, not reconnecting: %v", err)
			return
		}

		// Reset backoff and failure count if the connection was stable.
		if time.Since(connectedAt) > maxBackoff {
			backoff = time.Second
			consecutiveFailures = 0
		} else {
			consecutiveFailures++
		}

		if consecutiveFailures >= maxConsecutiveWSFailures {
			w.logger.Printf("mountfuse: ws giving up after %d consecutive failures", consecutiveFailures)
			return
		}

		w.logger.Printf("mountfuse: ws disconnected: %v; reconnecting in %v", err, backoff)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// isAuthError returns true if the error indicates an authentication/authorization
// failure (HTTP 401 or 403), where reconnecting would not help.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "status = 401") ||
		strings.Contains(msg, "status = 403") ||
		strings.Contains(msg, "StatusCode: 401") ||
		strings.Contains(msg, "StatusCode: 403")
}

func (w *WSInvalidator) listenOnce(ctx context.Context) error {
	wsURL, err := w.websocketURL()
	if err != nil {
		return fmt.Errorf("building ws url: %w", err)
	}

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + w.token},
		},
	})
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(maxWSReadLimit)

	w.logger.Printf("mountfuse: ws connected for workspace %s", w.workspaceID)

	for {
		var raw json.RawMessage
		if readErr := wsjson.Read(ctx, conn, &raw); readErr != nil {
			return fmt.Errorf("ws read: %w", readErr)
		}

		var event wsEvent
		if jsonErr := json.Unmarshal(raw, &event); jsonErr != nil {
			w.logger.Printf("mountfuse: ws invalid event payload: %v", jsonErr)
			continue
		}

		w.handleEvent(event)
	}
}

func (w *WSInvalidator) handleEvent(event wsEvent) {
	eventType := strings.ToLower(strings.TrimSpace(event.Type))
	if event.Path == "" {
		return
	}

	switch eventType {
	case "file.created", "file.updated", "file.deleted":
		w.state.invalidate(event.Path)
		w.logger.Printf("mountfuse: ws invalidated %s (%s)", event.Path, eventType)
	}
}

func (w *WSInvalidator) websocketURL() (string, error) {
	base, err := url.Parse(w.baseURL)
	if err != nil {
		return "", err
	}
	switch base.Scheme {
	case "http":
		base.Scheme = "ws"
	case "https":
		base.Scheme = "wss"
	case "ws", "wss":
		// already a WebSocket scheme; use as-is
	default:
		return "", fmt.Errorf("unsupported base URL scheme %q", base.Scheme)
	}
	base.Path = fmt.Sprintf("/v1/workspaces/%s/fs/ws", url.PathEscape(w.workspaceID))
	q := base.Query()
	q.Set("token", w.token)
	base.RawQuery = q.Encode()
	return base.String(), nil
}
