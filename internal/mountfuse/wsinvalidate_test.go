package mountfuse

import (
	"net/url"
	"strings"
	"testing"
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
