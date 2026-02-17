package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/agentworkforce/relayfile/internal/httpapi"
	"github.com/agentworkforce/relayfile/internal/relayfile"
)

func main() {
	addr := os.Getenv("RELAYFILE_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		StateFile:            os.Getenv("RELAYFILE_STATE_FILE"),
		MaxWritebackAttempts: intEnv("RELAYFILE_MAX_WRITEBACK_ATTEMPTS", 0),
		WritebackDelay:       durationEnv("RELAYFILE_WRITEBACK_RETRY_DELAY", 0),
		MaxEnvelopeAttempts:  intEnv("RELAYFILE_MAX_ENVELOPE_ATTEMPTS", 0),
		EnvelopeRetryDelay:   durationEnv("RELAYFILE_ENVELOPE_RETRY_DELAY", 0),
		SuppressionWindow:    durationEnv("RELAYFILE_SUPPRESSION_WINDOW", 0),
		CoalesceWindow:       durationEnv("RELAYFILE_COALESCE_WINDOW", 0),
		MaxStoredEnvelopes:   intEnv("RELAYFILE_MAX_STORED_ENVELOPES", 0),
		EnvelopeQueueSize:    intEnv("RELAYFILE_ENVELOPE_QUEUE_SIZE", 0),
	})
	server := httpapi.NewServerWithConfig(store, httpapi.ServerConfig{
		JWTSecret:          os.Getenv("RELAYFILE_JWT_SECRET"),
		InternalHMACSecret: os.Getenv("RELAYFILE_INTERNAL_HMAC_SECRET"),
		InternalMaxSkew:    durationEnv("RELAYFILE_INTERNAL_MAX_SKEW", 5*time.Minute),
		RateLimitMax:       intEnv("RELAYFILE_RATE_LIMIT_MAX", 0),
		RateLimitWindow:    durationEnv("RELAYFILE_RATE_LIMIT_WINDOW", time.Minute),
	})

	log.Printf("relayfile listening on %s", addr)
	if err := http.ListenAndServe(addr, server); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

func intEnv(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("invalid %s=%q, using fallback %d", name, raw, fallback)
		return fallback
	}
	return value
}

func durationEnv(name string, fallback time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("invalid %s=%q, using fallback %s", name, raw, fallback.String())
		return fallback
	}
	return value
}
