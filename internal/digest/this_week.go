package digest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ThisWeekPath is the rolling weekly digest artifact path. A workspace-local
// week starts Monday 00:00 (ISO-8601) and the file is rewritten on every
// in-window provider event until the week closes.
const ThisWeekPath = "digests/this-week.md"

// ThisWeekCover is the frontmatter `covers:` value emitted for the rolling
// weekly digest.
const ThisWeekCover = "this-week"

// MondayStart returns the local Monday 00:00 boundary that opens the
// workspace-local ISO calendar week containing now. The result is in tz; a
// nil tz is treated as UTC. The boundary is inclusive: events whose local
// timestamp equals the returned instant belong to the current week. The
// closing-window writer in last_week.go calls this through LastWeekStart so
// both the rolling-week and closing-week paths agree on the week boundary.
func MondayStart(now time.Time, tz *time.Location) time.Time {
	if tz == nil {
		tz = time.UTC
	}
	local := now.In(tz)
	// time.Weekday: Sunday=0 ... Saturday=6. Convert to Monday=0 ... Sunday=6
	// so the offset to subtract is always non-negative.
	offset := (int(local.Weekday()) + 6) % 7
	y, m, d := local.Date()
	return time.Date(y, m, d-offset, 0, 0, 0, 0, tz)
}

// ThisWeekWindow builds a Window covering [MondayStart(now), generatedAt) in
// tz. Window.Date is the Monday-start local date so the frontmatter `date:`
// field documents the week start. Window.Cover is the literal "this-week".
func ThisWeekWindow(now, generatedAt time.Time, providers []string, tz *time.Location) Window {
	if tz == nil {
		tz = time.UTC
	}
	monday := MondayStart(now, tz)
	return Window{
		Date:        monday,
		Cover:       ThisWeekCover,
		GeneratedAt: generatedAt,
		Providers:   append([]string(nil), providers...),
		TZ:          tz,
	}
}

// FilterEventsInThisWeek returns the subset of events whose Timestamp, taken
// in tz, falls in [windowStart, windowEnd). It preserves input order so
// downstream sorting remains deterministic. The helper is exported so other
// rolling-window callers (last-week, custom windows) can share the boundary
// semantics rather than re-implementing them.
func FilterEventsInThisWeek(events []ChangeEvent, windowStart, windowEnd time.Time, tz *time.Location) []ChangeEvent {
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

// thisWeekFilteredSource is a ChangeEventSource that narrows an underlying
// source to the [Monday, GeneratedAt) local window. It lets callers pass the
// raw event stream to RenderThisWeek/WriteThisWeek without pre-filtering and
// keeps the boundary logic centralized.
type thisWeekFilteredSource struct {
	inner ChangeEventSource
}

func (s thisWeekFilteredSource) Events(w Window) ([]ChangeEvent, error) {
	raw, err := s.inner.Events(w)
	if err != nil {
		return nil, err
	}
	tz := w.TZ
	if tz == nil {
		tz = time.UTC
	}
	return FilterEventsInThisWeek(raw, w.Date, w.GeneratedAt, tz), nil
}

// RenderThisWeek renders the rolling weekly digest for w. The Cover is forced
// to the canonical "this-week" string so callers cannot accidentally write a
// file labelled with a date string instead.
func RenderThisWeek(ctx context.Context, src ChangeEventSource, w Window) (string, []byte, Report, error) {
	if w.TZ == nil {
		w.TZ = time.UTC
	}
	w.Cover = ThisWeekCover
	rep, err := Run(ctx, thisWeekFilteredSource{inner: src}, w)
	if err != nil {
		return "", nil, Report{}, err
	}
	return ThisWeekPath, Render(rep), rep, nil
}

// ThisWeekWriteResult is returned by WriteThisWeek.
type ThisWeekWriteResult struct {
	Path    string
	Report  Report
	Written bool
}

// WriteThisWeek renders the current-week digest and atomically replaces the
// rolling artifact under mountRoot. Unlike date-stamped digests, this file is
// rewritten on every call: the week is open until the Sunday→Monday rollover
// promotes it to last-week.md. Atomic temp+rename keeps mount observers from
// reading a half-written file.
func WriteThisWeek(ctx context.Context, mountRoot string, src ChangeEventSource, w Window) (ThisWeekWriteResult, error) {
	path, content, rep, err := RenderThisWeek(ctx, src, w)
	if err != nil {
		return ThisWeekWriteResult{}, err
	}
	if strings.TrimSpace(mountRoot) == "" {
		return ThisWeekWriteResult{}, errors.New("digest: mount root is required")
	}

	localPath := filepath.Join(filepath.Clean(mountRoot), filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return ThisWeekWriteResult{}, err
	}

	tmp, err := os.CreateTemp(filepath.Dir(localPath), ".this-week-*.md.tmp")
	if err != nil {
		return ThisWeekWriteResult{}, err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return ThisWeekWriteResult{}, err
	}
	// os.CreateTemp makes 0600; match the 0644 convention used by the
	// other window writers so the digest is readable by mount/agents.
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return ThisWeekWriteResult{}, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return ThisWeekWriteResult{}, err
	}
	if err := os.Rename(tmpName, localPath); err != nil {
		_ = os.Remove(tmpName)
		return ThisWeekWriteResult{}, err
	}
	return ThisWeekWriteResult{Path: path, Report: rep, Written: true}, nil
}

