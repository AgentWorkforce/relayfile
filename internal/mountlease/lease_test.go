package mountlease

import (
	"errors"
	"testing"
)

func TestAcquireRefusesSecondSupervisorForSameLocalMirror(t *testing.T) {
	cacheDir := t.TempDir()
	localRoot := t.TempDir()
	first, err := acquireAt(cacheDir, "https://file.example.test/", "rw_shared", localRoot)
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	defer first.Release()

	if _, err := acquireAt(cacheDir, "https://file.example.test", "rw_shared", localRoot+"/."); !errors.Is(err, ErrHeld) {
		t.Fatalf("expected duplicate workspace lease refusal, got %v", err)
	}

	other, err := acquireAt(cacheDir, "https://file.example.test", "rw_other", localRoot)
	if err != nil {
		t.Fatalf("independent workspace should acquire: %v", err)
	}
	defer other.Release()
}

func TestAcquireAllowsIndependentLocalMirrorsOfSameWorkspace(t *testing.T) {
	cacheDir := t.TempDir()
	first, err := acquireAt(cacheDir, "https://file.example.test", "rw_shared", t.TempDir())
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	defer first.Release()

	second, err := acquireAt(cacheDir, "https://file.example.test", "rw_shared", t.TempDir())
	if err != nil {
		t.Fatalf("independent local mirror should acquire: %v", err)
	}
	defer second.Release()
}

func TestAcquireCanonicalizesEquivalentServerURLs(t *testing.T) {
	cacheDir := t.TempDir()
	localRoot := t.TempDir()
	first, err := acquireAt(cacheDir, "HTTPS://FILE.Example.Test:443/", "rw_shared", localRoot)
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	defer first.Release()

	if _, err := acquireAt(cacheDir, "https://file.example.test", "rw_shared", localRoot); !errors.Is(err, ErrHeld) {
		t.Fatalf("equivalent server URL bypassed lease: %v", err)
	}
}

func TestAcquireSucceedsAfterRelease(t *testing.T) {
	cacheDir := t.TempDir()
	localRoot := t.TempDir()
	first, err := acquireAt(cacheDir, "https://file.example.test", "rw_shared", localRoot)
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release first lease: %v", err)
	}

	second, err := acquireAt(cacheDir, "https://file.example.test", "rw_shared", localRoot)
	if err != nil {
		t.Fatalf("reacquire lease: %v", err)
	}
	defer second.Release()
}
