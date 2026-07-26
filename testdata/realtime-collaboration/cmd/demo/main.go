package main

import (
	"log"
	"net/http"

	"example.com/relayfile-collab/internal/backend"
)

func main() {
	mux := http.NewServeMux()
	mux.Handle("/api/status", backend.StatusHandler("relayfile-collab", "v1"))
	log.Fatal(http.ListenAndServe(":8090", mux))
}
