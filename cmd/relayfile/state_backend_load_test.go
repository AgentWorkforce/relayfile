package main

import (
	"errors"
	"strings"
	"testing"
)

// TestStateBackendLoadDecision covers #569: a state backend load failure is
// fatal at startup unless RELAYFILE_DEV_MODE is enabled, and DSN values are
// never echoed into the log line.
func TestStateBackendLoadDecision(t *testing.T) {
	t.Parallel()

	const dsn = "postgres://relayfile:s3cret@db.internal:5432/relayfile"
	tests := []struct {
		name         string
		loadErr      error
		devMode      bool
		secrets      []string
		wantFatal    bool
		wantContains []string
		wantAbsent   []string
	}{
		{
			name:      "no load error",
			loadErr:   nil,
			wantFatal: false,
		},
		{
			name:         "load error without dev mode is fatal",
			loadErr:      errors.New("pq: SSL is not enabled on the server"),
			wantFatal:    true,
			wantContains: []string{"state backend failed to load", "SSL is not enabled", "RELAYFILE_DEV_MODE=1"},
		},
		{
			name:         "load error in dev mode warns and continues",
			loadErr:      errors.New("dial tcp: connection refused"),
			devMode:      true,
			wantFatal:    false,
			wantContains: []string{"WARNING", "state backend failed to load", "connection refused"},
		},
		{
			name:         "dsn in error is redacted",
			loadErr:      errors.New("open " + dsn + ": bad"),
			secrets:      []string{"", dsn},
			wantFatal:    true,
			wantContains: []string{"<redacted>"},
			wantAbsent:   []string{dsn, "s3cret"},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fatal, msg := stateBackendLoadDecision(tt.loadErr, tt.devMode, tt.secrets...)
			if fatal != tt.wantFatal {
				t.Fatalf("fatal = %v, want %v (msg=%q)", fatal, tt.wantFatal, msg)
			}
			if tt.loadErr == nil && msg != "" {
				t.Fatalf("msg = %q, want empty", msg)
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(msg, want) {
					t.Fatalf("msg %q missing %q", msg, want)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(msg, absent) {
					t.Fatalf("msg %q leaks %q", msg, absent)
				}
			}
		})
	}
}
