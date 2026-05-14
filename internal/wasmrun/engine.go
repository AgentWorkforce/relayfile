package wasmrun

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const maxGuestPayloadBytes = 1 << 20

type wazeroRunner struct {
	runtime wazero.Runtime
}

func (r wazeroRunner) Run(ctx context.Context, req Invocation) (*DigestSection, error) {
	if req.Module.compiled == nil {
		return nil, ErrNoEntrypoint
	}
	instance, err := r.runtime.InstantiateModule(ctx, req.Module.compiled, wazero.NewModuleConfig().WithName(""))
	if err != nil {
		return nil, err
	}
	defer instance.Close(ctx)

	fn := instance.ExportedFunction(req.Module.Manifest.EntryPoint)
	if fn == nil && req.Module.Manifest.EntryPoint != "digest" {
		fn = instance.ExportedFunction("digest")
	}
	if fn == nil {
		return nil, ErrNoEntrypoint
	}

	callCtx := context.WithValue(ctx, invocationContextKey{}, &invocationState{
		invocation: req,
		module:     instance,
	})
	results, err := fn.Call(callCtx)
	if err != nil {
		return nil, err
	}
	return decodeSectionFromReturn(instance, fn.Definition().ResultTypes(), results)
}

func decodeSectionFromReturn(mod api.Module, resultTypes []api.ValueType, results []uint64) (*DigestSection, error) {
	if len(results) == 0 || len(resultTypes) == 0 {
		return nil, nil
	}
	switch resultTypes[0] {
	case api.ValueTypeI64:
		ptr, length := unpackPointer(results[0])
		if ptr == 0 || length == 0 {
			return nil, nil
		}
		if length > maxGuestPayloadBytes {
			return nil, fmt.Errorf("wasmrun: digest payload too large: %d bytes", length)
		}
		mem := mod.Memory()
		if mem == nil {
			return nil, fmt.Errorf("wasmrun: guest module exposes no memory")
		}
		data, ok := mem.Read(ptr, length)
		if !ok {
			return nil, fmt.Errorf("wasmrun: invalid digest pointer ptr=%d len=%d", ptr, length)
		}
		var section DigestSection
		if err := json.Unmarshal(data, &section); err != nil {
			return nil, fmt.Errorf("wasmrun: decode digest section: %w", err)
		}
		return &section, nil
	default:
		// Functions whose result is not a packed (ptr|len) pointer (e.g. i32
		// status codes) do not synthesize a digest section. This keeps the
		// default ABI backward-compatible with native wazero modules that have
		// no JSON return contract.
		return nil, nil
	}
}

func unpackPointer(packed uint64) (uint32, uint32) {
	return uint32(packed >> 32), uint32(packed & 0xffffffff)
}

func packPointer(ptr, length uint32) uint64 {
	return (uint64(ptr) << 32) | uint64(length)
}

type invocationState struct {
	invocation Invocation
	module     api.Module
}
