package wasmrun

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestInvocationWiringSeedsDeterministicHostBridge verifies that Engine.Invoke
// constructs a fresh DeterministicSource per call keyed by
// (workspaceID, windowFromMillis) and exposes its sequences through the
// Invocation hooks consumed by host_random / host_uuid_seq.
func TestInvocationWiringSeedsDeterministicHostBridge(t *testing.T) {
	ctx := context.Background()

	captureA := captureInvocation{}
	captureB := captureInvocation{}

	dctx := DigestContext{
		WorkspaceID: "rw_det",
		FunctionID:  "echo",
		WindowFrom:  time.UnixMilli(1700000000123).UTC(),
		WindowTo:    time.UnixMilli(1700003600456).UTC(),
	}

	for _, capture := range []*captureInvocation{&captureA, &captureB} {
		engine, err := NewEngine(ctx, Options{Runner: capture})
		if err != nil {
			t.Fatal(err)
		}
		mod, err := engine.Compile(ctx, signedTestModule("det", minimalWASM))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := engine.Invoke(ctx, mod, dctx); err != nil {
			t.Fatal(err)
		}
		engine.Close(ctx)
	}

	if len(captureA.randoms) == 0 || len(captureA.randoms) != len(captureB.randoms) {
		t.Fatalf("random samples were not captured")
	}
	for i := range captureA.randoms {
		if captureA.randoms[i] != captureB.randoms[i] {
			t.Fatalf("Random sequence diverged at %d: %v vs %v", i, captureA.randoms[i], captureB.randoms[i])
		}
		if captureA.uuids[i] != captureB.uuids[i] {
			t.Fatalf("UUID sequence diverged at %d: %v vs %v", i, captureA.uuids[i], captureB.uuids[i])
		}
	}
	if captureA.now != captureB.now {
		t.Fatalf("NowMillis diverged: %d vs %d", captureA.now, captureB.now)
	}
	if captureA.now != dctx.WindowTo.UnixMilli() {
		t.Fatalf("NowMillis not pinned to WindowTo: got %d want %d", captureA.now, dctx.WindowTo.UnixMilli())
	}
}

type captureInvocation struct {
	randoms []float64
	uuids   []uint64
	now     int64
}

func (c *captureInvocation) Run(_ context.Context, req Invocation) (*DigestSection, error) {
	if req.NowMillis != nil {
		c.now = req.NowMillis()
	}
	if req.Random != nil {
		for i := 0; i < 5; i++ {
			c.randoms = append(c.randoms, req.Random())
		}
	}
	if req.NextUUIDSeq != nil {
		for i := 0; i < 5; i++ {
			c.uuids = append(c.uuids, req.NextUUIDSeq())
		}
	}
	return nil, nil
}

func TestDeterministicSourceSameSeed(t *testing.T) {
	a := NewDeterministicSource("rw_1", 123)
	b := NewDeterministicSource("rw_1", 123)
	for i := 0; i < 20; i++ {
		if a.Float64() != b.Float64() {
			t.Fatal("same seed produced different random sequence")
		}
	}
	if a.UUID() != b.UUID() {
		t.Fatal("same seed produced different deterministic UUID")
	}
}

func TestDeterministicSourceDifferentSeed(t *testing.T) {
	a := NewDeterministicSource("rw_1", 123)
	b := NewDeterministicSource("rw_2", 123)
	if a.Float64() == b.Float64() {
		t.Fatal("different workspace should alter seeded random sequence")
	}
}

func TestDeterminismBootstrapClosesAmbientHosts(t *testing.T) {
	source := DeterminismBootstrapSource()
	for _, token := range []string{
		"Date.now",
		"performance",
		"randomUUID",
		"fetch",
		"XMLHttpRequest",
		"WebSocket",
		"Math.random",
		"host_random",
		"host_uuid_seq",
	} {
		if !strings.Contains(source, token) {
			t.Fatalf("bootstrap missing %q", token)
		}
	}
}
