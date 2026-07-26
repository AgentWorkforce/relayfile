package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/relayfile-collab/internal/model"
)

func TestStatusHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	StatusHandler("relayfile-collab", "v1").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d", rec.Code)
	}
	var got model.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Service != "relayfile-collab" || got.Version != "v1" {
		t.Fatalf("status = %+v", got)
	}
}
