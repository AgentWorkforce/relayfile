package mountfuse

import (
	"path"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountsync"
)

const (
	layoutFilename         = "LAYOUT.md"
	layoutRevision         = "virtual-layout"
	providerLayoutFilename = ".layout.md"
	providerLayoutRevision = "virtual-provider-layout"
	layoutContentType      = "text/markdown; charset=utf-8"
	aliasByTitleSegment    = "by-title"
	aliasByIDSegment       = "by-id"
	aliasByNameSegment     = "by-name"
	aliasByStateSegment    = "by-state"
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

## Filenames

Entity files use the ` + "`<sanitized-name>__<id>`" + ` filename convention. Recover the id from the last ` + "`__`" + `-separated segment.

## Lazy materialization

GitHub repo subtrees are synced eagerly by default. For huge-org workspaces, opt in to lazy mode with ` + "`--lazy-repos`" + ` or ` + "`RELAYFILE_LAZY_REPOS=true`" + ` to populate ` + "`github/repos/<owner>/<repo>`" + ` on first read via ` + "`LazyMaterialize`" + `. The first stat or directory read may incur one-time latency while the repo content is materialized.

## Integration-specific layouts

See per-integration ` + "`<integration>/.layout.md`" + ` files for integration-specific tree shapes.
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
	b.WriteString("Entity files use the `<identifier>__<uuid>.json` convention when a stable provider identifier is available.\n\n")
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
			b.WriteString("/.schema.json`\n")
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

func isVirtualProviderLayoutPath(remoteRoot, remotePath string) (string, bool) {
	remotePath = normalizeRemotePath(remotePath)
	if path.Base(remotePath) != providerLayoutFilename {
		return "", false
	}
	parentPath, _ := splitParent(remotePath)
	return providerRootSegment(remoteRoot, parentPath)
}

func virtualProviderLayoutMeta(remoteRoot string, manifest LayoutManifest) nodeMeta {
	manifest = normalizeLayoutManifest(manifest)
	content := providerLayoutMarkdown(manifest)
	return nodeMeta{
		path:        providerLayoutRemotePath(remoteRoot, manifest.Provider),
		name:        providerLayoutFilename,
		mode:        syscall.S_IFREG | 0o444,
		revision:    providerLayoutRevision,
		size:        uint64(len(content)),
		modTime:     time.Unix(0, 0).UTC(),
		contentType: layoutContentType,
	}
}

func readVirtualProviderLayout(remoteRoot string, manifest LayoutManifest) mountsync.RemoteFile {
	meta := virtualProviderLayoutMeta(remoteRoot, manifest)
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
	if manifest, ok := s.manifests[provider]; ok {
		return manifest
	}
	return LayoutManifest{
		Provider: provider,
		AliasSegments: []string{
			aliasByIDSegment,
			aliasByNameSegment,
			aliasByStateSegment,
			aliasByTitleSegment,
		},
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
