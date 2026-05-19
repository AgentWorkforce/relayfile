package digest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LastWeekPath is the canonical mount-relative path for the closing-window
// weekly digest. The artifact is rewritten only when the workspace-local
// week boundary advances; within a closed week the file is immutable.
const LastWeekPath = "digests/last-week.md"

// LastWeekCover is the literal value written to the digest's `covers`
// frontmatter field for weekly closing-window digests.
const LastWeekCover = "last-week"

// LastWeekStart returns the ISO-week Monday that opens the workspace-local
// week containing date. It is a thin wrapper over MondayStart so the
// closing-window writer and the rolling this-week writer share the same
// boundary logic — the parent spec mandates a single week-start convention
// for Work Item B.
func LastWeekStart(date time.Time, tz *time.Location) time.Time {
	return MondayStart(date, tz)
}

// IsLastWeekClosingDay reports whether justClosedLocalDay (interpreted in
// tz) is the final day of the workspace-local ISO week — i.e. a Sunday. The
// daily closing path uses this to decide whether to *also* persist
// `/digests/last-week.md` covering the just-closed week.
func IsLastWeekClosingDay(justClosedLocalDay time.Time, tz *time.Location) bool {
	if tz == nil {
		tz = time.UTC
	}
	return justClosedLocalDay.In(tz).Weekday() == time.Sunday
}

// LastWeekWindow builds a Window for a closed workspace-local week.
// weekStart is the Monday of the covered week; the digest's `date`
// frontmatter renders to this anchor. Bullets within each provider section
// remain sorted by event time ascending, matching the daily-digest renderer.
//
// Callers that have only the just-closed day in hand should pass
// `LastWeekStart(justClosedDay, tz)` as weekStart.
func LastWeekWindow(weekStart, generatedAt time.Time, providers []string, tz *time.Location) Window {
	if tz == nil {
		tz = time.UTC
	}
	local := weekStart.In(tz)
	return Window{
		Date:        time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, tz),
		Cover:       LastWeekCover,
		GeneratedAt: generatedAt,
		Providers:   append([]string(nil), providers...),
		TZ:          tz,
	}
}

// LastWeekWriteResult reports what WriteLastWeek did with the on-disk file.
// Written is true only when the file was created or replaced this call.
type LastWeekWriteResult struct {
	Path     string
	Report   Report
	Written  bool
	Replaced bool
}

// lastWeekFilteredSource narrows an underlying ChangeEventSource to the
// seven-day window [Window.Date, Window.Date+7days) in workspace-local time.
// Sharing FilterEventsInThisWeek with the rolling weekly writer keeps the
// boundary semantics centralized — both paths agree on what counts as "in
// the week" down to the second.
type lastWeekFilteredSource struct {
	inner ChangeEventSource
}

func (s lastWeekFilteredSource) Events(w Window) ([]ChangeEvent, error) {
	raw, err := s.inner.Events(w)
	if err != nil {
		return nil, err
	}
	tz := w.TZ
	if tz == nil {
		tz = time.UTC
	}
	weekStart := w.Date.In(tz)
	weekEnd := weekStart.AddDate(0, 0, 7)
	return FilterEventsInThisWeek(raw, weekStart, weekEnd, tz), nil
}

// RenderLastWeek renders the closing-window weekly artifact for w. The
// returned path is always LastWeekPath; the returned Report's frontmatter
// `date` reflects the week-start anchor and `covers` is forced to
// "last-week" so callers cannot accidentally produce a date-labelled cover.
func RenderLastWeek(ctx context.Context, src ChangeEventSource, w Window) (string, []byte, Report, error) {
	if w.TZ == nil {
		w.TZ = time.UTC
	}
	w.Cover = LastWeekCover
	rep, err := Run(ctx, lastWeekFilteredSource{inner: src}, w)
	if err != nil {
		return "", nil, Report{}, err
	}
	return LastWeekPath, Render(rep), rep, nil
}

// WriteLastWeek renders the weekly digest and writes /digests/last-week.md
// under mountRoot with closing-window semantics:
//
//   - If the file does not exist, it is created.
//   - If the file exists and its `date:` frontmatter already matches the
//     week-start being written, the call is a no-op (within-week
//     immutability — same content for the same closed week).
//   - If the file exists and covers an older week (different `date:`), the
//     file is replaced *whole* with the new week's content (next-boundary
//     replacement).
//
// Writes use temp+rename so observers see either the old or new file, never
// a partial write, and the underlying watcher receives a normal filesystem
// event for the changed path.
func WriteLastWeek(ctx context.Context, mountRoot string, src ChangeEventSource, w Window) (LastWeekWriteResult, error) {
	path, content, rep, err := RenderLastWeek(ctx, src, w)
	if err != nil {
		return LastWeekWriteResult{}, err
	}
	if strings.TrimSpace(mountRoot) == "" {
		return LastWeekWriteResult{}, errors.New("digest: mount root is required")
	}

	localPath := filepath.Join(filepath.Clean(mountRoot), filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return LastWeekWriteResult{}, err
	}

	existing, readErr := os.ReadFile(localPath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return LastWeekWriteResult{}, readErr
	}

	if readErr == nil {
		existingDate := readDateFrontmatter(existing)
		if existingDate != "" && existingDate == rep.Meta.Date {
			return LastWeekWriteResult{Path: path, Report: rep, Written: false}, nil
		}
	}

	tmpDir := filepath.Join(filepath.Clean(mountRoot), filepath.FromSlash(yesterdayTempDir))
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return LastWeekWriteResult{}, err
	}
	tmp, err := os.CreateTemp(tmpDir, ".last-week-*.md.tmp")
	if err != nil {
		return LastWeekWriteResult{}, err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return LastWeekWriteResult{}, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return LastWeekWriteResult{}, err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return LastWeekWriteResult{}, err
	}
	if err := os.Rename(tmpName, localPath); err != nil {
		_ = os.Remove(tmpName)
		return LastWeekWriteResult{}, err
	}
	return LastWeekWriteResult{
		Path:     path,
		Report:   rep,
		Written:  true,
		Replaced: readErr == nil,
	}, nil
}

// MaybeCloseLastWeekWindow persists the closing-window weekly digest iff
// the local-date transition from prev to now crossed a workspace-local
// Sunday→Monday boundary. The covered week is the one whose Monday is
// LastWeekStart(prev, tz). It is safe to call on every daily-close cycle
// because WriteLastWeek short-circuits when the file's frontmatter `date:`
// already matches the week being rendered.
//
// Mirrors MaybeCloseDateStampedWindow's clock-driven shape so the daily
// closing path can fire both helpers in sequence without bespoke wiring.
func MaybeCloseLastWeekWindow(
	ctx context.Context,
	now, prev time.Time,
	tz *time.Location,
	mountRoot string,
	src ChangeEventSource,
	providers []string,
	generatedAt time.Time,
) (LastWeekWriteResult, error) {
	if tz == nil {
		tz = time.UTC
	}
	prevLocal := prev.In(tz)
	nowLocal := now.In(tz)
	if prevLocal.Format("2006-01-02") >= nowLocal.Format("2006-01-02") {
		return LastWeekWriteResult{}, nil
	}
	if !IsLastWeekClosingDay(prevLocal, tz) {
		return LastWeekWriteResult{}, nil
	}
	weekStart := LastWeekStart(prevLocal, tz)
	window := LastWeekWindow(weekStart, generatedAt, providers, tz)
	return WriteLastWeek(ctx, mountRoot, src, window)
}

// readDateFrontmatter extracts the value of the `date:` line from the YAML
// frontmatter at the top of a digest body. Returns "" when no frontmatter
// or no `date:` line is present. The parser is intentionally minimal — the
// digest renderer emits a fixed, line-oriented frontmatter shape, so a full
// YAML parser would be overkill.
func readDateFrontmatter(body []byte) string {
	if !bytes.HasPrefix(body, []byte("---\n")) {
		return ""
	}
	rest := body[len("---\n"):]
	sc := bufio.NewScanner(bytes.NewReader(rest))
	for sc.Scan() {
		line := sc.Text()
		if line == "---" {
			return ""
		}
		if strings.HasPrefix(line, "date:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "date:"))
		}
	}
	return ""
}
