package mountfuse

import (
	"syscall"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountsync"
)

const (
	layoutFilename      = "LAYOUT.md"
	layoutRevision      = "virtual-layout"
	layoutContentType   = "text/markdown; charset=utf-8"
	aliasByTitleSegment = "by-title"
	aliasByIDSegment    = "by-id"
	aliasByNameSegment  = "by-name"
	aliasByStateSegment = "by-state"
)

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

When lazy mode is enabled, the ` + "`github/repos/<owner>/<repo>`" + ` subtree is populated on first read via ` + "`LazyMaterialize`" + `. The first stat or directory read may incur one-time latency while the repo content is materialized.

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
		path:        layoutRemotePath(remoteRoot),
		name:        layoutFilename,
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
