//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package mountsync

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRemoveLocalNoFollowRejectsAncestorSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside victim: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "redirect")); err != nil {
		t.Fatalf("create redirect: %v", err)
	}
	if err := removeLocalNoFollow(root, filepath.Join(root, "redirect", "victim")); err == nil {
		t.Fatal("removed a file through an ancestor symlink")
	}
	if _, err := os.Stat(victim); errors.Is(err, os.ErrNotExist) {
		t.Fatal("outside victim was removed")
	}
}

func TestOpenLocalRegularNoFollowRejectsAncestorSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "redirect")); err != nil {
		t.Fatalf("create redirect: %v", err)
	}
	if _, err := openLocalRegularNoFollow(root, filepath.Join(root, "redirect", "secret")); err == nil {
		t.Fatal("opened a file through a replaced ancestor symlink")
	}
}

func TestOpenLocalRegularNoFollowRejectsSymlinkMountRoot(t *testing.T) {
	actual := t.TempDir()
	if err := os.WriteFile(filepath.Join(actual, "file.txt"), []byte("content"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	base := t.TempDir()
	root := filepath.Join(base, "mount")
	if err := os.Symlink(actual, root); err != nil {
		t.Fatalf("create symlink mount root: %v", err)
	}
	if _, err := openLocalRegularNoFollow(root, filepath.Join(root, "file.txt")); err == nil {
		t.Fatal("opened a file through a symlink mount root")
	}
}

func TestOpenLocalRegularNoFollowRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("create fifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := openLocalRegularNoFollow("", path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("accepted FIFO as a regular file")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("opening FIFO blocked instead of rejecting it")
	}
}
