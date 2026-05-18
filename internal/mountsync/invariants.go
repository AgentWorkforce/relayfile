package mountsync

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrMountRootInvariantBroken is the typed error surfaced by both the daemon
// startup precheck and the top-of-cycle invariant assertion when the mount
// root has been clobbered (missing, replaced by a regular file, or otherwise
// not a directory). Callers that wrap a sync cycle inspect this error to
// distinguish a structural / refuse-to-run condition from a transient HTTP
// or filesystem failure, and to refuse silently recreating the mount root.
var ErrMountRootInvariantBroken = errors.New("mount root invariant broken")

// MountRootInvariantError carries the specifics of an invariant violation
// so operators can act on the structured payload (missing vs replaced-by-
// file vs other-non-directory). The "Reason" string is shaped for log lines
// and the incident report.
type MountRootInvariantError struct {
	Path         string
	Reason       string
	Kind         string // "missing" | "regular_file" | "symlink" | "other"
	IncidentPath string // populated when an INCIDENT-<ts>.md was written
}

func (e *MountRootInvariantError) Error() string {
	if e == nil {
		return "mount root invariant broken"
	}
	if e.IncidentPath != "" {
		return fmt.Sprintf("mount root invariant broken at %s (%s): %s; incident recorded at %s",
			e.Path, e.Kind, e.Reason, e.IncidentPath)
	}
	return fmt.Sprintf("mount root invariant broken at %s (%s): %s", e.Path, e.Kind, e.Reason)
}

func (e *MountRootInvariantError) Is(target error) bool {
	return target == ErrMountRootInvariantBroken
}

// CheckMountRootInvariant inspects localRoot and reports any structural
// violation that should refuse a daemon start or top-of-cycle sync. It does
// NOT write incident reports or modify state — that is the caller's
// responsibility (so a startup precheck can decide whether to honor a
// --reset-after-clobber acknowledgment before recreating the root).
//
// Returns nil when localRoot exists and is a directory.
func CheckMountRootInvariant(localRoot string) error {
	if strings.TrimSpace(localRoot) == "" {
		return &MountRootInvariantError{
			Path:   localRoot,
			Kind:   "other",
			Reason: "local root path is empty",
		}
	}
	clean := filepath.Clean(localRoot)
	info, err := os.Lstat(clean)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &MountRootInvariantError{
				Path:   clean,
				Kind:   "missing",
				Reason: "local root does not exist",
			}
		}
		return &MountRootInvariantError{
			Path:   clean,
			Kind:   "other",
			Reason: "lstat failed: " + err.Error(),
		}
	}
	switch {
	case info.Mode().IsDir():
		return nil
	case info.Mode().IsRegular():
		return &MountRootInvariantError{
			Path:   clean,
			Kind:   "regular_file",
			Reason: fmt.Sprintf("local root is a regular file (%d bytes); the mount directory was clobbered", info.Size()),
		}
	case info.Mode()&os.ModeSymlink != 0:
		return &MountRootInvariantError{
			Path:   clean,
			Kind:   "symlink",
			Reason: "local root is a symlink; expected a directory",
		}
	default:
		return &MountRootInvariantError{
			Path:   clean,
			Kind:   "other",
			Reason: fmt.Sprintf("local root is not a directory (mode=%s)", info.Mode().String()),
		}
	}
}

// WriteIncidentReport writes a markdown incident description that operators
// can read to understand a clobber. When the mount root itself is missing
// the report falls back to the parent directory of localRoot, then the
// system temp dir as a last resort. It returns the path written and never
// blocks the caller's error reporting on a write failure.
func WriteIncidentReport(localRoot string, invariantErr *MountRootInvariantError) (string, error) {
	if invariantErr == nil {
		return "", nil
	}
	ts := time.Now().UTC().Format("20060102T150405Z")
	filename := fmt.Sprintf("INCIDENT-%s.md", ts)

	cleanRoot := filepath.Clean(localRoot)
	candidates := []string{}
	if info, statErr := os.Lstat(cleanRoot); statErr == nil && info.IsDir() {
		candidates = append(candidates, filepath.Join(cleanRoot, ".relay"))
	}
	candidates = append(candidates, filepath.Dir(cleanRoot), os.TempDir())

	body := buildIncidentBody(localRoot, invariantErr, ts)

	var lastErr error
	for _, dir := range candidates {
		if dir == "" || dir == "." {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			lastErr = err
			continue
		}
		dest := filepath.Join(dir, filename)
		if err := os.WriteFile(dest, []byte(body), 0o644); err != nil {
			lastErr = err
			continue
		}
		return dest, nil
	}
	return "", lastErr
}

func buildIncidentBody(localRoot string, e *MountRootInvariantError, ts string) string {
	var sb strings.Builder
	sb.WriteString("# relayfile mount-root invariant incident\n\n")
	fmt.Fprintf(&sb, "- timestamp: %s\n", ts)
	fmt.Fprintf(&sb, "- local root: %s\n", localRoot)
	fmt.Fprintf(&sb, "- invariant: mount root must always be a directory\n")
	fmt.Fprintf(&sb, "- detected kind: %s\n", e.Kind)
	fmt.Fprintf(&sb, "- reason: %s\n\n", e.Reason)
	sb.WriteString("## Recovery\n\n")
	switch e.Kind {
	case "missing":
		sb.WriteString("The mount root is gone. Confirm whether the directory was deleted\n")
		sb.WriteString("by another process (rm -rf, git clean -fdx, sync tool, etc.).\n")
		sb.WriteString("To recreate a clean mount, pass `--reset-after-clobber` to\n")
		sb.WriteString("`relayfile mount` (or set `RELAYFILE_RESET_AFTER_CLOBBER=1`).\n")
		sb.WriteString("The daemon will refuse to start without this acknowledgment.\n")
	case "regular_file":
		sb.WriteString("The mount root path is now a regular file. This matches the\n")
		sb.WriteString("clobber signature where a same-named file was renamed over\n")
		sb.WriteString("the mount directory. Inspect and back up the file, then move\n")
		sb.WriteString("it aside before retrying. After moving the file aside, pass\n")
		sb.WriteString("`--reset-after-clobber` to authorize recreating the directory.\n")
	default:
		sb.WriteString("The mount root is not a directory. Investigate the path and,\n")
		sb.WriteString("if you intend to recreate the mount from cloud state, pass\n")
		sb.WriteString("`--reset-after-clobber` to authorize the rebuild.\n")
	}
	sb.WriteString("\nSee `docs/architecture/mount-invariants.md` for the protected\n")
	sb.WriteString("invariants and the full recovery procedure.\n")
	return sb.String()
}

// assertMountRootInvariant invokes the invariant check and, on violation,
// also writes an incident report (best-effort). It is intended for use at
// the top of every sync cycle and wraps the typed error so callers see
// errors.Is(err, ErrMountRootInvariantBroken).
func (s *Syncer) assertMountRootInvariant() error {
	if err := CheckMountRootInvariant(s.localRoot); err != nil {
		var inv *MountRootInvariantError
		if errors.As(err, &inv) {
			if path, werr := WriteIncidentReport(s.localRoot, inv); werr == nil && path != "" {
				inv.IncidentPath = path
			}
			s.logf("mount root invariant broken (%s): %s", inv.Kind, inv.Reason)
		}
		return err
	}
	return nil
}
