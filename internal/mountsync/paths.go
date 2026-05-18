package mountsync

import (
	"fmt"
	"path/filepath"
	"strings"
)

// MaxRemotePathLen is the upper bound (in bytes) on a single relative
// remote path. It exists to defend against a misbehaving provider
// returning a multi-KB path that would otherwise be propagated through
// the rest of the pipeline (state files, log lines, OS path APIs). The
// limit matches typical POSIX PATH_MAX so it does not artificially
// exclude any path the underlying filesystem could actually represent.
const MaxRemotePathLen = 4096

// RelativeRemotePath is a typed wrapper around the relative-path form used
// when constructing remote paths from local files (and vice versa). It
// exists to make a class of clobber bugs impossible by construction:
//
//   - the empty path / "." cannot be produced — the constructor rejects
//     them, so a writeback enqueue can never accidentally target the
//     mount root;
//   - paths whose only segment equals the mount-dir basename are rejected
//     up-front, mirroring the round-trip guard in localToRemotePath;
//   - parent traversal ("..", "../foo") is rejected before any join.
//
// The value carries the slash-separated relative form (always with "/"
// separators, never the OS separator) so it round-trips cleanly across
// the local-to-remote boundary.
type RelativeRemotePath struct {
	// rel is the cleaned, slash-separated relative path. Never empty,
	// never ".", never starts with "/" or "..".
	rel string
}

// String returns the slash-separated relative path.
func (p RelativeRemotePath) String() string { return p.rel }

// Slash returns the slash-separated form (alias kept for clarity at
// callsites that want to be explicit).
func (p RelativeRemotePath) Slash() string { return p.rel }

// IsZero reports whether p was never assigned a value via the constructor.
func (p RelativeRemotePath) IsZero() bool { return p.rel == "" }

// NewRelativeRemotePath builds a typed relative path. The mountBasename
// argument is the basename of the local mount directory; the constructor
// uses it to reject the single-segment round-trip-onto-root collision
// (a child whose name equals the mount-dir basename, which would map back
// onto the mount root via remoteToLocalPath).
//
// rel must be a non-empty, non-traversing relative path. The constructor
// accepts either OS-separator or slash-separator input and always
// normalizes to slashes.
func NewRelativeRemotePath(rel, mountBasename string) (RelativeRemotePath, error) {
	if rel == "" {
		return RelativeRemotePath{}, fmt.Errorf("relative path is empty")
	}
	// NUL bytes are silently truncated by some libc / syscall paths,
	// which turns "safe.txt\x00/../../etc/passwd" into a confused-deputy
	// hazard. A misbehaving provider must never be able to plant one.
	if strings.IndexByte(rel, 0) >= 0 {
		return RelativeRemotePath{}, fmt.Errorf("relative path contains NUL byte")
	}
	// Cap path length. Provider responses that exceed POSIX PATH_MAX
	// cannot represent a real filesystem target and indicate a degraded
	// or hostile upstream; reject at construction so the rest of the
	// pipeline never sees them.
	if len(rel) > MaxRemotePathLen {
		return RelativeRemotePath{}, fmt.Errorf("relative path exceeds %d bytes (got %d)", MaxRemotePathLen, len(rel))
	}
	rel = filepath.ToSlash(rel)
	// Reject explicit roots / dots.
	if rel == "/" || rel == "." {
		return RelativeRemotePath{}, fmt.Errorf("relative path %q resolves onto the mount root", rel)
	}
	// Strip a leading "./" so callers can pass "./foo" without surprise,
	// but reject anything that escapes via "..".
	if strings.HasPrefix(rel, "./") {
		rel = strings.TrimPrefix(rel, "./")
	}
	if strings.HasPrefix(rel, "/") {
		return RelativeRemotePath{}, fmt.Errorf("relative path %q must not start with '/'", rel)
	}
	if rel == ".." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") || strings.HasSuffix(rel, "/..") {
		return RelativeRemotePath{}, fmt.Errorf("relative path %q escapes the mount root via '..'", rel)
	}
	// Re-clean to collapse double slashes etc.; reject if cleaning would
	// reintroduce a forbidden form.
	cleaned := filepath.ToSlash(filepath.Clean(rel))
	if cleaned == "" || cleaned == "." || cleaned == "/" {
		return RelativeRemotePath{}, fmt.Errorf("relative path %q resolves onto the mount root after cleaning", rel)
	}
	if strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return RelativeRemotePath{}, fmt.Errorf("relative path %q escapes the mount root after cleaning", rel)
	}
	// Round-trip-onto-root guard: a single-segment path whose name is the
	// basename of the mount directory would resolve back onto the mount
	// root when joined with localRoot's parent under remoteRoot="/". This
	// is the production data-loss signature; reject up-front.
	if mountBasename != "" {
		// Only the single-segment form actually round-trips onto the root;
		// nested forms like "sub/<mountBasename>" remain safe.
		if cleaned == mountBasename {
			return RelativeRemotePath{}, fmt.Errorf("relative path %q collides with mount directory basename %q", rel, mountBasename)
		}
	}
	return RelativeRemotePath{rel: cleaned}, nil
}

// RelativeRemotePathFromLocal derives the typed relative path from an
// absolute (or relative-to-cwd) localPath under localRoot. Returns an
// error if localPath escapes localRoot or maps onto the mount root.
func RelativeRemotePathFromLocal(localRoot, localPath string) (RelativeRemotePath, error) {
	rel, err := filepath.Rel(localRoot, localPath)
	if err != nil {
		return RelativeRemotePath{}, err
	}
	return NewRelativeRemotePath(rel, filepath.Base(filepath.Clean(localRoot)))
}
