package mountlease

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrHeld = errors.New("relayfile workspace mount lease is already held")

// ErrNotHeld is returned by Inspect when no supervisor currently owns the
// local mirror. --notify-flush uses this to refuse kicking a daemon that is
// not running, rather than starting a second supervisor.
var ErrNotHeld = errors.New("relayfile workspace mount lease is not held")

// Info is the public view of a held mount lease. Callers use PID to signal
// the existing supervisor; they must not treat this as a lock grant.
type Info struct {
	PID         int    `json:"pid"`
	Server      string `json:"server"`
	WorkspaceID string `json:"workspaceId"`
	LocalRoot   string `json:"localRoot"`
	StartedAt   string `json:"startedAt"`
}

// FlushAck is written by the running daemon after a SIGUSR1-triggered
// reconcile. --notify-flush waits for Seq to advance rather than taking
// the lease itself. OK is false when the kicked cycle failed; waiters
// must not treat that as a successful barrier.
type FlushAck struct {
	Seq   int    `json:"seq"`
	PID   int    `json:"pid"`
	At    string `json:"at"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

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
	id, err := resolveLeaseIdentity(cacheDir, server, workspaceID, localRoot)
	if err != nil {
		return nil, err
	}
	if err := ensureLeaseDir(id.dir); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(id.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
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
		return nil, fmt.Errorf("%w for workspace %s at %s using local root %s; stop the existing mount before starting another supervisor", ErrHeld, id.workspaceID, id.server, id.localRoot)
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
		Server:      id.server,
		WorkspaceID: id.workspaceID,
		LocalRoot:   id.localRoot,
		StartedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		unlock()
		_ = file.Close()
		return nil, fmt.Errorf("write mount lease metadata: %w", err)
	}
	return &Lease{file: file, unlock: unlock}, nil
}

type leaseIdentity struct {
	server      string
	workspaceID string
	localRoot   string
	dir         string
	lockPath    string
	ackPath     string
}

func resolveLeaseIdentity(cacheDir, server, workspaceID, localRoot string) (leaseIdentity, error) {
	server = canonicalServerIdentity(server)
	workspaceID = strings.TrimSpace(workspaceID)
	localRoot = strings.TrimSpace(localRoot)
	if server == "" || workspaceID == "" || localRoot == "" {
		return leaseIdentity{}, errors.New("mount lease requires server, workspace id, and local root")
	}
	localRoot, err := filepath.Abs(localRoot)
	if err != nil {
		return leaseIdentity{}, fmt.Errorf("resolve mount lease local root: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(localRoot); err == nil {
		localRoot = resolved
	}
	dir := filepath.Join(cacheDir, "relayfile", "mount-leases")
	sum := sha256.Sum256([]byte(server + "\x00" + workspaceID + "\x00" + localRoot))
	hexID := hex.EncodeToString(sum[:])
	return leaseIdentity{
		server:      server,
		workspaceID: workspaceID,
		localRoot:   localRoot,
		dir:         dir,
		lockPath:    filepath.Join(dir, hexID+".lock"),
		ackPath:     filepath.Join(dir, hexID+".flush-ack"),
	}, nil
}

func ensureLeaseDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create mount lease directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect mount lease directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("mount lease directory %s is not a private directory", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure mount lease directory: %w", err)
	}
	return nil
}

// Inspect reads the held lease without acquiring it. Used by --notify-flush
// to find the existing supervisor PID.
func Inspect(server, workspaceID, localRoot string) (*Info, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user cache directory for mount lease: %w", err)
	}
	return inspectAt(cacheDir, server, workspaceID, localRoot)
}

func inspectAt(cacheDir, server, workspaceID, localRoot string) (*Info, error) {
	id, err := resolveLeaseIdentity(cacheDir, server, workspaceID, localRoot)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(id.lockPath, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w for workspace %s at %s using local root %s", ErrNotHeld, id.workspaceID, id.server, id.localRoot)
		}
		return nil, fmt.Errorf("open mount lease: %w", err)
	}
	defer file.Close()
	unlock, acquired, err := tryLockFile(file)
	if err != nil {
		return nil, fmt.Errorf("probe mount lease: %w", err)
	}
	if acquired {
		unlock()
		return nil, fmt.Errorf("%w for workspace %s at %s using local root %s", ErrNotHeld, id.workspaceID, id.server, id.localRoot)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("seek mount lease: %w", err)
	}
	payload, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read mount lease: %w", err)
	}
	var meta metadata
	if err := json.Unmarshal(payload, &meta); err != nil || meta.PID <= 0 {
		return nil, fmt.Errorf("%w for workspace %s at %s using local root %s", ErrNotHeld, id.workspaceID, id.server, id.localRoot)
	}
	return &Info{
		PID:         meta.PID,
		Server:      meta.Server,
		WorkspaceID: meta.WorkspaceID,
		LocalRoot:   meta.LocalRoot,
		StartedAt:   meta.StartedAt,
	}, nil
}

func ReadFlushAck(server, workspaceID, localRoot string) (FlushAck, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return FlushAck{}, fmt.Errorf("resolve user cache directory for mount lease: %w", err)
	}
	return readFlushAckAt(cacheDir, server, workspaceID, localRoot)
}

func readFlushAckAt(cacheDir, server, workspaceID, localRoot string) (FlushAck, error) {
	id, err := resolveLeaseIdentity(cacheDir, server, workspaceID, localRoot)
	if err != nil {
		return FlushAck{}, err
	}
	payload, err := os.ReadFile(id.ackPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return FlushAck{}, nil
		}
		return FlushAck{}, fmt.Errorf("read flush ack: %w", err)
	}
	var ack FlushAck
	if err := json.Unmarshal(payload, &ack); err != nil {
		return FlushAck{}, fmt.Errorf("decode flush ack: %w", err)
	}
	return ack, nil
}

func WriteFlushAck(server, workspaceID, localRoot string, ack FlushAck) error {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("resolve user cache directory for mount lease: %w", err)
	}
	return writeFlushAckAt(cacheDir, server, workspaceID, localRoot, ack)
}

func writeFlushAckAt(cacheDir, server, workspaceID, localRoot string, ack FlushAck) error {
	id, err := resolveLeaseIdentity(cacheDir, server, workspaceID, localRoot)
	if err != nil {
		return err
	}
	if err := ensureLeaseDir(id.dir); err != nil {
		return err
	}
	payload, err := json.Marshal(ack)
	if err != nil {
		return fmt.Errorf("encode flush ack: %w", err)
	}
	tmp, err := os.CreateTemp(id.dir, "flush-ack-*.tmp")
	if err != nil {
		return fmt.Errorf("create flush ack: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write flush ack: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close flush ack: %w", err)
	}
	if err := os.Rename(tmpName, id.ackPath); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("publish flush ack: %w", err)
	}
	return nil
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
