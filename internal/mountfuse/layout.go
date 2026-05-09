package mountfuse

import (
	"syscall"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountsync"
)

const (
	layoutFilename    = "LAYOUT.md"
	layoutRevision    = "virtual-layout"
	layoutContentType = "text/markdown; charset=utf-8"
)

const LayoutMarkdown = `# LAYOUT

This mount exposes upstream files together with navigation helpers.

## Indexes

` + "`_index.json`" + ` files live next to the content they index. Start with ` + "`notion/pages/_index.json`" + `, ` + "`linear/issues/_index.json`" + `, and ` + "`github/repos/_index.json`" + `.

## Find by title

To ` + "`find by title`" + `, read the relevant ` + "`_index.json`" + ` file, find the matching row, then read the named file from that row.

## Filenames

Entity files use the ` + "`<sanitized-name>__<id>`" + ` filename convention. Recover the id from the last ` + "`__`" + `-separated segment.

## Integration-specific layouts

See per-integration ` + "`<integration>/.layout.md`" + ` files for integration-specific tree shapes.

## Forward note

Direct by-title / by-id aliases land in a later release.
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
		mode:        syscall.S_IFREG | defaultFileMode,
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
