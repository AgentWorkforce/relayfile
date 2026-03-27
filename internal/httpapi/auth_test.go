package httpapi

import "testing"

func TestScopeMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		required string
		granted  map[string]struct{}
		want     bool
	}{
		{
			name:     "exact scope matches",
			required: "fs:read",
			granted:  map[string]struct{}{"fs:read": {}},
			want:     true,
		},
		{
			name:     "relayfile scoped read wildcard matches",
			required: "fs:read",
			granted:  map[string]struct{}{"relayfile:fs:read:*": {}},
			want:     true,
		},
		{
			name:     "relayfile read wildcard does not match write",
			required: "fs:write",
			granted:  map[string]struct{}{"relayfile:fs:read:*": {}},
			want:     false,
		},
		{
			name:     "relayfile full wildcard matches",
			required: "fs:read",
			granted:  map[string]struct{}{"relayfile:*:*:*": {}},
			want:     true,
		},
		{
			name:     "global wildcard matches",
			required: "fs:read",
			granted:  map[string]struct{}{"*:*:*:*": {}},
			want:     true,
		},
		{
			name:     "manage implies write",
			required: "fs:write",
			granted:  map[string]struct{}{"relayfile:fs:manage:*": {}},
			want:     true,
		},
		{
			name:     "wrong plane does not match",
			required: "fs:read",
			granted:  map[string]struct{}{"relaycast:fs:read:*": {}},
			want:     false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := scopeMatches(tt.granted, tt.required)
			if got != tt.want {
				t.Fatalf("scopeMatches(%v, %q) = %v, want %v", tt.granted, tt.required, got, tt.want)
			}
		})
	}
}
