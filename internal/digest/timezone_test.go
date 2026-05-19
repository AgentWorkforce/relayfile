package digest

import (
	"errors"
	"testing"
	"time"
)

func TestResolveTZ_Precedence(t *testing.T) {
	oldLocal := time.Local
	t.Cleanup(func() { time.Local = oldLocal })
	time.Local = time.FixedZone("LocalTest", 3600)
	t.Setenv("RELAYFILE_TZ", "America/New_York")

	loc, err := ResolveTZ(WorkspaceTZConfig{Timezone: "Europe/Oslo"})
	if err != nil {
		t.Fatalf("ResolveTZ config: %v", err)
	}
	if loc.String() != "Europe/Oslo" {
		t.Fatalf("config precedence loc = %q, want Europe/Oslo", loc.String())
	}

	loc, err = ResolveTZ(WorkspaceTZConfig{})
	if err != nil {
		t.Fatalf("ResolveTZ env: %v", err)
	}
	if loc.String() != "America/New_York" {
		t.Fatalf("env precedence loc = %q, want America/New_York", loc.String())
	}

	t.Setenv("RELAYFILE_TZ", "")
	loc, err = ResolveTZ(WorkspaceTZConfig{})
	if err != nil {
		t.Fatalf("ResolveTZ local: %v", err)
	}
	if loc.String() != "LocalTest" {
		t.Fatalf("local fallback loc = %q, want LocalTest", loc.String())
	}
}

func TestResolveTZ_InvalidIANA(t *testing.T) {
	_, err := ResolveTZ(WorkspaceTZConfig{Timezone: "No/Such_Zone"})
	if !errors.Is(err, ErrInvalidTimezone) {
		t.Fatalf("ResolveTZ invalid config err = %v, want ErrInvalidTimezone", err)
	}

	t.Setenv("RELAYFILE_TZ", "No/Such_Zone")
	_, err = ResolveTZ(WorkspaceTZConfig{})
	if !errors.Is(err, ErrInvalidTimezone) {
		t.Fatalf("ResolveTZ invalid env err = %v, want ErrInvalidTimezone", err)
	}
}
