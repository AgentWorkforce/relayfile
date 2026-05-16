package digest

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ErrInvalidTimezone identifies configured timezone names that are not valid
// IANA locations. Callers should surface this instead of silently falling back
// to UTC or time.Local when a workspace explicitly configured a bad value.
var ErrInvalidTimezone = errors.New("digest: invalid timezone")

// WorkspaceTZConfig is the shared workspace timezone resolver input.
// Timezone is expected to be an IANA location such as "America/New_York".
type WorkspaceTZConfig struct {
	Timezone string
}

// ResolveTZ resolves the workspace timezone using the v1 precedence:
// workspace config > RELAYFILE_TZ > time.Local.
func ResolveTZ(config WorkspaceTZConfig) (*time.Location, error) {
	if raw := strings.TrimSpace(config.Timezone); raw != "" {
		loc, err := time.LoadLocation(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidTimezone, raw)
		}
		return loc, nil
	}
	if raw := strings.TrimSpace(os.Getenv("RELAYFILE_TZ")); raw != "" {
		loc, err := time.LoadLocation(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidTimezone, raw)
		}
		return loc, nil
	}
	if time.Local != nil {
		return time.Local, nil
	}
	return time.UTC, nil
}
