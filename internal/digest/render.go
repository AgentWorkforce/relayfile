package digest

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func RenderMarkdown(d Digest) string {
	sections := sortSections(d.Sections)
	warnings := sortWarnings(d.Warnings)

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("date: ")
	b.WriteString(formatDate(d.Window.Date))
	b.WriteString("\n")
	b.WriteString("generated_at: ")
	b.WriteString(formatTimestamp(d.Window.GeneratedAt))
	b.WriteString("\n")
	b.WriteString("covers: ")
	b.WriteString(d.Window.Covers)
	b.WriteString("\n")
	b.WriteString("providers: [")
	b.WriteString(strings.Join(providerNames(sections), ", "))
	b.WriteString("]\n")
	b.WriteString(fmt.Sprintf("events: %d\n", d.Window.TotalEvents))
	if len(warnings) > 0 {
		b.WriteString("warnings: [")
		b.WriteString(strings.Join(warningValues(warnings), ", "))
		b.WriteString("]\n")
	}
	b.WriteString("---\n\n")

	if len(sections) == 0 {
		b.WriteString("_no activity_\n")
		return b.String()
	}

	for index, section := range sections {
		if index > 0 {
			b.WriteString("\n")
		}
		b.WriteString("## ")
		b.WriteString(section.Provider)
		b.WriteString("\n\n")
		if len(section.Bullets) == 0 {
			b.WriteString("_no activity_\n")
			continue
		}
		for _, bullet := range section.Bullets {
			b.WriteString("- ")
			b.WriteString(bullet.Text)
			if bullet.CanonicalPath != "" {
				b.WriteString(" - [")
				b.WriteString(bullet.CanonicalPath)
				b.WriteString("]")
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

func providerNames(sections []DigestSection) []string {
	names := make([]string, 0, len(sections))
	for _, section := range sections {
		names = append(names, section.Provider)
	}
	return names
}

func warningValues(warnings []DigestWarning) []string {
	values := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		values = append(values, fmt.Sprintf("%q", warning.Function+": "+warning.Reason))
	}
	sort.Strings(values)
	return values
}

func formatDate(ts time.Time) string {
	if ts.IsZero() {
		return "0001-01-01"
	}
	return ts.UTC().Format(time.DateOnly)
}

func formatTimestamp(ts time.Time) string {
	if ts.IsZero() {
		return "0001-01-01T00:00:00Z"
	}
	return ts.UTC().Format(time.RFC3339)
}
