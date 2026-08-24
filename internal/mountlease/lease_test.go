package mountlease

import (
	"errors"
	"os"
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

func TestInspectReadsHeldLeaseWithoutAcquiring(t *testing.T) {
	cacheDir := t.TempDir()
	localRoot := t.TempDir()
	first, err := acquireAt(cacheDir, "https://file.example.test", "rw_shared", localRoot)
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	defer first.Release()

	info, err := inspectAt(cacheDir, "https://file.example.test", "rw_shared", localRoot)
	if err != nil {
		t.Fatalf("inspect held lease: %v", err)
	}
	if info.PID != os.Getpid() {
		t.Fatalf("inspect PID = %d, want %d", info.PID, os.Getpid())
	}

	if _, err := acquireAt(cacheDir, "https://file.example.test", "rw_shared", localRoot); !errors.Is(err, ErrHeld) {
		t.Fatalf("inspect must not release or steal the lease, got %v", err)
	}
}

func TestInspectMissingLease(t *testing.T) {
	_, err := inspectAt(t.TempDir(), "https://file.example.test", "rw_missing", t.TempDir())
	if !errors.Is(err, ErrNotHeld) {
		t.Fatalf("expected ErrNotHeld, got %v", err)
	}
}

func TestFlushAckRoundTrip(t *testing.T) {
	cacheDir := t.TempDir()
	localRoot := t.TempDir()
	if err := writeFlushAckAt(cacheDir, "https://file.example.test", "rw_shared", localRoot, FlushAck{Seq: 3, PID: 99, At: "t"}); err != nil {
		t.Fatalf("write ack: %v", err)
	}
	got, err := readFlushAckAt(cacheDir, "https://file.example.test", "rw_shared", localRoot)
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	if got.Seq != 3 || got.PID != 99 {
		t.Fatalf("ack = %+v", got)
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
