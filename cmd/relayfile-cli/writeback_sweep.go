package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
)

const writebackSweepUsage = "usage: relayfile writeback sweep-drafts [WORKSPACE] [--path-prefix PREFIX] [--pattern GLOB ...] [--apply] [--json] [--server URL] [--token TOKEN]"

// sweepDraftsResult mirrors relayfile.SweepDraftsResult (internal/relayfile).
type sweepDraftsResult struct {
	DryRun  bool `json:"dryRun"`
	Scanned int  `json:"scanned"`
	Removed []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"removed"`
	Skipped []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"skipped"`
}

type stringSliceFlag []string

func (f *stringSliceFlag) String() string { return strings.Join(*f, ",") }

func (f *stringSliceFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("pattern must not be empty")
	}
	*f = append(*f, value)
	return nil
}

// runWritebackSweepDrafts drives the one-time residue sweep (issue #242)
// against the service: POST /v1/workspaces/{id}/writeback/sweep-drafts.
// Dry run unless --apply is passed.
func runWritebackSweepDrafts(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("writeback sweep-drafts", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	pathPrefix := fs.String("path-prefix", "", "restrict the sweep to a subtree")
	var patterns stringSliceFlag
	fs.Var(&patterns, "pattern", "basename glob for hand-named drafts (repeatable), e.g. wb-*.json")
	apply := fs.Bool("apply", false, "execute removals (default is a dry run)")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	server := fs.String("server", "", "relayfile server URL override")
	tokenOverride := fs.String("token", "", "relayfile token override")
	if err := fs.Parse(normalizeFlagArgs(args, map[string]bool{
		"path-prefix": true,
		"pattern":     true,
		"apply":       false,
		"json":        false,
		"server":      true,
		"token":       true,
	})); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New(writebackSweepUsage)
	}

	record, err := resolveWorkspaceRecord(firstArg(fs))
	if err != nil {
		return err
	}
	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	client, err := newAPIClient(resolveServer(*server, creds), resolveToken(*tokenOverride, creds))
	if err != nil {
		return err
	}

	requestBody := map[string]any{
		"pathPrefix": strings.TrimSpace(*pathPrefix),
		"patterns":   []string(patterns),
		"apply":      *apply,
	}
	var result sweepDraftsResult
	if err := client.postJSON(
		context.Background(),
		fmt.Sprintf("/v1/workspaces/%s/writeback/sweep-drafts", url.PathEscape(record.ID)),
		requestBody,
		&result,
	); err != nil {
		return err
	}

	if *jsonOutput {
		return writeJSON(stdout, result)
	}
	printSweepDraftsResult(stdout, result)
	return nil
}

func printSweepDraftsResult(stdout io.Writer, result sweepDraftsResult) {
	mode := "applied"
	if result.DryRun {
		mode = "dry-run"
	}
	fmt.Fprintf(stdout, "Sweep (%s): scanned %d, %d removable, %d skipped\n",
		mode, result.Scanned, len(result.Removed), len(result.Skipped))
	for _, removed := range result.Removed {
		verb := "removed"
		if result.DryRun {
			verb = "would remove"
		}
		fmt.Fprintf(stdout, "  %s\t%s\t(%s)\n", verb, removed.Path, removed.Reason)
	}
	for _, skipped := range result.Skipped {
		fmt.Fprintf(stdout, "  skipped\t%s\t(%s)\n", skipped.Path, skipped.Reason)
	}
	if result.DryRun && len(result.Removed) > 0 {
		fmt.Fprintln(stdout, "Re-run with --apply to remove.")
	}
}
