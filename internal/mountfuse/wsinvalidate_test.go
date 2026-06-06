package mountfuse

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"nhooyr.io/websocket"
)

func TestWebsocketURLOmitsTokenQuery(t *testing.T) {
	w := NewWSInvalidator("https://api.example.com", "secret-token", "ws_demo", nil, nil)

	raw, err := w.websocketURL()
	if err != nil {
		t.Fatalf("websocketURL failed: %v", err)
	}
	if strings.Contains(raw, "secret-token") {
		t.Fatalf("bearer token leaked into ws url: %q", raw)
	}

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url failed: %v", err)
	}
	if u.Scheme != "wss" {
		t.Fatalf("expected wss scheme, got %q", u.Scheme)
	}
	if u.Path != "/v1/workspaces/ws_demo/fs/ws" {
		t.Fatalf("unexpected ws path: %q", u.Path)
	}
	if got := u.Query().Get("token"); got != "" {
		t.Fatalf("expected no token query param, got %q (raw=%q)", got, u.RawQuery)
	}
}

func TestWSInvalidatorUsesLatestTokenFuncValue(t *testing.T) {
	token := "old-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer new-token" {
			t.Fatalf("expected latest token in Authorization header, got %q", got)
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Fatalf("accept websocket: %v", err)
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	defer server.Close()

	invalidator := NewWSInvalidatorWithTokenFunc(server.URL, "fallback-token", func() string {
		return token
	}, "ws_demo", nil, nil)
	token = "new-token"

	err := invalidator.listenOnce(context.Background())
	if err == nil || !errors.Is(err, websocket.CloseError{Code: websocket.StatusNormalClosure}) {
		t.Fatalf("expected normal websocket close after successful dial, got %v", err)
	}
}
