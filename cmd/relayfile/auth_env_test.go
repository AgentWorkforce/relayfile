package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clearAuthEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"RELAYFILE_AUTH_MODE",
		"RELAYCONTROL_INTERNAL_URL",
		"RELAYAUTH_JWKS_URL",
		"RELAYCONTROL_INTROSPECT_SECRET",
		"RELAYCONTROL_INTROSPECT_SECRET_FILE",
		"RELAYFILE_PUBLIC_ORIGIN",
		"RELAYFILE_INTROSPECTION_TIMEOUT",
		"RELAYFILE_INTROSPECTION_CACHE_TTL",
	} {
		t.Setenv(name, "")
	}
}

func TestDeriveAuthMode(t *testing.T) {
	tests := []struct {
		name         string
		explicit     string
		relaycontrol string
		jwks         string
		want         string
	}{
		{name: "nothing set keeps jwks", want: "jwks"},
		{name: "jwks only", jwks: "https://jwks.example.test", want: "jwks"},
		{name: "relaycontrol only", relaycontrol: "http://relaycontrol:8080", want: "introspection"},
		{name: "both set", relaycontrol: "http://relaycontrol:8080", jwks: "https://jwks.example.test", want: "dual"},
		{name: "whitespace values count as unset", relaycontrol: "  ", jwks: " ", want: "jwks"},
		{name: "explicit wins over derivation", explicit: "jwks", relaycontrol: "http://relaycontrol:8080", want: "jwks"},
		{name: "explicit is normalized", explicit: " Dual ", want: "dual"},
		{name: "explicit invalid passes through for server validation", explicit: "oauth", want: "oauth"},
	}
	for _, tt := range tests {
		if got := deriveAuthMode(tt.explicit, tt.relaycontrol, tt.jwks); got != tt.want {
			t.Fatalf("%s: deriveAuthMode = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestIntrospectionEndpoint(t *testing.T) {
	tests := map[string]string{
		"":                              "",
		"http://relaycontrol:8080":      "http://relaycontrol:8080/internal/v1/auth/introspect",
		"http://relaycontrol:8080/":     "http://relaycontrol:8080/internal/v1/auth/introspect",
		"http://relaycontrol:8080/base": "http://relaycontrol:8080/base/internal/v1/auth/introspect",
		"http://relaycontrol:8080/internal/v1/auth/introspect":  "http://relaycontrol:8080/internal/v1/auth/introspect",
		"http://relaycontrol:8080/internal/v1/auth/introspect/": "http://relaycontrol:8080/internal/v1/auth/introspect",
	}
	for in, want := range tests {
		if got := introspectionEndpoint(in); got != want {
			t.Fatalf("introspectionEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIntrospectionSecretFromEnv(t *testing.T) {
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "secret")
	if err := os.WriteFile(secretFile, []byte("  file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyFile := filepath.Join(dir, "empty")
	if err := os.WriteFile(emptyFile, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("direct", func(t *testing.T) {
		clearAuthEnv(t)
		t.Setenv("RELAYCONTROL_INTROSPECT_SECRET", " direct-secret ")
		got, err := introspectionSecretFromEnv()
		if err != nil || got != "direct-secret" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("file contents trimmed", func(t *testing.T) {
		clearAuthEnv(t)
		t.Setenv("RELAYCONTROL_INTROSPECT_SECRET_FILE", secretFile)
		got, err := introspectionSecretFromEnv()
		if err != nil || got != "file-secret" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("both set is an error", func(t *testing.T) {
		clearAuthEnv(t)
		t.Setenv("RELAYCONTROL_INTROSPECT_SECRET", "direct-secret")
		t.Setenv("RELAYCONTROL_INTROSPECT_SECRET_FILE", secretFile)
		_, err := introspectionSecretFromEnv()
		if err == nil || !strings.Contains(err.Error(), "only one of") {
			t.Fatalf("expected both-set error, got %v", err)
		}
		if strings.Contains(err.Error(), "direct-secret") {
			t.Fatalf("error leaked the secret")
		}
	})
	t.Run("missing file is an error", func(t *testing.T) {
		clearAuthEnv(t)
		t.Setenv("RELAYCONTROL_INTROSPECT_SECRET_FILE", filepath.Join(dir, "missing"))
		if _, err := introspectionSecretFromEnv(); err == nil {
			t.Fatalf("expected error for missing file")
		}
	})
	t.Run("empty file is an error", func(t *testing.T) {
		clearAuthEnv(t)
		t.Setenv("RELAYCONTROL_INTROSPECT_SECRET_FILE", emptyFile)
		if _, err := introspectionSecretFromEnv(); err == nil {
			t.Fatalf("expected error for empty file")
		}
	})
	t.Run("neither set", func(t *testing.T) {
		clearAuthEnv(t)
		got, err := introspectionSecretFromEnv()
		if err != nil || got != "" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
}

func TestAuthSettingsFromEnv(t *testing.T) {
	t.Run("unset keeps jwks defaults", func(t *testing.T) {
		clearAuthEnv(t)
		got, err := authSettingsFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if got != (authSettings{mode: "jwks"}) {
			t.Fatalf("unexpected settings for empty env: %+v", got)
		}
	})
	t.Run("introspection", func(t *testing.T) {
		clearAuthEnv(t)
		t.Setenv("RELAYCONTROL_INTERNAL_URL", "http://relaycontrol.relay.svc:8080")
		t.Setenv("RELAYCONTROL_INTROSPECT_SECRET", "s3cret")
		t.Setenv("RELAYFILE_PUBLIC_ORIGIN", "https://files.example.test")
		t.Setenv("RELAYFILE_INTROSPECTION_TIMEOUT", "2s")
		t.Setenv("RELAYFILE_INTROSPECTION_CACHE_TTL", "30s")
		got, err := authSettingsFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		want := authSettings{
			mode:                "introspection",
			introspectionURL:    "http://relaycontrol.relay.svc:8080/internal/v1/auth/introspect",
			introspectionSecret: "s3cret",
			audience:            "https://files.example.test",
			timeout:             2 * time.Second,
			cacheTTL:            30 * time.Second,
		}
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})
	t.Run("explicit zero cache TTL disables the cache", func(t *testing.T) {
		clearAuthEnv(t)
		t.Setenv("RELAYFILE_INTROSPECTION_CACHE_TTL", "0")
		got, err := authSettingsFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if got.cacheTTL >= 0 {
			t.Fatalf("expected a negative (disabled) cache TTL, got %s", got.cacheTTL)
		}
	})
	t.Run("both secret sources is fatal", func(t *testing.T) {
		clearAuthEnv(t)
		t.Setenv("RELAYCONTROL_INTROSPECT_SECRET", "a")
		t.Setenv("RELAYCONTROL_INTROSPECT_SECRET_FILE", "/nonexistent")
		if _, err := authSettingsFromEnv(); err == nil {
			t.Fatalf("expected error")
		}
	})
}
