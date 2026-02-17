package relayfile

import (
	"strings"
	"sync"
)

type StateBackendFactory func(dsn string) (StateBackend, error)
type EnvelopeQueueFactory func(dsn string, capacity int) (EnvelopeQueue, error)
type WritebackQueueFactory func(dsn string, capacity int) (WritebackQueue, error)

var backendFactoryRegistry = struct {
	mu             sync.RWMutex
	stateFactories map[string]StateBackendFactory
	envFactories   map[string]EnvelopeQueueFactory
	wbFactories    map[string]WritebackQueueFactory
}{
	stateFactories: map[string]StateBackendFactory{},
	envFactories:   map[string]EnvelopeQueueFactory{},
	wbFactories:    map[string]WritebackQueueFactory{},
}

func RegisterStateBackendFactory(scheme string, factory StateBackendFactory) {
	scheme = normalizeBackendScheme(scheme)
	if scheme == "" || factory == nil {
		return
	}
	backendFactoryRegistry.mu.Lock()
	defer backendFactoryRegistry.mu.Unlock()
	backendFactoryRegistry.stateFactories[scheme] = factory
}

func RegisterEnvelopeQueueFactory(scheme string, factory EnvelopeQueueFactory) {
	scheme = normalizeBackendScheme(scheme)
	if scheme == "" || factory == nil {
		return
	}
	backendFactoryRegistry.mu.Lock()
	defer backendFactoryRegistry.mu.Unlock()
	backendFactoryRegistry.envFactories[scheme] = factory
}

func RegisterWritebackQueueFactory(scheme string, factory WritebackQueueFactory) {
	scheme = normalizeBackendScheme(scheme)
	if scheme == "" || factory == nil {
		return
	}
	backendFactoryRegistry.mu.Lock()
	defer backendFactoryRegistry.mu.Unlock()
	backendFactoryRegistry.wbFactories[scheme] = factory
}

func lookupStateBackendFactory(scheme string) (StateBackendFactory, bool) {
	scheme = normalizeBackendScheme(scheme)
	backendFactoryRegistry.mu.RLock()
	defer backendFactoryRegistry.mu.RUnlock()
	factory, ok := backendFactoryRegistry.stateFactories[scheme]
	return factory, ok
}

func lookupEnvelopeQueueFactory(scheme string) (EnvelopeQueueFactory, bool) {
	scheme = normalizeBackendScheme(scheme)
	backendFactoryRegistry.mu.RLock()
	defer backendFactoryRegistry.mu.RUnlock()
	factory, ok := backendFactoryRegistry.envFactories[scheme]
	return factory, ok
}

func lookupWritebackQueueFactory(scheme string) (WritebackQueueFactory, bool) {
	scheme = normalizeBackendScheme(scheme)
	backendFactoryRegistry.mu.RLock()
	defer backendFactoryRegistry.mu.RUnlock()
	factory, ok := backendFactoryRegistry.wbFactories[scheme]
	return factory, ok
}

func normalizeBackendScheme(scheme string) string {
	return strings.ToLower(strings.TrimSpace(scheme))
}
