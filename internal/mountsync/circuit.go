package mountsync

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrCircuitOpen is returned by guarded operations (writebacks, destructive
// reconcile) when the cloud-error circuit breaker is currently tripped. The
// read-only mirror sync path treats this as a soft skip and continues; the
// writeback path treats it as a queue-and-retry condition.
var ErrCircuitOpen = errors.New("cloud-error circuit breaker is open")

// CircuitState reports whether the breaker is currently open and, if so,
// when it will permit retries again. Surfaced via the public status JSON.
type CircuitState struct {
	Open       bool      `json:"open"`
	OpenedAt   time.Time `json:"openedAt,omitempty"`
	OpenEvents uint64    `json:"openEvents,omitempty"`
	Failures   int       `json:"failures,omitempty"`
	WindowMs   int64     `json:"windowMs,omitempty"`
	CooldownMs int64     `json:"cooldownMs,omitempty"`
	Threshold  int       `json:"threshold,omitempty"`
	NextRetry  time.Time `json:"nextRetry,omitempty"`
}

// CloudErrorCircuit tracks 5xx / reset responses in a sliding window and
// trips when the count crosses a threshold. While open it refuses
// destructive operations and writebacks for a configurable cooldown.
//
// Configuration:
//   - RELAYFILE_CB_WINDOW (duration, default 60s): sliding window.
//   - RELAYFILE_CB_THRESHOLD (int, default 5): failures within the window
//     that trip the breaker.
//   - RELAYFILE_CB_COOLDOWN (duration, default 30s): time the breaker
//     stays open after the latest tripping failure.
type CloudErrorCircuit struct {
	mu         sync.Mutex
	window     time.Duration
	threshold  int
	cooldown   time.Duration
	failures   []time.Time
	open       bool
	openedAt   time.Time
	nextRetry  time.Time
	openEvents uint64
	now        func() time.Time
}

// NewCloudErrorCircuit returns a breaker configured from env (with the
// stated defaults). Tests can override the clock via SetClock.
func NewCloudErrorCircuit() *CloudErrorCircuit {
	c := &CloudErrorCircuit{
		window:    durationEnv("RELAYFILE_CB_WINDOW", 60*time.Second),
		threshold: intEnv("RELAYFILE_CB_THRESHOLD", 5),
		cooldown:  durationEnv("RELAYFILE_CB_COOLDOWN", 30*time.Second),
		now:       time.Now,
	}
	if c.window <= 0 {
		c.window = 60 * time.Second
	}
	if c.threshold <= 0 {
		c.threshold = 5
	}
	if c.cooldown <= 0 {
		c.cooldown = 30 * time.Second
	}
	return c
}

// SetClock overrides the time source. Tests-only.
func (c *CloudErrorCircuit) SetClock(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// RecordFailure registers a 5xx / reset / network failure. Returns true if
// the breaker transitioned from closed to open as a result of this call.
func (c *CloudErrorCircuit) RecordFailure() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	// Evict failures outside the window.
	cutoff := now.Add(-c.window)
	pruned := c.failures[:0]
	for _, t := range c.failures {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	c.failures = append(pruned, now)
	tripped := false
	if len(c.failures) >= c.threshold && !c.open {
		c.open = true
		c.openedAt = now
		c.openEvents++
		tripped = true
	}
	if c.open {
		c.nextRetry = now.Add(c.cooldown)
	}
	return tripped
}

// RecordSuccess clears the breaker if cooldown has elapsed. A single
// success during cooldown does NOT close it — callers must wait through
// the cooldown to avoid flapping.
func (c *CloudErrorCircuit) RecordSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.open {
		c.failures = c.failures[:0]
		return
	}
	if c.now().After(c.nextRetry) {
		c.open = false
		c.failures = c.failures[:0]
		c.openedAt = time.Time{}
		c.nextRetry = time.Time{}
	}
}

// IsOpen reports the current breaker state. If the cooldown has elapsed,
// IsOpen transitions the breaker to half-open and returns false; the next
// success or failure will then decide whether to keep it closed.
func (c *CloudErrorCircuit) IsOpen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.open {
		return false
	}
	if c.now().After(c.nextRetry) {
		// Allow a probe; remain logically open until the next outcome.
		// Treat as closed for callers so they can attempt the operation,
		// but keep openEvents accumulating from this trip.
		c.open = false
		c.openedAt = time.Time{}
		c.nextRetry = time.Time{}
		c.failures = c.failures[:0]
		return false
	}
	return true
}

// Snapshot returns a copy of the current state for status reporting.
func (c *CloudErrorCircuit) Snapshot() CircuitState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CircuitState{
		Open:       c.open,
		OpenedAt:   c.openedAt,
		OpenEvents: c.openEvents,
		Failures:   len(c.failures),
		WindowMs:   c.window.Milliseconds(),
		CooldownMs: c.cooldown.Milliseconds(),
		Threshold:  c.threshold,
		NextRetry:  c.nextRetry,
	}
}

// IsCloudFailureStatus reports whether an HTTP status code should count
// toward the breaker. 5xx and gateway-style timeouts (408, 425, 429) count;
// 4xx other than rate-limit do not.
func IsCloudFailureStatus(code int) bool {
	if code >= 500 && code <= 599 {
		return true
	}
	switch code {
	case 408, 425, 429:
		return true
	}
	return false
}

// IsCloudFailureError reports whether a generic error is a transport-level
// failure that should count toward the breaker (network reset, EOF,
// connection refused, etc.).
func IsCloudFailureError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "eof") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "tls handshake timeout") {
		return true
	}
	return false
}

func durationEnv(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

func intEnv(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return def
	}
	return v
}
