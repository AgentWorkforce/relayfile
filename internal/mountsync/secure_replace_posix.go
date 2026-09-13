//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package mountsync

// POSIX materialization uses directory file descriptors and *at operations.
// Once the parent directory is opened with O_NOFOLLOW, an attacker replacing
// an ancestor path cannot redirect the temp-file create or final rename.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var errAncestorSafetyUnsupported = errors.New("secure ancestor operations are unsupported on this platform")

func ensureSecureParentDirectory(root, target string) error {
	parent, _, err := secureParentDir(root, target)
	if parent != nil {
		_ = parent.Close()
	}
	return err
}

func secureParentDir(root, target string) (*os.File, string, error) {
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, "", err
	}
	targetAbs, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return nil, "", err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, "", fmt.Errorf("path %s escapes local root %s", target, root)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 || parts[len(parts)-1] == "" || parts[len(parts)-1] == "." {
		return nil, "", fmt.Errorf("invalid target path %s", target)
	}
	current, err := openDirectoryNoFollow(rootAbs)
	if err != nil {
		return nil, "", err
	}
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." || part == ".." {
			_ = current.Close()
			return nil, "", fmt.Errorf("invalid path component %q", part)
		}
		nextFD, openErr := unix.Openat(int(current.Fd()), part, directoryOpenFlags(), 0)
		if errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(int(current.Fd()), part, 0o755); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = current.Close()
				return nil, "", mkdirErr
			}
			nextFD, openErr = unix.Openat(int(current.Fd()), part, directoryOpenFlags(), 0)
		}
		if openErr != nil {
			_ = current.Close()
			return nil, "", openErr
		}
		next := os.NewFile(uintptr(nextFD), filepath.Join(current.Name(), part))
		_ = current.Close()
		current = next
	}
	return current, parts[len(parts)-1], nil
}

func secureTempName(base string) string {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ".relayfile.tmp-fallback"
	}
	short := base
	if len(short) > 40 {
		short = short[:40]
	}
	return "." + short + ".tmp-" + hex.EncodeToString(random[:])
}

func openSecureTemp(parent *os.File, base string, mode uint32) (*os.File, string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		name := secureTempName(base)
		fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, mode)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name)), name, nil
	}
	return nil, "", errors.New("could not allocate secure temporary file")
}

func writeFileAtomicSecure(root, target string, data []byte, mode os.FileMode) error {
	parent, base, err := secureParentDir(root, target)
	if err != nil {
		return err
	}
	defer parent.Close()
	tmp, name, err := openSecureTemp(parent, base, 0o600)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = unix.Unlinkat(int(parent.Fd()), name, 0)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(int(parent.Fd()), name, int(parent.Fd()), base); err != nil {
		return err
	}
	committed = true
	return nil
}

func writeSymlinkAtomicSecure(root, targetPath, target string) error {
	parent, base, err := secureParentDir(root, targetPath)
	if err != nil {
		return err
	}
	defer parent.Close()
	name := secureTempName(base)
	if err := unix.Symlinkat(target, int(parent.Fd()), name); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = unix.Unlinkat(int(parent.Fd()), name, 0)
		}
	}()
	if err := unix.Renameat(int(parent.Fd()), name, int(parent.Fd()), base); err != nil {
		return err
	}
	committed = true
	return nil
}
