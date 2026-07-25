package httpapi

// Measurement harness for docs/multi-agent-collaboration-assessment.md (Q3).
//
// Question: how badly does fork contention degrade as concurrent agent count
// grows, when agents write to DISJOINT paths (the best case for forks)?
//
// Method: N goroutines each simulate one agent working a private path
// (/agent-<i>/work.md). Each round: CreateFork -> WriteForkFile (If-Match "0"
// on first write, previous revision after) -> CommitFork. On parent_moved,
// the fork is discarded (per store.go, ParentRevision is never mutated, so a
// stuck fork can never succeed) and the round is retried with a fresh fork.
// Every agent runs for a fixed wall-clock budget so all N goroutines are
// racing to commit against the SAME workspace-wide revision counter
// concurrently. Run with: go test ./internal/httpapi/ -run TestAssessForkContention -v
//
// Deliberately not table-driven / t.Parallel() per .claude/rules/go-httpapi-tests.md:
// this is a wall-clock contention measurement, not a request/response contract
// check. Running trials in parallel would have them compete for CPU and
// goroutine-scheduler time and contaminate each other's throughput numbers —
// the opposite of what the rule's t.Parallel() guidance is for.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/relayfile"
)

func jsonBody(v any) *bytes.Reader {
	data, _ := json.Marshal(v)
	return bytes.NewReader(data)
}

func runForkContentionTrial(t *testing.T, agents int, budget, thinkTime time.Duration) {
	t.Helper()
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	server := NewServer(store)
	const wsID = "ws_contention_trial"

	var (
		totalAttempts   int64
		parentMoved     int64
		otherErrors     int64
		successes       int64
		wastedForkNanos int64
		mu              sync.Mutex
		roundsPerAgent  = make([]int, agents)
	)

	deadline := time.Now().Add(budget)
	var wg sync.WaitGroup
	for i := 0; i < agents; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			token := mustTestJWT(t, "dev-secret", wsID, fmt.Sprintf("Agent%d", i), []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))
			path := fmt.Sprintf("/agent-%d/work.md", i)
			round := 0
			for time.Now().Before(deadline) {
				round++
				atomic.AddInt64(&totalAttempts, 1)
				forkStart := time.Now()

				proposalID := fmt.Sprintf("agent-%d-round-%d", i, round)
				handle, code, err := createForkNoFatal(server, token, wsID, proposalID)
				if err != nil || code != http.StatusOK {
					atomic.AddInt64(&otherErrors, 1)
					continue
				}

				// Simulate real work. "*" matches whether the path already
				// exists (from a prior successful round) or not.
				content := fmt.Sprintf("agent %d round %d work", i, round)
				_, code, err = writeForkFileNoFatal(server, token, wsID, handle.ForkID, path, "*", content, fmt.Sprintf("corr_%d_%d_w", i, round))
				if err != nil || code != http.StatusAccepted {
					atomic.AddInt64(&otherErrors, 1)
					_, _ = discardForkNoFatal(server, token, wsID, handle.ForkID)
					continue
				}

				if thinkTime > 0 {
					time.Sleep(thinkTime)
				}
				_, commitCode, commitErr := commitForkNoFatal(server, token, wsID, handle.ForkID, fmt.Sprintf("corr_%d_%d_c", i, round))
				elapsed := time.Since(forkStart)

				switch {
				case commitCode == http.StatusOK:
					atomic.AddInt64(&successes, 1)
					mu.Lock()
					roundsPerAgent[i]++
					mu.Unlock()
				case commitCode == http.StatusConflict:
					atomic.AddInt64(&parentMoved, 1)
					atomic.AddInt64(&wastedForkNanos, elapsed.Nanoseconds())
				default:
					atomic.AddInt64(&otherErrors, 1)
					_ = commitErr
				}
			}
		}()
	}
	wg.Wait()

	total := atomic.LoadInt64(&totalAttempts)
	moved := atomic.LoadInt64(&parentMoved)
	ok := atomic.LoadInt64(&successes)
	errs := atomic.LoadInt64(&otherErrors)
	var avgWastedMs float64
	if moved > 0 {
		avgWastedMs = float64(atomic.LoadInt64(&wastedForkNanos)) / float64(moved) / float64(time.Millisecond)
	}
	var movedPct float64
	if total > 0 {
		movedPct = float64(moved) / float64(total) * 100
	}
	t.Logf("agents=%d think_time=%s budget=%s attempts=%d success=%d parent_moved=%d (%.1f%%) other_errors=%d avg_wasted_fork_lifetime_ms=%.2f",
		agents, thinkTime, budget, total, ok, moved, movedPct, errs, avgWastedMs)
}

func TestAssessForkContention(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping contention measurement in -short mode")
	}
	t.Run("tight-loop", func(t *testing.T) {
		for _, agents := range []int{1, 2, 4, 8} {
			agents := agents
			t.Run(fmt.Sprintf("agents=%d", agents), func(t *testing.T) {
				runForkContentionTrial(t, agents, 3*time.Second, 0)
			})
		}
	})
	t.Run("realistic-think-time-20ms", func(t *testing.T) {
		for _, agents := range []int{1, 2, 4, 8} {
			agents := agents
			t.Run(fmt.Sprintf("agents=%d", agents), func(t *testing.T) {
				runForkContentionTrial(t, agents, 3*time.Second, 20*time.Millisecond)
			})
		}
	})
}

// --- non-Fatal HTTP helpers (goroutine-safe; existing *ForTest helpers call
// t.Fatalf which is unsafe to call concurrently from background goroutines) ---

func createForkNoFatal(server http.Handler, token, workspaceID, proposalID string) (relayfile.ForkHandle, int, error) {
	req := httptest.NewRequest(http.MethodPost, "/v1/workspaces/"+workspaceID+"/forks", jsonBody(map[string]any{"proposalId": proposalID}))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Correlation-Id", "corr_create_"+proposalID)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	var handle relayfile.ForkHandle
	if rec.Code == http.StatusOK {
		_ = json.NewDecoder(rec.Body).Decode(&handle)
	}
	return handle, rec.Code, nil
}

func writeForkFileNoFatal(server http.Handler, token, workspaceID, forkID, path, ifMatch, content, correlationID string) (relayfile.WriteResult, int, error) {
	req := httptest.NewRequest(http.MethodPut, "/v1/workspaces/"+workspaceID+"/fs/file?path="+path+"&forkId="+forkID, jsonBody(map[string]any{
		"contentType": "text/markdown",
		"content":     content,
	}))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Correlation-Id", correlationID)
	req.Header.Set("If-Match", ifMatch)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	var result relayfile.WriteResult
	if rec.Code == http.StatusAccepted {
		_ = json.NewDecoder(rec.Body).Decode(&result)
	}
	return result, rec.Code, nil
}

func commitForkNoFatal(server http.Handler, token, workspaceID, forkID, correlationID string) (relayfile.ForkCommitResponse, int, error) {
	req := httptest.NewRequest(http.MethodPost, "/v1/workspaces/"+workspaceID+"/forks/"+forkID+"/commit", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Correlation-Id", correlationID)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	var result relayfile.ForkCommitResponse
	if rec.Code == http.StatusOK {
		_ = json.NewDecoder(rec.Body).Decode(&result)
	}
	return result, rec.Code, nil
}

func discardForkNoFatal(server http.Handler, token, workspaceID, forkID string) (int, error) {
	req := httptest.NewRequest(http.MethodDelete, "/v1/workspaces/"+workspaceID+"/forks/"+forkID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Correlation-Id", "corr_discard_"+forkID)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec.Code, nil
}
