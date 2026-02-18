package relayfile

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

type InMemoryStateBackend struct {
	mu       sync.Mutex
	snapshot *persistedState
}

func NewInMemoryStateBackend() *InMemoryStateBackend {
	return &InMemoryStateBackend{}
}

func (b *InMemoryStateBackend) Load() (*persistedState, error) {
	if b == nil {
		return nil, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.snapshot == nil {
		return nil, nil
	}
	data, err := json.Marshal(b.snapshot)
	if err != nil {
		return nil, err
	}
	var clone persistedState
	if err := json.Unmarshal(data, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}

func (b *InMemoryStateBackend) Save(state *persistedState) error {
	if b == nil || state == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	var clone persistedState
	if err := json.Unmarshal(data, &clone); err != nil {
		return err
	}
	b.snapshot = &clone
	return nil
}

func BuildStateBackendFromDSN(dsn string) (StateBackend, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, nil
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		return nil, err
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if factory, ok := lookupStateBackendFactory(scheme); ok {
		return factory(dsn)
	}
	switch scheme {
	case "", "file":
		path, pathErr := dsnPath(parsed, dsn)
		if pathErr != nil {
			return nil, pathErr
		}
		return NewJSONFileStateBackend(path), nil
	case "memory", "mem", "inmem":
		return NewInMemoryStateBackend(), nil
	case "postgres", "postgresql":
		return NewPostgresStateBackend(dsn)
	case "mysql", "sqlite":
		return nil, fmt.Errorf("%w: state backend %s", ErrNotImplemented, scheme)
	default:
		return nil, fmt.Errorf("unsupported state backend scheme: %s", scheme)
	}
}
