package relayfile

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestBuildEnvelopeQueueFromDSNMemory(t *testing.T) {
	queue, err := BuildEnvelopeQueueFromDSN("memory://", 7)
	if err != nil {
		t.Fatalf("build envelope memory queue failed: %v", err)
	}
	if queue == nil {
		t.Fatalf("expected non-nil envelope queue")
	}
	if queue.Capacity() != 7 {
		t.Fatalf("expected envelope queue capacity 7, got %d", queue.Capacity())
	}
}

func TestBuildWritebackQueueFromDSNFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wb-queue.json")
	queue, err := BuildWritebackQueueFromDSN("file://"+path, 9)
	if err != nil {
		t.Fatalf("build writeback file queue failed: %v", err)
	}
	if queue == nil {
		t.Fatalf("expected non-nil writeback queue")
	}
	if queue.Capacity() != 9 {
		t.Fatalf("expected writeback queue capacity 9, got %d", queue.Capacity())
	}
}

func TestBuildQueueFromDSNRejectsUnsupportedScheme(t *testing.T) {
	if _, err := BuildEnvelopeQueueFromDSN("redis://localhost:6379/0", 10); err == nil {
		t.Fatalf("expected unsupported scheme error for envelope queue")
	} else if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("expected not implemented error for envelope queue backend, got %v", err)
	}
	if _, err := BuildWritebackQueueFromDSN("redis://localhost:6379/0", 10); err == nil {
		t.Fatalf("expected unsupported scheme error for writeback queue")
	} else if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("expected not implemented error for writeback queue backend, got %v", err)
	}
}
