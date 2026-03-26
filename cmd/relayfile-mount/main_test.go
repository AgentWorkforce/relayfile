package main

import (
	"context"
	"errors"
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
