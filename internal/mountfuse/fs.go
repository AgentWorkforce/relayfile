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

	// maxInodeCacheSize limits the inode mapping cache to prevent unbounded
	// memory growth. When exceeded, the oldest half of entries are evicted.
	maxInodeCacheSize = 100_000
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
	LazyRepos       bool
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
	lazyRepos   *LazyMaterializeCache
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
	state := &fsState{
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
	if cfg.LazyRepos {
		state.lazyRepos = NewLazyMaterializeCache()
	}
	return state
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
			s.evictInodesIfNeeded()
			return sum
		}
		sum++
		if sum == ^uint64(0) {
			sum = 2
		}
	}
}

// evictInodesIfNeeded removes entries when the cache exceeds maxInodeCacheSize.
// Caller must hold s.inodeMu.
func (s *fsState) evictInodesIfNeeded() {
	if len(s.inodeByPath) <= maxInodeCacheSize {
		return
	}
	// Evict roughly half the entries (excluding the root).
	count := 0
	target := len(s.inodeByPath) / 2
	for p, ino := range s.inodeByPath {
		if p == s.remoteRoot {
			continue
		}
		delete(s.inodeByPath, p)
		delete(s.pathByInode, ino)
		count++
		if count >= target {
			break
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

	// Remove stale inode mapping so a recreated file at the same path gets a
	// fresh inode number, preventing the kernel from serving cached stale data.
	s.inodeMu.Lock()
	if ino, ok := s.inodeByPath[remotePath]; ok {
		delete(s.inodeByPath, remotePath)
		delete(s.pathByInode, ino)
	}
	s.inodeMu.Unlock()
}

func (s *fsState) listDirectory(ctx context.Context, remotePath string) (map[string]nodeMeta, error) {
	remotePath = normalizeRemotePath(remotePath)
	if err := s.ensureGithubRepoMaterialized(ctx, remotePath); err != nil {
		return nil, err
	}
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
	if remotePath == s.remoteRoot {
		// Alias directories such as by-title/by-id/by-state are adapter-owned
		// remote entries; the only synthesized root entry here is the virtual layout.
		entries[layoutFilename] = virtualLayoutMeta(s.remoteRoot)
	}
	s.putDir(remotePath, entries)
	return entries, nil
}

func resolveDirectoryEntry(entries map[string]nodeMeta, requestedName string) (nodeMeta, string, bool) {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	if resolvedName, ok := resolveNameByID(names, requestedName); ok {
		if meta, found := entries[resolvedName]; found {
			return meta, resolvedName, true
		}
	}
	// Direct exact-match fallback: covers basenames where the parsed ID is
	// empty (e.g. "test__.json") and ID-based lookup cannot help. The
	// ID-based lookup is preferred so that canonical (name__id) siblings win
	// over legacy (id-only) basenames when both exist.
	if meta, ok := entries[requestedName]; ok {
		return meta, requestedName, true
	}
	return nodeMeta{}, "", false
}

func (s *fsState) lookupMetadata(ctx context.Context, remotePath string) (nodeMeta, error) {
	remotePath = normalizeRemotePath(remotePath)
	if err := s.ensureGithubRepoMaterialized(ctx, remotePath); err != nil {
		return nodeMeta{}, err
	}
	if remotePath == s.remoteRoot {
		return nodeMeta{
			path:    remotePath,
			name:    path.Base(remotePath),
			mode:    syscall.S_IFDIR | defaultDirMode,
			modTime: time.Now(),
		}, nil
	}
	if isVirtualLayoutPath(s.remoteRoot, remotePath) {
		return virtualLayoutMeta(s.remoteRoot), nil
	}
	parentPath, name := splitParent(remotePath)
	entries, err := s.listDirectory(ctx, parentPath)
	if err != nil {
		return nodeMeta{}, err
	}
	// Resolve legacy and new-style basenames through the shared nameid parser.
	meta, _, ok := resolveDirectoryEntry(entries, name)
	if !ok {
		return nodeMeta{}, &mountsync.HTTPError{StatusCode: 404, Code: "not_found", Message: "not found"}
	}
	if meta.isFile() && meta.size == 0 {
		if file, err := s.readFile(ctx, meta.path); err == nil {
			meta.size = uint64(len(file.Content))
			meta.revision = file.Revision
			meta.contentType = file.ContentType
		}
	}
	return meta, nil
}

func (s *fsState) readFile(ctx context.Context, remotePath string) (mountsync.RemoteFile, error) {
	remotePath = normalizeRemotePath(remotePath)
	if isVirtualLayoutPath(s.remoteRoot, remotePath) {
		return readVirtualLayout(s.remoteRoot), nil
	}
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
	switch {
	case strings.EqualFold(entry.Type, "directory"), strings.EqualFold(entry.Type, "dir"):
		mode = syscall.S_IFDIR | defaultDirMode
	case strings.EqualFold(entry.Type, "symlink"):
		// Forward compatibility: if a future adapter emits symlink-like alias
		// entries, surface them as readable files and let lazy reads populate
		// size/revision details from ReadFile.
	}
	return nodeMeta{
		path:     childPath,
		name:     rel,
		mode:     mode,
		revision: entry.Revision,
		modTime:  time.Now(),
	}, true
}

func (s *fsState) ensureGithubRepoMaterialized(ctx context.Context, remotePath string) error {
	if s.lazyRepos == nil {
		return nil
	}
	owner, repo, ok := parseGithubRepoCoords(s.remoteRoot, remotePath)
	if !ok {
		return nil
	}
	if s.lazyRepos.Mark(owner, repo) {
		client, ok := s.client.(mountsync.LazyMaterializeClient)
		if !ok {
			s.lazyRepos.Resolve(owner, repo)
			return nil
		}
		if err := client.LazyMaterialize(ctx, s.workspaceID, owner, repo); err != nil {
			s.lazyRepos.Forget(owner, repo)
			return err
		}
		s.lazyRepos.Resolve(owner, repo)
		return nil
	}
	if ch := s.lazyRepos.Wait(owner, repo); ch != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
	return nil
}

func parseGithubRepoCoords(remoteRoot, remotePath string) (owner, repo string, ok bool) {
	remoteRoot = normalizeRemotePath(remoteRoot)
	remotePath = normalizeRemotePath(remotePath)
	if !isUnderRemoteRoot(remoteRoot, remotePath) {
		return "", "", false
	}
	rel := strings.TrimPrefix(remotePath, remoteRoot)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "", "", false
	}
	segments := strings.Split(rel, "/")
	if len(segments) < 4 || segments[0] != "github" || segments[1] != "repos" {
		return "", "", false
	}
	if strings.TrimSpace(segments[2]) == "" || strings.TrimSpace(segments[3]) == "" {
		return "", "", false
	}
	return segments[2], segments[3], true
}

func isUnderRemoteRoot(remoteRoot, remotePath string) bool {
	remoteRoot = normalizeRemotePath(remoteRoot)
	remotePath = normalizeRemotePath(remotePath)
	if remoteRoot == "/" {
		return remotePath == "/" || strings.HasPrefix(remotePath, "/")
	}
	return remotePath == remoteRoot || strings.HasPrefix(remotePath, remoteRoot+"/")
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

// nameWithId rebuilds the canonical basename from an already-clean name/id pair; it does not slugify raw titles.
func nameWithId(name, id string) string {
	stem := strings.TrimSpace(id)
	if strings.TrimSpace(name) != "" {
		stem = strings.TrimSpace(name) + "__" + stem
	}
	if stem == "" {
		return ""
	}
	return stem + ".json"
}

func resolveNameByID(names []string, requestedName string) (string, bool) {
	requestedID := IDFromBasename(requestedName)
	if requestedID == "" {
		return "", false
	}
	bestName := ""
	bestScore := -1
	for _, candidate := range names {
		candidateName, candidateID := ParseNameID(candidate)
		if candidateID != requestedID {
			continue
		}
		score := 0
		// Canonical preference: candidate must round-trip to itself when
		// reconstructed from its parsed (name, id, extension). This works for
		// any extension (including none, e.g. directories like
		// "thread__01HXYZ" or files like "notes__abc.md").
		if candidateName != "" {
			ext := path.Ext(candidate)
			if candidateName+"__"+candidateID+ext == candidate {
				score = 1
			}
		}
		if score > bestScore || (score == bestScore && (bestName == "" || candidate < bestName)) {
			bestName = candidate
			bestScore = score
		}
	}
	if bestName == "" {
		return "", false
	}
	return bestName, true
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
