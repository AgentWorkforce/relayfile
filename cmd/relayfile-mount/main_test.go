package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFloatEnvParsesValue(t *testing.T) {
	t.Setenv("RELAYFILE_TEST_FLOAT", "0.35")
	got := floatEnv("RELAYFILE_TEST_FLOAT", 0.1)
	if got != 0.35 {
		t.Fatalf("expected 0.35, got %f", got)
	}
}

func TestFloatEnvFallsBackOnInvalid(t *testing.T) {
	t.Setenv("RELAYFILE_TEST_FLOAT_BAD", "oops")
	got := floatEnv("RELAYFILE_TEST_FLOAT_BAD", 0.25)
	if got != 0.25 {
		t.Fatalf("expected fallback 0.25, got %f", got)
	}
}

func TestLazyReposEnvDefaultsFalse(t *testing.T) {
	t.Setenv("RELAYFILE_LAZY_REPOS", "")
	t.Setenv("RELAYFILE_MOUNT_LAZY_GITHUB_REPOS", "")

	if lazyReposEnv() {
		t.Fatal("expected lazy repos to default false")
	}
}

func TestLazyReposEnvParsesOptIn(t *testing.T) {
	t.Setenv("RELAYFILE_LAZY_REPOS", "true")
	t.Setenv("RELAYFILE_MOUNT_LAZY_GITHUB_REPOS", "")

	if !lazyReposEnv() {
		t.Fatal("expected RELAYFILE_LAZY_REPOS=true to opt in")
	}
}

func TestLazyReposEnvSupportsLegacyName(t *testing.T) {
	t.Setenv("RELAYFILE_LAZY_REPOS", "")
	t.Setenv("RELAYFILE_MOUNT_LAZY_GITHUB_REPOS", "true")

	if !lazyReposEnv() {
		t.Fatal("expected legacy lazy repos env var to opt in")
	}
}

func TestClampJitterRatio(t *testing.T) {
	if got := clampJitterRatio(-0.1); got != 0 {
		t.Fatalf("expected clamp to 0, got %f", got)
	}
	if got := clampJitterRatio(1.5); got != 1 {
		t.Fatalf("expected clamp to 1, got %f", got)
	}
	if got := clampJitterRatio(0.4); got != 0.4 {
		t.Fatalf("expected passthrough 0.4, got %f", got)
	}
}

func TestJitteredIntervalWithSample(t *testing.T) {
	base := 10 * time.Second
	if got := jitteredIntervalWithSample(base, 0, 0.2); got != base {
		t.Fatalf("expected no jitter interval %s, got %s", base, got)
	}
	if got := jitteredIntervalWithSample(base, 0.2, 0); got != 8*time.Second {
		t.Fatalf("expected min jitter interval 8s, got %s", got)
	}
	if got := jitteredIntervalWithSample(base, 0.2, 0.5); got != 10*time.Second {
		t.Fatalf("expected midpoint jitter interval 10s, got %s", got)
	}
	if got := jitteredIntervalWithSample(base, 0.2, 1); got != 12*time.Second {
		t.Fatalf("expected max jitter interval 12s, got %s", got)
	}
}

func TestResolveMountMode(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		fuse    bool
		want    string
		wantErr bool
	}{
		{name: "default empty mode uses poll", want: mountModePoll},
		{name: "explicit poll", mode: "poll", want: mountModePoll},
		{name: "explicit fuse", mode: "fuse", want: mountModeFuse},
		{name: "case and whitespace normalized", mode: " FUSE ", want: mountModeFuse},
		{name: "fuse flag overrides mode", mode: "poll", fuse: true, want: mountModeFuse},
		{name: "invalid mode errors", mode: "sync", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveMountMode(tc.mode, tc.fuse)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got mode %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveMountMode returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected mode %q, got %q", tc.want, got)
			}
		})
	}
}

func TestExecuteMountDispatchesPollMode(t *testing.T) {
	cfg := mountConfig{mode: mountModePoll}
	pollCalled := false
	fuseCalled := false

	err := executeMount(context.Background(), cfg,
		func(context.Context, mountConfig) error {
			pollCalled = true
			return nil
		},
		func(context.Context, mountConfig) error {
			fuseCalled = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("executeMount returned error: %v", err)
	}
	if !pollCalled {
		t.Fatal("expected poll runner to be called")
	}
	if fuseCalled {
		t.Fatal("did not expect fuse runner to be called")
	}
}

func TestExecuteMountDispatchesFuseMode(t *testing.T) {
	cfg := mountConfig{mode: mountModeFuse}
	pollCalled := false
	fuseCalled := false

	err := executeMount(context.Background(), cfg,
		func(context.Context, mountConfig) error {
			pollCalled = true
			return nil
		},
		func(context.Context, mountConfig) error {
			fuseCalled = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("executeMount returned error: %v", err)
	}
	if !fuseCalled {
		t.Fatal("expected fuse runner to be called")
	}
	if pollCalled {
		t.Fatal("did not expect poll runner to be called")
	}
}

func TestExecuteMountReturnsRunnerError(t *testing.T) {
	wantErr := errors.New("boom")
	cfg := mountConfig{mode: mountModeFuse}

	err := executeMount(context.Background(), cfg,
		func(context.Context, mountConfig) error { return nil },
		func(context.Context, mountConfig) error { return wantErr },
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error %v, got %v", wantErr, err)
	}
}

func TestExecuteMountRejectsUnsupportedMode(t *testing.T) {
	err := executeMount(context.Background(), mountConfig{mode: "invalid"},
		func(context.Context, mountConfig) error { return nil },
		func(context.Context, mountConfig) error { return nil },
	)
	if err == nil {
		t.Fatal("expected unsupported mode error")
	}
}

func TestNormalizeRemotePathsDedupesRepeatedFlagValues(t *testing.T) {
	got := normalizeRemotePaths(
		[]string{"/github/repos/acme/cloud", "github/repos/acme/cloud/", "/slack/channels/proj-cloud"},
		"/",
	)
	want := []string{"/github/repos/acme/cloud", "/slack/channels/proj-cloud"}
	if len(got) != len(want) {
		t.Fatalf("expected %d paths, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("path %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestScopedLocalDirKeepsProviderPrefixUnderMountRoot(t *testing.T) {
	got := scopedLocalDir("/workspace", "/github/repos/acme/cloud")
	want := filepath.Join("/workspace", "github", "repos", "acme", "cloud")
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestReadRemotePathsFileSupportsJSONAndLines(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "paths.json")
	if err := os.WriteFile(jsonPath, []byte(`["/github","/linear/issues"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	jsonPaths, err := readRemotePathsFile(jsonPath)
	if err != nil {
		t.Fatalf("read json paths: %v", err)
	}
	if want := []string{"/github", "/linear/issues"}; !stringSlicesEqual(jsonPaths, want) {
		t.Fatalf("expected json paths %v, got %v", want, jsonPaths)
	}

	linesPath := filepath.Join(dir, "paths.txt")
	if err := os.WriteFile(linesPath, []byte("\n# comment\n/github/repos/acme/cloud\n/slack/channels/proj-cloud\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linePaths, err := readRemotePathsFile(linesPath)
	if err != nil {
		t.Fatalf("read line paths: %v", err)
	}
	if want := []string{"/github/repos/acme/cloud", "/slack/channels/proj-cloud"}; !stringSlicesEqual(linePaths, want) {
		t.Fatalf("expected line paths %v, got %v", want, linePaths)
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
