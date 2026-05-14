package wasmrun

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestEngineDefaultWazeroABIEndToEnd compiles a hand-encoded WASM module that
// exercises the full runtime contract — without any injected ModuleRunner — and
// asserts that:
//
//   - host_log reads memory and surfaces the level + message to Logger;
//   - host_change_events reads a filter JSON payload from guest memory, calls
//     the EventsProvider, and writes the events JSON back through the guest's
//     `alloc` export;
//   - the digest function's packed i64 return is decoded as a DigestSection
//     pulled from guest memory.
func TestEngineDefaultWazeroABIEndToEnd(t *testing.T) {
	ctx := context.Background()

	provider := &recordingProvider{
		events: []ChangeEvent{
			{
				ID:       "evt-1",
				Path:     "/notion/databases/eng-roadmap/a",
				Resource: ChangeEventResource{ID: "a", Path: "/notion/databases/eng-roadmap/a"},
			},
		},
	}
	logSink := &recordingLogger{}

	engine, err := NewEngine(ctx, Options{
		EventsProvider: provider,
		Logger:         logSink.Log,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(ctx)

	wasm := buildEchoModule(`{"provider":"echo"}`, `{"paths":["/notion/databases/eng-roadmap/*"]}`)
	mod, err := engine.Compile(ctx, signedTestModule("echo", wasm))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	dctx := DigestContext{
		WorkspaceID: "rw_echo",
		FunctionID:  "echo",
		WindowFrom:  time.Unix(1700000000, 0).UTC(),
		WindowTo:    time.Unix(1700003600, 0).UTC(),
		PathScope:   []string{"/notion/databases/eng-roadmap/*"},
	}

	section, warnings, err := engine.Invoke(ctx, mod, dctx)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
	if section == nil {
		t.Fatal("expected decoded DigestSection from real wazero path, got nil")
	}
	if section.Provider != "echo" {
		t.Fatalf("expected provider \"echo\", got %q", section.Provider)
	}

	if provider.calls.Load() == 0 {
		t.Fatal("host_change_events did not invoke EventsProvider")
	}
	if got := provider.lastFilter().Paths; len(got) != 1 || got[0] != "/notion/databases/eng-roadmap/*" {
		t.Fatalf("filter not delivered via guest memory: %#v", got)
	}

	entries := logSink.snapshot()
	if len(entries) == 0 {
		t.Fatal("host_log did not deliver any message to Logger")
	}
	found := false
	for _, entry := range entries {
		if entry.level == "info" && entry.message == "echo: hello" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected info/echo: hello from host_log, got %#v", entries)
	}
}

// TestEngineDefaultWazeroABINullSection verifies that a digest function that
// packs ptr=0 in its return value materializes a nil DigestSection rather than
// a spurious empty struct.
func TestEngineDefaultWazeroABINullSection(t *testing.T) {
	ctx := context.Background()
	provider := &recordingProvider{}
	engine, err := NewEngine(ctx, Options{EventsProvider: provider})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(ctx)

	// Pass an empty filter and an empty section payload — the builder will lay
	// out the JSON at offset 16 with length 0, so digest returns
	// packPointer(16, 0). Per the ABI, len=0 → nil section.
	wasm := buildEchoModule("", "")
	mod, err := engine.Compile(ctx, signedTestModule("null", wasm))
	if err != nil {
		t.Fatal(err)
	}
	section, _, err := engine.Invoke(ctx, mod, DigestContext{FunctionID: "null"})
	if err != nil {
		t.Fatal(err)
	}
	if section != nil {
		t.Fatalf("expected nil section for zero-length return, got %#v", section)
	}
}

type recordingProvider struct {
	mu         sync.Mutex
	events     []ChangeEvent
	lastFilt   ChangeEventsFilter
	calls      atomicCount
	lastWSpace string
}

func (p *recordingProvider) ChangeEvents(ctx context.Context, workspaceID string, filter ChangeEventsFilter) ([]ChangeEvent, error) {
	p.mu.Lock()
	p.lastFilt = filter
	p.lastWSpace = workspaceID
	p.mu.Unlock()
	p.calls.Add(1)
	return append([]ChangeEvent(nil), p.events...), nil
}

func (p *recordingProvider) lastFilter() ChangeEventsFilter {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastFilt
}

type logEntry struct {
	level   string
	message string
}

type recordingLogger struct {
	mu      sync.Mutex
	entries []logEntry
}

func (l *recordingLogger) Log(level, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, logEntry{level: level, message: message})
}

func (l *recordingLogger) snapshot() []logEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]logEntry(nil), l.entries...)
}

type atomicCount struct {
	mu sync.Mutex
	n  int
}

func (a *atomicCount) Add(delta int) {
	a.mu.Lock()
	a.n += delta
	a.mu.Unlock()
}

func (a *atomicCount) Load() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}
