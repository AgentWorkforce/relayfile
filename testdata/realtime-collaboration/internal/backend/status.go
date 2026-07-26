package backend

import (
	"encoding/json"
	"net/http"

	"example.com/relayfile-collab/internal/model"
)

func StatusHandler(service, version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(model.NewStatus(service, version))
	})
}
