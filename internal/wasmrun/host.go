package wasmrun

import (
	"context"
	"encoding/json"
	"math"
	"path"
	"sort"
	"strings"

	"github.com/tetratelabs/wazero/api"
)

const (
	hostModuleName  = "relayfile"
	maxLogBytes     = 64 * 1024
	maxFilterBytes  = 64 * 1024
	allocExportName = "alloc"
)

type invocationContextKey struct{}

func currentInvocation(ctx context.Context) (*invocationState, bool) {
	state, ok := ctx.Value(invocationContextKey{}).(*invocationState)
	return state, ok
}

func (e *Engine) instantiateHost(ctx context.Context) error {
	builder := e.runtime.NewHostModuleBuilder(hostModuleName)

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(e.hostNowMs),
			[]api.ValueType{}, []api.ValueType{api.ValueTypeI64}).
		Export("host_now_ms")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(e.hostLog),
			[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{}).
		Export("host_log")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(e.hostChangeEvents),
			[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI64}).
		Export("host_change_events")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(e.hostRandom),
			[]api.ValueType{}, []api.ValueType{api.ValueTypeF64}).
		Export("host_random")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(e.hostUUIDSeq),
			[]api.ValueType{}, []api.ValueType{api.ValueTypeI64}).
		Export("host_uuid_seq")

	_, err := builder.Instantiate(ctx)
	return err
}

func (e *Engine) hostNowMs(ctx context.Context, _ api.Module, stack []uint64) {
	var ms int64
	if state, ok := currentInvocation(ctx); ok && state.invocation.NowMillis != nil {
		ms = state.invocation.NowMillis()
	}
	stack[0] = uint64(ms)
}

func (e *Engine) hostLog(ctx context.Context, mod api.Module, stack []uint64) {
	levelPtr := api.DecodeU32(stack[0])
	levelLen := api.DecodeU32(stack[1])
	msgPtr := api.DecodeU32(stack[2])
	msgLen := api.DecodeU32(stack[3])
	if levelLen > maxLogBytes || msgLen > maxLogBytes {
		return
	}
	mem := mod.Memory()
	if mem == nil {
		return
	}
	levelBytes, ok := mem.Read(levelPtr, levelLen)
	if !ok {
		return
	}
	msgBytes, ok := mem.Read(msgPtr, msgLen)
	if !ok {
		return
	}
	level := strings.ToLower(strings.TrimSpace(string(levelBytes)))
	if level == "" {
		level = "info"
	}
	message := string(msgBytes)
	state, _ := currentInvocation(ctx)
	if state != nil && state.invocation.Log != nil {
		state.invocation.Log(level, message)
	} else if e.opts.Logger != nil {
		e.opts.Logger(level, message)
	}
}

func (e *Engine) hostChangeEvents(ctx context.Context, mod api.Module, stack []uint64) {
	ptr := api.DecodeU32(stack[0])
	length := api.DecodeU32(stack[1])
	stack[0] = 0
	if length > maxFilterBytes {
		return
	}
	mem := mod.Memory()
	if mem == nil {
		return
	}
	var filter ChangeEventsFilter
	if length > 0 {
		raw, ok := mem.Read(ptr, length)
		if !ok {
			return
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &filter); err != nil {
				return
			}
		}
	}
	state, ok := currentInvocation(ctx)
	if !ok || state.invocation.ChangeEvents == nil {
		return
	}
	events, err := state.invocation.ChangeEvents(ctx, filter)
	if err != nil {
		return
	}
	payload, err := json.Marshal(events)
	if err != nil {
		return
	}
	written, ok := writeIntoGuest(ctx, mod, payload)
	if !ok {
		return
	}
	stack[0] = packPointer(written, uint32(len(payload)))
}

func (e *Engine) hostRandom(ctx context.Context, _ api.Module, stack []uint64) {
	v := 0.0
	if state, ok := currentInvocation(ctx); ok && state.invocation.Random != nil {
		v = state.invocation.Random()
	}
	stack[0] = math.Float64bits(v)
}

func (e *Engine) hostUUIDSeq(ctx context.Context, _ api.Module, stack []uint64) {
	var seq uint64
	if state, ok := currentInvocation(ctx); ok && state.invocation.NextUUIDSeq != nil {
		seq = state.invocation.NextUUIDSeq()
	}
	stack[0] = seq
}

func writeIntoGuest(ctx context.Context, mod api.Module, payload []byte) (uint32, bool) {
	if len(payload) == 0 {
		return 0, true
	}
	alloc := mod.ExportedFunction(allocExportName)
	if alloc == nil {
		return 0, false
	}
	results, err := alloc.Call(ctx, uint64(len(payload)))
	if err != nil || len(results) == 0 {
		return 0, false
	}
	dest := api.DecodeU32(results[0])
	if dest == 0 {
		return 0, false
	}
	mem := mod.Memory()
	if mem == nil {
		return 0, false
	}
	if !mem.Write(dest, payload) {
		return 0, false
	}
	return dest, true
}

func (e *Engine) scopedChangeEvents(ctx context.Context, dctx DigestContext, filter ChangeEventsFilter) ([]ChangeEvent, error) {
	if e.opts.EventsProvider == nil {
		return nil, nil
	}
	events, err := e.opts.EventsProvider.ChangeEvents(ctx, dctx.WorkspaceID, filter)
	if err != nil {
		return nil, err
	}

	scope := normalizePatterns(dctx.PathScope)
	requested := normalizePatterns(filter.Paths)
	filtered := events[:0]
	for _, event := range events {
		eventPath := event.Path
		if eventPath == "" {
			eventPath = event.Resource.Path
		}
		if matchesAny(scope, eventPath) && matchesAny(requested, eventPath) {
			filtered = append(filtered, event)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].ID == filtered[j].ID {
			return filtered[i].Path < filtered[j].Path
		}
		return filtered[i].ID < filtered[j].ID
	})
	return append([]ChangeEvent(nil), filtered...), nil
}

func normalizePatterns(patterns []string) []string {
	if len(patterns) == 0 {
		return []string{"*"}
	}
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		out = append(out, pattern)
	}
	if len(out) == 0 {
		return []string{"*"}
	}
	return out
}

func matchesAny(patterns []string, value string) bool {
	for _, pattern := range patterns {
		if matchPattern(pattern, value) {
			return true
		}
	}
	return false
}

func matchPattern(pattern, value string) bool {
	if pattern == "*" {
		return true
	}
	if ok, _ := path.Match(pattern, value); ok {
		return true
	}
	if strings.HasSuffix(pattern, "/*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(value, prefix)
	}
	return pattern == value
}

