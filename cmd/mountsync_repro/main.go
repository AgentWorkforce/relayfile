package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentworkforce/relayfile/internal/httpapi"
	"github.com/agentworkforce/relayfile/internal/mountsync"
	"github.com/agentworkforce/relayfile/internal/relayfile"
)

func main() {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	defer store.Close()
	api := httptest.NewServer(httpapi.NewServer(store))
	defer api.Close()

	workspace := "ws_probe"
	readToken := mustJWT("dev-secret", workspace, []string{"fs:read"}, time.Now().Add(time.Hour))
	writeToken := mustJWT("dev-secret", workspace, []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	writeFile(api.Client(), api.URL, writeToken, workspace, "/notion/docs/notes.md", "# notes", "0")

	localDir, err := os.MkdirTemp("", "mountsync-probe-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(localDir)

	client := mountsync.NewHTTPClient(api.URL, readToken, api.Client())
	syncer, err := mountsync.NewSyncer(client, mountsync.SyncerOptions{
		WorkspaceID: workspace,
		RemoteRoot:  "/notion",
		LocalRoot:   localDir,
		Scopes:      []string{"fs:read"},
	})
	if err != nil {
		panic(err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		panic(err)
	}

	path := filepath.Join(localDir, "docs", "notes.md")
	st, err := os.Stat(path)
	if err != nil {
		panic(err)
	}
	fmt.Printf("initial mode: %03o\n", st.Mode().Perm())

	if err := os.WriteFile(path, []byte("# local change"), 0o644); err != nil {
		panic(err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		panic(err)
	}
	st2, err := os.Stat(path)
	if err != nil {
		panic(err)
	}
	fmt.Printf("after rejection mode: %03o\n", st2.Mode().Perm())
}

func writeFile(httpClient *http.Client, baseURL, token, workspaceID, path, content, revision string) {
	payload := map[string]any{"contentType": "text/markdown", "content": content}
	body, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	req, err := http.NewRequest(http.MethodPut, baseURL+"/v1/workspaces/"+workspaceID+"/fs/file?path="+url.QueryEscape(path), bytes.NewReader(body))
	if err != nil {
		panic(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", revision)
	req.Header.Set("X-Correlation-Id", "corr-repro")
	resp, err := httpClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		payload, _ := io.ReadAll(resp.Body)
		panic(fmt.Sprintf("write failed %d: %s", resp.StatusCode, strings.TrimSpace(string(payload))))
	}
}

func mustJWT(secret, workspaceID string, scopes []string, exp time.Time) string {
	headerBytes, _ := json.Marshal(map[string]any{"alg": "HS256", "typ": "JWT"})
	payloadBytes, _ := json.Marshal(map[string]any{
		"workspace_id": workspaceID,
		"agent_name":   "MountSync",
		"scopes":       scopes,
		"exp":          exp.Unix(),
		"aud":          "relayfile",
	})
	h := base64.RawURLEncoding.EncodeToString(headerBytes)
	p := base64.RawURLEncoding.EncodeToString(payloadBytes)
	sigInput := h + "." + p
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(sigInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return sigInput + "." + sig
}
