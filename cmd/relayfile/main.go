package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/agentworkforce/relayfile/internal/httpapi"
	"github.com/agentworkforce/relayfile/internal/relayfile"
)

func main() {
	postgres, err := parsePostgresCommand(os.Args[1:], os.Stdin)
	if err != nil {
		log.Fatal(err)
	}
	if postgres != nil && postgres.action != "serve" {
		if err := executePostgresCommand(postgres, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	addrEnv := strings.TrimSpace(os.Getenv("RELAYFILE_ADDR"))
	addr := addrEnv
	if addr == "" {
		addr = ":8080"
	}

	// The internal webhook endpoint can turn HMAC-authenticated envelopes
	// into workspace file changes. An empty secret makes the server fall
	// back to the public, predictable "dev-internal-secret" constant — so
	// require an explicit secret in normal operation, and only allow the
	// built-in dev secret behind an explicit dev mode that is pinned to
	// loopback so it cannot be reached off-host.
	internalSecret := os.Getenv("RELAYFILE_INTERNAL_HMAC_SECRET")
	if strings.TrimSpace(internalSecret) == "" {
		if !boolEnv("RELAYFILE_DEV_MODE", false) {
			log.Fatalf("RELAYFILE_INTERNAL_HMAC_SECRET must be set; refusing to start with the built-in development secret. Set a strong secret, or set RELAYFILE_DEV_MODE=1 for local development (binds to loopback).")
		}
		if addrEnv == "" {
			addr = "127.0.0.1:8080"
		}
		log.Println("WARNING: RELAYFILE_DEV_MODE enabled with the built-in internal HMAC secret — local development only, do not use in production")
	}

	var stateBackend relayfile.StateBackend
	var envelopeQueue relayfile.EnvelopeQueue
	var writebackQueue relayfile.WritebackQueue
	if postgres != nil {
		stateBackend, envelopeQueue, writebackQueue, err = buildVerifiedStorage(postgres)
	} else {
		stateBackend, envelopeQueue, writebackQueue, err = buildStorageBackendsFromEnv()
	}
	if err != nil {
		log.Fatalf("failed to initialize storage backends: %v", err)
	}

	storeOptions := relayfile.StoreOptions{
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
		ExternalWritebackMode:  boolEnv("RELAYFILE_EXTERNAL_WRITEBACK", true),
	}
	var store *relayfile.Store
	if postgres != nil {
		storeOptions.BackendProfile = "production"
		store, err = relayfile.NewStoreWithOptionsChecked(storeOptions)
		if err != nil {
			log.Fatal("postgres_" + err.Error())
		}
	} else {
		store = relayfile.NewStoreWithOptions(storeOptions)
	}
	auth, err := authSettingsFromEnv()
	if err != nil {
		log.Fatalf("invalid relayfile auth config: %v", err)
	}
	server, err := httpapi.NewServerWithConfig(store, httpapi.ServerConfig{
		JWKSURL:               strings.TrimSpace(os.Getenv("RELAYAUTH_JWKS_URL")),
		InternalHMACSecret:    internalSecret,
		InternalMaxSkew:       durationEnv("RELAYFILE_INTERNAL_MAX_SKEW", 5*time.Minute),
		RateLimitMax:          intEnv("RELAYFILE_RATE_LIMIT_MAX", 0),
		RateLimitWindow:       durationEnv("RELAYFILE_RATE_LIMIT_WINDOW", time.Minute),
		MaxBodyBytes:          int64Env("RELAYFILE_MAX_BODY_BYTES", 0),
		AuthMode:              auth.mode,
		IntrospectionURL:      auth.introspectionURL,
		IntrospectionSecret:   auth.introspectionSecret,
		IntrospectionAudience: auth.audience,
		IntrospectionTimeout:  auth.timeout,
		IntrospectionCacheTTL: auth.cacheTTL,
	})
	if err != nil {
		log.Fatalf("invalid relayfile server config: %v", err)
	}
	logAuthMode(auth)

	log.Printf("relayfile listening on %s", addr)
	if err := http.ListenAndServe(addr, server); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

// authSettings is the bearer-auth configuration read from the environment.
// introspectionSecret must never be logged.
type authSettings struct {
	mode                string
	introspectionURL    string
	introspectionSecret string
	audience            string
	timeout             time.Duration
	cacheTTL            time.Duration
}

// deriveAuthMode picks the bearer auth mode. An explicit RELAYFILE_AUTH_MODE
// wins (validated by the server); otherwise RELAYCONTROL_INTERNAL_URL alone
// selects introspection, both it and RELAYAUTH_JWKS_URL select dual, and
// anything else keeps the historical JWKS behaviour.
func deriveAuthMode(explicitMode, relaycontrolURL, jwksURL string) string {
	if mode := strings.ToLower(strings.TrimSpace(explicitMode)); mode != "" {
		return mode
	}
	hasRelaycontrol := strings.TrimSpace(relaycontrolURL) != ""
	hasJWKS := strings.TrimSpace(jwksURL) != ""
	switch {
	case hasRelaycontrol && !hasJWKS:
		return httpapi.AuthModeIntrospection
	case hasRelaycontrol && hasJWKS:
		return httpapi.AuthModeDual
	default:
		return httpapi.AuthModeJWKS
	}
}

// introspectionEndpoint appends relaycontrol's introspection path to the
// RELAYCONTROL_INTERNAL_URL base unless it is already there.
func introspectionEndpoint(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	trimmed := strings.TrimRight(base, "/")
	if strings.HasSuffix(trimmed, httpapi.IntrospectionPath) {
		return trimmed
	}
	return trimmed + httpapi.IntrospectionPath
}

// introspectionSecretFromEnv reads RELAYCONTROL_INTROSPECT_SECRET or the file
// named by RELAYCONTROL_INTROSPECT_SECRET_FILE (contents trimmed). Setting
// both is an error. The secret value is never included in errors.
func introspectionSecretFromEnv() (string, error) {
	direct := os.Getenv("RELAYCONTROL_INTROSPECT_SECRET")
	file := strings.TrimSpace(os.Getenv("RELAYCONTROL_INTROSPECT_SECRET_FILE"))
	if direct != "" && file != "" {
		return "", fmt.Errorf("set only one of RELAYCONTROL_INTROSPECT_SECRET and RELAYCONTROL_INTROSPECT_SECRET_FILE")
	}
	if file == "" {
		return strings.TrimSpace(direct), nil
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read RELAYCONTROL_INTROSPECT_SECRET_FILE: %w", err)
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		return "", fmt.Errorf("RELAYCONTROL_INTROSPECT_SECRET_FILE %s is empty", file)
	}
	return secret, nil
}

func authSettingsFromEnv() (authSettings, error) {
	relaycontrolURL := os.Getenv("RELAYCONTROL_INTERNAL_URL")
	mode := deriveAuthMode(os.Getenv("RELAYFILE_AUTH_MODE"), relaycontrolURL, os.Getenv("RELAYAUTH_JWKS_URL"))
	// The introspection secret only matters when introspection can run; a
	// jwks-only deployment must not fail to start over a stale or missing
	// RELAYCONTROL_INTROSPECT_SECRET_FILE.
	secret := ""
	if mode == httpapi.AuthModeIntrospection || mode == httpapi.AuthModeDual {
		var err error
		secret, err = introspectionSecretFromEnv()
		if err != nil {
			return authSettings{}, err
		}
	}
	cacheTTL := durationEnv("RELAYFILE_INTROSPECTION_CACHE_TTL", 0)
	if strings.TrimSpace(os.Getenv("RELAYFILE_INTROSPECTION_CACHE_TTL")) != "" && cacheTTL <= 0 {
		// An explicit 0 disables the positive cache; the server treats a
		// zero value as "use the 60s default" and a negative one as off.
		cacheTTL = -1
	}
	return authSettings{
		mode:                mode,
		introspectionURL:    introspectionEndpoint(relaycontrolURL),
		introspectionSecret: secret,
		audience:            strings.TrimSpace(os.Getenv("RELAYFILE_PUBLIC_ORIGIN")),
		timeout:             durationEnv("RELAYFILE_INTROSPECTION_TIMEOUT", 0),
		cacheTTL:            cacheTTL,
	}, nil
}

func logAuthMode(auth authSettings) {
	if auth.mode == httpapi.AuthModeIntrospection || auth.mode == httpapi.AuthModeDual {
		host := "invalid-url"
		if parsed, err := url.Parse(auth.introspectionURL); err == nil {
			host = parsed.Host
		}
		log.Printf("relayfile auth mode: %s (relaycontrol introspection at %s)", auth.mode, host)
		return
	}
	log.Printf("relayfile auth mode: %s", auth.mode)
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

func boolEnv(name string, fallback bool) bool {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	default:
		log.Printf("invalid %s=%q, using fallback %v", name, raw, fallback)
		return fallback
	}
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
		// dataDir is commonly relative (defaults to ".relayfile"). A
		// "file://" + relative-path string parses with the first path
		// segment as the URL host, so the DSN consumer would resolve it
		// to filesystem root (/state.json). Resolve to an absolute path
		// and emit a fully-qualified file:// URL so the configured data
		// dir is honored.
		absDir, absErr := filepath.Abs(dataDir)
		if absErr != nil {
			return "", "", "", fmt.Errorf("resolve RELAYFILE_DATA_DIR %q: %w", dataDir, absErr)
		}
		return "file://" + filepath.Join(absDir, "state.json"),
			"file://" + filepath.Join(absDir, "envelope-queue.json"),
			"file://" + filepath.Join(absDir, "writeback-queue.json"),
			nil
	default:
		return "", "", "", fmt.Errorf("unsupported RELAYFILE_BACKEND_PROFILE: %s", profile)
	}
}

func buildAdaptersFromEnv() []relayfile.ProviderAdapter {
	// No provider-specific adapters are initialized by default.
	// All provider integration is handled externally via the generic webhook API.
	// Custom adapters can be provided as needed for specific use cases.
	return nil
}
