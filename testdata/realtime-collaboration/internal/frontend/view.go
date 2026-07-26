package frontend

import (
	"fmt"
	"html/template"
	"io"

	"example.com/relayfile-collab/internal/model"
)

var statusTemplate = template.Must(template.New("status").Parse(
	`<main><h1>{{.Service}}</h1><p class="version">{{.Version}}</p></main>`,
))

func RenderStatus(w io.Writer, status model.Status) error {
	if err := statusTemplate.Execute(w, status); err != nil {
		return fmt.Errorf("render status: %w", err)
	}
	return nil
}
