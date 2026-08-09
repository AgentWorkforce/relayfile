package main

import (
	"sync"
	"testing"
)

// TestFailedWritebacksSurvivesAlternation checks the specific claim that the
// failedWritebacks counter is zeroed when the two writers alternate.
//
// It is NOT zeroed. Both writers deliberately read the counter back before
// writing it: mountsync at internal/mountsync/syncer.go:7020
// (readPublicFailedWritebacks) and the CLI mirror at
// cmd/relayfile-cli/main.go:10986 (max against the persisted value). This test
// documents that hardening so a future refactor does not remove it.
func TestFailedWritebacksSurvivesAlternation(t *testing.T) {
	localDir := t.TempDir()
	syncer := newPublicStateWriter(t, localDir)

	for i := 0; i < 5; i++ {
		if err := incrementFailedWritebacksInState(localDir); err != nil {
			t.Fatalf("increment %d: %v", i, err)
		}
	}
	if got := readPersistedFailedWritebacks(localDir); got != 5 {
		t.Fatalf("precondition: counter = %d, want 5", got)
	}

	// Alternate the two writers several times.
	for i := 0; i < 3; i++ {
		writeMountsyncPublicState(t, syncer)
		if got := readPersistedFailedWritebacks(localDir); got != 5 {
			t.Errorf("after mountsync write %d: counter = %d, want 5 (mountsync dropped the counter)", i, got)
		}
		writeCLIMirrorState(t, localDir)
		if got := readPersistedFailedWritebacks(localDir); got != 5 {
			t.Errorf("after CLI mirror write %d: counter = %d, want 5 (CLI mirror dropped the counter)", i, got)
		}
	}
}

// TestFailedWritebacksLostUpdateUnderConcurrency probes the real hazard: both
// writers preserve the counter via read-modify-write, but neither holds a lock
// the other respects, so an increment landing between mountsync's read
// (syncer.go:7020) and its write (syncer.go:7200) is silently overwritten with
// the stale value.
//
// failedWritebacksStateMu (cmd/relayfile-cli/main.go) serializes the CLI-side
// increments against each other, but mountsync lives in another package and
// cannot take it. The mutex looks synchronized and is not.
//
// N increments must yield a counter of N.
func TestFailedWritebacksLostUpdateUnderConcurrency(t *testing.T) {
	const increments = 200

	localDir := t.TempDir()
	syncer := newPublicStateWriter(t, localDir)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// mountsync republishing public state, as the live daemon does ~1x/sec.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = syncer.FlushOutboxOnce(t.Context())
			}
		}
	}()

	for i := 0; i < increments; i++ {
		if err := incrementFailedWritebacksInState(localDir); err != nil {
			t.Fatalf("increment %d: %v", i, err)
		}
	}

	close(stop)
	wg.Wait()

	if got := readPersistedFailedWritebacks(localDir); got != increments {
		t.Errorf("failedWritebacks = %d after %d increments: %d writeback failures were silently lost. "+
			"failedWritebacksStateMu (cmd/relayfile-cli) does not cross into internal/mountsync, so "+
			"savePublicState's read at syncer.go:7020 / write at syncer.go:7200 overwrites increments "+
			"that land in between",
			got, increments, increments-got)
	}
}
