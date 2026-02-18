package relayfile

import (
	"fmt"
	"net/url"
	"strings"
)

func BuildEnvelopeQueueFromDSN(dsn string, capacity int) (EnvelopeQueue, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, nil
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		return nil, err
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if factory, ok := lookupEnvelopeQueueFactory(scheme); ok {
		return factory(dsn, capacity)
	}
	switch scheme {
	case "", "file":
		path, pathErr := dsnPath(parsed, dsn)
		if pathErr != nil {
			return nil, pathErr
		}
		return NewFileEnvelopeQueue(path, capacity)
	case "memory", "mem", "inmem":
		return NewInMemoryEnvelopeQueue(capacity), nil
	case "postgres", "postgresql":
		return NewPostgresEnvelopeQueue(dsn, capacity)
	case "redis", "rediss", "nats", "sqs", "kafka":
		return nil, fmt.Errorf("%w: envelope queue backend %s", ErrNotImplemented, scheme)
	default:
		return nil, fmt.Errorf("unsupported envelope queue scheme: %s", scheme)
	}
}

func BuildWritebackQueueFromDSN(dsn string, capacity int) (WritebackQueue, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, nil
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		return nil, err
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if factory, ok := lookupWritebackQueueFactory(scheme); ok {
		return factory(dsn, capacity)
	}
	switch scheme {
	case "", "file":
		path, pathErr := dsnPath(parsed, dsn)
		if pathErr != nil {
			return nil, pathErr
		}
		return NewFileWritebackQueue(path, capacity)
	case "memory", "mem", "inmem":
		return NewInMemoryWritebackQueue(capacity), nil
	case "postgres", "postgresql":
		return NewPostgresWritebackQueue(dsn, capacity)
	case "redis", "rediss", "nats", "sqs", "kafka":
		return nil, fmt.Errorf("%w: writeback queue backend %s", ErrNotImplemented, scheme)
	default:
		return nil, fmt.Errorf("unsupported writeback queue scheme: %s", scheme)
	}
}

func dsnPath(parsed *url.URL, raw string) (string, error) {
	if parsed == nil {
		return "", ErrInvalidInput
	}
	if strings.TrimSpace(parsed.Scheme) == "" {
		if strings.TrimSpace(raw) == "" {
			return "", ErrInvalidInput
		}
		return strings.TrimSpace(raw), nil
	}
	path := strings.TrimSpace(parsed.Path)
	if path == "" {
		path = strings.TrimSpace(parsed.Opaque)
	}
	if path == "" {
		path = strings.TrimSpace(parsed.Host)
	}
	if path == "" {
		return "", ErrInvalidInput
	}
	return path, nil
}
