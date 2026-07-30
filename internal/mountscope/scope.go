package mountscope

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	NodeModulesTopLevel = "node_modules"
	PermissionsFile     = "_PERMISSIONS.md"
)

type localTopLevelPolicy struct {
	name           string
	catalogOwned   bool
	infrastructure bool
}

// localTopLevelPolicies is the single inventory for names Relayfile must
// reserve at a mount boundary. Infrastructure roots are incidental metadata
// owned by source-control tools and can carry repository credentials; catalog
// roots are Relayfile bookkeeping and are reserved only at an exact/catalog
// root. Every derived predicate below comes from this table so planning,
// runtime exclusion, and the user-visible exclusion summary cannot drift.
var localTopLevelPolicies = []localTopLevelPolicy{
	{name: RuntimeTopLevel, catalogOwned: true},
	{name: SkillsTopLevel, catalogOwned: true},
	{name: DigestsTopLevel, catalogOwned: true},
	{name: NodeModulesTopLevel, catalogOwned: true},
	{name: PermissionsFile, catalogOwned: true},
	{name: ".git", infrastructure: true},
	{name: ".hg", infrastructure: true},
	{name: ".svn", infrastructure: true},
	{name: ".bzr", infrastructure: true},
	{name: "_darcs", infrastructure: true},
	{name: ".jj", infrastructure: true},
}

var reservedLocalTopLevels = func() map[string]localTopLevelPolicy {
	out := make(map[string]localTopLevelPolicy, len(localTopLevelPolicies))
	for _, policy := range localTopLevelPolicies {
		out[policy.name] = policy
	}
	return out
}()

var reservedLocalTopLevelIdentities = func() map[string]struct{} {
	out := make(map[string]struct{}, len(localTopLevelPolicies))
	for _, policy := range localTopLevelPolicies {
		out[localPathIdentity(policy.name)] = struct{}{}
	}
	return out
}()

var infrastructureTopLevelIdentities = func() map[string]struct{} {
	out := make(map[string]struct{})
	for _, policy := range localTopLevelPolicies {
		if policy.infrastructure {
			out[localPathIdentity(policy.name)] = struct{}{}
		}
	}
	return out
}()

// LocalContentPolicyReport describes existing top-level content that a mount
// must either exclude visibly or surface as convention-sensitive user data.
type LocalContentPolicyReport struct {
	ExcludedInfrastructure []string
	SensitiveUserContent   []string
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
	providerRoot := ProviderRoot(provider)
	for _, remotePath := range normalized {
		segment := strings.TrimPrefix(remotePath, "/")
		if i := strings.IndexByte(segment, '/'); i >= 0 {
			segment = segment[:i]
		}
		if strings.ToLower(strings.TrimSpace(segment)) != providerRoot {
			return fmt.Errorf(
				"--provider %s cannot filter multi-root mount containing %s; omit --provider so each scoped Syncer infers its own provider",
				provider,
				remotePath,
			)
		}
	}
	return nil
}

// ProviderRoot maps public provider identifiers to the top-level VFS segment
// they own. Provider filters and CLI cleanup share this owner so aliases whose
// ids differ from their storage roots cannot drift between planning and use.
func ProviderRoot(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "slack", "slack-sage":
		return "slack"
	case "slack-my-senior-dev":
		return "slack-msd"
	case "slack-nightcto":
		return "slack-nightcto"
	default:
		return provider
	}
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
		if err := ValidateScopedPathSet(normalized); err != nil {
			return nil, err
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
				if IsInfrastructureTopLevelIdentity(segment) {
					return nil, fmt.Errorf(
						"scoped remote root %s contains incidental infrastructure segment %s, which is excluded from mounts; choose a content root outside source-control metadata",
						remotePath,
						segment,
					)
				}
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

// ValidateScopedPathSet rejects roots that can collapse or overlap when a
// configuration moves between case- or normalization-sensitive filesystems.
// It is also used when reading persisted pre-planner configurations so a
// nested historical child cannot be normalized away with its private state
// left behind.
func ValidateScopedPathSet(paths []string) error {
	normalized := make([]string, 0, len(paths))
	for _, remotePath := range paths {
		if strings.TrimSpace(remotePath) == "" {
			return fmt.Errorf("scoped remote roots contain a blank entry whose prior scope is unknown")
		}
		normalized = append(normalized, NormalizePath(remotePath))
	}
	if len(normalized) == 0 {
		return fmt.Errorf("scoped remote roots are empty, so the prior scope is unknown")
	}
	for i, left := range normalized {
		for _, right := range normalized[i+1:] {
			foldedLeft := localPathIdentity(left)
			foldedRight := localPathIdentity(right)
			if IsWithin(foldedLeft, foldedRight) || IsWithin(foldedRight, foldedLeft) {
				return fmt.Errorf(
					"scoped remote roots %s and %s overlap on case- or normalization-insensitive filesystems; choose roots with distinct local path identities",
					left,
					right,
				)
			}
		}
	}
	return nil
}

// IsReservedLocalTopLevel reports whether an exact root-level name belongs to
// Relayfile bookkeeping or a host tool tree. Runtime consumers use exact
// matching so case-distinct provider paths remain valid on case-sensitive
// filesystems.
func IsReservedLocalTopLevel(name string) bool {
	_, ok := reservedLocalTopLevels[strings.TrimSpace(filepath.ToSlash(name))]
	return ok
}

// IsCatalogOwnedTopLevel reports whether an exact root-level name is
// Relayfile-owned bookkeeping. Unlike incidental infrastructure, these names
// remain ordinary provider content under an isolated scoped child.
func IsCatalogOwnedTopLevel(name string) bool {
	policy, ok := reservedLocalTopLevels[strings.TrimSpace(filepath.ToSlash(name))]
	return ok && policy.catalogOwned
}

// IsReservedLocalTopLevelIdentity reports whether a name would collide with a
// reserved top-level path on case- or normalization-insensitive filesystems.
// Mount planning uses this conservative identity check before it creates a
// scoped local path.
func IsReservedLocalTopLevelIdentity(name string) bool {
	_, ok := reservedLocalTopLevelIdentities[localPathIdentity(name)]
	return ok
}

// IsInfrastructureTopLevelIdentity reports whether a path segment could
// collide with an incidental source-control metadata root on a case- or
// normalization-insensitive filesystem. Planning is deliberately
// host-independent so a portable scoped config cannot become unsafe later.
func IsInfrastructureTopLevelIdentity(name string) bool {
	_, ok := infrastructureTopLevelIdentities[localPathIdentity(name)]
	return ok
}

// IsInfrastructureTopLevelAt reports whether name is the actual incidental
// infrastructure entry on this filesystem. Exact spelling is enough on every
// host; existing aliases use os.SameFile, while absent aliases use the case
// behavior of an existing ancestor. That keeps a future `.Git` from creating
// `.git` on an insensitive filesystem without excluding case-distinct user
// content on a sensitive filesystem.
func IsInfrastructureTopLevelAt(localRoot, name string) bool {
	name = strings.TrimSpace(filepath.ToSlash(name))
	policy, exact := reservedLocalTopLevels[name]
	if exact && policy.infrastructure {
		return true
	}
	candidatePath := filepath.Join(localRoot, filepath.FromSlash(name))
	candidateInfo, err := os.Stat(candidatePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err == nil {
		for _, policy := range localTopLevelPolicies {
			if !policy.infrastructure {
				continue
			}
			canonicalInfo, err := os.Stat(filepath.Join(localRoot, policy.name))
			if err == nil && os.SameFile(candidateInfo, canonicalInfo) {
				return true
			}
		}
	}
	return IsInfrastructureTopLevelIdentity(name) && filesystemFoldsPathCaseAt(localRoot)
}

func filesystemFoldsPathCaseAt(path string) bool {
	current := filepath.Clean(path)
	for {
		info, err := os.Stat(current)
		if err == nil {
			base := filepath.Base(current)
			for index := 0; index < len(base); index++ {
				flipped := base[index]
				switch {
				case flipped >= 'a' && flipped <= 'z':
					flipped -= 'a' - 'A'
				case flipped >= 'A' && flipped <= 'Z':
					flipped += 'a' - 'A'
				default:
					continue
				}
				aliasBase := base[:index] + string(flipped) + base[index+1:]
				aliasInfo, aliasErr := os.Stat(filepath.Join(filepath.Dir(current), aliasBase))
				if aliasErr == nil {
					return os.SameFile(info, aliasInfo)
				}
				if errors.Is(aliasErr, os.ErrNotExist) {
					return false
				}
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}

// InfrastructureTopLevelNames returns the exclusion inventory derived from
// localTopLevelPolicies. Callers use this for docs and diagnostics rather than
// maintaining a second list.
func InfrastructureTopLevelNames() []string {
	names := make([]string, 0)
	for _, policy := range localTopLevelPolicies {
		if policy.infrastructure {
			names = append(names, policy.name)
		}
	}
	sort.Strings(names)
	return names
}

// PortableLocalPathIdentity returns the conservative identity used when a
// process-wide boundary (such as the mount-start lock) must remain stable
// across case- and normalization-insensitive filesystems.
func PortableLocalPathIdentity(path string) string {
	return localPathIdentity(filepath.Clean(path))
}

// InspectLocalContentPolicy enumerates existing top-level paths whose mount
// treatment must be visible. Infrastructure is excluded; convention-sensitive
// files remain user content and are only surfaced so the operator can decide.
func InspectLocalContentPolicy(localRoot string) (LocalContentPolicyReport, error) {
	var report LocalContentPolicyReport
	entries, err := os.ReadDir(localRoot)
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return report, err
	}
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case IsInfrastructureTopLevelAt(localRoot, name):
			report.ExcludedInfrastructure = append(report.ExcludedInfrastructure, name)
		case isConventionSensitiveUserContent(name):
			report.SensitiveUserContent = append(report.SensitiveUserContent, name)
		}
	}
	return report, nil
}

func isConventionSensitiveUserContent(name string) bool {
	name = strings.TrimSpace(name)
	return name == ".env" ||
		strings.HasPrefix(name, ".env.") ||
		name == ".npmrc" ||
		name == ".pypirc" ||
		name == ".netrc" ||
		name == ".git-credentials"
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
