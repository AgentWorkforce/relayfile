package mountsync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultMountStateDirName  = ".relayfile-mount-state"
	LegacyMountStateFileName  = ".relayfile-mount-state.json"
	defaultPrivateStateFile   = "state.json"
	MountKindDaemon           = "daemon"
	MountKindFlush            = "flush"
	MountKindInitialSync      = "initial-sync"
	maxStatePathLength        = 1024
	maxStatePathComponentSize = 255
)

type MountStatePathOptions struct {
	WorkspaceID      string
	RemoteRoot       string
	LocalRoot        string
	StateFile        string
	StateDir         string
	MountKind        string
	ValidateOutside  bool
	ScopedLocalRoots []string
}

type MountStatePath struct {
	StateFile string
	StateDir  string
	MountID   string
	Kind      string
	Override  bool
}

func DefaultMountStateDir() string {
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		return filepath.Join(home, DefaultMountStateDirName)
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, DefaultMountStateDirName)
	}
	return filepath.Join(os.TempDir(), DefaultMountStateDirName)
}

func ResolveMountStatePath(opts MountStatePathOptions) (MountStatePath, error) {
	localRoot := filepath.Clean(strings.TrimSpace(opts.LocalRoot))
	if localRoot == "" || localRoot == "." {
		return MountStatePath{}, fmt.Errorf("local root is required")
	}
	kind := NormalizeMountKind(opts.MountKind)
	stateFile := strings.TrimSpace(opts.StateFile)
	if stateFile != "" {
		cleaned, err := cleanAbsolutePath(stateFile)
		if err != nil {
			return MountStatePath{}, fmt.Errorf("state file: %w", err)
		}
		if opts.ValidateOutside {
			if err := validatePrivateStatePath(cleaned, localRoot, opts.ScopedLocalRoots); err != nil {
				return MountStatePath{}, err
			}
		}
		return MountStatePath{
			StateFile: cleaned,
			StateDir:  filepath.Dir(cleaned),
			Kind:      kind,
			Override:  true,
		}, nil
	}

	stateDir := strings.TrimSpace(opts.StateDir)
	if stateDir == "" {
		stateDir = DefaultMountStateDir()
	}
	cleanDir, err := cleanAbsolutePath(stateDir)
	if err != nil {
		return MountStatePath{}, fmt.Errorf("state dir: %w", err)
	}
	mountID := MountStateID(opts.WorkspaceID, opts.RemoteRoot, localRoot, kind)
	resolved := filepath.Join(cleanDir, mountID, defaultPrivateStateFile)
	if opts.ValidateOutside {
		if err := validatePrivateStatePath(resolved, localRoot, opts.ScopedLocalRoots); err != nil {
			return MountStatePath{}, err
		}
	}
	return MountStatePath{
		StateFile: resolved,
		StateDir:  cleanDir,
		MountID:   mountID,
		Kind:      kind,
	}, nil
}

func NormalizeMountKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case MountKindFlush:
		return MountKindFlush
	case MountKindInitialSync:
		return MountKindInitialSync
	default:
		return MountKindDaemon
	}
}

func MountStateID(workspaceID, remoteRoot, localRoot, mountKind string) string {
	input := strings.Join([]string{
		strings.TrimSpace(workspaceID),
		normalizeRemotePath(remoteRoot),
		filepath.Clean(strings.TrimSpace(localRoot)),
		NormalizeMountKind(mountKind),
	}, "\x00")
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])[:32]
}

func QuarantineLegacyMountState(localRoot, stateDir string) ([]string, error) {
	localRoot = filepath.Clean(strings.TrimSpace(localRoot))
	if localRoot == "" || localRoot == "." {
		return nil, nil
	}
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		stateDir = DefaultMountStateDir()
	}
	stateDirAbs, err := cleanAbsolutePath(stateDir)
	if err != nil {
		return nil, err
	}
	quarantineDir := filepath.Join(stateDirAbs, "quarantine")
	candidates, err := legacyStateCandidates(localRoot)
	if err != nil {
		return nil, err
	}
	moved := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		info, err := os.Lstat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return moved, err
		}
		if info.IsDir() {
			continue
		}
		if err := os.MkdirAll(quarantineDir, 0o700); err != nil {
			return moved, err
		}
		target := filepath.Join(quarantineDir, filepath.Base(localRoot)+"-"+filepath.Base(candidate))
		target = uniqueQuarantinePath(target)
		if err := os.Rename(candidate, target); err != nil {
			return moved, err
		}
		moved = append(moved, target)
	}
	return moved, nil
}

func legacyStateCandidates(localRoot string) ([]string, error) {
	entries, err := os.ReadDir(localRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var candidates []string
	for _, entry := range entries {
		name := entry.Name()
		if name == LegacyMountStateFileName ||
			strings.HasPrefix(name, LegacyMountStateFileName+".tmp-") ||
			strings.HasPrefix(name, "."+LegacyMountStateFileName+".tmp-") {
			candidates = append(candidates, filepath.Join(localRoot, name))
		}
	}
	return candidates, nil
}

func uniqueQuarantinePath(path string) string {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func cleanAbsolutePath(path string) (string, error) {
	if hasControlCharacter(path) {
		return "", fmt.Errorf("contains control characters")
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func validatePrivateStatePath(statePath, localRoot string, extraRoots []string) error {
	statePath = filepath.Clean(statePath)
	if !filepath.IsAbs(statePath) {
		return fmt.Errorf("state path must be absolute")
	}
	if len(statePath) > maxStatePathLength {
		return fmt.Errorf("state path exceeds %d bytes", maxStatePathLength)
	}
	if err := validatePathComponents(statePath); err != nil {
		return err
	}
	roots := append([]string{localRoot}, extraRoots...)
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		rootAbs, err := cleanAbsolutePath(root)
		if err != nil {
			return err
		}
		if pathInsideOrEqual(statePath, rootAbs) {
			return fmt.Errorf("private state path %s must be outside local mount root %s", statePath, rootAbs)
		}
	}
	return nil
}

func validatePathComponents(path string) error {
	volume := filepath.VolumeName(path)
	rest := strings.TrimPrefix(path[len(volume):], string(os.PathSeparator))
	for _, part := range strings.Split(rest, string(os.PathSeparator)) {
		if part == "" {
			return fmt.Errorf("state path contains empty normalized segment")
		}
		if len(part) > maxStatePathComponentSize {
			return fmt.Errorf("state path component %q exceeds %d bytes", part, maxStatePathComponentSize)
		}
		if hasControlCharacter(part) {
			return fmt.Errorf("state path component %q contains control characters", part)
		}
	}
	return nil
}

func hasControlCharacter(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func pathInsideOrEqual(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}
