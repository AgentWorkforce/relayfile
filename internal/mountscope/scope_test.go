package mountscope

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPlanRequiresScopedLayoutForMultiplePaths(t *testing.T) {
	_, err := Plan(t.TempDir(), LayoutExact, []string{"/github", "/slack"}, "/", "")
	if err == nil || !strings.Contains(err.Error(), "--local-layout=scoped") {
		t.Fatalf("expected scoped-layout guidance, got %v", err)
	}
}

func TestPlanDeduplicatesAndScopesRemotePaths(t *testing.T) {
	root := t.TempDir()
	got, err := Plan(
		root,
		LayoutScoped,
		[]string{"/github/repos/acme/cloud", "github/repos/acme/cloud/", "/slack/channels/proj-cloud"},
		"/",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []Scope{
		{RemotePath: "/github/repos/acme/cloud", LocalDir: filepath.Join(root, "github", "repos", "acme", "cloud")},
		{RemotePath: "/slack/channels/proj-cloud", LocalDir: filepath.Join(root, "slack", "channels", "proj-cloud")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Plan() = %#v, want %#v", got, want)
	}
}

func TestNormalizePathsCollapsesOverlappingRoots(t *testing.T) {
	got := NormalizePaths(
		[]string{"/github/repos/acme", "/slack/channels/ops", "/github", "/github/issues"},
		"/",
	)
	want := []string{"/slack/channels/ops", "/github"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizePaths() = %v, want %v", got, want)
	}
}

func TestNormalizePathsIgnoresBlankEntriesBeforeApplyingFallback(t *testing.T) {
	got := NormalizePaths([]string{"/github", "", " \t "}, "/")
	if want := []string{"/github"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizePaths() = %v, want %v", got, want)
	}

	got = NormalizePaths([]string{"", " "}, "/slack")
	if want := []string{"/slack"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizePaths(all blank) = %v, want fallback %v", got, want)
	}
}

func TestPlanRejectsSharedStateFile(t *testing.T) {
	_, err := Plan(t.TempDir(), LayoutScoped, []string{"/github", "/slack"}, "/", "state.json")
	if err == nil || !strings.Contains(err.Error(), "use --state-dir") {
		t.Fatalf("expected state-dir guidance, got %v", err)
	}
}

func TestPlanRejectsScopedRootsThatOverlapReservedLocalPaths(t *testing.T) {
	root := t.TempDir()
	for _, remotePath := range []string{"/.git/config", "/.Relay", "/.skills/tools", "/Digests", "/NODE_MODULES/pkg", "/_permissions.md"} {
		t.Run(remotePath, func(t *testing.T) {
			_, err := Plan(root, LayoutScoped, []string{remotePath}, "/", "")
			if err == nil ||
				!strings.Contains(err.Error(), remotePath) ||
				!strings.Contains(err.Error(), "reserved local path") ||
				!strings.Contains(err.Error(), "--local-layout=exact") {
				t.Fatalf("expected reserved scoped-root refusal with remedy, got %v", err)
			}
		})
	}
	if _, err := Plan(root, LayoutExact, []string{"/.relay"}, "/", ""); err != nil {
		t.Fatalf("single exact mount should not map its remote root beneath the reserved path: %v", err)
	}
}

func TestPlanRejectsScopedRootsContainingNestedRuntimeSegments(t *testing.T) {
	root := t.TempDir()
	for _, remotePath := range []string{
		"/github/.relay/private",
		"/github/.relayfile-mount-state.json",
		"/github/.relayfile-mount-state.json.tmp-123",
	} {
		t.Run(remotePath, func(t *testing.T) {
			_, err := Plan(root, LayoutScoped, []string{remotePath}, "/", "")
			if err == nil ||
				!strings.Contains(err.Error(), remotePath) ||
				!strings.Contains(err.Error(), "reserved mount runtime segment") {
				t.Fatalf("expected nested runtime segment refusal, got %v", err)
			}
		})
	}
}

func TestPlanRejectsLocalFilesystemIdentityOverlap(t *testing.T) {
	root := t.TempDir()
	for _, paths := range [][]string{
		{"/github", "/GitHub"},
		{"/GitHub", "/github/repos/acme"},
		{"/Straße", "/STRASSE"},
		{"/Café", "/Cafe\u0301"},
	} {
		_, err := Plan(root, LayoutScoped, paths, "/", "")
		if err == nil ||
			!strings.Contains(err.Error(), paths[0]) ||
			!strings.Contains(err.Error(), paths[1]) ||
			!strings.Contains(err.Error(), "normalization-insensitive") {
			t.Fatalf("expected local-identity overlap refusal for %v, got %v", paths, err)
		}
	}
}

func TestReadPathsFileSupportsJSONAndLines(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "paths.json")
	if err := os.WriteFile(jsonPath, []byte(`["/github","/linear/issues"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	gotJSON, err := ReadPathsFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/github", "/linear/issues"}; !reflect.DeepEqual(gotJSON, want) {
		t.Fatalf("ReadPathsFile(JSON) = %v, want %v", gotJSON, want)
	}

	linesPath := filepath.Join(dir, "paths.txt")
	if err := os.WriteFile(linesPath, []byte("\n# comment\n/github\n/slack/channels/proj-cloud\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gotLines, err := ReadPathsFile(linesPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/github", "/slack/channels/proj-cloud"}; !reflect.DeepEqual(gotLines, want) {
		t.Fatalf("ReadPathsFile(lines) = %v, want %v", gotLines, want)
	}
}

func TestReadPathsFileNullEntryCannotWidenAllowlist(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "paths.json")
	if err := os.WriteFile(filePath, []byte(`["/github",null,""]`), 0o644); err != nil {
		t.Fatal(err)
	}
	paths, err := ReadPathsFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := NormalizePaths(paths, "/"), []string{"/github"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized JSON paths = %v, want %v", got, want)
	}
}
