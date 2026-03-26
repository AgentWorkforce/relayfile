package mountfuse

import (
	"context"
	"errors"
	"hash/fnv"
	"log"
	"mime"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountsync"
	gofusefs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

const (
	defaultAttrTTL     = 2 * time.Second
	defaultEntryTTL    = 5 * time.Second
	defaultNegativeTTL = 1 * time.Second
	defaultDirMode     = 0o755
	defaultFileMode    = 0o644
)

type Config struct {
	Client          mountsync.RemoteClient
	WorkspaceID     string
	RemoteRoot      string
	AttrTTL         time.Duration
	EntryTTL        time.Duration
	NegativeTimeout time.Duration
	UID             uint32
	GID             uint32
	Logger          *log.Logger
}

type MountedFS struct {
	Server *fuse.Server
	Root   *DirNode
}

func New(cfg Config) (*DirNode, error) {
	if cfg.Client == nil {
		return nil, errors.New("mountfuse: client is required")
	}
	if strings.TrimSpace(cfg.WorkspaceID) == "" {
		return nil, errors.New("mountfuse: workspace ID is required")
	}
	state := newFSState(cfg)
	return newDirNode(state, state.remoteRoot), nil
}

func Mount(mountPoint string, cfg Config) (*MountedFS, error) {
	root, err := New(cfg)
	if err != nil {
		return nil, err
	}
	opts := &gofusefs.Options{
		EntryTimeout:    durationPtr(root.state.entryTTL),
		AttrTimeout:     durationPtr(root.state.attrTTL),
		NegativeTimeout: durationPtr(root.state.negativeTTL),
		NullPermissions: true,
		UID:             cfg.UID,
		GID:             cfg.GID,
		RootStableAttr: &gofusefs.StableAttr{
			Ino:  root.state.inodeFor(root.path, true),
			Mode: syscall.S_IFDIR,
		},
	}
	server, err := gofusefs.Mount(mountPoint, root, opts)
	if err != nil {
		return nil, err
	}
	return &MountedFS{Server: server, Root: root}, nil
}

func (m *MountedFS) Unmount() error {
	if m == nil || m.Server == nil {
		return nil
	}
	return m.Server.Unmount()
}

// NewInvalidator creates a WSInvalidator that will invalidate the internal
// filesystem cache when remote changes are detected over WebSocket.
func (m *MountedFS) NewInvalidator(baseURL, token, workspaceID string, logger *log.Logger) *WSInvalidator {
	return NewWSInvalidator(baseURL, token, workspaceID, m.Root.state, logger)
}

type fsState struct {
	client      mountsync.RemoteClient
	workspaceID string
	remoteRoot  string
	attrTTL     time.Duration
	entryTTL    time.Duration
	negativeTTL time.Duration
	uid         uint32
	gid         uint32
	logger      *log.Logger
	cacheMu     sync.RWMutex
	dirCache    map[string]cachedDir
	fileCache   map[string]cachedFile
	inodeMu     sync.Mutex
	inodeByPath map[string]uint64
	pathByInode map[uint64]string
}

type nodeMeta struct {
	path        string
	name        string
	mode        uint32
	revision    string
	size        uint64
	modTime     time.Time
	contentType string
}

type cachedDir struct {
	entries   map[string]nodeMeta
	expiresAt time.Time
}

type cachedFile struct {
	file      mountsync.RemoteFile
	expiresAt time.Time
}

func newFSState(cfg Config) *fsState {
	attrTTL := cfg.AttrTTL
	if attrTTL <= 0 {
		attrTTL = defaultAttrTTL
	}
	entryTTL := cfg.EntryTTL
	if entryTTL <= 0 {
		entryTTL = defaultEntryTTL
	}
	negativeTTL := cfg.NegativeTimeout
	if negativeTTL <= 0 {
		negativeTTL = defaultNegativeTTL
	}
	return &fsState{
		client:      cfg.Client,
		workspaceID: strings.TrimSpace(cfg.WorkspaceID),
		remoteRoot:  normalizeRemotePath(cfg.RemoteRoot),
		attrTTL:     attrTTL,
		entryTTL:    entryTTL,
		negativeTTL: negativeTTL,
		uid:         cfg.UID,
		gid:         cfg.GID,
		logger:      cfg.Logger,
		dirCache:    make(map[string]cachedDir),
		fileCache:   make(map[string]cachedFile),
		inodeByPath: map[string]uint64{normalizeRemotePath(cfg.RemoteRoot): 1},
		pathByInode: map[uint64]string{1: normalizeRemotePath(cfg.RemoteRoot)},
	}
}

func (s *fsState) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

func (s *fsState) inodeFor(remotePath string, isDir bool) uint64 {
	remotePath = normalizeRemotePath(remotePath)
	s.inodeMu.Lock()
	defer s.inodeMu.Unlock()
	if ino, ok := s.inodeByPath[remotePath]; ok {
		return ino
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(remotePath))
	sum := h.Sum64()
	if sum == 0 || sum == ^uint64(0) {
		sum = 2
	}
	if isDir {
		sum |= 1
	} else {
		sum &^= 1
	}
	for {
		if existing, ok := s.pathByInode[sum]; !ok || existing == remotePath {
			s.inodeByPath[remotePath] = sum
			s.pathByInode[sum] = remotePath
			return sum
		}
		sum++
		if sum == ^uint64(0) {
			sum = 2
		}
	}
}

func (s *fsState) getDir(remotePath string) (map[string]nodeMeta, bool) {
	remotePath = normalizeRemotePath(remotePath)
	now := time.Now()
	s.cacheMu.RLock()
	cached, ok := s.dirCache[remotePath]
	s.cacheMu.RUnlock()
	if !ok || now.After(cached.expiresAt) {
		if ok {
			s.cacheMu.Lock()
			delete(s.dirCache, remotePath)
			s.cacheMu.Unlock()
		}
		return nil, false
	}
	out := make(map[string]nodeMeta, len(cached.entries))
	for name, meta := range cached.entries {
		out[name] = meta
	}
	return out, true
}

func (s *fsState) putDir(remotePath string, entries map[string]nodeMeta) {
	remotePath = normalizeRemotePath(remotePath)
	copyEntries := make(map[string]nodeMeta, len(entries))
	for name, meta := range entries {
		copyEntries[name] = meta
	}
	s.cacheMu.Lock()
	s.dirCache[remotePath] = cachedDir{
		entries:   copyEntries,
		expiresAt: time.Now().Add(s.entryTTL),
	}
	s.cacheMu.Unlock()
}

func (s *fsState) getFile(remotePath string) (mountsync.RemoteFile, bool) {
	remotePath = normalizeRemotePath(remotePath)
	now := time.Now()
	s.cacheMu.RLock()
	cached, ok := s.fileCache[remotePath]
	s.cacheMu.RUnlock()
	if !ok || now.After(cached.expiresAt) {
		if ok {
			s.cacheMu.Lock()
			delete(s.fileCache, remotePath)
			s.cacheMu.Unlock()
		}
		return mountsync.RemoteFile{}, false
	}
	return cached.file, true
}

func (s *fsState) putFile(file mountsync.RemoteFile) {
	file.Path = normalizeRemotePath(file.Path)
	s.cacheMu.Lock()
	s.fileCache[file.Path] = cachedFile{
		file:      file,
		expiresAt: time.Now().Add(s.attrTTL),
	}
	s.cacheMu.Unlock()
}

func (s *fsState) invalidate(remotePath string) {
	remotePath = normalizeRemotePath(remotePath)
	s.cacheMu.Lock()
	delete(s.fileCache, remotePath)
	delete(s.dirCache, remotePath)
	parent := path.Dir(remotePath)
	if parent == "." {
		parent = "/"
	}
	delete(s.dirCache, parent)
	s.cacheMu.Unlock()
}

func (s *fsState) listDirectory(ctx context.Context, remotePath string) (map[string]nodeMeta, error) {
	remotePath = normalizeRemotePath(remotePath)
	if entries, ok := s.getDir(remotePath); ok {
		return entries, nil
	}
	resp, err := s.client.ListTree(ctx, s.workspaceID, remotePath, 1, "")
	if err != nil {
		return nil, err
	}
	entries := map[string]nodeMeta{}
	for _, entry := range resp.Entries {
		meta, ok := s.treeEntryToMeta(entry, remotePath)
		if !ok {
			continue
		}
		entries[meta.name] = meta
	}
	s.putDir(remotePath, entries)
	return entries, nil
}

func (s *fsState) lookupMetadata(ctx context.Context, remotePath string) (nodeMeta, error) {
	remotePath = normalizeRemotePath(remotePath)
	if remotePath == s.remoteRoot {
		return nodeMeta{
			path:    remotePath,
			name:    path.Base(remotePath),
			mode:    syscall.S_IFDIR | defaultDirMode,
			modTime: time.Now(),
		}, nil
	}
	parentPath, name := splitParent(remotePath)
	entries, err := s.listDirectory(ctx, parentPath)
	if err != nil {
		return nodeMeta{}, err
	}
	meta, ok := entries[name]
	if !ok {
		return nodeMeta{}, &mountsync.HTTPError{StatusCode: 404, Code: "not_found", Message: "not found"}
	}
	if meta.isFile() && meta.size == 0 {
		if file, err := s.readFile(ctx, remotePath); err == nil {
			meta.size = uint64(len(file.Content))
			meta.revision = file.Revision
			meta.contentType = file.ContentType
		}
	}
	return meta, nil
}

func (s *fsState) readFile(ctx context.Context, remotePath string) (mountsync.RemoteFile, error) {
	remotePath = normalizeRemotePath(remotePath)
	if file, ok := s.getFile(remotePath); ok {
		return file, nil
	}
	file, err := s.client.ReadFile(ctx, s.workspaceID, remotePath)
	if err != nil {
		return mountsync.RemoteFile{}, err
	}
	file.Path = normalizeRemotePath(file.Path)
	s.putFile(file)
	return file, nil
}

func (s *fsState) treeEntryToMeta(entry mountsync.TreeEntry, parentPath string) (nodeMeta, bool) {
	childPath := normalizeRemotePath(entry.Path)
	parentPath = normalizeRemotePath(parentPath)
	if childPath == parentPath {
		return nodeMeta{}, false
	}
	rel := strings.TrimPrefix(childPath, parentPath)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" || strings.Contains(rel, "/") {
		return nodeMeta{}, false
	}
	mode := uint32(syscall.S_IFREG | defaultFileMode)
	if strings.EqualFold(entry.Type, "directory") || strings.EqualFold(entry.Type, "dir") {
		mode = syscall.S_IFDIR | defaultDirMode
	}
	return nodeMeta{
		path:     childPath,
		name:     rel,
		mode:     mode,
		revision: entry.Revision,
		modTime:  time.Now(),
	}, true
}

func (m nodeMeta) isDir() bool {
	return m.mode&syscall.S_IFMT == syscall.S_IFDIR
}

func (m nodeMeta) isFile() bool {
	return m.mode&syscall.S_IFMT == syscall.S_IFREG
}

func (m nodeMeta) fillEntry(out *fuse.EntryOut, state *fsState) {
	out.Attr.Ino = state.inodeFor(m.path, m.isDir())
	out.Attr.Mode = m.mode
	out.Attr.Size = m.size
	out.Attr.Owner = fuse.Owner{Uid: state.uid, Gid: state.gid}
	out.Attr.SetTimes(nil, &m.modTime, &m.modTime)
	out.SetEntryTimeout(state.entryTTL)
	out.SetAttrTimeout(state.attrTTL)
}

func (m nodeMeta) fillAttr(out *fuse.AttrOut, state *fsState) {
	out.Attr.Ino = state.inodeFor(m.path, m.isDir())
	out.Attr.Mode = m.mode
	out.Attr.Size = m.size
	out.Attr.Owner = fuse.Owner{Uid: state.uid, Gid: state.gid}
	out.Attr.Nlink = 1
	if m.isDir() {
		out.Attr.Nlink = 2
		out.Attr.Size = 4096
	}
	out.Attr.SetTimes(nil, &m.modTime, &m.modTime)
	out.SetTimeout(state.attrTTL)
}

func durationPtr(value time.Duration) *time.Duration {
	v := value
	return &v
}

func normalizeRemotePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/"
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(value, "/"))
	if cleaned == "." {
		return "/"
	}
	return cleaned
}

func splitParent(remotePath string) (string, string) {
	remotePath = normalizeRemotePath(remotePath)
	if remotePath == "/" {
		return "/", ""
	}
	return normalizeRemotePath(path.Dir(remotePath)), path.Base(remotePath)
}

func joinRemotePath(parentPath, name string) string {
	parentPath = normalizeRemotePath(parentPath)
	name = strings.TrimSpace(name)
	if parentPath == "/" {
		return normalizeRemotePath("/" + name)
	}
	return normalizeRemotePath(path.Join(parentPath, name))
}

func newChildNode(parent *DirNode, meta nodeMeta) *gofusefs.Inode {
	if meta.isDir() {
		node := newDirNode(parent.state, meta.path)
		return parent.NewInode(context.Background(), node, gofusefs.StableAttr{
			Mode: syscall.S_IFDIR,
			Ino:  parent.state.inodeFor(meta.path, true),
		})
	}
	node := newFileNode(parent.state, meta)
	return parent.NewInode(context.Background(), node, gofusefs.StableAttr{
		Mode: syscall.S_IFREG,
		Ino:  parent.state.inodeFor(meta.path, false),
	})
}

func sortedDirEntries(entries map[string]nodeMeta, state *fsState) []fuse.DirEntry {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]fuse.DirEntry, 0, len(names))
	for _, name := range names {
		meta := entries[name]
		out = append(out, fuse.DirEntry{
			Name: name,
			Mode: meta.mode,
			Ino:  state.inodeFor(meta.path, meta.isDir()),
		})
	}
	return out
}

func readErrno(err error) syscall.Errno {
	return mapErrno(err, syscall.ENOENT)
}

func writeErrno(err error) syscall.Errno {
	return mapErrno(err, syscall.EPERM)
}

func mapErrno(err error, forbidden syscall.Errno) syscall.Errno {
	if err == nil {
		return 0
	}
	if errors.Is(err, mountsync.ErrConflict) {
		return syscall.EAGAIN
	}
	if errors.Is(err, context.Canceled) {
		return syscall.EINTR
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return syscall.ETIMEDOUT
	}
	var httpErr *mountsync.HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case 403:
			return forbidden
		case 404:
			return syscall.ENOENT
		case 409, 412, 429:
			return syscall.EAGAIN
		default:
			if httpErr.StatusCode >= 500 {
				return syscall.EIO
			}
		}
	}
	return syscall.EIO
}

func contentTypeForPath(remotePath string) string {
	if ext := path.Ext(remotePath); ext != "" {
		if detected := mime.TypeByExtension(ext); detected != "" {
			return detected
		}
	}
	return "text/plain; charset=utf-8"
}
