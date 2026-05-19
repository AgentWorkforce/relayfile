package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	digestpkg "github.com/agentworkforce/relayfile/internal/digest"
)

const digestRebuildUsage = "usage: relayfile digest rebuild --window today|yesterday|this-week|last-week|YYYY-MM-DD [--workspace NAME] [--json]"

// digestRebuilder is the seam over the internal/digest generator. The CLI ships
// a stub today; the real implementation lands when work item 1 of the
// workspace-primitives spec wires the daemon-side generator.
type digestRebuilder interface {
	Rebuild(ctx context.Context, opts digestRebuildOptions) (digestRebuildResult, error)
}

type digestRebuildOptions struct {
	WorkspaceID string
	LocalDir    string
	Window      string
	Timezone    string
	Now         time.Time
}

type digestRebuildResult struct {
	WorkspaceID string   `json:"workspaceId,omitempty"`
	Window      string   `json:"window"`
	Path        string   `json:"path"`
	Events      int      `json:"events"`
	Warnings    []string `json:"warnings,omitempty"`
}

var digestNow = time.Now

// activeDigestRebuilder is overridden by tests via withDigestRebuilder.
var activeDigestRebuilder digestRebuilder = localDigestRebuilder{}

func withDigestRebuilder(r digestRebuilder, fn func()) {
	prev := activeDigestRebuilder
	activeDigestRebuilder = r
	defer func() { activeDigestRebuilder = prev }()
	fn()
}

func runDigest(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("digest subcommand is required: rebuild")
	}
	switch args[0] {
	case "rebuild":
		return runDigestRebuild(args[1:], stdout)
	default:
		return fmt.Errorf("unknown digest subcommand %q", args[0])
	}
}

func runDigestRebuild(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("digest rebuild", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	window := fs.String("window", "", "digest window: today, yesterday, this-week, last-week, or YYYY-MM-DD")
	workspace := fs.String("workspace", "", "workspace name or id")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"window":    true,
		"workspace": true,
		"json":      false,
	})); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New(digestRebuildUsage)
	}

	if strings.TrimSpace(*window) == "" {
		return errors.New(digestRebuildUsage)
	}
	normalizedWindow := strings.ToLower(strings.TrimSpace(*window))
	if !isDigestRebuildWindow(normalizedWindow) {
		return fmt.Errorf("unknown window %q: %s", *window, digestRebuildUsage)
	}

	workspaceID, record, err := resolveWorkspaceLikeStatus(strings.TrimSpace(*workspace))
	if err != nil {
		return err
	}

	result, err := activeDigestRebuilder.Rebuild(context.Background(), digestRebuildOptions{
		WorkspaceID: workspaceID,
		LocalDir:    record.LocalDir,
		Window:      normalizedWindow,
		Timezone:    record.Timezone,
		Now:         digestNow(),
	})
	if err != nil {
		return err
	}
	result.WorkspaceID = workspaceID
	result.Window = normalizedWindow
	path := strings.TrimSpace(result.Path)
	if path == "" {
		path = fmt.Sprintf("<mount>/digests/%s.md", normalizedWindow)
	}
	if *jsonOut {
		result.Path = path
		payload, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		payload = append(payload, '\n')
		_, err = stdout.Write(payload)
		return err
	}
	fmt.Fprintf(stdout, "Regenerated %s (events=%d)\n", path, result.Events)
	return nil
}

func isDigestRebuildWindow(window string) bool {
	switch window {
	case "today", "yesterday", "this-week", "last-week":
		return true
	default:
		if parsed, err := time.Parse("2006-01-02", window); err == nil {
			return parsed.Format("2006-01-02") == window
		}
		return false
	}
}

type localDigestRebuilder struct{}

func (localDigestRebuilder) Rebuild(ctx context.Context, opts digestRebuildOptions) (digestRebuildResult, error) {
	localDir := strings.TrimSpace(opts.LocalDir)
	if localDir == "" {
		return digestRebuildResult{}, errors.New("workspace localDir is required for digest rebuild")
	}
	tz, err := digestpkg.ResolveTZ(digestpkg.WorkspaceTZConfig{Timezone: opts.Timezone})
	if err != nil {
		return digestRebuildResult{}, err
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	src := localMirrorDigestSource{root: localDir}
	providers, err := collectDigestProviders(localDir)
	if err != nil {
		return digestRebuildResult{}, err
	}

	var path string
	var events int
	var warnings []string
	switch opts.Window {
	case "today":
		res, err := digestpkg.WriteToday(ctx, localDir, src, providers, now, tz, digestpkg.BuildOptions{})
		if err != nil {
			return digestRebuildResult{}, err
		}
		path, events = res.Path, res.Report.Meta.Events
		warnings = append(warnings, res.Report.Meta.Warnings...)
	case "yesterday":
		closedDay := localDateOnlyCLI(now.In(tz).AddDate(0, 0, -1), tz)
		_ = os.Remove(filepath.Join(localDir, filepath.FromSlash(digestpkg.YesterdayPath)))
		_ = os.Remove(filepath.Join(localDir, filepath.FromSlash(digestpkg.YesterdayMarkerPath)))
		dateStampedPath := digestpkg.DateStampedPath(closedDay, tz)
		_ = os.Remove(filepath.Join(localDir, filepath.FromSlash(dateStampedPath)))
		res, err := digestpkg.CloseLocalDayFor(ctx, localDir, src, closedDay, now, providers, tz)
		if err != nil {
			return digestRebuildResult{}, err
		}
		dateRes, err := digestpkg.WriteDateStamped(ctx, localDir, src, digestpkg.DateStampedWindow(closedDay, now, providers, tz))
		if err != nil {
			return digestRebuildResult{}, err
		}
		path, events = res.Path, res.Report.Meta.Events
		warnings = append(warnings, res.Report.Meta.Warnings...)
		warnings = append(warnings, dateRes.Report.Meta.Warnings...)
	case "this-week":
		w := digestpkg.ThisWeekWindow(now, now, providers, tz)
		res, err := digestpkg.WriteThisWeek(ctx, localDir, src, w)
		if err != nil {
			return digestRebuildResult{}, err
		}
		path, events = res.Path, res.Report.Meta.Events
		warnings = append(warnings, res.Report.Meta.Warnings...)
	case "last-week":
		weekStart := digestpkg.MondayStart(now, tz).AddDate(0, 0, -7)
		_ = os.Remove(filepath.Join(localDir, filepath.FromSlash(digestpkg.LastWeekPath)))
		w := digestpkg.LastWeekWindow(weekStart, now, providers, tz)
		res, err := digestpkg.WriteLastWeek(ctx, localDir, src, w)
		if err != nil {
			return digestRebuildResult{}, err
		}
		path, events = res.Path, res.Report.Meta.Events
		warnings = append(warnings, res.Report.Meta.Warnings...)
	default:
		date, err := time.ParseInLocation("2006-01-02", opts.Window, tz)
		if err != nil {
			return digestRebuildResult{}, fmt.Errorf("malformed digest date %q: %w", opts.Window, err)
		}
		today := localDateOnlyCLI(now.In(tz), tz)
		if date.After(today) {
			return digestRebuildResult{}, fmt.Errorf("digest window %s is in the future", opts.Window)
		}
		target := digestpkg.DateStampedPath(date, tz)
		_ = os.Remove(filepath.Join(localDir, filepath.FromSlash(target)))
		res, err := digestpkg.WriteDateStamped(ctx, localDir, src, digestpkg.DateStampedWindow(date, now, providers, tz))
		if err != nil {
			return digestRebuildResult{}, err
		}
		path, events = res.Path, res.Report.Meta.Events
		warnings = append(warnings, res.Report.Meta.Warnings...)
	}
	return digestRebuildResult{
		WorkspaceID: opts.WorkspaceID,
		Window:      opts.Window,
		Path:        filepath.Join(localDir, filepath.FromSlash(path)),
		Events:      events,
		Warnings:    warnings,
	}, nil
}

type localMirrorDigestSource struct {
	root string
}

func (s localMirrorDigestSource) Events(_ digestpkg.Window) ([]digestpkg.ChangeEvent, error) {
	var events []digestpkg.ChangeEvent
	err := filepath.WalkDir(s.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == s.root {
			return nil
		}
		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			switch strings.Split(rel, "/")[0] {
			case ".git", ".relay", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if digestpkg.IsDigestPath(rel) || strings.HasPrefix(rel, ".relay/") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		provider := strings.Split(rel, "/")[0]
		events = append(events, digestpkg.ChangeEvent{
			Provider:      provider,
			Timestamp:     info.ModTime(),
			Identifier:    rel,
			Verb:          "updated",
			CanonicalPath: rel,
		})
		return nil
	})
	return events, err
}

func collectDigestProviders(localDir string) ([]string, error) {
	entries, err := os.ReadDir(localDir)
	if err != nil {
		return nil, err
	}
	var providers []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		switch name {
		case ".git", ".relay", "node_modules", "digests":
			continue
		}
		providers = append(providers, name)
	}
	return providers, nil
}

func localDateOnlyCLI(t time.Time, tz *time.Location) time.Time {
	t = t.In(tz)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, tz)
}
