package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// commandMention matches guidance naming the relayfile binary as a command to
// run — "relayfile mount", "relayfile supervisor install". It deliberately
// requires a space, so the systemd unit name (relayfile-listen.service) and the
// launchd label (com.relayfile.listen) do not match: those are real filenames
// and must stay literal whoever is invoking us.
//
// A bare `relayfile` on its own in a usage block counts too: one such line
// survived a pass that only looked for `relayfile <verb>`.
var commandMention = regexp.MustCompile(`\brelayfile(?: [a-z]|\s*$|\s{2,})`)

// Nouns, not instructions. "delegated relayfile credentials" describes what is
// missing; it tells nobody to run anything.
var allowedNouns = []string{"relayfile credentials", "relayfile workspace id"}

func withoutAllowedNouns(text string) string {
	for _, noun := range allowedNouns {
		text = strings.ReplaceAll(text, noun, "")
	}
	return text
}

// TestUsageNamesTheInvokingProgram pins the contract behind relayfile#509:
// mounted as `agent-relay file`, nothing may tell the user to run `relayfile`,
// because that binary is not installed for them.
//
// Checked over the usage printers rather than one message, since the leak was
// never in one place: an earlier fix corrected five call sites found by
// grepping for "run relayfile", and review found dozens more phrased
// differently. A pattern is the only thing that catches the next one.
func TestUsageNamesTheInvokingProgram(t *testing.T) {
	t.Setenv(programNameEnv, "agent-relay file")

	printers := map[string]func(*bytes.Buffer){
		"usage":      func(b *bytes.Buffer) { printUsage(b) },
		"listen":     func(b *bytes.Buffer) { printListenUsage(b) },
		"supervisor": func(b *bytes.Buffer) { printSupervisorUsage(b) },
		"workspace":  func(b *bytes.Buffer) { printWorkspaceUsage(b, "") },
		"integration": func(b *bytes.Buffer) {
			printIntegrationUsage(b, "")
		},
		"ops":       func(b *bytes.Buffer) { printOpsUsage(b, "") },
		"writeback": func(b *bytes.Buffer) { printWritebackUsage(b, "") },
		"digest":    func(b *bytes.Buffer) { printDigestUsage(b, "") },
	}

	for name, print := range printers {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			print(&out)
			if found := commandMention.FindString(withoutAllowedNouns(out.String())); found != "" {
				t.Errorf("%s usage tells a mounted user to run %q; use programName()", name, found)
			}
			if !strings.Contains(out.String(), "agent-relay file") {
				t.Errorf("%s usage never names the invoking program", name)
			}
		})
	}
}

// TestUsageKeepsServiceFileNames guards the other direction: the systemd unit
// and launchd label are filenames on disk, identical for every caller, and a
// blanket rename would have broken them.
func TestUsageKeepsServiceFileNames(t *testing.T) {
	t.Setenv(programNameEnv, "agent-relay file")
	var out bytes.Buffer
	printSupervisorUsage(&out)
	for _, literal := range []string{"relayfile-listen.service", "com.relayfile.listen.plist"} {
		if !strings.Contains(out.String(), literal) {
			t.Errorf("supervisor usage no longer names %s; that is a real path, not a command", literal)
		}
	}
}

// TestUsageDefaultsToRelayfile keeps direct users seeing the name they typed.
func TestUsageDefaultsToRelayfile(t *testing.T) {
	t.Setenv(programNameEnv, "")
	var out bytes.Buffer
	printUsage(&out)
	if !strings.Contains(out.String(), "relayfile ") {
		t.Error("unmounted usage should name relayfile")
	}
	if strings.Contains(out.String(), "agent-relay file") {
		t.Error("unmounted usage must not name the host")
	}
}
