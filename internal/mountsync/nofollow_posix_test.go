//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package mountsync

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

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
