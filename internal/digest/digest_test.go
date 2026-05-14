package digest

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSchedulerMergesCustomerSection(t *testing.T) {
	d := generateForTest(t, testGeneratorConfig{
		functions: []FunctionRef{{Name: "eng-roadmap", FunctionID: "fn_eng", WorkspaceID: "rw_test"}},
		results: map[string]*DigestSection{
			"eng-roadmap": {
				Provider: "Eng Roadmap",
				Bullets: []DigestBullet{{
					Text:          "Digest function deployment moved to M1",
					CanonicalPath: "/notion/databases/eng-roadmap/deploy",
				}},
			},
		},
	})

	assertGolden(t, "testdata/customer/customer-merged-fixture.md", RenderMarkdown(d))
}

func TestSchedulerSortsCustomerSectionsAlphabetically(t *testing.T) {
	d := generateForTest(t, testGeneratorConfig{
		functions: []FunctionRef{
			{Name: "eng-roadmap", FunctionID: "fn_eng", WorkspaceID: "rw_test"},
			{Name: "customer-health", FunctionID: "fn_health", WorkspaceID: "rw_test"},
		},
		results: map[string]*DigestSection{
			"eng-roadmap": {
				Provider: "Eng Roadmap",
				Bullets:  []DigestBullet{{Text: "Roadmap moved", CanonicalPath: "/notion/roadmap"}},
			},
			"customer-health": {
				Provider: "Customer Health",
				Bullets:  []DigestBullet{{Text: "Acme renewed", CanonicalPath: "/salesforce/accounts/acme"}},
			},
		},
	})

	output := RenderMarkdown(d)
	assertOrder(t, output, "## github", "## linear", "## notion", "## Customer Health", "## Eng Roadmap")
}

func TestSchedulerSurfacesTimeoutWarning(t *testing.T) {
	d := generateForTest(t, testGeneratorConfig{
		functions: []FunctionRef{{Name: "eng-roadmap", FunctionID: "fn_eng", WorkspaceID: "rw_test"}},
		errors:    map[string]error{"eng-roadmap": ErrFunctionTimeout},
	})

	assertGolden(t, "testdata/customer/customer-timeout-fixture.md", RenderMarkdown(d))
}

func TestSchedulerSurfacesOOMWarning(t *testing.T) {
	d := generateForTest(t, testGeneratorConfig{
		functions: []FunctionRef{{Name: "eng-roadmap", FunctionID: "fn_eng", WorkspaceID: "rw_test"}},
		errors:    map[string]error{"eng-roadmap": ErrFunctionOOM},
	})

	assertGolden(t, "testdata/customer/customer-oom-fixture.md", RenderMarkdown(d))
}

func TestSchedulerSurfacesGenericError(t *testing.T) {
	d := generateForTest(t, testGeneratorConfig{
		functions: []FunctionRef{{Name: "eng-roadmap", FunctionID: "fn_eng", WorkspaceID: "rw_test"}},
		errors:    map[string]error{"eng-roadmap": errors.New("boom")},
	})

	assertGolden(t, "testdata/customer/customer-failure-fixture.md", RenderMarkdown(d))
}

func TestSchedulerHonorsBudget(t *testing.T) {
	clock := &fakeClock{current: fixtureGeneratedAt}
	runner := &fakeRunner{
		advance: map[string]time.Duration{
			"customer-health": 20 * time.Second,
			"eng-roadmap":     20 * time.Second,
		},
		results: map[string]*DigestSection{
			"customer-health": {Provider: "Customer Health", Bullets: []DigestBullet{{Text: "Acme renewed"}}},
			"eng-roadmap":     {Provider: "Eng Roadmap", Bullets: []DigestBullet{{Text: "Roadmap moved"}}},
			"skipped":         {Provider: "Skipped", Bullets: []DigestBullet{{Text: "Should not run"}}},
		},
		clock: clock,
	}
	g := NewGenerator(Options{
		Registry: &fakeRegistry{functions: []FunctionRef{
			{Name: "eng-roadmap", FunctionID: "fn_eng", WorkspaceID: "rw_test"},
			{Name: "skipped", FunctionID: "fn_skipped", WorkspaceID: "rw_test"},
			{Name: "customer-health", FunctionID: "fn_health", WorkspaceID: "rw_test"},
		}},
		Runner:         runner,
		CustomerBudget: 30 * time.Second,
		Now:            clock.now,
	})

	d, err := g.Generate(context.Background(), GenerateRequest{
		Window:             fixtureWindow(),
		FirstPartySections: firstPartySections(),
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	if got, want := runner.calls, []string{"customer-health", "eng-roadmap"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls mismatch\ngot:  %v\nwant: %v", got, want)
	}
	if got, want := d.Warnings, []DigestWarning{{Function: "skipped", Reason: WarningBudget}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("warnings mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestSchedulerNameSortedInvocationOrder(t *testing.T) {
	runner := &fakeRunner{results: map[string]*DigestSection{
		"zeta":  {Provider: "Zeta"},
		"alpha": {Provider: "Alpha"},
		"gamma": {Provider: "Gamma"},
	}}
	g := NewGenerator(Options{
		Registry: &fakeRegistry{functions: []FunctionRef{
			{Name: "zeta", FunctionID: "fn_zeta", WorkspaceID: "rw_test"},
			{Name: "alpha", FunctionID: "fn_alpha", WorkspaceID: "rw_test"},
			{Name: "gamma", FunctionID: "fn_gamma", WorkspaceID: "rw_test"},
		}},
		Runner: runner,
		Now:    func() time.Time { return fixtureGeneratedAt },
	})

	_, err := g.Generate(context.Background(), GenerateRequest{Window: fixtureWindow()})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	if got, want := runner.calls, []string{"alpha", "gamma", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls mismatch\ngot:  %v\nwant: %v", got, want)
	}
}

func TestSchedulerIsolatesFirstPartyFromCustomerFailure(t *testing.T) {
	d := generateForTest(t, testGeneratorConfig{
		functions: []FunctionRef{
			{Name: "bad", FunctionID: "fn_bad", WorkspaceID: "rw_test"},
			{Name: "good", FunctionID: "fn_good", WorkspaceID: "rw_test"},
		},
		panics: map[string]bool{"bad": true},
		results: map[string]*DigestSection{
			"good": {Provider: "Customer Health", Bullets: []DigestBullet{{Text: "Acme renewed"}}},
		},
	})

	output := RenderMarkdown(d)
	assertContains(t, output, "## github")
	assertContains(t, output, "## linear")
	assertContains(t, output, "## notion")
	assertContains(t, output, `warnings: ["bad: error"]`)
	assertContains(t, output, "## Customer Health")
}

func TestSchedulerByteStableAcrossReplays(t *testing.T) {
	config := testGeneratorConfig{
		functions: []FunctionRef{{Name: "eng-roadmap", FunctionID: "fn_eng", WorkspaceID: "rw_test"}},
		results: map[string]*DigestSection{
			"eng-roadmap": {Provider: "Eng Roadmap", Bullets: []DigestBullet{{Text: "Roadmap moved", CanonicalPath: "/notion/roadmap"}}},
		},
	}

	first := RenderMarkdown(generateForTest(t, config))
	second := RenderMarkdown(generateForTest(t, config))
	if first != second {
		t.Fatalf("digest output changed across identical replays\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

type testGeneratorConfig struct {
	functions []FunctionRef
	results   map[string]*DigestSection
	errors    map[string]error
	panics    map[string]bool
}

func generateForTest(t *testing.T, config testGeneratorConfig) Digest {
	t.Helper()
	g := NewGenerator(Options{
		Registry: &fakeRegistry{functions: config.functions},
		Runner: &fakeRunner{
			results: config.results,
			errors:  config.errors,
			panics:  config.panics,
		},
		Now: func() time.Time { return fixtureGeneratedAt },
	})
	d, err := g.Generate(context.Background(), GenerateRequest{
		Window:             fixtureWindow(),
		FirstPartySections: firstPartySections(),
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	return d
}

type fakeRegistry struct {
	functions []FunctionRef
}

func (r *fakeRegistry) ListActiveDigestFunctions(ctx context.Context, workspaceID string) ([]FunctionRef, error) {
	return append([]FunctionRef(nil), r.functions...), nil
}

type fakeRunner struct {
	results map[string]*DigestSection
	errors  map[string]error
	panics  map[string]bool
	advance map[string]time.Duration
	clock   *fakeClock
	calls   []string
}

func (r *fakeRunner) RunDigestFunction(ctx context.Context, fn FunctionRef, input DigestContext) (*DigestSection, error) {
	r.calls = append(r.calls, fn.Name)
	if r.panics[fn.Name] {
		panic("test panic")
	}
	if r.clock != nil {
		r.clock.current = r.clock.current.Add(r.advance[fn.Name])
	}
	if err := r.errors[fn.Name]; err != nil {
		return nil, err
	}
	section := r.results[fn.Name]
	if section == nil {
		return nil, nil
	}
	copied := *section
	copied.Bullets = append([]DigestBullet(nil), section.Bullets...)
	return &copied, nil
}

type fakeClock struct {
	current time.Time
}

func (c *fakeClock) now() time.Time {
	return c.current
}

var (
	fixtureDate        = time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	fixtureGeneratedAt = time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
)

func fixtureWindow() Window {
	return Window{
		WorkspaceID: "rw_test",
		Covers:      "yesterday",
		Date:        fixtureDate,
		From:        fixtureDate,
		To:          fixtureGeneratedAt,
		GeneratedAt: fixtureGeneratedAt,
		TotalEvents: 5,
	}
}

func firstPartySections() []DigestSection {
	return []DigestSection{
		{
			Provider: "linear",
			Bullets: []DigestBullet{{
				Text:          "DIG-42 moved custom digest functions to implementation",
				CanonicalPath: "/linear/issues/DIG-42",
			}},
		},
		{
			Provider: "github",
			Bullets: []DigestBullet{{
				Text:          "PR #184 added the custom digest fixture",
				CanonicalPath: "/github/pull/184",
			}},
		},
		{
			Provider: "notion",
			Bullets: []DigestBullet{{
				Text:          "Runtime design page updated the QuickJS sandbox notes",
				CanonicalPath: "/notion/pages/runtime-design",
			}},
		},
	}
}

func assertGolden(t *testing.T, path string, got string) {
	t.Helper()
	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	want := string(wantBytes)
	if got != want {
		t.Fatalf("golden mismatch for %s\ngot:\n%s\nwant:\n%s", path, got, want)
	}
}

func assertOrder(t *testing.T, output string, parts ...string) {
	t.Helper()
	previous := -1
	for _, part := range parts {
		index := strings.Index(output, part)
		if index == -1 {
			t.Fatalf("output did not contain %q:\n%s", part, output)
		}
		if index <= previous {
			t.Fatalf("%q was not after previous marker in output:\n%s", part, output)
		}
		previous = index
	}
}

func assertContains(t *testing.T, output string, needle string) {
	t.Helper()
	if !strings.Contains(output, needle) {
		t.Fatalf("output did not contain %q:\n%s", needle, output)
	}
}
