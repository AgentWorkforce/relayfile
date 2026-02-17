package relayfile

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestFileEnvelopeQueuePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "envelope-queue.json")
	queue, err := NewFileEnvelopeQueue(path, 4)
	if err != nil {
		t.Fatalf("new file envelope queue failed: %v", err)
	}
	if !queue.TryEnqueue("env_1") || !queue.TryEnqueue("env_2") {
		t.Fatalf("expected enqueue to succeed")
	}

	reopened, err := NewFileEnvelopeQueue(path, 4)
	if err != nil {
		t.Fatalf("reopen file envelope queue failed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	first, ok := reopened.Dequeue(ctx)
	if !ok || first != "env_1" {
		t.Fatalf("expected first dequeued envelope env_1, got %q (ok=%v)", first, ok)
	}
	second, ok := reopened.Dequeue(ctx)
	if !ok || second != "env_2" {
		t.Fatalf("expected second dequeued envelope env_2, got %q (ok=%v)", second, ok)
	}
}

func TestFileWritebackQueuePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "writeback-queue.json")
	queue, err := NewFileWritebackQueue(path, 4)
	if err != nil {
		t.Fatalf("new file writeback queue failed: %v", err)
	}
	if !queue.TryEnqueue(WritebackQueueItem{WorkspaceID: "ws_1", OpID: "op_1"}) {
		t.Fatalf("expected first writeback enqueue to succeed")
	}
	if !queue.TryEnqueue(WritebackQueueItem{WorkspaceID: "ws_2", OpID: "op_2"}) {
		t.Fatalf("expected second writeback enqueue to succeed")
	}

	reopened, err := NewFileWritebackQueue(path, 4)
	if err != nil {
		t.Fatalf("reopen file writeback queue failed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	first, ok := reopened.Dequeue(ctx)
	if !ok || first.OpID != "op_1" {
		t.Fatalf("expected first dequeued writeback op_1, got %+v (ok=%v)", first, ok)
	}
	second, ok := reopened.Dequeue(ctx)
	if !ok || second.OpID != "op_2" {
		t.Fatalf("expected second dequeued writeback op_2, got %+v (ok=%v)", second, ok)
	}
}

func TestFileQueueCapacityAndTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capacity-envelope-queue.json")
	queue, err := NewFileEnvelopeQueue(path, 1)
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}
	if !queue.TryEnqueue("env_cap_1") {
		t.Fatalf("expected first enqueue to succeed")
	}
	if queue.TryEnqueue("env_cap_2") {
		t.Fatalf("expected second enqueue to fail at capacity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, ok := queue.Dequeue(ctx)
	if !ok {
		t.Fatalf("expected first dequeue to succeed")
	}
	_, ok = queue.Dequeue(ctx)
	if ok {
		t.Fatalf("expected dequeue to time out when queue is empty")
	}
}
