// Package digest builds the deterministic activity-summary file written
// to `digests/<cover>.md` after every workspace change-event window.
//
// The package is intentionally I/O free: callers supply a Window (date,
// timezone, connected providers, generated-at timestamp) and a
// ChangeEventSource. The package coalesces events, sorts them
// deterministically, and renders Markdown with YAML frontmatter.
package digest

import (
	"context"
	"errors"
	"sort"
	"time"
)

// Run coalesces the supplied window's events into a Report. The report is
// deterministic: the same Window + Events input always yields the same
// Report, byte-equal after Render.
func Run(ctx context.Context, src ChangeEventSource, w Window) (Report, error) {
	if src == nil {
		return Report{}, errors.New("digest: nil ChangeEventSource")
	}
	if w.TZ == nil {
		w.TZ = time.UTC
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}

	events, err := src.Events(w)
	if err != nil {
		return Report{}, err
	}

	providers := append([]string(nil), w.Providers...)
	// Include any event provider not present in w.Providers so its bullets
	// still render and the events count stays consistent with what's shown.
	for _, ev := range events {
		if ev.Provider != "" {
			providers = append(providers, ev.Provider)
		}
	}
	sort.Strings(providers)
	providers = dedupe(providers)

	bucket := make(map[string][]ChangeEvent, len(providers))
	for _, p := range providers {
		bucket[p] = nil
	}
	for _, ev := range events {
		bucket[ev.Provider] = append(bucket[ev.Provider], ev)
	}

	sections := make([]DigestSection, 0, len(providers))
	for _, p := range providers {
		evs := bucket[p]
		sort.SliceStable(evs, func(i, j int) bool {
			a, b := evs[i], evs[j]
			if !a.Timestamp.Equal(b.Timestamp) {
				return a.Timestamp.Before(b.Timestamp)
			}
			if a.Identifier != b.Identifier {
				return a.Identifier < b.Identifier
			}
			if a.Verb != b.Verb {
				return a.Verb < b.Verb
			}
			return a.CanonicalPath < b.CanonicalPath
		})
		bullets := make([]DigestBullet, 0, len(evs))
		for _, e := range evs {
			bullets = append(bullets, DigestBullet{
				Identifier:    e.Identifier,
				Verb:          e.Verb,
				CanonicalPath: e.CanonicalPath,
			})
		}
		sections = append(sections, DigestSection{Provider: p, Bullets: bullets})
	}

	rep := Report{
		Meta: Meta{
			Date:        w.Date.In(w.TZ).Format("2006-01-02"),
			GeneratedAt: w.GeneratedAt.UTC().Format(time.RFC3339),
			Covers:      w.Cover,
			Providers:   providers,
			Events:      len(events),
		},
		Sections: sections,
	}
	return rep, nil
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := in[:0]
	prev := ""
	for i, v := range in {
		if i == 0 || v != prev {
			out = append(out, v)
		}
		prev = v
	}
	return out
}
