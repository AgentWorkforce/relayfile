package wasmrun

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

type Manifest struct {
	FunctionID  string `json:"functionId"`
	Name        string `json:"name"`
	ContentHash string `json:"contentHash"`
	EntryPoint  string `json:"entryPoint,omitempty"`
	SDKVersion  string `json:"sdkVersion,omitempty"`
}

type SignedModule struct {
	Manifest  Manifest `json:"manifest"`
	Wasm      []byte   `json:"wasm"`
	Signature []byte   `json:"signature,omitempty"`
}

type Verifier interface {
	Verify(ctx context.Context, module SignedModule) error
}

type NoopVerifier struct{}

func (NoopVerifier) Verify(context.Context, SignedModule) error {
	return nil
}

func ParseSignedModule(data []byte) (SignedModule, error) {
	var raw struct {
		Manifest  Manifest `json:"manifest"`
		Wasm      string   `json:"wasm"`
		Signature string   `json:"signature,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return SignedModule{}, fmt.Errorf("%w: %v", ErrInvalidEnvelope, err)
	}
	wasm, err := base64.StdEncoding.DecodeString(raw.Wasm)
	if err != nil {
		return SignedModule{}, fmt.Errorf("%w: invalid wasm payload: %v", ErrInvalidEnvelope, err)
	}
	var sig []byte
	if raw.Signature != "" {
		sig, err = base64.StdEncoding.DecodeString(raw.Signature)
		if err != nil {
			return SignedModule{}, fmt.Errorf("%w: invalid signature: %v", ErrInvalidEnvelope, err)
		}
	}
	env := SignedModule{Manifest: raw.Manifest, Wasm: wasm, Signature: sig}
	return env, env.Validate()
}

func (m SignedModule) MarshalJSON() ([]byte, error) {
	type wire struct {
		Manifest  Manifest `json:"manifest"`
		Wasm      string   `json:"wasm"`
		Signature string   `json:"signature,omitempty"`
	}
	out := wire{
		Manifest:  m.Manifest,
		Wasm:      base64.StdEncoding.EncodeToString(m.Wasm),
		Signature: base64.StdEncoding.EncodeToString(m.Signature),
	}
	if len(m.Signature) == 0 {
		out.Signature = ""
	}
	return json.Marshal(out)
}

func (m SignedModule) Validate() error {
	if m.Manifest.FunctionID == "" && m.Manifest.Name == "" {
		return fmt.Errorf("%w: missing function identity", ErrInvalidEnvelope)
	}
	if err := validateContentHash(m.Manifest.ContentHash); err != nil {
		return err
	}
	if len(m.Wasm) == 0 {
		return fmt.Errorf("%w: missing wasm payload", ErrInvalidEnvelope)
	}
	return nil
}

func validateContentHash(contentHash string) error {
	contentHash = strings.TrimSpace(contentHash)
	if contentHash == "" {
		return fmt.Errorf("%w: missing content hash", ErrInvalidEnvelope)
	}
	algo, digest, ok := strings.Cut(contentHash, ":")
	if !ok {
		algo = "sha256"
		digest = contentHash
	}
	if algo == "" {
		return fmt.Errorf("%w: missing content hash algorithm", ErrInvalidEnvelope)
	}
	for _, r := range algo {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		return fmt.Errorf("%w: invalid content hash algorithm", ErrInvalidEnvelope)
	}
	if len(digest) != sha256.Size*2 {
		return fmt.Errorf("%w: invalid content hash digest length", ErrInvalidEnvelope)
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("%w: invalid content hash digest: %v", ErrInvalidEnvelope, err)
	}
	if digest != strings.ToLower(digest) {
		return fmt.Errorf("%w: content hash digest must be lowercase hex", ErrInvalidEnvelope)
	}
	return nil
}

func ContentHash(wasm []byte) string {
	sum := sha256.Sum256(wasm)
	return "sha256:" + hex.EncodeToString(sum[:])
}
