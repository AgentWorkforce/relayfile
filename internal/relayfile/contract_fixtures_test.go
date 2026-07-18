package relayfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Fixture-driven conformance tests for the Relayfile Path Contract v1
// (contract/README.md): the path-normalization and containment areas run
// against the store's normalizePath and withinBase, the contract's
// reference implementations.

type pathContractFixtureFile[T any] struct {
	Contract string `json:"contract"`
	Version  int    `json:"version"`
	Area     string `json:"area"`
	Cases    []T    `json:"cases"`
}

func loadPathContractFixtures[T any](t *testing.T, name, wantArea string) []T {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "contract", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture file %s: %v", name, err)
	}
	var file pathContractFixtureFile[T]
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

func TestContractFixturesPathNormalization(t *testing.T) {
	t.Parallel()

	type normalizationCase struct {
		Name   string `json:"name"`
		Path   string `json:"path"`
		Expect struct {
			Normalized string `json:"normalized"`
		} `json:"expect"`
	}

	for _, tt := range loadPathContractFixtures[normalizationCase](t, "path-normalization.json", "path-normalization") {
		tt := tt
		t.Run(tt.Name, func(t *testing.T) {
			t.Parallel()

			if got := normalizePath(tt.Path); got != tt.Expect.Normalized {
				t.Fatalf("normalizePath(%q) = %q, want %q", tt.Path, got, tt.Expect.Normalized)
			}
		})
	}
}

func TestContractFixturesContainment(t *testing.T) {
	t.Parallel()

	type containmentCase struct {
		Name      string `json:"name"`
		Base      string `json:"base"`
		Candidate string `json:"candidate"`
		Expect    struct {
			Contains bool `json:"contains"`
		} `json:"expect"`
	}

	for _, tt := range loadPathContractFixtures[containmentCase](t, "containment.json", "containment") {
		tt := tt
		t.Run(tt.Name, func(t *testing.T) {
			t.Parallel()

			if got := withinBase(tt.Base, tt.Candidate); got != tt.Expect.Contains {
				t.Fatalf("withinBase(%q, %q) = %v, want %v", tt.Base, tt.Candidate, got, tt.Expect.Contains)
			}
		})
	}
}
