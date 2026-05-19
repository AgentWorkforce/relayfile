package mountsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentworkforce/relayfile/internal/digest"
)

// CloseScheduler drives workspace-local closing-window writes for
// `digests/yesterday.md`, `digests/<YYYY-MM-DD>.md`, and weekly rollups.
// The implementation is intentionally tick-based:
// the caller (the mount sync loop) decides how often Tick fires; the
// scheduler computes which local days have closed since the last successful
// write by consulting the per-workspace marker file written by
// `digest.WriteYesterday`. This keeps the scheduler restart-tolerant and
// avoids introducing a long-lived background goroutine that the rest of the
// daemon would have to coordinate with.
//
// Tick is idempotent: if the marker already records the most recently
// closed local day, Tick is a no-op and returns nil. If the marker is N
// days behind (e.g. after an offline gap), Tick closes each missing day in
// chronological order so the date-stamped archives sibling slice has a
// stable ordering to walk later.
type CloseScheduler struct {
	MountRoot string
	TZ        *time.Location
	Providers []string
	Source    digest.ChangeEventSource
	// Now lets tests inject a deterministic clock. Defaults to time.Now.
	Now func() time.Time
}

// RollingDigestCoalescer batches noisy provider changes before rewriting
// rolling digest artifacts such as today.md and this-week.md.
type RollingDigestCoalescer struct {
	Interval time.Duration
	Now      func() time.Time

	pending bool
	dueAt   time.Time
}

func (c *RollingDigestCoalescer) ObserveChange(path string) bool {
	if digest.IsDigestPath(path) {
		return false
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	interval := c.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	c.pending = true
	c.dueAt = now.Add(interval)
	return true
}

func (c *RollingDigestCoalescer) Due() bool {
	if !c.pending {
		return false
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	return !now.Before(c.dueAt)
}

func (c *RollingDigestCoalescer) MarkFlushed() {
	c.pending = false
	c.dueAt = time.Time{}
}

// Tick runs the close pipeline for every local day that has closed since
// the marker was last written. Returns the list of closed-day date strings
// (YYYY-MM-DD) in chronological order, useful for logging and trail
// breadcrumbs.
func (s *CloseScheduler) Tick(ctx context.Context) ([]string, error) {
	if strings.TrimSpace(s.MountRoot) == "" {
		return nil, errors.New("mountsync: scheduler mount root is required")
	}
	if s.Source == nil {
		return nil, errors.New("mountsync: scheduler change-event source is required")
	}
	tz := s.TZ
	if tz == nil {
		tz = time.UTC
	}
	clock := s.Now
	if clock == nil {
		clock = time.Now
	}
	now := clock()

	// The latest day that has *fully* closed is the calendar day before
	// `now` in the workspace TZ. If `now` itself is still that day's
	// midnight (00:00), the local day has just closed and we proceed.
	targetClosed := localDateOnly(now.In(tz).AddDate(0, 0, -1), tz)

	lastClosed := s.readMarker()
	startDay := targetClosed
	if !lastClosed.IsZero() {
		// Resume one local day past the last successful close.
		startDay = localDateOnly(lastClosed.AddDate(0, 0, 1), tz)
	}
	if startDay.After(targetClosed) {
		return nil, nil
	}

	var closed []string
	for day := startDay; !day.After(targetClosed); day = day.AddDate(0, 0, 1) {
		generatedAt := now
		// For catch-up days, anchor `generated_at` to the day boundary so
		// the produced report is deterministic regardless of how late the
		// daemon noticed the gap.
		if !day.Equal(targetClosed) {
			generatedAt = day.AddDate(0, 0, 1) // midnight of the day after the closed one
		}
		boundary := day.AddDate(0, 0, 1)
		if _, err := digest.MaybeCloseDateStampedWindow(ctx, boundary, day, tz, s.MountRoot, s.Source, s.Providers, generatedAt); err != nil {
			return closed, err
		}
		if _, err := digest.MaybeCloseLastWeekWindow(ctx, boundary, day, tz, s.MountRoot, s.Source, s.Providers, generatedAt); err != nil {
			return closed, err
		}
		res, err := digest.CloseLocalDayFor(ctx, s.MountRoot, s.Source, day, generatedAt, s.Providers, tz)
		if err != nil {
			return closed, err
		}
		_ = res
		closed = append(closed, day.Format("2006-01-02"))
	}
	return closed, nil
}

// localDateOnly returns t truncated to midnight in tz.
func localDateOnly(t time.Time, tz *time.Location) time.Time {
	t = t.In(tz)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, tz)
}

func (s *CloseScheduler) readMarker() time.Time {
	localPath := filepath.Join(filepath.Clean(s.MountRoot), filepath.FromSlash(digest.YesterdayMarkerPath))
	data, err := os.ReadFile(localPath)
	if err != nil {
		return time.Time{}
	}
	stamp := strings.TrimSpace(string(data))
	tz := s.TZ
	if tz == nil {
		tz = time.UTC
	}
	parsed, err := time.ParseInLocation("2006-01-02", stamp, tz)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
