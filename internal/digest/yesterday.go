package digest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// YesterdayWriteResult describes the outcome of a WriteYesterday call.
type YesterdayWriteResult struct {
	Path    string
	Report  Report
	Written bool
	Skipped bool
	// Bytes is the rendered (or pre-existing immutable) yesterday.md
	// content. Surfaced so callers can hash the close-write output without
	// re-reading the file.
	Bytes []byte
}

// YesterdayMarkerPath is the per-workspace lock file that records the close
// write for a given local day. Its existence (and matching `<date>` content)
// instructs subsequent calls to short-circuit, guaranteeing that
// `digests/yesterday.md` is byte-stable after the close write for that date,
// even if later ingest events arrive (parent spec WI-B gate B). Stored under
// `.relay/` because it is internal scheduler state rather than a mounted
// digest artifact; the mount watcher already excludes `.relay/`, preventing
// the marker from re-entering digest regeneration.
const YesterdayMarkerPath = ".relay/digests/yesterday.lock"

const yesterdayTempDir = ".relay/digests/tmp"

// YesterdayWindow builds a Window targeting the closed local day for the
// `digests/yesterday.md` artifact. The frontmatter `covers` value is the
// literal string `yesterday` so the file is self-describing as a rolling
// closed-day pointer (parent spec WI-B, child slice acceptance check #6).
func YesterdayWindow(date, generatedAt time.Time, providers []string, tz *time.Location) Window {
	if tz == nil {
		// TODO(workspace-primitives pr39 — timezone slice): once the
		// workspace config exposes a configured zone, callers should
		// pass it here. UTC is the documented fallback per the parent
		// spec until that slice lands.
		tz = time.UTC
	}
	localDate := date.In(tz)
	return Window{
		Date:        localDate,
		Cover:       "yesterday",
		GeneratedAt: generatedAt,
		Providers:   append([]string(nil), providers...),
		TZ:          tz,
	}
}

// RenderYesterday renders the closing-window artifact for the just-closed
// local day. `Meta.Date` is stamped with the closed day and `Meta.Covers` is
// the literal string `yesterday` (acceptance check #6). The renderer reuses
// Run so bullet ordering is byte-identical to what `today.md` produced for
// the same event set during the closed day (acceptance check #2 modulo
// frontmatter). Closing is metadata only; self-host digests remain exhaustive.
func RenderYesterday(ctx context.Context, src ChangeEventSource, w Window) (string, []byte, Report, error) {
	if w.TZ == nil {
		// TODO(workspace-primitives pr39 — timezone slice): see YesterdayWindow.
		w.TZ = time.UTC
	}
	w.Cover = "yesterday"
	w.Closing = true
	rep, err := Run(ctx, closedDailyFilteredSource{inner: src}, w)
	if err != nil {
		return "", nil, Report{}, err
	}
	return YesterdayPath, Render(rep), rep, nil
}

// readYesterdayMarker returns the date string recorded by the most recent
// close write, or "" if no marker exists.
func readYesterdayMarker(mountRoot string) string {
	localPath := filepath.Join(filepath.Clean(mountRoot), filepath.FromSlash(YesterdayMarkerPath))
	data, err := os.ReadFile(localPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// writeYesterdayMarker records `date` as the closed local day for the
// current `yesterday.md`. Subsequent CloseLocalDay calls for the same date
// short-circuit; calls for a later date overwrite the marker as part of
// rotation. The marker lives under `.relay/`, so the mount watcher skips it
// before it can be observed as a provider source event.
func writeYesterdayMarker(mountRoot, date string) error {
	localPath := filepath.Join(filepath.Clean(mountRoot), filepath.FromSlash(YesterdayMarkerPath))
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(localPath), ".yesterday-marker-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write([]byte(date + "\n")); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, localPath); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// WriteYesterday renders the closing-window digest and writes it to
// `<mountRoot>/digests/yesterday.md`. The write is idempotent in two ways:
//
//  1. When `w.Closing` is true (the close-write path) and the per-workspace
//     marker `.relay/digests/yesterday.lock` already records the same local
//     `date`, the on-disk file is treated as immutable for the rest of the
//     day: the existing bytes are returned and the marker re-stamped,
//     guaranteeing byte-equality across subsequent ingest events (gate B).
//  2. When the on-disk file already has byte-equal content, the file is left
//     untouched (mtime preserved) and Skipped is set on the result.
//
// When content differs (rotation forward at local midnight, or first write of
// the day), the writer performs an atomic temp-file + rename so the artifact
// is replaced as a single visible filesystem event (gate E).
func WriteYesterday(ctx context.Context, mountRoot string, src ChangeEventSource, w Window) (YesterdayWriteResult, error) {
	if strings.TrimSpace(mountRoot) == "" {
		return YesterdayWriteResult{}, errors.New("digest: mount root is required")
	}
	if w.TZ == nil {
		w.TZ = time.UTC
	}
	localDateStr := w.Date.In(w.TZ).Format("2006-01-02")
	path := YesterdayPath
	localPath := filepath.Join(filepath.Clean(mountRoot), filepath.FromSlash(path))

	if w.Closing && readYesterdayMarker(mountRoot) == localDateStr {
		// Close for this local day already happened; treat the artifact as
		// immutable. Re-read so the caller still gets a populated result.
		existing, readErr := os.ReadFile(localPath)
		if readErr != nil {
			return YesterdayWriteResult{}, fmt.Errorf("digest: marker present but yesterday.md unreadable: %w", readErr)
		}
		return YesterdayWriteResult{
			Path:    path,
			Report:  Report{Meta: Meta{Date: localDateStr, Covers: "yesterday"}, Sections: nil},
			Written: false,
			Skipped: true,
			Bytes:   existing,
		}, nil
	}

	_, content, rep, err := RenderYesterday(ctx, src, w)
	if err != nil {
		return YesterdayWriteResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return YesterdayWriteResult{}, err
	}

	if existing, err := os.ReadFile(localPath); err == nil {
		if bytes.Equal(existing, content) {
			if w.Closing {
				if err := writeYesterdayMarker(mountRoot, localDateStr); err != nil {
					return YesterdayWriteResult{}, err
				}
			}
			return YesterdayWriteResult{Path: path, Report: rep, Written: false, Skipped: true, Bytes: existing}, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return YesterdayWriteResult{}, err
	}

	tmpDir := filepath.Join(filepath.Clean(mountRoot), filepath.FromSlash(yesterdayTempDir))
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return YesterdayWriteResult{}, err
	}
	tmp, err := os.CreateTemp(tmpDir, ".yesterday-*.md.tmp")
	if err != nil {
		return YesterdayWriteResult{}, err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return YesterdayWriteResult{}, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return YesterdayWriteResult{}, err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return YesterdayWriteResult{}, err
	}
	if err := os.Rename(tmpName, localPath); err != nil {
		_ = os.Remove(tmpName)
		return YesterdayWriteResult{}, err
	}
	if w.Closing {
		if err := writeYesterdayMarker(mountRoot, localDateStr); err != nil {
			return YesterdayWriteResult{}, err
		}
	}
	return YesterdayWriteResult{Path: path, Report: rep, Written: true, Bytes: content}, nil
}

// CloseLocalDay invokes the yesterday writer for the local day that just
// closed (the calendar day preceding `now` in `tz`). It is the callable
// trigger this slice exposes to the existing tick/sync loop; this slice
// deliberately does not introduce a new scheduler (lead-plan §Code
// Changes / Closing-window trigger).
//
// Concurrency: serialization is the caller's responsibility — pass the
// existing digest write lock around CloseLocalDay so concurrent ticks
// observe a single rotation per local midnight. WriteYesterday's idempotent
// equal-bytes short-circuit means two losing ticks observe Skipped=true
// rather than a duplicate write.
func CloseLocalDay(ctx context.Context, mountRoot string, src ChangeEventSource, now time.Time, providers []string, tz *time.Location) (YesterdayWriteResult, error) {
	if tz == nil {
		// TODO(workspace-primitives pr39 — timezone slice): see YesterdayWindow.
		tz = time.UTC
	}
	closedDay := now.In(tz).AddDate(0, 0, -1)
	w := YesterdayWindow(closedDay, now, providers, tz)
	w.Closing = true
	return WriteYesterday(ctx, mountRoot, src, w)
}

// CloseLocalDayFor closes the specific local `closedDay`. It is the
// scheduler-friendly entry point: catch-up across offline days walks
// chronologically and invokes CloseLocalDayFor for each missing date. The
// marker prevents a re-close from mutating `yesterday.md` for dates older
// than the most recently closed one (only the latest catch-up date "wins"
// the artifact, but every closed day still produces its date-stamped
// archive via the sibling slice — out of scope here).
func CloseLocalDayFor(ctx context.Context, mountRoot string, src ChangeEventSource, closedDay, generatedAt time.Time, providers []string, tz *time.Location) (YesterdayWriteResult, error) {
	if tz == nil {
		tz = time.UTC
	}
	w := YesterdayWindow(closedDay, generatedAt, providers, tz)
	w.Closing = true
	return WriteYesterday(ctx, mountRoot, src, w)
}
