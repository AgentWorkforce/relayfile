package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentworkforce/relayfile/internal/relayfile"
)

// TestHealthReflectsStateBackendLoad covers #569: /health must not report a
// server as ready when its state backend failed to load.
func TestHealthReflectsStateBackendLoad(t *testing.T) {
	t.Parallel()

	corruptStateFile := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(corruptStateFile, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt state file: %v", err)
	}

	tests := []struct {
		name       string
		opts       relayfile.StoreOptions
		wantStatus int
		wantBody   map[string]any
	}{
		{
			name:       "healthy store",
			opts:       relayfile.StoreOptions{},
			wantStatus: http.StatusOK,
			wantBody:   map[string]any{"status": "ok", "features": []any{"symlink-v1"}},
		},
		{
			name:       "state backend load failure",
			opts:       relayfile.StoreOptions{StateFile: corruptStateFile},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   map[string]any{"status": "degraded", "error": "state backend failed to load"},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := relayfile.NewStoreWithOptions(tt.opts)
			t.Cleanup(store.Close)
			server := NewServer(store)

			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			var got map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tt.wantBody)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("body = %s, want %s", gotJSON, wantJSON)
			}
			if loadErr := store.LoadErr(); loadErr != nil && strings.Contains(rec.Body.String(), loadErr.Error()) {
				t.Fatalf("health body leaks load error detail: %s", rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), corruptStateFile) {
				t.Fatalf("health body leaks state backend location: %s", rec.Body.String())
			}
		})
	}
}
