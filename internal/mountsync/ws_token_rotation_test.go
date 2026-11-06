package mountsync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/relayfile"
)

func TestWebSocketRedialsWithRotatedBearer(t *testing.T) {
	previousInterval := webSocketTokenCheckInterval
	webSocketTokenCheckInterval = 10 * time.Millisecond
	t.Cleanup(func() { webSocketTokenCheckInterval = previousInterval })

	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)

	workspaceID := "ws_mount_ws_rotation"
	handler := newMountsyncAPIHandler(t, store)
	firstToken := mustMountsyncTestJWT(t, "dev-secret", workspaceID, "MountSync", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))
	secondToken := mustMountsyncTestJWT(t, "dev-secret", workspaceID, "MountSyncRotated", []string{"fs:read", "fs:write"}, time.Now().Add(2*time.Hour))
	if firstToken == secondToken {
		t.Fatal("test tokens must differ")
	}

	var mu sync.Mutex
	var dialTokens []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/fs/ws") {
			mu.Lock()
			dialTokens = append(dialTokens, r.URL.Query().Get("token"))
			mu.Unlock()
		}
		handler.ServeHTTP(w, r)
	}))
	defer api.Close()

	writeMountsyncRemoteFile(t, api.Client(), api.URL, firstToken, workspaceID, "/notion/Docs/A.md", "0", "# A")

	rootCtx, cancelRoot := context.WithCancel(context.Background())
	client := NewHTTPClient(api.URL, firstToken, api.Client())
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/notion",
		LocalRoot:   t.TempDir(),
		RootCtx:     rootCtx,
	})
	if err != nil {
		cancelRoot()
		t.Fatalf("new syncer failed: %v", err)
	}
	waitStopped := func(deadline time.Duration) bool {
		end := time.Now().Add(deadline)
		for time.Now().Before(end) {
			syncer.mu.Lock()
			stopped := syncer.wsConn == nil && !syncer.wsConnecting
			syncer.mu.Unlock()
			if stopped {
				return true
			}
			time.Sleep(time.Millisecond)
		}
		return false
	}
	defer func() {
		cancelRoot()
		if !waitStopped(2 * time.Second) {
			t.Error("websocket listener did not stop before temporary mount cleanup")
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := syncer.SyncOnce(ctx); err != nil {
		t.Fatalf("initial websocket sync failed: %v", err)
	}

	var rotated sync.Once
	client.SetTokenSourceFunc(func() (string, bool) {
		changed := false
		rotated.Do(func() { changed = true })
		return secondToken, changed
	})
	if !waitStopped(2 * time.Second) {
		t.Fatal("websocket did not disconnect after its bearer was rotated")
	}
	if err := syncer.SyncOnce(ctx); err != nil {
		t.Fatalf("sync after rotation failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(dialTokens) < 2 {
		t.Fatalf("expected a redial after rotation, saw %d dial(s)", len(dialTokens))
	}
	if dialTokens[0] != firstToken {
		t.Fatal("first dial did not use the original bearer")
	}
	if dialTokens[len(dialTokens)-1] != secondToken {
		t.Fatal("redial did not use the rotated bearer")
	}
}
