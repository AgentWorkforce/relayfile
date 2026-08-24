//go:build !unix

package main

import (
	"context"
	"errors"
)

func listenFlushRequests(_ context.Context) <-chan struct{} {
	return make(chan struct{})
}

func ignoreNotifyFlushSignal() {}

func notifyRunningMountFlush(_ context.Context, _ mountConfig) error {
	return errors.New("notify flush: SIGUSR1 kick is not supported on this platform")
}
