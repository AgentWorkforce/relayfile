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

func TestScopeMatchesPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		required string
		granted  map[string]struct{}
		path     string
		want     bool
	}{
		{
			name:     "exact short scope match",
			required: "fs:read",
			granted:  map[string]struct{}{"fs:read": {}},
			path:     "/any",
			want:     true,
		},
		{
			name:     "relayauth scope grants read for matching path",
			required: "fs:read",
			granted:  map[string]struct{}{"relayfile:fs:read:/src/app.ts": {}},
			path:     "/src/app.ts",
			want:     true,
		},
		{
			name:     "relayauth scope denies read for non-matching path",
			required: "fs:read",
			granted:  map[string]struct{}{"relayfile:fs:read:/src/app.ts": {}},
			path:     "/.env",
			want:     false,
		},
		{
			name:     "wildcard path grants all",
			required: "fs:read",
			granted:  map[string]struct{}{"relayfile:fs:read:*": {}},
			path:     "/anything",
			want:     true,
		},
		{
			name:     "directory wildcard grants descendants",
			required: "fs:read",
			granted:  map[string]struct{}{"relayfile:fs:read:/src/*": {}},
			path:     "/src/api/handler.ts",
			want:     true,
		},
		{
			name:     "directory wildcard does not grant siblings",
			required: "fs:read",
			granted:  map[string]struct{}{"relayfile:fs:read:/src/*": {}},
			path:     "/docs/readme.md",
			want:     false,
		},
		{
			name:     "write scope does not grant read",
			required: "fs:read",
			granted:  map[string]struct{}{"relayfile:fs:write:/src/*": {}},
			path:     "/src/app.ts",
			want:     false,
		},
		{
			name:     "wrong plane denied",
			required: "fs:read",
			granted:  map[string]struct{}{"relaycast:fs:read:*": {}},
			path:     "/src/app.ts",
			want:     false,
		},
		{
			name:     "multiple scopes, one matches",
			required: "fs:read",
			granted: map[string]struct{}{
				"relayfile:fs:read:/docs/*": {},
				"relayfile:fs:read:/src/*":  {},
			},
			path: "/src/app.ts",
			want: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := scopeMatchesPath(tt.granted, tt.required, tt.path)
			if got != tt.want {
				t.Fatalf("scopeMatchesPath(%v, %q, %q) = %v, want %v", tt.granted, tt.required, tt.path, got, tt.want)
			}
		})
	}

	t.Run("manage grants both read and write", func(t *testing.T) {
		t.Parallel()

		granted := map[string]struct{}{
			"relayfile:fs:manage:/src/*": {},
		}

		t.Run("read", func(t *testing.T) {
			t.Parallel()

			got := scopeMatchesPath(granted, "fs:read", "/src/app.ts")
			if got != true {
				t.Fatalf("scopeMatchesPath(%v, %q, %q) = %v, want %v", granted, "fs:read", "/src/app.ts", got, true)
			}
		})
		t.Run("write", func(t *testing.T) {
			t.Parallel()

			got := scopeMatchesPath(granted, "fs:write", "/src/app.ts")
			if got != true {
				t.Fatalf("scopeMatchesPath(%v, %q, %q) = %v, want %v", granted, "fs:write", "/src/app.ts", got, true)
			}
		})
	})
}
