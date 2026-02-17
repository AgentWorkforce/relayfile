package relayfile

import (
	"testing"
)

func TestRegisterStateBackendFactory(t *testing.T) {
	scheme := "statetestcustom"
	RegisterStateBackendFactory(scheme, func(dsn string) (StateBackend, error) {
		return NewInMemoryStateBackend(), nil
	})
	backend, err := BuildStateBackendFromDSN(scheme + "://example")
	if err != nil {
		t.Fatalf("build state backend via registered factory failed: %v", err)
	}
	if backend == nil {
		t.Fatalf("expected non-nil backend from registered state backend factory")
	}
}

func TestRegisterEnvelopeQueueFactory(t *testing.T) {
	scheme := "envqtestcustom"
	RegisterEnvelopeQueueFactory(scheme, func(dsn string, capacity int) (EnvelopeQueue, error) {
		return NewInMemoryEnvelopeQueue(capacity), nil
	})
	queue, err := BuildEnvelopeQueueFromDSN(scheme+"://example", 17)
	if err != nil {
		t.Fatalf("build envelope queue via registered factory failed: %v", err)
	}
	if queue == nil {
		t.Fatalf("expected non-nil queue from registered envelope queue factory")
	}
	if queue.Capacity() != 17 {
		t.Fatalf("expected queue capacity 17, got %d", queue.Capacity())
	}
}

func TestRegisterWritebackQueueFactory(t *testing.T) {
	scheme := "wbqtestcustom"
	RegisterWritebackQueueFactory(scheme, func(dsn string, capacity int) (WritebackQueue, error) {
		return NewInMemoryWritebackQueue(capacity), nil
	})
	queue, err := BuildWritebackQueueFromDSN(scheme+"://example", 19)
	if err != nil {
		t.Fatalf("build writeback queue via registered factory failed: %v", err)
	}
	if queue == nil {
		t.Fatalf("expected non-nil queue from registered writeback queue factory")
	}
	if queue.Capacity() != 19 {
		t.Fatalf("expected queue capacity 19, got %d", queue.Capacity())
	}
}
