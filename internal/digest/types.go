package digest

import "time"

// ChangeEvent is a single workspace activity event from a provider mount.
// Tags are aligned with the TypeScript DigestContext contract so that
// adapter-RPC marshalling round-trips without per-field translation.
type ChangeEvent struct {
	Provider      string    `json:"provider"`
	Timestamp     time.Time `json:"timestamp"`
	Identifier    string    `json:"identifier"`
	Verb          string    `json:"verb"`
	CanonicalPath string    `json:"canonicalPath"`
}

// DigestBullet is one rendered line under a provider section.
type DigestBullet struct {
	Identifier    string `json:"identifier"`
	Verb          string `json:"verb"`
	CanonicalPath string `json:"canonicalPath"`
}

// DigestSection groups bullets by provider.
type DigestSection struct {
	Provider string         `json:"provider"`
	Bullets  []DigestBullet `json:"bullets"`
}

// Window describes the digest run parameters.
type Window struct {
	Date        time.Time      // workspace-local calendar date
	Cover       string         // e.g. "yesterday"
	GeneratedAt time.Time      // UTC; used for `generated_at`
	Providers   []string       // connected providers (incl. zero-activity ones)
	TZ          *time.Location // workspace timezone; used for the `date` field
}

// Meta is the frontmatter bundle attached to a rendered digest.
type Meta struct {
	Date        string   `json:"date"`
	GeneratedAt string   `json:"generated_at"`
	Covers      string   `json:"covers"`
	Providers   []string `json:"providers"`
	Events      int      `json:"events"`
	Warnings    []string `json:"warnings,omitempty"`
}

// Report is the result of a digest Run.
type Report struct {
	Meta     Meta
	Sections []DigestSection
}

// ChangeEventSource supplies the events to coalesce for a window. Tests
// inject a slice-backed source; production wiring lives in the mount sync hook.
type ChangeEventSource interface {
	Events(window Window) ([]ChangeEvent, error)
}

// SliceSource is a trivial ChangeEventSource backed by an in-memory slice.
type SliceSource struct {
	Items []ChangeEvent
}

func (s SliceSource) Events(_ Window) ([]ChangeEvent, error) {
	return s.Items, nil
}
