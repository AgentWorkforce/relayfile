package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

const digestRebuildUsage = "usage: relayfile digest rebuild --window yesterday|today [--workspace NAME]"

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
}

type digestRebuildResult struct {
	Path   string
	Events int
}

// activeDigestRebuilder is overridden by tests via withDigestRebuilder.
var activeDigestRebuilder digestRebuilder = stubDigestRebuilder{}

type stubDigestRebuilder struct{}

func (stubDigestRebuilder) Rebuild(context.Context, digestRebuildOptions) (digestRebuildResult, error) {
	return digestRebuildResult{}, errors.New("digest generator not yet wired; see WI 1 in workspace-primitives-implementation-spec")
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
	window := fs.String("window", "", "digest window: yesterday or today")
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
	switch normalizedWindow {
	case "yesterday", "today":
	default:
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
