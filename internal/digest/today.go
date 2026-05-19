package digest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TodayCover is the frontmatter `covers:` value emitted for the rolling
// current-day digest.
const TodayCover = "today"

// BuildOptions tunes how BuildToday assembles the rolling current-day digest.
// MaxEvents = 0 means exhaustive (no cap).
type BuildOptions struct {
	MaxEvents int
}

// TodayWindow returns the [start, end) instants that bound the local
// calendar day containing now in tz. start is inclusive (00:00 local), end
// is exclusive (00:00 local of the next day). A nil tz is treated as UTC.
func TodayWindow(now time.Time, tz *time.Location) (start, end time.Time) {
	if tz == nil {
		tz = time.UTC
	}
	local := now.In(tz)
	start = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, tz)
	end = time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, tz)
	return
}

// FilterEventsInToday returns the subset of events whose Timestamp, taken in
// tz, falls in [windowStart, windowEnd). Order is preserved.
func FilterEventsInToday(events []ChangeEvent, windowStart, windowEnd time.Time, tz *time.Location) []ChangeEvent {
	if tz == nil {
		tz = time.UTC
	}
	start := windowStart.In(tz)
	end := windowEnd.In(tz)
	out := make([]ChangeEvent, 0, len(events))
	for _, ev := range events {
		ts := ev.Timestamp.In(tz)
		if ts.Before(start) {
			continue
		}
		if !ts.Before(end) {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// todayFilteredSource narrows an upstream source to today's [start, end)
// window before handing it to Run. It also enforces BuildOptions.MaxEvents
// by truncating to the first N in-window events (after the boundary filter
// but before sort) and recording a `truncated:` warning the renderer will
// surface in frontmatter.
type todayFilteredSource struct {
	inner    ChangeEventSource
	opts     BuildOptions
	tz       *time.Location
	warnings *[]string
}

func (s todayFilteredSource) Events(w Window) ([]ChangeEvent, error) {
	raw, err := s.inner.Events(w)
	if err != nil {
		return nil, err
	}
	tz := s.tz
	if tz == nil {
		tz = w.TZ
	}
	if tz == nil {
		tz = time.UTC
	}
	start, end := TodayWindow(w.Date, tz)
	in := FilterEventsInToday(raw, start, end, tz)
	if s.opts.MaxEvents > 0 && len(in) > s.opts.MaxEvents {
		dropped := len(in) - s.opts.MaxEvents
		in = in[:s.opts.MaxEvents]
		if s.warnings != nil {
			*s.warnings = append(*s.warnings, fmt.Sprintf("truncated: %d events exceeded MaxEvents=%d", dropped, s.opts.MaxEvents))
		}
	}
	return in, nil
}

// BuildToday computes the rolling current-day Report. The window is
// [local_midnight(now, tz), next_local_midnight(now, tz)). A nil tz falls
// back to UTC and adds a `tz-fallback` warning to the report.
func BuildToday(ctx context.Context, src ChangeEventSource, providers []string, now time.Time, tz *time.Location, opts BuildOptions) (Report, error) {
	if src == nil {
		return Report{}, errors.New("digest: nil ChangeEventSource")
	}
	var warnings []string
	if tz == nil {
		tz = time.UTC
		warnings = append(warnings, "tz-fallback: workspace timezone unset; using UTC")
	}
	start, _ := TodayWindow(now, tz)
	w := Window{
		Date:        start,
		Cover:       TodayCover,
		GeneratedAt: now,
		Providers:   append([]string(nil), providers...),
		TZ:          tz,
	}
	rep, err := Run(ctx, todayFilteredSource{inner: src, opts: opts, tz: tz, warnings: &warnings}, w)
	if err != nil {
		return Report{}, err
	}
	if len(warnings) > 0 {
		rep.Meta.Warnings = append(rep.Meta.Warnings, warnings...)
	}
	return rep, nil
}

// RenderToday assembles the rolling Report for now and renders it to bytes.
// Returns the canonical path, body, and the underlying Report.
func RenderToday(ctx context.Context, src ChangeEventSource, providers []string, now time.Time, tz *time.Location, opts BuildOptions) (string, []byte, Report, error) {
	rep, err := BuildToday(ctx, src, providers, now, tz, opts)
	if err != nil {
		return "", nil, Report{}, err
	}
	return TodayPath, Render(rep), rep, nil
}

// TodayWriteResult is returned by WriteToday.
type TodayWriteResult struct {
	Path    string
	Report  Report
	Written bool
}

// WriteToday renders the current-day digest and atomically replaces the
// rolling artifact under mountRoot. The write uses temp+rename so mount
// observers never see a half-written file. Unlike date-stamped digests this
// file is rewritten on every call; the day rollover from today→yesterday
// is the responsibility of the closing-window slice.
func WriteToday(ctx context.Context, mountRoot string, src ChangeEventSource, providers []string, now time.Time, tz *time.Location, opts BuildOptions) (TodayWriteResult, error) {
	path, content, rep, err := RenderToday(ctx, src, providers, now, tz, opts)
	if err != nil {
		return TodayWriteResult{}, err
	}
	if strings.TrimSpace(mountRoot) == "" {
		return TodayWriteResult{}, errors.New("digest: mount root is required")
	}

	localPath := filepath.Join(filepath.Clean(mountRoot), filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return TodayWriteResult{}, err
	}

	tmp, err := os.CreateTemp(filepath.Dir(localPath), ".today-*.md.tmp")
	if err != nil {
		return TodayWriteResult{}, err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return TodayWriteResult{}, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return TodayWriteResult{}, err
	}
	if err := os.Rename(tmpName, localPath); err != nil {
		_ = os.Remove(tmpName)
		return TodayWriteResult{}, err
	}
	return TodayWriteResult{Path: path, Report: rep, Written: true}, nil
}
