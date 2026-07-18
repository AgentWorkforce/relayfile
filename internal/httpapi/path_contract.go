package httpapi

import "strings"

// This file codifies the scope-path grammar defined in contract/README.md
// (Relayfile Path Contract v1). The grammar is exactly what scopePathMatches
// enforces at runtime: suffix globs only. These helpers are the reference
// implementation for the `valid` and `mounts_at` fixture expectations; they
// do not change any runtime verdict — an invalid scope path already matches
// nothing, because its glob characters are treated as literal bytes.

// scopePathValid reports whether a scope path conforms to the contract
// grammar: the literal "*", or an absolute path whose segments are non-empty,
// are not "." or "..", and where "*" appears only in the final segment as
// exactly "*", exactly "**", or a single trailing "*" on a non-empty name.
func scopePathValid(scopePath string) bool {
	if scopePath == "*" {
		return true
	}
	if !strings.HasPrefix(scopePath, "/") {
		return false
	}
	if scopePath == "/" {
		return true
	}
	segments := strings.Split(scopePath[1:], "/")
	for i, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		if i != len(segments)-1 {
			if strings.Contains(segment, "*") {
				return false
			}
			continue
		}
		switch {
		case !strings.Contains(segment, "*"):
		case segment == "*" || segment == "**":
		case strings.Count(segment, "*") == 1 && strings.HasSuffix(segment, "*"):
		default:
			return false
		}
	}
	return true
}

// scopePathMountRoot returns the deepest concrete directory implied by a
// valid scope path — where a mount client may anchor the grant locally.
// Invalid scope paths have no mount root; callers must surface that as an
// explicit error, never as a silently dropped path.
func scopePathMountRoot(scopePath string) (string, bool) {
	if !scopePathValid(scopePath) {
		return "", false
	}
	if scopePath == "*" || scopePath == "/" {
		return "/", true
	}
	switch {
	case strings.HasSuffix(scopePath, "/**"):
		return orRoot(strings.TrimSuffix(scopePath, "/**")), true
	case strings.HasSuffix(scopePath, "/*"):
		return orRoot(strings.TrimSuffix(scopePath, "/*")), true
	case strings.HasSuffix(scopePath, "*"):
		prefix := strings.TrimSuffix(scopePath, "*")
		return orRoot(prefix[:strings.LastIndex(prefix, "/")]), true
	default:
		return scopePath, true
	}
}

func orRoot(dir string) string {
	if dir == "" {
		return "/"
	}
	return dir
}
