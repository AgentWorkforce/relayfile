package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentworkforce/relayfile/internal/httpapi"
	"github.com/agentworkforce/relayfile/internal/relayfile"
)

func main() {
	addr := os.Getenv("RELAYFILE_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	stateBackend, envelopeQueue, writebackQueue, err := buildStorageBackendsFromEnv()
	if err != nil {
		log.Fatalf("failed to initialize storage backends: %v", err)
	}

	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		StateBackend:           stateBackend,
		StateFile:              os.Getenv("RELAYFILE_STATE_FILE"),
		MaxWritebackAttempts:   intEnv("RELAYFILE_MAX_WRITEBACK_ATTEMPTS", 0),
		WritebackDelay:         durationEnv("RELAYFILE_WRITEBACK_RETRY_DELAY", 0),
		MaxEnvelopeAttempts:    intEnv("RELAYFILE_MAX_ENVELOPE_ATTEMPTS", 0),
		EnvelopeRetryDelay:     durationEnv("RELAYFILE_ENVELOPE_RETRY_DELAY", 0),
		SuppressionWindow:      durationEnv("RELAYFILE_SUPPRESSION_WINDOW", 0),
		CoalesceWindow:         durationEnv("RELAYFILE_COALESCE_WINDOW", 0),
		MaxStoredEnvelopes:     intEnv("RELAYFILE_MAX_STORED_ENVELOPES", 0),
		EnvelopeQueueSize:      intEnv("RELAYFILE_ENVELOPE_QUEUE_SIZE", 0),
		EnvelopeQueue:          envelopeQueue,
		WritebackQueue:         writebackQueue,
		EnvelopeWorkers:        intEnv("RELAYFILE_ENVELOPE_WORKERS", 0),
		WritebackWorkers:       intEnv("RELAYFILE_WRITEBACK_WORKERS", 0),
		ProviderMaxConcurrency: intEnv("RELAYFILE_PROVIDER_MAX_CONCURRENCY", 0),
		Adapters:               buildAdaptersFromEnv(),
		BackendProfile:         strings.TrimSpace(os.Getenv("RELAYFILE_BACKEND_PROFILE")),
	})
	server := httpapi.NewServerWithConfig(store, httpapi.ServerConfig{
		JWTSecret:          os.Getenv("RELAYFILE_JWT_SECRET"),
		InternalHMACSecret: os.Getenv("RELAYFILE_INTERNAL_HMAC_SECRET"),
		InternalMaxSkew:    durationEnv("RELAYFILE_INTERNAL_MAX_SKEW", 5*time.Minute),
		RateLimitMax:       intEnv("RELAYFILE_RATE_LIMIT_MAX", 0),
		RateLimitWindow:    durationEnv("RELAYFILE_RATE_LIMIT_WINDOW", time.Minute),
		MaxBodyBytes:       int64Env("RELAYFILE_MAX_BODY_BYTES", 0),
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

func int64Env(name string, fallback int64) int64 {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
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

func buildStorageBackendsFromEnv() (relayfile.StateBackend, relayfile.EnvelopeQueue, relayfile.WritebackQueue, error) {
	if _, _, _, err := storageProfileDefaultsFromEnv(); err != nil {
		return nil, nil, nil, err
	}
	stateBackend, err := buildStateBackendFromEnv()
	if err != nil {
		return nil, nil, nil, err
	}
	envelopeQueue, writebackQueue, err := buildQueuesFromEnv()
	if err != nil {
		return nil, nil, nil, err
	}
	return stateBackend, envelopeQueue, writebackQueue, nil
}

func buildStateBackendFromEnv() (relayfile.StateBackend, error) {
	profileStateDSN, _, _, err := storageProfileDefaultsFromEnv()
	if err != nil {
		return nil, err
	}
	stateBackendDSN := strings.TrimSpace(os.Getenv("RELAYFILE_STATE_BACKEND_DSN"))
	stateFile := strings.TrimSpace(os.Getenv("RELAYFILE_STATE_FILE"))
	switch {
	case stateBackendDSN != "":
		return relayfile.BuildStateBackendFromDSN(stateBackendDSN)
	case stateFile != "":
		return relayfile.BuildStateBackendFromDSN(stateFile)
	case profileStateDSN != "":
		return relayfile.BuildStateBackendFromDSN(profileStateDSN)
	default:
		return nil, nil
	}
}

func buildQueuesFromEnv() (relayfile.EnvelopeQueue, relayfile.WritebackQueue, error) {
	_, profileEnvelopeQueueDSN, profileWritebackQueueDSN, err := storageProfileDefaultsFromEnv()
	if err != nil {
		return nil, nil, err
	}
	envelopeQueueDSN := strings.TrimSpace(os.Getenv("RELAYFILE_ENVELOPE_QUEUE_DSN"))
	writebackQueueDSN := strings.TrimSpace(os.Getenv("RELAYFILE_WRITEBACK_QUEUE_DSN"))
	envelopeQueueFile := strings.TrimSpace(os.Getenv("RELAYFILE_ENVELOPE_QUEUE_FILE"))
	writebackQueueFile := strings.TrimSpace(os.Getenv("RELAYFILE_WRITEBACK_QUEUE_FILE"))

	var envelopeQueue relayfile.EnvelopeQueue
	var writebackQueue relayfile.WritebackQueue

	if envelopeQueueDSN != "" {
		envelopeQueue, err = relayfile.BuildEnvelopeQueueFromDSN(envelopeQueueDSN, intEnv("RELAYFILE_ENVELOPE_QUEUE_SIZE", 0))
		if err != nil {
			return nil, nil, err
		}
	} else if envelopeQueueFile != "" {
		envelopeQueue, err = relayfile.BuildEnvelopeQueueFromDSN(envelopeQueueFile, intEnv("RELAYFILE_ENVELOPE_QUEUE_SIZE", 0))
		if err != nil {
			return nil, nil, err
		}
	} else if profileEnvelopeQueueDSN != "" {
		envelopeQueue, err = relayfile.BuildEnvelopeQueueFromDSN(profileEnvelopeQueueDSN, intEnv("RELAYFILE_ENVELOPE_QUEUE_SIZE", 0))
		if err != nil {
			return nil, nil, err
		}
	}
	if writebackQueueDSN != "" {
		writebackQueue, err = relayfile.BuildWritebackQueueFromDSN(writebackQueueDSN, intEnv("RELAYFILE_WRITEBACK_QUEUE_SIZE", 1024))
		if err != nil {
			return nil, nil, err
		}
	} else if writebackQueueFile != "" {
		writebackQueue, err = relayfile.BuildWritebackQueueFromDSN(writebackQueueFile, intEnv("RELAYFILE_WRITEBACK_QUEUE_SIZE", 1024))
		if err != nil {
			return nil, nil, err
		}
	} else if profileWritebackQueueDSN != "" {
		writebackQueue, err = relayfile.BuildWritebackQueueFromDSN(profileWritebackQueueDSN, intEnv("RELAYFILE_WRITEBACK_QUEUE_SIZE", 1024))
		if err != nil {
			return nil, nil, err
		}
	}
	return envelopeQueue, writebackQueue, nil
}

func storageProfileDefaultsFromEnv() (stateBackendDSN, envelopeQueueDSN, writebackQueueDSN string, err error) {
	profile := strings.ToLower(strings.TrimSpace(os.Getenv("RELAYFILE_BACKEND_PROFILE")))
	dataDir := strings.TrimSpace(os.Getenv("RELAYFILE_DATA_DIR"))
	if dataDir == "" {
		dataDir = ".relayfile"
	}
	switch profile {
	case "", "custom":
		return "", "", "", nil
	case "memory", "inmemory":
		return "memory://", "memory://", "memory://", nil
	case "production", "prod":
		productionDSN := strings.TrimSpace(os.Getenv("RELAYFILE_PRODUCTION_DSN"))
		if productionDSN == "" {
			productionDSN = strings.TrimSpace(os.Getenv("RELAYFILE_POSTGRES_DSN"))
		}
		if productionDSN == "" {
			return "", "", "", fmt.Errorf("RELAYFILE_PRODUCTION_DSN or RELAYFILE_POSTGRES_DSN is required when RELAYFILE_BACKEND_PROFILE=%s", profile)
		}
		return productionDSN, productionDSN, productionDSN, nil
	case "durable-local", "local-durable":
		return "file://" + filepath.Join(dataDir, "state.json"),
			"file://" + filepath.Join(dataDir, "envelope-queue.json"),
			"file://" + filepath.Join(dataDir, "writeback-queue.json"),
			nil
	default:
		return "", "", "", fmt.Errorf("unsupported RELAYFILE_BACKEND_PROFILE: %s", profile)
	}
}

func buildAdaptersFromEnv() []relayfile.ProviderAdapter {
	notionToken := strings.TrimSpace(os.Getenv("RELAYFILE_NOTION_TOKEN"))
	notionTokenFile := strings.TrimSpace(os.Getenv("RELAYFILE_NOTION_TOKEN_FILE"))
	tokenProvider := buildNotionTokenProviderWithCache(
		notionToken,
		notionTokenFile,
		durationEnv("RELAYFILE_NOTION_TOKEN_CACHE_TTL", 15*time.Second),
	)
	if tokenProvider == nil {
		return nil
	}
	notionBaseURL := strings.TrimSpace(os.Getenv("RELAYFILE_NOTION_BASE_URL"))
	notionClient := relayfile.NewHTTPNotionWriteClient(relayfile.NotionHTTPClientOptions{
		BaseURL:       notionBaseURL,
		TokenProvider: tokenProvider,
		APIVersion:    strings.TrimSpace(os.Getenv("RELAYFILE_NOTION_API_VERSION")),
		UserAgent:     strings.TrimSpace(os.Getenv("RELAYFILE_NOTION_USER_AGENT")),
		MaxRetries:    intEnv("RELAYFILE_NOTION_MAX_RETRIES", 0),
		BaseDelay:     durationEnv("RELAYFILE_NOTION_RETRY_BASE_DELAY", 0),
		MaxDelay:      durationEnv("RELAYFILE_NOTION_RETRY_MAX_DELAY", 0),
	})
	log.Printf("notion writeback client enabled")
	return []relayfile.ProviderAdapter{
		relayfile.NewNotionAdapter(notionClient),
	}
}

func buildNotionTokenProvider(staticToken, tokenFile string) relayfile.NotionAccessTokenProvider {
	return buildNotionTokenProviderWithCache(staticToken, tokenFile, 0)
}

func buildNotionTokenProviderWithCache(staticToken, tokenFile string, cacheTTL time.Duration) relayfile.NotionAccessTokenProvider {
	staticToken = strings.TrimSpace(staticToken)
	tokenFile = strings.TrimSpace(tokenFile)
	switch {
	case tokenFile != "":
		var mu sync.Mutex
		cachedToken := ""
		cacheExpiresAt := time.Time{}
		return func(ctx context.Context) (string, error) {
			_ = ctx
			if cacheTTL > 0 {
				mu.Lock()
				if cachedToken != "" && time.Now().UTC().Before(cacheExpiresAt) {
					token := cachedToken
					mu.Unlock()
					return token, nil
				}
				mu.Unlock()
			}
			data, err := os.ReadFile(tokenFile)
			if err != nil {
				return "", err
			}
			token := strings.TrimSpace(string(data))
			if token == "" {
				return "", fmt.Errorf("notion token file is empty")
			}
			if cacheTTL > 0 {
				mu.Lock()
				cachedToken = token
				cacheExpiresAt = time.Now().UTC().Add(cacheTTL)
				mu.Unlock()
			}
			return token, nil
		}
	case staticToken != "":
		return func(ctx context.Context) (string, error) {
			_ = ctx
			return staticToken, nil
		}
	default:
		return nil
	}
}
