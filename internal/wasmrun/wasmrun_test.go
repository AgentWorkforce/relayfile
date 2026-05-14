package wasmrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

var minimalWASM = []byte{
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
	0x01, 0x05, 0x01, 0x60, 0x00, 0x01, 0x7f,
	0x03, 0x02, 0x01, 0x00,
	0x07, 0x0a, 0x01, 0x06, 0x64, 0x69, 0x67, 0x65, 0x73, 0x74, 0x00, 0x00,
	0x0a, 0x06, 0x01, 0x04, 0x00, 0x41, 0x2a, 0x0b,
}

func TestEngineCompileAndInvokeNativeRunnerDeterministic(t *testing.T) {
	ctx := context.Background()
	provider := fakeProvider{
		events: []ChangeEvent{
			{ID: "2", Path: "/notion/other", Resource: ChangeEventResource{ID: "other", Path: "/notion/other"}},
			{ID: "1", Path: "/notion/databases/eng-roadmap/a", Resource: ChangeEventResource{ID: "a", Path: "/notion/databases/eng-roadmap/a"}},
		},
	}
	runner := ModuleRunnerFunc(func(ctx context.Context, req Invocation) (*DigestSection, error) {
		events, err := req.ChangeEvents(ctx, ChangeEventsFilter{Paths: []string{"/notion/databases/eng-roadmap/*"}})
		if err != nil {
			return nil, err
		}
		out := &DigestSection{Provider: "Eng Roadmap"}
		for _, event := range events {
			out.Bullets = append(out.Bullets, DigestBullet{
				Text:          event.Resource.ID + " at " + time.UnixMilli(req.NowMillis()).UTC().Format(time.RFC3339),
				CanonicalPath: event.Resource.Path,
			})
		}
		return out, nil
	})

	engine, err := NewEngine(ctx, Options{EventsProvider: provider, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(ctx)

	mod, err := engine.Compile(ctx, signedTestModule("eng-roadmap", minimalWASM))
	if err != nil {
		t.Fatal(err)
	}
	dctx := DigestContext{
		WorkspaceID: "rw_test",
		FunctionID:  "eng-roadmap",
		WindowFrom:  time.Unix(1700000000, 0).UTC(),
		WindowTo:    time.Unix(1700003600, 0).UTC(),
		PathScope:   []string{"/notion/databases/eng-roadmap/*"},
	}

	var first []byte
	for i := 0; i < 10; i++ {
		section, warnings, err := engine.Invoke(ctx, mod, dctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(warnings) != 0 {
			t.Fatalf("unexpected warnings: %#v", warnings)
		}
		got, err := json.Marshal(section)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = got
			continue
		}
		if !bytes.Equal(first, got) {
			t.Fatalf("non-deterministic output:\nfirst=%s\n got=%s", first, got)
		}
	}
	if !bytes.Contains(first, []byte(`"canonicalPath":"/notion/databases/eng-roadmap/a"`)) {
		t.Fatalf("filtered section missing scoped event: %s", first)
	}
	if bytes.Contains(first, []byte("other")) {
		t.Fatalf("section leaked out-of-scope event: %s", first)
	}
}

func TestEngineInvokeDefaultWazeroEntrypoint(t *testing.T) {
	ctx := context.Background()
	engine, err := NewEngine(ctx, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(ctx)

	mod, err := engine.Compile(ctx, signedTestModule("native", minimalWASM))
	if err != nil {
		t.Fatal(err)
	}
	section, warnings, err := engine.Invoke(ctx, mod, DigestContext{FunctionID: "native"})
	if err != nil {
		t.Fatal(err)
	}
	if section != nil {
		t.Fatalf("default ABI should not synthesize a section, got %#v", section)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
}

func TestEngineInvokeTimeout(t *testing.T) {
	ctx := context.Background()
	engine, err := NewEngine(ctx, Options{
		HardTimeout: 20 * time.Millisecond,
		SoftTimeout: time.Hour,
		Runner: ModuleRunnerFunc(func(ctx context.Context, req Invocation) (*DigestSection, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(ctx)

	mod, err := engine.Compile(ctx, signedTestModule("slow", minimalWASM))
	if err != nil {
		t.Fatal(err)
	}
	_, warnings, err := engine.Invoke(ctx, mod, DigestContext{FunctionID: "slow"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	if len(warnings) != 1 || warnings[0].Kind != WarningTimeout {
		t.Fatalf("expected timeout warning, got %#v", warnings)
	}
}

func TestEngineInvokeWarningClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		kind WarningKind
	}{
		{name: "host", err: ErrHostViolation, kind: WarningHostViolation},
		{name: "oom", err: errors.New("out of memory"), kind: WarningOOM},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			engine, err := NewEngine(ctx, Options{
				Runner: ModuleRunnerFunc(func(context.Context, Invocation) (*DigestSection, error) {
					return nil, tt.err
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close(ctx)
			mod, err := engine.Compile(ctx, signedTestModule(tt.name, minimalWASM))
			if err != nil {
				t.Fatal(err)
			}
			_, warnings, err := engine.Invoke(ctx, mod, DigestContext{FunctionID: tt.name})
			if !errors.Is(err, tt.err) {
				t.Fatalf("expected %v, got %v", tt.err, err)
			}
			if len(warnings) != 1 || warnings[0].Kind != tt.kind {
				t.Fatalf("expected %s warning, got %#v", tt.kind, warnings)
			}
		})
	}
}

func TestEngineInvokeDoesNotClassifyMissingMemoryAsOOM(t *testing.T) {
	ctx := context.Background()
	missingMemoryErr := errors.New("wasmrun: guest module exposes no memory")
	engine, err := NewEngine(ctx, Options{
		Runner: ModuleRunnerFunc(func(context.Context, Invocation) (*DigestSection, error) {
			return nil, missingMemoryErr
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(ctx)
	mod, err := engine.Compile(ctx, signedTestModule("missing-memory", minimalWASM))
	if err != nil {
		t.Fatal(err)
	}

	_, warnings, err := engine.Invoke(ctx, mod, DigestContext{FunctionID: "missing-memory"})
	if !errors.Is(err, missingMemoryErr) {
		t.Fatalf("expected %v, got %v", missingMemoryErr, err)
	}
	if len(warnings) != 0 {
		t.Fatalf("missing-memory errors should not be classified as OOM, got %#v", warnings)
	}
}

func TestEngineInvokeSoftWarning(t *testing.T) {
	ctx := context.Background()
	engine, err := NewEngine(ctx, Options{
		SoftTimeout: 5 * time.Millisecond,
		Runner: ModuleRunnerFunc(func(context.Context, Invocation) (*DigestSection, error) {
			time.Sleep(10 * time.Millisecond)
			return &DigestSection{Provider: "Slow"}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(ctx)
	mod, err := engine.Compile(ctx, signedTestModule("slow", minimalWASM))
	if err != nil {
		t.Fatal(err)
	}
	section, warnings, err := engine.Invoke(ctx, mod, DigestContext{FunctionID: "slow"})
	if err != nil {
		t.Fatal(err)
	}
	if section == nil || section.Provider != "Slow" {
		t.Fatalf("unexpected section: %#v", section)
	}
	if len(warnings) != 1 || warnings[0].Kind != WarningSoftSlow {
		t.Fatalf("expected soft warning, got %#v", warnings)
	}
}

func TestQuickJSWASMEmbedded(t *testing.T) {
	wasm, err := QuickJSWASM()
	if err != nil {
		t.Fatal(err)
	}
	if len(wasm) == 0 {
		t.Fatal("embedded QuickJS wasm was empty")
	}
	wasm[0] = 0xff
	again, err := QuickJSWASM()
	if err != nil {
		t.Fatal(err)
	}
	if again[0] == 0xff {
		t.Fatal("QuickJSWASM returned mutable package storage")
	}
}

func signedTestModule(name string, wasm []byte) SignedModule {
	return SignedModule{
		Manifest: Manifest{
			FunctionID:  name,
			Name:        name,
			ContentHash: ContentHash(wasm),
			EntryPoint:  "digest",
			SDKVersion:  "test",
		},
		Wasm: wasm,
	}
}

type fakeProvider struct {
	events []ChangeEvent
}

func (p fakeProvider) ChangeEvents(context.Context, string, ChangeEventsFilter) ([]ChangeEvent, error) {
	return append([]ChangeEvent(nil), p.events...), nil
}
