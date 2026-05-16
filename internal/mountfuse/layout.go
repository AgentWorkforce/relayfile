package mountfuse

import (
	"context"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountsync"
)

const (
	layoutFilename = "LAYOUT.md"
	layoutRevision = "virtual-layout"
	// providerLayoutFilename is the canonical per-provider layout filename.
	// PR39 Work Item A canonicalized on LAYOUT.md across runtime, fixtures,
	// and the workspace-layout skill contract. The legacy ".layout.md"
	// dotfile remains a virtual compatibility path for agents still following
	// the pre-canonical provider layout contract.
	providerLayoutFilename       = "LAYOUT.md"
	legacyProviderLayoutFilename = ".layout.md"
	providerLayoutRevision       = "virtual-provider-layout-v2"
	layoutContentType            = "text/markdown; charset=utf-8"
	aliasByTitleSegment          = "by-title"
	aliasByIDSegment             = "by-id"
	aliasByNameSegment           = "by-name"
	aliasByStateSegment          = "by-state"
	aliasByEditedSegment         = "by-edited"
	providerLayoutDeriveDepth    = 4
)

type LayoutManifest struct {
	Provider            string
	ResourceDirectories []string
	AliasSegments       []string
	WritebackResources  []string
	LazyMaterialization bool
}

const LayoutMarkdown = `# LAYOUT

This mount exposes upstream files together with navigation helpers.

## Indexes

` + "`_index.json`" + ` files live next to the content they index. Start with ` + "`notion/pages/_index.json`" + `, ` + "`linear/issues/_index.json`" + `, and ` + "`github/repos/_index.json`" + `.

## Find by title

To ` + "`find by title`" + `, read the relevant ` + "`_index.json`" + ` file, find the matching row, then read the named file from that row.

Direct title aliases also live under ` + "`notion/pages/" + aliasByTitleSegment + "/<title>.json`" + ` when that integration exports them.

## Find by id

Direct identifier aliases live under ` + "`linear/issues/" + aliasByIDSegment + "/<identifier>.json`" + ` when that integration exports them.

## Find by name

Direct name aliases live under ` + "`linear/users/" + aliasByNameSegment + "/<name>.json`" + ` and ` + "`github/repos/" + aliasByNameSegment + "/<owner>__<repo>.json`" + ` when those integrations export them.

## Find by state

State-grouped views live under ` + "`linear/issues/" + aliasByStateSegment + "/<state>/<file>.json`" + ` and ` + "`github/repos/<owner>/<repo>/issues/" + aliasByStateSegment + "/<state>/<file>.json`" + ` when those integrations export them.

## Find by edited date

Edited-date views live under ` + "`notion/pages/" + aliasByEditedSegment + "/YYYY-MM-DD/<file>.json`" + ` and equivalent issue or page resources when those integrations export them. Use these date buckets for activity-summary fallback lookups instead of recursive search.

## Filenames

Entity files use the ` + "`<sanitized-name>__<id>`" + ` filename convention. Recover the id from the last ` + "`__`" + `-separated segment.

## Lazy materialization

GitHub repo subtrees are synced eagerly by default. For huge-org workspaces, opt in to lazy mode with ` + "`--lazy-repos`" + ` or ` + "`RELAYFILE_LAZY_REPOS=true`" + ` to populate ` + "`github/repos/<owner>/<repo>`" + ` on first read via ` + "`LazyMaterialize`" + `. The first stat or directory read may incur one-time latency while the repo content is materialized.

## Integration-specific layouts

See per-integration ` + "`<integration>/LAYOUT.md`" + ` files for integration-specific tree shapes.
`

func layoutRemotePath(remoteRoot string) string {
	return joinRemotePath(remoteRoot, layoutFilename)
}

func isVirtualLayoutPath(remoteRoot, remotePath string) bool {
	return normalizeRemotePath(remotePath) == layoutRemotePath(remoteRoot)
}

func virtualLayoutMeta(remoteRoot string) nodeMeta {
	return nodeMeta{
		path: layoutRemotePath(remoteRoot),
		name: layoutFilename,
		// LAYOUT.md is a virtual read-only file; writes/deletes are not
		// supported, so advertise 0o444 to surface that in the mount.
		mode:        syscall.S_IFREG | 0o444,
		revision:    layoutRevision,
		size:        uint64(len(LayoutMarkdown)),
		modTime:     time.Unix(0, 0).UTC(),
		contentType: layoutContentType,
	}
}

func readVirtualLayout(remoteRoot string) mountsync.RemoteFile {
	meta := virtualLayoutMeta(remoteRoot)
	return mountsync.RemoteFile{
		Path:        meta.path,
		Revision:    meta.revision,
		ContentType: meta.contentType,
		Content:     LayoutMarkdown,
	}
}

func providerLayoutMarkdown(manifest LayoutManifest) string {
	manifest = normalizeLayoutManifest(manifest)
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(manifest.Provider)
	b.WriteString(" layout\n\n")
	b.WriteString("This file describes the mounted workspace layout for `")
	b.WriteString(manifest.Provider)
	b.WriteString("`.\n\n")
	b.WriteString("## Resource directories\n\n")
	if len(manifest.ResourceDirectories) == 0 {
		b.WriteString("_No resource directories have been advertised yet._\n\n")
	} else {
		for _, resource := range manifest.ResourceDirectories {
			b.WriteString("- `")
			b.WriteString(resource)
			b.WriteString("/`\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## Filenames\n\n")
	b.WriteString("Entity files use the `<identifier>__<uuid>.json` convention when a stable provider identifier is available. Recover the provider id from the last `__`-separated segment.\n\n")
	b.WriteString("## Alias indexes\n\n")
	if len(manifest.AliasSegments) == 0 {
		b.WriteString("_No alias indexes have been advertised yet._\n\n")
	} else {
		for _, segment := range manifest.AliasSegments {
			b.WriteString("- `")
			b.WriteString(segment)
			b.WriteString("/`\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## Writeback\n\n")
	if len(manifest.WritebackResources) == 0 {
		b.WriteString("_No writeback resources have been advertised yet._\n\n")
	} else {
		for _, resource := range manifest.WritebackResources {
			b.WriteString("- `")
			b.WriteString(resource)
			b.WriteString("/` (schema: `")
			b.WriteString(resource)
			b.WriteString("/.schema.json`)\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("Use `wb-<timestamp>.json` as the recommended filename for agent-authored writeback payloads.\n\n")
	b.WriteString("## Materialization\n\n")
	if manifest.LazyMaterialization {
		b.WriteString("This provider may materialize large subtrees lazily on first access.\n")
	} else {
		b.WriteString("This provider is expected to expose its advertised tree eagerly.\n")
	}
	return b.String()
}

func providerLayoutRemotePath(remoteRoot, provider string) string {
	return joinRemotePath(joinRemotePath(remoteRoot, provider), providerLayoutFilename)
}

func legacyProviderLayoutRemotePath(remoteRoot, provider string) string {
	return joinRemotePath(joinRemotePath(remoteRoot, provider), legacyProviderLayoutFilename)
}

func isProviderLayoutFilename(filename string) bool {
	return filename == providerLayoutFilename || filename == legacyProviderLayoutFilename
}

func isVirtualProviderLayoutPath(remoteRoot, remotePath string) (string, bool) {
	remotePath = normalizeRemotePath(remotePath)
	if !isProviderLayoutFilename(path.Base(remotePath)) {
		return "", false
	}
	parentPath, _ := splitParent(remotePath)
	return providerRootSegment(remoteRoot, parentPath)
}

func virtualProviderLayoutMeta(remoteRoot string, manifest LayoutManifest) nodeMeta {
	return virtualProviderLayoutMetaForFilename(remoteRoot, manifest, providerLayoutFilename)
}

func legacyVirtualProviderLayoutMeta(remoteRoot string, manifest LayoutManifest) nodeMeta {
	return virtualProviderLayoutMetaForFilename(remoteRoot, manifest, legacyProviderLayoutFilename)
}

func virtualProviderLayoutMetaForFilename(remoteRoot string, manifest LayoutManifest, filename string) nodeMeta {
	manifest = normalizeLayoutManifest(manifest)
	if !isProviderLayoutFilename(filename) {
		filename = providerLayoutFilename
	}
	content := providerLayoutMarkdown(manifest)
	return nodeMeta{
		path:        joinRemotePath(joinRemotePath(remoteRoot, manifest.Provider), filename),
		name:        filename,
		mode:        syscall.S_IFREG | 0o444,
		revision:    providerLayoutRevision,
		size:        uint64(len(content)),
		modTime:     time.Unix(0, 0).UTC(),
		contentType: layoutContentType,
	}
}

func readVirtualProviderLayout(remoteRoot string, manifest LayoutManifest) mountsync.RemoteFile {
	return readVirtualProviderLayoutForFilename(remoteRoot, manifest, providerLayoutFilename)
}

func readLegacyVirtualProviderLayout(remoteRoot string, manifest LayoutManifest) mountsync.RemoteFile {
	return readVirtualProviderLayoutForFilename(remoteRoot, manifest, legacyProviderLayoutFilename)
}

func readVirtualProviderLayoutForFilename(remoteRoot string, manifest LayoutManifest, filename string) mountsync.RemoteFile {
	meta := virtualProviderLayoutMetaForFilename(remoteRoot, manifest, filename)
	return mountsync.RemoteFile{
		Path:        meta.path,
		Revision:    meta.revision,
		ContentType: meta.contentType,
		Content:     providerLayoutMarkdown(manifest),
	}
}

func normalizeLayoutManifests(in map[string]LayoutManifest) map[string]LayoutManifest {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]LayoutManifest, len(in))
	for key, manifest := range in {
		if manifest.Provider == "" {
			manifest.Provider = key
		}
		manifest = normalizeLayoutManifest(manifest)
		if manifest.Provider == "" {
			continue
		}
		out[manifest.Provider] = manifest
	}
	return out
}

func normalizeLayoutManifest(manifest LayoutManifest) LayoutManifest {
	manifest.Provider = strings.TrimSpace(manifest.Provider)
	manifest.ResourceDirectories = cleanUniqueSorted(manifest.ResourceDirectories)
	manifest.AliasSegments = cleanUniqueSorted(manifest.AliasSegments)
	manifest.WritebackResources = cleanUniqueSorted(manifest.WritebackResources)
	return manifest
}

func (s *fsState) layoutManifest(provider string) LayoutManifest {
	provider = strings.TrimSpace(provider)
	if manifest, ok := s.configuredLayoutManifest(provider); ok {
		return manifest
	}
	return LayoutManifest{
		Provider: provider,
	}
}

func (s *fsState) configuredLayoutManifest(provider string) (LayoutManifest, bool) {
	provider = strings.TrimSpace(provider)
	if provider == "" || len(s.manifests) == 0 {
		return LayoutManifest{}, false
	}
	manifest, ok := s.manifests[provider]
	return manifest, ok
}

func (s *fsState) providerLayoutManifest(ctx context.Context, provider string) LayoutManifest {
	provider = strings.TrimSpace(provider)
	if manifest, ok := s.configuredLayoutManifest(provider); ok {
		return manifest
	}
	manifest, err := s.deriveProviderLayoutManifest(ctx, provider)
	if err != nil {
		s.logf("derive provider layout for %s: %v", provider, err)
		return LayoutManifest{Provider: provider}
	}
	return manifest
}

func (s *fsState) deriveProviderLayoutManifest(ctx context.Context, provider string) (LayoutManifest, error) {
	manifest := LayoutManifest{
		Provider:            strings.TrimSpace(provider),
		LazyMaterialization: s.lazyRepos != nil && strings.TrimSpace(provider) == "github",
	}
	if manifest.Provider == "" || s.client == nil {
		return manifest, nil
	}

	resources := map[string]struct{}{}
	aliases := map[string]struct{}{}
	providerRoot := joinRemotePath(s.remoteRoot, manifest.Provider)
	cursor := ""
	for {
		resp, err := s.client.ListTree(ctx, s.workspaceID, providerRoot, providerLayoutDeriveDepth, cursor)
		if err != nil {
			return manifest, err
		}
		for _, entry := range resp.Entries {
			rel, ok := relativeToRemoteRoot(providerRoot, entry.Path)
			if !ok || rel == "" {
				continue
			}
			segments := providerLayoutPathSegments(rel)
			if len(segments) == 0 {
				continue
			}
			if providerLayoutResourceSegment(segments[0]) != "" && (len(segments) > 1 || isTreeDirectory(entry.Type)) {
				resources[segments[0]] = struct{}{}
			}
			for _, segment := range segments[1:] {
				if isProviderLayoutAliasSegment(segment) {
					aliases[segment] = struct{}{}
				}
			}
		}
		if resp.NextCursor == nil || strings.TrimSpace(*resp.NextCursor) == "" {
			break
		}
		cursor = strings.TrimSpace(*resp.NextCursor)
	}

	for resource := range resources {
		manifest.ResourceDirectories = append(manifest.ResourceDirectories, resource)
	}
	for alias := range aliases {
		manifest.AliasSegments = append(manifest.AliasSegments, alias)
	}
	return normalizeLayoutManifest(manifest), nil
}

func isTreeDirectory(entryType string) bool {
	return strings.EqualFold(entryType, "directory") || strings.EqualFold(entryType, "dir")
}

func providerLayoutPathSegments(remotePath string) []string {
	remotePath = strings.Trim(remotePath, "/")
	if remotePath == "" {
		return nil
	}
	return strings.Split(remotePath, "/")
}

func providerLayoutResourceSegment(segment string) string {
	segment = strings.TrimSpace(segment)
	switch {
	case segment == "":
		return ""
	case isProviderLayoutAliasSegment(segment):
		return ""
	case isProviderLayoutReservedSegment(segment):
		return ""
	default:
		return segment
	}
}

func isProviderLayoutAliasSegment(segment string) bool {
	switch strings.TrimSpace(segment) {
	case aliasByTitleSegment, aliasByIDSegment, aliasByNameSegment, aliasByStateSegment, aliasByEditedSegment:
		return true
	default:
		return false
	}
}

func isProviderLayoutReservedSegment(segment string) bool {
	switch strings.TrimSpace(segment) {
	case "", ".relay", "digests", "_index.json", layoutFilename, legacyProviderLayoutFilename, ".relayfile-mount-state.json":
		return true
	default:
		return false
	}
}

func providerRootSegment(remoteRoot, remotePath string) (string, bool) {
	rel, ok := relativeToRemoteRoot(remoteRoot, remotePath)
	if !ok || rel == "" || strings.Contains(rel, "/") {
		return "", false
	}
	return rel, true
}

func relativeToRemoteRoot(remoteRoot, remotePath string) (string, bool) {
	remoteRoot = normalizeRemotePath(remoteRoot)
	remotePath = normalizeRemotePath(remotePath)
	if !isUnderRemoteRoot(remoteRoot, remotePath) {
		return "", false
	}
	if remotePath == remoteRoot {
		return "", true
	}
	if remoteRoot == "/" {
		return strings.TrimPrefix(remotePath, "/"), true
	}
	return strings.TrimPrefix(remotePath, remoteRoot+"/"), true
}

func cleanUniqueSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.Trim(strings.TrimSpace(value), "/")
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		cleaned = append(cleaned, value)
	}
	sort.Strings(cleaned)
	return cleaned
}
