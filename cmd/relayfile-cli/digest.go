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
	"sort"
	"strings"
	"time"

	digestpkg "github.com/agentworkforce/relayfile/internal/digest"
)

const digestRebuildUsage = "usage: relayfile digest rebuild --window today|yesterday|YYYY-MM-DD|this-week|last-week [--workspace NAME]"

// digestRebuilder is the seam over the internal/digest generator. Tests swap
// this out so CLI parsing can stay focused while the default implementation
// rebuilds local mirror digests.
type digestRebuilder interface {
	Rebuild(ctx context.Context, opts digestRebuildOptions) (digestRebuildResult, error)
}

type digestRebuildOptions struct {
	WorkspaceID string
	LocalDir    string
	Window      string
}

type digestRebuildResult struct {
	Path   string
	Events int
}

// activeDigestRebuilder is overridden by tests via withDigestRebuilder.
var activeDigestRebuilder digestRebuilder = localDigestRebuilder{now: time.Now}

type localDigestRebuilder struct {
	now func() time.Time
}

func (r localDigestRebuilder) Rebuild(ctx context.Context, opts digestRebuildOptions) (digestRebuildResult, error) {
	localDir := strings.TrimSpace(opts.LocalDir)
	if localDir == "" {
		return digestRebuildResult{}, fmt.Errorf("workspace %s has no local mirror", opts.WorkspaceID)
	}
	now := time.Now
	if r.now != nil {
		now = r.now
	}
	generatedAt := now().UTC()
	windows, err := resolveDigestWindows(opts.Window, generatedAt)
	if err != nil {
		return digestRebuildResult{}, err
	}
	remoteRoot := readMountRemoteRoot(localDir)
	providers, err := localDigestProviders(localDir, remoteRoot)
	if err != nil {
		return digestRebuildResult{}, err
	}
	src := localDigestSource{localDir: localDir, remoteRoot: remoteRoot}

	paths := make([]string, 0, len(windows)*2)
	totalEvents := 0
	for _, w := range windows {
		w.GeneratedAt = generatedAt
		w.Providers = providers
		w.TZ = time.UTC
		rep, err := digestpkg.Run(ctx, src, w)
		if err != nil {
			return digestRebuildResult{}, err
		}
		body := digestpkg.Render(rep)
		outPath := filepath.Join(localDir, "digests", digestOutputFilename(w))
		if err := writeFileAtomically(outPath, body, 0o644); err != nil {
			return digestRebuildResult{}, err
		}
		paths = append(paths, outPath)
		totalEvents += rep.Meta.Events

		switch strings.ToLower(strings.TrimSpace(opts.Window)) {
		case "today", "yesterday":
			aliasPath := filepath.Join(localDir, "digests", strings.ToLower(strings.TrimSpace(opts.Window))+".md")
			if err := writeFileAtomically(aliasPath, body, 0o644); err != nil {
				return digestRebuildResult{}, err
			}
			paths = append(paths, aliasPath)
		}
	}
	return digestRebuildResult{Path: strings.Join(paths, ", "), Events: totalEvents}, nil
}

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
	window := fs.String("window", "", "digest window: today, yesterday, YYYY-MM-DD, this-week, or last-week")
	workspace := fs.String("workspace", "", "workspace name or id")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"window":    true,
		"workspace": true,
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
	if _, err := resolveDigestWindows(normalizedWindow, time.Now().UTC()); err != nil {
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
	})
	if err != nil {
		return err
	}
	path := strings.TrimSpace(result.Path)
	if path == "" {
		path = fmt.Sprintf("<mount>/digests/%s.md", normalizedWindow)
	}
	fmt.Fprintf(stdout, "Regenerated %s (events=%d)\n", path, result.Events)
	return nil
}

func resolveDigestWindows(window string, now time.Time) ([]digestpkg.Window, error) {
	window = strings.ToLower(strings.TrimSpace(window))
	today := dateOnlyUTC(now)
	switch window {
	case "today":
		return []digestpkg.Window{{Date: today, Cover: "today"}}, nil
	case "yesterday":
		return []digestpkg.Window{{Date: today.AddDate(0, 0, -1), Cover: "yesterday"}}, nil
	case "this-week":
		start := today.AddDate(0, 0, -daysSinceMonday(today))
		return []digestpkg.Window{{Date: start, Cover: "this-week"}}, nil
	case "last-week":
		thisMonday := today.AddDate(0, 0, -daysSinceMonday(today))
		start := thisMonday.AddDate(0, 0, -7)
		return []digestpkg.Window{{Date: start, Cover: "last-week"}}, nil
	default:
		parsed, err := time.ParseInLocation("2006-01-02", window, time.UTC)
		if err != nil {
			return nil, err
		}
		return []digestpkg.Window{{Date: parsed, Cover: window}}, nil
	}
}

func digestOutputFilename(w digestpkg.Window) string {
	switch strings.TrimSpace(w.Cover) {
	case "this-week", "last-week":
		return w.Cover + ".md"
	default:
		return dateOnlyUTC(w.Date).Format("2006-01-02") + ".md"
	}
}

func dateOnlyUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func daysSinceMonday(t time.Time) int {
	return (int(t.UTC().Weekday()) + 6) % 7
}

func localDigestProviders(localDir, remoteRoot string) ([]string, error) {
	if provider := providerFromPath(remoteRoot); provider != "" {
		return []string{provider}, nil
	}
	entries, err := os.ReadDir(localDir)
	if err != nil {
		return nil, err
	}
	providers := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		switch name {
		case "", ".relay", ".skills", "digests", "node_modules":
			continue
		default:
			if strings.HasPrefix(name, ".") {
				continue
			}
			providers = append(providers, name)
		}
	}
	sort.Strings(providers)
	return providers, nil
}

type localDigestSource struct {
	localDir    string
	remoteRoot string
}

func (s localDigestSource) Events(window digestpkg.Window) ([]digestpkg.ChangeEvent, error) {
	start := dateOnlyUTC(window.Date)
	end := start.AddDate(0, 0, 1)
	switch strings.TrimSpace(window.Cover) {
	case "this-week", "last-week":
		end = start.AddDate(0, 0, 7)
	}
	var events []digestpkg.ChangeEvent
	err := filepath.WalkDir(s.localDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			rel, relErr := filepath.Rel(s.localDir, path)
			if relErr == nil && rel != "." {
				first := strings.SplitN(rel, string(os.PathSeparator), 2)[0]
				if first == entry.Name() {
					switch entry.Name() {
					case ".relay", ".git", ".skills", "digests", "node_modules":
						return filepath.SkipDir
					}
				}
			}
			return nil
		}
		if entry.Name() != "_index.json" {
			return nil
		}
		items, err := readDigestIndexItems(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(s.localDir, path)
		if err != nil {
			return err
		}
		indexRemotePath := remotePathForLocalRel(s.remoteRoot, filepath.ToSlash(rel))
		provider := providerFromPath(indexRemotePath)
		for _, item := range items {
			ts, ok := digestItemTimestamp(item)
			if !ok || ts.Before(start) || !ts.Before(end) {
				continue
			}
			events = append(events, digestpkg.ChangeEvent{
				Provider:      defaultIfBlank(stringField(item, "provider"), provider),
				Timestamp:     ts,
				Identifier:    digestItemIdentifier(item, indexRemotePath),
				Verb:          defaultIfBlank(stringField(item, "verb"), "updated"),
				CanonicalPath: digestItemPath(item, indexRemotePath),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return events, nil
}

func readDigestIndexItems(path string) ([]map[string]any, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("read digest index %s: %w", path, err)
	}
	return digestIndexItems(raw), nil
}

func digestIndexItems(raw any) []map[string]any {
	switch value := raw.(type) {
	case []any:
		return digestMapsFromSlice(value)
	case map[string]any:
		for _, key := range []string{"rows", "items", "entries", "results", "files", "data"} {
			if nested, ok := value[key].([]any); ok {
				return digestMapsFromSlice(nested)
			}
		}
		return []map[string]any{value}
	default:
		return nil
	}
}

func digestMapsFromSlice(values []any) []map[string]any {
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			out = append(out, item)
		}
	}
	return out
}

func digestItemTimestamp(item map[string]any) (time.Time, bool) {
	for _, key := range []string{"updated", "updatedAt", "updated_at", "lastEditedTime", "last_edited_time", "lastEditedAt", "last_edited_at", "lastModifiedAt", "last_modified_at", "modified", "modifiedAt", "modified_at", "created", "createdAt", "created_at", "ts", "timestamp"} {
		if parsed, ok := parseDigestTimestamp(stringField(item, key)); ok {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func parseDigestTimestamp(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func digestItemIdentifier(item map[string]any, fallbackPath string) string {
	for _, key := range []string{"identifier", "key", "number", "title", "name", "id"} {
		if value := stringField(item, key); value != "" {
			return value
		}
	}
	return strings.TrimSuffix(filepath.Base(fallbackPath), filepath.Ext(fallbackPath))
}

func digestItemPath(item map[string]any, fallbackPath string) string {
	for _, key := range []string{"canonicalPath", "path", "file", "href"} {
		if value := normalizeWritebackListPath(stringField(item, key)); value != "" {
			return value
		}
	}
	return fallbackPath
}

func stringField(item map[string]any, key string) string {
	value, ok := item[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return strings.TrimSpace(v.String())
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return strings.TrimSpace(fmt.Sprint(v))
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}
