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
			name:     "exact match",
			required: "fs:read",
			granted:  map[string]struct{}{"relayfile:fs:read:/src/app.ts": {}},
			path:     "/src/app.ts",
			want:     true,
		},
		{
			name:     "path prefix",
			required: "fs:read",
			granted:  map[string]struct{}{"relayfile:fs:read:/src/*": {}},
			path:     "/src/components/App.tsx",
			want:     true,
		},
		{
			name:     "wildcard",
			required: "fs:read",
			granted:  map[string]struct{}{"relayfile:fs:read:*": {}},
			path:     "/docs/readme.md",
			want:     true,
		},
		{
			name:     "wrong plane",
			required: "fs:read",
			granted:  map[string]struct{}{"relaycast:fs:read:/src/*": {}},
			path:     "/src/app.ts",
			want:     false,
		},
		{
			name:     ".env denied when only /src/app.ts scoped",
			required: "fs:read",
			granted:  map[string]struct{}{"relayfile:fs:read:/src/app.ts": {}},
			path:     "/.env",
			want:     false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := scopeMatchesPath(tt.granted, tt.required, tt.path)
			if got != tt.want {
				t.Fatalf(
					"scopeMatchesPath(%v, %q, %q) = %v, want %v",
					tt.granted,
					tt.required,
					tt.path,
					got,
					tt.want,
				)
			}
		})
	}

	t.Run("manage implies read/write", func(t *testing.T) {
		t.Parallel()

		granted := map[string]struct{}{
			"relayfile:fs:manage:/src/*": {},
		}

		for _, action := range []string{"read", "write"} {
			action := action
			t.Run(action, func(t *testing.T) {
				t.Parallel()

				got := scopeMatchesPath(granted, "fs:"+action, "/src/app.ts")
				if got != true {
					t.Fatalf(
						"scopeMatchesPath(%v, %q, %q) = %v, want %v",
						granted,
						"fs:"+action,
						"/src/app.ts",
						got,
						true,
					)
				}
			})
		}
	})
}
