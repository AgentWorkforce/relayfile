package mountlease

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrHeld = errors.New("relayfile workspace mount lease is already held")

// Lease is a process-lifetime singleton for one local workspace mirror. Both
// the standalone relayfile-mount binary and the product CLI acquire the same
// key, so two supervisors cannot concurrently own the same local files even
// when they use different private state directories. Separate local mirrors of
// one hosted workspace remain valid collaboration clients.
type Lease struct {
	file    *os.File
	unlock  func()
	release sync.Once
}

type metadata struct {
	PID         int    `json:"pid"`
	Server      string `json:"server"`
	WorkspaceID string `json:"workspaceId"`
	LocalRoot   string `json:"localRoot"`
	StartedAt   string `json:"startedAt"`
}

func Acquire(server, workspaceID, localRoot string) (*Lease, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user cache directory for mount lease: %w", err)
	}
	return acquireAt(cacheDir, server, workspaceID, localRoot)
}

func acquireAt(cacheDir, server, workspaceID, localRoot string) (*Lease, error) {
	server = canonicalServerIdentity(server)
	workspaceID = strings.TrimSpace(workspaceID)
	localRoot = strings.TrimSpace(localRoot)
	if server == "" || workspaceID == "" || localRoot == "" {
		return nil, errors.New("mount lease requires server, workspace id, and local root")
	}
	localRoot, err := filepath.Abs(localRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve mount lease local root: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(localRoot); err == nil {
		localRoot = resolved
	}
	root := filepath.Join(cacheDir, "relayfile", "mount-leases")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create mount lease directory: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect mount lease directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("mount lease directory %s is not a private directory", root)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("secure mount lease directory: %w", err)
	}

	identity := server + "\x00" + workspaceID + "\x00" + localRoot
	sum := sha256.Sum256([]byte(identity))
	lockPath := filepath.Join(root, hex.EncodeToString(sum[:])+".lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open mount lease: %w", err)
	}
	unlock, acquired, err := tryLockFile(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock workspace mount lease: %w", err)
	}
	if !acquired {
		_ = file.Close()
		return nil, fmt.Errorf("%w for workspace %s at %s using local root %s; stop the existing mount before starting another supervisor", ErrHeld, workspaceID, server, localRoot)
	}
	if err := file.Truncate(0); err != nil {
		unlock()
		_ = file.Close()
		return nil, fmt.Errorf("reset mount lease metadata: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		unlock()
		_ = file.Close()
		return nil, fmt.Errorf("seek mount lease metadata: %w", err)
	}
	if err := json.NewEncoder(file).Encode(metadata{
		PID:         os.Getpid(),
		Server:      server,
		WorkspaceID: workspaceID,
		LocalRoot:   localRoot,
		StartedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		unlock()
		_ = file.Close()
		return nil, fmt.Errorf("write mount lease metadata: %w", err)
	}
	return &Lease{file: file, unlock: unlock}, nil
}

func canonicalServerIdentity(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return raw
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		parsed.Host = "[" + hostname + "]"
	} else {
		parsed.Host = hostname
	}
	parsed.User = nil
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = strings.TrimRight(parsed.RawPath, "/")
	return strings.TrimRight(parsed.String(), "/")
}

func (l *Lease) Release() error {
	if l == nil {
		return nil
	}
	var closeErr error
	l.release.Do(func() {
		if l.unlock != nil {
			l.unlock()
		}
		if l.file != nil {
			closeErr = l.file.Close()
		}
	})
	return closeErr
}
