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

const (
	TodayPath     = "digests/today.md"
	YesterdayPath = "digests/yesterday.md"
)

// DateStampedPath returns the canonical closed-window digest path for date.
func DateStampedPath(date time.Time, tz *time.Location) string {
	if tz == nil {
		tz = time.UTC
	}
	return fmt.Sprintf("digests/%s.md", date.In(tz).Format("2006-01-02"))
}

// IsDigestPath recognizes all v1 digest artifacts. The rolling daily files
// (today.md, yesterday.md) and date-stamped closed-day files share this
// predicate with the rolling weekly artifact so the mount-sync recursion
// guard in `.claude/rules/relayfile-integration-digests.md` covers every
// digest path with a single check.
func IsDigestPath(path string) bool {
	normalized := normalizeDigestPath(path)
	return normalized == TodayPath ||
		normalized == YesterdayPath ||
		normalized == ThisWeekPath ||
		normalized == LastWeekPath ||
		IsDateStampedPath(normalized)
}

// IsDateStampedPath reports whether path has the canonical
// digests/YYYY-MM-DD.md shape and contains a valid calendar date.
func IsDateStampedPath(path string) bool {
	normalized := normalizeDigestPath(path)
	if !strings.HasPrefix(normalized, "digests/") || !strings.HasSuffix(normalized, ".md") {
		return false
	}
	stem := strings.TrimSuffix(strings.TrimPrefix(normalized, "digests/"), ".md")
	parsed, err := time.Parse("2006-01-02", stem)
	return err == nil && parsed.Format("2006-01-02") == stem
}

// DateStampedWindow builds a Window for a closed local day. The `covers`
// frontmatter uses the date string so the file is self-describing even after
// `yesterday.md` moves on to a later day.
func DateStampedWindow(date, generatedAt time.Time, providers []string, tz *time.Location) Window {
	if tz == nil {
		tz = time.UTC
	}
	localDate := date.In(tz)
	cover := localDate.Format("2006-01-02")
	return Window{
		Date:        localDate,
		Cover:       cover,
		GeneratedAt: generatedAt,
		Providers:   append([]string(nil), providers...),
		TZ:          tz,
	}
}

type DateStampedWriteResult struct {
	Path    string
	Report  Report
	Written bool
}

// RenderDateStamped renders the closed-window artifact for w.Date.
func RenderDateStamped(ctx context.Context, src ChangeEventSource, w Window) (string, []byte, Report, error) {
	if w.TZ == nil {
		w.TZ = time.UTC
	}
	w.Cover = w.Date.In(w.TZ).Format("2006-01-02")
	rep, err := Run(ctx, closedDailyFilteredSource{inner: src}, w)
	if err != nil {
		return "", nil, Report{}, err
	}
	return DateStampedPath(w.Date, w.TZ), Render(rep), rep, nil
}

type closedDailyFilteredSource struct {
	inner ChangeEventSource
}

func (s closedDailyFilteredSource) Events(w Window) ([]ChangeEvent, error) {
	raw, err := s.inner.Events(w)
	if err != nil {
		return nil, err
	}
	tz := w.TZ
	if tz == nil {
		tz = time.UTC
	}
	start := localDayStart(w.Date, tz)
	return FilterEventsInToday(raw, start, start.AddDate(0, 0, 1), tz), nil
}

func localDayStart(t time.Time, tz *time.Location) time.Time {
	if tz == nil {
		tz = time.UTC
	}
	local := t.In(tz)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, tz)
}

// WriteDateStamped renders and creates digests/YYYY-MM-DD.md under mountRoot.
// Existing files are treated as immutable closed windows and are not rewritten.
func WriteDateStamped(ctx context.Context, mountRoot string, src ChangeEventSource, w Window) (DateStampedWriteResult, error) {
	path, content, rep, err := RenderDateStamped(ctx, src, w)
	if err != nil {
		return DateStampedWriteResult{}, err
	}
	if strings.TrimSpace(mountRoot) == "" {
		return DateStampedWriteResult{}, errors.New("digest: mount root is required")
	}

	localPath := filepath.Join(filepath.Clean(mountRoot), filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return DateStampedWriteResult{}, err
	}

	f, err := os.OpenFile(localPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return DateStampedWriteResult{Path: path, Report: rep, Written: false}, nil
	}
	if err != nil {
		return DateStampedWriteResult{}, err
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(localPath)
		return DateStampedWriteResult{}, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(localPath)
		return DateStampedWriteResult{}, err
	}
	return DateStampedWriteResult{Path: path, Report: rep, Written: true}, nil
}

// MaybeCloseDateStampedWindow persists the closed-window date-stamped
// digest for the local day containing prev iff now has crossed into a
// later local day. It is the relayfile-side primitive cloud rotation
// calls at the same point today.md is rotated into yesterday.md.
//
// Returns Written=false (and a zero Path) when prev and now are the
// same local day. When the local-date boundary has been crossed,
// WriteDateStamped is invoked for the local day of prev; the
// O_CREATE|O_EXCL guard makes repeat invocations a no-op so this is
// safe to call from a clock-driven loop.
func MaybeCloseDateStampedWindow(
	ctx context.Context,
	now, prev time.Time,
	tz *time.Location,
	mountRoot string,
	src ChangeEventSource,
	providers []string,
	generatedAt time.Time,
) (DateStampedWriteResult, error) {
	if tz == nil {
		tz = time.UTC
	}
	prevLocal := prev.In(tz)
	nowLocal := now.In(tz)
	if prevLocal.Format("2006-01-02") >= nowLocal.Format("2006-01-02") {
		return DateStampedWriteResult{}, nil
	}
	window := DateStampedWindow(prevLocal, generatedAt, providers, tz)
	return WriteDateStamped(ctx, mountRoot, src, window)
}

func normalizeDigestPath(path string) string {
	normalized := filepath.ToSlash(strings.TrimSpace(path))
	normalized = strings.TrimPrefix(normalized, "/")
	return strings.Trim(normalized, "/")
}
