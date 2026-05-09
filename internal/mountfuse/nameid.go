package mountfuse

import (
	"path"
	"strings"
)

// ID is the LAST `__`-separated segment.
func ParseNameID(basename string) (name, id string) {
	base := strings.TrimSpace(basename)
	if base == "" || base == "/" || base == "." {
		return "", ""
	}
	base = path.Base(base)
	if base == "" || base == "/" || base == "." {
		return "", ""
	}
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" {
		return "", ""
	}
	idx := strings.LastIndex(stem, "__")
	if idx < 0 {
		return "", stem
	}
	return stem[:idx], stem[idx+2:]
}

func IDFromBasename(basename string) string {
	_, id := ParseNameID(basename)
	return id
}
