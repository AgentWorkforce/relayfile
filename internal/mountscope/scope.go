package mountscope

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	LayoutExact  = "exact"
	LayoutScoped = "scoped"
)

var reservedScopedLocalTopLevels = map[string]struct{}{
	".git":         {},
	".relay":       {},
	".skills":      {},
	"digests":      {},
	"node_modules": {},
}

// StringListFlag preserves every occurrence of a repeatable string flag.
type StringListFlag []string

func (f *StringListFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *StringListFlag) Set(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed != "" {
		*f = append(*f, trimmed)
	}
	return nil
}

func (f StringListFlag) Values() []string {
	return append([]string(nil), f...)
}

// ResolveLayout validates and normalizes a local mount layout.
func ResolveLayout(layout string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(layout))
	if normalized == "" {
		return LayoutExact, nil
	}
	switch normalized {
	case LayoutExact, LayoutScoped:
		return normalized, nil
	default:
		return "", fmt.Errorf("%q (supported: %s, %s)", layout, LayoutExact, LayoutScoped)
	}
}

// ReadPathsFile reads remote roots from a JSON array or newline-delimited file.
func ReadPathsFile(filePath string) ([]string, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return nil, nil
	}
	payload, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" {
		return nil, nil
	}
	var jsonPaths []string
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal(payload, &jsonPaths); err != nil {
			return nil, err
		}
		return jsonPaths, nil
	}
	var paths []string
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		paths = append(paths, line)
	}
	return paths, nil
}

// NormalizePaths cleans, deduplicates, and collapses overlapping remote roots.
func NormalizePaths(paths []string, fallback string) []string {
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(paths))
	for _, remotePath := range paths {
		// Empty entries are not roots. In particular, a blank/null entry from
		// a JSON paths file must not normalize to "/" and silently widen an
		// otherwise scoped allowlist.
		if strings.TrimSpace(remotePath) == "" {
			continue
		}
		cleaned := NormalizePath(remotePath)
		if _, ok := seen[cleaned]; ok {
			continue
		}
		redundant := false
		for _, existing := range normalized {
			if IsWithin(existing, cleaned) {
				redundant = true
				break
			}
		}
		if redundant {
			continue
		}
		kept := normalized[:0]
		for _, existing := range normalized {
			if IsWithin(cleaned, existing) {
				delete(seen, existing)
				continue
			}
			kept = append(kept, existing)
		}
		normalized = kept
		seen[cleaned] = struct{}{}
		normalized = append(normalized, cleaned)
	}
	if len(normalized) == 0 {
		return []string{NormalizePath(fallback)}
	}
	return normalized
}

// IsWithin reports whether candidate is equal to or nested beneath root.
func IsWithin(root, candidate string) bool {
	root = NormalizePath(root)
	candidate = NormalizePath(candidate)
	if root == "/" {
		return true
	}
	return candidate == root || strings.HasPrefix(candidate, root+"/")
}

// FirstPath returns the first normalized root, applying fallback when needed.
func FirstPath(paths []string, fallback string) string {
	return NormalizePaths(paths, fallback)[0]
}

// NormalizePath converts a remote path to a cleaned absolute slash path.
func NormalizePath(remotePath string) string {
	trimmed := strings.TrimSpace(remotePath)
	if trimmed == "" || trimmed == "/" {
		return "/"
	}
	trimmed = strings.ReplaceAll(trimmed, "\\", "/")
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	cleaned := filepath.Clean(trimmed)
	if cleaned == "." || cleaned == string(filepath.Separator) {
		return "/"
	}
	return filepath.ToSlash(cleaned)
}

// LocalDir maps a remote root to its directory beneath a scoped local root.
func LocalDir(localRoot, remotePath string) string {
	remotePath = NormalizePath(remotePath)
	if remotePath == "/" {
		return localRoot
	}
	return filepath.Join(localRoot, filepath.FromSlash(strings.TrimPrefix(remotePath, "/")))
}

// Scope binds one normalized remote root to its isolated local root.
type Scope struct {
	RemotePath string
	LocalDir   string
}

// Plan resolves the remote allowlist into non-overlapping local mount roots.
// Exact layout preserves the historical single-root behavior; multiple roots
// require scoped layout so their files and private .relay state cannot collide.
func Plan(localRoot, layout string, paths []string, fallback, stateFile string) ([]Scope, error) {
	resolvedLayout, err := ResolveLayout(layout)
	if err != nil {
		return nil, err
	}
	normalized := NormalizePaths(paths, fallback)
	if len(normalized) > 1 && resolvedLayout != LayoutScoped {
		return nil, fmt.Errorf("multiple --remote-path values require --local-layout=%s", LayoutScoped)
	}
	if len(normalized) > 1 && strings.TrimSpace(stateFile) != "" {
		return nil, fmt.Errorf("--state-file cannot be shared across multiple scoped mounts; use --state-dir instead")
	}
	scopes := make([]Scope, 0, len(normalized))
	for _, remotePath := range normalized {
		localDir := localRoot
		if resolvedLayout == LayoutScoped {
			firstSegment := strings.SplitN(strings.TrimPrefix(remotePath, "/"), "/", 2)[0]
			if _, reserved := reservedScopedLocalTopLevels[firstSegment]; reserved {
				return nil, fmt.Errorf(
					"scoped remote root %s overlaps reserved local path %s; choose a non-reserved remote root or use --local-layout=%s for a single path",
					remotePath,
					filepath.Join(localRoot, filepath.FromSlash(firstSegment)),
					LayoutExact,
				)
			}
			localDir = LocalDir(localRoot, remotePath)
		}
		scopes = append(scopes, Scope{
			RemotePath: remotePath,
			LocalDir:   localDir,
		})
	}
	return scopes, nil
}
