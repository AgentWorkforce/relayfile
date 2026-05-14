package wasmrun

import (
	"encoding/binary"
	"hash/fnv"
	"math/rand"
	"sync"
)

// quickJSDeterminismBootstrap is the JavaScript prologue injected ahead of
// every customer module by the QuickJS-in-WASM runner. It rebinds the
// non-deterministic ambient hosts onto the host-function bridge so a function
// deployed twice over the same fixture produces a byte-identical
// DigestSection.
//
// Required overrides per the spec:
//   - Date.now / performance.now → host_now_ms (pinned to window.to)
//   - Math.random → host_random (seeded by workspace + window.from)
//   - crypto.randomUUID → host_uuid_seq (counter scoped to invocation)
//   - fetch / XMLHttpRequest / WebSocket → unconditionally deleted
const quickJSDeterminismBootstrap = `
(function () {
  "use strict";
  const env = (globalThis.relayfile && globalThis.relayfile.host) || globalThis;
  const hostNow = env.host_now_ms || (() => 0);
  const hostRand = env.host_random || (() => 0);
  const hostUUID = env.host_uuid_seq || (() => 0);
  Date.now = () => Number(hostNow());
  if (!globalThis.performance) globalThis.performance = {};
  globalThis.performance.now = () => Number(hostNow());
  Math.random = () => {
    const v = hostRand();
    return typeof v === "number" ? v : 0;
  };
  globalThis.crypto = globalThis.crypto || {};
  globalThis.crypto.randomUUID = () => {
    const n = Number(hostUUID());
    let suffix = n.toString(16);
    while (suffix.length < 12) suffix = "0" + suffix;
    return "00000000-0000-4000-8000-" + suffix;
  };
  delete globalThis.fetch;
  delete globalThis.XMLHttpRequest;
  delete globalThis.WebSocket;
})();
`

// DeterminismBootstrapSource returns the JS prologue installed before a
// customer module is parsed by the QuickJS-in-WASM runner.
func DeterminismBootstrapSource() string {
	return quickJSDeterminismBootstrap
}

// DeterministicSource is the seeded entropy source backing host_random and
// host_uuid_seq for a single invocation. The seed is derived from
// (workspaceID, windowFromMillis) so two invocations over the same window
// produce identical randomness sequences.
type DeterministicSource struct {
	mu          sync.Mutex
	random      *rand.Rand
	uuidCounter uint64
}

func NewDeterministicSource(workspaceID string, windowFromMillis int64) *DeterministicSource {
	h := fnv.New64a()
	_, _ = h.Write([]byte(workspaceID))
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(windowFromMillis))
	_, _ = h.Write(buf[:])
	return &DeterministicSource{random: rand.New(rand.NewSource(int64(h.Sum64())))}
}

func (s *DeterministicSource) Float64() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.random.Float64()
}

func (s *DeterministicSource) UUID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uuidCounter++
	return deterministicUUID(s.uuidCounter)
}

func (s *DeterministicSource) NextUUIDSeq() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uuidCounter++
	return s.uuidCounter
}

func deterministicUUID(counter uint64) string {
	const prefix = "00000000-0000-4000-8000-"
	var suffix [12]byte
	n := counter
	for i := len(suffix) - 1; i >= 0; i-- {
		suffix[i] = "0123456789abcdef"[n&0xf]
		n >>= 4
	}
	return prefix + string(suffix[:])
}
