package relayfile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type fileEnvelopeQueue struct {
	path         string
	capacity     int
	pollInterval time.Duration
	mu           sync.Mutex
	items        []string
}

type fileWritebackQueue struct {
	path         string
	capacity     int
	pollInterval time.Duration
	mu           sync.Mutex
	items        []WritebackQueueItem
}

type fileEnvelopeQueueState struct {
	Items []string `json:"items"`
}

type fileWritebackQueueState struct {
	Items []WritebackQueueItem `json:"items"`
}

func NewFileEnvelopeQueue(path string, capacity int) (EnvelopeQueue, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, ErrInvalidInput
	}
	if capacity <= 0 {
		capacity = 1024
	}
	q := &fileEnvelopeQueue{
		path:         path,
		capacity:     capacity,
		pollInterval: 10 * time.Millisecond,
		items:        []string{},
	}
	if err := q.load(); err != nil {
		return nil, err
	}
	return q, nil
}

func NewFileWritebackQueue(path string, capacity int) (WritebackQueue, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, ErrInvalidInput
	}
	if capacity <= 0 {
		capacity = 1024
	}
	q := &fileWritebackQueue{
		path:         path,
		capacity:     capacity,
		pollInterval: 10 * time.Millisecond,
		items:        []WritebackQueueItem{},
	}
	if err := q.load(); err != nil {
		return nil, err
	}
	return q, nil
}

func (q *fileEnvelopeQueue) TryEnqueue(envelopeID string) bool {
	if strings.TrimSpace(envelopeID) == "" {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= q.capacity {
		return false
	}
	q.items = append(q.items, envelopeID)
	if err := q.saveLocked(); err != nil {
		q.items = q.items[:len(q.items)-1]
		return false
	}
	return true
}

func (q *fileEnvelopeQueue) Enqueue(ctx context.Context, envelopeID string) bool {
	for {
		if q.TryEnqueue(envelopeID) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(q.pollInterval):
		}
	}
}

func (q *fileEnvelopeQueue) Dequeue(ctx context.Context) (string, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			item := q.items[0]
			q.items = q.items[1:]
			if err := q.saveLocked(); err != nil {
				q.items = append([]string{item}, q.items...)
				q.mu.Unlock()
				select {
				case <-ctx.Done():
					return "", false
				case <-time.After(q.pollInterval):
					continue
				}
			}
			q.mu.Unlock()
			return item, true
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", false
		case <-time.After(q.pollInterval):
		}
	}
}

func (q *fileEnvelopeQueue) Depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

func (q *fileEnvelopeQueue) Capacity() int {
	return q.capacity
}

func (q *fileEnvelopeQueue) SnapshotEnvelopeIDs() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.items...)
}

func (q *fileEnvelopeQueue) Close() error {
	return nil
}

func (q *fileEnvelopeQueue) load() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	data, err := os.ReadFile(q.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var snapshot fileEnvelopeQueueState
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}
	if len(snapshot.Items) > q.capacity {
		q.items = append([]string(nil), snapshot.Items[len(snapshot.Items)-q.capacity:]...)
		return q.saveLocked()
	}
	q.items = append([]string(nil), snapshot.Items...)
	return nil
}

func (q *fileEnvelopeQueue) saveLocked() error {
	snapshot := fileEnvelopeQueueState{
		Items: append([]string(nil), q.items...),
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(q.path), 0o755); err != nil {
		return err
	}
	tmp := q.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, q.path)
}

func (q *fileWritebackQueue) TryEnqueue(task WritebackQueueItem) bool {
	if strings.TrimSpace(task.OpID) == "" {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= q.capacity {
		return false
	}
	q.items = append(q.items, task)
	if err := q.saveLocked(); err != nil {
		q.items = q.items[:len(q.items)-1]
		return false
	}
	return true
}

func (q *fileWritebackQueue) Enqueue(ctx context.Context, task WritebackQueueItem) bool {
	for {
		if q.TryEnqueue(task) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(q.pollInterval):
		}
	}
}

func (q *fileWritebackQueue) Dequeue(ctx context.Context) (WritebackQueueItem, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			item := q.items[0]
			q.items = q.items[1:]
			if err := q.saveLocked(); err != nil {
				q.items = append([]WritebackQueueItem{item}, q.items...)
				q.mu.Unlock()
				select {
				case <-ctx.Done():
					return WritebackQueueItem{}, false
				case <-time.After(q.pollInterval):
					continue
				}
			}
			q.mu.Unlock()
			return item, true
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return WritebackQueueItem{}, false
		case <-time.After(q.pollInterval):
		}
	}
}

func (q *fileWritebackQueue) Depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

func (q *fileWritebackQueue) Capacity() int {
	return q.capacity
}

func (q *fileWritebackQueue) SnapshotWritebacks() []WritebackQueueItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]WritebackQueueItem(nil), q.items...)
}

func (q *fileWritebackQueue) Close() error {
	return nil
}

func (q *fileWritebackQueue) load() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	data, err := os.ReadFile(q.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var snapshot fileWritebackQueueState
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}
	if len(snapshot.Items) > q.capacity {
		q.items = append([]WritebackQueueItem(nil), snapshot.Items[len(snapshot.Items)-q.capacity:]...)
		return q.saveLocked()
	}
	q.items = append([]WritebackQueueItem(nil), snapshot.Items...)
	return nil
}

func (q *fileWritebackQueue) saveLocked() error {
	snapshot := fileWritebackQueueState{
		Items: append([]WritebackQueueItem(nil), q.items...),
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(q.path), 0o755); err != nil {
		return err
	}
	tmp := q.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, q.path)
}
