package mountsync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// These Tier-1 fixtures prove local scoping correctness with the real Syncer.
// They do NOT prove the full section-7 N-load gate; CF Durable Object
// saturation remains a separate Tier-2 measurement.

func TestTeamTier1ScopedSyncerOverlayAndConcurrentWrites(t *testing.T) {
	const members = 4

	type memberHarness struct {
		name       string
		remoteRoot string
		localRoot  string
		client     *fakeClient
		syncer     *Syncer
	}

	harnesses := make([]memberHarness, 0, members)
	for i := range members {
		name := fmt.Sprintf("member-%d", i+1)
		remoteRoot := fmt.Sprintf("/project/cloud/packages/%s", name)
		localRoot := filepath.Join(t.TempDir(), "project", "cloud", "packages", name)
		if err := os.MkdirAll(localRoot, 0o755); err != nil {
			t.Fatalf("create seeded local root: %v", err)
		}
		seedPath := filepath.Join(localRoot, "README.md")
		if err := os.WriteFile(seedPath, []byte("# seeded from one-way read context\n"), 0o644); err != nil {
			t.Fatalf("seed local root: %v", err)
		}

		client := &fakeClient{
			files: map[string]RemoteFile{
				remoteRoot + "/README.md": {
					Path:        remoteRoot + "/README.md",
					Revision:    "rev_1",
					ContentType: "text/markdown",
					Content:     "# seeded from one-way read context\n",
				},
			},
			revisionCounter: 1,
		}
		syncer := newTier1TeamMemberSyncer(t, client, SyncerOptions{
			WorkspaceID: "ws_team_tier1",
			RemoteRoot:  remoteRoot,
			LocalRoot:   localRoot,
			Scopes: []string{
				"relayfile:fs:read:" + remoteRoot + "/*",
				"relayfile:fs:write:" + remoteRoot + "/*",
			},
			WebSocket:     boolPtr(false),
			FullPullEvery: -1,
		}, remoteRoot)

		if err := syncer.SyncOnce(context.Background()); err != nil {
			t.Fatalf("%s initial overlay sync failed: %v", name, err)
		}
		if client.listTreeCalls == 0 {
			t.Fatalf("%s initial overlay sync did not pull the remote tree", name)
		}
		if client.bulkWriteCalls != 0 {
			t.Fatalf("%s seeded overlay mass-enqueued writeback batches: %#v", name, client.bulkWriteBatches)
		}

		harnesses = append(harnesses, memberHarness{
			name:       name,
			remoteRoot: remoteRoot,
			localRoot:  localRoot,
			client:     client,
			syncer:     syncer,
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, len(harnesses))
	for _, harness := range harnesses {
		harness := harness
		wg.Add(1)
		go func() {
			defer wg.Done()
			localPath := filepath.Join(harness.localRoot, "README.md")
			body := fmt.Sprintf("# update from %s\n", harness.name)
			if err := os.WriteFile(localPath, []byte(body), 0o644); err != nil {
				errs <- fmt.Errorf("%s write in-scope edit: %w", harness.name, err)
				return
			}
			if err := harness.syncer.HandleLocalChange(ctx, "README.md", fsnotify.Write); err != nil {
				errs <- fmt.Errorf("%s sync in-scope edit: %w", harness.name, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	for _, harness := range harnesses {
		if harness.client.bulkWriteCalls != 1 {
			t.Fatalf("%s bulkWriteCalls = %d, want 1; batches=%#v", harness.name, harness.client.bulkWriteCalls, harness.client.bulkWriteBatches)
		}
		if len(harness.client.bulkWriteBatches) != 1 || len(harness.client.bulkWriteBatches[0]) != 1 {
			t.Fatalf("%s expected exactly one in-scope bulk file, got %#v", harness.name, harness.client.bulkWriteBatches)
		}
		got := normalizeRemotePath(harness.client.bulkWriteBatches[0][0].Path)
		want := harness.remoteRoot + "/README.md"
		if got != want {
			t.Fatalf("%s wrote %s, want %s", harness.name, got, want)
		}
		if !strings.Contains(harness.client.files[want].Content, harness.name) {
			t.Fatalf("%s in-scope edit did not reach remote state: %#v", harness.name, harness.client.files[want])
		}
	}
}

func TestTeamTier1ScopedWriteTokenFiltersOutOfScopeNewFiles(t *testing.T) {
	const assignedRoot = "/project/cloud/packages/member-a"
	client := &fakeClient{
		files: map[string]RemoteFile{
			assignedRoot + "/README.md": {
				Path:        assignedRoot + "/README.md",
				Revision:    "rev_1",
				ContentType: "text/markdown",
				Content:     "# member A\n",
			},
		},
		revisionCounter: 1,
	}
	localRoot := t.TempDir()
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID: "ws_team_tier1_filter",
		RemoteRoot:  "/project/cloud",
		LocalRoot:   localRoot,
		Scopes: []string{
			"relayfile:fs:read:/project/cloud/*",
			"relayfile:fs:write:" + assignedRoot + "/*",
		},
		WebSocket:     boolPtr(false),
		FullPullEvery: -1,
	})
	if err != nil {
		t.Fatalf("new syncer failed: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}

	client.bulkWriteCalls = 0
	client.bulkWriteBatches = nil
	inScopeNew := filepath.Join(localRoot, "packages", "member-a", "notes.md")
	outOfScopeNew := filepath.Join(localRoot, "packages", "member-b", "SHOULD_NOT_WRITE.md")
	if err := os.MkdirAll(filepath.Dir(inScopeNew), 0o755); err != nil {
		t.Fatalf("create in-scope dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(outOfScopeNew), 0o755); err != nil {
		t.Fatalf("create out-of-scope dir: %v", err)
	}
	if err := os.WriteFile(inScopeNew, []byte("in scope\n"), 0o644); err != nil {
		t.Fatalf("write in-scope local file: %v", err)
	}
	if err := os.WriteFile(outOfScopeNew, []byte("out of scope\n"), 0o644); err != nil {
		t.Fatalf("write out-of-scope local file: %v", err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("scoped sync failed: %v", err)
	}

	got := bulkWritePaths(client.bulkWriteBatches)
	want := []string{assignedRoot + "/notes.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("bulk write paths = %#v, want %#v; batches=%#v", got, want, client.bulkWriteBatches)
	}
	if _, leaked := client.files["/project/cloud/packages/member-b/SHOULD_NOT_WRITE.md"]; leaked {
		t.Fatalf("out-of-scope local file leaked into remote state")
	}
}

func TestTeamTier1MemberSyncerRejectsBroadWriteScopes(t *testing.T) {
	assignedRoot := "/project/cloud/packages/member-a"
	cases := []struct {
		name   string
		scopes []string
	}{
		{name: "empty scopes", scopes: nil},
		{name: "short broad write", scopes: []string{"fs:write"}},
		{name: "workspace broad relayauth write", scopes: []string{"relayfile:fs:write:/*"}},
		{name: "parent broad write mixed with assigned write", scopes: []string{"relayfile:fs:write:/project/cloud/*", "relayfile:fs:write:" + assignedRoot + "/*"}},
		{name: "admin token", scopes: []string{"admin:read", "fs:write"}},
		{name: "wrong assigned path", scopes: []string{"relayfile:fs:write:/project/cloud/packages/other/*"}},
		{name: "exact assigned path without subtree wildcard", scopes: []string{"relayfile:fs:write:" + assignedRoot}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newTier1TeamMemberSyncerForTest(&fakeClient{}, SyncerOptions{
				WorkspaceID: "ws_team_tier1_reject",
				RemoteRoot:  assignedRoot,
				LocalRoot:   t.TempDir(),
				Scopes:      tc.scopes,
			}, assignedRoot)
			if err == nil {
				t.Fatalf("expected scope set %#v to be rejected", tc.scopes)
			}
		})
	}
}

func newTier1TeamMemberSyncer(t *testing.T, client RemoteClient, opts SyncerOptions, assignedRoot string) *Syncer {
	t.Helper()
	syncer, err := newTier1TeamMemberSyncerForTest(client, opts, assignedRoot)
	if err != nil {
		t.Fatalf("new tier-1 team member syncer: %v", err)
	}
	return syncer
}

func newTier1TeamMemberSyncerForTest(client RemoteClient, opts SyncerOptions, assignedRoot string) (*Syncer, error) {
	if err := validateTier1MemberWriteScopes(opts.Scopes, assignedRoot); err != nil {
		return nil, err
	}
	remoteRoot := normalizeRemotePath(opts.RemoteRoot)
	assigned := normalizeRemotePath(assignedRoot)
	if !isUnderRemoteRoot(assigned, remoteRoot) {
		return nil, fmt.Errorf("mounted remote root %s must stay within assigned root %s", remoteRoot, assigned)
	}
	return NewSyncer(client, opts)
}

func validateTier1MemberWriteScopes(scopes []string, assignedRoot string) error {
	assigned := normalizeRemotePath(assignedRoot)
	writeScopes := 0
	for _, raw := range scopes {
		scope := strings.TrimSpace(raw)
		switch {
		case scope == "fs:write" || scope == "fs:manage":
			return fmt.Errorf("team member token must not include broad workspace/admin write scopes")
		case strings.HasPrefix(scope, "admin:"):
			return fmt.Errorf("team member token must not include broad workspace/admin write scopes")
		case strings.HasPrefix(scope, "relayfile:fs:write:"):
			writeScopes++
			path := strings.TrimPrefix(scope, "relayfile:fs:write:")
			if !strings.HasSuffix(path, "/*") {
				return fmt.Errorf("team member write scope %q must cover assigned root children %s/*", scope, assigned)
			}
			path = strings.TrimSuffix(path, "/*")
			if path == "" || path == "/" || normalizeRemotePath(path) != assigned {
				return fmt.Errorf("team member write scope %q must equal assigned root %s", scope, assigned)
			}
		case strings.HasPrefix(scope, "relayfile:fs:manage:"):
			return fmt.Errorf("team member token must not include broad workspace/admin write scopes")
		}
	}
	if writeScopes == 0 {
		return fmt.Errorf("team member token must include a non-empty path-scoped write grant for %s", assigned)
	}
	return nil
}

func bulkWritePaths(batches [][]BulkWriteFile) []string {
	var paths []string
	for _, batch := range batches {
		for _, file := range batch {
			paths = append(paths, normalizeRemotePath(file.Path))
		}
	}
	sort.Strings(paths)
	return paths
}
