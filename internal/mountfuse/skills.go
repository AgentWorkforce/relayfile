package mountfuse

import (
	"syscall"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountsync"
)

const (
	skillsDirname                = ".skills"
	activitySummaryFilename      = "activity-summary.md"
	activitySummaryRevision      = "virtual-activity-summary"
	activitySummaryContentType   = "text/markdown; charset=utf-8"
	ActivitySummarySkillMarkdown = `# activity-summary

Start activity questions from the digest surface before walking provider trees.

- Read ` + "`digests/yesterday.md`" + ` for yesterday, ` + "`digests/today.md`" + ` for today, or ` + "`digests/YYYY-MM-DD.md`" + ` for a specific UTC date.
- If a digest is missing or stale, run ` + "`relayfile digest rebuild --window yesterday`" + `, ` + "`--window today`" + `, ` + "`--window YYYY-MM-DD`" + `, ` + "`--window this-week`" + `, or ` + "`--window last-week`" + `.
- Use provider ` + "`_index.json`" + ` files for date filtering and only open individual entity files after the index has narrowed the set.
- Read ` + "`LAYOUT.md`" + ` and provider ` + "`<integration>/.layout.md`" + ` files when you need path conventions or alias views.
`
)

func skillsRemotePath(remoteRoot string) string {
	return joinRemotePath(remoteRoot, skillsDirname)
}

func activitySummaryRemotePath(remoteRoot string) string {
	return joinRemotePath(skillsRemotePath(remoteRoot), activitySummaryFilename)
}

func isVirtualSkillsDirPath(remoteRoot, remotePath string) bool {
	return normalizeRemotePath(remotePath) == skillsRemotePath(remoteRoot)
}

func isVirtualActivitySummaryPath(remoteRoot, remotePath string) bool {
	return normalizeRemotePath(remotePath) == activitySummaryRemotePath(remoteRoot)
}

func virtualSkillsDirMeta(remoteRoot string) nodeMeta {
	return nodeMeta{
		path:    skillsRemotePath(remoteRoot),
		name:    skillsDirname,
		mode:    syscall.S_IFDIR | 0o555,
		modTime: time.Unix(0, 0).UTC(),
	}
}

func virtualActivitySummaryMeta(remoteRoot string) nodeMeta {
	return nodeMeta{
		path:        activitySummaryRemotePath(remoteRoot),
		name:        activitySummaryFilename,
		mode:        syscall.S_IFREG | 0o444,
		revision:    activitySummaryRevision,
		size:        uint64(len(ActivitySummarySkillMarkdown)),
		modTime:     time.Unix(0, 0).UTC(),
		contentType: activitySummaryContentType,
	}
}

func readVirtualActivitySummary(remoteRoot string) mountsync.RemoteFile {
	meta := virtualActivitySummaryMeta(remoteRoot)
	return mountsync.RemoteFile{
		Path:        meta.path,
		Revision:    meta.revision,
		ContentType: meta.contentType,
		Content:     ActivitySummarySkillMarkdown,
	}
}

func virtualSkillsDirectory(remoteRoot string) map[string]nodeMeta {
	return map[string]nodeMeta{
		activitySummaryFilename: virtualActivitySummaryMeta(remoteRoot),
	}
}

func virtualRootDirectoryEntries(remoteRoot string, entries map[string]nodeMeta) {
	entries[layoutFilename] = virtualLayoutMeta(remoteRoot)
	entries[skillsDirname] = virtualSkillsDirMeta(remoteRoot)
}
