package mountscope

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	LayoutExact  = "exact"
	LayoutScoped = "scoped"

	RuntimeTopLevel     = ".relay"
	SkillsTopLevel      = ".skills"
	DigestsTopLevel     = "digests"
	GitTopLevel         = ".git"
	NodeModulesTopLevel = "node_modules"
	PermissionsFile     = "_PERMISSIONS.md"
)

var reservedLocalTopLevelNames = []string{
	RuntimeTopLevel,
	SkillsTopLevel,
	DigestsTopLevel,
	GitTopLevel,
	NodeModulesTopLevel,
	PermissionsFile,
}

var reservedLocalTopLevels = func() map[string]struct{} {
	out := make(map[string]struct{}, len(reservedLocalTopLevelNames))
	for _, name := range reservedLocalTopLevelNames {
		out[name] = struct{}{}
	}
	return out
}()

var reservedLocalTopLevelIdentities = func() map[string]struct{} {
	out := make(map[string]struct{}, len(reservedLocalTopLevelNames))
	for _, name := range reservedLocalTopLevelNames {
		out[localPathIdentity(name)] = struct{}{}
	}
	return out
}()

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

// ValidateEventProvider ensures one explicit provider filter can observe every
// configured root. A blank filter lets each Syncer infer its provider from its
// own root. A single root may intentionally use any server-supported filter,
// but a multi-root mount must not silently apply one provider to another
// provider's event feed.
func ValidateEventProvider(paths []string, provider string) error {
	normalized := NormalizePaths(paths, "/")
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || len(normalized) <= 1 {
		return nil
	}
	for _, remotePath := range normalized {
		segment := strings.TrimPrefix(remotePath, "/")
		if i := strings.IndexByte(segment, '/'); i >= 0 {
			segment = segment[:i]
		}
		if strings.ToLower(strings.TrimSpace(segment)) != provider {
			return fmt.Errorf(
				"--provider %s cannot filter multi-root mount containing %s; omit --provider so each scoped Syncer infers its own provider",
				provider,
				remotePath,
			)
		}
	}
	return nil
}

// ValidateExplicitPathsFile distinguishes an absent paths file from an
// explicitly configured file that contains no usable roots. The latter is an
// empty allowlist and must never fall through to the historical "/" fallback.
func ValidateExplicitPathsFile(pathsFile string, filePaths, directPaths []string) error {
	if strings.TrimSpace(pathsFile) == "" || len(directPaths) > 0 {
		return nil
	}
	for _, remotePath := range filePaths {
		if strings.TrimSpace(remotePath) != "" {
			return nil
		}
	}
	return fmt.Errorf(
		"paths-file %s contains no usable remote roots; refusing to widen an explicit empty allowlist to /",
		pathsFile,
	)
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
	if resolvedLayout == LayoutScoped {
		for i, left := range normalized {
			for _, right := range normalized[i+1:] {
				foldedLeft := localPathIdentity(left)
				foldedRight := localPathIdentity(right)
				if IsWithin(foldedLeft, foldedRight) || IsWithin(foldedRight, foldedLeft) {
					return nil, fmt.Errorf(
						"scoped remote roots %s and %s overlap on case- or normalization-insensitive filesystems; choose roots with distinct local path identities",
						left,
						right,
					)
				}
			}
		}
	}
	if len(normalized) > 1 && strings.TrimSpace(stateFile) != "" {
		return nil, fmt.Errorf("--state-file cannot be shared across multiple scoped mounts; use --state-dir instead")
	}
	scopes := make([]Scope, 0, len(normalized))
	for _, remotePath := range normalized {
		localDir := localRoot
		if resolvedLayout == LayoutScoped {
			if remotePath == "/" {
				return nil, fmt.Errorf(
					"workspace root / cannot use --local-layout=%s because it has no isolated child root; use --local-layout=%s",
					LayoutScoped,
					LayoutExact,
				)
			}
			segments := strings.Split(strings.TrimPrefix(remotePath, "/"), "/")
			firstSegment := segments[0]
			if IsReservedLocalTopLevelIdentity(firstSegment) {
				return nil, fmt.Errorf(
					"scoped remote root %s overlaps reserved local path %s; choose a non-reserved remote root or use --local-layout=%s for a single path",
					remotePath,
					filepath.Join(localRoot, filepath.FromSlash(firstSegment)),
					LayoutExact,
				)
			}
			for _, segment := range segments {
				if IsReservedRuntimeSegment(segment) {
					return nil, fmt.Errorf(
						"scoped remote root %s contains reserved mount runtime segment %s; choose a remote root outside mount runtime state",
						remotePath,
						segment,
					)
				}
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

// IsReservedLocalTopLevel reports whether an exact root-level name belongs to
// Relayfile bookkeeping or a host tool tree. Runtime consumers use exact
// matching so case-distinct provider paths remain valid on case-sensitive
// filesystems.
func IsReservedLocalTopLevel(name string) bool {
	_, ok := reservedLocalTopLevels[strings.TrimSpace(filepath.ToSlash(name))]
	return ok
}

// IsReservedLocalTopLevelIdentity reports whether a name would collide with a
// reserved top-level path on case- or normalization-insensitive filesystems.
// Mount planning uses this conservative identity check before it creates a
// scoped local path.
func IsReservedLocalTopLevelIdentity(name string) bool {
	_, ok := reservedLocalTopLevelIdentities[localPathIdentity(name)]
	return ok
}

// IsReservedRuntimeSegment reports whether a single remote path segment is
// owned by mount runtime state. These names are pruned anywhere in a remote
// path, so scoped roots containing one cannot be mounted as provider content.
func IsReservedRuntimeSegment(name string) bool {
	name = strings.TrimSpace(name)
	return name == RuntimeTopLevel ||
		name == ".relayfile-mount-state.json" ||
		strings.HasPrefix(name, ".relayfile-mount-state.json.tmp-")
}

// localPathIdentity models the equality rules that can collapse distinct
// remote spellings onto one local directory on common filesystems. It does
// not rewrite the remote path; it exists only for collision and reservation
// checks at the remote-to-local boundary.
func localPathIdentity(value string) string {
	return cases.Fold().String(norm.NFC.String(strings.TrimSpace(filepath.ToSlash(value))))
}
