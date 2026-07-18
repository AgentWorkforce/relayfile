package httpapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Fixture-driven conformance tests for the Relayfile Path Contract v1
// (contract/README.md). The fixtures under contract/fixtures/ are
// language-neutral; this runner executes the scope-path and auth-matching
// areas against the real enforcement functions in auth.go and the grammar
// reference in path_contract.go.

type contractFixtureFile[T any] struct {
	Contract string `json:"contract"`
	Version  int    `json:"version"`
	Area     string `json:"area"`
	Cases    []T    `json:"cases"`
}

func loadContractFixtures[T any](t *testing.T, name, wantArea string) []T {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "contract", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture file %s: %v", name, err)
	}
	var file contractFixtureFile[T]
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse fixture file %s: %v", name, err)
	}
	if file.Contract != "relayfile-path-contract" || file.Version != 1 || file.Area != wantArea {
		t.Fatalf(
			"fixture file %s header = (%q, %d, %q), want (\"relayfile-path-contract\", 1, %q)",
			name, file.Contract, file.Version, file.Area, wantArea,
		)
	}
	if len(file.Cases) == 0 {
		t.Fatalf("fixture file %s has no cases", name)
	}
	return file.Cases
}

func TestContractFixturesScopePaths(t *testing.T) {
	t.Parallel()

	type scopePathCase struct {
		Name       string   `json:"name"`
		ScopePath  string   `json:"scope_path"`
		ProbeFiles []string `json:"probe_files"`
		Expect     struct {
			Valid    bool    `json:"valid"`
			MountsAt *string `json:"mounts_at"`
		} `json:"expect"`
	}

	for _, tt := range loadContractFixtures[scopePathCase](t, "scope-paths.json", "scope-paths") {
		tt := tt
		t.Run(tt.Name, func(t *testing.T) {
			t.Parallel()

			if got := scopePathValid(tt.ScopePath); got != tt.Expect.Valid {
				t.Fatalf("scopePathValid(%q) = %v, want %v", tt.ScopePath, got, tt.Expect.Valid)
			}

			root, ok := scopePathMountRoot(tt.ScopePath)
			if tt.Expect.MountsAt == nil {
				if ok {
					t.Fatalf("scopePathMountRoot(%q) = (%q, true), want no mount root", tt.ScopePath, root)
				}
			} else if !ok || root != *tt.Expect.MountsAt {
				t.Fatalf(
					"scopePathMountRoot(%q) = (%q, %v), want (%q, true)",
					tt.ScopePath, root, ok, *tt.Expect.MountsAt,
				)
			}

			// Invalid scope paths must never match a canonical file path.
			// This is the fence that fails an implementation whose matcher
			// accepts mid-path globs, even if it skips validity checks.
			if !tt.Expect.Valid {
				for _, probe := range tt.ProbeFiles {
					if scopePathMatches(tt.ScopePath, probe) {
						t.Fatalf(
							"scopePathMatches(%q, %q) = true, want false for invalid scope path",
							tt.ScopePath, probe,
						)
					}
				}
			}
		})
	}
}

func TestContractFixturesAuthMatching(t *testing.T) {
	t.Parallel()

	type authCase struct {
		Name     string   `json:"name"`
		Scopes   []string `json:"scopes"`
		Required string   `json:"required"`
		File     string   `json:"file"`
		Expect   struct {
			AuthMatches bool `json:"auth_matches"`
		} `json:"expect"`
	}

	for _, tt := range loadContractFixtures[authCase](t, "auth-matching.json", "auth-matching") {
		tt := tt
		t.Run(tt.Name, func(t *testing.T) {
			t.Parallel()

			granted := map[string]struct{}{}
			for _, scope := range tt.Scopes {
				granted[scope] = struct{}{}
			}
			got := scopeMatchesPath(granted, tt.Required, tt.File)
			if got != tt.Expect.AuthMatches {
				t.Fatalf(
					"scopeMatchesPath(%v, %q, %q) = %v, want %v",
					tt.Scopes, tt.Required, tt.File, got, tt.Expect.AuthMatches,
				)
			}
		})
	}
}
