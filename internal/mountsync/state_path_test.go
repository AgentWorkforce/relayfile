package mountsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveMountStatePathUsesStateDirAndStableMountID(t *testing.T) {
	stateDir := t.TempDir()
	localDir := t.TempDir()

	first, err := ResolveMountStatePath(MountStatePathOptions{
		WorkspaceID:     "rw_123",
		RemoteRoot:      "/github/repos/AgentWorkforce/cloud/issues/1290",
		LocalRoot:       localDir,
		StateDir:        stateDir,
		MountKind:       MountKindDaemon,
		ValidateOutside: true,
	})
	if err != nil {
		t.Fatalf("ResolveMountStatePath failed: %v", err)
	}
	second, err := ResolveMountStatePath(MountStatePathOptions{
		WorkspaceID:     "rw_123",
		RemoteRoot:      "/github/repos/AgentWorkforce/cloud/issues/1290",
		LocalRoot:       localDir,
		StateDir:        stateDir,
		MountKind:       MountKindDaemon,
		ValidateOutside: true,
	})
	if err != nil {
		t.Fatalf("ResolveMountStatePath failed: %v", err)
	}

	if first.MountID == "" || first.MountID != second.MountID {
		t.Fatalf("expected stable non-empty mount id, got %q and %q", first.MountID, second.MountID)
	}
	want := filepath.Join(stateDir, first.MountID, "state.json")
	if first.StateFile != want {
		t.Fatalf("expected state file %q, got %q", want, first.StateFile)
	}
	if strings.HasPrefix(first.StateFile, localDir+string(os.PathSeparator)) {
		t.Fatalf("state file %q is inside local mount root %q", first.StateFile, localDir)
	}
}

func TestResolveMountStatePathCanonicalizesRelativeLocalRoot(t *testing.T) {
	parent := t.TempDir()
	localName := "mount"
	localDir := filepath.Join(parent, localName)
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatalf("mkdir local dir: %v", err)
	}
	stateDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(localDir); err != nil {
		t.Fatalf("chdir local dir: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get local working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})

	relative, err := ResolveMountStatePath(MountStatePathOptions{
		WorkspaceID:     "rw_123",
		RemoteRoot:      "/github/repos/AgentWorkforce/relayfile",
		LocalRoot:       ".",
		StateDir:        stateDir,
		ValidateOutside: true,
	})
	if err != nil {
		t.Fatalf("ResolveMountStatePath(dot local root) failed: %v", err)
	}
	absolute, err := ResolveMountStatePath(MountStatePathOptions{
		WorkspaceID:     "rw_123",
		RemoteRoot:      "/github/repos/AgentWorkforce/relayfile",
		LocalRoot:       cwd,
		StateDir:        stateDir,
		ValidateOutside: true,
	})
	if err != nil {
		t.Fatalf("ResolveMountStatePath(absolute) failed: %v", err)
	}
	if relative.MountID != absolute.MountID {
		t.Fatalf("expected relative and absolute local roots to share mount id, got %q and %q", relative.MountID, absolute.MountID)
	}
}

func TestResolveMountStatePathMountKindSeparatesState(t *testing.T) {
	stateDir := t.TempDir()
	localDir := t.TempDir()
	input := MountStatePathOptions{
		WorkspaceID:     "rw_123",
		RemoteRoot:      "/slack/channels/proj-cloud",
		LocalRoot:       localDir,
		StateDir:        stateDir,
		ValidateOutside: true,
	}
	ids := map[string]bool{}
	for _, kind := range []string{MountKindDaemon, MountKindFlush, MountKindInitialSync} {
		input.MountKind = kind
		resolved, err := ResolveMountStatePath(input)
		if err != nil {
			t.Fatalf("ResolveMountStatePath(%s) failed: %v", kind, err)
		}
		if ids[resolved.MountID] {
			t.Fatalf("mount kind %s reused mount id %s", kind, resolved.MountID)
		}
		ids[resolved.MountID] = true
	}
}

func TestResolveMountStatePathStateFileOverridesStateDir(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "exact.json")
	resolved, err := ResolveMountStatePath(MountStatePathOptions{
		WorkspaceID:     "rw_123",
		RemoteRoot:      "/memory/workspace",
		LocalRoot:       t.TempDir(),
		StateDir:        t.TempDir(),
		StateFile:       stateFile,
		ValidateOutside: true,
	})
	if err != nil {
		t.Fatalf("ResolveMountStatePath failed: %v", err)
	}
	if !resolved.Override {
		t.Fatal("expected explicit state-file override")
	}
	if resolved.StateFile != stateFile {
		t.Fatalf("expected explicit state file %q, got %q", stateFile, resolved.StateFile)
	}
	if resolved.MountID != "" {
		t.Fatalf("explicit state-file should not derive mount id, got %q", resolved.MountID)
	}
}

func TestResolveMountStatePathRejectsPrivateStateInsideMountRoot(t *testing.T) {
	localDir := t.TempDir()
	_, err := ResolveMountStatePath(MountStatePathOptions{
		WorkspaceID:     "rw_123",
		RemoteRoot:      "/google-mail/messages",
		LocalRoot:       localDir,
		StateFile:       filepath.Join(localDir, LegacyMountStateFileName),
		ValidateOutside: true,
	})
	if err == nil {
		t.Fatal("expected state file under local mount root to be rejected")
	}
	if !strings.Contains(err.Error(), "outside local mount root") {
		t.Fatalf("expected local mount root error, got %v", err)
	}
}

func TestResolveMountStatePathRejectsPrivateStateInsideScopedMountRoot(t *testing.T) {
	localDir := t.TempDir()
	scopedRoot := filepath.Join(localDir, "github", "repos", "AgentWorkforce", "cloud")
	_, err := ResolveMountStatePath(MountStatePathOptions{
		WorkspaceID:      "rw_123",
		RemoteRoot:       "/github/repos/AgentWorkforce/cloud/issues/1290",
		LocalRoot:        localDir,
		StateFile:        filepath.Join(scopedRoot, LegacyMountStateFileName),
		ValidateOutside:  true,
		ScopedLocalRoots: []string{scopedRoot},
	})
	if err == nil {
		t.Fatal("expected state file under scoped local root to be rejected")
	}
}

func TestResolveMountStatePathAllowsRepeatedProviderSegmentsOutsideStatePath(t *testing.T) {
	_, err := ResolveMountStatePath(MountStatePathOptions{
		WorkspaceID:     "rw_123",
		RemoteRoot:      "/github/repos/AgentWorkforce/cloud/issues/1290/github/repos/AgentWorkforce/cloud/issues/1290",
		LocalRoot:       t.TempDir(),
		StateDir:        t.TempDir(),
		ValidateOutside: true,
	})
	if err != nil {
		t.Fatalf("repeated provider segments in remote payload path should not fail state validation: %v", err)
	}
}

func TestResolveMountStatePathRejectsControlCharacters(t *testing.T) {
	_, err := ResolveMountStatePath(MountStatePathOptions{
		WorkspaceID:     "rw_123",
		RemoteRoot:      "/slack",
		LocalRoot:       t.TempDir(),
		StateDir:        filepath.Join(t.TempDir(), "bad\npath"),
		ValidateOutside: true,
	})
	if err == nil {
		t.Fatal("expected control character path to be rejected")
	}
}

func TestQuarantineLegacyMountStateOnlyMovesExactPrivateStateFiles(t *testing.T) {
	localDir := t.TempDir()
	stateDir := t.TempDir()
	files := map[string]string{
		LegacyMountStateFileName:                         "state",
		LegacyMountStateFileName + ".tmp-123":            "tmp",
		"." + LegacyMountStateFileName + ".tmp-1":        "oldtmp",
		LegacyMountStateFileName + ".backup":             "backup",
		filepath.Join(".relay", "state.json"):            "public",
		filepath.Join(".relay", "dead-letter", "x.json"): "dead",
		"provider-state.json":                            "provider",
	}
	for name, body := range files {
		path := filepath.Join(localDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	moved, err := QuarantineLegacyMountState(localDir, stateDir)
	if err != nil {
		t.Fatalf("QuarantineLegacyMountState failed: %v", err)
	}
	if len(moved) != 3 {
		t.Fatalf("expected 3 quarantined files, got %d: %v", len(moved), moved)
	}
	for _, name := range []string{
		LegacyMountStateFileName,
		LegacyMountStateFileName + ".tmp-123",
		"." + LegacyMountStateFileName + ".tmp-1",
	} {
		if _, err := os.Stat(filepath.Join(localDir, name)); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be moved, stat err=%v", name, err)
		}
	}
	for _, name := range []string{
		LegacyMountStateFileName + ".backup",
		filepath.Join(".relay", "state.json"),
		filepath.Join(".relay", "dead-letter", "x.json"),
		"provider-state.json",
	} {
		if _, err := os.Stat(filepath.Join(localDir, name)); err != nil {
			t.Fatalf("expected %s to remain, stat err=%v", name, err)
		}
	}
}

func TestNewSyncerRemountPrunesOnlyStalePrivateStateTemps(t *testing.T) {
	now := time.Now()
	localDir := t.TempDir()
	stateDir := t.TempDir()
	opts := SyncerOptions{
		WorkspaceID:   "rw_remount",
		RemoteRoot:    "/github/repos/AgentWorkforce/relayfile",
		LocalRoot:     localDir,
		StateDir:      stateDir,
		MountKind:     MountKindDaemon,
		ValidateState: true,
	}
	resolved, err := ResolveMountStatePath(MountStatePathOptions{
		WorkspaceID:     opts.WorkspaceID,
		RemoteRoot:      opts.RemoteRoot,
		LocalRoot:       opts.LocalRoot,
		StateDir:        opts.StateDir,
		MountKind:       opts.MountKind,
		ValidateOutside: true,
	})
	if err != nil {
		t.Fatalf("resolve private state path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(resolved.StateFile), 0o755); err != nil {
		t.Fatalf("mkdir private state directory: %v", err)
	}
	validState := []byte(`{"bootstrapComplete":true}`)
	if err := os.WriteFile(resolved.StateFile, validState, 0o644); err != nil {
		t.Fatalf("write reusable private state: %v", err)
	}

	activeTemp, err := os.CreateTemp(filepath.Dir(resolved.StateFile), atomicTempPattern(resolved.StateFile))
	if err != nil {
		t.Fatalf("create active state save: %v", err)
	}
	t.Cleanup(func() { _ = activeTemp.Close() })
	if _, err := activeTemp.WriteString(`{"inProgress":true}`); err != nil {
		t.Fatalf("write active state save: %v", err)
	}

	unrelated := filepath.Join(filepath.Dir(resolved.StateFile), ".unrelated.tmp-old")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write unrelated temp: %v", err)
	}
	old := now.Add(-staleMountStateTempAge - time.Hour)
	if err := os.Chtimes(unrelated, old, old); err != nil {
		t.Fatalf("age unrelated temp: %v", err)
	}

	legacyTemp := filepath.Join(localDir, LegacyMountStateFileName+".tmp-recoverable")
	if err := os.WriteFile(legacyTemp, []byte("recoverable"), 0o644); err != nil {
		t.Fatalf("write legacy migration candidate: %v", err)
	}
	if err := os.Chtimes(legacyTemp, old, old); err != nil {
		t.Fatalf("age legacy migration candidate: %v", err)
	}

	for remount := 1; remount <= 3; remount++ {
		staleTemp, err := os.CreateTemp(filepath.Dir(resolved.StateFile), atomicTempPattern(resolved.StateFile))
		if err != nil {
			t.Fatalf("remount %d create stale state temp: %v", remount, err)
		}
		stalePath := staleTemp.Name()
		if _, err := staleTemp.WriteString(`{"abandoned":true}`); err != nil {
			_ = staleTemp.Close()
			t.Fatalf("remount %d write stale state temp: %v", remount, err)
		}
		if err := staleTemp.Close(); err != nil {
			t.Fatalf("remount %d close stale state temp: %v", remount, err)
		}
		if err := os.Chtimes(stalePath, old, old); err != nil {
			t.Fatalf("remount %d age stale state temp: %v", remount, err)
		}

		if _, err := NewSyncer(&fakeClient{}, opts); err != nil {
			t.Fatalf("remount %d NewSyncer failed: %v", remount, err)
		}
		if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
			t.Fatalf("remount %d left stale state temp %s, stat err=%v", remount, stalePath, err)
		}
		if _, err := os.Stat(activeTemp.Name()); err != nil {
			t.Fatalf("remount %d removed active concurrent state save: %v", remount, err)
		}
		if _, err := os.Stat(unrelated); err != nil {
			t.Fatalf("remount %d removed unrelated temp: %v", remount, err)
		}
		if payload, err := os.ReadFile(resolved.StateFile); err != nil {
			t.Fatalf("remount %d removed reusable private state: %v", remount, err)
		} else if string(payload) != string(validState) {
			t.Fatalf("remount %d changed reusable private state: got %q", remount, payload)
		}
	}

	if _, err := os.Stat(legacyTemp); !os.IsNotExist(err) {
		t.Fatalf("legacy migration candidate was not quarantined, stat err=%v", err)
	}
	quarantineEntries, err := os.ReadDir(filepath.Join(stateDir, "quarantine"))
	if err != nil {
		t.Fatalf("read legacy quarantine: %v", err)
	}
	foundRecoverable := false
	for _, entry := range quarantineEntries {
		payload, err := os.ReadFile(filepath.Join(stateDir, "quarantine", entry.Name()))
		if err != nil {
			t.Fatalf("read quarantined legacy state: %v", err)
		}
		if string(payload) == "recoverable" {
			foundRecoverable = true
		}
	}
	if !foundRecoverable {
		t.Fatal("legacy migration candidate was cleaned instead of preserved in quarantine")
	}
}
