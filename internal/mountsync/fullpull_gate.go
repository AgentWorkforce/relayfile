package mountsync

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
)

// defaultFullPullReadConcurrency caps in-flight full-tree remote reads (tree
// pages, bulk reads, bootstrap point reads, export snapshots, GitHub clone
// manifests and tar seed streams) across EVERY
// Syncer in this process. A scoped mount runs one Syncer per remote path, and
// each Syncer already bounds itself to defaultBootstrapReadWorkers; without a
// process-wide gate, N scopes bootstrapping together multiplied that to 4N
// concurrent reads against the same single-threaded workspace Durable Object.
// Four keeps a single-scope mount at its historical throughput.
const (
	defaultFullPullReadConcurrency = 4
	maxFullPullReadConcurrency     = 16
)

// fullPullReadGate is process-wide on purpose: the resource it protects is the
// remote workspace, not one Syncer's state.
var fullPullReadGate = newReadGate(fullPullReadConcurrency())

type readGate struct {
	slots chan struct{}
}

func newReadGate(limit int) *readGate {
	if limit <= 0 {
		limit = defaultFullPullReadConcurrency
	}
	return &readGate{slots: make(chan struct{}, limit)}
}

// do runs fn while holding one slot. Waiting for a slot honours ctx so a
// cancelled or timed-out cycle never blocks behind siblings; the caller sees
// ctx.Err() exactly as if the request itself had been cancelled, which the
// bootstrap runner already treats as a resumable yield.
func (g *readGate) do(ctx context.Context, fn func() error) error {
	release, err := g.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// acquire takes one slot for a read whose lifetime outlives a single call — a
// streamed response that is consumed after the request returns. The caller
// must call release exactly once, when the stream is closed.
func (g *readGate) acquire(ctx context.Context) (release func(), err error) {
	select {
	case g.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() { once.Do(func() { <-g.slots }) }, nil
}

func fullPullReadConcurrency() int {
	raw := strings.TrimSpace(os.Getenv("RELAYFILE_FULL_PULL_READ_CONCURRENCY"))
	if raw == "" {
		return defaultFullPullReadConcurrency
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return defaultFullPullReadConcurrency
	}
	if v > maxFullPullReadConcurrency {
		return maxFullPullReadConcurrency
	}
	return v
}
