package digest

import (
	"bytes"
	"fmt"
	"strings"
)

// Render produces the Markdown bytes for a Report. Output is deterministic
// and matches the contract documented in the workspace-primitives spec
// (WI 1): YAML frontmatter, one H2 per provider, bullets ordered by event
// time, `_no activity_` body for providers with zero events.
func Render(rep Report) []byte {
	var b bytes.Buffer
	writeFrontmatter(&b, rep.Meta)
	b.WriteString("\n# Activity summary for ")
	b.WriteString(rep.Meta.Date)
	b.WriteString("\n")

	for _, sec := range rep.Sections {
		b.WriteString("\n## ")
		b.WriteString(sec.Provider)
		b.WriteString("\n\n")
		if len(sec.Bullets) == 0 {
			b.WriteString("_no activity_\n")
			continue
		}
		for _, bl := range sec.Bullets {
			fmt.Fprintf(&b, "- %s %s — [%s]\n", bl.Identifier, bl.Verb, bl.CanonicalPath)
		}
	}
	return b.Bytes()
}

func writeFrontmatter(b *bytes.Buffer, m Meta) {
	b.WriteString("---\n")
	fmt.Fprintf(b, "date: %s\n", m.Date)
	fmt.Fprintf(b, "generated_at: %s\n", m.GeneratedAt)
	fmt.Fprintf(b, "covers: %s\n", m.Covers)
	fmt.Fprintf(b, "providers: [%s]\n", strings.Join(m.Providers, ", "))
	fmt.Fprintf(b, "events: %d\n", m.Events)
	if len(m.Warnings) > 0 {
		b.WriteString("warnings:\n")
		for _, w := range m.Warnings {
			fmt.Fprintf(b, "  - %s\n", w)
		}
	}
	b.WriteString("---\n")
}
