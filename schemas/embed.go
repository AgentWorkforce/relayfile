package schemas

import "embed"

// FS exposes the canonical schema assets for validation.
//
//go:embed README.md github/*.json
var FS embed.FS
