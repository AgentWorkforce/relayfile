package main

import (
	"errors"
	"fmt"
	"testing"
)

// TestMapDelegatedTokenCloudError verifies that the delegated-token mint error
// classifier routes permanent client errors (bad/insufficient scopes, needs
// reauth) to needs-human sentinels while leaving genuinely transient backend
// failures retryable. The invalid_scope case is the storm-retry fix: a 400
// invalid_scope re-sends the same bad scopes on every retry and can never
// succeed, so it must NOT be treated as transient.
func TestMapDelegatedTokenCloudError(t *testing.T) {
	tests := []struct {
		name           string
		err            error
		wantSentinel   error // errors.Is target; nil means "no permanent sentinel"
		wantCredExpiry bool  // isMountCredentialExpired => pause-for-human, stop retry storm
	}{
		{
			name:           "nil error passes through",
			err:            nil,
			wantSentinel:   nil,
			wantCredExpiry: false,
		},
		{
			name:           "invalid_scope is permanent (storm-retry fix)",
			err:            &apiError{StatusCode: 400, Code: "invalid_scope", Message: "scopes must be relayfile path scopes within the requested paths"},
			wantSentinel:   ErrDelegatedScopeInvalid,
			wantCredExpiry: true,
		},
		{
			name:           "scope_insufficient is permanent",
			err:            &apiError{StatusCode: 403, Code: "scope_insufficient", Message: "insufficient scope"},
			wantSentinel:   ErrDelegatedScopeInsufficient,
			wantCredExpiry: true,
		},
		{
			name:           "needs_reauth is permanent",
			err:            &apiError{StatusCode: 401, Code: "needs_reauth", Message: "session expired"},
			wantSentinel:   ErrDelegatedRelayfileCredentialsExpired,
			wantCredExpiry: true,
		},
		{
			name:           "relayauth_unavailable stays transient (must remain retryable)",
			err:            &apiError{StatusCode: 503, Code: "relayauth_unavailable", Message: "upstream unavailable"},
			wantSentinel:   nil,
			wantCredExpiry: false,
		},
		{
			name:           "unknown code stays transient",
			err:            &apiError{StatusCode: 500, Code: "boom", Message: "internal error"},
			wantSentinel:   nil,
			wantCredExpiry: false,
		},
		{
			name:           "non-apiError stays transient",
			err:            fmt.Errorf("dial tcp: connection refused"),
			wantSentinel:   nil,
			wantCredExpiry: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapDelegatedTokenCloudError(tc.err)

			if tc.err == nil {
				if got != nil {
					t.Fatalf("expected nil for nil input, got %v", got)
				}
				return
			}

			if tc.wantSentinel != nil && !errors.Is(got, tc.wantSentinel) {
				t.Fatalf("expected error to wrap %v, got %v", tc.wantSentinel, got)
			}
			if tc.wantSentinel == nil {
				// Must not be misclassified as any permanent needs-human sentinel.
				for _, sentinel := range []error{ErrDelegatedScopeInvalid, ErrDelegatedScopeInsufficient, ErrDelegatedRelayfileCredentialsExpired, ErrCloudRefreshExpired} {
					if errors.Is(got, sentinel) {
						t.Fatalf("transient error misclassified as permanent %v: %v", sentinel, got)
					}
				}
			}

			if gotCred := isMountCredentialExpired(got); gotCred != tc.wantCredExpiry {
				t.Fatalf("isMountCredentialExpired = %v, want %v (err=%v)", gotCred, tc.wantCredExpiry, got)
			}
		})
	}
}
