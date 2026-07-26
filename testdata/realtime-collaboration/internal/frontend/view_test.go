package frontend

import (
	"bytes"
	"strings"
	"testing"

	"example.com/relayfile-collab/internal/model"
)

func TestRenderStatus(t *testing.T) {
	var out bytes.Buffer
	if err := RenderStatus(&out, model.NewStatus("relayfile-collab", "v1")); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"relayfile-collab", "v1"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("rendered status %q does not contain %q", out.String(), want)
		}
	}
}
