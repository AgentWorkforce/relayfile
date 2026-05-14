package wasmrun

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
)

const (
	DefaultHardTimeout = 5 * time.Second
	DefaultSoftTimeout = time.Second
	DefaultMemoryLimit = 64 * 1024 * 1024
)

var (
	ErrNoQuickJS       = errors.New("wasmrun: quickjs.wasm is not available")
	ErrNoEntrypoint    = errors.New("wasmrun: module does not export a digest entrypoint")
	ErrUnsignedModule  = errors.New("wasmrun: module signature verification failed")
	ErrHostViolation   = errors.New("wasmrun: host violation")
	ErrInvalidEnvelope = errors.New("wasmrun: invalid signed module envelope")
)

type WarningKind string

const (
	WarningTimeout       WarningKind = "timeout"
	WarningSoftSlow      WarningKind = "slow"
	WarningOOM           WarningKind = "oom"
	WarningHostViolation WarningKind = "host_violation"
	WarningPanic         WarningKind = "panic"
)

type Warning struct {
	Kind     WarningKind `json:"kind"`
	Function string      `json:"function"`
	Detail   string      `json:"detail,omitempty"`
}

type Options struct {
	CacheDir        string
	MaxCacheEntries int
	Verifier        Verifier
	EventsProvider  ChangeEventsProvider
	Runner          ModuleRunner
	Logger          func(level, msg string)
	HardTimeout     time.Duration
	SoftTimeout     time.Duration
	MemoryLimit     uint64
}

type Engine struct {
	runtime wazero.Runtime
	opts    Options

	mu       sync.Mutex
	compiled map[string]wazero.CompiledModule
	closed   bool
}

type CompiledModule struct {
	Manifest Manifest

	wasm     []byte
	compiled wazero.CompiledModule
	runner   ModuleRunner
}

type DigestContext struct {
	WorkspaceID string
	FunctionID  string
	WindowFrom  time.Time
	WindowTo    time.Time
	PathScope   []string
}

type DigestSection struct {
	Provider string         `json:"provider"`
	Bullets  []DigestBullet `json:"bullets,omitempty"`
}

type DigestBullet struct {
	Text          string `json:"text"`
	CanonicalPath string `json:"canonicalPath,omitempty"`
}

type ChangeEventsFilter struct {
	Paths []string `json:"paths,omitempty"`
}

type ChangeEvent struct {
	ID       string                 `json:"id"`
	Path     string                 `json:"path"`
	Resource ChangeEventResource    `json:"resource"`
	Summary  map[string]interface{} `json:"summary,omitempty"`
}

type ChangeEventResource struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Type string `json:"type,omitempty"`
}

type ChangeEventsProvider interface {
	ChangeEvents(ctx context.Context, workspaceID string, filter ChangeEventsFilter) ([]ChangeEvent, error)
}

type ModuleRunner interface {
	Run(ctx context.Context, req Invocation) (*DigestSection, error)
}

type ModuleRunnerFunc func(ctx context.Context, req Invocation) (*DigestSection, error)

func (f ModuleRunnerFunc) Run(ctx context.Context, req Invocation) (*DigestSection, error) {
	return f(ctx, req)
}

type Invocation struct {
	Module        CompiledModule
	DigestContext DigestContext
	ChangeEvents  func(context.Context, ChangeEventsFilter) ([]ChangeEvent, error)
	NowMillis     func() int64
	Log           func(level, message string)
	Random        func() float64
	NextUUIDSeq   func() uint64
	Determinism   *DeterministicSource
}

func NewEngine(ctx context.Context, opts Options) (*Engine, error) {
	if opts.Verifier == nil {
		opts.Verifier = NoopVerifier{}
	}
	if opts.Logger == nil {
		opts.Logger = func(string, string) {}
	}
	if opts.HardTimeout <= 0 {
		opts.HardTimeout = DefaultHardTimeout
	}
	if opts.SoftTimeout <= 0 {
		opts.SoftTimeout = DefaultSoftTimeout
	}
	if opts.MemoryLimit == 0 {
		opts.MemoryLimit = DefaultMemoryLimit
	}

	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithMemoryLimitPages(uint32((opts.MemoryLimit+65535)/65536)))

	e := &Engine{
		runtime:  runtime,
		opts:     opts,
		compiled: make(map[string]wazero.CompiledModule),
	}
	if err := e.instantiateHost(ctx); err != nil {
		_ = runtime.Close(ctx)
		return nil, err
	}
	return e, nil
}

func (e *Engine) Compile(ctx context.Context, env SignedModule) (CompiledModule, error) {
	if err := e.ensureOpen(); err != nil {
		return CompiledModule{}, err
	}
	if err := env.Validate(); err != nil {
		return CompiledModule{}, err
	}
	if err := e.opts.Verifier.Verify(ctx, env); err != nil {
		return CompiledModule{}, fmt.Errorf("%w: %v", ErrUnsignedModule, err)
	}

	hash := env.Manifest.ContentHash
	e.mu.Lock()
	if compiled, ok := e.compiled[hash]; ok {
		e.mu.Unlock()
		return CompiledModule{Manifest: env.Manifest, wasm: append([]byte(nil), env.Wasm...), compiled: compiled, runner: e.opts.Runner}, nil
	}
	e.mu.Unlock()

	compiled, err := e.runtime.CompileModule(ctx, env.Wasm)
	if err != nil {
		return CompiledModule{}, err
	}

	e.mu.Lock()
	if existing, ok := e.compiled[hash]; ok {
		compiled.Close(ctx)
		compiled = existing
	} else {
		e.compiled[hash] = compiled
	}
	e.mu.Unlock()

	return CompiledModule{Manifest: env.Manifest, wasm: append([]byte(nil), env.Wasm...), compiled: compiled, runner: e.opts.Runner}, nil
}

func (e *Engine) Invoke(ctx context.Context, mod CompiledModule, dctx DigestContext) (section *DigestSection, warnings []Warning, err error) {
	if err := e.ensureOpen(); err != nil {
		return nil, nil, err
	}
	if dctx.FunctionID == "" {
		dctx.FunctionID = mod.Manifest.FunctionID
	}
	if dctx.FunctionID == "" {
		dctx.FunctionID = mod.Manifest.Name
	}

	invokeCtx, cancel := context.WithTimeout(ctx, e.opts.HardTimeout)
	defer cancel()
	start := time.Now()

	defer func() {
		if recovered := recover(); recovered != nil {
			section = nil
			warnings = append(warnings, Warning{Kind: WarningPanic, Function: dctx.FunctionID, Detail: fmt.Sprint(recovered)})
			err = fmt.Errorf("wasmrun: panic invoking %s: %v", dctx.FunctionID, recovered)
		}
		if errors.Is(invokeCtx.Err(), context.DeadlineExceeded) {
			section = nil
			warnings = appendWarning(warnings, Warning{Kind: WarningTimeout, Function: dctx.FunctionID})
			err = context.DeadlineExceeded
		}
		if e.opts.SoftTimeout > 0 && time.Since(start) > e.opts.SoftTimeout && !hasWarning(warnings, WarningTimeout) {
			warnings = appendWarning(warnings, Warning{Kind: WarningSoftSlow, Function: dctx.FunctionID})
		}
	}()

	source := NewDeterministicSource(dctx.WorkspaceID, dctx.WindowFrom.UnixMilli())
	req := Invocation{
		Module:        mod,
		DigestContext: dctx,
		ChangeEvents: func(ctx context.Context, filter ChangeEventsFilter) ([]ChangeEvent, error) {
			return e.scopedChangeEvents(ctx, dctx, filter)
		},
		NowMillis: func() int64 {
			return dctx.WindowTo.UnixMilli()
		},
		Log: func(level, message string) {
			e.log(dctx.FunctionID, level, message)
		},
		Random:      source.Float64,
		NextUUIDSeq: source.NextUUIDSeq,
		Determinism: source,
	}

	runner := mod.runner
	if runner == nil {
		runner = wazeroRunner{runtime: e.runtime}
	}
	section, err = runner.Run(invokeCtx, req)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded), errors.Is(invokeCtx.Err(), context.DeadlineExceeded):
			warnings = appendWarning(warnings, Warning{Kind: WarningTimeout, Function: dctx.FunctionID})
		case errors.Is(err, ErrHostViolation):
			warnings = appendWarning(warnings, Warning{Kind: WarningHostViolation, Function: dctx.FunctionID, Detail: err.Error()})
		case isOOM(err):
			warnings = appendWarning(warnings, Warning{Kind: WarningOOM, Function: dctx.FunctionID, Detail: err.Error()})
		}
		return nil, warnings, err
	}
	return section, warnings, nil
}

func (e *Engine) Close(ctx context.Context) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	compiled := e.compiled
	e.compiled = nil
	e.mu.Unlock()

	var closeErr error
	for _, mod := range compiled {
		if err := mod.Close(ctx); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	if err := e.runtime.Close(ctx); err != nil && closeErr == nil {
		closeErr = err
	}
	return closeErr
}

func (e *Engine) ensureOpen() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return errors.New("wasmrun: engine closed")
	}
	return nil
}

func (e *Engine) log(functionID, level, message string) {
	if e.opts.Logger != nil {
		e.opts.Logger(level, functionID+": "+message)
	}
}

func appendWarning(warnings []Warning, warning Warning) []Warning {
	for _, existing := range warnings {
		if existing.Kind == warning.Kind && existing.Function == warning.Function {
			return warnings
		}
	}
	return append(warnings, warning)
}

func hasWarning(warnings []Warning, kind WarningKind) bool {
	for _, warning := range warnings {
		if warning.Kind == kind {
			return true
		}
	}
	return false
}

func isOOM(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "out of memory") || strings.Contains(msg, "oom") || strings.Contains(msg, "memory")
}
