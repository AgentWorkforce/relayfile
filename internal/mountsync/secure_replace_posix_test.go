//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package mountsync

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMovePathAtomicSecureFallsBackAcrossFilesystemsWithFinalMode(t *testing.T) {
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "nested", "file.txt")
	targetPath := filepath.Join(targetRoot, "nested", "file.txt")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o700); err != nil {
		t.Fatalf("create source parent: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("verified archive body\n"), 0o600); err != nil {
		t.Fatalf("write staged file: %v", err)
	}

	first := true
	renameat := func(oldDirFD int, oldPath string, newDirFD int, newPath string) error {
		if first {
			first = false
			return unix.EXDEV
		}
		return unix.Renameat(oldDirFD, oldPath, newDirFD, newPath)
	}

	if err := movePathAtomicSecureWithRename(sourceRoot, sourcePath, targetRoot, targetPath, 0o444, renameat); err != nil {
		t.Fatalf("publish staged file: %v", err)
	}
	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read published file: %v", err)
	}
	if string(got) != "verified archive body\n" {
		t.Fatalf("published body = %q", got)
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat published file: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o444 {
		t.Fatalf("published mode = %04o, want 0444", gotMode)
	}
	if _, err := os.Lstat(sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged source survived publish: %v", err)
	}
}

func TestMovePathAtomicSecureFallsBackAcrossFilesystemsForSymlink(t *testing.T) {
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "nested", "link")
	targetPath := filepath.Join(targetRoot, "nested", "link")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o700); err != nil {
		t.Fatalf("create source parent: %v", err)
	}
	if err := os.Symlink("../target.txt", sourcePath); err != nil {
		t.Fatalf("write staged symlink: %v", err)
	}

	first := true
	renameat := func(oldDirFD int, oldPath string, newDirFD int, newPath string) error {
		if first {
			first = false
			return unix.EXDEV
		}
		return unix.Renameat(oldDirFD, oldPath, newDirFD, newPath)
	}

	if err := movePathAtomicSecureWithRename(sourceRoot, sourcePath, targetRoot, targetPath, 0o444, renameat); err != nil {
		t.Fatalf("publish staged symlink: %v", err)
	}
	got, err := os.Readlink(targetPath)
	if err != nil {
		t.Fatalf("read published symlink: %v", err)
	}
	if got != "../target.txt" {
		t.Fatalf("published symlink = %q", got)
	}
}

func TestRemoveOwnedStagingTreeBoundedRefusesOversizedTree(t *testing.T) {
	stage := filepath.Join(t.TempDir(), "stage")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatalf("create stage: %v", err)
	}
	for _, name := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(stage, name), []byte(name), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	err := removeOwnedStagingTreeBounded(stage, 1)
	if !errors.Is(err, errStagingCleanupLimit) {
		t.Fatalf("oversized cleanup error = %v, want %v", err, errStagingCleanupLimit)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("oversized stage should remain untouched: %v", err)
	}
	if err := removeOwnedStagingTreeBounded(stage, 3); err != nil {
		t.Fatalf("bounded cleanup: %v", err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage survived bounded cleanup: %v", err)
	}
}
