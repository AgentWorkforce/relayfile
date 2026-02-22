package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIntEnvParsesValue(t *testing.T) {
	t.Setenv("RELAYFILE_TEST_INT", "42")
	got := intEnv("RELAYFILE_TEST_INT", 7)
	if got != 42 {
		t.Fatalf("expected 42, got %d", got)
	}
}

func TestIntEnvFallsBackOnInvalidValue(t *testing.T) {
	t.Setenv("RELAYFILE_TEST_INT_BAD", "not-a-number")
	got := intEnv("RELAYFILE_TEST_INT_BAD", 7)
	if got != 7 {
		t.Fatalf("expected fallback 7, got %d", got)
	}
}

func TestDurationEnvParsesValue(t *testing.T) {
	t.Setenv("RELAYFILE_TEST_DURATION", "150ms")
	got := durationEnv("RELAYFILE_TEST_DURATION", time.Second)
	if got != 150*time.Millisecond {
		t.Fatalf("expected 150ms, got %s", got)
	}
}

func TestDurationEnvFallsBackOnInvalidValue(t *testing.T) {
	t.Setenv("RELAYFILE_TEST_DURATION_BAD", "soon")
	got := durationEnv("RELAYFILE_TEST_DURATION_BAD", 2*time.Second)
	if got != 2*time.Second {
		t.Fatalf("expected fallback 2s, got %s", got)
	}
}

func TestInt64EnvParsesValue(t *testing.T) {
	t.Setenv("RELAYFILE_TEST_INT64", "1048576")
	got := int64Env("RELAYFILE_TEST_INT64", 512)
	if got != 1048576 {
		t.Fatalf("expected 1048576, got %d", got)
	}
}

func TestInt64EnvFallsBackOnInvalidValue(t *testing.T) {
	t.Setenv("RELAYFILE_TEST_INT64_BAD", "not-a-number")
	got := int64Env("RELAYFILE_TEST_INT64_BAD", 2048)
	if got != 2048 {
		t.Fatalf("expected fallback 2048, got %d", got)
	}
}

func TestEnvHelpersUseFallbackWhenUnset(t *testing.T) {
	_ = os.Unsetenv("RELAYFILE_TEST_INT_UNSET")
	_ = os.Unsetenv("RELAYFILE_TEST_INT64_UNSET")
	_ = os.Unsetenv("RELAYFILE_TEST_DURATION_UNSET")

	if got := intEnv("RELAYFILE_TEST_INT_UNSET", 9); got != 9 {
		t.Fatalf("expected fallback 9, got %d", got)
	}
	if got := int64Env("RELAYFILE_TEST_INT64_UNSET", 9000); got != 9000 {
		t.Fatalf("expected fallback 9000, got %d", got)
	}
	if got := durationEnv("RELAYFILE_TEST_DURATION_UNSET", 3*time.Second); got != 3*time.Second {
		t.Fatalf("expected fallback 3s, got %s", got)
	}
}

func TestBuildStateBackendFromEnvUnset(t *testing.T) {
	t.Setenv("RELAYFILE_STATE_BACKEND_DSN", "")
	t.Setenv("RELAYFILE_STATE_FILE", "")
	backend, err := buildStateBackendFromEnv()
	if err != nil {
		t.Fatalf("buildStateBackendFromEnv failed: %v", err)
	}
	if backend != nil {
		t.Fatalf("expected nil backend when state backend env is unset")
	}
}

func TestBuildStateBackendFromEnvDSN(t *testing.T) {
	t.Setenv("RELAYFILE_STATE_BACKEND_DSN", "memory://")
	backend, err := buildStateBackendFromEnv()
	if err != nil {
		t.Fatalf("buildStateBackendFromEnv failed: %v", err)
	}
	if backend == nil {
		t.Fatalf("expected memory backend from dsn")
	}
}

func TestBuildStateBackendFromEnvPostgresDSN(t *testing.T) {
	t.Setenv("RELAYFILE_STATE_BACKEND_DSN", "postgres://localhost/relayfile?sslmode=disable")
	backend, err := buildStateBackendFromEnv()
	if err != nil {
		t.Fatalf("buildStateBackendFromEnv failed for postgres dsn: %v", err)
	}
	if backend == nil {
		t.Fatalf("expected postgres backend from dsn")
	}
}

func TestBuildStateBackendFromEnvFileFallback(t *testing.T) {
	t.Setenv("RELAYFILE_STATE_BACKEND_DSN", "")
	t.Setenv("RELAYFILE_STATE_FILE", filepath.Join(t.TempDir(), "state.json"))
	backend, err := buildStateBackendFromEnv()
	if err != nil {
		t.Fatalf("buildStateBackendFromEnv failed: %v", err)
	}
	if backend == nil {
		t.Fatalf("expected file backend from state file fallback")
	}
}

func TestBuildStateBackendFromEnvInvalidDSN(t *testing.T) {
	t.Setenv("RELAYFILE_STATE_BACKEND_DSN", "unsupported://localhost/relayfile")
	_, err := buildStateBackendFromEnv()
	if err == nil {
		t.Fatalf("expected unsupported state backend scheme error")
	}
}

func TestBuildStateBackendFromEnvProfileFallback(t *testing.T) {
	t.Setenv("RELAYFILE_STATE_BACKEND_DSN", "")
	t.Setenv("RELAYFILE_STATE_FILE", "")
	t.Setenv("RELAYFILE_BACKEND_PROFILE", "inmemory")
	backend, err := buildStateBackendFromEnv()
	if err != nil {
		t.Fatalf("buildStateBackendFromEnv failed: %v", err)
	}
	if backend == nil {
		t.Fatalf("expected profile-backed state backend")
	}
}

func TestBuildQueuesFromEnvUnset(t *testing.T) {
	t.Setenv("RELAYFILE_ENVELOPE_QUEUE_FILE", "")
	t.Setenv("RELAYFILE_WRITEBACK_QUEUE_FILE", "")

	envelopeQueue, writebackQueue, err := buildQueuesFromEnv()
	if err != nil {
		t.Fatalf("buildQueuesFromEnv failed: %v", err)
	}
	if envelopeQueue != nil || writebackQueue != nil {
		t.Fatalf("expected nil queues when queue files are unset")
	}
}

func TestBuildQueuesFromEnvFileBacked(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RELAYFILE_ENVELOPE_QUEUE_FILE", filepath.Join(dir, "envelope-queue.json"))
	t.Setenv("RELAYFILE_WRITEBACK_QUEUE_FILE", filepath.Join(dir, "writeback-queue.json"))
	t.Setenv("RELAYFILE_ENVELOPE_QUEUE_SIZE", "12")
	t.Setenv("RELAYFILE_WRITEBACK_QUEUE_SIZE", "24")

	envelopeQueue, writebackQueue, err := buildQueuesFromEnv()
	if err != nil {
		t.Fatalf("buildQueuesFromEnv failed: %v", err)
	}
	if envelopeQueue == nil || writebackQueue == nil {
		t.Fatalf("expected both queues to be initialized")
	}
	if envelopeQueue.Capacity() != 12 {
		t.Fatalf("expected envelope queue capacity 12, got %d", envelopeQueue.Capacity())
	}
	if writebackQueue.Capacity() != 24 {
		t.Fatalf("expected writeback queue capacity 24, got %d", writebackQueue.Capacity())
	}
}

func TestBuildQueuesFromEnvDSNBacked(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RELAYFILE_ENVELOPE_QUEUE_DSN", "memory://")
	t.Setenv("RELAYFILE_WRITEBACK_QUEUE_DSN", "file://"+filepath.Join(dir, "writeback-queue-dsn.json"))
	t.Setenv("RELAYFILE_WRITEBACK_QUEUE_SIZE", "16")

	envelopeQueue, writebackQueue, err := buildQueuesFromEnv()
	if err != nil {
		t.Fatalf("buildQueuesFromEnv failed: %v", err)
	}
	if envelopeQueue == nil || writebackQueue == nil {
		t.Fatalf("expected both queues to be initialized from dsn")
	}
	if envelopeQueue.Capacity() != 1024 {
		t.Fatalf("expected memory envelope queue default capacity 1024, got %d", envelopeQueue.Capacity())
	}
	if writebackQueue.Capacity() != 16 {
		t.Fatalf("expected writeback queue capacity 16, got %d", writebackQueue.Capacity())
	}
}

func TestBuildQueuesFromEnvPostgresDSNBacked(t *testing.T) {
	t.Setenv("RELAYFILE_ENVELOPE_QUEUE_DSN", "postgres://localhost/relayfile?sslmode=disable")
	t.Setenv("RELAYFILE_WRITEBACK_QUEUE_DSN", "postgres://localhost/relayfile?sslmode=disable")
	t.Setenv("RELAYFILE_ENVELOPE_QUEUE_SIZE", "22")
	t.Setenv("RELAYFILE_WRITEBACK_QUEUE_SIZE", "33")

	envelopeQueue, writebackQueue, err := buildQueuesFromEnv()
	if err != nil {
		t.Fatalf("buildQueuesFromEnv failed for postgres dsn: %v", err)
	}
	if envelopeQueue == nil || writebackQueue == nil {
		t.Fatalf("expected both queues to be initialized from postgres dsn")
	}
	if envelopeQueue.Capacity() != 22 {
		t.Fatalf("expected postgres envelope queue capacity 22, got %d", envelopeQueue.Capacity())
	}
	if writebackQueue.Capacity() != 33 {
		t.Fatalf("expected postgres writeback queue capacity 33, got %d", writebackQueue.Capacity())
	}
}

func TestBuildQueuesFromEnvInvalidDSN(t *testing.T) {
	t.Setenv("RELAYFILE_ENVELOPE_QUEUE_DSN", "redis://localhost:6379/0")
	_, _, err := buildQueuesFromEnv()
	if err == nil {
		t.Fatalf("expected buildQueuesFromEnv to fail for unsupported queue dsn scheme")
	}
}

func TestBuildStorageBackendsFromEnv(t *testing.T) {
	t.Setenv("RELAYFILE_STATE_BACKEND_DSN", "memory://")
	t.Setenv("RELAYFILE_ENVELOPE_QUEUE_DSN", "memory://")
	t.Setenv("RELAYFILE_WRITEBACK_QUEUE_DSN", "memory://")

	stateBackend, envelopeQueue, writebackQueue, err := buildStorageBackendsFromEnv()
	if err != nil {
		t.Fatalf("buildStorageBackendsFromEnv failed: %v", err)
	}
	if stateBackend == nil || envelopeQueue == nil || writebackQueue == nil {
		t.Fatalf("expected non-nil state and queue backends")
	}
}

func TestBuildStorageBackendsFromEnvDurableLocalProfile(t *testing.T) {
	t.Setenv("RELAYFILE_BACKEND_PROFILE", "durable-local")
	t.Setenv("RELAYFILE_DATA_DIR", t.TempDir())

	stateBackend, envelopeQueue, writebackQueue, err := buildStorageBackendsFromEnv()
	if err != nil {
		t.Fatalf("buildStorageBackendsFromEnv failed: %v", err)
	}
	if stateBackend == nil || envelopeQueue == nil || writebackQueue == nil {
		t.Fatalf("expected non-nil backends for durable-local profile")
	}
}

func TestBuildStorageBackendsFromEnvProductionProfile(t *testing.T) {
	t.Setenv("RELAYFILE_BACKEND_PROFILE", "production")
	t.Setenv("RELAYFILE_PRODUCTION_DSN", "postgres://localhost/relayfile?sslmode=disable")

	stateBackend, envelopeQueue, writebackQueue, err := buildStorageBackendsFromEnv()
	if err != nil {
		t.Fatalf("buildStorageBackendsFromEnv failed: %v", err)
	}
	if stateBackend == nil || envelopeQueue == nil || writebackQueue == nil {
		t.Fatalf("expected non-nil backends for production profile")
	}
}

func TestBuildStorageBackendsFromEnvProductionProfileMissingDSN(t *testing.T) {
	t.Setenv("RELAYFILE_BACKEND_PROFILE", "production")
	t.Setenv("RELAYFILE_PRODUCTION_DSN", "")
	t.Setenv("RELAYFILE_POSTGRES_DSN", "")
	_, _, _, err := buildStorageBackendsFromEnv()
	if err == nil {
		t.Fatalf("expected production profile with missing dsn to return an error")
	}
}

func TestBuildStorageBackendsFromEnvInvalidProfile(t *testing.T) {
	t.Setenv("RELAYFILE_BACKEND_PROFILE", "unsupported-profile")
	_, _, _, err := buildStorageBackendsFromEnv()
	if err == nil {
		t.Fatalf("expected invalid profile to return an error")
	}
}

func TestBuildQueuesFromEnvProfileAllowsExplicitFileOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RELAYFILE_BACKEND_PROFILE", "inmemory")
	t.Setenv("RELAYFILE_ENVELOPE_QUEUE_FILE", filepath.Join(dir, "env-override.json"))
	t.Setenv("RELAYFILE_WRITEBACK_QUEUE_FILE", filepath.Join(dir, "wb-override.json"))

	envelopeQueue, writebackQueue, err := buildQueuesFromEnv()
	if err != nil {
		t.Fatalf("buildQueuesFromEnv failed: %v", err)
	}
	if !strings.Contains(fmt.Sprintf("%T", envelopeQueue), "fileEnvelopeQueue") {
		t.Fatalf("expected envelope queue override to use file-backed queue, got %T", envelopeQueue)
	}
	if !strings.Contains(fmt.Sprintf("%T", writebackQueue), "fileWritebackQueue") {
		t.Fatalf("expected writeback queue override to use file-backed queue, got %T", writebackQueue)
	}
}
