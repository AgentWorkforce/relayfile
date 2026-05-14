package wasmrun

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestParseSignedModuleRoundTripAndVerifier(t *testing.T) {
	env := signedTestModule("eng-roadmap", minimalWASM)
	env.Signature = []byte("signed")
	wire, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseSignedModule(wire)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Manifest.ContentHash != env.Manifest.ContentHash {
		t.Fatalf("hash mismatch: %s != %s", parsed.Manifest.ContentHash, env.Manifest.ContentHash)
	}

	verifier := &countingVerifier{}
	engine, err := NewEngine(context.Background(), Options{Verifier: verifier})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(context.Background())
	if _, err := engine.Compile(context.Background(), parsed); err != nil {
		t.Fatal(err)
	}
	if verifier.count != 1 {
		t.Fatalf("verifier called %d times", verifier.count)
	}
}

func TestParseSignedModuleAcceptsOpaqueContentHash(t *testing.T) {
	env := signedTestModule("opaque", minimalWASM)
	env.Manifest.ContentHash = ContentHash([]byte("bundled-js-plus-sdk-version"))
	wire, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseSignedModule(wire)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Manifest.ContentHash != env.Manifest.ContentHash {
		t.Fatalf("hash mismatch: %s != %s", parsed.Manifest.ContentHash, env.Manifest.ContentHash)
	}
}

func TestParseSignedModuleRejectsMalformedHash(t *testing.T) {
	env := signedTestModule("bad", minimalWASM)
	env.Manifest.ContentHash = "sha256:bad"
	wire, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSignedModule(wire); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("expected invalid envelope, got %v", err)
	}
}

func TestCompileRejectsVerifierFailure(t *testing.T) {
	ctx := context.Background()
	engine, err := NewEngine(ctx, Options{Verifier: failingVerifier{}})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(ctx)
	if _, err := engine.Compile(ctx, signedTestModule("unsigned", minimalWASM)); !errors.Is(err, ErrUnsignedModule) {
		t.Fatalf("expected unsigned module error, got %v", err)
	}
}

type countingVerifier struct {
	count int
}

func (v *countingVerifier) Verify(context.Context, SignedModule) error {
	v.count++
	return nil
}

type failingVerifier struct{}

func (failingVerifier) Verify(context.Context, SignedModule) error {
	return errors.New("bad signature")
}
