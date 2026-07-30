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

func TestPlanRejectsSharedStateFile(t *testing.T) {
	_, err := Plan(t.TempDir(), LayoutScoped, []string{"/github", "/slack"}, "/", "state.json")
	if err == nil || !strings.Contains(err.Error(), "use --state-dir") {
		t.Fatalf("expected state-dir guidance, got %v", err)
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
